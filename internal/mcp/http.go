package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	headerSessionID       = "Mcp-Session-Id"
	headerProtocolVersion = "Mcp-Protocol-Version"
	headerMethod          = "Mcp-Method"
	headerName            = "Mcp-Name"

	maxTokenBytes       = 512
	failedAuthPerMinute = 10
)

// Authenticator checks the bearer token on an HTTP request.
type Authenticator interface {
	// Authenticate returns the principal that token belongs to. token comes
	// from the Authorization header and is already known to be well-formed.
	// Return ErrInvalidToken for a token that is unknown, expired or
	// revoked; any other error is reported to the client as a temporary
	// failure. Errors are logged, so they must not contain the token.
	Authenticate(r *http.Request, token string) (Principal, error)
}

// AuthenticatorFunc adapts a function to the Authenticator interface.
type AuthenticatorFunc func(r *http.Request, token string) (Principal, error)

// Authenticate calls f(r, token).
func (f AuthenticatorFunc) Authenticate(r *http.Request, token string) (Principal, error) {
	return f(r, token)
}

// ErrInvalidToken is what an Authenticator returns for a token it does not
// accept.
var ErrInvalidToken = errors.New("mcp: invalid token")

// HTTPOptions configures the Streamable HTTP transport.
type HTTPOptions struct {
	// Authenticator checks every request's bearer token. It is required.
	Authenticator Authenticator
	// AllowedOrigins lists the web origins, such as
	// "https://panel.example.com:8443", whose pages may call the endpoint
	// from a browser. Requests without an Origin header, which is how
	// desktop and command-line clients connect, are always allowed.
	AllowedOrigins []string
	// MaxBodyBytes caps a request body; the default is the server's
	// MaxMessageBytes.
	MaxBodyBytes int64
	// SessionIdleTimeout ends a legacy session that has not been used for
	// this long; default 12 hours. SessionMaxAge ends one this long after it
	// began; default 7 days.
	SessionIdleTimeout time.Duration
	SessionMaxAge      time.Duration
	// MaxSessions caps legacy sessions in total, default 1000, and
	// MaxSessionsPerPrincipal per principal, default 16. At a cap, the least
	// recently used session is ended to make room.
	MaxSessions             int
	MaxSessionsPerPrincipal int
}

// HTTPHandler serves the Streamable HTTP transport on one endpoint, such as
// /mcp. Requests that carry 2026-07-28 metadata are served statelessly;
// initialize starts a legacy session identified by the Mcp-Session-Id
// header. Responses are always single JSON objects: the server sends no
// notifications, so it never needs an event stream.
type HTTPHandler struct {
	srv      *Server
	auth     Authenticator
	origins  map[string]bool
	maxBody  int64
	sessions *sessionStore
	failures *limiter
	ctx      context.Context
	cancel   context.CancelFunc
}

