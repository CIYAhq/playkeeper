package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// The Playkeeper Cloud app's webhook (the hosted blueprint's 1.4). Whop
// sends it the membership events of every business that installed the app,
// each naming its business, and the dashboard keeps each for that
// business's app store. The dashboard adds the webhook with the app's key
// and keeps it pointing at its address, as it does the key store's. When
// Whop won't let the app's key add it, the owner makes one on Whop's
// developer dashboard and pastes its secret in Settings › Sell on Whop.
const whopAppWebhookPath = "/api/public/whop/app-webhook"

// reWhopWebhookSecret is the shape of a webhook's signing secret, such as
// ws_….
var reWhopWebhookSecret = regexp.MustCompile(`^[A-Za-z0-9_+/=-]{16,256}$`)

// whopApp is the Playkeeper Cloud app on this dashboard.
type whopApp struct {
	// ClientID is the app's id, which signs customers in and names the app
	// its webhook is for, and Key acts on the businesses that installed it.
	ClientID, Key string
	// WebhookID is the webhook the dashboard added for the app, at
	// WebhookURL, signed with WebhookSecret. Without a WebhookID, the secret
	// is that of one the owner made. HookedAt is when the webhook last came
	// to point at the dashboard: an app store read before then is read
	// again (see whopAppEvery).
	WebhookID, WebhookURL, WebhookSecret string
	HookedAt                             time.Time
	// Problem is why the dashboard couldn't add or move the webhook.
	Problem string
}

func (s *Server) readWhopApp(ctx context.Context) (whopApp, error) {
	var a whopApp
	var hooked int64
	err := s.db.QueryRowContext(ctx, `SELECT client_id, api_key, webhook_id, webhook_url, webhook_secret, hooked_at, problem FROM whop_app WHERE id = 1`).
		Scan(&a.ClientID, &a.Key, &a.WebhookID, &a.WebhookURL, &a.WebhookSecret, &hooked, &a.Problem)
	if errors.Is(err, sql.ErrNoRows) {
		return whopApp{}, nil
	}
	a.HookedAt = msTimeOrZero(hooked)
	return a, err
}

// hooked says whether the app's webhook tells the dashboard at dash of the
// app stores' memberships: the one the dashboard added points there, or
// the owner made one and pasted its secret.
func (a whopApp) hooked(dash string) bool {
	if a.WebhookSecret == "" {
		return false
	}
	return a.WebhookID == "" || (dash != "" && a.WebhookURL == dash+whopAppWebhookPath)
}

// whopAppHooked says whether the app's webhook tells this dashboard of the
// app stores' memberships.
func (s *Server) whopAppHooked(ctx context.Context) bool {
	app, err := s.readWhopApp(ctx)
	if err != nil {
		return false
	}
	dash, _ := s.dashboardURL(ctx)
	return app.hooked(dash)
}

// whopAppWebhook receives the Playkeeper Cloud app's deliveries: it checks
// the signature with the app's webhook secret, counts each delivery once
// (with the key store's, in whop_deliveries), keeps what a membership event
// says for the app store of the business it names, and answers at once.
// An event of a business that isn't an app store here, such as one that
// installed the app and hasn't opened it yet, is dropped: the store's first
// read, once it's there, takes in every membership.
func (s *Server) whopAppWebhook() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != whopAppWebhookPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		app, err := s.readWhopApp(r.Context())
		if err != nil || app.WebhookSecret == "" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxWhopDelivery+1))
		if err != nil || len(body) > maxWhopDelivery {
			http.Error(w, "Too large", http.StatusRequestEntityTooLarge)
			return
		}
		now := s.now()
		ev, err := whop.VerifyWebhook(app.WebhookSecret, r.Header, body, now)
		if err != nil {
			s.log.Warn("refused a delivery to the Playkeeper Cloud app's webhook", "err", err)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		res, err := s.db.Exec(`INSERT OR IGNORE INTO whop_deliveries(id, received_at) VALUES(?,?)`, ev.ID, now.UnixMilli())
		if err != nil {
			http.Error(w, "Try again", http.StatusServiceUnavailable)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = s.db.Exec(`DELETE FROM whop_deliveries WHERE received_at < ?`, now.Add(-whopDeliveriesKept).UnixMilli())
		// Whop sends a delivery that isn't counted as seen again.
		again := func() {
			_, _ = s.db.Exec(`DELETE FROM whop_deliveries WHERE id = ?`, ev.ID)
			http.Error(w, "Try again", http.StatusServiceUnavailable)
		}
		st, ok, err := s.whopStoreByID(r.Context(), ev.AccountID)
		if err != nil {
			s.log.Error("could not find the store of a Whop delivery", "err", err)
			again()
			return
		}
		if !ok || st.Via != whopViaApp {
			w.WriteHeader(http.StatusOK)
			return
		}
		switch ev.Type {
		case whop.EventMembershipActivated, whop.EventMembershipDeactivated, whop.EventMembershipCancelling:
			var m whop.Membership
			if json.Unmarshal(ev.Data, &m) == nil && reWhopID.MatchString(m.ID) {
				if err := s.keepMembership(st.ID, m, true); err != nil {
					s.log.Error("could not keep a Whop membership", "store", st.ID, "err", err)
					again()
					return
				}
			}
		}
		s.kickWhopStore(st.ID)
		w.WriteHeader(http.StatusOK)
	})
}

