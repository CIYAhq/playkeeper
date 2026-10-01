package panel

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
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

// whopSellerLimits bound one seller's page, which calls a few times as it
// loads, each call waiting on a few of Whop's answers. They're the
// seller's own, once Whop's token says who they are (whopSeller), since
// every seller's page comes through Whop's proxy, from the same few
// addresses; whopSellerProxyLimits bound one address, only against floods.
var (
	whopSellerLimits      = publicLimits{perMinute: 60, open: 8}
	whopSellerProxyLimits = publicLimits{perMinute: 6000, open: 256, read: whopCallsFor + 10*time.Second, write: whopCallsFor + 10*time.Second}
)

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

// whopSeller answers the calls a seller's page makes, by what follows the
// store's address and their method: reading the seller's view of it, GET
// {store} (see sellerview.go); opening it, POST {store}/open; reading and
// setting its prices, GET and POST {store}/prices; Fix my plans, POST
// {store}/fix; and Open the store, POST {store}/sell (see sellerprices.go).
func (s *Server) whopSeller() http.Handler {
	calls := map[string]map[string]func(http.ResponseWriter, *http.Request, string){
		"":       {http.MethodGet: s.hWhopSellerView},
		"open":   {http.MethodPost: s.hWhopSellerOpen},
		"prices": {http.MethodGet: s.hWhopSellerPrices, http.MethodPost: s.hWhopSellerSetPrice},
		"fix":    {http.MethodPost: s.hWhopSellerFix},
		"sell":   {http.MethodPost: s.hWhopSellerSell},
	}
	perMinute := newLimiter(whopSellerLimits.perMinute, time.Minute, s.now)
	var mu sync.Mutex
	open := map[string]int{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		store, action, sub := strings.Cut(strings.TrimPrefix(r.URL.Path, whopSellerPrefix), "/")
		methods, known := calls[action]
		if !strings.HasPrefix(store, "biz_") || !reWhopID.MatchString(store) || !known || sub && action == "" {
			http.NotFound(w, r)
			return
		}
		answer := methods[r.Method]
		if answer == nil {
			w.Header().Set("Allow", strings.Join(slices.Sorted(maps.Keys(methods)), ", "))
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// A call without a valid token is refused as it comes (sellerAuth),
		// and counts against nobody's limits.
		if seller := s.sellerTokenUser(r); seller != "" {
			if ok, wait := perMinute.allow(seller); !ok {
				refuse(w, http.StatusTooManyRequests, wait)
				return
			}
			mu.Lock()
			busy := open[seller] >= whopSellerLimits.open
			if !busy {
				open[seller]++
			}
			mu.Unlock()
			if busy {
				refuse(w, http.StatusTooManyRequests, time.Second)
				return
			}
			defer func() {
				mu.Lock()
				if open[seller]--; open[seller] <= 0 {
					delete(open, seller)
				}
				mu.Unlock()
			}()
		}
		answer(w, r, store)
	})
}

// sellerTokenUser is the Whop user the request's token is for, when it's a
// valid one for the Playkeeper Cloud app, and "" otherwise.
func (s *Server) sellerTokenUser(r *http.Request) string {
	appID, _, err := s.whopAppClient(r.Context())
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(r.Context(), whopTimeout)
	defer cancel()
	user, err := s.whopTokens.Verify(ctx, r.Header.Get(whop.UserTokenHeader), appID, s.now())
	if err != nil {
		return ""
	}
	return user
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
