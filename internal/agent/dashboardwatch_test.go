package agent

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/machinelink"
)

// watchEnv is an agent that talks to the fake Discord and looks at its link
// every 20 ms.
func watchEnv(t *testing.T) (*agentEnv, *fakeHook) {
	return newDiscordEnvWith(t, func(e *agentEnv) {
		e.tweak = func(o *Options) { o.DashboardWatchInterval = 20 * time.Millisecond }
	})
}

// joinDashboard writes what a link that joined the dashboard at
// panel.example.com:8443, as m2, keeps.
func (e *agentEnv) joinDashboard() {
	e.t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.MkdirAll(e.cfg.LinkDir(), 0o700); err != nil {
		e.t.Fatal(err)
	}
	d := machinelink.Dashboard{Address: "panel.example.com:8443", Key: pub, MachineID: "abcdefghjk", Name: "m2", JoinedAt: time.Now()}
	if err := d.Save(e.cfg.LinkDashboardPath()); err != nil {
		e.t.Fatal(err)
	}
}

// linkIs writes the link's state as a running link does: state, with the
// dashboard last heard at heard.
func (e *agentEnv) linkIs(state machinelink.LinkState, heard time.Time) {
	e.t.Helper()
	b, _ := json.Marshal(machinelink.LinkStatus{State: state, Dashboard: "panel.example.com:8443", MachineID: "abcdefghjk", Name: "m2", LastSeen: heard})
	path := e.cfg.LinkStatusPath()
	if err := os.WriteFile(path+".tmp", b, 0o644); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		e.t.Fatal(err)
	}
}

// later moves the agent's clock on by d.
func (e *agentEnv) later(d time.Duration) { e.skew.Add(int64(d)) }

// watchMessages are the watch's messages Discord got.
func (f *fakeHook) watchMessages() []string {
	var out []string
	for _, r := range f.messages() {
		for _, title := range []string{"Dashboard can't be reached", "Dashboard reachable again"} {
			if strings.Contains(r.Raw, title) {
				out = append(out, title)
			}
		}
	}
	return out
}

// quietFor fails the test if the watch posts anything more than it had
// within d.
func (f *fakeHook) quietFor(e *agentEnv, d time.Duration, want ...string) {
	e.t.Helper()
	time.Sleep(d)
	if got := f.watchMessages(); !slices.Equal(got, want) {
		e.t.Fatalf("the watch posted %q, want %q", got, want)
	}
}

func (e *agentEnv) giveWebhook(url string) (int, map[string]any) {
	e.t.Helper()
	return e.call("PUT", "/v1/dashboard-watch", map[string]any{"webhookUrl": url, "actor": "playkeeper"})
}

func TestAJoinedMachinePostsOnlyThatItsDashboardCantBeReachedAndIsBack(t *testing.T) {
	e, f := watchEnv(t)
	e.joinDashboard()
	e.linkIs(machinelink.LinkConnected, e.a.now())
	if code, out := e.giveWebhook(hookURL); code != 204 {
		t.Fatalf("the dashboard's webhook: %d %v", code, out)
	}
	if e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'dashboard_watch.set' AND actor = 'playkeeper' AND detail = ''`) != 1 {
		t.Fatal("keeping the dashboard's webhook isn't audited")
	}
	f.quietFor(e, 150*time.Millisecond)

	// The link drops: the dashboard was last heard now.
	e.linkIs(machinelink.LinkRetrying, e.a.now())
	f.quietFor(e, 150*time.Millisecond)
	e.later(4 * time.Minute)
	f.quietFor(e, 150*time.Millisecond)
	e.later(2 * time.Minute)
	f.waitMessage(e, "Dashboard can't be reached", `**m2** hasn't reached the dashboard for 6 minutes.`, "https://panel.example.com:8443")
	e.later(10 * time.Minute)
	f.quietFor(e, 150*time.Millisecond, "Dashboard can't be reached")

	e.linkIs(machinelink.LinkConnected, e.a.now())
	f.waitMessage(e, "Dashboard reachable again", `**m2** reaches the dashboard again, after 16 minutes.`)
	f.quietFor(e, 150*time.Millisecond, "Dashboard can't be reached", "Dashboard reachable again")

	// A link that says it's connected but hasn't heard the dashboard for
	// two minutes is down too, and so is a link that isn't running.
	e.later(3 * time.Minute)
	f.quietFor(e, 150*time.Millisecond, "Dashboard can't be reached", "Dashboard reachable again")
	e.later(4 * time.Minute)
	f.waitMessage(e, "Dashboard can't be reached", "for 7 minutes")
	e.linkIs(machinelink.LinkConnected, e.a.now())
	f.waitMessage(e, "Dashboard reachable again", "after 7 minutes")
	if err := os.Remove(e.cfg.LinkStatusPath()); err != nil {
		t.Fatal(err)
	}
	f.quietFor(e, 150*time.Millisecond, "Dashboard can't be reached", "Dashboard reachable again", "Dashboard can't be reached", "Dashboard reachable again")
	e.later(4 * time.Minute)
	f.quietFor(e, 150*time.Millisecond, "Dashboard can't be reached", "Dashboard reachable again", "Dashboard can't be reached", "Dashboard reachable again")
	e.later(4 * time.Minute)
	f.waitMessage(e, "Dashboard can't be reached", "for 8 minutes")

	if strings.Contains(e.warnings.String(), hookToken) {
		t.Fatal("the agent logged the dashboard's webhook URL")
	}
	var n int
	e.a.db.QueryRow(`SELECT COUNT(*) FROM audit WHERE detail LIKE ? OR target LIKE ?`, "%"+hookToken+"%", "%"+hookToken+"%").Scan(&n)
	if n != 0 {
		t.Fatalf("the audit log recorded the dashboard's webhook URL %d times", n)
	}
	if _, out := e.call("GET", "/v1/discord", nil); out["connected"] != false {
		t.Fatalf("the dashboard's webhook connected this machine's own Discord: %v", out)
	}
}

