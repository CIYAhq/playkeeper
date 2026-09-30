package panel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/whop"
)

// Stores (the hosted blueprint's 1.1): one dashboard sells for many Whop
// businesses. Each is a store, whose id is its business's. The dashboard
// reaches the key store, of which there's at most one, with the API key
// its owner pasted in Settings › Sell on Whop, and each app store, a
// business that installed the Playkeeper Cloud app, with the app's key.
// Every store's plans, memberships, customers, messages and stock are its
// own: each query of them names its store, and nothing moves a plan or a
// membership to another store. The core's customers are per store too
// (Customer.Store), so someone who buys from two stores has two accounts.

// How the dashboard reaches a store.
const (
	whopViaKey = "key"
	whopViaApp = "app"
)

// errNoWhopApp is an app store's problem while the dashboard has no key
// for the Playkeeper Cloud app.
var errNoWhopApp = errors.New("the Playkeeper Cloud app's key isn't set, so the stores that installed it wait")

// whopStore is a store the dashboard sells for.
type whopStore struct {
	whop.Account
	// Via is how the dashboard reaches the store (whopViaKey, with Key, or
	// whopViaApp).
	Via         string
	Key         string
	ConnectedBy string
	ConnectedAt time.Time
	SyncedAt    time.Time
	Problem     string
	// WebhookID is the key store's webhook for membership events, at
	// WebhookURL, signed with WebhookSecret. App stores have none of their
	// own: the app's webhook tells of theirs.
	WebhookID, WebhookURL, WebhookSecret string
	// PolledAt is when the dashboard last read every membership.
	PolledAt time.Time
	// MarkedAs is the address the dashboard last marked the store's
	// products with, and TakenOverBy the dashboard that took the store
	// over, at TakenOverAt.
	MarkedAs, TakenOverBy string
	TakenOverAt           time.Time
	// SuspendedAt is when the owner suspended the store, zero while it
	// isn't, and SuspendReason why (see suspension.go).
	SuspendedAt   time.Time
	SuspendReason string
	// LeftAt is when the store left, zero while it hasn't, and LeftWhy why
	// (see leaving.go).
	LeftAt  time.Time
	LeftWhy string
}

const whopStoreColumns = `store_id, via, title, route, api_key, connected_by, connected_at, synced_at, problem, webhook_id, webhook_url, webhook_secret,
	polled_at, marked_as, taken_over_by, taken_over_at, suspended_at, suspend_reason, left_at, left_why`

func scanWhopStore(row interface{ Scan(...any) error }) (whopStore, error) {
	var st whopStore
	var connected, synced, polled, takenOver, suspended, left int64
	err := row.Scan(&st.ID, &st.Via, &st.Title, &st.Route, &st.Key, &st.ConnectedBy, &connected, &synced, &st.Problem,
		&st.WebhookID, &st.WebhookURL, &st.WebhookSecret, &polled, &st.MarkedAs, &st.TakenOverBy, &takenOver, &suspended, &st.SuspendReason, &left, &st.LeftWhy)
	st.ConnectedAt, st.SyncedAt, st.PolledAt = time.UnixMilli(connected).UTC(), msTimeOrZero(synced), msTimeOrZero(polled)
	st.TakenOverAt, st.SuspendedAt, st.LeftAt = msTimeOrZero(takenOver), msTimeOrZero(suspended), msTimeOrZero(left)
	return st, err
}

// whopStores lists the stores, the key store first.
func (s *Server) whopStores(ctx context.Context) ([]whopStore, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+whopStoreColumns+` FROM whop_stores ORDER BY via != 'key', connected_at, store_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []whopStore
	for rows.Next() {
		st, err := scanWhopStore(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// whopStoreByID is the store with the business id, or ok false.
func (s *Server) whopStoreByID(ctx context.Context, id string) (whopStore, bool, error) {
	st, err := scanWhopStore(s.db.QueryRowContext(ctx, `SELECT `+whopStoreColumns+` FROM whop_stores WHERE store_id = ?`, id))
	if isNoRows(err) {
		return whopStore{}, false, nil
	}
	return st, err == nil, err
}

// keyStore is the store Settings › Sell on Whop manages, or ok false.
func (s *Server) keyStore(ctx context.Context) (whopStore, bool, error) {
	st, err := scanWhopStore(s.db.QueryRowContext(ctx, `SELECT `+whopStoreColumns+` FROM whop_stores WHERE via = ?`, whopViaKey))
	if isNoRows(err) {
		return whopStore{}, false, nil
	}
	return st, err == nil, err
}

// addWhopStore registers a business that installed the Playkeeper Cloud app
// as an app store, which the reconciler then sells for, and says whether it
// was new, or an app store that left and is back (see leaving.go). A
// business that's a store already stays as it is.
func (s *Server) addWhopStore(ctx context.Context, a whop.Account) (bool, error) {
	if !reWhopID.MatchString(a.ID) {
		return false, fmt.Errorf("%q isn't a Whop business", a.ID)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO whop_stores(store_id, via, title, route, connected_at) VALUES(?,?,?,?,?) ON CONFLICT(store_id) DO NOTHING`,
		a.ID, whopViaApp, a.Title, a.Route, s.now().UnixMilli())
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.audit("system", "whop.store_added", a.ID, "succeeded", "installed the Playkeeper Cloud app: "+whopName(a))
		s.kickWhopStore(a.ID)
		return true, nil
	}
	return s.bringBackWhopStore(ctx, a)
}

// whopClientFor is the client a store's pass acts on it with, naming the
// store's id as the account in every call: its own key for the key store,
// and the Playkeeper Cloud app's key for an app store.
func (s *Server) whopClientFor(ctx context.Context, st whopStore) (*whop.Client, error) {
	switch st.Via {
	case whopViaKey:
		return s.whopClient(st.Key)
	case whopViaApp:
		var key string
		if err := s.db.QueryRowContext(ctx, `SELECT api_key FROM whop_app WHERE id = 1`).Scan(&key); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if key == "" {
			return nil, errNoWhopApp
		}
		return s.whopClient(key)
	}
	return nil, fmt.Errorf("the store %s is reached by %q, which isn't a way this dashboard knows", st.ID, st.Via)
}

// whopStoreReads are the grants an app store's pass reads its plans and
// memberships with.
var whopStoreReads = []string{"plan:basic:read", "member:basic:read"}

// whopGrantProblem is why the app store's plans and memberships as Whop
// lists them can't be trusted, "" when they can. Whop answers an app a
// business revoked or never granted with empty lists rather than a
// refusal, which would read as every plan ending, so the grant is checked
// before anything is read.
func whopGrantProblem(ctx context.Context, c *whop.Client, st whopStore) string {
	missing, err := c.Missing(ctx, st.ID)
	if err != nil {
		return "Playkeeper couldn't check the Playkeeper Cloud app's grant on this store, so nothing changed here: " + whopProblem(err)
	}
	var lost []string
	for _, a := range missing {
		if slices.Contains(whopStoreReads, a) {
			lost = append(lost, a)
		}
	}
	if len(lost) > 0 {
		return "The Playkeeper Cloud app's grant on this store lacks " + strings.Join(lost, ", ") + ", so nothing changes here until its seller approves the app again."
	}
	return ""
}

// whopOwner is who owns the store on Whop, as whom its messages go out:
// the owner of the key's own business for the key store, and the owner of
// the business the app's key reaches by id for an app store.
func whopOwner(ctx context.Context, c *whop.Client, st whopStore) (whop.User, error) {
	if st.Via == whopViaApp {
		return c.OwnerOf(ctx, st.ID)
	}
	return c.Owner(ctx)
}
