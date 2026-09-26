// Package panel is Playkeeper's HTTPS web panel. It runs as an unprivileged
// service account, has no Docker access, and forwards validated, authenticated
// requests to the local agent socket.
package panel

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/certs"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
	"github.com/CIYAhq/playkeeper/internal/mcp"
	"github.com/CIYAhq/playkeeper/internal/mojang"
	"github.com/CIYAhq/playkeeper/internal/packs"
	"github.com/CIYAhq/playkeeper/internal/portshare"
	"github.com/CIYAhq/playkeeper/internal/store"
	"github.com/CIYAhq/playkeeper/internal/version"
)

type Options struct {
	Config          config.Config
	Logger          *slog.Logger
	Now             func() time.Time
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	// Static is the built UI (web/dist); nil serves a "UI not built" page.
	Static fs.FS
	Agent  *agentclient.Client
	// Heads are where player faces come from (default: Mojang).
	Heads *HeadSources
	// Mojang looks Minecraft accounts up by name, for invite links and
	// faces (default: Mojang's profile service). One client keeps one
	// cache and one request budget.
	Mojang *mojang.Client
	// LinkRoutes are the agent routes joined machines may be sent (the
	// agent's route table). Without them the panel accepts no machines:
	// the root recovery commands leave them out, so they never create the
	// link key as root.
	LinkRoutes []machinelink.Route
	// LookupIP resolves names for the proxy check (default: the system
	// resolver).
	LookupIP func(ctx context.Context, host string) ([]netip.Addr, error)
	// HostIPs are this host's addresses, offered for joining when the
	// dashboard was opened at a loopback address (default: HostIPs). The
	// installed panel's unit leaves out AF_NETLINK, so there it finds none.
	HostIPs func() []net.IP
}

type Server struct {
	cfg     config.Config
	opts    Options
	db      *sql.DB
	log     *slog.Logger
	now     func() time.Time
	agent   *agentclient.Client
	static  fs.FS
	loginIP *limiter
	control *limiter
	// previews is previewRoutes' bucket, apart from control's.
	previews *limiter
	locks    *lockout
	// loginUser counts failed sign-ins per account from every address, so
	// guesses spread over many addresses stay slow. One address can't use
	// it up before its own lockout stops it.
	loginUser *limiter
	heads     *headFetcher
	mojang    *mojang.Client
	// joinGuard limits attempts on the public invite pages.
	joinGuard *invites.Guard
	hub       *machinelink.Hub
	proxies   proxyCache
	// mcpHTTP serves the MCP tools at /mcp to API tokens.
	mcpHTTP *mcp.HTTPHandler
	// refusals counts refusals for the audit log (see refusals.go).
	refusals refusalSink
	// audits counts audit rows written, to prune the log every so often;
	// auditMaxAge and maxAudit are how much of it is kept.
	audits      atomic.Int64
	auditMaxAge time.Duration
	maxAudit    int

	// beforeCodeCheck, when set, runs in each second sign-in step just
	// before the transaction that claims the sign-in and checks its code;
	// tests line up concurrent requests with it.
	beforeCodeCheck func()

	public      *publicGroup
	activePacks *activePacks
	// listings are the servers each machine last listed.
	listings listings
}

