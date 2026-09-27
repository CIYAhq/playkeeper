package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/install"
	"github.com/CIYAhq/playkeeper/internal/names"
)

func TestTheInstallerDeletesOnlyTheFolderGetShDownloadedItInto(t *testing.T) {
	const exe = "/tmp/playkeeper-get.AbC123/x/playkeeper-0.5.0-linux-amd64/playkeeper"
	for _, c := range []struct{ dir, want string }{
		{"/tmp/playkeeper-get.AbC123", "/tmp/playkeeper-get.AbC123"},
		{"/tmp/playkeeper-get.AbC123/", "/tmp/playkeeper-get.AbC123"},
		{"", ""},
		{"/tmp/playkeeper-get.Other", ""},
		{"/tmp", ""},
		{"/", ""},
		{"tmp/playkeeper-get.AbC123", ""},
		{"/tmp/playkeeper-get.AbC", ""},
	} {
		if got := getDir(c.dir, exe); got != c.want {
			t.Errorf("getDir(%q) = %q, want %q", c.dir, got, c.want)
		}
	}
}

func TestTheInstallerDeletesGetShsDownloadWhenItStopsEarly(t *testing.T) {
	removed := 0
	prev := removeGetDir
	removeGetDir = func() { removed++ }
	t.Cleanup(func() { removeGetDir = prev })
	for _, args := range [][]string{
		{"--no-such-flag"},
		{"--panel-port", "25565"},
		{"--release-url", "http://example.test/latest"},
		{"--join", "203.0.113.5:8443"},
	} {
		before := removed
		if err := runInstall(args); err == nil {
			t.Errorf("install %v was accepted", args)
		}
		if removed != before+1 {
			t.Errorf("install %v stopped without deleting get.sh's download", args)
		}
	}
}

// Regression for 1e19a0a: a reinstall that kept the admin account printed
// "sign in with your existing admin account" and then "Create your admin account".
func TestInstallSummaryForFirstInstallAndReinstall(t *testing.T) {
	var first, again bytes.Buffer
	writeInstallSummary(&first, &install.Result{URL: "https://192.0.2.10:8443", SetupCode: "abc123-def456-ghi789-jkl012", Fingerprint: "AA:BB:CC", Duration: 9 * time.Second})
	writeInstallSummary(&again, &install.Result{URL: "https://192.0.2.10:8443", Fingerprint: "AA:BB:CC", Duration: 10 * time.Second, ExistingAdm: true})
	for _, want := range []string{"https://192.0.2.10:8443/setup#code=abc123-def456-ghi789-jkl012", "Create your admin account", "sudo playkeeper setup-code", "AA:BB:CC", "Install finished in 9s."} {
		if !strings.Contains(first.String(), want) {
			t.Errorf("first install summary lacks %q:\n%s", want, first.String())
		}
	}
	for _, want := range []string{"Open https://192.0.2.10:8443 and sign in with your existing admin account", "worlds and backups were kept", "sudo playkeeper reset-password", "AA:BB:CC"} {
		if !strings.Contains(again.String(), want) {
			t.Errorf("reinstall summary lacks %q:\n%s", want, again.String())
		}
	}
	for _, bad := range []string{"Create your admin account", "setup code", "#code=", "setup-code"} {
		if strings.Contains(again.String(), bad) {
			t.Errorf("reinstall summary must not mention %q:\n%s", bad, again.String())
		}
	}
}

func TestDevStaysOffTheRealNamesServiceAndLetsEncrypt(t *testing.T) {
	cfg := config.Default()
	devDefaults(&cfg)
	if cfg.NamesURL != devNamesURL || cfg.ACMEDirectoryURL != devACMEDirectoryURL {
		t.Fatalf("dev defaults: names %q, ACME %q", cfg.NamesURL, cfg.ACMEDirectoryURL)
	}
	if _, err := names.CheckServiceURL(cfg.NamesURL); err != nil {
		t.Fatalf("the names client refuses the dev names service: %v", err)
	}
	if strings.Contains(cfg.NamesURL, "playkeeper.io") || !strings.Contains(cfg.ACMEDirectoryURL, "staging") {
		t.Fatalf("dev reaches the real services: names %q, ACME %q", cfg.NamesURL, cfg.ACMEDirectoryURL)
	}

	cfg = config.Default()
	cfg.NamesURL, cfg.ACMEDirectoryURL = "https://names.example.org", "https://ca.example.org/directory"
	devDefaults(&cfg)
	if cfg.NamesURL != "https://names.example.org" || cfg.ACMEDirectoryURL != "https://ca.example.org/directory" {
		t.Fatalf("dev replaced the services .dev/config.json names: names %q, ACME %q", cfg.NamesURL, cfg.ACMEDirectoryURL)
	}
}
