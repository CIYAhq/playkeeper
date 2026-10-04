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
// agent starts, right after each setup step the machine reaches for the
// first time, and twice a day after that, saying the machine runs
// Playkeeper, which version on which system, its kind of address, how many
// Minecraft servers it has and the furthest setup step it reached.
// README.md ("Usage stats") says what each field is.
const (
	kvUsageID    = "usage_id"
	kvUsageStats = "usage_stats"
	kvUsageSent  = "usage_sent"
	// The setup steps that send a heartbeat at once, the first time the
	// machine reaches each: the dashboard's first account, its first server
	// online, the first player's session on any of its servers (the
	// owner's included), and a second, different player's.
	kvUsageAccount = "usage_first_account"
	kvUsageOnline  = "usage_first_online"
	kvUsagePlayed  = "usage_first_played"
	kvUsageFriends = "usage_friends"
)

// usageSteps are the setup steps, furthest first, each with what a
// heartbeat says for it (usage.Heartbeat.Reached).
var usageSteps = []struct{ key, reached string }{
	{kvUsageFriends, usage.ReachedFriends},
	{kvUsagePlayed, usage.ReachedPlayed},
	{kvUsageOnline, usage.ReachedServer},
	{kvUsageAccount, usage.ReachedAccount},
}

// usageFirstAccountPath is the panel saying the dashboard's first account
// was just made. Only the agent's socket answers it (see socketOnly).
const usageFirstAccountPath = "/v1/usage-stats/first-account"

type usageState struct {
	// mu serializes making the usage ID and noting the setup steps.
	mu sync.Mutex
	// kick wakes the heartbeat loop when the switch turns usage stats on,
	// or a setup step sends one at once.
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
	h.Reached = a.usageReached()
	return h, nil
}

// usageReached is the furthest setup step the machine has reached, or none
// before its first: the dashboard's first account, or a joined machine's
// first server online.
func (a *Agent) usageReached() string {
	for _, step := range usageSteps {
		if _, noted, err := a.kvGet(step.key); err == nil && noted {
			return step.reached
		}
	}
	return ""
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
// switch turns usage stats on or a setup step is reached, counting the
// interval again from there.
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

// kickUsage has the heartbeat loop send one now. A kick it hasn't taken yet
// already covers this one: the heartbeat says what is true when it's sent.
func (a *Agent) kickUsage() {
	select {
	case a.usage.kick <- struct{}{}:
	default:
	}
}

// usageStep sends a heartbeat now, if usage stats are on, the first time the
// machine reaches the setup step noted under key, so a machine deleted
// within 12 hours of its install still reports how far its setup got.
func (a *Agent) usageStep(key string) error {
	a.usage.mu.Lock()
	_, noted, err := a.kvGet(key)
	if err == nil && !noted {
		err = a.kvSet(key, a.now().UTC().Format(time.RFC3339))
	}
	a.usage.mu.Unlock()
	if err == nil && !noted {
		a.kickUsage()
	}
	return err
}

// firstOnline notes a server showing online, the first time one does, so
// the heartbeat this may send counts it running.
func (a *Agent) firstOnline() {
	if err := a.usageStep(kvUsageOnline); err != nil {
		a.log.Info("could not note the first server online for usage stats", "err", err)
	}
}

// runOnline is the server's run logging that it's up. While an operation
// still shows an earlier step, such as a start's "starting_container", the
// server doesn't show online yet: the start notes it once it does
// (waitReady), and a start that had just given up waiting leaves it to the
// look once the operation has ended (opEnded). Otherwise it shows online
// now: after a start that gave up waiting for it, or with no start waiting
// at all.
func (s *server) runOnline() {
	op := s.currentOp()
	if op == nil {
		op = s.machineOp()
	}
	if _, shown := opPhase(op); !shown {
		s.firstOnline()
	}
}

// opEnded looks again at a server whose run is up, once its operation has
// ended and shows no step of its own any more.
func (s *server) opEnded() {
	s.mu.Lock()
	up := s.runPhase == api.PhaseOnline
	s.mu.Unlock()
	if up {
		s.runOnline()
	}
}

// sessionStarted notes the setup steps a player's session can reach: the
// first on any of the machine's servers, then the first by a second,
// different player.
func (s *server) sessionStarted() {
	if err := s.usageStep(kvUsagePlayed); err != nil {
		s.log.Info("could not note the first player for usage stats", "err", err)
		return
	}
	if _, noted, err := s.kvGet(kvUsageFriends); err != nil || noted {
		return
	}
	var players int
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT lower(player)) FROM sessions`).Scan(&players); err != nil || players < 2 {
		return
	}
	if err := s.usageStep(kvUsageFriends); err != nil {
		s.log.Info("could not note a second player for usage stats", "err", err)
	}
}

// loadUsage notes the setup steps a machine took before Playkeeper noted
// them, from what it keeps anyway: a server that came online (before
// 0.4.16), and players' sessions (before 0.4.18). So updating sends no
// heartbeat for a step taken long ago.
func (a *Agent) loadUsage() {
	for _, step := range []struct{ key, took string }{
		{kvUsageOnline, `SELECT EXISTS(SELECT 1 FROM events WHERE kind = 'server_ready')`},
		{kvUsagePlayed, `SELECT EXISTS(SELECT 1 FROM sessions)`},
		{kvUsageFriends, `SELECT COUNT(DISTINCT lower(player)) >= 2 FROM sessions`},
	} {
		if _, noted, err := a.kvGet(step.key); err != nil || noted {
			continue
		}
		var took bool
		if err := a.db.QueryRow(step.took).Scan(&took); err != nil || !took {
			continue
		}
		if err := a.kvSet(step.key, a.now().UTC().Format(time.RFC3339)); err != nil {
			a.log.Warn("could not note a setup step taken before", "step", step.key, "err", err)
		}
	}
}

// hUsageFirstAccount is the panel saying the dashboard's first account was
// just made.
func (a *Agent) hUsageFirstAccount(w http.ResponseWriter, r *http.Request) {
	if err := a.usageStep(kvUsageAccount); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
		a.kickUsage()
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
