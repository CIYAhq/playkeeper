package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// Whop as a billing provider (see hosting.go). A customer's first confirmed
// membership of a plan with an allowance starts them on the hosting core, a
// change of plans changes what they have, and their last plan ending pauses
// them. What the core has to tell them comes through Notify and goes out as
// a message in the store's support chat with them, like the reminder to
// download their world when they cancel. Whop tells the dashboard about
// memberships through a webhook at whopWebhookPath, and the dashboard reads
// them all every whopPollEvery too, since a webhook that keeps failing is
// switched off and what it missed isn't sent again. Only what Whop's API
// says counts: a delivery only says what to read again.
const (
	whopWebhookPath = "/api/public/whop/webhook"
	whopPollEvery   = 10 * time.Minute
	// maxWhopDelivery bounds a webhook delivery; a membership event is a
	// few kilobytes.
	maxWhopDelivery = 256 << 10
	// whopDeliveriesKept is how long a delivery's id is kept, well past the
	// three days Whop retries one.
	whopDeliveriesKept = 7 * 24 * time.Hour
	// whopMessagesKept is how long a message that went out is kept.
	whopMessagesKept = 30 * 24 * time.Hour
	// whopProvider is Whop's name as a billing provider.
	whopProvider = "whop"
)

// whopWebhookLimits let Whop's few senders deliver bursts, and bound anyone
// else who posts there.
var whopWebhookLimits = publicLimits{perMinute: 600, open: 16, read: 10 * time.Second, write: 10 * time.Second}

// reWhopID is the shape of Whop's ids, such as mem_… and user_….
var reWhopID = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// whopAccess are the statuses in which a membership grants access (see
// whop.Membership.HasAccess).
const whopAccess = `('trialing', 'active', 'canceling', 'past_due', 'completed')`

// whopWebhook receives Whop's deliveries: it checks the signature with the
// webhook's secret, counts each delivery once, keeps what a membership
// event says for the reconciler to read again, and answers at once, since
// Whop waits five seconds.
func (s *Server) whopWebhook() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != whopWebhookPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var account, secret string
		if err := s.db.QueryRow(`SELECT account_id, webhook_secret FROM whop_account WHERE id = 1`).Scan(&account, &secret); err != nil || secret == "" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxWhopDelivery+1))
		if err != nil || len(body) > maxWhopDelivery {
			http.Error(w, "Too large", http.StatusRequestEntityTooLarge)
			return
		}
		now := s.now()
		ev, err := whop.VerifyWebhook(secret, r.Header, body, now)
		if err != nil {
			s.log.Warn("refused a Whop webhook delivery", "err", err)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		res, err := s.db.Exec(`INSERT OR IGNORE INTO whop_deliveries(id, received_at) VALUES(?,?)`, ev.ID, now.UnixMilli())
		if err != nil {
			http.Error(w, "Try again", http.StatusServiceUnavailable)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = s.db.Exec(`DELETE FROM whop_deliveries WHERE received_at < ?`, now.Add(-whopDeliveriesKept).UnixMilli())
		if ev.AccountID == "" || ev.AccountID == account {
			switch ev.Type {
			case whop.EventMembershipActivated, whop.EventMembershipDeactivated, whop.EventMembershipCancelling:
				var m whop.Membership
				if json.Unmarshal(ev.Data, &m) == nil && reWhopID.MatchString(m.ID) {
					if err := s.keepMembership(m, true); err != nil {
						s.log.Error("could not keep a Whop membership", "err", err)
						// Whop sends it again, which must not count as seen.
						_, _ = s.db.Exec(`DELETE FROM whop_deliveries WHERE id = ?`, ev.ID)
						http.Error(w, "Try again", http.StatusServiceUnavailable)
						return
					}
				}
			}
		}
		s.kickWhop()
		w.WriteHeader(http.StatusOK)
	})
}

