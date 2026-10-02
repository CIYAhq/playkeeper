package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/install"
	"github.com/CIYAhq/playkeeper/internal/names"
	usagestats "github.com/CIYAhq/playkeeper/internal/usage"
	"github.com/CIYAhq/playkeeper/internal/whop"
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

// The browser's warning is what most people see first, so the summary says
// what to click; the fingerprint is an extra check on its own last line,
// which scripts/e2e/vm-e2e.sh reads.
func TestInstallSummarySaysWhatToClickAtTheBrowsersWarning(t *testing.T) {
	for _, res := range []*install.Result{
		{URL: "https://192.0.2.10:8443", SetupCode: "abc123", Fingerprint: "4E:FA:00", Duration: time.Second},
		{URL: "https://192.0.2.10:8443", Fingerprint: "4E:FA:00", Duration: time.Second, ExistingAdm: true},
	} {
		var b bytes.Buffer
		writeInstallSummary(&b, res)
		out := b.String()
		for _, want := range []string{"Click Advanced, then Proceed", "this warning is expected", "Safari: Show Details, then visit this website", "\nTo check the certificate in your browser, its SHA-256 fingerprint is 4E:FA:00\n"} {
			if !strings.Contains(out, want) {
				t.Errorf("the summary lacks %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "Continue only if") {
			t.Errorf("the summary still makes the fingerprint a condition:\n%s", out)
		}
		if steps, check := strings.Index(out, "  3. "), strings.Index(out, "SHA-256 fingerprint"); steps < 0 || check < steps {
			t.Errorf("the fingerprint comes before the steps end:\n%s", out)
		}
	}
}

// A setup link with a private address, which the installer couldn't swap for
// the public one, says so and where the public one is, after an install and
// after an upgrade; a public one needs no line.
func TestInstallSummarySaysWhenTheAddressIsPrivate(t *testing.T) {
	var private, public, upgraded, upgradedPublic bytes.Buffer
	writeInstallSummary(&private, &install.Result{URL: "https://10.0.0.5:8443", PrivateHost: true, SetupCode: "abc123", Fingerprint: "AA:BB"})
	writeInstallSummary(&public, &install.Result{URL: "https://203.0.113.7:8443", SetupCode: "abc123", Fingerprint: "AA:BB"})
	writeUpgradeSummary(&upgraded, &install.Result{URL: "https://10.0.0.5:8443", PrivateHost: true, Upgraded: true, FromVersion: "0.4.15", Fingerprint: "AA:BB"})
	writeUpgradeSummary(&upgradedPublic, &install.Result{URL: "https://203.0.113.7:8443", Upgraded: true, FromVersion: "0.4.15", Fingerprint: "AA:BB"})
	want := "\n10.0.0.5 is this VPS's private address: if the link won't open, use the public IP from your provider's console in its place.\n"
	for _, out := range []string{private.String(), upgraded.String()} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary lacks %q:\n%s", want, out)
		}
	}
	for _, out := range []string{public.String(), upgradedPublic.String()} {
		for _, bad := range []string{"private address", "is not your public address"} {
			if strings.Contains(out, bad) {
				t.Errorf("a public address's summary says %q:\n%s", bad, out)
			}
		}
	}
}

// Right under the link, the summary says what to do when it won't open: the
// provider's firewall steps, for the provider the installer told, with the
// install's own ports.
func TestInstallSummarySaysWhatToDoWhenTheLinkWontOpen(t *testing.T) {
	for _, c := range []struct {
		res  install.Result
		want string
	}{
		{install.Result{SetupCode: "abc123", PanelPort: 8443, GamePort: 25565, Provider: install.Providers[0]},
			"\n     Won't open? Open ports 8443 and 25565 in AWS's firewall: https://playkeeper.io/ports#aws\n"},
		{install.Result{SetupCode: "abc123", PanelPort: 9443, GamePort: 25570},
			"\n     Won't open? Open ports 9443 and 25570 in your provider's firewall: https://playkeeper.io/ports\n"},
		{install.Result{ExistingAdm: true, PanelPort: 8443, GamePort: 25565, Provider: install.Providers[3]},
			"\n     Won't open? Open ports 8443 and 25565 in Oracle Cloud's firewall: https://playkeeper.io/ports#oracle-cloud\n"},
	} {
		c.res.URL, c.res.Fingerprint = "https://192.0.2.10:8443", "AA:BB"
		var b bytes.Buffer
		writeInstallSummary(&b, &c.res)
		out := b.String()
		if !strings.Contains(out, c.want) {
			t.Errorf("the summary lacks %q:\n%s", c.want, out)
		}
		if strings.Index(out, "Won't open?") > strings.Index(out, "  2. ") {
			t.Errorf("the line isn't under the link:\n%s", out)
		}
	}
}

