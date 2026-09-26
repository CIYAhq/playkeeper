package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// These tests cover what is particular to Streamable HTTP: headers, status
// codes, sessions, origins and tokens.

const pingBody = `{"jsonrpc":"2.0","id":1,"method":"ping"}`

// answer checks the status of a response holding one JSON-RPC message and
// returns the message.
func answer(t *testing.T, rec *httptest.ResponseRecorder, status int) wireResp {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("HTTP %d, want %d: %s", rec.Code, status, rec.Body)
	}
	return httpResponses(t, rec, 1)[0]
}

// wantMessage checks an error's code, message and hint.
func wantMessage(t *testing.T, r wireResp, code int, msg, hint string) {
	t.Helper()
	e := r.wantError(t, code)
	got, _ := e.Data["hint"].(string)
	if e.Message != msg || got != hint {
		t.Errorf("error %q with hint %q,\nwant  %q with hint %q", e.Message, got, msg, hint)
	}
}

// wantHeaders checks response headers; an empty value means absent.
func wantHeaders(t *testing.T, rec *httptest.ResponseRecorder, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s: %q, want %q", k, got, v)
		}
	}
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(wait):
		t.Fatal("the request did not finish")
		var zero T
		return zero
	}
}

// background serves req in the background.
func background(h http.Handler, req *http.Request) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- serve(h, req) }()
	return done
}

// modernRequest builds a 2026-07-28 request with the headers that mirror its
// body, sent with the owner token.
func modernRequest(id int, method string, params map[string]any) *http.Request {
	b := clientBase{version: version20260728, modern: true}
	hdr := map[string]string{"MCP-Protocol-Version": version20260728, "Mcp-Method": method}
	if name, ok := params["name"].(string); ok && method == "tools/call" {
		hdr["Mcp-Name"] = name
	}
	return newRequest(http.MethodPost, string(b.message(id, method, params)), hdr)
}

// slowRequest builds c's request for the slow tool and returns its id.
func (c *httpClient) slowRequest() (*http.Request, int) {
	id := c.id()
	body := c.message(id, "tools/call", map[string]any{"name": "slow"})
	return newRequest(http.MethodPost, string(body), c.headers("tools/call", "slow")), id
}

func setHeader(k, v string) func(http.Header) { return func(h http.Header) { h.Set(k, v) } }
func addHeader(k, v string) func(http.Header) { return func(h http.Header) { h.Add(k, v) } }
func delHeader(k string) func(http.Header)    { return func(h http.Header) { h.Del(k) } }

func TestHTTPModernHeadersMustMirrorTheBody(t *testing.T) {
	const (
		notValid   = "The Mcp-Name header is not valid."
		encodeHint = "Encode values that are not plain ASCII as =?base64?…?=."
		unknown    = "Call tools/list to see the available tools."
	)
	echo := map[string]any{"name": "echo", "arguments": map[string]any{"text": "hi"}}
	long := strings.Repeat("x", 200)
	for _, tc := range []struct {
		name   string
		method string
		params map[string]any
		edit   func(http.Header)
		status int
		code   int // 0 for a result
		msg    string
		hint   string
	}{
		{"every header", "tools/call", echo, nil, 200, 0, "", ""},
		{"a Base64 tool name", "tools/call", echo, setHeader("Mcp-Name", "=?base64?ZWNobw==?="), 200, 0, "", ""},
		{"unpadded Base64", "tools/call", echo, setHeader("Mcp-Name", "=?base64?ZWNobw?="), 200, 0, "", ""},
		{"an Mcp-Name on another method", "tools/list", nil, setHeader("Mcp-Name", "echo"), 200, 0, "", ""},
		{"no protocol version header", "tools/list", nil, delHeader("Mcp-Protocol-Version"), 400, codeHeaderMismatch,
			"The MCP-Protocol-Version header is missing.",
			"Requests in protocol 2026-07-28 must mirror the protocol version in params._meta in it."},
		{"another protocol version", "tools/list", nil, setHeader("Mcp-Protocol-Version", "2025-11-25"), 400, codeHeaderMismatch,
			`The MCP-Protocol-Version header "2025-11-25" does not match the protocol version in params._meta, "2026-07-28".`, ""},
		{"two protocol version headers", "tools/list", nil, addHeader("Mcp-Protocol-Version", "2026-07-28"), 400, codeHeaderMismatch,
			"The MCP-Protocol-Version header appears more than once.", ""},
		{"no method header", "tools/list", nil, delHeader("Mcp-Method"), 400, codeHeaderMismatch,
			"The Mcp-Method header is missing.", "Requests in protocol 2026-07-28 must mirror the request's method in it."},
		{"another method", "tools/call", echo, setHeader("Mcp-Method", "tools/list"), 400, codeHeaderMismatch,
			`The Mcp-Method header "tools/list" does not match the request's method, "tools/call".`, ""},
		{"a method in another case", "tools/list", nil, setHeader("Mcp-Method", "Tools/List"), 400, codeHeaderMismatch,
			`The Mcp-Method header "Tools/List" does not match the request's method, "tools/list".`, ""},
		{"no name header", "tools/call", echo, delHeader("Mcp-Name"), 400, codeHeaderMismatch,
			"The Mcp-Name header is missing.", "Requests in protocol 2026-07-28 must mirror the tool name in the request in it."},
		{"another tool name", "tools/call", echo, setHeader("Mcp-Name", "restart_server"), 400, codeHeaderMismatch,
			`The Mcp-Name header "restart_server" does not match the tool name in the request, "echo".`, ""},
		{"a long tool name", "tools/call", echo, setHeader("Mcp-Name", long), 400, codeHeaderMismatch,
			`The Mcp-Name header "` + long[:128] + `…" does not match the tool name in the request, "echo".`, ""},
		{"Base64 of another name", "tools/call", echo, setHeader("Mcp-Name", "=?base64?ZWNobzI=?="), 400, codeHeaderMismatch,
			`The Mcp-Name header "echo2" does not match the tool name in the request, "echo".`, ""},
		{"broken Base64", "tools/call", echo, setHeader("Mcp-Name", "=?base64?!!!?="), 400, codeHeaderMismatch, notValid, encodeHint},
		{"badly padded Base64", "tools/call", echo, setHeader("Mcp-Name", "=?base64?ZWNobw=?="), 400, codeHeaderMismatch, notValid, encodeHint},
		{"a raw non-ASCII name", "tools/call", map[string]any{"name": "écho"}, nil, 400, codeHeaderMismatch, notValid, encodeHint},
		{"a control character", "tools/call", echo, setHeader("Mcp-Name", "ech\x01o"), 400, codeHeaderMismatch, notValid, encodeHint},
		// The header only has to agree with the body; the tool can still be
		// unknown.
		{"an encoded non-ASCII name", "tools/call", map[string]any{"name": "écho"}, setHeader("Mcp-Name", "=?base64?w6ljaG8=?="),
			200, codeInvalidParams, "Unknown tool: écho", unknown},
		{"no tool name to mirror", "tools/call", map[string]any{"arguments": map[string]any{}}, nil,
			200, codeInvalidParams, "tools/call needs the name of a tool.", unknown},
		{"an unknown method", "resources/list", nil, nil, 404, codeMethodNotFound, `Method "resources/list" is not supported.`, ""},
		{"an unknown method without its header", "resources/list", nil, delHeader("Mcp-Method"), 400, codeHeaderMismatch,
			"The Mcp-Method header is missing.", "Requests in protocol 2026-07-28 must mirror the request's method in it."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			h, _ := newHTTP(t, f, nil)
			req := modernRequest(1, tc.method, tc.params)
			if tc.edit != nil {
				tc.edit(req.Header)
			}
			rec := serve(h, req)
			r := answer(t, rec, tc.status)
			if r.id() != "1" {
				t.Errorf("response id %q, want 1", r.id())
			}
			if tc.code == 0 {
				r.wantResult(t)
			} else {
				wantMessage(t, r, tc.code, tc.msg, tc.hint)
			}
			if ran, want := len(f.calls()) == 1, tc.code == 0 && tc.method == "tools/call"; ran != want {
				t.Errorf("%d tool calls ran", len(f.calls()))
			}
			wantHeaders(t, rec, map[string]string{"Mcp-Session-Id": "", "Cache-Control": "no-store"})
		})
	}
}