// keepMembership stores what the dashboard heard of a membership. stale
// marks what a webhook said, which the reconciler reads again from Whop,
// since deliveries can come out of order. A cancellation Whop confirms is
// undone resets its reminder, so a later one is reminded again.
func (s *Server) keepMembership(m whop.Membership, stale bool) error {
	if !reWhopID.MatchString(m.ID) || !reWhopID.MatchString(m.UserID) || !reWhopID.MatchString(m.PlanID) {
		return nil
	}
	end := int64(0)
	if !m.PeriodEnd.IsZero() {
		end = m.PeriodEnd.UnixMilli()
	}
	_, err := s.db.Exec(`INSERT INTO whop_memberships(membership_id, whop_user_id, plan_id, status, cancel_at_period_end, period_end, stale, updated_at) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(membership_id) DO UPDATE SET whop_user_id = excluded.whop_user_id, plan_id = excluded.plan_id, status = excluded.status,
		cancel_at_period_end = excluded.cancel_at_period_end, period_end = excluded.period_end, stale = excluded.stale, updated_at = excluded.updated_at,
		told_cancel = CASE WHEN excluded.stale = 0 AND excluded.cancel_at_period_end = 0 THEN 0 ELSE whop_memberships.told_cancel END`,
		m.ID, m.UserID, m.PlanID, m.Status, m.CancelAtPeriodEnd, end, stale, s.now().UnixMilli())
	return err
}

// kickWhop has the reconciler look now rather than at its next tick.
func (s *Server) kickWhop() {
	select {
	case s.whopKick <- struct{}{}:
	default:
	}
}

// runWhop reconciles Sell on Whop until ctx ends: at once, when kicked, and
// every minute, which polls Whop every whopPollEvery and retries what
// failed.
func (s *Server) runWhop(ctx context.Context) {
	s.retryWhopNow()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.reconcileWhop(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.whopKick:
		case <-t.C:
		}
	}
}

// retryWhopNow drops the waits of the calls and messages that failed, so
// they're tried at once, and has the next pass read the store and every
// membership again. Starting again does it, so a release that brings what
// was missing, such as the hosting core or a webhook Whop now takes, needs
// no wait, and a purchase made while the webhook was missing is caught
// straight away.
func (s *Server) retryWhopNow() {
	if _, err := s.db.Exec(`UPDATE whop_customers SET next_try_at = 0`); err != nil {
		s.log.Error("could not retry Whop customers", "err", err)
	}
	if _, err := s.db.Exec(`UPDATE whop_messages SET next_try_at = 0 WHERE sent_at = 0`); err != nil {
		s.log.Error("could not retry Whop messages", "err", err)
	}
	if _, err := s.db.Exec(`UPDATE whop_account SET polled_at = 0, synced_at = 0`); err != nil {
		s.log.Error("could not have Whop read again", "err", err)
	}
}

// reconcileWhop brings customers in line with their memberships: it keeps
// the webhook pointing at the dashboard, reads memberships and plans from
// Whop, keeps each plan's stock to what the machines can take, makes the
// core's calls, and sends the messages waiting to go.
func (s *Server) reconcileWhop(ctx context.Context) {
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	a, ok, err := s.storedWhop()
	if err != nil || !ok || a.TakenOverBy != "" {
		return
	}
	c, err := s.whopClient(a.Key)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// Without the machine's address the webhook stays where it is.
	if dash, err := s.dashboardURL(ctx); err == nil {
		if err := s.ensureWhopWebhook(ctx, c, &a, dash); err != nil {
			s.log.Warn("could not keep Whop's webhook pointing at this dashboard", "err", err)
		}
	}
	if err := s.refreshWhopMemberships(ctx, c, a); err != nil {
		s.log.Warn("could not read memberships from Whop", "err", err)
	}
	s.refreshWhopPlans(ctx, c, a)
	if s.whopTakenOver() || !s.stillSellsFor(ctx, c, a.ID) {
		return
	}
	s.pushWhopStock(ctx, c, a.ID)
	s.syncWhopCustomers(ctx, c)
	s.remindCancelled(ctx, a)
	s.sendWhopMessages(ctx, c, a)
}

// whopTakenOver says whether another dashboard took the store over, as
// reading the store may have just found.
func (s *Server) whopTakenOver() bool {
	var by string
	return s.db.QueryRow(`SELECT taken_over_by FROM whop_account WHERE id = 1`).Scan(&by) == nil && by != ""
}

