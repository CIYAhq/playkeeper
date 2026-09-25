package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
)

type agentEnv struct {
	t    *testing.T
	dir  string
	cfg  config.Config
	fd   *fakeDocker
	rcon *fakeRCON
	slp  string
	a    *Agent
	ts   *httptest.Server
	fill *fakeFill
	// up stands in for Mojang and the other upstreams of the server types.
	up *fakeUpstream
	mu sync.Mutex
	// clockOffset moves the agent's clock and crashBackoff, when set, replaces
	// the zero backoff (both taken at start); diskFree, when set, is the free
	// space the agent measures.
	clockOffset  time.Duration
	crashBackoff []time.Duration
	diskFree     atomic.Int64
	// updateKeys are the release keys the agent trusts (none by default), and
	// stagedVersion is what a downloaded binary reports.
	updateKeys    []ed25519.PublicKey
	stagedVersion string
	// addons, when set, is the add-on library the agent uses.
	addons *addons.Library
	// pregenResumeAfter, when set, is how long the server must be empty
	// before a task paused for players continues.
	pregenResumeAfter time.Duration
	// sid is the server most helpers act on: the one create made last.
	sid string
	// live is the running agent, for the fake RCON's password check.
	live atomic.Pointer[Agent]
}

// srv is the current server's runtime handle.
func (e *agentEnv) srv() *server {
	e.t.Helper()
	s := e.a.serverByID(e.sid)
	if s == nil {
		e.t.Fatalf("no server %q", e.sid)
	}
	return s
}

// sp is the agent path of the current server's route.
func (e *agentEnv) sp(rest string) string { return "/v1/servers/" + e.sid + rest }

func (e *agentEnv) binaryVersion(string) (string, error) {
	if e.stagedVersion == "" {
		return "", errors.New("no staged version configured")
	}
	return e.stagedVersion, nil
}

func newAgentEnv(t *testing.T) *agentEnv {
	t.Helper()
	dir := t.TempDir()
	e := &agentEnv{t: t, dir: dir}
	e.fd = startFakeDocker(t, filepath.Join(dir, "docker.sock"))
	sum := sha256.Sum256(e.fd.jarContent)
	e.fill = startFakeFill(t, hex.EncodeToString(sum[:]))
	e.up = startFakeUpstream(t)
	e.up.serveMojang()
	cfg := config.Default()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.SocketPath = filepath.Join(dir, "agent.sock")
	cfg.DockerSocket = filepath.Join(dir, "docker.sock")
	cfg.GameUID, cfg.GameGID = os.Getuid(), os.Getgid()
	cfg.InstallID = "test-install-0001"
	cfg.Dev = true
	e.cfg = cfg
	e.start()
	return e
}

func (e *agentEnv) start() {
	e.t.Helper()
	if e.rcon == nil {
		// Each server generates its own RCON password; the fake accepts any of them.
		e.rcon = startFakeRCON(e.t, func(pw string) bool {
			a := e.live.Load()
			if a == nil {
				return false
			}
			for _, s := range a.serverList() {
				if p, err := s.rconPassword(); err == nil && p == pw {
					return true
				}
			}
			return false
		})
		e.slp = startFakeSLP(e.t, e.rcon)
	}
	offset := e.clockOffset
	backoff := e.crashBackoff
	if backoff == nil {
		backoff = []time.Duration{0}
	}
	a, err := New(Options{
		Config: e.cfg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return time.Now().Add(offset) },
		SampleInterval: 100 * time.Millisecond, ReconcileInterval: 50 * time.Millisecond, CrashBackoff: backoff,
		RCONAddr: func(string) string { return e.rcon.addr }, PingAddr: e.slp,
		HostMemoryMB: func() int { return 4096 }, DiskUsage: func(string) (int64, int64, error) {
			if free := e.diskFree.Load(); free > 0 {
				return free, 100 << 30, nil
			}
			return 50 << 30, 100 << 30, nil
		},
		CheckEgress: func(context.Context) error { return nil }, PortInUse: func(int) bool { return false },
		StopTimeout: 5 * time.Second, ReadyTimeout: 10 * time.Second, WarnDelay: 50 * time.Millisecond, BackupWarnDelay: 10 * time.Millisecond,
		FillURL: e.fill.srv.URL, UpdateCheckInterval: -1, UpdateKeys: e.updateKeys, BinaryVersion: e.binaryVersion,
		Addons: e.addons, PregenInterval: 50 * time.Millisecond, PregenResumeAfter: e.pregenResumeAfter,
		UpstreamClient: e.up.client(), PackClient: e.up.client(),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.a = a
	e.live.Store(a)
	a.Start()
	e.ts = httptest.NewServer(a.HandlerForTest())
	e.t.Cleanup(func() { e.stop() })
}

func (e *agentEnv) stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ts != nil {
		e.ts.Close()
		e.ts = nil
	}
	if e.a != nil {
		e.live.Store(nil)
		e.a.Close()
		e.a = nil
	}
}

func (e *agentEnv) call(method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	var r io.Reader
	if s, ok := body.(string); ok {
		r = strings.NewReader(s)
	} else if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, r)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	b, _ := io.ReadAll(resp.Body)
	if len(b) > 0 && b[0] == '{' {
		json.Unmarshal(b, &out)
	}
	return resp.StatusCode, out
}

func (e *agentEnv) status() api.ServerStatus {
	e.t.Helper()
	return e.srv().Status(context.Background())
}

