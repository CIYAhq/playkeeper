package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/discord"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// Discord alerts and the live status message. The agent runs the notifier,
// so alerts about crashes still go out while the panel is down. One webhook
// serves the whole machine: every alert names its server, and the live
// status message lists every server.

// DiscordURLEnv sends the agent's Discord requests to a fake webhook
// endpoint on this machine, for the end-to-end tests only. Anything but a
// loopback address is refused, and the agent logs a warning while it is set.
const DiscordURLEnv = "PLAYKEEPER_E2E_DISCORD_URL_UNSAFE"

// Discord hears about low disk space below the Overview's warning threshold,
// and again only after the space went back above lowDiskRearm.
const (
	lowDiskAlert = 5 << 30
	lowDiskRearm = 6 << 30
)

type discordState struct {
	n      *discord.Notifier
	client *http.Client

	mu          sync.Mutex
	settings    discord.Settings
	name        string
	connectedAt time.Time
	host        string
	lowDisk     bool
}

// initDiscord loads the saved settings and makes the notifier; Start runs it.
func (a *Agent) initDiscord() error {
	a.disc.client = a.opts.DiscordClient
	if a.disc.client == nil {
		c, err := discordClientFromEnv(a.log)
		if err != nil {
			return err
		}
		a.disc.client = c
	}
	s := discord.DefaultSettings()
	var raw, name, alerts, msgID, host string
	var live int
	var connected int64
	err := a.db.QueryRow(`SELECT webhook_url, webhook_name, alerts, live_status, status_message_id, public_host, connected_at FROM discord WHERE id = 1`).
		Scan(&raw, &name, &alerts, &live, &msgID, &host, &connected)
	switch {
	case err == nil:
		if raw != "" {
			if w, err := discord.ParseWebhookURL(raw); err == nil {
				s.Webhook = w
			} else {
				a.log.Warn("the saved Discord webhook is not valid; reconnect Discord in Settings")
			}
		}
		s.Alerts, s.LiveStatus, s.StatusMessageID = discord.ParseAlerts(alerts), live == 1, msgID
		a.disc.name, a.disc.host = name, host
		if connected > 0 {
			a.disc.connectedAt = time.UnixMilli(connected).UTC()
		}
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	a.disc.settings = s
	a.disc.n = discord.New(discord.Options{
		Settings: s, Server: a.discordDashboard(), Client: a.disc.client, Now: a.now, Logger: a.log,
		StatusGap: a.opts.DiscordStatusGap,
		OnStatusMessage: func(id string) {
			if _, err := a.db.Exec(`UPDATE discord SET status_message_id = ? WHERE id = 1`, id); err != nil {
				a.log.Error("save the Discord status message", "err", err)
			}
		},
	})
	return nil
}

// discordClientFromEnv is the client for DiscordURLEnv, or nil for the
// package's own client that talks to discord.com.
func discordClientFromEnv(log *slog.Logger) (*http.Client, error) {
	raw := os.Getenv(DiscordURLEnv)
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Port() == "" {
		return nil, fmt.Errorf("%s must look like http://127.0.0.1:8090", DiscordURLEnv)
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err != nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("%s must point at this machine (127.0.0.1 or ::1), not %s", DiscordURLEnv, u.Hostname())
	}
	log.Warn("Discord requests go to a test endpoint on this machine, not to Discord", "env", DiscordURLEnv, "endpoint", u.Host)
	return &http.Client{Timeout: 20 * time.Second, Transport: discordRedirect{to: u}}, nil
}

// discordRedirect sends requests for discord.com to the test endpoint.
type discordRedirect struct{ to *url.URL }

func (d discordRedirect) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "discord.com" {
		return nil, errors.New("only requests for discord.com go to the test endpoint")
	}
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host, r.Host = d.to.Scheme, d.to.Host, "discord.com"
	return http.DefaultTransport.RoundTrip(r)
}

// discordHost is the host players and the dashboard are reached at: the
// configured domain, or the host the dashboard was opened with.
func (a *Agent) discordHost() string {
	if a.cfg.Domain != "" {
		return a.cfg.Domain
	}
	a.disc.mu.Lock()
	defer a.disc.mu.Unlock()
	return a.disc.host
}