func TestHTTPModernRequestsNeedNoSession(t *testing.T) {
	const meta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`
	f := newFixture(t, nil)
	h, _ := newHTTP(t, f, nil)
	legacy := newHTTPClient(t, h, "tok-owner", version20251125)
	// A session ID is neither needed nor echoed, whether it exists or not.
	for _, session := range []string{legacy.session, "no-such-session"} {
		req := modernRequest(1, "tools/list", nil)
		req.Header.Set("Mcp-Session-Id", session)
		rec := serve(h, req)
		if res := answer(t, rec, http.StatusOK).wantResult(t); res["resultType"] != "complete" {
			t.Errorf("not a 2026-07-28 result: %v", res)
		}
		wantHeaders(t, rec, map[string]string{"Mcp-Session-Id": ""})
	}
	legacy.call(t, "ping", nil).wantResult(t)
	if n := h.sessions.size(); n != 1 {
		t.Errorf("%d sessions, want 1", n)
	}

	// Notifications are accepted without their headers being checked.
	httpResponses(t, post(h, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1,`+meta+`}}`, nil), 0)
	httpResponses(t, post(h, `{"jsonrpc":"2.0","method":"notifications/whatever","params":{`+meta+`}}`,
		map[string]string{"MCP-Protocol-Version": version20250618}), 0)

	// A tool that fails still answers 200: the failure is in the result.
	rec := serve(h, modernRequest(2, "tools/call", map[string]any{"name": "offline"}))
	if res := answer(t, rec, http.StatusOK).wantResult(t); res["isError"] != true {
		t.Errorf("want isError, got %v", res)
	}

	for _, tc := range []struct {
		name, meta string
		code       int
	}{
		{"an unsupported version", `{"io.modelcontextprotocol/protocolVersion":"2099-01-01","io.modelcontextprotocol/clientCapabilities":{}}`, codeUnsupportedVersion},
		{"no capabilities", `{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}`, codeInvalidParams},
		{"_meta that is not an object", `[]`, codeInvalidParams},
	} {
		rec := post(h, `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"_meta":`+tc.meta+`}}`,
			map[string]string{"MCP-Protocol-Version": version20260728, "Mcp-Method": "tools/list"})
		r := answer(t, rec, http.StatusBadRequest)
		if r.wantError(t, tc.code); r.id() != "3" {
			t.Errorf("%s: response id %q, want 3", tc.name, r.id())
		}
	}
}

