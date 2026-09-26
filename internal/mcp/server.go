package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/version"
)

// Options configures a Server. Tools is the only required field.
type Options struct {
	// Name and Version identify the server to clients; they default to
	// "playkeeper" and the build's version.
	Name    string
	Version string
	// Instructions is optional guidance for the model on using the tools.
	Instructions string
	Tools        []Tool
	Logger       *slog.Logger
	Now          func() time.Time
	// PageSize is the number of tools per tools/list page; default 50.
	PageSize int
	// CacheTTL is the freshness hint on server/discover and tools/list
	// results; default 5 minutes.
	CacheTTL time.Duration
	// CallTimeout bounds each tool call; default 2 minutes.
	CallTimeout time.Duration
	// ReadOnlyCallsPerMinute and MutatingCallsPerMinute limit tool calls per
	// principal; defaults 120 and 30.
	ReadOnlyCallsPerMinute int
	MutatingCallsPerMinute int
	// OnCall, when set, is called after every tool call, for auditing.
	OnCall func(CallRecord)
	// MaxMessageBytes caps one JSON-RPC message; default 256 KiB. HTTP
	// requests and stdio lines above it are refused.
	MaxMessageBytes int
}

// maxBatch caps the messages in one JSON-RPC batch.
const maxBatch = 64

// Server holds the tool registry and answers MCP requests. It is safe for
// concurrent use and can serve HTTP and stdio clients at the same time.
type Server struct {
	name, version, instructions string
	tools                       []*registered
	byName                      map[string]*registered
	log                         *slog.Logger
	now                         func() time.Time
	pageSize                    int
	cacheTTL                    time.Duration
	callTimeout                 time.Duration
	readLimit, writeLimit       *limiter
	onCall                      func(CallRecord)
	maxMessage                  int
}

// New checks the tools and returns a server for them.
func New(opts Options) (*Server, error) {
	if opts.Name == "" {
		opts.Name = "playkeeper"
	}
	if opts.Version == "" {
		opts.Version = version.Version
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.PageSize <= 0 {
		opts.PageSize = 50
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = 5 * time.Minute
	}
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = 2 * time.Minute
	}
	if opts.ReadOnlyCallsPerMinute <= 0 {
		opts.ReadOnlyCallsPerMinute = 120
	}
	if opts.MutatingCallsPerMinute <= 0 {
		opts.MutatingCallsPerMinute = 30
	}
	if opts.MaxMessageBytes <= 0 {
		opts.MaxMessageBytes = 256 << 10
	}
	s := &Server{
		name: opts.Name, version: opts.Version, instructions: opts.Instructions,
		byName: map[string]*registered{}, log: opts.Logger, now: opts.Now,
		pageSize: opts.PageSize, cacheTTL: opts.CacheTTL, callTimeout: opts.CallTimeout,
		readLimit:  newLimiter(opts.ReadOnlyCallsPerMinute, time.Minute, opts.Now),
		writeLimit: newLimiter(opts.MutatingCallsPerMinute, time.Minute, opts.Now),
		onCall:     opts.OnCall, maxMessage: opts.MaxMessageBytes,
	}
	for _, t := range opts.Tools {
		r, err := register(t)
		if err != nil {
			return nil, err
		}
		if s.byName[r.Name] != nil {
			return nil, fmt.Errorf("mcp: tool name %q is used twice", r.Name)
		}
		s.byName[r.Name] = r
		s.tools = append(s.tools, r)
	}
	return s, nil
}

// caller is who sent a request and in which protocol revision.
type caller struct {
	principal Principal
	version   string
	modern    bool
	client    ClientInfo
}

// requestEra reports whether a request uses the 2026-07-28 revision, which
// it declares in params._meta. A legacy revision named there is ignored:
// legacy requests take their revision from initialize.
func requestEra(params map[string]json.RawMessage) (modern bool, version string, e *rpcError) {
	v, declared, e := requestVersion(params)
	if e != nil || !declared {
		return false, "", e
	}
	switch {
	case isModern(v):
		return true, v, nil
	case isLegacy(v):
		return false, "", nil
	}
	return false, "", unsupportedVersion(v)
}

// modernCaller checks the rest of a 2026-07-28 request's metadata.
func modernCaller(p Principal, version string, params map[string]json.RawMessage) (caller, *rpcError) {
	client, e := modernMeta(params)
	if e != nil {
		return caller{}, e
	}
	return caller{principal: p, version: version, modern: true, client: client}, nil
}

