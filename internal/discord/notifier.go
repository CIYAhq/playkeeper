package discord

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"sync"
	"time"
)

// DefaultStatusInterval is the least time between two updates of the live
// status message that only change who is playing. A server coming online,
// starting, stopping or crashing shows within seconds.
const DefaultStatusInterval = time.Minute

const (
	maxQueue = 50
	// maxTries is how often a message is sent before Playkeeper gives up.
	maxTries = 4
	// firstBackoff is the wait after the first failure; it doubles after
	// every further one.
	firstBackoff = 2 * time.Second
	minWait      = time.Second
	// maxAlertAge drops alerts that could not be sent for this long: by then
	// they are old news. A rate limit longer than this counts as a failure
	// straight away.
	maxAlertAge = 15 * time.Minute
	// editCooldown is the wait after Discord refuses edits to a status
	// message older than an hour; its docs don't say how many it allows.
	editCooldown = 10 * time.Minute
	// statusGap is the least time between two updates of the live status
	// message when a server's state changes, so a server that crashes and
	// restarts at once costs one edit rather than a burst of them.
	statusGap = 2 * time.Second
	// statusBurst is the most updates of the live status message in one
	// StatusInterval. A state change beyond that waits until the oldest of
	// them is a StatusInterval old, well inside Discord's rate limits, which
	// its answers enforce as well.
	statusBurst = 10
)

// Options configure a Notifier.
type Options struct {
	Settings Settings
	Server   ServerInfo
	// Client sends the requests; nil means a client with a 20 second
	// timeout. Redirects are never followed.
	Client *http.Client
	Now    func() time.Time
	Logger *slog.Logger
	// StatusInterval is the least time between two updates of the live
	// status message that only change who is playing; 0 means
	// DefaultStatusInterval.
	StatusInterval time.Duration
	// StatusGap is the least time between any two updates of the live
	// status message; 0 means two seconds.
	StatusGap time.Duration
	// OnStatusMessage is called with the id of every live status message
	// the Notifier posts, for the caller to store as
	// Settings.StatusMessageID. It runs on Run's goroutine.
	OnStatusMessage func(id string)
	// OnDelivery is called when the first message goes through, whenever a
	// message fails for good, and when one goes through again after that.
	// It runs on Run's goroutine or in SendTest. Delivery returns the
	// current state at any time.
	OnDelivery func(Delivery)
}

// Delivery is how sending to Discord is going, for the settings screen.
type Delivery struct {
	// Sent is when a message last reached Discord; zero if none has.
	Sent time.Time `json:"sent,omitzero"`
	// Failed is when a message last failed for good; zero if none has since
	// the last one that went through.
	Failed time.Time `json:"failed,omitzero"`
	// Code, Msg and Hint explain the failure and are empty while messages go
	// through. Msg reads "Discord didn't accept the last message: …".
	Code string `json:"code,omitempty"`
	Msg  string `json:"msg,omitempty"`
	Hint string `json:"hint,omitempty"`
	// Status is Discord's HTTP status and RetryAfterSeconds the wait it
	// asked for, to fill in a translated message; 0 when they don't apply.
	Status            int `json:"status,omitempty"`
	RetryAfterSeconds int `json:"retryAfterSeconds,omitempty"`
	// Stopped means the webhook no longer works, so nothing is sent until a
	// new webhook URL is saved.
	Stopped bool `json:"stopped,omitempty"`
}

// A Notifier delivers one server's alerts and live status message to its
// Discord webhook. Its methods may be called from any goroutine; Run does
// the sending, one request at a time.
type Notifier struct {
	client          client
	now             func() time.Time
	log             *slog.Logger
	interval        time.Duration
	gap             time.Duration
	onStatusMessage func(string)
	onDelivery      func(Delivery)
	wake            chan struct{}

	mu       sync.Mutex
	settings Settings
	server   ServerInfo
	// gen counts webhook changes, so that an answer about the previous
	// webhook is ignored.
	gen    int
	broken *Error
	queue  []Event
	// quietUntil is when alerts about each subject may go out again.
	quietUntil map[string]time.Time
	// notBefore is when Discord's rate limit lets the next request go.
	notBefore  time.Time
	alertTries int
	alertRetry time.Time

	status     Status
	board      *Board
	haveStatus bool
	// shown identifies what the live status message shows (embed.key), and
	// shownStates the servers' states in it (Board.states).
	shown       string
	shownStates string
	statusAt    time.Time
	// statusSent are the times of the last statusBurst status updates.
	statusSent  []time.Time
	statusTries int
	statusRetry time.Time
	editsAfter  time.Time

	delivery Delivery
}

