package agent

// Playkeeper Cloud's disk limits: what a group of servers, one customer's,
// may take on the machine between them, counted as the Disk space page
// counts it. The dashboard sets them. What would take a group past its
// limit is refused: a backup, whether made on request, on schedule or
// before a restore, an update or an import; a file uploaded into a server's
// folder; a data or resource pack; a world imported or restored; and
// pre-generation. Play itself isn't held
// back, so a group can pass its limit, and those then stay refused until
// it's under again.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup"
	"github.com/CIYAhq/playkeeper/internal/diskusage"
)

const kvDiskLimits = "disk_limits"

// Bounds of a request: the limits, each one's servers, and each one's size
// and processor share for each GB of memory, in thousandths of a core.
const (
	maxDiskLimits       = 1000
	maxDiskLimitServers = 100
	maxDiskLimitBytes   = 1 << 50
	minCPUMilliPerGB    = 100
	maxCPUMilliPerGB    = 16000
)

var reDiskLimitID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type diskLimitState struct {
	mu     sync.Mutex
	limits []api.DiskLimit
	// held is what operations under way hold of each limit, and wrote what
	// operations that ended wrote, each counted until a scan begun after it
	// finds it on the disk.
	held  map[string]int64
	wrote []diskWrite
	// pregen is what each server's pre-generation task was expected to
	// write when it was recorded. While it's unfinished, the part of its
	// area still to do counts; an agent restarted meanwhile counts what the
	// scans find.
	pregen map[string]int64
}

type diskWrite struct {
	limit string
	at    time.Time
	bytes int64
}

// loadDiskLimits reads the disk limits. An agent that can't read them
// doesn't start, rather than start without them.
func (a *Agent) loadDiskLimits() error {
	a.limits.held, a.limits.pregen = map[string]int64{}, map[string]int64{}
	v, ok, err := a.kvGet(kvDiskLimits)
	if err != nil || !ok {
		return err
	}
	return json.Unmarshal([]byte(v), &a.limits.limits)
}

func (a *Agent) diskLimits() []api.DiskLimit {
	a.limits.mu.Lock()
	defer a.limits.mu.Unlock()
	out := make([]api.DiskLimit, len(a.limits.limits))
	for i, l := range a.limits.limits {
		l.Servers = slices.Clone(l.Servers)
		out[i] = l
	}
	return out
}

// diskLimitOf is the limit server id counts against, or nil.
func (a *Agent) diskLimitOf(id string) *api.DiskLimit {
	for _, l := range a.diskLimits() {
		if slices.Contains(l.Servers, id) {
			return &l
		}
	}
	return nil
}

// hDiskLimits answers every disk limit with what its servers take at the
// last scan, or a new one with ?fresh=1.
func (a *Agent) hDiskLimits(w http.ResponseWriter, r *http.Request) {
	limits := a.diskLimits()
	if len(limits) > 0 {
		rep, err := a.scanDisk(r.Context(), nil, r.URL.Query().Get("fresh") == "1")
		if err != nil {
			writeError(w, err)
			return
		}
		for i := range limits {
			limits[i].UsedBytes = usedBy(rep, limits[i].Servers)
		}
	}
	writeJSON(w, http.StatusOK, limits)
}

