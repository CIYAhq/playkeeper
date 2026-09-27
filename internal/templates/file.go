package templates

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
)

var bom = []byte("\xef\xbb\xbf")

// ParseFile reads a template file.
func ParseFile(data []byte) (*Template, error) {
	if len(data) > MaxFileSize {
		return nil, tooLarge()
	}
	t, e := parse(bytes.TrimPrefix(data, bom), false)
	if e != nil {
		return nil, e
	}
	return t, nil
}

// MarshalFile writes a valid template as a file: indented JSON with a
// final line break.
func MarshalFile(t *Template) ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(t); err != nil {
		return nil, err
	}
	if b.Len() > MaxFileSize {
		// Hundreds of add-ons with long names: without the indentation the
		// template fits, as Validate checks.
		return canonicalJSON(t)
	}
	return b.Bytes(), nil
}

// FileName suggests a name for the template's file, from its name:
// "Survival with friends" becomes survival-with-friends.playkeeper-template.
func FileName(t *Template) string {
	var b strings.Builder
	dash := false
	n := 0
	for _, r := range strings.ToLower(t.Name) {
		if n == 40 {
			break
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
				n++
			}
			b.WriteRune(r)
			n++
			dash = false
		} else {
			dash = true
		}
	}
	if b.Len() == 0 {
		return "server" + FileExtension
	}
	return b.String() + FileExtension
}

// Decode reads what someone uploaded or pasted: a template file, a link, or
// the data after # in one.
func Decode(data []byte) (*Template, error) {
	if len(data) > MaxFileSize {
		return nil, tooLarge()
	}
	text := bytes.TrimLeft(bytes.TrimPrefix(data, bom), " \t\r\n")
	if len(text) > 0 && text[0] == '{' {
		return ParseFile(data)
	}
	return DecodeLink(string(data))
}
