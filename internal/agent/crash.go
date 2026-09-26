package agent

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/diagnose"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

const (
	// crashLogLines is how many of a run's last container log lines are
	// read, as many as diagnose.ExplainCrash looks at.
	crashLogLines = 2000
	crashReadTime = 10 * time.Second
	// A crash report is a few kilobytes and Java's error report a few
	// hundred; one that ran on past this is cut.
	crashReportLimit = 1 << 20
	maxDirEntries    = 1000
	// keepNewestBackups stay when the crash helper offers to free disk
	// space by deleting backups; it aims for freeDiskTarget free.
	keepNewestBackups = 3
	freeDiskTarget    = 2 << 30
	removedAddonsDir  = "removed-addons"
)

var (
	reCrashReport = regexp.MustCompile(`^crash-[0-9A-Za-z._-]{1,80}\.txt$`)
	// Java writes hs_err_pid<pid>.log to its working folder, the data
	// directory, when the JVM itself fails.
	reJVMReport = regexp.MustCompile(`^hs_err_pid[0-9]{1,10}\.log$`)
)

// explainCrash works out why the server's run ended unexpectedly, or why it
// did not start, and keeps the answer until the server is online again or
// someone stops or starts it. id is the container that ran, when it still
// exists; startErr is Docker's error starting it. A start refused over a
// file in the server's folder is not a crash: noteRefusal keeps that. The
// log is read from Docker, not the console buffer: diagnose needs the lines
// as the server printed them, and redacts what it shows.
func (s *server) explainCrash(id string, st docker.ContainerState, start bool, startErr error) {
	sc, err := s.serverConfig()
	if err != nil || sc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, crashReadTime)
	defer cancel()
	runStart, _ := st.Started()
	_, _, maxMB := s.memoryFor(s.id)
	in := diagnose.CrashInput{
		ServerType: serverTypeOf(*sc), MCVersion: sc.MinecraftVersion, JavaVersion: minecraft.ImageJava,
		ExitCode: st.ExitCode, OOMKilled: st.OOMKilled, DockerError: st.Error,
		BudgetMB: sc.MemoryMB, HeapMB: minecraft.HeapMB(sc.MemoryMB), HostMB: s.opts.HostMemoryMB(),
		RoomMB: max(maxMB-sc.MemoryMB, 0), Port: s.gamePort,
	}
	var dockerErr *docker.APIError
	switch {
	case errors.As(startErr, &dockerErr):
		in.DockerError = dockerErr.Message
	case startErr != nil:
		in.DockerError = startErr.Error()
	}
	in.ViewDistance, _ = s.distances()
	if id != "" {
		in.Console = s.runLog(ctx, id, runStart)
	}
	in.CrashReport, in.CrashReportName = s.newestCrashReport(runStart)
	in.Addons = s.addons(*sc)
	var free int64 = -1
	if f, _, err := s.opts.DiskUsage(s.dataDir()); err == nil {
		free = f
		mb := f >> 20
		in.FreeDiskMB = &mb
	}
	backups, _ := s.listBackups("")
	var latest *api.Backup
	for i := range backups {
		if backups[i].Verified != nil && *backups[i].Verified {
			latest = &backups[i]
			break
		}
	}
	in.HasBackup = latest != nil
	d := diagnose.ExplainCrash(in)
	c := &api.Crash{
		At: s.now().UTC(), Start: start, Kind: string(d.Kind), Params: d.Params, Certain: d.Certain,
		Title: d.Title, Explanation: d.Explanation, Evidence: apiEvidence(d.Evidence), Fixes: apiActions(d.Fixes),
		Lines: []api.CrashLine{}, RoomMB: in.RoomMB,
	}
	for _, l := range d.Lines {
		c.Lines = append(c.Lines, api.CrashLine{Time: l.Time, Level: l.Level, Text: l.Text})
	}
	for i := range c.Fixes {
		f := &c.Fixes[i]
		switch diagnose.ActionKind(f.Kind) {
		case diagnose.ActionRestoreBackup:
			if latest != nil {
				f.Params = map[string]any{"backup_id": latest.ID, "made_at": latest.CreatedAt}
			}
		case diagnose.ActionFreeDisk:
			if free >= 0 {
				planFreeDisk(f, backups, free)
			}
		}
	}
	if d.Kind == diagnose.CrashPortInUse && d.Params["reason"] == nil && in.DockerError != "" {
		if port, ok := d.Params["port"].(int); ok {
			if name, found := s.portContainer(ctx, port); found {
				if name != "" {
					c.Params["holder_container"] = name
				}
			} else if name, pid, ok := s.opts.PortHolder(port); ok {
				c.Params["holder"], c.Params["holder_pid"] = name, pid
			}
		}
	}
	if d.Kind == diagnose.CrashDiskFull {
		var total int64
		for _, b := range backups {
			total += b.SizeBytes
		}
		c.Params["backups_mb"] = total >> 20
		if _, disk, err := s.opts.DiskUsage(s.dataDir()); err == nil {
			c.Params["disk_mb"] = disk >> 20
		}
	}
	s.mu.Lock()
	s.crash = c
	s.mu.Unlock()
	s.log.Info("crash explained", "server", s.id, "kind", d.Kind, "certain", d.Certain, "start", start)
}