func (e *agentEnv) waitOp(id string) *api.Operation {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cur := e.a.currentOp(); cur == nil || cur.ID != id {
			op, err := e.a.loadOperation(id)
			if err == nil && op.Status != api.OpRunning {
				return op
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("operation %s did not finish", id)
	return nil
}

func (e *agentEnv) waitFor(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %s", what)
}

// create makes a server with the defaults and waits until it is online; it
// becomes the current server.
func (e *agentEnv) create() {
	e.t.Helper()
	e.createWith(map[string]any{})
}

func (e *agentEnv) createWith(extra map[string]any) {
	e.t.Helper()
	body := map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin"}
	for k, v := range extra {
		body[k] = v
	}
	code, out := e.call("POST", "/v1/servers", body)
	if code != 202 {
		e.t.Fatalf("create: %d %v", code, out)
	}
	e.sid = out["serverId"].(string)
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded {
		e.t.Fatalf("create failed: %+v", op)
	}
	e.waitFor("online", func() bool { return e.status().Phase == api.PhaseOnline })
}

// startCreate asks for a server and returns its operation without waiting.
func (e *agentEnv) startCreate(body map[string]any) (int, map[string]any) {
	e.t.Helper()
	full := map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin"}
	for k, v := range body {
		full[k] = v
	}
	code, out := e.call("POST", "/v1/servers", full)
	if id, ok := out["serverId"].(string); ok {
		e.sid = id
	}
	return code, out
}

// waitExitRead waits until the log follower has read the stopped container to
// its end.
func (e *agentEnv) waitExitRead() {
	e.t.Helper()
	s := e.srv()
	e.waitFor("the follower to reach the exit", func() bool {
		c, err := e.a.docker.ContainerInspect(context.Background(), s.containerName())
		fin, ok := c.State.Finished()
		s.mu.Lock()
		defer s.mu.Unlock()
		return err == nil && ok && !c.State.Running && !s.followEnded[c.ID].Before(fin)
	})
}

func (e *agentEnv) onlineIdle() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() }

// cname and dataDir are the current (v2) server's container name and world
// directory; they work while the agent is stopped too.
func (e *agentEnv) cname() string   { return containerPrefix + e.sid }
func (e *agentEnv) dataDir() string { return filepath.Join(e.cfg.DataDir, "servers", e.sid, "data") }

// addIdleServer records a stopped server without starting it, for tests that
// need a server but no container; it becomes the current server.
func (e *agentEnv) addIdleServer() string {
	e.t.Helper()
	sc := api.ServerConfig{Type: api.TypePaper, VersionID: "paper-26.1.2", MinecraftVersion: "26.1.2", PaperBuild: 74, MemoryMB: 1536, HeapMB: 1024, LevelName: "world", MOTD: defaultMOTD, MaxPlayers: 10, Whitelist: true}
	s, _, err := e.a.addServer(newServerSpec{typ: api.TypePaper, config: sc, desired: api.DesiredStopped}, "", nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.sid = s.id
	return s.id
}

func (e *agentEnv) setCollectingSince(v string) {
	e.t.Helper()
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.a.db.Exec(`UPDATE servers SET collecting_since = ? WHERE id = ?`, t.UnixMilli(), e.sid); err != nil {
		e.t.Fatal(err)
	}
}

// failConfigSaves makes every save of a server's settings fail, like a disk error.
func (e *agentEnv) failConfigSaves() {
	e.t.Helper()
	if _, err := e.a.db.Exec(`CREATE TRIGGER fail_config BEFORE UPDATE OF config ON servers BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		e.t.Fatal(err)
	}
}

func (e *agentEnv) crashEvents() int {
	return e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'server_crashed'`)
}

func (e *agentEnv) opsOf(kind, status string) int {
	return e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = ? AND status = ?`, kind, status)
}

func (e *agentEnv) countRows(q string, args ...any) int {
	var n int
	if err := e.a.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestEULAGateRefusesAndDownloadsNothing(t *testing.T) {
	e := newAgentEnv(t)
	code, out := e.call("POST", "/v1/servers", map[string]any{"acceptEula": false, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin"})
	if code != 400 || out["code"] != api.CodeEULARequired {
		t.Fatalf("create without EULA: %d %v", code, out)
	}
	time.Sleep(200 * time.Millisecond)
	if e.fd.pulls != 0 || e.fd.containerCount("") != 0 || e.fd.called("POST /images/create") != 0 || e.fd.called("POST /containers/create") != 0 {
		t.Fatalf("EULA refusal still touched Docker: pulls=%d containers=%d calls=%v", e.fd.pulls, e.fd.containerCount(""), e.fd.calls)
	}
	if dirs, _ := os.ReadDir(filepath.Join(e.cfg.DataDir, "servers")); len(dirs) != 0 {
		t.Fatalf("a refused create made server directories: %v", dirs)
	}
	if n := len(e.a.serverList()); n != 0 {
		t.Fatalf("a refused create recorded %d server(s)", n)
	}
}

func TestInvalidInputsAndUnknownVerbsAreRejected(t *testing.T) {
	e := newAgentEnv(t)
	e.addIdleServer()
	create := func(extra map[string]any) map[string]any {
		body := map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin"}
		for k, v := range extra {
			body[k] = v
		}
		return body
	}
	bad := []struct {
		name, method, path string
		body               any
		want               int
	}{
		{"negative RAM", "POST", "/v1/servers", create(map[string]any{"memoryMB": -1}), 400},
		{"huge RAM", "POST", "/v1/servers", create(map[string]any{"memoryMB": 99999}), 400},
		{"off-list RAM", "POST", "/v1/servers", create(map[string]any{"memoryMB": 1000}), 400},
		{"RAM the other server has", "POST", "/v1/servers", create(map[string]any{"memoryMB": 2048}), 400},
		{"shell in version", "POST", "/v1/servers", create(map[string]any{"versionId": "latest; id"}), 400},
		{"latest version", "POST", "/v1/servers", create(map[string]any{"versionId": "latest"}), 400},
		{"extra argument", "POST", "/v1/servers", `{"acceptEula":true,"versionId":"paper-26.1.2","memoryMB":1536,"actor":"admin","cmd":"rm -rf /"}`, 400},
		{"control char MOTD", "POST", "/v1/servers", create(map[string]any{"motd": "a\nb"}), 400},
		{"control char name", "POST", "/v1/servers", create(map[string]any{"name": "a\nb"}), 400},
		{"name too long", "POST", "/v1/servers", create(map[string]any{"name": strings.Repeat("a", 33)}), 400},
		{"unknown type", "POST", "/v1/servers", create(map[string]any{"type": "forge"}), 400},
		{"another type's version", "POST", "/v1/servers", create(map[string]any{"type": "vanilla"}), 400},
		{"a build for Paper", "POST", "/v1/servers", create(map[string]any{"build": "41"}), 400},
		{"a build for Vanilla", "POST", "/v1/servers", create(map[string]any{"type": "vanilla", "versionId": "vanilla-26.2", "build": "1"}), 400},
		{"unknown play style", "POST", "/v1/servers", create(map[string]any{"playStyle": "chaos"}), 400},
		{"unknown difficulty", "POST", "/v1/servers", create(map[string]any{"gameplay": map[string]any{"difficulty": "insane"}}), 400},
		{"view distance too far", "POST", "/v1/servers", create(map[string]any{"gameplay": map[string]any{"viewDistance": 99}}), 400},
		{"trailing data", "POST", e.sp("/start"), `{"actor":"admin"} {"actor":"x"}`, 400},
		{"missing actor", "POST", e.sp("/start"), map[string]any{}, 400},
		{"world type after creation", "POST", e.sp("/settings"), map[string]any{"gameplay": map[string]any{"levelType": "flat"}, "actor": "admin"}, 400},
		{"hardcore after creation", "POST", e.sp("/settings"), map[string]any{"gameplay": map[string]any{"hardcore": true}, "actor": "admin"}, 400},
		{"rm verb", "POST", e.sp("/rm"), map[string]any{"actor": "admin"}, 404},
		{"rm top-level", "POST", "/v1/rm", map[string]any{"actor": "admin"}, 404},
		{"shell verb", "POST", "/v1/exec", map[string]any{"cmd": "id"}, 404},
		{"wrong method", "DELETE", "/v1/servers", nil, 404},
		{"unknown server", "GET", "/v1/servers/zzzzzzzzzz", nil, 404},
		{"traversal server id", "GET", "/v1/servers/..%2F..%2Fetc", nil, 400},
		{"traversal backup id", "GET", e.sp("/backups/..%2F..%2Fetc%2Fpasswd/download"), nil, 400},
		{"traversal backup verify", "POST", e.sp("/backups/..%2F..%2Fetc%2Fpasswd/verify"), map[string]any{"actor": "admin"}, 400},
		{"traversal restore id", "GET", "/v1/restore/..%2F..%2Fetc", nil, 400},
		{"bad whitelist name", "DELETE", e.sp("/whitelist/%3Bid?actor=admin"), nil, 400},
		{"bad operator name", "DELETE", e.sp("/operators/%3Bid?actor=admin"), nil, 400},
		{"bad kick name", "POST", e.sp("/kick"), map[string]any{"name": ";id", "actor": "admin"}, 400},
		{"bad operation id", "GET", "/v1/operations/..%2Fx", nil, 400},
		{"delete without the name", "POST", e.sp("/delete"), map[string]any{"confirm": "yes", "actor": "admin"}, 400},
	}
	for _, c := range bad {
		if code, out := e.call(c.method, c.path, c.body); code != c.want {
			t.Errorf("%s: %s %s -> %d %v, want %d", c.name, c.method, c.path, code, out, c.want)
		}
	}
	if e.fd.containerCount("") != 0 {
		t.Fatal("rejected requests created containers")
	}
	for _, cmd := range []string{"", "list\nstop", "say \x00", strings.Repeat("a", 300), "stop", "/restart now"} {
		if _, err := validateCommand(cmd); err == nil {
			t.Errorf("command %q should be rejected", cmd)
		}
	}
	for _, cmd := range []string{"list", ";id", "$(id)", "`id`", "say hello; rm -rf /"} {
		if got, err := validateCommand(cmd); err != nil || got != cmd {
			t.Errorf("command %q should pass through literally, got %q %v", cmd, got, err)
		}
	}
}

func TestSocketRejectsUnlistedPeers(t *testing.T) {
	e := newAgentEnv(t)
	for _, tc := range []struct {
		allowed []uint32
		want    int
	}{{[]uint32{uint32(os.Getuid()) + 12345}, 403}, {[]uint32{uint32(os.Getuid())}, 200}} {
		e.a.allowed = map[uint32]bool{}
		for _, u := range tc.allowed {
			e.a.allowed[u] = true
		}
		sock := filepath.Join(e.dir, fmt.Sprintf("peer-%d.sock", tc.want))
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: e.a.Handler(), ConnContext: withConn}
		go srv.Serve(ln)
		hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}}
		resp, err := hc.Get("http://agent/v1/health")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		srv.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("peer uid allowed=%v: got %d, want %d", tc.allowed, resp.StatusCode, tc.want)
		}
	}
}

func TestServeLeavesOtherFilesAlone(t *testing.T) {
	e := newAgentEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// In `playkeeper dev` the panel creates files in the same process while the
	// agent starts listening; a directory made at that moment keeps its mode.
	probe := filepath.Join(e.dir, "made-while-listening")
	orig := listenUnix
	t.Cleanup(func() { listenUnix = orig })
	listenUnix = func(path string) (net.Listener, error) {
		if err := os.Mkdir(probe, 0o700); err != nil {
			return nil, err
		}
		return orig(path)
	}
	go e.a.Serve(ctx)
	e.waitFor("agent socket", func() bool { _, err := os.Stat(e.cfg.SocketPath); return err == nil })
	if st, err := os.Stat(probe); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("a directory created while the agent started listening got mode %v (%v)", st.Mode(), err)
	}
	if st, err := os.Stat(e.cfg.SocketPath); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket must be owner-only outside a root install: %v %v", st.Mode(), err)
	}
}

func TestCreateStartStopAreIdempotent(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	st := e.status()
	if st.Config.MemoryMB != 1536 || st.Config.HeapMB != 1024 || st.Config.JarVerifiedAt == nil {
		t.Fatalf("config after create: %+v", st.Config)
	}
	if n := e.fd.containerCount(e.cname()); n != 1 {
		t.Fatalf("containers after create: %d", n)
	}
	e.fd.mu.Lock()
	c := e.fd.byName[e.cname()]
	cfg := c.cfg
	e.fd.mu.Unlock()
	if cfg.HostConfig.Memory != 1536<<20 || cfg.HostConfig.MemorySwap != 1536<<20 || env(cfg, "MEMORY") != "1024M" {
		t.Fatalf("memory limit/heap not applied: %d %s", cfg.HostConfig.Memory, env(cfg, "MEMORY"))
	}
	if env(cfg, "TYPE") != "CUSTOM" || env(cfg, "CUSTOM_SERVER") != "/data/paper-26.1.2-74.jar" || env(cfg, "SETUP_ONLY") != "" {
		t.Fatalf("the server container must run the verified jar without downloading: TYPE=%s CUSTOM_SERVER=%s", env(cfg, "TYPE"), env(cfg, "CUSTOM_SERVER"))
	}
	if env(cfg, "VERSION") != "26.1.2" || env(cfg, "SKIP_DOWNLOAD_DEFAULTS") != "TRUE" {
		t.Fatalf("the server container must name the pinned version and skip third-party default configs: VERSION=%s SKIP_DOWNLOAD_DEFAULTS=%s", env(cfg, "VERSION"), env(cfg, "SKIP_DOWNLOAD_DEFAULTS"))
	}
	if len(cfg.HostConfig.CapAdd) != 0 || cfg.HostConfig.CapDrop[0] != "ALL" || cfg.User == "" || env(cfg, "ONLINE_MODE") != "TRUE" || cfg.HostConfig.NetworkMode != networkName {
		t.Fatalf("container not hardened as designed: %+v", cfg)
	}
	if len(cfg.HostConfig.PortBindings) != 1 || cfg.HostConfig.PortBindings["25565/tcp"] == nil {
		t.Fatalf("only the game port may be published: %+v", cfg.HostConfig.PortBindings)
	}
	for _, b := range cfg.HostConfig.Binds {
		if strings.Contains(b, "docker.sock") {
			t.Fatal("the Docker socket must never be mounted")
		}
	}
	code, out := e.call("POST", e.sp("/start"), map[string]any{"actor": "admin"})
	if code != 200 || out["noop"] != true {
		t.Fatalf("second start should be a no-op: %d %v", code, out)
	}
	if n := e.fd.containerCount(e.cname()); n != 1 {
		t.Fatalf("second start created another container: %d", n)
	}
	code, out = e.call("POST", e.sp("/stop"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("stop: %d %v", code, out)
	}
	e.waitOp(out["id"].(string))
	if st := e.status(); st.Phase != api.PhaseStopped || st.Desired != api.DesiredStopped || st.Players != nil || st.Resources.CPUPercent != nil {
		t.Fatalf("after stop: %+v", st)
	}
	e.rcon.mu.Lock()
	sawSave := false
	for _, c := range e.rcon.commands {
		if strings.HasPrefix(c, "save-all") {
			sawSave = true
		}
	}
	e.rcon.mu.Unlock()
	if !sawSave {
		t.Fatal("stop must save the world (save-all) before stopping")
	}
	if code, out := e.call("POST", e.sp("/stop"), map[string]any{"actor": "admin"}); code != 200 || out["noop"] != true {
		t.Fatalf("second stop should be a no-op: %d %v", code, out)
	}
	if code, _ := e.call("POST", e.sp("/restart"), map[string]any{"actor": "admin"}); code != 409 {
		t.Fatalf("restart while stopped must conflict: %d", code)
	}
	time.Sleep(300 * time.Millisecond)
	if st := e.status(); st.Phase != api.PhaseStopped {
		t.Fatalf("reconciler restarted a server the user stopped: %s", st.Phase)
	}
	code, out = e.call("POST", e.sp("/start"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("start: %d %v", code, out)
	}
	e.waitOp(out["id"].(string))
	e.waitFor("online again", func() bool { return e.status().Phase == api.PhaseOnline })
	if n := e.fd.containerCount(e.cname()); n != 1 {
		t.Fatalf("containers after restart cycle: %d", n)
	}
}

func TestDownloadsArePinnedAndTelemetryIsOff(t *testing.T) {
	e := newAgentEnv(t)
	e.addIdleServer()
	sc := api.ServerConfig{VersionID: "paper-26.1.2", MinecraftVersion: "26.1.2", PaperBuild: 74, MemoryMB: 1536, MaxPlayers: 10, LevelName: "world"}
	setup, _ := e.srv().containerSpec(sc, true)
	for k, want := range map[string]string{"TYPE": "PAPER", "VERSION": "26.1.2", "PAPER_BUILD": "74", "SETUP_ONLY": "TRUE", "SKIP_DOWNLOAD_DEFAULTS": "TRUE"} {
		if got := env(setup, k); got != want {
			t.Errorf("setup container %s=%q, want %q", k, got, want)
		}
	}
	e.create()
	path := filepath.Join(e.dataDir(), "plugins", "bStats", "config.yml")
	if b, err := os.ReadFile(path); err != nil || !bStatsOff(b) {
		t.Fatalf("bStats must be off before the first start: %q %v", b, err)
	}
	// A restored archive can carry bStats switched on; the next start turns it off.
	if err := os.WriteFile(path, []byte("enabled: true\nserverUuid: 00000000-0000-0000-0000-000000000000\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"stop", "start"} {
		code, out := e.call("POST", e.sp("/"+verb), map[string]any{"actor": "admin"})
		if code != 202 {
			t.Fatalf("%s: %d %v", verb, code, out)
		}
		if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
			t.Fatalf("%s failed: %+v", verb, op)
		}
	}
	if b, _ := os.ReadFile(path); !bStatsOff(b) {
		t.Fatalf("bStats was left on after a start: %q", b)
	}
	for _, s := range []string{"enabled: true", "enabled:false", "# enabled: false\nenabled: true", ""} {
		if bStatsOff([]byte(s)) != (s == "enabled:false") {
			t.Errorf("bStatsOff(%q) = %v", s, !(s == "enabled:false"))
		}
	}
}

func TestConcurrentOperationsAreSerialized(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.fd.bootDelay = 300 * time.Millisecond
	type res struct {
		path string
		code int
		out  map[string]any
	}
	paths := []string{e.sp("/stop"), e.sp("/restart"), e.sp("/backups")}
	results := make(chan res, len(paths))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, p := range paths {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			<-start
			code, out := e.call("POST", p, map[string]any{"actor": "admin"})
			results <- res{p, code, out}
		}(p)
	}
	close(start)
	wg.Wait()
	close(results)
	var winner *res
	busy := 0
	for r := range results {
		switch r.code {
		case 202:
			if winner != nil {
				t.Fatalf("two operations ran at once: %s and %s", winner.path, r.path)
			}
			rr := r
			winner = &rr
		case 409:
			if r.out["code"] != api.CodeBusy {
				t.Fatalf("409 without busy code: %v", r.out)
			}
			busy++
		default:
			t.Fatalf("%s: unexpected %d %v", r.path, r.code, r.out)
		}
	}
	if winner == nil || busy != 2 {
		t.Fatalf("want exactly one winner and two 409s, got winner=%v busy=%d", winner, busy)
	}
	op := e.waitOp(winner.out["id"].(string))
	if op.Status != api.OpSucceeded {
		t.Fatalf("winning operation failed: %+v", op)
	}
	st := e.status()
	switch winner.path {
	case e.sp("/stop"):
		if st.Phase != api.PhaseStopped || st.Desired != api.DesiredStopped {
			t.Fatalf("after stop won: %s/%s", st.Phase, st.Desired)
		}
	default:
		e.waitFor("online after "+winner.path, func() bool { return e.status().Phase == api.PhaseOnline })
	}
	if n := e.fd.containerCount(e.cname()); n != 1 {
		t.Fatalf("containers: %d", n)
	}
}

// The operation a request gets back is its own copy: the running operation
// records progress without touching it, so the handler can encode it safely.
func TestStartedOperationsAreCopies(t *testing.T) {
	e := newAgentEnv(t)
	e.addIdleServer()
	s := e.srv()
	type opFn = func(context.Context, *opHandle) error
	for _, c := range []struct {
		name  string
		begin func(opFn) (*api.Operation, error)
	}{
		{"server", func(fn opFn) (*api.Operation, error) { return s.beginOp("backup", "admin", fn) }},
		{"machine", func(fn opFn) (*api.Operation, error) { return e.a.beginMachineOp("update", "admin", fn) }},
	} {
		recorded, release := make(chan struct{}), make(chan struct{})
		op, err := c.begin(func(ctx context.Context, h *opHandle) error {
			h.set("step", "copying")
			close(recorded)
			<-release
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		<-recorded
		_, shared := op.Detail["step"]
		close(release)
		if shared {
			t.Errorf("%s: the returned operation changed while the operation ran", c.name)
		}
		e.waitFor(c.name+" operation to end", func() bool {
			done, ok := s.holdOpLock()
			if ok {
				done()
			}
			return ok
		})
	}
}

func TestConcurrentStartAndStopLeaveDesiredMatchingContainer(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	for i := 0; i < 16; i++ {
		paths := []string{e.sp("/start"), e.sp("/stop")}
		codes := make([]int, len(paths))
		outs := make([]map[string]any, len(paths))
		var wg sync.WaitGroup
		start := make(chan struct{})
		for j, p := range paths {
			wg.Add(1)
			go func(j int, p string) {
				defer wg.Done()
				<-start
				codes[j], outs[j] = e.call("POST", p, map[string]any{"actor": "admin"})
			}(j, p)
		}
		close(start)
		wg.Wait()
		for j, code := range codes {
			switch {
			case code == 202:
				e.waitOp(outs[j]["id"].(string))
			case code == 200 && outs[j]["noop"] == true:
			case code == 409 && outs[j]["code"] == api.CodeBusy:
			default:
				t.Fatalf("iteration %d: %s -> %d %v", i, paths[j], code, outs[j])
			}
		}
		_, running, err := e.srv().containerRunning(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if desired := e.srv().desired(); (desired == api.DesiredRunning) != running {
			t.Fatalf("iteration %d (start %d, stop %d): desired %s but container running=%v", i, codes[0], codes[1], desired, running)
		}
	}
	if n := e.countRows(`SELECT COUNT(*) FROM operations WHERE kind IN ('recover', 'auto-restart')`); n != 0 {
		t.Fatalf("concurrent start/stop left a divergence the reconciler had to repair (%d operations)", n)
	}
}

func TestSessionsAreDedupedAndSpoofProof(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.fd.addLog("[12:01:00 INFO]: UUID of player PkBotFriend is d2f0fd39-4e0d-37a0-85e0-9c466776a4d7")
	e.fd.addLog("[12:01:00 INFO]: PkBotFriend joined the game")
	e.fd.addLog("[12:01:01 INFO]: PkBotFriend[IP hidden] logged in with entity id 1 at ([minecraft:overworld]0.5, 70.0, 0.5)")
	e.fd.addLog("[12:01:05 INFO]: <PkBotFriend> Foo joined the game")
	e.fd.addLog("[12:01:06 INFO]: [Not Secure] <PkBotFriend> Foo left the game")
	e.fd.addLog("[12:01:30 INFO]: PkBotFriend lost connection: Disconnected")
	e.fd.addLog("[12:01:30 INFO]: PkBotFriend left the game")
	e.waitFor("session closed", func() bool {
		return e.countRows(`SELECT COUNT(*) FROM sessions WHERE player = 'PkBotFriend' AND end_ts IS NOT NULL`) == 1
	})
	if n := e.countRows(`SELECT COUNT(*) FROM events WHERE player = 'Foo'`); n != 0 {
		t.Fatalf("chat created %d events for a fake player", n)
	}
	var uuid string
	e.a.db.QueryRow(`SELECT uuid FROM sessions WHERE player = 'PkBotFriend'`).Scan(&uuid)
	if uuid != "d2f0fd39-4e0d-37a0-85e0-9c466776a4d7" {
		t.Fatalf("uuid not attached: %q", uuid)
	}
	events := e.countRows(`SELECT COUNT(*) FROM events`)
	sessions := e.countRows(`SELECT COUNT(*) FROM sessions`)
	// Restart the agent: it replays the current run's log from its start.
	e.stop()
	e.start()
	e.waitFor("online after agent restart", func() bool { return e.status().Phase == api.PhaseOnline })
	time.Sleep(300 * time.Millisecond)
	if got := e.countRows(`SELECT COUNT(*) FROM events`); got != events {
		t.Fatalf("agent restart duplicated events: %d -> %d", events, got)
	}
	if got := e.countRows(`SELECT COUNT(*) FROM sessions`); got != sessions {
		t.Fatalf("agent restart duplicated sessions: %d -> %d", sessions, got)
	}
}

// The server log is json-file with rotation (10 MB, 3 files). Some Docker
// versions end a follow stream on rotation and return the current file again
// on the next request; neither may double count or stop collection.
func TestLogRotationNeitherDuplicatesNorStalls(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.fd.addLog("[12:01:00 INFO]: PkBotFriend joined the game")
	e.waitFor("join recorded", func() bool { return e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'join'`) == 1 })
	e.fd.rotate(1, true)
	e.fd.addLog("[12:02:00 INFO]: PkBotFriend left the game")
	e.fd.addLog("[12:02:10 INFO]: PkBotBuilder joined the game")
	e.waitFor("lines after the first rotation recorded", func() bool {
		return e.countRows(`SELECT COUNT(*) FROM events WHERE player = 'PkBotBuilder'`) == 1
	})
	e.fd.rotate(0, false)
	e.fd.addLog("[12:03:00 INFO]: PkBotBuilder left the game")
	e.waitFor("lines after the second rotation recorded", func() bool {
		return e.countRows(`SELECT COUNT(*) FROM events WHERE player = 'PkBotBuilder' AND kind = 'leave'`) == 1
	})
	time.Sleep(500 * time.Millisecond)
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM events WHERE player = 'PkBotFriend' AND kind = 'join'`:  1,
		`SELECT COUNT(*) FROM events WHERE player = 'PkBotFriend' AND kind = 'leave'`: 1,
		`SELECT COUNT(*) FROM events WHERE player = 'PkBotBuilder'`:                   2,
		`SELECT COUNT(*) FROM sessions`:                                               2,
		`SELECT COUNT(*) FROM sessions WHERE end_ts IS NULL`:                          0,
	} {
		if got := e.countRows(q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
}

func TestCrashIsDetectedSessionMarkedIncompleteAndRecovered(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.fd.addLog("[12:01:00 INFO]: PkBotBuilder joined the game")
	e.waitFor("session open", func() bool { return e.countRows(`SELECT COUNT(*) FROM sessions WHERE end_ts IS NULL`) == 1 })
	e.fd.crash(137)
	e.waitFor("crash handled", func() bool {
		return e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'server_crashed'`) == 1
	})
	var reason string
	var uncertain int
	e.a.db.QueryRow(`SELECT end_reason, end_uncertain FROM sessions WHERE player = 'PkBotBuilder'`).Scan(&reason, &uncertain)
	if reason != "server_crashed" || uncertain != 1 {
		t.Fatalf("crashed session: reason %q uncertain %d", reason, uncertain)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'leave'`); n != 0 {
		t.Fatalf("a crash must not invent leave events, got %d", n)
	}
	e.waitFor("auto-restart", func() bool { return e.status().Phase == api.PhaseOnline })
	// Two more crashes inside the window: the policy gives up after the third.
	for i := 0; i < 2; i++ {
		e.waitFor("online before crash", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
		e.fd.crash(1)
		e.waitFor(fmt.Sprintf("crash %d handled", i+2), func() bool {
			return e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'server_crashed'`) == i+2
		})
	}
	e.waitFor("gave up", func() bool {
		st := e.status()
		return st.Phase == api.PhaseCrashed && strings.Contains(st.LastError, "stopped restarting") && !e.a.busy()
	})
	if st := e.status(); st.Desired != api.DesiredRunning {
		t.Fatalf("divergence must stay visible (desired running, observed crashed): %+v", st)
	}
}

