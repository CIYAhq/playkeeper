package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/version"
)

// apiBase is Discord's API version 10. Discord doesn't serve v10 on
// discordapp.com, so requests go to discord.com whichever host the pasted
// webhook URL names.
const apiBase = "https://discord.com/api/v10"

const (
	requestTimeout = 20 * time.Second
	maxBody        = 1 << 20
	// maxWait caps any wait that Discord's headers ask for.
	maxWait = 24 * time.Hour
)

// Discord's JSON error codes that change what Playkeeper does.
const (
	errUnknownWebhook   = 10015
	errOldMessageEdits  = 30046
	errForumNeedsThread = 220001
)

// Codes the Notifier handles itself; they never reach the dashboard.
const (
	codeUnknownMessage = "unknown_message"
	codeEditLimit      = "edit_limit"
)

type client struct {
	hc *http.Client
}

// newClient copies hc, or a default client, and refuses redirects: Discord's
// API doesn't redirect, and nothing is sent anywhere but discord.com.
func newClient(hc *http.Client) client {
	c := http.Client{Timeout: requestTimeout}
	if hc != nil {
		c = *hc
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client{hc: &c}
}

// userAgent is in the form Discord's docs require.
func userAgent() string {
	return "DiscordBot (https://github.com/CIYAhq/playkeeper, " + version.Version + ")"
}

// result is the outcome of one request to Discord.
type result struct {
	messageID string
	err       *Error
	// retry says err is worth another try: a rate limit, a problem on
	// Discord's side, or the network.
	retry bool
	// resetAfter is set when the request used up its rate limit: nothing
	// more should go out until it has passed.
	resetAfter time.Duration
	canceled   bool
}

// execute posts a message with wait=true, so that Discord confirms it and
// answers with the message and its id.
func (c client) execute(ctx context.Context, w Webhook, m message) result {
	q := url.Values{"wait": {"true"}}
	if w.threadID != "" {
		q.Set("thread_id", w.threadID)
	}
	return c.do(ctx, w, http.MethodPost, apiBase+"/webhooks/"+w.id+"/"+*w.token+"?"+q.Encode(), m, false)
}

// edit replaces the embeds of a message the webhook posted.
func (c client) edit(ctx context.Context, w Webhook, messageID string, m message) result {
	u := apiBase + "/webhooks/" + w.id + "/" + *w.token + "/messages/" + messageID
	if w.threadID != "" {
		u += "?thread_id=" + w.threadID
	}
	return c.do(ctx, w, http.MethodPatch, u, m, true)
}

func (c client) do(ctx context.Context, w Webhook, method, u string, m message, editing bool) result {
	body, err := json.Marshal(m)
	if err != nil {
		return result{err: unprepared()}
	}
	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, method, u, bytes.NewReader(body))
	if err != nil {
		return result{err: unprepared()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent())
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return result{canceled: true}
		}
		return result{err: unreachable(w, err), retry: true}
	}
	defer resp.Body.Close()
	var r result
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		r.resetAfter = seconds(resp.Header.Get("X-RateLimit-Reset-After"))
	}
	data, complete := readBody(resp)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var sent struct {
			ID string `json:"id"`
		}
		if complete && json.Unmarshal(data, &sent) == nil && isSnowflake(sent.ID) {
			r.messageID = sent.ID
		}
		return r
	}
	var e apiError
	if complete {
		// Not every error body is JSON: Cloudflare answers 5xx with HTML.
		_ = json.Unmarshal(data, &e)
	}
	r.err = classify(resp.StatusCode, e, resp.Header, w, editing)
	r.retry = resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
	return r
}

