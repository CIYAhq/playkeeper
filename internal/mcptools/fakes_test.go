package mcptools

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
	"github.com/CIYAhq/playkeeper/internal/mcp"
)

// wait bounds every blocking step, so that a bug fails the test instead of
// hanging it.
const wait = 5 * time.Second

const (
	idA   = "aaaaaaaaaa"
	idB   = "bbbbbbbbbb"
	opA   = "0123456789abcdef"
	opB   = "fedcba9876543210"
	actor = "token:t1"
	// planFP is the fingerprint of chunky's plan.
	planFP = "5d41402abc4b2a76b9719d911017c592"
)

var t0 = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

// request is one request an agent received.
type request struct {
	Method, Path string
	Query        url.Values
	Body         map[string]any
	Actor        string
}

// reply is a canned answer. A string body is sent as it is; err fails the
// request on its way, as a machine link does.
type reply struct {
	status int
	body   any
	err    error
}

// fakeAgent answers agent routes with canned replies and records the
// requests. It sits behind a real agentclient.Client, so answers are
// decoded as they are in production.
type fakeAgent struct {
	mu      sync.Mutex
	replies map[string]reply
	reqs    []request
}

func newFakeAgent() *fakeAgent { return &fakeAgent{replies: map[string]reply{}} }

func (f *fakeAgent) on(method, path string, status int, body any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[method+" "+path] = reply{status: status, body: body}
}

func (f *fakeAgent) fail(method, path string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[method+" "+path] = reply{err: err}
}

func (f *fakeAgent) requests() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func (f *fakeAgent) RoundTrip(r *http.Request) (*http.Response, error) {
	var body map[string]any
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(b) > 0 {
			if err := json.Unmarshal(b, &body); err != nil {
				return nil, err
			}
		}
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, request{r.Method, r.URL.Path, r.URL.Query(), body, machinelink.ActorFrom(r.Context())})
	rep, ok := f.replies[r.Method+" "+r.URL.Path]
	f.mu.Unlock()
	if !ok {
		rep = reply{status: http.StatusNotFound, body: api.Error{Error: "No such route.", Code: api.CodeNotFound}}
	}
	if rep.err != nil {
		return nil, rep.err
	}
	b, ok := rep.body.(string)
	if !ok {
		raw, err := json.Marshal(rep.body)
		if err != nil {
			return nil, err
		}
		b = string(raw)
	}
	return &http.Response{StatusCode: rep.status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(b)), ContentLength: int64(len(b)), Request: r}, nil
}

// fakeBackend is a dashboard with machines m1 and m2.
type fakeBackend struct {
	mu        sync.Mutex
	access    Access
	accessErr error
	servers   []Server
	agents    map[string]*fakeAgent
	done      []string
	refused   []string
}

func (b *fakeBackend) setAccess(a Access, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.access, b.accessErr = a, err
}

func (b *fakeBackend) Access(context.Context, mcp.Principal) (Access, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.access, b.accessErr
}

func (b *fakeBackend) Servers(context.Context) ([]Server, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.servers), nil
}

func (b *fakeBackend) Agent(_ context.Context, id string) (Agent, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.servers {
		if s.ID == id {
			return agentclient.Via(b.agents[s.MachineID]), nil
		}
	}
	return nil, errors.New("fakeBackend: no such server")
}

func (b *fakeBackend) Done(_ context.Context, _ mcp.Principal, tool string, s *Server) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s == nil {
		b.done = append(b.done, tool)
		return
	}
	b.done = append(b.done, tool+" "+s.ID)
}

func (b *fakeBackend) Refused(_ context.Context, _ mcp.Principal, tool, kind string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refused = append(b.refused, tool+" "+kind)
}

func (b *fakeBackend) refusals() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.refused)
}

func (b *fakeBackend) calls() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.done)
}

// sample is an online Paper server with two players on it.
func sample(id, slug, name string) api.ServerStatus {
	return api.ServerStatus{
		ID: id, Slug: slug, Name: name, Phase: api.PhaseOnline, Desired: "running", Reachable: true, GamePort: 25565,
		Players:    &api.PlayerSnapshot{Online: 2, Max: 20, Names: []string{"Alex", "Steve"}},
		Config:     &api.ServerConfig{Type: api.TypePaper, MinecraftVersion: "1.21.8", MemoryMB: 3072, MaxPlayers: 20, Whitelist: true},
		Resources:  &api.Resources{TPS: new(19.9), CPUPercent: new(35.0), MemBytes: new(int64(2 << 30)), MemLimitBytes: new(int64(3 << 30))},
		LastBackup: &api.Backup{ID: "bk1", CreatedAt: t0.Add(-time.Hour)},
	}
}