// ensureWhopAppWebhook adds the app's webhook with the app's key, or moves
// the one it added to the dashboard's address when that changed. It leaves
// one the owner made alone, and waits without the app's key, its id or the
// dashboard's address. What Whop refuses is the app's problem, which
// Settings › Sell on Whop shows with what to do instead.
func (s *Server) ensureWhopAppWebhook(ctx context.Context) {
	app, err := s.readWhopApp(ctx)
	if err != nil || app.Key == "" || !whop.ValidClientID(app.ClientID) || (app.WebhookSecret != "" && app.WebhookID == "") {
		return
	}
	dash, err := s.dashboardURL(ctx)
	if err != nil || dash == "" {
		return
	}
	want := dash + whopAppWebhookPath
	if app.WebhookID != "" && app.WebhookURL == want {
		return
	}
	c, err := s.whopClient(app.Key)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, whopTimeout)
	defer cancel()
	if app.WebhookID != "" {
		err := c.UpdateWebhookURL(ctx, app.WebhookID, want)
		if err == nil {
			s.keepWhopAppWebhook(app.WebhookID, want, app.WebhookSecret)
			return
		}
		if !whop.NotFound(err) {
			s.whopAppRefused(err, want)
			return
		}
	}
	hook, err := c.CreateWebhook(ctx, app.ClientID, want)
	if err == nil && (hook.ID == "" || hook.Secret == "") {
		err = errors.New("Whop's answer had no webhook or no secret")
	}
	if err != nil {
		s.whopAppRefused(err, want)
		return
	}
	s.keepWhopAppWebhook(hook.ID, want, hook.Secret)
}

// keepWhopAppWebhook records the webhook the dashboard added or moved, as
// pointing at the dashboard from now.
func (s *Server) keepWhopAppWebhook(id, url, secret string) {
	if _, err := s.db.Exec(`UPDATE whop_app SET webhook_id = ?, webhook_url = ?, webhook_secret = ?, hooked_at = ?, problem = '' WHERE id = 1`,
		id, url, secret, s.now().UnixMilli()); err != nil {
		s.log.Error("could not record the Playkeeper Cloud app's webhook", "err", err)
	}
}

// whopAppRefused records why the dashboard couldn't add or move the app's
// webhook at want.
func (s *Server) whopAppRefused(err error, want string) {
	s.log.Warn("could not keep the Playkeeper Cloud app's webhook pointing at this dashboard", "err", err)
	var we *whop.Error
	problem := "Playkeeper couldn't reach Whop to add the Playkeeper Cloud app's webhook. It tries again in a minute."
	switch {
	case whop.KeyRefused(err):
		problem = "Whop doesn't take the Playkeeper Cloud app's key. Paste the app's key again."
	case errors.As(err, &we) && we.Status == http.StatusForbidden:
		problem = "Whop doesn't let the Playkeeper Cloud app's key add the app's webhook (" + cmpOr(we.Message, "a permission is missing") + "). " +
			"On Whop's developer dashboard, add a webhook for the app that sends membership events to " + want + ", and paste its secret here."
	case errors.As(err, &we):
		problem = "Whop said, adding the Playkeeper Cloud app's webhook: " + cmpOr(we.Message, http.StatusText(we.Status))
	}
	if _, err := s.db.Exec(`UPDATE whop_app SET problem = ? WHERE id = 1`, problem); err != nil {
		s.log.Error("could not record the Playkeeper Cloud app's problem", "err", err)
	}
}

// whopAppEvery is how often an app store's memberships are read: every
// whopPollEvery while the app's webhook tells of them, at once when the
// store wasn't read since the webhook came to point at the dashboard, since
// it tells only of what happens from then, and every whopPollUnhooked
// without it.
func (s *Server) whopAppEvery(ctx context.Context, st whopStore) time.Duration {
	app, err := s.readWhopApp(ctx)
	if err != nil {
		return whopPollUnhooked
	}
	dash, _ := s.dashboardURL(ctx)
	switch {
	case !app.hooked(dash):
		return whopPollUnhooked
	case st.PolledAt.Before(app.HookedAt):
		return 0
	}
	return whopPollEvery
}

