package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/hetzner"
	"github.com/CIYAhq/playkeeper/internal/version"
)

// Hetzner stock (Settings › Machines): the owner pastes a read-only API
// token of their Hetzner project and picks a server type, and the dashboard
// asks Hetzner once a minute where that type can be bought. When it comes
// into stock somewhere, the dashboard's Discord hears of it with a link
// that buys one there, so another machine can join while there's stock.
// Only the owner may, since the token reads their Hetzner project.
const actWatchStock action = "machines.stock"

const (
	// stockEvery is how often the dashboard asks Hetzner, well inside
	// Hetzner's 3,600 requests an hour for a token.
	stockEvery = time.Minute
	// stockTimeout bounds one question to Hetzner.
	stockTimeout = 20 * time.Second
	// stockRegone is how long a place must have been out of stock before
	// its coming back is posted again, so stock that flickers posts once.
	stockRegone = 30 * time.Minute
	// stockPatience is how many checks in a row may fail before the page
	// says Hetzner isn't answering.
	stockPatience = 5
	// stockSlow is how long the dashboard waits after Hetzner asked it to
	// slow down without saying until when, or said it sells no such type,
	// and stockSlowest the longest it waits whatever Hetzner said.
	stockSlow    = 5 * time.Minute
	stockSlowest = time.Hour
)

// stockTypes are the server types Settings offers to watch: Hetzner's
// cost-optimized line, the one that runs out.
var stockTypes = []string{"cx23", "cx33", "cx43", "cx53"}

// defaultStockType is the one Playkeeper Cloud's machines are.
const defaultStockType = "cx53"

// hetznerView is Settings › Machines › Hetzner stock.
type hetznerView struct {
	Connected   bool       `json:"connected"`
	TokenEnding string     `json:"tokenEnding,omitempty"`
	ServerType  string     `json:"serverType"`
	Types       []string   `json:"types"`
	SetBy       string     `json:"setBy,omitempty"`
	SetAt       *time.Time `json:"setAt,omitempty"`
	// CheckedAt is when Hetzner last answered.
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	// Problem is what stops the watch or keeps it from Hetzner's answer.
	Problem string `json:"problem,omitempty"`
	// Places are where Hetzner offers the type, in Hetzner's order.
	Places []hetznerPlaceView `json:"places"`
	// Discord says whether the dashboard's Discord is connected, which is
	// where the watch posts.
	Discord bool `json:"discord"`
}

// hetznerPlaceView is one place Hetzner offers the type.
type hetznerPlaceView struct {
	Location  string `json:"location"`
	City      string `json:"city"`
	Available bool   `json:"available"`
	// Since is when it came into stock or went out of it.
	Since *time.Time `json:"since,omitempty"`
	// BuyURL, while it's in stock, is Hetzner's console with this type and
	// place picked.
	BuyURL string `json:"buyUrl,omitempty"`
}

// stockPlace is what the dashboard keeps of one place between checks.
type stockPlace struct {
	Location  string `json:"location"`
	Available bool   `json:"available"`
	// Since is when (Unix milliseconds) it last came into stock or went out.
	Since int64 `json:"since"`
	// Posted is when it was last posted as in stock, 0 if never.
	Posted int64 `json:"posted,omitempty"`
}

// stockWatch is the stored watch.
type stockWatch struct {
	Token, ServerType, SetBy string
	SetAt, CheckedAt         time.Time
	Problem                  string
	Refused                  bool
	Failures                 int
	Places                   []stockPlace
}

// hetznerBody is what Settings sends: a token to start watching or replace
// the one kept, and the server type to watch.
type hetznerBody struct {
	Token      string `json:"token"`
	ServerType string `json:"serverType"`
}

func (s *Server) hetznerClient(token string) (*hetzner.Client, error) {
	base, err := hetzner.CheckAPIURL(s.cfg.HetznerAPIURL)
	if err != nil {
		return nil, err
	}
	return &hetzner.Client{APIURL: base, Token: token, UserAgent: "Playkeeper/" + version.Version}, nil
}