// When the server stops while the agent is down, for example during a host
// reboot, the agent does not know how it stopped, so it counts no crash. It
// reads the log to its end first, however long ago the exit was, so a leave
// logged meanwhile closes its session; a session still open ends at the exit,
// uncertain. The server is brought back.
func TestExitWhileTheAgentWasDownIsNotCounted(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stop      func(fd *fakeDocker)
		reason    string
		uncertain int
	}{
		{"clean shutdown", func(fd *fakeDocker) {
			fd.addLog("[12:02:00 INFO]: PkBotBuilder left the game")
			fd.externalStop()
		}, "left", 0},
		{"crash", func(fd *fakeDocker) { fd.crash(137) }, "server_stopped", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgentEnv(t)
			e.create()
			e.fd.addLog("[12:01:00 INFO]: PkBotBuilder joined the game")
			e.waitFor("session open", func() bool { return e.countRows(`SELECT COUNT(*) FROM sessions WHERE end_ts IS NULL`) == 1 })
			e.stop()
			tc.stop(e.fd)
			e.fd.mu.Lock()
			e.fd.logDelay = 300 * time.Millisecond
			e.fd.mu.Unlock()
			e.clockOffset = time.Minute
			e.start()
			e.waitFor("the server brought back", e.onlineIdle)
			if n := e.crashEvents(); n != 0 {
				t.Fatalf("an exit while the agent was down was counted as %d crash(es)", n)
			}
			if n := e.opsOf("recover", api.OpSucceeded); n != 1 {
				t.Fatalf("want one recover, got %d", n)
			}
			var reason string
			var uncertain int
			e.a.db.QueryRow(`SELECT end_reason, end_uncertain FROM sessions WHERE player = 'PkBotBuilder'`).Scan(&reason, &uncertain)
			if reason != tc.reason || uncertain != tc.uncertain {
				t.Fatalf("session ended with reason %q, uncertain %d; want %q, %d", reason, uncertain, tc.reason, tc.uncertain)
			}
		})
	}
}