// knownMethod reports whether the server implements a request method in the
// given era.
func knownMethod(modern bool, method string) bool {
	switch method {
	case "tools/list", "tools/call":
		return true
	case "server/discover":
		return modern
	case "ping":
		return !modern
	}
	return false
}

// handle answers one request that is not initialize. The transport has
// already worked out the caller's protocol revision.
func (s *Server) handle(ctx context.Context, c caller, m *message) (any, *rpcError) {
	if !knownMethod(c.modern, m.method) {
		return nil, methodNotFound(m.method)
	}
	var (
		result map[string]any
		err    *rpcError
	)
	switch m.method {
	case "ping":
		result = map[string]any{}
	case "server/discover":
		result = s.discover()
	case "tools/list":
		result, err = s.listTools(c, m.params)
	case "tools/call":
		result, err = s.callTool(ctx, c, m.params)
	}
	if err != nil {
		return nil, err
	}
	if c.modern {
		s.complete(result)
	}
	return result, nil
}

// notify handles a client notification. notifications/cancelled stops a
// request in rs; the others, notifications/initialized included, need no
// action, and unknown ones are ignored as JSON-RPC requires.
func (s *Server) notify(rs *requests, m *message) {
	if m.method != "notifications/cancelled" {
		return
	}
	id, ok := canonicalID(m.params["requestId"])
	if !ok {
		return
	}
	var reason string
	_ = json.Unmarshal(m.params["reason"], &reason)
	if rs.cancel(id) {
		s.log.Debug("mcp: request cancelled by the client", "id", truncate(string(id), 64), "reason", printable(reason, 200))
	}
}

// call runs a request registered in rs, so that notifications/cancelled can
// stop it. It returns nil when the client cancelled the request, which must
// then go unanswered.
func (s *Server) call(rs *requests, c caller, m *message) *response {
	ctx, finish, ok := rs.begin(m.id)
	if !ok {
		return reply(m.id, nil, errDuplicateID())
	}
	result, e := s.handle(ctx, c, m)
	if finish() {
		return nil
	}
	return reply(m.id, result, e)
}

func errDuplicateID() *rpcError {
	return newError(codeInvalidRequest, "A request with this id is already in progress.", "Give every request its own id.")
}

// batchMessage answers one message of a JSON-RPC batch sent in legacy
// revision c.version. It returns nil for messages that get no response.
func (s *Server) batchMessage(rs *requests, c caller, m *message) *response {
	if m == nil || m.kind == kindResponse {
		return nil
	}
	if m.kind == kindNotification {
		s.notify(rs, m)
		return nil
	}
	if modern, _, e := requestEra(m.params); e != nil || modern {
		if e == nil {
			e = newError(codeInvalidRequest, "Requests in protocol 2026-07-28 cannot be batched.", "Send each request on its own.")
		}
		return reply(m.id, nil, e)
	}
	if m.method == "initialize" {
		return reply(m.id, nil, newError(codeInvalidRequest, "initialize cannot be part of a batch.", ""))
	}
	return s.call(rs, c, m)
}

// batch answers the messages of a JSON-RPC batch one after another, in
// order. Batches exist only in 2025-03-26 and 2024-11-05.
func (s *Server) batch(rs *requests, c caller, raws []json.RawMessage) []*response {
	var out []*response
	for _, raw := range raws {
		m, errResp := decodeMessage(raw)
		if errResp != nil {
			out = append(out, errResp)
			continue
		}
		if resp := s.batchMessage(rs, c, m); resp != nil {
			out = append(out, resp)
		}
	}
	return out
}

// checkBatch decodes a batch and checks that revision v allows it. v is
// empty when no revision has been negotiated.
func checkBatch(body []byte, v string) ([]json.RawMessage, *rpcError) {
	var raws []json.RawMessage
	if err := json.Unmarshal(body, &raws); err != nil || len(raws) == 0 {
		return nil, newError(codeInvalidRequest, "A JSON-RPC batch must hold at least one message.", "")
	}
	if !allowsBatch(v) {
		if v == "" {
			return nil, newError(codeInvalidRequest,
				"JSON-RPC batches are only accepted after initialize has negotiated protocol 2025-03-26 or 2024-11-05.",
				"Send each message on its own.")
		}
		return nil, newError(codeInvalidRequest, "Protocol "+v+" does not allow JSON-RPC batches.", "Send each message on its own.")
	}
	if len(raws) > maxBatch {
		return nil, newError(codeInvalidRequest, fmt.Sprintf("A JSON-RPC batch may hold at most %d messages.", maxBatch), "")
	}
	return raws, nil
}