// hDiskLimitsSet replaces every disk limit.
func (a *Agent) hDiskLimitsSet(w http.ResponseWriter, r *http.Request) {
	var req api.DiskLimitsRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	limits, err := checkDiskLimits(req.Limits)
	if err != nil {
		writeError(w, err)
		return
	}
	a.limits.mu.Lock()
	old := a.limits.limits
	changed := !slices.EqualFunc(old, limits, func(x, y api.DiskLimit) bool {
		return x.ID == y.ID && x.LimitBytes == y.LimitBytes && slices.Equal(x.Servers, y.Servers) && x.CPUMilliPerGB == y.CPUMilliPerGB
	})
	if changed {
		raw, _ := json.Marshal(limits)
		if err := a.kvSet(kvDiskLimits, string(raw)); err != nil {
			a.limits.mu.Unlock()
			writeError(w, err)
			return
		}
		a.limits.limits = limits
	}
	a.limits.mu.Unlock()
	if changed {
		a.audit(actor, "disk_limits.set", "", "succeeded", fmt.Sprintf("%d disk limits", len(limits)))
	}
	// Every set puts the running servers' caps right, changed or not, and a
	// dashboard that stops waiting doesn't cut it short: the dashboard sets
	// the limits every minute, so a cap one set missed is caught by the next.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Minute)
	a.recapCPUs(ctx, limits)
	cancel()
	writeJSON(w, http.StatusOK, limits)
}

// cpuCapIn is server id's processor cap under limits, in billionths of a
// core, or 0 for none: its limit's share for each GB of memoryMB, and at most
// every core the machine has, which Docker allows no more than.
func cpuCapIn(limits []api.DiskLimit, id string, memoryMB int) int64 {
	for _, l := range limits {
		if l.CPUMilliPerGB > 0 && slices.Contains(l.Servers, id) {
			return min(int64(memoryMB)*int64(l.CPUMilliPerGB)*1_000_000/1024, int64(numCPU())*1_000_000_000)
		}
	}
	return 0
}

// cpuCap is the server's processor cap for memoryMB under the limits now.
func (s *server) cpuCap(memoryMB int) int64 {
	return cpuCapIn(s.diskLimits(), s.id, memoryMB)
}

// recapCPUs gives each running server the processor cap limits give it, or
// every core where they give none, against what its container has now, so
// a cap an earlier change missed is put right too. A server that isn't
// running gets its cap when its container is next made.
func (a *Agent) recapCPUs(ctx context.Context, limits []api.DiskLimit) {
	all := int64(numCPU()) * 1_000_000_000
	for _, s := range a.serverList() {
		sc, err := s.serverConfig()
		if err != nil || sc == nil {
			continue
		}
		c, err := s.docker.ContainerInspect(ctx, s.containerName())
		if err != nil || !c.State.Running {
			continue
		}
		want, have := cpuCapIn(limits, s.id, sc.MemoryMB), c.HostConfig.NanoCPUs
		if want == have || want == 0 && have == all {
			continue
		}
		if want == 0 {
			want = all
		}
		if err := s.docker.ContainerCPUs(ctx, c.ID, want); err != nil {
			s.log.Warn("could not change a running server's processor cap", "server", s.id, "err", err)
		}
	}
}

// checkDiskLimits refuses limits a server would count against twice, or
// that aren't limits, and returns them in order.
func checkDiskLimits(in []api.DiskLimit) ([]api.DiskLimit, error) {
	if len(in) > maxDiskLimits {
		return nil, errInvalid("At most %d disk limits.", maxDiskLimits)
	}
	out := make([]api.DiskLimit, 0, len(in))
	ids, servers := map[string]bool{}, map[string]bool{}
	for _, l := range in {
		switch {
		case !reDiskLimitID.MatchString(l.ID) || ids[l.ID]:
			return nil, errInvalid("Each disk limit needs an id of its own, of lower-case letters, digits and dashes.")
		case l.LimitBytes <= 0 || l.LimitBytes > maxDiskLimitBytes:
			return nil, errInvalid("A disk limit is more than nothing and at most 1 PiB.")
		case len(l.Servers) > maxDiskLimitServers:
			return nil, errInvalid("A disk limit covers at most %d servers.", maxDiskLimitServers)
		case l.CPUMilliPerGB != 0 && (l.CPUMilliPerGB < minCPUMilliPerGB || l.CPUMilliPerGB > maxCPUMilliPerGB):
			return nil, errInvalid("A processor share is from %d to %d thousandths of a core for each GB of memory, or none.", minCPUMilliPerGB, maxCPUMilliPerGB)
		}
		ids[l.ID] = true
		list := slices.Clone(l.Servers)
		slices.Sort(list)
		for _, id := range list {
			if !reServerID.MatchString(id) || servers[id] {
				return nil, errInvalid("A server counts against one disk limit at most.")
			}
			servers[id] = true
		}
		out = append(out, api.DiskLimit{ID: l.ID, LimitBytes: l.LimitBytes, Servers: list, CPUMilliPerGB: l.CPUMilliPerGB})
	}
	slices.SortFunc(out, func(x, y api.DiskLimit) int { return strings.Compare(x.ID, y.ID) })
	return out, nil
}

