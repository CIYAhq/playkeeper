package modpacks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

func TestInstallModrinthPack(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "", "")
	ref := Ref{Source: addons.Modrinth, Project: "adrenaline"}
	p := mustPlan(t, l, srv, InstallRequest{Ref: ref})

	file := f.mrpackOf("EZaeTUP8")
	pack := addons.Installed{
		Source: addons.Modrinth, ProjectID: "BYN9yKrV", Slug: "adrenaline", Name: "Adrenaline",
		IconURL:   "https://cdn.modrinth.com/data/BYN9yKrV/98f14519960b17418d5ba5ebfdc46c85154ad87b_96.webp",
		VersionID: "EZaeTUP8", VersionNumber: "26.5.0+mc26.2.fabric", Channel: "release",
		Published: time.Date(2026, 9, 21, 21, 6, 6, 209270000, time.UTC), FileName: "Adrenaline-26.5.0+mc26.2.fabric.mrpack",
		HashAlgo: "sha512", Hash: str(file["hashes"].(obj)["sha512"]), Size: int64(num(file["size"])),
	}
	sameJSON(t, "pack", p.Pack, pack)
	sameJSON(t, "requirements", p.Requirements, Requirements{Type: "fabric", MinecraftVersion: "26.2", LoaderVersion: "0.19.5"})
	if !p.Ready || p.Current != nil || len(p.Blockers)+len(p.Warnings)+len(p.Manual)+len(p.Optional)+p.Unchanged != 0 || p.Properties != nil {
		t.Errorf("plan not clean: %+v", p)
	}
	wantList(t, "skipped", skippedList(p.Skipped),
		"client_only mods/CrashAssistant-fabric-26.2-26.3-1.11.14.jar",
		"client_only mods/ImmediatelyFast-Fabric-1.16.5+26.2.jar",
		"client_only mods/Ixeris-4.6.8+26.2-fabric.jar",
		"client_only mods/asynclogger-2.2.2+26.1.2-fabric.jar",
		"client_only mods/bbe-fabric-1.3.8-beta.1+mc26.2.jar",
		"client_only mods/cull-fewer-leaves-1.1.2+26.1-fabric.jar",
		"client_only mods/dynamic-fps-3.11.9+minecraft-26.2.0-fabric.jar",
		"client_only mods/entityculling-fabric-1.11.2-mc26.2.jar",
		"client_only mods/modmenu-20.0.2.jar",
		"client_only mods/particle_core-0.3.3+26.2.jar",
		"client_only mods/sodium-fabric-0.9.2+mc26.2.jar",
	)
	// 17 mods for both sides, 2 server-only mods and the 7 overrides.
	var downloads, overrides int
	var size int64
	for _, c := range p.Changes {
		switch {
		case c.Action != ActionAdd || c.Yours || c.World || c.Optional:
			t.Errorf("change %+v", c)
		case c.Origin == Download && strings.HasPrefix(c.Path, "mods/") && c.Project != "":
			downloads++
			size += c.Size
		case c.Origin == Override && strings.HasPrefix(c.Path, "config/modpack_defaults/") && c.Project == "":
			overrides++
		default:
			t.Errorf("change %+v", c)
		}
	}
	if downloads != 19 || overrides != 7 || p.DownloadSize != size {
		t.Errorf("%d downloads and %d overrides of %d bytes, plan says %d bytes", downloads, overrides, size, p.DownloadSize)
	}
	if got := f.served("cdn"); len(got) != 1 || !strings.HasSuffix(got[0], ".mrpack") {
		t.Errorf("planning downloaded %q, want only the pack", got)
	}
	wantTree(t, srv.Dir)

	res, err := l.Install(context.Background(), srv, nil, InstallRequest{Ref: ref, Fingerprint: p.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	pack.InstalledAt = testNow
	sameJSON(t, "installed pack", res.Record.Pack, pack)
	if res.Record.Requirements != p.Requirements || !res.RestartNeeded || len(res.Kept)+len(res.Manual)+len(res.Warnings) != 0 || res.Record.Excluded != nil {
		t.Errorf("result %+v", res)
	}
	want := []string{"config/", "config/modpack_defaults/", "config/modpack_defaults/config/", "config/modpack_defaults/config/crash_assistant/", "mods/"}
	for _, c := range p.Changes {
		want = append(want, c.Path)
	}
	wantTree(t, srv.Dir, want...)
	if len(res.Record.Files) != len(p.Changes) {
		t.Fatalf("record has %d files, want %d", len(res.Record.Files), len(p.Changes))
	}
	for i, rf := range res.Record.Files {
		c := p.Changes[i]
		data := readFile(t, srv, rf.Path)
		if rf.Path != c.Path || rf.Origin != c.Origin || rf.Project != c.Project || rf.HashAlgo != "sha512" ||
			rf.Hash != sha512hex([]byte(data)) || rf.Size != int64(len(data)) || rf.Preexisting || rf.Optional {
			t.Errorf("record %+v for change %+v", rf, c)
		}
	}
	if got, want := readFile(t, srv, "mods/krypton-0.3.1.jar"), string(generated("/data/fQEb0iXm/versions/5WeL0Nkz/krypton-0.3.1.jar")); got != want {
		t.Errorf("krypton is %q, want %q", got, want)
	}
	vmp, _ := os.ReadFile("testdata/packs/adrenaline/overrides/config/modpack_defaults/config/vmp.properties")
	if got := readFile(t, srv, "config/modpack_defaults/config/vmp.properties"); got != string(vmp) {
		t.Errorf("vmp.properties is %q, want the pack's %q", got, vmp)
	}
	if !res.Record.Owns("mods/krypton-0.3.1.jar") || res.Record.Owns("mods/sodium-fabric-0.9.2+mc26.2.jar") {
		t.Error("Owns does not match the record")
	}
	// Players get every file but the two server-only mods.
	if len(res.Record.Client) != 28 {
		t.Errorf("record lists %d files for players, want 28", len(res.Record.Client))
	}
	for _, cf := range res.Record.Client {
		dl := f.parse(cf.Downloads[0])
		data := generated(dl.Path)
		if strings.Contains(cf.Path, "krypton") || strings.Contains(cf.Path, "vmp") || cf.Origin != Download || len(cf.Downloads) != 1 ||
			dl.Host != host(f.cdn) || cf.SHA1 != sha1hex(data) || cf.SHA512 != sha512hex(data) || cf.Size != int64(len(data)) ||
			cf.Env == nil || cf.Env.Client != "required" || !strings.HasPrefix(dl.Path, "/data/"+cf.Project+"/versions/") {
			t.Errorf("file for players %+v", cf)
		}
	}
	for _, r := range f.served("cdn") {
		if strings.Contains(r, "sodium") || strings.Contains(r, "modmenu") {
			t.Errorf("downloaded client-only %s", r)
		}
	}
	wantTree(t, l.TempDir)
	sameJSON(t, "stored record", roundTrip(t, res.Record), res.Record)

	_, err = l.PlanInstall(context.Background(), srv, &res.Record, InstallRequest{Ref: Ref{Source: addons.Modrinth, Project: "vanilla-perfected"}})
	e := wantKind(t, err, KindPackInstalled)
	if e.Msg != "Adrenaline is already installed on this server (version 26.5.0+mc26.2.fabric)." {
		t.Errorf("message %q", e.Msg)
	}
}

func TestInstallNeoForgePackWithOptionalFiles(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "", "")
	ref := Ref{Source: addons.Modrinth, Project: "create_plus"}
	_, err := l.PlanInstall(context.Background(), srv, nil, InstallRequest{Ref: ref})
	if e := wantKind(t, err, addons.KindOnlyPrerelease); e.Msg != "Create+ has only beta or alpha versions Playkeeper can run." {
		t.Errorf("message %q", e.Msg)
	}

	p := mustPlan(t, l, srv, InstallRequest{Ref: ref, AllowPrerelease: true})
	if p.Pack.VersionID != "BSg2ZS8u" || p.Pack.Channel != "alpha" || p.Pack.FileName != "Create+ 6.0.0 Alpha f.mrpack" {
		t.Errorf("pack %+v", p.Pack)
	}
	sameJSON(t, "requirements", p.Requirements, Requirements{Type: "neoforge", MinecraftVersion: "1.21.1", LoaderVersion: "21.1.233"})
	wantList(t, "changes", changeList(p.Changes),
		"add cache/c2me-dfc/DfcCompiled_0.class",
		"add config/ichunutil/themes/blue&black.json",
		"add configureddefaults/options.txt",
		"add data/fabricDefaultResourcePacks.dat",
		"add dynamic-resource-pack-cache/amendments-generated_pack/assets/amendments/models/block/signs/ecologics/sign_azalea_0.json",
		"add emi.json",
		"add mods/.connector/continuity-3.0.0+1.21.neoforge_mapped_moj_1.21.1.jar",
		"add mods/AxesAreWeapons-1.10.2-neoforge-1.21.jar",
		"add mods/aeroencasedpipe-1.0.7.jar",
		"add mods/aileron-1.21.1-neoforge-1.1.4.jar",
		"add mods/alloyed-3.0.9+1.21.1-neoforge.jar",
		"add mods/emi-1.1.24+1.21.1+neoforge.jar",
		"add mods/lithium-neoforge-0.15.3+mc1.21.1.jar",
		"add mods/statuseffectbars-1.21.1-NeoForge-1.0.2.jar",
		"add mods/voicechat-neoforge-1.21.1-2.6.18.jar",
	)
	var optional []string
	for _, o := range p.Optional {
		optional = append(optional, o.Path+" "+o.Name+" "+o.Project+" "+map[bool]string{true: "on", false: "off"}[o.Included])
	}
	wantList(t, "optional files", optional,
		"mods/emi-1.1.24+1.21.1+neoforge.jar emi-1.1.24+1.21.1+neoforge.jar fRiHVvU7 on",
		"mods/lithium-neoforge-0.15.3+mc1.21.1.jar lithium-neoforge-0.15.3+mc1.21.1.jar gvQqBUqZ on",
		"mods/voicechat-neoforge-1.21.1-2.6.18.jar voicechat-neoforge-1.21.1-2.6.18.jar 9eGKb6K1 on",
	)
	wantList(t, "skipped", skippedList(p.Skipped),
		"protected_path .mixin.out/class/net/minecraft/world/effect/MobEffect.class",
		"protected_path .qmenu_opened.marker",
		"client_only mods/.connector/temp/enchancement-1.21-r14$tooltipfix-1.1.1-1.20.jar",
		"client_only mods/asynclogger-1.1.4+1.21.1-neoforge.jar",
		"client_only resourcepacks/FreshAnimations_v1.10.4.zip",
	)
	wantList(t, "warnings", noticeList(p.Warnings),
		"environment_unknown: Create+ does not say whether mods/statuseffectbars-1.21.1-NeoForge-1.0.2.jar belongs on a server, so Playkeeper includes it.")

	// Optional files can be left out; the pack's other files cannot.
	off := map[string]bool{"mods/voicechat-neoforge-1.21.1-2.6.18.jar": false, "mods/emi-1.1.24+1.21.1+neoforge.jar": false, "mods/aileron-1.21.1-neoforge-1.1.4.jar": false}
	res := mustInstall(t, l, srv, InstallRequest{Ref: ref, AllowPrerelease: true, Include: off})
	sameJSON(t, "excluded", res.Record.Excluded, []Excluded{
		{Path: "mods/emi-1.1.24+1.21.1+neoforge.jar", Project: "fRiHVvU7"},
		{Path: "mods/voicechat-neoforge-1.21.1-2.6.18.jar", Project: "9eGKb6K1"},
	})
	var files []string
	for _, rf := range res.Record.Files {
		files = append(files, rf.Path)
		if rf.Optional != (rf.Path == "mods/lithium-neoforge-0.15.3+mc1.21.1.jar") {
			t.Errorf("%s optional %v", rf.Path, rf.Optional)
		}
	}
	if len(files) != 13 || slices.Contains(files, "mods/voicechat-neoforge-1.21.1-2.6.18.jar") || !slices.Contains(files, "mods/aileron-1.21.1-neoforge-1.1.4.jar") {
		t.Errorf("recorded files %q", files)
	}
	for _, p := range []string{"xmcl.json", ".qmenu_opened.marker", ".mixin.out", "resourcepacks", "mods/voicechat-neoforge-1.21.1-2.6.18.jar", "mods/.connector/temp"} {
		if _, err := os.Lstat(filepath.Join(srv.Dir, p)); err == nil {
			t.Errorf("%s was written", p)
		}
	}
	if got := readFile(t, srv, "config/ichunutil/themes/blue&black.json"); got != "placeholder for config/ichunutil/themes/blue&black.json\n" {
		t.Errorf("override is %q", got)
	}
	for _, r := range f.served("cdn") {
		if strings.Contains(r, "voicechat") || strings.Contains(r, "asynclogger") {
			t.Errorf("downloaded %s, which the server does not get", r)
		}
	}
}

