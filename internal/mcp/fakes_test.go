package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// wait bounds every blocking step, so that a bug fails the test instead of
// hanging it.
const wait = 5 * time.Second

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// logBuffer collects log output, so tests can check what is and is not
// logged.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func principal(s Scope) Principal {
	return Principal{ID: "token:" + string(s), Name: string(s) + " token", Scopes: []Scope{s}}
}

// gate lets a test hold tool calls in progress and see how they ended.
type gate struct {
	started chan struct{}
	release chan struct{}
	ended   chan error
	once    sync.Once
}

func newGate() *gate {
	return &gate{started: make(chan struct{}, 64), release: make(chan struct{}), ended: make(chan error, 64)}
}

func (g *gate) handler(ctx context.Context, c *Call) (*Result, error) {
	g.started <- struct{}{}
	select {
	case <-g.release:
		g.ended <- nil
		return &Result{Text: "released"}, nil
	case <-ctx.Done():
		g.ended <- ctx.Err()
		return nil, ctx.Err()
	}
}

func (g *gate) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-g.started:
	case <-time.After(wait):
		t.Fatal("the tool call did not start")
	}
}

// waitEnded returns nil for a call that was released and the context error
// for one that was cancelled.
func (g *gate) waitEnded(t *testing.T) error {
	t.Helper()
	select {
	case err := <-g.ended:
		return err
	case <-time.After(wait):
		t.Fatal("the tool call did not end")
		return nil
	}
}

// releaseOne lets one waiting call finish.
func (g *gate) releaseOne(t *testing.T) {
	t.Helper()
	select {
	case g.release <- struct{}{}:
	case <-time.After(wait):
		t.Fatal("no tool call was waiting to be released")
	}
}

// releaseAll lets every call finish, including later ones.
func (g *gate) releaseAll() { g.once.Do(func() { close(g.release) }) }

type fixture struct {
	srv   *Server
	clock *clock
	logs  *logBuffer
	gate  *gate

	mu      sync.Mutex
	records []CallRecord
}

func (f *fixture) calls() []CallRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.records)
}

func (f *fixture) outcomes() []Outcome {
	var out []Outcome
	for _, r := range f.calls() {
		out = append(out, r.Outcome)
	}
	return out
}