// complete adds the fields every 2026-07-28 result carries.
func (s *Server) complete(result map[string]any) {
	result["resultType"] = "complete"
	m, _ := result["_meta"].(map[string]any)
	if m == nil {
		m = map[string]any{}
		result["_meta"] = m
	}
	m[metaServerInfo] = s.serverInfo()
}

func (s *Server) serverInfo() map[string]string {
	return map[string]string{"name": s.name, "version": s.version}
}

func capabilities() map[string]any {
	return map[string]any{"tools": map[string]any{}}
}

// initialize answers a legacy initialize request and returns the negotiated
// revision and the client's self-reported identity.
func (s *Server) initialize(params map[string]json.RawMessage) (string, ClientInfo, map[string]any, *rpcError) {
	var requested string
	if err := json.Unmarshal(params["protocolVersion"], &requested); err != nil || requested == "" {
		return "", ClientInfo{}, nil, newError(codeInvalidParams, "initialize needs a protocolVersion string.", "")
	}
	v := negotiate(requested)
	result := map[string]any{
		"protocolVersion": v,
		"capabilities":    capabilities(),
		"serverInfo":      s.serverInfo(),
	}
	if s.instructions != "" {
		result["instructions"] = s.instructions
	}
	return v, parseClientInfo(params["clientInfo"]), result, nil
}

func (s *Server) discover() map[string]any {
	result := map[string]any{
		"supportedVersions": supportedVersions(),
		"capabilities":      capabilities(),
		"ttlMs":             s.cacheTTL.Milliseconds(),
		"cacheScope":        "private",
	}
	if s.instructions != "" {
		result["instructions"] = s.instructions
	}
	return result
}

// visible returns the tools principal p may call, in registration order.
func (s *Server) visible(p Principal) []*registered {
	var out []*registered
	for _, t := range s.tools {
		if p.Allows(t.Scope) {
			out = append(out, t)
		}
	}
	return out
}

// listTools pages through the tools the caller may call. The list depends
// on the caller's scopes, so modern results are marked cacheScope private.
func (s *Server) listTools(c caller, params map[string]json.RawMessage) (map[string]any, *rpcError) {
	tools := s.visible(c.principal)
	fp := fingerprint(tools)
	offset := 0
	if raw, ok := params["cursor"]; ok && !isNull(raw) {
		var cursor string
		if err := json.Unmarshal(raw, &cursor); err != nil {
			return nil, invalidCursor()
		}
		n, ok := parseCursor(cursor, fp, len(tools))
		if !ok {
			return nil, invalidCursor()
		}
		offset = n
	}
	end := min(offset+s.pageSize, len(tools))
	defs := make([]json.RawMessage, 0, end-offset)
	for _, t := range tools[offset:end] {
		defs = append(defs, t.defs[c.version])
	}
	result := map[string]any{"tools": defs}
	if end < len(tools) {
		result["nextCursor"] = makeCursor(end, fp)
	}
	if c.modern {
		result["ttlMs"] = s.cacheTTL.Milliseconds()
		result["cacheScope"] = "private"
	}
	return result, nil
}

func invalidCursor() *rpcError {
	return newError(codeInvalidParams, "The cursor is not valid for this tool list.", "Call tools/list without a cursor to start from the first page.")
}

// fingerprint identifies a caller's tool list, so a cursor from a different
// list (another token, or another build) is refused rather than misread.
func fingerprint(tools []*registered) string {
	h := fnv.New64a()
	for _, t := range tools {
		h.Write([]byte(t.Name))
		h.Write([]byte{0})
	}
	return strconv.FormatUint(h.Sum64(), 36)
}

func makeCursor(offset int, fp string) string {
	return base64.RawURLEncoding.EncodeToString([]byte("tools:" + strconv.Itoa(offset) + ":" + fp))
}

func parseCursor(cursor, fp string, total int) (int, bool) {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, false
	}
	parts := strings.Split(string(b), ":")
	if len(parts) != 3 || parts[0] != "tools" || parts[2] != fp {
		return 0, false
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n <= 0 || n >= total {
		return 0, false
	}
	return n, true
}

