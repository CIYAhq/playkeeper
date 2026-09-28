package modpacks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/modpacks/curseforge"
)

// cfRef names a file of the example Fabric pack on CurseForge.
func cfRef(file string) Ref {
	return Ref{Source: CurseForge, Project: "9100001", Version: file}
}

func manualList(ms []addons.ManualStep) []string {
	out := []string{}
	for _, m := range ms {
		out = append(out, string(m.Kind)+": "+m.Msg+" ("+m.URL+")")
	}
	return out
}

const ferriteCoreStep = string(addons.KindExternal) + ": FerriteCore's author only allows downloads through CurseForge's app, so Playkeeper cannot download ferritecore-9.0.0-fabric.jar for you. (https://www.curseforge.com/minecraft/mc-mods/ferritecore/files/7806040)"

func TestInstallCurseForgePack(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "", "")
	pl := mustPlan(t, l, srv, InstallRequest{Ref: cfRef("")})
	// The newest file is an alpha, so the newest release is picked.
	p := pl.Pack
	if p.Source != CurseForge || p.ProjectID != "9100001" || p.Slug != "example-fabric-pack" || p.Name != "Example Fabric Pack" ||
		p.IconURL != "https://media.forgecdn.net/avatars/thumbnails/900/1/256/256/logo.png" || p.VersionID != "9200002" ||
		p.VersionNumber != "Example Fabric Pack 2.0.0" || p.Channel != "release" || p.FileName != "Example Fabric Pack 2.0.0.zip" ||
		!p.Published.Equal(time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)) || p.HashAlgo != "sha1" || len(p.Hash) != 40 {
		t.Errorf("pack: %+v", p)
	}
	if pl.Requirements != (Requirements{"fabric", "26.2", "0.19.5"}) || !pl.Ready {
		t.Errorf("requirements %+v, blockers %q", pl.Requirements, noticeList(pl.Blockers))
	}
	wantList(t, "changes", changeList(pl.Changes),
		"add config/example.json",
		"add mods/cloth-config-26.3.155-fabric.jar",
		"add mods/fabric-api-0.141.0+26.3.jar",
		"add mods/lithium-fabric-0.25.3+mc26.3.jar",
		"add mods/placeholder-api-3.1.0+26.3.jar")
	wantList(t, "skipped", skippedList(pl.Skipped),
		"client_only mods/sodium-fabric-0.9.2+mc26.3.jar",
		"client_content options.txt",
		"client_content resourcepacks/Chat-Reporting-Helper-26.3.zip",
		"client_content resourcepacks/Translations-for-Sodium-26.3.zip",
		"protected_path server.properties")
	// FerriteCore's author forbids downloads outside CurseForge's app, so
	// the user gets its page instead.
	wantList(t, "manual", manualList(pl.Manual), ferriteCoreStep)
	sameJSON(t, "properties", pl.Properties, map[string]string{"difficulty": "hard", "view-distance": "8"})
	wantList(t, "warnings", noticeList(pl.Warnings),
		"server_properties: Example Fabric Pack ships server settings that Playkeeper does not take from packs: motd, server-port.")

	res := mustInstall(t, l, srv, InstallRequest{Ref: cfRef("")})
	wantTree(t, srv.Dir, "config/", "config/example.json", "mods/", "mods/cloth-config-26.3.155-fabric.jar",
		"mods/fabric-api-0.141.0+26.3.jar", "mods/lithium-fabric-0.25.3+mc26.3.jar", "mods/placeholder-api-3.1.0+26.3.jar")
	lithium := generated("lithium-fabric-0.25.3+mc26.3.jar")
	if readFile(t, srv, "mods/lithium-fabric-0.25.3+mc26.3.jar") != string(lithium) ||
		readFile(t, srv, "config/example.json") != `{"pack": "Example Fabric Pack 2.0.0"}`+"\n" {
		t.Error("the files written are not the pack's")
	}
	rec := roundTrip(t, res.Record)
	files := recordFiles(rec)
	if lf := files["mods/lithium-fabric-0.25.3+mc26.3.jar"]; lf.HashAlgo != "sha1" || lf.Hash != sha1hex(lithium) || lf.Project != "360438" || lf.Origin != Download {
		t.Errorf("lithium's record: %+v", lf)
	}
	if ex := files["config/example.json"]; ex.HashAlgo != "sha512" || ex.Origin != Override || len(files) != 5 {
		t.Errorf("record files: %+v", rec.Files)
	}
	wantList(t, "manual after installing", manualList(res.Manual), ferriteCoreStep)
	for _, r := range f.served("forgecdn") {
		if strings.Contains(r, "ferritecore") {
			t.Errorf("Playkeeper downloaded a file its author forbids: %s", r)
		}
	}
	if len(f.offHosts) != 0 || f.evilHits != 0 {
		t.Errorf("contacted %q", f.offHosts)
	}
}

