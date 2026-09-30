package panel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/version"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// Sell on Whop sells this machine's servers through a store on Whop: Whop
// takes the money and runs the buyers' memberships, and each plan the store
// sells lets a buyer create servers inside an allowance, as a creator does
// (see creators.go). Settings › Sell on Whop keeps the seller's Whop
// account and its API key, reads the store's plans with what each allows,
// and marks the store's products with the dashboard's address, so the store
// takes orders only once a dashboard sells for it. Only the owner may,
// since a key that runs a store and buyers who create servers on the
// machine are theirs to decide.
//
// One dashboard sells for a business, so each purchase starts its customer
// once. Another dashboard is refused a store this one's address marks
// unless its owner takes the store over, and this one then stops: it makes
// no more of the core's calls and sends no messages until its owner takes
// the store back (see markWhopProducts).
const actSellOnWhop action = "whop.manage"

// whopTimeout bounds one exchange with Whop, which takes a few requests.
const whopTimeout = 30 * time.Second

// whopView is Settings › Sell on Whop.
type whopView struct {
	Connected bool `json:"connected"`
	// Dashboard is where Whop and buyers reach this dashboard: its address
	// with the panel's port, "" until the machine has an address with a
	// certificate. The store takes orders only once it has one.
	Dashboard   string        `json:"dashboard"`
	Account     *whop.Account `json:"account,omitempty"`
	KeyEnding   string        `json:"keyEnding,omitempty"`
	ConnectedBy string        `json:"connectedBy,omitempty"`
	ConnectedAt *time.Time    `json:"connectedAt,omitempty"`
	SyncedAt    *time.Time    `json:"syncedAt,omitempty"`
	// Problem is what went wrong the last time the dashboard talked to Whop.
	Problem string `json:"problem,omitempty"`
	// TakenOverBy is the dashboard that sells for the store instead of this
	// one. TakenOverAt is when it took the store over from this one; it's
	// absent while this dashboard's own takeover isn't done.
	TakenOverBy string         `json:"takenOverBy,omitempty"`
	TakenOverAt *time.Time     `json:"takenOverAt,omitempty"`
	Plans       []whopPlanView `json:"plans"`
	// Webhook says whether Whop tells the dashboard about memberships as
	// they change; without it, the dashboard still reads them every few
	// minutes.
	Webhook   bool               `json:"webhook"`
	Customers []whopCustomerView `json:"customers"`
	// SignIn is Sign in with Whop's setup, once a store is connected.
	SignIn *whopSignInView `json:"signIn,omitempty"`
	// Needs are the permissions the key needs, for the steps to make one.
	Needs []string `json:"needs"`
	// Notice, on the answer to a disconnect alone, is what the owner still
	// has to do on Whop.
	Notice string `json:"notice,omitempty"`
}

// whopPlanView is one plan of the store.
type whopPlanView struct {
	ID           string `json:"id"`
	ProductID    string `json:"productId"`
	ProductTitle string `json:"productTitle"`
	Title        string `json:"title"`
	Price        string `json:"price"`
	// Visibility is Whop's: "visible", "hidden" (reachable only by its
	// direct link), "archived" or "quick_link".
	Visibility string `json:"visibility"`
	TrialDays  int    `json:"trialDays,omitempty"`
	// Allowance is what a buyer of the plan may create, zero while unset.
	Allowance invites.Allowance `json:"allowance,omitzero"`
	// AllowanceFrom is "store" when the plan's metadata on Whop sets it,
	// "owner" when it was set here, and "" while it's unset.
	AllowanceFrom string `json:"allowanceFrom,omitempty"`
}

// whopKeyBody is the key pasted in Settings › Sell on Whop. TakeOver takes
// the store over from another dashboard that sells for it.
type whopKeyBody struct {
	Key      string `json:"key"`
	TakeOver bool   `json:"takeOver"`
}

