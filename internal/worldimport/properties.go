package worldimport

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Setting is a server.properties value an import carries over to the new
// server.
type Setting struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

// Where a carried setting comes from.
const (
	SourceLevel      = "level.dat"
	SourceProperties = "server.properties"
)

const maxListedKeys = 64

// carriedKeys are the only settings an import carries over, in the order
// the preview lists them. Everything else in the old server's
// server.properties belongs to the old host.
func carriedKeys() []string { return []string{"level-seed", "gamemode", "difficulty", "hardcore"} }

func carried(key string) bool {
	switch key {
	case "level-seed", "gamemode", "difficulty", "hardcore":
		return true
	}
	return false
}

// neverCarried are settings that control access to the server or how it is
// reached. They are never copied, and the preview says so.
func neverCarried(key string) bool {
	switch key {
	case "enable-rcon", "broadcast-rcon-to-ops", "server-port", "server-ip", "enable-query",
		"online-mode", "white-list", "enforce-whitelist", "level-name", "enable-jmx-monitoring",
		"prevent-proxy-connections":
		return true
	}
	return strings.HasPrefix(key, "rcon.") || strings.HasPrefix(key, "query.") || strings.HasPrefix(key, "management-server-")
}

// CarrySettings picks the settings worth carrying from an imported world
// to its new server: the seed, game mode, difficulty and hardcore flag,
// taken from level.dat where it has them and from the old server.properties
// otherwise. refused lists the keys of the old file that are never copied,
// such as RCON, ports, online-mode and the whitelist; ignored lists the
// other keys that stay with the old host. Values of refused keys are never
// returned.
func CarrySettings(imported []byte, lv *Level) (carry []Setting, refused, ignored []string) {
	old := map[string]string{}
	for _, p := range parseProperties(imported) {
		old[p.key] = p.value
	}
	fromLevel := map[string]string{}
	if lv != nil {
		fromLevel["level-seed"] = lv.Seed
		fromLevel["gamemode"] = lv.GameMode
		fromLevel["difficulty"] = lv.Difficulty
		fromLevel["hardcore"] = strconv.FormatBool(lv.Hardcore)
	}
	for _, key := range carriedKeys() {
		if v, ok := normalizeSetting(key, fromLevel[key]); ok {
			carry = append(carry, Setting{Key: key, Value: v, Source: SourceLevel})
		} else if v, ok := normalizeSetting(key, old[key]); ok {
			carry = append(carry, Setting{Key: key, Value: v, Source: SourceProperties})
		}
	}
	keys := make([]string, 0, len(old))
	for k := range old {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		name := clip(k)
		switch {
		case carried(k) || name == "":
		case neverCarried(k):
			if len(refused) < maxListedKeys {
				refused = append(refused, name)
			}
		default:
			if len(ignored) < maxListedKeys {
				ignored = append(ignored, name)
			}
		}
	}
	return carry, refused, ignored
}

// ApplySettings writes carried settings into the new server's
// server.properties and returns the new content. Only the keys CarrySettings
// carries are applied, each value is checked again, and every other line
// stays as it was.
func ApplySettings(current []byte, carry []Setting) []byte {
	want := map[string]string{}
	var order []string
	for _, s := range carry {
		v, ok := normalizeSetting(s.Key, s.Value)
		if !carried(s.Key) || !ok {
			continue
		}
		if _, seen := want[s.Key]; !seen {
			order = append(order, s.Key)
		}
		want[s.Key] = v
	}
	var out bytes.Buffer
	done := map[string]bool{}
	last := 0
	for _, p := range parseProperties(current) {
		v, ok := want[p.key]
		if !ok {
			continue
		}
		out.Write(current[last:p.start])
		if !done[p.key] {
			done[p.key] = true
			out.WriteString(p.key + "=" + escapeValue(v))
			out.WriteString(lineBreakOf(current[p.start:p.end]))
		}
		last = p.end
	}
	out.Write(current[last:])
	for _, k := range order {
		if done[k] {
			continue
		}
		if b := out.Bytes(); len(b) > 0 && b[len(b)-1] != '\n' && b[len(b)-1] != '\r' {
			out.WriteByte('\n')
		}
		out.WriteString(k + "=" + escapeValue(want[k]) + "\n")
	}
	return out.Bytes()
}

