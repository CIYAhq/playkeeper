package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/version"
)

const defaultMOTD = "A Playkeeper server"

func (a *Agent) hHealth(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	ok := a.dockerOK
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, api.Health{OK: true, Version: version.Version, Docker: ok})
}

func (a *Agent) hPreflight(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.Preflight(r.Context()))
}

func (a *Agent) catalog() api.Catalog {
	host := a.opts.HostMemoryMB()
	opts, rec, max := minecraft.MemoryOptions(host)
	if opts == nil {
		opts = []int{}
	}
	return api.Catalog{Versions: minecraft.Versions(), MemoryOptionsMB: opts, RecommendedMemoryMB: rec, HostMemoryMB: host, MaxMemoryMB: max, Image: minecraft.ImageTag}
}

func (a *Agent) hCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.catalog())
}

// Status assembles desired and observed state. Nothing here is cached from
// earlier runs: player and resource snapshots are dropped once stale.
func (a *Agent) Status(ctx context.Context) api.ServerStatus {
	st := api.ServerStatus{GamePort: a.cfg.GamePort, OfflineModeTest: a.offline(), AgentVersion: version.Version, CollectingSince: a.collectingSince()}
	sc, _ := a.serverConfig()
	st.Config = sc
	st.Exists = sc != nil
	st.Desired = a.desired()
	st.Operation = a.currentOp()
	st.LastOperation = a.lastFinishedOperation()
	if free, _, err := a.opts.DiskUsage(a.cfg.DataDir); err == nil {
		if dc := diskCheck(free); dc.Status != "pass" {
			st.DiskWarning = &dc
		}
	}
	c, err := a.docker.ContainerInspect(ctx, containerName)
	a.mu.Lock()
	runPhase, detail := a.runPhase, a.runPhaseDetail
	st.LastError, st.LastErrorHint = a.lastError, a.lastErrorHint
	crashed := a.crashed
	st.CrashCount = len(a.crashes)
	players, res := a.players, a.resources
	reachable, reachableAt := a.reachable, a.reachableAt
	a.mu.Unlock()
	fresh := func(t time.Time) bool { return a.now().Sub(t) < 3*a.opts.SampleInterval+5*time.Second }
	running := false
	switch {
	case err != nil && !docker.IsNotFound(err):
		st.Phase = api.PhaseDockerUnavailable
		st.LastError = "Docker is not responding, so Playkeeper cannot see or control the server."
		st.LastErrorHint = "Check the Docker service: sudo systemctl status docker"
	case sc == nil:
		st.Phase = api.PhaseNotCreated
	case docker.IsNotFound(err):
		st.Phase = api.PhaseStopped
	case c.State.Running:
		running = true
		st.Phase = runPhase
		if st.Phase == "" || st.Phase == api.PhaseCrashed {
			st.Phase = api.PhaseStartingContainer
		}
		st.PhaseDetail = detail
		if t, ok := c.State.Started(); ok {
			st.StartedAt = &t
		}
		_, hash := a.containerSpec(*sc, false)
		st.PendingRestart = c.Config.Labels[labelSpec] != hash
	default:
		st.Phase = api.PhaseStopped
		if crashed {
			st.Phase = api.PhaseCrashed
		}
		code := c.State.ExitCode
		st.ExitCode = &code
	}
	if st.Operation != nil {
		switch api.Phase(st.Operation.Phase) {
		case api.PhasePulling, api.PhaseDownloading, api.PhaseStartingContainer, api.PhaseStopping:
			st.Phase = api.Phase(st.Operation.Phase)
		}
		if st.Operation.Phase == "verifying_download" {
			st.Phase = api.PhaseDownloading
			st.PhaseDetail = "Verifying checksum"
		}
	}
	if running && res != nil && fresh(res.At) {
		st.Resources = res
	} else if res != nil && fresh(res.At) {
		st.Resources = &api.Resources{At: res.At, DiskFreeBytes: res.DiskFreeBytes, DiskTotalBytes: res.DiskTotalBytes}
	}
	if running && players != nil && fresh(players.At) {
		st.Players = players
	}
	if running && reachable && fresh(reachableAt) {
		st.Reachable = true
		t := reachableAt
		st.ReachableAt = &t
	}
	if list, err := a.listBackups(`WHERE verified = 1 AND kind = 'manual'`); err == nil && len(list) > 0 {
		st.LastBackup = &list[0]
	} else if list, err := a.listBackups(`WHERE verified = 1`); err == nil && len(list) > 0 {
		st.LastBackup = &list[0]
	}
	return st
}