func TestRefusedPacks(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	f.setProject(obj{"id": "P7dR8mSH", "slug": "fabric-api", "project_type": "mod", "title": "Fabric API", "server_side": "required"})
	srv := newServer(t, "", "")
	for _, c := range []struct {
		ref  Ref
		kind addons.Kind
		msg  string
	}{
		{Ref{addons.Modrinth, "create_plus", "OirSzesD"}, KindForge, "Create+ runs on Forge, and Playkeeper does not run Forge servers."},
		{Ref{addons.Modrinth, "the-content-smp", "ck8SrkA4"}, KindMinecraft, "The Content Smp (Fan-Made) is for Minecraft 1.19.2, and Playkeeper runs Minecraft 1.21 and newer."},
		{Ref{addons.Modrinth, "the-content-smp", ""}, KindMinecraft, "The Content Smp (Fan-Made) is for Minecraft 1.19.2, and Playkeeper runs Minecraft 1.21 and newer."},
		{Ref{addons.Modrinth, "adrenaline", "wGteoJrN"}, KindMinecraft, "Adrenaline is for Minecraft 1.20.1, and Playkeeper runs Minecraft 1.21 and newer."},
		{Ref{addons.Modrinth, "sodiumplus", ""}, addons.KindClientOnly, "Sodium Plus is for the game client only, not for servers."},
		{Ref{addons.Modrinth, "fabric-api", ""}, KindNotModpack, "Fabric API is not a modpack on Modrinth."},
		{Ref{addons.Modrinth, "adrenaline", "jmYEuzNA"}, addons.KindNotFound, `Adrenaline has no version "jmYEuzNA".`},
		{Ref{addons.Modrinth, "adrenaline", "NOSUCHV1"}, addons.KindNotFound, `Adrenaline has no version "NOSUCHV1".`},
		{Ref{addons.Modrinth, "no-such-pack", ""}, addons.KindNotFound, `Modrinth has no project "no-such-pack".`},
		{Ref{addons.Modrinth, "../../etc", ""}, addons.KindInvalid, "The pack's project is not valid."},
		{Ref{addons.Modrinth, "adrenaline", "a/b"}, addons.KindInvalid, "The pack's version is not valid."},
		{Ref{"hangar", "adrenaline", ""}, addons.KindInvalid, `Packs come from Modrinth or CurseForge, not "hangar".`},
	} {
		_, err := l.PlanInstall(context.Background(), srv, nil, InstallRequest{Ref: c.ref, AllowPrerelease: true})
		if e := wantKind(t, err, c.kind); e.Msg != c.msg {
			t.Errorf("%+v: message %q, want %q", c.ref, e.Msg, c.msg)
		}
	}
	if got := f.served("cdn"); len(got) != 0 {
		t.Errorf("downloaded %q for packs Playkeeper refuses", got)
	}
	wantTree(t, srv.Dir)
	wantTree(t, l.TempDir)
}

