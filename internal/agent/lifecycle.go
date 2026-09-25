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
	a  *Agent
	op *api.Operation
}

func (h *opHandle) phase(p string) {
	h.a.opMu.Lock()
	h.op.Phase = p
	snap := *h.op
	h.a.opMu.Unlock()
	h.a.saveOperation(&snap)
}

func (h *opHandle) set(key string, v any) {
	h.a.opMu.Lock()
	h.op.Detail[key] = v
	snap := *h.op
	h.a.opMu.Unlock()
	h.a.saveOperation(&snap)
}

func (a *Agent) currentOp() *api.Operation {
	a.opMu.Lock()
	defer a.opMu.Unlock()
	if a.op == nil {
		return nil
	}
	c := *a.op
	c.Detail = map[string]any{}
	for k, v := range a.op.Detail {
		c.Detail[k] = v
	}
	return &c
}

func (a *Agent) busy() bool { return a.currentOp() != nil }

var opLabels = map[string]string{
	"create": "creating the server", "start": "starting the server", "stop": "stopping the server",
	"restart": "restarting the server", "backup": "a backup", "restore": "a restore", "recover": "an automatic restart",
	"auto-restart": "an automatic restart after a crash", "delete-backup": "deleting a backup",
}

func (a *Agent) busyError() error {
	cur := a.currentOp()
	what := "another operation"
	if cur != nil {
		what = opLabels[cur.Kind]
	}
	return &apiError{Status: http.StatusConflict, Code: api.CodeBusy, Msg: "Playkeeper is busy with " + what + ".", Hint: "Wait for it to finish, then try again.", Op: cur}
}

// holdOpLock takes the operation lock for a short decision, such as a start
// or stop that turns out to be a no-op, so it cannot interleave with an
// operation that changes the desired state.
func (a *Agent) holdOpLock() (release func(), ok bool) {
	select {
	case a.opLock <- struct{}{}:
		return func() { <-a.opLock }, true
	default:
		return nil, false
	}
}

// beginOp runs fn as the single exclusive operation. Concurrent requests get
// 409 with the operation that is in progress.
func (a *Agent) beginOp(kind, actor string, fn func(ctx context.Context, h *opHandle) error) (*api.Operation, error) {
	select {
	case a.opLock <- struct{}{}:
	default:
		return nil, a.busyError()
	}
	op := &api.Operation{ID: newID(), Kind: kind, Status: api.OpRunning, Actor: actor, StartedAt: a.now().UTC(), Detail: map[string]any{}}
	a.opMu.Lock()
	a.op = op
	snap := *op
	a.opMu.Unlock()
	a.saveOperation(&snap)
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() { <-a.opLock }()
		ctx, cancel := context.WithTimeout(a.ctx, 45*time.Minute)
		defer cancel()
		h := &opHandle{a: a, op: op}
		err := func() (err error) {
			defer func() {
				if v := recover(); v != nil {
					err = fmt.Errorf("internal error: %v", v)
				}
			}()
			return fn(ctx, h)
		}()
		a.opMu.Lock()
		fin := a.now().UTC()
		op.FinishedAt = &fin
		if err != nil {
			op.Status = api.OpFailed
			op.Error = err.Error()
			var ae *apiError
			if errors.As(err, &ae) {
				op.Hint = ae.Hint
			}
		} else {
			op.Status = api.OpSucceeded
		}
		done := *op
		a.op = nil
		a.opMu.Unlock()
		a.saveOperation(&done)
		a.audit(actor, kind, "server", done.Status, done.Error)
		if err != nil {
			a.log.Warn("operation failed", "kind", kind, "err", err)
		}
	}()
	c := snap
	return &c, nil
}

func (a *Agent) offline() bool { return a.opts.OfflineModeTest }

func (a *Agent) serverDir() string       { return filepath.Dir(a.cfg.ServerDataDir()) }
func (a *Agent) containerSecret() string { return filepath.Join(a.serverDir(), "rcon_password") }

