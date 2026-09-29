package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// A buyer of a plan the store sells gets a creator invite with the plan's
// allowance, sent in the store's support chat with them on Whop; the rest
// is the invite page and New server, as for any creator. Whop tells the
// dashboard about memberships through a webhook at whopWebhookPath, and the
// dashboard also reads them every whopPollEvery, since a webhook that keeps
// failing is switched off and what it missed isn't sent again.
const (
	whopWebhookPath = "/api/public/whop/webhook"
	whopPollEvery   = 10 * time.Minute
	// maxWhopDelivery bounds a webhook delivery; a membership event is a
	// few kilobytes.
	maxWhopDelivery = 256 << 10
	// whopDeliveriesKept is how long a delivery's id is kept, well past the
	// three days Whop retries one.
	whopDeliveriesKept = 7 * 24 * time.Hour
)

// whopWebhookLimits let Whop's few senders deliver bursts, and bound anyone
// else who posts there.
var whopWebhookLimits = publicLimits{perMinute: 600, open: 16, read: 10 * time.Second, write: 10 * time.Second}

// reWhopID is the shape of Whop's ids, such as mem_… and user_….
var reWhopID = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// whopAccess are the statuses in which a membership grants access (see
// whop.Membership.HasAccess).
const whopAccess = `('trialing', 'active', 'past_due', 'completed')`

// whopWebhook receives Whop's deliveries: it checks the signature with the
// webhook's secret, counts each delivery once, keeps what a membership
// event says for the reconciler to act on, and answers at once, since Whop
// waits five seconds.
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
// since deliveries can come out of order.
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
		cancel_at_period_end = excluded.cancel_at_period_end, period_end = excluded.period_end, stale = excluded.stale, updated_at = excluded.updated_at`,
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
// every minute, which polls Whop every whopPollEvery and retries invites
// that couldn't be sent.
func (s *Server) runWhop(ctx context.Context) {
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

// reconcileWhop brings buyers in line with their memberships: it keeps the
// webhook pointing at the dashboard, reads memberships from Whop, and sends
// each buyer with access and no account an invite.
func (s *Server) reconcileWhop(ctx context.Context) {
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	a, ok, err := s.storedWhop()
	if err != nil || !ok {
		return
	}
	c, err := s.whopClient(a.Key)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	dash := s.dashboardURL(ctx)
	if err := s.ensureWhopWebhook(ctx, c, &a, dash); err != nil {
		s.log.Warn("could not keep Whop's webhook pointing at this dashboard", "err", err)
	}
	if err := s.refreshWhopMemberships(ctx, c, a); err != nil {
		s.log.Warn("could not read memberships from Whop", "err", err)
	}
	if dash != "" {
		s.inviteWhopBuyers(ctx, c, a, dash)
	}
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
			_, err = s.db.Exec(`DELETE FROM whop_memberships WHERE membership_id = ?`, id)
			if err != nil {
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

// whopBuyer is one buyer as the reconciler sees them.
type whopBuyer struct {
	WhopUserID string
	Username   string
	UserID     sql.NullInt64
	JoinedAt   int64
	InviteID   string
	Attempts   int
	NextTryAt  int64
	// Allowance is what their memberships with access allow together.
	Allowance invites.Allowance
}

// inviteWhopBuyers sends an invite to each buyer with access who has no
// account and no invite that still works: a first one, one to replace an
// expired one, or one with a new allowance when their plans changed before
// they joined. A buyer who joined, or whose invite the owner turned off,
// gets none.
func (s *Server) inviteWhopBuyers(ctx context.Context, c *whop.Client, a whopAccount, dash string) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.whop_user_id, SUM(p.allowance_servers), SUM(p.allowance_memory_mb),
		COALESCE(b.username, ''), b.user_id, COALESCE(b.joined_at, 0), COALESCE(b.invite_id, ''), COALESCE(b.attempts, 0), COALESCE(b.next_try_at, 0)
		FROM whop_memberships m JOIN whop_plans p ON p.plan_id = m.plan_id AND p.allowance_from != ''
		LEFT JOIN whop_buyers b ON b.whop_user_id = m.whop_user_id
		WHERE m.status IN `+whopAccess+` GROUP BY m.whop_user_id ORDER BY MIN(m.updated_at)`)
	if err != nil {
		s.log.Error("could not list Whop buyers", "err", err)
		return
	}
	var buyers []whopBuyer
	for rows.Next() {
		var b whopBuyer
		if err := rows.Scan(&b.WhopUserID, &b.Allowance.Servers, &b.Allowance.MemoryMB, &b.Username, &b.UserID, &b.JoinedAt, &b.InviteID, &b.Attempts, &b.NextTryAt); err != nil {
			rows.Close()
			s.log.Error("could not read a Whop buyer", "err", err)
			return
		}
		b.Allowance = capAllowance(b.Allowance)
		buyers = append(buyers, b)
	}
	rows.Close()
	now := s.now()
	for _, b := range buyers {
		if b.UserID.Valid || b.JoinedAt != 0 || b.NextTryAt > now.UnixMilli() {
			continue
		}
		if b.InviteID != "" {
			inv, err := s.inviteByID(b.InviteID)
			if err == nil {
				switch inv.StatusAt(now) {
				case invites.StatusActive:
					if inv.Allowance == b.Allowance {
						continue
					}
				case invites.StatusRevoked, invites.StatusUsedUp:
					// The owner turned it off, or it made an account the
					// owner removed since: either way, not sent again.
					continue
				}
			}
		}
		if err := s.sendWhopInvite(ctx, c, a, dash, b); err != nil {
			s.whopInviteFailed(b, err)
		}
	}
}

