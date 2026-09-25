package curated

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

func TestList(t *testing.T) {
	open := []string{
		"MIT", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "MPL-2.0", "Artistic-2.0",
		"LGPL-3.0-only", "LGPL-3.0-or-later", "GPL-3.0-only", "GPL-3.0-or-later", "AGPL-3.0-only", "AGPL-3.0-or-later",
	}
	shortID := regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	modrinthID := regexp.MustCompile(`^[A-Za-z0-9]{8}$`)
	list := List()
	if n := len(list); n < 6 || n > 9 {
		t.Errorf("%d entries; the list is voice chat and 5 to 8 more", n)
	}
	if list[0].ID != VoiceChatID {
		t.Errorf("the first entry is %s, not voice chat", list[0].ID)
	}
	seen := map[string]bool{}
	for _, e := range list {
		if !shortID.MatchString(e.ID) || seen[e.ID] {
			t.Errorf("entry id %q is not a unique short id", e.ID)
		}
		seen[e.ID] = true
		if e.Name == "" || len(e.Name) > 32 {
			t.Errorf("%s: name %q", e.ID, e.Name)
		}
		if !oneSentence(e.Purpose) {
			t.Errorf("%s: purpose %q is not one short sentence", e.ID, e.Purpose)
		}
		if len(e.Projects) == 0 {
			t.Errorf("%s has no projects", e.ID)
		}
		types := map[string]bool{}
		for _, p := range e.Projects {
			if len(p.Types) == 0 {
				t.Errorf("%s: %s is for no server type", e.ID, p.Title)
			}
			for _, typ := range p.Types {
				if _, err := addons.TargetFor(typ); err != nil || types[typ] {
					t.Errorf("%s: server type %q cannot load add-ons or has two projects", e.ID, typ)
				}
				types[typ] = true
			}
			// Every project is on Modrinth for now; a Hangar project needs
			// fixtures of its own in TestProjectsMatchModrinth.
			if p.Source != addons.Modrinth || !modrinthID.MatchString(p.ID) || p.Slug == "" || p.PageURL != "https://modrinth.com/project/"+p.Slug {
				t.Errorf("%s: project %+v", e.ID, p)
			}
			if p.Title == "" || p.Author == "" {
				t.Errorf("%s: %s has no title or author to credit", e.ID, p.ID)
			}
			if !slices.Contains(open, p.License) && !strings.HasPrefix(p.Permission, "https://") {
				t.Errorf("%s: %s's licence %s is not open and no permission is linked", e.ID, p.Title, p.License)
			}
		}
		if e.Setup == nil {
			continue
		}
		for _, port := range e.Setup.Ports {
			if port.Protocol != "udp" && port.Protocol != "tcp" || port.Default < minPort || port.Default > maxPort {
				t.Errorf("%s: port %+v", e.ID, port)
			}
		}
		for _, s := range e.Setup.Steps {
			if s.Kind == "" || !strings.HasSuffix(s.Msg, ".") || len(s.Params) == 0 {
				t.Errorf("%s: step %+v", e.ID, s)
			}
		}
	}

	voice, _ := Get(VoiceChatID)
	if voice.Setup == nil || !slices.Equal(voice.Setup.Ports, []Port{{Protocol: "udp", Default: 24454}}) ||
		len(voice.Setup.Steps) != 1 || voice.Setup.Steps[0].Kind != KindClientMod {
		t.Errorf("voice chat setup %+v", voice.Setup)
	}

	list[0].Projects[0].Types[0] = "changed"
	list[0].Name = "changed"
	if again := List(); again[0].Name != "Voice chat" || again[0].Projects[0].Types[0] != "paper" {
		t.Error("changing a returned entry changed the list")
	}
}

func oneSentence(s string) bool {
	return s != "" && len(s) <= 120 && strings.HasSuffix(s, ".") && strings.Count(s, ".") == 1 && !strings.ContainsAny(s, "\n\t")
}