// serve makes an agent answer every route the tools use for a server, with
// op as the id of the operations it starts.
func serve(a *fakeAgent, st api.ServerStatus, op string) {
	p := "/v1/servers/" + st.ID
	started := func(kind string) api.Operation {
		return api.Operation{ID: op, ServerID: st.ID, Kind: kind, Status: api.OpRunning, StartedAt: t0}
	}
	a.on("GET", p, http.StatusOK, st)
	a.on("POST", p+"/start", http.StatusAccepted, started("start"))
	a.on("POST", p+"/stop", http.StatusAccepted, started("stop"))
	a.on("POST", p+"/restart", http.StatusAccepted, started("restart"))
	a.on("GET", p+"/logs", http.StatusOK, api.LogsResponse{Lines: []api.LogLine{{Seq: 1, TS: t0, Text: `Done (3.2s)! For help, type "help"`}}})
	a.on("POST", p+"/command", http.StatusOK, api.CommandResponse{Output: "Set the time to 1000"})
	a.on("GET", p+"/whitelist", http.StatusOK, []api.WhitelistEntry{{Name: "Alex"}, {Name: "Steve"}})
	a.on("POST", p+"/whitelist", http.StatusOK, map[string]any{"message": "Added Steve_1 to the whitelist."})
	a.on("DELETE", p+"/whitelist/Steve_1", http.StatusOK, map[string]any{"message": "Removed Steve_1 from the whitelist."})
	a.on("GET", p+"/backups", http.StatusOK, []api.Backup{{ID: "bk1", ServerID: st.ID, Kind: "manual", CreatedAt: t0.Add(-time.Hour), SizeBytes: 50 << 20}})
	a.on("POST", p+"/backups", http.StatusAccepted, started("backup"))
	a.on("GET", "/v1/operations/"+op, http.StatusOK, started("restart"))
	a.on("GET", p+"/metrics", http.StatusOK, api.MetricsResponse{Buckets: []api.MetricsBucket{
		{Start: t0.Add(-30 * time.Minute), State: "online", CPUAvg: new(80.0), PlayersMax: new(3)},
	}})
	a.on("GET", p+"/events", http.StatusOK, []api.Event{{ID: 1, TS: t0.Add(-2 * time.Hour), Kind: "server_crashed", Detail: "The server stopped unexpectedly."}})
	a.on("GET", p+"/addons/project/modrinth/chunky", http.StatusOK, chunky())
	a.on("POST", p+"/addons/install", http.StatusAccepted, started("addon-install"))
}

// chunky is Chunky's detail sheet on a server without it, looked up by its
// slug: its plan installs Chunky alone.
func chunky() api.AddonDetails {
	return api.AddonDetails{
		Card:   api.AddonCard{Source: "modrinth", ProjectID: "fALzjamp", Slug: "chunky", Name: "Chunky", Categories: []string{"utility"}},
		Latest: &api.AddonVersion{VersionID: "dPliWter", VersionNumber: "1.4.40", Channel: "release"},
		Plan: &api.AddonPlan{
			Steps:  []api.AddonStep{{Action: "install", Source: "modrinth", ProjectID: "fALzjamp", Name: "Chunky", VersionNumber: "1.4.40", Channel: "release"}},
			Manual: []api.AddonNotice{}, Blockers: []api.AddonNotice{}, Warnings: []api.AddonNotice{}, Ready: true, Fingerprint: planFP,
		},
	}
}

// newWorld is a dashboard with server A (Survival) on machine m1, my-vps,
// and server B (Cobblemon) on m2, home-server, or with both on m1. The
// caller may do everything everywhere until the test says otherwise.
func newWorld(oneMachine bool) *fakeBackend {
	b := &fakeBackend{
		access: Access{Scope: mcp.ScopeOwner, AllServers: true},
		agents: map[string]*fakeAgent{"m1": newFakeAgent(), "m2": newFakeAgent()},
	}
	b.add("m1", "my-vps", sample(idA, "survival", "Survival"), opA)
	if oneMachine {
		b.add("m1", "my-vps", sample(idB, "cobblemon", "Cobblemon"), opB)
	} else {
		b.add("m2", "home-server", sample(idB, "cobblemon", "Cobblemon"), opB)
	}
	return b
}

func (b *fakeBackend) add(machine, name string, st api.ServerStatus, op string) {
	b.servers = append(b.servers, Server{ServerStatus: st, MachineID: machine, MachineName: name})
	serve(b.agents[machine], st, op)
}

