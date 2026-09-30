package panel

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

// Pausing a customer whose plan ended (step 6 of the managed-beta plan).
// Their servers stop, starting or changing anything is refused, and their
// API tokens are revoked, but they may still sign in to see their servers
// and download backups (decision 3). Renewing within the grace period brings
// everything back as it was, with the servers stopped until they start
// them. A suspended customer, whom only the owner pauses, gets none of this
// undone by anything their billing provider does.

// graceDays is how long a paused customer's servers are kept.
const graceDays = 14

// Kinds of the messages pausing and resuming send.
const (
	messagePaused = "paused"
	messageBack   = "back"
)

const backText = "Welcome back: your Playkeeper plan is active again, and your servers can start whenever you like."

// pausedMay are what a paused customer may still do: look at their servers,
// download backups, and look after their own account.
var pausedMay = map[action]bool{actView: true, actMakeBackups: true, actManageAccount: true}

var (
	// errCustomerPaused refuses a paused customer all but what pausedMay has.
	errCustomerPaused = &invites.Error{Code: api.CodeForbidden, Status: http.StatusForbidden,
		Msg: "Your plan has ended, so your servers are paused.", Hint: "You can still see them and download backups. Renew your plan to start them again."}
	// errCustomerSuspended refuses a suspended customer everything.
	errCustomerSuspended = &invites.Error{Code: api.CodeForbidden, Status: http.StatusForbidden,
		Msg: "This account is suspended.", Hint: "Ask the owner of this Playkeeper."}
	// errServerPaused and errServerSuspended refuse the people a customer
	// shares their servers with what the customer may no longer do on them.
	errServerPaused = &invites.Error{Code: api.CodeForbidden, Status: http.StatusForbidden,
		Msg: "This server's plan has ended, so it's paused.", Hint: "It can start again once whoever made it renews their plan."}
	errServerSuspended = &invites.Error{Code: api.CodeForbidden, Status: http.StatusForbidden,
		Msg: "The account this server belongs to is suspended.", Hint: "Ask the owner of this Playkeeper."}
)

// PauseCustomer is for a customer whose access ended: their servers stop,
// their API tokens are revoked, and the grace period starts. A customer who
// is paused already, or suspended, stays as they are.
func (c customerCore) PauseCustomer(ctx context.Context, cust Customer, reason string) error {
	s := c.s
	s.customersMu.Lock()
	defer s.customersMu.Unlock()
	info, ok, err := c.CustomerAccount(ctx, cust.Provider, cust.Subject)
	switch {
	case err != nil:
		return err
	case !ok:
		return errNoCustomer
	case info.State != CustomerActive:
		return nil
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}
	now := s.now()
	until := now.Add(graceDays * 24 * time.Hour)
	if _, err := s.db.ExecContext(ctx, `UPDATE customers SET state = ?, paused_at = ?, delete_after = ?, pause_reason = ?, updated_at = ? WHERE user_id = ? AND state = ?`,
		string(CustomerPaused), now.UnixMilli(), until.UnixMilli(), reason, now.UnixMilli(), info.UserID, string(CustomerActive)); err != nil {
		return errDB
	}
	s.audit(cust.Provider, "customer.pause", info.Username, "succeeded", reason)
	s.kickDiskLimits()
	s.revokeAccountTokens(info.UserID, cust.Provider, "their plan ended")
	s.stopCustomerServers(ctx, info.UserID, cust.Provider)
	if err := s.notifier.Notify(ctx, cust, CustomerMessage{Kind: messagePaused, Text: pausedText(until)}); err != nil {
		s.log.Warn("could not tell a customer their plan ended", "user", info.UserID, "err", err)
	}
	return nil
}