func TestPackDependenciesDecideTheServer(t *testing.T) {
	for _, c := range []struct {
		deps obj
		want Requirements
		kind addons.Kind
	}{
		{deps: obj{"minecraft": "26.2", "fabric-loader": "0.19.5"}, want: Requirements{"fabric", "26.2", "0.19.5"}},
		{deps: obj{"minecraft": "26.2", "quilt-loader": "0.29.2-beta.3"}, want: Requirements{"quilt", "26.2", "0.29.2-beta.3"}},
		{deps: obj{"minecraft": "1.21.1", "neoforge": "21.1.233"}, want: Requirements{"neoforge", "1.21.1", "21.1.233"}},
		{deps: obj{"minecraft": "1.21.4"}, want: Requirements{"vanilla", "1.21.4", ""}},
		{deps: obj{"minecraft": "1.20.1", "forge": "47.4.0"}, kind: KindForge},
		{deps: obj{"minecraft": "26.2", "liteloader": "1.0"}, kind: KindUnknownLoader},
		{deps: obj{"minecraft": "26.2", "fabric-loader": "0.19.5", "quilt-loader": "0.29.1"}, kind: KindBadPack},
		{deps: obj{"minecraft": "26.2-rc1", "fabric-loader": "0.19.5"}, kind: KindMinecraft},
		{deps: obj{"minecraft": "1.20.4", "fabric-loader": "0.19.5"}, kind: KindMinecraft},
		{deps: obj{"minecraft": "26.2", "fabric-loader": "0.19.5 && rm -rf /"}, kind: KindBadPack},
	} {
		f := newFakes(t)
		l := f.library()
		index := testIndex()
		index["dependencies"] = c.deps
		id := f.addPack(index)
		p, err := l.PlanInstall(context.Background(), newServer(t, "", ""), nil, InstallRequest{Ref: testRef(id)})
		if c.kind != "" {
			wantKind(t, err, c.kind)
			continue
		}
		if err != nil {
			t.Errorf("%v: %v", c.deps, err)
			continue
		}
		sameJSON(t, "requirements", p.Requirements, c.want)
	}

	f := newFakes(t)
	l := f.library()
	l.Types = []string{"fabric"}
	index := testIndex()
	index["dependencies"] = obj{"minecraft": "26.2", "quilt-loader": "0.29.1"}
	_, err := l.PlanInstall(context.Background(), newServer(t, "", ""), nil, InstallRequest{Ref: testRef(f.addPack(index))})
	if e := wantKind(t, err, KindTypeUnavailable); e.Msg != "Test Pack needs a Quilt server, and Playkeeper cannot run Quilt servers yet." {
		t.Errorf("message %q", e.Msg)
	}
}

