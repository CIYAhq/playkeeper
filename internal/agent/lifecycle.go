package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// opHandle lets a running operation publish its phase.
type opHandle struct {
	save func(op *api.Operation)
	mu   func() func()
	op   *api.Operation
	// continues is set by an operation that goes on outside the agent (an
	// update handed to the updater): it stays running until its result is
	// recorded.
	continues bool
}

func (h *opHandle) phase(p string) {
	unlock := h.mu()
	h.op.Phase = p
	snap := copyOp(h.op)
	unlock()
	h.save(snap)
}

func (h *opHandle) set(key string, v any) {
	h.setAll(map[string]any{key: v})
}

func (h *opHandle) setAll(kv map[string]any) {
	unlock := h.mu()
	for k, v := range kv {
		h.op.Detail[k] = v
	}
	snap := copyOp(h.op)
	unlock()
	h.save(snap)
}

// copyOp copies an operation with its own Detail map, so the copy can be read
// and encoded while the operation goes on.
func copyOp(op *api.Operation) *api.Operation {
	if op == nil {
		return nil
	}
	c := *op
	c.Detail = map[string]any{}
	for k, v := range op.Detail {
		c.Detail[k] = v
	}
	return &c
}

func (s *server) currentOp() *api.Operation {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return copyOp(s.op)
}

// busy is true while this server runs an operation, or the machine runs one
// (a Playkeeper update being staged or installed).
func (s *server) busy() bool {
	return s.currentOp() != nil || s.installingUpdate() != "" || s.machineOp() != nil
}

var opLabels = map[string]string{
	"create": "being created", "start": "starting", "stop": "stopping",
	"restart": "restarting", "backup": "a backup", "restore": "a restore", "recover": "an automatic restart",
	"auto-restart": "an automatic restart after a crash", "delete-backup": "deleting a backup",
	"update": "a Playkeeper update", "update-version": "updating Minecraft", "delete": "being deleted",
}

// machineBusy is the error for a request that has to wait for a machine-wide
// operation (a Playkeeper update being staged or installed), or nil.
func (a *Agent) machineBusy() error {
	if v := a.installingUpdate(); v != "" {
		return &apiError{Status: http.StatusConflict, Code: api.CodeBusy, Msg: "Playkeeper is installing update " + v + ".", Hint: "The dashboard reconnects when it is done; try again then."}
	}
	if op := a.machineOp(); op != nil {
		return &apiError{Status: http.StatusConflict, Code: api.CodeBusy, Msg: "Playkeeper is busy with " + opLabels[op.Kind] + ".", Hint: "Wait for it to finish, then try again.", Op: op}
	}
	return nil
}

func (s *server) busyError() error {
	if err := s.machineBusy(); err != nil {
		return err
	}
	cur := s.currentOp()
	what := "another operation"
	if cur != nil {
		what = opLabels[cur.Kind]
	}
	return &apiError{Status: http.StatusConflict, Code: api.CodeBusy, Msg: s.name() + " is busy with " + what + ".", Hint: "Wait for it to finish, then try again.", Op: cur}
}

// holdOpLock takes the server's operation lock for a short decision, such as
// a start or stop that turns out to be a no-op, so it cannot interleave with
// an operation that changes the desired state.
func (s *server) holdOpLock() (release func(), ok bool) {
	select {
	case s.opLock <- struct{}{}:
		return func() { <-s.opLock }, true
	default:
		return nil, false
	}
}

// beginOp runs fn as the server's single exclusive operation. Concurrent
// requests for the same server get 409 with the operation in progress; other
// servers are not affected.
func (s *server) beginOp(kind, actor string, fn func(ctx context.Context, h *opHandle) error) (*api.Operation, error) {
	select {
	case s.opLock <- struct{}{}:
	default:
		return nil, s.busyError()
	}
	if err := s.machineBusy(); err != nil {
		<-s.opLock
		return nil, err
	}
	return s.startOp(kind, actor, fn), nil
}