// resumeCustomer brings back a customer whose plan ended and who renewed:
// they're active again, and their servers stay stopped until they start
// them. The caller holds s.customersMu.
func (s *Server) resumeCustomer(ctx context.Context, cust Customer, info *CustomerAccountInfo) error {
	res, err := s.db.ExecContext(ctx, `UPDATE customers SET state = ?, paused_at = 0, delete_after = 0, pause_reason = '', servers_deleted_at = 0, updated_at = ? WHERE user_id = ? AND state = ?`,
		string(CustomerActive), s.now().UnixMilli(), info.UserID, string(CustomerPaused))
	if err != nil {
		return errDB
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	info.State, info.SignIn = CustomerActive, true
	s.audit(cust.Provider, "customer.resume", info.Username, "succeeded", "their plan started again")
	if err := s.notifier.Notify(ctx, cust, CustomerMessage{Kind: messageBack, Text: backText}); err != nil {
		s.log.Warn("could not tell a customer their plan is back", "user", info.UserID, "err", err)
	}
	return nil
}

func pausedText(until time.Time) string {
	return "Your Playkeeper plan has ended, so your servers are stopped. You can still sign in, see them and download backups until " +
		until.UTC().Format("2 January") + ". Renew before then to pick up where you left off."
}

// stopCustomerServers stops each server the customer created, on the
// machine that has it: their home machine, or the one a move of theirs that
// stopped left it on. One that won't stop is logged; starting it stays
// refused while they're paused.
func (s *Server) stopCustomerServers(ctx context.Context, userID int64, actor string) {
	ids, err := s.creatorServers(userID)
	if err != nil || len(ids) == 0 {
		return
	}
	home, _, err := s.homeMachine(ctx, userID)
	if err != nil {
		s.log.Warn("could not find a paused customer's machine to stop their servers", "user", userID, "err", err)
		return
	}
	for _, id := range ids {
		if err := s.stopCustomerServer(ctx, id, home, actor); err != nil {
			s.log.Warn("could not stop a paused customer's server", "server", id, "err", err)
		}
	}
}

// stopCustomerServer stops server id where its record says, or on home
// without one.
func (s *Server) stopCustomerServer(ctx context.Context, id, home, actor string) error {
	at, err := s.recordedMachine(ctx, id)
	if err != nil {
		return err
	}
	m, err := s.machineByID(cmp.Or(at, home))
	if err != nil {
		return err
	}
	status, err := m.agent.Do(asActor(ctx, actor), http.MethodPost, "/v1/servers/"+id+"/stop", nil, map[string]string{"actor": actor}, nil)
	if err == nil && status >= 400 && status != http.StatusNotFound {
		err = fmt.Errorf("the agent answered %d", status)
	}
	return err
}

// heldRefusal is why a may not take act on serverID because the customer
// who made it is paused or suspended, or nil. Those they share it with may
// do on it what they still may (pausedMay), or only look at it while
// they're suspended. Whoever runs this Playkeeper, its owner and the admins
// of every server, may still look after it, though its machine refuses to
// start it (see customerHolds).
func (s *Server) heldRefusal(a access, act action, serverID string) error {
	if serverID == "" || act == actView || a.owner() || a.Servers.All {
		return nil
	}
	var state string
	err := s.db.QueryRow(`SELECT c.state FROM creator_servers v JOIN customers c ON c.user_id = v.user_id WHERE v.server_id = ?`, serverID).Scan(&state)
	switch {
	case isNoRows(err):
		return nil
	case err != nil:
		return errDB
	}
	switch CustomerState(state) {
	case CustomerPaused:
		if !pausedMay[act] {
			return errServerPaused
		}
	case CustomerSuspended:
		return errServerSuspended
	}
	return nil
}

// pausedUntil is when a paused customer's servers are deleted unless they
// renew, or nil for anyone else, and once they're deleted.
func (s *Server) pausedUntil(a access) *time.Time {
	if a.Customer != CustomerPaused {
		return nil
	}
	var ms, deleted int64
	if err := s.db.QueryRow(`SELECT delete_after, servers_deleted_at FROM customers WHERE user_id = ?`, a.UserID).Scan(&ms, &deleted); err != nil || ms == 0 || deleted != 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}

// serversDeleted reports whether a is a paused customer whose servers were
// deleted once the grace period ended (see deletion.go).
func (s *Server) serversDeleted(a access) bool {
	if a.Customer != CustomerPaused {
		return false
	}
	var deleted int64
	return s.db.QueryRow(`SELECT servers_deleted_at FROM customers WHERE user_id = ?`, a.UserID).Scan(&deleted) == nil && deleted != 0
}