func TestServerOverridesWin(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.2")
	srv.Owner = &addons.Owner{UID: os.Getuid(), GID: os.Getgid()}
	id := f.addPack(testIndex(f.indexFile("mods/a-1.jar", []byte("downloaded"))),
		entry{name: "overrides/mods/a-1.jar", data: []byte("from overrides")},
		entry{name: "server-overrides/mods/a-1.jar", data: []byte("from server-overrides")},
		entry{name: "overrides/config/both.txt", data: []byte("from overrides")},
		entry{name: "server-overrides/config/both.txt", data: []byte("from server-overrides")},
		entry{name: "overrides/config/common.txt", data: []byte("common")},
		entry{name: "client-overrides/config/client.txt", data: []byte("client")},
		entry{name: "client-overrides/mods/a-1.jar", data: []byte("from client-overrides")},
	)
	p := mustPlan(t, l, srv, InstallRequest{Ref: testRef(id)})
	wantList(t, "changes", changeList(p.Changes), "add config/both.txt", "add config/common.txt", "add mods/a-1.jar")
	if p.Changes[2].Origin != Override || p.DownloadSize != 0 || len(p.Skipped) != 0 {
		t.Errorf("plan %+v", p)
	}
	res := mustInstall(t, l, srv, InstallRequest{Ref: testRef(id)})
	wantTree(t, srv.Dir, "config/", "config/both.txt", "config/common.txt", "mods/", "mods/a-1.jar")
	for p, want := range map[string]string{"mods/a-1.jar": "from server-overrides", "config/both.txt": "from server-overrides", "config/common.txt": "common"} {
		if got := readFile(t, srv, p); got != want {
			t.Errorf("%s is %q, want %q", p, got, want)
		}
	}
	if rf := res.Record.Files[2]; rf.Origin != Override || rf.Hash != sha512hex([]byte("from server-overrides")) {
		t.Errorf("record %+v", rf)
	}
	for _, r := range f.served("cdn") {
		if strings.HasSuffix(r, "/a-1.jar") {
			t.Errorf("downloaded %s, which an override replaces", r)
		}
	}
}