// newFixture builds a server with the fake tools. edit, when set, can change
// the options first.
func newFixture(t *testing.T, edit func(*Options)) *fixture {
	t.Helper()
	f := &fixture{clock: newClock(), logs: &logBuffer{}, gate: newGate()}
	t.Cleanup(f.gate.releaseAll)
	opts := Options{
		Version:      "1.2.3",
		Instructions: "Use these tools to look after Minecraft servers.",
		Tools:        fakeTools(f),
		Logger:       slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:          f.clock.now,
		OnCall: func(r CallRecord) {
			f.mu.Lock()
			f.records = append(f.records, r)
			f.mu.Unlock()
		},
	}
	if edit != nil {
		edit(&opts)
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	f.srv = srv
	return f
}

// allTools are the fake tools in registration order: the first eight need
// the read scope, the next two manage and the last one owner.
var allTools = []string{
	"echo", "server_status", "numbers", "offline", "leaky", "explode", "wrong_output", "slow",
	"restart_server", "create_backup",
	"run_console_command",
}

type statusOutput struct {
	Online  bool `json:"online"`
	Players int  `json:"players"`
}

func fakeTools(f *fixture) []Tool {
	serverArg := &Schema{Type: "string", Description: "Server id or slug.", Pattern: `^[a-z0-9-]{1,32}$`}
	statusSchema := &Schema{Type: "object", Properties: map[string]*Schema{
		"online":  {Type: "boolean"},
		"players": {Type: "integer", Minimum: new(0.0)},
	}, Required: []string{"online", "players"}}
	return []Tool{
		{
			Name: "echo", Title: "Echo", Description: "Repeats the text.",
			InputSchema: Schema{Type: "object", Properties: map[string]*Schema{
				"text":  {Type: "string", MinLength: new(1), MaxLength: new(20)},
				"times": {Type: "integer", Minimum: new(1.0), Maximum: new(3.0), Default: 1},
			}, Required: []string{"text"}},
			Effect: ReadOnly, Scope: ScopeRead,
			Handler: func(ctx context.Context, c *Call) (*Result, error) {
				var a struct {
					Text  string `json:"text"`
					Times int    `json:"times"`
				}
				if err := c.Bind(&a); err != nil {
					return nil, err
				}
				return &Result{Text: strings.Repeat(a.Text, max(a.Times, 1))}, nil
			},
		},
		{
			Name: "server_status", Title: "Server status", Description: "Reports whether a server is online.",
			InputSchema:  Schema{Type: "object", Properties: map[string]*Schema{"server": serverArg}, Required: []string{"server"}},
			OutputSchema: statusSchema,
			Effect:       ReadOnly, Scope: ScopeRead,
			Handler: func(ctx context.Context, c *Call) (*Result, error) {
				return &Result{Text: "The server is online with 3 players.", Structured: statusOutput{Online: true, Players: 3}}, nil
			},
		},
		{
			Name: "numbers", Description: "Lists some numbers.",
			InputSchema: Schema{Type: "object"},
			Effect:      ReadOnly, Scope: ScopeRead,
			Handler: func(ctx context.Context, c *Call) (*Result, error) {
				return &Result{Structured: []int{1, 2, 3}}, nil
			},
		},
		{
			Name: "offline", Description: "Always finds the server offline.",
			InputSchema: Schema{Type: "object"},
			Effect:      ReadOnly, Scope: ScopeRead,
			Handler: func(ctx context.Context, c *Call) (*Result, error) {
				return nil, fmt.Errorf("checking: %w", &ToolError{
					Kind: "server_offline", Msg: "The server is offline.", Hint: "Start it first.",
					Params: map[string]string{"server": "survival"},
				})
			},
		},
		{
			Name: "leaky", Description: "Fails with an internal error.",
			InputSchema: Schema{Type: "object"},
			Effect:      ReadOnly, Scope: ScopeRead,
			Handler: func(ctx context.Context, c *Call) (*Result, error) {
				return nil, errors.New("dial unix /run/playkeeper.sock: secret-detail")
			},
		},
		{
			Name: "explode", Description: "Panics.",
			InputSchema: Schema{Type: "object"},
			Effect:      ReadOnly, Scope: ScopeRead,
			Handler: func(ctx context.Context, c *Call) (*Result, error) {
				panic("boom")
			},
		},
		{
			Name: "wrong_output", Description: "Breaks its output schema.",
			InputSchema: Schema{Type: "object"}, OutputSchema: statusSchema,
			Effect: ReadOnly, Scope: ScopeRead,
			Handler: func(ctx context.Context, c *Call) (*Result, error) {
				return &Result{Structured: map[string]any{"online": "yes", "players": 1}}, nil
			},
		},
		{
			Name: "slow", Description: "Waits until the test releases it.",
			InputSchema: Schema{Type: "object"},
			Effect:      ReadOnly, Scope: ScopeRead,
			Handler: f.gate.handler,
		},
		{
			Name: "restart_server", Title: "Restart server", Description: "Restarts a server.",
			InputSchema: Schema{Type: "object", Properties: map[string]*Schema{"server": serverArg}, Required: []string{"server"}},
			Effect:      Destructive, Scope: ScopeManage,
			Handler: func(ctx context.Context, c *Call) (*Result, error) {
				return &Result{Text: "Restarted."}, nil
			},
		},
		{
			Name: "create_backup", Description: "Makes a backup.",
			InputSchema: Schema{Type: "object"},
			Effect:      Additive, Scope: ScopeManage,
			Handler: func(ctx context.Context, c *Call) (*Result, error) {
				return &Result{Text: "Backup started."}, nil
			},
		},
		{
			Name: "run_console_command", Description: "Runs a console command.",
			InputSchema: Schema{Type: "object", Properties: map[string]*Schema{"command": {Type: "string", MaxLength: new(256)}}, Required: []string{"command"}},
			Effect:      Destructive, Scope: ScopeOwner,
			Handler: func(ctx context.Context, c *Call) (*Result, error) {
				return &Result{Text: "Done."}, nil
			},
		},
	}
}

// wireResp is one JSON-RPC response as a client sees it.
type wireResp struct {
	fields map[string]json.RawMessage
	status int // HTTP status; 0 on stdio
	result map[string]any
	err    *wireError
}

type wireError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}

