package discord

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const dashboardLine = "\n\n[Open the dashboard](https://panel.example.com/servers/k3j9x2)"

// calls lists requests as "POST ?wait=true" or "PATCH /messages/<id>", with
// the path relative to the test webhook.
func calls(reqs []fakeRequest) []string {
	var out []string
	for _, r := range reqs {
		s := r.Method + " " + strings.TrimPrefix(r.Path, "/api/v10/webhooks/"+testID+"/"+testToken)
		if q := r.Query.Encode(); q != "" {
			s += "?" + q
		}
		out = append(out, s)
	}
	return out
}

func TestAlertIsPostedAndConfirmed(t *testing.T) {
	h := newHarness(t, Settings{Webhook: testWebhook(t, ""), Alerts: DefaultAlerts()})
	crash := Crashed("The server ran out of memory and was killed.", true)
	h.Notify(crash)
	if next := h.sendDue(); !next.IsZero() {
		t.Errorf("nothing should wait after the alert went out, but something is due at %v", next)
	}
	reqs := h.fake.take()
	if len(reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodPost || r.Path != "/api/v10/webhooks/"+testID+"/"+testToken || r.Query.Encode() != "wait=true" {
		t.Errorf("sent %s %s?%s, want Execute Webhook with wait=true", r.Method, r.Path, r.Query.Encode())
	}
	crash.At = t0
	if len(r.Msg.Embeds) != 1 || !reflect.DeepEqual(r.Msg.Embeds[0], crash.embed(survival)) || r.Msg.Flags != 0 {
		t.Errorf("sent %+v", r.Msg)
	}
	if want := (Delivery{Sent: t0}); h.Delivery() != want || !slices.Equal(h.deliveries, []Delivery{want}) {
		t.Errorf("delivery %+v, reported %+v", h.Delivery(), h.deliveries)
	}
}

func TestMessagesGoToTheThreadInTheWebhookURL(t *testing.T) {
	h := newHarness(t, Settings{Webhook: testWebhook(t, testURL+"?thread_id="+testThread), Alerts: DefaultAlerts(), LiveStatus: true})
	h.Notify(Recovered())
	h.UpdateStatus(Status{State: StateOnline})
	h.sendDue()
	h.clock.Add(time.Minute)
	h.UpdateStatus(Status{State: StateOffline})
	h.sendDue()
	want := []string{
		"POST ?thread_id=" + testThread + "&wait=true",
		"POST ?thread_id=" + testThread + "&wait=true",
		"PATCH /messages/1289345999000000002?thread_id=" + testThread,
	}
	if got := calls(h.fake.take()); !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLiveStatusIsPostedOnceThenEditedWhenItChanges(t *testing.T) {
	h := newHarness(t, Settings{Webhook: testWebhook(t, ""), LiveStatus: true})
	since := t0.Add(-time.Hour)
	st := Status{State: StateOnline, Since: since, PlayersOnline: 1, MaxPlayers: 20, Players: []string{"alex"}}
	h.UpdateStatus(st)
	h.sendDue()
	const first = "1289345999000000001"
	reqs := h.fake.take()
	if len(reqs) != 1 || reqs[0].Method != http.MethodPost || reqs[0].Query.Get("wait") != "true" || reqs[0].Msg.Flags != flagSuppressNotifications {
		t.Fatalf("the status message must be posted once, without a notification, with wait=true: %q", calls(reqs))
	}
	if !slices.Equal(h.ids, []string{first}) || h.settings.StatusMessageID != first {
		t.Fatalf("the new message's id must be reported: %v", h.ids)
	}
	if e := reqs[0].Msg.Embeds[0]; e.Title != "Online" || !strings.Contains(e.Description, "<t:"+strconv.FormatInt(since.Unix(), 10)+":R>") || e.Timestamp != "2026-09-25T14:00:00Z" {
		t.Errorf("status embed: %+v", e)
	}

	h.clock.Set(t0.Add(10 * time.Second))
	st.PlayersOnline, st.Players = 2, []string{"alex", "Steve"}
	h.UpdateStatus(st)
	h.clock.Set(t0.Add(20 * time.Second))
	st.PlayersOnline, st.Players = 3, []string{"alex", "Steve", "Zed"}
	h.UpdateStatus(st)
	if next := h.sendDue(); !next.Equal(t0.Add(30*time.Second)) || len(h.fake.take()) != 0 {
		t.Fatalf("changes within 30 seconds of the last update must wait until then; next is %v", next)
	}
	h.clock.Set(t0.Add(30 * time.Second))
	h.sendDue()
	reqs = h.fake.take()
	if got := calls(reqs); !slices.Equal(got, []string{"PATCH /messages/" + first}) {
		t.Fatalf("want one edit of the status message, got %q", got)
	}
	e := reqs[0].Msg.Embeds[0]
	if e.Fields[0].Value != "3 of 20" || e.Fields[len(e.Fields)-1].Value != "alex, Steve, Zed" || e.Timestamp != "2026-09-25T14:00:30Z" {
		t.Errorf("the edit must show the latest status: %+v", e)
	}

	h.UpdateStatus(st)
	h.clock.Add(time.Hour)
	if next := h.sendDue(); !next.IsZero() || len(h.fake.take()) != 0 {
		t.Errorf("an unchanged status must not be sent again (next %v)", next)
	}
	h.UpdateStatus(Status{State: StateOffline})
	h.sendDue()
	if reqs = h.fake.take(); len(reqs) != 1 || reqs[0].Method != http.MethodPatch || reqs[0].Msg.Embeds[0].Title != "Offline" {
		t.Errorf("a change long after the last update goes out at once: %q", calls(reqs))
	}
	if !slices.Equal(h.ids, []string{first}) || len(h.deliveries) != 1 {
		t.Errorf("edits report no new id and no change in delivery: %v %+v", h.ids, h.deliveries)
	}
}

func TestSettingsChangesKeepTheStatusMessage(t *testing.T) {
	s := Settings{Webhook: testWebhook(t, ""), Alerts: DefaultAlerts()}
	h := newHarness(t, s)
	h.UpdateStatus(Status{State: StateOnline})
	if next := h.sendDue(); !next.IsZero() || len(h.fake.take()) != 0 {
		t.Fatal("nothing is posted while live status is off")
	}
	s.LiveStatus = true
	h.SetSettings(s)
	h.sendDue()
	if got := calls(h.fake.take()); !slices.Equal(got, []string{"POST ?wait=true"}) {
		t.Fatalf("turning live status on posts the status: %q", got)
	}

	// The dashboard saves the row it read before the id was stored.
	s.Alerts = Alerts(Kinds())
	h.SetSettings(s)
	h.clock.Add(time.Minute)
	h.UpdateStatus(Status{State: StateOffline})
	h.sendDue()
	if got := calls(h.fake.take()); !slices.Equal(got, []string{"PATCH /messages/1289345999000000001"}) {
		t.Errorf("saving other settings must not start a second status message: %q", got)
	}

	s.LiveStatus = false
	h.SetSettings(s)
	h.UpdateStatus(Status{State: StateStarting})
	h.clock.Add(time.Minute)
	if h.sendDue(); len(h.fake.take()) != 0 {
		t.Error("nothing is edited once live status is off")
	}
}

func TestDeletedStatusMessageIsPostedAgain(t *testing.T) {
	const stored, first, second = "1289345999000000777", "1289345999000000001", "1289345999000000002"
	h := newHarness(t, Settings{Webhook: testWebhook(t, ""), LiveStatus: true, StatusMessageID: stored})
	h.UpdateStatus(Status{State: StateStarting})
	h.sendDue()
	if got := calls(h.fake.take()); !slices.Equal(got, []string{"PATCH /messages/" + stored, "POST ?wait=true"}) {
		t.Fatalf("a stored status message that is gone must be posted again: %q", got)
	}
	if !slices.Equal(h.ids, []string{first}) {
		t.Fatalf("the new id must be reported: %v", h.ids)
	}

	h.fake.deleteMessage(first)
	h.clock.Add(time.Minute)
	h.UpdateStatus(Status{State: StateOnline})
	h.sendDue()
	h.clock.Add(time.Minute)
	h.UpdateStatus(Status{State: StateOffline})
	h.sendDue()
	want := []string{"PATCH /messages/" + first, "POST ?wait=true", "PATCH /messages/" + second}
	if got := calls(h.fake.take()); !slices.Equal(got, want) {
		t.Errorf("after the message was deleted in Discord: got %q, want %q", got, want)
	}
	if !slices.Equal(h.ids, []string{first, second}) {
		t.Errorf("ids reported: %v", h.ids)
	}
	if len(h.deliveries) != 1 || h.Delivery().Code != "" {
		t.Errorf("a deleted status message is not a delivery problem: %+v", h.deliveries)
	}
	if !strings.Contains(h.logs.String(), "posting a new one") {
		t.Errorf("log:\n%s", h.logs)
	}
}

func TestGoneWebhookStopsSendingUntilANewOneIsSaved(t *testing.T) {
	h := newHarness(t, Settings{Webhook: testWebhook(t, ""), Alerts: DefaultAlerts(), LiveStatus: true})
	h.UpdateStatus(Status{State: StateOnline})
	h.sendDue()
	oldMessage := h.ids[0]
	h.fake.take()

	h.fake.deleteWebhook(testID)
	h.clock.Add(time.Minute)
	h.Notify(Crashed("", true))
	h.UpdateStatus(Status{State: StateCrashed, Since: h.clock.Now()})
	h.sendDue()
	if got := calls(h.fake.take()); !slices.Equal(got, []string{"POST ?wait=true"}) {
		t.Fatalf("after Discord says the webhook is unknown, nothing more may be sent: %q", got)
	}
	want := Delivery{
		Sent: t0, Failed: t0.Add(time.Minute), Code: CodeWebhookGone, Status: http.StatusNotFound, Stopped: true,
		Msg:  "Discord didn't accept the last message: The webhook no longer exists in Discord; it may have been deleted.",
		Hint: "In Discord, create a new webhook for the channel and paste its URL here.",
	}
	if h.Delivery() != want || h.deliveries[len(h.deliveries)-1] != want {
		t.Errorf("delivery %+v, want %+v", h.Delivery(), want)
	}
	h.Notify(Crashed("", false))
	h.UpdateStatus(Status{State: StateOffline})
	h.clock.Add(time.Hour)
	if next := h.sendDue(); !next.IsZero() || len(h.fake.take()) != 0 {
		t.Errorf("a webhook that is gone must not be used again (next %v)", next)
	}

	newID, newToken := "1289345123456780000", strings.Repeat("Zq3v-9W0b_", 7)
	h.fake.addWebhook(newID, newToken)
	h.SetSettings(Settings{Webhook: testWebhook(t, "https://discord.com/api/webhooks/"+newID+"/"+newToken), Alerts: DefaultAlerts(), LiveStatus: true, StatusMessageID: oldMessage})
	h.Notify(BackupFailed("The disk is full."))
	h.sendDue()
	reqs := h.fake.take()
	if len(reqs) != 2 {
		t.Fatalf("the new webhook gets the alert and a status message of its own: %q", calls(reqs))
	}
	for _, r := range reqs {
		if r.Method != http.MethodPost || r.Path != "/api/v10/webhooks/"+newID+"/"+newToken || r.Query.Get("wait") != "true" {
			t.Errorf("sent %s %s to the new webhook", r.Method, r.Path)
		}
	}
	if d := h.Delivery(); d.Code != "" || d.Stopped || !d.Sent.Equal(h.clock.Now()) {
		t.Errorf("a new webhook starts with a clean delivery: %+v", d)
	}
	if len(h.ids) != 2 || h.ids[1] == oldMessage {
		t.Errorf("ids reported: %v", h.ids)
	}
}

func TestRateLimitIsHonoured(t *testing.T) {
	for _, c := range []struct {
		name  string
		reply fakeReply
		wait  time.Duration
	}{
		{"Discord's example", fakeReply{Status: 429, Header: map[string]string{
			"Retry-After": "65", "X-RateLimit-Limit": "10", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1470173023.123",
			"X-RateLimit-Reset-After": "64.57", "X-RateLimit-Bucket": "abcd1234", "X-RateLimit-Scope": "user",
		}, Body: fixture(t, "rate_limited.json")}, secondsFloat(64.57)},
		{"global", fakeReply{Status: 429, Header: map[string]string{"Retry-After": "65", "X-RateLimit-Global": "true", "X-RateLimit-Scope": "global"},
			Body: `{"message": "You are being rate limited.", "retry_after": 64.57, "global": true}`}, secondsFloat(64.57)},
		{"only a Retry-After header", fakeReply{Status: 429, Header: map[string]string{"Retry-After": "3", "Content-Type": "text/plain"}, Body: "Too Many Requests"}, 3 * time.Second},
		{"retry_after 0", fakeReply{Status: 429, Body: `{"message": "You are being rate limited.", "retry_after": 0, "global": false}`}, time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, Settings{Webhook: testWebhook(t, ""), Alerts: DefaultAlerts()})
			h.fake.reply(c.reply)
			h.Notify(LowDisk(1 << 30))
			next := h.sendDue()
			if want := t0.Add(c.wait); !next.Equal(want) {
				t.Fatalf("next try at %v, want %v", next, want)
			}
			h.clock.Set(next.Add(-time.Millisecond))
			if h.sendDue(); len(h.fake.take()) != 1 {
				t.Fatal("the alert was sent again before Discord's wait was over")
			}
			h.clock.Set(next)
			h.sendDue()
			if reqs := h.fake.take(); len(reqs) != 1 || reqs[0].Msg.Embeds[0].Title != "Low disk space" {
				t.Fatalf("the alert must go out once the wait is over: %q", calls(reqs))
			}
			if want := (Delivery{Sent: next}); h.Delivery() != want || !slices.Equal(h.deliveries, []Delivery{want}) {
				t.Errorf("a rate limit that passes is not a failure: %+v", h.deliveries)
			}
		})
	}
}