// planFreeDisk names the oldest backups whose deletion frees enough space,
// keeping the newest keepNewestBackups, so the fix can delete them.
func planFreeDisk(f *api.DiagnosisAction, backups []api.Backup, free int64) {
	if len(backups) <= keepNewestBackups || free >= freeDiskTarget {
		return
	}
	old := slices.Clone(backups[keepNewestBackups:])
	slices.Reverse(old)
	var ids []string
	var frees int64
	for _, b := range old {
		if free+frees >= freeDiskTarget {
			break
		}
		ids = append(ids, b.ID)
		frees += b.SizeBytes
	}
	if f.Params == nil {
		f.Params = map[string]any{}
	}
	f.Params["backup_ids"], f.Params["backups"], f.Params["frees_mb"], f.Params["keep"] = ids, len(ids), frees>>20, keepNewestBackups
}

// runLog reads the last lines a container printed since its run began.
func (s *server) runLog(ctx context.Context, id string, since time.Time) []string {
	sc, err := s.docker.ContainerLogs(ctx, id, docker.LogsOptions{Since: since, Tail: strconv.Itoa(crashLogLines)})
	if err != nil {
		s.log.Warn("crash helper could not read the log", "server", s.id, "err", err)
		return nil
	}
	defer sc.Close()
	var out []string
	for {
		l, err := sc.Next()
		if err != nil {
			return out
		}
		out = append(out, l.Text)
	}
}

// newestCrashReport reads the newest report written since the run began:
// Minecraft's crash report, or else the report Java writes when the JVM
// itself fails. The game can put a link or a named pipe in their place, so
// they are read through internal/gamefiles, and only their start.
func (s *server) newestCrashReport(since time.Time) (text, name string) {
	if since.IsZero() {
		return "", ""
	}
	d, err := s.gameFiles()
	if err != nil {
		return "", ""
	}
	defer d.Close()
	for _, r := range []struct {
		dir string
		re  *regexp.Regexp
	}{{"crash-reports", reCrashReport}, {".", reJVMReport}} {
		name := newestFile(d, r.dir, r.re, since.Add(-time.Second))
		if name == "" {
			continue
		}
		b, _, err := d.ReadRange(path.Join(r.dir, name), 0, crashReportLimit)
		if err != nil {
			s.log.Warn("crash helper could not read a report", "server", s.id, "file", name, "err", err)
			continue
		}
		return string(b), name
	}
	return "", ""
}

// newestFile names the newest file in dir whose name matches re, changed at
// or after from.
func newestFile(d *gamefiles.Dir, dir string, re *regexp.Regexp, from time.Time) string {
	entries, err := d.ReadDir(dir, maxDirEntries)
	if err != nil {
		return ""
	}
	var newest time.Time
	name := ""
	for _, e := range entries {
		if !e.Type().IsRegular() || !re.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().Before(from) || !info.ModTime().After(newest) {
			continue
		}
		newest, name = info.ModTime(), e.Name()
	}
	return name
}

