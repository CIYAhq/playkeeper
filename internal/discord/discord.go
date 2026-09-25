// Package discord posts a Minecraft server's alerts and a live status
// message to a Discord channel through a channel webhook. The admin creates
// the webhook in Discord and pastes its URL into Playkeeper; there is no bot
// account and no OAuth.
//
// Everything is per server. The caller makes one Notifier per server from
// the server's Settings, runs it, feeds it events (Notify) and status
// snapshots (UpdateStatus), and stores what it reports back: the id of the
// live status message, and how delivery is going. Requests follow Discord's
// API v10 docs (https://discord.com/developers/docs/resources/webhook):
// Execute Webhook with wait=true, Edit Webhook Message, the embed limits,
// allowed_mentions and the rate limit headers.
package discord

import "time"

// Codes say why something failed. They are stable, so the UI can translate
// them instead of showing Msg.
const (
	CodeInvalidURL      = "invalid_webhook_url"
	CodeInvalidSettings = "invalid_settings"
	CodeNotConnected    = "not_connected"
	CodeWebhookGone     = "webhook_gone"
	CodeWebhookRejected = "webhook_rejected"
	CodeNeedsThread     = "needs_thread"
	CodeForbidden       = "forbidden"
	CodeRejected        = "rejected"
	CodeRateLimited     = "rate_limited"
	CodeUnavailable     = "discord_unavailable"
	CodeUnreachable     = "unreachable"
	CodeUnexpected      = "unexpected_answer"
)

// Reasons say what is wrong with a pasted webhook URL (Error.Reason, with
// CodeInvalidURL).
const (
	ReasonEmpty    = "empty"
	ReasonNotURL   = "not_url"
	ReasonNotHTTPS = "not_https"
	ReasonHost     = "wrong_host"
	ReasonPath     = "not_webhook"
	ReasonID       = "bad_id"
	ReasonToken    = "bad_token"
	ReasonThread   = "bad_thread_id"
)

// Error is a problem with a webhook URL or with sending to Discord. Msg and
// Hint are plain English for the dashboard; neither ever contains the
// webhook's token.
type Error struct {
	Code   string
	Reason string
	Msg    string
	Hint   string
	// Status is Discord's HTTP status and DiscordCode its JSON error code;
	// both are 0 when Discord did not answer.
	Status      int
	DiscordCode int
	// RetryAfter is how long Discord asked Playkeeper to wait
	// (CodeRateLimited).
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Msg }

// stops reports whether the webhook itself no longer works, so nothing more
// should be sent to it until a new webhook URL is saved. Discord's docs ask
// clients not to use a webhook again after a 404.
func (e *Error) stops() bool {
	return e.Code == CodeWebhookGone || e.Code == CodeWebhookRejected || e.Code == CodeNeedsThread
}
