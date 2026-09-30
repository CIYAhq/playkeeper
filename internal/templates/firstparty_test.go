package templates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/addons/firstparty"
)

// noNetwork fails the test on any request: nothing about Playkeeper's own
// plugins may reach the network.
type noNetwork struct{ t *testing.T }

func (n noNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	n.t.Errorf("%s %s was requested", r.Method, r.URL)
	if r.Body != nil {
		r.Body.Close()
	}
	return nil, errors.New("Playkeeper's own plugins make no requests")
}

// offlineLibrary is the add-on library with every client failing the test
// on a request.
func offlineLibrary(t *testing.T) *addons.Library {
	l := addons.New(&http.Client{Transport: noNetwork{t}})
	l.TempDir = t.TempDir()
	return l
}

// aiBuildBattle is the build of AI Build Battle the binary carries,
// whichever build that is, and its bytes.
func aiBuildBattle(t *testing.T) (firstparty.Jar, []byte) {
	t.Helper()
	fp := firstparty.Lookup("ai-build-battle")
	if fp == nil {
		t.Fatal("AI Build Battle is not one of Playkeeper's own plugins")
	}
	j, err := fp.Jar()
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(j.Open())
	if err != nil {
		t.Fatal(err)
	}
	return j, data
}

// aiBuildBattleJSON is how a template lists AI Build Battle, pinned to the
// build j.
func aiBuildBattleJSON(j firstparty.Jar) string {
	return fmt.Sprintf(`{"source":"playkeeper","project":"ai-build-battle","slug":"ai-build-battle","name":"AI Build Battle",`+
		`"pin":{"versionId":%q,"versionNumber":%q,"channel":"release","hashAlgo":"sha256","hash":%q}}`, j.Version, j.Version, j.SHA256)
}

// aiBuildBattleTemplate is the Paper fixture with AI Build Battle, pinned to
// the build the binary carries, as its only add-on.
func aiBuildBattleTemplate(t *testing.T) *Template {
	t.Helper()
	j, _ := aiBuildBattle(t)
	var a Addon
	if err := json.Unmarshal([]byte(aiBuildBattleJSON(j)), &a); err != nil {
		t.Fatal(err)
	}
	tp := paperTemplate(t)
	tp.Addons = []Addon{a}
	return tp
}

// newPaperServer is an empty Paper server on the Minecraft version given.
func newPaperServer(t *testing.T, mc string) addons.Server {
	return addons.Server{Dir: t.TempDir(), Type: "paper", MinecraftVersion: mc}
}