// whopAccount is the stored connection.
type whopAccount struct {
	whop.Account
	Key         string
	ConnectedBy string
	ConnectedAt time.Time
	SyncedAt    time.Time
	Problem     string
	// WebhookID is the webhook Whop sends membership events to, at
	// WebhookURL, signed with WebhookSecret.
	WebhookID, WebhookURL, WebhookSecret string
	// PolledAt is when the dashboard last read every membership.
	PolledAt time.Time
	// MarkedAs is the address the dashboard last marked the store's
	// products with, and TakenOverBy the dashboard that took the store
	// over, at TakenOverAt.
	MarkedAs, TakenOverBy string
	TakenOverAt           time.Time
}

func (s *Server) whopClient(key string) (*whop.Client, error) {
	base, err := whop.CheckAPIURL(s.cfg.WhopAPIURL)
	if err != nil {
		return nil, err
	}
	return &whop.Client{APIURL: base, Key: key, UserAgent: "Playkeeper/" + version.Version}, nil
}

// storedWhop is the connection, or ok false when there is none.
func (s *Server) storedWhop() (whopAccount, bool, error) {
	var a whopAccount
	var connected, synced, polled, takenOver int64
	err := s.db.QueryRow(`SELECT account_id, title, route, api_key, connected_by, connected_at, synced_at, problem, webhook_id, webhook_url, webhook_secret, polled_at,
		marked_as, taken_over_by, taken_over_at
		FROM whop_account WHERE id = 1`).
		Scan(&a.ID, &a.Title, &a.Route, &a.Key, &a.ConnectedBy, &connected, &synced, &a.Problem, &a.WebhookID, &a.WebhookURL, &a.WebhookSecret, &polled,
			&a.MarkedAs, &a.TakenOverBy, &takenOver)
	if isNoRows(err) {
		return whopAccount{}, false, nil
	}
	if err != nil {
		return whopAccount{}, false, err
	}
	a.ConnectedAt, a.SyncedAt, a.PolledAt = time.UnixMilli(connected).UTC(), msTimeOrZero(synced), msTimeOrZero(polled)
	a.TakenOverAt = msTimeOrZero(takenOver)
	return a, true, nil
}

