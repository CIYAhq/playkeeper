package agent

// Wave 7 (0.4.0): the Disk space page. A scan measures every server's
// folders and the shared ones and proposes what can go; the clean-up is a
// machine operation, so no server operation writes files meanwhile, and the
// diskusage package checks every path again right before deleting it.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/diskusage"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// diskCacheFor is how long a scan answers the page before it scans again.
const diskCacheFor = 15 * time.Second

type diskCache struct {
	scan sync.Mutex // one scan at a time
	mu   sync.Mutex
	at   time.Time
	tz   string
	rep  *diskusage.Report
}

func (a *Agent) forgetDiskScan() {
	a.disk.mu.Lock()
	a.disk.rep = nil
	a.disk.mu.Unlock()
}

// diskLayout is where the machine's servers and shared folders are, with the
// backups each server's rules would delete. A restore that isn't over keeps
// its stage, and its server counts as busy, so neither the stage nor the
// world copies it may put back are offered.
func (a *Agent) diskLayout(ctx context.Context) diskusage.Layout {
	l := diskusage.Layout{BackupsDir: a.cfg.BackupsDir(), StagingDir: a.cfg.StagingDir(), DiskDir: a.cfg.DataDir}
	restoring := map[string]bool{}
	for stage, j := range a.unsettledSwaps() {
		l.ActiveStages = append(l.ActiveStages, stage)
		restoring[j.ServerID] = true
	}
	for _, s := range a.serverList() {
		sv := diskusage.Server{ID: s.id, Name: s.name(), DataDir: s.dataDir(), Busy: s.currentOp() != nil || restoring[s.id]}
		if fi, err := os.Stat(s.spoolDir()); err == nil && fi.IsDir() {
			sv.SpoolDir = s.spoolDir()
		}
		if sc, _ := s.serverConfig(); sc != nil {
			sv.Jar, sv.MinecraftVersion = filepath.Base(s.jarPath(*sc)), sc.MinecraftVersion
		}
		l.Servers = append(l.Servers, sv)
		prune := map[string]bool{}
		if res, err := s.retentionPlan(); err == nil {
			for _, id := range res.OnHost.DeleteIDs() {
				prune[id] = true
			}
		}
		if list, err := s.listBackups(""); err == nil {
			for _, b := range list {
				l.Backups = append(l.Backups, diskusage.Backup{ID: b.ID, ServerID: s.id, FileName: b.FileName, CreatedAt: b.CreatedAt, Prune: prune[b.ID]})
			}
		}
	}
	if img, err := a.docker.ImageInspect(ctx, minecraft.Image); err == nil {
		l.DockerImageBytes = img.Size
	}
	return l
}

func (a *Agent) diskOptions(loc *time.Location) diskusage.Options {
	return diskusage.Options{Now: a.now, Location: loc, DiskSpace: a.opts.DiskUsage}
}

// diskZone reads the dashboard's time zone, for dates in the texts.
func diskZone(tz string) (*time.Location, error) {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil || len(tz) > 64 {
		return nil, &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Msg: "Unknown time zone.", Field: "timeZone", Reason: "time_zone_invalid"}
	}
	return loc, nil
}

// hDisk scans the disk, or answers from a scan a few seconds old unless
// ?fresh=1 asks for a new one.
func (a *Agent) hDisk(w http.ResponseWriter, r *http.Request) {
	tz := r.URL.Query().Get("tz")
	loc, err := diskZone(tz)
	if err != nil {
		writeError(w, err)
		return
	}
	fresh := r.URL.Query().Get("fresh") == "1"
	a.disk.scan.Lock()
	defer a.disk.scan.Unlock()
	a.disk.mu.Lock()
	rep := a.disk.rep
	if rep != nil && (fresh || a.disk.tz != tz || a.now().Sub(a.disk.at) >= diskCacheFor) {
		rep = nil
	}
	a.disk.mu.Unlock()
	if rep == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
		defer cancel()
		rep, err = diskusage.Scan(ctx, a.diskLayout(ctx), a.diskOptions(loc))
		if err != nil {
			writeError(w, automationError(err))
			return
		}
		a.disk.mu.Lock()
		a.disk.at, a.disk.tz, a.disk.rep = a.now(), tz, rep
		a.disk.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, rep)
}