func New(opts Options) (*Server, error) {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = 12 * time.Hour
	}
	if opts.AbsoluteTimeout == 0 {
		opts.AbsoluteTimeout = 7 * 24 * time.Hour
	}
	if opts.Agent == nil {
		opts.Agent = agentclient.New(opts.Config.SocketPath)
	}
	if opts.LookupIP == nil {
		opts.LookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	if opts.HostIPs == nil {
		opts.HostIPs = HostIPs
	}
	if err := os.MkdirAll(opts.Config.PanelDir(), 0o700); err != nil {
		return nil, err
	}
	db, err := store.Open(filepath.Join(opts.Config.PanelDir(), "panel.db"), panelMigrations)
	if err != nil {
		return nil, err
	}
	src := defaultHeadSources()
	if opts.Heads != nil {
		src = *opts.Heads
	}
	mc := opts.Mojang
	if mc == nil {
		if mc, err = mojang.NewClient(mojang.Options{Now: opts.Now}); err != nil {
			db.Close()
			return nil, err
		}
	}
	s := &Server{
		cfg: opts.Config, opts: opts, db: db, log: opts.Logger, now: opts.Now, agent: opts.Agent, static: opts.Static,
		loginIP:     newLimiter(10, 15*time.Minute, opts.Now),
		control:     newLimiter(30, time.Minute, opts.Now),
		previews:    newLimiter(120, time.Minute, opts.Now),
		locks:       newLockout(opts.Now),
		loginUser:   newLimiter(30, time.Hour, opts.Now),
		heads:       newHeadFetcher(src, mc),
		mojang:      mc,
		joinGuard:   invites.NewGuard(invites.GuardLimits{}, opts.Now),
		auditMaxAge: 365 * 24 * time.Hour,
		maxAudit:    100_000,
	}
	s.activePacks = &activePacks{fetch: s.fetchActivePacks, now: opts.Now}
	s.public = newPublicGroup(s.publicRoutes(), opts.Now)
	if err := s.ensureWorkspace(); err != nil {
		db.Close()
		return nil, err
	}
	s.pruneAudit()
	s.noteVersion(version.Version)
	if err := s.startMCP(); err != nil {
		db.Close()
		return nil, err
	}
	s.sweepTokens()
	if len(opts.LinkRoutes) > 0 {
		if err := s.startHub(opts.LinkRoutes); err != nil {
			db.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *Server) Close() error {
	s.mcpHTTP.Close()
	if s.hub != nil {
		s.hub.Close()
	}
	s.flushRefusals(true)
	return s.db.Close()
}

type authLevel int

const (
	public         authLevel = iota
	publicMutation           // login/setup: same-origin + X-Requested-With, no session yet
	needSession
	needSessionCSRF
	// pendingSession is the second sign-in step: publicMutation's checks
	// plus a password-checked second-step cookie, which is not a session.
	pendingSession
)

// Route is one panel API route; tests iterate Routes() to check that every
// route enforces its authentication and CSRF level. Act is what the signed-in
// account must be permitted to do.
type Route struct {
	Method  string
	Pattern string
	Level   authLevel
	Act     action
	handler func(w http.ResponseWriter, r *http.Request, sess *session)
}

// Mutating reports whether the route changes state (and so needs CSRF).
func (rt Route) Mutating() bool {
	return rt.Level == needSessionCSRF || rt.Level == publicMutation || rt.Level == pendingSession
}

// NeedsSession reports whether the route requires a signed-in admin.
func (rt Route) NeedsSession() bool { return rt.Level == needSession || rt.Level == needSessionCSRF }

// previewRoutes only work out what a change would do. Their dialogs ask
// them while people type, so they count against a larger bucket of their
// own, and the change itself never finds the 30 actions a minute used up.
var previewRoutes = map[string]bool{
	"POST /api/servers/{id}/schedules/preview":     true,
	"POST /api/servers/{id}/backup-rules/estimate": true,
}

func (s *Server) Routes() []Route {
	view := func(p string, h func(http.ResponseWriter, *http.Request, *session)) Route {
		return Route{"GET", p, needSession, actView, h}
	}
	// Server routes are forwarded to the machine that runs the server.
	sg := func(p, agentPath string) Route {
		return Route{"GET", p, needSession, actView, s.serverProxy("GET", agentPath)}
	}
	// sm routes change a server; they need an admin unless smAs names a
	// lesser action.
	sm := func(method, p, agentPath string) Route {
		return Route{method, p, needSessionCSRF, actManageServers, s.serverProxy(method, agentPath)}
	}
	smAs := func(act action, method, p, agentPath string) Route {
		return Route{method, p, needSessionCSRF, act, s.serverProxy(method, agentPath)}
	}
	// Machine routes are forwarded to the machine named in the path.
	mg := func(p, agentPath string) Route {
		return Route{"GET", p, needSession, actView, s.machineProxy("GET", agentPath)}
	}
	mm := func(method, p, agentPath string, act action) Route {
		return Route{method, p, needSessionCSRF, act, s.machineProxy(method, agentPath)}
	}
	// The machine's address lists every server's join address, so looking
	// at it needs every server too.
	ag := func(p, agentPath string) Route {
		return Route{"GET", p, needSession, actView, everyServer(s.addressProxy("GET", agentPath))}
	}
	am := func(p, agentPath string) Route {
		return Route{"POST", p, needSessionCSRF, actManageMachine, s.addressProxy("POST", agentPath)}
	}
	an := func(p, agentPath string) Route {
		return Route{"POST", p, needSessionCSRF, actManageMachine, s.dashboardAddress(s.addressProxy("POST", agentPath))}
	}
	routes := []Route{
		{"GET", "/api/health", public, "", s.hHealth},
		{"GET", "/api/setup/status", public, "", s.hSetupStatus},
		{"POST", "/api/setup", publicMutation, "", s.hSetup},
		{"POST", "/api/auth/login", publicMutation, "", s.hLogin},
		{"POST", "/api/auth/second-factor", pendingSession, "", s.hSecondFactor},
		{"POST", "/api/auth/second-factor/cancel", pendingSession, "", s.hSecondFactorCancel},
		{"GET", "/api/auth/me", needSession, actManageAccount, s.hMe},
		{"POST", "/api/auth/logout", needSessionCSRF, actManageAccount, s.hLogout},
		{"POST", "/api/auth/logout-all", needSessionCSRF, actManageAccount, s.hLogoutAll},
		{"POST", "/api/auth/password", needSessionCSRF, actManageAccount, s.hPassword},
		{"GET", "/api/auth/2fa", needSession, actManageAccount, s.h2FAStatus},
		{"POST", "/api/auth/2fa/setup", needSessionCSRF, actManageAccount, s.h2FASetupStart},
		{"GET", "/api/auth/2fa/setup", needSession, actManageAccount, s.h2FASetupShow},
		{"DELETE", "/api/auth/2fa/setup", needSessionCSRF, actManageAccount, s.h2FASetupCancel},
		{"POST", "/api/auth/2fa/confirm", needSessionCSRF, actManageAccount, s.h2FAConfirm},
		{"POST", "/api/auth/2fa/disable", needSessionCSRF, actManageAccount, s.h2FADisable},
		{"POST", "/api/auth/2fa/recovery-codes", needSessionCSRF, actManageAccount, s.h2FARecoveryCodes},
		{"GET", "/api/me/prefs", needSession, actManageAccount, s.hPrefs},
		{"POST", "/api/me/prefs", needSessionCSRF, actManageAccount, s.hPrefsSet},
		view("/api/tokens", s.hTokens),
		{"POST", "/api/tokens", needSessionCSRF, actManageAccount, s.hTokenCreate},
		view("/api/tokens/activity", s.hTokenActivity),
		{"DELETE", "/api/tokens/{tid}", needSessionCSRF, actManageAccount, s.hTokenRevoke},
		{"GET", "/api/audit", needSession, actViewAuditTrail, s.hAudit},
		view("/api/projects", s.hProjects),
		view("/api/machines", s.hMachines),
		view("/api/machines/link", s.hMachineLink),
		{"POST", "/api/join-codes", needSessionCSRF, actManageMachine, s.hJoinCodeCreate},
		{"DELETE", "/api/join-codes/{cid}", needSessionCSRF, actManageMachine, s.hJoinCodeCancel},
		view("/api/machines/{mid}", s.hMachine),
		{"DELETE", "/api/machines/{mid}", needSessionCSRF, actManageMachine, s.hMachineRemove},
		view("/api/machines/{mid}/events", s.hMachineEvents),
		mg("/api/machines/{mid}/preflight", "/v1/preflight"),
		mg("/api/machines/{mid}/catalog", "/v1/catalog"),
		view("/api/machines/{mid}/activity", s.hMachineActivity),
		mg("/api/machines/{mid}/update", "/v1/update"),
		mm("POST", "/api/machines/{mid}/update/check", "/v1/update/check", actManageMachine),
		{"POST", "/api/machines/{mid}/update/apply", needSessionCSRF, actManageMachine, s.forwardThen("POST", "/v1/update/apply", s.recordUpdate)},
		ag("/api/machines/{mid}/address", "/v1/address"),
		{"GET", "/api/machines/{mid}/address/available", needSession, actManageMachine, s.machineProxy("GET", "/v1/address/available")},
		ag("/api/machines/{mid}/address/plan", "/v1/address/plan"),
		an("/api/machines/{mid}/address/claim", "/v1/address/claim"),
		an("/api/machines/{mid}/address/refresh", "/v1/address/refresh"),
		am("/api/machines/{mid}/address/release", "/v1/address/release"),
		an("/api/machines/{mid}/address/check", "/v1/address/check"),
		an("/api/machines/{mid}/address/certificate", "/v1/address/certificate"),
		mm("DELETE", "/api/machines/{mid}/address", "/v1/address", actManageMachine),
		{"POST", "/api/machines/{mid}/servers", needSessionCSRF, actCreateServers, s.forwardThen("POST", "/v1/servers", s.claimCreatedBy)},
		{"POST", "/api/machines/{mid}/restore/upload", needSessionCSRF, actCreateServers, s.rawUpload("/v1/restore/upload", "application/gzip")},
		{"GET", "/api/machines/{mid}/restore/{rid}", needSession, actRestore, s.restoreProxy("GET", "/v1/restore/{rid}", nil)},
		{"POST", "/api/machines/{mid}/restore/{rid}/apply", needSessionCSRF, actRestore, s.restoreProxy("POST", "/v1/restore/{rid}/apply", s.claimCreatedBy)},
		{"DELETE", "/api/machines/{mid}/restore/{rid}", needSessionCSRF, actRestore, s.restoreProxy("DELETE", "/v1/restore/{rid}", nil)},
		view("/api/machines/{mid}/operations/{op}", s.hOperation),
		view("/api/servers", s.hServers),
		sg("/api/servers/{id}", "/v1/servers/{id}"),
		smAs(actRunServers, "POST", "/api/servers/{id}/start", "/v1/servers/{id}/start"),
		smAs(actRunServers, "POST", "/api/servers/{id}/stop", "/v1/servers/{id}/stop"),
		smAs(actRunServers, "POST", "/api/servers/{id}/restart", "/v1/servers/{id}/restart"),
		sm("POST", "/api/servers/{id}/settings", "/v1/servers/{id}/settings"),
		sm("POST", "/api/servers/{id}/version", "/v1/servers/{id}/version"),
		smAs(actCreateServers, "POST", "/api/servers/{id}/delete", "/v1/servers/{id}/delete"),
		view("/api/servers/{id}/icon", s.rawGet("/v1/servers/{id}/icon", "image/png")),
		{"POST", "/api/servers/{id}/icon", needSessionCSRF, actManageServers, s.rawUpload("/v1/servers/{id}/icon", "image/png")},
		sg("/api/servers/{id}/logs", "/v1/servers/{id}/logs"),
		smAs(actConsole, "POST", "/api/servers/{id}/command", "/v1/servers/{id}/command"),
		view("/api/servers/{id}/whitelist", s.hWhitelist),
		smAs(actManagePlayers, "POST", "/api/servers/{id}/whitelist", "/v1/servers/{id}/whitelist"),
		{"DELETE", "/api/servers/{id}/whitelist/{name}", needSessionCSRF, actManagePlayers, s.hWhitelistRemove},
		sg("/api/servers/{id}/operators", "/v1/servers/{id}/operators"),
		smAs(actManagePlayers, "POST", "/api/servers/{id}/operators", "/v1/servers/{id}/operators"),
		smAs(actManagePlayers, "DELETE", "/api/servers/{id}/operators/{name}", "/v1/servers/{id}/operators/{name}"),
		smAs(actManagePlayers, "POST", "/api/servers/{id}/kick", "/v1/servers/{id}/kick"),
		sg("/api/servers/{id}/metrics", "/v1/servers/{id}/metrics"),
		sg("/api/servers/{id}/running", "/v1/servers/{id}/running"),
		sg("/api/servers/{id}/memory", "/v1/servers/{id}/memory"),
		sm("POST", "/api/servers/{id}/saving/resume", "/v1/servers/{id}/saving/resume"),
		sm("POST", "/api/servers/{id}/addons/remove-file", "/v1/servers/{id}/addons/remove-file"),
		sg("/api/servers/{id}/players/sessions", "/v1/servers/{id}/players/sessions"),
		sg("/api/servers/{id}/players/summary", "/v1/servers/{id}/players/summary"),
		sg("/api/servers/{id}/events", "/v1/servers/{id}/events"),
		view("/api/servers/{id}/activity", s.hServerActivity),
		sg("/api/servers/{id}/backups", "/v1/servers/{id}/backups"),
		smAs(actMakeBackups, "POST", "/api/servers/{id}/backups", "/v1/servers/{id}/backups"),
		smAs(actMakeBackups, "POST", "/api/servers/{id}/backups/{bid}/verify", "/v1/servers/{id}/backups/{bid}/verify"),
		{"GET", "/api/servers/{id}/backups/{bid}/download", needSession, actMakeBackups, s.hDownload},
		sm("DELETE", "/api/servers/{id}/backups/{bid}", "/v1/servers/{id}/backups/{bid}"),
		smAs(actRestore, "POST", "/api/servers/{id}/backups/{bid}/restore", "/v1/servers/{id}/backups/{bid}/restore"),
		{"POST", "/api/servers/{id}/restore/upload", needSessionCSRF, actRestore, s.rawUpload("/v1/servers/{id}/restore/upload", "application/gzip")},
		view("/api/players/{name}/head", s.hHead),
		view("/api/server", s.hLegacyStatus),
		// Follow-ups after 0.3.0.
		sg("/api/servers/{id}/world-copies", "/v1/servers/{id}/world-copies"),
		sm("DELETE", "/api/servers/{id}/world-copies/{name}", "/v1/servers/{id}/world-copies/{name}"),
		// Wave 1: plugins and mods, map pre-generation, data and resource packs.
		sg("/api/servers/{id}/addons", "/v1/servers/{id}/addons"),
		sg("/api/servers/{id}/addons/checks", "/v1/servers/{id}/addons/checks"),
		sg("/api/servers/{id}/addons/search", "/v1/servers/{id}/addons/search"),
		sg("/api/servers/{id}/addons/curated", "/v1/servers/{id}/addons/curated"),
		sg("/api/servers/{id}/addons/project/{source}/{project}", "/v1/servers/{id}/addons/project/{source}/{project}"),
		sg("/api/servers/{id}/addons/project/{source}/{project}/removal", "/v1/servers/{id}/addons/project/{source}/{project}/removal"),
		view("/api/servers/{id}/addons/icon", s.hAddonIcon),
		sm("POST", "/api/servers/{id}/addons/install", "/v1/servers/{id}/addons/install"),
		sm("POST", "/api/servers/{id}/addons/update/plan", "/v1/servers/{id}/addons/update/plan"),
		sm("POST", "/api/servers/{id}/addons/update", "/v1/servers/{id}/addons/update"),
		sm("POST", "/api/servers/{id}/addons/remove", "/v1/servers/{id}/addons/remove"),
		sm("POST", "/api/servers/{id}/addons/adopt", "/v1/servers/{id}/addons/adopt"),
		sm("POST", "/api/servers/{id}/addons/forget", "/v1/servers/{id}/addons/forget"),
		sg("/api/servers/{id}/pregen", "/v1/servers/{id}/pregen"),
		sm("POST", "/api/servers/{id}/pregen/start", "/v1/servers/{id}/pregen/start"),
		sm("POST", "/api/servers/{id}/pregen/pause", "/v1/servers/{id}/pregen/pause"),
		sm("POST", "/api/servers/{id}/pregen/continue", "/v1/servers/{id}/pregen/continue"),
		sm("POST", "/api/servers/{id}/pregen/cancel", "/v1/servers/{id}/pregen/cancel"),
		sg("/api/servers/{id}/datapacks", "/v1/servers/{id}/datapacks"),
		{"POST", "/api/servers/{id}/datapacks", needSessionCSRF, actManageServers, s.rawUpload("/v1/servers/{id}/datapacks", "application/zip", "name")},
		view("/api/servers/{id}/datapacks/{name}/icon", s.rawGet("/v1/servers/{id}/datapacks/{name}/icon", "image/png")),
		sm("POST", "/api/servers/{id}/datapacks/{name}/enable", "/v1/servers/{id}/datapacks/{name}/enable"),
		sm("POST", "/api/servers/{id}/datapacks/{name}/disable", "/v1/servers/{id}/datapacks/{name}/disable"),
		sm("DELETE", "/api/servers/{id}/datapacks/{name}", "/v1/servers/{id}/datapacks/{name}"),
		sg("/api/servers/{id}/resourcepack", "/v1/servers/{id}/resourcepack"),
		{"POST", "/api/servers/{id}/resourcepack", needSessionCSRF, actManageServers, s.hResourcePackUpload},
		sm("POST", "/api/servers/{id}/resourcepack/settings", "/v1/servers/{id}/resourcepack/settings"),
		sm("DELETE", "/api/servers/{id}/resourcepack", "/v1/servers/{id}/resourcepack"),
		view("/api/servers/{id}/resourcepack/icon", s.rawGet("/v1/servers/{id}/resourcepack/icon", "image/png")),

		// Wave 4: every server type.
		mg("/api/machines/{mid}/catalog/builds", "/v1/catalog/builds"),
		sm("POST", "/api/servers/{id}/software/reinstall", "/v1/servers/{id}/software/reinstall"),
		// Wave 4: modpacks.
		mg("/api/machines/{mid}/modpacks", "/v1/modpacks"),
		mg("/api/machines/{mid}/modpacks/{source}/{project}", "/v1/modpacks/{source}/{project}"),
		mg("/api/machines/{mid}/modpacks/{source}/{project}/versions/{version}/preview", "/v1/modpacks/{source}/{project}/versions/{version}/preview"),
		view("/api/machines/{mid}/modpacks/icon", s.hAddonIcon),
		// Wave 4: add-on sources. The CurseForge key is the machine's, so only
		// those who may manage the machine change it.
		mg("/api/machines/{mid}/addon-sources", "/v1/addon-sources"),
		mm("POST", "/api/machines/{mid}/addon-sources/curseforge", "/v1/addon-sources/curseforge", actManageAddonSources),
		mm("DELETE", "/api/machines/{mid}/addon-sources/curseforge", "/v1/addon-sources/curseforge", actManageAddonSources),
		// Wave 4: templates.
		sg("/api/servers/{id}/template", "/v1/servers/{id}/template"),
		sm("POST", "/api/servers/{id}/template/retry", "/v1/servers/{id}/template/retry"),
		{"POST", "/api/machines/{mid}/templates/plan", needSessionCSRF, actManageServers, s.rawUpload("/v1/templates/plan", "text/plain")},
		// Wave 4: sharing the pack with friends; the public page is in
		// publicRoutes.
		sg("/api/servers/{id}/mods/share", "/v1/servers/{id}/mods/share"),
		sm("POST", "/api/servers/{id}/mods/share", "/v1/servers/{id}/mods/share"),
		view("/api/servers/{id}/mods/share.mrpack", s.hPackShareFile),

		// Wave 7: schedules, sleep, backup rules and copies somewhere else, disk space.
		sg("/api/servers/{id}/schedules", "/v1/servers/{id}/schedules"),
		sm("POST", "/api/servers/{id}/schedules", "/v1/servers/{id}/schedules"),
		sm("POST", "/api/servers/{id}/schedules/preview", "/v1/servers/{id}/schedules/preview"),
		sg("/api/servers/{id}/schedules/runs", "/v1/servers/{id}/schedules/runs"),
		sm("POST", "/api/servers/{id}/schedules/{sid}", "/v1/servers/{id}/schedules/{sid}"),
		sm("DELETE", "/api/servers/{id}/schedules/{sid}", "/v1/servers/{id}/schedules/{sid}"),
		sg("/api/servers/{id}/sleep", "/v1/servers/{id}/sleep"),
		sm("POST", "/api/servers/{id}/sleep", "/v1/servers/{id}/sleep"),
		sg("/api/servers/{id}/backup-rules", "/v1/servers/{id}/backup-rules"),
		sm("POST", "/api/servers/{id}/backup-rules", "/v1/servers/{id}/backup-rules"),
		sm("POST", "/api/servers/{id}/backup-rules/estimate", "/v1/servers/{id}/backup-rules/estimate"),
		sg("/api/servers/{id}/offsite", "/v1/servers/{id}/offsite"),
		{"POST", "/api/servers/{id}/offsite", needSessionCSRF, actManageBackupCopies, s.serverProxy("POST", "/v1/servers/{id}/offsite")},
		{"POST", "/api/servers/{id}/offsite/test", needSessionCSRF, actManageBackupCopies, s.serverProxy("POST", "/v1/servers/{id}/offsite/test")},
		{"POST", "/api/servers/{id}/offsite/ssh-key", needSessionCSRF, actManageBackupCopies, s.serverProxy("POST", "/v1/servers/{id}/offsite/ssh-key")},
		smAs(actMakeBackups, "POST", "/api/servers/{id}/offsite/retry", "/v1/servers/{id}/offsite/retry"),
		{"GET", "/api/servers/{id}/offsite/recovery-key", needSession, actRecoveryKey, s.hRecoveryKey},
		{"POST", "/api/servers/{id}/offsite/new-key", needSessionCSRF, actRecoveryKey, s.serverProxy("POST", "/v1/servers/{id}/offsite/new-key")},
		sg("/api/servers/{id}/offsite/copies", "/v1/servers/{id}/offsite/copies"),
		smAs(actMakeBackups, "POST", "/api/servers/{id}/offsite/copies/{name}/check", "/v1/servers/{id}/offsite/copies/{name}/check"),
		{"DELETE", "/api/servers/{id}/offsite/copies/{name}", needSessionCSRF, actManageBackupCopies, s.serverProxy("DELETE", "/v1/servers/{id}/offsite/copies/{name}")},
		smAs(actRestore, "POST", "/api/servers/{id}/offsite/restore", "/v1/servers/{id}/offsite/restore"),
		smAs(actRestore, "POST", "/api/servers/{id}/offsite/restore/cancel", "/v1/servers/{id}/offsite/restore/cancel"),
		mm("POST", "/api/machines/{mid}/offsite/recover", "/v1/offsite/recover", actRecoverBackups),
		mm("POST", "/api/machines/{mid}/offsite/recover/restore", "/v1/offsite/recover/restore", actRecoverBackups),
		// The Disk space page lists every server's use of the disk.
		{"GET", "/api/machines/{mid}/disk", needSession, actView, everyServer(s.machineProxy("GET", "/v1/disk"))},
		mm("POST", "/api/machines/{mid}/disk/clean", "/v1/disk/clean", actManageMachine),
	}
	// Wave 5: invite links and join requests, player profiles, the team
	// and Discord.
	routes = append(routes, []Route{
		{"GET", "/api/servers/{id}/invites", needSession, actManagePlayers, s.hInvites},
		{"POST", "/api/servers/{id}/invites", needSessionCSRF, actManagePlayers, s.hInviteCreate},
		{"DELETE", "/api/servers/{id}/invites/{invite}", needSessionCSRF, actManagePlayers, s.hInviteRevoke},
		{"GET", "/api/servers/{id}/join-requests", needSession, actManagePlayers, s.hJoinRequests},
		{"POST", "/api/servers/{id}/join-requests/{request}/approve", needSessionCSRF, actManagePlayers, s.hJoinRequestApprove},
		{"POST", "/api/servers/{id}/join-requests/{request}/decline", needSessionCSRF, actManagePlayers, s.hJoinRequestDecline},
		view("/api/servers/{id}/players/profile", s.hProfile),
		smAs(actManagePlayers, "POST", "/api/servers/{id}/players/message", "/v1/servers/{id}/players/message"),
		smAs(actManagePlayers, "POST", "/api/servers/{id}/ban", "/v1/servers/{id}/ban"),
		{"GET", "/api/team", needSession, actManageTeam, s.hTeam},
		{"POST", "/api/team/invites", needSessionCSRF, actManageTeam, s.hTeamInviteCreate},
		{"PUT", "/api/team/invites/{invite}", needSessionCSRF, actManageTeam, s.hTeamInviteEdit},
		{"DELETE", "/api/team/invites/{invite}", needSessionCSRF, actManageTeam, s.hTeamInviteRevoke},
		{"PUT", "/api/team/members/{uid}", needSessionCSRF, actManageTeam, s.hTeamMemberEdit},
		{"DELETE", "/api/team/members/{uid}", needSessionCSRF, actManageTeam, s.hTeamMemberRemove},
		{"POST", "/api/team/members/{uid}/confirm-admin", needSessionCSRF, actManageTeam, s.hTeamConfirmAdmin},
		{"GET", "/api/discord", needSession, actManageMachine, s.discordProxy("GET", "/v1/discord")},
		{"POST", "/api/discord/connect", needSessionCSRF, actManageMachine, s.discordProxy("POST", "/v1/discord/connect")},
		{"PUT", "/api/discord", needSessionCSRF, actManageMachine, s.discordProxy("PUT", "/v1/discord")},
		{"DELETE", "/api/discord", needSessionCSRF, actManageMachine, s.discordProxy("DELETE", "/v1/discord")},
		{"POST", "/api/discord/test", needSessionCSRF, actManageMachine, s.discordProxy("POST", "/v1/discord/test")},
	}...)
	// Wave 6: each server's live map, and worlds people upload, for a new
	// server or to replace a server's world. An upload for a new server, and
	// making the server from it, need the same rights as creating a server.
	routes = append(routes, []Route{
		sg("/api/servers/{id}/map", "/v1/servers/{id}/map"),
		view("/api/servers/{id}/map/worlds", s.mapProxy("/v1/servers/{id}/map/worlds")),
		view("/api/servers/{id}/map/players", s.mapProxy("/v1/servers/{id}/map/players")),
		view("/api/servers/{id}/map/tiles/{world}/{zoom}/{tile}", s.mapProxy("/v1/servers/{id}/map/tiles/{world}/{zoom}/{tile}")),
		sm("POST", "/api/servers/{id}/map/enable", "/v1/servers/{id}/map/enable"),
		sm("POST", "/api/servers/{id}/map/disable", "/v1/servers/{id}/map/disable"),
		sm("POST", "/api/servers/{id}/map/share", "/v1/servers/{id}/map/share"),
		sm("POST", "/api/servers/{id}/map/restart-later", "/v1/servers/{id}/map/restart-later"),
		sm("POST", "/api/servers/{id}/world-imports", "/v1/servers/{id}/world-imports"),
		mm("POST", "/api/machines/{mid}/world-imports", "/v1/world-imports", actCreateServers),
		mg("/api/machines/{mid}/world-imports/{imp}", "/v1/world-imports/{imp}"),
		mm("DELETE", "/api/machines/{mid}/world-imports/{imp}", "/v1/world-imports/{imp}", actManageServers),
		mm("POST", "/api/machines/{mid}/world-imports/{imp}/files", "/v1/world-imports/{imp}/files", actManageServers),
		{"PUT", "/api/machines/{mid}/world-imports/{imp}/files/{n}", needSessionCSRF, actManageServers, s.hWorldUpload},
		{"POST", "/api/machines/{mid}/world-imports/{imp}/inspect", needSessionCSRF, actManageServers, s.forwardLong("/v1/world-imports/{imp}/inspect")},
		{"POST", "/api/machines/{mid}/world-imports/{imp}/preview", needSessionCSRF, actManageServers, s.forwardLong("/v1/world-imports/{imp}/preview")},
		{"POST", "/api/machines/{mid}/world-imports/{imp}/apply", needSessionCSRF, actManageServers, s.forwardLong("/v1/world-imports/{imp}/apply")},
		{"POST", "/api/machines/{mid}/world-imports/{imp}/create", needSessionCSRF, actCreateServers, s.forwardLong("/v1/world-imports/{imp}/create")},
	}...)
	return routes
}

// Handler returns the complete panel handler (API, health check and UI).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.Routes() {
		mux.HandleFunc(rt.Method+" "+rt.Pattern, s.guard(rt))
	}
	for _, rt := range s.public.routes {
		mux.Handle(rt.prefix, rt.handler)
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "Unknown API route.", "")
	})
	mux.Handle("/mcp", s.mcpHTTP)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/", s.serveUI)
	return s.securityHeaders(s.logRequests(mux))
}

