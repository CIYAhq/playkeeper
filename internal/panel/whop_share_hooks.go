package panel

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/whop"
)

// Where the store's pass meets Playkeeper's share (the hosted blueprint's
// 2.2, see whop_share.go). An open app store's share is checked each time
// its pass has just read its plans; a share gone or short closes it for a
// reason of its own, and after whopShareGrace it leaves. A store whose
// grant has lacked what its pass reads for whopGrantGrace leaves too. A
// customer starts, or their plan grows, only once each membership giving
// them servers was paid with the share. Every payment the checks read, and
// each renewal and refund since the last read, is kept for the seller's
// view.

const (
	// whopShareClosed is the reason the share check closes a store for.
	whopShareClosed = "share"
	// whopShareGrace is how long a store's share may stay gone or short,
	// its grant intact, before the store leaves, and whopGrantGrace how long
	// its grant may lack what its pass reads, as after the app was
	// uninstalled. The owner's to change.
	whopShareGrace = 72 * time.Hour
	whopGrantGrace = 7 * 24 * time.Hour
	// whopPaymentsBack is how far back a store's first read of its payments
	// and refunds goes, a month and a few days, and whopPaymentsOverlap how
	// far each later read goes back before the last one, so a payment Whop
	// records a little late isn't missed.
	whopPaymentsBack    = 35 * 24 * time.Hour
	whopPaymentsOverlap = time.Hour
)

// whopShareStep checks an open app store's share, once its pass has just
// read its plans (see syncWhopShares). A share gone or short closes the
// store for whopShareClosed, in the problem's words, and after
// whopShareGrace like that the store leaves. A share set right opens it
// again for that reason alone. A store that isn't open yet has no share to
// check, since Open the store sets it. It says whether the store left, and
// keeps st's closed words current, so the rest of the pass sees them.
func (s *Server) whopShareStep(ctx context.Context, c *whop.Client, st *whopStore) bool {
	notOpen, err := s.whopClosedFor(ctx, st.ID, whopNotOpenYet)
	if err != nil || notOpen {
		return false
	}
	problem, err := s.syncWhopShares(ctx, c, *st, false)
	if err != nil {
		s.log.Warn("could not check Playkeeper's share on a store", "store", st.ID, "err", err)
		return false
	}
	if problem == "" {
		if err := s.whopWatchClear(ctx, st.ID, "share_bad_since"); err != nil {
			s.log.Error("could not note a store's share is right", "store", st.ID, "err", err)
		}
		if _, err := s.openWhopStore(ctx, st.ID, whopShareClosed); err != nil {
			s.log.Error("could not open a store whose share is right again", "store", st.ID, "err", err)
		}
	} else {
		if err := s.closeWhopStore(ctx, st.ID, whopShareClosed, problem); err != nil {
			s.log.Error("could not close a store whose share is gone or short", "store", st.ID, "err", err)
			st.ClosedWhy = cmpOr(st.ClosedWhy, problem)
			return false
		}
		since, err := s.whopWatchSince(ctx, st.ID, "share_bad_since")
		if err == nil && s.now().Sub(since) >= whopShareGrace {
			if err := s.whopStoreLeft(ctx, st.ID, "Playkeeper's share has been gone or short for 3 days: "+problem); err == nil {
				return true
			}
			s.log.Error("could not have a store leave", "store", st.ID, "err", err)
		}
	}
	s.whopReadPayments(ctx, c, *st)
	if fresh, ok, err := s.whopStoreByID(ctx, st.ID); err == nil && ok {
		st.ClosedWhy = fresh.ClosedWhy
	} else if problem != "" {
		st.ClosedWhy = cmpOr(st.ClosedWhy, problem)
	}
	return false
}

