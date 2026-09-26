package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/discord"
)

// The webhook the tests connect; the token is made up.
const (
	hookID    = "1289345123456789012"
	hookToken = "pkTestToken_pkTestToken_pkTestToken_pkTestToken_pkTestToken_pkTestToken_"
	hookURL   = "https://discord.com/api/webhooks/" + hookID + "/" + hookToken
)

// fakeHook stands in for Discord's webhook API for one webhook: Get
// Webhook, Execute Webhook with wait=true and Edit Webhook Message. The
// discord package's own tests check the requests in detail.
type fakeHook struct {
	srv *httptest.Server

	mu     sync.Mutex
	got    []hookRequest
	posted int
}

type hookRequest struct {
	Method, Path, Raw string
}

func startFakeHook(t *testing.T) *fakeHook {
	t.Helper()
	f := &fakeHook{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// client sends the agent's requests for discord.com to the fake, as
// DiscordURLEnv does.
func (f *fakeHook) client() *http.Client {
	u, _ := url.Parse(f.srv.URL)
	return &http.Client{Transport: discordRedirect{to: u}}
}

func (f *fakeHook) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.got = append(f.got, hookRequest{r.Method, r.URL.Path, string(raw)})
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	base := "/api/v10/webhooks/" + hookID + "/" + hookToken
	switch {
	case r.Host != "discord.com" || !strings.HasPrefix(r.URL.Path, "/api/v10/webhooks/"+hookID+"/"):
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message": "Unknown Webhook", "code": 10015}`)
	case r.URL.Path != base && !strings.HasPrefix(r.URL.Path, base+"/"):
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"message": "Invalid Webhook Token", "code": 50027}`)
	case r.Method == http.MethodGet && r.URL.Path == base:
		fmt.Fprintf(w, `{"id": %q, "name": "Playkeeper", "type": 1}`, hookID)
	case r.Method == http.MethodPost && r.URL.Path == base && r.URL.Query().Get("wait") == "true":
		f.mu.Lock()
		f.posted++
		id := fmt.Sprintf("12893459990000%05d", f.posted)
		f.mu.Unlock()
		fmt.Fprintf(w, `{"id": %q}`, id)
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, base+"/messages/"):
		fmt.Fprintf(w, `{"id": %q}`, strings.TrimPrefix(r.URL.Path, base+"/messages/"))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		io.WriteString(w, `{"message": "405: Method Not Allowed", "code": 0}`)
	}
}

// messages are the messages posted or edited so far whose body contains
// every one of want.
func (f *fakeHook) messages(want ...string) []hookRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []hookRequest
	for _, r := range f.got {
		if r.Method == http.MethodGet {
			continue
		}
		ok := true
		for _, w := range want {
			ok = ok && strings.Contains(r.Raw, w)
		}
		if ok {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeHook) waitMessage(e *agentEnv, want ...string) {
	e.t.Helper()
	e.waitFor("a Discord message with "+strings.Join(want, ", "), func() bool { return len(f.messages(want...)) > 0 })
}

// count is how often s occurs in the messages posted or edited so far;
// alerts that go out together share a message.
func (f *fakeHook) count(s string) int {
	n := 0
	for _, r := range f.messages() {
		n += strings.Count(r.Raw, s)
	}
	return n
}

// statusMessage is the live status message as last posted or edited.
func (f *fakeHook) statusMessage() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.got) - 1; i >= 0; i-- {
		if r := f.got[i]; r.Method == http.MethodPatch || r.Method == http.MethodPost && strings.Contains(r.Raw, `"flags":4096`) {
			return r.Raw
		}
	}
	return ""
}

// waitStatus fails the test unless the live status message shows want
// within d.
func (f *fakeHook) waitStatus(e *agentEnv, d time.Duration, want string) {
	e.t.Helper()
	for deadline := time.Now().Add(d); !strings.Contains(f.statusMessage(), want); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			e.t.Fatalf("the live status message did not show %q within %v: %s", want, d, f.statusMessage())
		}
	}
}

// newDiscordEnv is an agent that talks to the fake Discord.
func newDiscordEnv(t *testing.T) (*agentEnv, *fakeHook) {
	t.Helper()
	f := startFakeHook(t)
	e := newAgentEnv(t)
	e.stop()
	e.discordClient = f.client()
	e.start()
	return e, f
}

func (e *agentEnv) connectDiscord() map[string]any {
	e.t.Helper()
	code, out := e.call("POST", "/v1/discord/connect", map[string]any{"webhookUrl": hookURL, "host": "play.example.com", "actor": "admin"})
	if code != 200 {
		e.t.Fatalf("connect Discord: %d %v", code, out)
	}
	return out
}

// noToken fails the test if the webhook's token shows up in v.
func noToken(t *testing.T, what string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), hookToken) || strings.Contains(string(b), "webhooks/"+hookID) {
		t.Fatalf("%s gives away the webhook URL: %s", what, b)
	}
}

func TestDiscordConnectKeepsTheWebhookURLInTheAgent(t *testing.T) {
	e, f := newDiscordEnv(t)
	if code, out := e.call("GET", "/v1/discord", nil); code != 200 || out["connected"] != false {
		t.Fatalf("before connecting: %d %v", code, out)
	}
	out := e.connectDiscord()
	if out["connected"] != true || out["webhookName"] != "Playkeeper" || out["liveStatus"] != true || out["connectedAt"] == nil {
		t.Fatalf("connected: %v", out)
	}
	alerts, _ := json.Marshal(out["alerts"])
	if string(alerts) != `["crash","recovered","low_disk","backup_failed","update_available","join_requested"]` {
		t.Fatalf("default alerts: %s", alerts)
	}
	noToken(t, "the connect answer", out)
	_, got := e.call("GET", "/v1/discord", nil)
	noToken(t, "the settings", got)
	var n int
	e.a.db.QueryRow(`SELECT COUNT(*) FROM audit WHERE detail LIKE ? OR target LIKE ?`, "%"+hookToken+"%", "%"+hookToken+"%").Scan(&n)
	if n != 0 {
		t.Fatalf("the audit log recorded the webhook URL %d times", n)
	}
	if e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'discord.connect' AND result = 'succeeded' AND detail = ''`) != 1 {
		t.Fatal("connecting is audited")
	}
	// Live status is on at the first connect: one message lists the servers.
	f.waitMessage(e, "No servers yet.")
	e.waitFor("the status message id saved", func() bool {
		return e.countRows(`SELECT COUNT(*) FROM discord WHERE status_message_id != ''`) == 1
	})
	// A restarted agent keeps the connection.
	e.stop()
	e.start()
	if _, out := e.call("GET", "/v1/discord", nil); out["connected"] != true || out["webhookName"] != "Playkeeper" {
		t.Fatalf("after a restart: %v", out)
	}
}

