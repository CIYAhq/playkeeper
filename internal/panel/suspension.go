package panel

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

// Suspending a customer or a store (task 3.2 of the hosted blueprint). The
// owner suspends a customer's account from the Team page, or an app store
// with every customer of it, and nothing a billing provider does lifts
// either. A suspended account's servers stop, its API tokens are revoked,
// it's signed out, and it can't sign in or do anything (see permit and
// customerHolds). Its plan goes on underneath: PauseCustomer and
// StartCustomer note its end and its return and leave the suspension be,
// so lifting it leaves the account active, or paused when its plan ended,
// with a fresh grace period since it couldn't sign in meanwhile. A
// suspended store sells nothing: the fleet keeps no room for its plans,
// each plan's stock is 0 on Whop, and its pass starts nobody.

// actSuspendCustomers suspends customers and stores, and lifts it. Only the
// owner may, so actNeeds leaves it out.
const actSuspendCustomers action = "customers.suspend"

// messageUnsuspended is the kind of the message a customer gets once
// nothing suspends their account and their plan is active.
const messageUnsuspended = "unsuspended"

const unsuspendedText = "Your Playkeeper account isn't suspended any more, and your servers can start whenever you like."

// maxSuspendReason bounds the owner's reason for a suspension.
const maxSuspendReason = 200

var (
	errNoSuchStore = &invites.Error{Code: api.CodeNotFound, Status: http.StatusNotFound, Msg: "No such store."}
	// errOwnStore refuses suspending the key store, which is the owner's own.
	errOwnStore = &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict,
		Msg: "Your own store can't be suspended.", Hint: "To stop selling, disconnect it in Settings › Sell on Whop."}
	errSuspendReason = &invites.Error{Code: api.CodeInvalid, Status: http.StatusBadRequest,
		Msg: "Say why, in a few words.", Hint: fmt.Sprintf("At most %d characters.", maxSuspendReason)}
)

// suspensionColumn is the column that says the owner suspended an account
// with its store, or on its own.
func suspensionColumn(withStore bool) string {
	if withStore {
		return "suspended_store"
	}
	return "suspended_self"
}

