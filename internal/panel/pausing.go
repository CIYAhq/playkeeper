package panel

import (
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
	res, err := s.db.ExecContext(ctx, `UPDATE customers SET state = ?, paused_at = 0, delete_after = 0, pause_reason = '', updated_at = ? WHERE user_id = ? AND state = ?`,
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

// stopCustomerServers stops each server the customer created, on their home
// machine. One that won't stop is logged; starting it stays refused while
// they're paused.
func (s *Server) stopCustomerServers(ctx context.Context, userID int64, actor string) {
	ids, err := s.creatorServers(userID)
	if err != nil || len(ids) == 0 {
		return
	}
	home, ok, err := s.homeMachine(ctx, userID)
	if err != nil || !ok {
		s.log.Warn("could not find a paused customer's machine to stop their servers", "user", userID, "err", err)
		return
	}
	m, err := s.machineByID(home)
	if err != nil {
		s.log.Warn("could not find a paused customer's machine to stop their servers", "user", userID, "err", err)
		return
	}
	for _, id := range ids {
		status, err := m.agent.Do(asActor(ctx, actor), http.MethodPost, "/v1/servers/"+id+"/stop", nil, map[string]string{"actor": actor}, nil)
		if err == nil && status >= 400 && status != http.StatusNotFound {
			err = fmt.Errorf("the agent answered %d", status)
		}
		if err != nil {
			s.log.Warn("could not stop a paused customer's server", "server", id, "err", err)
		}
	}
}

// pausedUntil is when a paused customer's servers are deleted unless they
// renew, or nil for anyone else.
func (s *Server) pausedUntil(a access) *time.Time {
	if a.Customer != CustomerPaused {
		return nil
	}
	var ms int64
	if err := s.db.QueryRow(`SELECT delete_after FROM customers WHERE user_id = ?`, a.UserID).Scan(&ms); err != nil || ms == 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}