// storedStockWatch is the watch, or ok false when there is none.
func (s *Server) storedStockWatch() (stockWatch, bool, error) {
	var w stockWatch
	var setAt, checkedAt int64
	var refused int
	var places string
	err := s.db.QueryRow(`SELECT token, server_type, set_by, set_at, checked_at, problem, refused, failures, places FROM hetzner_watch WHERE id = 1`).
		Scan(&w.Token, &w.ServerType, &w.SetBy, &setAt, &checkedAt, &w.Problem, &refused, &w.Failures, &places)
	if errors.Is(err, sql.ErrNoRows) {
		return stockWatch{}, false, nil
	}
	if err != nil {
		return stockWatch{}, false, err
	}
	w.SetAt, w.Refused = time.UnixMilli(setAt), refused != 0
	if checkedAt > 0 {
		w.CheckedAt = time.UnixMilli(checkedAt)
	}
	if json.Unmarshal([]byte(places), &w.Places) != nil {
		w.Places = nil
	}
	return w, true, nil
}

// saveStockState keeps what a check found, for the watch of token alone:
// one the owner replaced or stopped meanwhile is left as it is.
func (s *Server) saveStockState(w stockWatch) error {
	places, err := json.Marshal(w.Places)
	if err != nil {
		return err
	}
	var checked int64
	if !w.CheckedAt.IsZero() {
		checked = w.CheckedAt.UnixMilli()
	}
	refused := 0
	if w.Refused {
		refused = 1
	}
	_, err = s.db.Exec(`UPDATE hetzner_watch SET checked_at = ?, problem = ?, refused = ?, failures = ?, places = ? WHERE id = 1 AND token = ? AND server_type = ?`,
		checked, w.Problem, refused, w.Failures, string(places), w.Token, w.ServerType)
	return err
}

func (s *Server) hetznerView() (hetznerView, error) {
	v := hetznerView{ServerType: defaultStockType, Types: stockTypes, Places: []hetznerPlaceView{}}
	w, ok, err := s.storedStockWatch()
	if err != nil || !ok {
		return v, err
	}
	setAt := w.SetAt
	v.Connected, v.TokenEnding, v.ServerType, v.SetBy, v.SetAt, v.Problem = true, hetzner.Ending(w.Token), w.ServerType, w.SetBy, &setAt, w.Problem
	if !w.CheckedAt.IsZero() {
		checked := w.CheckedAt
		v.CheckedAt = &checked
	}
	for _, p := range w.Places {
		pv := hetznerPlaceView{Location: p.Location, City: hetzner.City(p.Location), Available: p.Available}
		if p.Since > 0 {
			since := time.UnixMilli(p.Since)
			pv.Since = &since
		}
		if p.Available {
			pv.BuyURL = hetzner.BuyURL(w.ServerType, p.Location)
		}
		v.Places = append(v.Places, pv)
	}
	return v, nil
}

func (s *Server) answerHetzner(w http.ResponseWriter, r *http.Request) {
	v, err := s.hetznerView()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	v.Discord = s.discordConnected(r.Context())
	writeJSON(w, http.StatusOK, v)
}

// discordConnected reports whether the dashboard's own agent, which holds
// the Discord settings, has Discord connected; false if it can't say.
func (s *Server) discordConnected(ctx context.Context) bool {
	m, err := s.localMachine()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var d api.DiscordSettings
	status, err := m.agent.Do(ctx, "GET", "/v1/discord", nil, nil, &d)
	return err == nil && status == http.StatusOK && d.Connected
}

// hHetzner is Settings › Machines › Hetzner stock.
func (s *Server) hHetzner(w http.ResponseWriter, r *http.Request, _ *session) {
	s.answerHetzner(w, r)
}

