package version

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestStringNamesTheVersionCommitAndDate(t *testing.T) {
	if got := String(); got != "playkeeper dev (unknown, unknown)" {
		t.Fatalf("a build without release values: %q", got)
	}
	v, c, d := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = v, c, d })
	Version, Commit, Date = "0.3.1", "930ebbe", "2026-09-25T20:00:00Z"
	if got := String(); got != "playkeeper 0.3.1 (930ebbe, 2026-09-25T20:00:00Z)" {
		t.Fatalf("got %q", got)
	}
}

type probe struct{}

// The release build sets these variables with -ldflags -X, which the linker
// silently skips for a name that does not exist.
func TestTheReleaseBuildSetsVariablesThatExist(t *testing.T) {
	b, err := os.ReadFile("../../scripts/package.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	if pkg := reflect.TypeOf(probe{}).PkgPath(); !strings.Contains(script, "\npkg="+pkg+"\n") {
		t.Fatalf("scripts/package.sh does not set pkg=%s", pkg)
	}
	vars := map[string]*string{"Version": &Version, "Commit": &Commit, "Date": &Date}
	set := map[string]bool{}
	for _, m := range regexp.MustCompile(`-X \$pkg\.(\w+)=`).FindAllStringSubmatch(script, -1) {
		set[m[1]] = true
		if vars[m[1]] == nil {
			t.Errorf("scripts/package.sh sets %s, which this package does not have", m[1])
		}
	}
	for name := range vars {
		if !set[name] {
			t.Errorf("scripts/package.sh does not set %s", name)
		}
	}
}
