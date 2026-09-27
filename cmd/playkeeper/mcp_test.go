package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/config"
)

// Finding F: a sudo rule for "playkeeper mcp" must not let its user choose
// the config, and with it the socket and data the tools use, or whom the
// logs name.
func TestMCPAsRootTakesNoArgumentsAndNoEnvironment(t *testing.T) {
	var asked []string
	getenv := func(env map[string]string) func(string) string {
		return func(k string) string {
			asked = append(asked, k)
			return env[k]
		}
	}
	c, err := mcpCallerFor(nil, 0, getenv(map[string]string{"SUDO_USER": "alice", "PLAYKEEPER_CONFIG": "/tmp/evil.json"}), "root")
	if err != nil || !c.root || c.configPath != config.DefaultPath || c.actor != "cli:alice" {
		t.Fatalf("sudo playkeeper mcp: %+v, %v", c, err)
	}
	if !slices.Equal(asked, []string{"SUDO_USER"}) {
		t.Fatalf("read from the environment: %v", asked)
	}
	for _, args := range [][]string{{"--config", "/tmp/evil.json"}, {"--config=/tmp/evil.json"}, {"-config", "/tmp/evil.json"}, {"--help"}, {"serve"}, {""}} {
		if c, err := mcpCallerFor(args, 0, getenv(nil), "root"); err == nil || !strings.Contains(err.Error(), "no arguments") {
			t.Errorf("as root with %q: %+v, %v", args, c, err)
		}
	}
	for sudoUser, want := range map[string]string{
		"":                      "cli:root",
		"deploy-bot":            "cli:deploy-bot",
		"bob smith":             "cli:root",
		"eve\nforged line":      "cli:root",
		"../../etc":             "cli:root",
		strings.Repeat("a", 33): "cli:root",
	} {
		if c, _ := mcpCallerFor(nil, 0, getenv(map[string]string{"SUDO_USER": sudoUser}), "root"); c.actor != want {
			t.Errorf("SUDO_USER %q acts as %q, want %q", sudoUser, c.actor, want)
		}
	}
}

func TestMCPWithoutRootTakesOnlyADevConfig(t *testing.T) {
	none := func(string) string { return "" }
	for _, args := range [][]string{nil, {"--config"}, {"extra"}, {"--config", "a.json", "b"}} {
		if _, err := mcpCallerFor(args, 1000, none, "alice"); err == nil || !strings.Contains(err.Error(), "sudo playkeeper mcp") {
			t.Errorf("without root, %q: %v", args, err)
		}
	}
	dir := t.TempDir()
	for _, dev := range []bool{false, true} {
		cfg := config.Default()
		cfg.Dev = dev
		path := filepath.Join(dir, fmt.Sprint(dev, ".json"))
		if err := cfg.Save(path); err != nil {
			t.Fatal(err)
		}
		c, err := mcpCallerFor([]string{"--config", path}, 1000, none, "alice")
		if err != nil || c.root || c.actor != "cli:alice" {
			t.Fatalf("without root, with a config: %+v, %v", c, err)
		}
		if _, err := mcpConfig(c); (err == nil) != dev {
			t.Errorf("a config with dev %v: %v", dev, err)
		}
	}
}

type fakeAgent struct {
	mu     sync.Mutex
	bodies map[string]string
}

// startFakeAgent serves a few agent routes on a Unix socket.
func startFakeAgent(t *testing.T) (string, *fakeAgent) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	replies := map[string]string{
		"GET /v1/servers": `[{"id":"abcdefghjk","slug":"survival","name":"Survival","phase":"online","desired":"running","reachable":true}]`,
		"POST /v1/servers/abcdefghjk/backups": `{"id":"0123456789abcdef","serverId":"abcdefghjk","kind":"backup","status":"running",` +
			`"startedAt":"2026-09-24T12:00:00Z"}`,
	}
	fa := &fakeAgent{bodies: map[string]string{}}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		body, _ := io.ReadAll(r.Body)
		fa.mu.Lock()
		fa.bodies[key] = string(body)
		fa.mu.Unlock()
		reply, ok := replies[key]
		if !ok {
			reply = `{"ok":true}`
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, reply)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return socket, fa
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func TestMCPOverStdioActsAsTheAccountThatRanSudo(t *testing.T) {
	socket, fa := startFakeAgent(t)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var logs lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- serveMCP(context.Background(), socket, "cli:alice", inR, outW, slog.New(slog.NewTextHandler(&logs, nil)))
		outW.Close()
	}()
	lines := bufio.NewScanner(outR)
	send := func(msg string) {
		t.Helper()
		if _, err := io.WriteString(inW, msg+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	ask := func(msg string) map[string]any {
		t.Helper()
		send(msg)
		if !lines.Scan() {
			t.Fatalf("no answer to %s: %v", msg, lines.Err())
		}
		var m map[string]any
		if err := json.Unmarshal(lines.Bytes(), &m); err != nil {
			t.Fatalf("stdout has more than JSON-RPC: %q", lines.Text())
		}
		if _, ok := m["result"].(map[string]any); !ok {
			t.Fatalf("%s: %v", msg, m)
		}
		return m["result"].(map[string]any)
	}
	ask(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	var names []string
	for _, tool := range ask(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)["tools"].([]any) {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	if !slices.Contains(names, "run_console_command") || !slices.Contains(names, "create_backup") {
		t.Fatalf("root's tools: %v", names)
	}
	res := ask(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_backup","arguments":{"server":"survival"}}}`)
	if text := fmt.Sprint(res["content"]); res["isError"] == true || !strings.Contains(text, "Backing up Survival") {
		t.Fatalf("create_backup: %v", res)
	}
	fa.mu.Lock()
	body := fa.bodies["POST /v1/servers/abcdefghjk/backups"]
	fa.mu.Unlock()
	if !strings.Contains(body, `"actor":"cli:alice"`) {
		t.Fatalf("the agent heard of the backup as %s", body)
	}
	inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("at the end of the input: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("playkeeper mcp went on after its input ended")
	}
}