// pluginFiles lists the files in a server's plugins folder.
func pluginFiles(t *testing.T, srv addons.Server) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(srv.Dir, "plugins"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// wantEmbedded checks that the server's plugins folder holds the embedded
// build of AI Build Battle, and nothing else.
func wantEmbedded(t *testing.T, srv addons.Server) {
	t.Helper()
	j, data := aiBuildBattle(t)
	if got := pluginFiles(t, srv); len(got) != 1 || got[0] != j.FileName {
		t.Fatalf("the plugins folder holds %v, want %s", got, j.FileName)
	}
	b, err := os.ReadFile(filepath.Join(srv.Dir, "plugins", j.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, data) {
		t.Error("the installed file is not the jar the binary carries")
	}
}

func TestTemplateListsPlaykeepersOwnPlugins(t *testing.T) {
	others := fixture(t, "paper-server.json").Addons
	for _, c := range []struct {
		name string
		edit func(*Template)
	}{
		{"pinned on Paper", func(*Template) {}},
		{"pinned on Purpur", func(tp *Template) { tp.Server.Type, tp.Server.Build = "purpur", nil }},
		{"newest version", func(tp *Template) { tp.Addons[0].Pin, tp.Addons[0].Latest = nil, true }},
		{"with Modrinth and Hangar add-ons", func(tp *Template) { tp.Addons = append(slices.Clone(others), tp.Addons[0]) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			tp := aiBuildBattleTemplate(t)
			c.edit(tp)
			if err := tp.Validate(); err != nil {
				t.Fatal(err)
			}
			roundTrip(t, tp)
		})
	}
}

func TestValidateRefusesPlaykeeperPlugins(t *testing.T) {
	cases := []struct {
		name           string
		edit           func(*Template)
		field, problem string
		msg            string
	}{
		{name: "plugin this Playkeeper does not have", edit: func(tp *Template) { tp.Addons[0].Project = "ai-build-royale" },
			field: "addons[0].project", problem: "value", msg: "Add-on 1 is a plugin of Playkeeper's own that this Playkeeper does not have."},
		{name: "project id with a space", edit: func(tp *Template) { tp.Addons[0].Project = "ai build battle" },
			field: "addons[0].project", problem: "value"},
		{name: "version with a slash", edit: func(tp *Template) { tp.Addons[0].Pin.VersionID = "0.1/0" },
			field: "addons[0].pin.versionId", problem: "value"},
		{name: "SHA-512", edit: func(tp *Template) {
			tp.Addons[0].Pin.HashAlgo, tp.Addons[0].Pin.Hash = "sha512", digest("sha512", "jar")
		}, field: "addons[0].pin.hashAlgo", problem: "value", msg: "The hash of AI Build Battle must be the SHA-256 Playkeeper publishes."},
		{name: "short hash", edit: func(tp *Template) { tp.Addons[0].Pin.Hash = tp.Addons[0].Pin.Hash[:40] },
			field: "addons[0].pin.hash", problem: "hash"},
		{name: "Fabric server", edit: func(tp *Template) { tp.Server.Type, tp.Server.Build = "fabric", nil },
			field: "addons[0].source", problem: "not_allowed", msg: "AI Build Battle does not run on Fabric servers."},
		{name: "listed twice", edit: func(tp *Template) { tp.Addons = append(tp.Addons, tp.Addons[0]) },
			field: "addons[1]", problem: "duplicate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tp := aiBuildBattleTemplate(t)
			c.edit(tp)
			e := refused(t, tp.Validate(), KindInvalid)
			if e.Params["field"] != c.field || e.Params["problem"] != c.problem {
				t.Errorf("got field %q problem %q, want %q %q (%s)", e.Params["field"], e.Params["problem"], c.field, c.problem, e.Msg)
			}
			if c.msg != "" && e.Msg != c.msg {
				t.Errorf("got %q, want %q", e.Msg, c.msg)
			}
		})
	}
	tp := aiBuildBattleTemplate(t)
	tp.Addons[0].Project = "ai-build-royale"
	if e := refused(t, tp.Validate(), KindInvalid); !strings.HasPrefix(e.Hint, "Update Playkeeper") {
		t.Errorf("a plugin a newer Playkeeper may have: got hint %q", e.Hint)
	}
}

func TestPlanPlaykeeperPlugin(t *testing.T) {
	j, _ := aiBuildBattle(t)
	p := planOf(t, aiBuildBattleTemplate(t), paperCatalog())
	want := addons.InstallRequest{Source: addons.Playkeeper, Project: "ai-build-battle", VersionID: j.Version}
	if len(p.Addons) != 1 || !reflect.DeepEqual(p.Addons[0].Request, want) || p.Addons[0].Unpinned || p.Addons[0].PageURL != "" || len(p.Skipped) != 0 {
		t.Fatalf("got %+v, skipped %v", p.Addons, p.Skipped)
	}

	tp := aiBuildBattleTemplate(t)
	tp.Server.Type, tp.Server.Build = "purpur", nil
	p = planOf(t, tp, paperCatalog())
	if p.Type.ID != "paper" || len(p.Addons) != 1 || !reflect.DeepEqual(p.Addons[0].Request, want) || len(p.Skipped) != 0 {
		t.Errorf("Purpur template on Paper: got %+v with %+v, skipped %v", p.Type, p.Addons, p.Skipped)
	}

	tp = aiBuildBattleTemplate(t)
	tp.Server.MinecraftVersion, tp.Server.Build = "26.1.1", nil
	p = planOf(t, tp, paperCatalog())
	want.VersionID = ""
	if len(p.Addons) != 1 || !reflect.DeepEqual(p.Addons[0].Request, want) || !p.Addons[0].Unpinned {
		t.Errorf("another Minecraft version: got %+v", p.Addons)
	}
	wantKinds(t, "warnings", p.Warnings, KindVersionSubstituted, KindAddonsUnpinned)
}

func TestInstallPlaykeeperPlugin(t *testing.T) {
	j, _ := aiBuildBattle(t)
	cases := []struct {
		name   string
		edit   func(*PlannedAddon)
		reason Kind // the reason it's skipped; "" when installed
	}{
		{name: "pinned to the build the binary carries", edit: func(*PlannedAddon) {}},
		{name: "newest version", edit: func(a *PlannedAddon) { a.Pin, a.Latest = nil, true }},
		{name: "pinned to an older build", edit: func(a *PlannedAddon) {
			a.Pin.VersionID, a.Pin.VersionNumber, a.Pin.Hash = "0.0.0-0", "0.0.0-0", digest("sha256", "older build")
		}, reason: addons.KindNoVersion},
		{name: "an older build, unpinned for another Minecraft version", edit: func(a *PlannedAddon) {
			a.Pin.VersionID, a.Pin.VersionNumber, a.Pin.Hash = "0.0.0-0", "0.0.0-0", digest("sha256", "older build")
			a.Unpinned = true
		}},
		{name: "another file under the same version", edit: func(a *PlannedAddon) { a.Pin.Hash = digest("sha256", "another build") },
			reason: KindPinMismatch},
		{name: "another name", edit: func(a *PlannedAddon) { a.Name, a.Slug = "Build Battle", "" }, reason: KindProjectMismatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pa := planOf(t, aiBuildBattleTemplate(t), paperCatalog()).Addons[0]
			c.edit(&pa)
			srv := newPaperServer(t, "26.2")
			res, err := InstallAddon(context.Background(), offlineLibrary(t), srv, nil, pa)
			if err != nil {
				t.Fatal(err)
			}
			if c.reason != "" {
				if res.Status != AddonSkipped || res.Reason == nil || res.Reason.Kind != c.reason || len(res.Records) != 0 {
					t.Fatalf("got %s (%+v), want it skipped with %s", res.Status, res.Reason, c.reason)
				}
				checkNotice(t, *res.Reason)
				if strings.Contains(res.Reason.Hint, "library") {
					t.Errorf("the hint sends the user to the library, which does not list Playkeeper's own plugins: %q", res.Reason.Hint)
				}
				if got := pluginFiles(t, srv); len(got) != 0 {
					t.Errorf("the plugins folder holds %v", got)
				}
				return
			}
			if res.Status != AddonInstalled || len(res.Records) != 1 {
				t.Fatalf("got %s (%+v), %v", res.Status, res.Reason, res.Records)
			}
			rec := res.Records[0]
			if rec.Source != addons.Playkeeper || rec.ProjectID != "ai-build-battle" || rec.VersionID != j.Version || rec.HashAlgo != "sha256" || rec.Hash != j.SHA256 || rec.FileName != j.FileName {
				t.Errorf("got record %+v", rec)
			}
			wantEmbedded(t, srv)
		})
	}
}

