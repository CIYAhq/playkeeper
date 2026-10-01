package panel

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/whop"
)

// Closing an app store (for the hosted blueprint's 2.1 to 2.3). A closed
// store sells nothing: the fleet keeps no room for its plans, each plan's
// stock is 0 on Whop, its hosting products are hidden from its page there
// (listWhopProducts), and nobody starts, neither a new buyer nor a customer
// coming back after their plan ended. Its customers go on, and their plans
// still change and end. Each party closes a store for its own reason and
// opens it for that reason alone, so the store sells once no reason holds
// it closed. A new app store, and one back after leaving, are closed as not
// open yet until 2.3's Open the store, which follows 2.2 setting
// Playkeeper's share; 2.2's share check closes a store whose share is gone
// or short for a reason of its own.

// whopNotOpenYet is the reason a new app store, or one back after leaving,
// is closed until its seller opens it, and whopNotOpenYetWhy its words.
const (
	whopNotOpenYet    = "not-open-yet"
	whopNotOpenYetWhy = "Not open yet"
)

// maxClosedWhy bounds the words of a reason a store is closed for.
const maxClosedWhy = 200

// reClosedBy is a reason's short name, such as "not-open-yet" or "share".
var reClosedBy = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// closeWhopStore closes the app store storeID for the reason by, with why
// as the words the owner and the seller see. Closed for by already, only
// its words change.
func (s *Server) closeWhopStore(ctx context.Context, storeID, by, why string) error {
	why = strings.TrimSpace(why)
	if len(why) > maxClosedWhy {
		why = strings.ToValidUTF8(why[:maxClosedWhy], "")
	}
	if !reClosedBy.MatchString(by) || why == "" {
		return fmt.Errorf("a store is closed for a reason, with words for it: %q, %q", by, why)
	}
	if err := s.appStore(ctx, storeID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO whop_store_closures(store_id, closed_by, why, closed_at) VALUES(?,?,?,?)
		ON CONFLICT(store_id, closed_by) DO UPDATE SET why = excluded.why WHERE whop_store_closures.why != excluded.why`, storeID, by, why, s.now().UnixMilli())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.audit("system", "whop.store_close", storeID, "succeeded", by+": "+why)
		s.kickSaleRoom()
		s.kickWhopStore(storeID)
	}
	return nil
}

// openWhopStore takes back the reason by for the app store storeID being
// closed, and says whether it's open now, with no other reason holding it.
func (s *Server) openWhopStore(ctx context.Context, storeID, by string) (bool, error) {
	if err := s.appStore(ctx, storeID); err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM whop_store_closures WHERE store_id = ? AND closed_by = ?`, storeID, by)
	if err != nil {
		return false, err
	}
	var left int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM whop_store_closures WHERE store_id = ?`, storeID).Scan(&left); err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		detail := by + " no longer holds it closed"
		if left == 0 {
			detail += "; it's open"
		}
		s.audit("system", "whop.store_open", storeID, "succeeded", detail)
		s.kickSaleRoom()
		s.kickWhopStore(storeID)
	}
	return left == 0, nil
}

// listWhopProducts keeps an app store's hosting products on its page on
// Whop, whop.com/<the store>, only while it sells, as its plans' stock is:
// while it sells nothing, closed, suspended or gone, each one shown there is
// hidden (unlistWhopProducts), and once it sells again, those are shown, and
// no other (relistWhopProducts), so a product its seller hid stays hidden.
// Open the store shows every one (showHostedProducts). What Whop refuses is
// tried again on the next pass.
func (s *Server) listWhopProducts(ctx context.Context, c *whop.Client, st whopStore, sells bool) {
	if st.Via != whopViaApp {
		return
	}
	list := s.unlistWhopProducts
	if sells {
		list = s.relistWhopProducts
	}
	if err := list(ctx, c, st.ID); err != nil {
		s.log.Warn("could not keep a store's hosting products on its page on Whop as it sells", "store", st.ID, "sells", sells, "err", err)
	}
}

// unlistWhopProducts hides from the store's page on Whop each of its hosting
// products shown there, those with a hosting plan that isn't archived, and
// keeps each one it looked at with whether it hid it. Only a hosting product
// it hasn't looked at since the store last sold has Whop read, so a store
// that stays closed costs no call, and one added meanwhile is hidden too.
func (s *Server) unlistWhopProducts(ctx context.Context, c *whop.Client, storeID string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT p.product_id FROM whop_plans p
		WHERE p.store_id = ? AND p.allowance_from != '' AND p.visibility != 'archived' AND p.product_id != ''
		AND NOT EXISTS (SELECT 1 FROM whop_unlisted_products u WHERE u.store_id = p.store_id AND u.product_id = p.product_id)`, storeID)
	if err != nil {
		return err
	}
	var look []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		look = append(look, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(look) == 0 {
		return err
	}
	products, err := c.Products(ctx, storeID)
	if err != nil {
		return err
	}
	shown := map[string]bool{}
	for _, p := range products {
		shown[p.ID] = p.Visibility == "visible"
	}
	slices.Sort(look)
	for _, id := range look {
		if shown[id] {
			if err := c.HideProduct(ctx, id); err != nil {
				return err
			}
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO whop_unlisted_products(store_id, product_id, hid) VALUES(?, ?, ?)`, storeID, id, shown[id]); err != nil {
			return err
		}
	}
	return nil
}

// relistWhopProducts shows again on the store's page on Whop each product
// the store's pass hid while it sold nothing (unlistWhopProducts), and
// forgets what it looked at. One that's no longer hidden there, archived
// or shown since, is left as it is.
func (s *Server) relistWhopProducts(ctx context.Context, c *whop.Client, storeID string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT product_id, hid FROM whop_unlisted_products WHERE store_id = ?`, storeID)
	if err != nil {
		return err
	}
	looked, hid := 0, map[string]bool{}
	for rows.Next() {
		var id string
		var h bool
		if err := rows.Scan(&id, &h); err != nil {
			rows.Close()
			return err
		}
		looked++
		if h {
			hid[id] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || looked == 0 {
		return err
	}
	if len(hid) > 0 {
		products, err := c.Products(ctx, storeID)
		if err != nil {
			return err
		}
		for _, p := range products {
			if !hid[p.ID] || p.Visibility != "hidden" {
				continue
			}
			if err := c.ShowProduct(ctx, p.ID); err != nil {
				return err
			}
		}
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM whop_unlisted_products WHERE store_id = ?`, storeID)
	return err
}

// appStore refuses a store that isn't an app store of this dashboard.
func (s *Server) appStore(ctx context.Context, storeID string) error {
	st, ok, err := s.whopStoreByID(ctx, storeID)
	switch {
	case err != nil:
		return err
	case !ok:
		return fmt.Errorf("this dashboard doesn't sell for %s", storeID)
	case st.Via != whopViaApp:
		return fmt.Errorf("the dashboard's own store isn't opened or closed: %s", storeID)
	}
	return nil
}