func TestHTTPRefusesBadRequests(t *testing.T) {
	const (
		notJSON    = "The request body is not valid JSON."
		wrongType  = "The request body must be JSON, sent with Content-Type: application/json."
		notAccepts = "The client must accept application/json responses."
		tooLarge   = "The request body is larger than 1 KiB."
	)
	f := newFixture(t, func(o *Options) { o.MaxMessageBytes = 1024 })
	h, _ := newHTTP(t, f, nil)
	big := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"` + strings.Repeat("x", 1024) + `"}}`
	full := pingBody + strings.Repeat(" ", 1024-len(pingBody))
	noContext := errNoContext()
	for _, tc := range []struct {
		name          string
		method        string
		body          string
		hdr           map[string]string
		unknownLength bool
		status        int
		code          int // 0 for a result
		msg           string
	}{
		{"GET", http.MethodGet, "", nil, false, 405, codeInvalidRequest,
			"The MCP endpoint offers no event stream; send each message in a POST request."},
		{"PUT", http.MethodPut, pingBody, nil, false, 405, codeInvalidRequest,
			"The MCP endpoint offers no event stream; send each message in a POST request."},
		{"no Content-Type", http.MethodPost, pingBody, map[string]string{"Content-Type": ""}, false, 415, codeInvalidRequest, wrongType},
		{"plain text", http.MethodPost, pingBody, map[string]string{"Content-Type": "text/plain"}, false, 415, codeInvalidRequest, wrongType},
		{"JSON in Latin-1", http.MethodPost, pingBody, map[string]string{"Content-Type": "application/json; charset=iso-8859-1"}, false, 415, codeInvalidRequest, wrongType},
		{"an event stream only", http.MethodPost, pingBody, map[string]string{"Accept": "text/event-stream"}, false, 406, codeInvalidRequest, notAccepts},
		{"JSON refused", http.MethodPost, pingBody, map[string]string{"Accept": "application/json;q=0, */*"}, false, 406, codeInvalidRequest, notAccepts},
		{"an empty body", http.MethodPost, "", nil, false, 400, codeParseError, notJSON},
		{"only spaces", http.MethodPost, " \r\n\t", nil, false, 400, codeParseError, notJSON},
		{"broken JSON", http.MethodPost, `{"jsonrpc":`, nil, false, 400, codeParseError, notJSON},
		{"two messages", http.MethodPost, pingBody + pingBody, nil, false, 400, codeParseError, notJSON},
		{"a body over the limit", http.MethodPost, big, nil, false, 413, codeInvalidRequest, tooLarge},
		{"a body over the limit of unknown length", http.MethodPost, big, nil, true, 413, codeInvalidRequest, tooLarge},
		{"a malformed notification", http.MethodPost, `{"jsonrpc":"2.0","method":5}`, nil, false, 400, codeInvalidRequest,
			"The notification is not a valid JSON-RPC message."},
		{"a notification outside a session", http.MethodPost, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil, false,
			400, codeInvalidParams, noContext.Message},
		// Lenient but valid.
		{"a body at the limit", http.MethodPost, full, nil, false, 200, 0, ""},
		{"a body at the limit of unknown length", http.MethodPost, full, nil, true, 200, 0, ""},
		{"an explicit UTF-8 charset", http.MethodPost, pingBody, map[string]string{"Content-Type": "application/json; charset=UTF-8"}, false, 200, 0, ""},
		{"a media type in capitals", http.MethodPost, pingBody, map[string]string{"Content-Type": "Application/JSON"}, false, 200, 0, ""},
		{"no Accept header", http.MethodPost, pingBody, map[string]string{"Accept": ""}, false, 200, 0, ""},
		{"Accept with a wildcard", http.MethodPost, pingBody, map[string]string{"Accept": "*/*"}, false, 200, 0, ""},
		{"Accept of JSON alone", http.MethodPost, pingBody, map[string]string{"Accept": "application/json"}, false, 200, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := newRequest(tc.method, tc.body, tc.hdr)
			if tc.unknownLength {
				req.ContentLength = -1
			}
			rec := serve(h, req)
			r := answer(t, rec, tc.status)
			if tc.code == 0 {
				r.wantResult(t)
			} else if e := r.wantError(t, tc.code); e.Message != tc.msg || r.id() != "" {
				t.Errorf("error %q with id %q, want %q without an id", e.Message, r.id(), tc.msg)
			}
			wantHeaders(t, rec, map[string]string{"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff"})
			if tc.status == http.StatusMethodNotAllowed {
				wantHeaders(t, rec, map[string]string{"Allow": "POST, DELETE, OPTIONS"})
			}
		})
	}

	t.Run("a smaller body limit", func(t *testing.T) {
		h, _ := newHTTP(t, f, func(o *HTTPOptions) { o.MaxBodyBytes = 30 })
		r := answer(t, post(h, pingBody, nil), http.StatusRequestEntityTooLarge)
		if e := r.wantError(t, codeInvalidRequest); e.Message != "The request body is larger than 30 bytes." {
			t.Errorf("%q", e.Message)
		}
	})
}

func TestHTTPSessionLifecycle(t *testing.T) {
	f := newFixture(t, nil)
	h, auth := newHTTP(t, f, nil)
	auth.tokens["tok-owner-narrowed"] = Principal{ID: "token:owner", Scopes: []Scope{ScopeRead}}
	c := newHTTPClient(t, h, "tok-owner", version20251125)
	if len(c.session) < 16 || strings.ContainsFunc(c.session, func(r rune) bool { return r < '!' || r > '~' }) {
		t.Errorf("session ID %q is not 16 or more visible ASCII characters", c.session)
	}

	list := func(token, session string) *httptest.ResponseRecorder {
		return post(h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, map[string]string{
			"Authorization": "Bearer " + token, "Mcp-Session-Id": session, "MCP-Protocol-Version": version20251125,
		})
	}
	end := func(token, session string) *httptest.ResponseRecorder {
		return serve(h, newRequest(http.MethodDelete, "", map[string]string{"Authorization": "Bearer " + token, "Mcp-Session-Id": session}))
	}
	gone := func(rec *httptest.ResponseRecorder) {
		t.Helper()
		wantMessage(t, answer(t, rec, http.StatusNotFound), codeInvalidRequest,
			"The session has ended or does not exist.", "Send initialize to start a new session.")
	}

	if got := toolNames(t, answer(t, list("tok-owner", c.session), 200).wantResult(t)); !slices.Equal(got, allTools) {
		t.Errorf("tools %v", got)
	}
	// Scopes come from each request's token, so a narrowed token of the
	// same principal sees fewer tools at once.
	if got := toolNames(t, answer(t, list("tok-owner-narrowed", c.session), 200).wantResult(t)); !slices.Equal(got, allTools[:8]) {
		t.Errorf("tools %v", got)
	}
	// A session ID is not a credential: to another token the session does
	// not exist.
	gone(list("tok-read", c.session))
	gone(list("tok-owner", "no-such-session"))
	noContext := errNoContext()
	wantMessage(t, answer(t, list("tok-owner", ""), 400), codeInvalidParams, noContext.Message, noContext.Data.(map[string]string)["hint"])

	wantMessage(t, answer(t, end("tok-owner", ""), 400), codeInvalidRequest, "DELETE needs the Mcp-Session-Id header of the session to end.", "")
	gone(end("tok-read", c.session))
	gone(end("tok-owner", "no-such-session"))
	answer(t, list("tok-owner", c.session), 200).wantResult(t)
	if rec := end("tok-owner", c.session); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("DELETE: HTTP %d: %s", rec.Code, rec.Body)
	}
	gone(list("tok-owner", c.session))
	gone(end("tok-owner", c.session))
	if n := h.sessions.size(); n != 0 {
		t.Errorf("%d sessions after DELETE", n)
	}

	// A client whose session ended initializes again, perhaps still sending
	// the old ID, and gets a new session.
	rec := post(h, string(c.message(2, "initialize", initParams(version20251125))), map[string]string{"Mcp-Session-Id": c.session})
	answer(t, rec, http.StatusOK).wantResult(t)
	if id := rec.Header().Get("Mcp-Session-Id"); id == "" || id == c.session {
		t.Errorf("re-initializing gave session ID %q", id)
	}
	// A failed initialize starts none.
	rec = post(h, `{"jsonrpc":"2.0","id":3,"method":"initialize","params":{}}`, nil)
	answer(t, rec, http.StatusOK).wantError(t, codeInvalidParams)
	if id := rec.Header().Get("Mcp-Session-Id"); id != "" {
		t.Errorf("a failed initialize gave session ID %q", id)
	}
	if n := h.sessions.size(); n != 1 {
		t.Errorf("%d sessions, want 1", n)
	}
}

func TestHTTPLegacyProtocolVersionHeader(t *testing.T) {
	f := newFixture(t, nil)
	h, _ := newHTTP(t, f, nil)
	c := newHTTPClient(t, h, "tok-owner", version20250618)
	for _, tc := range []struct {
		header string
		code   int // 0 for a result
		msg    string
	}{
		// Clients of 2025-03-26 and earlier do not send the header.
		{"", 0, ""},
		{"2025-06-18", 0, ""},
		{"2025-11-25", codeInvalidRequest, "The MCP-Protocol-Version header says 2025-11-25, but this session uses 2025-06-18."},
		{"2026-07-28", codeInvalidRequest, "The MCP-Protocol-Version header says 2026-07-28, but this session uses 2025-06-18."},
		{"2099-01-01", codeUnsupportedVersion, `Protocol version "2099-01-01" is not supported.`},
	} {
		rec := post(h, pingBody, map[string]string{"Mcp-Session-Id": c.session, "MCP-Protocol-Version": tc.header})
		if tc.code == 0 {
			answer(t, rec, http.StatusOK).wantResult(t)
			continue
		}
		r := answer(t, rec, http.StatusBadRequest)
		if e := r.wantError(t, tc.code); e.Message != tc.msg || r.id() != "1" {
			t.Errorf("header %q: id %q: %q", tc.header, r.id(), e.Message)
		}
	}
}

func TestHTTPResponsesAndNotificationsFromTheClient(t *testing.T) {
	f := newFixture(t, nil)
	h, _ := newHTTP(t, f, nil)
	c := newHTTPClient(t, h, "tok-owner", version20251125)
	response := `{"jsonrpc":"2.0","id":7,"result":{}}`
	notification := `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":1,"progress":1}}`
	for _, body := range []string{response, notification, `{"jsonrpc":"2.0","id":8,"error":{"code":-1,"message":"No."}}`} {
		httpResponses(t, post(h, body, c.headers("", "")), 0)
	}
	// Refusals carry no id: a notification has none, and a response's names
	// a request of the server's.
	for _, tc := range []struct {
		name    string
		body    string
		session string
		status  int
		code    int
	}{
		{"a response without a session", response, "", 400, codeInvalidParams},
		{"a notification without a session", notification, "", 400, codeInvalidParams},
		{"a response in an unknown session", response, "gone", 404, codeInvalidRequest},
		{"a notification in an unknown session", notification, "gone", 404, codeInvalidRequest},
	} {
		r := answer(t, post(h, tc.body, map[string]string{"Mcp-Session-Id": tc.session}), tc.status)
		if r.wantError(t, tc.code); r.id() != "" {
			t.Errorf("%s: the error has id %s", tc.name, r.id())
		}
	}
}

