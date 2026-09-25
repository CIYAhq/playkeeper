package addons

import (
	"context"
	"html"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
)

// Details is what the library shows about one add-on: its card, and the
// newest version that runs on the server with the author's notes for it.
type Details struct {
	Card
	// Latest is the newest release that runs on the server, or the newest
	// pre-release when there is no release; nil when no version does.
	Latest *VersionInfo `json:"latest,omitempty"`
	// Notes are the author's notes for Latest as one line of plain text,
	// shortened.
	Notes string `json:"notes,omitempty"`
	// Notice explains a missing Latest, or a pre-release one.
	Notice *Notice `json:"notice,omitempty"`
}

// maxNotes is how many characters of an author's notes Details keeps.
const maxNotes = 280

// Details looks up one add-on for the server. Only the source's API is
// read; nothing is downloaded.
func (l *Library) Details(ctx context.Context, srv Server, src Source, ref string) (*Details, error) {
	t, err := l.check(srv, src, ref, "")
	if err != nil {
		return nil, err
	}
	p, card, err := l.projectCard(ctx, t, src, ref)
	if err != nil {
		return nil, err
	}
	if src == Modrinth {
		card.Author = l.modrinthAuthor(ctx, p.ID)
	}
	d := &Details{Card: *card}
	if p.clientOnly {
		n := clientOnly(p).Notice
		d.Notice = &n
		return d, nil
	}
	cands, err := l.candidates(ctx, t, srv.MinecraftVersion, p)
	if err != nil {
		return nil, err
	}
	c, e := pick(p, cands, false, srv, t)
	switch {
	case e == nil:
	case len(cands) > 0:
		c, d.Notice = cands[0], &e.Notice
	default:
		d.Notice = &e.Notice
		return d, nil
	}
	v := c.info()
	d.Latest, d.Notes = &v, plainNotes(c.notes)
	return d, nil
}

// modrinthAuthor is the project owner's name, which only Modrinth's search
// index carries. It is left out when the search fails.
func (l *Library) modrinthAuthor(ctx context.Context, projectID string) string {
	res, err := l.Modrinth.Search(ctx, modrinth.SearchQuery{Facets: [][]string{modrinth.Facet("project_id", projectID)}, Limit: 1})
	if err != nil {
		return ""
	}
	for _, h := range res.Hits {
		if h.ProjectID == projectID {
			return h.Author
		}
	}
	return ""
}

var (
	reNoteImage   = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	reNoteLink    = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	reNoteBlock   = regexp.MustCompile(`(?i)</?(?:p|br|div|li|ul|ol|h[1-6]|tr|table|blockquote|pre|hr)\b[^>]*>`)
	reNoteTag     = regexp.MustCompile(`<[^>]*>`)
	reNoteBullet  = regexp.MustCompile(`^(?:[-*+•]|\d+[.)])\s+`)
	reNoteMarks   = regexp.MustCompile("\\*\\*|__|~~|`")
	reNoteRule    = regexp.MustCompile(`^[-*_=\s]{3,}$`)
	reNoteSpace   = regexp.MustCompile(`\s+`)
	noteHeadlines = map[string]bool{"changelog": true, "changes": true, "what's new": true, "whats new": true, "release notes": true}
)

// plainNotes turns an author's Markdown or HTML notes into one short line of
// plain text: links keep their words, images, headings and markup go.
func plainNotes(s string) string {
	s = reNoteImage.ReplaceAllString(s, "")
	s = reNoteLink.ReplaceAllString(s, "$1")
	s = reNoteBlock.ReplaceAllString(s, "\n")
	s = reNoteTag.ReplaceAllString(s, "")
	var parts []string
	for line := range strings.Lines(s) {
		line = strings.TrimSpace(html.UnescapeString(line))
		line = strings.TrimSpace(strings.TrimLeft(line, ">"))
		if strings.HasPrefix(line, "#") || reNoteRule.MatchString(line) {
			continue
		}
		line = reNoteBullet.ReplaceAllString(line, "")
		line = strings.TrimSpace(reNoteMarks.ReplaceAllString(line, ""))
		if line == "" || noteHeadlines[strings.ToLower(strings.TrimRight(line, ":"))] {
			continue
		}
		if !strings.ContainsAny(line[len(line)-1:], ".!?:;,") {
			line += "."
		}
		parts = append(parts, line)
	}
	out := reNoteSpace.ReplaceAllString(strings.Join(parts, " "), " ")
	out = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, out)
	if utf8.RuneCountInString(out) <= maxNotes {
		return out
	}
	r := []rune(out)[:maxNotes]
	cut := string(r)
	if i := strings.LastIndexByte(cut, ' '); i > maxNotes/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:") + "…"
}
