// Package agent is Playkeeper's root-owned local control agent. It is the only
// component that talks to Docker. It listens on a Unix socket that only root
// and the panel's service account may connect to, and exposes a fixed set of
// validated operations for the machine's Minecraft servers.
package agent

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/store"
)

const (
	networkName  = "playkeeper"
	labelManaged = "io.playkeeper.managed"
	labelInstall = "io.playkeeper.install"
	labelSpec    = "io.playkeeper.spec"
	rconPort     = 25575

	// OfflineModeEnv enables offline-mode servers for the protocol-bot test
	// harness only. The installer never sets it and the UI shows a permanent
	// warning while it is active.
	OfflineModeEnv = "PLAYKEEPER_E2E_OFFLINE_MODE_UNSAFE"
)

// Options configures an Agent. Zero values select production behaviour; the
// override hooks exist for tests.
type Options struct {
	Config         config.Config
	Logger         *slog.Logger
	Now            func() time.Time
	SampleInterval time.Duration
	// AllowedUIDs overrides the peers allowed on the socket (default: root and
	// the configured panel user).
	AllowedUIDs []uint32
	// RCONAddr maps the container address to the RCON address (tests).
	RCONAddr func(containerIP string) string
	// PingAddr overrides the address used for Server List Ping (tests).
	PingAddr        string
	OfflineModeTest bool
	HostMemoryMB    func() int
	DiskUsage       func(path string) (free, total int64, err error)
	CheckEgress     func(ctx context.Context) error
	PortInUse       func(port int) bool
	Retention       Retention
	// StopTimeout bounds a graceful server stop (default 90s).
	StopTimeout time.Duration
	// ReadyTimeout bounds waiting for "Done" after a start (default 10m).
	ReadyTimeout time.Duration
	// ReconcileInterval is how often desired and observed state are compared.
	ReconcileInterval time.Duration
	// CrashBackoff is the wait before each automatic restart after a crash.
	CrashBackoff []time.Duration
	// WarnDelay is how long players are warned in chat before a Minecraft
	// update stops the server (default 1 minute); BackupWarnDelay before a
	// backup does (default 3 seconds).
	WarnDelay       time.Duration
	BackupWarnDelay time.Duration
	// UpdateKeys are the release signing keys updates must be signed with;
	// without any, this agent cannot install updates. The playkeeper command
	// passes the keys compiled into the build.
	UpdateKeys []ed25519.PublicKey
	// UpdateCheckInterval is how often the agent looks for a new release
	// (default 12h; negative turns the automatic check off).
	UpdateCheckInterval time.Duration
	// BinaryVersion runs a downloaded binary's `version` command (tests).
	BinaryVersion func(path string) (string, error)
	// HTTPClient fetches releases and PaperMC's version list.
	HTTPClient *http.Client
	// FillURL is PaperMC's Fill API (default https://fill.papermc.io).
	FillURL string
}

// Retention bounds stored analytics and audit data.
type Retention struct {
	Samples    time.Duration
	Events     time.Duration
	Operations time.Duration
	Audit      time.Duration
	MaxSamples int
	MaxEvents  int
	MaxAudit   int
}

func DefaultRetention() Retention {
	return Retention{
		Samples:    30 * 24 * time.Hour,
		Events:     180 * 24 * time.Hour,
		Operations: 90 * 24 * time.Hour,
		Audit:      365 * 24 * time.Hour,
		MaxSamples: 250_000,
		MaxEvents:  500_000,
		MaxAudit:   100_000,
	}
}

type Agent struct {
	cfg     config.Config
	opts    Options
	db      *sql.DB
	docker  *docker.Client
	log     *slog.Logger
	now     func() time.Time
	started time.Time // when this agent process started

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// mopLock and mop are the machine-wide operation (a Playkeeper update).
	// It runs only while every server is idle, and servers wait for it.
	mopLock chan struct{}
	mopMu   sync.Mutex
	mop     *api.Operation

	srvMu   sync.Mutex
	servers map[string]*server
	// createMu serializes picking names, slugs, ports and memory for new servers.
	createMu sync.Mutex

	mu            sync.Mutex
	dockerOK      bool
	dockerVersion string
	hostCPU       *float64
	hostPrev      cpuTimes

	allowed map[uint32]bool

	upd     updateState
	catalog catalogCache
}