// addonDir is where the server's type loads plugins or mods from.
func addonDir(sc api.ServerConfig) string {
	switch serverTypeOf(sc) {
	case api.TypePaper, "purpur", "spigot", "folia":
		return "plugins"
	}
	return "mods"
}

// addons lists the plugin or mod jars the server loads.
func (s *server) addons(sc api.ServerConfig) []diagnose.Addon {
	d, err := s.gameFiles()
	if err != nil {
		return nil
	}
	defer d.Close()
	entries, err := d.ReadDir(addonDir(sc), maxDirEntries)
	if err != nil {
		return nil
	}
	var out []diagnose.Addon
	for _, e := range entries {
		if e.Type().IsRegular() && validAddonJar(e.Name()) == nil {
			out = append(out, diagnose.Addon{File: e.Name()})
		}
	}
	return out
}

// validAddonJar accepts the file name of a jar directly in the add-on folder,
// short enough to keep its name when it is moved aside with a time stamp.
func validAddonJar(name string) error {
	if len(name) > 200 || !strings.HasSuffix(name, ".jar") || len(name) == len(".jar") || strings.HasPrefix(name, ".") ||
		strings.ContainsAny(name, `/\`) || path.Base(name) != name || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return errInvalid("That is not the name of a plugin or mod file.")
	}
	return nil
}

func (s *server) hRemoveAddon(w http.ResponseWriter, r *http.Request) {
	var req api.RemoveAddonRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validAddonJar(req.Jar); err != nil {
		writeError(w, err)
		return
	}
	sc, err := s.serverConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	if sc == nil {
		writeError(w, errNotCreated())
		return
	}
	rel := addonDir(*sc) + "/" + req.Jar
	if err := s.checkAddon(rel); err != nil {
		writeError(w, err)
		return
	}
	if _, running, err := s.containerRunning(r.Context()); err != nil {
		writeError(w, err)
		return
	} else if running {
		writeError(w, errConflict(s.name()+" is running.", "Stop it before removing a plugin or mod."))
		return
	}
	op, err := s.beginOp("remove-addon", actor, func(ctx context.Context, h *opHandle) error {
		h.set("jar", req.Jar)
		if _, running, err := s.containerRunning(ctx); err != nil {
			return err
		} else if running {
			return errConflict(s.name()+" is running.", "Stop it before removing a plugin or mod.")
		}
		moved, err := s.moveAddon(rel)
		if err != nil {
			s.audit(actor, "addon.remove", req.Jar, "failed", err.Error())
			return err
		}
		s.audit(actor, "addon.remove", req.Jar, "succeeded", "moved to "+moved)
		s.log.Info("add-on removed", "server", s.id, "jar", req.Jar, "to", moved)
		if req.Start {
			return s.startNow(ctx, h)
		}
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// checkAddon makes sure rel is a jar file in the server's add-on folder.
func (s *server) checkAddon(rel string) error {
	root, err := os.OpenRoot(s.dataDir())
	if err != nil {
		return errNotFound("That plugin or mod")
	}
	defer root.Close()
	if st, err := root.Lstat(rel); err != nil || !st.Mode().IsRegular() {
		return errNotFound("That plugin or mod")
	}
	return nil
}

// moveAddon moves an add-on jar out of the data directory, where the game no
// longer loads it but the owner can put it back. The move stays inside the
// server's directory, so a symlink the game planted cannot redirect it.
func (s *server) moveAddon(rel string) (string, error) {
	root, err := os.OpenRoot(s.dir())
	if err != nil {
		return "", err
	}
	defer root.Close()
	src := "data/" + rel
	if st, err := root.Lstat(src); err != nil || !st.Mode().IsRegular() {
		return "", errNotFound("That plugin or mod")
	}
	if err := root.MkdirAll(removedAddonsDir, 0o750); err != nil {
		return "", err
	}
	dst := removedAddonsDir + "/" + s.now().UTC().Format("20060102-150405") + "-" + path.Base(rel)
	if err := root.Rename(src, dst); err != nil {
		return "", err
	}
	return dst, nil
}
