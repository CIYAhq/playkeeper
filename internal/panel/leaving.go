package panel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/whop"
)

// A store leaving (task 3.2 of the hosted blueprint, second half). The Whop
// side calls whopStoreLeft once it's sure a business uninstalled the
// Playkeeper Cloud app or removed Playkeeper's share. Nothing else makes a
// store leave: a missing grant or an empty read never does, since the grant
// is checked before anything is read (see whopGrantProblem). A store that
// left sells nothing, as a suspended one, and every customer of it who was
// started goes down the plan-end path from the dashboard's own records,
// with nothing read from Whop: paused, 14 days, a final backup, then
// deletion (see pausing.go and deletion.go). They're told in the store's
// support chat while the app can still post there; otherwise the message
// waits, and their dashboard says their plan ended. Added again, the store
// is back, closed as not open yet (see closing.go): its customers with a
// plan start again once its seller opens it, as a renewal does, and their
// 14 days go on meanwhile.

// maxLeftWhy bounds why a store left.
const maxLeftWhy = 200

// whopStoreLeft records that the app store storeID left, for why, and has
// its next pass end every customer's plan. Called again, it changes
// nothing. It doesn't wait on the reconciler, so the store's own pass may
// call it.
func (s *Server) whopStoreLeft(ctx context.Context, storeID, why string) error {
	st, ok, err := s.whopStoreByID(ctx, storeID)
	switch {
	case err != nil:
		return err
	case !ok:
		return fmt.Errorf("this dashboard doesn't sell for %s", storeID)
	case st.Via != whopViaApp:
		return errors.New("the dashboard's own store doesn't leave: it's disconnected in Settings › Sell on Whop")
	}
	why = strings.TrimSpace(why)
	if len(why) > maxLeftWhy {
		why = strings.ToValidUTF8(why[:maxLeftWhy], "")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE whop_stores SET left_at = ?, left_why = ? WHERE store_id = ? AND left_at = 0`, s.now().UnixMilli(), why, storeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.audit("system", "whop.store_left", storeID, "succeeded", whopName(st.Account)+": "+why)
		s.kickSaleRoom()
		s.kickWhopStore(storeID)
	}
	return nil
}

// bringBackWhopStore has an app store that left be a store again, closed as
// not open yet, and says whether it had left. Its next pass reads the store
// and every membership again, and once it's open, its customers with a plan
// start again.
func (s *Server) bringBackWhopStore(ctx context.Context, a whop.Account) (bool, error) {
	back := false
	err := s.immediate(ctx, func(conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, `UPDATE whop_stores SET left_at = 0, left_why = '', synced_at = 0, polled_at = 0,
			title = COALESCE(NULLIF(?, ''), title), route = COALESCE(NULLIF(?, ''), route) WHERE store_id = ? AND via = ? AND left_at != 0`,
			a.Title, a.Route, a.ID, whopViaApp)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		back = true
		_, err = conn.ExecContext(ctx, `INSERT INTO whop_store_closures(store_id, closed_by, why, closed_at) VALUES(?,?,?,?) ON CONFLICT(store_id, closed_by) DO NOTHING`,
			a.ID, whopNotOpenYet, whopNotOpenYetWhy, s.now().UnixMilli())
		return err
	})
	if err != nil {
		return false, err
	}
	if back {
		s.audit("system", "whop.store_back", a.ID, "succeeded", "installed the Playkeeper Cloud app again: "+whopName(a)+"; not open yet")
		s.kickSaleRoom()
		s.kickWhopStore(a.ID)
	}
	return back, nil
}

// endLeftStorePlans sends every customer of the store that left who was
// started, and isn't paused yet, down the plan-end path, from the
// dashboard's own records: nothing of theirs is read from Whop, where the
// store may be gone. One whose pause fails is tried again later.
func (s *Server) endLeftStorePlans(ctx context.Context, st whopStore) {
	custs, err := s.whopCustomers(ctx, st.ID)
	if err != nil {
		s.log.Error("could not list the customers of a store that left", "store", st.ID, "err", err)
		return
	}
	reason := "their store left Playkeeper Cloud"
	if st.LeftWhy != "" {
		reason += ": " + st.LeftWhy
	}
	now := s.now().UnixMilli()
	for _, wc := range custs {
		if wc.Applied == "" || wc.Paused || wc.NextTryAt > now {
			continue
		}
		cust := Customer{Provider: whopProvider, Store: st.ID, Subject: wc.WhopUserID, Handle: wc.Handle}
		at := s.now()
		if err := s.hosting.PauseCustomer(ctx, cust, reason); err != nil {
			s.whopCustomerFailed(st.ID, wc, err)
			continue
		}
		if err := s.recordWhopCustomer(cust, wc.Applied, true, at); err != nil {
			s.log.Error("could not record that a customer's plan ended", "store", st.ID, "err", err)
		}
	}
}