func TestHTTPSessionsExpire(t *testing.T) {
	f := newFixture(t, nil)
	h, _ := newHTTP(t, f, func(o *HTTPOptions) {
		o.SessionIdleTimeout = time.Hour
		o.SessionMaxAge = 3 * time.Hour
	})
	alive := func(c *httpClient) bool {
		t.Helper()
		r := c.call(t, "ping", nil)
		if r.status != http.StatusOK && r.status != http.StatusNotFound {
			t.Fatalf("ping: HTTP %d", r.status)
		}
		return r.status == http.StatusOK
	}
	busy := newHTTPClient(t, h, "tok-owner", version20251125)
	idle := newHTTPClient(t, h, "tok-owner", version20251125)
	for range 3 {
		f.clock.add(59 * time.Minute)
		if !alive(busy) {
			t.Fatal("a session in use expired")
		}
	}
	if alive(idle) {
		t.Error("a session idle for 2h57m is still alive")
	}
	f.clock.add(3 * time.Minute)
	if alive(busy) {
		t.Error("a session 3 hours old is still alive")
	}

	c := newHTTPClient(t, h, "tok-owner", version20251125)
	f.clock.add(time.Hour - time.Second)
	if !alive(c) {
		t.Error("a session idle for just under the timeout expired")
	}
	f.clock.add(time.Hour)
	if alive(c) {
		t.Error("a session idle for the timeout is still alive")
	}
	if n := h.sessions.size(); n != 0 {
		t.Errorf("%d sessions are left", n)
	}
}

func TestHTTPSessionsAreCapped(t *testing.T) {
	f := newFixture(t, nil)
	h, _ := newHTTP(t, f, func(o *HTTPOptions) {
		o.MaxSessions = 3
		o.MaxSessionsPerPrincipal = 2
	})
	open := func(token string) *httpClient {
		t.Helper()
		f.clock.add(time.Minute)
		return newHTTPClient(t, h, token, version20251125)
	}
	a1 := open("tok-owner")
	a2 := open("tok-owner")
	f.clock.add(time.Minute)
	a1.call(t, "ping", nil).wantResult(t)
	a3 := open("tok-owner")  // the owner's third session ends a2, its least recently used
	r1 := open("tok-read")   // three sessions in all
	m1 := open("tok-manage") // the fourth ends a1, now the least recently used of all
	for _, tc := range []struct {
		name  string
		c     *httpClient
		alive bool
	}{
		{"a1", a1, false}, {"a2", a2, false}, {"a3", a3, true}, {"r1", r1, true}, {"m1", m1, true},
	} {
		if got := tc.c.call(t, "ping", nil).status == http.StatusOK; got != tc.alive {
			t.Errorf("%s alive: %v, want %v", tc.name, got, tc.alive)
		}
	}
}

func TestHTTPCloseSessionsEndsAPrincipalsSessions(t *testing.T) {
	f := newFixture(t, nil)
	h, _ := newHTTP(t, f, nil)
	a := newHTTPClient(t, h, "tok-read", version20251125)
	b := newHTTPClient(t, h, "tok-read", version20250326)
	other := newHTTPClient(t, h, "tok-owner", version20251125)
	req, _ := a.slowRequest()
	done := background(h, req)
	f.gate.waitStarted(t)
	if n := h.CloseSessions("token:read"); n != 2 {
		t.Errorf("CloseSessions ended %d sessions, want 2", n)
	}
	wantMessage(t, answer(t, receive(t, done), http.StatusOK), codeInternalError, "The request was cancelled.", "")
	if err := f.gate.waitEnded(t); !errors.Is(err, context.Canceled) {
		t.Errorf("the call ended with %v", err)
	}
	for _, c := range []*httpClient{a, b} {
		if r := c.call(t, "ping", nil); r.status != http.StatusNotFound {
			t.Errorf("an ended session answered HTTP %d", r.status)
		}
	}
	other.call(t, "ping", nil).wantResult(t)
	if n := h.CloseSessions("token:read"); n != 0 {
		t.Errorf("CloseSessions ended %d sessions again", n)
	}
	if got := f.outcomes(); !slices.Equal(got, []Outcome{OutcomeCancelled}) {
		t.Errorf("outcomes %v", got)
	}
}