// startOp starts fn as the server's operation. The caller holds the
// operation lock, which the operation releases when fn returns.
func (s *server) startOp(kind, actor string, fn func(ctx context.Context, h *opHandle) error) *api.Operation {
	op := &api.Operation{ID: newID(), ServerID: s.id, Kind: kind, Status: api.OpRunning, Actor: actor, StartedAt: s.now().UTC(), Detail: map[string]any{}}
	s.opMu.Lock()
	s.op = op
	snap := copyOp(op)
	s.opMu.Unlock()
	s.saveOperation(snap)
	h := &opHandle{save: s.saveOperation, op: op, mu: func() func() { s.opMu.Lock(); return s.opMu.Unlock }}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.opLock }()
		ctx, cancel := context.WithTimeout(s.ctx, 45*time.Minute)
		defer cancel()
		err := runOp(ctx, h, fn)
		s.opMu.Lock()
		done := finishOp(op, h, err, s.now().UTC())
		s.op = nil
		s.opMu.Unlock()
		s.saveOperation(&done)
		if done.Status != api.OpRunning {
			s.audit(actor, kind, "server", done.Status, done.Error)
		}
		if err != nil {
			s.log.Warn("operation failed", "server", s.id, "kind", kind, "err", err)
		}
	}()
	return snap
}

func runOp(ctx context.Context, h *opHandle, fn func(ctx context.Context, h *opHandle) error) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("internal error: %v", v)
		}
	}()
	return fn(ctx, h)
}

// finishOp records how an operation ended; the caller holds its mutex.
func finishOp(op *api.Operation, h *opHandle, err error, fin time.Time) api.Operation {
	switch {
	case err != nil:
		op.FinishedAt = &fin
		op.Status = api.OpFailed
		op.Error = err.Error()
		var ae *apiError
		if errors.As(err, &ae) {
			op.Hint = ae.Hint
		}
	case h.continues:
	default:
		op.FinishedAt = &fin
		op.Status = api.OpSucceeded
	}
	return *copyOp(op)
}

func (a *Agent) offline() bool { return a.opts.OfflineModeTest }

func (s *server) levelName(sc api.ServerConfig) string {
	if sc.LevelName == "" {
		return "world"
	}
	return sc.LevelName
}

