package install

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/usage"
)

// reports records what an install would send the stats service.
type reports struct {
	mu   sync.Mutex
	sent []usage.Install
}

func (r *reports) send(_ context.Context, e usage.Install) error {
	if err := e.Check(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, e)
	return nil
}

func (r *reports) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.sent {
		out = append(out, e.Event+":"+e.Step)
	}
	return out
}

func (r *reports) all() []usage.Install {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]usage.Install(nil), r.sent...)
}

func withUsage(o Options, r *reports, change ...func(*Usage)) Options {
	o.Usage = Usage{Source: usage.SourceSite, Channel: "hn", Send: r.send}
	for _, c := range change {
		c(&o.Usage)
	}
	return o
}

func installedConfig(t *testing.T, h *fakeHost) config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join(h.root, ConfigDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The installer says usage stats are on before it checks anything, sends
// that the install started once the plan is accepted and how it ended, and
// the agent carries on under the same random ID.
func TestAnInstallReportsItsStartAndEndUnderTheIDItLeavesTheAgent(t *testing.T) {
	h := newFakeHost(t)
	r := &reports{}
	o := withUsage(opts(""), r)
	o.Yes = true
	res, err := Run(context.Background(), h.system(t), o, "0.4.4")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.events(); strings.Join(got, " ") != "started: succeeded:" {
		t.Fatalf("sent %v", got)
	}
	sent := r.all()
	want := usage.System{Version: "0.4.4", OS: "ubuntu", OSVersion: "24.04", Arch: "amd64", Source: usage.SourceSite, Channel: "hn", Kind: usage.KindDashboard}
	for _, e := range sent {
		if e.System != want || e.ID != sent[0].ID {
			t.Errorf("sent %+v, want %+v under one ID", e, want)
		}
	}
	cfg := installedConfig(t, h)
	if cfg.UsageID != sent[0].ID || cfg.UsageSource != usage.SourceSite || cfg.UsageChannel != "hn" || cfg.UsageStats != "" || cfg.UsageTest {
		t.Errorf("config.json: %+v", cfg)
	}
	if cfg.UsageID == cfg.InstallID || strings.Contains(cfg.InstallID, cfg.UsageID) {
		t.Error("the usage ID is the install ID, which backups and Docker labels carry")
	}
	out := o.Out.(*bytes.Buffer).String()
	notice, checks := strings.Index(out, "Anonymous usage stats are on"), strings.Index(out, "Checking this server")
	if notice < 0 || checks < notice || !strings.Contains(out, AboutURL) || !strings.Contains(out, "sudo DO_NOT_TRACK=1 sh") || !strings.Contains(out, "no IP address") {
		t.Errorf("the installer doesn't say first what it sends and how to turn it off:\n%s", out)
	}
	if !res.UsageOn {
		t.Error("the result doesn't say usage stats are on")
	}
}

func TestDecliningThePlanSendsNothing(t *testing.T) {
	h := newFakeHost(t)
	r := &reports{}
	if _, err := Run(context.Background(), h.system(t), withUsage(opts("n\n"), r), "0.4.4"); !errors.Is(err, errDeclined) {
		t.Fatal(err)
	}
	// Reports go out in the background, so one sent would land a moment later.
	time.Sleep(100 * time.Millisecond)
	if got := r.events(); len(got) != 0 {
		t.Errorf("a declined install sent %v", got)
	}
}

// Checks that turn an install away are counted by name, as the service
// counts them, never with what they found.
func TestARefusedInstallSaysWhichChecksRefusedIt(t *testing.T) {
	h := newFakeHost(t)
	h.memMB = 1000
	h.listening[25565] = true
	r := &reports{}
	if _, err := Run(context.Background(), h.system(t), withUsage(opts(""), r), "0.4.4"); err == nil {
		t.Fatal("the install went ahead")
	}
	if got := r.events(); strings.Join(got, " ") != "refused:memory+port" {
		t.Errorf("sent %v", got)
	}
}

func TestAFailedInstallSaysWhichStepFailedAndCountsAsATest(t *testing.T) {
	for step, code := range map[string]string{"install and start systemd services": "services", "create directories": "directories", "write install manifest": "manifest"} {
		t.Run(code, func(t *testing.T) {
			h := newFakeHost(t)
			t.Setenv(FailStepEnv, step)
			r := &reports{}
			o := withUsage(opts(""), r)
			o.Yes = true
			if _, err := Run(context.Background(), h.system(t), o, "0.4.4"); err == nil {
				t.Fatal("the injected failure didn't stop the install")
			}
			sent := r.all()
			if got := r.events(); strings.Join(got, " ") != "started: failed:"+code {
				t.Fatalf("sent %v", got)
			}
			if !sent[1].Test {
				t.Error("an install with an injected failure isn't marked as a test")
			}
		})
	}
}

func TestDoNotTrackSendsNothingAndTheAgentKeepsItOff(t *testing.T) {
	h := newFakeHost(t)
	r := &reports{}
	o := withUsage(opts(""), r, func(u *Usage) { u.Choice, u.Why = usage.Off, usage.EnvDoNotTrack })
	o.Yes = true
	res, err := Run(context.Background(), h.system(t), o, "0.4.4")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.events(); len(got) != 0 {
		t.Errorf("DO_NOT_TRACK=1 sent %v", got)
	}
	cfg := installedConfig(t, h)
	if cfg.UsageStats != "off" || cfg.UsageID != "" || cfg.UsageSource != usage.SourceSite {
		t.Errorf("config.json: %+v", cfg)
	}
	out := o.Out.(*bytes.Buffer).String()
	if !strings.Contains(out, "Anonymous usage stats are off (DO_NOT_TRACK is set): nothing is sent.") || res.UsageOn {
		t.Errorf("the installer doesn't say usage stats are off:\n%s", out)
	}
}

// An install made while a GitHub Actions job runs on the machine is the
// project's own test, which the service keeps out of every count.
func TestAnInstallDuringAnActionsJobIsATest(t *testing.T) {
	h := newFakeHost(t)
	h.procs = []string{"/home/runner/actions-runner/cached/bin/Runner.Worker spawnclient 118 121"}
	r := &reports{}
	o := withUsage(opts(""), r)
	o.Yes = true
	if _, err := Run(context.Background(), h.system(t), o, "0.4.4"); err != nil {
		t.Fatal(err)
	}
	for _, e := range r.all() {
		if !e.Test {
			t.Errorf("%s was not marked as a test", e.Event)
		}
	}
	if !installedConfig(t, h).UsageTest {
		t.Error("config.json doesn't mark the install as a test for the agent")
	}
}

// A stats service that doesn't answer holds the install up for a moment at
// most.
func TestAServiceThatDoesntAnswerHoldsTheInstallUpForAMomentAtMost(t *testing.T) {
	reportWait = 100 * time.Millisecond
	t.Cleanup(func() { reportWait = 3 * time.Second })
	h := newFakeHost(t)
	o := opts("")
	o.Yes = true
	o.Usage = Usage{Source: usage.SourceTarball, Send: func(ctx context.Context, _ usage.Install) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	start := time.Now()
	if _, err := Run(context.Background(), h.system(t), o, "0.4.4"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the install waited %v for the stats service", d)
	}
}

// An upgrade says it too, since the new version sends what the old one
// didn't, and records an off the environment chose, which then outranks the
// switch in Settings.
func TestAnUpgradeSaysSoAndRecordsDoNotTrack(t *testing.T) {
	h := newFakeHost(t)
	installedAt(t, h, "0.4.3", true)
	sys := h.system(t)
	bin := newBinary(t, "0.4.4")
	sys.Executable = func() (string, error) { return bin, nil }
	r := &reports{}
	o := withUsage(opts(""), r, func(u *Usage) { u.Choice, u.Why = usage.Off, usage.EnvDoNotTrack })
	o.Yes = true
	if _, err := Run(context.Background(), sys, o, "0.4.4"); err != nil {
		t.Fatalf("%v\n%s", err, o.Out)
	}
	cfg := installedConfig(t, h)
	if cfg.UsageStats != "off" || cfg.InstallID != "install-0001" || cfg.UsageID != "" {
		t.Errorf("config.json after the upgrade: %+v", cfg)
	}
	if !strings.Contains(o.Out.(*bytes.Buffer).String(), "Anonymous usage stats are off (DO_NOT_TRACK is set)") {
		t.Errorf("the upgrade doesn't say:\n%s", o.Out)
	}
	if got := r.events(); len(got) != 0 {
		t.Errorf("an upgrade sent %v: only installs report", got)
	}

	// The next upgrade, without the variable, keeps it off and says why.
	h2 := newFakeHost(t)
	c := installedAt(t, h2, "0.4.4", true)
	c.UsageStats = "off"
	c.Save(filepath.Join(h2.root, ConfigDir, "config.json"))
	sys2 := h2.system(t)
	bin2 := newBinary(t, "0.4.5")
	sys2.Executable = func() (string, error) { return bin2, nil }
	o2 := withUsage(opts(""), r)
	o2.Yes = true
	if _, err := Run(context.Background(), sys2, o2, "0.4.5"); err != nil {
		t.Fatalf("%v\n%s", err, o2.Out)
	}
	if got := installedConfig(t, h2); got.UsageStats != "off" {
		t.Errorf("the next upgrade turned usage stats back on: %+v", got)
	}
	if !strings.Contains(o2.Out.(*bytes.Buffer).String(), "Anonymous usage stats are off (turned off when Playkeeper was installed)") {
		t.Errorf("the next upgrade doesn't say why they're off:\n%s", o2.Out)
	}
}

func TestAnUpgradeFromBeforeUsageStatsSaysTheyreOn(t *testing.T) {
	h := newFakeHost(t)
	installedAt(t, h, "0.4.3", true)
	sys := h.system(t)
	bin := newBinary(t, "0.4.4")
	sys.Executable = func() (string, error) { return bin, nil }
	o := withUsage(opts(""), &reports{})
	o.Yes = true
	if _, err := Run(context.Background(), sys, o, "0.4.4"); err != nil {
		t.Fatalf("%v\n%s", err, o.Out)
	}
	out := o.Out.(*bytes.Buffer).String()
	if notice, plan := strings.Index(out, "Anonymous usage stats are on"), strings.Index(out, "This upgrades it"); notice < 0 || plan < notice {
		t.Errorf("the upgrade doesn't say usage stats are on before its plan:\n%s", out)
	}
	if cfg := installedConfig(t, h); cfg.UsageStats != "" || cfg.UsageID != "" {
		t.Errorf("an upgrade without a choice changed config.json: %+v", cfg)
	}
}
