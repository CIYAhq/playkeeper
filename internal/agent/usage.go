package agent

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/platform"
	"github.com/CIYAhq/playkeeper/internal/usage"
	"github.com/CIYAhq/playkeeper/internal/version"
)

// Anonymous usage stats (internal/usage): a heartbeat a minute after the
// agent starts and twice a day after that, saying the machine runs
// Playkeeper, which version on which system, its kind of address and how
// many Minecraft servers it has. README.md ("Usage stats") says what each
// field is.
const (
	kvUsageID    = "usage_id"
	kvUsageStats = "usage_stats"
	kvUsageSent  = "usage_sent"
)

type usageState struct {
	mu sync.Mutex
	// kick wakes the heartbeat loop when the switch turns usage stats on.
	kick chan struct{}
}

// usageDecision is whether usage stats are on, what decided and the
// environment variable that did, if one. Root's choices come first: the
// agent's own environment, then an off recorded when Playkeeper was
// installed or upgraded; then the switch in Settings; then an on recorded
// at install; then usage.DefaultOn. playkeeper dev never sends.
func (a *Agent) usageDecision() (on bool, reason, variable string) {
	if a.cfg.Dev {
		return false, api.UsageDev, ""
	}
	switch choice, v := usage.FromEnv(a.opts.Getenv); choice {
	case usage.Off:
		return false, api.UsageEnv, v
	case usage.On:
		return true, api.UsageEnv, v
	}
	if a.cfg.UsageStats == "off" {
		return false, api.UsageInstall, ""
	}
	switch v, _, _ := a.kvGet(kvUsageStats); v {
	case "on":
		return true, api.UsageSettings, ""
	case "off":
		return false, api.UsageSettings, ""
	}
	if a.cfg.UsageStats == "on" {
		return true, api.UsageInstall, ""
	}
	return usage.DefaultOn, api.UsageDefault, ""
}

// usageID is the random ID heartbeats go under: the installer's, or one of
// the agent's own for an install from before 0.4.4 or one that turned usage
// stats on later.
func (a *Agent) usageID() (string, error) {
	if usage.IsID(a.cfg.UsageID) {
		return a.cfg.UsageID, nil
	}
	a.usage.mu.Lock()
	defer a.usage.mu.Unlock()
	if id, ok, err := a.kvGet(kvUsageID); err != nil || (ok && usage.IsID(id)) {
		return id, err
	}
	id := usage.NewID()
	return id, a.kvSet(kvUsageID, id)
}

// usageTest reports whether this is one of the project's own test installs
// (usage.System.Test): marked when it was installed, running the end-to-end
// tests' offline harness, marked in the agent's environment, or on a
// machine running a GitHub Actions job.
func (a *Agent) usageTest() bool {
	return a.cfg.UsageTest || a.offline() || a.opts.BuiltInListsTest || usage.TestFromEnv(a.opts.Getenv) || usage.ActionsJob(a.opts.Processes())
}

// usageReport is the heartbeat as the agent would send it now.
func (a *Agent) usageReport(ctx context.Context) (usage.Heartbeat, error) {
	id, err := a.usageID()
	if err != nil {
		return usage.Heartbeat{}, err
	}
	o := platform.ReadOS(a.opts.OSRelease)
	name, release := usage.CleanOS(o.ID, o.VersionID)
	source := usage.CleanSource(a.cfg.UsageSource)
	if source == "" && !usage.IsRelease(version.Version) {
		source = usage.SourceBuild
	}
	kind := usage.KindDashboard
	if a.joined() {
		kind = usage.KindJoined
	}
	h := usage.Heartbeat{ID: id, System: usage.System{
		Version: version.Version, OS: name, OSVersion: release, Arch: usage.CleanArch(runtime.GOARCH),
		Source: source, Channel: usage.CleanChannel(a.cfg.UsageChannel), Kind: kind, Test: a.usageTest(),
	}}
	switch a.address().Kind {
	case api.AddressPlaykeeper:
		h.Address = usage.AddressFree
	case api.AddressOwn:
		h.Address = usage.AddressOwn
	default:
		h.Address = usage.AddressIP
	}
	for _, s := range a.serverList() {
		h.Servers++
		if s.Status(ctx).Phase == api.PhaseOnline {
			h.Running++
		}
	}
	h.Servers, h.Running = min(h.Servers, usage.MaxServers), min(h.Running, usage.MaxServers)
	return h, nil
}

