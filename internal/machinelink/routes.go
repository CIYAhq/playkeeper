package machinelink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode"
)

// ActorHeader carries who made a request: the account, or schedule:<id>.
// The agent reads the same header.
const ActorHeader = "X-Playkeeper-Actor"

// Route is one agent request the dashboard may send a machine. The list
// is the agent's own route table, so the dashboard can ask a machine for
// exactly what it can ask its own agent, and nothing else.
type Route struct {
	Method string
	// Pattern is an http.ServeMux path pattern, such as
	// /v1/servers/{id}/start. It may end in a wildcard for the rest of the
	// path, such as /v1/servers/{id}/map/{rest...}; a request still matches
	// only as a clean path.
	Pattern string
	// Stream marks requests whose bodies are large or open-ended (log
	// streams, backup downloads, uploads). They have no time or size
	// limit of their own; a dead link still ends them.
	Stream bool
}

type allowlist struct {
	mux    *http.ServeMux
	routes map[string]Route
}

func newAllowlist(routes []Route) (a *allowlist, err error) {
	if len(routes) == 0 {
		return nil, errors.New("machinelink: no routes are allowed")
	}
	a = &allowlist{mux: http.NewServeMux(), routes: make(map[string]Route, len(routes))}
	defer func() {
		if v := recover(); v != nil {
			a, err = nil, fmt.Errorf("machinelink: bad route: %v", v)
		}
	}()
	for _, r := range routes {
		switch r.Method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			return nil, fmt.Errorf("machinelink: route %s %s: method not allowed", r.Method, r.Pattern)
		}
		p := r.Pattern
		if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "/_link/") || strings.HasSuffix(p, "/") ||
			strings.ContainsAny(p, " \t?#%") || path.Clean(p) != p {
			return nil, fmt.Errorf("machinelink: route %s %s: pattern not allowed", r.Method, p)
		}
		key := r.Method + " " + p
		a.mux.Handle(key, http.NotFoundHandler())
		a.routes[key] = r
	}
	return a, nil
}

// match finds the route for a request. Only clean paths match, the way
// the agent's router sees them: no dot segments, doubled or trailing
// slashes, or escaped slashes.
func (a *allowlist) match(method string, u *url.URL) (Route, bool) {
	p := u.Path
	if u.RawPath != "" || p == "" || p[0] != '/' || path.Clean(p) != p || strings.HasPrefix(p, "/_link/") {
		return Route{}, false
	}
	req := &http.Request{Method: method, URL: &url.URL{Path: p}, Host: "machine"}
	_, pattern := a.mux.Handler(req)
	r, ok := a.routes[pattern]
	if !ok || (r.Method != method && !(r.Method == http.MethodGet && method == http.MethodHead)) {
		return Route{}, false
	}
	return r, true
}

func mutating(method string) bool { return method != http.MethodGet && method != http.MethodHead }

type actorKey struct{}

// WithActor returns a context whose requests through a machine's
// transport are made by actor, unless they set ActorHeader themselves.
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFrom is the actor WithActor put in ctx, or "".
func ActorFrom(ctx context.Context) string {
	a, _ := ctx.Value(actorKey{}).(string)
	return a
}

func actorOf(r *http.Request) string {
	if a := r.Header.Get(ActorHeader); a != "" {
		return a
	}
	return ActorFrom(r.Context())
}

// cleanActor applies the agent's rule for actor names: at most 64 bytes
// of printable text.
func cleanActor(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 64 {
		return "", false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return "", false
		}
	}
	return s, true
}

// hopHeaders belong to one connection, or must never cross to another
// machine: browser credentials stay with the dashboard.
var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade", "Cookie", "Authorization", "Proxy-Authorization"}

func stripHeaders(h http.Header) {
	for _, f := range h.Values("Connection") {
		for _, name := range strings.Split(f, ",") {
			if name = strings.TrimSpace(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, name := range hopHeaders {
		h.Del(name)
	}
}

// apiError is the agent's error body (api.Error), so the panel reads a
// refusal from the link like one from the agent.
type apiError struct {
	Error  string            `json:"error"`
	Code   string            `json:"code"`
	Hint   string            `json:"hint,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

func writeAPIError(w http.ResponseWriter, status int, e *Error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(apiError{Error: e.Msg, Code: e.Code, Hint: e.Hint, Params: e.Params})
}
