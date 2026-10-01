package panel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/whop"
)

// Where the store's pass meets Playkeeper's share (the hosted blueprint's
// 2.2, see whop_share.go). An open app store's share is checked each time
// its pass has just read its plans, and before a payment check when it
// wasn't just now (whopShareDue); a share gone or short closes it for a
// reason of its own, and after whopShareGrace it leaves. A store whose
// grant has lacked what its pass reads for whopGrantGrace leaves too. A
// customer's membership gives them servers only once a payment of it
// carried the share for its plan (whopPaidPlan). Every payment the checks
// read, and each renewal and refund since the last read, is kept for the
// seller's view.

const (
	// whopShareClosed is the reason the share check closes a store for.
	whopShareClosed = "share"
	// whopShareGrace is how long a store's share may stay gone or short,
	// its grant intact, before the store leaves, and whopGrantGrace how long
	// its grant may lack what its pass reads, as after the app was
	// uninstalled. The owner's to change.
	whopShareGrace = 72 * time.Hour
	whopGrantGrace = 7 * 24 * time.Hour
	// whopShareUncheckedFor is how long the share check may keep failing
	// before the store closes for it (noteWhopShareUnchecked).
	whopShareUncheckedFor = time.Hour
	// whopBillingPeriod is how long a payment of a hosted plan pays for,
	// since Open the store sells only plans that renew every 30 days. A
	// membership gives a customer already running servers while its latest
	// payment that carried Playkeeper's share is younger than that plus the
	// renewal grace, which covers Whop's retries of a renewal that failed:
	// whopRenewalGraceDays, up to whopRenewalGraceMax, the owner's to
	// change. A start or a restart needs a payment from its billing period.
	whopBillingPeriod    = 30 * 24 * time.Hour
	whopRenewalGraceDays = 7
	whopRenewalGraceMax  = 30
	// whopShareFresh is how recently the share check must have found the
	// store's share right for a payment check to count. A payment's fee
	// lines don't say who received its share, so between the store's reads
	// a seller could swap Playkeeper for a partner of their own at the same
	// percentage, and the payments made meanwhile would look paid.
	whopShareFresh = 2 * time.Minute
	// whopPaymentsBack is how far back a store's first read of its payments
	// and refunds goes, a month and a few days, and whopPaymentsOverlap how
	// far each later read goes back before the last one, so a payment Whop
	// records a little late isn't missed.
	whopPaymentsBack    = 35 * 24 * time.Hour
	whopPaymentsOverlap = time.Hour
)

// whopShareStep checks an open app store's share, once its pass has just
// read its plans or a payment check waits on it (whopShareDue), and notes
// when it found it right (see syncWhopShares). A share gone or short
// closes the store for whopShareClosed, in the problem's words, and after
// whopShareGrace like that the store leaves. A check that keeps failing
// closes it too, but never has it leave (noteWhopShareUnchecked). A share
// set right opens it again for that reason alone. A store that isn't open
// yet has no share to check, since Open the store sets it. It says whether
// the store left, and keeps st's closed words current, so the rest of the
// pass sees them.
func (s *Server) whopShareStep(ctx context.Context, c *whop.Client, st *whopStore) bool {
	notOpen, err := s.whopClosedFor(ctx, st.ID, whopNotOpenYet)
	if err != nil || notOpen {
		return false
	}
	problem, err := s.syncWhopShares(ctx, c, *st, false)
	if err != nil {
		s.log.Warn("could not check Playkeeper's share on a store", "store", st.ID, "err", err)
		s.noteWhopShareUnchecked(ctx, st, err)
		return false
	}
	if err := s.whopWatchClear(ctx, st.ID, "share_unchecked_since"); err != nil {
		s.log.Error("could not note a store's share was checked", "store", st.ID, "err", err)
	}
	s.noteWhopShareRight(ctx, st.ID, problem == "")
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
	s.whopReadPayments(ctx, c, *st, problem == "")
	if fresh, ok, err := s.whopStoreByID(ctx, st.ID); err == nil && ok {
		st.ClosedWhy = fresh.ClosedWhy
	} else if problem != "" {
		st.ClosedWhy = cmpOr(st.ClosedWhy, problem)
	}
	return false
}

