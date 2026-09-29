package panel

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// What happens to a buyer who made their account, as their memberships
// change. Their allowance follows their plans. When their last plan ends,
// their sign-in pauses at once, and each server they created gets a final
// backup, then stops. A plan that starts again within whopGrace brings
// everything back; after it, those servers are deleted, their backups with
// them, while the account stays paused. A buyer who cancels a plan that
// still runs is reminded to download their world. Each change is told in
// the store's support chat. A buyer with a membership Whop's API hasn't
// confirmed yet is left as they are until it does.
const (
	whopGrace = 14 * 24 * time.Hour
	// whopBackupWait is how long a server whose plan ended waits for its
	// final backup before it stops without one.
	whopBackupWait = 30 * time.Minute
)

// What a buyer was last told, so each change is told once.
const (
	toldEnded   = "ended"
	toldDeleted = "deleted"
	toldBack    = "back"
)

// joinedBuyer is a buyer with an account, as the lifecycle sees them.
type joinedBuyer struct {
	WhopUserID, Username, ChannelID, Told string
	UserID                                int64
	Account                               string
	PausedAt, EndedAt, StoppedAt          int64
	DeletedAt                             int64
	// Allowance is what their confirmed memberships with access allow
	// together; zero means none grants access.
	Allowance invites.Allowance
	// Unconfirmed counts memberships a webhook told of that Whop's API
	// hasn't confirmed yet.
	Unconfirmed int
}

// whopLifecycle moves each buyer with an account along, and reminds those
// who cancelled.
func (s *Server) whopLifecycle(ctx context.Context, c *whop.Client, a whopAccount, dash string) {
	s.remindCancelled(ctx, c, a)
	rows, err := s.db.QueryContext(ctx, `SELECT b.whop_user_id, b.username, b.channel_id, b.told, b.user_id, u.username, u.paused_at, b.ended_at, b.stopped_at, b.deleted_at,
		COALESCE(SUM(CASE WHEN m.status IN `+whopAccess+` AND m.stale = 0 AND p.allowance_from != '' THEN p.allowance_servers END), 0),
		COALESCE(SUM(CASE WHEN m.status IN `+whopAccess+` AND m.stale = 0 AND p.allowance_from != '' THEN p.allowance_memory_mb END), 0),
		COALESCE(SUM(m.stale), 0)
		FROM whop_buyers b JOIN users u ON u.id = b.user_id
		LEFT JOIN whop_memberships m ON m.whop_user_id = b.whop_user_id LEFT JOIN whop_plans p ON p.plan_id = m.plan_id
		WHERE u.role != 'owner' GROUP BY b.whop_user_id`)
	if err != nil {
		s.log.Error("could not list Whop buyers with accounts", "err", err)
		return
	}
	var buyers []joinedBuyer
	for rows.Next() {
		var b joinedBuyer
		if err := rows.Scan(&b.WhopUserID, &b.Username, &b.ChannelID, &b.Told, &b.UserID, &b.Account, &b.PausedAt, &b.EndedAt, &b.StoppedAt, &b.DeletedAt,
			&b.Allowance.Servers, &b.Allowance.MemoryMB, &b.Unconfirmed); err != nil {
			rows.Close()
			s.log.Error("could not read a Whop buyer", "err", err)
			return
		}
		b.Allowance = capAllowance(b.Allowance)
		buyers = append(buyers, b)
	}
	rows.Close()
	for _, b := range buyers {
		if b.Unconfirmed > 0 {
			continue
		}
		if err := s.stepBuyer(ctx, c, a, dash, b); err != nil {
			s.log.Warn("could not move a Whop buyer along", "buyer", b.WhopUserID, "err", err)
		}
	}
}