func (a *Agent) levelName(sc api.ServerConfig) string {
	if sc.LevelName == "" {
		return "world"
	}
	return sc.LevelName
}

// containerSpec is the complete, hardened definition of the one Minecraft
// container. Its hash is stored as a label so any drift forces a recreate.
func (a *Agent) containerSpec(sc api.ServerConfig, setupOnly bool) (docker.ContainerConfig, string) {
	online := "TRUE"
	if a.offline() {
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
		env = append(env, "TYPE=CUSTOM", "CUSTOM_SERVER=/data/"+filepath.Base(a.jarPath(sc)))
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
		"LEVEL="+a.levelName(sc),
		"SERVER_PORT=25565",
		"TZ=UTC",
		"USE_AIKAR_FLAGS=TRUE",
	)
	limit := int64(sc.MemoryMB) << 20
	pids := int64(2048)
	stop := int(a.opts.StopTimeout.Seconds())
	cfg := docker.ContainerConfig{
		Image:       minecraft.Image,
		Env:         env,
		User:        fmt.Sprintf("%d:%d", a.cfg.GameUID, a.cfg.GameGID),
		StopSignal:  "SIGTERM",
		StopTimeout: &stop,
		Labels:      map[string]string{labelManaged: "true", labelInstall: a.cfg.InstallID},
		HostConfig: docker.HostConfig{
			Binds:         []string{a.cfg.ServerDataDir() + ":/data", a.containerSecret() + ":/run/secrets/rcon_password:ro"},
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
		cfg.HostConfig.PortBindings = map[string][]docker.PortBinding{"25565/tcp": {{HostPort: strconv.Itoa(a.cfg.GamePort)}}}
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

func (a *Agent) ensureDirs() error {
	data := a.cfg.ServerDataDir()
	if err := os.MkdirAll(data, 0o750); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(data), 0o755); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(data, a.cfg.GameUID, a.cfg.GameGID); err != nil {
			return err
		}
	}
	return a.ensureRCONSecret()
}

// ensureRCONSecret creates the host-generated RCON password: a root-only copy
// for the agent and a read-only copy the game user mounts into the container.
func (a *Agent) ensureRCONSecret() error {
	path := a.cfg.RCONSecretPath()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		b = []byte(randomSecret(24))
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	cpath := a.containerSecret()
	if cur, err := os.ReadFile(cpath); err != nil || string(cur) != string(b) {
		os.Remove(cpath)
		if err := os.WriteFile(cpath, b, 0o400); err != nil {
			return err
		}
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(cpath, a.cfg.GameUID, a.cfg.GameGID); err != nil {
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
func (a *Agent) ensureTelemetryOff() error {
	dir := filepath.Join(a.cfg.ServerDataDir(), "plugins", "bStats")
	path := filepath.Join(dir, "config.yml")
	if b, err := os.ReadFile(path); err == nil && bStatsOff(b) {
		return nil
	}
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
		if os.Geteuid() == 0 {
			if err := os.Chown(d, a.cfg.GameUID, a.cfg.GameGID); err != nil {
				return err
			}
		}
	}
	if err := os.WriteFile(path, []byte(bStatsConfig), 0o640); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		return os.Chown(path, a.cfg.GameUID, a.cfg.GameGID)
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

func (a *Agent) rconPassword() (string, error) {
	b, err := os.ReadFile(a.cfg.RCONSecretPath())
	return strings.TrimSpace(string(b)), err
}

func (a *Agent) dockerErr(err error) error {
	return &apiError{Status: http.StatusServiceUnavailable, Code: api.CodeDockerUnavailable, Msg: "Docker is not responding: " + err.Error(), Hint: "Check that the Docker service is running (sudo systemctl status docker)."}
}

func (a *Agent) jarPath(sc api.ServerConfig) string {
	return filepath.Join(a.cfg.ServerDataDir(), fmt.Sprintf("paper-%s-%d.jar", sc.MinecraftVersion, sc.PaperBuild))
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
// server does not run) and verifies the jar against the pinned Fill v3
// checksum before the server is ever started with it.
func (a *Agent) ensureServerSoftware(ctx context.Context, h *opHandle, sc *api.ServerConfig) error {
	if _, err := minecraft.LookupVersion(sc.VersionID); err != nil {
		return err
	}
	want := a.opts.JarSHA256(sc.VersionID)
	jar := a.jarPath(*sc)
	if sum, err := fileSHA256(jar); err == nil && sum == want {
		return nil
	}
	h.phase(string(api.PhaseDownloading))
	a.setRunPhase(api.PhaseDownloading, "")
	setupName := containerName + "-setup"
	_ = a.docker.ContainerRemove(ctx, setupName, true)
	spec, _ := a.containerSpec(*sc, true)
	id, err := a.docker.ContainerCreate(ctx, setupName, spec)
	if err != nil {
		return a.dockerErr(err)
	}
	defer a.docker.ContainerRemove(context.Background(), id, true)
	if err := a.docker.ContainerStart(ctx, id); err != nil {
		return a.dockerErr(err)
	}
	var tail []string
	logs, err := a.docker.ContainerLogs(ctx, id, docker.LogsOptions{Follow: true})
	if err == nil {
		for {
			l, err := logs.Next()
			if err != nil {
				break
			}
			text := minecraft.CleanLine(l.Text)
			a.console.append(l.TS, text)
			tail = append(tail, text)
			if len(tail) > 8 {
				tail = tail[1:]
			}
		}
		logs.Close()
	}
	var c docker.ContainerJSON
	for i := 0; i < 60; i++ {
		c, err = a.docker.ContainerInspect(ctx, id)
		if err != nil || !c.State.Running {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return a.dockerErr(err)
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
	now := a.now().UTC()
	sc.JarVerifiedAt = &now
	if err := a.saveServerConfig(*sc); err != nil {
		return err
	}
	a.recordEvent(now, "server_software_verified", "", "playkeeper", filepath.Base(jar)+" sha256 "+sum)
	a.log.Info("server software verified", "jar", filepath.Base(jar), "sha256", sum)
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
func (a *Agent) startServer(ctx context.Context, h *opHandle, sc api.ServerConfig) error {
	if err := a.ensureDirs(); err != nil {
		return err
	}
	if err := a.ensureImage(ctx, h); err != nil {
		return err
	}
	if err := a.ensureNetwork(ctx); err != nil {
		return err
	}
	if err := a.ensureServerSoftware(ctx, h, &sc); err != nil {
		return err
	}
	if err := a.ensureTelemetryOff(); err != nil {
		return err
	}
	spec, hash := a.containerSpec(sc, false)
	c, err := a.docker.ContainerInspect(ctx, containerName)
	switch {
	case err == nil && c.Config.Labels[labelManaged] != "true":
		return &apiError{Msg: "A container named " + containerName + " exists but was not created by Playkeeper.", Hint: "Playkeeper will not touch it. Rename or remove that container, then press Start."}
	case err == nil && c.State.Running && c.Config.Labels[labelSpec] == hash:
		return nil
	case err == nil && c.State.Running:
		if err := a.stopContainer(ctx, h, c.ID); err != nil {
			return err
		}
		fallthrough
	case err == nil && c.Config.Labels[labelSpec] != hash:
		if err := a.docker.ContainerRemove(ctx, c.ID, true); err != nil && !docker.IsNotFound(err) {
			return a.dockerErr(err)
		}
		c.ID = ""
	case err != nil && !docker.IsNotFound(err):
		return a.dockerErr(err)
	}
	id := c.ID
	if id == "" || docker.IsNotFound(err) {
		id, err = a.docker.ContainerCreate(ctx, containerName, spec)
		if err != nil {
			return a.dockerErr(err)
		}
	}
	h.phase(string(api.PhaseStartingContainer))
	a.resetRun(api.PhaseStartingContainer)
	a.resetRCON()
	if err := a.docker.ContainerStart(ctx, id); err != nil {
		// A container whose start failed (for example on a busy port) can keep
		// broken network state; discard it so the next start creates it fresh.
		_ = a.docker.ContainerRemove(context.Background(), id, true)
		return classifyStartError(err, a.cfg.GamePort)
	}
	a.mu.Lock()
	delete(a.intentional, id)
	a.mu.Unlock()
	return a.waitReady(ctx, h, id)
}

func classifyStartError(err error, port int) error {
	msg := err.Error()
	if strings.Contains(msg, "port is already allocated") || strings.Contains(msg, "address already in use") || strings.Contains(msg, "bind") {
		return &apiError{Msg: fmt.Sprintf("Port %d is already in use by another program, so the server cannot accept players.", port),
			Hint: fmt.Sprintf("Stop the program using port %d (see: sudo ss -ltnp 'sport = :%d'), then press Start.", port, port)}
	}
	return &apiError{Msg: "Docker could not start the server: " + msg, Hint: "Check the Console and `sudo journalctl -u playkeeper-agent` for details."}
}

func (a *Agent) waitReady(ctx context.Context, h *opHandle, id string) error {
	deadline := a.now().Add(a.opts.ReadyTimeout)
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	reported := ""
	for {
		a.mu.Lock()
		phase, lastErr, hint := a.runPhase, a.lastError, a.lastErrorHint
		a.mu.Unlock()
		if phase == api.PhaseOnline {
			h.phase(string(api.PhaseOnline))
			return nil
		}
		if string(phase) != reported && phase != "" {
			reported = string(phase)
			h.phase(reported)
		}
		c, err := a.docker.ContainerInspect(ctx, id)
		if err == nil && !c.State.Running {
			// This start reports the exit; the reconcile loop must not count
			// it a second time as a crash.
			if fin, ok := c.State.Finished(); ok {
				a.markExitHandled(id, fin)
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
		if a.now().After(deadline) {
			return &apiError{Msg: fmt.Sprintf("The server did not finish starting within %s.", a.opts.ReadyTimeout), Hint: "Open the Console to see what it is doing; it may still finish."}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// stopContainer saves the world and stops the container gracefully.
func (a *Agent) stopContainer(ctx context.Context, h *opHandle, id string) error {
	h.phase(string(api.PhaseStopping))
	a.mu.Lock()
	a.intentional[id] = true
	a.runPhase = api.PhaseStopping
	a.mu.Unlock()
	if out, err := a.rconCommand("save-all flush"); err != nil {
		a.log.Warn("save-all before stop failed", "err", err)
	} else {
		a.log.Info("world saved before stop", "reply", out)
	}
	if err := a.docker.ContainerStop(ctx, id, a.opts.StopTimeout); err != nil && !docker.IsNotFound(err) {
		return a.dockerErr(err)
	}
	a.resetRCON()
	return nil
}

// stopServer is idempotent: a missing or stopped container is a no-op.
func (a *Agent) stopServer(ctx context.Context, h *opHandle) error {
	c, err := a.docker.ContainerInspect(ctx, containerName)
	if docker.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return a.dockerErr(err)
	}
	if !c.State.Running {
		return nil
	}
	return a.stopContainer(ctx, h, c.ID)
}

func (a *Agent) containerRunning(ctx context.Context) (docker.ContainerJSON, bool, error) {
	c, err := a.docker.ContainerInspect(ctx, containerName)
	if docker.IsNotFound(err) {
		return c, false, nil
	}
	if err != nil {
		return c, false, a.dockerErr(err)
	}
	return c, c.State.Running, nil
}

func (a *Agent) setRunPhase(p api.Phase, detail string) {
	a.mu.Lock()
	a.runPhase, a.runPhaseDetail = p, detail
	a.mu.Unlock()
}

func (a *Agent) resetRun(p api.Phase) {
	a.mu.Lock()
	a.runPhase = p
	a.runPhaseDetail = ""
	a.sawStopping = false
	a.lastError, a.lastErrorHint = "", ""
	a.mu.Unlock()
}

// reconcileLoop converges observed state toward the desired state: it
// restarts the server after a host reboot or Docker restart, and applies the
// crash policy after unexpected exits.
func (a *Agent) reconcileLoop(ctx context.Context) {
	t := time.NewTicker(a.opts.ReconcileInterval)
	defer t.Stop()
	for {
		a.reconcile(ctx)
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

func (a *Agent) reconcile(ctx context.Context) {
	if a.busy() {
		return
	}
	sc, err := a.serverConfig()
	if err != nil || sc == nil {
		return
	}
	desired := a.desired()
	c, err := a.docker.ContainerInspect(ctx, containerName)
	if err != nil {
		a.mu.Lock()
		due := len(a.crashes) < maxCrashes && a.now().After(a.nextAutoRestart)
		a.mu.Unlock()
		if docker.IsNotFound(err) && desired == api.DesiredRunning && due {
			a.autoStart("recover")
		}
		return
	}
	if c.State.Running {
		return
	}
	// A container that never started has the zero finish time.
	fin, _ := c.State.Finished()
	a.mu.Lock()
	last, ok := a.handledExit[c.ID]
	handled := ok && last.Equal(fin)
	ended := a.followEnded[c.ID]
	intentional := a.intentional[c.ID]
	graceful := a.sawStopping
	a.mu.Unlock()
	if handled {
		a.mu.Lock()
		gaveUp := len(a.crashes) >= maxCrashes
		due := a.crashed && !gaveUp && a.now().After(a.nextAutoRestart)
		a.mu.Unlock()
		if due && desired == api.DesiredRunning {
			a.autoStart("auto-restart")
		}
		return
	}
	// Decide only once the follower has read this container's log to its end,
	// however long ago it exited, so the joins and leaves it logged are in
	// before open sessions are closed. If the log cannot be read, decide
	// anyway followerGrace after first seeing the exit.
	if ended.Before(fin) {
		a.mu.Lock()
		seen := a.exitSeen[c.ID]
		if !seen.fin.Equal(fin) {
			seen = seenExit{fin: fin, at: a.now()}
			a.exitSeen[c.ID] = seen
		}
		a.mu.Unlock()
		if a.now().Sub(seen.at) < followerGrace {
			return
		}
	}
	a.markExitHandled(c.ID, fin)
	switch {
	case fin.Before(a.started):
		// The server stopped while the agent was not running (a host reboot or
		// an agent restart), or its container never started. How it stopped is
		// unknown, so it is not counted as a crash: sessions still open end
		// then, uncertain, and a server that should be running is brought back.
		// The restart policy lives in memory and starts over with each agent
		// process, so a server it gave up on is tried again.
		a.closeOpenSessions(fin, "server_stopped", true)
		a.mu.Lock()
		due := len(a.crashes) < maxCrashes && a.now().After(a.nextAutoRestart)
		a.mu.Unlock()
		if desired == api.DesiredRunning && due {
			a.autoStart("recover")
		}
	case intentional:
		a.closeOpenSessions(fin, "server_stopped", false)
	case graceful:
		a.closeOpenSessions(fin, "server_stopped", false)
		a.recordEvent(fin, "server_stopped_externally", "", "docker", fmt.Sprintf("exit code %d", c.State.ExitCode))
		if desired == api.DesiredRunning {
			a.autoStart("recover")
		}
	default:
		a.closeOpenSessions(fin, "server_crashed", true)
		a.recordCrash(fin, c.State)
		if desired == api.DesiredRunning {
			a.mu.Lock()
			due := len(a.crashes) < maxCrashes && a.now().After(a.nextAutoRestart)
			a.mu.Unlock()
			if due {
				a.autoStart("auto-restart")
			}
		}
	}
}

type seenExit struct{ fin, at time.Time }

// markExitHandled records that a container exit has been dealt with, so the
// reconcile loop does not count it again.
func (a *Agent) markExitHandled(id string, fin time.Time) {
	a.mu.Lock()
	a.handledExit[id] = fin
	a.mu.Unlock()
}

func (a *Agent) recordCrash(fin time.Time, st docker.ContainerState) {
	a.mu.Lock()
	var recent []time.Time
	for _, t := range a.crashes {
		if fin.Sub(t) < crashWindow {
			recent = append(recent, t)
		}
	}
	a.crashes = append(recent, fin)
	n := len(a.crashes)
	a.crashed = true
	a.runPhase = api.PhaseCrashed
	if st.OOMKilled {
		a.lastError = "The server ran out of memory and was killed."
		a.lastErrorHint = "Choose a larger memory budget in Settings, then start the server."
	} else {
		a.lastError = fmt.Sprintf("The server stopped unexpectedly (exit code %d) without shutting down cleanly.", st.ExitCode)
		a.lastErrorHint = "Check the Console for the last lines before the crash."
	}
	if n >= maxCrashes {
		a.lastError += fmt.Sprintf(" Playkeeper stopped restarting it after %d crashes in %d minutes.", n, int(crashWindow.Minutes()))
		a.lastErrorHint += " Fix the cause, then press Start."
	} else {
		a.nextAutoRestart = a.now().Add(a.opts.CrashBackoff[min(n-1, len(a.opts.CrashBackoff)-1)])
	}
	detail := a.lastError
	a.mu.Unlock()
	a.recordEvent(fin, "server_crashed", "", "docker", detail)
	a.log.Warn("server crashed", "exit", st.ExitCode, "oom", st.OOMKilled, "crashes", n)
}

func (a *Agent) autoStart(kind string) {
	_, err := a.beginOp(kind, "playkeeper", func(ctx context.Context, h *opHandle) error {
		sc, err := a.serverConfig()
		if err != nil || sc == nil {
			return errNotCreated()
		}
		if err := a.startServer(ctx, h, *sc); err != nil {
			a.autoStartFailed(err)
			return err
		}
		return nil
	})
	if err == nil {
		a.log.Info("automatic start", "kind", kind)
	}
}

// autoStartFailed counts a failed automatic start like a crash, so a lasting
// problem (a busy port, an unreachable registry) gets the same backoff and is
// given up after maxCrashes attempts instead of being retried every tick.
func (a *Agent) autoStartFailed(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	var recent []time.Time
	for _, t := range a.crashes {
		if now.Sub(t) < crashWindow {
			recent = append(recent, t)
		}
	}
	a.crashes = append(recent, now)
	// The reconcile loop retries after the backoff whether the failed
	// container was removed or is still there with its exit already counted.
	a.crashed = true
	n := len(a.crashes)
	if n >= maxCrashes {
		a.lastError = fmt.Sprintf("Playkeeper stopped trying to start the server after %d failed attempts in %d minutes: %s", n, int(crashWindow.Minutes()), err.Error())
		a.lastErrorHint = "Fix the cause, then press Start."
		return
	}
	a.nextAutoRestart = now.Add(a.opts.CrashBackoff[min(n-1, len(a.opts.CrashBackoff)-1)])
}

// startFailed is called when a start the user asked for did not bring the
// server up. The error's hint tells them to fix the cause and press Start, so
// nothing retries in the background; a container that is still running (a
// slow start that timed out) keeps the desired state running.
func (a *Agent) startFailed(ctx context.Context) {
	if _, running, err := a.containerRunning(ctx); err == nil && !running {
		_ = a.setDesired(api.DesiredStopped)
	}
}
