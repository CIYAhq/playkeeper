package site

import (
	"html"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The server.properties editor lists exactly the settings the 26.3 server
// writes, with its defaults, and writes the file as the server does.
func TestServerPropertiesMatchTheServer(t *testing.T) {
	b, err := os.ReadFile("testdata/server-26.3.properties")
	if err != nil {
		t.Fatal(err)
	}
	var server []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !strings.HasPrefix(l, "#") {
			server = append(server, l)
		}
	}

	// Every line after the header is the server's, escaped the same way.
	got := strings.Split(defaultProperties(), "\n")
	if head := strings.Join(got[:2], "\n") + "\n"; head != propertiesHeader {
		t.Errorf("the file starts %q", head)
	}
	if strings.Join(got[2:], "\n") != strings.Join(server, "\n") {
		t.Errorf("the editor's defaults aren't the 26.3 server's:\n%s\nwant\n%s", strings.Join(got[2:], "\n"), strings.Join(server, "\n"))
	}

	ids := map[string]bool{}
	for _, p := range allProperties() {
		if ids[p.ID()] {
			t.Errorf("two settings have the id %s", p.ID())
		}
		ids[p.ID()] = true
		if p.What == "" || !strings.HasSuffix(p.What, ".") {
			t.Errorf("%s: what it does should be a sentence: %q", p.Key, p.What)
		}
		switch p.Kind {
		case "bool":
			if p.Default != "true" && p.Default != "false" {
				t.Errorf("%s: a bool's default is %q", p.Key, p.Default)
			}
		case "int":
			n, err := strconv.Atoi(p.Default)
			if err != nil {
				t.Errorf("%s: an int's default is %q", p.Key, p.Default)
			}
			for _, lim := range []struct {
				v      string
				broken func(int) bool
			}{{p.Min, func(m int) bool { return n < m }}, {p.Max, func(m int) bool { return n > m }}} {
				if lim.v == "" {
					continue
				}
				if m, err := strconv.Atoi(lim.v); err != nil || lim.broken(m) {
					t.Errorf("%s: the default %d is outside %s", p.Key, n, p.Range())
				}
			}
		case "choice":
			if !slices.ContainsFunc(p.Choices, func(c PropertyChoice) bool { return c.Value == p.Default }) {
				t.Errorf("%s: the default %q isn't one of its choices", p.Key, p.Default)
			}
		case "text":
		default:
			t.Errorf("%s: kind %q", p.Key, p.Kind)
		}
	}
	for key := range retiredProperties {
		if slices.ContainsFunc(allProperties(), func(p ServerProperty) bool { return p.Key == key }) {
			t.Errorf("%s is listed as retired and as a setting", key)
		}
	}

	// The page has a row for each, with what it does and its control, and
	// starts with the file the script writes.
	page := pages(build(t, Default))["/tools/server-properties"]
	text := html.UnescapeString(page)
	for _, p := range allProperties() {
		if !strings.Contains(page, `id="`+p.ID()+`" data-prop="`+p.Key+`"`) || !strings.Contains(text, p.What) {
			t.Errorf("/tools/server-properties has no row for %s", p.Key)
		}
	}
	if !strings.Contains(text, defaultProperties()+"</pre>") {
		t.Error("/tools/server-properties doesn't show the default file")
	}
	js, err := os.ReadFile("../../site/static/js/tools/server-properties.js")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(propertiesHeader, "\n"), "\n")
	if want := "var HEADER = ['" + strings.Join(lines, "', '") + "'];"; !strings.Contains(string(js), want) {
		t.Errorf("server-properties.js doesn't start the file as Go does: want %s", want)
	}
}

// escapeProperty writes what java.util.Properties.store writes.
func TestEscapeProperty(t *testing.T) {
	for in, want := range map[string]string{
		"minecraft:normal":        `minecraft\:normal`,
		"A Minecraft Server":      "A Minecraft Server",
		" lead":                   `\ lead`,
		"§aGreen\nline 2":         `\u00A7aGreen\nline 2`,
		`C:\keys\a=b#c!d`:         `C\:\\keys\\a\=b\#c\!d`,
		"♥ 😀":                     `\u2665 \uD83D\uDE00`,
		"{}":                      "{}",
		"https://example.com/p?x": `https\://example.com/p?x`,
	} {
		if got := escapeProperty(in); got != want {
			t.Errorf("escapeProperty(%q) = %s, want %s", in, got, want)
		}
	}
	if !regexp.MustCompile(`^[\x20-\x7e\n]*$`).MatchString(defaultProperties()) {
		t.Error("the default file has characters past ASCII")
	}
}