// guard enforces the route's authentication level before any handler code
// runs. Every failure path denies the request.
func (s *Server) guard(rt Route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch rt.Level {
		case public:
			rt.handler(w, r, nil)
		case publicMutation:
			if !s.sameOrigin(r) || r.Header.Get("X-Requested-With") != "playkeeper" {
				writeErr(w, http.StatusForbidden, api.CodeForbidden, "Cross-site request refused.", "")
				return
			}
			rt.handler(w, r, nil)
		case pendingSession:
			if !s.sameOrigin(r) || r.Header.Get("X-Requested-With") != "playkeeper" {
				writeErr(w, http.StatusForbidden, api.CodeForbidden, "Cross-site request refused.", "")
				return
			}
			p, err := s.pendingFrom(r)
			if err != nil {
				clearPendingCookie(w)
				writeErr(w, http.StatusUnauthorized, api.CodeUnauthorized, "Please sign in again.", "")
				return
			}
			rt.handler(w, r, &p)
		case needSession, needSessionCSRF:
			sess, err := s.sessionFrom(r)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, api.CodeUnauthorized, "Please sign in.", "")
				return
			}
			if rt.Level == needSessionCSRF {
				tok := r.Header.Get("X-CSRF-Token")
				if !s.sameOrigin(r) || tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(sess.CSRF)) != 1 {
					writeErr(w, http.StatusForbidden, api.CodeForbidden, "Security token missing or invalid. Reload the page and try again.", "")
					return
				}
				bucket := s.control
				if previewRoutes[rt.Method+" "+rt.Pattern] {
					bucket = s.previews
				}
				if ok, wait := bucket.allow("session:" + sess.IDHash); !ok {
					w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
					writeErr(w, http.StatusTooManyRequests, api.CodeRateLimited, "Too many actions in a short time. Wait a moment.", "")
					return
				}
			}
			acct, err := s.access(sess.User)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
				return
			}
			sess.Access = acct
			if err := permit(acct, rt.Act, r.PathValue("id")); err != nil {
				switch rt.Act {
				case actRecoveryKey:
					s.audit(sess.User.Username, "offsite.recovery_key", r.PathValue("id"), "refused", "not allowed to hold backup keys")
				case actRecoverBackups:
					s.audit(sess.User.Username, "offsite.recover", r.PathValue("mid"), "refused", "not allowed to bring servers back from copies")
				}
				writeRefusal(w, err)
				return
			}
			rt.handler(w, r, &sess)
		default:
			writeErr(w, http.StatusForbidden, api.CodeForbidden, "Forbidden.", "")
		}
	}
}

