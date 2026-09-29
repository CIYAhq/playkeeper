package panel

import (
	"context"
	"errors"
	"net/http"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

// The ready server (step 5 of the managed-beta plan). Once placement gives a
// new customer a home machine, the core tells them, through their billing
// provider, that their server is ready to start: they sign in and create it
// in one dialog, whose start accepts Minecraft's EULA, as every server's
// does, since Playkeeper downloads nothing before that. With no room yet
// they're told once that it's being set up, their dashboard says so, and
// creating a server waits. The fleet calls startWaitingCustomer when room
// appears, which places them and tells them it's ready.

// Kinds of the messages the core sends through the notifier.
const (
	messageReady     = "ready"
	messageSettingUp = "setting_up"
)

const settingUpText = "Your Playkeeper server is being set up. We'll message you here as soon as it's ready."

// errWaitingForRoom refuses a server to a customer placement hasn't given a
// home machine yet.
var errWaitingForRoom = &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict,
	Msg: "Your server is being set up: there's no room for it on a machine yet.", Hint: "We'll message you as soon as it's ready."}

// tellPlaced tells an active customer where their server stands: that it's
// ready to start once placed, and that it's being set up while they wait for
// room. Each is sent once, recorded as sent, and "being set up" never after
// "ready", so a repeated call sends nothing. A message that couldn't be
// sent is sent by the next call.
func (s *Server) tellPlaced(ctx context.Context, cust Customer, userID int64, placed bool) error {
	var toldReady, toldWaiting int64
	if err := s.db.QueryRowContext(ctx, `SELECT told_ready, told_waiting FROM customers WHERE user_id = ?`, userID).Scan(&toldReady, &toldWaiting); err != nil {
		return errDB
	}
	switch {
	case toldReady != 0:
		return nil
	case placed:
		if err := s.notifier.Notify(ctx, cust, CustomerMessage{Kind: messageReady, Text: s.readyText(ctx)}); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE customers SET told_ready = ?, told_waiting = 0 WHERE user_id = ?`, s.now().UnixMilli(), userID); err != nil {
			return errDB
		}
	case toldWaiting == 0:
		if err := s.notifier.Notify(ctx, cust, CustomerMessage{Kind: messageSettingUp, Text: settingUpText}); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE customers SET told_waiting = ? WHERE user_id = ?`, s.now().UnixMilli(), userID); err != nil {
			return errDB
		}
	}
	return nil
}

// readyText is the ready message, with the dashboard's address once it has
// one that opens without a warning.
func (s *Server) readyText(ctx context.Context) string {
	if dash, _ := s.dashboardURL(ctx); dash != "" {
		return "Your Playkeeper server is ready to start. Sign in at " + dash + " and create your server: it's up a few minutes later."
	}
	return "Your Playkeeper server is ready to start. Sign in on your Playkeeper dashboard and create your server: it's up a few minutes later."
}

// startWaitingCustomer places a customer who was waiting for room, and tells
// them their server is ready. The fleet calls it when room appears. A
// customer still without room is left waiting, and a paused or suspended one
// gets no server and no message until they're active again.
func (s *Server) startWaitingCustomer(ctx context.Context, userID int64) error {
	s.customersMu.Lock()
	defer s.customersMu.Unlock()
	var cust Customer
	var state, planID string
	var al invites.Allowance
	err := s.db.QueryRowContext(ctx, `SELECT c.provider, c.subject, c.handle, c.state, c.plan_id, m.allowance_servers, m.allowance_memory_mb, m.allowance_disk_gb
		FROM customers c JOIN project_members m ON m.user_id = c.user_id WHERE c.user_id = ? ORDER BY m.created_at LIMIT 1`, userID).
		Scan(&cust.Provider, &cust.Subject, &cust.Handle, &state, &planID, &al.Servers, &al.MemoryMB, &al.DiskGB)
	switch {
	case isNoRows(err):
		return errNoCustomer
	case err != nil:
		return errDB
	}
	if CustomerState(state) != CustomerActive {
		return nil
	}
	_, err = s.placeCustomer(ctx, userID, CustomerPlan{ID: planID, Servers: al.Servers, MemoryMB: al.MemoryMB, DiskGB: al.DiskGB})
	switch {
	case errors.Is(err, errNoRoom):
		return nil
	case err != nil:
		return err
	}
	return s.tellPlaced(ctx, cust, userID, true)
}

// customerWaiting reports whether a is a customer placement hasn't given a
// home machine yet. One whose home can't be read waits too.
func (s *Server) customerWaiting(ctx context.Context, a access) bool {
	if a.Customer == "" {
		return false
	}
	var machineID string
	err := s.db.QueryRowContext(ctx, `SELECT machine_id FROM customer_homes WHERE user_id = ?`, a.UserID).Scan(&machineID)
	return err != nil || machineID == ""
}