func TestHTTPCloseCancelsEveryRequest(t *testing.T) {
	f := newFixture(t, nil)
	h, _ := newHTTP(t, f, nil)
	c := newHTTPClient(t, h, "tok-read", version20251125)
	modern := background(h, modernRequest(1, "tools/call", map[string]any{"name": "slow"}))
	f.gate.waitStarted(t)
	req, _ := c.slowRequest()
	legacy := background(h, req)
	f.gate.waitStarted(t)
	h.Close()
	for _, done := range []<-chan *httptest.ResponseRecorder{modern, legacy} {
		wantMessage(t, answer(t, receive(t, done), http.StatusOK), codeInternalError, "The request was cancelled.", "")
	}
	if n := h.sessions.size(); n != 0 {
		t.Errorf("%d sessions after Close", n)
	}
	if got := f.outcomes(); !slices.Equal(got, []Outcome{OutcomeCancelled, OutcomeCancelled}) {
		t.Errorf("outcomes %v", got)
	}
}

func TestHTTPLegacyRequestsAreCancelledByNotification(t *testing.T) {
	f := newFixture(t, nil)
	h, _ := newHTTP(t, f, nil)
	c := newHTTPClient(t, h, "tok-read", version20251125)
	otherSession := newHTTPClient(t, h, "tok-read", version20251125)
	req, id := c.slowRequest()
	done := background(h, req)
	f.gate.waitStarted(t)

	// A request can only be cancelled from its own session.
	otherSession.notify(t, "notifications/cancelled", map[string]any{"requestId": id})
	select {
	case err := <-f.gate.ended:
		t.Fatalf("another session's notification ended the call: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	c.notify(t, "notifications/cancelled", map[string]any{"requestId": id, "reason": "The user pressed stop."})
	// The request goes unanswered, but its POST still needs an answer.
	if rec := receive(t, done); rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Errorf("HTTP %d: %s", rec.Code, rec.Body)
	}
	if err := f.gate.waitEnded(t); !errors.Is(err, context.Canceled) {
		t.Errorf("the call ended with %v", err)
	}
	if got := f.outcomes(); !slices.Equal(got, []Outcome{OutcomeCancelled}) {
		t.Errorf("outcomes %v", got)
	}
	logs := f.logs.String()
	if !strings.Contains(logs, "request cancelled by the client") || !strings.Contains(logs, "The user pressed stop.") {
		t.Errorf("the cancellation was not logged:\n%s", logs)
	}
	// The id is free again.
	answer(t, post(h, string(c.message(id, "ping", nil)), c.headers("ping", "")), http.StatusOK).wantResult(t)
}

func TestHTTPDisconnectsCancelOnlyModernRequests(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		f := newFixture(t, nil)
		h, _ := newHTTP(t, f, nil)
		c := newHTTPClient(t, h, "tok-read", version20251125)
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := c.slowRequest()
		done := background(h, req.WithContext(ctx))
		f.gate.waitStarted(t)
		cancel()
		select {
		case err := <-f.gate.ended:
			t.Fatalf("the call ended with %v when the client disconnected", err)
		case <-time.After(20 * time.Millisecond):
		}
		f.gate.releaseOne(t)
		if got := resultText(t, answer(t, receive(t, done), http.StatusOK).wantResult(t)); got != "released" {
			t.Errorf("result %q", got)
		}
		if err := f.gate.waitEnded(t); err != nil {
			t.Errorf("the call ended with %v", err)
		}
	})
	t.Run("modern", func(t *testing.T) {
		f := newFixture(t, nil)
		h, _ := newHTTP(t, f, nil)
		ctx, cancel := context.WithCancel(context.Background())
		done := background(h, modernRequest(1, "tools/call", map[string]any{"name": "slow"}).WithContext(ctx))
		f.gate.waitStarted(t)
		cancel()
		if err := f.gate.waitEnded(t); !errors.Is(err, context.Canceled) {
			t.Errorf("the call ended with %v", err)
		}
		wantMessage(t, answer(t, receive(t, done), http.StatusOK), codeInternalError, "The request was cancelled.", "")
		if got := f.outcomes(); !slices.Equal(got, []Outcome{OutcomeCancelled}) {
			t.Errorf("outcomes %v", got)
		}
	})
}

func TestHTTPBatchStatuses(t *testing.T) {
	f := newFixture(t, nil)
	h, _ := newHTTP(t, f, nil)
	old := newHTTPClient(t, h, "tok-owner", version20250326)
	newer := newHTTPClient(t, h, "tok-owner", version20250618)
	ping := `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`
	for _, tc := range []struct {
		name   string
		body   string
		hdr    map[string]string
		status int
	}{
		{"in a 2025-03-26 session", ping, old.headers("", ""), 200},
		{"of notifications only", `[{"jsonrpc":"2.0","method":"notifications/initialized"}]`, old.headers("", ""), 202},
		{"empty", `[]`, old.headers("", ""), 400},
		{"without a session", ping, nil, 400},
		{"in a 2025-06-18 session", ping, newer.headers("", ""), 400},
		{"in an unknown session", ping, map[string]string{"Mcp-Session-Id": "gone"}, 404},
	} {
		rec := post(h, tc.body, tc.hdr)
		switch tc.status {
		case http.StatusOK:
			if rec.Code != http.StatusOK {
				t.Errorf("%s: HTTP %d: %s", tc.name, rec.Code, rec.Body)
			} else if canon(t, httpResponses(t, rec, 1)[0].wantResult(t)) != "{}" || rec.Body.Bytes()[0] != '[' {
				t.Errorf("%s: %s", tc.name, rec.Body)
			}
		case http.StatusAccepted:
			httpResponses(t, rec, 0)
		default:
			r := answer(t, rec, tc.status)
			if r.wantError(t, codeInvalidRequest); r.id() != "" {
				t.Errorf("%s: the error has id %s", tc.name, r.id())
			}
		}
	}
}

