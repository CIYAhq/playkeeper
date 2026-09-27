package templates

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
)

const (
	linkVersion = 1
	headerSize  = 7
)

// Link is a template as a link to the share page.
type Link struct {
	// URL is the share page's address with the template after #.
	URL string `json:"url"`
	// Payload is the template's data: what follows # on the share page,
	// and #template= on the create-server page.
	Payload string `json:"payload"`
	// Warning is set when the link is long enough for chat apps to cut it.
	Warning *Notice `json:"warning,omitempty"`
}

// NewLink encodes a valid template as a link. Templates too large for one
// travel as files.
func NewLink(t *Template) (*Link, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	payload, err := encode(t)
	if err != nil {
		return nil, err
	}
	if len(payload) > MaxLinkLength {
		return nil, fail(KindLinkTooLong, kv("length", strconv.Itoa(len(payload)), "limit", strconv.Itoa(MaxLinkLength)),
			"This template is too large for a link.", "Download it as a file and send that instead.")
	}
	l := &Link{URL: ShareURL + "#" + payload, Payload: payload}
	if n := len(l.URL); n > ChatLinkLength {
		w := notice(KindLinkLong, kv("length", strconv.Itoa(n)),
			fmt.Sprintf("This link is %d characters long, and some chat apps cut or refuse links that long.", n),
			"If it does not open for your friends, send them the template file instead.")
		l.Warning = &w
	}
	return l, nil
}

// encode packs a template for a link: the header (format, length, the first
// bytes of the SHA-256) and the raw DEFLATE of its canonical JSON, in
// base64url without padding.
func encode(t *Template) (string, error) {
	js, err := canonicalJSON(t)
	if err != nil {
		return "", err
	}
	var z bytes.Buffer
	w, err := flate.NewWriter(&z, flate.BestCompression)
	if err != nil {
		return "", err
	}
	w.Write(js)
	if err := w.Close(); err != nil {
		return "", err
	}
	body := z.Bytes()
	if len(body) > 0xffff {
		return "", fail(KindLinkTooLong, kv("limit", strconv.Itoa(MaxLinkLength)),
			"This template is too large for a link.", "Download it as a file and send that instead.")
	}
	sum := sha256.Sum256(body)
	raw := make([]byte, 0, headerSize+len(body))
	raw = append(raw, linkVersion, byte(len(body)>>8), byte(len(body)))
	raw = append(raw, sum[:4]...)
	raw = append(raw, body...)
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeLink reads a template from a link to the share page or the
// create-server page, or from the data after # alone. Line breaks and
// spaces from chat apps, and punctuation after the link, are ignored.
func DecodeLink(s string) (*Template, error) {
	payload, e := linkPayload(s)
	if e != nil {
		return nil, e
	}
	js, e := unpack(payload)
	if e != nil {
		return nil, e
	}
	t, e := parse(js, true)
	switch {
	case e == nil:
		return t, nil
	case e.Kind == KindFileDamaged || e.Kind == KindNotTemplate:
		return nil, linkDamaged()
	}
	return nil, e
}

func linkPayload(s string) (string, *Error) {
	if len(s) > 2*MaxLinkLength {
		return "", linkTooLarge()
	}
	s = strings.TrimSpace(s)
	_, frag, hasFrag := strings.Cut(s, "#")
	if hasFrag {
		s = strings.TrimPrefix(frag, "template=")
	} else if strings.HasPrefix(s, ShareURL) {
		return "", incomplete()
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimRightFunc(s, func(r rune) bool { return !isBase64URL(r) })
	switch {
	case s == "" && hasFrag:
		return "", incomplete()
	// The first character is the top of the format byte, so links of
	// formats 0 to 15 start with A to D.
	case s == "" || s[0] < 'A' || s[0] > 'D' || !onlyRunes(s, isBase64URL):
		return "", fail(KindNotTemplate, nil, "This is not a Playkeeper template link.", "Template links start with "+ShareURL+"#.")
	case len(s) > MaxLinkLength:
		return "", linkTooLarge()
	}
	return s, nil
}

// unpack checks a link's data and returns the template's JSON.
func unpack(payload string) ([]byte, *Error) {
	// A link cut at any point can end inside a base64 group.
	if len(payload)%4 == 1 {
		payload = payload[:len(payload)-1]
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, linkDamaged()
	}
	switch {
	case len(raw) == 0:
		return nil, incomplete()
	case raw[0] > linkVersion:
		return nil, newer()
	case raw[0] != linkVersion:
		return nil, linkDamaged()
	case len(raw) < headerSize:
		return nil, incomplete()
	}
	n, body := int(raw[1])<<8|int(raw[2]), raw[headerSize:]
	switch {
	case len(body) < n:
		return nil, incomplete()
	case len(body) > n:
		return nil, linkDamaged()
	}
	if sum := sha256.Sum256(body); !bytes.Equal(sum[:4], raw[3:headerSize]) {
		return nil, linkDamaged()
	}
	return inflate(body)
}

// inflate decompresses a link's template, reading no more than a template
// can be.
func inflate(body []byte) ([]byte, *Error) {
	br := bytes.NewReader(body)
	zr := flate.NewReader(br)
	defer zr.Close()
	js, err := io.ReadAll(io.LimitReader(zr, MaxFileSize+1))
	switch {
	case len(js) > MaxFileSize:
		return nil, linkTooLarge()
	case err != nil || br.Len() > 0:
		return nil, linkDamaged()
	}
	return js, nil
}

func isBase64URL(r rune) bool { return isAlnum(r) || r == '-' || r == '_' }

func incomplete() *Error {
	return fail(KindLinkIncomplete, nil, "This template link is cut off.",
		"Copy the whole link again, or ask whoever shared it for the template file.")
}

func linkDamaged() *Error {
	return fail(KindLinkDamaged, nil, "This template link is damaged.",
		"Copy it again, or ask whoever shared it for a new link or the template file.")
}

func linkTooLarge() *Error {
	return fail(KindTooLarge, kv("limit", strconv.Itoa(MaxLinkLength)), "This link holds more than a template can, so Playkeeper did not open it.",
		"Ask whoever shared it for the template file instead.")
}
