package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/discord"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// opHandle lets a running operation publish its phase.
type opHandle struct {
	save func(op *api.Operation)
	mu   func() func()
	op   *api.Operation
	// continues is set by an operation that goes on outside this agent
	// process (an update handed to the updater, or a restore the next agent
	// process finishes): it stays running until its result is recorded.
	continues bool
	// cancel ends the operation's context. cancellable is set by an operation
	// that can stop without changing anything, until it commits; cancelled
	// records that it was asked to. The caller holds mu for both.
	cancel      context.CancelFunc
	cancellable bool
	cancelled   bool
}

// allowCancel lets the operation be cancelled until it commits.
func (h *opHandle) allowCancel() {
	unlock := h.mu()
	h.cancellable = true
	unlock()
}

// commit ends the part of the operation that can be cancelled. It is false
// when a cancel came first; the operation then undoes what it did.
func (h *opHandle) commit() bool {
	unlock := h.mu()
	defer unlock()
	h.cancellable = false
	return !h.cancelled
}

// callOff ends the operation as cancelled: before changing anything, it
// found it had nothing to do.
func (h *opHandle) callOff() {
	unlock := h.mu()
	h.cancelled = true
	unlock()
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

func (h *opHandle) get(key string) any {
	unlock := h.mu()
	defer unlock()
	return h.op.Detail[key]
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
	"addon-install": "installing add-ons", "addon-update": "updating add-ons", "pregen-start": "starting map pre-generation",
	"address.publish": "publishing the address", "certificate.issue": "getting a certificate",
	"remove-addon": "removing a plugin or mod",
	// Wave 4.
	"reinstall": "reinstalling its server software", "template-retry": "installing its template's add-ons",
	// Wave 7 (0.4.0)
	"sleep": "falling asleep", "wake": "waking up", "disk-cleanup": "freeing disk space", "offsite-restore": "restoring a copy", "offsite-check": "checking a copy",
	"offsite-recover": "restoring a server from a recovery key",
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
	return s.launchOp(&api.Operation{ID: newID(), ServerID: s.id, Kind: kind, Status: api.OpRunning, Actor: actor, StartedAt: s.now().UTC(), Detail: map[string]any{}}, fn)
}

// opTimeout is how long an operation may run, but for those in noDeadline.
var opTimeout = 45 * time.Minute

// noDeadline are the operations a fixed deadline would cut short: a copy
// can take hours to download over a slow link. The download's stall timeout
// stops them when the copy stops coming, and a restore from a copy can be
// cancelled.
var noDeadline = map[string]bool{"offsite-restore": true, "offsite-recover": true}

// opContext is the context an operation of kind runs in.
func opContext(parent context.Context, kind string) (context.Context, context.CancelFunc) {
	if noDeadline[kind] {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, opTimeout)
}

// launchOp runs fn as op, a new operation or one a previous agent process
// left running, like startOp.
func (s *server) launchOp(op *api.Operation, fn func(ctx context.Context, h *opHandle) error) *api.Operation {
	if op.Detail == nil {
		op.Detail = map[string]any{}
	}
	kind := op.Kind
	ctx, cancel := opContext(s.ctx, kind)
	h := &opHandle{save: s.saveOperation, op: op, mu: func() func() { s.opMu.Lock(); return s.opMu.Unlock }, cancel: cancel}
	s.opMu.Lock()
	s.op, s.opH = op, h
	snap := copyOp(op)
	s.opMu.Unlock()
	s.saveOperation(snap)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.opLock }()
		defer cancel()
		err := runOp(ctx, h, fn)
		s.opMu.Lock()
		done := finishOp(op, h, err, s.now().UTC())
		s.op, s.opH = nil, nil
		s.opMu.Unlock()
		s.finishOperation(s.id, "server", &done)
		if kind == "backup" && done.Status == api.OpFailed {
			s.alert(discord.BackupFailed(done.Error))
		}
		if done.Status == api.OpFailed {
			s.log.Warn("operation failed", "server", s.id, "kind", kind, "err", err)
		}
	}()
	return snap
}