// NewHTTPHandler returns the HTTP transport for srv.
func NewHTTPHandler(srv *Server, opts HTTPOptions) (*HTTPHandler, error) {
	if srv == nil {
		return nil, errors.New("mcp: NewHTTPHandler needs a server")
	}
	if opts.Authenticator == nil {
		return nil, errors.New("mcp: HTTPOptions.Authenticator is required")
	}
	origins := map[string]bool{}
	for _, o := range opts.AllowedOrigins {
		n, ok := normalizeOrigin(o)
		if !ok {
			return nil, fmt.Errorf("mcp: allowed origin %q must be a scheme and host, such as https://panel.example.com:8443", o)
		}
		origins[n] = true
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = int64(srv.maxMessage)
	}
	if opts.SessionIdleTimeout <= 0 {
		opts.SessionIdleTimeout = 12 * time.Hour
	}
	if opts.SessionMaxAge <= 0 {
		opts.SessionMaxAge = 7 * 24 * time.Hour
	}
	if opts.MaxSessions <= 0 {
		opts.MaxSessions = 1000
	}
	if opts.MaxSessionsPerPrincipal <= 0 {
		opts.MaxSessionsPerPrincipal = 16
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &HTTPHandler{
		srv: srv, auth: opts.Authenticator, origins: origins, maxBody: opts.MaxBodyBytes,
		sessions: newSessionStore(ctx, srv.now, opts.SessionIdleTimeout, opts.SessionMaxAge,
			opts.MaxSessions, opts.MaxSessionsPerPrincipal),
		failures: newLimiter(failedAuthPerMinute, time.Minute, srv.now),
		ctx:      ctx, cancel: cancel,
	}, nil
}

// CloseSessions ends the legacy sessions of a principal, for example when its
// token is revoked, and cancels their requests in progress. It returns the
// number of sessions ended.
func (h *HTTPHandler) CloseSessions(principalID string) int {
	return h.sessions.removePrincipal(principalID)
}

// Close ends all sessions and cancels every request in progress. The handler
// must not be used afterwards.
func (h *HTTPHandler) Close() {
	h.cancel()
	h.sessions.closeAll()
}

// ServeHTTP answers one request to the MCP endpoint.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if origins := r.Header.Values("Origin"); len(origins) > 0 {
		if len(origins) > 1 || !h.allowedOrigin(origins[0]) {
			h.fail(w, http.StatusForbidden, nil, newError(codeInvalidRequest,
				"Requests from this web page's origin are not allowed.",
				"Connect from an MCP client application, or add the origin to the allowed origins."))
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origins[0])
		w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id, WWW-Authenticate, Retry-After")
		w.Header().Add("Vary", "Origin")
	}
	switch r.Method {
	case http.MethodPost:
		h.post(w, r)
	case http.MethodDelete:
		h.delete(w, r)
	case http.MethodOptions:
		h.options(w, r)
	default:
		w.Header().Set("Allow", "POST, DELETE, OPTIONS")
		h.fail(w, http.StatusMethodNotAllowed, nil, newError(codeInvalidRequest,
			"The MCP endpoint offers no event stream; send each message in a POST request.", ""))
	}
}

func (h *HTTPHandler) allowedOrigin(origin string) bool {
	n, ok := normalizeOrigin(origin)
	return ok && h.origins[n]
}

// normalizeOrigin returns an origin as lowercase scheme://host[:port],
// without a default port. It refuses "null" and anything with a path.
func normalizeOrigin(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, true
}