func New(opts Options) (*Agent, error) {
	if err := opts.Config.Validate(); err != nil {
		return nil, err
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.SampleInterval == 0 {
		opts.SampleInterval = 15 * time.Second
	}
	if opts.HostMemoryMB == nil {
		opts.HostMemoryMB = hostMemoryMB
	}
	if opts.DiskUsage == nil {
		opts.DiskUsage = diskUsage
	}
	if opts.CheckEgress == nil {
		opts.CheckEgress = checkEgress
	}
	if opts.PortInUse == nil {
		opts.PortInUse = portInUse
	}
	if opts.RCONAddr == nil {
		opts.RCONAddr = func(ip string) string { return net.JoinHostPort(ip, strconv.Itoa(rconPort)) }
	}
	if opts.Retention == (Retention{}) {
		opts.Retention = DefaultRetention()
	}
	if opts.StopTimeout == 0 {
		opts.StopTimeout = 90 * time.Second
	}
	if opts.ReadyTimeout == 0 {
		opts.ReadyTimeout = 10 * time.Minute
	}
	if opts.ReconcileInterval == 0 {
		opts.ReconcileInterval = 3 * time.Second
	}
	if len(opts.CrashBackoff) == 0 {
		opts.CrashBackoff = []time.Duration{0, 30 * time.Second, 2 * time.Minute}
	}
	if opts.WarnDelay == 0 {
		opts.WarnDelay = time.Minute
	}
	if opts.BackupWarnDelay == 0 {
		opts.BackupWarnDelay = 3 * time.Second
	}
	if opts.UpdateCheckInterval == 0 {
		opts.UpdateCheckInterval = 12 * time.Hour
	}
	if opts.BinaryVersion == nil {
		opts.BinaryVersion = binaryVersion
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 10 * time.Minute}
	}
	if opts.FillURL == "" {
		opts.FillURL = minecraft.DefaultFillURL
	}
	cfg := opts.Config
	for _, d := range []string{cfg.AgentDir(), cfg.BackupsDir(), cfg.StagingDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "servers"), 0o755); err != nil {
		return nil, err
	}
	db, err := store.Open(filepath.Join(cfg.AgentDir(), "agent.db"), migrations)
	if err != nil {
		return nil, err
	}
	a := &Agent{
		cfg:     cfg,
		opts:    opts,
		db:      db,
		docker:  docker.New(cfg.DockerSocket),
		log:     opts.Logger,
		now:     opts.Now,
		started: opts.Now(),
		mopLock: make(chan struct{}, 1),
		servers: map[string]*server{},
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.allowed = map[uint32]bool{}
	if len(opts.AllowedUIDs) > 0 {
		for _, u := range opts.AllowedUIDs {
			a.allowed[u] = true
		}
	} else {
		a.allowed[0] = true
		if u, err := user.Lookup(cfg.PanelUser); err == nil {
			if id, err := strconv.ParseUint(u.Uid, 10, 32); err == nil {
				a.allowed[uint32(id)] = true
			}
		} else if cfg.Dev {
			a.allowed[uint32(os.Getuid())] = true
		}
	}
	if err := a.migrateSingleServer(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate the existing server: %w", err)
	}
	if err := a.loadServers(); err != nil {
		db.Close()
		return nil, err
	}
	a.loadUpdateState()
	a.collectUpdateResult()
	a.markInterruptedOperations()
	a.pruneStages()
	return a, nil
}

// Start launches the background loops: each server's follower, collector and
// reconciler, and the machine's pruning, sampling and update checks.
func (a *Agent) Start() {
	for _, s := range a.serverList() {
		s.startLoops()
	}
	a.loop(a.pruneLoop)
	a.loop(a.updateLoop)
	a.loop(a.hostLoop)
}

func (a *Agent) loop(fn func(ctx context.Context)) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		fn(a.ctx)
	}()
}

// Close stops background work and waits for running operations to observe
// cancellation.
func (a *Agent) Close() {
	a.cancel()
	a.wg.Wait()
	for _, s := range a.serverList() {
		s.resetRCON()
	}
	a.db.Close()
}

// machineOp is the machine-wide operation in progress, if any.
func (a *Agent) machineOp() *api.Operation {
	a.mopMu.Lock()
	defer a.mopMu.Unlock()
	return copyOp(a.mop)
}

// currentOp is the machine-wide operation, or else the first server
// operation in progress (tests and the update refusal message use it).
func (a *Agent) currentOp() *api.Operation {
	if op := a.machineOp(); op != nil {
		return op
	}
	for _, s := range a.serverList() {
		if op := s.currentOp(); op != nil {
			return op
		}
	}
	return nil
}

// busy is true while any server or the machine runs an operation.
func (a *Agent) busy() bool { return a.currentOp() != nil || a.installingUpdate() != "" }

