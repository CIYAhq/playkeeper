package panel

import (
	"context"
	"database/sql"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/whop"
)

// Selling only what fits: the fleet tells the billing side how many more of
// each plan the machines can take, and the reconciler makes that each plan's
// stock on Whop, which Whop enforces at checkout. Plans stay as visible or
// hidden as the owner set them, and a plan the fleet hasn't spoken for keeps
// whatever stock it has on Whop.

// saleStock is the billing side's half of selling only what fits.
// SalePlans are the plans the store sells. SetAvailability takes how many
// more of each plan the machines can take now: numbers any mix of which fits
// at once, since two plans can sell between calls. Plans it leaves out stay
// as they are. The fleet calls it after every change in room and every few
// minutes. Neither waits on the billing provider.
type saleStock interface {
	SalePlans(ctx context.Context) ([]SalePlan, error)
	SetAvailability(ctx context.Context, left map[string]int) error
}

// SalePlan is a plan a store sells: Store is the store's id. Free is one
// that charges nothing, such as the hidden creator plan; the fleet gives
// room to paid plans first.
type SalePlan struct {
	CustomerPlan
	Store string
	Free  bool
}

// maxWhopStock bounds a plan's stock, far above what machines can take.
const maxWhopStock = 100_000

// whopStock is saleStock for a store on Whop.
type whopStock struct{ s *Server }

// SalePlans lists every store's plans with an allowance that aren't
// archived, hidden ones too, since they sell through their own links. A
// store another dashboard took over is that dashboard's to sell, on its
// own machines, and a store that's suspended, left or closed sells
// nothing, so their plans take none of this one's room.
func (ws whopStock) SalePlans(ctx context.Context) ([]SalePlan, error) {
	rows, err := ws.s.db.QueryContext(ctx, `SELECT p.store_id, p.plan_id, p.title, p.allowance_servers, p.allowance_memory_mb, p.disk_gb, p.free FROM whop_plans p
		JOIN whop_stores st ON st.store_id = p.store_id
		WHERE p.allowance_from != '' AND p.visibility != 'archived' AND st.taken_over_by = '' AND st.suspended_at = 0 AND st.left_at = 0
		AND NOT EXISTS (SELECT 1 FROM whop_store_closures c WHERE c.store_id = st.store_id) ORDER BY p.store_id, p.position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SalePlan
	for rows.Next() {
		var p SalePlan
		var title string
		if err := rows.Scan(&p.Store, &p.ID, &title, &p.Servers, &p.MemoryMB, &p.DiskGB, &p.Free); err != nil {
			return nil, err
		}
		p.Name = cmpOr(title, p.ID)
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetAvailability keeps the fleet's numbers for the plans the stores sell,
// each for its store, ignoring any other, and has the reconciler write them
// to Whop now.
func (ws whopStock) SetAvailability(ctx context.Context, left map[string]int) error {
	now := ws.s.now().UnixMilli()
	err := ws.s.immediate(ctx, func(c *sql.Conn) error {
		for id, n := range left {
			if _, err := c.ExecContext(ctx, `INSERT INTO whop_stock(store_id, plan_id, want, set_at)
				SELECT store_id, plan_id, ?, ? FROM whop_plans WHERE plan_id = ? AND allowance_from != '' AND visibility != 'archived'
				ON CONFLICT(plan_id) DO UPDATE SET want = excluded.want, set_at = excluded.set_at WHERE whop_stock.store_id = excluded.store_id`,
				min(max(n, 0), maxWhopStock), now, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	ws.s.kickWhop()
	return nil
}

// pushWhopStock sets the stock of each plan the fleet spoke for to its
// number less the purchases that number doesn't count yet (see
// uncountedPurchases).
//
// Whop takes one off a plan's stock for each purchase, including ones the
// dashboard hasn't heard of yet, and a write replaces what Whop has. So
// before writing, it reads the plan's memberships from Whop again: the
// target then counts every purchase Whop took off, and is right whatever
// Whop's stock says. There's nothing to write while the dashboard's own
// count still gives the stock it last set, the store as last read shows
// that stock, and no membership of the plan came or went since: Whop's
// stock is then the one set less the purchases not heard of yet, and so is
// the target. A stock changed by hand on Whop is put back once the
// dashboard reads the store again.
func (s *Server) pushWhopStock(ctx context.Context, c *whop.Client, storeID string) {
	type plan struct {
		id                                    string
		want, stock, written, known, knownNow int
		setAt                                 int64
		unlimited                             bool
	}
	rows, err := s.db.QueryContext(ctx, `SELECT k.plan_id, k.want, k.set_at, k.written, k.known, p.stock, p.unlimited_stock,
		(SELECT COUNT(*) FROM whop_memberships m WHERE m.store_id = k.store_id AND m.plan_id = k.plan_id) FROM whop_stock k
		JOIN whop_plans p ON p.store_id = k.store_id AND p.plan_id = k.plan_id
		WHERE k.store_id = ? AND p.allowance_from != '' AND p.visibility != 'archived' ORDER BY p.position`, storeID)
	if err != nil {
		s.log.Error("could not read the plans' stock", "err", err)
		return
	}
	var plans []plan
	setAt := map[string]int64{}
	for rows.Next() {
		var p plan
		if err := rows.Scan(&p.id, &p.want, &p.setAt, &p.written, &p.known, &p.stock, &p.unlimited, &p.knownNow); err != nil {
			rows.Close()
			s.log.Error("could not read the plans' stock", "err", err)
			return
		}
		plans = append(plans, p)
		setAt[p.id] = p.setAt
	}
	rows.Close()
	if len(plans) == 0 {
		return
	}
	uncounted, err := s.uncountedPurchases(ctx, storeID, setAt)
	if err != nil {
		s.log.Error("could not count the purchases the fleet's numbers don't", "err", err)
		return
	}
	for _, p := range plans {
		if !p.unlimited && p.stock == p.written && p.known == p.knownNow && p.stock == max(p.want-uncounted[p.id], 0) {
			continue
		}
		target, known, err := s.whopStockTarget(ctx, c, storeID, p.id, p.want, p.setAt)
		if err != nil {
			s.log.Warn("could not read a plan's purchases from Whop", "plan", p.id, "err", err)
			continue
		}
		if err := c.SetPlanStock(ctx, p.id, target); err != nil {
			s.log.Warn("could not set a plan's stock on Whop", "plan", p.id, "err", err)
			continue
		}
		if _, err := s.db.Exec(`UPDATE whop_plans SET stock = ?, unlimited_stock = 0 WHERE store_id = ? AND plan_id = ?`, target, storeID, p.id); err != nil {
			s.log.Error("could not record a plan's stock", "err", err)
		}
		if _, err := s.db.Exec(`UPDATE whop_stock SET written = ?, known = ? WHERE store_id = ? AND plan_id = ?`, target, known, storeID, p.id); err != nil {
			s.log.Error("could not record a plan's stock", "err", err)
		}
	}
}

// whopStockTarget is the stock a plan of the store should have: the fleet's
// number, want, said at setAt, less the purchases it doesn't count, with
// every membership of the plan read from Whop first. known is how many of
// the plan's memberships the dashboard knew of just before, so the target
// takes each of them into account.
func (s *Server) whopStockTarget(ctx context.Context, c *whop.Client, storeID, planID string, want int, setAt int64) (target, known int, err error) {
	ms, err := c.PlanMemberships(ctx, storeID, planID)
	if err != nil {
		return 0, 0, err
	}
	for _, m := range ms {
		if err := s.keepMembership(storeID, m, false); err != nil {
			return 0, 0, err
		}
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM whop_memberships WHERE store_id = ? AND plan_id = ?`, storeID, planID).Scan(&known); err != nil {
		return 0, 0, err
	}
	uncounted, err := s.uncountedPurchases(ctx, storeID, map[string]int64{planID: setAt})
	if err != nil {
		return 0, 0, err
	}
	return max(want-uncounted[planID], 0), known, nil
}

