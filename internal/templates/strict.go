package templates

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Limits on a template's JSON beyond what Validate checks. The schema is
// small and shallow, so anything past them is not a template.
const (
	maxDepth   = 5
	maxItems   = 256
	maxMembers = 16   // build details
	maxString  = 2048 // bytes
)

// forbiddenKeys and forbiddenParts are keys a template never carries, by
// their letters and digits in lower case: "online-mode" is onlinemode.
// They are refused by name rather than as unknown fields, so a template
// that tries to carry them says so.
var (
	forbiddenKeys = []string{
		"whitelist", "allowlist", "enforcewhitelist", "onlinemode", "op", "ops", "operators", "admins",
		"eula", "eulaaccepted", "ip", "ips", "serverip", "address", "host", "hostname",
		"port", "ports", "serverport", "gameport", "queryport", "enablequery",
		"seed", "levelseed", "world", "worlds", "levelname", "bans", "banned", "bannedplayers", "bannedips", "players", "usercache",
	}
	forbiddenParts = []string{"password", "secret", "token", "command", "rcon", "apikey", "privatekey", "webhook", "credential"}
)

func forbidden(key string) bool {
	var b strings.Builder
	for _, r := range strings.ToLower(key) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	k := b.String()
	return slices.Contains(forbiddenKeys, k) || slices.ContainsFunc(forbiddenParts, func(p string) bool { return strings.Contains(k, p) })
}

// parse reads a template's JSON strictly: the template's own key names,
// exactly and once each, none a template never carries, no nulls, whole
// numbers only, within the limits above; then Validate. A link's JSON must
// also be canonical, what Playkeeper writes; a file may be tidied by hand.
func parse(data []byte, canonical bool) (*Template, *Error) {
	if len(data) > MaxFileSize {
		return nil, tooLarge()
	}
	if e := probe(data); e != nil {
		return nil, e
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	w := walker{dec: dec}
	if e := w.value(reflect.TypeFor[Template](), "", 0); e != nil {
		return nil, e
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, damaged()
	}
	var t Template
	strict := json.NewDecoder(bytes.NewReader(data))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&t); err != nil {
		return nil, fail(KindInvalid, kv("field", "", "problem", "type"), "The template has a value of the wrong kind.", askAgain)
	}
	if !canonical {
		t.tidy()
	}
	if e := t.validate(); e != nil {
		return nil, e
	}
	if canonical {
		if c, err := canonicalJSON(&t); err != nil || !bytes.Equal(c, data) {
			return nil, invalid("", "not_canonical", "The template in this link was not written by Playkeeper.")
		}
	}
	return &t, nil
}

// probe tells templates from other data and finds the format, so that other
// files, damaged ones and newer templates each get their own message.
func probe(data []byte) *Error {
	marked := bytes.Contains(data, []byte(`"playkeeperTemplate"`))
	var top map[string]json.RawMessage
	if !utf8.Valid(data) || json.Unmarshal(data, &top) != nil {
		if marked {
			return damaged()
		}
		return notTemplate()
	}
	raw, ok := top["playkeeperTemplate"]
	if !ok {
		return notTemplate()
	}
	n, err := strconv.ParseInt(string(raw), 10, 32)
	switch {
	case err != nil || n < 1:
		return invalid("playkeeperTemplate", "value", "The template does not say which format it has.")
	case n > Format:
		return newer()
	}
	return nil
}

func notTemplate() *Error {
	return fail(KindNotTemplate, nil, "This is not a Playkeeper template.",
		"Templates are .playkeeper-template files or links that start with "+ShareURL+"#.")
}

func damaged() *Error {
	return fail(KindFileDamaged, nil, "This template file is damaged or incomplete.",
		"Download it again, or ask whoever shared it for a new copy.")
}

func tooLarge() *Error {
	return fail(KindTooLarge, kv("limit", maxFileLabel), "This is larger than the "+maxFileLabel+" a template can be, so Playkeeper did not open it.",
		"Check that it is a Playkeeper template (.playkeeper-template).")
}

type walker struct{ dec *json.Decoder }