// normalizeSetting checks a value for a carried key and returns it in the
// form Minecraft writes.
func normalizeSetting(key, v string) (string, bool) {
	v = strings.TrimSpace(v)
	switch key {
	case "level-seed":
		if v == "" || len(v) > 128 || !utf8.ValidString(v) || strings.IndexFunc(v, unsafeRune) >= 0 {
			return "", false
		}
		return v, true
	case "gamemode":
		switch strings.ToLower(v) {
		case "0", "survival":
			return "survival", true
		case "1", "creative":
			return "creative", true
		case "2", "adventure":
			return "adventure", true
		case "3", "spectator":
			return "spectator", true
		}
	case "difficulty":
		switch strings.ToLower(v) {
		case "0", "peaceful":
			return "peaceful", true
		case "1", "easy":
			return "easy", true
		case "2", "normal":
			return "normal", true
		case "3", "hard":
			return "hard", true
		}
	case "hardcore":
		switch strings.ToLower(v) {
		case "true", "false":
			return strings.ToLower(v), true
		}
	}
	return "", false
}

// property is one key and value of a properties file, with the bytes of
// its logical line, line breaks included.
type property struct {
	key, value string
	start, end int
}

// parseProperties reads a properties file the way java.util.Properties
// does: comment lines start with # or !, a line ending in an odd number of
// backslashes continues on the next one, and the key ends at the first
// unescaped =, : or whitespace.
func parseProperties(b []byte) []property {
	var out []property
	for i := 0; i < len(b); {
		start := i
		j, end, next := physicalLine(b, i)
		if j == end || b[j] == '#' || b[j] == '!' {
			i = next
			continue
		}
		var logical strings.Builder
		for {
			seg := b[j:end]
			if trailingBackslashes(seg)%2 == 0 {
				logical.Write(seg)
				break
			}
			logical.Write(seg[:len(seg)-1])
			if next >= len(b) {
				break
			}
			j, end, next = physicalLine(b, next)
		}
		key, value := splitProperty(logical.String())
		out = append(out, property{key: key, value: value, start: start, end: next})
		i = next
	}
	return out
}

// physicalLine returns where the text of the line starting at i begins
// after leading whitespace, where it ends before the line break, and where
// the next line starts.
func physicalLine(b []byte, i int) (text, end, next int) {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\f') {
		i++
	}
	end = i
	for end < len(b) && b[end] != '\n' && b[end] != '\r' {
		end++
	}
	next = end
	if next < len(b) && b[next] == '\r' {
		next++
	}
	if next < len(b) && b[next] == '\n' {
		next++
	}
	return i, end, next
}

func trailingBackslashes(b []byte) int {
	n := 0
	for i := len(b) - 1; i >= 0 && b[i] == '\\'; i-- {
		n++
	}
	return n
}

func lineBreakOf(line []byte) string {
	switch {
	case bytes.HasSuffix(line, []byte("\r\n")):
		return "\r\n"
	case bytes.HasSuffix(line, []byte("\n")):
		return "\n"
	case bytes.HasSuffix(line, []byte("\r")):
		return "\r"
	}
	return ""
}

func splitProperty(line string) (key, value string) {
	keyEnd, valueStart := len(line), len(line)
	hasSep, escaped := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if !escaped && (c == '=' || c == ':') {
			keyEnd, valueStart, hasSep = i, i+1, true
			break
		}
		if !escaped && (c == ' ' || c == '\t' || c == '\f') {
			keyEnd, valueStart = i, i+1
			break
		}
		escaped = c == '\\' && !escaped
	}
	for valueStart < len(line) {
		c := line[valueStart]
		if c != ' ' && c != '\t' && c != '\f' {
			if hasSep || (c != '=' && c != ':') {
				break
			}
			hasSep = true
		}
		valueStart++
	}
	return unescapeProperty(line[:keyEnd]), unescapeProperty(line[valueStart:])
}

func unescapeProperty(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	var pending []uint16
	flush := func() {
		if len(pending) > 0 {
			b.WriteString(string(utf16.Decode(pending)))
			pending = pending[:0]
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 == len(s) {
			flush()
			if c != '\\' {
				b.WriteByte(c)
			}
			continue
		}
		i++
		if s[i] == 'u' && i+4 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+5], 16, 16); err == nil {
				pending = append(pending, uint16(n))
				i += 4
				continue
			}
		}
		flush()
		switch s[i] {
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 'f':
			b.WriteByte('\f')
		default:
			b.WriteByte(s[i])
		}
	}
	flush()
	return b.String()
}

// escapeValue writes a value the way java.util.Properties.store does, with
// everything outside printable ASCII as a \u escape, so it reads the same
// whichever encoding the server assumes.
func escapeValue(v string) string {
	var b strings.Builder
	for i, r := range v {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == ' ' && i == 0:
			b.WriteString(`\ `)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '=' || r == ':' || r == '#' || r == '!':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r > 0x7e:
			for _, u := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&b, `\u%04X`, u)
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// propertyValue returns the value of key in a properties file.
func propertyValue(b []byte, key string) (string, bool) {
	v, ok := "", false
	for _, p := range parseProperties(b) {
		if p.key == key {
			v, ok = p.value, true
		}
	}
	return v, ok
}
