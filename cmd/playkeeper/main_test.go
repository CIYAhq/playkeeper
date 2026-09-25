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