// options answers CORS preflight requests from allowed origins; the origin
// was checked by ServeHTTP.
func (h *HTTPHandler) options(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "POST, DELETE, OPTIONS")
	if r.Header.Get("Origin") != "" {
		w.Header().Set("Access-Control-Allow-Methods", "POST, DELETE")
		w.Header().Set("Access-Control-Allow-Headers",
			"Authorization, Content-Type, Accept, Mcp-Session-Id, Mcp-Protocol-Version, Mcp-Method, Mcp-Name")
		w.Header().Set("Access-Control-Max-Age", "600")
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTPHandler) delete(w http.ResponseWriter, r *http.Request) {
	p, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	id := r.Header.Get(headerSessionID)
	if id == "" {
		h.fail(w, http.StatusBadRequest, nil, newError(codeInvalidRequest,
			"DELETE needs the Mcp-Session-Id header of the session to end.", ""))
		return
	}
	if !h.sessions.remove(id, p.ID) {
		h.fail(w, http.StatusNotFound, nil, errNoSession())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func errNoSession() *rpcError {
	return newError(codeInvalidRequest, "The session has ended or does not exist.", "Send initialize to start a new session.")
}

// authenticate checks the bearer token and writes the error response when it
// is missing or wrong.
func (h *HTTPHandler) authenticate(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	ip := clientIP(r)
	if ok, wait := h.failures.peek(ip); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		h.fail(w, http.StatusTooManyRequests, nil, newError(codeInvalidRequest,
			"Too many requests with a missing or wrong API token came from this address.",
			"Wait a minute, then check the token in the client's configuration."))
		return Principal{}, false
	}
	token, present, wellFormed := bearerToken(r)
	if !present {
		h.failures.take(ip)
		h.challenge(w, "",
			"This endpoint needs a Playkeeper API token.",
			`Create a token in the Playkeeper panel and send it in the Authorization header as "Bearer <token>".`)
		return Principal{}, false
	}
	var p Principal
	err := ErrInvalidToken
	if wellFormed {
		p, err = h.auth.Authenticate(r, token)
	}
	switch {
	case errors.Is(err, ErrInvalidToken):
		h.failures.take(ip)
		h.srv.log.Info("mcp: rejected an API token", "ip", ip)
		h.challenge(w, "invalid_token",
			"The API token is not valid; it may have been revoked.",
			"Check the token in the client's configuration, or create a new one in the Playkeeper panel.")
		return Principal{}, false
	case err != nil || p.ID == "":
		if err == nil {
			err = errors.New("the principal has no ID")
		}
		h.srv.log.Error("mcp: checking an API token failed", "ip", ip, "err", err)
		w.Header().Set("Retry-After", "5")
		h.fail(w, http.StatusServiceUnavailable, nil, newError(codeInternalError,
			"Playkeeper could not check the API token.", "Try again in a moment."))
		return Principal{}, false
	}
	return p, true
}

func (h *HTTPHandler) challenge(w http.ResponseWriter, code, msg, hint string) {
	v := `Bearer realm="` + h.srv.name + `"`
	if code != "" {
		v += `, error="` + code + `", error_description="The token is unknown, malformed or revoked."`
	}
	w.Header().Set("WWW-Authenticate", v)
	h.fail(w, http.StatusUnauthorized, nil, newError(codeInvalidRequest, msg, hint))
}

// bearerToken reads the token from the Authorization header. present is false
// when there is no bearer credential at all.
func bearerToken(r *http.Request) (token string, present, wellFormed bool) {
	values := r.Header.Values("Authorization")
	if len(values) == 0 {
		return "", false, false
	}
	scheme, rest, _ := strings.Cut(strings.TrimSpace(values[0]), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return "", len(values) > 1, false
	}
	token = strings.TrimSpace(rest)
	return token, true, len(values) == 1 && len(token) <= maxTokenBytes && isToken68(token)
}

// isToken68 reports whether s has the syntax of a bearer token (RFC 6750).
func isToken68(s string) bool {
	body := strings.TrimRight(s, "=")
	if body == "" {
		return false
	}
	for i := 0; i < len(body); i++ {
		switch c := body[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '.', c == '_', c == '~', c == '+', c == '/':
		default:
			return false
		}
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (h *HTTPHandler) post(w http.ResponseWriter, r *http.Request) {
	p, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		h.fail(w, http.StatusUnsupportedMediaType, nil, newError(codeInvalidRequest,
			"The request body must be JSON, sent with Content-Type: application/json.", ""))
		return
	}
	if !acceptsJSON(r.Header.Values("Accept")) {
		h.fail(w, http.StatusNotAcceptable, nil, newError(codeInvalidRequest,
			"The client must accept application/json responses.",
			"Send Accept: application/json, text/event-stream."))
		return
	}
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 || !json.Valid(body) {
		h.fail(w, http.StatusBadRequest, nil, newError(codeParseError, "The request body is not valid JSON.", ""))
		return
	}
	if body[0] == '[' {
		h.batch(w, r, p, body)
		return
	}
	m, errResp := decodeMessage(body)
	if errResp != nil {
		h.writeJSON(w, http.StatusBadRequest, errResp)
		return
	}
	if m == nil {
		h.fail(w, http.StatusBadRequest, nil, newError(codeInvalidRequest, "The notification is not a valid JSON-RPC message.", ""))
		return
	}
	modern, version, e := requestEra(m.params)
	switch {
	case e != nil:
		h.fail(w, http.StatusBadRequest, m.id, e)
	case modern:
		h.modern(w, r, p, m, version)
	case m.kind == kindRequest && m.method == "initialize":
		h.initialize(w, p, m)
	default:
		h.legacy(w, r, p, m)
	}
}

func (h *HTTPHandler) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	tooLarge := newError(codeInvalidRequest, "The request body is larger than "+sizeText(h.maxBody)+".", "")
	if r.ContentLength > h.maxBody {
		h.fail(w, http.StatusRequestEntityTooLarge, nil, tooLarge)
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBody))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		h.fail(w, http.StatusRequestEntityTooLarge, nil, tooLarge)
		return nil, false
	case err != nil:
		h.fail(w, http.StatusBadRequest, nil, newError(codeInvalidRequest, "The request body could not be read.", ""))
		return nil, false
	}
	return body, true
}

func sizeText(n int64) string {
	if n >= 1024 && n%1024 == 0 {
		return strconv.FormatInt(n/1024, 10) + " KiB"
	}
	return plural(int(n), "byte")
}

// isJSON reports whether a Content-Type is JSON in UTF-8.
func isJSON(contentType string) bool {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || mt != "application/json" {
		return false
	}
	cs, ok := params["charset"]
	return !ok || strings.EqualFold(cs, "utf-8")
}

