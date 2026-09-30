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

// The Playkeeper Cloud app (the hosted blueprint's 1.4). Its key acts on
// every business that installed the app, each an app store here, and its
// webhook hears of all their memberships, each event naming its business.
// Whop doesn't let an app's key add the app's own webhook, so the owner
// makes it once on Whop's developer dashboard, sending membership events
// to whopAppWebhookPath, and pastes its secret in Settings › Sell on Whop.
const whopAppWebhookPath = "/api/public/whop/app-webhook"

// reWhopWebhookSecret is the shape of a webhook's signing secret, such as
// ws_….
var reWhopWebhookSecret = regexp.MustCompile(`^[A-Za-z0-9_+/=-]{16,256}$`)

// whopApp is the Playkeeper Cloud app on this dashboard.
type whopApp struct {
	// Key acts on the businesses that installed the app.
	Key string
	// WebhookSecret signs the deliveries of the app's webhook, and
	// HookedAt is when the owner pasted it: an app store read before then
	// is read again (see whopAppEvery).
	WebhookSecret string
	HookedAt      time.Time
}

func (s *Server) readWhopApp(ctx context.Context) (whopApp, error) {
	var a whopApp
	var hooked int64
	err := s.db.QueryRowContext(ctx, `SELECT api_key, webhook_secret, hooked_at FROM whop_app WHERE id = 1`).Scan(&a.Key, &a.WebhookSecret, &hooked)
	if errors.Is(err, sql.ErrNoRows) {
		return whopApp{}, nil
	}
	a.HookedAt = msTimeOrZero(hooked)
	return a, err
}

// whopAppHooked says whether the app's webhook tells the dashboard of the
// app stores' memberships: its secret is there.
func (s *Server) whopAppHooked(ctx context.Context) bool {
	app, err := s.readWhopApp(ctx)
	return err == nil && app.WebhookSecret != ""
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

// whopAppEvery is how often an app store's memberships are read: every
// whopPollEvery while the app's webhook tells of them, at once when the
// store wasn't read since its secret was pasted, since the webhook tells
// only of what happens from then, and every whopPollUnhooked without it.
func (s *Server) whopAppEvery(ctx context.Context, st whopStore) time.Duration {
	app, err := s.readWhopApp(ctx)
	switch {
	case err != nil || app.WebhookSecret == "":
		return whopPollUnhooked
	case st.PolledAt.Before(app.HookedAt):
		return 0
	}
	return whopPollEvery
}

// errWhopAppUnapproved is an app store's problem while its business hasn't
// approved every permission the Playkeeper Cloud app asks for, as when its
// owner approved the app for another of their businesses, or took a
// permission back.
var errWhopAppUnapproved = errors.New("this business hasn't approved every permission the Playkeeper Cloud app asks for")

// whopAppApproved checks, before an app store's pass reads anything, that
// its business still grants the app everything the store needs. Whop
// answers a read an app isn't granted with an empty list, as with
// memberships, which would read as every plan ending. A store that lacks a
// permission waits with that as its problem, and is read afresh once it has
// them all.
func (s *Server) whopAppApproved(ctx context.Context, c *whop.Client, st whopStore) bool {
	lacking, err := c.Lacks(ctx, st.ID, whop.AppNeeds)
	if err == nil && len(lacking) == 0 {
		return true
	}
	problem := whopProblem(err)
	if err == nil {
		problem = errWhopAppUnapproved.Error() + " (it lacks " + strings.Join(lacking, ", ") + "). Its owner approves the app again, picking this business on Whop."
	}
	if _, err := s.db.Exec(`UPDATE whop_stores SET problem = ?, synced_at = 0, polled_at = 0 WHERE store_id = ?`, problem, st.ID); err != nil {
		s.log.Error("could not record a store's problem", "store", st.ID, "err", err)
	}
	return false
}

// whopAppView is the Playkeeper Cloud app in Settings › Sell on Whop.
type whopAppView struct {
	// KeyEnding is the end of the app's key, while there is one.
	KeyEnding string `json:"keyEnding,omitempty"`
	// Stores is how many businesses that installed the app it sells for.
	Stores int `json:"stores"`
	// Webhook says whether the app's webhook's secret is there, and
	// WebhookURL is where the webhook must send membership events, "" while
	// the machine has no address.
	Webhook    bool   `json:"webhook"`
	WebhookURL string `json:"webhookUrl,omitempty"`
}

func (s *Server) whopAppView(ctx context.Context, dash string) (whopAppView, error) {
	app, err := s.readWhopApp(ctx)
	if err != nil {
		return whopAppView{}, err
	}
	v := whopAppView{KeyEnding: whop.Ending(app.Key), Webhook: app.WebhookSecret != ""}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM whop_stores WHERE via = ?`, whopViaApp).Scan(&v.Stores); err != nil {
		return whopAppView{}, err
	}
	if dash != "" {
		v.WebhookURL = dash + whopAppWebhookPath
	}
	return v, nil
}

// whopAppBody is what the owner sets of the Playkeeper Cloud app: its API
// key, and the secret of the webhook they made for it on Whop. A field left
// out stays as it is, and an empty one clears it.
type whopAppBody struct {
	Key           *string `json:"key"`
	WebhookSecret *string `json:"webhookSecret"`
}

// hWhopAppSet keeps the Playkeeper Cloud app's key, or its webhook's
// secret.
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
		_, err = tx.Exec(`UPDATE whop_app SET api_key = ? WHERE id = 1`, key)
		did = append(did, cmpOr(whopEndingNote("the app's key", key), "cleared the app's key"))
	}
	if err == nil && req.WebhookSecret != nil {
		hooked := int64(0)
		if secret != "" {
			hooked = s.now().UnixMilli()
		}
		_, err = tx.Exec(`UPDATE whop_app SET webhook_secret = ?, hooked_at = ? WHERE id = 1`, secret, hooked)
		did = append(did, cmpOr(whopEndingNote("the app's webhook's secret", secret), "cleared the app's webhook's secret"))
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