// whopReadPayments keeps each payment an open app store was paid since its
// payments were last read, and each it refunded since, for the seller's view
// (keepCheckedPayment), on the share check's schedule. A payment's share
// comes from its fee lines, which are kept too. The read counts as done
// only once all of it is, so what failed is read again next time.
func (s *Server) whopReadPayments(ctx context.Context, c *whop.Client, st whopStore) {
	now := s.now()
	var last int64
	if err := s.db.QueryRowContext(ctx, `SELECT payments_read_at FROM whop_share_watch WHERE store_id = ?`, st.ID).Scan(&last); err != nil && !isNoRows(err) {
		s.log.Error("could not read when a store's payments were read", "store", st.ID, "err", err)
		return
	}
	since := now.Add(-whopPaymentsBack)
	if last > 0 {
		since = time.UnixMilli(last).Add(-whopPaymentsOverlap)
	}
	pays, err := c.PaymentsSince(ctx, st.ID, since)
	if err != nil {
		s.log.Warn("could not read a store's payments", "store", st.ID, "err", err)
		return
	}
	refunds, err := c.RefundsSince(ctx, st.ID, since)
	if err != nil {
		s.log.Warn("could not read a store's refunds", "store", st.ID, "err", err)
		return
	}
	for _, r := range refunds {
		pay, err := c.Payment(ctx, r.PaymentID)
		if err != nil {
			s.log.Warn("could not read a refunded payment", "store", st.ID, "payment", r.PaymentID, "err", err)
			return
		}
		pays = append(pays, pay)
	}
	for _, pay := range pays {
		lines, err := c.PaymentFees(ctx, pay.ID)
		if err == nil {
			err = s.keepWhopFeeLines(ctx, st.ID, pay.ID, lines)
		}
		if err != nil {
			s.log.Warn("could not read a payment's fee lines", "store", st.ID, "payment", pay.ID, "err", err)
			return
		}
		s.keepCheckedPayment(ctx, st, pay, lines, "")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO whop_share_watch(store_id, payments_read_at) VALUES(?, ?)
		ON CONFLICT(store_id) DO UPDATE SET payments_read_at = excluded.payments_read_at`, st.ID, now.UnixMilli()); err != nil {
		s.log.Error("could not note a store's payments were read", "store", st.ID, "err", err)
	}
}

// keepCheckedPayment keeps a payment the share check read for the seller's
// view (keepWhopPayment): what was paid and refunded, and Playkeeper's share
// in its fee lines (whopShareOf), in the smallest unit of its currency. Its
// buyer is whopUserID when Whop names none. One it can't keep is logged,
// since the check's answer doesn't hang on it.
func (s *Server) keepCheckedPayment(ctx context.Context, st whopStore, pay whop.Payment, lines []whop.PaymentFee, whopUserID string) {
	if pay.Total == nil {
		s.log.Warn("a payment has no total to keep", "store", st.ID, "payment", pay.ID)
		return
	}
	amount, err := pay.Total.Minor()
	if err != nil {
		s.log.Warn("could not read a payment's total", "store", st.ID, "payment", pay.ID, "err", err)
		return
	}
	var refunded int64
	if pay.Refunded != nil {
		if refunded, err = pay.Refunded.Minor(); err != nil {
			s.log.Warn("could not read what a payment refunded", "store", st.ID, "payment", pay.ID, "err", err)
			return
		}
	}
	if pay.User != nil && pay.User.ID != "" {
		whopUserID = pay.User.ID
	}
	paidAt := pay.PaidTime()
	if paidAt.IsZero() {
		paidAt = s.now()
	}
	p := whopPayment{ID: pay.ID, Store: st.ID, WhopUserID: whopUserID, PlanID: pay.PlanID, Currency: pay.Total.Currency,
		Amount: amount, Share: min(whopShareOf(lines, pay.Total.Currency), amount), Refunded: min(refunded, amount), PaidAt: paidAt}
	if err := s.keepWhopPayment(ctx, p); err != nil {
		s.log.Warn("could not keep a payment for the seller's view", "store", st.ID, "payment", pay.ID, "err", err)
	}
}

// whopShareOf is Playkeeper's share in a payment's fee lines, in the
// smallest unit of currency: its revenue share lines added up, whichever
// sign Whop writes them with, so a share given back nets to nothing.
func whopShareOf(lines []whop.PaymentFee, currency string) int64 {
	var n int64
	for _, l := range lines {
		if l.Origin != whopShareOrigin || l.Settled.Currency != currency {
			continue
		}
		if m, err := l.Settled.Minor(); err == nil {
			n += m
		}
	}
	return max(n, -n)
}

// whopGrantWatch notes since when an app store's grant has lacked what its
// pass reads (lost), and has the store leave once that's whopGrantGrace:
// Whop says nothing when the app is uninstalled, which reads like a grant
// taken back. A grant Whop couldn't check (problem, with nothing lost)
// counts neither way. It says whether the store left.
func (s *Server) whopGrantWatch(ctx context.Context, st whopStore, lost []string, problem string) bool {
	switch {
	case len(lost) > 0:
		since, err := s.whopWatchSince(ctx, st.ID, "grant_gone_since")
		if err != nil || s.now().Sub(since) < whopGrantGrace {
			return false
		}
		if err := s.whopStoreLeft(ctx, st.ID, "The Playkeeper Cloud app's grant has lacked "+strings.Join(lost, ", ")+" for 7 days, as when the app was uninstalled."); err != nil {
			s.log.Error("could not have a store leave", "store", st.ID, "err", err)
			return false
		}
		return true
	case problem == "":
		if err := s.whopWatchClear(ctx, st.ID, "grant_gone_since"); err != nil {
			s.log.Error("could not note a store's grant is back", "store", st.ID, "err", err)
		}
	}
	return false
}

// whopWatchSince is since when the store has been as column says (see
// whop_share_watch), from now if it wasn't.
func (s *Server) whopWatchSince(ctx context.Context, storeID, column string) (time.Time, error) {
	if column != "share_bad_since" && column != "grant_gone_since" {
		return time.Time{}, fmt.Errorf("no such watch %q", column)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO whop_share_watch(store_id, `+column+`) VALUES(?, ?)
		ON CONFLICT(store_id) DO UPDATE SET `+column+` = CASE WHEN `+column+` = 0 THEN excluded.`+column+` ELSE `+column+` END`, storeID, s.now().UnixMilli()); err != nil {
		return time.Time{}, err
	}
	var since int64
	err := s.db.QueryRowContext(ctx, `SELECT `+column+` FROM whop_share_watch WHERE store_id = ?`, storeID).Scan(&since)
	return time.UnixMilli(since), err
}