type diskCleanRequest struct {
	Actor string   `json:"actor"`
	IDs   []string `json:"ids,omitempty"`
	// Ways deletes all that a way with one button offers ("Delete old
	// logs") as the scan finds it when the button is pressed, however many
	// files that is. Ways the user reviews item by item take IDs.
	Ways     []diskusage.WayID `json:"ways,omitempty"`
	TimeZone string            `json:"timeZone,omitempty"`
}

var (
	reDiskID = regexp.MustCompile(`^[0-9a-f]{32}$`)
	diskWays = []diskusage.WayID{diskusage.WayOldBackups, diskusage.WayOldLogs, diskusage.WayOldCrashReports, diskusage.WayUnusedSoftware,
		diskusage.WayDownloads, diskusage.WaySetAside, diskusage.WayUnfinished}
)

// maxDiskProblems is how many items that couldn't go the operation lists.
const maxDiskProblems = 50

// hDiskClean deletes what the user chose from the page. It scans again
// first, and deletes only what that scan still offers.
func (a *Agent) hDiskClean(w http.ResponseWriter, r *http.Request) {
	var req diskCleanRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	loc, err := diskZone(req.TimeZone)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(req.IDs) == 0 && len(req.Ways) == 0 {
		writeError(w, &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Field: "ids", Reason: "invalid_ids", Msg: "Choose what to delete."})
		return
	}
	for _, id := range req.IDs {
		if !reDiskID.MatchString(id) {
			writeError(w, &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Field: "ids", Reason: "invalid_ids",
				Msg: "One of the chosen items is not valid.", Hint: "Scan again and choose from the new list."})
			return
		}
	}
	for _, way := range req.Ways {
		if !slices.Contains(diskWays, way) {
			writeError(w, &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Field: "ways", Reason: "invalid_ways", Msg: "Unknown way to free space."})
			return
		}
	}
	op, err := a.beginMachineOp("disk-cleanup", actor, func(ctx context.Context, h *opHandle) error {
		defer a.forgetDiskScan()
		layout, opts := a.diskLayout(ctx), a.diskOptions(loc)
		ids := req.IDs
		if len(req.Ways) > 0 {
			h.phase("scanning")
			rep, err := diskusage.Scan(ctx, layout, opts)
			if err != nil {
				return automationError(err)
			}
			for _, way := range rep.Ways {
				if way.Action != diskusage.ActionReview && slices.Contains(req.Ways, way.ID) {
					ids = append(ids, way.CandidateIDs...)
				}
			}
		}
		h.phase("deleting")
		res, err := diskusage.Clean(ctx, layout, opts, ids, diskBackups{a, actor})
		if err != nil {
			return automationError(err)
		}
		deleted := 0
		var problems []diskusage.Outcome
		for _, o := range res.Outcomes {
			switch o.Status {
			case diskusage.StatusDeleted:
				deleted++
			case diskusage.StatusGone:
			default:
				problems = append(problems, o)
			}
		}
		h.set("freed", res.Freed)
		h.set("deleted", deleted)
		if len(problems) > maxDiskProblems {
			h.set("moreProblems", len(problems)-maxDiskProblems)
			problems = problems[:maxDiskProblems]
		}
		if len(problems) > 0 {
			h.set("problems", problems)
		}
		a.audit(actor, "disk.cleaned", "machine", "succeeded", fmt.Sprintf("%d of %d deleted, %s freed", deleted, len(res.Outcomes), humanBytes(res.Freed)))
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// diskBackups deletes a backup the rules no longer keep the way the World
// page does, record and all.
type diskBackups struct {
	a     *Agent
	actor string
}

func (d diskBackups) DeleteBackup(_ context.Context, id string) error {
	for _, s := range d.a.serverList() {
		if b, err := s.getBackup(id); err == nil {
			return s.removeBackup(b, d.actor)
		}
	}
	return errNotFound("Backup")
}
