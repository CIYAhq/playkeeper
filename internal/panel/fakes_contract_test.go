package panel

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The browser tests' fake panel refuses only with codes the real panel or an
// agent declares, so the dashboard is tested against the answers it gets.
func TestTheFakePanelRefusesWithCodesThePanelSends(t *testing.T) {
	declared := map[string]bool{}
	declaration := regexp.MustCompile(`(?:\b(?:Code|Kind)\w*\s*(?:[A-Za-z_.]+\s*)?=\s*|Code:\s*)"([a-z_]+)"`)
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, err := os.ReadFile(path)
		for _, m := range declaration.FindAllSubmatch(b, -1) {
			declared[string(m[1])] = true
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "test", "e2e", "ui", "fakes.ts"))
	if err != nil {
		t.Fatal(err)
	}
	refusal := regexp.MustCompile(`\{[^{}]*\berror\b[^{}]*\bcode: '([a-z_]+)'|refuse\(\d+, '([a-z_]+)'`)
	found := 0
	for _, m := range refusal.FindAllStringSubmatch(string(src), -1) {
		code := m[1] + m[2]
		found++
		if !declared[code] {
			t.Errorf("the fake panel refuses with %q, which neither the panel nor an agent sends", code)
		}
	}
	if found < 10 || !declared["invalid_request"] {
		t.Fatalf("found %d refusals in the fake panel and %d codes in the Go code: the patterns no longer match", found, len(declared))
	}
}