// value reads one value of type t, and everything inside it.
func (w walker) value(t reflect.Type, path string, depth int) *Error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	tok, err := w.dec.Token()
	if err != nil {
		return damaged()
	}
	if tok == nil {
		return invalid(path, "null", fmt.Sprintf("The template's %s is empty (null).", label(path)))
	}
	wrong := invalid(path, "type", fmt.Sprintf("The template's %s has a value of the wrong kind.", label(path)))
	switch t.Kind() {
	case reflect.Struct, reflect.Map:
		if tok != json.Delim('{') {
			return wrong
		}
		if depth >= maxDepth {
			return invalid(path, "depth", "The template is nested more deeply than a template can be.")
		}
		return w.object(t, path, depth)
	case reflect.Slice:
		if tok != json.Delim('[') {
			return wrong
		}
		if depth >= maxDepth {
			return invalid(path, "depth", "The template is nested more deeply than a template can be.")
		}
		for i := 0; w.dec.More(); i++ {
			if i == maxItems {
				return invalid(path, "too_many", fmt.Sprintf("The template's %s has more than %d entries.", label(path), maxItems))
			}
			if e := w.value(t.Elem(), fmt.Sprintf("%s[%d]", path, i), depth+1); e != nil {
				return e
			}
		}
		_, err = w.dec.Token()
	case reflect.String:
		s, ok := tok.(string)
		if !ok {
			return wrong
		}
		if len(s) > maxString {
			return invalid(path, "too_long", fmt.Sprintf("The template's %s is longer than a template allows.", label(path)))
		}
	case reflect.Bool:
		if _, ok := tok.(bool); !ok {
			return wrong
		}
	case reflect.Int:
		n, ok := tok.(json.Number)
		if !ok {
			return wrong
		}
		if _, err := strconv.ParseInt(string(n), 10, 32); err != nil {
			return invalid(path, "number", fmt.Sprintf("The template's %s is not a whole number in range.", label(path)))
		}
	default:
		return wrong
	}
	if err != nil {
		return damaged()
	}
	return nil
}

// object reads the members of a struct (by its JSON names) or of a map of
// strings.
func (w walker) object(t reflect.Type, path string, depth int) *Error {
	fields := map[string]reflect.Type{}
	if t.Kind() == reflect.Struct {
		for i := range t.NumField() {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name != "" && name != "-" {
				fields[name] = f.Type
			}
		}
	}
	seen := map[string]bool{}
	for w.dec.More() {
		tok, err := w.dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok {
			return damaged()
		}
		at := key
		if path != "" {
			at = path + "." + key
		}
		if forbidden(key) {
			return fail(KindForbiddenField, kv("field", printable(at)),
				fmt.Sprintf("The template contains \"%s\", which templates never carry.", printable(at)),
				"Templates never hold ports, RCON, the allowlist, operators, secrets, world data or console commands. "+askAgain)
		}
		var ft reflect.Type
		if t.Kind() == reflect.Struct {
			if ft, ok = fields[key]; !ok {
				return fail(KindUnknownField, kv("field", printable(at)),
					fmt.Sprintf("The template contains \"%s\", which Playkeeper does not know.", printable(at)),
					"It may have been changed by hand. "+askAgain)
			}
		} else if len(seen) == maxMembers {
			return invalid(path, "too_many", fmt.Sprintf("The template's %s has more than %d entries.", label(path), maxMembers))
		} else {
			ft = t.Elem()
		}
		if seen[key] {
			return invalid(at, "duplicate", fmt.Sprintf("The template's %s is there twice.", label(at)))
		}
		seen[key] = true
		if e := w.value(ft, at, depth+1); e != nil {
			return e
		}
	}
	if _, err := w.dec.Token(); err != nil {
		return damaged()
	}
	return nil
}

// label names a field in a message.
func label(path string) string {
	if path == "" {
		return "content"
	}
	return "\"" + printable(path) + "\""
}

// canonicalJSON is the one encoding of a template that links carry.
func canonicalJSON(t *Template) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(t); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// tidy undoes harmless edits to a template file: spaces around the name
// and description, and upper-case hashes.
func (t *Template) tidy() {
	t.Name = strings.TrimSpace(t.Name)
	t.Description = strings.TrimSpace(t.Description)
	for i := range t.Addons {
		if p := t.Addons[i].Pin; p != nil {
			p.Hash = strings.ToLower(p.Hash)
		}
	}
	if m := t.Modpack; m != nil {
		m.Pin.Hash = strings.ToLower(m.Pin.Hash)
	}
	for i := range t.Packs {
		t.Packs[i].SHA1 = strings.ToLower(t.Packs[i].SHA1)
		t.Packs[i].SHA256 = strings.ToLower(t.Packs[i].SHA256)
	}
}
