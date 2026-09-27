package modpacks

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

func vpRef(version string) Ref {
	return Ref{Source: addons.Modrinth, Project: "vanilla-perfected", Version: version}
}

func recordFiles(r Record) map[string]File {
	out := map[string]File{}
	for _, f := range r.Files {
		out[f.Path] = f
	}
	return out
}

// Vanilla Perfected 1.0.3 changes four settings files, renames four mods and
// swaps a shader pack. The user changed two of the pack's settings, deleted
// a setting and a mod, and added a mod of their own.
func TestUpdateVanillaPerfected(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.1.2")
	rec := roundTrip(t, mustInstall(t, l, srv, InstallRequest{Ref: vpRef("jmYEuzNA")}).Record)
	if rec.Pack.VersionID != "jmYEuzNA" || rec.Pack.Channel != "beta" || len(rec.Files) != 16 {
		t.Fatalf("installed %s (%s) with %d files", rec.Pack.VersionID, rec.Pack.Channel, len(rec.Files))
	}

	chk, err := l.CheckUpdate(ctx, rec, false)
	if err != nil {
		t.Fatal(err)
	}
	// A beta is installed, so betas count. The newest version for Minecraft
	// 26.1.2 is a release; newer betas are for 26.2.
	if chk.Latest == nil || chk.Latest.ID != "zCNpmrT6" || chk.Newest == nil || chk.Newest.ID != "Bu8RKHri" ||
		chk.Newest.MinecraftVersion != "26.2" || chk.Current.VersionID != "jmYEuzNA" {
		t.Errorf("update check: %+v", chk)
	}

	writeFile(t, srv, "config/iris.properties", "shaderPack=mine\n")
	writeFile(t, srv, "config/continuity.json", "{}\n")
	for _, p := range []string{"config/MouseTweaks.cfg", "mods/lithium-fabric-0.24.3+mc26.1.2.jar"} {
		if err := os.Remove(filepath.Join(srv.Dir, p)); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, srv, "mods/mine.jar", "my own mod")

	pl, err := l.PlanUpdate(ctx, srv, rec, UpdateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if pl.Pack.VersionID != "zCNpmrT6" || pl.Current == nil || pl.Current.VersionID != "jmYEuzNA" || !pl.Ready {
		t.Errorf("plan moves %v to %s, ready %v", pl.Current, pl.Pack.VersionID, pl.Ready)
	}
	wantList(t, "changes", changeList(pl.Changes),
		"keep config/continuity.json (yours)",
		"replace config/iris.properties (yours)",
		"replace config/packed_packs/preferences.properties",
		"replace config/simple_datapacks.json",
		"remove mods/fabric-api-0.150.0+26.1.2.jar",
		"add mods/fabric-api-0.152.1+26.1.2.jar",
		"remove mods/fabric-language-kotlin-1.13.11+kotlin.2.3.21.jar",
		"add mods/fabric-language-kotlin-1.13.12+kotlin.2.4.0.jar",
		"remove mods/pickupnotifications-2.0.2+26.1.jar",
		"add mods/pickupnotifications-fabric-3.0.1+26.1.jar")
	// The deleted mod's update is left out because it is the same project.
	wantList(t, "skipped", skippedList(pl.Skipped),
		"user_removed config/MouseTweaks.cfg",
		"user_removed mods/lithium-fabric-0.24.5+mc26.1.2.jar",
		"client_content options.txt",
		"client_content resourcepacks/Almost Vanilla Potions.zip",
		"client_content resourcepacks/Fresh Buckets.zip.rpo",
		"client_content shaderpacks/ComplementaryReimagined_r5.8.1.zip.txt",
		"client_content shaderpacks/I Like Vanilla v1.3.6b.zip",
		"client_content shaderpacks/I Like Vanilla v1.3.6b.zip.txt")
	var added int64
	for _, c := range pl.Changes {
		if c.Action == ActionAdd {
			added += c.Size
		}
	}
	if pl.Unchanged != 7 || pl.DownloadSize != added || len(pl.Warnings) != 0 || len(pl.Manual) != 0 {
		t.Errorf("unchanged %d, download %d of %d, warnings %q, manual %v", pl.Unchanged, pl.DownloadSize, added, noticeList(pl.Warnings), pl.Manual)
	}

	orig := readFile(t, srv, "config/animatica.properties")
	writeFile(t, srv, "config/animatica.properties", "changed after the plan")
	_, err = l.Update(ctx, srv, rec, UpdateRequest{Fingerprint: pl.Fingerprint})
	wantKind(t, err, addons.KindPlanChanged)
	writeFile(t, srv, "config/animatica.properties", orig)

	res, err := l.Update(ctx, srv, rec, UpdateRequest{Fingerprint: pl.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	wantList(t, "kept", res.Kept, "config/continuity.json")
	wantTree(t, srv.Dir,
		"config/", "config/animatica.properties", "config/continuity.json", "config/iris.properties",
		"config/litematica/", "config/litematica/litematica_Mom's House.json",
		"config/packed_packs/", "config/packed_packs/preferences.properties", "config/simple_datapacks.json",
		"credits.txt", "datapacks/", "datapacks/AllMobHeads_V11.zip",
		"mods/", "mods/MouseTweaks-fabric-mc26.1-2.31.jar", "mods/cloth-config-26.1.154.jar",
		"mods/fabric-api-0.152.1+26.1.2.jar", "mods/fabric-language-kotlin-1.13.12+kotlin.2.4.0.jar", "mods/mine.jar",
		"mods/pickupnotifications-fabric-3.0.1+26.1.jar", "mods/placeholder-api-3.0.0+26.1.jar")
	iris, err := os.ReadFile("testdata/packs/vanilla-perfected-1.0.3/overrides/config/iris.properties")
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, srv, "config/iris.properties") != string(iris) || readFile(t, srv, "config/continuity.json") != "{}\n" ||
		readFile(t, srv, "mods/mine.jar") != "my own mod" {
		t.Error("the update did not leave the files as planned")
	}

	rec2 := roundTrip(t, res.Record)
	files := recordFiles(rec2)
	continuity, _ := os.ReadFile("testdata/packs/vanilla-perfected-1.0.3/overrides/config/continuity.json")
	if len(files) != 14 || files["config/iris.properties"].Hash != sha512hex(iris) ||
		files["config/continuity.json"].Hash != sha512hex(continuity) || files["mods/mine.jar"].Path != "" {
		t.Errorf("record files: %+v", rec2.Files)
	}
	sameJSON(t, "excluded", rec2.Excluded, []Excluded{
		{Path: "config/MouseTweaks.cfg"},
		{Path: "mods/lithium-fabric-0.24.5+mc26.1.2.jar", Project: "gvQqBUqZ"},
	})
	if rec2.Pack.VersionID != "zCNpmrT6" || !rec2.Pack.InstalledAt.Equal(testNow) || rec2.Requirements != (Requirements{"fabric", "26.1.2", "0.19.2"}) {
		t.Errorf("record: %+v %+v", rec2.Pack, rec2.Requirements)
	}

	_, err = l.PlanUpdate(ctx, srv, rec2, UpdateRequest{})
	if e := wantKind(t, err, addons.KindUpToDate); e.Msg != "Vanilla Perfected is up to date (version 1.0.3+26.1.2)." {
		t.Errorf("message %q", e.Msg)
	}
	_, err = l.PlanUpdate(ctx, srv, rec2, UpdateRequest{Version: "zCNpmrT6"})
	wantKind(t, err, addons.KindUpToDate)
	_, err = l.PlanUpdate(ctx, srv, rec2, UpdateRequest{Version: "EZaeTUP8"})
	if e := wantKind(t, err, addons.KindNotFound); e.Msg != `Vanilla Perfected has no version "EZaeTUP8".` {
		t.Errorf("message %q", e.Msg)
	}
}

func TestUpdateKeepsWhatTheUserAsks(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.1.2")
	rec := roundTrip(t, mustInstall(t, l, srv, InstallRequest{Ref: vpRef("jmYEuzNA")}).Record)
	writeFile(t, srv, "config/iris.properties", "shaderPack=mine\n")
	if err := os.Remove(filepath.Join(srv.Dir, "mods", "lithium-fabric-0.24.3+mc26.1.2.jar")); err != nil {
		t.Fatal(err)
	}

	// Deleted pack files come back only when asked for; files the pack
	// needs cannot be turned off.
	req := UpdateRequest{
		Keep:    []string{"config/iris.properties"},
		Include: map[string]bool{"mods/lithium-fabric-0.24.5+mc26.1.2.jar": true, "mods/fabric-api-0.152.1+26.1.2.jar": false},
	}
	pl, err := l.PlanUpdate(ctx, srv, rec, req)
	if err != nil {
		t.Fatal(err)
	}
	wantList(t, "changes", changeList(pl.Changes),
		"keep config/iris.properties (yours)",
		"replace config/packed_packs/preferences.properties",
		"replace config/simple_datapacks.json",
		"remove mods/fabric-api-0.150.0+26.1.2.jar",
		"add mods/fabric-api-0.152.1+26.1.2.jar",
		"remove mods/fabric-language-kotlin-1.13.11+kotlin.2.3.21.jar",
		"add mods/fabric-language-kotlin-1.13.12+kotlin.2.4.0.jar",
		"add mods/lithium-fabric-0.24.5+mc26.1.2.jar",
		"remove mods/pickupnotifications-2.0.2+26.1.jar",
		"add mods/pickupnotifications-fabric-3.0.1+26.1.jar")
	req.Fingerprint = pl.Fingerprint
	res, err := l.Update(ctx, srv, rec, req)
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, srv, "config/iris.properties") != "shaderPack=mine\n" {
		t.Error("the user's settings were overwritten")
	}
	rec2 := roundTrip(t, res.Record)
	if f := recordFiles(rec2)["config/iris.properties"]; !f.Preexisting || len(rec2.Excluded) != 0 {
		t.Errorf("record: %+v, excluded %v", f, rec2.Excluded)
	}
	// A file the user kept is theirs: removing the pack leaves it.
	rp, err := l.PlanRemove(srv, rec2)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rp.Changes {
		if c.Path == "config/iris.properties" && (c.Action != ActionKeep || !c.Yours) {
			t.Errorf("removal would %s the user's settings", c.Action)
		}
	}
}

func TestCheckUpdate(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	l := f.library()
	rec := Record{
		Pack: addons.Installed{
			Source: addons.Modrinth, ProjectID: "BYN9yKrV", Slug: "adrenaline", Name: "Adrenaline", VersionID: "r5uSD3JV",
			VersionNumber: "26.4.2+mc26.2.fabric", Channel: "release", Published: time.Date(2026, 7, 28, 19, 12, 37, 193647000, time.UTC),
		},
		Requirements: Requirements{Type: "fabric", MinecraftVersion: "26.2", LoaderVersion: "0.19.2"},
	}
	chk, err := l.CheckUpdate(ctx, rec, false)
	if err != nil {
		t.Fatal(err)
	}
	// 26.5.0 for Minecraft 26.2 is the update; the 26.3 version is a beta
	// and the others are for older Minecraft versions.
	if chk.Latest == nil || chk.Latest.ID != "EZaeTUP8" || chk.Latest.Number != "26.5.0+mc26.2.fabric" || chk.Newest != nil {
		t.Errorf("update check: %+v", chk)
	}
	if chk, err = l.CheckUpdate(ctx, rec, true); err != nil || chk.Latest == nil || chk.Latest.ID != "EZaeTUP8" ||
		chk.Newest == nil || chk.Newest.ID != "afEtFNbW" || chk.Newest.Channel != "beta" || chk.Newest.MinecraftVersion != "26.3" {
		t.Errorf("update check with pre-releases: %+v, %v", chk, err)
	}
}

func TestUpdateToAnotherMinecraftVersionOrServerType(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.2")
	a := f.indexFile("mods/a-1.jar", []byte("a1"))
	rec := roundTrip(t, mustInstall(t, l, srv, InstallRequest{Ref: testRef(f.addPack(testIndex(a)))}).Record)

	quilt := testIndex(a)
	quilt["dependencies"] = obj{"minecraft": "26.2", "quilt-loader": "0.30.0"}
	vq := f.addPack(quilt)
	f.editVersion(vq, func(v obj) { v["loaders"] = []any{"quilt"} })
	older := testIndex(a)
	older["dependencies"] = obj{"minecraft": "26.1", "fabric-loader": "0.19.5"}
	vo := f.addPack(older)
	f.editVersion(vo, func(v obj) { v["game_versions"] = []any{"26.1"} })

	// The newest version is for an older Minecraft version, which would be
	// a downgrade, so the Quilt version is offered.
	chk, err := l.CheckUpdate(ctx, rec, false)
	if err != nil || chk.Latest != nil || chk.Newest == nil || chk.Newest.ID != vq || chk.Newest.Type != "quilt" {
		t.Errorf("update check: %+v, %v", chk, err)
	}
	_, err = l.PlanUpdate(ctx, srv, rec, UpdateRequest{})
	wantKind(t, err, addons.KindUpToDate)

	pl, err := l.PlanUpdate(ctx, srv, rec, UpdateRequest{Version: vq})
	if err != nil {
		t.Fatal(err)
	}
	wantList(t, "warnings", noticeList(pl.Warnings),
		"server_type_change: Test Pack needs a Quilt server on Minecraft 26.2; this server runs Fabric 26.2, and the update switches it.")
	if pl.Requirements != (Requirements{"quilt", "26.2", "0.30.0"}) || !pl.Ready || len(pl.Changes) != 0 || pl.Unchanged != 1 {
		t.Errorf("plan: %+v, changes %q", pl.Requirements, changeList(pl.Changes))
	}

	// Without a world, going back to an older Minecraft version is allowed.
	if pl, err = l.PlanUpdate(ctx, srv, rec, UpdateRequest{Version: vo}); err != nil || !pl.Ready {
		t.Fatalf("plan: %v, blockers %q", err, noticeList(pl.Blockers))
	}
	writeFile(t, srv, "world/level.dat", "a world")
	pl, err = l.PlanUpdate(ctx, srv, rec, UpdateRequest{Version: vo})
	if err != nil {
		t.Fatal(err)
	}
	wantList(t, "blockers", noticeList(pl.Blockers),
		"minecraft_downgrade: Test Pack is for Minecraft 26.1, older than this server's 26.2, and Minecraft cannot open a world in an older version.")
	_, err = l.Update(ctx, srv, rec, UpdateRequest{Version: vo})
	wantKind(t, err, KindDowngrade)

	v4 := f.addPack(testIndex(f.indexFile("mods/a-2.jar", []byte("a2"))))
	if chk, err = l.CheckUpdate(ctx, rec, false); err != nil || chk.Latest == nil || chk.Latest.ID != v4 || chk.Newest != nil {
		t.Errorf("update check: %+v, %v", chk, err)
	}
}

func TestUpdateTouchesOnlyThePacksFiles(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.2")
	opt := f.indexFile("mods/opt-1.jar", []byte("opt"), "optional", "optional")
	b := f.indexFile("mods/b-1.jar", []byte("b1"))
	v1 := f.addPack(testIndex(f.indexFile("mods/a-1.jar", []byte("a1")), b, opt),
		entry{name: "overrides/config/x.txt", data: []byte("x1")},
		entry{name: "overrides/config/drop.txt", data: []byte("d1")},
		entry{name: "overrides/world/datapacks/w.zip", data: []byte("w1")})
	res := mustInstall(t, l, srv, InstallRequest{Ref: testRef(v1), Include: map[string]bool{"mods/opt-1.jar": false}})
	rec := roundTrip(t, res.Record)
	writeFile(t, srv, "config/drop.txt", "mine")

	f.addPack(testIndex(f.indexFile("mods/a-2.jar", []byte("a2")), b, opt),
		entry{name: "overrides/config/x.txt", data: []byte("x2")},
		entry{name: "overrides/world/datapacks/w.zip", data: []byte("w2")})
	pl, err := l.PlanUpdate(ctx, srv, rec, UpdateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// The user changed a file the new version drops, so it stays as theirs.
	// The world is the server's once it exists.
	wantList(t, "changes", changeList(pl.Changes),
		"keep config/drop.txt (yours)",
		"replace config/x.txt",
		"remove mods/a-1.jar",
		"add mods/a-2.jar")
	wantList(t, "skipped", skippedList(pl.Skipped), "optional_off mods/opt-1.jar", "world_files world/datapacks/w.zip")
	sameJSON(t, "optional", pl.Optional, []Optional{{Path: "mods/opt-1.jar", Name: "opt-1.jar", Project: "opt", Size: 3}})
	if pl.Unchanged != 1 {
		t.Errorf("unchanged %d", pl.Unchanged)
	}
	if on, err := l.PlanUpdate(ctx, srv, rec, UpdateRequest{Include: map[string]bool{"mods/opt-1.jar": true}}); err != nil ||
		!slices.Contains(changeList(on.Changes), "add mods/opt-1.jar") {
		t.Errorf("turning the optional file on: %v", err)
	}

	up, err := l.Update(ctx, srv, rec, UpdateRequest{Fingerprint: pl.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	wantTree(t, srv.Dir, "config/", "config/drop.txt", "config/x.txt", "mods/", "mods/a-2.jar", "mods/b-1.jar",
		"world/", "world/datapacks/", "world/datapacks/w.zip")
	if readFile(t, srv, "config/drop.txt") != "mine" || readFile(t, srv, "config/x.txt") != "x2" || readFile(t, srv, "world/datapacks/w.zip") != "w1" {
		t.Error("the update did not leave the files as planned")
	}
	wantList(t, "kept", up.Kept, "config/drop.txt")
	var paths []string
	for _, file := range up.Record.Files {
		paths = append(paths, file.Path)
	}
	wantList(t, "record", paths, "config/x.txt", "mods/a-2.jar", "mods/b-1.jar")
	sameJSON(t, "excluded", up.Record.Excluded, []Excluded{{Path: "mods/opt-1.jar", Project: "opt"}})
}

// If writing an update fails part way, the server's folder is put back as
// it was.
func TestFailedUpdateIsTakenBack(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.2")
	v1 := f.addPack(testIndex(f.indexFile("mods/a-1.jar", []byte("a1"))), entry{name: "overrides/config/x.txt", data: []byte("x1")})
	rec := roundTrip(t, mustInstall(t, l, srv, InstallRequest{Ref: testRef(v1)}).Record)
	a2 := f.indexFile("mods/a-2.jar", []byte("a2"))
	f.addPack(testIndex(a2, f.indexFile("mods/c-1.jar", []byte("c1"))), entry{name: "overrides/config/x.txt", data: []byte("x2")})
	pl, err := l.PlanUpdate(ctx, srv, rec, UpdateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	wantList(t, "changes", changeList(pl.Changes), "replace config/x.txt", "remove mods/a-1.jar", "add mods/a-2.jar", "add mods/c-1.jar")

	// While the new files download, something puts a file where the update
	// adds one.
	f.hook(f.parse(strs(a2["downloads"])[0]).Path, func(w http.ResponseWriter, r *http.Request) {
		os.WriteFile(filepath.Join(srv.Dir, "mods", "c-1.jar"), []byte("someone else's"), 0o644)
		w.Write([]byte("a2"))
	})
	_, err = l.Update(ctx, srv, rec, UpdateRequest{Fingerprint: pl.Fingerprint})
	wantKind(t, err, addons.KindPlanChanged)
	wantTree(t, srv.Dir, "config/", "config/x.txt", "mods/", "mods/a-1.jar", "mods/c-1.jar")
	if readFile(t, srv, "config/x.txt") != "x1" || readFile(t, srv, "mods/a-1.jar") != "a1" || readFile(t, srv, "mods/c-1.jar") != "someone else's" {
		t.Error("the server's folder was not put back")
	}
	wantTree(t, l.TempDir)
}