// suspendCustomer suspends the customer's account userID, on its own or
// with its store, for reason, and reports whether that changed anything. An
// account that becomes suspended has its servers stopped and its API tokens
// revoked, and is signed out everywhere; one suspended already stays so,
// for this too.
func (s *Server) suspendCustomer(ctx context.Context, userID int64, withStore bool, actor, reason string) (bool, error) {
	s.customersMu.Lock()
	defer s.customersMu.Unlock()
	var state, username string
	var self, store bool
	err := s.db.QueryRowContext(ctx, `SELECT c.state, c.suspended_self, c.suspended_store, u.username FROM customers c JOIN users u ON u.id = c.user_id WHERE c.user_id = ?`, userID).
		Scan(&state, &self, &store, &username)
	switch {
	case isNoRows(err):
		return false, errNotACustomer
	case err != nil:
		return false, errDB
	case withStore && store || !withStore && self:
		return false, nil
	}
	col, now := suspensionColumn(withStore), s.now().UnixMilli()
	if CustomerState(state) == CustomerSuspended {
		if _, err := s.db.ExecContext(ctx, `UPDATE customers SET `+col+` = 1, updated_at = ? WHERE user_id = ?`, now, userID); err != nil {
			return false, errDB
		}
		s.audit(actor, "customer.suspend", username, "succeeded", reason+"; it was suspended already")
		return true, nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE customers SET state = ?, suspended_at = ?, suspend_reason = ?, `+col+` = 1, updated_at = ? WHERE user_id = ?`,
		string(CustomerSuspended), now, reason, now, userID); err != nil {
		return false, errDB
	}
	s.audit(actor, "customer.suspend", username, "succeeded", reason)
	s.kickDiskLimits()
	s.revokeAccountTokens(userID, actor, "their account was suspended")
	s.deleteUserSessions(userID)
	s.stopCustomerServers(ctx, userID, actor)
	return true, nil
}

// liftSuspension lifts the owner's suspension of the account userID on its
// own, or with its store, and reports whether that changed anything. Once
// nothing suspends it, the account is active again, with its servers
// stopped until started, or paused with a fresh grace period when its plan
// ended meanwhile, and the customer is told which.
func (s *Server) liftSuspension(ctx context.Context, userID int64, withStore bool, actor string) (bool, error) {
	s.customersMu.Lock()
	defer s.customersMu.Unlock()
	var cust Customer
	var state, username string
	var self, store bool
	var pausedAt, deletedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT c.provider, c.store, c.subject, c.handle, u.username, c.state, c.suspended_self, c.suspended_store, c.paused_at, c.servers_deleted_at
		FROM customers c JOIN users u ON u.id = c.user_id WHERE c.user_id = ?`, userID).
		Scan(&cust.Provider, &cust.Store, &cust.Subject, &cust.Handle, &username, &state, &self, &store, &pausedAt, &deletedAt)
	switch {
	case isNoRows(err):
		return false, errNotACustomer
	case err != nil:
		return false, errDB
	case withStore && !store || !withStore && !self:
		return false, nil
	}
	col, now := suspensionColumn(withStore), s.now()
	if self && store || CustomerState(state) != CustomerSuspended {
		if _, err := s.db.ExecContext(ctx, `UPDATE customers SET `+col+` = 0, updated_at = ? WHERE user_id = ?`, now.UnixMilli(), userID); err != nil {
			return false, errDB
		}
		s.audit(actor, "customer.unsuspend", username, "succeeded", "it stays suspended for the other reason")
		return true, nil
	}
	next, until := CustomerActive, int64(0)
	msg := CustomerMessage{Kind: messageUnsuspended, Text: unsuspendedText}
	if pausedAt != 0 {
		next, msg = CustomerPaused, CustomerMessage{}
		if deletedAt == 0 {
			t := now.Add(graceDays * 24 * time.Hour)
			until, msg = t.UnixMilli(), CustomerMessage{Kind: messagePaused, Text: pausedText(t)}
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE customers SET state = ?, suspended_at = 0, suspend_reason = '', suspended_self = 0, suspended_store = 0,
		delete_after = CASE WHEN ? > 0 THEN ? ELSE delete_after END, updated_at = ? WHERE user_id = ?`,
		string(next), until, until, now.UnixMilli(), userID); err != nil {
		return false, errDB
	}
	s.audit(actor, "customer.unsuspend", username, "succeeded", "it's "+string(next)+" now")
	s.kickDiskLimits()
	if msg.Kind != "" {
		if err := s.notifier.Notify(ctx, cust, msg); err != nil {
			s.log.Warn("could not tell a customer their suspension was lifted", "user", userID, "err", err)
		}
	}
	return true, nil
}

// pauseUnderSuspension notes that a suspended customer's plan ended, so
// lifting the suspension leaves them paused. The caller holds
// s.customersMu.
func (s *Server) pauseUnderSuspension(ctx context.Context, cust Customer, info CustomerAccountInfo, reason string) error {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `UPDATE customers SET paused_at = ?, delete_after = ?, pause_reason = ?, updated_at = ? WHERE user_id = ? AND state = ? AND paused_at = 0`,
		now.UnixMilli(), now.Add(graceDays*24*time.Hour).UnixMilli(), reason, now.UnixMilli(), info.UserID, string(CustomerSuspended))
	if err != nil {
		return errDB
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.audit(cust.Provider, "customer.pause", info.Username, "succeeded", reason+"; their account stays suspended")
	}
	return nil
}

// resumeUnderSuspension notes that a suspended customer's plan started
// again, so lifting the suspension leaves them active. The caller holds
// s.customersMu.
func (s *Server) resumeUnderSuspension(ctx context.Context, cust Customer, info CustomerAccountInfo) error {
	res, err := s.db.ExecContext(ctx, `UPDATE customers SET paused_at = 0, delete_after = 0, pause_reason = '', servers_deleted_at = 0, updated_at = ?
		WHERE user_id = ? AND state = ? AND paused_at != 0`, s.now().UnixMilli(), info.UserID, string(CustomerSuspended))
	if err != nil {
		return errDB
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.audit(cust.Provider, "customer.resume", info.Username, "succeeded", "their plan started again; their account stays suspended")
	}
	return nil
}

// storeCustomers lists the accounts of the provider's customers of store.
func (s *Server) storeCustomers(ctx context.Context, provider, store string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM customers WHERE provider = ? AND store = ? ORDER BY user_id`, provider, store)
	if err != nil {
		return nil, errDB
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, errDB
		}
		out = append(out, id)
	}
	if rows.Err() != nil {
		return nil, errDB
	}
	return out, nil
}