// The install command run again where this version runs gives the way in
// again: the link, with a new setup code before setup, what to do when it
// won't open, how to update and how to start over.
func TestTheInstallCommandRunAgainSaysHowToGetIn(t *testing.T) {
	always := []string{
		"\n     Won't open? Open ports 8443 and 25565 in Oracle Cloud's firewall: https://playkeeper.io/ports#oracle-cloud\n",
		"  2. Your browser warns that the connection isn't private. Click Advanced, then Proceed:",
		"\nTo update: in the dashboard, Settings › Playkeeper › Check for updates.\n",
		"\nTo start over: sudo playkeeper uninstall, then the install command again. Worlds and backups stay.\n",
		"\nTo start over without them: sudo playkeeper uninstall --purge, then the install command again.\n",
	}
	for _, c := range []struct {
		name      string
		res       install.Result
		want, not []string
	}{
		{"before setup", install.Result{SetupCode: "abc123"},
			[]string{"\n  1. Open this link in your browser:\n       https://203.0.113.7:8443/setup#code=abc123\n     (a new setup code: abc123 — works once, expires in 24 hours)\n"},
			[]string{"Forgot the password?", "Lost the setup code?"}},
		{"with its admin", install.Result{ExistingAdm: true},
			[]string{"\n  1. Open https://203.0.113.7:8443 and sign in.\n", "\nForgot the password? sudo playkeeper reset-password <username>\n"},
			[]string{"setup#code", "Lost the setup code?"}},
		{"not known", install.Result{},
			[]string{"\n  1. Open https://203.0.113.7:8443 in your browser.\n", "\nLost the setup code? sudo playkeeper setup-code\n", "\nForgot the password? sudo playkeeper reset-password <username>\n"},
			nil},
	} {
		c.res.UpToDate, c.res.URL, c.res.PanelPort, c.res.GamePort, c.res.Provider = true, "https://203.0.113.7:8443", 8443, 25565, install.Providers[3]
		var b bytes.Buffer
		writeInstalledSummary(&b, &c.res)
		out := b.String()
		for _, want := range append(c.want, always...) {
			if !strings.Contains(out, want) {
				t.Errorf("%s: the summary lacks %q:\n%s", c.name, want, out)
			}
		}
		for _, bad := range c.not {
			if strings.Contains(out, bad) {
				t.Errorf("%s: the summary says %q:\n%s", c.name, bad, out)
			}
		}
		if strings.Index(out, "Won't open?") > strings.Index(out, "  2. ") {
			t.Errorf("%s: Won't open? isn't under the link:\n%s", c.name, out)
		}
	}

	var joined bytes.Buffer
	writeInstalledSummary(&joined, &install.Result{UpToDate: true, NoPanel: true})
	for _, want := range []string{"its servers are in the dashboard it joined", "To update it: in that dashboard, Settings › Machines.", "To start over: sudo playkeeper uninstall, then connect it again"} {
		if !strings.Contains(joined.String(), want) {
			t.Errorf("a joined machine's summary lacks %q:\n%s", want, joined.String())
		}
	}
	if strings.Contains(joined.String(), "Won't open?") {
		t.Errorf("a joined machine's summary names a dashboard link:\n%s", joined.String())
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
	if strings.Contains(cfg.NamesURL, "playkeeper.io") || !strings.Contains(cfg.ACMEDirectoryURL, "staging") || cfg.WhopAPIURL != whop.SandboxAPIURL {
		t.Fatalf("dev reaches the real services: names %q, ACME %q, Whop %q", cfg.NamesURL, cfg.ACMEDirectoryURL, cfg.WhopAPIURL)
	}

	cfg = config.Default()
	cfg.NamesURL, cfg.ACMEDirectoryURL = "https://names.example.org", "https://ca.example.org/directory"
	devDefaults(&cfg)
	if cfg.NamesURL != "https://names.example.org" || cfg.ACMEDirectoryURL != "https://ca.example.org/directory" {
		t.Fatalf("dev replaced the services .dev/config.json names: names %q, ACME %q", cfg.NamesURL, cfg.ACMEDirectoryURL)
	}
}

func TestTheInstallTakesUsageStatsFromTheEnvironment(t *testing.T) {
	env := map[string]string{"DO_NOT_TRACK": "1", "PLAYKEEPER_INSTALL_SOURCE": "playkeeper.io", "PLAYKEEPER_INSTALL_CHANNEL": "HN", "PLAYKEEPER_USAGE_TEST": "1"}
	u, err := installUsage(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	// This test binary isn't a release, so it comes from the source.
	if u.Choice != usagestats.Off || u.Why != usagestats.EnvDoNotTrack || u.Source != usagestats.SourceBuild || u.Channel != "hn" || !u.Test || u.Send == nil {
		t.Errorf("%+v", u)
	}
	env = map[string]string{"PLAYKEEPER_STATS_URL": "http://stats.example.test"}
	if _, err := installUsage(func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), "PLAYKEEPER_STATS_URL") {
		t.Errorf("a plain http:// stats service elsewhere: %v", err)
	}
	var summary bytes.Buffer
	writeInstallSummary(&summary, &install.Result{URL: "https://192.0.2.10:8443", SetupCode: "abc123", Fingerprint: "AA:BB", UsageOn: true})
	if !strings.Contains(summary.String(), "Anonymous usage stats are on; Settings › Playkeeper turns them off.") {
		t.Errorf("the summary doesn't say where usage stats turn off:\n%s", summary.String())
	}
}

func TestStatusSaysWhetherServersAreKeptFromTheMachine(t *testing.T) {
	for _, c := range []struct {
		g    api.NetworkGuard
		want string
	}{
		{api.NetworkGuard{On: true, Host: true}, "servers can't reach this machine or the cloud's metadata service"},
		{api.NetworkGuard{On: true}, "servers can reach this machine, not the cloud's metadata service (Keep servers away from this machine is off in Machine settings)"},
		{api.NetworkGuard{Host: true}, "starts with the first server"},
		{api.NetworkGuard{Problem: "the iptables command isn't installed"}, "off, so servers can reach this machine and the cloud's metadata service (the iptables command isn't installed)"},
	} {
		if got := guardLine(c.g); got != "Network guard: "+c.want {
			t.Errorf("%+v: got %q", c.g, got)
		}
	}
}