// stillSellsFor reads the store's products again just before the
// reconciler writes stock, calls the core or sends messages, so a dashboard
// another one took the store over from stops within a pass rather than at
// its next read of the store. That holds without an address too, since a
// dashboard's own marks are the address it last marked with. Without
// Whop's answer, or when the machine can't be asked for its address, it
// does none of that this pass.
func (s *Server) stillSellsFor(ctx context.Context, c *whop.Client, accountID string) bool {
	dash, err := s.dashboardURL(ctx)
	if err != nil {
		return false
	}
	products, err := c.Products(ctx, accountID)
	if err != nil {
		s.log.Warn("could not check that this dashboard still sells for the store", "err", err)
		return false
	}
	var markedAs string
	if err := s.db.QueryRowContext(ctx, `SELECT marked_as FROM whop_account WHERE id = 1`).Scan(&markedAs); err != nil {
		return false
	}
	taken, err := s.noticeTakeover(ctx, accountID, products, dash, markedAs)
	return err == nil && !taken
}

// ensureWhopWebhook adds the webhook, or moves it to the dashboard's
// address when that changed. Without an address there's nowhere to send to.
func (s *Server) ensureWhopWebhook(ctx context.Context, c *whop.Client, a *whopAccount, dash string) error {
	if dash == "" {
		return nil
	}
	want := dash + whopWebhookPath
	if a.WebhookID != "" && a.WebhookURL == want {
		return nil
	}
	if a.WebhookID != "" {
		err := c.UpdateWebhookURL(ctx, a.WebhookID, want)
		if err == nil {
			a.WebhookURL = want
			_, err = s.db.Exec(`UPDATE whop_account SET webhook_url = ? WHERE id = 1`, want)
			return err
		}
		if !whop.NotFound(err) {
			return err
		}
	}
	hook, err := c.CreateWebhook(ctx, a.ID, want)
	if err != nil {
		return err
	}
	a.WebhookID, a.WebhookURL, a.WebhookSecret = hook.ID, want, hook.Secret
	_, err = s.db.Exec(`UPDATE whop_account SET webhook_id = ?, webhook_url = ?, webhook_secret = ? WHERE id = 1`, hook.ID, want, hook.Secret)
	return err
}

// refreshWhopMemberships reads every membership from Whop once every
// whopPollEvery, and in between only those a webhook told of.
func (s *Server) refreshWhopMemberships(ctx context.Context, c *whop.Client, a whopAccount) error {
	now := s.now()
	if now.Sub(a.PolledAt) >= whopPollEvery {
		all, err := c.Memberships(ctx, a.ID)
		if err != nil {
			return err
		}
		for _, m := range all {
			if err := s.keepMembership(m, false); err != nil {
				return err
			}
		}
		_, err = s.db.Exec(`UPDATE whop_account SET polled_at = ? WHERE id = 1`, now.UnixMilli())
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT membership_id FROM whop_memberships WHERE stale = 1`)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		stale = append(stale, id)
	}
	rows.Close()
	for _, id := range stale {
		m, err := c.Membership(ctx, id)
		if whop.NotFound(err) {
			if _, err := s.db.Exec(`DELETE FROM whop_memberships WHERE membership_id = ?`, id); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := s.keepMembership(m, false); err != nil {
			return err
		}
	}
	return nil
}

// refreshWhopPlans reads the store again every whopPollEvery, so a plan's
// allowance changed on Whop reaches its customers, and sooner when a
// membership is of a plan the dashboard hasn't read yet.
func (s *Server) refreshWhopPlans(ctx context.Context, c *whop.Client, a whopAccount) {
	since := s.now().Sub(a.SyncedAt)
	if since < whopPollEvery {
		var unknown int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM whop_memberships m
			WHERE m.stale = 0 AND NOT EXISTS (SELECT 1 FROM whop_plans p WHERE p.plan_id = m.plan_id)`).Scan(&unknown); err != nil || unknown == 0 || since < 2*time.Minute {
			return
		}
	}
	problem := ""
	if err := s.readWhopStore(ctx, c, a.ID, false); err != nil {
		problem = whopProblem(err)
		s.log.Warn("could not read the store on Whop", "err", err)
	}
	if _, err := s.db.Exec(`UPDATE whop_account SET synced_at = ?, problem = ? WHERE id = 1`, s.now().UnixMilli(), problem); err != nil {
		s.log.Error("could not record reading the store on Whop", "err", err)
	}
}