// sameOrigin rejects browser requests whose Origin is not this panel.
func (s *Server) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return r.Header.Get("Sec-Fetch-Site") == "" || r.Header.Get("Sec-Fetch-Site") == "same-origin"
	}
	u, err := url.Parse(o)
	return err == nil && u.Scheme == "https" && u.Host == r.Host
}

func (s *Server) sessionFrom(r *http.Request) (session, error) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return session{}, errNoSession
	}
	return s.lookupSession(c.Value)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(s.opts.AbsoluteTimeout.Seconds())})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; font-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		h.Set("Strict-Transport-Security", hsts(r.Host))
		next.ServeHTTP(w, r)
	})
}

// hsts is the Strict-Transport-Security value for a request's host.
// Browsers ignore it for IP addresses, so the dashboard's IP address always
// stays reachable. On a name it lasts a day: if the name's certificate ever
// lapses, the browser refuses the self-signed fallback for at most a day
// after the last visit, instead of a year.
func hsts(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return "max-age=31536000"
	}
	return "max-age=86400"
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logRequests records method, path, status and duration. It never logs query
// strings, headers or bodies (which may carry codes, cookies or passwords),
// nor more of a public path than its route's prefix, and invite codes in
// paths are redacted.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") || sw.status >= 400 {
			s.log.Info("request", "method", r.Method, "path", s.public.logPath(invites.RedactPath(r.URL.Path)), "status", sw.status, "ms", time.Since(start).Milliseconds())
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg, hint string) {
	writeJSON(w, status, api.Error{Error: msg, Code: code, Hint: hint})
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected trailing data")
	}
	return nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// addressKey is the client's address for limits: an IPv4 address or an