// discordBase is the dashboard's https address, or "" when it isn't known.
func (a *Agent) discordBase() string {
	host := a.discordHost()
	if host == "" {
		return ""
	}
	if a.cfg.PanelPort == 443 {
		if strings.Contains(host, ":") {
			return "https://[" + host + "]"
		}
		return "https://" + host
	}
	return "https://" + net.JoinHostPort(host, strconv.Itoa(a.cfg.PanelPort))
}

func (a *Agent) discordDashboard() discord.ServerInfo {
	return discord.ServerInfo{DashboardURL: a.discordBase()}
}

func (a *Agent) discordConnected() bool {
	a.disc.mu.Lock()
	defer a.disc.mu.Unlock()
	return !a.disc.settings.Webhook.IsZero()
}

// discordInfo describes the server in its alerts and status line.
func (s *server) discordInfo() discord.ServerInfo {
	info := discord.ServerInfo{ID: s.id, Name: s.name()}
	if host := s.discordHost(); host != "" {
		if addr, err := invites.JoinAddress(host, s.gamePort); err == nil {
			info.Address = addr
		}
	}
	if base := s.discordBase(); base != "" {
		if row, err := s.row(); err == nil {
			info.DashboardURL = base + "/servers/" + row.Slug
		}
	}
	if sc, _ := s.serverConfig(); sc != nil {
		info.Version, info.MOTD = sc.MinecraftVersion, sc.MOTD
	}
	return info
}

// alert posts e about this server if Discord is connected and e's kind on.
func (s *server) alert(e discord.Event) {
	if !s.discordConnected() {
		return
	}
	e.Server = s.discordInfo()
	s.disc.n.Notify(e)
}

// discordStatus is the server's line in the live status message: its state
// now, as the dashboard shows it, and who is playing as last sampled.
func (s *server) discordStatus(ctx context.Context) discord.Status {
	sc, _ := s.serverConfig()
	busy := s.busy()
	op := s.currentOp()
	if op == nil {
		op = s.machineOp()
	}
	c, err := s.docker.ContainerInspect(ctx, s.containerName())
	s.mu.Lock()
	crashed := s.crashed || err == nil && !busy && s.pendingCrash(c)
	phase := observedPhase(c, err, sc != nil, s.runPhase, crashed, s.softwareChanged != nil, op)
	players := s.players
	s.mu.Unlock()
	st := discord.Status{State: discord.StateOffline}
	switch phase {
	case api.PhaseOnline:
		st.State = discord.StateOnline
		if players != nil && s.fresh(players.At) {
			st.PlayersOnline, st.MaxPlayers, st.Players = players.Online, players.Max, players.Names
		}
		// Right after a start, before the first sample, the slots are the
		// server's setting, so the line reads "0 of 10" rather than "0".
		if st.MaxPlayers == 0 && sc != nil {
			st.MaxPlayers = sc.MaxPlayers
		}
	case api.PhaseCrashed:
		st.State = discord.StateCrashed
	case api.PhasePulling, api.PhaseStartingContainer, api.PhaseDownloading, api.PhaseStarting, api.PhasePreparingWorld:
		st.State = discord.StateStarting
	case api.PhaseNotCreated, api.PhaseStopped, api.PhaseStopping, api.PhaseDockerUnavailable:
	}
	return st
}

// pendingCrash reports whether the server's container c exited in a way the
// reconcile loop counts as a crash on its next pass, which may be seconds
// away: the exit is not handled yet, its log has been read to the end, and
// it came while the agent was running, unasked, without the server logging a
// clean shutdown. The caller holds s.mu.
func (s *server) pendingCrash(c docker.ContainerJSON) bool {
	if c.State.Running {
		return false
	}
	fin, _ := c.State.Finished()
	if last, ok := s.handledExit[c.ID]; ok && last.Equal(fin) || s.followEnded[c.ID].Before(fin) {
		return false
	}
	return !fin.Before(s.started) && !s.intentional[c.ID] && !s.stoppedCleanly()
}

func (a *Agent) discordBoard(ctx context.Context) discord.Board {
	var b discord.Board
	for _, s := range a.serverList() {
		if sc, _ := s.serverConfig(); sc == nil {
			continue
		}
		b.Servers = append(b.Servers, discord.BoardServer{Server: s.discordInfo(), Status: s.discordStatus(ctx)})
	}
	return b
}

