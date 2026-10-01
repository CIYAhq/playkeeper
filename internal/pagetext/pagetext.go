// Package pagestream reads the link of the live stream an owner offers on a
// server's public page: a Twitch channel's or a YouTube channel's page,
// which the page links written one way.
package pagestream

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/api"
)

var (
	reTwitchLogin = regexp.MustCompile(`^[A-Za-z0-9_]{4,25}$`)
	reYouTubeID   = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)
)

// Parse is the stream the channel link raw names, with its link written the
// one way the page shows it, or false for anything but a Twitch channel's
// or a YouTube channel's page.
func Parse(raw string) (api.PublicStream, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Port() != "" {
		return api.PublicStream{}, false
	}
	host := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."), "m.")
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	switch {
	case host == "twitch.tv" && len(parts) == 1 && reTwitchLogin.MatchString(parts[0]):
		login := strings.ToLower(parts[0])
		return api.PublicStream{Site: "twitch", Channel: login, URL: "https://www.twitch.tv/" + login}, true
	case host == "youtube.com" && len(parts) == 2 && parts[0] == "channel" && reYouTubeID.MatchString(parts[1]):
		return api.PublicStream{Site: "youtube", Channel: parts[1], URL: "https://www.youtube.com/channel/" + parts[1]}, true
	}
	return api.PublicStream{}, false
}