func TestDiscordConnectExplainsBadWebhooks(t *testing.T) {
	e, _ := newDiscordEnv(t)
	for _, c := range []struct {
		url, code string
		status    int
	}{
		{"https://example.com/api/webhooks/" + hookID + "/" + hookToken, "discord_invalid_webhook_url", 400},
		{"https://discord.com/api/webhooks/1289345000000000001/" + hookToken, "discord_webhook_gone", 422},
		{"https://discord.com/api/webhooks/" + hookID + "/" + strings.Repeat("x", 72), "discord_webhook_rejected", 422},
	} {
		code, out := e.call("POST", "/v1/discord/connect", map[string]any{"webhookUrl": c.url, "actor": "admin"})
		if code != c.status || out["code"] != c.code || out["error"] == "" {
			t.Errorf("%s: %d %v", c.url, code, out)
		}
		noToken(t, "an error", out)
	}
	if e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'discord.connect' AND result = 'failed' AND detail = 'webhook_gone'`) != 1 {
		t.Fatal("a failed connect is audited with the reason only")
	}
	if _, out := e.call("GET", "/v1/discord", nil); out["connected"] != false {
		t.Fatalf("a webhook that doesn't work is not saved: %v", out)
	}
}

func TestDiscordSettingsTestMessageAndDisconnect(t *testing.T) {
	e, f := newDiscordEnv(t)
	if code, out := e.call("POST", "/v1/discord/test", map[string]any{"actor": "admin"}); code != 409 || out["code"] != "discord_not_connected" {
		t.Fatalf("test before connecting: %d %v", code, out)
	}
	e.connectDiscord()
	code, out := e.call("PUT", "/v1/discord", map[string]any{"alerts": []string{"crash", "backup_failed"}, "liveStatus": false, "actor": "admin"})
	alerts, _ := json.Marshal(out["alerts"])
	if code != 200 || string(alerts) != `["crash","backup_failed"]` || out["liveStatus"] != false {
		t.Fatalf("settings: %d %v", code, out)
	}
	if code, out := e.call("PUT", "/v1/discord", map[string]any{"alerts": []string{"crash", "everything"}, "actor": "admin"}); code != 400 || out["code"] != "discord_invalid_settings" {
		t.Fatalf("unknown alert: %d %v", code, out)
	}
	if code, out := e.call("POST", "/v1/discord/test", map[string]any{"actor": "admin"}); code != 200 || out["connected"] != true {
		t.Fatalf("test message: %d %v", code, out)
	}
	f.waitMessage(e, "Test message", "Discord is connected")
	if code, _ := e.call("DELETE", "/v1/discord?actor=admin", nil); code != 204 {
		t.Fatalf("disconnect: %d", code)
	}
	if _, out := e.call("GET", "/v1/discord", nil); out["connected"] != false || out["webhookName"] != nil {
		t.Fatalf("after disconnecting: %v", out)
	}
	if e.countRows(`SELECT COUNT(*) FROM discord WHERE webhook_url = '' AND status_message_id = ''`) != 1 {
		t.Fatal("disconnecting forgets the webhook")
	}
	// Reconnecting keeps the choices made before.
	out = e.connectDiscord()
	if alerts, _ := json.Marshal(out["alerts"]); string(alerts) != `["crash","backup_failed"]` || out["liveStatus"] != false {
		t.Fatalf("reconnected: %v", out)
	}
	for _, action := range []string{"discord.settings", "discord.test", "discord.disconnect"} {
		if e.countRows(`SELECT COUNT(*) FROM audit WHERE action = ? AND result = 'succeeded'`, action) == 0 {
			t.Errorf("%s is not audited", action)
		}
	}
}

func TestDiscordAlertsComeFromTheAgent(t *testing.T) {
	e, f := newDiscordEnv(t)
	e.connectDiscord()
	e.create()
	e.call("PUT", "/v1/discord", map[string]any{"alerts": []string{"crash", "recovered", "player_joined"}, "liveStatus": true, "actor": "admin"})
	e.fd.addLog("[12:01:00 INFO]: PkBotBuilder joined the game")
	f.waitMessage(e, "Player joined", "PkBotBuilder")
	e.fd.crash(137)
	f.waitMessage(e, "Server crashed", "Playkeeper is restarting it")
	e.waitFor("auto-restart", func() bool { return e.status().Phase == api.PhaseOnline })
	f.waitMessage(e, "Back online")
	// Alerts link to the server's page on the dashboard.
	f.waitMessage(e, "https://play.example.com:"+fmt.Sprint(e.cfg.PanelPort)+"/servers/")
}

// The alert when Playkeeper gives up restarting a server says so once, and
// says why the server crashed.
func TestDiscordGaveUpAlertSaysItOnceWithTheCause(t *testing.T) {
	e, f := newDiscordEnv(t)
	e.connectDiscord()
	e.create()
	for i := 1; i <= maxCrashes; i++ {
		e.waitFor("online before the crash", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
		e.fd.crash(137)
		e.waitFor(fmt.Sprintf("crash %d counted", i), func() bool { return e.crashEvents() == i })
	}
	f.waitMessage(e, "Server crashed and stays off")
	var got []string
	for _, r := range f.messages("Server crashed and stays off") {
		var msg struct {
			Embeds []struct{ Title, Description string }
		}
		if err := json.Unmarshal([]byte(r.Raw), &msg); err != nil {
			t.Fatal(err)
		}
		for _, em := range msg.Embeds {
			if em.Title == "Server crashed and stays off" {
				got = append(got, em.Description)
			}
		}
	}
	want := "**My server** kept crashing, so Playkeeper stopped restarting it. Open the dashboard to see what went wrong.\n\n" +
		"The server stopped unexpectedly \\(exit code 137\\) without shutting down cleanly.\n\n" +
		"[Open the dashboard](https://play.example.com:" + fmt.Sprint(e.cfg.PanelPort) + "/servers/my-server)"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("the alert says:\n%q\nwant:\n%q", got, want)
	}
	if st := e.status(); !strings.Contains(st.LastError, "Playkeeper stopped restarting it after 3 crashes in 15 minutes.") {
		t.Fatalf("the dashboard still says Playkeeper gave up: %q", st.LastError)
	}
}

// The live status message shows the server as it is, not as the last sample
// saw it: no sample runs here once the agent has started. Crashes, coming
// back online and Playkeeper giving up restarting each show within seconds,
// not after the minute between routine updates.
func TestDiscordLiveStatusShowsCrashesWithinSeconds(t *testing.T) {
	f := startFakeHook(t)
	e := newAgentEnv(t)
	e.stop()
	// Playkeeper restarts the server four seconds after a crash, long enough
	// for the crash to show first.
	e.discordClient, e.sampleInterval, e.crashBackoff = f.client(), time.Hour, []time.Duration{4 * time.Second}
	e.start()
	e.create()
	e.connectDiscord()
	const within = 4 * time.Second
	f.waitStatus(e, within, "** · Online")
	for i := 1; i <= maxCrashes; i++ {
		// A line after "Done", as a running server logs: the follower reads a
		// stopped container's last line again, and a "Done" read again counts
		// as a new start.
		e.fd.addLog("[12:00:05 INFO]: Timings Reset")
		e.fd.crash(137)
		f.waitStatus(e, within, "** · Crashed")
		if i == maxCrashes {
			break
		}
		e.waitFor("back online", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
		f.waitStatus(e, within, "** · Online")
	}
	e.waitFor("Playkeeper gave up restarting the server", func() bool {
		return strings.Contains(e.status().LastError, "stopped restarting")
	})
	f.waitStatus(e, 0, "** · Crashed")
}

// Right after a start or a restart, before a sample says who is playing,
// the live status message counts the slots the server's settings give it:
// "Online · 0 of 12", not "Online · 0" until the next routine update. No
// sample runs here once the agent has started.
func TestDiscordLiveStatusCountsSlotsBeforeTheFirstSample(t *testing.T) {
	f := startFakeHook(t)
	e := newAgentEnv(t)
	e.stop()
	e.discordClient, e.sampleInterval, e.crashBackoff = f.client(), time.Hour, []time.Duration{4 * time.Second}
	e.start()
	e.createWith(map[string]any{"maxPlayers": 12})
	e.connectDiscord()
	const within = 4 * time.Second
	f.waitStatus(e, within, "** · Online · 0 of 12")
	e.fd.addLog("[12:00:05 INFO]: Timings Reset")
	e.fd.crash(137)
	f.waitStatus(e, within, "** · Crashed")
	e.waitFor("back online", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
	f.waitStatus(e, within, "** · Online · 0 of 12")
}

// An exit the reconcile loop has yet to handle shows in Discord as what the
// loop will count it as, so a crash does not show as a stop first. The
// reconcile loop does not run here once the agent has started.
func TestDiscordShowsAnExitAsTheReconcileLoopWillCountIt(t *testing.T) {
	e := newAgentEnv(t)
	e.stop()
	e.reconcileInterval = time.Hour
	e.start()
	e.create()
	ctx := context.Background()
	state := func() discord.State { return e.srv().discordStatus(ctx).State }
	logRead := func() bool {
		s := e.srv()
		c, err := s.docker.ContainerInspect(ctx, s.containerName())
		fin, _ := c.State.Finished()
		s.mu.Lock()
		defer s.mu.Unlock()
		return err == nil && !c.State.Running && !s.followEnded[c.ID].Before(fin)
	}
	if st := state(); st != discord.StateOnline {
		t.Fatalf("online: %s", st)
	}
	e.fd.externalStop()
	e.waitFor("the log read to its end", logRead)
	if st := state(); st != discord.StateOffline {
		t.Fatalf("a clean shutdown is not a crash: %s", st)
	}
	if code, out := e.call("POST", e.sp("/start"), map[string]any{"actor": "admin"}); code != 202 {
		t.Fatalf("start: %d %v", code, out)
	}
	e.waitFor("online again", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
	e.fd.crash(137)
	e.waitFor("the crash shown", func() bool { return state() == discord.StateCrashed })
	if n := e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'server_crashed'`); n != 0 {
		t.Fatalf("the reconcile loop counted the crash (%d), so this test shows nothing", n)
	}
}

