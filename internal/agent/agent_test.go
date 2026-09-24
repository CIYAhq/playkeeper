package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
)

type agentEnv struct {
	t      *testing.T
	dir    string
	cfg    config.Config
	fd     *fakeDocker
	rcon   *fakeRCON
	slp    string
	a      *Agent
	ts     *httptest.Server
	jarSum string
	mu     sync.Mutex
}

func newAgentEnv(t *testing.T) *agentEnv {
	t.Helper()
	dir := t.TempDir()
	e := &agentEnv{t: t, dir: dir}
	e.fd = startFakeDocker(t, filepath.Join(dir, "docker.sock"))
	sum := sha256.Sum256(e.fd.jarContent)
	e.jarSum = hex.EncodeToString(sum[:])
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
		// The agent generates the RCON password on first start; the fake learns it lazily.
		e.rcon = startFakeRCON(e.t, "")
		e.slp = startFakeSLP(e.t, e.rcon)
	}
	a, err := New(Options{
		Config: e.cfg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		SampleInterval: 100 * time.Millisecond, ReconcileInterval: 50 * time.Millisecond, CrashBackoff: []time.Duration{0},
		RCONAddr: func(string) string { return e.rcon.addr }, PingAddr: e.slp,
		HostMemoryMB: func() int { return 4096 }, DiskUsage: func(string) (int64, int64, error) { return 50 << 30, 100 << 30, nil },
		CheckEgress: func(context.Context) error { return nil }, PortInUse: func(int) bool { return false },
		JarSHA256: func(string) string { return e.jarSum }, StopTimeout: 5 * time.Second, ReadyTimeout: 10 * time.Second,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := a.ensureRCONSecret(); err != nil {
		e.t.Fatal(err)
	}
	pw, _ := a.rconPassword()
	e.rcon.mu.Lock()
	e.rcon.password = pw
	e.rcon.mu.Unlock()
	a.Start()
	e.a = a
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
	return e.a.Status(context.Background())
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

func (e *agentEnv) create() {
	e.t.Helper()
	code, out := e.call("POST", "/v1/server", map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin"})
	if code != 202 {
		e.t.Fatalf("create: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded {
		e.t.Fatalf("create failed: %+v", op)
	}
	e.waitFor("online", func() bool { return e.status().Phase == api.PhaseOnline })
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
	code, out := e.call("POST", "/v1/server", map[string]any{"acceptEula": false, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin"})
	if code != 400 || out["code"] != api.CodeEULARequired {
		t.Fatalf("create without EULA: %d %v", code, out)
	}
	code, _ = e.call("POST", "/v1/server/start", map[string]any{"actor": "admin"})
	if code != 409 {
		t.Fatalf("start before create/EULA: %d", code)
	}
	time.Sleep(200 * time.Millisecond)
	if e.fd.pulls != 0 || e.fd.containerCount("") != 0 || e.fd.called("POST /images/create") != 0 || e.fd.called("POST /containers/create") != 0 {
		t.Fatalf("EULA refusal still touched Docker: pulls=%d containers=%d calls=%v", e.fd.pulls, e.fd.containerCount(""), e.fd.calls)
	}
	if _, err := os.Stat(filepath.Join(e.cfg.ServerDataDir(), "eula.txt")); err == nil {
		t.Fatal("eula.txt must not exist before acceptance")
	}
	if st := e.status(); st.Phase != api.PhaseNotCreated || st.Config != nil {
		t.Fatalf("status after refusal: %+v", st)
	}
}

func TestInvalidInputsAndUnknownVerbsAreRejected(t *testing.T) {
	e := newAgentEnv(t)
	bad := []struct {
		name, method, path string
		body               any
		want               int
	}{
		{"negative RAM", "POST", "/v1/server", map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": -1, "actor": "admin"}, 400},
		{"huge RAM", "POST", "/v1/server", map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 99999, "actor": "admin"}, 400},
		{"off-list RAM", "POST", "/v1/server", map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1000, "actor": "admin"}, 400},
		{"shell in version", "POST", "/v1/server", map[string]any{"acceptEula": true, "versionId": "latest; id", "memoryMB": 1536, "actor": "admin"}, 400},
		{"latest version", "POST", "/v1/server", map[string]any{"acceptEula": true, "versionId": "latest", "memoryMB": 1536, "actor": "admin"}, 400},
		{"extra argument", "POST", "/v1/server", `{"acceptEula":true,"versionId":"paper-26.1.2","memoryMB":1536,"actor":"admin","cmd":"rm -rf /"}`, 400},
		{"trailing data", "POST", "/v1/server/start", `{"actor":"admin"} {"actor":"x"}`, 400},
		{"missing actor", "POST", "/v1/server/start", map[string]any{}, 400},
		{"control char MOTD", "POST", "/v1/server", map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "motd": "a\nb", "actor": "admin"}, 400},
		{"rm verb", "POST", "/v1/server/rm", map[string]any{"actor": "admin"}, 404},
		{"rm top-level", "POST", "/v1/rm", map[string]any{"actor": "admin"}, 404},
		{"shell verb", "POST", "/v1/exec", map[string]any{"cmd": "id"}, 404},
		{"wrong method", "DELETE", "/v1/server", nil, 404},
		{"traversal backup id", "GET", "/v1/backups/..%2F..%2Fetc%2Fpasswd/download", nil, 400},
		{"traversal backup verify", "POST", "/v1/backups/..%2F..%2Fetc%2Fpasswd/verify", map[string]any{"actor": "admin"}, 400},
		{"traversal restore id", "GET", "/v1/restore/..%2F..%2Fetc", nil, 400},
		{"bad whitelist name", "DELETE", "/v1/server/whitelist/%3Bid?actor=admin", nil, 400},
		{"bad operation id", "GET", "/v1/operations/..%2Fx", nil, 400},
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
	if n := e.fd.containerCount(containerName); n != 1 {
		t.Fatalf("containers after create: %d", n)
	}
	e.fd.mu.Lock()
	c := e.fd.byName[containerName]
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
	code, out := e.call("POST", "/v1/server/start", map[string]any{"actor": "admin"})
	if code != 200 || out["noop"] != true {
		t.Fatalf("second start should be a no-op: %d %v", code, out)
	}
	if n := e.fd.containerCount(containerName); n != 1 {
		t.Fatalf("second start created another container: %d", n)
	}
	code, out = e.call("POST", "/v1/server/stop", map[string]any{"actor": "admin"})
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
	if code, out := e.call("POST", "/v1/server/stop", map[string]any{"actor": "admin"}); code != 200 || out["noop"] != true {
		t.Fatalf("second stop should be a no-op: %d %v", code, out)
	}
	if code, _ := e.call("POST", "/v1/server/restart", map[string]any{"actor": "admin"}); code != 409 {
		t.Fatalf("restart while stopped must conflict: %d", code)
	}
	time.Sleep(300 * time.Millisecond)
	if st := e.status(); st.Phase != api.PhaseStopped {
		t.Fatalf("reconciler restarted a server the user stopped: %s", st.Phase)
	}
	code, out = e.call("POST", "/v1/server/start", map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("start: %d %v", code, out)
	}
	e.waitOp(out["id"].(string))
	e.waitFor("online again", func() bool { return e.status().Phase == api.PhaseOnline })
	if n := e.fd.containerCount(containerName); n != 1 {
		t.Fatalf("containers after restart cycle: %d", n)
	}
}

func TestDownloadsArePinnedAndTelemetryIsOff(t *testing.T) {
	e := newAgentEnv(t)
	sc := api.ServerConfig{VersionID: "paper-26.1.2", MinecraftVersion: "26.1.2", PaperBuild: 74, MemoryMB: 1536, MaxPlayers: 10, LevelName: "world"}
	setup, _ := e.a.containerSpec(sc, true)
	for k, want := range map[string]string{"TYPE": "PAPER", "VERSION": "26.1.2", "PAPER_BUILD": "74", "SETUP_ONLY": "TRUE", "SKIP_DOWNLOAD_DEFAULTS": "TRUE"} {
		if got := env(setup, k); got != want {
			t.Errorf("setup container %s=%q, want %q", k, got, want)
		}
	}
	e.create()
	path := filepath.Join(e.cfg.ServerDataDir(), "plugins", "bStats", "config.yml")
	if b, err := os.ReadFile(path); err != nil || !bStatsOff(b) {
		t.Fatalf("bStats must be off before the first start: %q %v", b, err)
	}
	// A restored archive can carry bStats switched on; the next start turns it off.
	if err := os.WriteFile(path, []byte("enabled: true\nserverUuid: 00000000-0000-0000-0000-000000000000\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"stop", "start"} {
		code, out := e.call("POST", "/v1/server/"+verb, map[string]any{"actor": "admin"})
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
	paths := []string{"/v1/server/stop", "/v1/server/restart", "/v1/backups"}
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
	case "/v1/server/stop":
		if st.Phase != api.PhaseStopped || st.Desired != api.DesiredStopped {
			t.Fatalf("after stop won: %s/%s", st.Phase, st.Desired)
		}
	default:
		e.waitFor("online after "+winner.path, func() bool { return e.status().Phase == api.PhaseOnline })
	}
	if n := e.fd.containerCount(containerName); n != 1 {
		t.Fatalf("containers: %d", n)
	}
}

func TestConcurrentStartAndStopLeaveDesiredMatchingContainer(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	for i := 0; i < 16; i++ {
		paths := []string{"/v1/server/start", "/v1/server/stop"}
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
		_, running, err := e.a.containerRunning(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if desired := e.a.desired(); (desired == api.DesiredRunning) != running {
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
	e.jarSum = strings.Repeat("0", 64)
	e.stop()
	e.start()
	code, out := e.call("POST", "/v1/server", map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin"})
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "checksum") {
		t.Fatalf("mismatched jar must fail: %+v", op)
	}
	if e.fd.containerCount(containerName+"-setup") != 0 {
		t.Fatal("setup container must be removed")
	}
	e.fd.mu.Lock()
	_, created := e.fd.byName[containerName]
	e.fd.mu.Unlock()
	if created {
		t.Fatal("the server container must not be created with an unverified jar")
	}
	if _, err := os.Stat(filepath.Join(e.cfg.ServerDataDir(), "paper-26.1.2-74.jar")); err == nil {
		t.Fatal("the unverified jar must be deleted")
	}
}

func TestPortCollisionHasActionableError(t *testing.T) {
	e := newAgentEnv(t)
	e.fd.startErr = "driver failed programming external connectivity: Bind for 0.0.0.0:25565 failed: port is already allocated"
	code, out := e.call("POST", "/v1/server", map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin"})
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "25565") || !strings.Contains(op.Hint, "ss -ltnp") {
		t.Fatalf("port collision: %+v", op)
	}
	if n := e.fd.containerCount(containerName); n != 0 {
		t.Fatalf("a container whose start failed must be discarded, found %d", n)
	}
	// The hint says to press Start after fixing the cause; nothing retries meanwhile.
	time.Sleep(300 * time.Millisecond)
	if d := e.a.desired(); d != api.DesiredStopped {
		t.Fatalf("after a failed start the desired state must be stopped, got %s", d)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM operations WHERE kind IN ('recover', 'auto-restart')`); n != 0 {
		t.Fatalf("a failed start was retried in the background %d times", n)
	}
	e.fd.mu.Lock()
	e.fd.startErr = ""
	e.fd.mu.Unlock()
	code, out = e.call("POST", "/v1/server/start", map[string]any{"actor": "admin"})
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
	if err := e.a.docker.ContainerRemove(context.Background(), containerName, true); err != nil {
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
	code, out := e.call("POST", "/v1/server/start", map[string]any{"actor": "admin"})
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
	e.a.db.Exec(`DELETE FROM samples`)
	e.a.kvSet(kvCollectingSince, base.Format(time.RFC3339Nano))
	ins := func(t0 time.Time, state string, players any) {
		e.a.db.Exec(`INSERT INTO samples(ts, state, players_online) VALUES(?,?,?)`, t0.UnixMilli(), state, players)
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
	m, err := e.a.Metrics("24h", base.Add(100*time.Minute))
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
	e.a.kvSet(kvCollectingSince, now.Add(-48*time.Hour).Format(time.RFC3339Nano))
	e.a.db.Exec(`INSERT INTO sessions(player, start_ts, end_ts, end_reason, source) VALUES('A', ?, ?, 'left', 'server_log')`, now.Add(-2*time.Hour).UnixMilli(), now.Add(-time.Hour).UnixMilli())
	e.a.db.Exec(`INSERT INTO sessions(player, start_ts, end_ts, end_reason, end_uncertain, source) VALUES('B', ?, ?, 'server_crashed', 1, 'server_log')`, now.Add(-3*time.Hour).UnixMilli(), now.Add(-150*time.Minute).UnixMilli())
	s, err := e.a.Summary(2, "UTC", now)
	if err != nil {
		t.Fatal(err)
	}
	today := s.Days[len(s.Days)-1]
	if today.UniquePlayers != 2 || today.PlaytimeSeconds != 3600+1800 || !today.PlaytimeLowerBound {
		t.Fatalf("today: %+v", today)
	}
	if s.UncertainSessions != 1 || today.Coverage >= 1 {
		t.Fatalf("summary must flag uncertainty and incomplete coverage: %+v", s)
	}
	if _, err := e.a.Summary(7, "Not/AZone", now); err == nil {
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
	code, out := e.call("POST", "/v1/backups", map[string]any{"actor": "admin"})
	if code != 202 {
		e.t.Fatalf("backup: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		e.t.Fatalf("backup op: %+v", op)
	}
	e.waitFor("online after backup", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
	list, _ := e.a.listBackups(`WHERE kind = 'manual'`)
	code, preview := e.call("POST", "/v1/backups/"+list[0].ID+"/restore", map[string]any{"actor": "admin"})
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
	world := filepath.Join(e.cfg.ServerDataDir(), "world")
	if err := os.WriteFile(filepath.Join(world, "later.dat"), []byte("built after the backup"), 0o640); err != nil {
		t.Fatal(err)
	}
	// The rollback archive's recorded checksum stops matching its file, as if
	// the disk had changed it, so the archive is written but fails its check.
	if _, err := e.a.db.Exec(`CREATE TRIGGER damaged_rollback AFTER INSERT ON backups WHEN NEW.kind = 'rollback'
		BEGIN UPDATE backups SET sha256 = '` + strings.Repeat("0", 64) + `' WHERE id = NEW.id; END`); err != nil {
		t.Fatal(err)
	}
	live := worldHash(t, e.cfg.ServerDataDir())
	op := e.applyRestore(id, phrase)
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "verified rollback archive") {
		t.Fatalf("a restore without a verified rollback archive must refuse: %+v", op)
	}
	if got := worldHash(t, e.cfg.ServerDataDir()); got != live {
		t.Fatal("the live world was replaced although its rollback archive failed verification")
	}
	e.waitFor("previous world running again", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
}

func TestRestoreUndoesTheSwapWhenSettingsCannotBeSaved(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	id, phrase := e.backupAndStage()
	world := filepath.Join(e.cfg.ServerDataDir(), "world")
	if err := os.WriteFile(filepath.Join(world, "later.dat"), []byte("built after the backup"), 0o640); err != nil {
		t.Fatal(err)
	}
	live := worldHash(t, e.cfg.ServerDataDir())
	for _, ev := range []string{"INSERT", "UPDATE"} {
		if _, err := e.a.db.Exec(`CREATE TRIGGER fail_config_` + ev + ` BEFORE ` + ev + ` ON kv WHEN NEW.key = 'server_config' BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
			t.Fatal(err)
		}
	}
	op := e.applyRestore(id, phrase)
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "previous world was put back") {
		t.Fatalf("a restore whose settings cannot be saved must fail and say so: %+v", op)
	}
	if got := worldHash(t, e.cfg.ServerDataDir()); got != live {
		t.Fatal("the restored world was left in place without its settings")
	}
	if left, _ := filepath.Glob(e.cfg.ServerDataDir() + ".replaced-*"); len(left) != 0 {
		t.Fatalf("the moved-aside world was left behind: %v", left)
	}
	e.waitFor("previous world running again", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
}

// A world a restore would refuse is not backed up at all: the backup fails
// before anything is written, says why, and the server comes back.
func TestBackupRefusesAWorldARestoreWouldRefuse(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	world := filepath.Join(e.cfg.ServerDataDir(), "world")
	deep := filepath.Join(world, strings.Repeat("a", 250), strings.Repeat("b", 250), strings.Repeat("c", 250), strings.Repeat("d", 250))
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "r.mca"), []byte("region"), 0o640); err != nil {
		t.Fatal(err)
	}
	code, out := e.call("POST", "/v1/backups", map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("backup: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "a restore would refuse it") || !strings.Contains(op.Error, "entry name too long") {
		t.Fatalf("backing up a world a restore would refuse must fail and say why: %+v", op)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM backups`); n != 0 {
		t.Fatalf("%d backup rows recorded for a refused backup", n)
	}
	if files, _ := os.ReadDir(e.cfg.BackupsDir()); len(files) != 0 {
		t.Fatalf("a refused backup left files: %v", files)
	}
	e.waitFor("server running again", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
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
	req, _ := http.NewRequest("POST", e.ts.URL+"/v1/restore/upload", bytes.NewReader(archive))
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
	world := filepath.Join(e.cfg.ServerDataDir(), "world")
	os.WriteFile(filepath.Join(e.cfg.ServerDataDir(), "server.properties"), []byte("level-name=world\nrcon.password=topsecret\n"), 0o644)
	os.WriteFile(filepath.Join(world, "marker.txt"), []byte("nonce-original"), 0o644)
	code, out := e.call("POST", "/v1/backups", map[string]any{"actor": "admin", "note": "first"})
	if code != 202 {
		t.Fatalf("backup: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded {
		t.Fatalf("backup op: %+v", op)
	}
	list, _ := e.a.listBackups("")
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
	live := worldHash(t, e.cfg.ServerDataDir())

	flipped := append([]byte(nil), archive...)
	flipped[len(flipped)/2] ^= 0xff
	evil := craftTar(t, map[string]string{"playkeeper-backup/data/../../../etc/cron.d/x": "* * * * * root id"})
	for name, bad := range map[string][]byte{"flipped byte": flipped, "truncated": archive[:len(archive)/2], "traversal": evil, "not gzip": []byte("hello")} {
		code, out := e.upload(bad)
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("%s archive: %d %v", name, code, out)
		}
		if got := worldHash(t, e.cfg.ServerDataDir()); got != live {
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
	if got := worldHash(t, e.cfg.ServerDataDir()); got != live {
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
	list, _ = e.a.listBackups(`WHERE kind = 'rollback'`)
	if len(list) != 1 || list[0].Verified == nil || !*list[0].Verified {
		t.Fatalf("rollback archive: %+v", list)
	}
	// Restoring the rollback archive brings the replaced state back.
	e.waitFor("idle", func() bool { return !e.a.busy() })
	code, preview = e.call("POST", "/v1/backups/"+list[0].ID+"/restore", map[string]any{"actor": "admin"})
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
	code, _ := e.call("POST", "/v1/server/command", map[string]any{"actor": "admin", "command": "ban-ip 203.0.113.77"})
	if code != 200 {
		t.Fatalf("command: %d", code)
	}
	e.waitFor("join stored", func() bool { return e.countRows(`SELECT COUNT(*) FROM events WHERE kind='join'`) == 1 })
	pw, _ := e.a.rconPassword()
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
	for _, l := range e.a.console.since("", 0, consoleCapacity).Lines {
		if strings.Contains(l.Text, "203.0.113") {
			t.Fatalf("console shows an IP: %q", l.Text)
		}
	}
}

func TestWhitelistAndConsoleAreAudited(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	if code, out := e.call("POST", "/v1/server/whitelist", map[string]any{"actor": "admin", "name": "PkBotFriend"}); code != 200 {
		t.Fatalf("invite: %d %v", code, out)
	}
	if code, _ := e.call("POST", "/v1/server/whitelist", map[string]any{"actor": "admin", "name": "bad name;id"}); code != 400 {
		t.Fatalf("bad name accepted: %d", code)
	}
	code, out := e.call("POST", "/v1/server/command", map[string]any{"actor": "admin", "command": "$(id)"})
	if code != 200 || !strings.Contains(out["output"].(string), "Unknown or incomplete command") {
		t.Fatalf("$(id) must reach Minecraft as text: %d %v", code, out)
	}
	if code, _ := e.call("POST", "/v1/server/settings", map[string]any{"actor": "admin", "maxPlayers": 20}); code != 200 {
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