func TestLongRateLimitCountsAsAFailure(t *testing.T) {
	h := newHarness(t, Settings{Webhook: testWebhook(t, ""), Alerts: DefaultAlerts(), LiveStatus: true})
	h.fake.reply(fakeReply{Status: 429, Header: map[string]string{"Retry-After": "1337"}, Body: `{"message": "You are being rate limited.", "retry_after": 1336.57, "global": false}`})
	h.Notify(BackupFailed(""))
	h.UpdateStatus(Status{State: StateOnline})
	next := h.sendDue()
	if n := len(h.fake.take()); n != 1 || !next.Equal(t0.Add(secondsFloat(1336.57))) {
		t.Fatalf("nothing more goes out until a 22 minute rate limit is over: %d requests, next at %v", n, next)
	}
	want := Delivery{
		Failed: t0, Code: CodeRateLimited, Status: http.StatusTooManyRequests, RetryAfterSeconds: 1337,
		Msg:  "Discord didn't accept the last message: Discord asked Playkeeper to wait 23 minutes before sending more.",
		Hint: "Messages go out again once the wait is over. If other apps post through the same webhook, give Playkeeper a webhook of its own.",
	}
	if h.Delivery() != want {
		t.Errorf("delivery %+v, want %+v", h.Delivery(), want)
	}
	h.clock.Set(next)
	h.sendDue()
	if reqs := h.fake.take(); len(reqs) != 1 || reqs[0].Msg.Embeds[0].Title != "Online" {
		t.Errorf("the alert is old news by then; only the status message goes out: %q", calls(reqs))
	}
	if d := h.Delivery(); d.Code != "" || !d.Sent.Equal(next) {
		t.Errorf("delivery after the wait: %+v", d)
	}
}