// A server with AI Build Battle exports it with the pin of the build it
// has, and a server made from that template gets the same file.
func TestExportCarriesPlaykeepersOwnPlugin(t *testing.T) {
	ctx := context.Background()
	j, data := aiBuildBattle(t)
	for _, c := range []struct {
		name  string
		setup func(*testing.T, *addons.Library, addons.Server) []addons.Installed
		notes []Kind
	}{
		{"installed by Playkeeper", func(t *testing.T, lib *addons.Library, srv addons.Server) []addons.Installed {
			res, err := lib.Install(ctx, srv, nil, addons.InstallRequest{Source: addons.Playkeeper, Project: "ai-build-battle"})
			if err != nil {
				t.Fatal(err)
			}
			return res.Installed
		}, []Kind{KindNoteWorld, KindNotePlayers, KindNoteAddonConfig}},
		{"added by hand", func(t *testing.T, _ *addons.Library, srv addons.Server) []addons.Installed {
			if err := os.MkdirAll(filepath.Join(srv.Dir, "plugins"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(srv.Dir, "plugins", "AIBuildBattle.jar"), data, 0o644); err != nil {
				t.Fatal(err)
			}
			return nil
		}, []Kind{KindNoteWorld, KindNotePlayers, KindNoteAddonConfig, KindNoteIdentified}},
	} {
		t.Run(c.name, func(t *testing.T) {
			lib := offlineLibrary(t)
			srv := newPaperServer(t, "26.2")
			rows := c.setup(t, lib, srv)
			folder, err := lib.Scan(ctx, srv, rows, true)
			if err != nil {
				t.Fatal(err)
			}
			tp, rep, err := Export(Setup{Name: "AI Build Battle", Type: "paper", MinecraftVersion: "26.2", Addons: rows, Folder: folder}, ExportOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(tp.Addons) != 1 || len(rep.LeftOut) != 0 {
				t.Fatalf("got add-ons %v, left out %v", addonNames(tp), rep.LeftOut)
			}
			if js, _ := json.Marshal(tp.Addons[0]); string(js) != aiBuildBattleJSON(j) {
				t.Errorf("got %s\nwant %s", js, aiBuildBattleJSON(j))
			}
			if wantKinds(t, "notes", rep.Notes, c.notes...) && len(c.notes) > 3 {
				if n := rep.Notes[3]; n.Msg != "AI Build Battle was added by hand; it is one of Playkeeper's own plugins, so it travels like the others." {
					t.Errorf("got %q", n.Msg)
				}
			}

			newSrv := newPaperServer(t, "26.2")
			p := planOf(t, tp, paperCatalog())
			res, err := InstallAddon(ctx, lib, newSrv, nil, p.Addons[0])
			if err != nil || res.Status != AddonInstalled {
				t.Fatalf("got %v, %+v", err, res)
			}
			wantEmbedded(t, newSrv)
		})
	}
}

func TestExportLeavesOutPlaykeeperPlugins(t *testing.T) {
	row := func(project, name string) addons.Installed {
		return addons.Installed{Source: addons.Playkeeper, ProjectID: project, Slug: project, Name: name, VersionID: "1.0.0", VersionNumber: "1.0.0",
			Channel: "release", FileName: project + "-1.0.0.jar", HashAlgo: "sha256", Hash: digest("sha256", project)}
	}
	for _, c := range []struct {
		name string
		typ  string
		rec  addons.Installed
		msg  string
	}{
		{"on a Fabric server", "fabric", row("ai-build-battle", "AI Build Battle"),
			"AI Build Battle was left out: this Playkeeper has no build of it for Fabric servers."},
		{"one this Playkeeper does not have", "paper", row("ai-build-royale", "AI Build Royale"),
			"AI Build Royale was left out: this Playkeeper has no build of it for Paper servers."},
	} {
		t.Run(c.name, func(t *testing.T) {
			tp, rep, err := Export(Setup{Name: "Server", Type: c.typ, MinecraftVersion: "26.2", Addons: []addons.Installed{c.rec}}, ExportOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(tp.Addons) != 0 || !wantKinds(t, "left out", rep.LeftOut, KindLeftOutAddon) {
				t.Fatalf("got %v", addonNames(tp))
			}
			if rep.LeftOut[0].Msg != c.msg {
				t.Errorf("got %q, want %q", rep.LeftOut[0].Msg, c.msg)
			}
		})
	}
}