// hHetznerSet starts watching with a token, replaces the token, or watches
// another type. Hetzner is asked at once: a token it refuses, or a type it
// doesn't sell, isn't kept, and a type in stock now is posted now.
func (s *Server) hHetznerSet(w http.ResponseWriter, r *http.Request, sess *session) {
	var req hetznerBody
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	typ := cmpOr(strings.TrimSpace(req.ServerType), defaultStockType)
	if !slices.Contains(stockTypes, typ) {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Pick one of the server types listed.", "")
		return
	}
	token := strings.TrimSpace(req.Token)
	if token != "" && !hetzner.ValidToken(token) {
		writeErr(w, http.StatusBadRequest, api.CodeHetznerTokenRefused, "That doesn't look like a Hetzner API token.", "Hetzner's tokens are 64 letters and digits. Copy it again from your project's Security › API tokens.")
		return
	}
	s.hetznerMu.Lock()
	defer s.hetznerMu.Unlock()
	prev, had, err := s.storedStockWatch()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	switch {
	case token == "" && !had:
		writeErr(w, http.StatusBadRequest, api.CodeHetznerTokenRefused, "Paste a read-only Hetzner API token.", "Make one in your Hetzner project's Security › API tokens, with Read permission.")
		return
	case token == "":
		token = prev.Token
	}
	c, err := s.hetznerClient(token)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Playkeeper's Hetzner settings are wrong: "+err.Error(), "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), stockTimeout)
	defer cancel()
	stock, err := c.Stock(ctx, typ)
	if err != nil {
		s.hetznerRefusal(w, sess, typ, err)
		return
	}
	now := s.now()
	if _, err := s.db.Exec(`INSERT INTO hetzner_watch(id, token, server_type, set_by, set_at) VALUES(1,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET token = excluded.token, server_type = excluded.server_type, set_by = excluded.set_by, set_at = excluded.set_at,
		checked_at = 0, problem = '', refused = 0, failures = 0, places = '[]'`,
		token, typ, sess.User.Username, now.UnixMilli()); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	detail := "token ending " + hetzner.Ending(token) + ", watching " + typ
	switch {
	case had && prev.Token != token:
		detail = "replaced the token; " + detail
	case had:
		detail = "now watching " + typ + " instead of " + prev.ServerType
	}
	s.audit(sess.User.Username, "hetzner.watch", typ, "succeeded", detail)
	watch := stockWatch{Token: token, ServerType: typ, SetBy: sess.User.Username, SetAt: now}
	s.noteStock(watch, stock, now)
	s.answerHetzner(w, r)
}

// hetznerRefusal answers a setting Hetzner refused or couldn't be asked
// about.
func (s *Server) hetznerRefusal(w http.ResponseWriter, sess *session, typ string, err error) {
	var he *hetzner.Error
	switch {
	case hetzner.TokenRefused(err):
		s.audit(sess.User.Username, "hetzner.watch", typ, "refused", "Hetzner refused the token")
		writeErr(w, http.StatusBadRequest, api.CodeHetznerTokenRefused, "Hetzner didn't take that token.", "Copy it again, or make a new one with Read permission in your project's Security › API tokens.")
	case errors.Is(err, hetzner.ErrNoSuchType):
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("Hetzner doesn't sell %s any more.", strings.ToUpper(typ)), "Pick another server type.")
	case errors.As(err, &he):
		writeErr(w, http.StatusBadGateway, api.CodeUpstream, "Hetzner said: "+cmpOr(he.Message, fmt.Sprintf("error %d", he.Status)), "Try again in a minute.")
	default:
		s.log.Warn("could not reach Hetzner", "err", err)
		writeErr(w, http.StatusBadGateway, api.CodeUpstream, "Playkeeper couldn't reach Hetzner.", "Check that this machine can reach api.hetzner.cloud, then try again.")
	}
}

// hHetznerStop stops watching and forgets the token.
func (s *Server) hHetznerStop(w http.ResponseWriter, r *http.Request, sess *session) {
	s.hetznerMu.Lock()
	defer s.hetznerMu.Unlock()
	prev, had, err := s.storedStockWatch()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if had {
		if _, err := s.db.Exec(`DELETE FROM hetzner_watch`); err != nil {
			writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
			return
		}
		s.audit(sess.User.Username, "hetzner.stop", prev.ServerType, "succeeded", "forgot the token ending "+hetzner.Ending(prev.Token))
	}
	s.answerHetzner(w, r)
}

// runStock asks Hetzner for the watched type's stock until ctx ends.
func (s *Server) runStock(ctx context.Context) {
	t := time.NewTimer(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		t.Reset(s.checkStock(ctx))
	}
}

