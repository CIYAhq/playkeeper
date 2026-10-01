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
// switched off and what it missed isn't sent again. Without the webhook,
// reading is how a purchase is heard of, every whopPollUnhooked. Only what
// Whop's API says counts: a delivery only says what to read again.
const (
	whopWebhookPath = "/api/public/whop/webhook"
	whopPollEvery   = 10 * time.Minute
	// whopPollUnhooked is a little under the reconciler's minute, so each
	// of its looks reads, however long the one before took to get there.
	whopPollUnhooked = 50 * time.Second
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

// whopAppHosting are the statuses in which a membership gives servers in an
// app store (whopHosts).
const whopAppHosting = `('trialing', 'active', 'canceling', 'past_due')`

// whopHosts says whether a membership in status gives its customer servers
// in a store reached via: one with access, but in an app store not a
// one-time purchase ("completed"), which would keep its servers for good
// on one payment, while hosted plans renew monthly.
func whopHosts(via, status string) bool {
	if via == whopViaApp && status == "completed" {
		return false
	}
	return whop.Membership{Status: status}.HasAccess()
}

// whopWebhook receives Whop's deliveries for the key store: it checks the
// signature with the store's webhook secret, counts each delivery once,
// keeps what a membership event of the store's says for the reconciler to
// read again, and answers at once, since Whop waits five seconds. An app
// store's deliveries come through the app's webhook.
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
		st, ok, err := s.keyStore(r.Context())
		if err != nil || !ok || st.WebhookSecret == "" {
			http.NotFound(w, r)
			return
		}
		account, secret := st.ID, st.WebhookSecret
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
					if err := s.keepMembership(account, m, true); err != nil {
						s.log.Error("could not keep a Whop membership", "err", err)
						// Whop sends it again, which must not count as seen.
						_, _ = s.db.Exec(`DELETE FROM whop_deliveries WHERE id = ?`, ev.ID)
						http.Error(w, "Try again", http.StatusServiceUnavailable)
						return
					}
				}
			}
		}
		s.kickWhopStore(account)
		w.WriteHeader(http.StatusOK)
	})
}

// keepMembership stores what the dashboard heard of a membership of the
// store storeID. stale marks what a webhook said, which the reconciler
// reads again from Whop, since deliveries can come out of order. A
// cancellation Whop confirms is undone resets its reminder, so a later one
// is reminded again. A membership another store has stays that store's.
func (s *Server) keepMembership(storeID string, m whop.Membership, stale bool) error {
	if !reWhopID.MatchString(storeID) || !reWhopID.MatchString(m.ID) || !reWhopID.MatchString(m.UserID) || !reWhopID.MatchString(m.PlanID) {
		return nil
	}
	end := int64(0)
	if !m.PeriodEnd.IsZero() {
		end = m.PeriodEnd.UnixMilli()
	}
	// A customer the dashboard deleted stays deleted: a membership of theirs
	// that no longer gives access isn't brought back (see erasure.go). The
	// look is part of the write, so a deletion can't land between the two,
	// as it could for a webhook, which doesn't wait for whopMu.
	args := append([]any{storeID, m.ID, m.UserID, m.PlanID, m.Status, m.CancelAtPeriodEnd, end, stale, s.now().UnixMilli(), m.HasAccess()}, forgottenArgs(storeID, m.UserID)...)
	_, err := s.db.Exec(`INSERT INTO whop_memberships(store_id, membership_id, whop_user_id, plan_id, status, cancel_at_period_end, period_end, stale, updated_at)
		SELECT ?,?,?,?,?,?,?,?,? WHERE ? OR NOT (`+forgottenSQL+`)
		ON CONFLICT(membership_id) DO UPDATE SET whop_user_id = excluded.whop_user_id, plan_id = excluded.plan_id, status = excluded.status,
		cancel_at_period_end = excluded.cancel_at_period_end, period_end = excluded.period_end, stale = excluded.stale, updated_at = excluded.updated_at,
		told_cancel = CASE WHEN excluded.stale = 0 AND excluded.cancel_at_period_end = 0 THEN 0 ELSE whop_memberships.told_cancel END
		WHERE whop_memberships.store_id = excluded.store_id`, args...)
	return err
}