// noteWhopShareUnchecked notes since when the share check has failed for
// the store, err being the latest failure, and once that's
// whopShareUncheckedFor, closes the store for whopShareClosed in its words:
// nobody would be started on what it sells meanwhile (whopShareFresh). The
// next check that gets an answer opens it again, or closes it for what it
// found. A failing check never has the store leave, since a Whop outage
// would have every store leave: a store whose grant was taken back leaves
// by the grant watch (whopGrantWatch).
func (s *Server) noteWhopShareUnchecked(ctx context.Context, st *whopStore, err error) {
	since, werr := s.whopWatchSince(ctx, st.ID, "share_unchecked_since")
	if werr != nil {
		s.log.Error("could not note since when a store's share couldn't be checked", "store", st.ID, "err", werr)
		return
	}
	if s.now().Sub(since) < whopShareUncheckedFor {
		return
	}
	why := "Playkeeper's share couldn't be checked on Whop for an hour, so the store doesn't sell until it can be."
	var we *whop.Error
	if errors.As(err, &we) {
		why = "Playkeeper's share couldn't be checked on Whop for an hour (Whop said: " + cmpOr(we.Message, fmt.Sprintf("error %d", we.Status)) +
			"), so the store doesn't sell until it can be."
	}
	if err := s.closeWhopStore(ctx, st.ID, whopShareClosed, why); err != nil {
		s.log.Error("could not close a store whose share couldn't be checked", "store", st.ID, "err", err)
	}
	st.ClosedWhy = cmpOr(st.ClosedWhy, why)
}

// noteWhopShareRight notes when the share check last found the store's
// share right, or that it found it wrong (see whopShareFresh).
func (s *Server) noteWhopShareRight(ctx context.Context, storeID string, right bool) {
	at := int64(0)
	if right {
		at = s.now().UnixMilli()
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO whop_share_watch(store_id, share_right_at) VALUES(?, ?)
		ON CONFLICT(store_id) DO UPDATE SET share_right_at = excluded.share_right_at`, storeID, at); err != nil {
		s.log.Error("could not note what the share check found", "store", storeID, "err", err)
	}
}

// whopShareRecent says whether the share check found the store's share
// right within whopShareFresh.
func (s *Server) whopShareRecent(ctx context.Context, storeID string) bool {
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT share_right_at FROM whop_share_watch WHERE store_id = ?`, storeID).Scan(&at)
	return err == nil && at > 0 && s.now().Sub(time.UnixMilli(at)) < whopShareFresh
}

// whopShareDue says whether an open app store's pass checks its share
// though it didn't just read its plans: a payment check waits on it
// (whopPaidPlan), and it wasn't found right recently enough.
func (s *Server) whopShareDue(ctx context.Context, st whopStore) bool {
	if st.Via != whopViaApp || st.ClosedWhy != "" || s.whopShareRecent(ctx, st.ID) {
		return false
	}
	custs, err := s.whopCustomers(ctx, st.ID)
	if err != nil {
		s.log.Error("could not list Whop customers", "store", st.ID, "err", err)
		return false
	}
	now, grace := s.now(), s.whopRenewalGrace(ctx)
	for _, wc := range custs {
		if wc.Unconfirmed > 0 || wc.NextTryAt > now.UnixMilli() {
			continue
		}
		since := now.Add(-whopPaidFor(wc, grace))
		for _, h := range wc.Hosting {
			if h.lapsed(since).checkDue(now.UnixMilli()) {
				return true
			}
		}
	}
	return false
}

