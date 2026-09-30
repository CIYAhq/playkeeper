package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/pagestream"
)

// What the owner adds to the public page: their words for it (About), a
// live stream it offers to play, and a status board their tools post
// through the API or MCP. Everything is plain text the page shows as it
// is, within the bounds in api.

// plainSpaces is s with tabs and other kinds of space (a no-break space
// pasted from a document, say) as plain spaces.
func plainSpaces(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || unicode.Is(unicode.Zs, r) {
			return ' '
		}
		return r
	}, s)
}

// printable is whether r shows as text. The zero-width joiner that builds
// emoji counts; control and direction-changing characters don't.
func printable(r rune) bool {
	return unicode.IsPrint(r) || r == '\u200d'
}

// validAbout is the owner's words for the page, tidied: plain text, line
// breaks kept, at most two in a row.
func validAbout(s string) (string, error) {
	s = strings.TrimSpace(plainSpaces(strings.ReplaceAll(s, "\r\n", "\n")))
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	if utf8.RuneCountInString(s) > api.PublicAboutMax {
		return "", errInvalid("The page's About text can be at most %d characters.", api.PublicAboutMax)
	}
	if strings.Count(s, "\n")+1 > api.PublicAboutLines {
		return "", errInvalid("The page's About text can have at most %d lines.", api.PublicAboutLines)
	}
	for _, r := range s {
		if r != '\n' && !printable(r) {
			return "", errInvalid("The page's About text may not contain control characters.")
		}
	}
	return s, nil
}

// errStreamLink says which links make a stream.
func errStreamLink() error {
	return &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Msg: "That isn't a Twitch or YouTube channel link.",
		Hint: "Paste your Twitch channel (twitch.tv/yourname) or your YouTube channel's link with its ID (youtube.com/channel/UC…)."}
}

// parseStream is the stream a channel link names, and its link written the
// one way the page stores it. An empty link is no stream.
func parseStream(raw string) (api.PublicStream, error) {
	if strings.TrimSpace(raw) == "" {
		return api.PublicStream{}, nil
	}
	st, ok := pagestream.Parse(raw)
	if !ok {
		return api.PublicStream{}, errStreamLink()
	}
	return st, nil
}

// boardLine is one line of a board: trimmed, printable, between 1 and max
// characters, or empty when optional.
func boardLine(what, s string, max int, optional bool) (string, error) {
	s = strings.TrimSpace(plainSpaces(s))
	if s == "" {
		if optional {
			return "", nil
		}
		return "", errInvalid("A board's %s can't be empty.", what)
	}
	if utf8.RuneCountInString(s) > max {
		return "", errInvalid("A board's %s can be at most %d characters.", what, max)
	}
	for _, r := range s {
		if !printable(r) {
			return "", errInvalid("A board's %s must be one line of text.", what)
		}
	}
	return s, nil
}

// validBoard is the board req posts, as the page shows it.
func (a *Agent) validBoard(req api.PublicBoardRequest) (api.PublicBoard, error) {
	now := a.now().UTC()
	b := api.PublicBoard{Live: req.Live, UpdatedAt: now}
	var err error
	if b.Headline, err = boardLine("headline", req.Headline, api.BoardHeadlineMax, true); err != nil {
		return b, err
	}
	if len(req.Stats) > api.BoardStatsMax {
		return b, errInvalid("A board can show at most %d numbers.", api.BoardStatsMax)
	}
	if len(req.Checklist) > api.BoardItemsMax {
		return b, errInvalid("A board's checklist can have at most %d items.", api.BoardItemsMax)
	}
	seen := map[string]bool{}
	for _, st := range req.Stats {
		var out api.BoardStat
		if out.Label, err = boardLine("number's label", st.Label, api.BoardStatLabelMax, false); err != nil {
			return b, err
		}
		if out.Value, err = boardLine("number", st.Value, api.BoardStatValueMax, false); err != nil {
			return b, err
		}
		if seen["s "+strings.ToLower(out.Label)] {
			return b, errInvalid("A board names %q twice.", out.Label)
		}
		seen["s "+strings.ToLower(out.Label)] = true
		b.Stats = append(b.Stats, out)
	}
	for _, it := range req.Checklist {
		label, err := boardLine("checklist item", it.Label, api.BoardItemLabelMax, false)
		if err != nil {
			return b, err
		}
		if seen["c "+strings.ToLower(label)] {
			return b, errInvalid("A board's checklist names %q twice.", label)
		}
		seen["c "+strings.ToLower(label)] = true
		b.Checklist = append(b.Checklist, api.BoardItem{Label: label, Done: it.Done})
	}
	if req.Next != nil {
		next := req.Next.UTC()
		if next.Before(now.Add(-24*time.Hour)) || next.After(now.Add(api.BoardNextWithin)) {
			return b, errInvalid("The next session must start within %d days.", int(api.BoardNextWithin.Hours()/24))
		}
		b.Next = &next
	}
	return b, nil
}