// holdBackup holds what a backup of the server may take against its disk
// limit, until done.
func (s *server) holdBackup(ctx context.Context) (done func(wrote bool), err error) {
	if s.diskLimitOf(s.id) == nil {
		return func(bool) {}, nil
	}
	need, err := s.backupNeed(ctx)
	if err != nil {
		return nil, err
	}
	return s.holdDiskLimit(ctx, s.id, need)
}

// backupNeed is what a backup of the server may take: its measure, or, for a
// world that can't be measured, everything the server takes now, so a backup
// counts even when it can't be sized.
func (s *server) backupNeed(ctx context.Context) (int64, error) {
	if size, err := backup.Measure(s.dataDir(), archiveLimits()); err == nil {
		return size.ArchiveBytes(), nil
	}
	rep, err := s.scanDisk(ctx, nil, false)
	if err != nil {
		return 0, err
	}
	return usedBy(rep, []string{s.id}), nil
}

// unpackedBytes is what a staged world takes once unpacked: the sizes of its
// files, each checked against the archive when it was staged. The manifest's
// own total is the uploader's word, so it isn't used.
func unpackedBytes(m backup.Manifest) int64 {
	var n int64
	for _, f := range m.Files {
		n += f.Size
	}
	return n
}

// usedBy is what servers take in a scan.
func usedBy(rep *diskusage.Report, servers []string) int64 {
	var n int64
	for _, sv := range rep.Servers {
		if slices.Contains(servers, sv.ID) {
			n += sv.Total.Bytes
		}
	}
	return n
}

// holdDiskLimit checks that need more bytes fit server id's disk limit,
// beside what its servers take and what is on its way to them, and holds
// them until done: done(true) once they're written, which keeps them counted
// until a scan begun after finds them, and done(false) when nothing was. A
// server with no limit gets a done that does nothing.
func (a *Agent) holdDiskLimit(ctx context.Context, id string, need int64) (done func(wrote bool), err error) {
	l := a.diskLimitOf(id)
	if l == nil {
		return func(bool) {}, nil
	}
	rep, err := a.scanDisk(ctx, nil, false)
	if err != nil {
		return nil, err
	}
	a.limits.mu.Lock()
	defer a.limits.mu.Unlock()
	used := usedBy(rep, l.Servers) + a.onTheWay(l.Servers) + a.limits.held[l.ID] + a.pregenOnTheWay(l.Servers)
	for _, w := range a.limits.wrote {
		if w.limit == l.ID && !w.at.Before(rep.ScannedAt) {
			used += w.bytes
		}
	}
	if used+need > l.LimitBytes {
		return nil, errDiskLimit(used, l.LimitBytes, need)
	}
	a.limits.held[l.ID] += need
	var once sync.Once
	return func(wrote bool) {
		once.Do(func() {
			a.limits.mu.Lock()
			defer a.limits.mu.Unlock()
			a.limits.held[l.ID] -= need
			if wrote {
				a.limits.wrote = append(a.limits.wrote, diskWrite{limit: l.ID, at: a.now(), bytes: need})
			}
		})
	}, nil
}