func TestUsedUpRateLimitBucketDelaysTheNextRequest(t *testing.T) {
	h := newHarness(t, Settings{Webhook: testWebhook(t, ""), Alerts: DefaultAlerts()})
	h.fake.reply(fakeReply{Header: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset-After": "1.500"}})
	h.Notify(Crashed("", true))
	h.sendDue()
	h.Notify(BackupFailed(""))
	next := h.sendDue()
	if n := len(h.fake.take()); n != 1 || !next.Equal(t0.Add(1500*time.Millisecond)) {
		t.Fatalf("with the bucket used up, the next request waits for X-RateLimit-Reset-After: %d sent, next at %v", n, next)
	}
	h.clock.Set(next)
	h.sendDue()
	if n := len(h.fake.take()); n != 1 {
		t.Errorf("%d requests once the bucket reset, want 1", n)
	}
}

func TestServerErrorsBackOffThenGiveUp(t *testing.T) {
	h := newHarness(t, Settings{Webhook: testWebhook(t, ""), Alerts: DefaultAlerts()})
	bad := fakeReply{Status: http.StatusBadGateway, Header: map[string]string{"Content-Type": "text/html"}, Body: fixture(t, "bad_gateway.html")}
	h.fake.reply(bad, bad, bad, bad)
	h.Notify(Crashed("", true))
	var waits []time.Duration
	for next := h.sendDue(); !next.IsZero(); next = h.sendDue() {
		waits = append(waits, next.Sub(h.clock.Now()))
		h.clock.Set(next)
	}
	if n := len(h.fake.take()); n != 4 || !slices.Equal(waits, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}) {
		t.Fatalf("%d tries with waits %v, want 4 tries with waits of 2, 4 and 8 seconds", n, waits)
	}
	want := Delivery{
		Failed: t0.Add(14 * time.Second), Code: CodeUnavailable, Status: http.StatusBadGateway,
		Msg:  "Discord didn't accept the last message: Discord had a problem and answered HTTP 502.",
		Hint: "Discord may be having an outage (see discordstatus.com). New alerts go out as usual once it is over.",
	}
	if h.Delivery() != want || !slices.Equal(h.deliveries, []Delivery{want}) {
		t.Errorf("delivery %+v, want %+v", h.deliveries, want)
	}
	h.Notify(BackupFailed(""))
	h.sendDue()
	if n := len(h.fake.take()); n != 1 || h.Delivery().Code != "" || len(h.deliveries) != 2 {
		t.Errorf("the next alert goes out as usual and clears the failure: %d requests, %+v", n, h.deliveries)
	}
}

func TestStatusIsSentAgainAfterAnOutage(t *testing.T) {
	h := newHarness(t, Settings{Webhook: testWebhook(t, ""), LiveStatus: true})
	down := fakeReply{Status: http.StatusServiceUnavailable, Body: `{"message": "503: Service Unavailable", "code": 0}`}
	h.fake.reply(down, down, down, down)
	h.UpdateStatus(Status{State: StateOnline})
	h.settle()
	if n := len(h.fake.take()); n != 5 || len(h.ids) != 1 {
		t.Fatalf("%d requests and ids %v, want 4 failed tries and then the post", n, h.ids)
	}
	if !h.clock.Now().Equal(t0.Add(44 * time.Second)) {
		t.Errorf("after four tries over 14 seconds, the status waits one interval: posted at %v", h.clock.Now())
	}
	if len(h.deliveries) != 2 || h.deliveries[0].Code != CodeUnavailable || h.deliveries[1].Code != "" {
		t.Errorf("deliveries %+v", h.deliveries)
	}
}

func TestPermanentFailuresAreReportedWithoutRetrying(t *testing.T) {
	for _, c := range []struct {
		name    string
		reply   fakeReply
		code    string
		msg     string
		stopped bool
	}{
		{"webhook token reset", fakeReply{Status: 401, Body: fixture(t, "invalid_webhook_token.json")},
			CodeWebhookRejected, "Discord no longer accepts this webhook URL; its secret part was probably reset.", true},
		{"forum channel", fakeReply{Status: 400, Body: fixture(t, "forum_needs_thread.json")},
			CodeNeedsThread, "The webhook belongs to a forum channel, where every message needs a thread.", true},
		{"no access", fakeReply{Status: 403, Body: fixture(t, "missing_access.json")},
			CodeForbidden, "Discord did not let the webhook post in its channel (Missing Access).", false},
		{"invalid form body", fakeReply{Status: 400, Body: fixture(t, "invalid_form_body.json")},
			CodeRejected, "Discord rejected the message (Invalid Form Body: embeds.0.description: Must be 4096 or fewer in length).", false},
		{"too large", fakeReply{Status: 413, Body: `{"message": "Request entity too large", "code": 40005}`},
			CodeRejected, "Discord rejected the message (Request entity too large).", false},
		{"redirect", fakeReply{Status: 302, Header: map[string]string{"Location": "https://evil.example/collect", "Content-Type": "text/html"}},
			CodeUnexpected, "Discord answered in a way Playkeeper did not expect (HTTP 302).", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, Settings{Webhook: testWebhook(t, ""), Alerts: DefaultAlerts()})
			h.fake.reply(c.reply)
			h.Notify(Crashed("", true))
			h.settle()
			if n := len(h.fake.take()); n != 1 {
				t.Errorf("%d requests, want exactly 1: this is not worth retrying", n)
			}
			d := h.Delivery()
			if d.Code != c.code || d.Msg != "Discord didn't accept the last message: "+c.msg || d.Hint == "" || d.Stopped != c.stopped || d.Status != c.reply.Status || !d.Failed.Equal(t0) {
				t.Errorf("delivery %+v, want code %s, stopped %v and message %q", d, c.code, c.stopped, c.msg)
			}
			h.Notify(BackupFailed(""))
			h.settle()
			if n := len(h.fake.take()); c.stopped && n != 0 || !c.stopped && n != 1 {
				t.Errorf("%d requests for the next alert; a stopped webhook gets none, others carry on", n)
			}
		})
	}
}

