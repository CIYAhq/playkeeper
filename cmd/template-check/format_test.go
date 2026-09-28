package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/templates"
)

// A template pinned by the check reads like the rest of
// site/data/templates: writing any of them back changes nothing.
func TestFormatWritesTemplatesAsTheSiteKeepsThem(t *testing.T) {
	dir := filepath.Join("..", "..", "site", "data", "templates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || e.Name() == "cards.json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		tpl, err := templates.Decode(raw)
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		got, err := formatTemplate(tpl)
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if string(got) != string(raw) {
			t.Errorf("%s written back differs:\n%s", e.Name(), got)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no templates in site/data/templates")
	}
}

func TestSpacedWritesOneLine(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":1,"b":{"c":"x, y: {z}"}}`: `{ "a": 1, "b": { "c": "x, y: {z}" } }`,
		`{"e":{}}`:                      `{ "e": {} }`,
		`{"q":"say \"hi\", {ok}"}`:      `{ "q": "say \"hi\", {ok}" }`,
	} {
		if got := spaced([]byte(in)); got != want {
			t.Errorf("spaced(%s) = %s, want %s", in, got, want)
		}
	}
}
