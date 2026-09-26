package discord

import (
	"encoding/json"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// webhookHosts are the API hosts in Discord's docs, plus discordapp.com, the
// old domain that webhook URLs copied before 2020 still carry. Requests
// always go to discord.com, whichever host the pasted URL names.
var webhookHosts = map[string]bool{
	"discord.com":        true,
	"canary.discord.com": true,
	"ptb.discord.com":    true,
	"discordapp.com":     true,
}

var (
	reWebhookPath = regexp.MustCompile(`^/api(?:/v[0-9]{1,2})?/webhooks/([^/]+)/([^/]+)/?$`)
	reSnowflake   = regexp.MustCompile(`^[0-9]{17,20}$`)
	// Webhook tokens are 68 characters of URL-safe base64 today; webhooks
	// from before 2020 have 100 hex digits.
	reToken = regexp.MustCompile(`^[A-Za-z0-9_-]{60,100}$`)
)

// isSnowflake reports whether s is a Discord id: a 64-bit number, which has
// at least 17 digits for anything made since 2015.
func isSnowflake(s string) bool {
	if !reSnowflake.MatchString(s) {
		return false
	}
	_, err := strconv.ParseUint(s, 10, 64)
	return err == nil
}

// A Webhook is a Discord channel webhook: the webhook's id plus the secret
// token that lets anyone who has it post in the channel, and optionally a
// thread of that channel to post in. The zero Webhook means Discord is not
// connected.
//
// The token leaves the package only through SecretURL. String, GoString,
// LogValue and MarshalJSON leave it out.
type Webhook struct {
	id       string
	threadID string
	// token is a pointer so that fmt prints an address rather than the token
	// when a Webhook sits in an unexported field of a struct being printed.
	token *string
}

// ParseWebhookURL checks a webhook URL pasted from Discord (a channel's
// settings, Integrations, Webhooks, Copy Webhook URL). It accepts
// discord.com, its canary and ptb hosts and the old discordapp.com domain,
// paths with or without an API version, and an optional thread_id query
// parameter naming a thread in the webhook's channel. The error never
// repeats the URL, since the URL contains the token.
func ParseWebhookURL(raw string) (Webhook, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Webhook{}, urlError(ReasonEmpty, "No webhook URL was given.")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		if !strings.Contains(raw, "://") {
			return Webhook{}, urlError(ReasonNotHTTPS, "A Discord webhook URL starts with https://.")
		}
		return Webhook{}, urlError(ReasonNotURL, "That is not a web address.")
	}
	if u.Scheme != "https" {
		return Webhook{}, urlError(ReasonNotHTTPS, "A Discord webhook URL starts with https://.")
	}
	if u.User != nil || !webhookHosts[strings.ToLower(u.Hostname())] || (u.Port() != "" && u.Port() != "443") {
		return Webhook{}, urlError(ReasonHost, "That address is not on discord.com, so it is not a Discord webhook URL.")
	}
	m := reWebhookPath.FindStringSubmatch(u.Path)
	if m == nil {
		return Webhook{}, urlError(ReasonPath, "That address is on Discord but is not a webhook URL.")
	}
	if !isSnowflake(m[1]) {
		return Webhook{}, urlError(ReasonID, "The webhook URL is damaged: the number after /webhooks/ is not a Discord id.")
	}
	if !reToken.MatchString(m[2]) {
		return Webhook{}, urlError(ReasonToken, "The webhook URL looks cut off or changed: its secret last part is not valid.")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Webhook{}, urlError(ReasonNotURL, "That is not a web address.")
	}
	thread := q.Get("thread_id")
	if thread != "" && !isSnowflake(thread) {
		return Webhook{}, urlError(ReasonThread, "The thread_id in the webhook URL is not a Discord id.")
	}
	token := m[2]
	return Webhook{id: m[1], threadID: thread, token: &token}, nil
}

func urlError(reason, msg string) *Error {
	return &Error{
		Code: CodeInvalidURL, Reason: reason, Msg: msg,
		Hint: "In Discord, open the channel's settings, choose Integrations, then Webhooks, and use Copy Webhook URL.",
	}
}

// IsZero reports whether no webhook is set.
func (w Webhook) IsZero() bool { return w.token == nil }

// ID is the webhook's id. It is not a secret.
func (w Webhook) ID() string { return w.id }

// ThreadID is the thread messages go to, or "" for the channel itself.
func (w Webhook) ThreadID() string { return w.threadID }

// SecretURL is the webhook URL to store, in a canonical form. It contains the
// token: keep it out of logs, error messages and anything sent to a browser.
func (w Webhook) SecretURL() string {
	if w.IsZero() {
		return ""
	}
	return w.url(*w.token)
}

// String is the webhook URL with the token replaced by [redacted].
func (w Webhook) String() string {
	if w.IsZero() {
		return ""
	}
	return w.url("[redacted]")
}

func (w Webhook) url(token string) string {
	s := "https://discord.com/api/webhooks/" + w.id + "/" + token
	if w.threadID != "" {
		s += "?thread_id=" + w.threadID
	}
	return s
}

// GoString keeps the token out of %#v.
func (w Webhook) GoString() string { return "discord.Webhook(" + strconv.Quote(w.String()) + ")" }

// LogValue keeps the token out of slog output.
func (w Webhook) LogValue() slog.Value { return slog.StringValue(w.String()) }

// MarshalJSON writes only the webhook's id and thread, or null: JSON never
// carries the token, so store SecretURL in a column of its own.
func (w Webhook) MarshalJSON() ([]byte, error) {
	if w.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId,omitempty"`
	}{w.id, w.threadID})
}

// Equal reports whether w and o are the same webhook, token and thread.
func (w Webhook) Equal(o Webhook) bool {
	if w.IsZero() || o.IsZero() {
		return w.IsZero() == o.IsZero()
	}
	return w.id == o.id && w.threadID == o.threadID && *w.token == *o.token
}

// redact removes the token from s, for text that may have picked it up
// from a request URL.
func (w Webhook) redact(s string) string {
	if w.IsZero() {
		return s
	}
	return strings.ReplaceAll(s, *w.token, "[redacted]")
}