// suspendWhopStore suspends the app store storeID for reason, with every
// customer of it, and reports whether that changed anything. From its next
// pass it sells nothing (see reconcileWhopStore). Asked again, it suspends
// any customer of the store it missed.
func (s *Server) suspendWhopStore(ctx context.Context, storeID, actor, reason string) (bool, error) {
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	st, ok, err := s.whopStoreByID(ctx, storeID)
	switch {
	case err != nil:
		return false, errDB
	case !ok:
		return false, errNoSuchStore
	case st.Via != whopViaApp:
		return false, errOwnStore
	}
	res, err := s.db.ExecContext(ctx, `UPDATE whop_stores SET suspended_at = ?, suspend_reason = ? WHERE store_id = ? AND suspended_at = 0`, s.now().UnixMilli(), reason, storeID)
	if err != nil {
		return false, errDB
	}
	n, _ := res.RowsAffected()
	changed := n > 0
	ids, err := s.storeCustomers(ctx, whopProvider, storeID)
	if err != nil {
		return changed, err
	}
	with := 0
	for _, id := range ids {
		did, err := s.suspendCustomer(ctx, id, true, actor, reason)
		if err != nil {
			return changed, err
		}
		if did {
			with++
		}
	}
	if changed || with > 0 {
		s.audit(actor, "whop.store_suspend", storeID, "succeeded", fmt.Sprintf("%s: %s; %d customer(s) suspended with it", whopName(st.Account), reason, with))
		s.kickSaleRoom()
		s.kickWhopStore(storeID)
	}
	return changed || with > 0, nil
}

// liftWhopStore lifts the suspension of the store storeID, and of the
// customers suspended with it but not those the owner suspended on their
// own, and reports whether that changed anything. Its next pass reads the
// store and every membership again, then sells as the room allows.
func (s *Server) liftWhopStore(ctx context.Context, storeID, actor string) (bool, error) {
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	st, ok, err := s.whopStoreByID(ctx, storeID)
	switch {
	case err != nil:
		return false, errDB
	case !ok:
		return false, errNoSuchStore
	}
	res, err := s.db.ExecContext(ctx, `UPDATE whop_stores SET suspended_at = 0, suspend_reason = '', synced_at = 0, polled_at = 0 WHERE store_id = ? AND suspended_at != 0`, storeID)
	if err != nil {
		return false, errDB
	}
	n, _ := res.RowsAffected()
	changed := n > 0
	ids, err := s.storeCustomers(ctx, whopProvider, storeID)
	if err != nil {
		return changed, err
	}
	with := 0
	for _, id := range ids {
		did, err := s.liftSuspension(ctx, id, true, actor)
		if err != nil {
			return changed, err
		}
		if did {
			with++
		}
	}
	if changed || with > 0 {
		s.audit(actor, "whop.store_unsuspend", storeID, "succeeded", fmt.Sprintf("%s; %d customer(s) lifted with it", whopName(st.Account), with))
		s.kickSaleRoom()
		s.kickWhopStore(storeID)
	}
	return changed || with > 0, nil
}

