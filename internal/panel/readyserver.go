package panel

import (
	"context"
	"errors"
	"net/http"
	"net/url"

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

// Kinds of the messages the core sends through the notifier. A customer
// told their server was ready who loses their machine, as a removed
// machine's customers do, is told there's no room for their servers while
// no machine has any, and then that there's room again.
const (
	messageReady     = "ready"
	messageSettingUp = "setting_up"
	messageNoRoom    = "no_room"
	messageRoomAgain = "room_again"
)

const (
	settingUpText = "Your Playkeeper server is being set up. We'll message you here as soon as it's ready."
	noRoomText    = "There's no room for your Playkeeper servers right now. We'll message you here as soon as there is."
)

// errWaitingForRoom refuses a server to a customer placement hasn't given a
// home machine yet, and errNoRoomAgain to one told their server was ready
// who has lost their machine.
var (
	errWaitingForRoom = &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict,
		Msg: "Your server is being set up: there's no room for it on a machine yet.", Hint: "We'll message you as soon as it's ready."}
	errNoRoomAgain = &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict,
		Msg: "There's no room for your servers right now.", Hint: "We'll message you as soon as there is."}
)

// tellPlaced tells an active customer where their server stands: that it's
// ready to start once placed, and that it's being set up while they wait for
// room. Each is sent once, recorded as sent, and "being set up" never after
// "ready", so a repeated call sends nothing. One told it was ready who has
// lost their machine is told instead that there's no room for their
// servers, and once placed again, that there is. A message that couldn't be
// sent is sent by the next call.
func (s *Server) tellPlaced(ctx context.Context, cust Customer, userID int64, placed bool) error {
	var toldReady, toldWaiting int64
	if err := s.db.QueryRowContext(ctx, `SELECT told_ready, told_waiting FROM customers WHERE user_id = ?`, userID).Scan(&toldReady, &toldWaiting); err != nil {
		return errDB
	}
	switch {
	case toldReady != 0 && placed && toldWaiting != 0:
		if err := s.notifier.Notify(ctx, cust, CustomerMessage{Kind: messageRoomAgain, Text: s.roomAgainText(ctx, cust)}); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE customers SET told_waiting = 0 WHERE user_id = ?`, userID); err != nil {
			return errDB
		}
	case toldReady != 0 && !placed && toldWaiting == 0:
		if err := s.notifier.Notify(ctx, cust, CustomerMessage{Kind: messageNoRoom, Text: noRoomText}); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE customers SET told_waiting = ? WHERE user_id = ?`, s.now().UnixMilli(), userID); err != nil {
			return errDB
		}
	case toldReady != 0:
		return nil
	case placed:
		if err := s.notifier.Notify(ctx, cust, CustomerMessage{Kind: messageReady, Text: s.readyText(ctx, cust)}); err != nil {
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

// readyText is the ready message, with where the customer signs in once
// the dashboard has an address that opens without a warning.
func (s *Server) readyText(ctx context.Context, cust Customer) string {
	if at := s.signInAt(ctx, cust); at != "" {
		return "Your Playkeeper server is ready to start. Sign in at " + at + " and create your server: it's up a few minutes later."
	}
	return "Your Playkeeper server is ready to start. Sign in on your Playkeeper dashboard and create your server: it's up a few minutes later."
}

// roomAgainText is the message for customer cust, who lost their machine,
// once there's room for their servers again.
func (s *Server) roomAgainText(ctx context.Context, cust Customer) string {
	if at := s.signInAt(ctx, cust); at != "" {
		return "There's room for your Playkeeper servers again. Sign in at " + at + " to create your server."
	}
	return "There's room for your Playkeeper servers again. Sign in on your Playkeeper dashboard to create your server."
}

// signInAt is where the customer signs in: the dashboard's sign-in page for
// the store they bought from, so someone who bought from two stores signs
// in to this store's account. It's "" while the dashboard has no address
// that opens without a warning.
func (s *Server) signInAt(ctx context.Context, cust Customer) string {
	dash, _ := s.dashboardURL(ctx)
	if dash == "" {
		return ""
	}
	return dash + "/login?store=" + url.QueryEscape(cust.Store)
}

// startWaitingCustomer places a customer who was waiting for room, and tells
// them their server is ready. The fleet calls it when room appears, and
// runCustomers every minute anyway. A customer still without room is left
// waiting, and a paused or suspended one gets no server and no message until
// they're active again.
func (s *Server) startWaitingCustomer(ctx context.Context, userID int64) error {
	s.customersMu.Lock()
	defer s.customersMu.Unlock()
	var cust Customer
	var state, planID string
	var al invites.Allowance
	err := s.db.QueryRowContext(ctx, `SELECT c.provider, c.store, c.subject, c.handle, c.state, c.plan_id, m.allowance_servers, m.allowance_memory_mb, m.allowance_disk_gb
		FROM customers c JOIN project_members m ON m.user_id = c.user_id WHERE c.user_id = ? ORDER BY m.created_at LIMIT 1`, userID).
		Scan(&cust.Provider, &cust.Store, &cust.Subject, &cust.Handle, &state, &planID, &al.Servers, &al.MemoryMB, &al.DiskGB)
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
		return s.tellPlaced(ctx, cust, userID, false)
	case err != nil:
		return err
	}
	return s.tellPlaced(ctx, cust, userID, true)
}

// customerWaiting reports whether a is an active customer placement hasn't
// given a home machine yet. One whose home can't be read waits too.
func (s *Server) customerWaiting(ctx context.Context, a access) bool {
	if a.Customer != CustomerActive {
		return false
	}
	var machineID string
	err := s.db.QueryRowContext(ctx, `SELECT machine_id FROM customer_homes WHERE user_id = ?`, a.UserID).Scan(&machineID)
	return err != nil || machineID == ""
}

// waitingAgain reports whether customer a, waiting for room, was told
// their server was ready before: they lost their machine.
func (s *Server) waitingAgain(ctx context.Context, a access) bool {
	if !s.customerWaiting(ctx, a) {
		return false
	}
	var toldReady int64
	return s.db.QueryRowContext(ctx, `SELECT told_ready FROM customers WHERE user_id = ?`, a.UserID).Scan(&toldReady) == nil && toldReady != 0
}

// waitingRefusal is why customer a may make no server while they wait for
// room, or nil.
func (s *Server) waitingRefusal(ctx context.Context, a access) error {
	switch {
	case s.waitingAgain(ctx, a):
		return errNoRoomAgain
	case s.customerWaiting(ctx, a):
		return errWaitingForRoom
	}
	return nil
}