func TestEditLimitWaitsBeforeEditingAgain(t *testing.T) {
	h := newHarness(t, Settings{Webhook: testWebhook(t, ""), LiveStatus: true})
	h.UpdateStatus(Status{State: StateOnline, PlayersOnline: 1})
	h.sendDue()
	h.fake.take()
	h.clock.Add(2 * time.Hour)
	h.fake.reply(fakeReply{Status: 400, Body: fixture(t, "edit_limit.json")})
	h.UpdateStatus(Status{State: StateOnline, PlayersOnline: 2})
	next := h.sendDue()
	if want := h.clock.Now().Add(10 * time.Minute); !next.Equal(want) {
		t.Fatalf("after Discord refuses more edits, the next one waits 10 minutes: next at %v, want %v", next, want)
	}
	h.clock.Set(next)
	h.sendDue()
	if got := calls(h.fake.take()); !slices.Equal(got, []string{"PATCH /messages/1289345999000000001", "PATCH /messages/1289345999000000001"}) {
		t.Errorf("got %q", got)
	}
	if len(h.deliveries) != 1 || h.Delivery().Code != "" {
		t.Errorf("Discord's edit limit is not a delivery problem: %+v", h.deliveries)
	}
}

func TestRepeatedAlertsAreThrottled(t *testing.T) {
	h := newHarness(t, Settings{Webhook: testWebhook(t, ""), Alerts: Alerts(Kinds())})
	sent := func() []string {
		t.Helper()
		h.sendDue()
		var got []string
		for _, r := range h.fake.take() {
			for _, e := range r.Msg.Embeds {
				got = append(got, strings.TrimSuffix(e.Description, dashboardLine))
			}
		}
		return got
	}
	h.Notify(Crashed("first", true))
	h.clock.Add(time.Minute)
	h.Notify(Crashed("second", true))
	h.Notify(PlayerJoined("Steve"))
	h.Notify(PlayerJoined("steve"))
	h.Notify(PlayerJoined("Alex"))
	h.Notify(Crashed("gave up", false))
	h.Notify(UpdateAvailable("0.4.0"))
	want := []string{
		"**Survival** stopped unexpectedly. Playkeeper is restarting it.\n\nfirst",
		"**Steve** joined **Survival**.",
		"**Alex** joined **Survival**.",
		"**Survival** kept crashing, so Playkeeper stopped restarting it. Open the dashboard to see what went wrong.\n\ngave up",
		"Playkeeper 0.4.0 is available. You can update it from the dashboard.",
	}
	if got := sent(); !slices.Equal(got, want) {
		t.Errorf("within the quiet period, a second alert of the same kind about the same thing is dropped:\n%q\nwant\n%q", got, want)
	}

	h.clock.Set(t0.Add(5 * time.Minute))
	h.Notify(Crashed("third", true))
	h.Notify(PlayerJoined("Steve"))
	h.Notify(UpdateAvailable("0.4.0"))
	h.Notify(UpdateAvailable("0.4.1"))
	want = []string{
		"**Survival** stopped unexpectedly. Playkeeper is restarting it.\n\nthird",
		"Playkeeper 0.4.1 is available. You can update it from the dashboard.",
	}
	if got := sent(); !slices.Equal(got, want) {
		t.Errorf("five minutes after the first crash:\n%q\nwant\n%q", got, want)
	}

	h.clock.Set(t0.Add(6 * time.Minute))
	h.Notify(PlayerJoined("STEVE"))
	if got := sent(); !slices.Equal(got, []string{"**STEVE** joined **Survival**."}) {
		t.Errorf("five minutes after Steve joined: %q", got)
	}
	if !strings.Contains(h.logs.String(), "dropped a repeated alert") {
		t.Errorf("log:\n%s", h.logs)
	}
}

