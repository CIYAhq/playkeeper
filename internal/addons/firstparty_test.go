package addons

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/addons/firstparty"
	"github.com/CIYAhq/playkeeper/internal/addons/geysermc"
	"github.com/CIYAhq/playkeeper/internal/addons/hangar"
	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
)

// noNetwork fails the test that sends a request through it: nothing about
// Playkeeper's own plugins may reach the network.
type noNetwork struct{ t *testing.T }

func (n noNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	n.t.Errorf("%s %s was requested", r.Method, r.URL)
	if r.Body != nil {
		r.Body.Close()
	}
	return nil, errors.New("Playkeeper's own plugins make no requests")
}

// offline is a Library whose every client fails the test on a request.
func offline(t *testing.T) *Library {
	c := &http.Client{Transport: noNetwork{t}}
	now := func() time.Time { return testNow }
	return &Library{
		Modrinth: modrinth.New(fetch.Options{HTTP: c, Now: now}),
		Hangar:   hangar.New(fetch.Options{HTTP: c, Now: now}),
		GeyserMC: geysermc.New(fetch.Options{HTTP: c, Now: now}),
		HTTP:     c,
		Now:      now,
		TempDir:  t.TempDir(),
	}
}

// aiBuildBattle is AI Build Battle's registry entry, and the build the
// binary carries with its bytes, whichever build that is.
func aiBuildBattle(t *testing.T) (*firstparty.Plugin, firstparty.Jar, []byte) {
	t.Helper()
	fp := firstparty.Lookup("ai-build-battle")
	if fp == nil {
		t.Fatal("AI Build Battle is not in the registry")
	}
	j, err := fp.Jar()
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(j.Open())
	if err != nil {
		t.Fatal(err)
	}
	return fp, j, data
}

// oldVersion comes before any version the embedded jar could have.
const oldVersion = "0.0.0-0"

