package panel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/config"
)

const watchHook = "https://discord.com/api/webhooks/1289345123456789012/pkTestToken_pkTestToken_pkTestToken_pkTestToken_pkTestToken_pkTestToken_"

// watchSyncing runs the joined machines' watch sync for the test, on its
// kicks alone.
func watchSyncing(t *testing.T, e *env) {
	t.Helper()
	was := watchSyncEvery
	watchSyncEvery = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.srv.runWatchSync(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		watchSyncEvery = was
	})
}

// discordWebhook has the dashboard's agent give url as Discord's webhook,
// "" while Discord isn't connected.
func (e *env) discordWebhook(url string) {
	e.reply("GET", "/v1/discord/webhook", `{"webhookUrl":"`+url+`"}`)
}

// watchLog is what a joined machine was told about the dashboard's webhook,
// in order.
type watchLog struct {
	mu  sync.Mutex
	got []string
}

func (l *watchLog) add(s string) {
	l.mu.Lock()
	l.got = append(l.got, s)
	l.mu.Unlock()
}

func (l *watchLog) told() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.got)
}

// listen has ra note the watch's requests in l.
func (l *watchLog) listen(ra *remoteAgent) {
	ra.handle("PUT /v1/dashboard-watch", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		l.add("give " + req["webhookUrl"] + " as " + req["actor"])
		w.WriteHeader(http.StatusNoContent)
	})
	ra.handle("DELETE /v1/dashboard-watch", func(w http.ResponseWriter, r *http.Request) {
		l.add("clear as " + r.URL.Query().Get("actor"))
		w.WriteHeader(http.StatusNoContent)
	})
}

func (l *watchLog) waitFor(t *testing.T, want ...string) {
	t.Helper()
	eventually(t, "the machine was told "+jsonOf(want), func() bool { return slices.Equal(l.told(), want) })
	time.Sleep(50 * time.Millisecond)
	if got := l.told(); !slices.Equal(got, want) {
		t.Fatalf("the machine was told %q, want %q", got, want)
	}
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

const (
	gaveHook    = "give " + watchHook + " as playkeeper"
	clearedHook = "clear as playkeeper"
)

func TestOnlyConfirmedMachinesHaveTheDashboardsWebhook(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	own := owner(t, e)
	e.discordWebhook(watchHook)
	j := joinForCustomersAs(t, e, own)
	var l watchLog
	l.listen(j.ra)
	watchSyncing(t, e)
	l.waitFor(t, clearedHook)

	path := "/api/machines/" + j.d.MachineID + "/customers"
	if r := e.do(t, "PUT", path, `{"on":true}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("the owner confirms the machine: %d %v", r.status, r.body)
	}
	l.waitFor(t, clearedHook, gaveHook)

	e.discordWebhook("")
	if r := e.do(t, "DELETE", "/api/discord", "", own.auth()); r.status != http.StatusOK {
		t.Fatalf("disconnecting Discord: %d %v", r.status, r.body)
	}
	l.waitFor(t, clearedHook, gaveHook, clearedHook)
	e.discordWebhook(watchHook)
	if r := e.do(t, "POST", "/api/discord/connect", `{"webhookUrl":"`+watchHook+`"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("connecting Discord: %d %v", r.status, r.body)
	}
	l.waitFor(t, clearedHook, gaveHook, clearedHook, gaveHook)

	if r := e.do(t, "PUT", path, `{"on":false}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("the owner stops the machine taking customers: %d %v", r.status, r.body)
	}
	l.waitFor(t, clearedHook, gaveHook, clearedHook, gaveHook, clearedHook)
}

func TestAMachineTheHetznerTokenConfirmedHasTheDashboardsWebhook(t *testing.T) {
	f := newFakeHetzner(t)
	e := newEnvConfig(t, func(c *config.Config) { withDomain(c); c.HetznerAPIURL = f.srv.URL }, nil)
	own := owner(t, e)
	e.discordWebhook(watchHook)
	j := joinForCustomersAs(t, e, own)
	if r := e.do(t, "PUT", "/api/hetzner", `{"token":"`+hetznerTestToken+`"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("watching Hetzner: %d %v", r.status, r.body)
	}
	var l watchLog
	l.listen(j.ra)
	watchSyncing(t, e)
	l.waitFor(t, clearedHook)
	f.inProject("fleet-1", "127.0.0.1")
	e.srv.checkStock(context.Background())
	if by := e.takesCustomersBy(t, j.d.MachineID); by != "hetzner:fleet-1" {
		t.Fatalf("the machine found in the Hetzner project, confirmed by %q", by)
	}
	l.waitFor(t, clearedHook, gaveHook)
}

// A machine that's away when Discord is disconnected clears the webhook
// once its link comes back.
func TestAMachineThatWasAwayCatchesUpOnTheDashboardsWebhook(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	own := owner(t, e)
	e.discordWebhook(watchHook)
	j := joinForCustomersAs(t, e, own)
	var l watchLog
	l.listen(j.ra)
	watchSyncing(t, e)
	if r := e.do(t, "PUT", "/api/machines/"+j.d.MachineID+"/customers", `{"on":true}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("the owner confirms the machine: %d %v", r.status, r.body)
	}
	l.waitFor(t, clearedHook, gaveHook)

	j.link.stop()
	eventually(t, "the machine is away", func() bool { return linkState(e.machineView(t, own.cookie, j.d.MachineID)) != "connected" })
	e.discordWebhook("")
	if r := e.do(t, "DELETE", "/api/discord", "", own.auth()); r.status != http.StatusOK {
		t.Fatalf("disconnecting Discord: %d %v", r.status, r.body)
	}
	time.Sleep(100 * time.Millisecond)
	if got := l.told(); !slices.Equal(got, []string{clearedHook, gaveHook}) {
		t.Fatalf("a machine that's away was told %q", got)
	}
	e.runLink(t, j.d, j.identity, j.ra)
	l.waitFor(t, clearedHook, gaveHook, clearedHook)
}