func TestHTTPOriginsAreChecked(t *testing.T) {
	f := newFixture(t, nil)
	h, auth := newHTTP(t, f, func(o *HTTPOptions) {
		o.AllowedOrigins = []string{"https://panel.example.com:8443", "HTTPS://Panel.Example.org:443", "http://[::1]:3000"}
	})
	refuse := func(t *testing.T, origins ...string) {
		t.Helper()
		// The origin is checked first, so a wrong token is not even looked at.
		req := newRequest(http.MethodPost, pingBody, map[string]string{"Authorization": "Bearer tok-wrong"})
		for _, o := range origins {
			req.Header.Add("Origin", o)
		}
		before := auth.calls()
		rec := serve(h, req)
		wantMessage(t, answer(t, rec, http.StatusForbidden), codeInvalidRequest,
			"Requests from this web page's origin are not allowed.",
			"Connect from an MCP client application, or add the origin to the allowed origins.")
		wantHeaders(t, rec, map[string]string{"Access-Control-Allow-Origin": ""})
		if auth.calls() != before {
			t.Error("the token was checked")
		}
	}
	for _, tc := range []struct {
		name    string
		origins []string
		allowed bool
	}{
		{"no origin", nil, true},
		{"an allowed origin", []string{"https://panel.example.com:8443"}, true},
		{"different case", []string{"https://PANEL.example.COM:8443"}, true},
		{"a default port left out", []string{"https://panel.example.org"}, true},
		{"an IPv6 origin", []string{"http://[::1]:3000"}, true},
		{"another port", []string{"https://panel.example.com"}, false},
		{"another scheme", []string{"http://panel.example.com:8443"}, false},
		{"another host", []string{"https://evil.example"}, false},
		{"a longer host", []string{"https://panel.example.com.evil.example:8443"}, false},
		{"null", []string{"null"}, false},
		{"a path", []string{"https://panel.example.com:8443/mcp"}, false},
		{"an empty origin", []string{""}, false},
		{"two origins", []string{"https://panel.example.com:8443", "https://evil.example"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.allowed {
				refuse(t, tc.origins...)
				return
			}
			req := newRequest(http.MethodPost, pingBody, nil)
			for _, o := range tc.origins {
				req.Header.Add("Origin", o)
			}
			rec := serve(h, req)
			answer(t, rec, http.StatusOK).wantResult(t)
			want := map[string]string{"Access-Control-Allow-Origin": "", "Vary": ""}
			if len(tc.origins) == 1 {
				want = map[string]string{
					"Access-Control-Allow-Origin":   tc.origins[0],
					"Access-Control-Expose-Headers": "Mcp-Session-Id, WWW-Authenticate, Retry-After",
					"Vary":                          "Origin",
				}
			}
			wantHeaders(t, rec, want)
		})
	}
	// Refused origins do not count as failed tokens.
	for range failedAuthPerMinute {
		refuse(t, "https://evil.example")
	}
	answer(t, post(h, pingBody, nil), http.StatusOK).wantResult(t)
}

func TestHTTPPreflight(t *testing.T) {
	f := newFixture(t, nil)
	h, auth := newHTTP(t, f, func(o *HTTPOptions) { o.AllowedOrigins = []string{"https://panel.example.com"} })
	preflight := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization, content-type, mcp-protocol-version")
		return serve(h, req)
	}
	rec := preflight("https://panel.example.com")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	wantHeaders(t, rec, map[string]string{
		"Access-Control-Allow-Origin":  "https://panel.example.com",
		"Access-Control-Allow-Methods": "POST, DELETE",
		"Access-Control-Allow-Headers": "Authorization, Content-Type, Accept, Mcp-Session-Id, Mcp-Protocol-Version, Mcp-Method, Mcp-Name",
		"Access-Control-Max-Age":       "600",
		"Vary":                         "Origin",
		"Allow":                        "POST, DELETE, OPTIONS",
	})

	rec = preflight("https://evil.example")
	answer(t, rec, http.StatusForbidden).wantError(t, codeInvalidRequest)
	wantHeaders(t, rec, map[string]string{"Access-Control-Allow-Methods": "", "Access-Control-Allow-Origin": ""})

	rec = preflight("")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS without an origin: HTTP %d", rec.Code)
	}
	wantHeaders(t, rec, map[string]string{"Allow": "POST, DELETE, OPTIONS", "Access-Control-Allow-Methods": ""})
	if n := auth.calls(); n != 0 {
		t.Errorf("preflight requests checked %d tokens", n)
	}
}