// IPv6 /64, which one household usually has to itself.
func (s *Server) addressKey(r *http.Request) string {
	ip, err := netip.ParseAddr(clientIP(r))
	if err != nil {
		return clientIP(r)
	}
	return invites.AddressKey(ip)
}

// limitKey is the sign-in limiter's key for a client address: an IPv6
// client counts by its /64, since one host can send from every address in
// it.
func limitKey(host string) string {
	a, err := netip.ParseAddr(host)
	if err != nil {
		return "ip:" + host
	}
	if a = a.Unmap(); a.Is6() {
		if p, err := a.Prefix(64); err == nil {
			return "net:" + p.String()
		}
	}
	return "ip:" + a.String()
}

// --- public routes ---

func (s *Server) hHealth(w http.ResponseWriter, r *http.Request, _ *session) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version.Version})
}

func (s *Server) hSetupStatus(w http.ResponseWriter, r *http.Request, _ *session) {
	n, err := s.userCount()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	writeJSON(w, http.StatusOK, api.SetupStatus{NeedsSetup: n == 0, Machine: s.localMachineName(), Version: version.Version})
}

type credentials struct {
	Token    string `json:"token,omitempty"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) rateLimitIP(w http.ResponseWriter, r *http.Request) bool {
	if ok, wait := s.loginIP.allow(limitKey(clientIP(r))); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeErr(w, http.StatusTooManyRequests, api.CodeRateLimited, "Too many attempts. Try again in a few minutes.", "")
		return false
	}
	return true
}

func (s *Server) hSetup(w http.ResponseWriter, r *http.Request, _ *session) {
	if !s.rateLimitIP(w, r) {
		return
	}
	n, err := s.userCount()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if n > 0 {
		writeErr(w, http.StatusConflict, api.CodeConflict, "Setup is already complete. Sign in instead.", "")
		return
	}
	var c credentials
	if err := decodeJSON(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	if err := s.checkSetupToken(c.Token); err != nil {
		s.audit("(setup)", "setup", "admin", "refused", "invalid or expired setup code")
		writeErr(w, http.StatusForbidden, api.CodeForbidden, err.Error(), "")
		return
	}
	if err := validUsername(c.Username); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, err.Error(), "")
		return
	}
	if err := validPassword(c.Password, c.Username); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, err.Error(), "")
		return
	}
	u, err := s.createFirstAdmin(c.Username, c.Password)
	if err != nil {
		writeErr(w, http.StatusConflict, api.CodeConflict, "Setup is already complete. Sign in instead.", "")
		return
	}
	s.consumeSetupToken()
	token, sess, err := s.newSession(u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not start a session.", "")
		return
	}
	s.audit(u.Username, "setup", "admin", "succeeded", "first admin account created")
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, s.meBody(sess))
}

func (s *Server) hLogin(w http.ResponseWriter, r *http.Request, _ *session) {
	if !s.rateLimitIP(w, r) {
		return
	}
	var c credentials
	if err := decodeJSON(r, &c); err != nil || c.Token != "" {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	account := "user:" + strings.ToLower(c.Username)
	key := account + "@" + s.addressKey(r)
	locked, wait := s.locks.locked(key)
	if !locked {
		var ready bool
		ready, wait = s.loginUser.ready(account)
		locked = !ready
	}
	if locked {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeErr(w, http.StatusTooManyRequests, api.CodeRateLimited, "Too many failed sign-ins for this account. Try again later.", "")
		return
	}
	u, ok := s.authenticate(c.Username, c.Password)
	if !ok {
		s.locks.fail(key)
		s.loginUser.allow(account)
		actor := "(unknown user)"
		if u.Username != "" {
			actor = u.Username
		} else if _, err := s.userByName(c.Username); err == nil {
			actor = c.Username
		}
		s.audit(actor, "login", "panel", "failed", "")
		writeErr(w, http.StatusUnauthorized, api.CodeUnauthorized, "Wrong username or password.", "")
		return
	}
	s.locks.succeed(key)
	if started, err := s.secondFactorNeeded(w, u); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not start the second sign-in step.", "")
		return
	} else if started {
		return
	}
	token, sess, err := s.newSession(u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not start a session.", "")
		return
	}
	s.audit(u.Username, "login", "panel", "succeeded", "")
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, s.meBody(sess))
}

func (s *Server) userByName(name string) (user, error) {
	var u user
	err := s.db.QueryRow(`SELECT id, username FROM users WHERE username = ?`, name).Scan(&u.ID, &u.Username)
	return u, err
}

// accessBody is what the signed-in account may do, for the UI to show only
// what works. The panel checks every request anyway.
type accessBody struct {
	ProjectID string        `json:"projectId,omitempty"`
	Team      string        `json:"team,omitempty"` // "" while the project has the default name
	Role      string        `json:"role"`
	Servers   invites.Scope `json:"servers"`
	TwoFactor bool          `json:"twoFactor"`
	// NeedsTwoFactor is set for an admin whose admin rights wait until
	// two-factor sign-in is on; AwaitingConfirmation once it's on and they
	// wait for the owner or an admin to confirm them.
	NeedsTwoFactor       bool     `json:"needsTwoFactor,omitempty"`
	AwaitingConfirmation bool     `json:"awaitingConfirmation,omitempty"`
	Can                  []action `json:"can"`
}

func (s *Server) meBody(sess session) map[string]any {
	a := sess.Access
	if a.UserID == 0 {
		a, _ = s.access(sess.User)
	}
	body := map[string]any{
		"user":      map[string]string{"username": sess.User.Username, "role": sess.User.Role},
		"csrfToken": sess.CSRF,
		"access": accessBody{ProjectID: a.ProjectID, Team: s.teamName(a.ProjectID), Role: a.ProjectRole, Servers: a.Servers, TwoFactor: a.FactorOn,
			NeedsTwoFactor:       invites.RequiresTwoFactor(a.InstallRole, a.ProjectRole) && !a.FactorOn,
			AwaitingConfirmation: a.awaitingConfirmation(), Can: a.can()},
		"expiresAt":          sess.ExpiresAt.UTC(),
		"idleTimeoutSeconds": int(s.opts.IdleTimeout.Seconds()),
		"version":            version.Version,
	}
	var changed int64
	if s.db.QueryRow(`SELECT password_changed_at FROM users WHERE id = ?`, sess.User.ID).Scan(&changed) == nil {
		body["passwordChangedAt"] = msTime(changed)
	}
	return body
}

func (s *Server) hMe(w http.ResponseWriter, r *http.Request, sess *session) {
	writeJSON(w, http.StatusOK, s.meBody(*sess))
}

func (s *Server) hLogout(w http.ResponseWriter, r *http.Request, sess *session) {
	s.deleteSession(sess.IDHash)
	s.audit(sess.User.Username, "logout", "panel", "succeeded", "")
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) hLogoutAll(w http.ResponseWriter, r *http.Request, sess *session) {
	s.deleteUserSessions(sess.User.ID)
	s.audit(sess.User.Username, "logout-all", "panel", "succeeded", "")
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) hPassword(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	if _, ok := s.authenticate(sess.User.Username, req.CurrentPassword); !ok {
		s.audit(sess.User.Username, "password.change", "panel", "failed", "wrong current password")
		writeErr(w, http.StatusForbidden, api.CodeForbidden, "Your current password is not correct.", "")
		return
	}
	if err := validPassword(req.NewPassword, sess.User.Username); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, err.Error(), "")
		return
	}
	h, err := hashPassword(req.NewPassword)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not change the password.", "")
		return
	}
	if _, err := s.db.Exec(`UPDATE users SET password_hash = ?, password_changed_at = ? WHERE id = ?`, h, s.now().UnixMilli(), sess.User.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not change the password.", "")
		return
	}
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND id_hash != ?`, sess.User.ID, sess.IDHash)
	_, _ = s.db.Exec(`DELETE FROM pending_logins WHERE user_id = ?`, sess.User.ID)
	s.audit(sess.User.Username, "password.change", "panel", "succeeded", "other sessions signed out")
	w.WriteHeader(http.StatusNoContent)
}