// whopCustomer is one customer as the reconciler sees them.
type whopCustomer struct {
	WhopUserID string
	Handle     string
	// Applied is the plan last given to the core (see planKey), "" before
	// they were started; Paused whether their plan's end paused them.
	Applied   string
	Paused    bool
	Attempts  int
	NextTryAt int64
	Problem   string
	// Plan is what their confirmed memberships with access allow together;
	// Unconfirmed counts memberships a webhook told of that Whop's API
	// hasn't confirmed yet, and Latest is the status of their newest one.
	Plan        CustomerPlan
	Unconfirmed int
	Latest      string
	UpdatedAt   int64
}

// planPart is one plan with an allowance that a customer's membership grants.
type planPart struct {
	id, name                  string
	servers, memoryMB, diskGB int
}

// customerPlan is what several plans allow together: their servers and
// memory added up within an allowance's bounds, and their disk added up
// when each says one, else 0 for the core's default.
func customerPlan(parts []planPart) CustomerPlan {
	if len(parts) == 0 {
		return CustomerPlan{}
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].id < parts[j].id })
	var p CustomerPlan
	var ids, names []string
	disk := true
	for _, pt := range parts {
		ids, names = append(ids, pt.id), append(names, cmpOr(pt.name, pt.id))
		p.Servers += pt.servers
		p.MemoryMB += pt.memoryMB
		p.DiskGB += pt.diskGB
		disk = disk && pt.diskGB > 0
	}
	al := capAllowance(invites.Allowance{Servers: p.Servers, MemoryMB: p.MemoryMB})
	p.ID, p.Name, p.Servers, p.MemoryMB = strings.Join(ids, "+"), strings.Join(names, " + "), al.Servers, al.MemoryMB
	if !disk {
		p.DiskGB = 0
	}
	return p
}

// planKey is what the core was given for a plan, so a change shows.
func planKey(p CustomerPlan) string {
	return fmt.Sprintf("%s|%d|%d|%d", p.ID, p.Servers, p.MemoryMB, p.DiskGB)
}

// capAllowance keeps what several plans allow together within an
// allowance's bounds.
func capAllowance(al invites.Allowance) invites.Allowance {
	al.Servers = min(al.Servers, invites.MaxAllowanceServers)
	al.MemoryMB = min(al.MemoryMB, invites.MaxAllowanceMemoryMB)
	return al
}