// capAllowance keeps what several plans allow together within an
// allowance's bounds.
func capAllowance(al invites.Allowance) invites.Allowance {
	al.Servers = min(al.Servers, invites.MaxAllowanceServers)
	al.MemoryMB = min(al.MemoryMB, invites.MaxAllowanceMemoryMB)
	return al
}

// sendWhopInvite makes a creator invite for a buyer and sends it in the
// store's support chat with them. An invite that couldn't be sent is turned
// off again, since its code is only known now.
func (s *Server) sendWhopInvite(ctx context.Context, c *whop.Client, a whopAccount, dash string, b whopBuyer) error {
	if b.Username == "" {
		if u, err := c.User(ctx, b.WhopUserID); err == nil {
			b.Username = u.Username
		}
	}
	if err := s.keepServersAway(ctx, "whop"); err != nil {
		return fmt.Errorf("couldn't keep servers away from this machine: %w", err)
	}
	label := b.Username
	if len([]rune(label)) > invites.MaxLabelRunes || label == "" {
		label = "Whop buyer"
	}
	var ownerID int64
	if err := s.db.QueryRow(`SELECT id FROM users WHERE role = 'owner'`).Scan(&ownerID); err != nil {
		return fmt.Errorf("this dashboard has no owner yet: %w", err)
	}
	created, err := invites.NewMember(invites.MemberSpec{ProjectID: s.projectID(access{}), Role: invites.RoleAdmin, Label: label, Allowance: b.Allowance},
		s.accountByID(ownerID), nil, s.now())
	if err != nil {
		return err
	}
	if err := s.insertInvite(created.Invite); err != nil {
		return err
	}
	channel, err := c.OpenSupportChat(ctx, a.ID, b.WhopUserID)
	if err == nil {
		err = c.SendMessage(ctx, channel, whopInviteMessage(whopName(a.Account), dash+created.Path, b.Allowance))
	}
	if err != nil {
		s.revokeInvite(created.Invite.ID)
		return err
	}
	if b.InviteID != "" {
		s.revokeInvite(b.InviteID)
	}
	now := s.now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO whop_buyers(whop_user_id, username, invite_id, invited_at, channel_id) VALUES(?,?,?,?,?)
		ON CONFLICT(whop_user_id) DO UPDATE SET username = excluded.username, invite_id = excluded.invite_id, invited_at = excluded.invited_at,
		channel_id = excluded.channel_id, attempts = 0, next_try_at = 0, problem = ''`, b.WhopUserID, b.Username, created.Invite.ID, now, channel); err != nil {
		return err
	}
	s.audit("whop", "invite.create", created.Invite.Actor(), "succeeded",
		fmt.Sprintf("Sell on Whop, for %s; %s; sent in the support chat; works 7 days", cmpOr(b.Username, b.WhopUserID), allowanceText(b.Allowance)))
	return nil
}

func (s *Server) revokeInvite(id string) {
	if _, err := s.db.Exec(`UPDATE invites SET revoked_at = ? WHERE id = ? AND revoked_at = 0`, s.now().UnixMilli(), id); err != nil {
		s.log.Error("could not turn off an invite", "invite", id, "err", err)
	}
}

// whopInviteFailed records why a buyer's invite couldn't be sent, and when
// to try again: soon at first, then less often, at most every six hours.
func (s *Server) whopInviteFailed(b whopBuyer, err error) {
	s.log.Warn("could not send a Whop buyer their invite", "buyer", b.WhopUserID, "err", err)
	wait := min(time.Minute<<min(b.Attempts, 9), 6*time.Hour)
	if _, dbErr := s.db.Exec(`INSERT INTO whop_buyers(whop_user_id, username, attempts, next_try_at, problem) VALUES(?,?,1,?,?)
		ON CONFLICT(whop_user_id) DO UPDATE SET attempts = attempts + 1, next_try_at = excluded.next_try_at, problem = excluded.problem`,
		b.WhopUserID, b.Username, s.now().Add(wait).UnixMilli(), whopProblem(err)); dbErr != nil {
		s.log.Error("could not record a Whop invite that failed", "err", dbErr)
	}
}

// whopInviteMessage is what a buyer reads in the store's support chat.
func whopInviteMessage(store, link string, al invites.Allowance) string {
	servers := "1 server"
	if al.Servers != 1 {
		servers = fmt.Sprintf("%d servers", al.Servers)
	}
	return fmt.Sprintf("Thanks for choosing %s! Your server panel is ready.\n\n**[Open your invite](%s)**\n\n"+
		"It works once, for 7 days: make an account, turn on two-factor sign-in, then create your server, up to %s with %s of memory between them.",
		store, link, servers, whopMemory(al.MemoryMB))
}

func whopMemory(mb int) string {
	if mb%1024 == 0 {
		return fmt.Sprintf("%d GB", mb/1024)
	}
	return fmt.Sprintf("%.1f GB", float64(mb)/1024)
}

// whopBuyerView is one buyer on Settings › Sell on Whop.
type whopBuyerView struct {
	WhopUserID string `json:"whopUserId"`
	Username   string `json:"username,omitempty"`
	// Status is "invited" (an invite that works was sent), "joined" (they
	// made their account), "removed" (the owner removed that account),
	// "turned_off" (the owner turned their invite off), "sending" (no
	// invite could be sent yet) or "ended" (no membership grants access).
	Status    string            `json:"status"`
	Account   string            `json:"account,omitempty"`
	Allowance invites.Allowance `json:"allowance,omitzero"`
	InvitedAt *time.Time        `json:"invitedAt,omitempty"`
	Problem   string            `json:"problem,omitempty"`
}

// whopBuyers lists the buyers the dashboard knows, newest first.
func (s *Server) whopBuyers(ctx context.Context) ([]whopBuyerView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.whop_user_id,
		COALESCE(SUM(CASE WHEN m.status IN `+whopAccess+` AND p.allowance_from != '' THEN p.allowance_servers END), 0),
		COALESCE(SUM(CASE WHEN m.status IN `+whopAccess+` AND p.allowance_from != '' THEN p.allowance_memory_mb END), 0),
		COALESCE(b.username, ''), COALESCE(u.username, ''), COALESCE(b.joined_at, 0), COALESCE(b.invite_id, ''), COALESCE(b.invited_at, 0), COALESCE(b.problem, '')
		FROM whop_memberships m JOIN whop_plans p ON p.plan_id = m.plan_id
		LEFT JOIN whop_buyers b ON b.whop_user_id = m.whop_user_id LEFT JOIN users u ON u.id = b.user_id
		GROUP BY m.whop_user_id ORDER BY MAX(m.updated_at) DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.now()
	out := []whopBuyerView{}
	for rows.Next() {
		var v whopBuyerView
		var joined, invited int64
		var invite string
		if err := rows.Scan(&v.WhopUserID, &v.Allowance.Servers, &v.Allowance.MemoryMB, &v.Username, &v.Account, &joined, &invite, &invited, &v.Problem); err != nil {
			return nil, err
		}
		v.Allowance = capAllowance(v.Allowance)
		if invited != 0 {
			t := time.UnixMilli(invited).UTC()
			v.InvitedAt = &t
		}
		switch {
		case v.Account != "":
			v.Status = "joined"
		case joined != 0:
			v.Status = "removed"
		case v.Allowance.IsZero():
			v.Status = "ended"
		case invite != "":
			v.Status = "sending"
			if inv, err := s.inviteByID(invite); err == nil {
				switch inv.StatusAt(now) {
				case invites.StatusActive:
					v.Status = "invited"
				case invites.StatusRevoked:
					v.Status = "turned_off"
				}
			}
		default:
			v.Status = "sending"
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