func (b *fakeBackend) allRequests() []request {
	return append(b.agents["m1"].requests(), b.agents["m2"].requests()...)
}

// sampleArgs are valid arguments for a tool, naming server.
func sampleArgs(t *testing.T, tool mcp.Tool, server string) map[string]any {
	t.Helper()
	args := map[string]any{}
	for _, name := range tool.InputSchema.Required {
		switch name {
		case "server":
			args[name] = server
		case "message":
			args[name] = "Back in five minutes"
		case "command":
			args[name] = "time set day"
		case "player":
			args[name] = "Steve_1"
		case "operation":
			args[name] = opA
		case "source":
			args[name] = "modrinth"
		case "project":
			args[name] = "chunky"
		default:
			t.Fatalf("%s: no sample value for its required argument %q; add one to sampleArgs", tool.Name, name)
		}
	}
	return args
}

func token(scope mcp.Scope) mcp.Principal {
	return mcp.Principal{ID: actor, Name: "Claude on my laptop", Scopes: []mcp.Scope{scope}}
}

// client talks to an MCP server over stdio, as an MCP client would.
type client struct {
	t     *testing.T
	in    io.Writer
	lines chan []byte
	id    int
}

// connect serves b's tools to p over stdio and initializes a client.
func connect(t *testing.T, b Backend, p mcp.Principal) *client {
	t.Helper()
	srv, err := mcp.New(mcp.Options{Tools: Tools(b), Instructions: Instructions, Logger: slog.New(slog.DiscardHandler),
		ReadOnlyCallsPerMinute: 10000, MutatingCallsPerMinute: 10000})
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		srv.ServeStdio(ctx, p, inR, outW)
		outW.Close()
		close(stopped)
	}()
	c := &client{t: t, in: inW, lines: make(chan []byte, 16)}
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(nil, 4<<20)
		for sc.Scan() {
			c.lines <- bytes.Clone(sc.Bytes())
		}
		close(c.lines)
	}()
	t.Cleanup(func() {
		inW.Close()
		cancel()
		select {
		case <-stopped:
		case <-time.After(wait):
			t.Error("the MCP server did not stop")
		}
	})
	c.rpc("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "test-client", "version": "0.1"}})
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return c
}

func (c *client) send(msg map[string]any) {
	c.t.Helper()
	b, err := json.Marshal(msg)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		c.t.Fatalf("writing to the MCP server: %v", err)
	}
}

func (c *client) rpc(method string, params map[string]any) map[string]any {
	c.t.Helper()
	c.id++
	c.send(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	var line []byte
	select {
	case l, ok := <-c.lines:
		if !ok {
			c.t.Fatal("the MCP server closed its output")
		}
		line = l
	case <-time.After(wait):
		c.t.Fatalf("%s: no answer from the MCP server", method)
	}
	var resp struct {
		ID     int            `json:"id"`
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		c.t.Fatalf("%s: %v in %s", method, err, line)
	}
	if resp.Error != nil {
		c.t.Fatalf("%s: JSON-RPC error %d: %s", method, resp.Error.Code, resp.Error.Message)
	}
	if resp.ID != c.id {
		c.t.Fatalf("%s: answer to request %d, want %d", method, resp.ID, c.id)
	}
	return resp.Result
}

// result is a tool call's result. Kind is set when the call failed.
type result struct {
	Text       string
	Structured map[string]any
	Kind       string
	Hint       string
}

func (c *client) call(tool string, args map[string]any) result {
	c.t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	raw, err := json.Marshal(c.rpc("tools/call", map[string]any{"name": tool, "arguments": args}))
	if err != nil {
		c.t.Fatal(err)
	}
	var r struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Structured map[string]any `json:"structuredContent"`
		IsError    bool           `json:"isError"`
		Meta       map[string]struct {
			Kind string `json:"kind"`
			Hint string `json:"hint"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		c.t.Fatal(err)
	}
	out := result{Structured: r.Structured}
	if len(r.Content) > 0 {
		out.Text = r.Content[0].Text
	}
	if r.IsError {
		detail := r.Meta["io.playkeeper/error"]
		out.Kind, out.Hint = cmp.Or(detail.Kind, "(no kind)"), detail.Hint
	}
	return out
}

// ok checks that a tool call succeeded.
func ok(t *testing.T, tool string, r result) {
	t.Helper()
	if r.Kind != "" {
		t.Fatalf("%s failed (%s): %s", tool, r.Kind, r.Text)
	}
}