func TestTheDashboardsWebhookOutlastsARestart(t *testing.T) {
	e, f := watchEnv(t)
	e.joinDashboard()
	e.linkIs(machinelink.LinkConnected, e.a.now())
	if code, out := e.giveWebhook(hookURL); code != 204 {
		t.Fatalf("the dashboard's webhook: %d %v", code, out)
	}
	e.stop()
	e.start()
	e.linkIs(machinelink.LinkRetrying, e.a.now())
	e.later(5 * time.Minute)
	f.waitMessage(e, "Dashboard can't be reached")
}

func TestAMachineThatIsntJoinedRefusesTheDashboardsWebhook(t *testing.T) {
	e, _ := watchEnv(t)
	code, out := e.giveWebhook(hookURL)
	if code != 409 || out["code"] != "conflict" {
		t.Fatalf("a machine that isn't joined: %d %v", code, out)
	}
	e.joinDashboard()
	code, out = e.giveWebhook("https://example.com/api/webhooks/" + hookID + "/" + hookToken)
	if code != 400 || out["code"] != "invalid_request" {
		t.Fatalf("a URL that isn't a Discord webhook: %d %v", code, out)
	}
	noToken(t, "a refusal", out)
	if n := e.countRows(`SELECT COUNT(*) FROM dashboard_watch`); n != 0 {
		t.Fatalf("a refused webhook was kept: %d", n)
	}
}

func TestAMachineForgetsTheDashboardsWebhookWhenItLeaves(t *testing.T) {
	e, f := watchEnv(t)
	e.joinDashboard()
	e.linkIs(machinelink.LinkConnected, e.a.now())
	if code, out := e.giveWebhook(hookURL); code != 204 {
		t.Fatalf("the dashboard's webhook: %d %v", code, out)
	}
	// playkeeper leave, or the dashboard removing the machine, makes the
	// link forget the dashboard and stop.
	if err := os.Remove(e.cfg.LinkDashboardPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.cfg.LinkStatusPath()); err != nil {
		t.Fatal(err)
	}
	e.waitFor("the webhook forgotten", func() bool { return e.countRows(`SELECT COUNT(*) FROM dashboard_watch`) == 0 })
	if e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'dashboard_watch.cleared' AND actor = 'playkeeper' AND detail = 'this machine isn''t joined to a dashboard any more'`) != 1 {
		t.Fatal("forgetting the webhook isn't audited")
	}
	e.joinDashboard()
	e.later(10 * time.Minute)
	f.quietFor(e, 150*time.Millisecond)
}

func TestTheDashboardClearsItsWebhook(t *testing.T) {
	e, f := watchEnv(t)
	e.joinDashboard()
	e.linkIs(machinelink.LinkConnected, e.a.now())
	if code, out := e.giveWebhook(hookURL); code != 204 {
		t.Fatalf("the dashboard's webhook: %d %v", code, out)
	}
	for range 2 {
		if code, out := e.call("DELETE", "/v1/dashboard-watch?actor=playkeeper", nil); code != 204 {
			t.Fatalf("clearing the webhook: %d %v", code, out)
		}
	}
	if e.countRows(`SELECT COUNT(*) FROM dashboard_watch`) != 0 || e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'dashboard_watch.cleared' AND detail = ''`) != 1 {
		t.Fatal("the webhook is cleared, and that's audited once")
	}
	e.linkIs(machinelink.LinkRetrying, e.a.now())
	e.later(10 * time.Minute)
	f.quietFor(e, 150*time.Millisecond)
}

func TestOnlyTheAgentsSocketGivesTheDiscordWebhook(t *testing.T) {
	e, _ := newDiscordEnv(t)
	if code, out := e.call("GET", discordWebhookPath, nil); code != 200 || out["webhookUrl"] != "" {
		t.Fatalf("before Discord is connected: %d %v", code, out)
	}
	e.connectDiscord()
	if code, out := e.call("GET", discordWebhookPath, nil); code != 200 || out["webhookUrl"] != hookURL {
		t.Fatalf("with Discord connected: %d %v", code, out)
	}
	if !socketOnly["GET "+discordWebhookPath] {
		t.Fatal("a machine link can read the Discord webhook")
	}
	for _, r := range LinkRoutes() {
		if r.Pattern == discordWebhookPath {
			t.Fatalf("a machine link offers %s %s", r.Method, r.Pattern)
		}
	}
	if code, _ := e.call("DELETE", "/v1/discord?actor=admin", nil); code != 204 {
		t.Fatalf("disconnecting Discord: %d", code)
	}
	if _, out := e.call("GET", discordWebhookPath, nil); out["webhookUrl"] != "" {
		t.Fatalf("after Discord is disconnected: %v", out)
	}
}