// New makes a Notifier. It sends nothing until Run is called.
func New(o Options) *Notifier {
	n := &Notifier{
		client:          newClient(o.Client),
		now:             o.Now,
		log:             o.Logger,
		interval:        o.StatusInterval,
		gap:             o.StatusGap,
		onStatusMessage: o.OnStatusMessage,
		onDelivery:      o.OnDelivery,
		wake:            make(chan struct{}, 1),
		settings:        o.Settings.clean(),
		server:          o.Server,
		quietUntil:      map[string]time.Time{},
	}
	if n.now == nil {
		n.now = time.Now
	}
	if n.log == nil {
		n.log = slog.New(slog.DiscardHandler)
	}
	if n.interval <= 0 {
		n.interval = DefaultStatusInterval
	}
	if n.gap <= 0 {
		n.gap = statusGap
	}
	return n
}

// Notify queues an alert for e if its kind is turned on. An alert of the
// same kind about the same thing as one queued within the kind's quiet
// period is dropped, so a crash loop or a player who keeps reconnecting
// posts once every few minutes rather than every time.
func (n *Notifier) Notify(e Event) {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	if e.At.IsZero() {
		e.At = now
	}
	if n.settings.Webhook.IsZero() || n.broken != nil || !n.settings.Alerts.posts(e.Kind) {
		return
	}
	for s, t := range n.quietUntil {
		if !now.Before(t) {
			delete(n.quietUntil, s)
		}
	}
	subject := e.subject()
	if _, quiet := n.quietUntil[subject]; quiet {
		n.log.Debug("discord: dropped a repeated alert", "kind", e.Kind)
		return
	}
	if len(n.queue) >= maxQueue {
		n.log.Warn("discord: dropped an alert because too many are waiting", "kind", e.Kind, "webhook", n.settings.Webhook)
		return
	}
	n.quietUntil[subject] = now.Add(e.Kind.quiet())
	n.queue = append(n.queue, e)
	n.signal()
}

// UpdateStatus gives the Notifier the server's current status. When live
// status is on, the status message is updated whenever what it shows
// changes: within seconds when the server's state changes, and at most once
// per StatusInterval when only players come and go.
func (n *Notifier) UpdateStatus(s Status) {
	s.Players = slices.Clone(s.Players)
	n.mu.Lock()
	n.status, n.haveStatus = s, true
	n.mu.Unlock()
	n.signal()
}

// SetServer updates the server's name, dashboard link, address, version and
// message of the day.
func (n *Notifier) SetServer(info ServerInfo) {
	n.mu.Lock()
	n.server = info
	n.mu.Unlock()
	n.signal()
}

// SetSettings applies settings saved in the dashboard. It ignores
// s.StatusMessageID: the Notifier keeps the status message it knows. A
// different webhook starts afresh: its own rate limits, a new status message
// (store status_message_id as "" with it), and sending again if the previous
// webhook had stopped working.
func (n *Notifier) SetSettings(s Settings) {
	s = s.clean()
	n.mu.Lock()
	old := n.settings
	s.StatusMessageID = old.StatusMessageID
	if !s.Webhook.Equal(old.Webhook) {
		n.gen++
		n.broken, n.delivery, n.quietUntil = nil, Delivery{}, map[string]time.Time{}
		n.notBefore, n.alertTries, n.alertRetry = time.Time{}, 0, time.Time{}
		n.shown, n.shownStates, n.statusAt, n.statusSent = "", "", time.Time{}, nil
		n.statusTries, n.statusRetry, n.editsAfter = 0, time.Time{}, time.Time{}
		s.StatusMessageID = ""
		if s.Webhook.IsZero() {
			n.queue = nil
		}
	}
	n.settings = s
	n.mu.Unlock()
	n.signal()
}

// Delivery reports how sending to Discord is going.
func (n *Notifier) Delivery() Delivery {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.delivery
}

// SendTest posts a test message to the saved webhook now, outside the
// queue, and returns Discord's answer as an *Error (nil when it went
// through).
func (n *Notifier) SendTest(ctx context.Context) error {
	n.mu.Lock()
	w, info, gen, wait := n.settings.Webhook, n.server, n.gen, n.notBefore.Sub(n.now())
	n.mu.Unlock()
	if w.IsZero() {
		return errNotConnected()
	}
	if wait > 0 {
		return rateLimited(wait)
	}
	r := n.client.execute(ctx, w, newMessage(testEmbed(info)))
	if r.canceled {
		return ctx.Err()
	}
	var d *Delivery
	n.mu.Lock()
	if gen == n.gen {
		now := n.now()
		n.limit(r, now)
		if r.err == nil {
			n.broken = nil
			d = n.succeeded(now)
		} else {
			if r.err.stops() {
				n.broken, n.queue = r.err, nil
			}
			d = n.failed(r.err, now)
		}
	}
	n.mu.Unlock()
	n.report(nil, d)
	if r.err != nil {
		return r.err
	}
	return nil
}