func TestNewHTTPHandlerChecksItsOptions(t *testing.T) {
	f := newFixture(t, nil)
	auth := newFakeAuth()
	origin := func(o string) HTTPOptions { return HTTPOptions{Authenticator: auth, AllowedOrigins: []string{o}} }
	badOrigin := func(o string) string {
		return `mcp: allowed origin "` + o + `" must be a scheme and host, such as https://panel.example.com:8443`
	}
	for _, tc := range []struct {
		name string
		srv  *Server
		opts HTTPOptions
		want string
	}{
		{"no server", nil, HTTPOptions{Authenticator: auth}, "mcp: NewHTTPHandler needs a server"},
		{"no authenticator", f.srv, HTTPOptions{}, "mcp: HTTPOptions.Authenticator is required"},
		{"an origin without a scheme", f.srv, origin("panel.example.com"), badOrigin("panel.example.com")},
		{"a wildcard", f.srv, origin("*"), badOrigin("*")},
		{"an origin with a path", f.srv, origin("https://panel.example.com/mcp"), badOrigin("https://panel.example.com/mcp")},
		{"a file URL", f.srv, origin("file:///srv"), badOrigin("file:///srv")},
	} {
		h, err := NewHTTPHandler(tc.srv, tc.opts)
		if err == nil || err.Error() != tc.want || h != nil {
			t.Errorf("%s: got %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestHTTPBearerTokens(t *testing.T) {
	const (
		missing          = "This endpoint needs a Playkeeper API token."
		missingHint      = `Create a token in the Playkeeper panel and send it in the Authorization header as "Bearer <token>".`
		invalid          = "The API token is not valid; it may have been revoked."
		invalidHint      = "Check the token in the client's configuration, or create a new one in the Playkeeper panel."
		plainChallenge   = `Bearer realm="playkeeper"`
		invalidChallenge = `Bearer realm="playkeeper", error="invalid_token", error_description="The token is unknown, malformed or revoked."`
	)
	for _, tc := range []struct {
		name    string
		values  []string
		ok      bool
		missing bool // otherwise invalid
		checked bool // whether the authenticator saw the token
	}{
		{"a valid token", []string{"Bearer tok-read"}, true, false, true},
		{"a lowercase scheme", []string{"bearer tok-read"}, true, false, true},
		{"extra spaces", []string{"  Bearer   tok-read  "}, true, false, true},
		{"no header", nil, false, true, false},
		{"another scheme", []string{"Basic dXNlcjpwYXNz"}, false, true, false},
		{"an unknown token", []string{"Bearer tok-wrong"}, false, false, true},
		{"the longest token", []string{"Bearer " + strings.Repeat("a", maxTokenBytes)}, false, false, true},
		{"an overlong token", []string{"Bearer " + strings.Repeat("a", maxTokenBytes+1)}, false, false, false},
		{"the scheme alone", []string{"Bearer"}, false, false, false},
		{"an empty token", []string{"Bearer "}, false, false, false},
		{"a space in the token", []string{"Bearer tok read"}, false, false, false},
		{"a quoted token", []string{`Bearer "tok-read"`}, false, false, false},
		{"two credentials", []string{"Bearer tok-read, Basic dXNlcjpwYXNz"}, false, false, false},
		{"two headers", []string{"Bearer tok-read", "Bearer tok-read"}, false, false, false},
		{"Basic, then Bearer", []string{"Basic dXNlcjpwYXNz", "Bearer tok-read"}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			h, auth := newHTTP(t, f, nil)
			req := newRequest(http.MethodPost, pingBody, map[string]string{"Authorization": ""})
			for _, v := range tc.values {
				req.Header.Add("Authorization", v)
			}
			rec := serve(h, req)
			if checked := auth.calls() == 1; checked != tc.checked {
				t.Errorf("the authenticator saw the token: %v, want %v", checked, tc.checked)
			}
			switch {
			case tc.ok:
				answer(t, rec, http.StatusOK).wantResult(t)
				wantHeaders(t, rec, map[string]string{"WWW-Authenticate": ""})
			case tc.missing:
				wantMessage(t, answer(t, rec, http.StatusUnauthorized), codeInvalidRequest, missing, missingHint)
				wantHeaders(t, rec, map[string]string{"WWW-Authenticate": plainChallenge})
			default:
				wantMessage(t, answer(t, rec, http.StatusUnauthorized), codeInvalidRequest, invalid, invalidHint)
				wantHeaders(t, rec, map[string]string{"WWW-Authenticate": invalidChallenge})
			}
			if logs := f.logs.String(); strings.Contains(logs, "tok-") || strings.Contains(logs, "dXNlcjpwYXNz") {
				t.Errorf("a credential was logged:\n%s", logs)
			}
		})
	}

	t.Run("DELETE", func(t *testing.T) {
		h, _ := newHTTP(t, newFixture(t, nil), nil)
		rec := serve(h, newRequest(http.MethodDelete, "", map[string]string{"Authorization": "", "Mcp-Session-Id": "abc"}))
		wantMessage(t, answer(t, rec, http.StatusUnauthorized), codeInvalidRequest, missing, missingHint)
	})
}

func TestHTTPAuthenticatorFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fault  func(*fakeAuth)
		logged string
	}{
		{"an error", func(a *fakeAuth) { a.err = errors.New("database is locked") }, "database is locked"},
		{"a principal without an ID", func(a *fakeAuth) { a.tokens["tok-read"] = Principal{Scopes: []Scope{ScopeRead}} }, "the principal has no ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			h, auth := newHTTP(t, f, nil)
			tc.fault(auth)
			for range failedAuthPerMinute + 1 {
				rec := post(h, pingBody, map[string]string{"Authorization": "Bearer tok-read"})
				wantMessage(t, answer(t, rec, http.StatusServiceUnavailable), codeInternalError,
					"Playkeeper could not check the API token.", "Try again in a moment.")
				wantHeaders(t, rec, map[string]string{"Retry-After": "5"})
			}
			logs := f.logs.String()
			if !strings.Contains(logs, "checking an API token failed") || !strings.Contains(logs, tc.logged) {
				t.Errorf("the failure was not logged:\n%s", logs)
			}
			if strings.Contains(logs, "tok-read") {
				t.Errorf("the token was logged:\n%s", logs)
			}
			// Temporary failures are not held against the address.
			auth.err, auth.tokens["tok-read"] = nil, principal(ScopeRead)
			answer(t, post(h, pingBody, map[string]string{"Authorization": "Bearer tok-read"}), http.StatusOK).wantResult(t)
		})
	}
}

func TestHTTPFailedTokensAreThrottledPerAddress(t *testing.T) {
	const (
		tooMany     = "Too many requests with a missing or wrong API token came from this address."
		tooManyHint = "Wait a minute, then check the token in the client's configuration."
	)
	f := newFixture(t, nil)
	h, auth := newHTTP(t, f, nil)
	send := func(authorization, addr string) *httptest.ResponseRecorder {
		req := newRequest(http.MethodPost, pingBody, map[string]string{"Authorization": authorization})
		if addr != "" {
			req.RemoteAddr = addr
		}
		return serve(h, req)
	}
	throttled := func(rec *httptest.ResponseRecorder, retry string) {
		t.Helper()
		wantMessage(t, answer(t, rec, http.StatusTooManyRequests), codeInvalidRequest, tooMany, tooManyHint)
		wantHeaders(t, rec, map[string]string{"Retry-After": retry})
	}
	for i := range failedAuthPerMinute {
		if rec := send("Bearer tok-wrong", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: HTTP %d", i+1, rec.Code)
		}
	}
	before := auth.calls()
	throttled(send("Bearer tok-read", ""), "6")
	if auth.calls() != before {
		t.Error("a throttled request's token was checked")
	}
	// Other addresses are not affected, and missing tokens count as well.
	answer(t, send("Bearer tok-read", "198.51.100.7:4000"), http.StatusOK).wantResult(t)
	for range failedAuthPerMinute {
		send("", "[2001:db8::1]:443")
	}
	throttled(send("Bearer tok-read", "[2001:db8::1]:443"), "6")

	f.clock.add(5 * time.Second)
	throttled(send("Bearer tok-read", ""), "1")
	f.clock.add(time.Second)
	answer(t, send("Bearer tok-read", ""), http.StatusOK).wantResult(t)
	if rec := send("Bearer tok-wrong", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("HTTP %d", rec.Code)
	}
	throttled(send("Bearer tok-read", ""), "6")
}

func TestHTTPLogsHoldNoSecrets(t *testing.T) {
	f := newFixture(t, nil)
	h, _ := newHTTP(t, f, nil)
	c := newHTTPClient(t, h, "tok-manage", version20251125)
	for _, args := range []map[string]any{{"text": "hunter2"}, {"text": "hunter2", "times": 9}} {
		c.call(t, "tools/call", map[string]any{"name": "echo", "arguments": args}).wantResult(t)
	}
	c.call(t, "tools/call", map[string]any{"name": "restart_server", "arguments": map[string]any{"server": "hunter2"}}).wantResult(t)
	c.call(t, "tools/call", map[string]any{"name": "run_console_command", "arguments": map[string]any{"command": "op hunter2"}}).wantResult(t)
	post(h, pingBody, map[string]string{"Authorization": "Bearer tok-wrong-hunter2"})
	logs := f.logs.String()
	for _, secret := range []string{"hunter2", "tok-", c.session} {
		if strings.Contains(logs, secret) {
			t.Errorf("the logs contain %q:\n%s", secret, logs)
		}
	}
	for _, want := range []string{"mcp: session started", "tool=restart_server", "outcome=invalid_arguments", "outcome=denied", "mcp: rejected an API token"} {
		if !strings.Contains(logs, want) {
			t.Errorf("the logs lack %q:\n%s", want, logs)
		}
	}
}