// whopAppView is the Playkeeper Cloud app in Settings › Sell on Whop.
type whopAppView struct {
	// KeyEnding is the end of the app's key, while there is one.
	KeyEnding string `json:"keyEnding,omitempty"`
	// Stores is how many businesses that installed the app it sells for.
	Stores int `json:"stores"`
	// Webhook says whether the app's webhook tells the dashboard of their
	// memberships. WebhookBy is "dashboard" for one the dashboard added,
	// and "owner" for one the owner made and pasted the secret of.
	// WebhookURL is where it must send them, "" while the machine has no
	// address.
	Webhook    bool   `json:"webhook"`
	WebhookBy  string `json:"webhookBy,omitempty"`
	WebhookURL string `json:"webhookUrl,omitempty"`
	// Problem is why the dashboard couldn't add or move the webhook.
	Problem string `json:"problem,omitempty"`
}

func (s *Server) whopAppView(ctx context.Context, dash string) (whopAppView, error) {
	app, err := s.readWhopApp(ctx)
	if err != nil {
		return whopAppView{}, err
	}
	v := whopAppView{KeyEnding: whop.Ending(app.Key), Webhook: app.hooked(dash), Problem: app.Problem}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM whop_stores WHERE via = ?`, whopViaApp).Scan(&v.Stores); err != nil {
		return whopAppView{}, err
	}
	switch {
	case app.WebhookSecret == "":
	case app.WebhookID == "":
		v.WebhookBy = "owner"
	default:
		v.WebhookBy = "dashboard"
	}
	if dash != "" {
		v.WebhookURL = dash + whopAppWebhookPath
	}
	return v, nil
}

// whopAppBody is what the owner sets of the Playkeeper Cloud app: its API
// key, which acts on the businesses that installed the app, and the secret
// of a webhook for the app they made on Whop themselves, for when Whop
// won't let the key add one. A field left out stays as it is, and an empty
// one clears it.
type whopAppBody struct {
	Key           *string `json:"key"`
	WebhookSecret *string `json:"webhookSecret"`
}

// hWhopAppSet keeps the Playkeeper Cloud app's key, or the secret of the
// webhook the owner made for it.
func (s *Server) hWhopAppSet(w http.ResponseWriter, r *http.Request, sess *session) {
	var req whopAppBody
	if err := decodeJSON(r, &req); err != nil || (req.Key == nil && req.WebhookSecret == nil) {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	var key, secret string
	if req.Key != nil {
		if key = strings.TrimSpace(*req.Key); key != "" && !whop.ValidKey(key) {
			writeErr(w, http.StatusBadRequest, api.CodeWhopKeyRefused, "That doesn't look like a Whop API key.",
				"Copy the Playkeeper Cloud app's API key again from Whop's developer dashboard.")
			return
		}
	}
	if req.WebhookSecret != nil {
		if secret = strings.TrimSpace(*req.WebhookSecret); secret != "" && !reWhopWebhookSecret.MatchString(secret) {
			writeErr(w, http.StatusBadRequest, api.CodeInvalid, "That doesn't look like a Whop webhook's secret.",
				"Copy it again from the app's webhook on Whop's developer dashboard.")
			return
		}
	}
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	defer tx.Rollback()
	var did []string
	_, err = tx.Exec(`INSERT INTO whop_app(id) VALUES(1) ON CONFLICT(id) DO NOTHING`)
	if err == nil && req.Key != nil {
		_, err = tx.Exec(`UPDATE whop_app SET api_key = ?, problem = '' WHERE id = 1`, key)
		did = append(did, cmpOr(whopEndingNote("the app's key", key), "cleared the app's key"))
	}
	if err == nil && req.WebhookSecret != nil {
		if secret != "" {
			_, err = tx.Exec(`UPDATE whop_app SET webhook_id = '', webhook_url = '', webhook_secret = ?, hooked_at = ?, problem = '' WHERE id = 1`,
				secret, s.now().UnixMilli())
			did = append(did, whopEndingNote("the secret of a webhook made on Whop", secret))
		} else {
			// Only one the owner made: the dashboard's own stays while it
			// points here.
			_, err = tx.Exec(`UPDATE whop_app SET webhook_secret = '', hooked_at = 0 WHERE id = 1 AND webhook_id = ''`)
			did = append(did, "cleared the secret of a webhook made on Whop")
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	s.audit(sess.User.Username, "whop.app", "panel", "succeeded", strings.Join(did, "; "))
	s.kickWhop()
	s.answerWhop(w, r)
}

// whopEndingNote names what was set by the end of its secret, "" for none.
func whopEndingNote(what, secret string) string {
	if secret == "" {
		return ""
	}
	return what + ", ending " + whop.Ending(secret)
}