// A start that fails because the server exits while starting is one failure:
// the start reports it, and neither the reconcile loop nor an agent restart
// counts the same exit again as a crash. So after a real crash the restart
// policy gives up after maxCrashes real failures, not fewer.
func TestFailedStartIsCountedOnce(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	crashes := func() int { return e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'server_crashed'`) }
	run := func(verb, want string) {
		t.Helper()
		code, out := e.call("POST", e.sp("/"+verb), map[string]any{"actor": "admin"})
		if code != 202 {
			t.Fatalf("%s: %d %v", verb, code, out)
		}
		if op := e.waitOp(out["id"].(string)); op.Status != want {
			t.Fatalf("%s: %+v", verb, op)
		}
	}
	// settled waits until the log follower has read the stopped container to
	// its end, then gives the reconcile loop a few ticks to classify the exit.
	settled := func() {
		t.Helper()
		e.waitExitRead()
		time.Sleep(300 * time.Millisecond)
	}
	run("stop", api.OpSucceeded)
	e.fd.mu.Lock()
	e.fd.bootExit = 134
	e.fd.mu.Unlock()
	run("start", api.OpFailed)
	settled()
	if n, phase := crashes(), e.status().Phase; n != 0 || phase != api.PhaseStopped {
		t.Fatalf("the failed start was counted again: %d crash event(s), phase %s", n, phase)
	}
	e.stop()
	e.start()
	settled()
	if n := crashes(); n != 0 {
		t.Fatalf("an agent restart counted the old exit again: %d crash event(s)", n)
	}

	e.fd.mu.Lock()
	e.fd.bootExit = 0
	e.fd.mu.Unlock()
	run("start", api.OpSucceeded)
	e.fd.mu.Lock()
	e.fd.bootExit = 134
	e.fd.mu.Unlock()
	e.fd.crash(137)
	e.waitFor("the restart policy to give up", func() bool {
		return strings.Contains(e.status().LastError, "stopped trying to start") && !e.a.busy()
	})
	if n := e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'auto-restart'`); n != maxCrashes-1 {
		t.Fatalf("want %d automatic restarts after the crash before giving up, got %d", maxCrashes-1, n)
	}
	if n := crashes(); n != 1 {
		t.Fatalf("want the one real crash recorded, got %d", n)
	}
}