func parseResp(t *testing.T, raw []byte) wireResp {
	t.Helper()
	r := wireResp{}
	if err := json.Unmarshal(raw, &r.fields); err != nil {
		t.Fatalf("the response is not a JSON object: %v\n%s", err, raw)
	}
	if string(r.fields["jsonrpc"]) != `"2.0"` {
		t.Fatalf(`the response lacks "jsonrpc": "2.0": %s`, raw)
	}
	res, hasResult := r.fields["result"]
	e, hasError := r.fields["error"]
	if hasResult == hasError {
		t.Fatalf("a response needs exactly one of result and error: %s", raw)
	}
	if hasResult {
		v, err := decodeJSON(res)
		if err != nil {
			t.Fatal(err)
		}
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("the result is not an object: %s", raw)
		}
		r.result = m
	}
	if hasError {
		if err := json.Unmarshal(e, &r.err); err != nil {
			t.Fatal(err)
		}
		if r.err.Message == "" {
			t.Fatalf("an error needs a message: %s", raw)
		}
	}
	return r
}

// id is the response's id as raw JSON, or "" when it has none.
func (r wireResp) id() string { return string(r.fields["id"]) }

// wantError fails unless r is an error with the given code.
func (r wireResp) wantError(t *testing.T, code int) *wireError {
	t.Helper()
	if r.err == nil {
		t.Fatalf("want error %d, got result %v", code, r.result)
	}
	if r.err.Code != code {
		t.Fatalf("want error %d, got %d: %s", code, r.err.Code, r.err.Message)
	}
	return r.err
}

// wantResult fails unless r is a result, and returns it.
func (r wireResp) wantResult(t *testing.T) map[string]any {
	t.Helper()
	if r.err != nil {
		t.Fatalf("want a result, got error %d: %s (%v)", r.err.Code, r.err.Message, r.err.Data)
	}
	return r.result
}

// dig walks decoded JSON through object keys and array indexes.
func dig(t *testing.T, v any, path ...any) any {
	t.Helper()
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("looking up %q in a non-object: %v", k, v)
			}
			if v, ok = m[k]; !ok {
				t.Fatalf("no %q in %v", k, m)
			}
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				t.Fatalf("no index %d in %v", k, v)
			}
			v = a[k]
		}
	}
	return v
}

func has(v any, key string) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	_, ok = m[key]
	return ok
}

// keys returns an object's keys in order.
func keys(t *testing.T, v any) []string {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("not an object: %v", v)
	}
	return sortedKeys(m)
}

// canon returns decoded JSON as compact JSON with sorted keys.
func canon(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// toolNames returns the names in a tools/list result.
func toolNames(t *testing.T, res map[string]any) []string {
	t.Helper()
	list, ok := res["tools"].([]any)
	if !ok {
		t.Fatalf("tools is not an array: %v", res["tools"])
	}
	names := []string{}
	for _, tool := range list {
		name, _ := dig(t, tool, "name").(string)
		names = append(names, name)
	}
	return names
}

// resultText returns the text of a tool result's first content block.
func resultText(t *testing.T, res map[string]any) string {
	t.Helper()
	if got := dig(t, res, "content", 0, "type"); got != "text" {
		t.Fatalf("the first content block has type %v", got)
	}
	s, _ := dig(t, res, "content", 0, "text").(string)
	return s
}

// client speaks one protocol revision over one transport, as an MCP client
// would: legacy clients initialize first, modern ones add _meta to every
// request.
type client interface {
	base() *clientBase
	// initialize sends initialize asking for version and, when it succeeds,
	// continues in the negotiated revision.
	initialize(t *testing.T, version string) wireResp
	call(t *testing.T, method string, params map[string]any) wireResp
	notify(t *testing.T, method string, params map[string]any)
	// send writes a raw message and returns exactly want responses. Over
	// HTTP, want 0 requires 202 Accepted with no body.
	send(t *testing.T, raw string, want int) []wireResp
	// quiet checks that the server wrote nothing for earlier messages.
	quiet(t *testing.T)
}

type clientBase struct {
	version string
	modern  bool
	nextID  int
}

func (b *clientBase) base() *clientBase { return b }

// message encodes a request, or a notification when id is 0.
func (b *clientBase) message(id int, method string, params map[string]any) []byte {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != 0 {
		msg["id"] = id
	}
	p := maps.Clone(params)
	if b.modern {
		if p == nil {
			p = map[string]any{}
		}
		p["_meta"] = map[string]any{
			metaProtocolVersion:    b.version,
			metaClientCapabilities: map[string]any{},
			metaClientInfo:         map[string]any{"name": "test-client", "version": "0.1"},
		}
	}
	if p != nil {
		msg["params"] = p
	}
	out, _ := json.Marshal(msg)
	return out
}

func (b *clientBase) id() int {
	b.nextID++
	return b.nextID
}

func initParams(version string) map[string]any {
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test-client", "version": "0.1"},
	}
}