func TestInstallPlaykeeperWritesTheEmbeddedJar(t *testing.T) {
	fp, j, data := aiBuildBattle(t)
	for _, typ := range []string{"paper", "purpur"} {
		t.Run(typ, func(t *testing.T) {
			ctx := context.Background()
			l := offline(t)
			srv := newServer(t, typ, "1.21.8")
			srv.Owner = &Owner{UID: os.Getuid(), GID: os.Getgid()}
			req := InstallRequest{Source: Playkeeper, Project: "ai-build-battle"}
			p, err := l.PlanInstall(ctx, srv, nil, req)
			if err != nil {
				t.Fatal(err)
			}
			if !p.Ready || stepList(p) != "AI Build Battle "+j.Version+" "+j.Version || len(p.Blockers)+len(p.Manual)+len(p.Warnings) != 0 {
				t.Fatalf("plan %s", jsonText(p))
			}
			if s := p.Steps[0]; s.FileName != j.FileName || s.HashAlgo != "sha256" || s.Hash != j.SHA256 || s.Size != j.Size || s.url != "" {
				t.Errorf("step %+v", s)
			}

			var verified bool
			req.Fingerprint = p.Fingerprint
			req.OnProgress = func(pr Progress) { verified = verified || pr.Verified && pr.Received == j.Size }
			res, err := l.Install(ctx, srv, nil, req)
			if err != nil {
				t.Fatal(err)
			}
			plugins := filepath.Join(srv.Dir, "plugins")
			if got, want := ls(t, plugins), []string{"ai-build-battle-" + j.Version + ".jar"}; !slices.Equal(got, want) {
				t.Fatalf("plugins holds %v, want %v", got, want)
			}
			if got := readFile(t, filepath.Join(plugins, j.FileName)); !bytes.Equal(got, data) || sha256hex(got) != j.SHA256 {
				t.Errorf("%s is not the jar the binary carries", j.FileName)
			}
			if fi, err := os.Stat(filepath.Join(plugins, j.FileName)); err != nil || fi.Mode().Perm()&^0o640 != 0 {
				t.Errorf("%s: mode %v, %v", j.FileName, fi.Mode(), err)
			}
			if len(res.Installed) != 1 {
				t.Fatalf("installed %+v", res.Installed)
			}
			sameJSON(t, "the record", res.Installed[0], Installed{
				Source: Playkeeper, ProjectID: "ai-build-battle", Slug: "ai-build-battle", Name: "AI Build Battle",
				Summary:   "Type /aibuild and an AI model builds it in front of you, block by block.",
				VersionID: j.Version, VersionNumber: j.Version, Channel: "release", Published: fp.Published,
				FileName: j.FileName, HashAlgo: "sha256", Hash: sha256hex(data), Size: int64(len(data)), InstalledAt: testNow,
			})
			if !verified {
				t.Error("no progress said the file was verified")
			}
			if !res.RestartNeeded || len(res.Replaced)+len(res.Manual)+len(res.Warnings) != 0 {
				t.Errorf("result %+v", res)
			}
			if ls(t, l.TempDir) != nil {
				t.Errorf("temporary files left: %v", ls(t, l.TempDir))
			}

			scan, err := l.Scan(ctx, srv, res.Installed, true)
			if err != nil {
				t.Fatal(err)
			}
			if got := statuses(scan); !slices.Equal(got, []FileStatus{FileManaged}) || len(scan.Missing) != 0 {
				t.Errorf("after the install the scan found %v, missing %v", got, scan.Missing)
			}
			if _, err := l.Install(ctx, srv, res.Installed, InstallRequest{Source: Playkeeper, Project: "ai-build-battle"}); KindOf(err) != KindAlreadyInstalled {
				t.Errorf("installing it again: %v", err)
			}

			key := Key{Playkeeper, "ai-build-battle"}
			if pre, err := l.PreviewUninstall(ctx, srv, res.Installed, key); err != nil || pre.Changed || pre.Missing {
				t.Fatalf("removal preview %+v, %v", pre, err)
			}
			rm, err := l.Uninstall(ctx, srv, res.Installed, key, UninstallOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if rm.Removed.Key() != key || ls(t, plugins) != nil {
				t.Errorf("removed %+v, plugins holds %v", rm.Removed, ls(t, plugins))
			}
		})
	}
}

func TestPlaykeeperRefusesOtherServerTypes(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		kind Kind
		msg  string
	}{
		{"fabric", KindNoVersion, "AI Build Battle has no version for Fabric servers on Minecraft 1.21.8."},
		{"quilt", KindNoVersion, "AI Build Battle has no version for Quilt servers on Minecraft 1.21.8."},
		{"neoforge", KindNoVersion, "AI Build Battle has no version for NeoForge servers on Minecraft 1.21.8."},
		{"forge", KindNoVersion, "AI Build Battle has no version for Forge servers on Minecraft 1.21.8."},
		{"vanilla", KindNoAddons, "Vanilla servers cannot load plugins or mods."},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			ctx := context.Background()
			l := offline(t)
			srv := newServer(t, tc.typ, "1.21.8")
			req := InstallRequest{Source: Playkeeper, Project: "ai-build-battle"}
			if _, err := l.PlanInstall(ctx, srv, nil, req); KindOf(err) != tc.kind {
				t.Errorf("plan: %v, want kind %s", err, tc.kind)
			}
			_, err := l.Install(ctx, srv, nil, req)
			if e := wantKind(t, err, tc.kind); e.Msg != tc.msg {
				t.Errorf("message %q, want %q", e.Msg, tc.msg)
			}
			if got := ls(t, srv.Dir); got != nil {
				t.Errorf("the server folder holds %v", got)
			}
			if tc.kind == KindNoVersion {
				d, err := l.Details(ctx, srv, Playkeeper, "ai-build-battle")
				if err != nil || d.Latest != nil || d.Notice == nil || d.Notice.Kind != KindNoVersion {
					t.Errorf("details %+v, %v", d, err)
				}
			}
		})
	}
}

func TestPlaykeeperHasOnlyTheEmbeddedVersion(t *testing.T) {
	fp, j, _ := aiBuildBattle(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name, project, version string
		kind                   Kind
		msg                    string
	}{
		{name: "the embedded version", project: "ai-build-battle", version: j.Version},
		{name: "any other version", project: "ai-build-battle", version: oldVersion, kind: KindNoVersion,
			msg: "That version of AI Build Battle does not fit this server (Paper, Minecraft 1.21.8)."},
		{name: "a project it doesn't have", project: "no-such-plugin", kind: KindNotFound,
			msg: `Playkeeper has no plugin of its own called "no-such-plugin".`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := offline(t)
			srv := newServer(t, "paper", "1.21.8")
			p, err := l.PlanInstall(ctx, srv, nil, InstallRequest{Source: Playkeeper, Project: tc.project, VersionID: tc.version})
			if tc.kind == "" {
				if err != nil || !p.Ready || stepList(p) != "AI Build Battle "+j.Version+" "+j.Version {
					t.Fatalf("plan %s, %v", jsonText(p), err)
				}
				return
			}
			if e := wantKind(t, err, tc.kind); e.Msg != tc.msg {
				t.Errorf("message %q, want %q", e.Msg, tc.msg)
			}
		})
	}

	l := offline(t)
	vs, err := l.Versions(ctx, newServer(t, "purpur", "26.1"), Playkeeper, "ai-build-battle")
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "versions", vs, []VersionInfo{{VersionID: j.Version, VersionNumber: j.Version, Channel: "release", Published: fp.Published, FileName: j.FileName, Size: j.Size}})
}

