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

// The site describes a release, in the docs and the home page's structured
// data, only once site/data/release.json names it: the next release's
// section in CHANGELOG.md, which pull requests fill in as they merge, stays
// off the site until the release's own commit reaches main. That commit
// leaves the home page's first line alone: it's about AI agents, not the
// release.
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
	describes := func(o *Output, version string) {
		t.Helper()
		all := pages(o)
		if !strings.Contains(all["/docs"], "For Playkeeper "+version+" ·") {
			t.Errorf("with %s in site/data/release.json, the docs aren't for it", version)
		}
		home := all["/"]
		if !strings.Contains(home, `"softwareVersion":"`+version+`"`) {
			t.Errorf("with %s in site/data/release.json, the home page's structured data doesn't name it", version)
		}
		if lead := between(home, `<div class="hero-text">`, "<h1 "); !strings.Contains(lead, `href="/docs/machines-and-ai-agents"`) || !strings.Contains(lead, "Let Claude or GPT run your server") {
			t.Errorf("with %s in site/data/release.json, the home page doesn't start with Let Claude or GPT run your server: %q", version, lead)
		}
		if strings.Contains(home, version+" is out") {
			t.Errorf("the home page says %s is out", version)
		}
	}

	o, err := buildWith("")
	if err != nil {
		t.Fatal(err)
	}
	describes(o, published)
	for p, html := range pages(o) {
		if strings.Contains(html, next) {
			t.Errorf("%s shows %s, which isn't published", p, next)
		}
	}

	o, err = buildWith(`{"version": "` + next + `"}`)
	if err != nil {
		t.Fatal(err)
	}
	describes(o, next)

	for _, release := range []string{`{"version": "98.7.6"}`, `{"version": "v` + published + `"}`, `{"version": ""}`, `{}`, `not json`} {
		if _, err := buildWith(release); err == nil {
			t.Errorf("site/data/release.json %s builds", release)
		}
	}
}
