package panel

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// Deleting a Playkeeper Cloud customer (the privacy policy's deletion): on
// the owner's request from the Team page, or some days after the
// customer's servers were deleted, as the owner sets. Their account and
// personal records go, at their store alone: the same Whop user at another
// store is another customer. Their payments keep what the store's accounts
// need, without who paid. It runs in the background, as a lapsed
// customer's deletion does (see deletion.go), since deleting servers takes
// a while.

const (
	// actDeleteCustomers deletes a customer, and sets how many days after
	// their servers were deleted a customer is. Only the owner may.
	actDeleteCustomers action = "customers.delete"
	// metaCustomerRetention is the panel_meta key holding those days.
	metaCustomerRetention = "customer_retention_days"
	// defaultCustomerRetention is the days until the owner sets others. They
	// may set from minCustomerRetention, as long as a deleted server's final
	// backup is kept, since its customer is told they can download it until
	// then, to maxCustomerRetention.
	defaultCustomerRetention = 30
	minCustomerRetention     = finalBackupDays
	maxCustomerRetention     = 3650
)

var (
	// errCustomerHasPlan refuses deleting a customer who still has a plan
	// at their store, whom the store's next read would bring back.
	errCustomerHasPlan = errors.New("they still have a plan at their store on Whop, and the store's next read would bring them back. Cancel it on Whop first, then delete them")
	// errErasureWaits leaves a deletion for a later look while the
	// customer's servers are being moved, or a copy a move left of one is
	// still on its old machine.
	errErasureWaits = errors.New("the customer's servers are being moved")
)

// erasable is a customer as their deletion needs them.
type erasable struct {
	userID                   int64
	username                 string
	provider, store, subject string
	state                    CustomerState
	requestedAt              int64
	requestedBy              string
}

// erasableCustomer reads the customer userID, and false for an account
// that isn't one.
func (s *Server) erasableCustomer(ctx context.Context, userID int64) (erasable, bool, error) {
	c := erasable{userID: userID}
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT u.username, c.provider, c.store, c.subject, c.state, c.erase_requested_at, c.erase_actor
		FROM customers c JOIN users u ON u.id = c.user_id WHERE c.user_id = ?`, userID).
		Scan(&c.username, &c.provider, &c.store, &c.subject, &state, &c.requestedAt, &c.requestedBy)
	switch {
	case isNoRows(err):
		return c, false, nil
	case err != nil:
		return c, false, errDB
	}
	c.state = CustomerState(state)
	return c, true, nil
}

// hasPlan says whether the customer still has a plan at their store: they
// run as active, or a membership of theirs there gives access, even one
// Whop's API hasn't confirmed yet, which may be a renewal.
func (s *Server) hasPlan(ctx context.Context, q querier, c erasable) (bool, error) {
	if c.state == CustomerActive {
		return true, nil
	}
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM whop_memberships WHERE store_id = ? AND whop_user_id = ? AND status IN `+whopAccess,
		c.store, c.subject).Scan(&n)
	if err != nil {
		return false, errDB
	}
	return n > 0, nil
}

// erasedSubject is how the store remembers a customer it deleted: a hash of
// who they were there, which says nothing without the Whop user's id.
func erasedSubject(store, subject string) string {
	sum := sha256.Sum256([]byte(store + "\x00" + subject))
	return hex.EncodeToString(sum[:])
}

// forgotten says whether the Whop user was a customer of the store whom the
// dashboard deleted, and isn't one again, so the store's reads leave out a
// membership of theirs that no longer gives access.
func (s *Server) forgotten(storeID, userID string) bool {
	var gone bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM erased_customers WHERE store_id = ? AND subject_hash = ?)
		AND NOT EXISTS(SELECT 1 FROM customers WHERE provider = ? AND store = ? AND subject = ?)`,
		storeID, erasedSubject(storeID, userID), whopProvider, storeID, userID).Scan(&gone)
	return err == nil && gone
}

// customerRetention is the days after a customer's servers were deleted
// before the customer is: the owner's setting, or defaultCustomerRetention.
func (s *Server) customerRetention(ctx context.Context) int {
	var v string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM panel_meta WHERE key = ?`, metaCustomerRetention).Scan(&v); err != nil {
		return defaultCustomerRetention
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < minCustomerRetention || n > maxCustomerRetention {
		return defaultCustomerRetention
	}
	return n
}