// checkStock asks Hetzner once, keeps what it found and posts places that
// came into stock. It returns when to ask next.
func (s *Server) checkStock(ctx context.Context) time.Duration {
	s.hetznerMu.Lock()
	defer s.hetznerMu.Unlock()
	w, ok, err := s.storedStockWatch()
	if err != nil {
		s.log.Warn("could not read the Hetzner stock watch", "err", err)
		return stockEvery
	}
	if !ok || w.Refused {
		return stockEvery
	}
	c, err := s.hetznerClient(w.Token)
	if err != nil {
		s.log.Warn("the Hetzner stock watch can't ask Hetzner", "err", err)
		return stockEvery
	}
	qctx, cancel := context.WithTimeout(ctx, stockTimeout)
	stock, err := c.Stock(qctx, w.ServerType)
	cancel()
	if ctx.Err() != nil {
		return stockEvery
	}
	now := s.now()
	next := stockEvery
	switch limited, until := hetzner.RateLimited(err); {
	case err == nil:
		s.noteStock(w, stock, now)
		qctx, cancel := context.WithTimeout(ctx, stockTimeout)
		if err := s.confirmFromHetzner(qctx, c); err != nil {
			s.log.Info("could not look for joined machines in the Hetzner project", "err", err)
		}
		cancel()
		return stockEvery
	case hetzner.TokenRefused(err):
		w.Refused, w.Problem = true, "Hetzner no longer takes the token, so the watch stopped. Paste a new read-only token."
		s.log.Warn("Hetzner refused the stock watch's token; the watch stopped")
	case errors.Is(err, hetzner.ErrNoSuchType):
		w.Problem = fmt.Sprintf("Hetzner doesn't sell %s any more. Pick another server type.", strings.ToUpper(w.ServerType))
		next = stockSlow
	case limited:
		next = stockSlow
		if d := until.Sub(now); d > next {
			next = min(d, stockSlowest)
		}
		fallthrough
	default:
		w.Failures++
		if w.Failures >= stockPatience {
			w.Problem = "Hetzner hasn't answered the last few checks. The watch keeps asking."
		}
		s.log.Info("could not ask Hetzner for stock", "err", err, "failures", w.Failures)
	}
	if err := s.saveStockState(w); err != nil {
		s.log.Warn("could not keep the Hetzner stock watch's state", "err", err)
	}
	return next
}

// noteStock keeps what Hetzner answered for w and posts the places that
// came into stock. The caller holds s.hetznerMu.
func (s *Server) noteStock(w stockWatch, stock hetzner.Stock, now time.Time) {
	var arrived []string
	w.Places, arrived = stockChanges(w.Places, stock, now)
	w.CheckedAt, w.Problem, w.Failures = now, "", 0
	if err := s.saveStockState(w); err != nil {
		s.log.Warn("could not keep the Hetzner stock watch's state", "err", err)
		return
	}
	if len(arrived) > 0 {
		s.notifyInStock(w, arrived)
	}
}

// stockChanges is each place Hetzner offers the type, as it stands now
// after before, and the places to post: those in stock that weren't, unless
// they were posted and are back from less than stockRegone away. A place
// never seen before that's in stock is posted too, as on the first check.
func stockChanges(before []stockPlace, stock hetzner.Stock, now time.Time) (after []stockPlace, arrived []string) {
	at := now.UnixMilli()
	for _, l := range stock.Locations {
		i := slices.IndexFunc(before, func(p stockPlace) bool { return p.Location == l.Name })
		p := stockPlace{Location: l.Name, Available: l.Available, Since: at}
		switch {
		case i < 0:
			if l.Available {
				p.Posted = at
				arrived = append(arrived, l.Name)
			}
		case before[i].Available == l.Available:
			p = before[i]
		case l.Available:
			p.Posted = before[i].Posted
			if p.Posted == 0 || at-before[i].Since >= stockRegone.Milliseconds() {
				p.Posted = at
				arrived = append(arrived, l.Name)
			}
		default:
			p.Posted = before[i].Posted
		}
		after = append(after, p)
	}
	return after, arrived
}

// notifyInStock tells the Discord notifier of the dashboard's own agent,
// which holds the Discord settings and words the alert, where w's type came
// into stock. Discord is optional, so a failure only shows in the log.
func (s *Server) notifyInStock(w stockWatch, locations []string) {
	m, err := s.localMachine()
	if err != nil {
		s.log.Info("could not post machines in stock to Discord", "err", err)
		return
	}
	if len(locations) > 10 {
		locations = locations[:10]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := api.DiscordNotifyRequest{Kind: api.DiscordInStock, ServerType: w.ServerType, Locations: locations, Actor: w.SetBy}
	if _, err := m.agent.Do(asActor(ctx, w.SetBy), "POST", "/v1/discord/notify", nil, req, nil); err != nil {
		s.log.Info("could not post machines in stock to Discord", "err", err)
	}
}