func TestBackupFailureIsPostedToDiscord(t *testing.T) {
	e, f := newDiscordEnv(t)
	e.connectDiscord()
	e.create()
	world := filepath.Join(e.dataDir(), "world")
	deep := filepath.Join(world, strings.Repeat("a", 250), strings.Repeat("b", 250), strings.Repeat("c", 250), strings.Repeat("d", 250))
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "r.mca"), []byte("region"), 0o640); err != nil {
		t.Fatal(err)
	}
	code, out := e.call("POST", e.sp("/backups"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("backup: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpFailed {
		t.Fatalf("the backup should fail: %+v", op)
	}
	f.waitMessage(e, "Backup failed", "a restore would refuse it")
	e.waitFor("server running again", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
}

func TestLowDiskAlertFiresOnceAndRearms(t *testing.T) {
	e, f := newDiscordEnv(t)
	e.connectDiscord()
	e.diskFree.Store(4 << 30)
	f.waitMessage(e, "Low disk space")
	e.diskFree.Store(5<<30 + 512<<20)
	time.Sleep(500 * time.Millisecond)
	if n := len(f.messages("Low disk space")); n != 1 {
		t.Fatalf("%d low disk alerts while the disk stayed nearly full", n)
	}
	e.a.disc.mu.Lock()
	still := e.a.disc.lowDisk
	e.a.disc.mu.Unlock()
	if !still {
		t.Fatal("the alert rearms only once the space is back above 6 GiB")
	}
	e.diskFree.Store(7 << 30)
	e.waitFor("the alert rearmed", func() bool {
		e.a.disc.mu.Lock()
		defer e.a.disc.mu.Unlock()
		return !e.a.disc.lowDisk
	})
}

func TestUpdateAlertGoesOutOncePerVersion(t *testing.T) {
	e, f := newDiscordEnv(t)
	e.a.alertUpdate("0.9.0")
	e.connectDiscord()
	e.a.alertUpdate("0.9.0")
	e.a.alertUpdate("0.9.0")
	f.waitMessage(e, "Playkeeper update available")
	e.stop()
	e.start()
	e.a.alertUpdate("0.9.0")
	time.Sleep(300 * time.Millisecond)
	if n := len(f.messages("Playkeeper update available")); n != 1 {
		t.Fatalf("%d alerts about one release", n)
	}
}

func TestMinecraftUpdateAlertGoesOutOncePerVersion(t *testing.T) {
	e, f := newDiscordEnv(t)
	ctx := t.Context()
	e.addIdleServer()
	e.addIdleServer()
	sc, _ := e.srv().serverConfig()
	if err := e.srv().saveServerConfig(withBuild(*sc, api.CatalogEntry{ID: "paper-26.2", MinecraftVersion: "26.2", PaperBuild: 129})); err != nil {
		t.Fatal(err)
	}
	e.a.alertMinecraftUpdates(ctx)
	if n := e.countRows(`SELECT COUNT(*) FROM servers WHERE minecraft_update_alerted != ''`); n != 0 {
		t.Fatalf("%d servers count as told before Discord was connected", n)
	}
	e.connectDiscord()
	e.a.alertMinecraftUpdates(ctx)
	e.a.alertMinecraftUpdates(ctx)
	f.waitMessage(e, "**My server** can be updated to Minecraft 26.2 on the Settings tab.")
	e.stop()
	e.start()
	e.a.alertMinecraftUpdates(ctx)
	time.Sleep(300 * time.Millisecond)
	if n := f.count("Minecraft update available"); n != 1 {
		t.Fatalf("%d Minecraft update alerts, want one: for My server, about 26.2 rather than the experimental 26.3, and none for My server 2, which runs 26.2", n)
	}

	e.stop()
	e.fill.set("", []fillVersionSpec{
		{"26.3", "SUPPORTED", []fillBuildSpec{{41, "ALPHA"}, {57, "STABLE"}}},
		{"26.2", "SUPPORTED", []fillBuildSpec{{129, "STABLE"}}},
		{"26.1.2", "UNSUPPORTED", []fillBuildSpec{{74, "STABLE"}}},
	})
	e.start()
	e.a.alertMinecraftUpdates(ctx)
	e.a.alertMinecraftUpdates(ctx)
	f.waitMessage(e, "**My server** can be updated to Minecraft 26.3 on the Settings tab.")
	f.waitMessage(e, "**My server 2** can be updated to Minecraft 26.3 on the Settings tab.")
	time.Sleep(300 * time.Millisecond)
	if n := f.count("Minecraft update available"); n != 3 {
		t.Fatalf("%d Minecraft update alerts after 26.3 got a stable build, want 3: one more for each server", n)
	}
}

func TestMinecraftUpdateAlertsAreForPaperServersOnly(t *testing.T) {
	e, f := newDiscordEnv(t)
	e.connectDiscord()
	fabric := e.addIdleServer()
	if _, err := e.a.db.Exec(`UPDATE servers SET type = 'fabric' WHERE id = ?`, fabric); err != nil {
		t.Fatal(err)
	}
	e.addIdleServer()
	e.a.alertMinecraftUpdates(t.Context())
	f.waitMessage(e, "**My server 2** can be updated to Minecraft 26.2 on the Settings tab.")
	time.Sleep(300 * time.Millisecond)
	if n := f.count("Minecraft update available"); n != 1 {
		t.Fatalf("%d Minecraft update alerts: PaperMC's versions are for the Paper server, not the Fabric one", n)
	}
}

func TestDiscordNotifyTakesJoinRequestsOnly(t *testing.T) {
	e, f := newDiscordEnv(t)
	e.connectDiscord()
	e.addIdleServer()
	for _, c := range []struct {
		body   map[string]any
		status int
	}{
		{map[string]any{"kind": "player_joined", "serverId": e.sid, "player": "mara_k", "actor": "panel"}, 400},
		{map[string]any{"kind": "join_requested", "serverId": e.sid, "player": "mara k", "actor": "panel"}, 400},
		{map[string]any{"kind": "join_requested", "serverId": "zzzzzzzzzz", "player": "mara_k", "actor": "panel"}, 404},
		{map[string]any{"kind": "join_requested", "serverId": e.sid, "player": "mara_k"}, 400},
	} {
		if code, out := e.call("POST", "/v1/discord/notify", c.body); code != c.status {
			t.Errorf("%v: %d %v", c.body, code, out)
		}
	}
	if code, out := e.call("POST", "/v1/discord/notify", map[string]any{"kind": "join_requested", "serverId": e.sid, "player": "mara_k", "actor": "panel"}); code != 204 {
		t.Fatalf("join request: %d %v", code, out)
	}
	f.waitMessage(e, "Join request", `mara\\_k`, "wants to join")
}

func TestDiscordNotifyTakesTwoFactorChangesWithEveryAlertOff(t *testing.T) {
	e, f := newDiscordEnv(t)
	e.connectDiscord()
	if code, out := e.call("PUT", "/v1/discord", map[string]any{"alerts": []string{}, "liveStatus": false, "actor": "admin"}); code != 200 {
		t.Fatalf("turn every alert off: %d %v", code, out)
	}
	for _, c := range []struct {
		body   map[string]any
		status int
	}{
		{map[string]any{"kind": "two_factor_changed", "member": "", "on": true, "admin": true, "actor": "panel"}, 400},
		{map[string]any{"kind": "two_factor_changed", "member": "mara k", "on": true, "admin": true, "actor": "panel"}, 400},
		{map[string]any{"kind": "two_factor_changed", "member": "@everyone", "on": true, "admin": true, "actor": "panel"}, 400},
		{map[string]any{"kind": "two_factor_changed", "member": "mara", "on": true, "admin": true}, 400},
		{map[string]any{"kind": "two_factor", "member": "mara", "on": true, "admin": true, "actor": "panel"}, 400},
		{map[string]any{"kind": "admin_confirmed", "member": "@everyone", "actor": "juno"}, 400},
		{map[string]any{"kind": "admin_confirmed", "member": "mara", "actor": "invite:abc"}, 400},
	} {
		if code, out := e.call("POST", "/v1/discord/notify", c.body); code != c.status {
			t.Errorf("%v: %d %v", c.body, code, out)
		}
	}
	if code, out := e.call("POST", "/v1/discord/notify", map[string]any{"kind": "two_factor_changed", "member": "mara", "on": true, "admin": true, "actor": "panel"}); code != 204 {
		t.Fatalf("two-factor change: %d %v", code, out)
	}
	f.waitMessage(e, "Two-factor sign-in turned on", "**mara**", "confirm them on the Team page")
	if n := len(f.messages("Two-factor sign-in")); n != 1 {
		t.Fatalf("%d messages for one change", n)
	}
	if code, out := e.call("POST", "/v1/discord/notify", map[string]any{"kind": "admin_confirmed", "member": "mara", "actor": "juno"}); code != 204 {
		t.Fatalf("confirmation: %d %v", code, out)
	}
	f.waitMessage(e, "Admin rights confirmed", "**juno** gave **mara** Admin rights")
}

func TestDiscordTestEndpointMustBeOnThisMachine(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	for _, bad := range []string{
		"http://203.0.113.7:8090", "http://example.com:8090", "http://127.0.0.1", "http://user:pw@127.0.0.1:8090",
		"http://127.0.0.1:8090/api", "http://127.0.0.1:8090?to=discord", "ftp://127.0.0.1:8090", "127.0.0.1:8090",
	} {
		t.Setenv(DiscordURLEnv, bad)
		if _, err := discordClientFromEnv(log); err == nil {
			t.Errorf("%s was accepted", bad)
		}
		if err := (&Agent{log: log}).initDiscord(); err == nil {
			t.Errorf("the agent started with %s", bad)
		}
	}
	t.Setenv(DiscordURLEnv, "")
	if c, err := discordClientFromEnv(log); c != nil || err != nil {
		t.Fatalf("unset: %v %v", c, err)
	}
	t.Setenv(DiscordURLEnv, "http://[::1]:8090")
	c, err := discordClientFromEnv(log)
	if err != nil || c == nil {
		t.Fatalf("loopback: %v", err)
	}
	if _, err := c.Get("https://example.com/"); err == nil || !strings.Contains(err.Error(), "only requests for discord.com") {
		t.Fatalf("the test endpoint only stands in for discord.com: %v", err)
	}
}