// retentionView is the owner's setting: the days after a customer's
// servers were deleted before the customer is, their default, the fewest
// and the most.
type retentionView struct {
	Days    int `json:"days"`
	Default int `json:"default"`
	Min     int `json:"min"`
	Max     int `json:"max"`
}

func (s *Server) retentionOf(ctx context.Context) retentionView {
	return retentionView{Days: s.customerRetention(ctx), Default: defaultCustomerRetention, Min: minCustomerRetention, Max: maxCustomerRetention}
}

// hCustomerRetention answers the owner's setting.
func (s *Server) hCustomerRetention(w http.ResponseWriter, r *http.Request, _ *session) {
	writeJSON(w, http.StatusOK, s.retentionOf(r.Context()))
}

// hSetCustomerRetention sets how many days after a customer's servers were
// deleted the customer is.
func (s *Server) hSetCustomerRetention(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		Days int `json:"days"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Days < minCustomerRetention || req.Days > maxCustomerRetention {
		msg := fmt.Sprintf("Choose from %d to %d days.", minCustomerRetention, maxCustomerRetention)
		writeJSON(w, http.StatusBadRequest, api.Error{Error: msg, Code: api.CodeInvalid, Field: "days"})
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `INSERT INTO panel_meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		metaCustomerRetention, strconv.Itoa(req.Days)); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	s.audit(sess.User.Username, "customer.retention", "customers", "succeeded", fmt.Sprintf("a customer is deleted %d days after their servers", req.Days))
	s.kickErasures()
	writeJSON(w, http.StatusOK, s.retentionOf(r.Context()))
}

// hCustomerDelete is the owner asking for a customer's account and personal
// records to be deleted, with the account's name typed to confirm. A
// customer with a plan at their store isn't. The customer is signed out at
// once, and the deletion runs in the background.
func (s *Server) hCustomerDelete(w http.ResponseWriter, r *http.Request, sess *session) {
	uid, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "No such customer.", "")
		return
	}
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Type the account's name to confirm.", "")
		return
	}
	ctx := r.Context()
	c, ok, err := s.erasableCustomer(ctx, uid)
	switch {
	case err != nil:
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	case !ok:
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "No such customer.", "")
		return
	case req.Confirm != c.username:
		writeJSON(w, http.StatusBadRequest, api.Error{Error: "Type " + c.username + " to confirm.", Code: api.CodeInvalid, Field: "confirm"})
		return
	}
	plan, err := s.hasPlan(ctx, s.db, c)
	switch {
	case err != nil:
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	case plan:
		writeErr(w, http.StatusConflict, api.CodeConflict, c.username+": "+errCustomerHasPlan.Error()+".", "")
		return
	}
	actor := sess.User.Username
	res, err := s.db.ExecContext(ctx, `UPDATE customers SET erase_requested_at = ?, erase_actor = ? WHERE user_id = ? AND erase_requested_at = 0`,
		s.now().UnixMilli(), actor, uid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.deleteUserSessions(uid)
		s.revokeAccountTokens(uid, actor, "their account is being deleted")
		s.audit(actor, "customer.erase", c.username, "requested", "their account and personal records at "+c.store+", on request")
	}
	s.kickErasures()
	writeJSON(w, http.StatusAccepted, map[string]bool{"deleting": true})
}

// kickErasures has the deletion loop look for customers to delete now.
func (s *Server) kickErasures() {
	select {
	case s.eraseKick <- struct{}{}:
	default:
	}
}

