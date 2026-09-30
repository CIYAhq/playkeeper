package panel

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// A seller's page inside their Whop dashboard (the hosted blueprint's 2.1).
// Whop shows the Playkeeper Cloud app's dashboard view through its proxy,
// which adds a token saying who's looking to the page and to each call it
// makes to a relative address. There's no cookie and no Playkeeper session:
// sellerAuth checks the token and asks Whop whether its user is on the
// business's team. The page's first call opens the store: it checks the
// business approved every permission the app asks for, and registers it.
const (
	whopSellerPrefix = "/api/public/whop/seller/"
	// whopSellerPage is where the page is, which Whop's frames may show.
	whopSellerPage = "/whop/seller/"
)

// whopSellerLimits bound a seller's page, which calls a few times as it
// loads, each call waiting on a few of Whop's answers.
var whopSellerLimits = publicLimits{perMinute: 60, open: 8, read: whopCallsFor + 10*time.Second, write: whopCallsFor + 10*time.Second}

var (
	errSellerNoApp   = errors.New("Playkeeper Cloud doesn't sell for businesses on this dashboard yet")
	errSellerToken   = errors.New("This page opens inside your Whop dashboard, which says who you are")
	errSellerNotTeam = errors.New("Only the business's team on Whop can open its Playkeeper Cloud page")
)

// whopAppClient is the Playkeeper Cloud app's id and a client acting with
// its key, errSellerNoApp while either isn't set.
func (s *Server) whopAppClient(ctx context.Context) (string, *whop.Client, error) {
	var appID, key string
	if err := s.db.QueryRowContext(ctx, `SELECT client_id, api_key FROM whop_app WHERE id = 1`).Scan(&appID, &key); err != nil && !isNoRows(err) {
		return "", nil, err
	}
	if !whop.ValidClientID(appID) || key == "" {
		return "", nil, errSellerNoApp
	}
	c, err := s.whopClient(key)
	return appID, c, err
}

// sellerAuth checks that the request carries Whop's token for the
// Playkeeper Cloud app, and that Whop says its user is on storeID's team,
// whatever their role, and returns the user (user_…).
func (s *Server) sellerAuth(r *http.Request, storeID string) (string, error) {
	appID, c, err := s.whopAppClient(r.Context())
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(r.Context(), whopTimeout)
	defer cancel()
	user, err := s.whopTokens.Verify(ctx, r.Header.Get(whop.UserTokenHeader), appID, s.now())
	if errors.Is(err, whop.ErrUserToken) {
		return "", errSellerToken
	}
	if err != nil {
		return "", err
	}
	level, err := c.Access(ctx, user, storeID)
	if err != nil {
		return "", err
	}
	if level != "admin" {
		return "", errSellerNotTeam
	}
	return user, nil
}

// fromSellerPage says whether a call came from the seller's page itself. A
// page elsewhere can't send the header the dashboard's own calls carry
// without asking first, which the dashboard never allows, and a browser
// that says where a call came from must say this page.
func fromSellerPage(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	return r.Header.Get("X-Requested-With") == "playkeeper" && (site == "" || site == "same-origin")
}

// whopSeller answers the calls a seller's page makes: opening the store,
// POST {store}/open, and reading the seller's view of it, GET {store} (see
// sellerview.go).
func (s *Server) whopSeller() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		store, action, sub := strings.Cut(strings.TrimPrefix(r.URL.Path, whopSellerPrefix), "/")
		if !strings.HasPrefix(store, "biz_") || !reWhopID.MatchString(store) || sub && action != "open" {
			http.NotFound(w, r)
			return
		}
		method, answer := http.MethodGet, s.hWhopSellerView
		if sub {
			method, answer = http.MethodPost, s.hWhopSellerOpen
		}
		if r.Method != method {
			w.Header().Set("Allow", method)
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		answer(w, r, store)
	})
}

// whopSellerView is how a seller's store stands, for their page.
type whopSellerView struct {
	Store whopSellerStore `json:"store"`
	// New says this open registered the store.
	New bool `json:"new"`
}

type whopSellerStore struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Route string `json:"route,omitempty"`
	// Problem is what the store is waiting on, "" while it can sell.
	Problem string `json:"problem,omitempty"`
}

// hWhopSellerOpen is a seller opening the Playkeeper Cloud app's page in
// their Whop dashboard, the first time or again. It checks who's looking,
// and that the business approved every permission the app asks for, since
// its owner may have approved the app for another of their businesses. It
// then registers the business as a store, and says how the store stands.
func (s *Server) hWhopSellerOpen(w http.ResponseWriter, r *http.Request, store string) {
	if !fromSellerPage(r) {
		writeErr(w, http.StatusForbidden, api.CodeForbidden, "Open this page inside your Whop dashboard.", "")
		return
	}
	if _, err := s.sellerAuth(r, store); err != nil {
		s.sellerRefusal(w, err)
		return
	}
	appID, c, err := s.whopAppClient(r.Context())
	if err != nil {
		s.sellerRefusal(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), whopTimeout)
	defer cancel()
	lacking, err := c.Lacks(ctx, store, whop.AppNeeds)
	if err != nil {
		s.sellerRefusal(w, err)
		return
	}
	if len(lacking) > 0 {
		writeJSON(w, http.StatusConflict, api.Error{
			Error:  "This business hasn't approved everything Playkeeper Cloud asks for.",
			Code:   api.CodeWhopNotApproved,
			Hint:   "Open the install link, pick this business in Whop's business picker, and approve.",
			Params: map[string]any{"installUrl": "https://whop.com/apps/" + appID + "/install", "lacking": lacking},
		})
		return
	}
	acc, err := c.Business(ctx, store)
	if err != nil {
		s.sellerRefusal(w, err)
		return
	}
	added, err := s.addWhopStore(ctx, acc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	st, ok, err := s.whopStoreByID(ctx, store)
	if err != nil || !ok {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	writeJSON(w, http.StatusOK, whopSellerView{Store: whopSellerStore{ID: st.ID, Title: st.Title, Route: st.Route, Problem: st.Problem}, New: added})
}

// sellerRefusal answers a seller's call that can't go on.
func (s *Server) sellerRefusal(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errSellerToken):
		writeErr(w, http.StatusUnauthorized, api.CodeWhopToken, err.Error()+".", "")
	case errors.Is(err, errSellerNotTeam):
		writeErr(w, http.StatusForbidden, api.CodeWhopNotTeam, err.Error()+".", "")
	case errors.Is(err, errSellerNoApp):
		writeErr(w, http.StatusServiceUnavailable, api.CodeRetryLater, err.Error()+".", "")
	default:
		s.log.Warn("a seller's page couldn't reach Whop", "err", err)
		writeErr(w, http.StatusBadGateway, api.CodeRetryLater, whopProblem(err), "")
	}
}