func TestPlaykeeperDetails(t *testing.T) {
	fp, j, _ := aiBuildBattle(t)
	d, err := offline(t).Details(context.Background(), newServer(t, "paper", "1.21.8"), Playkeeper, "ai-build-battle")
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "details", d, &Details{
		Card: Card{
			Source: Playkeeper, ProjectID: "ai-build-battle", Slug: "ai-build-battle", Name: "AI Build Battle", Author: "Playkeeper",
			Summary: "Type /aibuild and an AI model builds it in front of you, block by block.",
			License: "AGPL-3.0-only", Updated: fp.Published,
		},
		Latest: &VersionInfo{VersionID: j.Version, VersionNumber: j.Version, Channel: "release", Published: fp.Published, FileName: j.FileName, Size: j.Size},
	})
}

func TestPlaykeeperUpdatesToTheEmbeddedVersion(t *testing.T) {
	fp, j, data := aiBuildBattle(t)
	ctx := context.Background()
	l := offline(t)
	srv := newServer(t, "paper", "1.21.8")
	oldData := pluginJar(t, "AIBuildBattle", oldVersion)
	old := Installed{
		Source: Playkeeper, ProjectID: fp.ID, Slug: fp.Slug, Name: fp.Name, VersionID: oldVersion, VersionNumber: oldVersion, Channel: "release",
		Published: fp.Published, FileName: "ai-build-battle-" + oldVersion + ".jar", HashAlgo: "sha256", Hash: sha256hex(oldData), Size: int64(len(oldData)),
		InstalledAt: testNow.Add(-time.Hour),
	}
	plugins := filepath.Join(srv.Dir, "plugins")
	writeFile(t, filepath.Join(plugins, old.FileName), oldData)

	for _, tc := range []struct {
		installed string
		available bool
	}{
		{oldVersion, true},
		{j.Version, false},
		{"999999.0", false},
	} {
		rec := old
		rec.VersionID, rec.VersionNumber = tc.installed, tc.installed
		st, err := l.CheckUpdates(ctx, srv, []Installed{rec})
		if err != nil {
			t.Fatal(err)
		}
		if len(st) != 1 || st[0].Latest == nil || st[0].Latest.VersionID != j.Version || st[0].Available != tc.available {
			t.Errorf("with %s installed: %s, want available %v", tc.installed, jsonText(st), tc.available)
		}
	}
	st, err := l.CheckUpdates(ctx, newServer(t, "fabric", "1.21.8"), []Installed{old})
	if err != nil || len(st) != 1 || st[0].Latest != nil || st[0].Notice == nil || st[0].Notice.Kind != KindNoVersion {
		t.Errorf("on Fabric: %s, %v", jsonText(st), err)
	}

	res, err := l.Update(ctx, srv, []Installed{old}, UpdateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := ls(t, plugins); !slices.Equal(got, []string{j.FileName}) {
		t.Fatalf("plugins holds %v after the update", got)
	}
	if !bytes.Equal(readFile(t, filepath.Join(plugins, j.FileName)), data) {
		t.Errorf("%s is not the jar the binary carries", j.FileName)
	}
	if len(res.Installed) != 1 || res.Installed[0].VersionID != j.Version || res.Installed[0].Hash != j.SHA256 ||
		len(res.Replaced) != 1 || res.Replaced[0].FileName != old.FileName {
		t.Errorf("result %s", jsonText(res))
	}
}

func TestScanIdentifiesPlaykeepersOwnJars(t *testing.T) {
	fp, j, data := aiBuildBattle(t)
	ctx := context.Background()
	want := Installed{
		Source: Playkeeper, ProjectID: "ai-build-battle", Slug: "ai-build-battle", Name: "AI Build Battle", Summary: fp.Summary,
		VersionID: j.Version, VersionNumber: j.Version, Channel: "release", Published: fp.Published,
		FileName: "AIBuildBattle.jar", HashAlgo: "sha256", Hash: j.SHA256, Size: j.Size, InstalledAt: testNow,
	}

	t.Run("offline", func(t *testing.T) {
		l := offline(t)
		srv := newServer(t, "paper", "1.21.8")
		writeFile(t, filepath.Join(srv.Dir, "plugins", "AIBuildBattle.jar"), data)
		res, err := l.Scan(ctx, srv, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Entries) != 1 || res.Entries[0].Status != FileIdentified || len(res.Warnings) != 0 {
			t.Fatalf("scan %s", jsonText(res))
		}
		sameJSON(t, "the identified record", res.Entries[0].Identified, &want)

		res, err = l.Scan(ctx, srv, []Installed{want}, true)
		if err != nil || !slices.Equal(statuses(res), []FileStatus{FileManaged}) {
			t.Errorf("once adopted: %v, %v", statuses(res), err)
		}
	})

	t.Run("beside files Modrinth knows", func(t *testing.T) {
		f := newFakes(t)
		l := f.library()
		srv := newServer(t, "paper", "26.2")
		plugins := filepath.Join(srv.Dir, "plugins")
		backwards := f.mfile("SxGhdsPK").data
		writeFile(t, filepath.Join(plugins, "AIBuildBattle.jar"), data)
		writeFile(t, filepath.Join(plugins, "ViaBackwards.jar"), backwards)
		res, err := l.Scan(ctx, srv, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := statuses(res); !slices.Equal(got, []FileStatus{FileIdentified, FileIdentified}) {
			t.Fatalf("statuses %v", got)
		}
		if got := res.Entries[0].Identified; got == nil || got.Source != Playkeeper || got.Hash != want.Hash {
			t.Errorf("AIBuildBattle.jar was identified as %+v", got)
		}
		var body struct{ Hashes []string }
		reqs := f.sentTo("modrinth", "/v2/version_files")
		if len(reqs) != 1 || json.Unmarshal([]byte(reqs[0].body), &body) != nil || !slices.Equal(body.Hashes, []string{sha512hex(backwards)}) {
			t.Errorf("Modrinth was asked about %+v", reqs)
		}
		if s := f.strays(); len(s) > 0 {
			t.Errorf("requests went to %v", s)
		}
	})
}

func TestPlaykeeperSeesACopyAddedByHand(t *testing.T) {
	l := offline(t)
	srv := newServer(t, "paper", "1.21.8")
	writeFile(t, filepath.Join(srv.Dir, "plugins", "AIBuildBattle-dev.jar"), pluginJar(t, "AIBuildBattle", "0.0.9"))
	p, err := l.PlanInstall(context.Background(), srv, nil, InstallRequest{Source: Playkeeper, Project: "ai-build-battle"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Ready || len(p.Blockers) != 1 || p.Blockers[0].Kind != KindDuplicate || !strings.Contains(p.Blockers[0].Msg, "AIBuildBattle-dev.jar") {
		t.Errorf("plan %s", jsonText(p))
	}
}

func TestAPlaykeeperFileIsCheckedBeforeTheFolder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(s *Step)
		kind   Kind
	}{
		{"another hash", func(s *Step) { s.Hash = strings.Repeat("0", 64) }, KindHashMismatch},
		{"another size", func(s *Step) { s.Size++ }, KindSizeMismatch},
		{"a plugin it doesn't have", func(s *Step) { s.ProjectID = "no-such-plugin" }, KindNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			l := offline(t)
			srv := newServer(t, "paper", "1.21.8")
			p, err := l.PlanInstall(ctx, srv, nil, InstallRequest{Source: Playkeeper, Project: "ai-build-battle"})
			if err != nil {
				t.Fatal(err)
			}
			tc.change(&p.Steps[0])
			_, err = l.apply(ctx, srv, p, nil)
			wantKind(t, err, tc.kind)
			if got := ls(t, filepath.Join(srv.Dir, "plugins")); got != nil {
				t.Errorf("plugins holds %v", got)
			}
			if ls(t, l.TempDir) != nil {
				t.Errorf("temporary files left: %v", ls(t, l.TempDir))
			}
		})
	}
}

func TestPlaykeeperIsNotSearched(t *testing.T) {
	for _, typ := range []string{"paper", "purpur", "fabric", "quilt", "neoforge", "forge"} {
		tg, err := TargetFor(typ)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(tg.Sources(), Playkeeper) {
			t.Errorf("%s servers search Playkeeper", typ)
		}
	}
	_, err := offline(t).Search(context.Background(), newServer(t, "paper", "1.21.8"), Query{Source: Playkeeper, Text: "build"})
	wantKind(t, err, KindSourceUnsupported)
}
