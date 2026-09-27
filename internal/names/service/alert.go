package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Kinds of alerts, in the alert= field of their log line.
const (
	alertClaims         = "claims_budget"
	alertZoneNearlyFull = "zone_nearly_full"
	alertZoneFull       = "zone_full"
	alertChallenges     = "challenges_refused"
	alertChecks         = "liveness_checks"
	alertCertificates   = "certificate_budget"
	alertProxy          = "unlisted_proxy"
)

const (
	// alertEvery: each kind of alert goes out at most this often.
	alertEvery     = 6 * time.Hour
	alertTimeout   = 10 * time.Second
	maxAlertAnswer = 64 << 10
)

// alerter tells the owner about limits the service is running into: always
// as a warning in the log with alert=<kind>, and as a message to the webhook
// in NAMES_ALERT_WEBHOOK_URL if it is set. Sending never holds up a request.
type alerter struct {
	url  string
	from string
	hc   *http.Client
	log  *slog.Logger
	now  func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
	wg   sync.WaitGroup
}

func (a *alerter) send(kind, msg string, attrs ...any) {
	a.mu.Lock()
	now := a.now()
	if t, ok := a.last[kind]; ok && now.Sub(t) < alertEvery {
		a.mu.Unlock()
		return
	}
	a.last[kind] = now
	a.mu.Unlock()
	a.log.Warn("Alert: "+msg, append([]any{"alert", kind}, attrs...)...)
	if a.url == "" {
		return
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.post(a.from + msg)
	}()
}

// post sends one message. Failures are logged without the URL, whose path
// is the webhook's secret.
func (a *alerter) post(msg string) {
	body, err := json.Marshal(map[string]string{"content": msg})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), alertTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewReader(body))
	if err != nil {
		a.log.Warn("Could not send an alert to " + EnvAlertWebhook)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "playkeeper-names (https://github.com/CIYAhq/playkeeper)")
	resp, err := a.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		a.log.Warn("Could not send an alert to "+EnvAlertWebhook, "error", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAlertAnswer))
	if resp.StatusCode/100 != 2 {
		a.log.Warn("The webhook in "+EnvAlertWebhook+" refused an alert", "status", resp.StatusCode)
	}
}

// wait returns once the alerts being sent are done.
func (a *alerter) wait() { a.wg.Wait() }
