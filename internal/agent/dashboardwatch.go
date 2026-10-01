package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/discord"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
)

// A joined machine's watch on its dashboard (the fleet plan's section 5).
// The dashboard gives each joined machine it confirmed, by the owner or the
// Hetzner token, its Discord webhook over the machine link, and tells it to
// clear the webhook when Discord is disconnected or the machine stops being
// confirmed. The machine keeps it in agent.db, which only root can read, and
// posts with it only that it can't reach the dashboard, once its link has
// been down for dashboardDownAfter, and that it reaches it again. A machine
// that isn't joined to a dashboard any more, after playkeeper leave or once
// the dashboard removed it, forgets the webhook, and so does one that has
// joined again since, that dashboard or another.

// discordWebhookPath gives the dashboard's own panel the webhook's URL, to
// pass on to the machines it confirmed; it's socket-only.
const discordWebhookPath = "/v1/discord/webhook"

const (
	// dashboardDownAfter is how long the link is down before the machine
	// posts, as long as the dashboard waits to post a machine off it.
	dashboardDownAfter = 5 * time.Minute
	// linkQuietAfter is how long without the dashboard's heartbeat, which
	// comes every 15 seconds, a link that says it's connected counts as down.
	linkQuietAfter = 2 * time.Minute
	// maxLinkStatusBytes bounds the link's state file the watch reads.
	maxLinkStatusBytes = 64 << 10
)

type dashboardWatch struct {
	n *discord.Notifier

	mu sync.Mutex
	// set says the machine has the dashboard's webhook, and join is the join
	// it came with (dashboardJoin).
	set  bool
	join string
	// downSince is when the dashboard was last heard before the link went
	// down, zero while it's up, and posted whether the machine has posted
	// that it can't reach the dashboard since.
	downSince time.Time
	posted    bool
}

// dashboardJoin names a join: the dashboard's key and the id it gave this
// machine, which a machine that joins again, that dashboard or another,
// doesn't have.
func dashboardJoin(d machinelink.Dashboard) string { return d.Fingerprint() + " " + d.MachineID }

// initDashboardWatch loads the dashboard's webhook, if the dashboard gave
// this machine one, and makes the watch's notifier; Start runs it. Only
// the watch's two kinds are posted: no other alert, and no status message.
func (a *Agent) initDashboardWatch() error {
	var s discord.Settings
	var raw, key, machineID string
	err := a.db.QueryRow(`SELECT webhook_url, dashboard_key, machine_id FROM dashboard_watch WHERE id = 1`).Scan(&raw, &key, &machineID)
	switch {
	case err == nil:
		if w, err := discord.ParseWebhookURL(raw); err == nil {
			s.Webhook, a.watch.set, a.watch.join = w, true, key+" "+machineID
		}
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	a.watch.n = discord.New(discord.Options{Settings: s, Client: a.disc.client, Now: a.now, Logger: a.log})
	return nil
}

// hDiscordWebhook gives the dashboard's own panel the webhook's URL, or ""
// while Discord isn't connected.
func (a *Agent) hDiscordWebhook(w http.ResponseWriter, r *http.Request) {
	a.disc.mu.Lock()
	url := a.disc.settings.Webhook.SecretURL()
	a.disc.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"webhookUrl": url})
}