func TestServerEnvironmentAndOptionalFiles(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.2")
	id := f.addPack(testIndex(
		f.indexFile("mods/both-1.jar", []byte("both"), "required", "required"),
		f.indexFile("mods/server-1.jar", []byte("server"), "unsupported", "required"),
		f.indexFile("mods/client-1.jar", []byte("client"), "required", "unsupported"),
		f.indexFile("mods/opt-1.jar", []byte("optional"), "optional", "optional"),
		f.indexFile("mods/clientopt-1.jar", []byte("client optional"), "optional", "unsupported"),
		f.indexFile("mods/noenv-1.jar", []byte("everywhere")),
		f.indexFile("mods/odd-1.jar", []byte("odd"), "unknown", "unknown"),
	))
	p := mustPlan(t, l, srv, InstallRequest{Ref: testRef(id)})
	wantList(t, "changes", changeList(p.Changes),
		"add mods/both-1.jar", "add mods/noenv-1.jar", "add mods/odd-1.jar", "add mods/opt-1.jar", "add mods/server-1.jar")
	wantList(t, "skipped", skippedList(p.Skipped), "client_only mods/client-1.jar", "client_only mods/clientopt-1.jar")
	sameJSON(t, "optional", p.Optional, []Optional{{Path: "mods/opt-1.jar", Name: "opt-1.jar", Project: "opt", Size: 8, Included: true}})
	wantList(t, "warnings", noticeList(p.Warnings),
		"environment_unknown: Test Pack does not say whether mods/odd-1.jar belongs on a server, so Playkeeper includes it.")

	req := InstallRequest{Ref: testRef(id), Include: map[string]bool{"mods/opt-1.jar": false, "mods/both-1.jar": false}}
	p = mustPlan(t, l, srv, req)
	wantList(t, "skipped", skippedList(p.Skipped), "client_only mods/client-1.jar", "client_only mods/clientopt-1.jar", "optional_off mods/opt-1.jar")
	res := mustInstall(t, l, srv, req)
	wantTree(t, srv.Dir, "mods/", "mods/both-1.jar", "mods/noenv-1.jar", "mods/odd-1.jar", "mods/server-1.jar")
	sameJSON(t, "excluded", res.Record.Excluded, []Excluded{{Path: "mods/opt-1.jar", Project: "opt"}})
	for _, r := range f.served("cdn") {
		if strings.Contains(r, "/client") || strings.Contains(r, "/opt-1.jar") {
			t.Errorf("downloaded %s, which the server does not get", r)
		}
	}
}

