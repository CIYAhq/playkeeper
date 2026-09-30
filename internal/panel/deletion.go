package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// Deleting a paused customer's servers once the grace period ends (step 6 of
// the managed-beta plan, second half). Each server is deleted on its machine
// with a final backup kept finalBackupDays, which the customer downloads
// from Home. Their home machine is freed, so the memory their plan set aside
// there goes back, and renewing places them again and tells them their
// server is ready, as for a new customer.

const (
	// finalBackupDays is how long a deleted server's final backup is kept.
	finalBackupDays = 30
	// lapsedEvery is how often the core looks for paused customers whose
	// grace period has ended.
	lapsedEvery = 10 * time.Minute
	// deleteWait is how long one server's deletion, its final backup
	// included, may take before the next look tries again.
	deleteWait = 30 * time.Minute
	// opPoll is how often a deletion under way is asked how it's going.
	opPoll = 2 * time.Second
)

// messageDeleted is the kind of the message a customer gets once their
// servers are deleted.
const messageDeleted = "deleted"

// errNotLapsed stops a deletion whose customer renewed, or was suspended,
// while it was under way.
var errNotLapsed = errors.New("the customer isn't paused past their grace period any more")

// runLapsedCustomers deletes lapsed customers' servers every lapsedEvery,
// until ctx ends.
func (s *Server) runLapsedCustomers(ctx context.Context) {
	t := time.NewTicker(lapsedEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.deleteLapsedCustomers(ctx)
		}
	}
}

// deleteLapsedCustomers deletes the servers of each paused customer whose
// grace period has ended. One that can't be finished is tried again at the
// next look.
func (s *Server) deleteLapsedCustomers(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM customers WHERE state = ? AND delete_after > 0 AND delete_after <= ? AND servers_deleted_at = 0
		ORDER BY delete_after`, string(CustomerPaused), s.now().UnixMilli())
	if err != nil {
		s.log.Error("could not list the customers whose grace period ended", "err", err)
		return
	}
	var lapsed []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			lapsed = append(lapsed, id)
		}
	}
	rows.Close()
	for _, id := range lapsed {
		if err := s.deleteLapsedCustomer(ctx, id); err != nil {
			s.log.Warn("could not delete the servers of a customer whose grace period ended; trying again later", "user", id, "err", err)
		}
	}
}

// deleteLapsedCustomer deletes each server the customer created, one at a
// time and only while they're still paused past their grace period, then
// records it, frees their home machine and tells them. A customer who
// renews meanwhile keeps what's left. One whose servers are being moved,
// or whose move stopped, is left for a later look: their servers aren't all
// on their home machine.
func (s *Server) deleteLapsedCustomer(ctx context.Context, userID int64) error {
	if s.customerMoving(ctx, userID) {
		return nil
	}
	ids, err := s.creatorServers(userID)
	if err != nil {
		return err
	}
	home, placed, err := s.homeMachine(ctx, userID)
	if err != nil {
		return err
	}
	var m machine
	if len(ids) > 0 {
		if !placed {
			return errors.New("they have servers but no home machine")
		}
		if m, err = s.machineByID(home); err != nil {
			return err
		}
	}
	for _, id := range ids {
		err := s.deleteLapsedServer(ctx, m, userID, id)
		if errors.Is(err, errNotLapsed) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	err = s.finishLapsed(ctx, userID, home, len(ids))
	if errors.Is(err, errNotLapsed) {
		return nil
	}
	return err
}

// deleteLapsedServer deletes the customer's server id on m, keeping its
// final backup, and waits until it's gone.
func (s *Server) deleteLapsedServer(ctx context.Context, m machine, userID int64, id string) error {
	opID, err := s.startLapsedDelete(ctx, m, userID, id)
	if err != nil || opID == "" {
		return err
	}
	op, err := waitAgentOp(ctx, m, opID, deleteWait)
	if err != nil {
		return err
	}
	if op.Status != api.OpSucceeded {
		return fmt.Errorf("deleting %s failed: %s", id, op.Error)
	}
	s.forgetCreatorServer(id)
	return nil
}

// startLapsedDelete asks m to delete the customer's server id, keeping its
// final backup finalBackupDays, while they're still paused past their grace
// period, and returns the deletion's operation: "" when m has no such
// server any more. A renewal waits until the deletion has started, so it
// can't slip in between. The machine is recorded as keeping their final
// backups first, so one that renews during the deletion still gets theirs.
func (s *Server) startLapsedDelete(ctx context.Context, m machine, userID int64, id string) (string, error) {
	s.customersMu.Lock()
	defer s.customersMu.Unlock()
	if _, lapsed, err := s.lapsedCustomer(ctx, userID); err != nil {
		return "", err
	} else if !lapsed {
		return "", errNotLapsed
	}
	actx := asActor(ctx, placementActor)
	var st api.ServerStatus
	status, err := m.agent.Do(actx, http.MethodGet, "/v1/servers/"+id, nil, nil, &st)
	switch {
	case err != nil:
		return "", err
	case status == http.StatusNotFound:
		s.forgetCreatorServer(id)
		return "", nil
	case status != http.StatusOK:
		return "", fmt.Errorf("the agent answered %d about %s", status, id)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE customers SET final_backups_machine = ? WHERE user_id = ?`, m.ID, userID); err != nil {
		return "", errDB
	}
	req := api.DeleteServerRequest{Confirm: st.Name, Actor: placementActor, ForgetKey: true,
		KeepFinalBackupDays: finalBackupDays, KeptFor: keptFor(userID)}
	var op api.Operation
	status, err = m.agent.Do(actx, http.MethodPost, "/v1/servers/"+id+"/delete", nil, req, &op)
	switch {
	case err != nil:
		return "", err
	case status != http.StatusAccepted || op.ID == "":
		return "", fmt.Errorf("the agent answered %d to deleting %s", status, id)
	}
	return op.ID, nil
}