// After an agent restart the restart policy starts over, so a server that
// should be running is brought back: after a crash that was waiting out its
// backoff, after a start the agent was stopped in the middle of, and after the
// policy gave up. If the cause is still there, it gives up again and says why.
func TestAgentRestartBringsBackAServerThatShouldBeRunning(t *testing.T) {
	t.Run("a crash waiting out its backoff", func(t *testing.T) {
		e := newAgentEnv(t)
		e.stop()
		e.crashBackoff = []time.Duration{time.Minute}
		e.start()
		e.create()
		e.fd.crash(137)
		e.waitFor("the crash to be counted", func() bool { return e.crashEvents() == 1 && e.status().Phase == api.PhaseCrashed })
		e.stop()
		e.start()
		e.waitFor("the server brought back", e.onlineIdle)
		if n, c := e.opsOf("recover", api.OpSucceeded), e.crashEvents(); n != 1 || c != 1 {
			t.Fatalf("want one recover and the crash counted once, got %d and %d", n, c)
		}
	})

	t.Run("a recover the agent was stopped in", func(t *testing.T) {
		e := newAgentEnv(t)
		e.create()
		inspected := e.fd.called("GET /images/")
		e.fd.mu.Lock()
		e.fd.holdImages = true
		e.fd.mu.Unlock()
		e.fd.externalStop()
		e.waitFor("the recover to be under way", func() bool {
			op := e.a.currentOp()
			return op != nil && op.Kind == "recover" && e.fd.called("GET /images/") > inspected
		})
		e.stop()
		e.fd.mu.Lock()
		e.fd.holdImages = false
		e.fd.mu.Unlock()
		e.start()
		e.waitFor("the recover to be finished", e.onlineIdle)
		if n := e.opsOf("recover", api.OpSucceeded); n != 1 {
			t.Fatalf("want the recover finished once, got %d", n)
		}
		if n := e.status().CrashCount; n != 0 {
			t.Fatalf("stopping the agent is not a failed start, but %d failure(s) were counted", n)
		}
	})

	t.Run("a container created but never started", func(t *testing.T) {
		e := newAgentEnv(t)
		e.create()
		e.fd.addLog("[12:01:00 INFO]: PkBotBuilder joined the game")
		e.waitFor("session open", func() bool { return e.countRows(`SELECT COUNT(*) FROM sessions WHERE end_ts IS NULL`) == 1 })
		e.stop()
		// The agent was killed after creating the container and before starting it.
		e.fd.mu.Lock()
		c := e.fd.byName[e.cname()]
		c.running, c.started, c.finished = false, time.Time{}, time.Time{}
		e.fd.mu.Unlock()
		e.start()
		e.waitFor("the start to be finished", e.onlineIdle)
		if n := e.opsOf("recover", api.OpSucceeded); n != 1 {
			t.Fatalf("want one recover, got %d", n)
		}
		var reason string
		var uncertain int
		e.a.db.QueryRow(`SELECT end_reason, end_uncertain FROM sessions WHERE player = 'PkBotBuilder'`).Scan(&reason, &uncertain)
		if reason != "server_stopped" || uncertain != 1 {
			t.Fatalf("the session left open ended with reason %q, uncertain %d; want it ended before the start, uncertain", reason, uncertain)
		}
	})

	t.Run("a policy that gave up", func(t *testing.T) {
		e := newAgentEnv(t)
		e.create()
		for i := 1; i <= maxCrashes; i++ {
			e.waitFor("online before the crash", e.onlineIdle)
			e.fd.crash(1)
			e.waitFor("the crash to be counted", func() bool { return e.crashEvents() == i })
		}
		e.waitFor("the policy to give up", func() bool {
			return strings.Contains(e.status().LastError, "stopped restarting") && !e.a.busy()
		})
		e.stop()
		e.fd.mu.Lock()
		e.fd.bootExit = 134
		e.fd.mu.Unlock()
		e.start()
		e.waitFor("the policy to give up again", func() bool {
			return strings.Contains(e.status().LastError, "stopped trying to start") && !e.a.busy()
		})
		if n := e.countRows(`SELECT COUNT(*) FROM operations WHERE kind IN ('recover', 'auto-restart') AND status = 'failed'`); n != maxCrashes {
			t.Fatalf("want %d failed automatic starts after the agent restart, got %d", maxCrashes, n)
		}
		if n := e.crashEvents(); n != maxCrashes {
			t.Fatalf("the crashes from before the agent restart were counted again: %d crash events", n)
		}
	})
}

// Right after an agent restart the follower replays the previous run's log,
// whose ready line is history. A start that begins meanwhile, by the reconcile
// loop or by the user, succeeds only once the new run is ready.
func TestStartDuringLogReplayWaitsForTheNewRun(t *testing.T) {
	slowReplay := func(e *agentEnv, bootExit int) {
		e.fd.mu.Lock()
		e.fd.logDelay = 150 * time.Millisecond
		e.fd.bootExit = bootExit
		e.fd.mu.Unlock()
	}
	userStart := func(e *agentEnv) *api.Operation {
		code, out := e.call("POST", e.sp("/start"), map[string]any{"actor": "admin"})
		if code != 202 {
			e.t.Fatalf("start: %d %v", code, out)
		}
		return e.waitOp(out["id"].(string))
	}
	// crashedAndStopped leaves a run that was ready, crashed, and was then
	// stopped by the user, so nothing restarts it and its log ends with "Done".
	crashedAndStopped := func(t *testing.T) *agentEnv {
		e := newAgentEnv(t)
		e.stop()
		e.crashBackoff = []time.Duration{time.Minute}
		e.start()
		e.create()
		e.fd.crash(137)
		e.waitFor("the crash to be counted", func() bool { return e.crashEvents() == 1 })
		if code, out := e.call("POST", e.sp("/stop"), map[string]any{"actor": "admin"}); code != 200 {
			t.Fatalf("stop: %d %v", code, out)
		}
		return e
	}

	t.Run("an automatic start whose new run fails", func(t *testing.T) {
		e := newAgentEnv(t)
		e.create()
		inspected := e.fd.called("GET /images/")
		e.fd.mu.Lock()
		e.fd.holdImages = true
		e.fd.mu.Unlock()
		e.fd.crash(137)
		e.waitFor("the automatic restart to be under way", func() bool {
			op := e.a.currentOp()
			return op != nil && op.Kind == "auto-restart" && e.fd.called("GET /images/") > inspected
		})
		e.stop()
		e.fd.mu.Lock()
		e.fd.holdImages = false
		e.fd.mu.Unlock()
		slowReplay(e, 134)
		e.start()
		e.waitFor("the automatic starts to stop", func() bool {
			return strings.Contains(e.status().LastError, "stopped") && !e.a.busy()
		})
		if n := e.countRows(`SELECT COUNT(*) FROM operations WHERE kind IN ('recover', 'auto-restart') AND status = 'succeeded'`); n != 0 {
			t.Fatalf("%d automatic start(s) succeeded although every new run failed", n)
		}
		if st := e.status(); !strings.Contains(st.LastError, "stopped trying to start") {
			t.Fatalf("want the failed starts given up, got %q", st.LastError)
		}
	})

	t.Run("a start the user asks for whose new run fails", func(t *testing.T) {
		e := crashedAndStopped(t)
		e.stop()
		slowReplay(e, 134)
		e.start()
		if op := userStart(e); op.Status != api.OpFailed {
			t.Fatalf("the start succeeded although the new run failed: %+v", op)
		}
	})

	t.Run("a start the user asks for that comes up", func(t *testing.T) {
		e := crashedAndStopped(t)
		e.stop()
		slowReplay(e, 0)
		e.start()
		if op := userStart(e); op.Status != api.OpSucceeded {
			t.Fatalf("start: %+v", op)
		}
		e.waitFor("online", e.onlineIdle)
	})
}