func TestProtectedClientAndWorldFiles(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	props := "# the pack's settings\ndifficulty=hard\nmotd=Hello\\: world\nlevel-seed = 12345\nserver-port=25570\n" +
		"rcon.password=hunter2-secret\nenable-rcon=true\nview-distance=12\npvp:false\nwhite-list=true\nbad key=1\n"
	entries := []entry{{name: "overrides/server.properties", data: []byte(props)}, {name: "overrides/config/ok.txt", data: []byte("ok")}}
	for _, p := range []string{
		"eula.txt", "ops.json", "whitelist.json", "user_jvm_args.txt", "start.sh", "run.BAT", "fabric-server-launch.jar",
		"libraries/net/x.jar", "logs/latest.log", ".env", ".fabric/remapped.jar", "config/a.txt.playkeeper-new-0123456789ab",
		"options.txt", "servers.dat", "resourcepacks/r.zip", "shaderpacks/s.zip", "saves/w/level.dat",
		"world/level.dat", "world_nether/DIM-1/region/r.0.0.mca", "survival/data/raids.dat",
	} {
		entries = append(entries, entry{name: "overrides/" + p, data: []byte(p)})
	}
	id := f.addPack(testIndex(f.indexFile("world/datapacks/dp-1.zip", []byte("data pack"))), entries...)

	// A server without a world gets the pack's world, and nothing records it.
	srv := newServer(t, "fabric", "26.2")
	srv.World = "survival"
	p := mustPlan(t, l, srv, InstallRequest{Ref: testRef(id)})
	wantList(t, "changes", changeList(p.Changes),
		"add config/ok.txt",
		"add survival/data/raids.dat (world)",
		"add world/datapacks/dp-1.zip (world)",
		"add world/level.dat (world)",
		"add world_nether/DIM-1/region/r.0.0.mca (world)",
	)
	wantList(t, "skipped", skippedList(p.Skipped),
		"protected_path .env",
		"protected_path .fabric/remapped.jar",
		"protected_path config/a.txt.playkeeper-new-0123456789ab",
		"protected_path eula.txt",
		"protected_path fabric-server-launch.jar",
		"protected_path libraries/net/x.jar",
		"protected_path logs/latest.log",
		"protected_path ops.json",
		"client_content options.txt",
		"client_content resourcepacks/r.zip",
		"protected_path run.BAT",
		"client_content saves/w/level.dat",
		"protected_path server.properties",
		"client_content servers.dat",
		"client_content shaderpacks/s.zip",
		"protected_path start.sh",
		"protected_path user_jvm_args.txt",
		"protected_path whitelist.json",
	)
	sameJSON(t, "suggested settings", p.Properties, map[string]string{"difficulty": "hard", "level-seed": "12345", "pvp": "false", "view-distance": "12"})
	wantList(t, "warnings", noticeList(p.Warnings),
		"server_properties: Test Pack ships server settings that Playkeeper does not take from packs: enable-rcon, motd, rcon.password, server-port, white-list.")
	if b, _ := json.Marshal(p); strings.Contains(string(b), "hunter2") {
		t.Error("the plan shows a value of a setting Playkeeper does not take")
	}
	res := mustInstall(t, l, srv, InstallRequest{Ref: testRef(id)})
	wantTree(t, srv.Dir, "config/", "config/ok.txt", "survival/", "survival/data/", "survival/data/raids.dat",
		"world/", "world/datapacks/", "world/datapacks/dp-1.zip", "world/level.dat",
		"world_nether/", "world_nether/DIM-1/", "world_nether/DIM-1/region/", "world_nether/DIM-1/region/r.0.0.mca")
	if len(res.Record.Files) != 1 || res.Record.Files[0].Path != "config/ok.txt" {
		t.Errorf("recorded %+v", res.Record.Files)
	}
	sameJSON(t, "suggested settings", res.Properties, p.Properties)

	// A server with a world keeps it as it is.
	srv = newServer(t, "fabric", "26.2")
	srv.World = "survival"
	writeFile(t, srv, "survival/level.dat", "the server's world")
	p = mustPlan(t, l, srv, InstallRequest{Ref: testRef(id)})
	wantList(t, "changes", changeList(p.Changes), "add config/ok.txt")
	var world []string
	for _, s := range p.Skipped {
		if s.Reason == KindWorld {
			world = append(world, s.Path)
		}
	}
	wantList(t, "world files", world, "survival/data/raids.dat", "world/datapacks/dp-1.zip", "world/level.dat", "world_nether/DIM-1/region/r.0.0.mca")
}