// containerSpec is the complete, hardened definition of the server's
// container. Its hash is stored as a label so any drift forces a recreate. A
// v1 server's definition is exactly 0.2.0's while its settings are unchanged.
func (s *server) containerSpec(sc api.ServerConfig, setupOnly bool) (docker.ContainerConfig, string) {
	online := "TRUE"
	if s.offline() {
		online = "FALSE"
	}
	// Only the setup-only container downloads Paper. The server container runs
	// the jar that was verified against the pinned checksum, so a start never
	// re-downloads Paper. SKIP_DOWNLOAD_DEFAULTS stops the image fetching
	// unpinned default config files from a third-party repository.
	env := []string{"EULA=TRUE", "VERSION=" + sc.MinecraftVersion}
	if setupOnly {
		env = append(env, "TYPE=PAPER", "PAPER_BUILD="+strconv.Itoa(sc.PaperBuild), "SETUP_ONLY=TRUE")
	} else {
		env = append(env, "TYPE=CUSTOM", "CUSTOM_SERVER=/data/"+filepath.Base(s.jarPath(sc)))
	}
	env = append(env,
		"SKIP_DOWNLOAD_DEFAULTS=TRUE",
		"MEMORY="+strconv.Itoa(minecraft.HeapMB(sc.MemoryMB))+"M",
		"MOTD="+sc.MOTD,
		"MAX_PLAYERS="+strconv.Itoa(sc.MaxPlayers),
		"ONLINE_MODE="+online,
		"ENABLE_WHITELIST=TRUE",
		"ENFORCE_WHITELIST=TRUE",
		"ENABLE_RCON=TRUE",
		"RCON_PORT="+strconv.Itoa(rconPort),
		"RCON_PASSWORD_FILE=/run/secrets/rcon_password",
		"BROADCAST_RCON_TO_OPS=FALSE",
		"LOG_IPS=FALSE",
		"ENABLE_QUERY=FALSE",
		"ENABLE_AUTOPAUSE=FALSE",
		"LEVEL="+s.levelName(sc),
		"SERVER_PORT=25565",
		"TZ=UTC",
		"USE_AIKAR_FLAGS=TRUE",
	)
	env = append(env, gameplayEnv(sc.Gameplay)...)
	limit := int64(sc.MemoryMB) << 20
	pids := int64(2048)
	stop := int(s.opts.StopTimeout.Seconds())
	cfg := docker.ContainerConfig{
		Image:       minecraft.Image,
		Env:         env,
		User:        fmt.Sprintf("%d:%d", s.cfg.GameUID, s.cfg.GameGID),
		StopSignal:  "SIGTERM",
		StopTimeout: &stop,
		Labels:      s.labels(),
		HostConfig: docker.HostConfig{
			Binds:         []string{s.dataDir() + ":/data", s.containerSecret() + ":/run/secrets/rcon_password:ro"},
			RestartPolicy: docker.RestartPolicy{Name: "no"},
			Memory:        limit,
			MemorySwap:    limit,
			PidsLimit:     &pids,
			CapDrop:       []string{"ALL"},
			SecurityOpt:   []string{"no-new-privileges"},
			LogConfig:     docker.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
			NetworkMode:   networkName,
		},
	}
	if !setupOnly {
		cfg.ExposedPorts = map[string]struct{}{"25565/tcp": {}}
		cfg.HostConfig.PortBindings = map[string][]docker.PortBinding{"25565/tcp": {{HostPort: strconv.Itoa(s.gamePort)}}}
	}
	b, _ := json.Marshal(cfg)
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:8])
	cfg.Labels[labelSpec] = hash
	return cfg, hash
}

func (a *Agent) ensureImage(ctx context.Context, h *opHandle) error {
	if _, err := a.docker.ImageInspect(ctx, minecraft.Image); err == nil {
		return nil
	} else if !docker.IsNotFound(err) {
		return a.dockerErr(err)
	}
	h.phase(string(api.PhasePulling))
	var last time.Time
	err := a.docker.ImagePull(ctx, minecraft.Image, func(p docker.PullProgress) {
		if time.Since(last) > time.Second {
			last = time.Now()
			h.set("pull", p.Status)
		}
	})
	if err != nil {
		return &apiError{Msg: "Could not download the Minecraft runtime image: " + err.Error(), Hint: "Check that this host can reach Docker Hub (registry-1.docker.io), then press Start again."}
	}
	return nil
}

func (a *Agent) ensureNetwork(ctx context.Context) error {
	if _, err := a.docker.NetworkInspect(ctx, networkName); err == nil {
		return nil
	} else if !docker.IsNotFound(err) {
		return a.dockerErr(err)
	}
	_, err := a.docker.NetworkCreate(ctx, networkName, map[string]string{labelManaged: "true", labelInstall: a.cfg.InstallID})
	if err != nil && !docker.IsConflict(err) {
		return a.dockerErr(err)
	}
	return nil
}

func (s *server) ensureDirs() error {
	data := s.dataDir()
	if err := os.MkdirAll(data, 0o750); err != nil {
		return err
	}
	for _, d := range []string{filepath.Dir(s.dir()), s.dir()} {
		if err := os.Chmod(d, 0o755); err != nil {
			return err
		}
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(data, s.cfg.GameUID, s.cfg.GameGID); err != nil {
			return err
		}
	}
	return s.ensureRCONSecret()
}

