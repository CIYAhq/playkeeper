package discord

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"
)

// Limits from Discord's message docs (Embed Limits) and Execute Webhook.
// Discord counts characters; counting UTF-16 code units, as textLen does,
// never counts fewer.
const (
	maxEmbeds      = 10
	maxTitle       = 256
	maxDescription = 4096
	maxFields      = 25
	maxFieldName   = 256
	maxFieldValue  = 1024
	maxFooter      = 2048
	maxAuthorName  = 256
	maxEmbedsTotal = 6000
)

// flagSuppressNotifications posts a message without a push or desktop
// notification. Execute Webhook accepts it; Edit Webhook Message does not.
const flagSuppressNotifications = 1 << 12

// Playkeeper's colours, from the dashboard's design tokens.
const (
	colorGreen = 0x15803D // --primary, the brand green
	colorAmber = 0xF59E0B // --warning
	colorGrey  = 0x5C6157 // --muted-foreground
	colorRed   = 0xEF4444 // --destructive
)

// message is the JSON body of Execute Webhook and Edit Webhook Message.
// Playkeeper sends embeds only, never content.
type message struct {
	Embeds          []embed         `json:"embeds"`
	AllowedMentions allowedMentions `json:"allowed_mentions"`
	Flags           int             `json:"flags,omitempty"`
}

// allowedMentions always goes out with an empty parse list, which allows no
// mentions at all. Leaving it out would mean Discord's defaults, and an edit
// without it re-parses mentions with those defaults.
type allowedMentions struct {
	Parse []string `json:"parse"`
}

// embed leaves out url: Discord shows only the first of several embeds with
// the same url in one message, and batched alerts would all link to the
// same dashboard.
type embed struct {
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description,omitempty"`
	Timestamp   string       `json:"timestamp,omitempty"`
	Color       int          `json:"color"`
	Author      *embedAuthor `json:"author,omitempty"`
	Fields      []embedField `json:"fields,omitempty"`
	Footer      *embedFooter `json:"footer,omitempty"`
}

type embedAuthor struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

type embedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type embedFooter struct {
	Text string `json:"text"`
}

func newMessage(embeds ...embed) message {
	return message{Embeds: embeds, AllowedMentions: allowedMentions{Parse: []string{}}}
}

// fit makes the embed meet Discord's limits: every text within its field's
// limit, at most 25 fields, and no more than 6000 characters in all.
func (e *embed) fit() {
	e.Title = clip(e.Title, maxTitle)
	e.Description = clip(e.Description, maxDescription)
	if e.Author != nil {
		if e.Author.Name = clip(e.Author.Name, maxAuthorName); e.Author.Name == "" {
			e.Author = nil
		}
	}
	if e.Footer != nil {
		if e.Footer.Text = clip(e.Footer.Text, maxFooter); e.Footer.Text == "" {
			e.Footer = nil
		}
	}
	fields := e.Fields[:0]
	for _, f := range e.Fields {
		f.Name, f.Value = clip(f.Name, maxFieldName), clip(f.Value, maxFieldValue)
		if f.Name != "" && f.Value != "" && len(fields) < maxFields {
			fields = append(fields, f)
		}
	}
	e.Fields = fields
	for len(e.Fields) > 0 && e.size() > maxEmbedsTotal {
		e.Fields = e.Fields[:len(e.Fields)-1]
	}
	if over := e.size() - maxEmbedsTotal; over > 0 {
		e.Description = clip(e.Description, textLen(e.Description)-over)
	}
}

// size is what the embed counts towards the 6000 characters a message's
// embeds may have together.
func (e embed) size() int {
	n := textLen(e.Title) + textLen(e.Description)
	for _, f := range e.Fields {
		n += textLen(f.Name) + textLen(f.Value)
	}
	if e.Author != nil {
		n += textLen(e.Author.Name)
	}
	if e.Footer != nil {
		n += textLen(e.Footer.Text)
	}
	return n
}

// key identifies what the embed shows, ignoring its timestamp.
func (e embed) key() string {
	e.Timestamp = ""
	b, _ := json.Marshal(e)
	return string(b)
}

func textLen(s string) int {
	n := 0
	for _, r := range s {
		n += max(utf16.RuneLen(r), 1)
	}
	return n
}

// clip trims s and shortens it to at most limit characters, ending with "…"
// when it cuts. It never leaves a lone backslash at the end, where it would
// escape the "…".
func clip(s string, limit int) string {
	s = strings.TrimSpace(s)
	if textLen(s) <= limit {
		return s
	}
	if limit < 1 {
		return ""
	}
	n, cut := 0, 0
	for i, r := range s {
		w := max(utf16.RuneLen(r), 1)
		if n+w > limit-1 {
			cut = i
			break
		}
		n += w
	}
	s = strings.TrimRightFunc(s[:cut], unicode.IsSpace)
	if trailing := len(s) - len(strings.TrimRight(s, `\`)); trailing%2 == 1 {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// oneLine makes user text safe to show on one line: valid UTF-8, no control
// or text-direction characters, whitespace runs turned into single spaces.
func oneLine(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r) || unicode.IsControl(r):
			space = true
			continue
		case isBidiControl(r):
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// isBidiControl reports the invisible characters that reverse or isolate
// the direction of the text around them, which can disguise a name.
func isBidiControl(r rune) bool {
	return r == '\u061C' || r == '\u200E' || r == '\u200F' || (r >= '\u202A' && r <= '\u202E') || (r >= '\u2066' && r <= '\u2069')
}

// stripFormatting removes Minecraft's § colour and style codes.
func stripFormatting(s string) string {
	if !strings.ContainsRune(s, '§') {
		return s
	}
	var b strings.Builder
	skip := false
	for _, r := range s {
		switch {
		case skip:
			skip = false
		case r == '§':
			skip = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// markdownSpecial are the characters Discord's markdown gives a meaning to:
// formatting, quotes, headings and lists, links (including bare https://
// ones), and @ and < for mentions, emoji and timestamps.
const markdownSpecial = "\\*_~`|<>#-[]():@"

// escape makes text show literally in Discord markdown by putting a
// backslash before every special character; Discord shows a backslash
// before punctuation as just the punctuation.
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(markdownSpecial, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// userText prepares a name or other text from users or the server for an
// embed: cleaned to one line, shortened to limit, and escaped.
func userText(s string, limit int) string {
	return escape(clip(oneLine(stripFormatting(s)), limit))
}

// dashboardLink checks a link to Playkeeper's dashboard: https, with a host,
// and nothing that could end a markdown link early. It returns "" for
// anything else.
func dashboardLink(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(raw) > 512 {
		return ""
	}
	s := (&url.URL{Scheme: "https", Host: u.Host, Path: u.Path}).String()
	return strings.NewReplacer("(", "%28", ")", "%29").Replace(s)
}

// reAddress is a join address as Playkeeper shows it: a host name or IPv4
// address, or an IPv6 address in brackets, with an optional port.
var reAddress = regexp.MustCompile(`^(?:[A-Za-z0-9.-]{1,253}|\[[0-9A-Fa-f:.]{2,45}\])(?::[0-9]{1,5})?$`)

// joinAddress is the address as inline code, or "" if it is not a plain
// host and port.
func joinAddress(addr string) string {
	addr = strings.TrimSpace(addr)
	if !reAddress.MatchString(addr) {
		return ""
	}
	return "`" + addr + "`"
}
