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

// Machine reports the machine and what its servers take of it.
func (a *Agent) Machine(ctx context.Context) api.Machine {
	host, _ := os.Hostname()
	m := api.Machine{
		Hostname: host, OS: osName(), Arch: archName(), CPUs: numCPU(),
		MemoryTotalMB: a.opts.HostMemoryMB(), SystemReserveMB: minecraft.HostReserveMB,
		ServersMemoryMB: a.reservedMemoryMB(""), AgentVersion: version.Version,
		DefaultGamePort: a.cfg.GamePort, OfflineModeTest: a.offline(), Operation: a.machineOp(),
		UpdateInstalling: a.installingUpdate(), Servers: len(a.serverList()),
	}
	m.MemoryFreeMB = max(0, m.MemoryTotalMB-m.SystemReserveMB-m.ServersMemoryMB)
	if info := a.updateInfo(); info.Available {
		m.UpdateAvailable = info.Latest
	}
	if free, total, err := a.opts.DiskUsage(a.cfg.DataDir); err == nil {
		m.DiskFreeBytes, m.DiskTotalBytes = &free, &total
		if dc := diskCheck(free); dc.Status != "pass" {
			m.DiskWarning = &dc
		}
	}
	a.mu.Lock()
	m.Docker, m.DockerVersion = a.dockerOK, a.dockerVersion
	if a.hostCPU != nil {
		v := *a.hostCPU
		m.CPUPercent = &v
	}
	a.mu.Unlock()
	return m
}

func (a *Agent) hMachine(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.Machine(r.Context()))
}

// catalogInfo is what a server can choose: for a new server when forServer
// is empty, or for an existing one's settings.
func (a *Agent) catalogInfo(ctx context.Context, forServer string) api.Catalog {
	host := a.opts.HostMemoryMB()
	opts, rec, max := a.memoryFor(forServer)
	if opts == nil {
		opts = []int{}
	}
	c := api.Catalog{
		Type: api.TypePaper, Types: serverTypes(), Versions: []api.CatalogEntry{},
		MemoryOptionsMB: opts, RecommendedMemoryMB: rec, HostMemoryMB: host, MaxMemoryMB: max,
		SystemReserveMB: minecraft.HostReserveMB, MemoryFreeMB: max, Servers: []api.ServerMemory{}, Image: minecraft.ImageTag,
	}
	for _, s := range a.serverList() {
		sc, _ := s.serverConfig()
		if sc == nil || s.id == forServer {
			continue
		}
		_, running, _ := s.containerRunning(ctx)
		c.Servers = append(c.Servers, api.ServerMemory{ID: s.id, Name: s.name(), MemoryMB: sc.MemoryMB, Running: running})
	}
	if forServer == "" {
		if p, err := a.nextGamePort(); err == nil {
			c.SuggestedPort = p
		}
	}
	if v, at, err := a.versionCatalog(ctx); err != nil {
		c.VersionsError = "Could not load the Minecraft versions from PaperMC: " + err.Error() + ". Check that this server can reach fill.papermc.io."
	} else {
		c.Versions = v
		c.VersionsCheckedAt = &at
	}
	return c
}

func (a *Agent) hCatalog(w http.ResponseWriter, r *http.Request) {
	forServer := r.URL.Query().Get("server")
	if forServer != "" && a.serverByID(forServer) == nil {
		writeError(w, errNotFound("Server"))
		return
	}
	if t := r.URL.Query().Get("type"); t != "" && t != api.TypePaper {
		writeError(w, errInvalid("Only Paper servers can be created for now."))
		return
	}
	writeJSON(w, http.StatusOK, a.catalogInfo(r.Context(), forServer))
}