func TestAlertsAreBatchedTenToAMessage(t *testing.T) {
	h := newHarness(t, Settings{Webhook: testWebhook(t, ""), Alerts: Alerts{KindPlayerJoined}})
	for i := range 60 {
		h.Notify(PlayerJoined(fmt.Sprintf("Player%02d", i)))
	}
	h.sendDue()
	var sizes []int
	var players []string
	for _, r := range h.fake.take() {
		sizes = append(sizes, len(r.Msg.Embeds))
		for _, e := range r.Msg.Embeds {
			players = append(players, strings.Fields(e.Description)[0])
		}
	}
	if !slices.Equal(sizes, []int{10, 10, 10, 10, 10}) || players[0] != "**Player00**" || players[49] != "**Player49**" {
		t.Errorf("50 alerts go out in order in messages of 10 embeds, and the 10 beyond the queue's limit are dropped: %v", sizes)
	}
	if !strings.Contains(h.logs.String(), "too many are waiting") {
		t.Errorf("log:\n%s", h.logs)
	}
}

func TestAlertsThatAreOffOrTooOldAreDropped(t *testing.T) {
	h := newHarness(t, Settings{Webhook: testWebhook(t, ""), Alerts: DefaultAlerts()})
	h.Notify(PlayerJoined("Steve"))
	h.Notify(Stopped())
	late := Crashed("", true)
	late.At = t0.Add(-16 * time.Minute)
	h.Notify(late)
	h.Notify(BackupFailed(""))
	h.SetSettings(Settings{Webhook: testWebhook(t, ""), Alerts: Alerts{KindCrash}})
	if next := h.sendDue(); !next.IsZero() || len(h.fake.take()) != 0 {
		t.Errorf("alerts that are off, turned off while waiting, or older than 15 minutes must not be sent (next %v)", next)
	}
	if !strings.Contains(h.logs.String(), "could not be sent in time") {
		t.Errorf("log:\n%s", h.logs)
	}
}