func TestFilesAlreadyOnTheServer(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.2")
	id := f.addPack(testIndex(f.indexFile("mods/same-1.jar", []byte("same")), f.indexFile("mods/other-1.jar", []byte("the pack's"))),
		entry{name: "overrides/config/same.txt", data: []byte("same")},
		entry{name: "overrides/config/mine.txt", data: []byte("the pack's")},
	)
	for p, data := range map[string]string{
		"mods/same-1.jar": "same", "mods/other-1.jar": "the user's", "config/same.txt": "same", "config/mine.txt": "the user's",
		"mods/extra.jar": "the user's own mod", "mods/.hidden.jar": "hidden", "mods/notes.txt": "not a mod",
	} {
		writeFile(t, srv, p, data)
	}
	p := mustPlan(t, l, srv, InstallRequest{Ref: testRef(id)})
	wantList(t, "changes", changeList(p.Changes), "replace config/mine.txt (yours)", "replace mods/other-1.jar (yours)")
	wantList(t, "warnings", noticeList(p.Warnings),
		"extra_mods: The mods folder already has a mod that is not part of Test Pack, extra.jar. It stays, and may clash with the pack's mods.")
	if p.Unchanged != 2 {
		t.Errorf("%d unchanged, want 2", p.Unchanged)
	}

	req := InstallRequest{Ref: testRef(id), Keep: []string{"config/mine.txt"}}
	p = mustPlan(t, l, srv, req)
	wantList(t, "changes", changeList(p.Changes), "keep config/mine.txt (yours)", "replace mods/other-1.jar (yours)")
	res := mustInstall(t, l, srv, req)
	wantList(t, "kept", res.Kept, "config/mine.txt")
	for p, want := range map[string]string{"config/mine.txt": "the user's", "mods/other-1.jar": "the pack's", "mods/extra.jar": "the user's own mod"} {
		if got := readFile(t, srv, p); got != want {
			t.Errorf("%s is %q, want %q", p, got, want)
		}
	}
	var pre []string
	for _, rf := range res.Record.Files {
		if rf.Preexisting {
			pre = append(pre, rf.Path)
		}
	}
	wantList(t, "files the server had", pre, "config/mine.txt", "config/same.txt", "mods/same-1.jar")
}