// whopWatchClear notes the store is no longer as column says.
func (s *Server) whopWatchClear(ctx context.Context, storeID, column string) error {
	if column != "share_bad_since" && column != "grant_gone_since" {
		return fmt.Errorf("no such watch %q", column)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE whop_share_watch SET `+column+` = 0 WHERE store_id = ?`, storeID)
	return err
}

// whopClosedFor says whether the store is closed for the reason by.
func (s *Server) whopClosedFor(ctx context.Context, storeID, by string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM whop_store_closures WHERE store_id = ? AND closed_by = ?`, storeID, by).Scan(&one)
	if isNoRows(err) {
		return false, nil
	}
	return err == nil, err
}

// whopCustomerPaid checks that each membership giving an app store's
// customer their servers was paid with Playkeeper's share for its plan (see
// whopSharePaid), before they start or their plan grows.
func (s *Server) whopCustomerPaid(ctx context.Context, c *whop.Client, st whopStore, whopUserID string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT m.membership_id, m.status, p.allowance_memory_mb FROM whop_memberships m
		JOIN whop_plans p ON p.store_id = m.store_id AND p.plan_id = m.plan_id
		WHERE m.store_id = ? AND m.whop_user_id = ? AND m.stale = 0 AND p.allowance_from != '' ORDER BY m.membership_id`, st.ID, whopUserID)
	if err != nil {
		return err
	}
	type owed struct {
		membership string
		memoryMB   int
	}
	var all []owed
	for rows.Next() {
		var o owed
		var status string
		if err := rows.Scan(&o.membership, &status, &o.memoryMB); err != nil {
			rows.Close()
			return err
		}
		if (whop.Membership{Status: status}).HasAccess() {
			all = append(all, o)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, o := range all {
		if err := s.whopSharePaid(ctx, c, st, whopUserID, o.membership, o.memoryMB); err != nil {
			return err
		}
	}
	return nil
}

// whopPlanGrows says whether p gives more servers or memory than the plan
// applied last (see planKey), as a new membership does. A plan applied in
// a shape it can't read counts as growing, so it's checked.
func whopPlanGrows(applied string, p CustomerPlan) bool {
	parts := strings.Split(applied, "|")
	if len(parts) < 4 {
		return true
	}
	servers, err1 := strconv.Atoi(parts[len(parts)-3])
	memoryMB, err2 := strconv.Atoi(parts[len(parts)-2])
	if err1 != nil || err2 != nil {
		return true
	}
	return p.Servers > servers || p.MemoryMB > memoryMB
}