// whopRenewalGrace is how long a renewal may be late before its membership
// stops giving servers, as the owner set it (whopBillingPeriod).
func (s *Server) whopRenewalGrace(ctx context.Context) time.Duration {
	days := whopRenewalGraceDays
	if app, err := s.readWhopApp(ctx); err == nil {
		days = app.RenewalGraceDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// whopPaidFor is how long a payment of a customer's membership gives them
// servers: through the renewal grace for a customer already running, and
// its billing period alone for a start or a restart, so a membership
// brought back without a payment doesn't start them on an old one.
func whopPaidFor(wc whopCustomer, grace time.Duration) time.Duration {
	if wc.Applied != "" && !wc.Paused {
		return whopBillingPeriod + grace
	}
	return whopBillingPeriod
}

// whopReadPayments keeps each payment an open app store was paid since its
// payments were last read, and each it refunded since, for the seller's view
// (keepCheckedPayment), on the share check's schedule. Only payments for its
// hosting products count, those the dashboard keeps Playkeeper's share on
// (whopSharesSet), since a seller may sell other things on the same
// business. A payment's share comes from its fee lines, which are kept too,
// and read once: a payment the overlap lists again is read again only when
// more of it was refunded since (whopPaymentKept). Each also counts for its
// membership's payment check (noteWhopRenewal), so renewals need no reads
// of their own: a refund always, and a payment only while the share check
// has just found the share right (shareRight).
// Whop lists refunds only by when they were asked for, so the refunds read
// goes back to the oldest one still unsettled last time, which may yet
// change its payment. The read counts as done only once all of it is, so
// what failed is read again next time.
func (s *Server) whopReadPayments(ctx context.Context, c *whop.Client, st whopStore, shareRight bool) {
	now := s.now()
	var last, refundsFrom int64
	if err := s.db.QueryRowContext(ctx, `SELECT payments_read_at, refunds_from FROM whop_share_watch WHERE store_id = ?`, st.ID).Scan(&last, &refundsFrom); err != nil && !isNoRows(err) {
		s.log.Error("could not read when a store's payments were read", "store", st.ID, "err", err)
		return
	}
	hosting, err := s.whopSharesSet(ctx, st.ID)
	if err != nil {
		s.log.Error("could not read which of a store's products it hosts", "store", st.ID, "err", err)
		return
	}
	back := now.Add(-whopPaymentsBack)
	since := back
	if last > 0 {
		since = time.UnixMilli(last).Add(-whopPaymentsOverlap)
	}
	refundsSince := since
	if refundsFrom > 0 {
		refundsSince = time.UnixMilli(refundsFrom).Add(-whopPaymentsOverlap)
	}
	if refundsSince.Before(back) {
		refundsSince = back
	}
	pays, err := c.PaidSince(ctx, st.ID, since)
	if err != nil {
		s.log.Warn("could not read a store's payments", "store", st.ID, "err", err)
		return
	}
	refunds, err := c.RefundsSince(ctx, st.ID, refundsSince)
	if err != nil {
		s.log.Warn("could not read a store's refunds", "store", st.ID, "err", err)
		return
	}
	var unsettled time.Time
	refunded := map[string]bool{}
	for _, r := range refunds {
		if at := r.Created(); r.Unsettled() && !at.IsZero() && (unsettled.IsZero() || at.Before(unsettled)) {
			unsettled = at
		}
		if refunded[r.PaymentID] {
			continue
		}
		refunded[r.PaymentID] = true
		pay, err := c.Payment(ctx, r.PaymentID)
		if err != nil {
			s.log.Warn("could not read a refunded payment", "store", st.ID, "payment", r.PaymentID, "err", err)
			return
		}
		pays = append(pays, pay)
	}
	for _, pay := range pays {
		if _, ok := hosting[pay.ProductID]; !ok || s.whopPaymentKept(ctx, st.ID, pay) {
			continue
		}
		lines, err := c.PaymentFees(ctx, pay.ID)
		if err == nil {
			err = s.keepWhopFeeLines(ctx, st.ID, pay.ID, lines)
		}
		if err != nil {
			s.log.Warn("could not read a payment's fee lines", "store", st.ID, "payment", pay.ID, "err", err)
			return
		}
		s.keepCheckedPayment(ctx, st, pay, lines, "")
		s.noteWhopRenewal(ctx, st, pay, lines, shareRight)
	}
	from := int64(0)
	if !unsettled.IsZero() {
		from = unsettled.UnixMilli()
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO whop_share_watch(store_id, payments_read_at, refunds_from) VALUES(?, ?, ?)
		ON CONFLICT(store_id) DO UPDATE SET payments_read_at = excluded.payments_read_at, refunds_from = excluded.refunds_from`, st.ID, now.UnixMilli(), from); err != nil {
		s.log.Error("could not note a store's payments were read", "store", st.ID, "err", err)
	}
}

// noteWhopRenewal keeps what a payment the payments read found says of its
// membership's payment check. One refunded in full that was the latest the
// membership was paid with leaves it with none, so the payment check looks
// again at once for the latest it wasn't refunded (whopSharePaid), as the
// payment before; whatever the share check found, since a refund only takes
// away. Otherwise, while credit says the share check has just found the
// share right, one that carried Playkeeper's share for what the
// membership's plan allows has it paid for its plan, and is the latest it
// was paid with when it's newer than the one kept.
func (s *Server) noteWhopRenewal(ctx context.Context, st whopStore, pay whop.Payment, lines []whop.PaymentFee, credit bool) {
	if pay.MembershipID == "" {
		return
	}
	var err error
	switch {
	case whopRefundedInFull(pay):
		_, err = s.db.ExecContext(ctx, `UPDATE whop_membership_checks SET paid_at = 0, next_check_at = 0
			WHERE store_id = ? AND membership_id = ? AND paid_payment = ?`, st.ID, pay.MembershipID, pay.ID)
	case !credit:
		return
	default:
		var part planPart
		err = s.db.QueryRowContext(ctx, `SELECT p.plan_id, p.title, p.allowance_servers, p.allowance_memory_mb, p.disk_gb FROM whop_memberships m
			JOIN whop_plans p ON p.store_id = m.store_id AND p.plan_id = m.plan_id AND p.allowance_from != ''
			WHERE m.store_id = ? AND m.membership_id = ? AND m.stale = 0`, st.ID, pay.MembershipID).Scan(&part.id, &part.name, &part.servers, &part.memoryMB, &part.diskGB)
		if isNoRows(err) || err == nil && !whopSharePaidIn(lines, whopShareFor(part.memoryMB)) {
			return
		}
		at := pay.PaidTime()
		if at.IsZero() {
			at = s.now()
		}
		if err == nil {
			_, err = s.db.ExecContext(ctx, `INSERT INTO whop_membership_checks(store_id, membership_id, paid_plan_id, paid_title, paid_servers, paid_mb, paid_disk_gb,
				checked_at, answered, paid_at, paid_payment) VALUES(?,?,?,?,?,?,?,?,1,?,?)
				ON CONFLICT(store_id, membership_id) DO UPDATE SET paid_plan_id = excluded.paid_plan_id, paid_title = excluded.paid_title,
				paid_servers = excluded.paid_servers, paid_mb = excluded.paid_mb, paid_disk_gb = excluded.paid_disk_gb, problem = '', attempts = 0,
				next_check_at = 0, checked_at = excluded.checked_at, answered = 1,
				paid_payment = CASE WHEN excluded.paid_at >= paid_at THEN excluded.paid_payment ELSE paid_payment END, paid_at = MAX(paid_at, excluded.paid_at)`,
				st.ID, pay.MembershipID, part.id, part.name, part.servers, part.memoryMB, part.diskGB, s.now().UnixMilli(), at.UnixMilli(), pay.ID)
		}
	}
	if err != nil {
		s.log.Error("could not keep what a payment says of its membership", "store", st.ID, "payment", pay.ID, "err", err)
	}
}

// whopPaymentKept says whether a payment is kept for the store as Whop has
// it now: read before, with nothing refunded since. Its fee lines were kept
// with it (keepWhopFeeLines), so they aren't read again.
func (s *Server) whopPaymentKept(ctx context.Context, storeID string, pay whop.Payment) bool {
	if pay.Total == nil {
		return false
	}
	amount, err := pay.Total.Minor()
	if err != nil {
		return false
	}
	var refunded int64
	if pay.Refunded != nil {
		if refunded, err = pay.Refunded.Minor(); err != nil {
			return false
		}
	}
	var kept int64
	err = s.db.QueryRowContext(ctx, `SELECT refunded FROM whop_payments WHERE payment_id = ? AND store_id = ?`, pay.ID, storeID).Scan(&kept)
	return err == nil && kept == min(refunded, amount)
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

// whopWatches are the columns of whop_share_watch that say since when a
// store has been some way, 0 while it isn't.
var whopWatches = map[string]bool{"share_bad_since": true, "grant_gone_since": true, "share_unchecked_since": true}

// whopWatchSince is since when the store has been as column says (see
// whop_share_watch), from now if it wasn't.
func (s *Server) whopWatchSince(ctx context.Context, storeID, column string) (time.Time, error) {
	if !whopWatches[column] {
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
	if !whopWatches[column] {
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

// whopPaidPlan is what an app store's customer is hosted with: what their
// memberships that give servers allow together, each counted only as far
// as a payment of it carried Playkeeper's share (whopSharePaid), as
// whop_membership_checks keeps. A membership never found paid gives
// nothing; one moved to a plan with more memory gives what it was paid for
// until a payment carries the share for its new plan. One not paid for its
// plan is checked while the store is open and its share was found right
// recently (whopShareFresh), at once and then on the usual retry schedule,
// without holding up the rest of the customer, so a paid membership that
// ends pauses them even while another waits. A payment counts only as long
// as whopPaidFor says, grace being the renewal grace: past it, what the
// membership was paid for lapses, and the check looks for a newer payment.
// What waits is waiting, "" when nothing does, and unsure is whether a
// membership that gives nothing never had Whop's answer, paid or not
// (whopNotPaid), or had the payment it was paid with refunded and no check
// since, so what it would give isn't known yet.
func (s *Server) whopPaidPlan(ctx context.Context, c *whop.Client, st whopStore, wc whopCustomer, grace time.Duration) (plan CustomerPlan, waiting string, unsure bool) {
	now := s.now()
	since := now.Add(-whopPaidFor(wc, grace))
	var parts []planPart
	var waits []string
	for _, h := range wc.Hosting {
		// A refund of the payment it was paid with leaves it paid at no
		// time, until the check finds the payment before or refuses it.
		refunded := h.Paid.memoryMB > 0 && h.PaidAt.IsZero()
		switch h = h.lapsed(since); {
		case refunded:
			h.Problem = "its payment on Whop (" + h.PaidPayment + ") was refunded, so its server waits for the payment before it to be checked"
		case h.Lapsed:
			h.Problem = "no payment on Whop has carried Playkeeper's share for it since " + h.PaidAt.UTC().Format("2 January") + ", so its server waits"
		}
		if h.checkDue(now.UnixMilli()) && st.ClosedWhy == "" && s.whopShareRecent(ctx, st.ID) {
			pay, err := s.whopSharePaid(ctx, c, st, wc.WhopUserID, h.ID, h.Part.memoryMB, since)
			var np *whopNotPaid
			switch {
			case err == nil:
				at := pay.PaidTime()
				if at.IsZero() {
					at = now
				}
				h.Paid, h.Problem, h.Answered, h.PaidAt, h.PaidPayment = h.Part, "", true, at, pay.ID
				refunded = false
			case errors.As(err, &np):
				h.Problem, h.Answered, h.Refused = err.Error(), true, true
				refunded = false
			default:
				h.Problem = err.Error()
				var we *whop.Error
				if errors.As(err, &we) {
					h.Problem = whopProblem(err)
				}
			}
			s.keepWhopMembershipCheck(st.ID, h)
		}
		if h.paidFor() {
			parts = append(parts, h.Part)
			continue
		}
		if h.Paid.memoryMB > 0 {
			parts = append(parts, h.Paid)
		} else if !h.Answered || refunded {
			unsure = true
		}
		if h.Problem != "" {
			waits = append(waits, h.Problem)
		}
	}
	return customerPlan(parts), strings.Join(waits, "; "), unsure
}

// keepWhopMembershipCheck keeps what the payment check found for a
// membership: what it gives as paid for, with the payment it was paid with,
// or why it isn't and when to look again. One whose payment lapsed and the
// check refused gives nothing until a newer payment carries the share.
func (s *Server) keepWhopMembershipCheck(store string, h whopHosting) {
	now := s.now()
	var err error
	if h.paidFor() {
		_, err = s.db.Exec(`INSERT INTO whop_membership_checks(store_id, membership_id, paid_plan_id, paid_title, paid_servers, paid_mb, paid_disk_gb, checked_at, answered,
			paid_at, paid_payment) VALUES(?,?,?,?,?,?,?,?,1,?,?)
			ON CONFLICT(store_id, membership_id) DO UPDATE SET paid_plan_id = excluded.paid_plan_id, paid_title = excluded.paid_title, paid_servers = excluded.paid_servers,
			paid_mb = excluded.paid_mb, paid_disk_gb = excluded.paid_disk_gb, problem = '', attempts = 0, next_check_at = 0, checked_at = excluded.checked_at, answered = 1,
			paid_at = excluded.paid_at, paid_payment = excluded.paid_payment`,
			store, h.ID, h.Paid.id, h.Paid.name, h.Paid.servers, h.Paid.memoryMB, h.Paid.diskGB, now.UnixMilli(), h.PaidAt.UnixMilli(), h.PaidPayment)
	} else {
		_, err = s.db.Exec(`INSERT INTO whop_membership_checks(store_id, membership_id, problem, attempts, next_check_at, checked_at, answered) VALUES(?,?,?,1,?,?,?)
			ON CONFLICT(store_id, membership_id) DO UPDATE SET problem = excluded.problem, attempts = attempts + 1, next_check_at = excluded.next_check_at, checked_at = excluded.checked_at,
			answered = MAX(answered, excluded.answered), paid_mb = CASE WHEN ? THEN 0 ELSE paid_mb END`,
			store, h.ID, h.Problem, now.Add(whopBackoff(h.Attempts)).UnixMilli(), now.UnixMilli(), h.Answered, h.Refused && h.Lapsed)
	}
	if err != nil {
		s.log.Error("could not keep what the payment check found", "store", store, "membership", h.ID, "err", err)
	}
}

// noteWhopPaymentProblem shows why some of an app store's customer's
// memberships don't give servers yet (whopPaidPlan), and clears it once
// none waits, unless a call for them failed since: that problem stays
// until their next try.
func (s *Server) noteWhopPaymentProblem(store, whopUserID, problem string) {
	var err error
	if problem == "" {
		_, err = s.db.Exec(`UPDATE whop_customers SET problem = '' WHERE store_id = ? AND whop_user_id = ? AND attempts = 0 AND problem != ''`, store, whopUserID)
	} else {
		_, err = s.db.Exec(`INSERT INTO whop_customers(store_id, whop_user_id, problem, updated_at) VALUES(?,?,?,?)
			ON CONFLICT(store_id, whop_user_id) DO UPDATE SET problem = excluded.problem WHERE whop_customers.attempts = 0 AND whop_customers.problem != excluded.problem`,
			store, whopUserID, problem, s.now().UnixMilli())
	}
	if err != nil {
		s.log.Error("could not note why a customer's membership waits", "store", store, "customer", whopUserID, "err", err)
	}
}