// beginMachineOp runs fn as the machine-wide operation. It needs every
// server idle, and holds their operation locks until fn returns, so no server
// operation starts meanwhile.
func (a *Agent) beginMachineOp(kind, actor string, fn func(ctx context.Context, h *opHandle) error) (*api.Operation, error) {
	select {
	case a.mopLock <- struct{}{}:
	default:
		return nil, &apiError{Status: http.StatusConflict, Code: api.CodeBusy, Msg: "Playkeeper is busy with " + opLabels[opKind(a.machineOp(), "update")] + ".", Hint: "Wait for it to finish, then try again.", Op: a.machineOp()}
	}
	if v := a.installingUpdate(); v != "" {
		<-a.mopLock
		return nil, &apiError{Status: http.StatusConflict, Code: api.CodeBusy, Msg: "Playkeeper is installing update " + v + ".", Hint: "The dashboard reconnects when it is done; try again then."}
	}
	var held []*server
	release := func() {
		for _, s := range held {
			<-s.opLock
		}
	}
	// createMu keeps a new server from appearing between taking the servers'
	// locks and publishing the operation, which addServer checks for.
	a.createMu.Lock()
	for _, s := range a.serverList() {
		select {
		case s.opLock <- struct{}{}:
			held = append(held, s)
		default:
			release()
			a.createMu.Unlock()
			<-a.mopLock
			cur := s.currentOp()
			what := "an operation"
			if cur != nil {
				what = opLabels[cur.Kind]
			}
			return nil, &apiError{Status: http.StatusConflict, Code: api.CodeBusy, Msg: s.name() + " is busy with " + what + ".", Hint: "Wait for it to finish, then try again.", Op: cur}
		}
	}
	op := &api.Operation{ID: newID(), Kind: kind, Status: api.OpRunning, Actor: actor, StartedAt: a.now().UTC(), Detail: map[string]any{}}
	a.mopMu.Lock()
	a.mop = op
	snap := copyOp(op)
	a.mopMu.Unlock()
	a.createMu.Unlock()
	a.saveOperation(snap)
	h := &opHandle{save: a.saveOperation, op: op, mu: func() func() { a.mopMu.Lock(); return a.mopMu.Unlock }}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() { <-a.mopLock }()
		defer release()
		ctx, cancel := context.WithTimeout(a.ctx, 45*time.Minute)
		defer cancel()
		err := runOp(ctx, h, fn)
		a.mopMu.Lock()
		done := finishOp(op, h, err, a.now().UTC())
		a.mop = nil
		a.mopMu.Unlock()
		a.saveOperation(&done)
		if done.Status != api.OpRunning {
			a.audit(actor, kind, "machine", done.Status, done.Error)
		}
		if err != nil {
			a.log.Warn("operation failed", "kind", kind, "err", err)
		}
	}()
	return snap, nil
}

// Serve listens on the configured Unix socket until ctx is cancelled. The
// socket is mode 0660, owned by root and the panel user's primary group.
func (a *Agent) Serve(ctx context.Context) error {
	sock := a.cfg.SocketPath
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		return err
	}
	if st, err := os.Lstat(sock); err == nil && st.Mode()&os.ModeSocket != 0 {
		os.Remove(sock)
	}
	// No umask change here: it is process-wide, and in `playkeeper dev` the
	// panel creates its files in the same process. Every connection is still
	// checked against the peer-UID allowlist; the mode only narrows who may
	// connect at all.
	ln, err := listenUnix(sock)
	if err != nil {
		return fmt.Errorf("listen %s: %w", sock, err)
	}
	mode := os.FileMode(0o600)
	if u, err := user.Lookup(a.cfg.PanelUser); err == nil && os.Geteuid() == 0 {
		gid, _ := strconv.Atoi(u.Gid)
		if err := os.Chown(sock, 0, gid); err != nil {
			ln.Close()
			return err
		}
		mode = 0o660
	}
	if err := os.Chmod(sock, mode); err != nil {
		ln.Close()
		return err
	}
	srv := &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext:       withConn,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	a.log.Info("agent listening", "socket", sock)
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type connKey struct{}

func withConn(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
}

// authorizePeer rejects connections from any process whose UID is not
// explicitly allowed. Unknown or unreadable credentials are rejected.
func (a *Agent) authorizePeer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Context().Value(connKey{}).(net.Conn)
		uid, err := peerUID(c)
		if err != nil || !a.allowed[uid] {
			writeErr(w, http.StatusForbidden, api.CodeForbidden, "This process is not allowed to control Playkeeper.", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Handler returns the agent's HTTP handler: a closed route table behind the
// peer-credential check. Anything not listed returns 404.
func (a *Agent) Handler() http.Handler {
	return a.authorizePeer(a.routes())
}

// HandlerForTest skips the peer check; tests use it with in-memory listeners.
func (a *Agent) HandlerForTest() http.Handler { return a.routes() }

func (a *Agent) routes() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range a.routeTable() {
		mux.HandleFunc(rt.Method+" "+rt.Pattern, rt.Handler)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "Unknown agent operation.", "")
	})
	return recoverer(a.log, mux)
}

var listenUnix = func(path string) (net.Listener, error) { return net.Listen("unix", path) }