// whopCustomers reads every customer the dashboard knows, from their
// memberships and what was done for them, newest first.
func (s *Server) whopCustomers(ctx context.Context) ([]whopCustomer, error) {
	byID := map[string]*whopCustomer{}
	get := func(id string) *whopCustomer {
		if wc, ok := byID[id]; ok {
			return wc
		}
		wc := &whopCustomer{WhopUserID: id}
		byID[id] = wc
		return wc
	}
	rows, err := s.db.QueryContext(ctx, `SELECT whop_user_id, handle, applied, paused, attempts, next_try_at, problem, updated_at FROM whop_customers`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var r whopCustomer
		if err := rows.Scan(&id, &r.Handle, &r.Applied, &r.Paused, &r.Attempts, &r.NextTryAt, &r.Problem, &r.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		wc := get(id)
		r.WhopUserID = id
		*wc = r
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT m.whop_user_id, m.plan_id, m.status, m.stale, m.updated_at,
		COALESCE(p.title, ''), COALESCE(p.allowance_servers, 0), COALESCE(p.allowance_memory_mb, 0), COALESCE(p.disk_gb, 0), COALESCE(p.allowance_from, '')
		FROM whop_memberships m LEFT JOIN whop_plans p ON p.plan_id = m.plan_id ORDER BY m.updated_at, m.membership_id`)
	if err != nil {
		return nil, err
	}
	parts := map[string][]planPart{}
	for rows.Next() {
		var id, from string
		var pt planPart
		var status string
		var stale bool
		var updated int64
		if err := rows.Scan(&id, &pt.id, &status, &stale, &updated, &pt.name, &pt.servers, &pt.memoryMB, &pt.diskGB, &from); err != nil {
			rows.Close()
			return nil, err
		}
		wc := get(id)
		wc.Latest, wc.UpdatedAt = status, max(wc.UpdatedAt, updated)
		switch {
		case stale:
			wc.Unconfirmed++
		case from != "" && (whop.Membership{Status: status}).HasAccess():
			parts[id] = append(parts[id], pt)
		}
	}
	rows.Close()
	out := make([]whopCustomer, 0, len(byID))
	for id, wc := range byID {
		wc.Plan = customerPlan(parts[id])
		out = append(out, *wc)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].WhopUserID < out[j].WhopUserID
	})
	return out, nil
}

// syncWhopCustomers makes the core's calls each customer's memberships ask
// for: a first plan starts them, a change of plans changes theirs, and
// their last plan ending pauses them; a plan starting again after that
// starts them again. A customer with a membership Whop's API hasn't
// confirmed is left as they are until it does, and one whose call failed
// waits for their next try.
func (s *Server) syncWhopCustomers(ctx context.Context, c *whop.Client) {
	custs, err := s.whopCustomers(ctx)
	if err != nil {
		s.log.Error("could not list Whop customers", "err", err)
		return
	}
	now := s.now().UnixMilli()
	for _, wc := range custs {
		if wc.Unconfirmed > 0 || wc.NextTryAt > now {
			continue
		}
		if err := s.stepWhopCustomer(ctx, c, wc); err != nil {
			s.whopCustomerFailed(wc, err)
		}
	}
}

func (s *Server) stepWhopCustomer(ctx context.Context, c *whop.Client, wc whopCustomer) error {
	has := wc.Plan.Servers > 0 && wc.Plan.MemoryMB > 0
	cust := Customer{Provider: whopProvider, Subject: wc.WhopUserID, Handle: wc.Handle}
	// Taken before the core's call, since the fleet may count the customer
	// in its room before the call returns (see pushWhopStock).
	at := s.now()
	switch {
	case has && (wc.Applied == "" || wc.Paused):
		if cust.Handle == "" {
			u, err := c.User(ctx, wc.WhopUserID)
			if err != nil {
				return fmt.Errorf("couldn't read their username on Whop: %w", err)
			}
			cust.Handle = u.Username
			if _, err := s.db.Exec(`INSERT INTO whop_customers(whop_user_id, handle, updated_at) VALUES(?,?,?)
				ON CONFLICT(whop_user_id) DO UPDATE SET handle = excluded.handle`, cust.Subject, cust.Handle, s.now().UnixMilli()); err != nil {
				return err
			}
		}
		if _, err := s.hosting.StartCustomer(ctx, cust, wc.Plan); err != nil {
			return err
		}
		return s.recordWhopCustomer(cust, planKey(wc.Plan), false, at)
	case has && wc.Applied != planKey(wc.Plan):
		if err := s.hosting.ChangeCustomerPlan(ctx, cust, wc.Plan); err != nil {
			return err
		}
		return s.recordWhopCustomer(cust, planKey(wc.Plan), false, at)
	case !has && wc.Applied != "" && !wc.Paused:
		if err := s.hosting.PauseCustomer(ctx, cust, "their Whop membership is "+cmpOr(wc.Latest, "gone")); err != nil {
			return err
		}
		return s.recordWhopCustomer(cust, wc.Applied, true, at)
	}
	return nil
}

// recordWhopCustomer notes the plan the core now has for a customer, since
// at, and whether they're paused, clearing any problem.
func (s *Server) recordWhopCustomer(cust Customer, applied string, paused bool, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO whop_customers(whop_user_id, handle, applied, paused, applied_at, updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(whop_user_id) DO UPDATE SET handle = excluded.handle, applied = excluded.applied, paused = excluded.paused, applied_at = excluded.applied_at,
		attempts = 0, next_try_at = 0, problem = '', updated_at = excluded.updated_at`,
		cust.Subject, cust.Handle, applied, paused, at.UnixMilli(), s.now().UnixMilli())
	return err
}

// whopCustomerFailed records why a customer's call failed, and when to try
// again: soon at first, then less often, at most every six hours.
func (s *Server) whopCustomerFailed(wc whopCustomer, err error) {
	s.log.Warn("could not bring a Whop customer in line with their plans", "customer", wc.WhopUserID, "err", err)
	problem := err.Error()
	var we *whop.Error
	if errors.As(err, &we) {
		problem = whopProblem(err)
	}
	if _, dbErr := s.db.Exec(`INSERT INTO whop_customers(whop_user_id, attempts, next_try_at, problem, updated_at) VALUES(?,1,?,?,?)
		ON CONFLICT(whop_user_id) DO UPDATE SET attempts = attempts + 1, next_try_at = excluded.next_try_at, problem = excluded.problem`,
		wc.WhopUserID, s.now().Add(whopBackoff(wc.Attempts)).UnixMilli(), problem, s.now().UnixMilli()); dbErr != nil {
		s.log.Error("could not record a Whop customer's problem", "err", dbErr)
	}
}

// whopBackoff is how long to wait after attempts failed tries: a minute,
// doubling, and at most six hours.
func whopBackoff(attempts int) time.Duration {
	return min(time.Minute<<min(attempts, 9), 6*time.Hour)
}

// billingNotifier is the notifier the core calls: it sends each message
// the way the customer's billing provider reaches them.
type billingNotifier struct{ s *Server }

// Notify queues a message for a Whop customer, to go out in the store's
// support chat with them from the reconciler's next look. It never waits
// on Whop.
func (n billingNotifier) Notify(ctx context.Context, c Customer, m CustomerMessage) error {
	switch c.Provider {
	case whopProvider:
		return n.s.queueWhopMessage(ctx, c.Subject, m.Kind, m.Text)
	default:
		return fmt.Errorf("no billing provider called %q", c.Provider)
	}
}

func (s *Server) queueWhopMessage(ctx context.Context, whopUserID, kind, text string) error {
	if !reWhopID.MatchString(whopUserID) || strings.TrimSpace(text) == "" {
		return errors.New("a message needs a Whop user and some words")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO whop_messages(whop_user_id, kind, text, created_at) VALUES(?,?,?,?)`,
		whopUserID, kind, text, s.now().UnixMilli()); err != nil {
		return err
	}
	s.kickWhop()
	return nil
}

// remindCancelled reminds a customer who cancelled the plans that keep their
// servers running to download their world before the last one ends, once
// for those cancellations. Cancelling one plan while another goes on stops
// nothing, so it says nothing.
func (s *Server) remindCancelled(ctx context.Context, a whopAccount) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.membership_id, m.whop_user_id, m.period_end, m.told_cancel FROM whop_memberships m
		JOIN whop_plans p ON p.plan_id = m.plan_id AND p.allowance_from != ''
		JOIN whop_customers c ON c.whop_user_id = m.whop_user_id AND c.applied != '' AND c.paused = 0
		WHERE m.stale = 0 AND m.status IN `+whopAccess+` AND NOT EXISTS (
			SELECT 1 FROM whop_memberships o JOIN whop_plans q ON q.plan_id = o.plan_id AND q.allowance_from != ''
			WHERE o.whop_user_id = m.whop_user_id AND o.stale = 0 AND o.status IN `+whopAccess+` AND o.cancel_at_period_end = 0)`)
	if err != nil {
		s.log.Error("could not list cancelled Whop memberships", "err", err)
		return
	}
	type ending struct {
		memberships []string
		end         int64
		told        bool
	}
	byUser := map[string]*ending{}
	var users []string
	for rows.Next() {
		var id, user string
		var end int64
		var told bool
		if err := rows.Scan(&id, &user, &end, &told); err != nil {
			rows.Close()
			return
		}
		e := byUser[user]
		if e == nil {
			e = &ending{told: true}
			byUser[user] = e
			users = append(users, user)
		}
		e.memberships, e.end, e.told = append(e.memberships, id), max(e.end, end), e.told && told
	}
	rows.Close()
	for _, user := range users {
		e := byUser[user]
		if e.told {
			continue
		}
		when := "the end of the time you've paid for"
		if e.end != 0 {
			when = time.UnixMilli(e.end).UTC().Format("2 January")
		}
		text := fmt.Sprintf("You cancelled your %s plan. It keeps running until %s, then your servers stop. "+
			"To keep a copy of your world, download it before then: open your server, then World › Backups › Download.", whopName(a.Account), when)
		if err := s.queueWhopMessage(ctx, user, "cancelling", text); err != nil {
			s.log.Error("could not queue a cancellation reminder", "err", err)
			continue
		}
		for _, id := range e.memberships {
			if _, err := s.db.Exec(`UPDATE whop_memberships SET told_cancel = 1 WHERE membership_id = ?`, id); err != nil {
				s.log.Error("could not record a cancellation reminder", "err", err)
			}
		}
	}
}

// sendWhopMessages sends the messages waiting to go, oldest first, each in
// the store's support chat with its customer, opened the first time. A
// customer's messages go in order: after one fails, theirs wait for its
// next try.
func (s *Server) sendWhopMessages(ctx context.Context, c *whop.Client, a whopAccount) {
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM whop_messages WHERE sent_at != 0 AND sent_at < ?`, now.Add(-whopMessagesKept).UnixMilli()); err != nil {
		s.log.Error("could not forget old Whop messages", "err", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT w.id, w.whop_user_id, w.text, w.attempts, w.next_try_at, COALESCE(c.channel_id, '')
		FROM whop_messages w LEFT JOIN whop_customers c ON c.whop_user_id = w.whop_user_id WHERE w.sent_at = 0 ORDER BY w.id LIMIT 100`)
	if err != nil {
		s.log.Error("could not list Whop messages", "err", err)
		return
	}
	type message struct {
		id                  int64
		user, text, channel string
		attempts            int
		nextTryAt           int64
	}
	var due []message
	for rows.Next() {
		var m message
		if err := rows.Scan(&m.id, &m.user, &m.text, &m.attempts, &m.nextTryAt, &m.channel); err != nil {
			rows.Close()
			return
		}
		due = append(due, m)
	}
	rows.Close()
	held := map[string]bool{}
	channels := map[string]string{}
	var token string
	var tokenErr error
	for _, m := range due {
		if held[m.user] {
			continue
		}
		if m.nextTryAt > now.UnixMilli() {
			held[m.user] = true
			continue
		}
		if token == "" && tokenErr == nil {
			token, tokenErr = whopSenderToken(ctx, c, a.ID)
		}
		if tokenErr != nil {
			s.whopMessageFailed(m.id, m.attempts, tokenErr)
			held[m.user] = true
			continue
		}
		channel := cmpOr(channels[m.user], m.channel)
		if channel == "" {
			ch, err := c.OpenSupportChat(ctx, a.ID, m.user)
			if err != nil {
				s.whopMessageFailed(m.id, m.attempts, err)
				held[m.user] = true
				continue
			}
			channel = ch
			if _, err := s.db.Exec(`INSERT INTO whop_customers(whop_user_id, channel_id, updated_at) VALUES(?,?,?)
				ON CONFLICT(whop_user_id) DO UPDATE SET channel_id = excluded.channel_id`, m.user, ch, now.UnixMilli()); err != nil {
				s.log.Error("could not record a Whop customer's support chat", "err", err)
			}
		}
		channels[m.user] = channel
		if err := c.SendMessage(ctx, token, channel, m.text); err != nil {
			if whop.NotFound(err) {
				// The chat is gone; the next try opens another.
				_, _ = s.db.Exec(`UPDATE whop_customers SET channel_id = '' WHERE whop_user_id = ?`, m.user)
			}
			s.whopMessageFailed(m.id, m.attempts, err)
			held[m.user] = true
			continue
		}
		if _, err := s.db.Exec(`UPDATE whop_messages SET sent_at = ?, problem = '' WHERE id = ?`, now.UnixMilli(), m.id); err != nil {
			s.log.Error("could not record a Whop message that went out", "err", err)
		}
	}
}

