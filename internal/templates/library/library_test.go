package library

import (
	"slices"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/templates"
)

// Every template in the list is one Playkeeper reads, and makes a link short
// enough to share.
func TestEveryTemplateReads(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("library.json lists no templates")
	}
	for _, tpl := range all {
		file, err := templates.Decode(tpl.File)
		if err != nil {
			t.Errorf("%s: %v", tpl.ID, err)
			continue
		}
		if file.Name != tpl.Name {
			t.Errorf("%s is called %q in the list and %q in its file", tpl.ID, tpl.Name, file.Name)
		}
		if link, err := templates.NewLink(file); err != nil || link.Warning != nil {
			t.Errorf("%s makes no short link: %v", tpl.ID, err)
		}
		if tpl.Art == "" {
			t.Errorf("%s has no art", tpl.ID)
		}
	}
}

// A release lists a template only from the release that opens it, and its
// pre-releases list what it does. A development build lists everything.
func TestForListsWhatTheReleaseOpens(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	held := slices.IndexFunc(all, func(t Template) bool { return t.OpensFrom != "" })
	if held < 0 {
		t.Skip("no template in the list waits for a release")
	}
	from := all[held]
	ids := func(version string) []string {
		list, err := For(version)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, t := range list {
			out = append(out, t.ID)
		}
		return out
	}
	if slices.Contains(ids("0.0.1"), from.ID) {
		t.Errorf("0.0.1 lists %s, which opens from %s", from.ID, from.OpensFrom)
	}
	for _, v := range []string{from.OpensFrom, "v" + from.OpensFrom, from.OpensFrom + "-rc.1", "99.0.0", "dev"} {
		if !slices.Contains(ids(v), from.ID) {
			t.Errorf("%s doesn't list %s, which opens from %s", v, from.ID, from.OpensFrom)
		}
	}
	if got := ids("dev"); len(got) != len(all) {
		t.Errorf("a development build lists %d of the %d templates", len(got), len(all))
	}
}
