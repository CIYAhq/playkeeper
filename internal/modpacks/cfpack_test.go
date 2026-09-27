package modpacks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
		{"Forge in the manifest", func(f *fakes) Ref { return cfRef(strconv.FormatInt(f.cfAddFile(loader("forge-47.2.0")), 10)) },
			KindForge, "Example Fabric Pack runs on Forge, and Playkeeper does not run Forge servers."},
		{"old Minecraft in the manifest", func(f *fakes) Ref {
			return cfRef(strconv.FormatInt(f.cfAddFile(func(m obj) { m["minecraft"].(obj)["version"] = "1.20.1" }), 10))
		}, KindMinecraft, "Example Fabric Pack is for Minecraft 1.20.1, and Playkeeper runs Minecraft 1.21 and newer."},
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