// eraseDueCustomers deletes each customer the owner asked to delete, and
// each whose servers were deleted longer ago than the owner's setting. One
// that can't be finished is tried again at the next look.
func (s *Server) eraseDueCustomers(ctx context.Context) {
	s.erasingMu.Lock()
	defer s.erasingMu.Unlock()
	days := s.customerRetention(ctx)
	cutoff := s.now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM customers WHERE erase_requested_at > 0
		OR (state = ? AND servers_deleted_at > 0 AND servers_deleted_at <= ?) ORDER BY user_id`, string(CustomerPaused), cutoff)
	if err != nil {
		s.log.Error("could not list the customers to delete", "err", err)
		return
	}
	var due []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			due = append(due, id)
		}
	}
	rows.Close()
	for _, id := range due {
		err := s.eraseCustomer(ctx, id, days)
		switch {
		case errors.Is(err, errCustomerHasPlan), errors.Is(err, errErasureWaits):
		case err != nil:
			s.log.Warn("could not delete a customer; trying again later", "user", id, "err", err)
		}
	}
}

// eraseCustomer deletes the customer userID, asked for or due, and returns
// errCustomerHasPlan for one who has a plan at their store again:
//   - each of their servers, on its machine, with every backup and no final
//     one kept;
//   - the backups machines keep for them, of their deleted servers and of
//     copies their moves left;
//   - their records at their store: their memberships, their Whop customer
//     row and the messages sent to them there;
//   - their invites, and their servers' join requests, players' origins and
//     shared links;
//   - their account, with its sign-ins, tokens, two-factor, preferences,
//     home machine and moves.
//
// Their payments keep what the store's accounts need, without who paid, and
// a hash of who they were keeps the store's reads from bringing back a
// membership of theirs that ended. Each deletion starts only after another
// look finds they still have no plan (whileNoPlan), so one who buys again
// meanwhile keeps whatever wasn't deleted yet, and their account.
func (s *Server) eraseCustomer(ctx context.Context, userID int64, days int) error {
	c, ok, err := s.erasableCustomer(ctx, userID)
	if err != nil || !ok {
		return err
	}
	actor, why := placementActor, fmt.Sprintf("%d days after their servers were deleted", days)
	if c.requestedAt > 0 {
		actor, why = cmpOr(c.requestedBy, placementActor), "on request"
	}
	if s.customerMoving(ctx, userID) || s.leftCopiesPending(ctx, userID) {
		return errErasureWaits
	}
	erased, err := s.eraseServers(ctx, userID)
	kept := 0
	if err == nil {
		kept, err = s.eraseKeptBackups(ctx, userID)
	}
	var payments int64
	if err == nil {
		payments, err = s.eraseCustomerRecords(ctx, c)
	}
	if err != nil {
		return s.stopErasure(ctx, c, actor, erased, err)
	}
	s.kickDiskLimits()
	s.kickSaleRoom()
	s.audit(actor, "customer.erase", c.username, "succeeded", fmt.Sprintf("their account and personal records at %s, %s: %d server(s) with their backups, %d kept backup(s); %d payment(s) kept for the store's accounts, without who paid",
		c.store, why, erased, kept, payments))
	return nil
}

// stopErasure returns err, why a customer's deletion stopped once erased of
// their servers were deleted. One who has a plan at their store again has a
// request for their deletion cleared, and the audit log says so. Otherwise
// it says when servers went before the deletion stopped, since it's only
// tried again later.
func (s *Server) stopErasure(ctx context.Context, c erasable, actor string, erased int, err error) error {
	after := ""
	if erased > 0 {
		after = fmt.Sprintf(", after %d server(s) with their backups were deleted", erased)
	}
	switch {
	case errors.Is(err, errCustomerHasPlan) && c.requestedAt > 0:
		if _, dbErr := s.db.ExecContext(ctx, `UPDATE customers SET erase_requested_at = 0, erase_actor = '' WHERE user_id = ?`, c.userID); dbErr != nil {
			return errDB
		}
		s.audit(actor, "customer.erase", c.username, "refused", "they have a plan at their store again"+after)
	case erased > 0:
		s.audit(actor, "customer.erase", c.username, "failed", fmt.Sprintf("it stopped%s, and goes on later: %v", after, err))
	}
	return err
}

// whileNoPlan runs start, the start of deleting something of the customer
// userID's, only while they still have no plan at their store, with the
// store's pass and the hosting core waiting, so a renewal can't slip in
// between the look and the start. One with a plan gets errCustomerHasPlan.
func (s *Server) whileNoPlan(ctx context.Context, userID int64, start func() error) error {
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	s.customersMu.Lock()
	defer s.customersMu.Unlock()
	c, ok, err := s.erasableCustomer(ctx, userID)
	switch {
	case err != nil:
		return err
	case !ok:
		return fmt.Errorf("account %d isn't a customer any more", userID)
	}
	plan, err := s.hasPlan(ctx, s.db, c)
	if err != nil {
		return err
	}
	if plan {
		return errCustomerHasPlan
	}
	return start()
}

// leftCopiesPending says whether a copy a move of the customer's left is
// still on its old machine, to be deleted there.
func (s *Server) leftCopiesPending(ctx context.Context, userID int64) bool {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM left_copies WHERE user_id = ? AND left_at = 0`, userID).Scan(&n)
	return err != nil || n > 0
}