// callTool runs tools/call. Unknown tools and malformed requests are
// JSON-RPC errors; everything the model can correct is a result with
// isError set.
func (s *Server) callTool(ctx context.Context, c caller, params map[string]json.RawMessage) (map[string]any, *rpcError) {
	var name string
	if err := json.Unmarshal(params["name"], &name); err != nil || name == "" {
		return nil, newError(codeInvalidParams, "tools/call needs the name of a tool.", "Call tools/list to see the available tools.")
	}
	t := s.byName[name]
	if t == nil {
		return nil, newError(codeInvalidParams, "Unknown tool: "+truncate(name, 128), "Call tools/list to see the available tools.")
	}
	args, perr := decodeArguments(params["arguments"])
	if perr != nil {
		return nil, perr
	}
	start := s.now()
	res, structured, terr, rerr, outcome := s.execute(ctx, c, t, args)
	kind := ""
	if terr != nil {
		kind = terr.Kind
	}
	s.record(CallRecord{
		Principal: c.principal, Tool: t.Name, Effect: t.Effect, Outcome: outcome, Kind: kind,
		Duration: s.now().Sub(start), Protocol: c.version, Client: c.client,
	})
	if rerr != nil {
		return nil, rerr
	}
	if terr != nil {
		return toolErrorResult(terr), nil
	}
	return successResult(c.version, res, structured), nil
}

func decodeArguments(raw json.RawMessage) (map[string]any, *rpcError) {
	if len(raw) == 0 || isNull(raw) {
		return map[string]any{}, nil
	}
	v, err := decodeJSON(raw)
	obj, ok := v.(map[string]any)
	if err != nil || !ok {
		return nil, newError(codeInvalidParams, "The tool arguments must be a JSON object.", "")
	}
	return obj, nil
}

// execute checks rate limit, scope and arguments, then runs the handler.
// Refused calls count against the limit too, so a token can't make
// unlimited calls it isn't allowed. Exactly one of res, terr and rerr is
// set on return.
func (s *Server) execute(ctx context.Context, c caller, t *registered, args map[string]any) (res *Result, structured json.RawMessage, terr *ToolError, rerr *rpcError, outcome Outcome) {
	lim := s.writeLimit
	if t.Effect == ReadOnly {
		lim = s.readLimit
	}
	if ok, wait := lim.take(c.principal.ID); !ok {
		secs := retryAfter(wait)
		return nil, nil, &ToolError{
			Kind:   KindRateLimited,
			Msg:    fmt.Sprintf("Too many tool calls with this token. Try again in %s.", plural(secs, "second")),
			Params: map[string]string{"retry_after_seconds": strconv.Itoa(secs)},
		}, nil, OutcomeLimited
	}
	if !c.principal.Allows(t.Scope) {
		return nil, nil, scopeError(c.principal, t), nil, OutcomeDenied
	}
	p := problems{root: "The arguments"}
	normalised := t.InputSchema.validate(args, "", &p)
	if len(p.list) > 0 {
		msg := "The arguments for " + t.Name + " are not valid: " + p.summary()
		if len(p.list) > 1 {
			msg = "The arguments for " + t.Name + " are not valid:\n" + p.summary()
		}
		return nil, nil, &ToolError{
			Kind: KindInvalidArguments,
			Msg:  msg,
			Hint: "Check the tool's input schema and call it again.",
		}, nil, OutcomeInvalid
	}
	rawArgs, err := json.Marshal(normalised)
	if err != nil {
		s.log.Error("mcp: encoding validated arguments", "tool", t.Name, "err", err)
		return nil, nil, nil, internalError(), OutcomeFailed
	}
	call := &Call{Tool: t.Name, Principal: c.principal, Arguments: rawArgs, Protocol: c.version, Client: c.client}

	res, panicked, herr := s.run(ctx, t, call)
	switch {
	case panicked:
		return nil, nil, nil, internalError(), OutcomeFailed
	case ctx.Err() != nil:
		return nil, nil, nil, newError(codeInternalError, "The request was cancelled.", ""), OutcomeCancelled
	case errors.Is(herr, context.DeadlineExceeded):
		return nil, nil, &ToolError{
			Kind: KindTimeout,
			Msg:  fmt.Sprintf("%s did not finish within %s.", t.Name, s.callTimeout),
			Hint: "It may still complete in the background; check the current state before calling it again.",
		}, nil, OutcomeTimeout
	case herr != nil:
		var te *ToolError
		if errors.As(herr, &te) {
			return nil, nil, te, nil, OutcomeToolError
		}
		s.log.Error("mcp: tool failed", "tool", t.Name, "principal", c.principal.ID, "err", herr)
		return nil, nil, &ToolError{
			Kind: KindInternal,
			Msg:  t.Name + " failed unexpectedly.",
			Hint: "The details are in the Playkeeper logs.",
		}, nil, OutcomeFailed
	}
	if res == nil {
		res = &Result{}
	}
	if res.Structured != nil {
		structured, err = json.Marshal(res.Structured)
		if err != nil {
			s.log.Error("mcp: encoding structured result", "tool", t.Name, "err", err)
			return nil, nil, nil, internalError(), OutcomeFailed
		}
	}
	if t.OutputSchema != nil {
		if structured == nil {
			s.log.Error("mcp: tool returned no structured content for its output schema", "tool", t.Name)
			return nil, nil, nil, internalError(), OutcomeFailed
		}
		v, _ := decodeJSON(structured)
		p := problems{root: "The result"}
		t.OutputSchema.validate(v, "", &p)
		if len(p.list) > 0 {
			s.log.Error("mcp: tool result does not match its output schema", "tool", t.Name, "problem", p.list[0])
			return nil, nil, nil, internalError(), OutcomeFailed
		}
	}
	return res, structured, nil, nil, OutcomeOK
}