// start makes c speak version: a modern client just sends _meta from now
// on, a legacy one initializes.
func start(t *testing.T, c client, version string) client {
	t.Helper()
	if isModern(version) {
		b := c.base()
		b.version, b.modern = version, true
		return c
	}
	r := c.initialize(t, version)
	if got := dig(t, r.wantResult(t), "protocolVersion"); got != version {
		t.Fatalf("initialize negotiated %v, want %s", got, version)
	}
	c.notify(t, "notifications/initialized", nil)
	return c
}

// opener connects a client to f's server as p. The client has not
// initialized.
type opener func(t *testing.T, f *fixture, p Principal) client

var transports = []struct {
	name string
	open opener
}{
	{"http", func(t *testing.T, f *fixture, p Principal) client {
		h, auth := newHTTP(t, f, nil)
		auth.tokens["tok-test"] = p
		return &httpClient{h: h, token: "tok-test"}
	}},
	{"stdio", func(t *testing.T, f *fixture, p Principal) client {
		return &stdioClient{pipe: startStdio(t, f.srv, p)}
	}},
}

// eachTransport runs test over every transport.
func eachTransport(t *testing.T, test func(t *testing.T, open opener)) {
	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) { test(t, tr.open) })
	}
}

// eachEra runs test for every transport and revision, with clients that
// already speak the revision.
func eachEra(t *testing.T, test func(t *testing.T, connect opener, version string)) {
	for _, tr := range transports {
		for _, v := range supportedVersions() {
			t.Run(tr.name+"/"+v, func(t *testing.T) {
				test(t, func(t *testing.T, f *fixture, p Principal) client { return start(t, tr.open(t, f, p), v) }, v)
			})
		}
	}
}

// fakeAuth accepts a fixed set of tokens.
type fakeAuth struct {
	mu     sync.Mutex
	tokens map[string]Principal
	err    error
	seen   int
}

func newFakeAuth() *fakeAuth {
	return &fakeAuth{tokens: map[string]Principal{
		"tok-read":   principal(ScopeRead),
		"tok-manage": principal(ScopeManage),
		"tok-owner":  principal(ScopeOwner),
	}}
}

func (a *fakeAuth) Authenticate(r *http.Request, token string) (Principal, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen++
	if a.err != nil {
		return Principal{}, a.err
	}
	p, ok := a.tokens[token]
	if !ok {
		return Principal{}, ErrInvalidToken
	}
	return p, nil
}

func (a *fakeAuth) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.seen
}