// The Overview warns about low disk space with the preflight's thresholds and
// advice, and a backup refused for space records how much it needed, so its
// failure can be dropped once that much is free again.
func TestStatusWarnsAboutLowDiskWithThePreflightAdvice(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	if w := e.a.Machine(context.Background()).DiskWarning; w != nil {
		t.Fatalf("50 GB free needs no warning: %+v", w)
	}
	e.diskFree.Store(4 << 30)
	if w := e.a.Machine(context.Background()).DiskWarning; w == nil || w.Status != "warn" || !strings.Contains(w.Fix, "Keep at least 5 GB free") {
		t.Fatalf("4 GB free: %+v", w)
	}
	e.diskFree.Store(1 << 20)
	if w := e.a.Machine(context.Background()).DiskWarning; w == nil || w.Status != "fail" || !strings.Contains(w.Fix, "Free at least 5 GB") {
		t.Fatalf("1 MB free: %+v", w)
	}
	code, out := e.call("POST", e.sp("/backups"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("backup: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpFailed || op.Detail["neededBytes"] == nil {
		t.Fatalf("a backup refused for space must record how much it needed: %+v", op)
	}
}

func TestExternalCleanStopIsRestored(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.fd.externalStop()
	e.waitFor("recover event", func() bool {
		return e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'server_stopped_externally'`) == 1
	})
	e.waitFor("online again", func() bool { return e.status().Phase == api.PhaseOnline })
	if n := e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'server_crashed'`); n != 0 {
		t.Fatalf("a clean external stop was counted as a crash")
	}
}

func TestJarChecksumMismatchIsNeverRun(t *testing.T) {
	e := newAgentEnv(t)
	e.fill.set(strings.Repeat("0", 64), nil)
	code, out := e.startCreate(nil)
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "checksum") {
		t.Fatalf("mismatched jar must fail: %+v", op)
	}
	if e.fd.containerCount(e.cname()+"-setup") != 0 {
		t.Fatal("setup container must be removed")
	}
	e.fd.mu.Lock()
	_, created := e.fd.byName[e.cname()]
	e.fd.mu.Unlock()
	if created {
		t.Fatal("the server container must not be created with an unverified jar")
	}
	if _, err := os.Stat(filepath.Join(e.dataDir(), "paper-26.1.2-74.jar")); err == nil {
		t.Fatal("the unverified jar must be deleted")
	}
}

func TestPortCollisionHasActionableError(t *testing.T) {
	e := newAgentEnv(t)
	e.fd.startErr = "driver failed programming external connectivity: Bind for 0.0.0.0:25565 failed: port is already allocated"
	code, out := e.startCreate(nil)
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "25565") || !strings.Contains(op.Hint, "ss -ltnp") {
		t.Fatalf("port collision: %+v", op)
	}
	if n := e.fd.containerCount(e.cname()); n != 0 {
		t.Fatalf("a container whose start failed must be discarded, found %d", n)
	}
	// The hint says to press Start after fixing the cause; nothing retries meanwhile.
	time.Sleep(300 * time.Millisecond)
	if d := e.srv().desired(); d != api.DesiredStopped {
		t.Fatalf("after a failed start the desired state must be stopped, got %s", d)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM operations WHERE kind IN ('recover', 'auto-restart')`); n != 0 {
		t.Fatalf("a failed start was retried in the background %d times", n)
	}
	e.fd.mu.Lock()
	e.fd.startErr = ""
	e.fd.mu.Unlock()
	code, out = e.call("POST", e.sp("/start"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("start after freeing the port: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("start after freeing the port: %+v", op)
	}
}

func TestFailedAutomaticStartsBackOffAndGiveUp(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.fd.mu.Lock()
	e.fd.startErr = "driver failed programming external connectivity: Bind for 0.0.0.0:25565 failed: port is already allocated"
	e.fd.mu.Unlock()
	// The container disappears while the server should be running, and every
	// automatic start then fails.
	if err := e.a.docker.ContainerRemove(context.Background(), e.cname(), true); err != nil {
		t.Fatal(err)
	}
	failed := func() int {
		return e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'recover' AND status = 'failed'`)
	}
	e.waitFor("automatic starts to give up", func() bool { return failed() >= maxCrashes && !e.a.busy() })
	time.Sleep(400 * time.Millisecond)
	if n := e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'recover'`); n != maxCrashes {
		t.Fatalf("want %d automatic attempts before giving up, got %d", maxCrashes, n)
	}
	st := e.status()
	if st.Desired != api.DesiredRunning || !strings.Contains(st.LastError, "stopped trying to start") {
		t.Fatalf("the divergence and the reason must stay visible: desired=%s lastError=%q", st.Desired, st.LastError)
	}
	e.fd.mu.Lock()
	e.fd.startErr = ""
	e.fd.mu.Unlock()
	code, out := e.call("POST", e.sp("/start"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("start after fixing the cause: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("start after fixing the cause: %+v", op)
	}
}

func TestMetricsShowGapsNotZeros(t *testing.T) {
	e := newAgentEnv(t)
	e.stop()
	e.start()
	base := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	e.addIdleServer()
	e.a.db.Exec(`DELETE FROM samples`)
	e.setCollectingSince(base.Format(time.RFC3339Nano))
	ins := func(t0 time.Time, state string, players any) {
		e.a.db.Exec(`INSERT INTO samples(server_id, ts, state, players_online) VALUES(?,?,?,?)`, e.sid, t0.UnixMilli(), state, players)
	}
	for m := 0; m < 30; m++ { // 30 min online with 2 players
		for s := 0; s < 60; s += 15 {
			ins(base.Add(time.Duration(m)*time.Minute+time.Duration(s)*time.Second), "online", 2)
		}
	}
	for m := 30; m < 45; m++ { // 15 min stopped
		for s := 0; s < 60; s += 15 {
			ins(base.Add(time.Duration(m)*time.Minute+time.Duration(s)*time.Second), "stopped", nil)
		}
	}
	// 45..90: nothing at all (collector down), then online with 0 players.
	for m := 90; m < 100; m++ {
		for s := 0; s < 60; s += 15 {
			ins(base.Add(time.Duration(m)*time.Minute+time.Duration(s)*time.Second), "online", 0)
		}
	}
	e.a.opts.SampleInterval = 15 * time.Second
	m, err := e.srv().Metrics("24h", base.Add(100*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	find := func(at time.Time) api.MetricsBucket {
		for _, b := range m.Buckets {
			if !at.Before(b.Start) && at.Before(b.Start.Add(time.Duration(m.BucketSeconds)*time.Second)) {
				return b
			}
		}
		t.Fatalf("no bucket for %s", at)
		return api.MetricsBucket{}
	}
	if b := find(base.Add(5 * time.Minute)); b.State != "online" || b.PlayersMax == nil || *b.PlayersMax != 2 {
		t.Fatalf("online bucket: %+v", b)
	}
	if b := find(base.Add(35 * time.Minute)); b.State != "offline" || b.PlayersMax != nil {
		t.Fatalf("stopped period must be offline with no player value, got %+v", b)
	}
	if b := find(base.Add(65 * time.Minute)); b.State != "no_data" || b.PlayersMax != nil || b.Coverage != 0 {
		t.Fatalf("collector gap must be no_data, not zero: %+v", b)
	}
	if b := find(base.Add(95 * time.Minute)); b.State != "online" || b.PlayersMax == nil || *b.PlayersMax != 0 {
		t.Fatalf("a real zero must stay a zero: %+v", b)
	}
	if b := find(base.Add(-30 * time.Minute)); b.State != "not_collected" {
		t.Fatalf("time before install must be not_collected, got %+v", b)
	}
	var sawGap, sawOffline bool
	for _, g := range m.Gaps {
		if g.Kind == "collector_down" && !g.From.After(base.Add(45*time.Minute)) && !g.To.Before(base.Add(90*time.Minute)) {
			sawGap = true
		}
		if g.Kind == "server_offline" {
			sawOffline = true
		}
	}
	if !sawGap || !sawOffline {
		t.Fatalf("gaps not reported: %+v", m.Gaps)
	}
}

func TestDailySummaryFlagsIncompleteSessions(t *testing.T) {
	e := newAgentEnv(t)
	now := time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC)
	e.addIdleServer()
	e.setCollectingSince(now.Add(-48 * time.Hour).Format(time.RFC3339Nano))
	e.a.db.Exec(`INSERT INTO sessions(server_id, player, start_ts, end_ts, end_reason, source) VALUES(?, 'A', ?, ?, 'left', 'server_log')`, e.sid, now.Add(-2*time.Hour).UnixMilli(), now.Add(-time.Hour).UnixMilli())
	e.a.db.Exec(`INSERT INTO sessions(server_id, player, start_ts, end_ts, end_reason, end_uncertain, source) VALUES(?, 'B', ?, ?, 'server_crashed', 1, 'server_log')`, e.sid, now.Add(-3*time.Hour).UnixMilli(), now.Add(-150*time.Minute).UnixMilli())
	e.a.db.Exec(`INSERT INTO sessions(server_id, player, start_ts, end_ts, end_reason, start_uncertain, source) VALUES(?, 'C', ?, ?, 'left', 1, 'player_list')`, e.sid, now.Add(-26*time.Hour).UnixMilli(), now.Add(-25*time.Hour).UnixMilli())
	s, err := e.srv().Summary(2, "UTC", now)
	if err != nil {
		t.Fatal(err)
	}
	// A session that ended in a crash makes the day's total an upper bound; one
	// whose start was not seen makes it a lower bound.
	today, yesterday := s.Days[len(s.Days)-1], s.Days[len(s.Days)-2]
	if today.UniquePlayers != 2 || today.PlaytimeSeconds != 3600+1800 || !today.PlaytimeUpperBound || today.PlaytimeLowerBound {
		t.Fatalf("today, with a crash-ended session, must be an upper bound: %+v", today)
	}
	if !yesterday.PlaytimeLowerBound || yesterday.PlaytimeUpperBound {
		t.Fatalf("yesterday, with a session whose start was not seen, must be a lower bound: %+v", yesterday)
	}
	if s.UncertainSessions != 2 || today.Coverage >= 1 {
		t.Fatalf("summary must flag uncertainty and incomplete coverage: %+v", s)
	}
	if _, err := e.srv().Summary(7, "Not/AZone", now); err == nil {
		t.Fatal("invalid time zone accepted")
	}
}

func TestCoverageCountsTimeNotSamples(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	var s []sample
	// The agent was down from 60 s to 100 s; its restart sampled at once (100 s)
	// and again on the next tick (101 s), so the sample count barely drops.
	for _, sec := range []int{0, 15, 30, 45, 100, 101, 115, 130, 145} {
		s = append(s, sample{ts: t0.Add(time.Duration(sec) * time.Second)})
	}
	if got := coveredFraction(s, t0, t0.Add(160*time.Second), 15*time.Second); got < 0.749 || got > 0.751 {
		t.Fatalf("covered fraction = %.3f, want 0.75 (120 of 160 s)", got)
	}
	since := t0
	if got := coverage(s, t0, t0.Add(160*time.Second), 15*time.Second, &since); got >= 0.95 {
		t.Fatalf("a day with a 40 s collection gap in 160 s must not look complete: %.3f", got)
	}
	if got := coveredFraction(s[:4], t0, t0.Add(60*time.Second), 15*time.Second); got != 1 {
		t.Fatalf("regular samples must cover their span fully: %.3f", got)
	}
}

func TestRetentionBoundsTables(t *testing.T) {
	e := newAgentEnv(t)
	e.stop()
	e.start()
	e.a.opts.Retention.MaxSamples = 100
	e.a.opts.Retention.MaxEvents = 50
	old := time.Now().Add(-400 * 24 * time.Hour).UnixMilli()
	for i := 0; i < 300; i++ {
		e.a.db.Exec(`INSERT OR IGNORE INTO samples(ts, state) VALUES(?, 'online')`, time.Now().UnixMilli()-int64(i))
		e.a.db.Exec(`INSERT INTO events(ts, kind, source, dedup_key, ingested_at) VALUES(?, 'join', 'test', ?, 0)`, time.Now().UnixMilli(), fmt.Sprintf("k%d", i))
	}
	e.a.db.Exec(`INSERT INTO audit(ts, actor, action, result) VALUES(?, 'x', 'old', 'ok')`, old)
	e.a.db.Exec(`INSERT INTO sessions(player, start_ts, end_ts, source) VALUES('Old', ?, ?, 'test')`, old, old+1)
	e.a.prune()
	if n := e.countRows(`SELECT COUNT(*) FROM samples`); n > 100 {
		t.Fatalf("samples not capped: %d", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM events`); n > 50 {
		t.Fatalf("events not capped: %d", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'old'`); n != 0 {
		t.Fatal("audit older than retention was kept")
	}
	if n := e.countRows(`SELECT COUNT(*) FROM sessions WHERE player = 'Old'`); n != 0 {
		t.Fatal("sessions older than retention were kept")
	}
}

func TestConsoleBufferIsBounded(t *testing.T) {
	r := newRing(consoleCapacity)
	base := time.Now()
	for i := 0; i < consoleCapacity+1500; i++ {
		r.append(base.Add(time.Duration(i)), fmt.Sprintf("line %d", i))
	}
	if r.len() != consoleCapacity {
		t.Fatalf("console buffer grew to %d lines", r.len())
	}
	resp := r.since(r.epoch, 1, 5000)
	if !resp.Truncated || len(resp.Lines) != consoleCapacity {
		t.Fatalf("since: %d lines truncated=%v", len(resp.Lines), resp.Truncated)
	}
	if got := r.since(r.epoch, resp.Next, 10); len(got.Lines) != 0 {
		t.Fatal("no new lines expected")
	}
}

// backupAndStage makes a backup of the current world and stages it for a
// restore, returning the restore id and its confirmation phrase.
func (e *agentEnv) backupAndStage() (string, string) {
	e.t.Helper()
	code, out := e.call("POST", e.sp("/backups"), map[string]any{"actor": "admin"})
	if code != 202 {
		e.t.Fatalf("backup: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		e.t.Fatalf("backup op: %+v", op)
	}
	e.waitFor("online after backup", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
	list, _ := e.srv().listBackups(`kind = 'manual'`)
	code, preview := e.call("POST", e.sp("/backups/"+list[0].ID+"/restore"), map[string]any{"actor": "admin"})
	if code != 200 {
		e.t.Fatalf("stage: %d %v", code, preview)
	}
	return preview["id"].(string), preview["confirmPhrase"].(string)
}

func (e *agentEnv) applyRestore(id, phrase string) *api.Operation {
	e.t.Helper()
	code, out := e.call("POST", "/v1/restore/"+id+"/apply", map[string]any{"confirm": phrase, "actor": "admin"})
	if code != 202 {
		e.t.Fatalf("apply: %d %v", code, out)
	}
	return e.waitOp(out["id"].(string))
}

func TestRestoreNeedsAVerifiedRollbackArchive(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	id, phrase := e.backupAndStage()
	world := filepath.Join(e.dataDir(), "world")
	if err := os.WriteFile(filepath.Join(world, "later.dat"), []byte("built after the backup"), 0o640); err != nil {
		t.Fatal(err)
	}
	// The rollback archive's recorded checksum stops matching its file, as if
	// the disk had changed it, so the archive is written but fails its check.
	if _, err := e.a.db.Exec(`CREATE TRIGGER damaged_rollback AFTER INSERT ON backups WHEN NEW.kind = 'rollback'
		BEGIN UPDATE backups SET sha256 = '` + strings.Repeat("0", 64) + `' WHERE id = NEW.id; END`); err != nil {
		t.Fatal(err)
	}
	live := worldHash(t, e.dataDir())
	op := e.applyRestore(id, phrase)
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "verified rollback archive") {
		t.Fatalf("a restore without a verified rollback archive must refuse: %+v", op)
	}
	if got := worldHash(t, e.dataDir()); got != live {
		t.Fatal("the live world was replaced although its rollback archive failed verification")
	}
	e.waitFor("previous world running again", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
}

func TestRestoreUndoesTheSwapWhenSettingsCannotBeSaved(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	id, phrase := e.backupAndStage()
	world := filepath.Join(e.dataDir(), "world")
	if err := os.WriteFile(filepath.Join(world, "later.dat"), []byte("built after the backup"), 0o640); err != nil {
		t.Fatal(err)
	}
	live := worldHash(t, e.dataDir())
	e.failConfigSaves()
	op := e.applyRestore(id, phrase)
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "previous world was put back") {
		t.Fatalf("a restore whose settings cannot be saved must fail and say so: %+v", op)
	}
	if got := worldHash(t, e.dataDir()); got != live {
		t.Fatal("the restored world was left in place without its settings")
	}
	if left, _ := filepath.Glob(e.dataDir() + ".replaced-*"); len(left) != 0 {
		t.Fatalf("the moved-aside world was left behind: %v", left)
	}
	e.waitFor("previous world running again", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
}

// A world a restore would refuse is not backed up at all: the backup fails
// before anything is written, says why, and the server comes back.
func TestBackupRefusesAWorldARestoreWouldRefuse(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	world := filepath.Join(e.dataDir(), "world")
	deep := filepath.Join(world, strings.Repeat("a", 250), strings.Repeat("b", 250), strings.Repeat("c", 250), strings.Repeat("d", 250))
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "r.mca"), []byte("region"), 0o640); err != nil {
		t.Fatal(err)
	}
	code, out := e.call("POST", e.sp("/backups"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("backup: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "a restore would refuse it") || !strings.Contains(op.Error, "entry name too long") {
		t.Fatalf("backing up a world a restore would refuse must fail and say why: %+v", op)
	}
	if !strings.Contains(op.Hint, "Rename or remove that file in "+e.dataDir()) {
		t.Fatalf("the refusal must say what to do: %q", op.Hint)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM backups`); n != 0 {
		t.Fatalf("%d backup rows recorded for a refused backup", n)
	}
	if files, _ := os.ReadDir(e.cfg.BackupsDir()); len(files) != 0 {
		t.Fatalf("a refused backup left files: %v", files)
	}
	e.waitFor("server running again", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
}

// Re-compressing a backup keeps every file and per-file hash, so only the
// whole-archive SHA-256 recorded at backup time can notice. Both checking
// the backup again and restoring from it must refuse.
func TestRecompressedBackupFailsItsRecordedChecksum(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	code, out := e.call("POST", e.sp("/backups"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("backup: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("backup op: %+v", op)
	}
	list, _ := e.srv().listBackups(`kind = 'manual'`)
	b := list[0]
	recompress(t, filepath.Join(e.cfg.BackupsDir(), b.FileName))
	e.waitFor("idle", func() bool { return !e.a.busy() })

	code, out = e.call("POST", e.sp("/backups/"+b.ID+"/verify"), map[string]any{"actor": "admin"})
	if code != 200 || out["verified"] != false || !strings.Contains(fmt.Sprint(out["verifyError"]), "does not match the recorded") {
		t.Fatalf("checking a re-compressed backup again: %d %v", code, out)
	}
	code, out = e.call("POST", e.sp("/backups/"+b.ID+"/restore"), map[string]any{"actor": "admin"})
	if code != http.StatusUnprocessableEntity || !strings.Contains(fmt.Sprint(out["error"]), "no longer matches its recorded checksum") {
		t.Fatalf("restoring from a re-compressed backup: %d %v", code, out)
	}
	if entries, _ := os.ReadDir(e.cfg.StagingDir()); len(entries) != 0 {
		t.Fatalf("the refused restore left staging data: %v", entries)
	}
}

// recompress rewrites a gzip file with the same content but different bytes.
func recompress(t *testing.T, path string) {
	t.Helper()
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(orig))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Header.Comment = "re-compressed"
	zw.Write(raw)
	zw.Close()
	if bytes.Equal(buf.Bytes(), orig) {
		t.Fatal("re-compressing did not change the file")
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A stage holds an archive copy and its extracted world. A failed restore
// deletes its stage (the backup or uploaded file is still there to retry
// from), and the agent deletes stages left from before it started.
func TestFailedRestoreDeletesItsStageAndStartPrunesLeftovers(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	id, phrase := e.backupAndStage()
	e.failConfigSaves()
	if op := e.applyRestore(id, phrase); op.Status != api.OpFailed {
		t.Fatalf("the restore should fail: %+v", op)
	}
	if left, _ := os.ReadDir(e.cfg.StagingDir()); len(left) != 0 {
		t.Fatalf("the failed restore left its stage: %v", left)
	}
	e.waitFor("idle", func() bool { return !e.a.busy() })
	list, _ := e.srv().listBackups(`kind = 'manual'`)
	if code, out := e.call("POST", e.sp("/backups/"+list[0].ID+"/restore"), map[string]any{"actor": "admin"}); code != 200 {
		t.Fatalf("stage: %d %v", code, out)
	}
	if left, _ := os.ReadDir(e.cfg.StagingDir()); len(left) != 1 {
		t.Fatalf("expected the new preview's stage, got %v", left)
	}
	e.stop()
	e.start()
	if left, _ := os.ReadDir(e.cfg.StagingDir()); len(left) != 0 {
		t.Fatalf("stages from before the agent started were not pruned: %v", left)
	}
}

// When a restore fails after the swap and putting the previous world back
// fails too, the live directory is missing: nothing may be deleted. The
// previous world stays in its aside copy, the restored copy is moved out of
// the staging folder (which the agent clears at start), the stage is kept,
// and the error names both copies.
func TestRestoreKeepsBothCopiesWhenPuttingThePreviousWorldBackFails(t *testing.T) {
	for _, step := range []string{"settings save", "moving the restored world into place"} {
		t.Run(step, func(t *testing.T) {
			e := newAgentEnv(t)
			e.create()
			id, phrase := e.backupAndStage()
			live, staged := e.dataDir(), filepath.Join(e.cfg.StagingDir(), id, "data")
			if step == "settings save" {
				e.failConfigSaves()
			}
			renameDir = func(from, to string) error {
				if to == live && (strings.HasPrefix(from, live+".replaced-") || step != "settings save" && from == staged) {
					return errors.New("injected rename failure")
				}
				return os.Rename(from, to)
			}
			t.Cleanup(func() { renameDir = os.Rename })
			op := e.applyRestore(id, phrase)
			copies := func() (restored, previous string) {
				t.Helper()
				failed, _ := filepath.Glob(live + ".failed-restore-*")
				asides, _ := filepath.Glob(live + ".replaced-*")
				if len(failed) != 1 || len(asides) != 1 {
					t.Fatalf("want the restored copy outside the stage and the previous world's aside copy, got %v and %v: %+v", failed, asides, op)
				}
				for _, dir := range []string{failed[0], asides[0]} {
					if _, err := os.Stat(filepath.Join(dir, "world")); err != nil {
						t.Fatalf("%s has no world: %+v", dir, op)
					}
				}
				return failed[0], asides[0]
			}
			restored, previous := copies()
			if _, err := os.Stat(filepath.Join(e.cfg.StagingDir(), id)); err != nil {
				t.Fatal("the stage must be kept when the undo fails")
			}
			if op.Status != api.OpFailed || !strings.Contains(op.Error, previous) || !strings.Contains(op.Error, restored) {
				t.Fatalf("the error must name the previous world's copy and the restored copy: %+v", op)
			}
			e.stop()
			e.start()
			copies()
		})
	}
}

func worldHash(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() && strings.Contains(p, "/world/") {
			b, _ := os.ReadFile(p)
			fmt.Fprintf(h, "%s:%x\n", strings.TrimPrefix(p, dir), sha256.Sum256(b))
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

func (e *agentEnv) upload(archive []byte) (int, map[string]any) {
	req, _ := http.NewRequest("POST", e.ts.URL+e.sp("/restore/upload"), bytes.NewReader(archive))
	req.Header.Set("X-Playkeeper-Actor", "admin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestBackupRestoreRollbackAndRefusals(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	world := filepath.Join(e.dataDir(), "world")
	os.WriteFile(filepath.Join(e.dataDir(), "server.properties"), []byte("level-name=world\nrcon.password=topsecret\n"), 0o644)
	os.WriteFile(filepath.Join(world, "marker.txt"), []byte("nonce-original"), 0o644)
	code, out := e.call("POST", e.sp("/backups"), map[string]any{"actor": "admin", "note": "first"})
	if code != 202 {
		t.Fatalf("backup: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded {
		t.Fatalf("backup op: %+v", op)
	}
	list, _ := e.srv().listBackups("")
	if len(list) != 1 || list[0].Verified == nil || !*list[0].Verified || list[0].Location != "on-host" {
		t.Fatalf("backup record: %+v", list)
	}
	b := list[0]
	archive, err := os.ReadFile(filepath.Join(e.cfg.BackupsDir(), b.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(archive); hex.EncodeToString(sum[:]) != b.SHA256 {
		t.Fatal("recorded whole-archive SHA-256 does not match the file")
	}
	if bytes.Contains(archive, []byte("topsecret")) {
		t.Fatal("backup leaks the RCON password")
	}
	e.waitFor("online after backup", func() bool { return e.status().Phase == api.PhaseOnline })

	os.WriteFile(filepath.Join(world, "marker.txt"), []byte("nonce-changed"), 0o644)
	live := worldHash(t, e.dataDir())

	flipped := append([]byte(nil), archive...)
	flipped[len(flipped)/2] ^= 0xff
	evil := craftTar(t, map[string]string{"playkeeper-backup/data/../../../etc/cron.d/x": "* * * * * root id"})
	for name, bad := range map[string][]byte{"flipped byte": flipped, "truncated": archive[:len(archive)/2], "traversal": evil, "not gzip": []byte("hello")} {
		code, out := e.upload(bad)
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("%s archive: %d %v", name, code, out)
		}
		if got := worldHash(t, e.dataDir()); got != live {
			t.Fatalf("%s archive changed the live world", name)
		}
	}
	entries, _ := os.ReadDir(e.cfg.StagingDir())
	if len(entries) != 0 {
		t.Fatalf("refused uploads left staging data: %v", entries)
	}

	code, preview := e.upload(archive)
	if code != 200 || preview["compatible"] != true || preview["willCreateRollback"] != true || preview["confirmPhrase"] != "replace world" {
		t.Fatalf("preview: %d %v", code, preview)
	}
	id := preview["id"].(string)
	if code, _ := e.call("POST", "/v1/restore/"+id+"/apply", map[string]any{"actor": "admin", "confirm": "yes"}); code != 400 {
		t.Fatalf("wrong confirmation accepted: %d", code)
	}
	if got := worldHash(t, e.dataDir()); got != live {
		t.Fatal("a refused confirmation changed the world")
	}
	code, out = e.call("POST", "/v1/restore/"+id+"/apply", map[string]any{"actor": "admin", "confirm": "replace world"})
	if code != 202 {
		t.Fatalf("apply: %d %v", code, out)
	}
	op = e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded {
		t.Fatalf("restore: %+v", op)
	}
	if got, _ := os.ReadFile(filepath.Join(world, "marker.txt")); string(got) != "nonce-original" {
		t.Fatalf("restored marker = %q", got)
	}
	list, _ = e.srv().listBackups(`kind = 'rollback'`)
	if len(list) != 1 || list[0].Verified == nil || !*list[0].Verified {
		t.Fatalf("rollback archive: %+v", list)
	}
	// Restoring the rollback archive brings the replaced state back.
	e.waitFor("idle", func() bool { return !e.a.busy() })
	code, preview = e.call("POST", e.sp("/backups/"+list[0].ID+"/restore"), map[string]any{"actor": "admin"})
	if code != 200 {
		t.Fatalf("stage rollback: %d %v", code, preview)
	}
	code, out = e.call("POST", "/v1/restore/"+preview["id"].(string)+"/apply", map[string]any{"actor": "admin", "confirm": "replace world"})
	if code != 202 {
		t.Fatalf("apply rollback: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("rollback restore: %+v", op)
	}
	if got, _ := os.ReadFile(filepath.Join(world, "marker.txt")); string(got) != "nonce-changed" {
		t.Fatalf("after restoring the rollback archive marker = %q", got)
	}
	actions := map[string]bool{}
	audit, _ := e.a.listAudit(100)
	for _, a := range audit {
		actions[a.Action] = true
	}
	for _, want := range []string{"create", "eula.accepted", "backup.created", "restore.uploaded", "restore.applied"} {
		if !actions[want] {
			t.Errorf("no audit row for %s (have %v)", want, actions)
		}
	}
}

func craftTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestNoIPsOrSecretsAreStored(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.fd.addLog("[12:01:00 INFO]: PkBotFriend[/203.0.113.9:5555] logged in with entity id 1")
	e.fd.addLog("[12:01:00 INFO]: PkBotFriend joined the game")
	code, _ := e.call("POST", e.sp("/command"), map[string]any{"actor": "admin", "command": "ban-ip 203.0.113.77"})
	if code != 200 {
		t.Fatalf("command: %d", code)
	}
	e.waitFor("join stored", func() bool { return e.countRows(`SELECT COUNT(*) FROM events WHERE kind='join'`) == 1 })
	pw, _ := e.srv().rconPassword()
	rows, _ := e.a.db.Query(`SELECT COALESCE(player,'') || COALESCE(uuid,'') || detail FROM events
		UNION ALL SELECT actor || action || target || detail FROM audit
		UNION ALL SELECT value FROM kv UNION ALL SELECT error || hint || detail FROM operations`)
	defer rows.Close()
	for rows.Next() {
		var s string
		rows.Scan(&s)
		if strings.Contains(s, "203.0.113") {
			t.Fatalf("an IP address was stored: %q", s)
		}
		if strings.Contains(s, pw) {
			t.Fatal("the RCON password was stored in the database")
		}
	}
	for _, l := range e.srv().console.since("", 0, consoleCapacity).Lines {
		if strings.Contains(l.Text, "203.0.113") {
			t.Fatalf("console shows an IP: %q", l.Text)
		}
	}
}

func TestWhitelistAndConsoleAreAudited(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	if code, out := e.call("POST", e.sp("/whitelist"), map[string]any{"actor": "admin", "name": "PkBotFriend"}); code != 200 {
		t.Fatalf("invite: %d %v", code, out)
	}
	if code, _ := e.call("POST", e.sp("/whitelist"), map[string]any{"actor": "admin", "name": "bad name;id"}); code != 400 {
		t.Fatalf("bad name accepted: %d", code)
	}
	code, out := e.call("POST", e.sp("/command"), map[string]any{"actor": "admin", "command": "$(id)"})
	if code != 200 || !strings.Contains(out["output"].(string), "Unknown or incomplete command") {
		t.Fatalf("$(id) must reach Minecraft as text: %d %v", code, out)
	}
	if code, _ := e.call("POST", e.sp("/settings"), map[string]any{"actor": "admin", "maxPlayers": 20}); code != 200 {
		t.Fatalf("settings: %d", code)
	}
	if !e.status().PendingRestart {
		t.Fatal("changed settings must show a pending restart")
	}
	audit, _ := e.a.listAudit(50)
	want := map[string]bool{"whitelist.add": false, "console.command": false, "settings.changed": false}
	for _, a := range audit {
		if _, ok := want[a.Action]; ok && a.Actor == "admin" {
			want[a.Action] = true
		}
	}
	for k, v := range want {
		if !v {
			t.Errorf("missing audit row %s", k)
		}
	}
}
