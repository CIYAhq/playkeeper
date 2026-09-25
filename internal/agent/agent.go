// Package agent is Playkeeper's root-owned local control agent. It is the only
// component that talks to Docker. It listens on a Unix socket that only root
// and the panel's service account may connect to, and exposes a fixed set of
// validated operations for the single Minecraft server.
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
	"github.com/CIYAhq/playkeeper/internal/update"
)

const (
	containerName = "playkeeper-minecraft"
	networkName   = "playkeeper"
	labelManaged  = "io.playkeeper.managed"
	labelInstall  = "io.playkeeper.install"
	labelSpec     = "io.playkeeper.spec"
	rconPort      = 25575

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
	// UpdateKeys are the release signing keys updates must be signed with
	// (default: the keys compiled into this build).
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
	console *ring

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	opLock chan struct{}
	opMu   sync.Mutex
	op     *api.Operation

	mu              sync.Mutex
	dockerOK        bool
	runPhase        api.Phase
	runPhaseDetail  string
	runStartedAt    time.Time
	sawStopping     bool
	lastError       string
	lastErrorHint   string
	reachable       bool
	reachableAt     time.Time
	players         *api.PlayerSnapshot
	resources       *api.Resources
	prevCPU         *docker.Stats
	crashes         []time.Time
	crashed         bool
	handledExit     map[string]time.Time
	exitSeen        map[string]seenExit
	intentional     map[string]bool
	followEnded     map[string]time.Time
	listMissing     map[string]int
	listExtra       map[string]int
	uuids           map[string]string
	nextAutoRestart time.Time

	rconMu sync.Mutex
	rcon   *minecraft.RCON
	rconIP string

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
	if opts.UpdateKeys == nil {
		opts.UpdateKeys = update.TrustedKeys()
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
	for _, d := range []string{cfg.AgentDir(), cfg.BackupsDir(), cfg.StagingDir(), filepath.Dir(cfg.ServerDataDir())} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	db, err := store.Open(filepath.Join(cfg.AgentDir(), "agent.db"), migrations)
	if err != nil {
		return nil, err
	}
	a := &Agent{
		cfg:         cfg,
		opts:        opts,
		db:          db,
		docker:      docker.New(cfg.DockerSocket),
		log:         opts.Logger,
		now:         opts.Now,
		started:     opts.Now(),
		console:     newRing(consoleCapacity),
		opLock:      make(chan struct{}, 1),
		handledExit: map[string]time.Time{},
		exitSeen:    map[string]seenExit{},
		intentional: map[string]bool{},
		followEnded: map[string]time.Time{},
		listMissing: map[string]int{},
		listExtra:   map[string]int{},
		uuids:       map[string]string{},
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
	a.markInterruptedOperations()
	a.pruneStages()
	a.loadUpdateState()
	return a, nil
}

// Start launches the background loops (collector, log follower, reconciler).
func (a *Agent) Start() {
	a.loop(a.followLoop)
	a.loop(a.sampleLoop)
	a.loop(a.reconcileLoop)
	a.loop(a.pruneLoop)
	a.loop(a.updateLoop)
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
	a.rconMu.Lock()
	if a.rcon != nil {
		a.rcon.Close()
	}
	a.rconMu.Unlock()
	a.db.Close()
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
	return []Route{
		{"GET", "/v1/health", a.hHealth},
		{"GET", "/v1/preflight", a.hPreflight},
		{"GET", "/v1/catalog", a.hCatalog},
		{"GET", "/v1/server", a.hStatus},
		{"POST", "/v1/server", a.hCreate},
		{"POST", "/v1/server/start", a.hStart},
		{"POST", "/v1/server/stop", a.hStop},
		{"POST", "/v1/server/restart", a.hRestart},
		{"POST", "/v1/server/settings", a.hSettings},
		{"GET", "/v1/server/logs", a.hLogs},
		{"POST", "/v1/server/command", a.hCommand},
		{"GET", "/v1/server/whitelist", a.hWhitelist},
		{"POST", "/v1/server/whitelist", a.hWhitelistAdd},
		{"DELETE", "/v1/server/whitelist/{name}", a.hWhitelistRemove},
		{"GET", "/v1/operations/{id}", a.hOperation},
		{"GET", "/v1/metrics", a.hMetrics},
		{"GET", "/v1/players/sessions", a.hSessions},
		{"GET", "/v1/players/summary", a.hSummary},
		{"GET", "/v1/events", a.hEvents},
		{"GET", "/v1/backups", a.hBackups},
		{"POST", "/v1/backups", a.hBackupCreate},
		{"POST", "/v1/backups/{id}/verify", a.hBackupVerify},
		{"GET", "/v1/backups/{id}/download", a.hBackupDownload},
		{"DELETE", "/v1/backups/{id}", a.hBackupDelete},
		{"POST", "/v1/restore/upload", a.hRestoreUpload},
		{"POST", "/v1/backups/{id}/restore", a.hRestoreFromBackup},
		{"GET", "/v1/restore/{id}", a.hRestorePreview},
		{"POST", "/v1/restore/{id}/apply", a.hRestoreApply},
		{"DELETE", "/v1/restore/{id}", a.hRestoreDiscard},
		{"GET", "/v1/audit", a.hAudit},
		{"GET", "/v1/update", a.hUpdate},
		{"POST", "/v1/update/check", a.hUpdateCheck},
		{"POST", "/v1/update/apply", a.hUpdateApply},
		{"POST", "/v1/server/version", a.hVersionChange},
	}
}

// Routes exposes the route table so tests can iterate every allowlisted verb.
func (a *Agent) Routes() []Route { return a.routeTable() }