// cancelOp cancels the running operation id, of the given kind, while it can
// still stop without changing anything.
func (s *server) cancelOp(kind, id string) (*api.Operation, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.op == nil || s.op.ID != id || s.op.Kind != kind {
		return nil, errConflict("That isn't running any more.", "")
	}
	if !s.opH.cancellable {
		return nil, errConflict("It's too late to cancel: it's nearly done.", "Wait a moment for it to finish.")
	}
	s.opH.cancelled = true
	s.opH.cancel()
	return copyOp(s.op), nil
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
	case h.cancelled:
		op.FinishedAt = &fin
		op.Status = api.OpCancelled
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
//
// Only the setup-only container downloads Paper. The server container runs
// the jar that was verified against the pinned checksum, so a start never
// re-downloads Paper. Other types run the files their verified install
// recorded, the way its manifest says.
//
// current is the environment of the server's existing container, if any: a
// stored resource pack offer whose settings can't be built keeps the pack
// settings it has, so the server goes on offering what it did, the machine
// keeps serving that pack, and the Packs page says what's wrong.
func (s *server) containerSpec(sc api.ServerConfig, setupOnly bool, current []string) (docker.ContainerConfig, string) {
	var typeEnv []string
	switch {
	case sc.Software != nil:
		typeEnv = s.runEnv()
	case setupOnly:
		typeEnv = []string{"TYPE=PAPER", "PAPER_BUILD=" + strconv.Itoa(sc.PaperBuild), "SETUP_ONLY=TRUE"}
	default:
		typeEnv = []string{"TYPE=CUSTOM", "CUSTOM_SERVER=/data/" + filepath.Base(s.jarPath(sc))}
	}
	return s.specWith(sc, typeEnv, setupOnly, current)
}

// specWith is the container definition with typeEnv, the part of the env
// that depends on the server type. SKIP_DOWNLOAD_DEFAULTS stops the image
// fetching unpinned default config files from a third-party repository.
func (s *server) specWith(sc api.ServerConfig, typeEnv []string, setupOnly bool, current []string) (docker.ContainerConfig, string) {
	online := "TRUE"
	if s.offline() {
		online = "FALSE"
	}
	env := append([]string{"EULA=TRUE", "VERSION=" + sc.MinecraftVersion}, typeEnv...)
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
	pack, err := resourcePackEnv(s.currentOffer(sc.ResourcePack))
	if err != nil {
		pack = keptPackEnv(current)
	}
	env = append(env, pack...)
	limit := int64(sc.MemoryMB) << 20
	pids := int64(2048)
	stop := int(s.opts.StopTimeout.Seconds())
	cfg := docker.ContainerConfig{
		Image:       runtimeImage(sc.MinecraftVersion),
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
		// Voice chat's UDP port has the same number inside and out, as its
		// settings say (installVoiceChat).
		if p := sc.VoiceChatPort; p > 0 {
			voice := strconv.Itoa(p) + "/udp"
			cfg.ExposedPorts[voice] = struct{}{}
			cfg.HostConfig.PortBindings[voice] = []docker.PortBinding{{HostPort: strconv.Itoa(p)}}
		}
	}
	b, _ := json.Marshal(cfg)
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:8])
	cfg.Labels[labelSpec] = hash
	if !setupOnly {
		// The GC log stays out of the hash, so adding it never restarts a
		// running server; startServer recreates a stopped container without
		// it, so it applies from the server's next start.
		cfg.Env = append(cfg.Env, "JVM_OPTS="+gcLogFlag)
		cfg.Labels[labelGCLog] = gcLogVersion
	}
	return cfg, hash
}

// runtimeImage is the pinned image a server of Minecraft version mc runs
// in: the one with the Java that version was made for.
func runtimeImage(mc string) string {
	if img, _, ok := minecraft.ImageFor(minecraft.JavaFor(mc)); ok {
		return img
	}
	return minecraft.Image
}

