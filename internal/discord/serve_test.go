package discord

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
)

// TestServeFakeDiscord runs the fake Discord as a small local server for the
// end-to-end checks, when PLAYKEEPER_FAKE_DISCORD_ADDR is a loopback address
// and port such as 127.0.0.1:8090. Start the agent with
// PLAYKEEPER_E2E_DISCORD_URL_UNSAFE=http://127.0.0.1:8090 and connect the
// webhook URL this prints (its token is the tests' made-up one).
// GET /_fake/messages lists what arrived, without the token, and
// POST /_fake/stop ends the test, which fails if any request broke the rules
// the fake checks. It also ends after PLAYKEEPER_FAKE_DISCORD_FOR (20m by
// default); run it with -timeout 0.
func TestServeFakeDiscord(t *testing.T) {
	addr := os.Getenv("PLAYKEEPER_FAKE_DISCORD_ADDR")
	if addr == "" {
		t.Skip("set PLAYKEEPER_FAKE_DISCORD_ADDR=127.0.0.1:8090 to run the fake Discord for the end-to-end checks")
	}
	ap, err := netip.ParseAddrPort(addr)
	if err != nil || !ap.Addr().IsLoopback() {
		t.Fatalf("PLAYKEEPER_FAKE_DISCORD_ADDR must be a loopback address and port, not %q", addr)
	}
	life := 20 * time.Minute
	if v := os.Getenv("PLAYKEEPER_FAKE_DISCORD_FOR"); v != "" {
		if life, err = time.ParseDuration(v); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeDiscord{t: t, got: make(chan fakeRequest, 100), tokens: map[string]string{testID: testToken}, messages: map[string]bool{}}
	stop := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_fake/messages", func(w http.ResponseWriter, r *http.Request) {
		type seen struct {
			Method string  `json:"method"`
			Path   string  `json:"path"`
			Embeds []embed `json:"embeds"`
		}
		f.mu.Lock()
		out := []seen{}
		for _, req := range f.requests {
			out = append(out, seen{req.Method, strings.ReplaceAll(req.Path, testToken, "<token>"), req.Msg.Embeds})
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /_fake/stop", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		select {
		case <-stop:
		default:
			close(stop)
		}
	})
	mux.HandleFunc("/", f.serve)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	fmt.Printf("fake Discord listening on http://%s\nwebhook URL: https://discord.com/api/webhooks/%s/%s\n", addr, testID, testToken)
	select {
	case <-stop:
	case <-time.After(life):
	}
	srv.Close()
	f.mu.Lock()
	t.Logf("%d requests reached the fake Discord", len(f.requests))
	f.mu.Unlock()
}