// readBody reads the body up to maxBody; complete is false if it is larger
// or could not be read.
func readBody(resp *http.Response) (data []byte, complete bool) {
	if resp.ContentLength > maxBody {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	return data, err == nil && len(data) <= maxBody
}

// apiError is Discord's JSON error body, including a 429's retry_after.
type apiError struct {
	Message    string          `json:"message"`
	Code       int             `json:"code"`
	RetryAfter float64         `json:"retry_after"`
	Errors     json.RawMessage `json:"errors"`
}

func classify(status int, b apiError, h http.Header, w Webhook, editing bool) *Error {
	var e *Error
	said := discordSaid(b, w)
	switch {
	case status == http.StatusTooManyRequests:
		wait := secondsFloat(b.RetryAfter)
		if wait == 0 {
			wait = seconds(h.Get("Retry-After"))
		}
		if wait == 0 {
			wait = seconds(h.Get("X-RateLimit-Reset-After"))
		}
		if wait == 0 {
			wait = 5 * time.Second
		}
		e = rateLimited(wait)
	case status >= 500:
		e = &Error{
			Code: CodeUnavailable,
			Msg:  fmt.Sprintf("Discord had a problem and answered HTTP %d.", status),
			Hint: "Discord may be having an outage (see discordstatus.com). New alerts go out as usual once it is over.",
		}
	case status == http.StatusNotFound && editing && b.Code != errUnknownWebhook:
		e = &Error{Code: codeUnknownMessage, Msg: "The live status message no longer exists in Discord."}
	case status == http.StatusNotFound:
		e = &Error{
			Code: CodeWebhookGone,
			Msg:  "The webhook no longer exists in Discord; it may have been deleted.",
			Hint: "In Discord, create a new webhook for the channel and paste its URL here.",
		}
	case status == http.StatusUnauthorized:
		e = &Error{
			Code: CodeWebhookRejected,
			Msg:  "Discord no longer accepts this webhook URL; its secret part was probably reset.",
			Hint: "In Discord, copy the webhook URL again and paste it here.",
		}
	case status == http.StatusForbidden:
		e = &Error{
			Code: CodeForbidden,
			Msg:  "Discord did not let the webhook post in its channel" + said + ".",
			Hint: "In Discord, check that the webhook's channel still exists and that the webhook may post there.",
		}
	case b.Code == errForumNeedsThread:
		e = &Error{
			Code: CodeNeedsThread,
			Msg:  "The webhook belongs to a forum channel, where every message needs a thread.",
			Hint: "Create the webhook in a text channel instead, or add ?thread_id= and the id of a thread to the URL.",
		}
	case b.Code == errOldMessageEdits:
		e = &Error{Code: codeEditLimit, Msg: "Discord limits edits to messages older than an hour."}
	case status >= 400:
		e = &Error{
			Code: CodeRejected,
			Msg:  "Discord rejected the message" + said + ".",
			Hint: "This looks like a bug in Playkeeper. Please report it with this message.",
		}
	default:
		e = &Error{
			Code: CodeUnexpected,
			Msg:  fmt.Sprintf("Discord answered in a way Playkeeper did not expect (HTTP %d).", status),
			Hint: "Try again later. If it keeps happening, please report it.",
		}
	}
	e.Status, e.DiscordCode = status, b.Code
	return e
}

func rateLimited(wait time.Duration) *Error {
	return &Error{
		Code: CodeRateLimited, RetryAfter: wait,
		Msg:  "Discord asked Playkeeper to wait " + waitText(wait) + " before sending more.",
		Hint: "Messages go out again once the wait is over. If other apps post through the same webhook, give Playkeeper a webhook of its own.",
	}
}

func unreachable(w Webhook, err error) *Error {
	// A *url.Error repeats the request URL, token included.
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	cause := "no answer in time"
	var ne net.Error
	if !errors.Is(err, context.DeadlineExceeded) && !(errors.As(err, &ne) && ne.Timeout()) {
		cause = clip(oneLine(w.redact(err.Error())), 200)
	}
	return &Error{
		Code: CodeUnreachable,
		Msg:  "Playkeeper could not reach Discord (" + cause + ").",
		Hint: "Check that this machine can reach discord.com over HTTPS.",
	}
}

func unprepared() *Error {
	return &Error{Code: CodeUnexpected, Msg: "Playkeeper could not prepare the message for Discord.", Hint: "Please report this."}
}

func errNotConnected() *Error {
	return &Error{Code: CodeNotConnected, Msg: "Discord is not connected.", Hint: "Paste a Discord webhook URL first."}
}

// discordSaid is Discord's own explanation as " (…)", or "".
func discordSaid(b apiError, w Webhook) string {
	msg := b.Message
	if d := formError(b.Errors); d != "" {
		msg += ": " + d
	}
	if msg = clip(oneLine(w.redact(msg)), 300); msg == "" {
		return ""
	}
	return " (" + strings.TrimSuffix(msg, ".") + ")"
}

// formError finds the first detail in the errors object of Discord's
// "Invalid Form Body" answers, such as
// "embeds.0.description: Must be 4096 or fewer in length."
func formError(raw json.RawMessage) string {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return firstFormError(v, "")
}

func firstFormError(v any, path string) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if list, ok := m["_errors"].([]any); ok && len(list) > 0 {
		if e, ok := list[0].(map[string]any); ok {
			if msg, ok := e["message"].(string); ok && msg != "" {
				if path == "" {
					return msg
				}
				return path + ": " + msg
			}
		}
	}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if k == "_errors" {
			continue
		}
		p := k
		if path != "" {
			p = path + "." + k
		}
		if s := firstFormError(m[k], p); s != "" {
			return s
		}
	}
	return ""
}

// seconds reads a header value in seconds, such as "64.57"; 0 if it is
// missing or not a positive number.
func seconds(v string) time.Duration {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0
	}
	return secondsFloat(f)
}

func secondsFloat(f float64) time.Duration {
	switch {
	case math.IsNaN(f) || f <= 0:
		return 0
	case f >= maxWait.Seconds():
		return maxWait
	}
	return time.Duration(f * float64(time.Second))
}

func waitText(d time.Duration) string {
	switch s := int(math.Ceil(d.Seconds())); {
	case s <= 1:
		return "a second"
	case s < 120:
		return strconv.Itoa(s) + " seconds"
	case s < 2*3600:
		return strconv.Itoa((s+59)/60) + " minutes"
	default:
		return strconv.Itoa((s+3599)/3600) + " hours"
	}
}

// SendTest posts a test message to w now, outside any queue, and returns
// Discord's answer as an *Error (nil when it went through). It serves the
// settings screen's "Send a test message", also for a webhook URL that has
// not been saved yet. Notifier.SendTest does the same for a saved one.
func SendTest(ctx context.Context, hc *http.Client, w Webhook, info ServerInfo) error {
	if w.IsZero() {
		return errNotConnected()
	}
	r := newClient(hc).execute(ctx, w, newMessage(testEmbed(info)))
	switch {
	case r.canceled:
		return ctx.Err()
	case r.err != nil:
		return r.err
	}
	return nil
}