func TestAcceptsJSON(t *testing.T) {
	for _, tc := range []struct {
		values []string
		want   bool
	}{
		{nil, true},
		{[]string{"application/json"}, true},
		{[]string{"application/json, text/event-stream"}, true},
		{[]string{"text/event-stream", "application/json"}, true},
		{[]string{"*/*"}, true},
		{[]string{"application/*"}, true},
		{[]string{"Application/JSON"}, true},
		{[]string{"application/json;q=0.5"}, true},
		{[]string{"application/*;q=0, application/json"}, true},
		{[]string{"text/event-stream"}, false},
		{[]string{"text/html, text/plain"}, false},
		{[]string{"application/json;q=0"}, false},
		{[]string{"*/*, application/json;q=0"}, false},
		{[]string{"application/json;q=0, */*"}, false},
		{[]string{""}, false},
	} {
		if got := acceptsJSON(tc.values); got != tc.want {
			t.Errorf("acceptsJSON(%q) = %v, want %v", tc.values, got, tc.want)
		}
	}
}

func TestIsJSON(t *testing.T) {
	for _, tc := range []struct {
		contentType string
		want        bool
	}{
		{"application/json", true},
		{"application/json; charset=utf-8", true},
		{"application/json; charset=UTF-8", true},
		{"Application/JSON", true},
		{"application/json; charset=iso-8859-1", false},
		{"application/json-patch+json", false},
		{"text/plain", false},
		{"application/json; charset", false},
		{"", false},
	} {
		if got := isJSON(tc.contentType); got != tc.want {
			t.Errorf("isJSON(%q) = %v, want %v", tc.contentType, got, tc.want)
		}
	}
}

func TestNormalizeOrigin(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"https://panel.example.com", "https://panel.example.com", true},
		{"https://panel.example.com:443", "https://panel.example.com", true},
		{"http://panel.example.com:80", "http://panel.example.com", true},
		{"https://panel.example.com:80", "https://panel.example.com:80", true},
		{"HTTPS://Panel.Example.COM:8443", "https://panel.example.com:8443", true},
		{"https://panel.example.com/", "https://panel.example.com", true},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080", true},
		{"http://[::1]:3000", "http://[::1]:3000", true},
		{"http://[::1]", "http://[::1]", true},
		{"null", "", false},
		{"", "", false},
		{"*", "", false},
		{"panel.example.com", "", false},
		{"ftp://panel.example.com", "", false},
		{"https://user@panel.example.com", "", false},
		{"https://panel.example.com/mcp", "", false},
		{"https://panel.example.com?x=1", "", false},
		{"https://panel.example.com#top", "", false},
		{"https://", "", false},
		{"https:panel.example.com", "", false},
	} {
		if got, ok := normalizeOrigin(tc.in); got != tc.want || ok != tc.ok {
			t.Errorf("normalizeOrigin(%q) = %q, %v, want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestDecodeHeaderValue(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"echo", "echo", true},
		{"a b\tc", "a b\tc", true},
		{"=?base64?ZWNobw==?=", "echo", true},
		{"=?base64?ZWNobw?=", "echo", true},
		{"=?base64?w6ljaG8=?=", "écho", true},
		{"=?base64??=", "", true},
		// Too short for the Base64 form, so plain ASCII.
		{"=?base64?", "=?base64?", true},
		{"=?base64?!!!?=", "", false},
		{"=?base64?ZWNobw=?=", "", false},
		{"écho", "", false},
		{"ech\x00o", "", false},
		{"ech\x7fo", "", false},
		{"line\nbreak", "", false},
	} {
		if got, ok := decodeHeaderValue(tc.in); got != tc.want || ok != tc.ok {
			t.Errorf("decodeHeaderValue(%q) = %q, %v, want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestBearerToken(t *testing.T) {
	long := strings.Repeat("a", maxTokenBytes+1)
	for _, tc := range []struct {
		values              []string
		token               string
		present, wellFormed bool
	}{
		{nil, "", false, false},
		{[]string{"Bearer abc.DEF-123_~+/="}, "abc.DEF-123_~+/=", true, true},
		{[]string{"bearer abc"}, "abc", true, true},
		{[]string{"  BEARER   abc  "}, "abc", true, true},
		{[]string{"Basic dXNlcjpwYXNz"}, "", false, false},
		{[]string{"Bearerabc"}, "", false, false},
		{[]string{"Bearer"}, "", true, false},
		{[]string{"Bearer "}, "", true, false},
		{[]string{"Bearer ==="}, "===", true, false},
		{[]string{"Bearer ab=c"}, "ab=c", true, false},
		{[]string{"Bearer a b"}, "a b", true, false},
		{[]string{`Bearer "abc"`}, `"abc"`, true, false},
		{[]string{"Bearer " + long[1:]}, long[1:], true, true},
		{[]string{"Bearer " + long}, long, true, false},
		{[]string{"Bearer abc", "Bearer abc"}, "abc", true, false},
		{[]string{"Basic dXNlcjpwYXNz", "Bearer abc"}, "", true, false},
	} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		for _, v := range tc.values {
			req.Header.Add("Authorization", v)
		}
		token, present, wellFormed := bearerToken(req)
		if token != tc.token || present != tc.present || wellFormed != tc.wellFormed {
			t.Errorf("bearerToken(%q) = %q, %v, %v, want %q, %v, %v",
				tc.values, token, present, wellFormed, tc.token, tc.present, tc.wellFormed)
		}
	}
}

func TestSizeText(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{
		{1, "1 byte"},
		{100, "100 bytes"},
		{1024, "1 KiB"},
		{1500, "1500 bytes"},
		{256 << 10, "256 KiB"},
	} {
		if got := sizeText(tc.n); got != tc.want {
			t.Errorf("sizeText(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestClientIP(t *testing.T) {
	for _, tc := range []struct{ addr, want string }{
		{"192.0.2.1:1234", "192.0.2.1"},
		{"[2001:db8::1]:443", "2001:db8::1"},
		{"@", "@"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.RemoteAddr = tc.addr
		if got := clientIP(req); got != tc.want {
			t.Errorf("clientIP(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}