// acceptsJSON reports whether an Accept header allows application/json. No
// header at all accepts anything; the most specific matching range decides,
// so "*/*, application/json;q=0" refuses JSON.
func acceptsJSON(values []string) bool {
	if len(values) == 0 {
		return true
	}
	best, q := -1, 0.0
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			mt, params, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err != nil {
				continue
			}
			rank := -1
			switch mt {
			case "application/json":
				rank = 2
			case "application/*":
				rank = 1
			case "*/*":
				rank = 0
			}
			if rank <= best {
				continue
			}
			best, q = rank, 1
			if s, ok := params["q"]; ok {
				if f, err := strconv.ParseFloat(s, 64); err == nil {
					q = f
				}
			}
		}
	}
	return best >= 0 && q > 0
}

// modern serves a 2026-07-28 request statelessly. The HTTP headers that
// mirror the body must agree with it, so that an intermediary routing on the
// headers and the server acting on the body see the same request.
func (h *HTTPHandler) modern(w http.ResponseWriter, r *http.Request, p Principal, m *message, version string) {
	if m.kind != kindRequest {
		// This revision defines no client notifications over HTTP; accept
		// and ignore them as JSON-RPC requires.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if e := checkHeader(r, headerProtocolVersion, "the protocol version in params._meta", version); e != nil {
		h.fail(w, http.StatusBadRequest, m.id, e)
		return
	}
	c, e := modernCaller(p, version, m.params)
	if e != nil {
		h.fail(w, http.StatusBadRequest, m.id, e)
		return
	}
	if e := checkHeader(r, headerMethod, "the request's method", m.method); e != nil {
		h.fail(w, http.StatusBadRequest, m.id, e)
		return
	}
	if !knownMethod(true, m.method) {
		h.fail(w, http.StatusNotFound, m.id, methodNotFound(m.method))
		return
	}
	if m.method == "tools/call" {
		var name string
		if json.Unmarshal(m.params["name"], &name) == nil {
			if e := checkHeader(r, headerName, "the tool name in the request", name); e != nil {
				h.fail(w, http.StatusBadRequest, m.id, e)
				return
			}
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer context.AfterFunc(h.ctx, cancel)()
	result, e := h.srv.handle(ctx, c, m)
	if e != nil {
		status := http.StatusOK
		if e.Code == codeMethodNotFound {
			status = http.StatusNotFound
		}
		h.fail(w, status, m.id, e)
		return
	}
	h.writeJSON(w, http.StatusOK, reply(m.id, result, nil))
}

// checkHeader requires header to be present once and equal to want. Mcp-Name
// may carry its value in the "=?base64?…?=" form.
func checkHeader(r *http.Request, header, what, want string) *rpcError {
	name := header
	if header == headerProtocolVersion {
		name = "MCP-Protocol-Version"
	}
	values := r.Header.Values(header)
	switch {
	case len(values) == 0:
		return newError(codeHeaderMismatch, "The "+name+" header is missing.", "Requests in protocol 2026-07-28 must mirror "+what+" in it.")
	case len(values) > 1:
		return newError(codeHeaderMismatch, "The "+name+" header appears more than once.", "")
	}
	got, ok := values[0], true
	if header == headerName {
		got, ok = decodeHeaderValue(got)
	}
	if !ok {
		return newError(codeHeaderMismatch, "The "+name+" header is not valid.", "Encode values that are not plain ASCII as =?base64?…?=.")
	}
	if got != want {
		return newError(codeHeaderMismatch, fmt.Sprintf("The %s header %q does not match %s, %q.", name, truncate(got, 128), what, truncate(want, 128)), "")
	}
	return nil
}

// decodeHeaderValue reads a header value that may use the Base64 sentinel
// form of Streamable HTTP. Plain values must be visible ASCII, spaces or
// tabs.
func decodeHeaderValue(v string) (string, bool) {
	const prefix, suffix = "=?base64?", "?="
	if len(v) >= len(prefix)+len(suffix) && strings.HasPrefix(v, prefix) && strings.HasSuffix(v, suffix) {
		enc := v[len(prefix) : len(v)-len(suffix)]
		b, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			if b, err = base64.RawStdEncoding.DecodeString(enc); err != nil {
				return "", false
			}
		}
		return string(b), true
	}
	for i := 0; i < len(v); i++ {
		if c := v[i]; (c < 0x20 || c > 0x7e) && c != '\t' {
			return "", false
		}
	}
	return v, true
}

// initialize starts a legacy session. A session ID the client still sends
// is ignored: a client re-initializing after its session ended gets a new
// one.
func (h *HTTPHandler) initialize(w http.ResponseWriter, p Principal, m *message) {
	version, client, result, e := h.srv.initialize(m.params)
	if e != nil {
		h.fail(w, http.StatusOK, m.id, e)
		return
	}
	s := h.sessions.create(p.ID, version, client)
	h.srv.log.Debug("mcp: session started", "principal", p.ID, "protocol", version, "client", client.Name)
	w.Header().Set(headerSessionID, s.id)
	h.writeJSON(w, http.StatusOK, reply(m.id, result, nil))
}

// session finds the legacy session a request names and checks its protocol
// version header. It writes the error response and returns nil when there
// is no usable session.
func (h *HTTPHandler) session(w http.ResponseWriter, r *http.Request, p Principal, id json.RawMessage) *session {
	s := h.sessions.get(r.Header.Get(headerSessionID), p.ID)
	if s == nil {
		h.fail(w, http.StatusNotFound, id, errNoSession())
		return nil
	}
	if v := r.Header.Get(headerProtocolVersion); v != "" && v != s.version {
		e := unsupportedVersion(v)
		if isModern(v) || isLegacy(v) {
			e = newError(codeInvalidRequest,
				fmt.Sprintf("The MCP-Protocol-Version header says %s, but this session uses %s.", v, s.version),
				"Send the protocol version that initialize returned.")
		}
		h.fail(w, http.StatusBadRequest, id, e)
		return nil
	}
	return s
}

// legacy serves a message that belongs to a session started by initialize.
func (h *HTTPHandler) legacy(w http.ResponseWriter, r *http.Request, p Principal, m *message) {
	if r.Header.Get(headerSessionID) == "" {
		if m.kind == kindRequest && m.method == "ping" {
			h.writeJSON(w, http.StatusOK, reply(m.id, map[string]any{}, nil))
			return
		}
		h.fail(w, http.StatusBadRequest, m.id, errNoContext())
		return
	}
	s := h.session(w, r, p, m.id)
	if s == nil {
		return
	}
	// A session's requests run under the session's context, not the HTTP
	// request's: in legacy revisions a dropped connection is not a
	// cancellation, notifications/cancelled is.
	switch m.kind {
	case kindResponse:
		w.WriteHeader(http.StatusAccepted)
	case kindNotification:
		h.srv.notify(&s.reqs, m)
		w.WriteHeader(http.StatusAccepted)
	default:
		resp := h.srv.call(&s.reqs, h.sessionCaller(p, s), m)
		if resp == nil {
			// The client cancelled the request, so there is no response to
			// send, but the POST still needs an answer.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		h.writeJSON(w, http.StatusOK, resp)
	}
}

// sessionCaller identifies a legacy request. The principal comes from this
// request's token, so scopes are always current.
func (h *HTTPHandler) sessionCaller(p Principal, s *session) caller {
	return caller{principal: p, version: s.version, client: s.client}
}

// batch serves a JSON-RPC batch, which only 2025-03-26 and 2024-11-05
// sessions may send.
func (h *HTTPHandler) batch(w http.ResponseWriter, r *http.Request, p Principal, body []byte) {
	var s *session
	version := ""
	if r.Header.Get(headerSessionID) != "" {
		if s = h.session(w, r, p, nil); s == nil {
			return
		}
		version = s.version
	}
	raws, e := checkBatch(body, version)
	if e != nil {
		h.fail(w, http.StatusBadRequest, nil, e)
		return
	}
	out := h.srv.batch(&s.reqs, h.sessionCaller(p, s), raws)
	if len(out) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

func (h *HTTPHandler) fail(w http.ResponseWriter, status int, id json.RawMessage, e *rpcError) {
	h.writeJSON(w, status, reply(id, nil, e))
}

func (h *HTTPHandler) writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		h.srv.log.Error("mcp: encoding a response", "err", err)
		status = http.StatusInternalServerError
		b, _ = json.Marshal(reply(nil, nil, newError(codeInternalError, "The server could not encode its response.", "")))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