func internalError() *rpcError {
	return newError(codeInternalError, "The server failed to run the tool.", "The details are in the Playkeeper logs.")
}

// run calls the handler under the call timeout. It returns when the handler
// does or when ctx ends, so a handler that ignores its context cannot hold
// the request open.
func (s *Server) run(ctx context.Context, t *registered, call *Call) (res *Result, panicked bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, s.callTimeout)
	defer cancel()
	type outcome struct {
		res      *Result
		panicked bool
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("mcp: tool panicked", "tool", t.Name, "panic", v, "stack", string(debug.Stack()))
				done <- outcome{panicked: true}
			}
		}()
		res, err := t.Handler(ctx, call)
		done <- outcome{res: res, err: err}
	}()
	select {
	case o := <-done:
		if o.err != nil && ctx.Err() != nil && (errors.Is(o.err, context.Canceled) || errors.Is(o.err, context.DeadlineExceeded)) {
			o.err = ctx.Err()
		}
		return o.res, o.panicked, o.err
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

func scopeError(p Principal, t *registered) *ToolError {
	have := "no scope"
	if h := p.highest(); h != "" {
		have = "the " + string(h) + " scope"
	}
	return &ToolError{
		Kind:   KindScopeMissing,
		Msg:    fmt.Sprintf("This token has %s, but %s needs the %s scope.", have, t.Name, t.Scope),
		Hint:   fmt.Sprintf("Create a token with the %s scope in the Playkeeper panel and update the client's configuration.", t.Scope),
		Params: map[string]string{"required_scope": string(t.Scope), "tool": t.Name},
	}
}

func textBlock(text string) map[string]any { return map[string]any{"type": "text", "text": text} }

func toolErrorResult(e *ToolError) map[string]any {
	detail := map[string]any{"kind": e.Kind}
	if e.Hint != "" {
		detail["hint"] = e.Hint
	}
	if len(e.Params) > 0 {
		detail["params"] = e.Params
	}
	return map[string]any{
		"content": []any{textBlock(e.text())},
		"isError": true,
		"_meta":   map[string]any{metaToolError: detail},
	}
}

// successResult shapes a result for revision v. structuredContent arrived in
// 2025-06-18, restricted to objects until 2026-07-28; the JSON is also sent
// as text for clients that only read content.
func successResult(v string, r *Result, structured json.RawMessage) map[string]any {
	var blocks []any
	if r.Text != "" || structured == nil {
		blocks = append(blocks, textBlock(r.Text))
	}
	if structured != nil {
		blocks = append(blocks, textBlock(string(structured)))
	}
	result := map[string]any{"content": blocks}
	if structured != nil && atLeast(v, version20250618) && (isModern(v) || structured[0] == '{') {
		result["structuredContent"] = structured
	}
	return result
}

func (s *Server) record(rec CallRecord) {
	level := slog.LevelInfo
	if rec.Effect == ReadOnly && rec.Outcome == OutcomeOK {
		level = slog.LevelDebug
	}
	s.log.Log(context.Background(), level, "mcp: tool call", "tool", rec.Tool, "principal", rec.Principal.ID,
		"outcome", string(rec.Outcome), "kind", rec.Kind, "duration", rec.Duration.Round(time.Millisecond), "protocol", rec.Protocol)
	if s.onCall != nil {
		s.onCall(rec)
	}
}