// diskLimitRefusal refuses need more bytes that wouldn't fit server id's
// disk limit, for what counts as on its way until it's written, like an
// upload's announced file.
func (a *Agent) diskLimitRefusal(ctx context.Context, id string, need int64) error {
	done, err := a.holdDiskLimit(ctx, id, need)
	if err == nil {
		done(false)
	}
	return err
}

// noteDiskWrite counts bytes just written into server id's folder until a
// scan begun after finds them.
func (a *Agent) noteDiskWrite(id string, bytes int64) {
	l := a.diskLimitOf(id)
	if l == nil {
		return
	}
	a.limits.mu.Lock()
	a.limits.wrote = append(a.limits.wrote, diskWrite{limit: l.ID, at: a.now(), bytes: bytes})
	a.limits.mu.Unlock()
}

// notePregen counts what server id's pre-generation task, just recorded as
// started, may write, while it's unfinished. Only a start that recorded its
// task calls it, so a start refused meanwhile leaves the reservation alone.
func (a *Agent) notePregen(id string, bytes int64) {
	a.limits.mu.Lock()
	a.limits.pregen[id] = bytes
	a.limits.mu.Unlock()
}

// pregenOnTheWay is what unfinished pre-generation on servers may still
// write: each task's estimate, less the part of its area already done. The
// caller holds a.limits.mu.
func (a *Agent) pregenOnTheWay(servers []string) int64 {
	var n int64
	for _, id := range servers {
		bytes, ok := a.limits.pregen[id]
		if !ok {
			continue
		}
		s := a.serverByID(id)
		if s == nil {
			delete(a.limits.pregen, id)
			continue
		}
		t, err := s.lastPregen()
		switch {
		case err != nil:
		case !t.unfinished():
			delete(a.limits.pregen, id)
			continue
		case t.Total > 0:
			bytes = bytes * max(t.Total-t.Chunks, 0) / t.Total
		}
		n += bytes
	}
	return n
}

// forgetDiskWrites forgets the writes before a scan that began at t, which
// found them on the disk.
func (a *Agent) forgetDiskWrites(t time.Time) {
	a.limits.mu.Lock()
	a.limits.wrote = slices.DeleteFunc(a.limits.wrote, func(w diskWrite) bool { return w.at.Before(t) })
	a.limits.mu.Unlock()
}

// onTheWay is what uploads announced for servers and haven't put in place:
// files into their folders, and worlds to import into them, other than one
// being applied, whose operation holds what it writes.
func (a *Agent) onTheWay(servers []string) int64 {
	var n int64
	a.uploads.mu.Lock()
	ups := make([]*fileUpload, 0, len(a.uploads.byID))
	for _, up := range a.uploads.byID {
		if slices.Contains(servers, up.serverID) {
			ups = append(ups, up)
		}
	}
	a.uploads.mu.Unlock()
	for _, up := range ups {
		up.mu.Lock()
		for _, f := range up.files {
			if !up.gone && !f.placed {
				n += f.size
			}
		}
		up.mu.Unlock()
	}
	a.imports.mu.Lock()
	imps := make([]*worldImport, 0, len(a.imports.byID))
	for _, imp := range a.imports.byID {
		if slices.Contains(servers, imp.serverID) {
			imps = append(imps, imp)
		}
	}
	a.imports.mu.Unlock()
	for _, imp := range imps {
		imp.mu.Lock()
		if !imp.gone && imp.busy != "applying" {
			for _, f := range imp.files {
				n += f.size
			}
		}
		imp.mu.Unlock()
	}
	return n
}

func errDiskLimit(used, limit, need int64) *apiError {
	return &apiError{Status: http.StatusInsufficientStorage, Code: api.CodeDiskLimit,
		Msg:  fmt.Sprintf("That needs about %s, and these servers have %s of their %s disk limit left.", humanBytes(need), humanBytes(max(limit-used, 0)), humanBytes(limit)),
		Hint: "Delete backups or files you don't need to make room."}
}