// finishLapsed records that the customer's servers are gone, frees their
// home machine and tells them until when their final backups download,
// while they're still paused past their grace period.
func (s *Server) finishLapsed(ctx context.Context, userID int64, home string, deleted int) error {
	s.customersMu.Lock()
	defer s.customersMu.Unlock()
	info, lapsed, err := s.lapsedCustomer(ctx, userID)
	if err != nil {
		return err
	}
	if !lapsed {
		return errNotLapsed
	}
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `UPDATE customers SET servers_deleted_at = ?, final_backups_machine = ?, told_ready = 0, told_waiting = 0, updated_at = ?
		WHERE user_id = ? AND state = ?`, now.UnixMilli(), home, now.UnixMilli(), userID, string(CustomerPaused)); err != nil {
		return errDB
	}
	if err := s.setHome(ctx, userID, ""); err != nil {
		return err
	}
	s.kickDiskLimits()
	s.kickSaleRoom()
	until := now.Add(finalBackupDays * 24 * time.Hour)
	s.audit(placementActor, "customer.delete", info.username, "succeeded",
		fmt.Sprintf("%d server(s) deleted %d days after their plan ended, each one's final backup kept until %s", deleted, graceDays, until.UTC().Format(time.DateOnly)))
	if err := s.notifier.Notify(ctx, info.customer, CustomerMessage{Kind: messageDeleted, Text: s.deletedText(ctx, until, deleted > 0)}); err != nil {
		s.log.Warn("could not tell a customer their servers were deleted", "user", userID, "err", err)
	}
	return nil
}

// lapsedInfo is who a lapsed customer is.
type lapsedInfo struct {
	customer Customer
	username string
}