func newHTTP(t *testing.T, f *fixture, edit func(*HTTPOptions)) (*HTTPHandler, *fakeAuth) {
	t.Helper()
	auth := newFakeAuth()
	opts := HTTPOptions{Authenticator: auth}
	if edit != nil {
		edit(&opts)
	}
	h, err := NewHTTPHandler(f.srv, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h, auth
}

// newRequest builds a request to the endpoint with the headers a
// well-behaved client sets, as the owner; hdr adds to them or, with an empty
// value, removes them.
func newRequest(method, body string, hdr map[string]string) *http.Request {
	req := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer tok-owner")
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	return req
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func post(h http.Handler, body string, hdr map[string]string) *httptest.ResponseRecorder {
	return serve(h, newRequest(http.MethodPost, body, hdr))
}

// httpClient is a client over Streamable HTTP.
type httpClient struct {
	clientBase
	h       *HTTPHandler
	token   string
	session string
	last    *httptest.ResponseRecorder
}

// newHTTPClient connects to h with token and starts speaking version.
func newHTTPClient(t *testing.T, h *HTTPHandler, token, version string) *httpClient {
	t.Helper()
	c := &httpClient{h: h, token: token}
	start(t, c, version)
	return c
}

func (c *httpClient) initialize(t *testing.T, version string) wireResp {
	t.Helper()
	r := c.call(t, "initialize", initParams(version))
	if r.err == nil {
		c.version, _ = r.result["protocolVersion"].(string)
		c.session = c.last.Header().Get("Mcp-Session-Id")
		if c.session == "" {
			t.Fatal("initialize returned no Mcp-Session-Id")
		}
	}
	return r
}

// headers are the ones the client sends with a message; name is the tool
// name for tools/call.
func (c *httpClient) headers(method, name string) map[string]string {
	hdr := map[string]string{"Authorization": "Bearer " + c.token}
	switch {
	case c.modern:
		hdr["MCP-Protocol-Version"] = c.version
		if method != "" {
			hdr["Mcp-Method"] = method
		}
		if name != "" {
			hdr["Mcp-Name"] = name
		}
	case c.session != "":
		hdr["Mcp-Session-Id"] = c.session
		if atLeast(c.version, version20250618) {
			hdr["MCP-Protocol-Version"] = c.version
		}
	}
	return hdr
}

func (c *httpClient) do(body []byte, method, name string) *httptest.ResponseRecorder {
	return post(c.h, string(body), c.headers(method, name))
}

func (c *httpClient) call(t *testing.T, method string, params map[string]any) wireResp {
	t.Helper()
	name, _ := params["name"].(string)
	if method != "tools/call" {
		name = ""
	}
	id := c.id()
	c.last = c.do(c.message(id, method, params), method, name)
	if got := c.last.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("%s: HTTP %d with Content-Type %q: %s", method, c.last.Code, got, c.last.Body)
	}
	r := parseResp(t, c.last.Body.Bytes())
	r.status = c.last.Code
	if r.id() != strconv.Itoa(id) {
		t.Fatalf("%s: response id %s, want %d", method, r.id(), id)
	}
	return r
}

func (c *httpClient) notify(t *testing.T, method string, params map[string]any) {
	t.Helper()
	httpResponses(t, c.do(c.message(0, method, params), method, ""), 0)
}

func (c *httpClient) send(t *testing.T, raw string, want int) []wireResp {
	t.Helper()
	var head struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	_ = json.Unmarshal([]byte(raw), &head)
	return httpResponses(t, c.do([]byte(raw), head.Method, head.Params.Name), want)
}

// quiet has nothing to check: every POST gets its own answer.
func (c *httpClient) quiet(t *testing.T) {}

func httpResponses(t *testing.T, rec *httptest.ResponseRecorder, want int) []wireResp {
	t.Helper()
	body := bytes.TrimSpace(rec.Body.Bytes())
	if want == 0 {
		if rec.Code != http.StatusAccepted || len(body) != 0 {
			t.Fatalf("want 202 with no body, got %d: %s", rec.Code, body)
		}
		return nil
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("HTTP %d with Content-Type %q: %s", rec.Code, got, body)
	}
	var out []wireResp
	if len(body) > 0 && body[0] == '[' {
		var raws []json.RawMessage
		if err := json.Unmarshal(body, &raws); err != nil {
			t.Fatal(err)
		}
		for _, raw := range raws {
			out = append(out, parseResp(t, raw))
		}
	} else {
		out = append(out, parseResp(t, body))
	}
	if len(out) != want {
		t.Fatalf("want %d responses, got %d (HTTP %d): %s", want, len(out), rec.Code, body)
	}
	for i := range out {
		out[i].status = rec.Code
	}
	return out
}

var errStuck = errors.New("ServeStdio did not return")

// stdioPipe runs ServeStdio on pipes. Every line the server writes must be
// read by the test; the cleanup reports any that are left.
type stdioPipe struct {
	in       *io.PipeWriter
	lines    chan []byte
	cancel   context.CancelFunc
	finished chan struct{}
	err      error // set before finished is closed
}

func startStdio(t *testing.T, srv *Server, p Principal) *stdioPipe {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	sp := &stdioPipe{in: inW, lines: make(chan []byte, 64), cancel: cancel, finished: make(chan struct{})}
	go func() {
		sp.err = srv.ServeStdio(ctx, p, inR, outW)
		inR.Close()
		outW.Close()
		close(sp.finished)
	}()
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(nil, 4<<20)
		for sc.Scan() {
			sp.lines <- bytes.Clone(sc.Bytes())
		}
		close(sp.lines)
	}()
	t.Cleanup(func() {
		cancel()
		inW.Close()
		if sp.wait(t) == errStuck {
			t.Error(errStuck)
			return
		}
		for line := range sp.lines {
			t.Errorf("unexpected output from the server: %s", line)
		}
	})
	return sp
}