// stopWhopSales has each plan of the store sell none: its stock is 0 from
// the next write. The fleet has no number for a suspended store's plans
// (see SalePlans), and one it had is put back to 0 before any write.
func (s *Server) stopWhopSales(ctx context.Context, storeID string) {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO whop_stock(store_id, plan_id, want, set_at)
		SELECT store_id, plan_id, 0, ? FROM whop_plans WHERE store_id = ? AND allowance_from != '' AND visibility != 'archived'
		ON CONFLICT(plan_id) DO UPDATE SET want = 0, set_at = excluded.set_at WHERE whop_stock.store_id = excluded.store_id AND whop_stock.want != 0`,
		s.now().UnixMilli(), storeID); err != nil {
		s.log.Error("could not stop a suspended store's sales", "store", storeID, "err", err)
	}
}

// suspendBody is the owner's reason for a suspension.
type suspendBody struct {
	Reason string `json:"reason"`
}

// suspendReason reads the owner's reason for a suspension.
func suspendReason(r *http.Request) (string, error) {
	var req suspendBody
	if err := decodeJSON(r, &req); err != nil {
		return "", errSuspendReason
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || len(reason) > maxSuspendReason || !printable(reason) {
		return "", errSuspendReason
	}
	return reason, nil
}

// customerSuspension is where a customer's account stands after the owner
// suspended it or lifted that.
type customerSuspension struct {
	State          CustomerState `json:"state"`
	SuspendedSelf  bool          `json:"suspendedSelf"`
	SuspendedStore bool          `json:"suspendedStore"`
}

// hCustomerSuspend suspends a customer's account on its own.
func (s *Server) hCustomerSuspend(w http.ResponseWriter, r *http.Request, sess *session) {
	uid, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil {
		writeRefusal(w, errNotACustomer)
		return
	}
	reason, err := suspendReason(r)
	if err != nil {
		writeRefusal(w, err)
		return
	}
	if _, err := s.suspendCustomer(r.Context(), uid, false, sess.User.Username, reason); err != nil {
		writeRefusal(w, err)
		return
	}
	s.answerSuspension(w, r, uid)
}

// hCustomerUnsuspend lifts the owner's own suspension of a customer's
// account, which stays suspended while its store is.
func (s *Server) hCustomerUnsuspend(w http.ResponseWriter, r *http.Request, sess *session) {
	uid, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil {
		writeRefusal(w, errNotACustomer)
		return
	}
	if _, err := s.liftSuspension(r.Context(), uid, false, sess.User.Username); err != nil {
		writeRefusal(w, err)
		return
	}
	s.answerSuspension(w, r, uid)
}

func (s *Server) answerSuspension(w http.ResponseWriter, r *http.Request, uid int64) {
	var out customerSuspension
	if err := s.db.QueryRowContext(r.Context(), `SELECT state, suspended_self, suspended_store FROM customers WHERE user_id = ?`, uid).
		Scan(&out.State, &out.SuspendedSelf, &out.SuspendedStore); err != nil {
		writeRefusal(w, errNotACustomer)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// storeView is an app store as the owner's list of them shows it, with how
// many customers have an account from it.
type storeView struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Route         string     `json:"route,omitempty"`
	Customers     int        `json:"customers"`
	Problem       string     `json:"problem,omitempty"`
	SuspendedAt   *time.Time `json:"suspendedAt,omitempty"`
	SuspendReason string     `json:"suspendReason,omitempty"`
}

// storesBody is the owner's list of app stores.
type storesBody struct {
	Stores []storeView `json:"stores"`
}

// hWhopStores lists the app stores.
func (s *Server) hWhopStores(w http.ResponseWriter, r *http.Request, _ *session) {
	s.answerStores(w, r)
}

// hWhopStoreSuspend suspends an app store with every customer of it.
func (s *Server) hWhopStoreSuspend(w http.ResponseWriter, r *http.Request, sess *session) {
	reason, err := suspendReason(r)
	if err != nil {
		writeRefusal(w, err)
		return
	}
	if _, err := s.suspendWhopStore(r.Context(), r.PathValue("store"), sess.User.Username, reason); err != nil {
		writeRefusal(w, err)
		return
	}
	s.answerStores(w, r)
}

// hWhopStoreUnsuspend lifts an app store's suspension, and its customers'.
func (s *Server) hWhopStoreUnsuspend(w http.ResponseWriter, r *http.Request, sess *session) {
	if _, err := s.liftWhopStore(r.Context(), r.PathValue("store"), sess.User.Username); err != nil {
		writeRefusal(w, err)
		return
	}
	s.answerStores(w, r)
}

func (s *Server) answerStores(w http.ResponseWriter, r *http.Request) {
	stores, err := s.whopStores(r.Context())
	if err != nil {
		writeRefusal(w, errDB)
		return
	}
	counts := map[string]int{}
	rows, err := s.db.QueryContext(r.Context(), `SELECT store, COUNT(*) FROM customers WHERE provider = ? GROUP BY store`, whopProvider)
	if err != nil {
		writeRefusal(w, errDB)
		return
	}
	for rows.Next() {
		var store string
		var n int
		if rows.Scan(&store, &n) == nil {
			counts[store] = n
		}
	}
	rows.Close()
	out := storesBody{Stores: []storeView{}}
	for _, st := range stores {
		if st.Via != whopViaApp {
			continue
		}
		v := storeView{ID: st.ID, Title: cmpOr(st.Title, st.ID), Route: st.Route, Customers: counts[st.ID], Problem: st.Problem, SuspendReason: st.SuspendReason}
		if !st.SuspendedAt.IsZero() {
			v.SuspendedAt = &st.SuspendedAt
		}
		out.Stores = append(out.Stores, v)
	}
	writeJSON(w, http.StatusOK, out)
}