// lapsedCustomer says who the customer is, and whether they're still paused
// past their grace period with their servers not yet deleted.
func (s *Server) lapsedCustomer(ctx context.Context, userID int64) (lapsedInfo, bool, error) {
	var info lapsedInfo
	var state string
	var deleteAfter, deletedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT c.provider, c.subject, c.handle, u.username, c.state, c.delete_after, c.servers_deleted_at
		FROM customers c JOIN users u ON u.id = c.user_id WHERE c.user_id = ?`, userID).
		Scan(&info.customer.Provider, &info.customer.Subject, &info.customer.Handle, &info.username, &state, &deleteAfter, &deletedAt)
	switch {
	case isNoRows(err):
		return info, false, nil
	case err != nil:
		return info, false, errDB
	}
	return info, CustomerState(state) == CustomerPaused && deleteAfter > 0 && deleteAfter <= s.now().UnixMilli() && deletedAt == 0, nil
}

// deletedText is the message a customer gets once their servers are
// deleted: until when the final backups download, and where.
func (s *Server) deletedText(ctx context.Context, until time.Time, servers bool) string {
	where := "on your Playkeeper dashboard"
	if dash, _ := s.dashboardURL(ctx); dash != "" {
		where = "at " + dash
	}
	text := fmt.Sprintf("Your Playkeeper plan ended %d days ago, so your servers are deleted now.", graceDays)
	if servers {
		text += " You can download a final backup of each until " + until.UTC().Format("2 January") + ": sign in " + where + "."
	}
	return text + " Renew any time to start again."
}

// keptFor labels the kept backups of the account's deleted servers, as its
// disk limit is named.
func keptFor(userID int64) string { return diskLimitPrefix + strconv.FormatInt(userID, 10) }

// forgetCreatorServer forgets who created a server that's gone.
func (s *Server) forgetCreatorServer(id string) {
	if _, err := s.db.Exec(`DELETE FROM creator_servers WHERE server_id = ?`, id); err != nil {
		s.log.Warn("could not forget a deleted server's creator", "server", id, "err", err)
	}
}

// waitAgentOp waits, for at most wait, until m's operation id has ended.
func waitAgentOp(ctx context.Context, m machine, id string, wait time.Duration) (api.Operation, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	t := time.NewTicker(opPoll)
	defer t.Stop()
	for {
		var op api.Operation
		status, err := m.agent.Do(ctx, http.MethodGet, "/v1/operations/"+id, nil, nil, &op)
		if err == nil && status == http.StatusOK && op.Status != api.OpRunning {
			return op, nil
		}
		select {
		case <-ctx.Done():
			return api.Operation{}, fmt.Errorf("the deletion %s didn't end within %s", id, wait)
		case <-t.C:
		}
	}
}

// finalBackup is a deleted server's final backup, as its customer sees it.
type finalBackup struct {
	ID         string    `json:"id"`
	ServerName string    `json:"serverName"`
	SizeBytes  int64     `json:"sizeBytes"`
	MadeAt     time.Time `json:"madeAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// hasFinalBackups reports whether a is a customer a machine keeps final
// backups for, paused or renewed.
func (s *Server) hasFinalBackups(a access) bool {
	if a.Customer == "" {
		return false
	}
	var mid string
	return s.db.QueryRow(`SELECT final_backups_machine FROM customers WHERE user_id = ?`, a.UserID).Scan(&mid) == nil && mid != ""
}

// finalBackupsOf lists the kept backups of the account's deleted servers,
// from the machine keeping them, and returns that machine. An account with
// none recorded has none.
func (s *Server) finalBackupsOf(ctx context.Context, userID int64) ([]api.KeptBackup, machine, error) {
	var mid string
	err := s.db.QueryRowContext(ctx, `SELECT final_backups_machine FROM customers WHERE user_id = ?`, userID).Scan(&mid)
	switch {
	case isNoRows(err) || err == nil && mid == "":
		return nil, machine{}, nil
	case err != nil:
		return nil, machine{}, errDB
	}
	m, err := s.machineByID(mid)
	if err != nil {
		return nil, machine{}, err
	}
	var list []api.KeptBackup
	status, err := m.agent.Do(ctx, http.MethodGet, "/v1/kept-backups", url.Values{"keptFor": {keptFor(userID)}}, nil, &list)
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("the agent answered %d to listing kept backups", status)
	}
	if err != nil {
		return nil, machine{}, err
	}
	return slices.DeleteFunc(list, func(k api.KeptBackup) bool { return k.KeptFor != keptFor(userID) }), m, nil
}

// hFinalBackups lists the final backups of the account's deleted servers.
func (s *Server) hFinalBackups(w http.ResponseWriter, r *http.Request, sess *session) {
	list, _, err := s.finalBackupsOf(r.Context(), sess.User.ID)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	out := make([]finalBackup, 0, len(list))
	for _, k := range list {
		out = append(out, finalBackup{ID: k.ID, ServerName: k.ServerName, SizeBytes: k.SizeBytes, MadeAt: k.MadeAt, ExpiresAt: k.ExpiresAt})
	}
	writeJSON(w, http.StatusOK, out)
}

// hFinalBackupDownload downloads one of the account's final backups, and
// nobody else's: only an id from their own list reaches the machine.
func (s *Server) hFinalBackupDownload(w http.ResponseWriter, r *http.Request, sess *session) {
	kid := r.PathValue("kid")
	list, m, err := s.finalBackupsOf(r.Context(), sess.User.ID)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	if !slices.ContainsFunc(list, func(k api.KeptBackup) bool { return k.ID == kid }) {
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "No such final backup.", "")
		return
	}
	s.relayArchive(w, r, m, "/v1/kept-backups/"+kid+"/download", kid, sess.User.Username)
}
