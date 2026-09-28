package main

import (
	"bytes"
	"crypto/ed25519"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/machinelink"
)

func TestAJoinedMachineLooksForItsDashboardUnderTheNewBase(t *testing.T) {
	for in, want := range map[string]string{
		"siya.playkeeper.io":      "siya.playkeeper.me",
		"siya.playkeeper.io:8443": "siya.playkeeper.me",
		"Siya.PlayKeeper.IO:9443": "siya.playkeeper.me:9443",
	} {
		if got, ok := movedDashboard(in); !ok || got != want {
			t.Errorf("movedDashboard(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"siya.playkeeper.me", "playkeeper.io", "www.playkeeper.io.example.com", "203.0.113.10:8443", "[2001:db8::1]:8443", "play.example.com", "x.playkeeper.io"} {
		if got, ok := movedDashboard(in); ok {
			t.Errorf("movedDashboard(%q) = %q, want no move", in, got)
		}
	}

	path := filepath.Join(t.TempDir(), "dashboard.json")
	d := machinelink.Dashboard{Address: "siya.playkeeper.io", Key: make(ed25519.PublicKey, ed25519.PublicKeySize), MachineID: "abcdefghij", Name: "home-server", JoinedAt: time.Now().UTC()}
	if err := d.Save(path); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	addrs, follow := linkAddresses(&d, path, log)
	if !slices.Equal(addrs, []string{"siya.playkeeper.me", "siya.playkeeper.io"}) {
		t.Fatalf("addresses tried: %v", addrs)
	}
	moved, err := machinelink.ParseAddress("siya.playkeeper.me")
	if err != nil {
		t.Fatal(err)
	}
	follow(moved)
	saved, err := machinelink.LoadDashboard(path)
	if err != nil || saved.Address != "siya.playkeeper.me" || saved.MachineID != d.MachineID || !saved.Key.Equal(d.Key) {
		t.Fatalf("the dashboard file after following: %+v, %v", saved, err)
	}
	if !strings.Contains(logs.String(), "from=siya.playkeeper.io to=siya.playkeeper.me") {
		t.Errorf("following is not logged: %s", logs.String())
	}
	if addrs, _ := linkAddresses(&saved, path, log); !slices.Equal(addrs, []string{"siya.playkeeper.me"}) {
		t.Errorf("addresses tried after following: %v", addrs)
	}
	ip := machinelink.Dashboard{Address: "203.0.113.10"}
	if addrs, _ := linkAddresses(&ip, path, log); !slices.Equal(addrs, []string{"203.0.113.10"}) {
		t.Errorf("addresses tried for a dashboard joined at its IP address: %v", addrs)
	}
}