func TestPathsInTheWay(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.2")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(srv.Dir, "mods")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, srv, "config", "a file where the pack needs a folder")
	if err := os.MkdirAll(filepath.Join(srv.Dir, "data", "b.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	id := f.addPack(testIndex(f.indexFile("mods/a-1.jar", []byte("a"))),
		entry{name: "overrides/config/a.txt", data: []byte("a")}, entry{name: "overrides/data/b.txt", data: []byte("b")})
	p := mustPlan(t, l, srv, InstallRequest{Ref: testRef(id)})
	wantList(t, "blockers", noticeList(p.Blockers),
		"in_the_way: Test Pack needs to write config/a.txt, but config on the server is a file or a link, not a folder.",
		"in_the_way: Test Pack needs to write data/b.txt, but a folder, a link or a special file is in its place on the server.",
		"in_the_way: Test Pack needs to write mods/a-1.jar, but mods on the server is a file or a link, not a folder.",
	)
	if p.Ready {
		t.Error("plan is ready")
	}
	_, err := l.Install(context.Background(), srv, nil, InstallRequest{Ref: testRef(id)})
	wantKind(t, err, KindInTheWay)
	wantTree(t, outside)
	wantTree(t, srv.Dir, "config", "data/", "data/b.txt/", "mods")
	for _, r := range f.served("cdn") {
		if strings.HasSuffix(r, "/a-1.jar") {
			t.Errorf("downloaded %s for a plan that cannot be carried out", r)
		}
	}
}

func TestServerTypeAndVersionChecks(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	id := f.addPack(testIndex(f.indexFile("mods/a-1.jar", []byte("a"))))
	for _, c := range []struct {
		typ, mc  string
		world    bool
		warnings []string
		blockers []string
	}{
		{typ: "fabric", mc: "26.2", world: true},
		{typ: "paper", mc: "26.2", world: true, warnings: []string{
			"server_type_change: Test Pack needs a Fabric server on Minecraft 26.2; this server runs Paper 26.2, and the install switches it."}},
		{typ: "fabric", mc: "26.1.2", world: true, warnings: []string{
			"server_type_change: Test Pack needs a Fabric server on Minecraft 26.2; this server runs Fabric 26.1.2, and the install switches it."}},
		{typ: "fabric", mc: "26.3", warnings: []string{
			"server_type_change: Test Pack needs a Fabric server on Minecraft 26.2; this server runs Fabric 26.3, and the install switches it."}},
		{typ: "fabric", mc: "26.3", world: true, warnings: []string{
			"server_type_change: Test Pack needs a Fabric server on Minecraft 26.2; this server runs Fabric 26.3, and the install switches it."},
			blockers: []string{"minecraft_downgrade: Test Pack is for Minecraft 26.2, older than this server's 26.3, and Minecraft cannot open a world in an older version."}},
	} {
		srv := newServer(t, c.typ, c.mc)
		if c.world {
			writeFile(t, srv, "world/level.dat", "a world")
		}
		p := mustPlan(t, l, srv, InstallRequest{Ref: testRef(id)})
		wantList(t, c.typ+" "+c.mc+" warnings", noticeList(p.Warnings), c.warnings...)
		wantList(t, c.typ+" "+c.mc+" blockers", noticeList(p.Blockers), c.blockers...)
		if p.Ready != (len(c.blockers) == 0) {
			t.Errorf("%s %s: ready %v", c.typ, c.mc, p.Ready)
		}
		if c.typ == "paper" && p.Warnings[0].Hint != "Plugins in the plugins folder do not load on a Fabric server." {
			t.Errorf("hint %q", p.Warnings[0].Hint)
		}
		if len(c.blockers) > 0 {
			_, err := l.Install(context.Background(), srv, nil, InstallRequest{Ref: testRef(id)})
			wantKind(t, err, KindDowngrade)
			wantTree(t, srv.Dir, "world/", "world/level.dat")
		}
	}
}

func TestInstallRefusesAChangedPlan(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.2")
	id := f.addPack(testIndex(f.indexFile("mods/a-1.jar", []byte("the pack's"))))
	p := mustPlan(t, l, srv, InstallRequest{Ref: testRef(id)})
	writeFile(t, srv, "mods/a-1.jar", "the user's")
	_, err := l.Install(context.Background(), srv, nil, InstallRequest{Ref: testRef(id), Fingerprint: p.Fingerprint})
	wantKind(t, err, addons.KindPlanChanged)
	if got := readFile(t, srv, "mods/a-1.jar"); got != "the user's" {
		t.Errorf("the user's file is now %q", got)
	}
	p2 := mustPlan(t, l, srv, InstallRequest{Ref: testRef(id)})
	wantList(t, "changes", changeList(p2.Changes), "replace mods/a-1.jar (yours)")
	if _, err := l.Install(context.Background(), srv, nil, InstallRequest{Ref: testRef(id), Fingerprint: p2.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, srv, "mods/a-1.jar"); got != "the pack's" {
		t.Errorf("mods/a-1.jar is %q", got)
	}
	wantTree(t, srv.Dir, "mods/", "mods/a-1.jar")
	wantTree(t, l.TempDir)
}