func msTimeOrZero(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// whopView reads Settings › Sell on Whop as it stands.
func (s *Server) whopView(ctx context.Context) (whopView, error) {
	v := whopView{Plans: []whopPlanView{}, Customers: []whopCustomerView{}, Needs: whop.Needs}
	v.Dashboard, _ = s.dashboardURL(ctx)
	a, ok, err := s.storedWhop()
	if err != nil || !ok {
		return v, err
	}
	acc := a.Account
	v.Connected, v.Account, v.KeyEnding, v.ConnectedBy, v.Problem = true, &acc, whop.Ending(a.Key), a.ConnectedBy, a.Problem
	v.Webhook = whopHooked(a, v.Dashboard)
	signIn, err := s.readWhopSignIn(ctx, v.Dashboard)
	if err != nil {
		return v, err
	}
	v.SignIn = &signIn
	if v.Customers, err = s.whopCustomerViews(ctx, a.ID); err != nil {
		return v, err
	}
	v.ConnectedAt = &a.ConnectedAt
	if !a.SyncedAt.IsZero() {
		v.SyncedAt = &a.SyncedAt
	}
	if a.TakenOverBy != "" {
		v.TakenOverBy = a.TakenOverBy
		if !a.TakenOverAt.IsZero() {
			v.TakenOverAt = &a.TakenOverAt
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT plan_id, product_id, product_title, title, price, visibility, trial_days, allowance_servers, allowance_memory_mb, allowance_from
		FROM whop_plans WHERE visibility != 'archived' ORDER BY position`)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var p whopPlanView
		if err := rows.Scan(&p.ID, &p.ProductID, &p.ProductTitle, &p.Title, &p.Price, &p.Visibility, &p.TrialDays, &p.Allowance.Servers, &p.Allowance.MemoryMB, &p.AllowanceFrom); err != nil {
			return v, err
		}
		v.Plans = append(v.Plans, p)
	}
	return v, rows.Err()
}

func (s *Server) hWhop(w http.ResponseWriter, r *http.Request, _ *session) {
	v, err := s.whopView(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// hWhopConnect checks a key with Whop, keeps it, and reads the store.
func (s *Server) hWhopConnect(w http.ResponseWriter, r *http.Request, sess *session) {
	var req whopKeyBody
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	key := strings.TrimSpace(req.Key)
	if !whop.ValidKey(key) {
		writeErr(w, http.StatusBadRequest, api.CodeWhopKeyRefused, "That doesn't look like a Whop API key.", "Copy it again from Whop's dashboard, under Developer.")
		return
	}
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	c, err := s.whopClient(key)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Playkeeper's Whop settings are wrong: "+err.Error(), "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), whopTimeout)
	defer cancel()
	acc, err := c.Me(ctx)
	if err != nil {
		s.whopRefusal(w, sess, err)
		return
	}
	prev, had, err := s.storedWhop()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if had && prev.ID != acc.ID {
		writeErr(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("This dashboard sells for %s. That key belongs to %s.", whopName(prev.Account), whopName(acc)),
			"Disconnect first to sell for another business.")
		return
	}
	missing, err := c.Missing(ctx, acc.ID)
	var checkErr *whop.Error
	switch {
	case err != nil && errors.As(err, &checkErr) && !whop.KeyRefused(err):
		// Whop wouldn't say what the key may do. The work itself says so
		// soon enough, as the connection's problem, so it isn't refused here.
		s.log.Warn("could not check the Whop key's permissions", "err", err)
		missing = nil
	case err != nil:
		s.whopRefusal(w, sess, err)
		return
	}
	if len(missing) > 0 {
		s.audit(sess.User.Username, "whop.connect", acc.ID, "refused", "the key lacks "+strings.Join(missing, ", "))
		writeJSON(w, http.StatusBadRequest, api.Error{Code: api.CodeWhopPermissions, Error: "That key can't do everything selling needs.",
			Hint: "Make a new key on Whop with these permissions too: " + strings.Join(missing, ", ") + ".", Params: map[string]any{"missing": strings.Join(missing, ",")}})
		return
	}
	products, err := c.Products(ctx, acc.ID)
	if err != nil {
		s.whopRefusal(w, sess, err)
		return
	}
	dash, _ := s.dashboardURL(ctx)
	markedAs := ""
	if had {
		markedAs = prev.MarkedAs
	}
	other := otherSeller(products, acc.ID, dash, markedAs)
	if other != "" && !req.TakeOver {
		s.audit(sess.User.Username, "whop.connect", acc.ID, "refused", "another Playkeeper sells for the store: "+other)
		writeJSON(w, http.StatusConflict, api.Error{Code: api.CodeWhopOtherSeller, Error: fmt.Sprintf("Another Playkeeper sells for %s, at %s.", whopName(acc), other),
			Hint: "Take the store over to sell from this dashboard instead. That one stops selling.", Params: map[string]any{"dashboard": other}})
		return
	}
	// Until the store carries this dashboard's marks, the other dashboard
	// sells and this one doesn't (see readWhopStore). A new key has every
	// membership read at once, so purchases the dashboard missed are caught.
	now := s.now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO whop_account(id, account_id, title, route, api_key, connected_by, connected_at, taken_over_by) VALUES(1,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET title = excluded.title, route = excluded.route, api_key = excluded.api_key, problem = '',
		taken_over_by = excluded.taken_over_by, taken_over_at = 0, polled_at = 0`,
		acc.ID, acc.Title, acc.Route, key, sess.User.Username, now, other); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	adopted, err := s.adoptWhopCustomers(ctx, acc.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	s.syncWhop(ctx, c, acc.ID, true)
	detail := "key ending " + whop.Ending(key)
	if had {
		detail = "replaced the key; " + detail
	}
	if adopted > 0 {
		detail += fmt.Sprintf("; took on the customers from before stores (%d)", adopted)
	}
	if other != "" {
		detail += "; " + s.takeoverOutcome(other)
	}
	s.audit(sess.User.Username, "whop.connect", acc.ID, "succeeded", detail)
	s.answerWhop(w, r)
}

// adoptWhopCustomers gives store, just connected, the Whop customers who
// have no store: those of a dashboard that sold for a store before the core
// kept stores, and had none connected when it upgraded. Until then any
// store it connected found them, so the next one still does. A customer
// the store already has keeps their own account.
func (s *Server) adoptWhopCustomers(ctx context.Context, store string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE OR IGNORE customers SET store = ? WHERE provider = ? AND store = ''`, store, whopProvider)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// takeoverOutcome says whether taking the store over from other worked,
// and if not, why, for the audit log.
func (s *Server) takeoverOutcome(other string) string {
	a, ok, err := s.storedWhop()
	if err == nil && ok && a.TakenOverBy == "" {
		return "took the store over from " + other
	}
	return "couldn't take the store over from " + other + ": " + cmpOr(a.Problem, "the store isn't marked for this dashboard yet")
}

// otherSeller is the address of another dashboard that sells for business,
// by the marking on its products, "" when none does. dash and markedAs are
// this dashboard's addresses, now and when it last marked them.
func otherSeller(products []whop.Product, business, dash, markedAs string) string {
	for _, p := range products {
		if seller := whop.Seller(p.Metadata, business); seller != "" && seller != dash && seller != markedAs {
			return seller
		}
	}
	return ""
}

// whopRefusal answers a request Whop refused or couldn't be asked.
func (s *Server) whopRefusal(w http.ResponseWriter, sess *session, err error) {
	var we *whop.Error
	switch {
	case whop.KeyRefused(err):
		s.audit(sess.User.Username, "whop.connect", "", "refused", "Whop refused the key")
		writeErr(w, http.StatusBadRequest, api.CodeWhopKeyRefused, "Whop didn't take that key.", "Copy it again from Whop's dashboard, under Developer, or make a new one.")
	case errors.As(err, &we):
		writeErr(w, http.StatusBadGateway, api.CodeUpstream, "Whop said: "+cmpOr(we.Message, fmt.Sprintf("error %d", we.Status)), "Try again in a minute.")
	default:
		s.log.Warn("could not reach Whop", "err", err)
		writeErr(w, http.StatusBadGateway, api.CodeUpstream, "Playkeeper couldn't reach Whop.", "Check that this machine can reach api.whop.com, then try again.")
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func whopName(a whop.Account) string { return cmpOr(a.Title, a.ID) }

// whopSyncBody asks to read the store again. TakeOver takes the store over
// from the dashboard that sells for it, as when that one took it over.
type whopSyncBody struct {
	TakeOver bool `json:"takeOver"`
}

// hWhopSync reads the store again.
func (s *Server) hWhopSync(w http.ResponseWriter, r *http.Request, sess *session) {
	var req whopSyncBody
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
			return
		}
	}
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	a, ok, err := s.storedWhop()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if !ok {
		writeErr(w, http.StatusConflict, api.CodeConflict, "This dashboard doesn't sell on Whop yet.", "Connect a Whop API key first.")
		return
	}
	c, err := s.whopClient(a.Key)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Playkeeper's Whop settings are wrong: "+err.Error(), "")
		return
	}
	// Reading the store again reads every membership again too, at the
	// reconciler's next pass.
	if _, err := s.db.Exec(`UPDATE whop_account SET polled_at = 0 WHERE id = 1`); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), whopTimeout)
	defer cancel()
	s.syncWhop(ctx, c, a.ID, req.TakeOver)
	if req.TakeOver && a.TakenOverBy != "" {
		outcome := s.takeoverOutcome(a.TakenOverBy)
		result := "succeeded"
		if s.whopTakenOver() {
			result = "failed"
		}
		s.audit(sess.User.Username, "whop.take_over", a.ID, result, outcome)
	}
	s.answerWhop(w, r)
}

func (s *Server) answerWhop(w http.ResponseWriter, r *http.Request) {
	v, err := s.whopView(context.WithoutCancel(r.Context()))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// whopPlanBody sets what a plan allows; zeros clear it.
type whopPlanBody struct {
	Servers  int `json:"servers"`
	MemoryMB int `json:"memoryMB"`
}

// hWhopPlan sets what a plan lets a buyer create, when its metadata on Whop
// doesn't say, then marks the store's products again.
func (s *Server) hWhopPlan(w http.ResponseWriter, r *http.Request, sess *session) {
	var req whopPlanBody
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	al := invites.Allowance{Servers: req.Servers, MemoryMB: req.MemoryMB}
	if !al.IsZero() {
		if err := al.Check(); err != nil {
			writeRefusal(w, err)
			return
		}
	}
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	a, ok, err := s.storedWhop()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	id := r.PathValue("plan")
	var from, title string
	if ok {
		err = s.db.QueryRow(`SELECT allowance_from, title FROM whop_plans WHERE plan_id = ?`, id).Scan(&from, &title)
	}
	switch {
	case !ok || isNoRows(err):
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "No such plan.", "Read the store again.")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	case from == "store":
		writeErr(w, http.StatusConflict, api.CodeConflict, "This plan's allowance comes from the plan on Whop.", "Change its metadata on Whop, then read the store again.")
		return
	}
	newFrom := "owner"
	if al.IsZero() {
		newFrom = ""
	}
	if _, err := s.db.Exec(`UPDATE whop_plans SET allowance_servers = ?, allowance_memory_mb = ?, allowance_from = ? WHERE plan_id = ?`,
		al.Servers, al.MemoryMB, newFrom, id); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	detail := "no allowance"
	if !al.IsZero() {
		detail = allowanceText(al)
	}
	s.audit(sess.User.Username, "whop.plan", id, "succeeded", fmt.Sprintf("%s: %s", cmpOr(title, id), detail))
	s.kickSaleRoom()
	if c, err := s.whopClient(a.Key); err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), whopTimeout)
		defer cancel()
		s.syncWhop(ctx, c, a.ID, false)
	}
	s.answerWhop(w, r)
}

// hWhopDisconnect stops selling: the store's products lose the dashboard's
// address, so the store stops taking orders, and the key is forgotten.
func (s *Server) hWhopDisconnect(w http.ResponseWriter, r *http.Request, sess *session) {
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	a, ok, err := s.storedWhop()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if !ok {
		s.answerWhop(w, r)
		return
	}
	detail := "the store's products keep no address"
	notice := ""
	if c, err := s.whopClient(a.Key); err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), whopTimeout)
		defer cancel()
		// Marks at the address the machine has now are this dashboard's too,
		// such as those of a takeover Whop refused after taking them.
		self, _ := s.dashboardURL(ctx)
		if err := s.markWhopProducts(ctx, c, a.ID, nil, "", []string{self, a.MarkedAs}, false); err != nil {
			// A store that still names this dashboard keeps taking orders, so
			// the key stays for another try, unless Whop no longer takes it.
			if !whop.KeyRefused(err) {
				s.log.Warn("could not take the dashboard's address off the store's products", "err", err)
				writeErr(w, http.StatusBadGateway, api.CodeUpstream, "Playkeeper couldn't close the store on Whop, so it still sells for this dashboard.",
					"Try again in a minute. Until then the store keeps taking orders.")
				return
			}
			detail = "Whop refused the key, so the store's products keep this dashboard's address"
			notice = "Whop no longer takes this dashboard's key, so the store may still take orders. On Whop, take " + whop.MetaDashboard + " off the store's products, or hide its plans."
		}
		if a.WebhookID != "" {
			if err := c.DeleteWebhook(ctx, a.WebhookID); err != nil {
				s.log.Warn("could not remove Whop's webhook", "err", err)
				detail += "; couldn't remove the webhook: " + err.Error()
			}
		}
	}
	err = s.immediate(r.Context(), func(c *sql.Conn) error {
		for _, q := range []string{`DELETE FROM whop_plans`, `DELETE FROM whop_memberships`, `DELETE FROM whop_customers`, `DELETE FROM whop_messages`,
			`DELETE FROM whop_deliveries`, `DELETE FROM whop_stock`, `DELETE FROM whop_account`} {
			if _, err := c.ExecContext(r.Context(), q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	s.audit(sess.User.Username, "whop.disconnect", a.ID, "succeeded", detail)
	v, err := s.whopView(context.WithoutCancel(r.Context()))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	v.Notice = notice
	writeJSON(w, http.StatusOK, v)
}

// syncWhop reads the store's plans with what each allows, and marks its
// products with the dashboard's address, claiming them from another
// dashboard when claim says to (see markWhopProducts). What went wrong is
// kept as the connection's problem for the page to show.
func (s *Server) syncWhop(ctx context.Context, c *whop.Client, accountID string, claim bool) {
	problem := ""
	if err := s.readWhopStore(ctx, c, accountID, claim); err != nil {
		problem = whopProblem(err)
		s.log.Warn("could not read the store on Whop", "err", err)
	}
	if a, ok, err := s.storedWhop(); err == nil && ok && a.TakenOverBy == "" {
		if dash, err := s.dashboardURL(ctx); err == nil {
			if err := s.ensureWhopWebhook(ctx, c, &a, dash); err != nil && problem == "" {
				problem = whopProblem(err)
				s.log.Warn("could not add Whop's webhook", "err", err)
			}
		}
	}
	if _, err := s.db.Exec(`UPDATE whop_account SET synced_at = ?, problem = ? WHERE id = 1`, s.now().UnixMilli(), problem); err != nil {
		s.log.Error("could not record reading the store on Whop", "err", err)
	}
	s.kickWhop()
}

func whopProblem(err error) string {
	var we *whop.Error
	switch {
	case whop.KeyRefused(err):
		return "Whop no longer takes this dashboard's key. Connect a new one."
	case errors.As(err, &we) && we.Status == http.StatusForbidden:
		return "Whop refused part of the work: " + cmpOr(we.Message, "a permission is missing") + ". Connect a key with every permission listed."
	case errors.As(err, &we):
		return "Whop said: " + cmpOr(we.Message, fmt.Sprintf("error %d", we.Status))
	case errors.Is(err, errNoDashboardAddress), errors.Is(err, errAddressUnknown):
		return err.Error()
	}
	return "Playkeeper couldn't reach Whop. It tries again when you read the store."
}

var errNoDashboardAddress = errors.New("This machine has no address with a certificate yet, so the store can't take orders. Give it one in Machine settings › Address.")

// readWhopStore reads the store's plans with what each allows, and marks
// its products for this dashboard. A dashboard another one took the store
// over from marks nothing. One that claims the store sells again only once
// its marks are written, which needs an address: until then the other
// dashboard still sells.
func (s *Server) readWhopStore(ctx context.Context, c *whop.Client, accountID string, claim bool) error {
	products, err := c.Products(ctx, accountID)
	if err != nil {
		return err
	}
	plans, err := c.Plans(ctx, accountID)
	if err != nil {
		return err
	}
	selling := map[string]bool{}
	err = s.immediate(ctx, func(conn *sql.Conn) error {
		owned := map[string]invites.Allowance{}
		rows, err := conn.QueryContext(ctx, `SELECT plan_id, allowance_servers, allowance_memory_mb FROM whop_plans WHERE allowance_from = 'owner'`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var al invites.Allowance
			if err := rows.Scan(&id, &al.Servers, &al.MemoryMB); err != nil {
				rows.Close()
				return err
			}
			owned[id] = al
		}
		rows.Close()
		// A plan someone still has stays, even once it's archived or gone
		// from the list, so its customers keep what it gives them.
		if _, err := conn.ExecContext(ctx, `DELETE FROM whop_plans WHERE plan_id NOT IN (SELECT plan_id FROM whop_memberships)`); err != nil {
			return err
		}
		for i, p := range plans {
			var al invites.Allowance
			from := ""
			if n, mb, ok := whop.PlanAllowance(p.Metadata); ok && (invites.Allowance{Servers: n, MemoryMB: mb}).Check() == nil {
				al, from = invites.Allowance{Servers: n, MemoryMB: mb}, "store"
			} else if o, ok := owned[p.ID]; ok {
				al, from = o, "owner"
			}
			if !al.IsZero() && p.Visibility != "archived" {
				selling[p.Product.ID] = true
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO whop_plans(plan_id, product_id, product_title, title, price, visibility, trial_days, allowance_servers, allowance_memory_mb, allowance_from, disk_gb, position, stock, unlimited_stock, free)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(plan_id) DO UPDATE SET product_id = excluded.product_id, product_title = excluded.product_title, title = excluded.title, price = excluded.price,
				visibility = excluded.visibility, trial_days = excluded.trial_days, allowance_servers = excluded.allowance_servers, allowance_memory_mb = excluded.allowance_memory_mb,
				allowance_from = excluded.allowance_from, disk_gb = excluded.disk_gb, position = excluded.position, stock = excluded.stock, unlimited_stock = excluded.unlimited_stock,
				free = excluded.free`,
				p.ID, p.Product.ID, p.Product.Title, p.Title, p.Price(), p.Visibility, p.TrialDays, al.Servers, al.MemoryMB, from, whop.PlanDiskGB(p.Metadata), i,
				max(int(p.Stock), 0), p.UnlimitedStock, p.Free()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	dash, err := s.dashboardURL(ctx)
	if err != nil {
		return err
	}
	var markedAs, takenBy string
	if err := s.db.QueryRowContext(ctx, `SELECT marked_as, taken_over_by FROM whop_account WHERE id = 1`).Scan(&markedAs, &takenBy); err != nil && !isNoRows(err) {
		return err
	}
	switch {
	case takenBy != "" && !claim:
		return nil
	case takenBy != "" && dash == "":
		return errNoDashboardAddress
	case !claim && dash != "":
		if taken, err := s.noticeTakeover(ctx, accountID, products, dash, markedAs); taken || err != nil {
			return err
		}
	}
	if err := s.markWhopProducts(ctx, c, accountID, products, dash, []string{dash, markedAs}, claim && dash != ""); err != nil {
		return err
	}
	if dash != "" && dash != markedAs {
		if _, err := s.db.ExecContext(ctx, `UPDATE whop_account SET marked_as = ? WHERE id = 1`, dash); err != nil {
			return err
		}
	}
	if takenBy != "" {
		if _, err := s.db.ExecContext(ctx, `UPDATE whop_account SET taken_over_by = '', taken_over_at = 0 WHERE id = 1`); err != nil {
			return err
		}
	}
	if dash == "" && len(selling) > 0 {
		return errNoDashboardAddress
	}
	return nil
}

// noticeTakeover records that another dashboard took the store over when
// the store's products carry another dashboard's marks for this business,
// and says whether they do. dash and markedAs are this dashboard's own
// addresses, now and when it last marked the products, so a new address
// isn't mistaken for another dashboard.
func (s *Server) noticeTakeover(ctx context.Context, accountID string, products []whop.Product, dash, markedAs string) (bool, error) {
	other := otherSeller(products, accountID, dash, markedAs)
	if other == "" {
		return false, nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE whop_account SET taken_over_by = ?, taken_over_at = ? WHERE id = 1`, other, s.now().UnixMilli()); err != nil {
		return true, err
	}
	s.audit("system", "whop.taken_over", accountID, "succeeded", "another Playkeeper took the store over, at "+other+"; this dashboard stopped selling")
	return true, nil
}

// markWhopProducts gives the products that sell servers (those with a plan
// that has an allowance) the dashboard's address, and takes it off the
// others. With dash empty it takes it off them all. products nil reads
// them first. own are the addresses whose marks are this dashboard's.
//
// Another dashboard's marks stay, since that dashboard took the store over,
// unless claim takes the store from it. The caller checks for them first
// (see readWhopStore): taken over, a dashboard writes nothing.
//
// The marks change on every product or on none: when Whop refuses one, the
// products already changed are put back as they were. Otherwise a takeover
// Whop half took would stop the other dashboard, which sees the new marks,
// while this one waits for the rest.
func (s *Server) markWhopProducts(ctx context.Context, c *whop.Client, accountID string, products []whop.Product, dash string, own []string, claim bool) error {
	if products == nil {
		var err error
		if products, err = c.Products(ctx, accountID); err != nil {
			return err
		}
	}
	selling := map[string]bool{}
	if dash != "" {
		rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT product_id FROM whop_plans WHERE allowance_from != '' AND visibility != 'archived'`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			selling[id] = true
		}
		rows.Close()
	}
	var changed []whop.Product
	for _, p := range products {
		addr := ""
		if selling[p.ID] {
			addr = dash
		}
		if seller := whop.Seller(p.Metadata, accountID); addr == "" && seller != "" && !slices.Contains(own, seller) && !claim {
			continue
		}
		meta, differs := whop.WithSeller(p.Metadata, addr, accountID)
		if !differs {
			continue
		}
		if err := c.SetProductMetadata(ctx, p.ID, meta); err != nil {
			// Whop may have made the change before its answer went wrong.
			s.putWhopMarksBack(ctx, c, append(changed, p))
			return err
		}
		changed = append(changed, p)
	}
	return nil
}

// putWhopMarksBack gives products the metadata they had, even once the
// request that changed them ran out of time.
func (s *Server) putWhopMarksBack(ctx context.Context, c *whop.Client, products []whop.Product) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), whopTimeout)
	defer cancel()
	for _, p := range slices.Backward(products) {
		if err := c.SetProductMetadata(ctx, p.ID, p.Metadata); err != nil {
			s.log.Warn("could not put a product's marks on Whop back", "product", p.ID, "err", err)
		}
	}
}

