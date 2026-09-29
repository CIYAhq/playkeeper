package panel

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/agent"
	"github.com/CIYAhq/playkeeper/internal/config"
)

// realAgent is an agent of its own, reached through its handler alone. It
// has no servers, and no names service or certificate authority answers
// it, so nothing leaves the machine.
func realAgent(t *testing.T) http.Handler {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.SocketPath = filepath.Join(dir, "agent.sock")
	cfg.NamesURL = "http://127.0.0.1:1"
	cfg.ACMEDirectoryURL = "http://127.0.0.1:1/directory"
	a, err := agent.New(agent.Options{Config: cfg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), AllowedUIDs: []uint32{uint32(os.Getuid())}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a.HandlerForTest()
}

// patternMatches reports whether an agent path is one of pattern's, whose
// {names} stand for one path segment each.
func patternMatches(pattern, path string) bool {
	want, got := strings.Split(pattern, "/"), strings.Split(path, "/")
	if len(want) != len(got) {
		return false
	}
	for i, w := range want {
		if strings.HasPrefix(w, "{") && strings.HasSuffix(w, "}") {
			if got[i] == "" {
				return false
			}
		} else if w != got[i] {
			return false
		}
	}
	return true
}

// Every agent request the panel stamps with panelHost, the host the
// dashboard was opened with, reaches an agent handler that takes it. In
// 0.4.5 the switch for an address for each server didn't: its request type
// had no panelHost, the agent refuses fields it doesn't know, and turning
// it on failed with 'unknown field "panelHost"'. Each route goes through the
// real panel's forwarding, and what reaches the agent goes to a real
// agent's handler.
func TestEveryRouteThatStampsPanelHostReachesAnAgentThatTakesIt(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	mid := e.localMachine(t)
	real := realAgent(t)
	routes := e.srv.Routes()
	reached := map[string]bool{}
	e.srv.hostStamped.Range(func(k, _ any) bool {
		reached[k.(string)] = false
		return true
	})
	if len(reached) == 0 {
		t.Fatal("the panel stamps panelHost into no agent route, so the list of them isn't kept")
	}
	for _, rt := range routes {
		if !rt.NeedsSession() || strings.HasPrefix(rt.Pattern, "/api/auth/") {
			continue
		}
		e.agent.mu.Lock()
		n := len(e.agent.reqs)
		e.agent.mu.Unlock()
		e.do(t, rt.Method, samplePath(strings.ReplaceAll(rt.Pattern, "{mid}", mid)), `{}`, auth(cookie, csrf))
		e.agent.mu.Lock()
		sent := slices.Clone(e.agent.reqs[n:])
		e.agent.mu.Unlock()
		for _, req := range sent {
			if _, inBody := req.body["panelHost"]; !inBody && !req.query.Has("panelHost") {
				continue
			}
			key := ""
			for k := range reached {
				if method, pattern, _ := strings.Cut(k, " "); method == req.method && patternMatches(pattern, req.path) {
					key = k
				}
			}
			if key == "" {
				t.Errorf("%s %s stamped panelHost into %s %s, which isn't on the panel's list of them", rt.Method, rt.Pattern, req.method, req.path)
				continue
			}
			reached[key] = true
			var body io.Reader
			if req.method != http.MethodGet {
				raw, _ := json.Marshal(req.body)
				body = bytes.NewReader(raw)
			}
			r := httptest.NewRequest(req.method, req.path+"?"+req.query.Encode(), body)
			r.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			real.ServeHTTP(rec, r)
			var out struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			if rec.Code == http.StatusNotFound || strings.Contains(out.Error, "unknown field") {
				t.Errorf("%s %s: the agent refused what the panel sent to %s %s: %d %s", rt.Method, rt.Pattern, req.method, req.path, rec.Code, strings.TrimSpace(rec.Body.String()))
			}
		}
	}
	for k, ok := range reached {
		if !ok {
			t.Errorf("the panel stamps panelHost into %s, but no route reached it with {}", k)
		}
	}
}