// hDashboardWatchSet keeps the dashboard's webhook for the watch, with the
// join it came with. A machine that isn't joined to a dashboard refuses it.
func (a *Agent) hDashboardWatchSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Actor      string `json:"actor"`
		WebhookURL string `json:"webhookUrl"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	d, err := machinelink.LoadDashboard(a.cfg.LinkDashboardPath())
	if err != nil {
		writeError(w, errConflict("This machine isn't joined to a dashboard, so it keeps no dashboard's webhook.", ""))
		return
	}
	wh, err := discord.ParseWebhookURL(req.WebhookURL)
	if err != nil {
		writeError(w, errInvalid("That isn't a Discord webhook URL."))
		return
	}
	a.watch.mu.Lock()
	defer a.watch.mu.Unlock()
	res, err := a.db.Exec(`INSERT INTO dashboard_watch(id, webhook_url, dashboard_key, machine_id, set_at) VALUES(1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET webhook_url = excluded.webhook_url, dashboard_key = excluded.dashboard_key, machine_id = excluded.machine_id, set_at = excluded.set_at
		WHERE webhook_url != excluded.webhook_url OR dashboard_key != excluded.dashboard_key OR machine_id != excluded.machine_id`,
		wh.SecretURL(), d.Fingerprint(), d.MachineID, a.now().UnixMilli())
	if err != nil {
		writeError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		a.audit(actor, "dashboard_watch.set", "discord", "succeeded", "")
	}
	a.watch.set, a.watch.join = true, dashboardJoin(d)
	a.watch.n.SetSettings(discord.Settings{Webhook: wh})
	w.WriteHeader(http.StatusNoContent)
}

// hDashboardWatchClear forgets the dashboard's webhook.
func (a *Agent) hDashboardWatchClear(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	if err := a.clearDashboardWatch(actor, ""); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// clearDashboardWatch forgets the dashboard's webhook, auditing it as actor
// did, for why, when there was one.
func (a *Agent) clearDashboardWatch(actor, why string) error {
	a.watch.mu.Lock()
	defer a.watch.mu.Unlock()
	return a.clearDashboardWatchLocked(actor, why)
}

// clearDashboardWatchLocked is clearDashboardWatch with a.watch.mu held.
func (a *Agent) clearDashboardWatchLocked(actor, why string) error {
	res, err := a.db.Exec(`DELETE FROM dashboard_watch WHERE id = 1`)
	if err != nil {
		return err
	}
	a.watch.set, a.watch.join, a.watch.downSince, a.watch.posted = false, "", time.Time{}, false
	a.watch.n.SetSettings(discord.Settings{})
	if n, _ := res.RowsAffected(); n > 0 {
		a.audit(actor, "dashboard_watch.cleared", "discord", "succeeded", why)
	}
	return nil
}

// watchDashboard looks at the link every DashboardWatchInterval.
func (a *Agent) watchDashboard(ctx context.Context) {
	every := a.opts.DashboardWatchInterval
	if every <= 0 {
		every = 30 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.lookAtDashboard()
		}
	}
}

// lookAtDashboard posts that the dashboard can't be reached once the link
// has been down for dashboardDownAfter, and that it's reachable again once
// the link is back, while the machine has the dashboard's webhook. A
// machine that isn't joined any more, or has joined again since the
// webhook came, forgets it.
func (a *Agent) lookAtDashboard() {
	a.watch.mu.Lock()
	set := a.watch.set
	a.watch.mu.Unlock()
	if !set {
		return
	}
	d, err := machinelink.LoadDashboard(a.cfg.LinkDashboardPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		a.log.Warn("the watch on the dashboard can't read the dashboard this machine joined", "err", err)
		return
	}
	now := a.now()
	var up bool
	var heard time.Time
	if err == nil {
		up, heard = linkUp(a.cfg.LinkStatusPath(), now)
	}
	a.watch.mu.Lock()
	defer a.watch.mu.Unlock()
	if !a.watch.set {
		return
	}
	why := ""
	switch {
	case err != nil:
		why = "this machine isn't joined to a dashboard any more"
	case dashboardJoin(d) != a.watch.join:
		why = "this machine joined a dashboard again"
	}
	if why != "" {
		if err := a.clearDashboardWatchLocked("playkeeper", why); err != nil {
			a.log.Error("forget the dashboard's webhook", "err", err)
		}
		return
	}
	a.watch.n.SetServer(discord.ServerInfo{DashboardURL: "https://" + d.Address})
	if up {
		if a.watch.posted {
			a.watch.n.Notify(discord.DashboardBack(d.Name, minutesSince(a.watch.downSince, now)))
		}
		a.watch.downSince, a.watch.posted = time.Time{}, false
		return
	}
	if a.watch.downSince.IsZero() {
		a.watch.downSince = now
		if !heard.IsZero() && heard.Before(now) {
			a.watch.downSince = heard
		}
	}
	if !a.watch.posted && now.Sub(a.watch.downSince) >= dashboardDownAfter {
		a.watch.n.Notify(discord.DashboardDown(d.Name, minutesSince(a.watch.downSince, now)))
		a.watch.posted = true
	}
}

// linkUp reads the state the link keeps at path: whether it's connected and
// heard the dashboard's heartbeat within linkQuietAfter, and when it last
// heard it (zero if it never did). A link that isn't running keeps none.
func linkUp(path string, now time.Time) (bool, time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return false, time.Time{}
	}
	defer f.Close()
	var st machinelink.LinkStatus
	if err := json.NewDecoder(io.LimitReader(f, maxLinkStatusBytes)).Decode(&st); err != nil {
		return false, time.Time{}
	}
	return st.State == machinelink.LinkConnected && now.Sub(st.LastSeen) < linkQuietAfter, st.LastSeen
}

// minutesSince is the whole minutes from t to now, at least 1.
func minutesSince(t, now time.Time) int {
	return max(1, int(now.Sub(t)/time.Minute))
}