func TestProjectsMatchModrinth(t *testing.T) {
	type project struct {
		ID, Slug, Title string
		ServerSide      string `json:"server_side"`
		License         struct{ ID string }
		Loaders         []string
	}
	type hit struct {
		ProjectID string `json:"project_id"`
		Author    string
	}
	var projects []project
	var search struct{ Hits []hit }
	readJSON(t, "testdata/projects.json", &projects)
	readJSON(t, "testdata/search.json", &search)
	// Purpur runs Paper plugins. Quilt counts only where a project says it
	// runs there, although Quilt loads many Fabric mods.
	loaders := map[string][]string{
		"paper": {"paper"}, "purpur": {"purpur", "paper"}, "fabric": {"fabric"}, "quilt": {"quilt"}, "neoforge": {"neoforge"},
	}
	used := map[string]bool{}
	for _, e := range List() {
		for _, p := range e.Projects {
			used[p.ID] = true
			i := slices.IndexFunc(projects, func(m project) bool { return m.ID == p.ID })
			if i < 0 {
				t.Errorf("%s: project %s is not in testdata/projects.json", e.ID, p.ID)
				continue
			}
			m := projects[i]
			if m.Slug != p.Slug || m.Title != p.Title || m.License.ID != p.License {
				t.Errorf("%s: listed as %s %q under %s; Modrinth has %s %q under %s", e.ID, p.Slug, p.Title, p.License, m.Slug, m.Title, m.License.ID)
			}
			if m.ServerSide == "unsupported" {
				t.Errorf("%s: %s does not run on servers", e.ID, p.Title)
			}
			for _, typ := range p.Types {
				if !slices.ContainsFunc(loaders[typ], func(l string) bool { return slices.Contains(m.Loaders, l) }) {
					t.Errorf("%s: %s is listed for %s, but Modrinth lists it for %v", e.ID, p.Title, typ, m.Loaders)
				}
			}
			j := slices.IndexFunc(search.Hits, func(h hit) bool { return h.ProjectID == p.ID })
			if j < 0 || search.Hits[j].Author != p.Author {
				t.Errorf("%s: %s's author is not %s in testdata/search.json", e.ID, p.Title, p.Author)
			}
		}
	}
	for _, m := range projects {
		if !used[m.ID] {
			t.Errorf("testdata/projects.json has %s, which no entry uses", m.Slug)
		}
	}
}

func TestForType(t *testing.T) {
	plugins := []string{"voice-chat", "rollback", "pregenerate", "newer-clients", "essentials", "permissions"}
	for typ, want := range map[string][]string{
		"paper":    plugins,
		"purpur":   plugins,
		"fabric":   {"voice-chat", "rollback", "pregenerate", "permissions", "lag-finder"},
		"quilt":    {"voice-chat", "rollback", "lag-finder"},
		"neoforge": {"voice-chat", "pregenerate", "permissions", "lag-finder"},
		"vanilla":  nil,
		"forge":    nil,
	} {
		var got []string
		for _, e := range ForType(typ) {
			got = append(got, e.ID)
		}
		if !slices.Equal(got, want) {
			t.Errorf("ForType(%q) = %v, want %v", typ, got, want)
		}
	}
}

func TestFor(t *testing.T) {
	rollback, ok := Get("rollback")
	if !ok {
		t.Fatal("no rollback entry")
	}
	p, err := rollback.For("quilt")
	if err != nil || p.Title != "Ledger" || p.Request() != (addons.InstallRequest{Source: addons.Modrinth, Project: "LVN9ygNV"}) {
		t.Errorf("For(quilt) = %+v, %v", p, err)
	}
	if p, err := rollback.For("purpur"); err != nil || p.Title != "CoreProtect" {
		t.Errorf("For(purpur) = %+v, %v", p, err)
	}

	_, err = rollback.For("neoforge")
	e := wantKind(t, err, KindNotForType)
	if e.Msg != "Grief rollback is only offered for Paper, Purpur, Fabric and Quilt servers; this server runs NeoForge." ||
		e.Hint != "Search the add-ons page for one made for NeoForge." || e.Params["types"] != "paper,purpur,fabric,quilt" || e.Params["id"] != "rollback" {
		t.Errorf("notice %+v", e.Notice)
	}
	_, err = rollback.For("vanilla")
	wantKind(t, err, addons.KindNoAddons)
	_, err = rollback.For("forge")
	wantKind(t, err, addons.KindUnknownServerType)

	voice, _ := Get(VoiceChatID)
	for _, typ := range []string{"paper", "purpur", "fabric", "quilt", "neoforge"} {
		if p, err := voice.For(typ); err != nil || p.ID != "9eGKb6K1" || p.Permission == "" {
			t.Errorf("voice chat For(%s) = %+v, %v", typ, p, err)
		}
	}
	lag, _ := Get("lag-finder")
	_, err = lag.For("paper")
	if e := wantKind(t, err, KindNotForType); e.Msg != "Lag finder is only offered for Fabric, Quilt and NeoForge servers; this server runs Paper." {
		t.Errorf("message %q", e.Msg)
	}

	if _, ok := Get("nope"); ok {
		t.Error("Get found an entry that does not exist")
	}
	if e, ok := ByProject(addons.Modrinth, "9eGKb6K1"); !ok || e.ID != VoiceChatID {
		t.Errorf("ByProject found %q, %v", e.ID, ok)
	}
	if e, ok := ByProject(addons.Modrinth, "LVN9ygNV"); !ok || e.ID != "rollback" {
		t.Errorf("ByProject found %q, %v", e.ID, ok)
	}
	for _, c := range []struct {
		src addons.Source
		id  string
	}{{addons.Hangar, "9eGKb6K1"}, {addons.Modrinth, "simple-voice-chat"}, {addons.Modrinth, ""}} {
		if e, ok := ByProject(c.src, c.id); ok {
			t.Errorf("ByProject(%s, %q) found %s", c.src, c.id, e.ID)
		}
	}
}

func wantKind(t *testing.T, err error, k addons.Kind) *addons.Error {
	t.Helper()
	var e *addons.Error
	if !errors.As(err, &e) || e.Kind != k {
		t.Fatalf("error %v, want kind %s", err, k)
	}
	return e
}

func readJSON(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}