// whopSenderToken gets a token to send messages as the store's owner, who
// is in each of its support chats, since Whop takes none from the key. It
// may send support chat messages and nothing else, and the dashboard gets a
// new one each time it sends rather than keep one.
func whopSenderToken(ctx context.Context, c *whop.Client, accountID string) (string, error) {
	owner, err := c.Owner(ctx)
	if err != nil {
		return "", fmt.Errorf("couldn't read who owns the store on Whop: %w", err)
	}
	return c.UserToken(ctx, accountID, owner.ID, whop.MessageAction)
}

func (s *Server) whopMessageFailed(id int64, attempts int, err error) {
	s.log.Warn("could not send a message on Whop", "err", err)
	if _, dbErr := s.db.Exec(`UPDATE whop_messages SET attempts = attempts + 1, next_try_at = ?, problem = ? WHERE id = ?`,
		s.now().Add(whopBackoff(attempts)).UnixMilli(), whopProblem(err), id); dbErr != nil {
		s.log.Error("could not record a Whop message that failed", "err", dbErr)
	}
}

// whopCustomerView is one customer on Settings › Sell on Whop.
type whopCustomerView struct {
	WhopUserID string `json:"whopUserId"`
	Handle     string `json:"handle,omitempty"`
	// Status is "starting" (their plans ask for hosting the core hasn't
	// given yet, as after buying again), "active", "paused" (their plans
	// ended) or "ended" (no plan grants access, and they were never
	// started).
	Status    string            `json:"status"`
	Plan      string            `json:"plan,omitempty"`
	Allowance invites.Allowance `json:"allowance,omitzero"`
	// Account is their account on this dashboard, once the core has made it.
	Account string `json:"account,omitempty"`
	Problem string `json:"problem,omitempty"`
	// MessageProblem is why a message to them hasn't gone out on Whop, while
	// it waits for its next try.
	MessageProblem string `json:"messageProblem,omitempty"`
}