// eraseServers deletes each of the customer's servers on the machine that
// has it, with every backup and no final one kept, forgetting each with its
// records once it's gone, and returns how many were deleted on a machine.
// Those out of reach are forgotten, as a lapsed customer's are.
func (s *Server) eraseServers(ctx context.Context, userID int64) (int, error) {
	home, _, err := s.homeMachine(ctx, userID)
	if err != nil {
		return 0, err
	}
	m, here, gone, err := s.lapsedServers(ctx, userID, home)
	if err != nil {
		return 0, err
	}
	erased := 0
	for _, id := range here {
		opID, err := s.startErase(ctx, m, userID, id)
		if err != nil {
			return erased, err
		}
		if opID != "" {
			op, err := waitAgentOp(ctx, m, opID, deleteWait)
			if err != nil {
				return erased, err
			}
			if op.Status != api.OpSucceeded {
				return erased, fmt.Errorf("deleting %s failed: %s", id, op.Error)
			}
			erased++
		}
		if err := s.forgetErasedServer(ctx, id); err != nil {
			return erased, err
		}
	}
	for _, lost := range gone {
		if err := s.forgetErasedServer(ctx, lost); err != nil {
			return erased, err
		}
	}
	return erased, nil
}

// startErase asks m to delete the customer's server id with all its
// backups, keeping no final one, while they still have no plan
// (whileNoPlan), and returns the deletion's operation: "" when m has no
// such server any more.
func (s *Server) startErase(ctx context.Context, m machine, userID int64, id string) (string, error) {
	var opID string
	err := s.whileNoPlan(ctx, userID, func() error {
		actx := asActor(ctx, placementActor)
		var st api.ServerStatus
		status, err := m.agent.Do(actx, http.MethodGet, "/v1/servers/"+id, nil, nil, &st)
		switch {
		case err != nil:
			return err
		case status == http.StatusNotFound:
			return nil
		case status != http.StatusOK:
			return fmt.Errorf("the agent answered %d about %s", status, id)
		}
		req := api.DeleteServerRequest{Confirm: st.Name, Actor: placementActor, ForgetKey: true}
		var op api.Operation
		status, err = m.agent.Do(actx, http.MethodPost, "/v1/servers/"+id+"/delete", nil, req, &op)
		switch {
		case err != nil:
			return err
		case status != http.StatusAccepted || op.ID == "":
			return fmt.Errorf("the agent answered %d to deleting %s", status, id)
		}
		opID = op.ID
		return nil
	})
	return opID, err
}

// forgetErasedServer forgets a server of a customer being deleted once it's
// gone: its join requests, players' origins and shared links, then who
// created it, last, so a deletion that stops midway still knows the server
// when it's tried again.
func (s *Server) forgetErasedServer(ctx context.Context, id string) error {
	for _, q := range []string{`DELETE FROM join_requests WHERE server_id = ?`, `DELETE FROM player_origins WHERE server_id = ?`,
		`DELETE FROM public_links WHERE server_id = ?`, `DELETE FROM creator_servers WHERE server_id = ?`} {
		if _, err := s.db.ExecContext(ctx, q, id); err != nil {
			return errDB
		}
	}
	return nil
}