// discordLoop keeps the live status message and the low disk alert current.
// It looks at the servers as often as the reconcile loop does, so a server
// that comes online, stops or crashes shows in Discord within seconds.
func (a *Agent) discordLoop(ctx context.Context) {
	t := time.NewTicker(a.opts.ReconcileInterval)
	defer t.Stop()
	for {
		if a.discordConnected() {
			a.checkLowDisk()
			a.disc.n.UpdateBoard(a.discordBoard(ctx))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *Agent) checkLowDisk() {
	free, _, err := a.opts.DiskUsage(a.cfg.DataDir)
	if err != nil {
		return
	}
	a.disc.mu.Lock()
	was := a.disc.lowDisk
	switch {
	case free < lowDiskAlert:
		a.disc.lowDisk = true
	case free >= lowDiskRearm:
		a.disc.lowDisk = false
	}
	now := a.disc.lowDisk
	a.disc.mu.Unlock()
	if now && !was {
		a.disc.n.Notify(discord.LowDisk(free))
	}
}

// alertUpdate posts a new Playkeeper release once per version.
func (a *Agent) alertUpdate(version string) {
	if version == "" || !a.discordConnected() {
		return
	}
	res, err := a.db.Exec(`UPDATE discord SET update_alerted = ? WHERE id = 1 AND update_alerted != ?`, version, version)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n == 1 {
		a.disc.n.Notify(discord.UpdateAvailable(version))
	}
}

// alertMinecraftUpdates posts, once per version for each server, that a
// newer Minecraft version is out: the update its Settings tab offers. Each
// server type's list is fetched once, and only when a server of that type is
// there to check.
func (a *Agent) alertMinecraftUpdates(ctx context.Context) {
	if !a.discordConnected() {
		return
	}
	catalogs := map[string][]api.CatalogEntry{}
	for _, s := range a.serverList() {
		sc, _ := s.serverConfig()
		if sc == nil || sc.MinecraftVersion == "" {
			continue
		}
		typ := configType(*sc)
		versions, fetched := catalogs[typ]
		if !fetched {
			versions, _, _ = a.typeCatalog(ctx, typ)
			catalogs[typ] = versions
		}
		e, ok := newerStable(*sc, versions)
		if !ok {
			continue
		}
		res, err := a.db.Exec(`UPDATE servers SET minecraft_update_alerted = ? WHERE id = ? AND minecraft_update_alerted != ?`, e.MinecraftVersion, s.id, e.MinecraftVersion)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n == 1 {
			s.alert(discord.MinecraftUpdateAvailable(e.MinecraftVersion))
		}
	}
}

func (a *Agent) discordSettings() api.DiscordSettings {
	a.disc.mu.Lock()
	s := a.disc.settings
	out := api.DiscordSettings{Connected: !s.Webhook.IsZero(), LiveStatus: s.LiveStatus, Alerts: []string{}, Kinds: []string{}}
	if out.Connected {
		out.WebhookName = a.disc.name
		if !a.disc.connectedAt.IsZero() {
			t := a.disc.connectedAt
			out.ConnectedAt = &t
		}
	}
	a.disc.mu.Unlock()
	for _, k := range s.Alerts {
		out.Alerts = append(out.Alerts, string(k))
	}
	for _, k := range discord.Kinds() {
		out.Kinds = append(out.Kinds, string(k))
	}
	d := a.disc.n.Delivery()
	out.Delivery = api.DiscordDelivery{Code: d.Code, Msg: d.Msg, Hint: d.Hint, RetryAfterSeconds: d.RetryAfterSeconds, Stopped: d.Stopped}
	if !d.Sent.IsZero() {
		t := d.Sent.UTC()
		out.Delivery.Sent = &t
	}
	if !d.Failed.IsZero() {
		t := d.Failed.UTC()
		out.Delivery.Failed = &t
	}
	return out
}

func (a *Agent) hDiscord(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.discordSettings())
}

// cleanHost keeps a dashboard host name the panel passed on, or "".
func cleanHost(h string) string {
	h = strings.TrimSpace(h)
	if h == "" || len(h) > 253 {
		return ""
	}
	if _, err := invites.JoinAddress(h, 25565); err != nil {
		return ""
	}
	return h
}

func (a *Agent) hDiscordConnect(w http.ResponseWriter, r *http.Request) {
	var req api.DiscordConnectRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	wh, err := discord.ParseWebhookURL(req.WebhookURL)
	if err != nil {
		writeDiscordErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	name, err := discord.WebhookName(ctx, a.disc.client, wh)
	if err != nil {
		a.audit(actor, "discord.connect", "discord", "failed", discordCode(err))
		writeDiscordErr(w, err)
		return
	}
	now := a.now().UTC()
	host := cleanHost(req.Host)
	a.disc.mu.Lock()
	s := a.disc.settings
	if !s.Webhook.Equal(wh) {
		s.StatusMessageID = ""
	}
	s.Webhook = wh
	// The webhook already in use keeps its live status message, as the
	// notifier does, so an agent that restarts edits that message instead
	// of posting a second one.
	_, err = a.db.Exec(`INSERT INTO discord(id, webhook_url, webhook_name, alerts, live_status, status_message_id, public_host, connected_at)
		VALUES(1, ?, ?, ?, ?, '', ?, ?)
		ON CONFLICT(id) DO UPDATE SET webhook_url = excluded.webhook_url, webhook_name = excluded.webhook_name,
		live_status = excluded.live_status, connected_at = excluded.connected_at,
		status_message_id = CASE WHEN webhook_url = excluded.webhook_url THEN status_message_id ELSE '' END,
		public_host = CASE WHEN excluded.public_host = '' THEN public_host ELSE excluded.public_host END`,
		wh.SecretURL(), name, s.Alerts.String(), boolInt(s.LiveStatus), host, now.UnixMilli())
	if err == nil {
		a.disc.settings, a.disc.name, a.disc.connectedAt = s, name, now
		if host != "" {
			a.disc.host = host
		}
	}
	a.disc.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	a.disc.n.SetServer(a.discordDashboard())
	a.disc.n.SetSettings(s)
	a.audit(actor, "discord.connect", "discord", "succeeded", "")
	writeJSON(w, http.StatusOK, a.discordSettings())
}

func (a *Agent) hDiscordSettings(w http.ResponseWriter, r *http.Request) {
	var req api.DiscordSettingsRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	var alerts discord.Alerts
	for _, k := range req.Alerts {
		alerts = append(alerts, discord.Kind(k))
	}
	host := cleanHost(req.Host)
	a.disc.mu.Lock()
	s := a.disc.settings
	s.Alerts = alerts
	if req.LiveStatus != nil {
		s.LiveStatus = *req.LiveStatus
	}
	if err := s.Validate(); err != nil {
		a.disc.mu.Unlock()
		writeDiscordErr(w, err)
		return
	}
	s.Alerts = discord.ParseAlerts(alerts.String())
	_, err = a.db.Exec(`INSERT INTO discord(id, alerts, live_status, public_host) VALUES(1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET alerts = excluded.alerts, live_status = excluded.live_status,
		public_host = CASE WHEN excluded.public_host = '' THEN public_host ELSE excluded.public_host END`,
		s.Alerts.String(), boolInt(s.LiveStatus), host)
	if err == nil {
		a.disc.settings = s
		if host != "" {
			a.disc.host = host
		}
	}
	a.disc.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	a.disc.n.SetServer(a.discordDashboard())
	a.disc.n.SetSettings(s)
	a.audit(actor, "discord.settings", "discord", "succeeded", "alerts: "+nonEmptyOr(s.Alerts.String(), "none")+fmt.Sprintf("; live status %v", s.LiveStatus))
	writeJSON(w, http.StatusOK, a.discordSettings())
}

func (a *Agent) hDiscordDisconnect(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	a.disc.mu.Lock()
	s := a.disc.settings
	s.Webhook, s.StatusMessageID = discord.Webhook{}, ""
	_, err = a.db.Exec(`UPDATE discord SET webhook_url = '', webhook_name = '', status_message_id = '', connected_at = 0 WHERE id = 1`)
	if err == nil {
		a.disc.settings, a.disc.name, a.disc.connectedAt = s, "", time.Time{}
	}
	a.disc.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	a.disc.n.SetSettings(s)
	a.audit(actor, "discord.disconnect", "discord", "succeeded", "")
	w.WriteHeader(http.StatusNoContent)
}

func (a *Agent) hDiscordTest(w http.ResponseWriter, r *http.Request) {
	var req api.ActionRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	if err := a.disc.n.SendTest(ctx); err != nil {
		a.audit(actor, "discord.test", "discord", "failed", discordCode(err))
		writeDiscordErr(w, err)
		return
	}
	a.audit(actor, "discord.test", "discord", "succeeded", "")
	writeJSON(w, http.StatusOK, a.discordSettings())
}

// hDiscordNotify takes the alerts the panel knows about first: join
// requests from invite links, team members turning two-factor sign-in on or
// off, and admins confirming a member's Admin rights.
func (a *Agent) hDiscordNotify(w http.ResponseWriter, r *http.Request) {
	var req api.DiscordNotifyRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if _, err := validActor(req.Actor); err != nil {
		writeError(w, err)
		return
	}
	switch req.Kind {
	case api.DiscordJoinRequested:
	case api.DiscordTwoFactorChanged:
		if invites.ValidUsername(req.Member) != nil {
			writeError(w, errInvalid("Usernames are 3–32 letters, numbers, dots, dashes or underscores."))
			return
		}
		if a.discordConnected() {
			a.disc.n.Notify(discord.TwoFactorChanged(req.Member, req.On, req.Admin))
		}
		w.WriteHeader(http.StatusNoContent)
		return
	case api.DiscordAdminConfirmed:
		if invites.ValidUsername(req.Member) != nil || invites.ValidUsername(req.Actor) != nil {
			writeError(w, errInvalid("Usernames are 3–32 letters, numbers, dots, dashes or underscores."))
			return
		}
		if a.discordConnected() {
			a.disc.n.Notify(discord.AdminConfirmed(req.Member, req.Actor))
		}
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		writeError(w, errInvalid("Only join requests, two-factor changes and Admin confirmations can be reported."))
		return
	}
	if !minecraft.ValidPlayerName(req.Player) {
		writeError(w, errInvalid("Minecraft usernames are 3–16 letters, numbers or underscores."))
		return
	}
	if s := a.serverByID(req.ServerID); s != nil {
		s.alert(discord.JoinRequested(req.Player))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// A server on a joined machine: this agent holds the dashboard's Discord
	// settings, so the panel sends the server's name along.
	name, err := validName(req.ServerName)
	if err != nil || !reServerID.MatchString(req.ServerID) {
		writeError(w, errNotFound("Server"))
		return
	}
	if a.discordConnected() {
		e := discord.JoinRequested(req.Player)
		e.Server = discord.ServerInfo{ID: req.ServerID, Name: name}
		a.disc.n.Notify(e)
	}
	w.WriteHeader(http.StatusNoContent)
}

func discordCode(err error) string {
	var de *discord.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return "error"
}

// writeDiscordErr answers with Discord's problem, never the webhook URL: the
// package's messages leave the token out.
func writeDiscordErr(w http.ResponseWriter, err error) {
	var de *discord.Error
	if !errors.As(err, &de) {
		writeError(w, err)
		return
	}
	status := http.StatusBadGateway
	switch de.Code {
	case discord.CodeInvalidURL, discord.CodeInvalidSettings:
		status = http.StatusBadRequest
	case discord.CodeNotConnected:
		status = http.StatusConflict
	case discord.CodeWebhookGone, discord.CodeWebhookRejected, discord.CodeNeedsThread, discord.CodeForbidden:
		status = http.StatusUnprocessableEntity
	case discord.CodeRateLimited:
		status = http.StatusTooManyRequests
	}
	code := de.Code
	if !strings.HasPrefix(code, "discord_") {
		code = "discord_" + code
	}
	body := api.Error{Error: de.Msg, Code: code, Hint: de.Hint}
	if de.Reason != "" {
		body.Params = map[string]any{"reason": de.Reason}
	}
	if de.RetryAfter > 0 {
		secs := strconv.Itoa(int(de.RetryAfter.Round(time.Second).Seconds()) + 1)
		w.Header().Set("Retry-After", secs)
		if body.Params == nil {
			body.Params = map[string]any{}
		}
		body.Params["seconds"] = secs
	}
	writeJSON(w, status, body)
}