// ensureRCONSecret creates the host-generated RCON password: a root-only copy
// for the agent and a read-only copy the game user mounts into the container.
func (s *server) ensureRCONSecret() error {
	path := s.agentSecret()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		b = []byte(randomSecret(24))
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return err
	}
	cpath := s.containerSecret()
	if cur, err := os.ReadFile(cpath); err != nil || string(cur) != string(b) {
		os.Remove(cpath)
		if err := os.WriteFile(cpath, b, 0o400); err != nil {
			return err
		}
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(cpath, s.cfg.GameUID, s.cfg.GameGID); err != nil {
			return err
		}
	}
	return nil
}

// bStatsConfig switches off the bStats usage statistics that Paper enables by
// default and would send from the user's server to bstats.org.
const bStatsConfig = "# Written by Playkeeper: Paper's bStats usage statistics are off,\n# so this server does not report to bstats.org.\nenabled: false\n"

// ensureTelemetryOff runs before every start because restored archives carry
// the plugins directory, including whatever bStats setting they were made with.
func (s *server) ensureTelemetryOff() error {
	dir := filepath.Join(s.dataDir(), "plugins", "bStats")
	path := filepath.Join(dir, "config.yml")
	if b, err := os.ReadFile(path); err == nil && bStatsOff(b) {
		return nil
	}
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
		if os.Geteuid() == 0 {
			if err := os.Chown(d, s.cfg.GameUID, s.cfg.GameGID); err != nil {
				return err
			}
		}
	}
	if err := os.WriteFile(path, []byte(bStatsConfig), 0o640); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		return os.Chown(path, s.cfg.GameUID, s.cfg.GameGID)
	}
	return nil
}

func bStatsOff(b []byte) bool {
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "enabled" {
			return strings.TrimSpace(v) == "false"
		}
	}
	return false
}

func (s *server) rconPassword() (string, error) {
	b, err := os.ReadFile(s.agentSecret())
	return strings.TrimSpace(string(b)), err
}

func (a *Agent) dockerErr(err error) error {
	return &apiError{Status: http.StatusServiceUnavailable, Code: api.CodeDockerUnavailable, Msg: "Docker is not responding: " + err.Error(), Hint: "Check that the Docker service is running (sudo systemctl status docker)."}
}

