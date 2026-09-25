package addons

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDetailsModrinth(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	f.patchVersion(f.chunkyPaper(), func(v obj) {
		v["changelog"] = "## Changes\n- Faster on **Paper** 26.2\n- Pausing no longer skips chunks\n\n![banner](https://cdn.example/b.png)"
	})

	d, err := l.Details(context.Background(), srv, Modrinth, "chunky")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "Chunky" || d.Author != "pop4959" || d.ProjectID != "fALzjamp" || d.PageURL != "https://modrinth.com/plugin/chunky" || d.Notice != nil {
		t.Errorf("card %+v", d.Card)
	}
	if d.Latest == nil || d.Latest.VersionNumber != "1.5.3" || d.Latest.Channel != "release" {
		t.Errorf("latest %+v", d.Latest)
	}
	if d.Notes != "Faster on Paper 26.2. Pausing no longer skips chunks." {
		t.Errorf("notes %q", d.Notes)
	}
	search := f.sentTo("modrinth", "/v2/search")
	if len(search) != 1 || !strings.Contains(search[0].query.Get("facets"), `"project_id:fALzjamp"`) {
		t.Errorf("the author lookup sent %+v", search)
	}
}

func TestDetailsHangar(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")

	d, err := l.Details(context.Background(), srv, Hangar, "ViaVersion")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "ViaVersion" || d.Author != "ViaVersion" || d.Source != Hangar || d.Latest == nil || d.Latest.VersionNumber != "5.12.0" {
		t.Errorf("details %+v, latest %+v", d.Card, d.Latest)
	}
	if d.Notes == "" || strings.ContainsAny(d.Notes, "\n#*`[]") {
		t.Errorf("notes %q", d.Notes)
	}
	if len(f.sentTo("modrinth", "/v2/search")) != 0 {
		t.Error("a Hangar project's author comes from Hangar")
	}
}

func TestDetailsWithoutAVersionForTheServer(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "1.8.9")

	d, err := l.Details(context.Background(), srv, Modrinth, "chunky")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "Chunky" || d.Latest != nil || d.Notice == nil || d.Notice.Kind != KindNoVersion {
		t.Errorf("details %+v, latest %+v, notice %+v", d.Card, d.Latest, d.Notice)
	}
}

func TestPlainNotes(t *testing.T) {
	for in, want := range map[string]string{
		"# 1.4.40\n\n* Faster on Paper 26.1\n* Pausing no longer skips chunks":              "Faster on Paper 26.1. Pausing no longer skips chunks.",
		"Changelog:\n1. Fixed `/chunky pause`\n2) See [the wiki](https://example.org/wiki)": "Fixed /chunky pause. See the wiki.",
		"<p>Fixed &amp; improved</p><br>Thanks to <b>everyone</b>!":                         "Fixed & improved. Thanks to everyone!",
		"> Quoted note\n\n---\n\n~~old~~ new":                                               "Quoted note. old new.",
		"line\x07with a bell":                                                               "linewith a bell.",
		"":                                                                                  "",
	} {
		if got := plainNotes(in); got != want {
			t.Errorf("plainNotes(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("Chunky pre-generates the world. ", 40)
	got := plainNotes(long)
	if n := utf8.RuneCountInString(got); n > maxNotes+1 || !strings.HasSuffix(got, "…") || strings.HasSuffix(got, " …") {
		t.Errorf("long notes cut to %d characters: %q", n, got)
	}
}

func TestPreviewUninstall(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	plugins := filepath.Join(srv.Dir, "plugins")
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Hangar, Project: "ViaBackwards"})
	backwards, via := installed[0], installed[1]
	writeFile(t, filepath.Join(plugins, "ViaBackwards", "config.yml"), []byte("enabled: true\n"))

	p, err := l.PreviewUninstall(srv, installed, backwards.Key())
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "preview", p, &RemovalPreview{Record: backwards, NeededBy: []string{}, Orphans: []Installed{via}, ConfigFolder: "ViaBackwards"})

	p, err = l.PreviewUninstall(srv, installed, via.Key())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.NeededBy, []string{"ViaBackwards"}) || len(p.Orphans) != 0 || p.Changed || p.Missing {
		t.Errorf("preview %+v", p)
	}

	jar := filepath.Join(plugins, via.FileName)
	writeFile(t, jar, append(readFile(t, jar), "patched"...))
	if p, _ = l.PreviewUninstall(srv, installed, via.Key()); !p.Changed {
		t.Errorf("a changed file must show: %+v", p)
	}
	if got := ls(t, plugins); len(got) != 3 {
		t.Errorf("a preview changed the folder: %v", got)
	}
	_, err = l.PreviewUninstall(srv, installed, Key{Modrinth, "nope"})
	wantKind(t, err, KindNotManaged)
}

func TestChangedFilesAreReplacedOrRemovedOnlyWhenAsked(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	plugins := filepath.Join(srv.Dir, "plugins")
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "viaversion", VersionID: "ZH8459B6"})
	jar := filepath.Join(plugins, "ViaVersion-5.11.0.jar")
	writeFile(t, jar, append(readFile(t, jar), "patched"...))

	_, err := l.Uninstall(srv, installed, installed[0].Key(), UninstallOptions{})
	wantKind(t, err, KindModified)
	p := mustPlanUpdate(t, l, srv, installed, UpdateRequest{Changed: true})
	if !p.Ready || len(p.Blockers) != 0 || !slices.ContainsFunc(p.Warnings, func(n Notice) bool { return n.Kind == KindModified }) {
		t.Errorf("plan %+v", p)
	}
	res, err := l.Update(context.Background(), srv, installed, UpdateRequest{Changed: true, Fingerprint: p.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if got := ls(t, plugins); !slices.Equal(got, []string{"ViaVersion-5.12.0.jar"}) {
		t.Errorf("plugins holds %v", got)
	}

	updated := res.Installed
	jar = filepath.Join(plugins, "ViaVersion-5.12.0.jar")
	writeFile(t, jar, append(readFile(t, jar), "patched"...))
	if _, err := l.Uninstall(srv, updated, updated[0].Key(), UninstallOptions{Changed: true}); err != nil {
		t.Fatal(err)
	}
	if got := ls(t, plugins); got != nil {
		t.Errorf("plugins holds %v", got)
	}
}

// chunkyPaper is the id of Chunky's newest Paper version in the fixtures.
func (f *fakes) chunkyPaper() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.mOrder {
		v := f.mVersions[id]
		if v["project_id"] == "fALzjamp" && slices.Contains(strs(v["loaders"]), "paper") {
			return id
		}
	}
	f.t.Fatal("the fixtures have no Paper version of Chunky")
	return ""
}