func TestSendTestPostsAConfirmation(t *testing.T) {
	f := newFakeDiscord(t)
	ctx := context.Background()
	if err := SendTest(ctx, f.client(), testWebhook(t, ""), survival); err != nil {
		t.Fatal(err)
	}
	reqs := f.take()
	if got := calls(reqs); !slices.Equal(got, []string{"POST ?wait=true"}) {
		t.Fatalf("got %q", got)
	}
	e := reqs[0].Msg.Embeds[0]
	if e.Title != "Test message" || e.Description != "Discord is connected. Playkeeper will post alerts for **Survival** in this channel."+dashboardLine || e.Timestamp != "" {
		t.Errorf("test message: %+v", e)
	}

	var de *Error
	if err := SendTest(ctx, f.client(), Webhook{}, survival); !errors.As(err, &de) || de.Code != CodeNotConnected {
		t.Errorf("without a webhook: %v", err)
	}
	f.deleteWebhook(testID)
	err := SendTest(ctx, f.client(), testWebhook(t, ""), survival)
	if !errors.As(err, &de) || de.Code != CodeWebhookGone || de.Status != http.StatusNotFound || de.DiscordCode != 10015 || de.Hint == "" {
		t.Errorf("to a deleted webhook: %+v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := SendTest(canceled, f.client(), testWebhook(t, ""), survival); !errors.Is(err, context.Canceled) {
		t.Errorf("after cancelling: %v", err)
	}
	if n := len(f.take()); n != 1 {
		t.Errorf("%d requests, want only the one to the deleted webhook", n)
	}
}

func TestNotifierSendTestReportsDelivery(t *testing.T) {
	h := newHarness(t, Settings{Webhook: testWebhook(t, ""), Alerts: DefaultAlerts()})
	ctx := context.Background()
	h.fake.reply(fakeReply{Header: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset-After": "30"}})
	if err := h.SendTest(ctx); err != nil {
		t.Fatal(err)
	}
	if want := (Delivery{Sent: t0}); h.Delivery() != want || !slices.Equal(h.deliveries, []Delivery{want}) {
		t.Errorf("delivery %+v", h.deliveries)
	}
	var e *Error
	if err := h.SendTest(ctx); !errors.As(err, &e) || e.Code != CodeRateLimited || e.RetryAfter != 30*time.Second || e.Msg != "Discord asked Playkeeper to wait 30 seconds before sending more." {
		t.Errorf("a test while the rate limit is used up is refused without a request: %v", err)
	}
	if n := len(h.fake.take()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}

	h.clock.Add(30 * time.Second)
	h.fake.deleteWebhook(testID)
	if err := h.SendTest(ctx); !errors.As(err, &e) || e.Code != CodeWebhookGone {
		t.Errorf("to a deleted webhook: %v", err)
	}
	if d := h.Delivery(); !d.Stopped || d.Code != CodeWebhookGone {
		t.Errorf("a test that finds the webhook gone stops sending: %+v", d)
	}
	h.fake.take()
	h.Notify(Crashed("", true))
	if h.sendDue(); len(h.fake.take()) != 0 {
		t.Error("alerts must not go to a webhook a test found gone")
	}

	h.SetSettings(Settings{})
	if err := h.SendTest(ctx); !errors.As(err, &e) || e.Code != CodeNotConnected {
		t.Errorf("after disconnecting: %v", err)
	}
}

func TestTokenNeverLeaksIntoErrorsOrLogs(t *testing.T) {
	w := testWebhook(t, "")
	h := newHarness(t, Settings{Webhook: w, Alerts: DefaultAlerts(), LiveStatus: true})
	echo := `{"message": "Unknown route https://discord.com/api/v10/webhooks/` + testID + `/` + testToken + `", "code": 0}`
	h.fake.reply(fakeReply{Status: 400, Body: echo})
	h.Notify(Crashed("", true))
	h.UpdateStatus(Status{State: StateOnline})
	h.settle()
	if len(h.deliveries) == 0 || !strings.Contains(h.deliveries[0].Msg, testID+"/[redacted]") {
		t.Errorf("Discord's explanation is shown with the token redacted: %+v", h.deliveries)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	addr := closed.Listener.Addr().String()
	closed.Close()
	errs := []error{
		SendTest(context.Background(), &http.Client{Transport: toFake{t: t, addr: addr}}, w, survival),
		SendTest(context.Background(), &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("proxy refused %s", r.URL)
		})}, w, survival),
	}
	out := []string{fmt.Sprintf("%v %+v %#v", h.Notifier, h.Notifier, h.Notifier)}
	for _, err := range errs {
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeUnreachable || e.Hint == "" {
			t.Errorf("got %v, want an unreachable error", err)
			continue
		}
		out = append(out, err.Error(), fmt.Sprintf("%+v %#v", e, e))
	}
	if !strings.Contains(errs[1].Error(), testID+"/[redacted]") {
		t.Errorf("the network error is kept, with the token redacted: %v", errs[1])
	}
	for _, s := range out {
		if strings.Contains(s, testToken) {
			t.Errorf("the token leaked into %s", s)
		}
	}
}

func TestRunSendsAsThingsHappenAndStopsWhenCanceled(t *testing.T) {
	f := newFakeDiscord(t)
	posted := make(chan string, 1)
	n := New(Options{
		Settings:        Settings{Webhook: testWebhook(t, ""), Alerts: DefaultAlerts(), LiveStatus: true},
		Server:          survival,
		Client:          f.client(),
		OnStatusMessage: func(id string) { posted <- id },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		n.Run(ctx)
		close(done)
	}()
	n.Notify(Crashed("", true))
	n.UpdateStatus(Status{State: StateOnline})
	if r := f.wait(t); r.Msg.Embeds[0].Title != "Server crashed" {
		t.Errorf("first request: %+v", r.Msg)
	}
	if r := f.wait(t); r.Msg.Embeds[0].Title != "Online" {
		t.Errorf("second request: %+v", r.Msg)
	}
	select {
	case id := <-posted:
		if id != "1289345999000000002" {
			t.Errorf("status message id %q", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the status message id was not reported")
	}
	n.Notify(BackupFailed(""))
	if r := f.wait(t); r.Msg.Embeds[0].Title != "Backup failed" {
		t.Errorf("an idle Run must wake up for a new alert: %+v", r.Msg)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was canceled")
	}
}

func TestOversizedAnswerIsNotRead(t *testing.T) {
	big := `{"id": "1289345999000000001", "pad": "` + strings.Repeat("x", maxBody) + `"}`
	for _, c := range []struct {
		name   string
		header map[string]string
	}{
		{"with Content-Length", map[string]string{"Content-Length": strconv.Itoa(len(big))}},
		{"chunked", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, Settings{Webhook: testWebhook(t, ""), LiveStatus: true})
			h.fake.reply(fakeReply{Status: http.StatusOK, Header: c.header, Body: big})
			h.UpdateStatus(Status{State: StateOnline})
			h.sendDue()
			d := h.Delivery()
			if len(h.ids) != 0 || d.Code != CodeUnexpected || d.Msg != "Discord didn't accept the last message: Discord did not confirm the live status message, so Playkeeper cannot update it." {
				t.Errorf("an answer over 1 MiB must not be read: ids %v, delivery %+v", h.ids, d)
			}
		})
	}
}

func TestRateLimitTimesAreReadSafely(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"65": 65 * time.Second, " 3 ": 3 * time.Second, "0.400": 400 * time.Millisecond, "64.57": secondsFloat(64.57),
		"": 0, "0": 0, "-1": 0, "abc": 0, "NaN": 0, "1e12": maxWait, "Inf": maxWait,
	} {
		if got := seconds(in); got != want {
			t.Errorf("seconds(%q) = %v, want %v", in, got, want)
		}
	}
	if got := secondsFloat(64.57).Round(time.Millisecond); got != 64570*time.Millisecond {
		t.Errorf("64.57 seconds read as %v", got)
	}
	for d, want := range map[time.Duration]string{
		0: "a second", time.Second: "a second", 1500 * time.Millisecond: "2 seconds", 65 * time.Second: "65 seconds",
		secondsFloat(1336.57): "23 minutes", 3 * time.Hour: "3 hours", maxWait: "24 hours",
	} {
		if got := waitText(d); got != want {
			t.Errorf("waitText(%v) = %q, want %q", d, got, want)
		}
	}
}