// Status assembles the server's desired and observed state. Nothing here is
// cached from earlier runs: player and resource snapshots are dropped once
// stale.
func (s *server) Status(ctx context.Context) api.ServerStatus {
	st := api.ServerStatus{ID: s.id, GamePort: s.gamePort, OfflineModeTest: s.offline(), CollectingSince: s.collectingSince()}
	if row, err := s.row(); err == nil {
		st.Name, st.Slug, st.Game, st.Type, st.CreatedAt = row.Name, row.Slug, row.Game, row.Type, row.CreatedAt
	}
	sc, _ := s.serverConfig()
	st.Config = sc
	st.Exists = sc != nil
	st.Desired = s.desired()
	st.Operation = s.currentOp()
	if st.Operation == nil {
		st.Operation = s.machineOp()
	}
	st.LastOperation = s.lastFinishedOperation()
	c, err := s.docker.ContainerInspect(ctx, s.containerName())
	s.mu.Lock()
	runPhase, detail := s.runPhase, s.runPhaseDetail
	st.LastError, st.LastErrorHint = s.lastError, s.lastErrorHint
	crashed := s.crashed
	st.CrashCount = len(s.crashes)
	players, res := s.players, s.resources
	reachable, reachableAt := s.reachable, s.reachableAt
	if !s.worldAt.IsZero() {
		world := s.worldBytes
		st.WorldBytes = &world
	}
	s.mu.Unlock()
	fresh := func(t time.Time) bool { return s.now().Sub(t) < 3*s.opts.SampleInterval+5*time.Second }
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
		_, hash := s.containerSpec(*sc, false)
		st.PendingRestart = c.Config.Labels[labelSpec] != hash
	default:
		st.Phase = api.PhaseStopped
		if crashed {
			st.Phase = api.PhaseCrashed
		}
		code := c.State.ExitCode
		st.ExitCode = &code
		if t, ok := c.State.Finished(); ok {
			st.StoppedAt = &t
		}
	}
	if sc != nil && running && iconNewer(sc, st.StartedAt) {
		st.PendingRestart = true
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
	if list, err := s.listBackups(`verified = 1 AND kind = 'manual'`); err == nil && len(list) > 0 {
		st.LastBackup = &list[0]
	} else if list, err := s.listBackups(`verified = 1`); err == nil && len(list) > 0 {
		st.LastBackup = &list[0]
	}
	if sc != nil {
		st.Gameplay = effectiveGameplay(sc.Gameplay, readProperties(s.dataDir()))
	}
	st.FirstSteps = s.firstSteps()
	return st
}

// firstSteps ticks off the "Get started" checklist from what has happened.
func (s *server) firstSteps() api.FirstSteps {
	var fs api.FirstSteps
	if list, err := s.whitelist(); err == nil && len(list) > 0 {
		fs.Invited = list[0].Name
	}
	var player string
	var ts int64
	if s.db.QueryRow(`SELECT player, start_ts FROM sessions WHERE server_id = ? ORDER BY start_ts LIMIT 1`, s.id).Scan(&player, &ts) == nil {
		t := time.UnixMilli(ts).UTC()
		fs.FriendJoined, fs.FriendJoinedAt = player, &t
	}
	var n, dl int
	_ = s.db.QueryRow(`SELECT COUNT(*), COUNT(downloaded_at) FROM backups WHERE server_id = ? AND kind = 'manual' AND verified = 1`, s.id).Scan(&n, &dl)
	fs.BackedUp, fs.Downloaded = n > 0, dl > 0
	return fs
}

func (s *server) hStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Status(r.Context()))
}

