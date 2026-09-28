package usage

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// installing is a command that runs the installer as root: install.sh or the
// binary's install command, or get.sh piped into sh.
var installing = regexp.MustCompile(`\bsudo\b.*(/install\.sh\b|\bplaykeeper install\b|\|\s*sudo\b.*\bsh\b)|get\.sh"?\s*\|\s*sudo\b`)

// quiet is what keeps a test install's reports off the real service: usage
// stats off, or a recorder on the runner itself.
var quiet = regexp.MustCompile(`\bDO_NOT_TRACK=1\b|\bPLAYKEEPER_STATS_URL=http://127\.0\.0\.1:`)

// logicalLines joins lines continued with a backslash, and leaves out
// comments.
func logicalLines(text string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "#") && cur.Len() == 0 {
			continue
		}
		if strings.HasSuffix(line, `\`) {
			cur.WriteString(strings.TrimSuffix(line, `\`))
			continue
		}
		cur.WriteString(line)
		out = append(out, cur.String())
		cur.Reset()
	}
	return out
}

// The project's CI and test machines install Playkeeper dozens of times a
// day; none may count as a real install. Every command in its workflows and
// scripts that installs sends nothing (DO_NOT_TRACK=1) or sends to a
// recorder on the runner. Installs that forget are still marked as tests
// while an Actions job runs (ActionsJob), but the KVM guests the lab boots
// run none.
func TestEveryInstallTheProjectRunsSendsNoUsageStats(t *testing.T) {
	var files []string
	for _, pattern := range []string{"../../.github/workflows/*.yml", "../../.github/actions/*/action.yml", "../../scripts/*.sh", "../../scripts/e2e/*.sh"} {
		m, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	found := 0
	for _, f := range files {
		// They install nothing: one quotes the scripts it changes, the
		// other writes the command for people into the release notes.
		if base := filepath.Base(f); base == "negative-controls.sh" || base == "release-notes.sh" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range logicalLines(string(b)) {
			if !installing.MatchString(line) {
				continue
			}
			found++
			if !quiet.MatchString(line) {
				t.Errorf("%s installs Playkeeper without DO_NOT_TRACK=1, so its usage stats would count as a real install:\n%s", f, strings.TrimSpace(line))
			}
		}
	}
	if found < 25 {
		t.Fatalf("found %d installs in the workflows and scripts: the pattern no longer matches them", found)
	}
}