// whopCustomerViews lists the customers the dashboard knows, newest first.
func (s *Server) whopCustomerViews(ctx context.Context) ([]whopCustomerView, error) {
	custs, err := s.whopCustomers(ctx)
	if err != nil {
		return nil, err
	}
	waiting, err := s.whopMessageProblems(ctx)
	if err != nil {
		return nil, err
	}
	out := []whopCustomerView{}
	for i, wc := range custs {
		if i == 200 {
			break
		}
		v := whopCustomerView{WhopUserID: wc.WhopUserID, Handle: wc.Handle, Plan: wc.Plan.Name, Problem: wc.Problem, MessageProblem: waiting[wc.WhopUserID],
			Allowance: invites.Allowance{Servers: wc.Plan.Servers, MemoryMB: wc.Plan.MemoryMB}}
		has := wc.Plan.Servers > 0 && wc.Plan.MemoryMB > 0
		switch {
		case has && (wc.Applied == "" || wc.Paused):
			v.Status = "starting"
		case has:
			v.Status = "active"
		case wc.Applied != "":
			v.Status = "paused"
		default:
			v.Status = "ended"
		}
		if info, ok, err := s.hosting.CustomerAccount(ctx, whopProvider, wc.WhopUserID); err == nil && ok {
			v.Account = info.Username
		}
		out = append(out, v)
	}
	return out, nil
}

// whopMessageProblems is why each customer's message that failed hasn't
// gone out yet. Theirs go in order, so the oldest one waiting is the one
// being tried.
func (s *Server) whopMessageProblems(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT whop_user_id, problem FROM whop_messages WHERE sent_at = 0 AND problem != '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var user, problem string
		if err := rows.Scan(&user, &problem); err != nil {
			return nil, err
		}
		if _, ok := out[user]; !ok {
			out[user] = problem
		}
	}
	return out, rows.Err()
}