func (a *Agent) hServers(w http.ResponseWriter, r *http.Request) {
	out := []api.ServerStatus{}
	for _, s := range a.serverList() {
		out = append(out, s.Status(r.Context()))
	}
	writeJSON(w, http.StatusOK, out)
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

var playStyles = map[string]bool{"": true, "friends": true, "creative": true, "hardcore": true, "solo": true}

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
	typ := req.Type
	if typ == "" {
		typ = api.TypePaper
	}
	if !typeAvailable(typ) {
		writeError(w, errInvalid("%s servers can't be created yet. Choose Paper.", typeName(typ)))
		return
	}
	name := ""
	if strings.TrimSpace(req.Name) != "" {
		if name, err = validName(req.Name); err != nil {
			writeError(w, err)
			return
		}
	}
	if !playStyles[req.PlayStyle] {
		writeError(w, errInvalid("Unknown play style."))
		return
	}
	entry, err := a.catalogEntry(r.Context(), req.VersionID)
	if err != nil {
		writeError(w, err)
		return
	}
	if entry.Experimental && !req.AcceptExperimental {
		writeError(w, errInvalid("%s is experimental. Confirm that you accept the risk to your world to use it.", entry.Label))
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
	var gp api.Gameplay
	if req.Gameplay != nil {
		if gp, err = validGameplay(*req.Gameplay, true); err != nil {
			writeError(w, err)
			return
		}
	}
	now := a.now().UTC()
	sc := withBuild(api.ServerConfig{
		Type: typ, MemoryMB: req.MemoryMB, HeapMB: minecraft.HeapMB(req.MemoryMB),
		LevelName: "world", MOTD: motd, MaxPlayers: maxPlayers, Whitelist: true, EULAAcceptedAt: now, EULAAcceptedBy: actor, CreatedAt: now,
		PlayStyle: req.PlayStyle, Gameplay: gp,
	}, entry)
	_, op, err := a.addServer(newServerSpec{name: name, typ: typ, config: sc, desired: api.DesiredRunning, actor: actor}, "create", func(s *server) func(ctx context.Context, h *opHandle) error {
		return func(ctx context.Context, h *opHandle) error {
			s.audit(actor, "eula.accepted", "minecraft-eula", "recorded", "https://www.minecraft.net/en-us/eula")
			s.recordEvent(s.now(), "server_created", "", "playkeeper", entry.Label)
			if err := s.startServer(ctx, h, sc); err != nil {
				s.startFailed(ctx)
				return err
			}
			return nil
		}
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func actionActor(r *http.Request) (string, error) {
	var req api.ActionRequest
	if err := decode(r, &req); err != nil {
		return "", err
	}
	return validActor(req.Actor)
}

func (s *server) hStart(w http.ResponseWriter, r *http.Request) {
	actor, err := actionActor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	_, running, err := s.containerRunning(r.Context())
	if err == nil && running {
		_ = s.setDesired(api.DesiredRunning)
	}
	release()
	if err != nil {
		writeError(w, err)
		return
	}
	if running {
		s.audit(actor, "start", "server", "no-op", "already running")
		writeJSON(w, http.StatusOK, map[string]any{"noop": true, "message": "The server is already running."})
		return
	}
	s.mu.Lock()
	s.crashes, s.crashed, s.nextAutoRestart = nil, false, time.Time{}
	s.mu.Unlock()
	op, err := s.beginOp("start", actor, func(ctx context.Context, h *opHandle) error {
		if err := s.setDesired(api.DesiredRunning); err != nil {
			return err
		}
		cur, _ := s.serverConfig()
		if cur == nil {
			return errNotCreated()
		}
		if err := s.startServer(ctx, h, *cur); err != nil {
			s.startFailed(ctx)
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

func (s *server) hStop(w http.ResponseWriter, r *http.Request) {
	actor, err := actionActor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	_, running, err := s.containerRunning(r.Context())
	if err == nil && !running {
		_ = s.setDesired(api.DesiredStopped)
		s.mu.Lock()
		s.crashed = false
		s.mu.Unlock()
	}
	release()
	if err != nil {
		writeError(w, err)
		return
	}
	if !running {
		s.audit(actor, "stop", "server", "no-op", "already stopped")
		writeJSON(w, http.StatusOK, map[string]any{"noop": true, "message": "The server is already stopped."})
		return
	}
	op, err := s.beginOp("stop", actor, func(ctx context.Context, h *opHandle) error {
		if err := s.setDesired(api.DesiredStopped); err != nil {
			return err
		}
		return s.stopServer(ctx, h)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// restart stops and starts a running server, with its current settings.
func (s *server) restart(actor string) (*api.Operation, error) {
	return s.beginOp("restart", actor, func(ctx context.Context, h *opHandle) error {
		if err := s.stopServer(ctx, h); err != nil {
			return err
		}
		cur, _ := s.serverConfig()
		if cur == nil {
			return errNotCreated()
		}
		if err := s.startServer(ctx, h, *cur); err != nil {
			s.startFailed(ctx)
			return err
		}
		return nil
	})
}

func (s *server) hRestart(w http.ResponseWriter, r *http.Request) {
	actor, err := actionActor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if s.busy() {
		writeError(w, s.busyError())
		return
	}
	if _, running, err := s.containerRunning(r.Context()); err != nil {
		writeError(w, err)
		return
	} else if !running {
		writeError(w, errConflict("The server is not running, so it cannot be restarted.", "Use Start instead."))
		return
	}
	op, err := s.restart(actor)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (s *server) hSettings(w http.ResponseWriter, r *http.Request) {
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
	if err := s.applySettings(req, actor); err != nil {
		writeError(w, err)
		return
	}
	resp := api.SettingsResponse{}
	if req.Restart {
		if _, running, _ := s.containerRunning(r.Context()); running {
			op, err := s.restart(actor)
			if err != nil {
				writeError(w, err)
				return
			}
			resp.Operation = op
		}
	}
	resp.Server = s.Status(r.Context())
	status := http.StatusOK
	if resp.Operation != nil {
		status = http.StatusAccepted
	}
	writeJSON(w, status, resp)
}

// applySettings checks a settings change in full, then saves it. It holds the
// server's operation lock, so no operation rewrites the settings meanwhile,
// and createMu, so a name or memory share is checked against the other
// servers' current ones.
func (s *server) applySettings(req api.SettingsRequest, actor string) error {
	release, ok := s.holdOpLock()
	if !ok {
		return s.busyError()
	}
	defer release()
	s.createMu.Lock()
	defer s.createMu.Unlock()
	if s.busy() {
		return s.busyError()
	}
	sc, err := s.serverConfig()
	if err != nil {
		return err
	}
	if sc == nil {
		return errNotCreated()
	}
	var changed []string
	old := s.name()
	name := old
	if req.Name != nil {
		if name, err = validName(*req.Name); err != nil {
			return err
		}
		if s.nameTaken(name, s.id) {
			return errConflict(fmt.Sprintf("A server named %q already exists on this machine.", name), "Pick another name.")
		}
		if name != old {
			changed = append(changed, fmt.Sprintf("name %q→%q", old, name))
		}
	}
	if req.MemoryMB != nil {
		if err := s.validMemory(*req.MemoryMB, s.id); err != nil {
			return err
		}
		if sc.MemoryMB != *req.MemoryMB {
			changed = append(changed, fmt.Sprintf("memoryMB %d→%d", sc.MemoryMB, *req.MemoryMB))
		}
		sc.MemoryMB, sc.HeapMB = *req.MemoryMB, minecraft.HeapMB(*req.MemoryMB)
	}
	if req.MOTD != nil {
		m, err := validMOTD(*req.MOTD)
		if err != nil {
			return err
		}
		if m != sc.MOTD {
			changed = append(changed, "motd")
		}
		sc.MOTD = m
	}
	if req.MaxPlayers != nil {
		n, err := validMaxPlayers(*req.MaxPlayers)
		if err != nil || *req.MaxPlayers == 0 {
			return errInvalid("Max players must be between 1 and 100.")
		}
		if n != sc.MaxPlayers {
			changed = append(changed, fmt.Sprintf("maxPlayers %d→%d", sc.MaxPlayers, n))
		}
		sc.MaxPlayers = n
	}
	if req.Gameplay != nil {
		gp, err := validGameplay(*req.Gameplay, false)
		if err != nil {
			return err
		}
		next := mergeGameplay(sc.Gameplay, gp)
		changed = append(changed, gameplayChanges(effectiveGameplay(sc.Gameplay, readProperties(s.dataDir())), effectiveGameplay(next, nil), gp)...)
		sc.Gameplay = next
	}
	if name != old {
		if _, err := s.db.Exec(`UPDATE servers SET name = ? WHERE id = ?`, name, s.id); err != nil {
			return err
		}
	}
	if err := s.saveServerConfig(*sc); err != nil {
		return err
	}
	s.audit(actor, "settings.changed", "server", "succeeded", strings.Join(changed, ", "))
	return nil
}

func (s *server) hDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteServerRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	name := s.name()
	if strings.TrimSpace(req.Confirm) != name {
		s.audit(actor, "server.deleted", s.id, "refused", "confirmation did not match the name")
		writeError(w, errInvalid("Type the server's name, %q, to delete it.", name))
		return
	}
	op, err := s.beginOp("delete", actor, func(ctx context.Context, h *opHandle) error {
		if err := s.setDesired(api.DesiredStopped); err != nil {
			return err
		}
		if err := s.deleteServer(ctx, h, actor); err != nil {
			return err
		}
		s.prunePacks(s.Agent.ctx)
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (s *server) hLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > consoleCapacity {
		limit = 500
	}
	writeJSON(w, http.StatusOK, s.console.since(q.Get("epoch"), after, limit))
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

// online reports whether the server can take console commands.
func (s *server) online(ctx context.Context) bool {
	s.mu.Lock()
	online := s.runPhase == api.PhaseOnline
	s.mu.Unlock()
	_, running, _ := s.containerRunning(ctx)
	return running && online
}

func (s *server) hCommand(w http.ResponseWriter, r *http.Request) {
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
	if !s.online(r.Context()) {
		s.audit(actor, "console.command", "server", "refused", minecraft.RedactIPs(cmd))
		writeError(w, errConflict("The server is not online, so it cannot run commands.", "Start the server first."))
		return
	}
	out, err := s.rconCommand(cmd)
	if err != nil {
		s.audit(actor, "console.command", "server", "failed", minecraft.RedactIPs(cmd))
		writeError(w, &apiError{Status: http.StatusBadGateway, Code: api.CodeInternal, Msg: "The server did not accept the command: " + err.Error(), Hint: "Wait until the server is online and try again."})
		return
	}
	s.audit(actor, "console.command", "server", "succeeded", minecraft.RedactIPs(cmd))
	writeJSON(w, http.StatusOK, api.CommandResponse{Output: minecraft.CleanLine(out)})
}

func (a *Agent) hOperation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !reStageID.MatchString(id) {
		writeError(w, errInvalid("invalid operation id"))
		return
	}
	if cur := a.machineOp(); cur != nil && cur.ID == id {
		writeJSON(w, http.StatusOK, cur)
		return
	}
	for _, s := range a.serverList() {
		if cur := s.currentOp(); cur != nil && cur.ID == id {
			writeJSON(w, http.StatusOK, cur)
			return
		}
	}
	op, err := a.loadOperation(id)
	if err != nil {
		writeError(w, errNotFound("Operation"))
		return
	}
	writeJSON(w, http.StatusOK, op)
}

func (s *server) hMetrics(w http.ResponseWriter, r *http.Request) {
	rg := r.URL.Query().Get("range")
	if rg == "" {
		rg = "24h"
	}
	m, err := s.Metrics(rg, s.now())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *server) hSessions(w http.ResponseWriter, r *http.Request) {
	rg := r.URL.Query().Get("range")
	if rg == "" {
		rg = "7d"
	}
	spec, ok := metricRanges[rg]
	if !ok {
		writeError(w, errInvalid("range must be one of 1h, 24h, 7d, 30d"))
		return
	}
	now := s.now()
	list, err := s.Sessions(now.Add(-spec.span), now, now, 500)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.SessionsResponse{From: now.Add(-spec.span).UTC(), To: now.UTC(), Sessions: list})
}

func (s *server) hSummary(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days == 0 {
		days = 14
	}
	sum, err := s.Summary(days, r.URL.Query().Get("tz"), s.now())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *server) hEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	ev, err := s.Events(limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (a *Agent) hActivity(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	id := r.URL.Query().Get("server")
	if id != "" && a.serverByID(id) == nil {
		writeError(w, errNotFound("Server"))
		return
	}
	list, err := a.Activity(id, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) hBackups(w http.ResponseWriter, r *http.Request) {
	list, err := s.listBackups("")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) hBackupCreate(w http.ResponseWriter, r *http.Request) {
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
	op, err := s.beginOp("backup", actor, func(ctx context.Context, h *opHandle) error {
		return s.backupOp(ctx, h, actor, strings.TrimSpace(req.Note))
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (s *server) hBackupVerify(w http.ResponseWriter, r *http.Request) {
	actor, err := actionActor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	b, err := s.verifyBackup(r.PathValue("bid"))
	if err != nil {
		writeError(w, err)
		return
	}
	result := "failed"
	if b.Verified != nil && *b.Verified {
		result = "succeeded"
	}
	s.audit(actor, "backup.verified", b.ID, result, b.VerifyError)
	writeJSON(w, http.StatusOK, b)
}

func (s *server) hBackupDownload(w http.ResponseWriter, r *http.Request) {
	b, err := s.getBackup(r.PathValue("bid"))
	if err != nil {
		writeError(w, err)
		return
	}
	f, err := os.Open(s.backupPath(b.FileName))
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
		_, _ = s.db.Exec(`UPDATE backups SET downloaded_at = ? WHERE id = ?`, s.now().UnixMilli(), b.ID)
		s.audit(actorFromHeader(r), "backup.downloaded", b.ID, "succeeded", b.FileName)
	}
}

func actorFromHeader(r *http.Request) string {
	a, err := validActor(r.Header.Get("X-Playkeeper-Actor"))
	if err != nil {
		return "unknown"
	}
	return a
}

func (s *server) hBackupDelete(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	b, err := s.getBackup(r.PathValue("bid"))
	if err != nil {
		writeError(w, err)
		return
	}
	if s.busy() {
		writeError(w, &apiError{Status: http.StatusConflict, Code: api.CodeBusy, Msg: s.name() + " is busy; try again when the current task finishes.", Op: s.currentOp()})
		return
	}
	os.Remove(s.backupPath(b.FileName))
	os.Remove(s.backupPath(b.FileName) + ".sha256")
	if _, err := s.db.Exec(`DELETE FROM backups WHERE id = ?`, b.ID); err != nil {
		writeError(w, err)
		return
	}
	s.audit(actor, "backup.deleted", b.ID, "succeeded", b.FileName)
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

// hRestoreUpload stages an uploaded archive to replace this server's world.
func (s *server) hRestoreUpload(w http.ResponseWriter, r *http.Request) {
	s.restoreUpload(w, r, s)
}

// hRestoreUploadNew stages an uploaded archive for a new server.
func (a *Agent) hRestoreUploadNew(w http.ResponseWriter, r *http.Request) {
	a.restoreUpload(w, r, nil)
}

func (a *Agent) restoreUpload(w http.ResponseWriter, r *http.Request, target *server) {
	actor := actorFromHeader(r)
	if actor == "unknown" {
		writeError(w, errInvalid("X-Playkeeper-Actor header is required"))
		return
	}
	p, err := a.stageArchive(r.Body, "upload", a.uploadLimit(), target)
	if err != nil {
		a.auditFor(serverIDOf(target), actor, "restore.uploaded", "", "refused", err.Error())
		writeError(w, err)
		return
	}
	a.auditFor(serverIDOf(target), actor, "restore.uploaded", p.ID, "validated", "sha256 "+p.SHA256)
	writeJSON(w, http.StatusOK, p)
}

func serverIDOf(s *server) string {
	if s == nil {
		return ""
	}
	return s.id
}

func (s *server) hRestoreFromBackup(w http.ResponseWriter, r *http.Request) {
	actor, err := actionActor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	b, err := s.getBackup(r.PathValue("bid"))
	if err != nil {
		writeError(w, err)
		return
	}
	f, err := os.Open(s.backupPath(b.FileName))
	if err != nil {
		writeError(w, errNotFound("Backup file"))
		return
	}
	defer f.Close()
	p, err := s.stageArchive(f, "backup "+b.ID, s.uploadLimit(), s)
	if err != nil {
		s.audit(actor, "restore.staged", b.ID, "refused", err.Error())
		writeError(w, err)
		return
	}
	if p.SHA256 != b.SHA256 {
		os.RemoveAll(s.stageDir(p.ID))
		writeError(w, &apiError{Status: http.StatusUnprocessableEntity, Code: api.CodeInvalid, Msg: "The backup file no longer matches its recorded checksum.", Hint: "Nothing was changed. Use another backup."})
		return
	}
	s.audit(actor, "restore.staged", b.ID, "validated", "sha256 "+p.SHA256)
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
		a.auditFor(p.ServerID, actor, "restore.applied", r.PathValue("id"), "refused", "confirmation phrase mismatch")
		writeError(w, errInvalid("Type \"%s\" to confirm the restore.", p.ConfirmPhrase))
		return
	}
	if p.NeedsEULA && !req.AcceptEULA {
		writeErr(w, http.StatusBadRequest, api.CodeEULARequired, "You must accept the Minecraft EULA before Playkeeper downloads or starts a server.", "")
		return
	}
	name := ""
	if strings.TrimSpace(req.Name) != "" {
		if name, err = validName(req.Name); err != nil {
			writeError(w, err)
			return
		}
	}
	target := a.serverByID(p.ServerID)
	if p.ServerID != "" && target == nil {
		writeError(w, errNotFound("Server"))
		return
	}
	if req.MemoryMB != 0 {
		if err := a.validMemory(req.MemoryMB, p.ServerID); err != nil {
			writeError(w, err)
			return
		}
	}
	restore := func(s *server) func(ctx context.Context, h *opHandle) error {
		return func(ctx context.Context, h *opHandle) error {
			if p.NeedsEULA {
				s.audit(actor, "eula.accepted", "minecraft-eula", "recorded", "https://www.minecraft.net/en-us/eula")
			}
			return s.restoreOp(ctx, h, st, req, actor)
		}
	}
	var op *api.Operation
	if target == nil {
		op, err = a.restoreAsNewServer(st, req, name, actor, restore)
	} else {
		op, err = target.beginOp("restore", actor, restore(target))
	}
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
	for _, s := range a.serverList() {
		if op := s.currentOp(); op != nil && op.Kind == "restore" && op.Detail["stage"] == id {
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

// whitelist reads the server's allowlist file.
func (s *server) whitelist() ([]api.WhitelistEntry, error) {
	b, err := os.ReadFile(filepath.Join(s.dataDir(), "whitelist.json"))
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