func (s *server) jarPath(sc api.ServerConfig) string {
	return filepath.Join(s.dataDir(), fmt.Sprintf("paper-%s-%d.jar", sc.MinecraftVersion, sc.PaperBuild))
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ensureServerSoftware downloads Paper with a setup-only container (the
// server does not run) and verifies the jar against the checksum PaperMC's
// Fill v3 API published for the build, before the server is ever started
// with it.
func (s *server) ensureServerSoftware(ctx context.Context, h *opHandle, sc *api.ServerConfig) error {
	want, err := jarChecksum(*sc)
	if err != nil {
		return &apiError{Msg: "The server's software cannot be verified: " + err.Error() + ".", Hint: "Choose a version under Settings, or restore a backup."}
	}
	jar := s.jarPath(*sc)
	if sum, err := fileSHA256(jar); err == nil && sum == want {
		return nil
	}
	h.phase(string(api.PhaseDownloading))
	s.setRunPhase(api.PhaseDownloading, "")
	setupName := s.containerName() + "-setup"
	_ = s.docker.ContainerRemove(ctx, setupName, true)
	spec, _ := s.containerSpec(*sc, true)
	id, err := s.docker.ContainerCreate(ctx, setupName, spec)
	if err != nil {
		return s.dockerErr(err)
	}
	defer s.docker.ContainerRemove(context.Background(), id, true)
	if err := s.docker.ContainerStart(ctx, id); err != nil {
		return s.dockerErr(err)
	}
	var tail []string
	logs, err := s.docker.ContainerLogs(ctx, id, docker.LogsOptions{Follow: true})
	if err == nil {
		for {
			l, err := logs.Next()
			if err != nil {
				break
			}
			text := minecraft.CleanLine(l.Text)
			s.console.append(l.TS, text)
			tail = append(tail, text)
			if len(tail) > 8 {
				tail = tail[1:]
			}
		}
		logs.Close()
	}
	var c docker.ContainerJSON
	for i := 0; i < 60; i++ {
		c, err = s.docker.ContainerInspect(ctx, id)
		if err != nil || !c.State.Running {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return s.dockerErr(err)
	}
	if c.State.ExitCode != 0 {
		return &apiError{Msg: "Downloading the Minecraft server software failed (exit code " + strconv.Itoa(c.State.ExitCode) + "): " + lastNonEmpty(tail),
			Hint: "Check that this host can reach fill.papermc.io and piston-data.mojang.com, then press Start again."}
	}
	h.phase("verifying_download")
	sum, err := fileSHA256(jar)
	if err != nil {
		return &apiError{Msg: "The server software was not downloaded where expected (" + filepath.Base(jar) + ").", Hint: "Press Start to try again."}
	}
	if sum != want {
		os.Remove(jar)
		return &apiError{Msg: fmt.Sprintf("The downloaded %s does not match the pinned checksum (got %s, want %s). It was deleted and not run.", filepath.Base(jar), sum[:16], want[:16]),
			Hint: "This can mean a corrupted download or a tampered mirror. Press Start to download again."}
	}
	now := s.now().UTC()
	sc.JarVerifiedAt = &now
	if err := s.saveServerConfig(*sc); err != nil {
		return err
	}
	s.recordEvent(now, "server_software_verified", "", "playkeeper", filepath.Base(jar)+" sha256 "+sum)
	s.log.Info("server software verified", "server", s.id, "jar", filepath.Base(jar), "sha256", sum)
	return nil
}

func lastNonEmpty(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return "no output"
}

// startServer brings the server to "online". It is idempotent: a container
// already running with the desired spec is left alone.
func (s *server) startServer(ctx context.Context, h *opHandle, sc api.ServerConfig) error {
	if err := s.ensureDirs(); err != nil {
		return err
	}
	if err := s.ensureImage(ctx, h); err != nil {
		return err
	}
	if err := s.ensureNetwork(ctx); err != nil {
		return err
	}
	if err := s.ensureServerSoftware(ctx, h, &sc); err != nil {
		return err
	}
	if err := s.ensureTelemetryOff(); err != nil {
		return err
	}
	name := s.containerName()
	spec, hash := s.containerSpec(sc, false)
	c, err := s.docker.ContainerInspect(ctx, name)
	switch {
	case err == nil && c.Config.Labels[labelManaged] != "true":
		return &apiError{Msg: "A container named " + name + " exists but was not created by Playkeeper.", Hint: "Playkeeper will not touch it. Rename or remove that container, then press Start."}
	case err == nil && c.State.Running && c.Config.Labels[labelSpec] == hash:
		return nil
	case err == nil && c.State.Running:
		if err := s.stopContainer(ctx, h, c.ID); err != nil {
			return err
		}
		fallthrough
	case err == nil && c.Config.Labels[labelSpec] != hash:
		if err := s.docker.ContainerRemove(ctx, c.ID, true); err != nil && !docker.IsNotFound(err) {
			return s.dockerErr(err)
		}
		c.ID = ""
	case err != nil && !docker.IsNotFound(err):
		return s.dockerErr(err)
	}
	id := c.ID
	if id == "" || docker.IsNotFound(err) {
		id, err = s.docker.ContainerCreate(ctx, name, spec)
		if err != nil {
			return s.dockerErr(err)
		}
	}
	h.phase(string(api.PhaseStartingContainer))
	s.resetRun(api.PhaseStartingContainer)
	s.resetRCON()
	if err := s.docker.ContainerStart(ctx, id); err != nil {
		// A container whose start failed (for example on a busy port) can keep
		// broken network state; discard it so the next start creates it fresh.
		_ = s.docker.ContainerRemove(context.Background(), id, true)
		return classifyStartError(err, s.gamePort)
	}
	s.mu.Lock()
	delete(s.intentional, id)
	s.mu.Unlock()
	return s.waitReady(ctx, h, id)
}

func classifyStartError(err error, port int) error {
	msg := err.Error()
	if strings.Contains(msg, "port is already allocated") || strings.Contains(msg, "address already in use") || strings.Contains(msg, "bind") {
		return &apiError{Msg: fmt.Sprintf("Port %d is already in use by another program, so the server cannot accept players.", port),
			Hint: fmt.Sprintf("Stop the program using port %d (see: sudo ss -ltnp 'sport = :%d'), then press Start.", port, port)}
	}
	return &apiError{Msg: "Docker could not start the server: " + msg, Hint: "Check the Console and `sudo journalctl -u playkeeper-agent` for details."}
}

func (s *server) waitReady(ctx context.Context, h *opHandle, id string) error {
	deadline := s.now().Add(s.opts.ReadyTimeout)
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	reported := ""
	for {
		s.mu.Lock()
		phase, lastErr, hint := s.runPhase, s.lastError, s.lastErrorHint
		s.mu.Unlock()
		if phase == api.PhaseOnline {
			h.phase(string(api.PhaseOnline))
			return nil
		}
		if string(phase) != reported && phase != "" {
			reported = string(phase)
			h.phase(reported)
		}
		c, err := s.docker.ContainerInspect(ctx, id)
		if err == nil && !c.State.Running {
			// This start reports the exit; the reconcile loop must not count
			// it a second time as a crash.
			if fin, ok := c.State.Finished(); ok {
				s.markExitHandled(id, fin)
			}
			msg := fmt.Sprintf("The server stopped while starting (exit code %d).", c.State.ExitCode)
			if lastErr != "" {
				msg += " " + lastErr
			}
			if c.State.OOMKilled {
				msg += " It ran out of memory."
				hint = "Choose a larger memory budget in Settings."
			}
			if hint == "" {
				hint = "Open the Console to see the last lines the server printed."
			}
			return &apiError{Msg: msg, Hint: hint}
		}
		if s.now().After(deadline) {
			return &apiError{Msg: fmt.Sprintf("The server did not finish starting within %s.", s.opts.ReadyTimeout), Hint: "Open the Console to see what it is doing; it may still finish."}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// stopContainer saves the world and stops the container gracefully.
func (s *server) stopContainer(ctx context.Context, h *opHandle, id string) error {
	h.phase(string(api.PhaseStopping))
	s.mu.Lock()
	s.intentional[id] = true
	s.runPhase = api.PhaseStopping
	s.mu.Unlock()
	if out, err := s.rconCommand("save-all flush"); err != nil {
		s.log.Warn("save-all before stop failed", "server", s.id, "err", err)
	} else {
		s.log.Info("world saved before stop", "server", s.id, "reply", out)
	}
	if err := s.docker.ContainerStop(ctx, id, s.opts.StopTimeout); err != nil && !docker.IsNotFound(err) {
		return s.dockerErr(err)
	}
	s.resetRCON()
	return nil
}

// stopServer is idempotent: a missing or stopped container is a no-op.
func (s *server) stopServer(ctx context.Context, h *opHandle) error {
	c, err := s.docker.ContainerInspect(ctx, s.containerName())
	if docker.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return s.dockerErr(err)
	}
	if !c.State.Running {
		return nil
	}
	return s.stopContainer(ctx, h, c.ID)
}

func (s *server) containerRunning(ctx context.Context) (docker.ContainerJSON, bool, error) {
	c, err := s.docker.ContainerInspect(ctx, s.containerName())
	if docker.IsNotFound(err) {
		return c, false, nil
	}
	if err != nil {
		return c, false, s.dockerErr(err)
	}
	return c, c.State.Running, nil
}

func (s *server) setRunPhase(p api.Phase, detail string) {
	s.mu.Lock()
	s.runPhase, s.runPhaseDetail = p, detail
	s.mu.Unlock()
}

func (s *server) resetRun(p api.Phase) {
	s.mu.Lock()
	s.runPhase = p
	s.runPhaseDetail = ""
	s.sawStopping = false
	s.lastError, s.lastErrorHint = "", ""
	s.mu.Unlock()
}

// reconcileLoop converges observed state toward the desired state: it
// restarts the server after a host reboot or Docker restart, and applies the
// crash policy after unexpected exits.
func (s *server) reconcileLoop(ctx context.Context) {
	t := time.NewTicker(s.opts.ReconcileInterval)
	defer t.Stop()
	for {
		s.reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

const (
	crashWindow   = 15 * time.Minute
	maxCrashes    = 3
	followerGrace = 20 * time.Second
)

func (s *server) reconcile(ctx context.Context) {
	if s.busy() {
		return
	}
	sc, err := s.serverConfig()
	if err != nil || sc == nil {
		return
	}
	desired := s.desired()
	c, err := s.docker.ContainerInspect(ctx, s.containerName())
	if err != nil {
		if docker.IsNotFound(err) {
			s.resumeSaving(ctx, c, false)
		}
		s.mu.Lock()
		due := len(s.crashes) < maxCrashes && s.now().After(s.nextAutoRestart)
		s.mu.Unlock()
		if docker.IsNotFound(err) && desired == api.DesiredRunning && due {
			s.autoStart("recover")
		}
		return
	}
	s.resumeSaving(ctx, c, c.State.Running)
	if c.State.Running {
		return
	}
	// A container that never started has the zero finish time.
	fin, _ := c.State.Finished()
	s.mu.Lock()
	last, ok := s.handledExit[c.ID]
	handled := ok && last.Equal(fin)
	ended := s.followEnded[c.ID]
	intentional := s.intentional[c.ID]
	graceful := s.sawStopping
	s.mu.Unlock()
	if handled {
		s.mu.Lock()
		gaveUp := len(s.crashes) >= maxCrashes
		due := s.crashed && !gaveUp && s.now().After(s.nextAutoRestart)
		s.mu.Unlock()
		if due && desired == api.DesiredRunning {
			s.autoStart("auto-restart")
		}
		return
	}
	// Decide only once the follower has read this container's log to its end,
	// however long ago it exited, so the joins and leaves it logged are in
	// before open sessions are closed. If the log cannot be read, decide
	// anyway followerGrace after first seeing the exit.
	if ended.Before(fin) {
		s.mu.Lock()
		seen := s.exitSeen[c.ID]
		if !seen.fin.Equal(fin) {
			seen = seenExit{fin: fin, at: s.now()}
			s.exitSeen[c.ID] = seen
		}
		s.mu.Unlock()
		if s.now().Sub(seen.at) < followerGrace {
			return
		}
	}
	s.markExitHandled(c.ID, fin)
	switch {
	case fin.Before(s.started):
		// The server stopped while the agent was not running (a host reboot or
		// an agent restart), or its container never started. How it stopped is
		// unknown, so it is not counted as a crash: sessions still open end at
		// the exit, or now if there was none, uncertain, and a server that
		// should be running is brought back. The restart policy lives in memory
		// and starts over with each agent process, so a server it gave up on is
		// tried again.
		end := fin
		if end.IsZero() {
			end = s.now()
		}
		s.closeOpenSessions(end, "server_stopped", true)
		s.mu.Lock()
		due := len(s.crashes) < maxCrashes && s.now().After(s.nextAutoRestart)
		s.mu.Unlock()
		if desired == api.DesiredRunning && due {
			s.autoStart("recover")
		}
	case intentional:
		s.closeOpenSessions(fin, "server_stopped", false)
	case graceful:
		s.closeOpenSessions(fin, "server_stopped", false)
		s.recordEvent(fin, "server_stopped_externally", "", "docker", fmt.Sprintf("exit code %d", c.State.ExitCode))
		if desired == api.DesiredRunning {
			s.autoStart("recover")
		}
	default:
		s.closeOpenSessions(fin, "server_crashed", true)
		s.recordCrash(fin, c.State)
		if desired == api.DesiredRunning {
			s.mu.Lock()
			due := len(s.crashes) < maxCrashes && s.now().After(s.nextAutoRestart)
			s.mu.Unlock()
			if due {
				s.autoStart("auto-restart")
			}
		}
	}
}

type seenExit struct{ fin, at time.Time }

// markExitHandled records that a container exit has been dealt with, so the
// reconcile loop does not count it again.
func (s *server) markExitHandled(id string, fin time.Time) {
	s.mu.Lock()
	s.handledExit[id] = fin
	s.mu.Unlock()
}

func (s *server) recordCrash(fin time.Time, st docker.ContainerState) {
	s.mu.Lock()
	var recent []time.Time
	for _, t := range s.crashes {
		if fin.Sub(t) < crashWindow {
			recent = append(recent, t)
		}
	}
	s.crashes = append(recent, fin)
	n := len(s.crashes)
	s.crashed = true
	s.runPhase = api.PhaseCrashed
	if st.OOMKilled {
		s.lastError = "The server ran out of memory and was killed."
		s.lastErrorHint = "Choose a larger memory budget in Settings, then start the server."
	} else {
		s.lastError = fmt.Sprintf("The server stopped unexpectedly (exit code %d) without shutting down cleanly.", st.ExitCode)
		s.lastErrorHint = "Check the Console for the last lines before the crash."
	}
	if n >= maxCrashes {
		s.lastError += fmt.Sprintf(" Playkeeper stopped restarting it after %d crashes in %d minutes.", n, int(crashWindow.Minutes()))
		s.lastErrorHint += " Fix the cause, then press Start."
	} else {
		s.nextAutoRestart = s.now().Add(s.opts.CrashBackoff[min(n-1, len(s.opts.CrashBackoff)-1)])
	}
	detail := s.lastError
	oom := st.OOMKilled
	s.mu.Unlock()
	kind := "exit"
	if oom {
		kind = "oom"
	}
	s.recordEvent(fin, "server_crashed", "", "docker", detail)
	s.log.Warn("server crashed", "server", s.id, "exit", st.ExitCode, "cause", kind, "crashes", n)
}

func (s *server) autoStart(kind string) {
	_, err := s.beginOp(kind, "playkeeper", func(ctx context.Context, h *opHandle) error {
		sc, err := s.serverConfig()
		if err != nil || sc == nil {
			return errNotCreated()
		}
		if err := s.startServer(ctx, h, *sc); err != nil {
			s.autoStartFailed(err)
			return err
		}
		return nil
	})
	if err == nil {
		s.log.Info("automatic start", "server", s.id, "kind", kind)
	}
}

// autoStartFailed counts a failed automatic start like a crash, so a lasting
// problem (a busy port, an unreachable registry) gets the same backoff and is
// given up after maxCrashes attempts instead of being retried every tick.
func (s *server) autoStartFailed(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var recent []time.Time
	for _, t := range s.crashes {
		if now.Sub(t) < crashWindow {
			recent = append(recent, t)
		}
	}
	s.crashes = append(recent, now)
	// The reconcile loop retries after the backoff whether the failed
	// container was removed or is still there with its exit already counted.
	s.crashed = true
	n := len(s.crashes)
	if n >= maxCrashes {
		s.lastError = fmt.Sprintf("Playkeeper stopped trying to start the server after %d failed attempts in %d minutes: %s", n, int(crashWindow.Minutes()), err.Error())
		s.lastErrorHint = "Fix the cause, then press Start."
		return
	}
	s.nextAutoRestart = now.Add(s.opts.CrashBackoff[min(n-1, len(s.opts.CrashBackoff)-1)])
}

// startFailed is called when a start the user asked for did not bring the
// server up. The error's hint tells them to fix the cause and press Start, so
// nothing retries in the background; a container that is still running (a
// slow start that timed out) keeps the desired state running.
func (s *server) startFailed(ctx context.Context) {
	if _, running, err := s.containerRunning(ctx); err == nil && !running {
		_ = s.setDesired(api.DesiredStopped)
	}
}
