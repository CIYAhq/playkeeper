// Package agent is Playkeeper's root-owned local control agent. It is the only
// component that talks to Docker. It listens on a Unix socket that only root
// and the panel's service account may connect to, and exposes a fixed set of
// validated operations for the machine's Minecraft servers.
package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/certs"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/modpacks"
	"github.com/CIYAhq/playkeeper/internal/modpacks/curseforge"
	"github.com/CIYAhq/playkeeper/internal/pregen"
	"github.com/CIYAhq/playkeeper/internal/store"
	"github.com/CIYAhq/playkeeper/internal/templates"
	"github.com/CIYAhq/playkeeper/internal/webmap"
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
	// ProcStat reads /proc/stat, for the machine's CPU use and steal.
	ProcStat    func() ([]byte, error)
	DiskUsage   func(path string) (free, total int64, err error)
	CheckEgress func(ctx context.Context) error
	PortInUse   func(port int) bool
	// UDPPortInUse reports a UDP port something on the machine listens on;
	// add-ons such as voice chat get one no one uses.
	UDPPortInUse func(port int) bool
	Retention    Retention
	// StopTimeout bounds a graceful server stop (default 90s).
	StopTimeout time.Duration
	// ReadyTimeout bounds waiting for "Done" after a start (default 10m).
	ReadyTimeout time.Duration
	// ReadyPoll is how often a start looks whether the server is up
	// (default 500ms).
	ReadyPoll time.Duration
	// FollowRetry is how long the log follower waits before looking again
	// when there's no container, its log can't be read, or its run has
	// ended (default 2s).
	FollowRetry time.Duration
	// DiscordStatusGap is the least time between two edits of Discord's
	// live status message (0: the notifier's two seconds).
	DiscordStatusGap time.Duration
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
	// DiscordClient sends Discord requests (tests); nil means discord.com,
	// or the test endpoint in DiscordURLEnv.
	DiscordClient *http.Client
	// Addons is the plugin and mod library (default: Modrinth and Hangar
	// through HTTPClient, downloading into the staging folder).
	Addons *addons.Library
	// PregenInterval is how often a running map pre-generation is checked
	// (default 5s); PregenResumeAfter is how long a server must be empty
	// before a task paused for its players continues (default 2 minutes).
	PregenInterval    time.Duration
	PregenResumeAfter time.Duration
	// DataPackWait bounds how long switching a data pack on or off waits
	// for the server to reload its data (default a minute).
	DataPackWait time.Duration
	// NamesHTTP carries requests to the free address service (tests); nil
	// uses the names client's own, which never use a proxy.
	NamesHTTP *http.Client
	// Resolver looks up the machine's names as the public sees them
	// (default: public DNS-over-HTTPS resolvers).
	Resolver certs.Resolver
	// Issue gets a certificate (tests replace Let's Encrypt with it).
	Issue func(ctx context.Context, is *certs.Issuer, req certs.Request) (*certs.Certificate, error)
	// HTTP01Addr is where Let's Encrypt's HTTP-01 checks are answered
	// while a certificate is being issued (default ":80").
	HTTP01Addr string
	// AddressInterval is how often the address loop looks at the address
	// (default 1 minute; negative turns the ticker off).
	AddressInterval time.Duration
	// PublishPoll is how often a free address's records are looked at
	// while they are being published (default 30s, which the names
	// service's per-key rate limit allows).
	PublishPoll time.Duration
	// PublicAddrs are the public addresses of the machine's network
	// interfaces (tests).
	PublicAddrs func() []netip.Addr
	// CertRoots are the certificate authorities players' games trust, for
	// resource pack links (tests); nil means the system's.
	CertRoots *x509.CertPool
	// PortHolder names the process listening on a host TCP port, for a
	// start that failed over a taken port no Docker container publishes
	// (default: read from /proc).
	PortHolder func(port int) (name string, pid int, ok bool)
	// UpstreamClient reads the server software and modpack upstreams
	// (Mojang, Fabric, Quilt, NeoForge, Forge, Purpur, Modrinth, CurseForge) at
	// their fixed HTTPS hosts; tests swap its transport. It defaults to
	// HTTPClient.
	UpstreamClient *http.Client
	// Modpacks is the modpack library (tests). By default it reaches
	// Modrinth, and CurseForge with the machine's key, through
	// UpstreamClient, and is built again when the key changes.
	Modpacks *modpacks.Library
	// PackClient downloads the data packs a template names, from any
	// public host but only over HTTPS to public addresses (default
	// templates.PackClient); tests swap it.
	PackClient *http.Client

	// Wave 6: the live map.
	// MapAddr maps the container address to squaremap's address (tests).
	MapAddr func(containerIP string) string
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
	hostTimes     []cpuSnapshot

	allowed map[uint32]bool

	upd     updateState
	catalog catalogCache
	disc    discordState
	browse  browseCache
	// curatedPicks are the curated add-ons that fit a type and Minecraft
	// version (wave 4).
	curatedPicks *ttlCache[[]curatedPick]
	voicePorts   voicePorts
	icons        iconCache
	// packMu serializes changes to the resource pack store with pruning it.
	packMu sync.Mutex
	addr   addressRuntime
	// panelCerts are the certificates the panel serves, looked at afresh
	// for every resource pack link.
	panelCerts *certs.Store

	software softwareCache

	// Wave 4: the modpack library with the CurseForge key in effect, and
	// answers from the pack sources kept for a little while.
	packLib          atomic.Pointer[modpacks.Library]
	packKeyMu        sync.Mutex
	packKey          curseforge.Key
	packKeyProblem   string
	keyFileMu        sync.Mutex
	packSearches     *ttlCache[*api.ModpackResults]
	packDetails      *ttlCache[*api.ModpackDetail]
	packPreviews     *ttlCache[packPreview]
	packPreviewSlots chan struct{}

	// Wave 4: templates planned on this machine, by their plan's
	// fingerprint, until a server is created from one.
	templatePlans *ttlCache[*templates.Template]

	// Wave 4: each server's friends' share, built on the first ask.
	shares friendsShares

	// Wave 6: the live map, and servers started from a world.
	maps      mapState
	mapClient *http.Client
	imports   importRegistry

	// Wave 7 (0.4.0): the Disk space page's last scan.
	disk diskCache
	// unreadableSwaps is the error last logged for each stage whose swap
	// journal can't be read, so each is logged once.
	unreadableSwaps sync.Map
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
	if opts.ProcStat == nil {
		opts.ProcStat = func() ([]byte, error) { return os.ReadFile("/proc/stat") }
	}
	if opts.PortHolder == nil {
		opts.PortHolder = func(port int) (string, int, bool) { return portHolder("/proc", port) }
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
	if opts.UDPPortInUse == nil {
		opts.UDPPortInUse = udpPortInUse
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
	if opts.ReadyPoll == 0 {
		opts.ReadyPoll = 500 * time.Millisecond
	}
	if opts.FollowRetry == 0 {
		opts.FollowRetry = 2 * time.Second
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
	if opts.Resolver == nil {
		opts.Resolver = certs.PublicResolver{}
	}
	if opts.HTTP01Addr == "" {
		opts.HTTP01Addr = ":80"
	}
	if opts.AddressInterval == 0 {
		opts.AddressInterval = time.Minute
	}
	if opts.PublishPoll == 0 {
		opts.PublishPoll = 30 * time.Second
	}
	if opts.PublicAddrs == nil {
		opts.PublicAddrs = func() []netip.Addr { return certs.ExpectedAddrs() }
	}
	if opts.UpstreamClient == nil {
		opts.UpstreamClient = opts.HTTPClient
	}
	if opts.PackClient == nil {
		opts.PackClient = templates.PackClient()
	}
	if opts.MapAddr == nil {
		opts.MapAddr = func(ip string) string { return net.JoinHostPort(ip, strconv.Itoa(webmap.Port)) }
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
	if opts.Addons == nil {
		lib := addons.New(opts.HTTPClient)
		lib.TempDir = cfg.StagingDir()
		opts.Addons = lib
	}
	if opts.PregenInterval == 0 {
		opts.PregenInterval = 5 * time.Second
	}
	if opts.PregenResumeAfter == 0 {
		opts.PregenResumeAfter = pregen.DefaultResumeAfter
	}
	if opts.DataPackWait == 0 {
		opts.DataPackWait = time.Minute
	}
	panelCerts, err := certs.NewStore(certs.StoreOptions{Dir: cfg.CertsDir(), Now: opts.Now, RecheckEvery: -1})
	if err != nil {
		return nil, err
	}
	db, err := store.Open(filepath.Join(cfg.AgentDir(), "agent.db"), migrations)
	if err != nil {
		return nil, err
	}
	a := &Agent{
		cfg:        cfg,
		opts:       opts,
		db:         db,
		docker:     docker.New(cfg.DockerSocket),
		log:        opts.Logger,
		now:        opts.Now,
		started:    opts.Now(),
		mopLock:    make(chan struct{}, 1),
		servers:    map[string]*server{},
		panelCerts: panelCerts,

		packSearches:     newTTLCache[*api.ModpackResults](5*time.Minute, 64),
		packDetails:      newTTLCache[*api.ModpackDetail](10*time.Minute, 64),
		packPreviews:     newTTLCache[packPreview](30*time.Minute, 32),
		packPreviewSlots: make(chan struct{}, 2),
		templatePlans:    newTTLCache[*templates.Template](time.Hour, 32),
		curatedPicks:     newTTLCache[[]curatedPick](curatedTTL, 32),

		mapClient: webmap.NewClient(),
	}
	a.loadPacks()
	a.ctx, a.cancel = context.WithCancel(context.Background())
	if a.opts.Issue == nil {
		a.opts.Issue = a.issue
	}
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
	if err := a.initDiscord(); err != nil {
		db.Close()
		return nil, err
	}
	a.loadUpdateState()
	a.collectUpdateResult()
	a.loadAddress()
	a.markInterruptedOperations(a.findInterruptedRestores()...)
	a.pruneStages()
	a.pruneArchiveLeftovers()
	return a, nil
}

// Start launches the background loops: each server's follower, collector and
// reconciler, and the machine's pruning, sampling, update checks and address.
// A restore a previous agent process was in the middle of is finished first.
func (a *Agent) Start() {
	for _, s := range a.serverList() {
		s.recoverAtStart()
		s.startLoops()
	}
	a.loop(a.pruneLoop)
	a.loop(a.updateLoop)
	a.loop(a.hostLoop)
	a.loop(a.addressLoop)
	a.loop(a.disc.n.Run)
	a.loop(a.discordLoop)
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
		a.finishOperation("", "machine", &done)
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
	return append([]Route{
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
		{"GET", "/v1/servers/{id}/running", srv((*server).hRunning)},
		{"GET", "/v1/servers/{id}/memory", srv((*server).hMemory)},
		{"GET", "/v1/servers/{id}/players/sessions", srv((*server).hSessions)},
		{"GET", "/v1/servers/{id}/players/summary", srv((*server).hSummary)},
		{"GET", "/v1/servers/{id}/events", srv((*server).hEvents)},
		{"GET", "/v1/servers/{id}/backups", srv((*server).hBackups)},
		{"POST", "/v1/servers/{id}/backups", srv((*server).hBackupCreate)},
		{"POST", "/v1/servers/{id}/backups/{bid}/verify", srv((*server).hBackupVerify)},
		{"GET", "/v1/servers/{id}/backups/{bid}/download", srv((*server).hBackupDownload)},
		{"DELETE", "/v1/servers/{id}/backups/{bid}", srv((*server).hBackupDelete)},
		{"POST", "/v1/servers/{id}/backups/{bid}/restore", srv((*server).hRestoreFromBackup)},
		{"POST", "/v1/servers/{id}/saving/resume", srv((*server).hSavingResume)},
		{"POST", "/v1/servers/{id}/addons/remove-file", srv((*server).hRemoveAddon)},
		{"POST", "/v1/servers/{id}/restore/upload", srv((*server).hRestoreUpload)},
		{"GET", "/v1/servers/{id}/addons", srv((*server).hAddons)},
		{"GET", "/v1/servers/{id}/addons/checks", srv((*server).hAddonChecks)},
		{"GET", "/v1/servers/{id}/addons/search", srv((*server).hAddonSearch)},
		{"GET", "/v1/servers/{id}/addons/project/{source}/{project}", srv((*server).hAddonDetails)},
		{"GET", "/v1/servers/{id}/addons/project/{source}/{project}/removal", srv((*server).hAddonRemovePreview)},
		{"POST", "/v1/servers/{id}/addons/install", srv((*server).hAddonInstall)},
		{"POST", "/v1/servers/{id}/addons/update/plan", srv((*server).hAddonUpdatePlan)},
		{"POST", "/v1/servers/{id}/addons/update", srv((*server).hAddonUpdate)},
		{"POST", "/v1/servers/{id}/addons/remove", srv((*server).hAddonRemove)},
		{"POST", "/v1/servers/{id}/addons/adopt", srv((*server).hAddonAdopt)},
		{"POST", "/v1/servers/{id}/addons/forget", srv((*server).hAddonForget)},
		{"GET", "/v1/servers/{id}/pregen", srv((*server).hPregen)},
		{"POST", "/v1/servers/{id}/pregen/start", srv((*server).hPregenStart)},
		{"POST", "/v1/servers/{id}/pregen/pause", srv((*server).hPregenPause)},
		{"POST", "/v1/servers/{id}/pregen/continue", srv((*server).hPregenContinue)},
		{"POST", "/v1/servers/{id}/pregen/cancel", srv((*server).hPregenCancel)},
		{"GET", "/v1/servers/{id}/datapacks", srv((*server).hDataPacks)},
		{"POST", "/v1/servers/{id}/datapacks", srv((*server).hDataPackAdd)},
		{"GET", "/v1/servers/{id}/datapacks/{name}/icon", srv((*server).hDataPackIcon)},
		{"POST", "/v1/servers/{id}/datapacks/{name}/enable", srv((*server).hDataPackEnable)},
		{"POST", "/v1/servers/{id}/datapacks/{name}/disable", srv((*server).hDataPackDisable)},
		{"DELETE", "/v1/servers/{id}/datapacks/{name}", srv((*server).hDataPackRemove)},
		{"GET", "/v1/servers/{id}/resourcepack", srv((*server).hResourcePack)},
		{"POST", "/v1/servers/{id}/resourcepack", srv((*server).hResourcePackSet)},
		{"POST", "/v1/servers/{id}/resourcepack/settings", srv((*server).hResourcePackSettings)},
		{"DELETE", "/v1/servers/{id}/resourcepack", srv((*server).hResourcePackRemove)},
		{"GET", "/v1/servers/{id}/resourcepack/icon", srv((*server).hResourcePackIcon)},
		{"GET", "/v1/resource-packs/active", a.hActiveResourcePacks},
		{"GET", "/v1/addons/icon", a.hAddonIcon},
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
		// wave 5: player profiles, messages and bans; Discord.
		{"GET", "/v1/servers/{id}/players/profile", srv((*server).hProfile)},
		{"POST", "/v1/servers/{id}/players/message", srv((*server).hMessage)},
		{"POST", "/v1/servers/{id}/ban", srv((*server).hBan)},
		{"GET", "/v1/discord", a.hDiscord},
		{"POST", "/v1/discord/connect", a.hDiscordConnect},
		{"PUT", "/v1/discord", a.hDiscordSettings},
		{"DELETE", "/v1/discord", a.hDiscordDisconnect},
		{"POST", "/v1/discord/test", a.hDiscordTest},
		{"POST", "/v1/discord/notify", a.hDiscordNotify},
		// Follow-ups after 0.3.0.
		{"GET", "/v1/servers/{id}/world-copies", srv((*server).hWorldCopies)},
		{"DELETE", "/v1/servers/{id}/world-copies/{name}", srv((*server).hWorldCopyDelete)},
		{"GET", "/v1/address", a.hAddress},
		{"DELETE", "/v1/address", a.hAddressDelete},
		{"GET", "/v1/address/available", a.hAddressAvailable},
		{"GET", "/v1/address/alive/{nonce}", a.hAddressAlive},
		{"GET", "/v1/address/plan", a.hAddressPlan},
		{"POST", "/v1/address/claim", a.hAddressClaim},
		{"POST", "/v1/address/refresh", a.hAddressRefresh},
		{"POST", "/v1/address/release", a.hAddressRelease},
		{"POST", "/v1/address/check", a.hAddressCheck},
		{"POST", "/v1/address/certificate", a.hAddressCertificate},

		// Wave 4: every server type.
		{"GET", "/v1/catalog/builds", a.hCatalogBuilds},
		{"POST", "/v1/servers/{id}/software/reinstall", srv((*server).hSoftwareReinstall)},
		// Wave 4: modpacks.
		{"GET", "/v1/modpacks", a.hModpackSearch},
		{"GET", "/v1/modpacks/{source}/{project}", a.hModpackDetail},
		{"GET", "/v1/modpacks/{source}/{project}/versions/{version}/preview", a.hModpackPreview},
		// Wave 4: templates.
		{"GET", "/v1/servers/{id}/template", srv((*server).hTemplate)},
		{"POST", "/v1/servers/{id}/template/retry", srv((*server).hTemplateRetry)},
		{"POST", "/v1/templates/plan", a.hTemplatePlan},
		// Wave 4: sharing the pack with friends.
		{"GET", "/v1/servers/{id}/mods/share", srv((*server).hPackShare)},
		{"POST", "/v1/servers/{id}/mods/share", srv((*server).hPackShareSet)},
		{"GET", "/v1/servers/{id}/mods/share.mrpack", srv((*server).hPackShareFile)},
		{"GET", "/v1/packs/{token}", a.hPackLink},
		// Wave 4: curated add-ons.
		{"GET", "/v1/servers/{id}/addons/curated", srv((*server).hAddonCurated)},
		// Wave 4: add-on sources.
		{"GET", "/v1/addon-sources", a.hAddonSources},
		{"POST", "/v1/addon-sources/curseforge", a.hCurseForgeKeySet},
		{"DELETE", "/v1/addon-sources/curseforge", a.hCurseForgeKeyRemove},

		// Wave 6: the live map and the shared map.
		{"GET", "/v1/servers/{id}/map", srv((*server).hMap)},
		{"GET", "/v1/servers/{id}/map/{rest...}", srv((*server).hMapProxy)},
		{"POST", "/v1/servers/{id}/map/enable", srv((*server).hMapEnable)},
		{"POST", "/v1/servers/{id}/map/disable", srv((*server).hMapDisable)},
		{"POST", "/v1/servers/{id}/map/share", srv((*server).hMapShare)},
		{"POST", "/v1/servers/{id}/map/restart-later", srv((*server).hMapRestartLater)},
		{"GET", "/v1/public-maps/{token}", a.hPublicMap},
		{"GET", "/v1/public-maps/{token}/{rest...}", a.hPublicMapProxy},
		// Wave 6: worlds people upload, for a new server or to replace one's world.
		{"POST", "/v1/servers/{id}/world-imports", srv((*server).hWorldImportNew)},
		{"POST", "/v1/world-imports", a.hWorldImportNewServer},
		{"GET", "/v1/world-imports/{imp}", a.hWorldImport},
		{"DELETE", "/v1/world-imports/{imp}", a.hWorldImportDelete},
		{"POST", "/v1/world-imports/{imp}/files", a.hWorldImportFile},
		{"PUT", "/v1/world-imports/{imp}/files/{n}", a.hWorldImportUpload},
		{"POST", "/v1/world-imports/{imp}/inspect", a.hWorldImportInspect},
		{"POST", "/v1/world-imports/{imp}/preview", a.hWorldImportPreview},
		{"POST", "/v1/world-imports/{imp}/apply", a.hWorldImportApply},
		{"POST", "/v1/world-imports/{imp}/create", a.hWorldImportCreate},
	}, a.automationRoutes()...)
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
