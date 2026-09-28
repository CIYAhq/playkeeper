package install

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/platform"
	"github.com/CIYAhq/playkeeper/internal/usage"
)

// Usage is how an install takes part in usage stats (internal/usage). The
// zero value sends nothing.
type Usage struct {
	// Choice is what DO_NOT_TRACK and PLAYKEEPER_USAGE_STATS said, and Why
	// names the variable that said it.
	Choice usage.Choice
	Why    string
	// Source and Channel are how Playkeeper got onto the machine and the
	// playkeeper.io channel code (usage.SourceFor, usage.CleanChannel).
	// Test marks a test install (PLAYKEEPER_USAGE_TEST); an install made
	// while a GitHub Actions job runs on the machine is one too.
	Source, Channel string
	Test            bool
	// URL is where reports go: usage.DefaultURL when empty.
	URL string
	// Send sends a report; nil sends nothing.
	Send func(ctx context.Context, e usage.Install) error
}

// On reports whether usage stats are on for a fresh install.
func (u Usage) On() bool {
	on, _ := u.state("")
	return on
}

// setting is what config.json records: the environment's choice, if it made
// one, which the agent follows until Settings changes it.
func (u Usage) setting() string {
	switch u.Choice {
	case usage.Off:
		return "off"
	case usage.On:
		return "on"
	}
	return ""
}

// AboutURL describes usage stats: what is sent, when, and how to turn it off.
const AboutURL = "https://github.com/CIYAhq/playkeeper#usage-stats"

// Why usage stats are off, as the installer says it.
const (
	offInstalled = "turned off when Playkeeper was installed"
	offSettings  = "turned off in Settings"
)

// state is whether usage stats are on once an install whose config.json
// records setting ("" on a fresh install) has run, and why when they're
// off: an off the environment or config.json chose outranks the switch in
// Settings, which only root's choices do.
func (u Usage) state(setting string) (bool, string) {
	switch {
	case u.Choice == usage.Off && u.Why == usage.EnvSwitch:
		return false, usage.EnvSwitch + " is off"
	case u.Choice == usage.Off:
		return false, u.Why + " is set"
	case u.Choice == usage.On:
		return true, ""
	case setting == "off":
		return false, offInstalled
	}
	return usage.DefaultOn, ""
}

// upgradeState is state for an upgrade of the install cfg describes: the
// agent that runs now, when it answers, says whether they are off, as the
// switch in Settings can have turned them.
func (u Usage) upgradeState(ctx context.Context, sys System, cfg config.Config) (bool, string) {
	on, why := u.state(cfg.UsageStats)
	if !on || u.Choice != usage.Unset {
		return on, why
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var st api.UsageStats
	if status, err := agentclient.New(sys.P(cfg.SocketPath)).Do(cctx, "GET", "/v1/usage-stats", nil, nil, &st); err != nil || status != 200 || st.On {
		return on, why
	}
	switch st.Reason {
	case api.UsageSettings:
		return false, offSettings
	case api.UsageEnv:
		return false, st.Variable + " is set for Playkeeper's agent"
	}
	return false, offInstalled
}

// record is cfg with what the environment chose recorded: usage stats off
// or on, the test mark and another service.
func (u Usage) record(cfg config.Config, sys System) config.Config {
	if s := u.setting(); s != "" {
		cfg.UsageStats = s
	}
	if u.Test || testInstall(sys) {
		cfg.UsageTest = true
	}
	if u.URL != "" {
		cfg.StatsURL = u.URL
	}
	return cfg
}

// notice is what the installer says about usage stats before anything else,
// so nothing is sent before it has been said: on, or off and why.
func (u Usage) notice(on bool, why string) []string {
	where := "stats.playkeeper.io"
	if u.URL != "" {
		where = strings.TrimPrefix(strings.TrimPrefix(u.URL, "https://"), "http://")
	}
	switch {
	case on:
		return []string{
			"Anonymous usage stats are on: a random ID, the version, the system and how many servers run go to",
			where + ", with no IP address or names kept. Details: " + AboutURL,
			"Turn them off in Settings › Playkeeper, or install with DO_NOT_TRACK=1: curl -fsSL https://playkeeper.io/install | sudo DO_NOT_TRACK=1 sh",
		}
	case why != "":
		return []string{fmt.Sprintf("Anonymous usage stats are off (%s): nothing is sent.", why)}
	}
	return []string{"Anonymous usage stats are off: nothing is sent. To help count Playkeeper installs, turn them on in Settings › Playkeeper."}
}

// reporter sends one install's reports, each without holding up the
// install; wait gives the ones on their way a moment to arrive.
type reporter struct {
	u   Usage
	sys usage.System
	id  string
	wg  sync.WaitGroup
}

// newReporter reports for a fresh install of version on a machine running
// host, or returns nil when usage stats are off or nothing sends them.
func newReporter(u Usage, sys System, host platform.OS, version, kind string) *reporter {
	if !u.On() || u.Send == nil {
		return nil
	}
	name, release := usage.CleanOS(host.ID, host.VersionID)
	return &reporter{u: u, id: usage.NewID(), sys: usage.System{
		Version: version, OS: name, OSVersion: release, Arch: usage.CleanArch(sys.Arch()),
		Source: u.Source, Channel: u.Channel, Kind: kind, Test: u.Test || testInstall(sys),
	}}
}

// testInstall reports what marks an install as a test on its own: a GitHub
// Actions job running on the machine, or the tests' injected failures.
func testInstall(sys System) bool {
	return usage.ActionsJob(sys.Processes()) || os.Getenv(FailStepEnv) != ""
}

// reportWait bounds how long the installer waits for its last report.
var reportWait = 3 * time.Second

func (r *reporter) send(ctx context.Context, event, step string) {
	if r == nil {
		return
	}
	e := usage.Install{ID: r.id, Event: event, Step: step, System: r.sys}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_ = r.u.Send(sctx, e)
	}()
}

// wait waits up to reportWait for the reports on their way.
func (r *reporter) wait() {
	if r == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(reportWait):
	}
}

// refusedChecks are the failed checks that turned an install away, as the
// service counts them: "memory+port".
func refusedChecks(f Facts) string {
	var ids []string
	for _, c := range f.Checks {
		if c.Status != "fail" {
			continue
		}
		id := c.ID
		if strings.HasPrefix(id, "port-") {
			id = "port"
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	if len(ids) > 8 {
		ids = ids[:8]
	}
	return strings.Join(ids, "+")
}