// publicBoard is the board the owner's tools last posted, or nil.
func (s *server) publicBoard() *api.PublicBoard {
	var raw string
	if err := s.db.QueryRow(`SELECT public_board FROM servers WHERE id = ?`, s.id).Scan(&raw); err != nil || raw == "" {
		return nil
	}
	var b api.PublicBoard
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		s.log.Warn("the public page's board can't be read", "server", s.id, "err", err)
		return nil
	}
	return &b
}

// hPublicBoardSet posts the server's status board, in place of the last.
// Only posting one where there was none is audited: a board changes as
// often as its tools post, like a scoreboard.
func (s *server) hPublicBoardSet(w http.ResponseWriter, r *http.Request) {
	var req api.PublicBoardRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	b, err := s.validBoard(req)
	if err != nil {
		writeError(w, err)
		return
	}
	raw, err := json.Marshal(b)
	if err != nil {
		writeError(w, err)
		return
	}
	had := s.publicBoard() != nil
	if _, err := s.db.Exec(`UPDATE servers SET public_board = ? WHERE id = ?`, string(raw), s.id); err != nil {
		writeError(w, err)
		return
	}
	if !had {
		s.audit(actor, "public_page.board_posted", "public_page", "changed", b.Headline)
	}
	writeJSON(w, http.StatusOK, b)
}

// hPublicBoardClear takes the status board off the page.
func (s *server) hPublicBoardClear(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	res, err := s.db.Exec(`UPDATE servers SET public_board = '' WHERE id = ? AND public_board != ''`, s.id)
	if err != nil {
		writeError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.audit(actor, "public_page.board_cleared", "public_page", "changed", "")
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}

// pageText is the About text and stream link req sets, checked and written
// the way the page stores them (nil for one req leaves alone), and what
// changed for the audit log. Both are checked before either is saved.
func pageText(req api.PublicPageRequest) (about, stream *string, changed []string, err error) {
	if req.About != nil {
		a, err := validAbout(*req.About)
		if err != nil {
			return nil, nil, nil, err
		}
		about = &a
		if a == "" {
			changed = append(changed, "about cleared")
		} else {
			changed = append(changed, "about set")
		}
	}
	if req.Stream != nil {
		st, err := parseStream(*req.Stream)
		if err != nil {
			return nil, nil, nil, err
		}
		stream = &st.URL
		changed = append(changed, "stream "+nonEmptyOr(st.URL, "off"))
	}
	return about, stream, changed, nil
}

// pageStream is the stream the page offers to play, or nil.
func (s *server) pageStream(link string) *api.PublicStream {
	if link == "" {
		return nil
	}
	st, err := parseStream(link)
	if err != nil {
		var ae *apiError
		if !errors.As(err, &ae) {
			s.log.Warn("the public page's stream link can't be read", "server", s.id, "err", err)
		}
		return nil
	}
	return &st
}