// ResetAdmin replaces the admin password from the host (root CLI recovery).
func (s *Server) ResetAdmin(username, password string) error {
	if err := validPassword(password, username); err != nil {
		return err
	}
	h, err := hashPassword(password)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE users SET password_hash = ?, password_changed_at = ? WHERE username = ?`, h, s.now().UnixMilli(), username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("no such user: " + username)
	}
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE username = ?)`, username)
	_, _ = s.db.Exec(`DELETE FROM pending_logins WHERE user_id = (SELECT id FROM users WHERE username = ?)`, username)
	s.audit("root@host", "password.reset", username, "succeeded", "reset from the server command line")
	if u, err := s.userByName(username); err == nil {
		s.revokeAccountTokens(u.ID, "root@host", "its account's password was reset from the server command line")
	}
	return nil
}

// Usernames lists admin accounts (root CLI).
func (s *Server) Usernames() ([]string, error) {
	rows, err := s.db.Query(`SELECT username FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if rows.Scan(&u) == nil {
			out = append(out, u)
		}
	}
	return out, rows.Err()
}

func (s *Server) hAudit(w http.ResponseWriter, r *http.Request, sess *session) {
	s.flushRefusals(false)
	rows, err := s.db.Query(`SELECT id, ts, actor, action, target, result, detail FROM audit ORDER BY id DESC LIMIT 200`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	defer rows.Close()
	type entry struct {
		api.AuditEntry
		Source    string `json:"source"`
		MachineID string `json:"machineId,omitempty"`
		// ActorKind and ActorName say who a token or command-line actor is.
		ActorKind string `json:"actorKind,omitempty"`
		ActorName string `json:"actorName,omitempty"`
	}
	out := []entry{}
	for rows.Next() {
		var e entry
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.Actor, &e.Action, &e.Target, &e.Result, &e.Detail); err == nil {
			e.TS = time.UnixMilli(ts).UTC()
			e.Source = "panel"
			out = append(out, e)
		}
	}
	rows.Close()
	machines, _ := s.machines()
	if len(machines) == 0 {
		machines = []machine{{Kind: localKind, agent: s.agent}}
	}
	audits := make([][]api.AuditEntry, len(machines))
	var wg sync.WaitGroup
	for i, m := range machines {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(r.Context(), machineTimeout)
			defer cancel()
			m.agent.Do(ctx, "GET", "/v1/audit", url.Values{"limit": {"200"}}, nil, &audits[i])
		})
	}
	wg.Wait()
	for i, m := range machines {
		for _, a := range audits[i] {
			out = append(out, entry{AuditEntry: a, Source: "agent", MachineID: m.ID})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS.After(out[j].TS) })
	if len(out) > 300 {
		out = out[:300]
	}
	names := s.actorNames()
	for i := range out {
		out[i].ActorKind, out[i].ActorName = actorInfo(out[i].Actor, names)
	}
	writeJSON(w, http.StatusOK, out)
}

// --- agent proxy ---

var pathKeys = []string{"id", "name", "bid", "rid", "op", "sid", "source", "project", "version"}

func agentPath(pattern string, r *http.Request) string {
	out := pattern
	for _, key := range pathKeys {
		out = strings.ReplaceAll(out, "{"+key+"}", url.PathEscape(r.PathValue(key)))
	}
	return out
}

func (s *Server) agentFailure(w http.ResponseWriter, err error) {
	status, body := failureOf(err)
	writeJSON(w, status, body)
}

// target is the machine a request goes to: the one named by {mid}, or the
// one that runs the server named by {id}.
func (s *Server) target(w http.ResponseWriter, r *http.Request) (machine, bool) {
	if r.PathValue("mid") != "" {
		return s.machineFromPath(w, r)
	}
	id := r.PathValue("id")
	if !reMachineID.MatchString(id) {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid server id.", "")
		return machine{}, false
	}
	m, err := s.machineForServer(id)
	switch {
	case errors.Is(err, errDisputed):
		writeErr(w, http.StatusConflict, codeServerDisputed, "Two machines say they run this server, so the dashboard sends its requests to neither.",
			"Remove the machine that shouldn't list it in Settings › Machines.")
		return machine{}, false
	case errors.Is(err, errServerMachine):
		s.agentFailure(w, err)
		return machine{}, false
	case err != nil:
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "Server not found.", "")
		return machine{}, false
	}
	return m, true
}

func (s *Server) serverProxy(method, pattern string) func(http.ResponseWriter, *http.Request, *session) {
	return s.forward(method, pattern)
}

func (s *Server) machineProxy(method, pattern string) func(http.ResponseWriter, *http.Request, *session) {
	return s.forward(method, pattern)
}

// addressProxy forwards an address route with panelHost, the host the
// dashboard was opened with, in the query or body. The agent keeps it when
// it is a public IP address: behind NAT that address is on no network
// interface, and an own domain's A record needs it.
func (s *Server) addressProxy(method, pattern string) func(http.ResponseWriter, *http.Request, *session) {
	return s.forwardTo(method, pattern, true, nil)
}

// dashboardAddress refuses to give a joined machine a free name or an own
// domain, or to keep one: they stay with the dashboard's machine, and a
// joined machine's servers join at its IP and port. Letting one go works.
func (s *Server) dashboardAddress(next func(http.ResponseWriter, *http.Request, *session)) func(http.ResponseWriter, *http.Request, *session) {
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		m, ok := s.machineFromPath(w, r)
		if !ok {
			return
		}
		if m.Kind == remoteKind {
			writeErr(w, http.StatusConflict, api.CodeConflict, "Free names and own domains are for the dashboard's machine.",
				"Players join "+m.Name+"'s servers at its IP address and each server's port.")
			return
		}
		next(w, r, sess)
	}
}

// claimCreatedBy records the server a machine route just created on a
// joined machine.
func (s *Server) claimCreatedBy(m machine, _ *session, raw json.RawMessage) { s.claimCreated(m, raw) }

// recordUpdate puts a dashboard-started update in a joined machine's events.
func (s *Server) recordUpdate(m machine, sess *session, _ json.RawMessage) {
	if m.Kind == remoteKind {
		s.machineEvent(m.ID, s.now(), "machine.update", sess.User.Username, "", "")
	}
}

// forward sends the request to its machine's agent. GETs pass the query on;
// JSON bodies get the signed-in account stamped as actor (the agent checks
// every field and rejects unknown ones); DELETEs pass the actor in the query.
// Machine links read the actor from the request's context.
func (s *Server) forward(method, pattern string) func(http.ResponseWriter, *http.Request, *session) {
	return s.forwardTo(method, pattern, false, nil)
}

// forwardThen is forward, then calls then with the answer of a request that
// succeeded.
func (s *Server) forwardThen(method, pattern string, then func(machine, *session, json.RawMessage)) func(http.ResponseWriter, *http.Request, *session) {
	return s.forwardTo(method, pattern, false, then)
}

// asActor is ctx for requests to a machine's agent made on actor's behalf. A
// machine link refuses a change that names no actor, so every request that
// changes something on a machine goes with one.
func asActor(ctx context.Context, actor string) context.Context {
	return machinelink.WithActor(ctx, actor)
}

// forwardTo is forward, also stamping panelHost when withHost is set and
// calling then (if set) after a request that succeeded. What the panel stamps
// replaces anything the browser sent under the same name.
func (s *Server) forwardTo(method, pattern string, withHost bool, then func(machine, *session, json.RawMessage)) func(http.ResponseWriter, *http.Request, *session) {
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		m, ok := s.target(w, r)
		if !ok {
			return
		}
		ctx := asActor(r.Context(), sess.User.Username)
		path := agentPath(pattern, r)
		// The host the dashboard was opened with is its own machine's
		// address: a joined machine would point its name at the dashboard.
		host := ""
		if withHost && m.Kind != remoteKind {
			host = r.Host
		}
		var raw json.RawMessage
		var status int
		var err error
		switch method {
		case "GET":
			q := r.URL.Query()
			if withHost {
				q.Del("panelHost")
				if host != "" {
					q.Set("panelHost", host)
				}
			}
			status, err = m.agent.Do(ctx, "GET", path, q, nil, &raw)
		case "DELETE":
			status, err = m.agent.Do(ctx, "DELETE", path, url.Values{"actor": {sess.User.Username}}, nil, &raw)
		default:
			body := map[string]any{}
			b, rerr := io.ReadAll(io.LimitReader(r.Body, 64<<10))
			if rerr != nil {
				writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request body.", "")
				return
			}
			if len(bytes.TrimSpace(b)) > 0 {
				if err := json.Unmarshal(b, &body); err != nil {
					writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Request body must be a JSON object.", "")
					return
				}
			}
			if withHost {
				delete(body, "panelHost")
				if host != "" {
					body["panelHost"] = host
				}
			}
			body["actor"] = sess.User.Username
			status, err = m.agent.Do(ctx, method, path, nil, body, &raw)
		}
		if err != nil {
			s.agentFailure(w, err)
			return
		}
		if then != nil {
			then(m, sess, raw)
		}
		if status == http.StatusNoContent || len(raw) == 0 {
			w.WriteHeader(status)
			return
		}
		writeJSON(w, status, raw)
	}
}

func (s *Server) hServerActivity(w http.ResponseWriter, r *http.Request, _ *session) {
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	q := url.Values{"server": {r.PathValue("id")}}
	if l := r.URL.Query().Get("limit"); l != "" {
		q.Set("limit", l)
	}
	s.activity(w, r, m, q)
}

// withActorNames is an activity feed as the dashboard reads it, with the
// names of the tokens and command-line accounts that acted.
func (s *Server) withActorNames(list []api.Activity) []map[string]any {
	names := s.actorNames()
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		raw, err := json.Marshal(a)
		var e map[string]any
		if err != nil || json.Unmarshal(raw, &e) != nil {
			continue
		}
		if kind, name := actorInfo(a.Actor, names); kind != "" {
			e["actorKind"], e["actorName"] = kind, name
		}
		out = append(out, e)
	}
	return out
}

// activity relays a machine's activity feed, with the names of the tokens
// and command-line accounts that acted.
func (s *Server) activity(w http.ResponseWriter, r *http.Request, m machine, q url.Values) {
	var list []map[string]any
	status, err := m.agent.Do(r.Context(), "GET", "/v1/activity", q, nil, &list)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	names := s.actorNames()
	for _, e := range list {
		actor, _ := e["actor"].(string)
		if kind, name := actorInfo(actor, names); kind != "" {
			e["actorKind"], e["actorName"] = kind, name
		}
	}
	if list == nil {
		list = []map[string]any{}
	}
	writeJSON(w, status, list)
}

var (
	reFileWord = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	reSHA256   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// hDownload streams a backup archive as a download the panel describes
// itself. A joined machine chooses the bytes but not their type or name,
// so nothing it sends can render on the panel's origin.
func (s *Server) hDownload(w http.ResponseWriter, r *http.Request, sess *session) {
	bid := r.PathValue("bid")
	if !reFileWord.MatchString(bid) {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid backup id.", "")
		return
	}
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	resp, err := m.agent.Raw(r.Context(), "GET", agentPath("/v1/servers/{id}/backups/{bid}/download", r), nil, nil, map[string]string{"X-Playkeeper-Actor": sess.User.Username}, true)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		s.agentFailure(w, agentclient.DecodeError(resp))
		return
	}
	if resp.StatusCode != http.StatusOK {
		s.agentFailure(w, agentclient.ErrBadAnswer)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", `attachment; filename="`+backupFileName(bid, resp.Header.Get("Content-Disposition"))+`"`)
	h.Set("Content-Security-Policy", "sandbox")
	if resp.ContentLength >= 0 {
		h.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	if sum := resp.Header.Get("X-Playkeeper-SHA256"); reSHA256.MatchString(sum) {
		h.Set("X-Playkeeper-SHA256", sum)
	}
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, resp.Body)
}

// backupFileName is the name a backup downloads as: the agent's
// playkeeper-<world>-<id>.tar.gz when the machine's name has exactly that
// shape for the id asked for, and playkeeper-<id>.tar.gz otherwise.
func backupFileName(bid, disposition string) string {
	name := "playkeeper-" + bid + ".tar.gz"
	_, params, err := mime.ParseMediaType(disposition)
	if err != nil {
		return name
	}
	world, ok := strings.CutPrefix(params["filename"], "playkeeper-")
	if !ok {
		return name
	}
	if world, ok = strings.CutSuffix(world, "-"+bid+".tar.gz"); !ok || !reFileWord.MatchString(world) {
		return name
	}
	return params["filename"]
}

// rawGet streams a non-JSON agent response of the given type (an image).
func (s *Server) rawGet(pattern, contentType string) func(http.ResponseWriter, *http.Request, *session) {
	return func(w http.ResponseWriter, r *http.Request, _ *session) {
		m, ok := s.target(w, r)
		if !ok {
			return
		}
		resp, err := m.agent.Raw(r.Context(), "GET", agentPath(pattern, r), nil, nil, nil, false)
		if err != nil {
			s.agentFailure(w, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			s.agentFailure(w, agentclient.DecodeError(resp))
			return
		}
		if resp.StatusCode != http.StatusOK {
			s.agentFailure(w, agentclient.ErrBadAnswer)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		io.Copy(w, io.LimitReader(resp.Body, 1<<20))
	}
}

// rawUpload streams an uploaded file (a backup archive, a server icon, a
// data pack) to the agent, which checks it, with the query keys named.
func (s *Server) rawUpload(pattern, contentType string, keys ...string) func(http.ResponseWriter, *http.Request, *session) {
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		m, ok := s.target(w, r)
		if !ok {
			return
		}
		q := url.Values{}
		for _, k := range keys {
			if v := r.URL.Query().Get(k); v != "" {
				q.Set(k, v)
			}
		}
		s.relayUpload(w, r, m, agentPath(pattern, r), q, contentType, sess)
	}
}

func (s *Server) relayUpload(w http.ResponseWriter, r *http.Request, m machine, path string, q url.Values, contentType string, sess *session) {
	resp, err := m.agent.Raw(r.Context(), "POST", path, q, r.Body,
		map[string]string{"X-Playkeeper-Actor": sess.User.Username, "Content-Type": contentType}, true)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		s.agentFailure(w, agentclient.DecodeError(resp))
		return
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case err != nil || resp.StatusCode < 200 || resp.StatusCode > 299:
		s.agentFailure(w, agentclient.ErrBadAnswer)
	case len(bytes.TrimSpace(b)) == 0:
		w.WriteHeader(resp.StatusCode)
	case !json.Valid(b):
		s.agentFailure(w, agentclient.ErrBadAnswer)
	default:
		writeJSON(w, resp.StatusCode, json.RawMessage(b))
	}
}

// --- UI ---

const uiMissing = `<!doctype html><meta charset="utf-8"><title>Playkeeper</title><p>The Playkeeper web UI is not built into this binary. Run <code>make web</code> and rebuild.</p>`

func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	cache := "no-cache"
	if invites.RedactPath(r.URL.Path) != r.URL.Path {
		// Anything under the invite path may carry a code.
		cache = "no-store"
		w.Header().Set("Cache-Control", cache)
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.static == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, uiMissing)
		return
	}
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p != "" && p != "index.html" {
		if st, err := fs.Stat(s.static, p); err == nil && !st.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, s.static, p)
			return
		}
		if strings.HasPrefix(p, "assets/") || path.Ext(p) != "" {
			http.NotFound(w, r)
			return
		}
	}
	s.writeIndex(w, cache)
}

// writeIndex answers with the UI's index.html.
func (s *Server) writeIndex(w http.ResponseWriter, cacheControl string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", cacheControl)
	if s.static == nil {
		io.WriteString(w, uiMissing)
		return
	}
	b, err := fs.ReadFile(s.static, "index.html")
	if err != nil {
		io.WriteString(w, uiMissing)
		return
	}
	w.Write(b)
}

// tlsConfig serves the certificate the agent saved for the name a browser
// asks for (the machine's address), and the self-signed one otherwise: for
// the IP address, the installer's check on localhost, and a name whose
// certificate lapsed. New and renewed certificates are picked up without a
// restart.
func (s *Server) tlsConfig() (*tls.Config, error) {
	certFile, keyFile := filepath.Join(s.cfg.TLSDir(), "cert.pem"), filepath.Join(s.cfg.TLSDir(), "key.pem")
	if _, err := EnsureSelfSignedCert(s.cfg.TLSDir(), s.now()); err != nil {
		return nil, err
	}
	store, err := certs.NewStore(certs.StoreOptions{Dir: s.cfg.CertsDir(), FallbackCert: certFile, FallbackKey: keyFile, Now: s.now})
	if err != nil {
		return nil, err
	}
	if _, err := store.Loaded(); err != nil {
		s.log.Warn("a saved certificate cannot be used; the self-signed one is served instead", "err", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: store.GetCertificate}, nil
}

// ListenAndServeTLS serves the panel over HTTPS until ctx ends, and on the
// same port the plain-HTTP resource pack downloads of players' games.
func (s *Server) ListenAndServeTLS(ctx context.Context) error {
	tc, err := s.tlsConfig()
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(s.cfg.PanelBind, strconv.Itoa(s.cfg.PanelPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.serveAlive(ctx, tc)
	s.log.Info("panel listening", "addr", "https://"+addr)
	return s.serve(ctx, ln, tc)
}

// serve answers on ln until ctx ends: HTTPS for the panel and joined
// machines' links, and plain HTTP for players' games, which refuse the
// panel's self-signed certificate.
func (s *Server) serve(ctx context.Context, ln net.Listener, tc *tls.Config) error {
	split := portshare.Split(ln, portshare.Options{})
	defer split.Close()
	secure := s.httpServer(ln.Addr().String(), tc)
	plain := &http.Server{
		Handler:           s.plainHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	errc := make(chan error, 2)
	go func() { errc <- secure.ServeTLS(split.TLS(), "", "") }()
	go func() { errc <- plain.Serve(split.Plain()) }()
	var err error
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	secure.Shutdown(c)
	plain.Shutdown(c)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// httpServer is the panel's HTTPS server. With machine links on, machines
// share its port: TLS hands connections that offer the link's ALPN to the
// hub, and browsers keep HTTP/2.
func (s *Server) httpServer(addr string, tc *tls.Config) *http.Server {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		TLSConfig:         tc,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	if s.hub != nil {
		srv.TLSConfig = s.hub.ShareTLS(tc)
		srv.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){machinelink.ALPN: s.hub.HandleTLSNextProto}
		srv.Protocols = new(http.Protocols)
		srv.Protocols.SetHTTP1(true)
		srv.Protocols.SetHTTP2(true)
		srv.RegisterOnShutdown(func() { s.hub.Close() })
	}
	return srv
}

// plainHandler answers plain HTTP on the panel's port: resource pack
// downloads, and a redirect to HTTPS for everything else.
func (s *Server) plainHandler() http.Handler {
	return s.logRequests(packs.NewPlainHandler(s.public.handler(packs.PathPrefix)))
}