func (a *Agent) statsService() string {
	if a.cfg.StatsURL != "" {
		return a.cfg.StatsURL
	}
	return usage.DefaultURL
}

// usageStats is what the dashboard shows: whether usage stats are on, why,
// and the heartbeat, field for field.
func (a *Agent) usageStats(ctx context.Context) (api.UsageStats, error) {
	on, reason, variable := a.usageDecision()
	st := api.UsageStats{On: on, Reason: reason, Variable: variable, Service: a.statsService(),
		CanChange: reason != api.UsageDev && reason != api.UsageEnv && !(reason == api.UsageInstall && !on)}
	if v, ok, _ := a.kvGet(kvUsageSent); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			st.LastSent = &t
		}
	}
	var err error
	st.Report, err = a.usageReport(ctx)
	return st, err
}

// sendUsage sends a heartbeat if usage stats are on.
func (a *Agent) sendUsage(ctx context.Context) error {
	if on, _, _ := a.usageDecision(); !on {
		return nil
	}
	h, err := a.usageReport(ctx)
	if err != nil {
		return err
	}
	c := usage.Client{URL: a.cfg.StatsURL, HTTP: a.opts.UsageClient, Version: version.Version}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.SendHeartbeat(sctx, h); err != nil {
		return err
	}
	return a.kvSet(kvUsageSent, a.now().UTC().Format(time.RFC3339))
}

// usageLoop sends a heartbeat UsageFirst after the agent starts, then every
// UsageInterval plus up to a 24th of it (half an hour for 12 hours), so
// machines started together don't all send at once, and right away when the
// switch turns usage stats on.
func (a *Agent) usageLoop(ctx context.Context) {
	if a.opts.UsageInterval < 0 {
		return
	}
	wait := a.opts.UsageFirst
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-a.usage.kick:
			t.Stop()
		case <-t.C:
		}
		if err := a.sendUsage(ctx); err != nil && !errors.Is(err, context.Canceled) {
			a.log.Info("usage stats were not sent", "err", err)
		}
		wait = a.opts.UsageInterval + time.Duration(rand.Int64N(int64(a.opts.UsageInterval/24)+1))
	}
}

func (a *Agent) hUsageStats(w http.ResponseWriter, r *http.Request) {
	st, err := a.usageStats(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// hUsageStatsSet is the switch in Settings.
func (a *Agent) hUsageStatsSet(w http.ResponseWriter, r *http.Request) {
	var req api.UsageStatsRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	switch on, reason, variable := a.usageDecision(); {
	case reason == api.UsageDev:
		writeError(w, errConflict("playkeeper dev never sends usage stats.", ""))
		return
	case reason == api.UsageEnv:
		writeError(w, errConflict(variable+" is set for Playkeeper's agent on this machine, so it decides.",
			"To use the switch, remove it from the agent's environment: sudo systemctl edit playkeeper-agent"))
		return
	case reason == api.UsageInstall && !on:
		writeError(w, errConflict("Usage stats were turned off when Playkeeper was installed, which the switch can't change.",
			`To use the switch, remove "usageStats": "off" from /etc/playkeeper/config.json, then run: sudo systemctl restart playkeeper-agent`))
		return
	}
	v := "off"
	if req.On {
		v = "on"
	}
	if err := a.kvSet(kvUsageStats, v); err != nil {
		writeError(w, err)
		return
	}
	a.audit(actor, "usage_stats."+v, "", "succeeded", "")
	if req.On {
		select {
		case a.usage.kick <- struct{}{}:
		default:
		}
	}
	a.hUsageStats(w, r)
}

// procCmdlines are the command lines of the machine's processes, as /proc
// has them, for a GitHub Actions job running on the machine.
func procCmdlines() []string {
	var out []string
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(b) == 0 {
			continue
		}
		out = append(out, strings.ReplaceAll(strings.TrimRight(string(b), "\x00"), "\x00", " "))
	}
	return out
}
