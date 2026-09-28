package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/templates"
)

// formatTemplate writes a template the way site/data/templates keeps them:
// settings and a modpack over several lines, the server and each add-on or
// pack on one, so a bumped version changes one line.
func formatTemplate(t *templates.Template) ([]byte, error) {
	var raw bytes.Buffer
	e := json.NewEncoder(&raw)
	e.SetEscapeHTML(false)
	if err := e.Encode(t); err != nil {
		return nil, err
	}
	d := json.NewDecoder(&raw)
	if tok, err := d.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("a template is a JSON object")
	}
	var b bytes.Buffer
	b.WriteString("{\n")
	for first := true; d.More(); first = false {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return nil, err
		}
		if !first {
			b.WriteString(",\n")
		}
		k, _ := json.Marshal(key)
		fmt.Fprintf(&b, "  %s: ", k)
		switch key {
		case "settings", "modpack":
			if err := json.Indent(&b, v, "  ", "  "); err != nil {
				return nil, err
			}
		case "addons", "resourcePacks", "dataPacks":
			var items []json.RawMessage
			if err := json.Unmarshal(v, &items); err != nil {
				return nil, err
			}
			b.WriteString("[\n")
			for i, it := range items {
				b.WriteString("    " + spaced(it))
				if i < len(items)-1 {
					b.WriteByte(',')
				}
				b.WriteByte('\n')
			}
			b.WriteString("  ]")
		default:
			b.WriteString(spaced(v))
		}
	}
	b.WriteString("\n}\n")
	return b.Bytes(), nil
}

// spaced writes JSON on one line with a space after each colon and comma
// and inside braces: { "a": 1, "b": 2 }.
func spaced(raw json.RawMessage) string {
	var c bytes.Buffer
	if err := json.Compact(&c, raw); err != nil {
		return string(raw)
	}
	s := c.Bytes()
	var out strings.Builder
	inString, escaped := false, false
	for i, ch := range s {
		if inString {
			out.WriteByte(ch)
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
			out.WriteByte(ch)
		case '{':
			if i+1 < len(s) && s[i+1] == '}' {
				out.WriteByte('{')
			} else {
				out.WriteString("{ ")
			}
		case '}':
			if i > 0 && s[i-1] == '{' {
				out.WriteByte('}')
			} else {
				out.WriteString(" }")
			}
		case ',':
			out.WriteString(", ")
		case ':':
			out.WriteString(": ")
		default:
			out.WriteByte(ch)
		}
	}
	return out.String()
}