// kickWhop has the reconciler look at every store now rather than at its
// next tick.
func (s *Server) kickWhop() {
	s.whopKickMu.Lock()
	s.whopKickAll = true
	s.whopKickMu.Unlock()
	s.wakeWhop()
}

// kickWhopStore has the reconciler look at the store now, and at no other
// store for it: a store's delivery or message doesn't wait on every
// store's pass.
func (s *Server) kickWhopStore(id string) {
	s.whopKickMu.Lock()
	if s.whopKicked == nil {
		s.whopKicked = map[string]bool{}
	}
	s.whopKicked[id] = true
	s.whopKickMu.Unlock()
	s.wakeWhop()
}

func (s *Server) wakeWhop() {
	select {
	case s.whopKick <- struct{}{}:
	default:
	}
}

// takeWhopKicks is the stores the kicks since the last asked to look at,
// nil for every store.
func (s *Server) takeWhopKicks() map[string]bool {
	s.whopKickMu.Lock()
	defer s.whopKickMu.Unlock()
	only := s.whopKicked
	if s.whopKickAll || len(only) == 0 {
		only = nil
	}
	s.whopKicked, s.whopKickAll = nil, false
	return only
}

// runWhop reconciles Sell on Whop until ctx ends: every store at once and
// every minute, which polls Whop every whopPollEvery and retries what
// failed, and the stores kicked when kicked.
func (s *Server) runWhop(ctx context.Context) {
	s.retryWhopNow()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	var only map[string]bool
	for {
		s.reconcileWhop(ctx, only)
		select {
		case <-ctx.Done():
			return
		case <-s.whopKick:
			only = s.takeWhopKicks()
		case <-t.C:
			only = nil
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
	if _, err := s.db.Exec(`UPDATE whop_stores SET polled_at = 0, synced_at = 0`); err != nil {
		s.log.Error("could not have Whop read again", "err", err)
	}
}

// reconcileWhop brings each store's customers in line with their
// memberships, a store at a time (see reconcileWhopStore): the stores in
// only, or every store for nil. One store's pass waits on Whop's answers
// before the next starts, so the stores the app's one key reaches share
// Whop's limit for it.
func (s *Server) reconcileWhop(ctx context.Context, only map[string]bool) {
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	stores, err := s.whopStores(ctx)
	if err != nil {
		s.log.Error("could not list the stores", "err", err)
		return
	}
	for _, st := range stores {
		if only == nil || only[st.ID] {
			s.reconcileWhopStore(ctx, st)
		}
	}
}

// reconcileWhopStore brings the store's customers in line with their
// memberships: it keeps the key store's webhook pointing at the dashboard,
// reads the store's memberships and plans from Whop, keeps each of its
// plans' stock to what the machines can take, makes the core's calls for
// its customers, and sends the messages waiting to go in its chats.
func (s *Server) reconcileWhopStore(ctx context.Context, st whopStore) {
	if st.TakenOverBy != "" {
		return
	}
	// A store that left ends its customers' plans from what the dashboard
	// kept, whether or not Whop still answers for it, so once its own
	// suspension is lifted, its customers' is too, into their ended plans.
	left := !st.LeftAt.IsZero()
	if left {
		s.endLeftStorePlans(ctx, st)
		if st.SuspendedAt.IsZero() {
			s.liftWithStore(ctx, st)
		}
	}
	c, err := s.whopClientFor(ctx, st)
	if err != nil {
		s.whopStoreNeedsALook(st.ID, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if st.Via == whopViaApp {
		lost, problem := whopGrantProblem(ctx, c, st)
		if !left && s.whopGrantWatch(ctx, st, lost, problem) {
			return
		}
		if problem != "" {
			s.whopStoreNeedsALook(st.ID, problem)
			return
		}
	}
	// A suspended store, or one that left, sells nothing, and its pass does
	// little else: each of its plans, a new one included, gets a stock of 0
	// on Whop, and a store that left tells its customers their plans ended.
	if left || !st.SuspendedAt.IsZero() {
		s.refreshWhopPlans(ctx, c, st)
		s.stopWhopSales(ctx, st.ID)
		s.pushWhopStock(ctx, c, st.ID)
		if left {
			s.sendWhopMessages(ctx, c, st)
		}
		return
	}
	// Without the machine's address the webhook stays where it is. An app
	// store's memberships are read every minute until the app's webhook
	// tells of them.
	every := whopPollUnhooked
	if dash, err := s.dashboardURL(ctx); err == nil && st.Via == whopViaKey {
		was := whopHooked(st, dash)
		if err := s.ensureWhopWebhook(ctx, c, &st, dash); err != nil {
			s.log.Warn("could not keep Whop's webhook pointing at this dashboard", "err", err)
		}
		switch {
		case !whopHooked(st, dash):
		case was:
			every = whopPollEvery
		default:
			// A webhook tells only of what happens once it's there, so
			// what happened since the last read is read now.
			every = 0
		}
	}
	if st.Via == whopViaApp {
		every = s.whopAppEvery(ctx, st)
	}
	read := s.refreshWhopMemberships(ctx, c, st, every)
	if read != nil {
		s.log.Warn("could not read memberships from Whop", "store", st.ID, "err", read)
	}
	plansRead := s.refreshWhopPlans(ctx, c, st)
	if st.Via == whopViaApp && plansRead && s.whopShareStep(ctx, c, &st) {
		return
	}
	if s.whopTakenOver(st.ID) || !s.stillSellsFor(ctx, c, st) {
		return
	}
	// A closed store sells nothing and starts nobody (see closing.go).
	if st.ClosedWhy != "" {
		s.stopWhopSales(ctx, st.ID)
	}
	s.pushWhopStock(ctx, c, st.ID)
	s.syncWhopCustomers(ctx, c, st)
	if read == nil {
		s.liftWithStore(ctx, st)
	}
	s.remindCancelled(ctx, st)
	s.sendWhopMessages(ctx, c, st)
}

// whopStoreNeedsALook records why the store's pass changed nothing, as its
// problem, for its page to say it needs a look, and has the next pass that
// gets further read the store and every membership again: what happened
// meanwhile counts, and the problem goes once the store is back.
func (s *Server) whopStoreNeedsALook(storeID, problem string) {
	s.log.Warn("a store's pass changed nothing", "store", storeID, "problem", problem)
	if _, err := s.db.Exec(`UPDATE whop_stores SET problem = ?, synced_at = 0, polled_at = 0 WHERE store_id = ?`, problem, storeID); err != nil {
		s.log.Error("could not record a store's problem", "store", storeID, "err", err)
	}
}

// whopTakenOver says whether another dashboard took the store over, as
// reading the store may have just found.
func (s *Server) whopTakenOver(storeID string) bool {
	var by string
	return s.db.QueryRow(`SELECT taken_over_by FROM whop_stores WHERE store_id = ?`, storeID).Scan(&by) == nil && by != ""
}

// stillSellsFor reads the store's products again just before the
// reconciler writes stock, calls the core or sends messages, so a dashboard
// another one took the store over from stops within a pass rather than at
// its next read of the store. That holds without an address too, since a
// dashboard's own marks are the address it last marked with. Without
// Whop's answer, or when the machine can't be asked for its address, it
// does none of that this pass. The one-seller marks are the key store's,
// so an app store's pass goes on.
func (s *Server) stillSellsFor(ctx context.Context, c *whop.Client, st whopStore) bool {
	if st.Via != whopViaKey {
		return true
	}
	dash, err := s.dashboardURL(ctx)
	if err != nil {
		return false
	}
	products, err := c.Products(ctx, st.ID)
	if err != nil {
		s.log.Warn("could not check that this dashboard still sells for the store", "err", err)
		return false
	}
	var markedAs string
	if err := s.db.QueryRowContext(ctx, `SELECT marked_as FROM whop_stores WHERE store_id = ?`, st.ID).Scan(&markedAs); err != nil {
		return false
	}
	taken, err := s.noticeTakeover(ctx, st.ID, products, dash, markedAs)
	return err == nil && !taken
}

// ensureWhopWebhook adds the key store's webhook, or moves it to the
// dashboard's address when that changed. Without an address there's
// nowhere to send to.
func (s *Server) ensureWhopWebhook(ctx context.Context, c *whop.Client, st *whopStore, dash string) error {
	if dash == "" {
		return nil
	}
	want := dash + whopWebhookPath
	if st.WebhookID != "" && st.WebhookURL == want {
		return nil
	}
	if st.WebhookID != "" {
		err := c.UpdateWebhookURL(ctx, st.WebhookID, want)
		if err == nil {
			st.WebhookURL = want
			_, err = s.db.Exec(`UPDATE whop_stores SET webhook_url = ? WHERE store_id = ?`, want, st.ID)
			return err
		}
		if !whop.NotFound(err) {
			return err
		}
	}
	hook, err := c.CreateWebhook(ctx, st.ID, want)
	if err != nil {
		return err
	}
	st.WebhookID, st.WebhookURL, st.WebhookSecret = hook.ID, want, hook.Secret
	_, err = s.db.Exec(`UPDATE whop_stores SET webhook_id = ?, webhook_url = ?, webhook_secret = ? WHERE store_id = ?`, hook.ID, want, hook.Secret, st.ID)
	return err
}

// whopHooked says whether the store's own webhook points at the dashboard
// at dash.
func whopHooked(st whopStore, dash string) bool {
	return st.WebhookID != "" && dash != "" && st.WebhookURL == dash+whopWebhookPath
}

// refreshWhopMemberships reads every membership of the store from Whop once
// every every, and in between only those a webhook told of.
func (s *Server) refreshWhopMemberships(ctx context.Context, c *whop.Client, st whopStore, every time.Duration) error {
	now := s.now()
	if now.Sub(st.PolledAt) >= every {
		all, err := c.Memberships(ctx, st.ID)
		if err != nil {
			return err
		}
		for _, m := range all {
			if err := s.keepMembership(st.ID, m, false); err != nil {
				return err
			}
		}
		_, err = s.db.Exec(`UPDATE whop_stores SET polled_at = ? WHERE store_id = ?`, now.UnixMilli(), st.ID)
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT membership_id FROM whop_memberships WHERE store_id = ? AND stale = 1`, st.ID)
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
			if _, err := s.db.Exec(`DELETE FROM whop_memberships WHERE store_id = ? AND membership_id = ?`, st.ID, id); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := s.keepMembership(st.ID, m, false); err != nil {
			return err
		}
	}
	return nil
}

// refreshWhopPlans reads the store again every whopPollEvery, so a plan's
// allowance changed on Whop reaches its customers, and sooner when the
// store may be out of date (see whopStoreDue). It says whether it just read
// the store.
func (s *Server) refreshWhopPlans(ctx context.Context, c *whop.Client, st whopStore) bool {
	if !s.whopStoreDue(ctx, st) {
		return false
	}
	problem := ""
	if err := s.readWhopStore(ctx, c, st.ID, false); err != nil {
		problem = whopProblem(err)
		s.log.Warn("could not read the store on Whop", "store", st.ID, "err", err)
	}
	if _, err := s.db.Exec(`UPDATE whop_stores SET synced_at = ?, problem = ? WHERE store_id = ?`, s.now().UnixMilli(), problem, st.ID); err != nil {
		s.log.Error("could not record reading the store on Whop", "err", err)
	}
	return problem == ""
}

// whopStoreDue says whether the reconciler reads the store this pass: every
// whopPollEvery; at once when the products name another address than the
// dashboard's, since the store sends buyers there and the old one may no
// longer answer (the dashboard losing port 443, say), but only a minute
// after a read that failed; and two minutes after the last read when a
// membership is of a plan the dashboard hasn't read yet.
func (s *Server) whopStoreDue(ctx context.Context, st whopStore) bool {
	since := s.now().Sub(st.SyncedAt)
	if since >= whopPollEvery {
		return true
	}
	if st.Problem == "" || since >= time.Minute {
		if dash, err := s.dashboardURL(ctx); err == nil && dash != "" && dash != st.MarkedAs {
			return true
		}
	}
	var unknown int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM whop_memberships m WHERE m.store_id = ? AND m.stale = 0
		AND NOT EXISTS (SELECT 1 FROM whop_plans p WHERE p.store_id = m.store_id AND p.plan_id = m.plan_id)`, st.ID).Scan(&unknown)
	return err == nil && unknown > 0 && since >= 2*time.Minute
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
	// Hosting is each confirmed membership of theirs that gives servers,
	// with what the payment check found for it, since in an app store only
	// those it found paid count (whopPaidPlan).
	Hosting []whopHosting
}

// whopHosting is one membership that gives a customer servers: Part, what
// its plan gives now, and what the payment check found, Paid, what it gave
// when a payment of it last carried Playkeeper's share (zero for never),
// and while it isn't paid for Part, the Problem it found, its Attempts and
// when it looks again.
type whopHosting struct {
	ID          string
	Part, Paid  planPart
	Problem     string
	Attempts    int
	NextCheckAt int64
}

// paidFor says whether a payment of the membership carried the share for
// what its plan gives now: the share is by memory alone.
func (h whopHosting) paidFor() bool {
	return h.Paid.memoryMB > 0 && h.Paid.memoryMB >= h.Part.memoryMB
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

// whopCustomers reads every customer the dashboard knows of the store, from
// their memberships of its plans and what was done for them, newest first.
func (s *Server) whopCustomers(ctx context.Context, storeID string) ([]whopCustomer, error) {
	var via string
	if err := s.db.QueryRowContext(ctx, `SELECT via FROM whop_stores WHERE store_id = ?`, storeID).Scan(&via); err != nil && !isNoRows(err) {
		return nil, err
	}
	byID := map[string]*whopCustomer{}
	get := func(id string) *whopCustomer {
		if wc, ok := byID[id]; ok {
			return wc
		}
		wc := &whopCustomer{WhopUserID: id}
		byID[id] = wc
		return wc
	}
	rows, err := s.db.QueryContext(ctx, `SELECT whop_user_id, handle, applied, paused, attempts, next_try_at, problem, updated_at FROM whop_customers WHERE store_id = ?`, storeID)
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
	rows, err = s.db.QueryContext(ctx, `SELECT m.whop_user_id, m.membership_id, m.plan_id, m.status, m.stale, m.updated_at,
		COALESCE(p.title, ''), COALESCE(p.allowance_servers, 0), COALESCE(p.allowance_memory_mb, 0), COALESCE(p.disk_gb, 0), COALESCE(p.allowance_from, ''),
		COALESCE(k.paid_plan_id, ''), COALESCE(k.paid_title, ''), COALESCE(k.paid_servers, 0), COALESCE(k.paid_mb, 0), COALESCE(k.paid_disk_gb, 0),
		COALESCE(k.problem, ''), COALESCE(k.attempts, 0), COALESCE(k.next_check_at, 0)
		FROM whop_memberships m LEFT JOIN whop_plans p ON p.store_id = m.store_id AND p.plan_id = m.plan_id
		LEFT JOIN whop_membership_checks k ON k.store_id = m.store_id AND k.membership_id = m.membership_id
		WHERE m.store_id = ? ORDER BY m.updated_at, m.membership_id`, storeID)
	if err != nil {
		return nil, err
	}
	parts := map[string][]planPart{}
	for rows.Next() {
		var id, from string
		var pt planPart
		var h whopHosting
		var status string
		var stale bool
		var updated int64
		if err := rows.Scan(&id, &h.ID, &pt.id, &status, &stale, &updated, &pt.name, &pt.servers, &pt.memoryMB, &pt.diskGB, &from,
			&h.Paid.id, &h.Paid.name, &h.Paid.servers, &h.Paid.memoryMB, &h.Paid.diskGB, &h.Problem, &h.Attempts, &h.NextCheckAt); err != nil {
			rows.Close()
			return nil, err
		}
		wc := get(id)
		wc.Latest, wc.UpdatedAt = status, max(wc.UpdatedAt, updated)
		switch {
		case stale:
			wc.Unconfirmed++
		case from != "" && whopHosts(via, status):
			parts[id] = append(parts[id], pt)
			h.Part = pt
			wc.Hosting = append(wc.Hosting, h)
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
// waits for their next try. While the store is closed, nobody starts. In an
// app store, only memberships whose payment carried Playkeeper's share
// count (whopPaidPlan). Each is the core's customer of the store.
func (s *Server) syncWhopCustomers(ctx context.Context, c *whop.Client, st whopStore) {
	custs, err := s.whopCustomers(ctx, st.ID)
	if err != nil {
		s.log.Error("could not list Whop customers", "store", st.ID, "err", err)
		return
	}
	now := s.now().UnixMilli()
	for _, wc := range custs {
		if wc.Unconfirmed > 0 || wc.NextTryAt > now {
			continue
		}
		waits := ""
		if st.Via == whopViaApp {
			wc.Plan, waits = s.whopPaidPlan(ctx, c, st, wc)
		}
		if err := s.stepWhopCustomer(ctx, c, st, wc); err != nil {
			s.whopCustomerFailed(st.ID, wc, err)
			continue
		}
		if st.Via == whopViaApp {
			s.noteWhopPaymentProblem(st.ID, wc.WhopUserID, waits)
		}
	}
}

func (s *Server) stepWhopCustomer(ctx context.Context, c *whop.Client, st whopStore, wc whopCustomer) error {
	store := st.ID
	has := wc.Plan.Servers > 0 && wc.Plan.MemoryMB > 0
	cust := Customer{Provider: whopProvider, Store: store, Subject: wc.WhopUserID, Handle: wc.Handle}
	// Taken before the core's call, since the fleet may count the customer
	// in its room before the call returns (see pushWhopStock).
	at := s.now()
	switch {
	case has && (wc.Applied == "" || wc.Paused) && st.ClosedWhy != "":
		// A closed store starts nobody: they start once it opens.
	case has && (wc.Applied == "" || wc.Paused):
		if cust.Handle == "" {
			u, err := c.User(ctx, wc.WhopUserID)
			if err != nil {
				return fmt.Errorf("couldn't read their username on Whop: %w", err)
			}
			cust.Handle = u.Username
			if _, err := s.db.Exec(`INSERT INTO whop_customers(store_id, whop_user_id, handle, updated_at) VALUES(?,?,?,?)
				ON CONFLICT(store_id, whop_user_id) DO UPDATE SET handle = excluded.handle`, store, cust.Subject, cust.Handle, s.now().UnixMilli()); err != nil {
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

// recordWhopCustomer notes the plan the core now has for a customer of
// their store, since at, and whether they're paused, clearing any problem.
func (s *Server) recordWhopCustomer(cust Customer, applied string, paused bool, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO whop_customers(store_id, whop_user_id, handle, applied, paused, applied_at, updated_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(store_id, whop_user_id) DO UPDATE SET handle = excluded.handle, applied = excluded.applied, paused = excluded.paused, applied_at = excluded.applied_at,
		attempts = 0, next_try_at = 0, problem = '', updated_at = excluded.updated_at`,
		cust.Store, cust.Subject, cust.Handle, applied, paused, at.UnixMilli(), s.now().UnixMilli())
	return err
}

// whopCustomerFailed records why the call for a customer of the store
// failed, and when to try again: soon at first, then less often, at most
// every six hours.
func (s *Server) whopCustomerFailed(store string, wc whopCustomer, err error) {
	s.log.Warn("could not bring a Whop customer in line with their plans", "store", store, "customer", wc.WhopUserID, "err", err)
	problem := err.Error()
	var we *whop.Error
	if errors.As(err, &we) {
		problem = whopProblem(err)
	}
	if _, dbErr := s.db.Exec(`INSERT INTO whop_customers(store_id, whop_user_id, attempts, next_try_at, problem, updated_at) VALUES(?,?,1,?,?,?)
		ON CONFLICT(store_id, whop_user_id) DO UPDATE SET attempts = attempts + 1, next_try_at = excluded.next_try_at, problem = excluded.problem`,
		store, wc.WhopUserID, s.now().Add(whopBackoff(wc.Attempts)).UnixMilli(), problem, s.now().UnixMilli()); dbErr != nil {
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

// Notify queues a message for a Whop customer, to go out in the support
// chat of the store they bought from, from the reconciler's next look. It
// never waits on Whop.
func (n billingNotifier) Notify(ctx context.Context, c Customer, m CustomerMessage) error {
	switch c.Provider {
	case whopProvider:
		return n.s.queueWhopMessage(ctx, c.Store, c.Subject, m.Kind, m.Text)
	default:
		return fmt.Errorf("no billing provider called %q", c.Provider)
	}
}

// errNotThisStore refuses a message for a customer of a store the dashboard
// doesn't sell for, which would go out in another store's chat.
var errNotThisStore = errors.New("that customer bought from a store this dashboard doesn't sell for")

// queueWhopMessage queues a message for the Whop user who bought from
// store, which must be a store the dashboard sells for: the message goes
// out in that store's support chat and no other.
func (s *Server) queueWhopMessage(ctx context.Context, store, whopUserID, kind, text string) error {
	if !reWhopID.MatchString(whopUserID) || strings.TrimSpace(text) == "" {
		return errors.New("a message needs a Whop user and some words")
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO whop_messages(store_id, whop_user_id, kind, text, created_at) SELECT store_id, ?, ?, ?, ? FROM whop_stores WHERE store_id = ?`,
		whopUserID, kind, text, s.now().UnixMilli(), store)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotThisStore
	}
	s.kickWhopStore(store)
	return nil
}

// remindCancelled reminds a customer of the store who cancelled the plans
// that keep their servers running to download their world before the last
// one ends, once for those cancellations. Cancelling one plan while another
// goes on stops nothing, so it says nothing.
func (s *Server) remindCancelled(ctx context.Context, st whopStore) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.membership_id, m.whop_user_id, m.period_end, m.told_cancel FROM whop_memberships m
		JOIN whop_plans p ON p.store_id = m.store_id AND p.plan_id = m.plan_id AND p.allowance_from != ''
		JOIN whop_customers c ON c.store_id = m.store_id AND c.whop_user_id = m.whop_user_id AND c.applied != '' AND c.paused = 0
		WHERE m.store_id = ? AND m.stale = 0 AND m.status IN `+whopAccess+` AND NOT EXISTS (
			SELECT 1 FROM whop_memberships o JOIN whop_plans q ON q.store_id = o.store_id AND q.plan_id = o.plan_id AND q.allowance_from != ''
			WHERE o.store_id = m.store_id AND o.whop_user_id = m.whop_user_id AND o.stale = 0 AND o.status IN `+whopAccess+` AND o.cancel_at_period_end = 0)`, st.ID)
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
			"To keep a copy of your world, download it before then: open your server, then World › Backups › Download.", whopName(st.Account), when)
		if err := s.queueWhopMessage(ctx, st.ID, user, "cancelling", text); err != nil {
			s.log.Error("could not queue a cancellation reminder", "err", err)
			continue
		}
		for _, id := range e.memberships {
			if _, err := s.db.Exec(`UPDATE whop_memberships SET told_cancel = 1 WHERE store_id = ? AND membership_id = ?`, st.ID, id); err != nil {
				s.log.Error("could not record a cancellation reminder", "err", err)
			}
		}
	}
}

// sendWhopMessages sends the store's messages waiting to go, oldest first,
// each in the store's support chat with its customer, opened the first
// time. A customer's messages go in order: after one fails, theirs wait for
// its next try.
func (s *Server) sendWhopMessages(ctx context.Context, c *whop.Client, st whopStore) {
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM whop_messages WHERE store_id = ? AND sent_at != 0 AND sent_at < ?`, st.ID, now.Add(-whopMessagesKept).UnixMilli()); err != nil {
		s.log.Error("could not forget old Whop messages", "err", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT w.id, w.whop_user_id, w.text, w.attempts, w.next_try_at, COALESCE(c.channel_id, '')
		FROM whop_messages w LEFT JOIN whop_customers c ON c.store_id = w.store_id AND c.whop_user_id = w.whop_user_id
		WHERE w.store_id = ? AND w.sent_at = 0 ORDER BY w.id LIMIT 100`, st.ID)
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
			token, tokenErr = whopSenderToken(ctx, c, st)
		}
		if tokenErr != nil {
			s.whopMessageFailed(m.id, m.attempts, tokenErr)
			held[m.user] = true
			continue
		}
		channel := cmpOr(channels[m.user], m.channel)
		if channel == "" {
			ch, err := c.OpenSupportChat(ctx, st.ID, m.user)
			if err != nil {
				s.whopMessageFailed(m.id, m.attempts, err)
				held[m.user] = true
				continue
			}
			channel = ch
			if _, err := s.db.Exec(`INSERT INTO whop_customers(store_id, whop_user_id, channel_id, updated_at) VALUES(?,?,?,?)
				ON CONFLICT(store_id, whop_user_id) DO UPDATE SET channel_id = excluded.channel_id`, st.ID, m.user, ch, now.UnixMilli()); err != nil {
				s.log.Error("could not record a Whop customer's support chat", "err", err)
			}
		}
		channels[m.user] = channel
		if err := c.SendMessage(ctx, token, channel, m.text); err != nil {
			if whop.NotFound(err) {
				// The chat is gone; the next try opens another.
				_, _ = s.db.Exec(`UPDATE whop_customers SET channel_id = '' WHERE store_id = ? AND whop_user_id = ?`, st.ID, m.user)
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
// is in each of its support chats, since Whop takes none from a key. It
// may send support chat messages and nothing else, and the dashboard gets a
// new one each time it sends rather than keep one.
func whopSenderToken(ctx context.Context, c *whop.Client, st whopStore) (string, error) {
	owner, err := whopOwner(ctx, c, st)
	if err != nil {
		return "", fmt.Errorf("couldn't read who owns the store on Whop: %w", err)
	}
	return c.UserToken(ctx, st.ID, owner.ID, whop.MessageAction)
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

// whopCustomerViews lists the customers the dashboard knows of store,
// newest first.
func (s *Server) whopCustomerViews(ctx context.Context, store string) ([]whopCustomerView, error) {
	custs, err := s.whopCustomers(ctx, store)
	if err != nil {
		return nil, err
	}
	waiting, err := s.whopMessageProblems(ctx, store)
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
		if info, ok, err := s.hosting.CustomerAccount(ctx, whopProvider, store, wc.WhopUserID); err == nil && ok {
			v.Account = info.Username
		}
		out = append(out, v)
	}
	return out, nil
}

// whopMessageProblems is why the message that failed of each of the store's
// customers hasn't gone out yet. Theirs go in order, so the oldest one
// waiting is the one being tried.
func (s *Server) whopMessageProblems(ctx context.Context, store string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT whop_user_id, problem FROM whop_messages WHERE store_id = ? AND sent_at = 0 AND problem != '' ORDER BY id`, store)
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