// eraseKeptBackups deletes the backups each machine keeps for the account,
// of its deleted servers and of copies its moves left, each while the
// customer still has no plan (whileNoPlan), and returns how many. A machine
// that can't be asked leaves the deletion for a later look.
func (s *Server) eraseKeptBackups(ctx context.Context, userID int64) (int, error) {
	ms, err := s.machines()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range ms {
		for _, label := range []string{keptFor(userID), movedKeptFor(userID)} {
			var list []api.KeptBackup
			status, err := m.agent.Do(ctx, http.MethodGet, "/v1/kept-backups", url.Values{"keptFor": {label}}, nil, &list)
			if err == nil && status != http.StatusOK {
				err = fmt.Errorf("the agent answered %d to listing kept backups", status)
			}
			if err != nil {
				return n, fmt.Errorf("the backups %s keeps for the customer: %w", machineLabel(m), err)
			}
			for _, k := range list {
				if k.KeptFor != label {
					continue
				}
				del := func() error {
					status, err := m.agent.Do(ctx, http.MethodDelete, "/v1/kept-backups/"+url.PathEscape(k.ID), nil, nil, nil)
					if err == nil && status != http.StatusNoContent && status != http.StatusOK && status != http.StatusNotFound {
						err = fmt.Errorf("the agent answered %d to deleting a kept backup", status)
					}
					return err
				}
				if err := s.whileNoPlan(ctx, userID, del); err != nil {
					return n, fmt.Errorf("a backup %s keeps for the customer: %w", machineLabel(m), err)
				}
				n++
			}
		}
	}
	return n, nil
}

// eraseCustomerRecords deletes the customer's records and account, in one
// transaction while the store's pass and the hosting core wait, and returns
// how many of their payments were kept without who paid. A customer who
// bought again meanwhile keeps everything.
func (s *Server) eraseCustomerRecords(ctx context.Context, c erasable) (int64, error) {
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	s.customersMu.Lock()
	defer s.customersMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, errDB
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM customers WHERE user_id = ?`, c.userID).Scan(&state); err != nil {
		return 0, errDB
	}
	c.state = CustomerState(state)
	if plan, err := s.hasPlan(ctx, tx, c); err != nil || plan {
		return 0, cmp.Or(err, errCustomerHasPlan)
	}
	exec := func(q string, args ...any) error {
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return errDB
		}
		return nil
	}
	var payments int64
	if c.provider == whopProvider {
		for _, q := range []string{
			`DELETE FROM whop_memberships WHERE store_id = ? AND whop_user_id = ?`,
			`DELETE FROM whop_customers WHERE store_id = ? AND whop_user_id = ?`,
			`DELETE FROM whop_messages WHERE store_id = ? AND whop_user_id = ?`,
		} {
			if err := exec(q, c.store, c.subject); err != nil {
				return 0, err
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE whop_payments SET whop_user_id = '' WHERE store_id = ? AND whop_user_id = ?`, c.store, c.subject)
		if err != nil {
			return 0, errDB
		}
		payments, _ = res.RowsAffected()
		if err := exec(`INSERT INTO erased_customers(store_id, subject_hash, erased_at) VALUES(?,?,?)
			ON CONFLICT(store_id, subject_hash) DO UPDATE SET erased_at = excluded.erased_at`, c.store, erasedSubject(c.store, c.subject), s.now().UnixMilli()); err != nil {
			return 0, err
		}
	}
	for _, q := range []string{
		`DELETE FROM invites WHERE created_by = ?`, `DELETE FROM server_moves WHERE user_id = ?`, `DELETE FROM left_copies WHERE user_id = ?`,
		`DELETE FROM move_restarts WHERE user_id = ?`, `DELETE FROM users WHERE id = ?`,
	} {
		if err := exec(q, c.userID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, errDB
	}
	return payments, nil
}
