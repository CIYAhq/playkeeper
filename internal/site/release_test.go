package site

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// overlay reads the files in over instead of base's.
type overlay struct {
	base fs.FS
	over fstest.MapFS
}

func (o overlay) Open(name string) (fs.File, error) {
	if _, ok := o.over[name]; ok {
		return o.over.Open(name)
	}
	return o.base.Open(name)
}

// The site says a release is out, and describes it, only once
// site/data/release.json names it: the next release's section in
// CHANGELOG.md, which pull requests fill in as they merge, stays off the
// site until the release's own commit reaches main.
func TestTheSiteShowsOnlyThePublishedRelease(t *testing.T) {
	published, err := releaseVersion(os.DirFS("../.."))
	if err != nil {
		t.Fatal(err)
	}
	changelog, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	const next = "99.0.0"
	first := strings.Index(string(changelog), "\n## ") + 1
	unreleased := string(changelog[:first]) + "## " + next + "\n\n- Not out yet.\n\n" + string(changelog[first:])
	buildWith := func(release string) (*Output, error) {
		over := fstest.MapFS{"CHANGELOG.md": {Data: []byte(unreleased)}}
		if release != "" {
			over["site/data/release.json"] = &fstest.MapFile{Data: []byte(release)}
		}
		return Build(Options{Root: overlay{os.DirFS("../.."), over}, Settings: Default, Now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)})
	}

	o, err := buildWith("")
	if err != nil {
		t.Fatal(err)
	}
	all := pages(o)
	if home := all["/"]; !strings.Contains(home, "Playkeeper "+published+" is out") {
		t.Errorf("the home page doesn't say %s is out", published)
	}
	for p, html := range all {
		if strings.Contains(html, next) {
			t.Errorf("%s shows %s, which isn't published", p, next)
		}
	}

	o, err = buildWith(`{"version": "` + next + `"}`)
	if err != nil {
		t.Fatal(err)
	}
	if home := pages(o)["/"]; !strings.Contains(home, "Playkeeper "+next+" is out") {
		t.Errorf("once site/data/release.json names %s, the home page doesn't say it's out", next)
	}

	for _, release := range []string{`{"version": "98.7.6"}`, `{"version": "v` + published + `"}`, `{"version": ""}`, `{}`, `not json`} {
		if _, err := buildWith(release); err == nil {
			t.Errorf("site/data/release.json %s builds", release)
		}
	}
}