// errAddressUnknown is dashboardURL's answer when the agent can't be asked
// for the machine's address. Nothing that depends on the address changes
// then: a store stays open, or closed, as it was.
var errAddressUnknown = errors.New("Playkeeper couldn't ask this machine for its address. It tries again when you read the store.")

// dashboardURL is where Whop and buyers reach this dashboard: the machine's
// address, once the agent has a certificate for it that's still good,
// without a port while the dashboard answers there on port 443 (see
// dashboard443.go) and with the panel's port otherwise. It's "" without a
// certificate: Whop only calls trusted HTTPS addresses, and a buyer's invite
// link must open without a warning. An agent that can't be asked is
// errAddressUnknown, never "".
func (s *Server) dashboardURL(ctx context.Context) (string, error) {
	now, _, err := s.dashboardURLs(ctx)
	return now, err
}

// dashboardURLs is dashboardURL, and the dashboard's address at the panel's
// port, which keeps reaching it whatever port 443 does.
func (s *Server) dashboardURLs(ctx context.Context) (now, panelPort string, err error) {
	var addr api.Address
	if status, err := s.agent.Do(ctx, "GET", "/v1/address", nil, nil, &addr); err != nil || status != http.StatusOK {
		return "", "", errAddressUnknown
	}
	host := s.certifiedHost(addr)
	if host == "" {
		return "", "", nil
	}
	port := s.cfg.PanelPort
	if addr.Dashboard != nil && addr.Dashboard.Port != 0 {
		port = addr.Dashboard.Port
	}
	return hostURL(host, port), hostURL(host, s.cfg.PanelPort), nil
}

// certifiedHost is the machine's name, lowercase, while the agent has a
// certificate for it that's still good, and "" otherwise.
func (s *Server) certifiedHost(addr api.Address) string {
	cert := addr.Certificate
	if addr.Kind == api.AddressNone || addr.Host == "" || cert == nil || cert.NotAfter == nil || !cert.NotAfter.After(s.now()) {
		return ""
	}
	for _, n := range cert.Names {
		if strings.EqualFold(n, addr.Host) {
			return strings.ToLower(addr.Host)
		}
	}
	return ""
}
