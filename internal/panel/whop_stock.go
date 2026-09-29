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
	SalePlans(ctx context.Context) ([]CustomerPlan, error)
	SetAvailability(ctx context.Context, left map[string]int) error
}

// maxWhopStock bounds a plan's stock, far above what machines can take.
const maxWhopStock = 100_000

// whopStock is saleStock for a store on Whop.
type whopStock struct{ s *Server }

// SalePlans lists the plans with an allowance that aren't archived, hidden
// ones too, since they sell through their own links.
func (ws whopStock) SalePlans(ctx context.Context) ([]CustomerPlan, error) {
	rows, err := ws.s.db.QueryContext(ctx, `SELECT plan_id, title, allowance_servers, allowance_memory_mb, disk_gb FROM whop_plans
		WHERE allowance_from != '' AND visibility != 'archived' ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CustomerPlan
	for rows.Next() {
		var p CustomerPlan
		var title string
		if err := rows.Scan(&p.ID, &title, &p.Servers, &p.MemoryMB, &p.DiskGB); err != nil {
			return nil, err
		}
		p.Name = cmpOr(title, p.ID)
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetAvailability keeps the fleet's numbers for the plans the store sells,
// ignoring any other, and has the reconciler write them to Whop now.
func (ws whopStock) SetAvailability(ctx context.Context, left map[string]int) error {
	now := ws.s.now().UnixMilli()
	err := ws.s.immediate(ctx, func(c *sql.Conn) error {
		for id, n := range left {
			if _, err := c.ExecContext(ctx, `INSERT INTO whop_stock(plan_id, want, set_at)
				SELECT plan_id, ?, ? FROM whop_plans WHERE plan_id = ? AND allowance_from != '' AND visibility != 'archived'
				ON CONFLICT(plan_id) DO UPDATE SET want = excluded.want, set_at = excluded.set_at`, min(max(n, 0), maxWhopStock), now, id); err != nil {
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
// uncountedPurchases), where that differs from the stock the dashboard last
// saw on Whop.
//
// A write replaces what Whop has, and Whop takes one off for each purchase,
// including ones the dashboard hasn't heard of yet, while what it last saw
// may be minutes old. So before writing, it reads the plan's memberships
// from Whop again: the target then counts every purchase Whop took off, and
// is right whatever Whop's stock says. It's written even when it matches
// what the dashboard last saw, which purchases may have lowered since. A
// stock changed by hand on Whop is put back once the dashboard reads the
// store again.
func (s *Server) pushWhopStock(ctx context.Context, c *whop.Client, accountID string) {
	type plan struct {
		id          string
		want, stock int
		setAt       int64
		unlimited   bool
	}
	rows, err := s.db.QueryContext(ctx, `SELECT k.plan_id, k.want, k.set_at, p.stock, p.unlimited_stock FROM whop_stock k
		JOIN whop_plans p ON p.plan_id = k.plan_id WHERE p.allowance_from != '' AND p.visibility != 'archived' ORDER BY p.position`)
	if err != nil {
		s.log.Error("could not read the plans' stock", "err", err)
		return
	}
	var plans []plan
	setAt := map[string]int64{}
	for rows.Next() {
		var p plan
		if err := rows.Scan(&p.id, &p.want, &p.setAt, &p.stock, &p.unlimited); err != nil {
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
	uncounted, err := s.uncountedPurchases(ctx, setAt)
	if err != nil {
		s.log.Error("could not count the purchases the fleet's numbers don't", "err", err)
		return
	}
	for _, p := range plans {
		if !p.unlimited && p.stock == max(p.want-uncounted[p.id], 0) {
			continue
		}
		target, err := s.whopStockTarget(ctx, c, accountID, p.id, p.want, p.setAt)
		if err != nil {
			s.log.Warn("could not read a plan's purchases from Whop", "plan", p.id, "err", err)
			continue
		}
		if err := c.SetPlanStock(ctx, p.id, target); err != nil {
			s.log.Warn("could not set a plan's stock on Whop", "plan", p.id, "err", err)
			continue
		}
		if _, err := s.db.Exec(`UPDATE whop_plans SET stock = ?, unlimited_stock = 0 WHERE plan_id = ?`, target, p.id); err != nil {
			s.log.Error("could not record a plan's stock", "err", err)
		}
	}
}

// whopStockTarget is the stock a plan should have: the fleet's number, want,
// said at setAt, less the purchases it doesn't count, with every membership
// of the plan read from Whop first.
func (s *Server) whopStockTarget(ctx context.Context, c *whop.Client, accountID, planID string, want int, setAt int64) (int, error) {
	ms, err := c.PlanMemberships(ctx, accountID, planID)
	if err != nil {
		return 0, err
	}
	for _, m := range ms {
		if err := s.keepMembership(m, false); err != nil {
			return 0, err
		}
	}
	uncounted, err := s.uncountedPurchases(ctx, map[string]int64{planID: setAt})
	if err != nil {
		return 0, err
	}
	return max(want-uncounted[planID], 0), nil
}

// uncountedPurchases counts, for each plan in setAt, the purchases with
// access that the fleet's number for it, said at setAt, doesn't count yet:
// those whose customer the core wasn't given the plan for, as while their
// start keeps failing, or was given it only since. A paused customer's
// purchases count as not yet counted, since their room may be gone.
func (s *Server) uncountedPurchases(ctx context.Context, setAt map[string]int64) (map[string]int, error) {
	type given struct {
		plans map[string]int
		at    int64
	}
	custs := map[string]given{}
	rows, err := s.db.QueryContext(ctx, `SELECT whop_user_id, applied, applied_at FROM whop_customers WHERE paused = 0 AND applied != ''`)
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
	rows, err = s.db.QueryContext(ctx, `SELECT whop_user_id, plan_id FROM whop_memberships WHERE status IN `+whopAccess)
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