// wait returns ServeStdio's result, or errStuck.
func (sp *stdioPipe) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-sp.finished:
		return sp.err
	case <-time.After(wait):
		return errStuck
	}
}

// stillRunning checks that ServeStdio has not returned within a short time.
func (sp *stdioPipe) stillRunning(t *testing.T) {
	t.Helper()
	select {
	case <-sp.finished:
		t.Fatalf("ServeStdio returned early: %v", sp.err)
	case <-time.After(20 * time.Millisecond):
	}
}

func (sp *stdioPipe) write(t *testing.T, s string) {
	t.Helper()
	if _, err := io.WriteString(sp.in, s); err != nil {
		t.Fatalf("writing to the server: %v", err)
	}
}

func (sp *stdioPipe) next(t *testing.T) []byte {
	t.Helper()
	select {
	case line, ok := <-sp.lines:
		if !ok {
			t.Fatal("the server closed its output")
		}
		return line
	case <-time.After(wait):
		t.Fatal("no response from the server")
		return nil
	}
}

// stdioClient is a client over stdio.
type stdioClient struct {
	clientBase
	pipe *stdioPipe
}

// newStdioClient starts serving p over stdio and starts speaking version.
func newStdioClient(t *testing.T, srv *Server, p Principal, version string) *stdioClient {
	t.Helper()
	c := &stdioClient{pipe: startStdio(t, srv, p)}
	start(t, c, version)
	return c
}

func (c *stdioClient) initialize(t *testing.T, version string) wireResp {
	t.Helper()
	r := c.call(t, "initialize", initParams(version))
	if r.err == nil {
		c.version, _ = r.result["protocolVersion"].(string)
	}
	return r
}

func (c *stdioClient) call(t *testing.T, method string, params map[string]any) wireResp {
	t.Helper()
	id := c.id()
	c.pipe.write(t, string(c.message(id, method, params))+"\n")
	r := parseResp(t, c.pipe.next(t))
	if r.id() != strconv.Itoa(id) {
		t.Fatalf("%s: response id %s, want %d", method, r.id(), id)
	}
	return r
}

func (c *stdioClient) notify(t *testing.T, method string, params map[string]any) {
	t.Helper()
	c.pipe.write(t, string(c.message(0, method, params))+"\n")
}

func (c *stdioClient) send(t *testing.T, raw string, want int) []wireResp {
	t.Helper()
	c.pipe.write(t, raw+"\n")
	var out []wireResp
	for len(out) < want {
		line := c.pipe.next(t)
		if len(line) > 0 && line[0] == '[' {
			var raws []json.RawMessage
			if err := json.Unmarshal(line, &raws); err != nil {
				t.Fatal(err)
			}
			for _, raw := range raws {
				out = append(out, parseResp(t, raw))
			}
			continue
		}
		out = append(out, parseResp(t, line))
	}
	if len(out) != want {
		t.Fatalf("want %d responses, got %d", want, len(out))
	}
	return out
}

// quiet sends a ping, answered with a result or an error depending on the
// revision; either way the answer must be the next line.
func (c *stdioClient) quiet(t *testing.T) {
	t.Helper()
	c.call(t, "ping", nil)
}