func (a *Agent) ensureImage(ctx context.Context, h *opHandle, image string) error {
	if _, err := a.docker.ImageInspect(ctx, image); err == nil {
		return nil
	} else if !docker.IsNotFound(err) {
		return a.dockerErr(err)
	}
	h.phase(string(api.PhasePulling))
	var last time.Time
	err := a.docker.ImagePull(ctx, image, func(p docker.PullProgress) {
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
	// A server started without its world directory generates a new world, so
	// never recreate one a restore moved aside and could not put back.
	if _, err := os.Stat(data); errors.Is(err, os.ErrNotExist) {
		if prev := s.newestPreviousWorld(); prev != "" {
			return &apiError{Msg: "The world folder is missing because a restore did not finish; the previous world is at " + prev + ".", Hint: "Move that folder back to " + data + ", then press Start."}
		}
	}
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
	// Java refuses to start when its GC log's folder is missing.
	logs := filepath.Join(data, "logs")
	if err := os.Mkdir(logs, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if os.Geteuid() == 0 {
		if err := giveFolder(logs, s.cfg.GameUID, s.cfg.GameGID); err != nil {
			return err
		}
	}
	return s.ensureRCONSecret()
}

// giveFolder gives the game user the folder at path on every start, not only
// when Playkeeper makes it, so a chown that failed once doesn't keep Java from
// writing there. The game owns data/: the folder is changed through a handle
// opened without following a link or waiting on a pipe, and a link or anything
// but a folder at path is left alone.
func giveFolder(path string, uid, gid int) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Chown(uid, gid)
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
	d, err := s.gameFiles()
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.EnsureFile("plugins/bStats/config.yml", []byte(bStatsConfig), 0o640, bStatsOff); err != nil {
		return gameFileError(err, "Paper's bStats usage statistics could not be switched off, so the server was not started.")
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

// maxJarBytes is far above the size of any Paper jar (about 50 MB), so a
// larger file is not one and is not read.
const maxJarBytes = 256 << 20

func (s *server) jarSHA256(ctx context.Context, sc api.ServerConfig) (string, error) {
	d, err := s.gameFiles()
	if err != nil {
		return "", err
	}
	defer d.Close()
	return d.SHA256(ctx, filepath.Base(s.jarPath(sc)), maxJarBytes)
}

// ensureServerSoftware downloads Paper with a setup-only container (the
// server does not run) and verifies the jar against the checksum PaperMC's
// Fill v3 API published for the build, before the server is ever started
// with it. A verified jar that changed since is not replaced on its own: the
// server stays off until the user reinstalls.
func (s *server) ensureServerSoftware(ctx context.Context, h *opHandle, sc *api.ServerConfig) error {
	want, err := jarChecksum(*sc)
	if err != nil {
		return &apiError{Msg: "The server's software cannot be verified: " + err.Error() + ".", Hint: "Choose a version under Settings, or restore a backup."}
	}
	jar := s.jarPath(*sc)
	sum, err := s.jarSHA256(ctx, *sc)
	if gamefiles.KindOf(err) == gamefiles.KindTooLarge {
		// Too large to be the software Playkeeper installed, so it's a
		// different file, found without reading all of it.
		sum, err = "", nil
	}
	switch {
	case gamefiles.KindOf(err) != "":
		return gameFileError(err, "The server software could not be checked, so it was not run.")
	case err == nil && sum == want:
		s.clearSoftwareChanged()
		return nil
	case err == nil && sc.JarVerifiedAt != nil:
		var changed *time.Time
		if fi, err := os.Lstat(jar); err == nil {
			t := fi.ModTime().UTC()
			changed = &t
		}
		return s.softwareChangedError(&api.SoftwareChange{File: filepath.Base(jar), Algorithm: "sha256", Recorded: want, Found: sum,
			InstalledAt: sc.JarVerifiedAt, ChangedAt: changed, DetectedAt: s.now().UTC(), Software: softwareLabel(*sc)})
	case err == nil:
		// Left by a download that never finished: the image would keep it.
		if err := os.Remove(jar); err != nil {
			return err
		}
	}
	h.phase(string(api.PhaseDownloading))
	s.setRunPhase(api.PhaseDownloading, "")
	if sc.JarVerifiedAt != nil {
		sc.JarVerifiedAt = nil
		if err := s.saveServerConfig(*sc); err != nil {
			return err
		}
	}
	spec, _ := s.containerSpec(*sc, true, nil)
	tail, code, err := s.runSetupContainer(ctx, h, spec)
	if err != nil {
		return err
	}
	if code != 0 {
		return &apiError{Msg: "Downloading the Minecraft server software failed (exit code " + strconv.Itoa(code) + "): " + lastNonEmpty(tail),
			Hint: "Check that this host can reach fill.papermc.io and piston-data.mojang.com, then press Start again."}
	}
	h.phase("verifying_download")
	sum, err = s.jarSHA256(ctx, *sc)
	if gamefiles.KindOf(err) != "" {
		return gameFileError(err, "The server software could not be checked, so it was not run.")
	}
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
	s.clearSoftwareChanged()
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
func (s *server) startServer(ctx context.Context, h *opHandle, sc api.ServerConfig) (err error) {
	pastFiles := false
	defer func() { s.noteRefusal(err, pastFiles) }()
	if err := s.ensureDirs(); err != nil {
		return err
	}
	if err := s.ensureOriginalSaved(h, sc); err != nil {
		return err
	}
	if err := s.ensureImage(ctx, h, runtimeImage(sc.MinecraftVersion)); err != nil {
		return err
	}
	if err := s.ensureNetwork(ctx); err != nil {
		return err
	}
	if sc.Modpack != nil && sc.Modpack.Pending {
		if err := s.installPendingPack(ctx, h, &sc); err != nil {
			return err
		}
	}
	if err := s.ensureSoftware(ctx, h, &sc); err != nil {
		return err
	}
	if sc.Template != nil && sc.Template.Pending {
		if err := s.installPendingTemplate(ctx, h, &sc); err != nil {
			return err
		}
	}
	if takesPlugins(sc) {
		if err := s.ensureTelemetryOff(); err != nil {
			return err
		}
	}
	if err := s.writeMapConfig(); err != nil {
		return err
	}
	pastFiles = true
	name := s.containerName()
	c, err := s.docker.ContainerInspect(ctx, name)
	spec, hash := s.containerSpec(sc, false, c.Config.Env)
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
	case err == nil && (c.Config.Labels[labelSpec] != hash || c.Config.Labels[labelGCLog] != gcLogVersion):
		if err := s.docker.ContainerRemove(ctx, c.ID, true); err != nil && !docker.IsNotFound(err) {
			return s.dockerErr(err)
		}
		c.ID = ""
	case err != nil && !docker.IsNotFound(err):
		return s.dockerErr(err)
	}
	id := c.ID
	if id == "" || docker.IsNotFound(err) {
		// A modpack can move the server to another Minecraft version, and so
		// to another Java, after the image was pulled above.
		if err := s.ensureImage(ctx, h, spec.Image); err != nil {
			return err
		}
		id, err = s.docker.ContainerCreate(ctx, name, spec)
		if err != nil {
			return s.dockerErr(err)
		}
	}
	s.leaveSleep()
	h.phase(string(api.PhaseStartingContainer))
	s.resetRun(api.PhaseStartingContainer)
	s.resetRCON()
	if err := s.docker.ContainerStart(ctx, id); err != nil {
		// A container whose start failed (for example on a busy port) can keep
		// broken network state; discard it so the next start creates it fresh.
		_ = s.docker.ContainerRemove(context.Background(), id, true)
		s.explainCrash("", docker.ContainerState{}, true, err)
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
	t := time.NewTicker(s.opts.ReadyPoll)
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
			s.explainCrash(id, c.State, true, nil)
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

// waitOnline waits until the server is online. startServer returns at once
// for a container that was already running, which after an agent restart may
// still be starting.
func (s *server) waitOnline(ctx context.Context, h *opHandle) error {
	c, err := s.docker.ContainerInspect(ctx, s.containerName())
	if err != nil {
		return s.dockerErr(err)
	}
	return s.waitReady(ctx, h, c.ID)
}

// stopping is true once the agent is shutting down: an operation that fails
// then may have failed only because it was cut short.
func (s *server) stopping() bool { return s.ctx.Err() != nil }

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
	// Said here rather than when the reconcile loop sees the exit: a restart
	// or an update starts the server again before it looks. A server falling
	// asleep isn't news: it does so whenever it's empty, and wakes when
	// someone joins.
	if h.op.Kind != "sleep" {
		s.alert(discord.Stopped())
	}
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
	s.sawStopping, s.sawCrash = false, false
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

// stoppedCleanly reports whether the run that ended logged a clean shutdown.
// A crashing server logs "Stopping server" too, after the error, so that
// alone isn't one. The reconcile loop and Discord's live status both go by
// this. The caller holds s.mu.
func (s *server) stoppedCleanly() bool { return s.sawStopping && !s.sawCrash }

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
	graceful := s.stoppedCleanly()
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
		s.alert(discord.Event{Kind: discord.KindStopped, At: fin})
		s.recordEvent(fin, "server_stopped_externally", "", "docker", fmt.Sprintf("exit code %d", c.State.ExitCode))
		if desired == api.DesiredRunning {
			s.autoStart("recover")
		}
	default:
		s.closeOpenSessions(fin, "server_crashed", true)
		cause := s.recordCrash(fin, c.State)
		// A server that wasn't meant to be running is left off, which is
		// not Playkeeper giving up on it.
		wanted := desired == api.DesiredRunning
		s.mu.Lock()
		restarting := wanted && len(s.crashes) < maxCrashes
		s.mu.Unlock()
		s.alert(discord.Event{Kind: discord.KindCrash, Detail: cause, Restarting: restarting, GaveUp: wanted && !restarting, At: fin})
		s.explainCrash(c.ID, c.State, false, nil)
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

// recordCrash counts a crash and returns its cause in one sentence, without
// what Playkeeper does about it, which the crash alert says in its own words.
func (s *server) recordCrash(fin time.Time, st docker.ContainerState) string {
	s.mu.Lock()
	var recent []time.Time
	for _, t := range s.crashes {
		if fin.Sub(t) < crashWindow {
			recent = append(recent, t)
		}
	}
	s.crashes = append(recent, fin)
	n := len(s.crashes)
	s.crashed, s.runCrashed = true, true
	s.runPhase = api.PhaseCrashed
	if st.OOMKilled {
		s.lastError = "The server ran out of memory and was killed."
		s.lastErrorHint = "Choose a larger memory budget in Settings, then start the server."
	} else {
		s.lastError = fmt.Sprintf("The server stopped unexpectedly (exit code %d) without shutting down cleanly.", st.ExitCode)
		s.lastErrorHint = "Check the Console for the last lines before the crash."
	}
	cause := s.lastError
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
	return cause
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
	gaveUp := s.countFailedStart(err)
	s.mu.Unlock()
	if gaveUp {
		s.alert(discord.StartFailed(err.Error()))
	}
}

// countFailedStart counts a failed automatic start; the caller holds s.mu.
// It reports whether Playkeeper gave up.
func (s *server) countFailedStart(err error) bool {
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
		return true
	}
	s.nextAutoRestart = now.Add(s.opts.CrashBackoff[min(n-1, len(s.opts.CrashBackoff)-1)])
	return false
}

// startFailed is called when a start the user asked for did not bring the
// server up. The error's hint tells them to fix the cause and press Start, so
// nothing retries in the background; a container that is still running (a
// slow start that timed out) keeps the desired state running. Either way the
// server isn't asleep, so the stand-in stops answering in its place.
func (s *server) startFailed(ctx context.Context) {
	if _, running, err := s.containerRunning(ctx); err == nil && !running {
		_ = s.setDesired(api.DesiredStopped)
	}
	s.leaveSleep()
}
