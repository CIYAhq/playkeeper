package site

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Channel is a place visitors come from, like a creator's video or a launch
// post. Its code ends its link, /go/<code>, which adds these UTM tags, and its
// install command, /install/<code>.
type Channel struct {
	Code, Source, Medium, Campaign string
}

// channelCode is what nginx.conf's /install/<code> takes: any such code
// redirects, not only a channel's, so a typo in a command on screen still
// installs.
var channelCode = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// channels are the creators asked for a sponsored video, the launch posts, and
// the Whop page a Meta ad test sends people through.
var channels = []Channel{
	{"cygnus", "youtube", "sponsor", "creators-oct26"},
	{"madhu", "youtube", "sponsor", "creators-oct26"},
	{"kasai", "youtube", "sponsor", "creators-oct26"},
	{"doopa", "youtube", "sponsor", "creators-oct26"},
	{"lth", "youtube", "sponsor", "creators-oct26"},
	{"nicx", "youtube", "sponsor", "creators-oct26"},
	{"linuxbtw", "youtube", "sponsor", "creators-oct26"},
	{"hn", "hackernews", "community", "launch-sep26"},
	{"selfhosted", "reddit", "community", "launch-sep26"},
	{"ph", "producthunt", "community", "launch-sep26"},
	{"x", "x", "social", "launch-sep26"},
	{"whop", "whop", "paid", "pk01-launch"},
}

func checkChannels(cs []Channel) error {
	seen := map[string]bool{}
	for _, c := range cs {
		if !channelCode.MatchString(c.Code) || seen[c.Code] {
			return fmt.Errorf("channel code %q is taken or isn't 1 to 32 of a-z, 0-9 and -", c.Code)
		}
		if c.Source == "" || c.Medium == "" || c.Campaign == "" {
			return fmt.Errorf("channel %s needs a source, a medium and a campaign", c.Code)
		}
		seen[c.Code] = true
	}
	return nil
}

// landing is where a channel's link goes: the landing page with its tags,
// utm_content being the code the page picks the install command by.
func (c Channel) landing() string {
	q := url.QueryEscape
	return fmt.Sprintf("/?utm_source=%s&utm_medium=%s&utm_campaign=%s&utm_content=%s", q(c.Source), q(c.Medium), q(c.Campaign), q(c.Code))
}

// channelLinks is nginx's /go/<code> for each channel, in any case and with or
// without a trailing slash, and the landing page for any other /go/ address.
func channelLinks(cs []Channel) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "location ~* ^/go/%s/?$ {\n    return 302 %s;\n}\n", c.Code, c.landing())
	}
	b.WriteString("location /go/ {\n    return 302 /;\n}\n")
	return b.String()
}

// channelCodes is the codes the landing page shows an install command for.
func channelCodes(cs []Channel) string {
	codes := make([]string, len(cs))
	for i, c := range cs {
		codes[i] = c.Code
	}
	return strings.Join(codes, " ")
}