func (s *Server) stepBuyer(ctx context.Context, c *whop.Client, a whopAccount, dash string, b joinedBuyer) error {
	now := s.now()
	store := whopName(a.Account)
	switch {
	case !b.Allowance.IsZero() && b.EndedAt == 0:
		return s.followPlans(ctx, c, a, b)
	case !b.Allowance.IsZero():
		// A plan started again: back in, with what the plans allow now.
		if _, err := s.db.Exec(`UPDATE users SET paused_at = 0 WHERE id = ?`, b.UserID); err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE whop_buyers SET ended_at = 0, stopped_at = 0, deleted_at = 0 WHERE whop_user_id = ?`, b.WhopUserID); err != nil {
			return err
		}
		if err := s.setBuyerAllowance(b, b.Allowance); err != nil {
			return err
		}
		detail := "a plan started again; sign-in open"
		msg := fmt.Sprintf("Welcome back to %s! Your panel is open again: sign in at %s and start your server.", store, dash)
		if b.DeletedAt != 0 {
			detail += "; their servers were deleted before"
			msg = fmt.Sprintf("Welcome back to %s! Your panel is open again at %s. Your old server was deleted after your last plan ended, so create a new one.", store, dash)
		}
		s.audit("whop", "whop.buyer_back", b.Account, "succeeded", detail)
		s.tellBuyer(ctx, c, a, b, toldBack, msg)
		return nil
	case b.EndedAt == 0:
		// Their last plan ended: sign-in pauses now, the servers wind down.
		s.pauseAccount(b.UserID)
		b.EndedAt = now.UnixMilli()
		if _, err := s.db.Exec(`UPDATE whop_buyers SET ended_at = ?, stopped_at = 0 WHERE whop_user_id = ?`, b.EndedAt, b.WhopUserID); err != nil {
			return err
		}
		s.audit("whop", "whop.buyer_ended", b.Account, "succeeded", "their plan ended; sign-in paused, servers get a final backup and stop")
		s.tellBuyer(ctx, c, a, b, toldEnded, fmt.Sprintf("Your %s plan ended, so your server is stopped and your panel is paused. "+
			"Renew within 14 days to pick up where you left off: your world and its backups are kept until %s, then deleted.", store, now.Add(whopGrace).Format("2 January")))
		return s.windDown(ctx, b)
	case b.DeletedAt == 0 && !now.Before(time.UnixMilli(b.EndedAt).Add(whopGrace)):
		gone, err := s.deleteBuyerServers(ctx, b)
		if err != nil || !gone {
			return err
		}
		if _, err := s.db.Exec(`UPDATE whop_buyers SET deleted_at = ? WHERE whop_user_id = ?`, now.UnixMilli(), b.WhopUserID); err != nil {
			return err
		}
		s.audit("whop", "whop.buyer_deleted", b.Account, "succeeded", "14 days after their plan ended; their servers were deleted with their backups")
		s.tellBuyer(ctx, c, a, b, toldDeleted, fmt.Sprintf("Your %s server and its backups were deleted, 14 days after your plan ended. Buy a plan again anytime to start a new one.", store))
		return nil
	case b.DeletedAt == 0 && b.StoppedAt == 0:
		return s.windDown(ctx, b)
	}
	return nil
}

// followPlans gives a buyer whose plans still grant access what those plans
// allow now.
func (s *Server) followPlans(ctx context.Context, c *whop.Client, a whopAccount, b joinedBuyer) error {
	var cur invites.Allowance
	if err := s.db.QueryRow(`SELECT allowance_servers, allowance_memory_mb FROM project_members WHERE user_id = ?`, b.UserID).Scan(&cur.Servers, &cur.MemoryMB); err != nil {
		return err
	}
	if cur == b.Allowance {
		return nil
	}
	if err := s.setBuyerAllowance(b, b.Allowance); err != nil {
		return err
	}
	s.audit("whop", "whop.buyer_plan", b.Account, "succeeded", fmt.Sprintf("%s, from their plans on Whop", allowanceText(b.Allowance)))
	servers := "1 server"
	if b.Allowance.Servers != 1 {
		servers = fmt.Sprintf("%d servers", b.Allowance.Servers)
	}
	s.tellBuyer(ctx, c, a, b, "", fmt.Sprintf("Your %s plan changed: you can now have up to %s with %s of memory between them.", whopName(a.Account), servers, whopMemory(b.Allowance.MemoryMB)))
	return nil
}

func (s *Server) setBuyerAllowance(b joinedBuyer, al invites.Allowance) error {
	s.creators.Lock()
	defer s.creators.Unlock()
	_, err := s.db.Exec(`UPDATE project_members SET allowance_servers = ?, allowance_memory_mb = ? WHERE user_id = ? AND (allowance_servers > 0 OR allowance_memory_mb > 0)`,
		al.Servers, al.MemoryMB, b.UserID)
	return err
}

// pauseAccount stops an account from signing in: its sessions, sign-ins
// waiting for their second step and API tokens end, and a paused account
// can't start new ones (see hLogin and lookupSession). The owner is never
// paused.
func (s *Server) pauseAccount(userID int64) {
	now := s.now().UnixMilli()
	res, err := s.db.Exec(`UPDATE users SET paused_at = ? WHERE id = ? AND role != 'owner' AND paused_at = 0`, now, userID)
	if err != nil {
		s.log.Error("could not pause an account", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return
	}
	s.deleteUserSessions(userID)
	s.revokeAccountTokens(userID, "whop", "its account was paused when its plan ended")
}

// buyerServers are the servers a buyer created on the dashboard's machine.
func (s *Server) buyerServers(userID int64) ([]string, error) {
	return s.creatorServers(userID)
}

// windDown gives each of a buyer's servers a final backup, then stops it; a
// server whose backup doesn't come within whopBackupWait stops without one.
// It's done once every server is stopped.
func (s *Server) windDown(ctx context.Context, b joinedBuyer) error {
	ids, err := s.buyerServers(b.UserID)
	if err != nil {
		return err
	}
	ended := time.UnixMilli(b.EndedAt)
	done := true
	ctx = asActor(ctx, "whop")
	for _, id := range ids {
		var st api.ServerStatus
		status, err := s.agent.Do(ctx, "GET", "/v1/servers/"+url.PathEscape(id), nil, nil, &st)
		if status == http.StatusNotFound {
			continue
		}
		if err != nil || status != http.StatusOK {
			done = false
			continue
		}
		if st.Phase == api.PhaseStopped || st.Phase == api.PhaseCrashed || st.Phase == api.PhaseNotCreated {
			continue
		}
		done = false
		var backups []api.Backup
		if status, err := s.agent.Do(ctx, "GET", "/v1/servers/"+url.PathEscape(id)+"/backups", nil, nil, &backups); err != nil || status != http.StatusOK {
			continue
		}
		final := false
		for _, bk := range backups {
			final = final || !bk.CreatedAt.Before(ended)
		}
		if !final && s.now().Sub(ended) < whopBackupWait {
			// Refused while another operation runs; the next look asks again.
			_, _ = s.agent.Do(ctx, "POST", "/v1/servers/"+url.PathEscape(id)+"/backups", nil, api.BackupRequest{Actor: "whop", Note: "Final backup: the plan ended"}, nil)
			continue
		}
		_, _ = s.agent.Do(ctx, "POST", "/v1/servers/"+url.PathEscape(id)+"/stop", nil, api.ActionRequest{Actor: "whop"}, nil)
	}
	if !done {
		return nil
	}
	_, err = s.db.Exec(`UPDATE whop_buyers SET stopped_at = ? WHERE whop_user_id = ?`, s.now().UnixMilli(), b.WhopUserID)
	return err
}

// deleteBuyerServers deletes each server a buyer created, with its backups,
// and reports whether they're all gone.
func (s *Server) deleteBuyerServers(ctx context.Context, b joinedBuyer) (bool, error) {
	ids, err := s.buyerServers(b.UserID)
	if err != nil {
		return false, err
	}
	gone := true
	ctx = asActor(ctx, "whop")
	for _, id := range ids {
		var st api.ServerStatus
		status, err := s.agent.Do(ctx, "GET", "/v1/servers/"+url.PathEscape(id), nil, nil, &st)
		if status == http.StatusNotFound {
			if _, err := s.db.Exec(`DELETE FROM creator_servers WHERE server_id = ?`, id); err != nil {
				return false, err
			}
			continue
		}
		gone = false
		if err != nil || status != http.StatusOK {
			continue
		}
		_, _ = s.agent.Do(ctx, "POST", "/v1/servers/"+url.PathEscape(id)+"/delete", nil, api.DeleteServerRequest{Actor: "whop", Confirm: st.Name, ForgetKey: true}, nil)
	}
	return gone, nil
}

// remindCancelled tells each buyer with an account who cancelled a plan
// that still runs to download their world before it ends, once a
// membership.
func (s *Server) remindCancelled(ctx context.Context, c *whop.Client, a whopAccount) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.membership_id, m.period_end, b.whop_user_id, b.username, b.channel_id, b.told, b.user_id, u.username
		FROM whop_memberships m JOIN whop_buyers b ON b.whop_user_id = m.whop_user_id JOIN users u ON u.id = b.user_id
		WHERE m.status IN `+whopAccess+` AND m.cancel_at_period_end = 1 AND m.told_cancel = 0 AND m.stale = 0`)
	if err != nil {
		s.log.Error("could not list cancelled Whop memberships", "err", err)
		return
	}
	type reminder struct {
		membership string
		end        int64
		b          joinedBuyer
	}
	var due []reminder
	for rows.Next() {
		var r reminder
		if err := rows.Scan(&r.membership, &r.end, &r.b.WhopUserID, &r.b.Username, &r.b.ChannelID, &r.b.Told, &r.b.UserID, &r.b.Account); err != nil {
			rows.Close()
			return
		}
		due = append(due, r)
	}
	rows.Close()
	for _, r := range due {
		when := "the end of its period"
		if r.end != 0 {
			when = time.UnixMilli(r.end).UTC().Format("2 January")
		}
		if s.tellBuyer(ctx, c, a, r.b, "", fmt.Sprintf("You cancelled your %s plan. It keeps running until %s. "+
			"Download your world before then: open your server, then World › Backups › Download.", whopName(a.Account), when)) {
			_, _ = s.db.Exec(`UPDATE whop_memberships SET told_cancel = 1 WHERE membership_id = ?`, r.membership)
		}
	}
}

// tellBuyer sends a buyer a message in the store's support chat, and
// reports whether it went. told, when set, records what they were told, and
// a buyer already told it isn't told again.
func (s *Server) tellBuyer(ctx context.Context, c *whop.Client, a whopAccount, b joinedBuyer, told, msg string) bool {
	if told != "" && b.Told == told {
		return true
	}
	channel := b.ChannelID
	if channel == "" {
		var err error
		if channel, err = c.OpenSupportChat(ctx, a.ID, b.WhopUserID); err != nil {
			s.log.Warn("could not open a Whop buyer's support chat", "buyer", b.WhopUserID, "err", err)
			return false
		}
	}
	if err := c.SendMessage(ctx, channel, msg); err != nil {
		s.log.Warn("could not message a Whop buyer", "buyer", b.WhopUserID, "err", err)
		return false
	}
	if told != "" {
		if _, err := s.db.Exec(`UPDATE whop_buyers SET told = ?, channel_id = ? WHERE whop_user_id = ?`, told, channel, b.WhopUserID); err != nil {
			s.log.Error("could not record what a Whop buyer was told", "err", err)
		}
	}
	return true
}
