package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agent"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/install"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
)

func TestJoinTakesTheDashboardsCommandEitherWay(t *testing.T) {
	const fp = "Z287KN4CDZD0Z8A4XXJA514NKG"
	for _, args := range [][]string{
		{"alex.playkeeper.io:8443", "--code", "7KQ2-M9XD", "--fingerprint", fp, "--name", "home-server"},
		{"--code", "7KQ2-M9XD", "--fingerprint", fp, "--name", "home-server", "alex.playkeeper.io:8443"},
	} {
		j, err := parseJoinArgs(args)
		if err != nil || j.address != "alex.playkeeper.io:8443" || j.code != "7KQ2-M9XD" || j.fingerprint != fp || j.name != "home-server" || j.config != config.DefaultPath {
			t.Errorf("%q: %+v, %v", args, j, err)
		}
	}
	for args, want := range map[string]string{
		"alex.playkeeper.io:8443 --code 7KQ2-M9XD":                                "usage: sudo playkeeper join ADDRESS",
		"--code 7KQ2-M9XD --fingerprint " + fp:                                    "usage: sudo playkeeper join ADDRESS",
		"alex.playkeeper.io:8443 --code 7KQ2 --fingerprint " + fp:                 "Join codes are 8 letters and digits",
		"alex.playkeeper.io:8443 --code 7KQ2-M9XD --fingerprint Z287KN4C":         "fingerprint should be 26 letters and digits",
		"alex.playkeeper.io:8443 extra --code 7KQ2-M9XD --fingerprint " + fp:      `unexpected argument "extra"`,
		"alex.playkeeper.io:8443 --code 7KQ2-M9XD --fingerprint " + fp + " --yes": "flag provided but not defined: -yes",
	} {
		if _, err := parseJoinArgs(strings.Fields(args)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", args, err, want)
		}
	}
	if got := groupFingerprint(fp); got != "Z287 KN4C DZD0 Z8A4 XXJA 514N KG" {
		t.Errorf("grouped fingerprint %q", got)
	}
}

// waitUntil polls cond for up to 15 seconds.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting until %s", what)
}

func TestADevMachineJoinsServesTheDashboardAndForgetsItWhenRemoved(t *testing.T) {
	ctx := context.Background()
	socket, fa := startFakeAgent(t)
	id, err := machinelink.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	store := machinelink.NewMemoryStore()
	hub, err := machinelink.NewHub(machinelink.HubOptions{Identity: id, Store: store, Routes: agent.LinkRoutes(), Version: "0.4.0"})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go hub.Serve(ln)
	t.Cleanup(func() { hub.Close() })
	code, _, err := hub.NewJoinCode(ctx, "alex")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Dev, cfg.DataDir, cfg.SocketPath = true, dir, socket
	configPath := filepath.Join(dir, "config.json")
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	j, err := parseJoinArgs([]string{ln.Addr().String(), "--code", strings.ToLower(code), "--fingerprint", groupFingerprint(hub.Fingerprint()), "--name", "home-server", "--config", configPath})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := join(ctx, &out, cfg, j); err != nil {
		t.Fatalf("join: %v", err)
	}
	machines, _ := store.Machines(ctx)
	if len(machines) != 1 {
		t.Fatalf("the dashboard has %+v", machines)
	}
	m := machines[0]
	for _, want := range []string{"This machine joined the dashboard at " + ln.Addr().String() + " as home-server.", groupFingerprint(m.Fingerprint()), "playkeeper link --config " + configPath} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("join does not say %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), code) {
		t.Errorf("join repeated the code:\n%s", out.String())
	}

	linkCtx, stop := context.WithCancel(ctx)
	defer stop()
	var logs lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- serveLink(linkCtx, cfg, slog.New(slog.NewTextHandler(&logs, nil)), 20*time.Millisecond)
	}()
	statusFile := install.LinkStatusFile(cfg)
	waitUntil(t, "the link says it is connected", func() bool {
		var st machinelink.LinkStatus
		b, err := os.ReadFile(statusFile)
		return err == nil && json.Unmarshal(b, &st) == nil && st.State == machinelink.LinkConnected && hub.Connected(m.ID)
	})
	var status bytes.Buffer
	writeLinkStatus(&status, cfg, configPath, time.Now())
	for _, want := range []string{"This machine is home-server in the dashboard at " + ln.Addr().String() + ".", "Link:         connected since", groupFingerprint(hub.Fingerprint())} {
		if !strings.Contains(status.String(), want) {
			t.Errorf("status does not say %q:\n%s", want, status.String())
		}
	}

	req, _ := http.NewRequestWithContext(machinelink.WithActor(ctx, "user:alex"), "POST", "http://home-server/v1/servers/abcdefghjk/backups", strings.NewReader(`{}`))
	resp, err := (&http.Client{Transport: hub.Transport(m.ID)}).Do(req)
	if err != nil {
		t.Fatalf("a request from the dashboard: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "0123456789abcdef") {
		t.Fatalf("the agent answered %d: %s", resp.StatusCode, body)
	}
	fa.mu.Lock()
	heard := fa.bodies["POST /v1/servers/abcdefghjk/backups"]
	fa.mu.Unlock()
	if heard != "{}" {
		t.Fatalf("the agent heard %q", heard)
	}
	waitUntil(t, "the link logs the request", func() bool {
		logs.mu.Lock()
		defer logs.mu.Unlock()
		s := logs.b.String()
		return strings.Contains(s, "dashboard request") && strings.Contains(s, "actor=user:alex") && strings.Contains(s, "status=200")
	})

	if err := hub.Remove(ctx, m.ID, "user:alex"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the link ended with %v once removed", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the link kept running after the dashboard removed the machine")
	}
	for _, p := range []string{cfg.LinkDashboardPath(), cfg.LinkKeyPath(), statusFile} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("a removed machine kept %s", p)
		}
	}
	if err := serveLink(ctx, cfg, slog.New(slog.DiscardHandler), time.Second); err == nil || !strings.Contains(err.Error(), "isn't connected to a dashboard") {
		t.Fatalf("the link of a machine that isn't joined: %v", err)
	}
	out.Reset()
	if err := leave(ctx, &out, cfg, false); err != nil || !strings.Contains(out.String(), "isn't connected to another dashboard") {
		t.Fatalf("leaving once removed: %v\n%s", err, out.String())
	}
}