// A CurseForge pack's mods that Modrinth lists as for the game client only,
// by the same file, stay off the server even when CurseForge doesn't tag
// them, as such a mod stops the server's first start: by the version's
// environment, or by the project's server_side for versions from before
// it. A client-only mod only CurseForge's app may download needs no step.
// When Modrinth can't be asked, every mod goes on, with a warning.
func TestCurseForgeClientModsModrinthKnowsStayOff(t *testing.T) {
	f := newFakes(t)
	f.modrinthMod("CLOTH001", "cloth-config-26.3.155-fabric.jar", generated("cloth-config-26.3.155-fabric.jar"), "client_only", "unknown")
	f.modrinthMod("LITHI001", "lithium-fabric-0.25.3+mc26.3.jar", generated("lithium-fabric-0.25.3+mc26.3.jar"), "", "unsupported")
	f.modrinthMod("FERRI001", "ferritecore-9.0.0-fabric.jar", generated("ferritecore-9.0.0-fabric.jar"), "client_only", "unknown")
	f.modrinthMod("FAPI0001", "fabric-api-0.141.0+26.3.jar", generated("fabric-api-0.141.0+26.3.jar"), "client_and_server", "required")
	pl := mustPlan(t, f.library(), newServer(t, "", ""), InstallRequest{Ref: cfRef("")})
	wantList(t, "changes", changeList(pl.Changes),
		"add config/example.json",
		"add mods/fabric-api-0.141.0+26.3.jar",
		"add mods/placeholder-api-3.1.0+26.3.jar")
	for _, want := range []string{"cloth-config-26.3.155-fabric.jar", "ferritecore-9.0.0-fabric.jar", "lithium-fabric-0.25.3+mc26.3.jar", "sodium-fabric-0.9.2+mc26.3.jar"} {
		if !slices.Contains(skippedList(pl.Skipped), "client_only mods/"+want) {
			t.Errorf("%s isn't left off as client only: %q", want, skippedList(pl.Skipped))
		}
	}
	wantList(t, "manual", manualList(pl.Manual))
	wantList(t, "warnings", noticeList(pl.Warnings),
		"server_properties: Example Fabric Pack ships server settings that Playkeeper does not take from packs: motd, server-port.")

	// The pack's own datapack uses Lithium's particles, so Lithium stays;
	// text that isn't a resource location keeps nothing.
	id := f.cfAddFile(nil,
		entry{name: "overrides/datapacks/spring/data/spring/worldgen/biome/spring.json", data: []byte(`{"effects": {"particle": {"options": {"type": "lithium:spark"}}}}`)},
		entry{name: "overrides/config/notes.txt", data: []byte("Cloth Config: see clothconfig docs\n")},
		entry{name: "overrides/datapacks/spring/data/spring/icon.png", data: []byte("cloth-config:not-read")})
	pl = mustPlan(t, f.library(), newServer(t, "", ""), InstallRequest{Ref: cfRef(strconv.FormatInt(id, 10))})
	wantList(t, "changes with a datapack using Lithium", changeList(pl.Changes),
		"add config/example.json",
		"add config/notes.txt",
		"add datapacks/spring/data/spring/icon.png",
		"add datapacks/spring/data/spring/worldgen/biome/spring.json",
		"add mods/fabric-api-0.141.0+26.3.jar",
		"add mods/lithium-fabric-0.25.3+mc26.3.jar",
		"add mods/placeholder-api-3.1.0+26.3.jar")

	f.hook("modrinth /v2/version_files", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	pl = mustPlan(t, f.library(), newServer(t, "", ""), InstallRequest{Ref: cfRef("9200002")})
	wantList(t, "changes when Modrinth can't be asked", changeList(pl.Changes),
		"add config/example.json",
		"add mods/cloth-config-26.3.155-fabric.jar",
		"add mods/fabric-api-0.141.0+26.3.jar",
		"add mods/lithium-fabric-0.25.3+mc26.3.jar",
		"add mods/placeholder-api-3.1.0+26.3.jar")
	wantList(t, "warnings when Modrinth can't be asked", noticeList(pl.Warnings),
		"environment_unknown: Playkeeper could not ask Modrinth which of Example Fabric Pack's mods are for the game client only, so it installs them all.",
		"server_properties: Example Fabric Pack ships server settings that Playkeeper does not take from packs: motd, server-port.")
}

// A client-only mod of a CurseForge pack that another of its mods requires
// by CurseForge's own lists goes on the server: Sodium, which CurseForge
// tags for players' games, once Lithium requires it. An optional dependency
// doesn't count.
func TestCurseForgeClientModAServerModRequiresGoesOn(t *testing.T) {
	const sodium = "mods/sodium-fabric-0.9.2+mc26.3.jar"
	for _, c := range []struct {
		name     string
		relation int
		on       bool
	}{
		{"required", curseforge.RequiredDependency, true},
		{"optional", 2, false},
	} {
		f := newFakes(t)
		f.cfChange(8895969, func(file obj) { file["dependencies"] = []any{obj{"modId": 394468, "relationType": c.relation}} })
		pl := mustPlan(t, f.library(), newServer(t, "", ""), InstallRequest{Ref: cfRef("")})
		on := slices.Contains(changeList(pl.Changes), "add "+sodium)
		if on != c.on || slices.Contains(skippedList(pl.Skipped), "client_only "+sodium) == c.on {
			t.Errorf("%s: changes %q, skipped %q", c.name, changeList(pl.Changes), skippedList(pl.Skipped))
		}
	}
}

func TestUpdateCurseForgePack(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.2")
	rec := roundTrip(t, mustInstall(t, l, srv, InstallRequest{Ref: cfRef("9200001")}).Record)

	chk, err := l.CheckUpdate(ctx, rec, false)
	if err != nil || chk.Latest == nil || chk.Latest.ID != "9200002" || chk.Latest.Number != "Example Fabric Pack 2.0.0" || chk.Newest != nil {
		t.Errorf("update check: %+v, %v", chk, err)
	}
	chk, err = l.CheckUpdate(ctx, rec, true)
	if err != nil || chk.Newest == nil || chk.Newest.ID != "9200003" || chk.Newest.Channel != "alpha" ||
		chk.Newest.MinecraftVersion != "26.3" || chk.Newest.Type != "fabric" {
		t.Errorf("update check with pre-releases: %+v, %v", chk, err)
	}

	pl, err := l.PlanUpdate(ctx, srv, rec, UpdateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	wantList(t, "changes", changeList(pl.Changes), "replace config/example.json")
	wantList(t, "manual", manualList(pl.Manual), ferriteCoreStep)
	if pl.Unchanged != 4 || pl.Pack.VersionID != "9200002" || pl.Current.VersionID != "9200001" {
		t.Errorf("unchanged %d, moving %s to %s", pl.Unchanged, pl.Current.VersionID, pl.Pack.VersionID)
	}
	res, err := l.Update(ctx, srv, rec, UpdateRequest{Fingerprint: pl.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, srv, "config/example.json") != `{"pack": "Example Fabric Pack 2.0.0"}`+"\n" || res.Record.Pack.VersionID != "9200002" {
		t.Error("the update did not happen as planned")
	}

	// Without a key, a CurseForge pack can still be removed.
	l.CurseForge = nil
	rp, err := l.PlanRemove(srv, res.Record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Remove(srv, res.Record, rp.Fingerprint); err != nil {
		t.Fatal(err)
	}
	wantTree(t, srv.Dir, "config/", "mods/")
}

func TestRefusedCurseForgePacks(t *testing.T) {
	loader := func(id string) func(m obj) {
		return func(m obj) { m["minecraft"].(obj)["modLoaders"] = []any{obj{"id": id, "primary": true}} }
	}
	for _, c := range []struct {
		name string
		ref  func(f *fakes) Ref
		kind addons.Kind
		msg  string
	}{
		{"malware", func(f *fakes) Ref {
			f.cfChange(9200002, func(file obj) { file["fileStatus"] = curseforge.StatusMalwareDetected })
			return cfRef("9200002")
		}, KindMalware, "CurseForge flagged this version of Example Fabric Pack as malware."},
		{"server pack", func(f *fakes) Ref { return cfRef("9200004") }, addons.KindInvalid,
			"That file is Example Fabric Pack's server pack; Playkeeper installs the pack itself and leaves out what servers do not need."},
		{"no download address", func(f *fakes) Ref {
			f.cfChange(9200002, func(file obj) { file["downloadUrl"] = nil })
			return cfRef("9200002")
		}, addons.KindExternal, "Example Fabric Pack's author only allows downloading it through CurseForge's app, so Playkeeper cannot install it."},
		{"pack on another host", func(f *fakes) Ref {
			f.cfChange(9200002, func(file obj) { file["downloadUrl"] = f.url(f.evil, "/pack.zip") })
			return cfRef("9200002")
		}, addons.KindHostNotAllowed, "Example Fabric Pack 2.0.0.zip would be downloaded from 127.0.0.1, which is not a host packs may download from."},
		{"pack is not the size listed", func(f *fakes) Ref {
			f.cfServe(9200002, []byte("another pack"))
			return cfRef("9200002")
		}, addons.KindSizeMismatch, "The download of Example Fabric Pack 2.0.0.zip is not the size CurseForge lists, so Playkeeper did not install Example Fabric Pack."},
		{"pack does not match its hash", func(f *fakes) Ref {
			var n int
			f.cfChange(9200002, func(file obj) { n = int(num(file["fileLength"])) })
			f.cfServe(9200002, bytes.Repeat([]byte("x"), n))
			return cfRef("9200002")
		}, addons.KindHashMismatch, "The download of Example Fabric Pack 2.0.0.zip does not match the sha1 hash CurseForge lists, so Playkeeper did not install Example Fabric Pack."},
		{"another project's file", func(f *fakes) Ref { return cfRef("8913512") }, addons.KindNotFound, `Example Fabric Pack has no version "8913512".`},
		{"a mod", func(f *fakes) Ref { return Ref{Source: CurseForge, Project: "306612"} }, KindNotModpack, "Fabric API is not a modpack on CurseForge."},
		{"no such project", func(f *fakes) Ref { return Ref{Source: CurseForge, Project: "1"} }, addons.KindNotFound, `CurseForge has no project "1".`},
		{"slug", func(f *fakes) Ref { return Ref{Source: CurseForge, Project: "example-fabric-pack"} }, addons.KindInvalid, "The pack's project is not valid."},
		{"not a file id", func(f *fakes) Ref { return cfRef("abc") }, addons.KindInvalid, "The pack's version is not valid."},
		{"a loader Playkeeper does not know in the manifest", func(f *fakes) Ref { return cfRef(strconv.FormatInt(f.cfAddFile(loader("liteloader-1.0")), 10)) },
			KindUnknownLoader, "Example Fabric Pack needs a mod loader Playkeeper does not know (liteloader)."},
		{"old Minecraft in the manifest", func(f *fakes) Ref {
			return cfRef(strconv.FormatInt(f.cfAddFile(func(m obj) { m["minecraft"].(obj)["version"] = "1.20" }), 10))
		}, KindMinecraft, "Example Fabric Pack is for Minecraft 1.20, and Playkeeper runs Minecraft 1.20.1 and newer."},
		{"no manifest", func(f *fakes) Ref {
			return cfRef(strconv.FormatInt(f.cfAddFile(func(m obj) { m["manifestType"] = "somethingElse" }), 10))
		}, KindBadPack, ""},
	} {
		f := newFakes(t)
		l := f.library()
		srv := newServer(t, "", "")
		_, err := l.PlanInstall(context.Background(), srv, nil, InstallRequest{Ref: c.ref(f)})
		var e *addons.Error
		if !errors.As(err, &e) || e.Kind != c.kind || c.msg != "" && e.Msg != c.msg {
			t.Errorf("%s: %v (kind %q), want kind %q: %s", c.name, err, addons.KindOf(err), c.kind, c.msg)
		}
		wantTree(t, srv.Dir)
		wantTree(t, l.TempDir)
		if f.evilHits != 0 {
			t.Errorf("%s: contacted a host CurseForge packs may not use", c.name)
		}
	}
}

func TestCurseForgeModsInThePack(t *testing.T) {
	lithium := func(change func(f *fakes, file obj)) func(f *fakes) {
		return func(f *fakes) { f.cfChange(8895969, func(file obj) { change(f, file) }) }
	}
	for _, c := range []struct {
		name   string
		change func(f *fakes)
		// kind and msg are the plan's one blocker; without one, the plan is
		// ready with the manual steps and without the file left out.
		kind    addons.Kind
		msg     string
		manual  []string
		leftOut string
	}{
		{name: "malware", change: lithium(func(f *fakes, file obj) { file["fileStatus"] = curseforge.StatusMalwareDetected }),
			kind: KindMalware, msg: "CurseForge flagged lithium-fabric-0.25.3+mc26.3.jar (Lithium), which Example Fabric Pack uses, as malware."},
		{name: "another host", change: lithium(func(f *fakes, file obj) { file["downloadUrl"] = f.url(f.evil, "/lithium.jar") }),
			kind: addons.KindHostNotAllowed, msg: "CurseForge lists lithium-fabric-0.25.3+mc26.3.jar at 127.0.0.1, which is not one of CurseForge's file hosts."},
		{name: "file name", change: lithium(func(f *fakes, file obj) { file["fileName"] = "../lithium.jar" }),
			kind: addons.KindBadFileName, msg: `Lithium offers a file named "../lithium.jar", which Playkeeper will not write to the server.`},
		{name: "no usable hash", change: lithium(func(f *fakes, file obj) { file["hashes"] = list(file["hashes"])[1:] }),
			kind: addons.KindNoHash, msg: "CurseForge lists no usable hash for lithium-fabric-0.25.3+mc26.3.jar, so Playkeeper cannot check the download."},
		{name: "gone", change: func(f *fakes) { f.cfChange(8913512, func(file obj) { file["isAvailable"] = false }) }, manual: []string{
			string(KindUnavailable) + ": Fabric API, which Example Fabric Pack uses, is no longer available on CurseForge. (https://www.curseforge.com/minecraft/mc-mods/fabric-api)",
			ferriteCoreStep,
		}, leftOut: "mods/fabric-api-0.141.0+26.3.jar"},
		{name: "not a mod", change: func(f *fakes) { f.cfChangeMod(360438, func(m obj) { m["classId"] = curseforge.ClassDataPacks }) }, manual: []string{
			string(KindOtherContent) + ": Example Fabric Pack uses Lithium (lithium-fabric-0.25.3+mc26.3.jar), which is not a mod, so Playkeeper does not install it on servers. (https://www.curseforge.com/minecraft/mc-mods/lithium/files/8895969)",
			ferriteCoreStep,
		}, leftOut: "mods/lithium-fabric-0.25.3+mc26.3.jar"},
	} {
		f := newFakes(t)
		l := f.library()
		srv := newServer(t, "fabric", "26.2")
		c.change(f)
		pl := mustPlan(t, l, srv, InstallRequest{Ref: cfRef("9200002")})
		if c.kind == "" {
			if !pl.Ready || len(pl.Changes) != 4 || slices.Contains(changeList(pl.Changes), "add "+c.leftOut) {
				t.Errorf("%s: ready %v, changes %q", c.name, pl.Ready, changeList(pl.Changes))
			}
			wantList(t, c.name+": manual", manualList(pl.Manual), c.manual...)
			continue
		}
		wantList(t, c.name+": blockers", noticeList(pl.Blockers), string(c.kind)+": "+c.msg)
		_, err := l.Install(context.Background(), srv, nil, InstallRequest{Ref: cfRef("9200002"), Fingerprint: pl.Fingerprint})
		if addons.KindOf(err) != c.kind {
			t.Errorf("%s: installing: %v", c.name, err)
		}
		wantTree(t, srv.Dir)
		if f.evilHits != 0 {
			t.Errorf("%s: contacted a host CurseForge files may not come from", c.name)
		}
	}

	// A mod that does not match CurseForge's hash stops the install.
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.2")
	f.cfServe(8895969, []byte("tampered"))
	pl := mustPlan(t, l, srv, InstallRequest{Ref: cfRef("9200002")})
	_, err := l.Install(context.Background(), srv, nil, InstallRequest{Ref: cfRef("9200002"), Fingerprint: pl.Fingerprint})
	if e := wantKind(t, err, addons.KindHashMismatch); e.Msg != "The download of mods/lithium-fabric-0.25.3+mc26.3.jar does not match the sha1 hash CurseForge lists, so Playkeeper did not install Example Fabric Pack." {
		t.Errorf("message %q", e.Msg)
	}
	wantTree(t, srv.Dir)
	wantTree(t, l.TempDir)
}

// Mods CurseForge doesn't let Playkeeper download come from the pack's
// server files, each checked against the SHA-1 CurseForge lists for it:
// FerriteCore, whose author only allows CurseForge's app, and Fabric API
// once CurseForge no longer offers its file.
func TestCurseForgeModsFromServerFiles(t *testing.T) {
	const ferrite, fabricAPI = "mods/ferritecore-9.0.0-fabric.jar", "mods/fabric-api-0.141.0+26.3.jar"
	gone := string(KindUnavailable) + ": Fabric API, which Example Fabric Pack uses, is no longer available on CurseForge. (https://www.curseforge.com/minecraft/mc-mods/fabric-api)"
	serverFiles := func(ferriteJar []byte) []entry {
		return []entry{
			{name: ferrite, data: ferriteJar},
			{name: fabricAPI, data: generated("fabric-api-0.141.0+26.3.jar")},
			{name: "config/server-only.json", data: []byte("{}\n")},
			{name: "startserver.sh", data: []byte("#!/bin/sh\njava -jar installer.jar\n")},
		}
	}
	setUp := func(t *testing.T, change func(f *fakes, l *Library, pack, server int64), entries []entry) (*fakes, *Library, Server, Ref, int64) {
		f := newFakes(t)
		l := f.library()
		srv := newServer(t, "fabric", "26.2")
		pack := f.cfAddFile(nil)
		server := f.cfAddServerFiles(pack, entries...)
		f.cfChange(8913512, func(file obj) { file["isAvailable"], file["fileStatus"] = false, curseforge.StatusDeleted })
		if change != nil {
			change(f, l, pack, server)
		}
		return f, l, srv, cfRef(strconv.FormatInt(pack, 10)), server
	}
	fetched := func(f *fakes, server int64) bool {
		return slices.ContainsFunc(f.served("forgecdn"), func(r string) bool { return strings.Contains(r, fmt.Sprintf("ServerFiles-%d", server)) })
	}

	f, l, srv, ref, server := setUp(t, nil, serverFiles(generated("ferritecore-9.0.0-fabric.jar")))
	pl := mustPlan(t, l, srv, InstallRequest{Ref: ref})
	wantList(t, "manual", manualList(pl.Manual))
	wantList(t, "changes", changeList(pl.Changes),
		"add config/example.json",
		"add mods/cloth-config-26.3.155-fabric.jar",
		"add "+fabricAPI,
		"add "+ferrite,
		"add mods/lithium-fabric-0.25.3+mc26.3.jar",
		"add mods/placeholder-api-3.1.0+26.3.jar")
	res := mustInstall(t, l, srv, InstallRequest{Ref: ref})
	if readFile(t, srv, ferrite) != string(generated("ferritecore-9.0.0-fabric.jar")) || readFile(t, srv, fabricAPI) != string(generated("fabric-api-0.141.0+26.3.jar")) {
		t.Error("the mods written are not the server files'")
	}
	files := recordFiles(roundTrip(t, res.Record))
	if fc := files[ferrite]; fc.Project != "459857" || fc.Origin != Override || fc.HashAlgo != "sha512" || fc.Size != int64(len(generated("ferritecore-9.0.0-fabric.jar"))) {
		t.Errorf("FerriteCore's record: %+v", fc)
	}
	if _, ok := files["startserver.sh"]; ok || len(files) != 6 {
		t.Errorf("record files: %+v", res.Record.Files)
	}
	wantList(t, "manual after installing", manualList(res.Manual))
	if !fetched(f, server) || slices.ContainsFunc(f.served("forgecdn"), func(r string) bool { return strings.Contains(r, "ferritecore") }) {
		t.Errorf("forgecdn served %q", f.served("forgecdn"))
	}
	wantTree(t, l.TempDir)

	// Server files that keep everything in a folder of their own, like All
	// the Mods 9's Server-Files-1.1.1, or that hold only a mods folder, give
	// their mods too.
	var wrapped []entry
	for _, e := range serverFiles(generated("ferritecore-9.0.0-fabric.jar")) {
		wrapped = append(wrapped, entry{name: "Server-Files-2.0.0/" + e.name, data: e.data})
	}
	for name, entries := range map[string][]entry{"in a folder": wrapped, "only mods": serverFiles(generated("ferritecore-9.0.0-fabric.jar"))[:2]} {
		_, l, srv, ref, _ := setUp(t, nil, entries)
		pl := mustPlan(t, l, srv, InstallRequest{Ref: ref})
		wantList(t, name+": manual", manualList(pl.Manual))
		if changes := changeList(pl.Changes); !slices.Contains(changes, "add "+ferrite) || !slices.Contains(changes, "add "+fabricAPI) {
			t.Errorf("%s: changes %q", name, changes)
		}
	}

	// Server files that hold most of the pack's mods say which ones a server
	// needs, as All the Mods 9's leave out Mekalus, a shader mod that stops a
	// server from starting. What they leave out stays off the server, and
	// its step to download it by hand goes, but friends still get it. Those
	// above hold two of the five mods, too few to say.
	const cloth, lithium, placeholder = "mods/cloth-config-26.3.155-fabric.jar", "mods/lithium-fabric-0.25.3+mc26.3.jar", "mods/placeholder-api-3.1.0+26.3.jar"
	copies := func(rels ...string) []entry {
		var out []entry
		for _, rel := range rels {
			out = append(out, entry{name: rel, data: []byte("the server files' copy")})
		}
		return out
	}
	for _, c := range []struct {
		name     string
		entries  []entry
		changes  []string
		leftOut  []string
		friends  string
		noManual bool
	}{
		{"they leave out a mod CurseForge lets Playkeeper download", append(serverFiles(generated("ferritecore-9.0.0-fabric.jar")), copies(cloth, placeholder)...),
			[]string{"add config/example.json", "add " + cloth, "add " + fabricAPI, "add " + ferrite, "add " + placeholder}, []string{lithium}, lithium, true},
		{"they leave out a mod only CurseForge's app downloads", append(serverFiles(nil)[1:], copies(cloth, lithium, placeholder)...),
			[]string{"add config/example.json", "add " + cloth, "add " + fabricAPI, "add " + lithium, "add " + placeholder}, []string{ferrite}, ferrite, true},
	} {
		_, l, srv, ref, _ := setUp(t, nil, c.entries)
		pl := mustPlan(t, l, srv, InstallRequest{Ref: ref})
		wantList(t, c.name+": changes", changeList(pl.Changes), c.changes...)
		wantList(t, c.name+": manual", manualList(pl.Manual))
		var leftOut []string
		for _, s := range pl.Skipped {
			if s.Reason == KindNotInServerFiles {
				leftOut = append(leftOut, s.Path)
			}
		}
		wantList(t, c.name+": left out", leftOut, c.leftOut...)
		res := mustInstall(t, l, srv, InstallRequest{Ref: ref})
		if !slices.ContainsFunc(res.Record.Client, func(f ClientFile) bool { return f.Path == c.friends }) {
			t.Errorf("%s: friends don't get %s: %+v", c.name, c.friends, res.Record.Client)
		}
		if _, err := os.Stat(filepath.Join(srv.Dir, filepath.FromSlash(c.friends))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: %s is on the server (%v)", c.name, c.friends, err)
		}
	}

	// So does a mod Modrinth lists as client-only when they have it: the
	// pack's authors run it on servers.
	listLithium := func(f *fakes, _ *Library, _, _ int64) {
		f.modrinthMod("LITHI001", "lithium-fabric-0.25.3+mc26.3.jar", generated("lithium-fabric-0.25.3+mc26.3.jar"), "client_only", "unsupported")
	}
	entries := append(serverFiles(generated("ferritecore-9.0.0-fabric.jar")), entry{name: lithium, data: generated("lithium-fabric-0.25.3+mc26.3.jar")})
	_, l, srv, ref, _ = setUp(t, listLithium, append(entries, copies(cloth, placeholder)...))
	pl = mustPlan(t, l, srv, InstallRequest{Ref: ref})
	if !slices.Contains(changeList(pl.Changes), "add "+lithium) || slices.Contains(pl.Skipped, Skipped{Path: lithium, Reason: addons.KindClientOnly}) {
		t.Errorf("Lithium, which Modrinth lists as client-only and the server files have: changes %q, skipped %+v", changeList(pl.Changes), pl.Skipped)
	}

	// And a mod CurseForge tags for players' games goes on the server when
	// they have it, as Better MC [FABRIC] BMC2's have Mod Menu, which one of
	// its mods needs: their copy, when it is the file CurseForge lists.
	const sodium = "mods/sodium-fabric-0.9.2+mc26.3.jar"
	for _, c := range []struct {
		name string
		data []byte
		kept bool
	}{
		{"they have the file CurseForge lists", generated("sodium-fabric-0.9.2+mc26.3.jar"), true},
		{"their copy is another file", []byte("another sodium"), false},
	} {
		entries := append(serverFiles(generated("ferritecore-9.0.0-fabric.jar")), copies(cloth, lithium, placeholder)...)
		_, l, srv, ref, _ := setUp(t, nil, append(entries, entry{name: sodium, data: c.data}))
		pl := mustPlan(t, l, srv, InstallRequest{Ref: ref})
		clientOnly := slices.Contains(pl.Skipped, Skipped{Path: sodium, Reason: addons.KindClientOnly})
		if slices.Contains(changeList(pl.Changes), "add "+sodium) != c.kept || clientOnly == c.kept {
			t.Errorf("%s: changes %q, skipped %+v", c.name, changeList(pl.Changes), pl.Skipped)
		}
		if !c.kept {
			continue
		}
		res := mustInstall(t, l, srv, InstallRequest{Ref: ref})
		if readFile(t, srv, sodium) != string(c.data) || recordFiles(res.Record)[sodium].Origin != Override {
			t.Errorf("%s: Sodium on the server isn't the server files' copy: %+v", c.name, recordFiles(res.Record)[sodium])
		}
		if !slices.ContainsFunc(res.Record.Client, func(f ClientFile) bool { return f.Path == sodium }) {
			t.Errorf("%s: friends don't get Sodium", c.name)
		}
	}

	// Otherwise the steps stay, and nothing comes from the server files.
	for _, c := range []struct {
		name    string
		change  func(f *fakes, l *Library, pack, server int64)
		entries []entry
		fetched bool
		manual  []string
	}{
		{name: "their copy isn't the file CurseForge lists", entries: serverFiles([]byte("another ferritecore")), fetched: true, manual: []string{ferriteCoreStep}},
		{name: "they don't have the mod", entries: serverFiles(nil)[1:], fetched: true, manual: []string{ferriteCoreStep}},
		{name: "CurseForge doesn't let Playkeeper download them", change: func(f *fakes, _ *Library, _, server int64) {
			f.cfChange(server, func(file obj) { file["downloadUrl"] = nil })
		}, manual: []string{gone, ferriteCoreStep}},
		{name: "they are for another version of the pack", change: func(f *fakes, _ *Library, _, server int64) {
			f.cfChange(server, func(file obj) { file["parentProjectFileId"] = 9200001 })
		}, manual: []string{gone, ferriteCoreStep}},
		{name: "they are larger than Playkeeper accepts", change: func(_ *fakes, l *Library, _, _ int64) { l.Limits.ServerFiles = 100 }, manual: []string{gone, ferriteCoreStep}},
		{name: "CurseForge no longer knows the gone file", change: func(f *fakes, _ *Library, _, _ int64) {
			f.mu.Lock()
			delete(f.cfFiles, 8913512)
			f.mu.Unlock()
		}, fetched: true, manual: []string{gone}},
	} {
		entries := c.entries
		if entries == nil {
			entries = serverFiles(generated("ferritecore-9.0.0-fabric.jar"))
		}
		f, l, srv, ref, server := setUp(t, c.change, entries)
		pl := mustPlan(t, l, srv, InstallRequest{Ref: ref})
		wantList(t, c.name+": manual", manualList(pl.Manual), c.manual...)
		if slices.Contains(manualList(pl.Manual), ferriteCoreStep) == slices.Contains(changeList(pl.Changes), "add "+ferrite) ||
			slices.ContainsFunc(changeList(pl.Changes), func(s string) bool { return strings.Contains(s, "server-only") || strings.Contains(s, "startserver") }) {
			t.Errorf("%s: changes %q", c.name, changeList(pl.Changes))
		}
		if fetched(f, server) != c.fetched {
			t.Errorf("%s: server files fetched %v, want %v", c.name, !c.fetched, c.fetched)
		}
		wantTree(t, srv.Dir)
	}

	// Server files that don't match CurseForge's hash stop the install.
	f, l, srv, ref, server = setUp(t, nil, serverFiles(generated("ferritecore-9.0.0-fabric.jar")))
	var n int
	f.cfChange(server, func(file obj) { n = int(num(file["fileLength"])) })
	f.cfServe(server, bytes.Repeat([]byte("x"), n))
	_, err := l.PlanInstall(context.Background(), srv, nil, InstallRequest{Ref: ref})
	if e := wantKind(t, err, addons.KindHashMismatch); e.Msg != fmt.Sprintf("The download of ServerFiles-%d.zip does not match the sha1 hash CurseForge lists, so Playkeeper did not install Example Fabric Pack.", server) {
		t.Errorf("message %q", e.Msg)
	}
	wantTree(t, srv.Dir)
	wantTree(t, l.TempDir)
}

// CurseForge's optional mods are off unless the user turns them on.
func TestOptionalCurseForgeMods(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.2")
	id := f.cfAddFile(func(m obj) { list(m["files"])[2].(obj)["required"] = false })
	ref := cfRef(strconv.FormatInt(id, 10))
	const path = "mods/lithium-fabric-0.25.3+mc26.3.jar"
	pl := mustPlan(t, l, srv, InstallRequest{Ref: ref})
	sameJSON(t, "optional", pl.Optional, []Optional{{Path: path, Name: "Lithium", Project: "360438", Size: int64(len(generated("lithium-fabric-0.25.3+mc26.3.jar")))}})
	if !slices.Contains(skippedList(pl.Skipped), "optional_off "+path) || slices.Contains(changeList(pl.Changes), "add "+path) {
		t.Errorf("skipped %q, changes %q", skippedList(pl.Skipped), changeList(pl.Changes))
	}
	res := mustInstall(t, l, srv, InstallRequest{Ref: ref, Include: map[string]bool{path: true}})
	if f := recordFiles(res.Record)[path]; !f.Optional || f.Project != "360438" || len(res.Record.Excluded) != 0 {
		t.Errorf("record: %+v, excluded %v", f, res.Record.Excluded)
	}
}

func TestCurseForgeKey(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	now := func() time.Time { return testNow }

	l := f.library()
	const wrong = "wrong-key-0123456789abcdef"
	bad, err := curseforge.NewKey(wrong, curseforge.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	l.CurseForge = curseforge.New(bad, fetch.Options{BaseURL: f.curseforge.URL, HTTP: f.client, Now: now})
	_, err = l.Search(ctx, Query{Source: CurseForge})
	if e := wantKind(t, err, KindKeyRefused); e.Msg != "CurseForge refused Playkeeper's API key." ||
		strings.Contains(fmt.Sprintf("%v %+v %#v", err, e, e), wrong) {
		t.Errorf("error %#v", e)
	}
	_, err = l.PlanInstall(ctx, newServer(t, "", ""), nil, InstallRequest{Ref: cfRef("")})
	wantKind(t, err, KindKeyRefused)

	// Without a key, CurseForge is not offered and never asked.
	l.CurseForge = nil
	sameJSON(t, "sources", l.Sources(), []addons.Source{addons.Modrinth})
	asked := len(f.served("curseforge"))
	for name, call := range map[string]func() error{
		"plan": func() error {
			_, err := l.PlanInstall(ctx, newServer(t, "", ""), nil, InstallRequest{Ref: cfRef("")})
			return err
		},
		"search":   func() error { _, err := l.Search(ctx, Query{Source: CurseForge}); return err },
		"versions": func() error { _, err := l.Versions(ctx, CurseForge, "9100001", ""); return err },
		"detail":   func() error { _, err := l.Detail(ctx, CurseForge, "9100001"); return err },
		"update check": func() error {
			_, err := l.CheckUpdate(ctx, Record{Pack: addons.Installed{Source: CurseForge, ProjectID: "9100001", VersionID: "9200001"}}, false)
			return err
		},
	} {
		err := call()
		if e := addons.KindOf(err); e != KindNoCurseForge || !strings.HasPrefix(err.Error(), "CurseForge is not available on this Playkeeper: it has no CurseForge API key.") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.served("curseforge")) != asked {
		t.Error("CurseForge was asked without a key")
	}

	if New(f.client, curseforge.Key{}).CurseForge != nil {
		t.Error("New offers CurseForge without a key")
	}
	key, err := curseforge.NewKey(testKey, curseforge.KeyBuild)
	if err != nil {
		t.Fatal(err)
	}
	if lib := New(f.client, key); lib.CurseForge == nil || !slices.Equal(lib.Sources(), []addons.Source{addons.Modrinth, CurseForge}) {
		t.Error("New does not offer CurseForge with a key")
	}
}

// A CurseForge pack whose manifest names Forge runs on a Forge server, on
// the Forge build the manifest names.
func TestCurseForgeForgePack(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	id := f.cfAddFile(func(m obj) { m["minecraft"].(obj)["modLoaders"] = []any{obj{"id": "forge-65.1.3", "primary": true}} })
	pl := mustPlan(t, l, newServer(t, "", ""), InstallRequest{Ref: cfRef(strconv.FormatInt(id, 10))})
	if pl.Requirements != (Requirements{"forge", "26.2", "65.1.3"}) || !pl.Ready {
		t.Errorf("requirements %+v, blockers %q", pl.Requirements, noticeList(pl.Blockers))
	}
}

// The heap a CurseForge manifest recommends is the one the pack asks for,
// unless its user_jvm_args.txt says otherwise.
func TestCurseForgePackHeap(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.2")
	ram := func(mb any) func(m obj) { return func(m obj) { m["minecraft"].(obj)["recommendedRam"] = mb } }
	for _, c := range []struct {
		name string
		id   int64
		want int
	}{
		{"recommended", f.cfAddFile(ram(8196)), 8196},
		{"user_jvm_args.txt wins", f.cfAddFile(ram(8196), entry{name: "overrides/user_jvm_args.txt", data: []byte("-Xms2G\n-Xmx6G\n")}), 6 << 10},
		{"not a heap", f.cfAddFile(ram(64)), 0},
		// Better MC [FABRIC] BMC2 writes it as text.
		{"recommended as text", f.cfAddFile(ram("10000")), 10000},
		{"text that is not a number", f.cfAddFile(ram("8G")), 0},
	} {
		if p := mustPlan(t, l, srv, InstallRequest{Ref: cfRef(strconv.FormatInt(c.id, 10))}); p.HeapMB != c.want {
			t.Errorf("%s: heap %d MB, want %d", c.name, p.HeapMB, c.want)
		}
	}
}