// Run sends alerts and status updates as they fall due, until ctx is done.
// Call it once per Notifier.
func (n *Notifier) Run(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for ctx.Err() == nil {
		next := n.step(ctx)
		var fire <-chan time.Time
		if !next.IsZero() {
			d := next.Sub(n.now())
			if d <= 0 {
				continue
			}
			timer.Reset(d)
			fire = timer.C
		}
		select {
		case <-ctx.Done():
			return
		case <-n.wake:
		case <-fire:
		}
	}
}

func (n *Notifier) signal() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

// job is one request to make.
type job struct {
	gen     int
	webhook Webhook
	msg     message
	// count is how many queued alerts msg carries.
	count     int
	status    bool
	key       string
	states    string
	messageID string
}

// step makes at most one request, if one is due, and returns when the next
// one is due: zero when nothing is waiting.
func (n *Notifier) step(ctx context.Context) time.Time {
	n.mu.Lock()
	j, next := n.plan(n.now())
	n.mu.Unlock()
	if j == nil {
		return next
	}
	var r result
	if j.status && j.messageID != "" {
		r = n.client.edit(ctx, j.webhook, j.messageID, j.msg)
	} else {
		r = n.client.execute(ctx, j.webhook, j.msg)
	}
	n.mu.Lock()
	id, d := n.apply(j, r, n.now())
	_, next = n.plan(n.now())
	n.mu.Unlock()
	n.report(id, d)
	return next
}

// plan finds the request to make now, with now as its due time, or else
// when the next one is due (zero when nothing is waiting). Alerts go before
// status updates.
func (n *Notifier) plan(now time.Time) (*job, time.Time) {
	w := n.settings.Webhook
	if w.IsZero() || n.broken != nil {
		n.queue = nil
		return nil, time.Time{}
	}
	stale := 0
	n.queue = slices.DeleteFunc(n.queue, func(e Event) bool {
		old := now.Sub(e.At) > maxAlertAge
		if old {
			stale++
		}
		return old || !n.settings.Alerts.posts(e.Kind)
	})
	if stale > 0 {
		n.log.Warn("discord: dropped alerts that could not be sent in time", "count", stale, "webhook", w)
	}
	var next time.Time
	if len(n.queue) > 0 {
		due := later(n.alertRetry, n.notBefore)
		if !due.After(now) {
			return n.alertJob(w), now
		}
		next = due
	}
	j, due := n.statusJob(w, now)
	if j != nil {
		return j, now
	}
	if !due.IsZero() && (next.IsZero() || due.Before(next)) {
		next = due
	}
	return nil, next
}

// alertJob puts as many queued alerts into one message as Discord allows.
func (n *Notifier) alertJob(w Webhook) *job {
	var embeds []embed
	total := 0
	for _, e := range n.queue {
		info := n.server
		if e.Server != (ServerInfo{}) {
			info = e.Server
			info.DashboardURL = n.server.DashboardURL
			if e.Server.DashboardURL != "" {
				info.DashboardURL = e.Server.DashboardURL
			}
		}
		em := e.embed(info)
		if len(embeds) == maxEmbeds || (len(embeds) > 0 && total+em.size() > maxEmbedsTotal) {
			break
		}
		embeds = append(embeds, em)
		total += em.size()
	}
	return &job{gen: n.gen, webhook: w, msg: newMessage(embeds...), count: len(embeds)}
}

// statusJob is the status update to make now if the status message is out
// of date, or else when it may next be updated.
func (n *Notifier) statusJob(w Webhook, now time.Time) (*job, time.Time) {
	if !n.settings.LiveStatus || !n.haveStatus {
		return nil, time.Time{}
	}
	e, states := n.status.embed(n.server), string(n.status.State)
	if n.board != nil {
		e, states = n.board.embed(n.server), n.board.states()
	}
	key := e.key()
	if key == n.shown {
		return nil, time.Time{}
	}
	id := n.settings.StatusMessageID
	due := later(n.statusRetry, n.notBefore)
	switch {
	case n.statusAt.IsZero():
	case states != n.shownStates:
		due = later(due, later(n.statusAt.Add(n.gap), n.burstEnds()))
	default:
		due = later(due, n.statusAt.Add(n.interval))
	}
	if id != "" {
		due = later(due, n.editsAfter)
	}
	if due.After(now) {
		return nil, due
	}
	e.Timestamp = now.UTC().Format(time.RFC3339)
	j := &job{gen: n.gen, webhook: w, msg: newMessage(e), status: true, key: key, states: states, messageID: id}
	if id == "" {
		j.msg.Flags = flagSuppressNotifications
	}
	return j, time.Time{}
}

// burstEnds is when the live status message may next be updated without
// going over statusBurst updates in a StatusInterval.
func (n *Notifier) burstEnds() time.Time {
	if len(n.statusSent) < statusBurst {
		return time.Time{}
	}
	return n.statusSent[len(n.statusSent)-statusBurst].Add(n.interval)
}