func (a *Agent) hStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.Status(r.Context()))
}

func validMOTD(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultMOTD, nil
	}
	if utf8.RuneCountInString(s) > 59 {
		return "", errInvalid("The server description can be at most 59 characters.")
	}
	for _, r := range s {
		if !unicode.IsPrint(r) || r == '§' {
			return "", errInvalid("The server description may not contain control or formatting characters.")
		}
	}
	return s, nil
}

func validMaxPlayers(n int) (int, error) {
	if n == 0 {
		return 10, nil
	}
	if n < 1 || n > 100 {
		return 0, errInvalid("Max players must be between 1 and 100.")
	}
	return n, nil
}

func (a *Agent) validMemory(mb int) error {
	host := a.opts.HostMemoryMB()
	if !minecraft.ValidBudget(mb, host) {
		opts, _, _ := minecraft.MemoryOptions(host)
		return errInvalid("Memory budget must be one of %v MB on this host (it has %d MB).", opts, host)
	}
	return nil
}

func (a *Agent) hCreate(w http.ResponseWriter, r *http.Request) {
	var req api.CreateServerRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if !req.AcceptEULA {
		a.audit(actor, "server.create", "server", "refused", "EULA not accepted")
		writeErr(w, http.StatusBadRequest, api.CodeEULARequired, "You must accept the Minecraft EULA before Playkeeper downloads or starts a server.", "Read https://www.minecraft.net/en-us/eula and tick the box to accept it.")
		return
	}
	if sc, _ := a.serverConfig(); sc != nil {
		writeError(w, errConflict("A server already exists. Playkeeper runs one Minecraft server per host.", "Use Start on the Overview, or restore a backup from the World page."))
		return
	}
	entry, err := minecraft.LookupVersion(req.VersionID)
	if err != nil {
		writeError(w, errInvalid("Unknown server version. Choose one of the listed versions."))
		return
	}
	if err := a.validMemory(req.MemoryMB); err != nil {
		writeError(w, err)
		return
	}
	motd, err := validMOTD(req.MOTD)
	if err != nil {
		writeError(w, err)
		return
	}
	maxPlayers, err := validMaxPlayers(req.MaxPlayers)
	if err != nil {
		writeError(w, err)
		return
	}
	now := a.now().UTC()
	sc := api.ServerConfig{
		VersionID: entry.ID, MinecraftVersion: entry.MinecraftVersion, PaperBuild: entry.PaperBuild, MemoryMB: req.MemoryMB, HeapMB: minecraft.HeapMB(req.MemoryMB),
		LevelName: "world", MOTD: motd, MaxPlayers: maxPlayers, Whitelist: true, EULAAcceptedAt: now, EULAAcceptedBy: actor, CreatedAt: now, Image: minecraft.Image,
	}
	op, err := a.beginOp("create", actor, func(ctx context.Context, h *opHandle) error {
		if cur, _ := a.serverConfig(); cur != nil {
			return errConflict("A server already exists.", "")
		}
		if err := a.saveServerConfig(sc); err != nil {
			return err
		}
		a.audit(actor, "eula.accepted", "minecraft-eula", "recorded", "https://www.minecraft.net/en-us/eula")
		if err := a.setDesired(api.DesiredRunning); err != nil {
			return err
		}
		if err := a.startServer(ctx, h, sc); err != nil {
			a.startFailed(ctx)
			return err
		}
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (a *Agent) actionActor(r *http.Request) (string, error) {
	var req api.ActionRequest
	if err := decode(r, &req); err != nil {
		return "", err
	}
	return validActor(req.Actor)
}

func (a *Agent) hStart(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actionActor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	sc, _ := a.serverConfig()
	if sc == nil {
		writeError(w, errNotCreated())
		return
	}
	release, ok := a.holdOpLock()
	if !ok {
		writeError(w, a.busyError())
		return
	}
	_, running, err := a.containerRunning(r.Context())
	if err == nil && running {
		_ = a.setDesired(api.DesiredRunning)
	}
	release()
	if err != nil {
		writeError(w, err)
		return
	}
	if running {
		a.audit(actor, "start", "server", "no-op", "already running")
		writeJSON(w, http.StatusOK, map[string]any{"noop": true, "message": "The server is already running."})
		return
	}
	a.mu.Lock()
	a.crashes, a.crashed, a.nextAutoRestart = nil, false, time.Time{}
	a.mu.Unlock()
	op, err := a.beginOp("start", actor, func(ctx context.Context, h *opHandle) error {
		if err := a.setDesired(api.DesiredRunning); err != nil {
			return err
		}
		cur, _ := a.serverConfig()
		if err := a.startServer(ctx, h, *cur); err != nil {
			a.startFailed(ctx)
			return err
		}
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (a *Agent) hStop(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actionActor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if sc, _ := a.serverConfig(); sc == nil {
		writeError(w, errNotCreated())
		return
	}
	release, ok := a.holdOpLock()
	if !ok {
		writeError(w, a.busyError())
		return
	}
	_, running, err := a.containerRunning(r.Context())
	if err == nil && !running {
		_ = a.setDesired(api.DesiredStopped)
		a.mu.Lock()
		a.crashed = false
		a.mu.Unlock()
	}
	release()
	if err != nil {
		writeError(w, err)
		return
	}
	if !running {
		a.audit(actor, "stop", "server", "no-op", "already stopped")
		writeJSON(w, http.StatusOK, map[string]any{"noop": true, "message": "The server is already stopped."})
		return
	}
	op, err := a.beginOp("stop", actor, func(ctx context.Context, h *opHandle) error {
		if err := a.setDesired(api.DesiredStopped); err != nil {
			return err
		}
		return a.stopServer(ctx, h)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (a *Agent) hRestart(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actionActor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if sc, _ := a.serverConfig(); sc == nil {
		writeError(w, errNotCreated())
		return
	}
	if a.busy() {
		writeError(w, a.busyError())
		return
	}
	if _, running, err := a.containerRunning(r.Context()); err != nil {
		writeError(w, err)
		return
	} else if !running {
		writeError(w, errConflict("The server is not running, so it cannot be restarted.", "Use Start instead."))
		return
	}
	op, err := a.beginOp("restart", actor, func(ctx context.Context, h *opHandle) error {
		if err := a.stopServer(ctx, h); err != nil {
			return err
		}
		cur, _ := a.serverConfig()
		if err := a.startServer(ctx, h, *cur); err != nil {
			a.startFailed(ctx)
			return err
		}
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (a *Agent) hSettings(w http.ResponseWriter, r *http.Request) {
	var req api.SettingsRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	sc, _ := a.serverConfig()
	if sc == nil {
		writeError(w, errNotCreated())
		return
	}
	if a.busy() {
		writeError(w, &apiError{Status: http.StatusConflict, Code: api.CodeBusy, Msg: "Playkeeper is busy; try again when the current task finishes.", Op: a.currentOp()})
		return
	}
	var changed []string
	if req.MemoryMB != nil {
		if err := a.validMemory(*req.MemoryMB); err != nil {
			writeError(w, err)
			return
		}
		if sc.MemoryMB != *req.MemoryMB {
			changed = append(changed, fmt.Sprintf("memoryMB %d→%d", sc.MemoryMB, *req.MemoryMB))
		}
		sc.MemoryMB, sc.HeapMB = *req.MemoryMB, minecraft.HeapMB(*req.MemoryMB)
	}
	if req.MOTD != nil {
		m, err := validMOTD(*req.MOTD)
		if err != nil {
			writeError(w, err)
			return
		}
		if m != sc.MOTD {
			changed = append(changed, "motd")
		}
		sc.MOTD = m
	}
	if req.MaxPlayers != nil {
		n, err := validMaxPlayers(*req.MaxPlayers)
		if err != nil || *req.MaxPlayers == 0 {
			writeError(w, errInvalid("Max players must be between 1 and 100."))
			return
		}
		if n != sc.MaxPlayers {
			changed = append(changed, fmt.Sprintf("maxPlayers %d→%d", sc.MaxPlayers, n))
		}
		sc.MaxPlayers = n
	}
	if err := a.saveServerConfig(*sc); err != nil {
		writeError(w, err)
		return
	}
	a.audit(actor, "settings.changed", "server", "succeeded", strings.Join(changed, ", "))
	writeJSON(w, http.StatusOK, a.Status(r.Context()))
}

func (a *Agent) hLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > consoleCapacity {
		limit = 500
	}
	writeJSON(w, http.StatusOK, a.console.since(q.Get("epoch"), after, limit))
}

// validateCommand accepts one Minecraft console command. Commands go to the
// game over RCON as literal text; they are never interpreted by a host shell.
func validateCommand(c string) (string, error) {
	c = strings.TrimSpace(c)
	c = strings.TrimPrefix(c, "/")
	if c == "" {
		return "", errInvalid("Type a Minecraft command, for example: list")
	}
	if len(c) > 256 {
		return "", errInvalid("Commands can be at most 256 characters.")
	}
	for _, r := range c {
		if r < 0x20 || r == 0x7f {
			return "", errInvalid("Commands cannot contain line breaks or control characters.")
		}
	}
	switch strings.ToLower(strings.Fields(c)[0]) {
	case "stop", "restart":
		return "", errInvalid("Use the Stop or Restart buttons so Playkeeper knows the server was stopped on purpose.")
	}
	return c, nil
}

func (a *Agent) hCommand(w http.ResponseWriter, r *http.Request) {
	var req api.CommandRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	cmd, err := validateCommand(req.Command)
	if err != nil {
		writeError(w, err)
		return
	}
	a.mu.Lock()
	online := a.runPhase == api.PhaseOnline
	a.mu.Unlock()
	if _, running, _ := a.containerRunning(r.Context()); !running || !online {
		a.audit(actor, "console.command", "server", "refused", minecraft.RedactIPs(cmd))
		writeError(w, errConflict("The server is not online, so it cannot run commands.", "Start the server first."))
		return
	}
	out, err := a.rconCommand(cmd)
	if err != nil {
		a.audit(actor, "console.command", "server", "failed", minecraft.RedactIPs(cmd))
		writeError(w, &apiError{Status: http.StatusBadGateway, Code: api.CodeInternal, Msg: "The server did not accept the command: " + err.Error(), Hint: "Wait until the server is online and try again."})
		return
	}
	a.audit(actor, "console.command", "server", "succeeded", minecraft.RedactIPs(cmd))
	writeJSON(w, http.StatusOK, api.CommandResponse{Output: minecraft.CleanLine(out)})
}

func (a *Agent) whitelist() ([]api.WhitelistEntry, error) {
	b, err := os.ReadFile(filepath.Join(a.cfg.ServerDataDir(), "whitelist.json"))
	if os.IsNotExist(err) {
		return []api.WhitelistEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []api.WhitelistEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []api.WhitelistEntry{}
	}
	return entries, nil
}

func (a *Agent) hWhitelist(w http.ResponseWriter, r *http.Request) {
	list, err := a.whitelist()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *Agent) whitelistChange(w http.ResponseWriter, r *http.Request, name, actor, verb string) {
	if !minecraft.ValidPlayerName(name) {
		writeError(w, errInvalid("Minecraft usernames are 3–16 letters, numbers or underscores."))
		return
	}
	a.mu.Lock()
	online := a.runPhase == api.PhaseOnline
	a.mu.Unlock()
	if _, running, _ := a.containerRunning(r.Context()); !running || !online {
		writeError(w, errConflict("Start the server to change who can join.", ""))
		return
	}
	out, err := a.rconCommand("whitelist " + verb + " " + name)
	if err != nil {
		writeError(w, &apiError{Status: http.StatusBadGateway, Code: api.CodeInternal, Msg: "The server did not respond: " + err.Error()})
		return
	}
	out = minecraft.StripANSI(out)
	result := "succeeded"
	status := http.StatusOK
	if strings.Contains(out, "does not exist") || strings.Contains(out, "Unknown") {
		result, status = "failed", http.StatusUnprocessableEntity
	}
	a.audit(actor, "whitelist."+verb, name, result, out)
	if status != http.StatusOK {
		writeErr(w, status, api.CodeInvalid, out, "Check the spelling of the Java Edition username.")
		return
	}
	list, _ := a.whitelist()
	writeJSON(w, http.StatusOK, map[string]any{"message": out, "whitelist": list})
}

func (a *Agent) hWhitelistAdd(w http.ResponseWriter, r *http.Request) {
	var req api.WhitelistRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	a.whitelistChange(w, r, req.Name, actor, "add")
}

func (a *Agent) hWhitelistRemove(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	a.whitelistChange(w, r, r.PathValue("name"), actor, "remove")
}

func (a *Agent) hOperation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !reStageID.MatchString(id) {
		writeError(w, errInvalid("invalid operation id"))
		return
	}
	if cur := a.currentOp(); cur != nil && cur.ID == id {
		writeJSON(w, http.StatusOK, cur)
		return
	}
	op, err := a.loadOperation(id)
	if err != nil {
		writeError(w, errNotFound("Operation"))
		return
	}
	writeJSON(w, http.StatusOK, op)
}

func (a *Agent) hMetrics(w http.ResponseWriter, r *http.Request) {
	rg := r.URL.Query().Get("range")
	if rg == "" {
		rg = "24h"
	}
	m, err := a.Metrics(rg, a.now())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (a *Agent) hSessions(w http.ResponseWriter, r *http.Request) {
	rg := r.URL.Query().Get("range")
	if rg == "" {
		rg = "7d"
	}
	spec, ok := metricRanges[rg]
	if !ok {
		writeError(w, errInvalid("range must be one of 1h, 24h, 7d, 30d"))
		return
	}
	now := a.now()
	list, err := a.Sessions(now.Add(-spec.span), now, now, 500)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.SessionsResponse{From: now.Add(-spec.span).UTC(), To: now.UTC(), Sessions: list})
}

func (a *Agent) hSummary(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days == 0 {
		days = 14
	}
	s, err := a.Summary(days, r.URL.Query().Get("tz"), a.now())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (a *Agent) hEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	ev, err := a.Events(limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (a *Agent) hBackups(w http.ResponseWriter, r *http.Request) {
	list, err := a.listBackups("")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *Agent) hBackupCreate(w http.ResponseWriter, r *http.Request) {
	var req api.BackupRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(req.Note) > 200 {
		writeError(w, errInvalid("Notes can be at most 200 characters."))
		return
	}
	if sc, _ := a.serverConfig(); sc == nil {
		writeError(w, errNotCreated())
		return
	}
	op, err := a.beginOp("backup", actor, func(ctx context.Context, h *opHandle) error {
		return a.backupOp(ctx, h, actor, strings.TrimSpace(req.Note))
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (a *Agent) hBackupVerify(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actionActor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	b, err := a.verifyBackup(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	result := "failed"
	if b.Verified != nil && *b.Verified {
		result = "succeeded"
	}
	a.audit(actor, "backup.verified", b.ID, result, b.VerifyError)
	writeJSON(w, http.StatusOK, b)
}

func (a *Agent) hBackupDownload(w http.ResponseWriter, r *http.Request) {
	b, err := a.getBackup(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	f, err := os.Open(a.backupPath(b.FileName))
	if err != nil {
		writeError(w, errNotFound("Backup file"))
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+b.FileName+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(b.SizeBytes, 10))
	w.Header().Set("X-Playkeeper-SHA256", b.SHA256)
	n, err := io.Copy(w, f)
	if err == nil && n == b.SizeBytes {
		_, _ = a.db.Exec(`UPDATE backups SET downloaded_at = ? WHERE id = ?`, a.now().UnixMilli(), b.ID)
		a.audit(actorFromHeader(r), "backup.downloaded", b.ID, "succeeded", b.FileName)
	}
}

func actorFromHeader(r *http.Request) string {
	a, err := validActor(r.Header.Get("X-Playkeeper-Actor"))
	if err != nil {
		return "unknown"
	}
	return a
}

func (a *Agent) hBackupDelete(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	b, err := a.getBackup(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if a.busy() {
		writeError(w, &apiError{Status: http.StatusConflict, Code: api.CodeBusy, Msg: "Playkeeper is busy; try again when the current task finishes.", Op: a.currentOp()})
		return
	}
	os.Remove(a.backupPath(b.FileName))
	os.Remove(a.backupPath(b.FileName) + ".sha256")
	if _, err := a.db.Exec(`DELETE FROM backups WHERE id = ?`, b.ID); err != nil {
		writeError(w, err)
		return
	}
	a.audit(actor, "backup.deleted", b.ID, "succeeded", b.FileName)
	w.WriteHeader(http.StatusNoContent)
}

func (a *Agent) uploadLimit() int64 {
	limit := int64(64 << 30)
	if free, _, err := a.opts.DiskUsage(a.cfg.StagingDir()); err == nil {
		// The archive and its extracted copy both need room.
		if half := (free - minFreeAfterBackup) / 2; half < limit {
			limit = half
		}
	}
	if limit < 0 {
		limit = 0
	}
	return limit
}

func (a *Agent) hRestoreUpload(w http.ResponseWriter, r *http.Request) {
	actor := actorFromHeader(r)
	if actor == "unknown" {
		writeError(w, errInvalid("X-Playkeeper-Actor header is required"))
		return
	}
	p, err := a.stageArchive(r.Body, "upload", a.uploadLimit())
	if err != nil {
		a.audit(actor, "restore.uploaded", "", "refused", err.Error())
		writeError(w, err)
		return
	}
	a.audit(actor, "restore.uploaded", p.ID, "validated", "sha256 "+p.SHA256)
	writeJSON(w, http.StatusOK, p)
}

func (a *Agent) hRestoreFromBackup(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actionActor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	b, err := a.getBackup(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	f, err := os.Open(a.backupPath(b.FileName))
	if err != nil {
		writeError(w, errNotFound("Backup file"))
		return
	}
	defer f.Close()
	p, err := a.stageArchive(f, "backup "+b.ID, a.uploadLimit())
	if err != nil {
		a.audit(actor, "restore.staged", b.ID, "refused", err.Error())
		writeError(w, err)
		return
	}
	if p.SHA256 != b.SHA256 {
		os.RemoveAll(a.stageDir(p.ID))
		writeError(w, &apiError{Status: http.StatusUnprocessableEntity, Code: api.CodeInvalid, Msg: "The backup file no longer matches its recorded checksum.", Hint: "Nothing was changed. Use another backup."})
		return
	}
	a.audit(actor, "restore.staged", b.ID, "validated", "sha256 "+p.SHA256)
	writeJSON(w, http.StatusOK, p)
}

func (a *Agent) hRestorePreview(w http.ResponseWriter, r *http.Request) {
	st, err := a.loadStage(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st.preview)
}

func (a *Agent) hRestoreApply(w http.ResponseWriter, r *http.Request) {
	var req api.RestoreApplyRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	st, err := a.loadStage(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	p := st.preview
	if !p.Compatible {
		writeError(w, errConflict("This backup cannot be restored here: "+strings.Join(p.Problems, " "), ""))
		return
	}
	if strings.TrimSpace(req.Confirm) != p.ConfirmPhrase {
		a.audit(actor, "restore.applied", r.PathValue("id"), "refused", "confirmation phrase mismatch")
		writeError(w, errInvalid("Type \"%s\" to confirm the restore.", p.ConfirmPhrase))
		return
	}
	if p.NeedsEULA && !req.AcceptEULA {
		writeErr(w, http.StatusBadRequest, api.CodeEULARequired, "You must accept the Minecraft EULA before Playkeeper downloads or starts a server.", "")
		return
	}
	if req.MemoryMB != 0 {
		if err := a.validMemory(req.MemoryMB); err != nil {
			writeError(w, err)
			return
		}
	}
	op, err := a.beginOp("restore", actor, func(ctx context.Context, h *opHandle) error {
		if p.NeedsEULA {
			a.audit(actor, "eula.accepted", "minecraft-eula", "recorded", "https://www.minecraft.net/en-us/eula")
		}
		return a.restoreOp(ctx, h, st, req, actor)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (a *Agent) hRestoreDiscard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !reStageID.MatchString(id) {
		writeError(w, errInvalid("invalid restore id"))
		return
	}
	if a.busy() {
		if op := a.currentOp(); op != nil && op.Kind == "restore" {
			writeError(w, errConflict("A restore is in progress.", ""))
			return
		}
	}
	os.RemoveAll(a.stageDir(id))
	w.WriteHeader(http.StatusNoContent)
}

func (a *Agent) hAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	list, err := a.listAudit(limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}