// Route is one allowlisted agent operation.
type Route struct {
	Method  string
	Pattern string
	Handler http.HandlerFunc
}

func (a *Agent) routeTable() []Route {
	srv := a.withServer
	return []Route{
		{"GET", "/v1/health", a.hHealth},
		{"GET", "/v1/machine", a.hMachine},
		{"GET", "/v1/preflight", a.hPreflight},
		{"GET", "/v1/catalog", a.hCatalog},
		{"GET", "/v1/servers", a.hServers},
		{"POST", "/v1/servers", a.hCreate},
		{"GET", "/v1/servers/{id}", srv((*server).hStatus)},
		{"POST", "/v1/servers/{id}/start", srv((*server).hStart)},
		{"POST", "/v1/servers/{id}/stop", srv((*server).hStop)},
		{"POST", "/v1/servers/{id}/restart", srv((*server).hRestart)},
		{"POST", "/v1/servers/{id}/settings", srv((*server).hSettings)},
		{"GET", "/v1/servers/{id}/icon", srv((*server).hIcon)},
		{"POST", "/v1/servers/{id}/icon", srv((*server).hIconSet)},
		{"POST", "/v1/servers/{id}/version", srv((*server).hVersionChange)},
		{"POST", "/v1/servers/{id}/delete", srv((*server).hDelete)},
		{"GET", "/v1/servers/{id}/logs", srv((*server).hLogs)},
		{"POST", "/v1/servers/{id}/command", srv((*server).hCommand)},
		{"GET", "/v1/servers/{id}/whitelist", srv((*server).hWhitelist)},
		{"POST", "/v1/servers/{id}/whitelist", srv((*server).hWhitelistAdd)},
		{"DELETE", "/v1/servers/{id}/whitelist/{name}", srv((*server).hWhitelistRemove)},
		{"GET", "/v1/servers/{id}/operators", srv((*server).hOperators)},
		{"POST", "/v1/servers/{id}/operators", srv((*server).hOperatorAdd)},
		{"DELETE", "/v1/servers/{id}/operators/{name}", srv((*server).hOperatorRemove)},
		{"POST", "/v1/servers/{id}/kick", srv((*server).hKick)},
		{"GET", "/v1/servers/{id}/metrics", srv((*server).hMetrics)},
		{"GET", "/v1/servers/{id}/players/sessions", srv((*server).hSessions)},
		{"GET", "/v1/servers/{id}/players/summary", srv((*server).hSummary)},
		{"GET", "/v1/servers/{id}/events", srv((*server).hEvents)},
		{"GET", "/v1/servers/{id}/backups", srv((*server).hBackups)},
		{"POST", "/v1/servers/{id}/backups", srv((*server).hBackupCreate)},
		{"POST", "/v1/servers/{id}/backups/{bid}/verify", srv((*server).hBackupVerify)},
		{"GET", "/v1/servers/{id}/backups/{bid}/download", srv((*server).hBackupDownload)},
		{"DELETE", "/v1/servers/{id}/backups/{bid}", srv((*server).hBackupDelete)},
		{"POST", "/v1/servers/{id}/backups/{bid}/restore", srv((*server).hRestoreFromBackup)},
		{"POST", "/v1/servers/{id}/restore/upload", srv((*server).hRestoreUpload)},
		{"POST", "/v1/restore/upload", a.hRestoreUploadNew},
		{"GET", "/v1/restore/{id}", a.hRestorePreview},
		{"POST", "/v1/restore/{id}/apply", a.hRestoreApply},
		{"DELETE", "/v1/restore/{id}", a.hRestoreDiscard},
		{"GET", "/v1/operations/{id}", a.hOperation},
		{"GET", "/v1/activity", a.hActivity},
		{"GET", "/v1/audit", a.hAudit},
		{"GET", "/v1/update", a.hUpdate},
		{"POST", "/v1/update/check", a.hUpdateCheck},
		{"POST", "/v1/update/apply", a.hUpdateApply},
		// Follow-ups after 0.3.0.
		{"GET", "/v1/servers/{id}/world-copies", srv((*server).hWorldCopies)},
		{"DELETE", "/v1/servers/{id}/world-copies/{name}", srv((*server).hWorldCopyDelete)},
	}
}

// Routes exposes the route table so tests can iterate every allowlisted verb.
func (a *Agent) Routes() []Route { return a.routeTable() }

// withServer resolves the {id} in a server route; an unknown id is 404.
func (a *Agent) withServer(h func(*server, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !reServerID.MatchString(id) {
			writeError(w, errInvalid("invalid server id"))
			return
		}
		s := a.serverByID(id)
		if s == nil {
			writeError(w, errNotFound("Server"))
			return
		}
		h(s, w, r)
	}
}

func opKind(op *api.Operation, def string) string {
	if op == nil {
		return def
	}
	return op.Kind
}