// apply records the outcome of j and returns what to report once the lock
// is released: a new status message id and a change in delivery.
func (n *Notifier) apply(j *job, r result, now time.Time) (*string, *Delivery) {
	if r.canceled || j.gen != n.gen {
		return nil, nil
	}
	n.limit(r, now)
	if j.status {
		n.statusSent = append(n.statusSent[max(0, len(n.statusSent)-statusBurst+1):], now)
	}
	if r.err == nil && j.status && j.messageID == "" && r.messageID == "" {
		r.err = &Error{
			Code: CodeUnexpected,
			Msg:  "Discord did not confirm the live status message, so Playkeeper cannot update it.",
			Hint: "Playkeeper posts a new status message when the status changes. If it keeps happening, please report it.",
		}
	}
	if r.err == nil {
		d := n.succeeded(now)
		if !j.status {
			n.dropSent(j)
			n.alertTries, n.alertRetry = 0, time.Time{}
			return nil, d
		}
		n.shown, n.shownStates, n.statusAt, n.statusTries, n.statusRetry = j.key, j.states, now, 0, time.Time{}
		if j.messageID != "" {
			return nil, d
		}
		n.settings.StatusMessageID = r.messageID
		return &r.messageID, d
	}
	e := r.err
	switch {
	case e.stops():
		n.log.Warn("discord: stopped sending because the webhook no longer works", "webhook", j.webhook, "code", e.Code, "error", e.Msg)
		n.broken, n.queue = e, nil
		return nil, n.failed(e, now)
	case e.Code == codeUnknownMessage:
		n.log.Info("discord: the live status message was deleted; posting a new one", "webhook", j.webhook)
		n.settings.StatusMessageID, n.shown, n.shownStates, n.statusAt = "", "", "", time.Time{}
		return nil, nil
	case e.Code == codeEditLimit:
		n.log.Info("discord: Discord refuses more edits to the live status message for now", "webhook", j.webhook, "retry_in", editCooldown)
		n.editsAfter = now.Add(editCooldown)
		return nil, nil
	}
	tries, retryAt := &n.alertTries, &n.alertRetry
	if j.status {
		tries, retryAt = &n.statusTries, &n.statusRetry
	}
	*tries++
	if r.retry && *tries < maxTries && e.RetryAfter <= maxAlertAge {
		wait := e.RetryAfter
		if wait == 0 {
			wait = firstBackoff << (*tries - 1)
		}
		*retryAt = now.Add(max(wait, minWait))
		n.log.Info("discord: sending failed; trying again", "webhook", j.webhook, "code", e.Code, "error", e.Msg, "retry_in", wait)
		return nil, nil
	}
	n.log.Warn("discord: gave up sending a message", "webhook", j.webhook, "code", e.Code, "error", e.Msg, "tries", *tries)
	*tries, *retryAt = 0, time.Time{}
	switch {
	case !j.status:
		n.dropSent(j)
	case r.retry:
		n.statusRetry = now.Add(n.interval)
	default:
		n.shown, n.shownStates, n.statusAt = j.key, j.states, now
	}
	return nil, n.failed(e, now)
}

// dropSent removes the alerts j carried from the head of the queue, which
// only grows at the tail while j is out, unless SendTest emptied it.
func (n *Notifier) dropSent(j *job) {
	n.queue = n.queue[min(j.count, len(n.queue)):]
}

// limit notes when Discord's rate limit lets the next request go.
func (n *Notifier) limit(r result, now time.Time) {
	wait := r.resetAfter
	if r.err != nil && r.err.Code == CodeRateLimited {
		wait = max(wait, r.err.RetryAfter)
	}
	if wait > 0 {
		n.notBefore = later(n.notBefore, now.Add(wait))
	}
}

func (n *Notifier) succeeded(now time.Time) *Delivery {
	changed := n.delivery.Code != "" || n.delivery.Sent.IsZero()
	n.delivery = Delivery{Sent: now}
	if !changed {
		return nil
	}
	d := n.delivery
	return &d
}

func (n *Notifier) failed(e *Error, now time.Time) *Delivery {
	n.delivery = Delivery{
		Sent: n.delivery.Sent, Failed: now, Code: e.Code,
		Msg: "Discord didn't accept the last message: " + e.Msg, Hint: e.Hint,
		Status: e.Status, RetryAfterSeconds: int(math.Ceil(e.RetryAfter.Seconds())), Stopped: e.stops(),
	}
	d := n.delivery
	return &d
}

func (n *Notifier) report(id *string, d *Delivery) {
	if id != nil && n.onStatusMessage != nil {
		n.onStatusMessage(*id)
	}
	if d != nil && n.onDelivery != nil {
		n.onDelivery(*d)
	}
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