// uncountedPurchases counts, for each of the store's plans in setAt, the
// purchases with access that the fleet's number for it, said at setAt,
// doesn't count yet: those whose customer the core wasn't given the plan
// for, as while their start keeps failing, or was given it only since. A
// paused customer's purchases count as not yet counted, since their room
// may be gone.
func (s *Server) uncountedPurchases(ctx context.Context, storeID string, setAt map[string]int64) (map[string]int, error) {
	type given struct {
		plans map[string]int
		at    int64
	}
	custs := map[string]given{}
	rows, err := s.db.QueryContext(ctx, `SELECT whop_user_id, applied, applied_at FROM whop_customers WHERE store_id = ? AND paused = 0 AND applied != ''`, storeID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, applied string
		var at int64
		if err := rows.Scan(&id, &applied, &at); err != nil {
			rows.Close()
			return nil, err
		}
		g := given{plans: map[string]int{}, at: at}
		ids, _, _ := strings.Cut(applied, "|")
		for _, p := range strings.Split(ids, "+") {
			g.plans[p]++
		}
		custs[id] = g
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT whop_user_id, plan_id FROM whop_memberships WHERE store_id = ? AND status IN `+whopAccess, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var user, plan string
		if err := rows.Scan(&user, &plan); err != nil {
			return nil, err
		}
		at, ok := setAt[plan]
		if !ok {
			continue
		}
		if g, ok := custs[user]; ok && g.plans[plan] > 0 && g.at <= at {
			g.plans[plan]--
			continue
		}
		out[plan]++
	}
	return out, rows.Err()
}
