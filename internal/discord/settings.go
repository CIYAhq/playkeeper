package discord

import "fmt"

// Settings are one server's Discord settings. They map onto a row keyed by
// the server's id:
//
//	webhook_url        TEXT     Webhook.SecretURL(), "" when not connected
//	alerts             TEXT     Alerts.String()
//	live_status        INTEGER  LiveStatus
//	status_message_id  TEXT     StatusMessageID
//
// webhook_url is a secret: it never goes to the browser.
type Settings struct {
	Webhook Webhook
	Alerts  Alerts
	// LiveStatus keeps one message in the channel up to date with the
	// server's state, players, address and version.
	LiveStatus bool
	// StatusMessageID is the live status message the Notifier posted last
	// ("" before the first). Store what OnStatusMessage reports; New reads it
	// back after a restart, and SetSettings ignores it.
	StatusMessageID string
}

// DefaultSettings are those of a server that has not connected Discord.
func DefaultSettings() Settings { return Settings{Alerts: DefaultAlerts()} }

// Validate checks settings that came from the dashboard.
func (s Settings) Validate() error {
	for _, k := range s.Alerts {
		if !k.Valid() {
			return &Error{Code: CodeInvalidSettings, Msg: fmt.Sprintf("%q is not a kind of alert Playkeeper knows.", clip(oneLine(string(k)), 40))}
		}
	}
	if s.StatusMessageID != "" && !isSnowflake(s.StatusMessageID) {
		return &Error{Code: CodeInvalidSettings, Msg: "The status message id is not a Discord id."}
	}
	return nil
}

// clean is a copy that shares nothing with s, with its alerts in order and
// a status message id that is valid or "".
func (s Settings) clean() Settings {
	s.Alerts = s.Alerts.normal()
	if !isSnowflake(s.StatusMessageID) {
		s.StatusMessageID = ""
	}
	return s
}
