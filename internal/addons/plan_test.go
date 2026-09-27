package addons

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func mustPlan(t *testing.T, l *Library, srv Server, installed []Installed, req InstallRequest) *Plan {
	t.Helper()
	p, err := l.PlanInstall(context.Background(), srv, installed, req)
	if err != nil {
		t.Fatalf("planning %s: %v", req.Project, err)
	}
	return p
}

func wantSteps(t *testing.T, p *Plan, want string) {
	t.Helper()
	if got := stepList(p); got != want {
		t.Errorf("steps %q, want %q", got, want)
	}
}

func wantNotices(t *testing.T, what string, got []Notice, want ...string) {
	t.Helper()
	var msgs []string
	for _, n := range got {
		msgs = append(msgs, string(n.Kind)+": "+n.Msg)
	}
	if !slices.Equal(msgs, want) {
		t.Errorf("%s %q, want %q", what, msgs, want)
	}
}

func TestPlanInstallModrinthWithRequiredDependencies(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	p := mustPlan(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "viarewind"})

	wantSteps(t, p, "ViaRewind 4.2.0 EPLCoxMK, ViaBackwards 5.12.0 SxGhdsPK, ViaVersion 5.12.0 FaishMnD")
	if !p.Ready || len(p.Blockers)+len(p.Warnings)+len(p.Manual)+len(p.Satisfied)+len(p.Suggestions) != 0 {
		t.Errorf("plan not clean: %+v", p)
	}
	jar := f.mfile("EPLCoxMK")
	sameJSON(t, "ViaRewind's step", p.Steps[0], Step{
		Action: ActionInstall, Source: Modrinth, ProjectID: "TbHIxhx5", Slug: "viarewind", Name: "ViaRewind",
		Summary:   "ViaVersion addon to allow 1.8.x and 1.7.x clients on newer server versions.",
		IconURL:   "https://cdn.modrinth.com/data/TbHIxhx5/f59ffe031387b06a9b1efa736dbbb4db44284574_96.webp",
		VersionID: "EPLCoxMK", VersionNumber: "4.2.0", Channel: "release", Published: time.Date(2026, 9, 18, 15, 8, 24, 612555000, time.UTC),
		FileName: "ViaRewind-4.2.0.jar", Size: int64(len(jar.data)), HashAlgo: "sha512", Hash: sha512hex(jar.data),
		Requires: []string{"NpvuJQoq", "P1OZGk5p"},
	})
	for _, s := range p.Steps[1:] {
		if s.DependencyOf != "TbHIxhx5" {
			t.Errorf("%s is a dependency of %q", s.Name, s.DependencyOf)
		}
	}
	if !slices.Equal(p.Steps[1].Requires, []string{"P1OZGk5p"}) || p.Steps[2].Requires != nil {
		t.Errorf("requires %v and %v", p.Steps[1].Requires, p.Steps[2].Requires)
	}

	q := f.sentTo("modrinth", "/v2/project/TbHIxhx5/version")[0].query
	if q.Get("loaders") != `["paper","spigot","bukkit"]` || q.Get("game_versions") != `["26.2"]` {
		t.Errorf("versions asked with %v", q)
	}
	if n := len(f.sent("cdn")); n != 0 {
		t.Errorf("planning downloaded %d files", n)
	}
	if ls(t, srv.Dir) != nil {
		t.Errorf("planning wrote %v", ls(t, srv.Dir))
	}
	if again := mustPlan(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "viarewind"}); again.Fingerprint != p.Fingerprint || p.Fingerprint == "" {
		t.Errorf("fingerprints %q and %q", p.Fingerprint, again.Fingerprint)
	}
	reworded := *p
	reworded.Steps = slices.Clone(p.Steps)
	reworded.Steps[0].Summary = "Lets old clients join."
	if reworded.fingerprint() != p.Fingerprint {
		t.Error("a reworded description changed the fingerprint")
	}
}

// The fingerprint follows what a plan does, not the order it lists the files
// and dependencies in.
func TestFingerprintIgnoresOrderButNotVersions(t *testing.T) {
	f := newFakes(t)
	p := mustPlan(t, f.library(), newServer(t, "paper", "26.2"), nil, InstallRequest{Source: Hangar, Project: "ViaRewind"})
	p.Satisfied = []Satisfied{{Name: "ProtocolLib", For: "ViaRewind", FileName: "ProtocolLib.jar"}, {Name: "packetevents", For: "ViaRewind", FileName: "packetevents.jar", Managed: true}}
	want := p.fingerprint()
	reordered := *p
	reordered.Steps = slices.Clone(p.Steps)
	slices.Reverse(reordered.Steps)
	reordered.Steps[2].Requires = []string{"31", "12"}
	reordered.Satisfied = slices.Clone(p.Satisfied)
	slices.Reverse(reordered.Satisfied)
	if got := reordered.fingerprint(); got != want {
		t.Errorf("the same plan in another order has fingerprint %q, want %q", got, want)
	}
	reordered.Steps[1].VersionID, reordered.Steps[1].VersionNumber = "30500", "5.13.0"
	if reordered.fingerprint() == want {
		t.Error("a new version of a dependency did not change the fingerprint")
	}
}

func TestPlanInstallHangarWithRequiredDependencies(t *testing.T) {
	f := newFakes(t)
	p := mustPlan(t, f.library(), newServer(t, "paper", "26.2"), nil, InstallRequest{Source: Hangar, Project: "ViaRewind"})

	wantSteps(t, p, "ViaRewind 4.2.0 30418, ViaBackwards 5.12.0 30417, ViaVersion 5.12.0 30415")
	if !p.Ready {
		t.Errorf("not ready: %+v", p.Blockers)
	}
	jar := f.hfile("30418")
	sameJSON(t, "ViaRewind's step", p.Steps[0], Step{
		Action: ActionInstall, Source: Hangar, ProjectID: "112", Slug: "ViaRewind", Name: "ViaRewind",
		Summary:   "ViaVersion addon to allow 1.8.x and 1.7.x clients on newer server versions.",
		IconURL:   "https://hangarcdn.papermc.io/avatars/project/112.webp?v=1",
		VersionID: "30418", VersionNumber: "4.2.0", Channel: "release", Published: time.Date(2026, 9, 18, 15, 8, 16, 539019000, time.UTC),
		FileName: "ViaRewind-4.2.0.jar", Size: int64(len(jar.data)), HashAlgo: "sha256", Hash: sha256hex(jar.data),
		Requires: []string{"12", "31"},
	})
	if p.Steps[1].DependencyOf != "112" || p.Steps[2].DependencyOf != "112" || !slices.Equal(p.Steps[1].Requires, []string{"31"}) {
		t.Errorf("dependencies %+v", p.Steps[1:])
	}
	q := f.sentTo("hangar", "/api/v1/projects/112/versions")[0].query
	if q.Get("platform") != "PAPER" || q.Get("platformVersion") != "26.2" || q.Get("limit") != "25" {
		t.Errorf("versions asked with %v", q)
	}
}

func TestPlanInstallPicksTheLoadersVersion(t *testing.T) {
	for _, tc := range []struct{ typ, folder, steps string }{
		{"paper", "plugins", "Chunky 1.5.3 MdY6JATr"},
		{"purpur", "plugins", "Chunky 1.5.3 MdY6JATr"},
		{"fabric", "mods", "Chunky 1.5.3 4Eotm6ov, Fabric API 0.161.0+26.2 ewUK83HI"},
		{"quilt", "mods", "Chunky 1.5.3 4Eotm6ov, Fabric API 0.161.0+26.2 ewUK83HI"},
		{"neoforge", "mods", "Chunky 1.5.4 EyCqftOK"},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			f := newFakes(t)
			p := mustPlan(t, f.library(), newServer(t, tc.typ, "26.2"), nil, InstallRequest{Source: Modrinth, Project: "chunky"})
			wantSteps(t, p, tc.steps)
			if p.Target.Folder != tc.folder || !p.Ready {
				t.Errorf("folder %q, ready %v, blockers %+v", p.Target.Folder, p.Ready, p.Blockers)
			}
		})
	}
}

// A version that runs on several loaders lists every loader's dependencies;
// a Paper server does not need Fabric API.
func TestPlanInstallSkipsDependenciesForOtherLoaders(t *testing.T) {
	f := newFakes(t)
	f.patchVersion("MdY6JATr", func(v obj) {
		v["loaders"] = append(list(v["loaders"]), "fabric")
		v["dependencies"] = []any{mdep("required", "P7dR8mSH", "")}
	})
	p := mustPlan(t, f.library(), newServer(t, "paper", "26.2"), nil, InstallRequest{Source: Modrinth, Project: "chunky"})
	wantSteps(t, p, "Chunky 1.5.3 MdY6JATr")
	if !p.Ready || p.Steps[0].Requires != nil {
		t.Errorf("ready %v, requires %v", p.Ready, p.Steps[0].Requires)
	}
	if reqs := f.sentTo("modrinth", "/v2/project/P7dR8mSH/version"); len(reqs) != 0 {
		t.Errorf("asked for Fabric API's versions")
	}
}

func TestPlanInstallHonoursPinnedDependencyVersions(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.2")
	req := InstallRequest{Source: Modrinth, Project: "better-stats"}
	wantSteps(t, mustPlan(t, l, srv, nil, req), "Better Statistics Screen 5.5.6+fn-26.2 ga3HGfK0, TCDCommons API 5.5.6+fn-26.2 NM3aWYRR")

	req.VersionID = "UDcubpko"
	wantSteps(t, mustPlan(t, l, srv, nil, req), "Better Statistics Screen 5.5.5+fn-26.2 UDcubpko, TCDCommons API 5.5.5+fn-26.2 E3obAdZc")

	f.patchVersion("E3obAdZc", func(v obj) { v["game_versions"] = []any{"26.1"} })
	wantSteps(t, mustPlan(t, l, srv, nil, req), "Better Statistics Screen 5.5.5+fn-26.2 UDcubpko, TCDCommons API 5.5.6+fn-26.2 NM3aWYRR")
}

func TestPlanInstallRefusals(t *testing.T) {
	f := newFakes(t)
	f.addModrinthProject(obj{"id": "PACK0001", "slug": "some-pack", "title": "Some Pack", "project_type": "modpack", "server_side": "required", "loaders": []any{"fabric"}})
	l := f.library()
	chunkyMismatch := "That version of Chunky does not fit this server (Paper, Minecraft 26.2)."
	for _, tc := range []struct {
		name      string
		typ, mc   string
		installed []Installed
		req       InstallRequest
		kind      Kind
		msg       string
	}{
		{"client-only mod", "fabric", "26.2", nil, InstallRequest{Source: Modrinth, Project: "sodium"},
			KindClientOnly, "Sodium only runs in the game client, not on a server."},
		{"mod for another loader", "paper", "26.2", nil, InstallRequest{Source: Modrinth, Project: "fabric-api"},
			KindNoVersion, "Fabric API has no version for Paper servers on Minecraft 26.2."},
		{"version for another loader", "paper", "26.2", nil, InstallRequest{Source: Modrinth, Project: "chunky", VersionID: "4Eotm6ov"},
			KindNoVersion, chunkyMismatch},
		{"version for older Minecraft", "paper", "26.2", nil, InstallRequest{Source: Modrinth, Project: "chunky", VersionID: "P3y2MXnd"},
			KindNoVersion, chunkyMismatch},
		{"version of another project", "paper", "26.2", nil, InstallRequest{Source: Modrinth, Project: "chunky", VersionID: "SxGhdsPK"},
			KindNoVersion, chunkyMismatch},
		{"unknown version", "paper", "26.2", nil, InstallRequest{Source: Modrinth, Project: "chunky", VersionID: "NOPE1234"},
			KindNoVersion, chunkyMismatch},
		{"only pre-releases", "fabric", "26.3", nil, InstallRequest{Source: Modrinth, Project: "zfastnoise"},
			KindOnlyPrerelease, "Fast Noise has only pre-release versions for Minecraft 26.3 (newest: 1.1.0-beta.5+26.3, a beta)."},
		{"modpack", "fabric", "26.2", nil, InstallRequest{Source: Modrinth, Project: "some-pack"},
			KindNotAddon, "Some Pack is a modpack, not a plugin or mod."},
		{"already installed", "paper", "26.2", []Installed{{Source: Modrinth, ProjectID: "TbHIxhx5", Name: "ViaRewind", VersionNumber: "4.1.1", FileName: "ViaRewind-4.1.1.jar"}},
			InstallRequest{Source: Modrinth, Project: "viarewind"}, KindAlreadyInstalled, "ViaRewind is already installed on this server (version 4.1.1)."},
		{"unknown Modrinth project", "paper", "26.2", nil, InstallRequest{Source: Modrinth, Project: "no-such-project"},
			KindNotFound, `Modrinth has no project "no-such-project".`},
		{"unknown Hangar project", "paper", "26.2", nil, InstallRequest{Source: Hangar, Project: "NoSuchPlugin"},
			KindNotFound, `Hangar has no project "NoSuchPlugin".`},
		{"Hangar for mods", "fabric", "26.2", nil, InstallRequest{Source: Hangar, Project: "ViaVersion"},
			KindSourceUnsupported, "Hangar only has plugins, and this server runs mods."},
		{"project with a path", "paper", "26.2", nil, InstallRequest{Source: Modrinth, Project: "../chunky"},
			KindInvalid, "That is not a valid project name or id."},
		{"version with a path", "paper", "26.2", nil, InstallRequest{Source: Modrinth, Project: "chunky", VersionID: "a/b"},
			KindInvalid, "That is not a valid version id."},
		{"vanilla", "vanilla", "26.2", nil, InstallRequest{Source: Modrinth, Project: "chunky"},
			KindNoAddons, "Vanilla servers cannot load plugins or mods."},
	} {
		_, err := l.PlanInstall(context.Background(), newServer(t, tc.typ, tc.mc), tc.installed, tc.req)
		var e *Error
		if !errors.As(err, &e) || e.Kind != tc.kind || e.Msg != tc.msg {
			t.Errorf("%s: %v (kind %q), want %q: %s", tc.name, err, KindOf(err), tc.kind, tc.msg)
		}
	}
	if n := len(f.sent("cdn")); n != 0 {
		t.Errorf("refused plans downloaded %d files", n)
	}
}

func TestPlanInstallCountsFilesAlreadyOnTheServer(t *testing.T) {
	handMade := func(t *testing.T, srv Server) {
		writeFile(t, filepath.Join(srv.Dir, "plugins", "ViaVersion.jar"), pluginJar(t, "ViaVersion", "5.9.0"))
	}
	t.Run("added by hand", func(t *testing.T) {
		for _, tc := range []struct {
			src     Source
			project string
			steps   string
		}{
			{Modrinth, "viarewind", "ViaRewind 4.2.0 EPLCoxMK, ViaBackwards 5.12.0 SxGhdsPK"},
			{Hangar, "ViaRewind", "ViaRewind 4.2.0 30418, ViaBackwards 5.12.0 30417"},
		} {
			f := newFakes(t)
			srv := newServer(t, "paper", "26.2")
			handMade(t, srv)
			p := mustPlan(t, f.library(), srv, nil, InstallRequest{Source: tc.src, Project: tc.project})
			wantSteps(t, p, tc.steps)
			sameJSON(t, string(tc.src)+" satisfied", p.Satisfied, []Satisfied{
				{Name: "ViaVersion", For: "ViaRewind", FileName: "ViaVersion.jar"},
				{Name: "ViaVersion", For: "ViaBackwards", FileName: "ViaVersion.jar"},
			})
			if !p.Ready {
				t.Errorf("%s: not ready: %+v", tc.src, p.Blockers)
			}
		}
	})
	t.Run("the add-on itself added by hand", func(t *testing.T) {
		f := newFakes(t)
		srv := newServer(t, "paper", "26.2")
		handMade(t, srv)
		p := mustPlan(t, f.library(), srv, nil, InstallRequest{Source: Modrinth, Project: "viaversion"})
		wantNotices(t, "blockers", p.Blockers, "duplicate: ViaVersion is already on this server as ViaVersion.jar.")
		if p.Ready {
			t.Error("ready")
		}
	})
	t.Run("installed from the other source", func(t *testing.T) {
		f := newFakes(t)
		l := f.library()
		srv := newServer(t, "paper", "26.2")
		installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "viaversion"})
		p := mustPlan(t, l, srv, installed, InstallRequest{Source: Hangar, Project: "ViaRewind"})
		wantSteps(t, p, "ViaRewind 4.2.0 30418, ViaBackwards 5.12.0 30417")
		sameJSON(t, "satisfied", p.Satisfied, []Satisfied{
			{Name: "ViaVersion", For: "ViaRewind", FileName: "ViaVersion-5.12.0.jar", Managed: true},
			{Name: "ViaVersion", For: "ViaBackwards", FileName: "ViaVersion-5.12.0.jar", Managed: true},
		})

		p = mustPlan(t, l, srv, installed, InstallRequest{Source: Hangar, Project: "ViaVersion"})
		if len(p.Blockers) == 0 || p.Blockers[0].Msg != "ViaVersion is already installed from Modrinth." || p.Blockers[0].Hint != "Remove it first to switch to the copy on Hangar." {
			t.Errorf("blockers %+v", p.Blockers)
		}
	})
}

func TestPlanInstallRefusesConflicts(t *testing.T) {
	t.Run("with an add-on Playkeeper installed", func(t *testing.T) {
		f := newFakes(t)
		l := f.library()
		srv := newServer(t, "fabric", "26.2")
		installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "anti-xray"})
		p := mustPlan(t, l, srv, installed, InstallRequest{Source: Modrinth, Project: "zfastnoise"})
		wantNotices(t, "blockers", p.Blockers, "conflict: Fast Noise cannot run together with AntiXray (antixray-fabric-1.4.16+26.1.jar), which is on this server.")
		if p.Ready {
			t.Error("ready")
		}
	})
	t.Run("with a file added by hand that Modrinth knows", func(t *testing.T) {
		f := newFakes(t)
		srv := newServer(t, "fabric", "26.2")
		writeFile(t, filepath.Join(srv.Dir, "mods", "antixray.jar"), f.mfile("AK313N9m").data)
		p := mustPlan(t, f.library(), srv, nil, InstallRequest{Source: Modrinth, Project: "zfastnoise"})
		wantNotices(t, "blockers", p.Blockers, "conflict: Fast Noise cannot run together with AntiXray (antixray.jar), which is on this server.")
	})
	t.Run("an installed add-on cannot run with the new one", func(t *testing.T) {
		f := newFakes(t)
		l := f.library()
		srv := newServer(t, "fabric", "26.2")
		installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "zfastnoise"})
		p := mustPlan(t, l, srv, installed, InstallRequest{Source: Modrinth, Project: "moonrise-opt"})
		wantNotices(t, "blockers", p.Blockers, "conflict: Moonrise cannot run together with Fast Noise (zfastnoise-1.0.40+26.2.jar), which is on this server.")
	})
	t.Run("within one plan", func(t *testing.T) {
		f := newFakes(t)
		f.patchVersion("FaishMnD", func(v obj) { v["dependencies"] = append(list(v["dependencies"]), mdep("incompatible", "NpvuJQoq", "")) })
		p := mustPlan(t, f.library(), newServer(t, "paper", "26.2"), nil, InstallRequest{Source: Modrinth, Project: "viarewind"})
		wantNotices(t, "blockers", p.Blockers, "conflict: ViaVersion cannot run together with ViaBackwards, which this plan also needs.")
	})
}

func TestPlanInstallManualSteps(t *testing.T) {
	t.Run("dependency that is not on Hangar", func(t *testing.T) {
		f := newFakes(t)
		l := f.library()
		srv := newServer(t, "paper", "26.2")
		p := mustPlan(t, l, srv, nil, InstallRequest{Source: Hangar, Project: "Orebfuscator"})
		wantSteps(t, p, "Orebfuscator 5.6.2 30490")
		sameJSON(t, "manual", p.Manual, []ManualStep{{
			Notice: Notice{Kind: KindDepExternal, Params: map[string]string{"name": "Orebfuscator", "dependency": "ProtocolLib", "folder": "plugins"},
				Msg: "Orebfuscator needs ProtocolLib, which is not on Hangar.", Hint: "Download ProtocolLib yourself, then put it in the server's plugins folder."},
			URL: "https://github.com/dmulloy2/ProtocolLib/",
		}})
		if !p.Ready {
			t.Errorf("not ready: %+v", p.Blockers)
		}

		writeFile(t, filepath.Join(srv.Dir, "plugins", "ProtocolLib.jar"), pluginJar(t, "ProtocolLib", "5.4.0"))
		p = mustPlan(t, l, srv, nil, InstallRequest{Source: Hangar, Project: "Orebfuscator"})
		if len(p.Manual) != 0 {
			t.Errorf("manual %+v", p.Manual)
		}
		sameJSON(t, "satisfied", p.Satisfied, []Satisfied{{Name: "ProtocolLib", For: "Orebfuscator", FileName: "ProtocolLib.jar"}})
	})
	t.Run("add-on only offered on another site", func(t *testing.T) {
		f := newFakes(t)
		l := f.library()
		srv := newServer(t, "paper", "26.2")
		link := "https://download.geysermc.org/v2/projects/geyser/versions/latest/builds/latest/downloads/spigot"
		p := mustPlan(t, l, srv, nil, InstallRequest{Source: Hangar, Project: "Geyser"})
		if len(p.Steps) != 0 || p.Ready {
			t.Errorf("steps %q, ready %v", stepList(p), p.Ready)
		}
		sameJSON(t, "manual", p.Manual, []ManualStep{{
			Notice: Notice{Kind: KindExternal, Params: map[string]string{"name": "Geyser", "version": "Geyser", "host": "download.geysermc.org", "folder": "plugins"},
				Msg: "Geyser is only offered on download.geysermc.org, so Playkeeper cannot install it for you.", Hint: "Download it from that page, then put it in the server's plugins folder."},
			URL: link,
		}})

		_, err := l.Install(context.Background(), srv, nil, InstallRequest{Source: Hangar, Project: "Geyser"})
		wantKind(t, err, KindExternal)
		if ls(t, srv.Dir) != nil || len(f.sent("cdn")) != 0 || len(f.strays()) != 0 {
			t.Errorf("wrote %v, downloaded %d, strays %v", ls(t, srv.Dir), len(f.sent("cdn")), f.strays())
		}

		vs, err := l.Versions(context.Background(), srv, Hangar, "Geyser")
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, "versions", vs, []VersionInfo{{VersionID: "238", VersionNumber: "Geyser", Channel: "release",
			Published: time.Date(2023, 3, 19, 20, 49, 50, 933106000, time.UTC), ExternalURL: link}})
	})
	t.Run("file that is not on Modrinth", func(t *testing.T) {
		f := newFakes(t)
		f.patchVersion("MdY6JATr", func(v obj) { v["dependencies"] = []any{mdep("required", "", "SomeLib.jar")} })
		l := f.library()
		srv := newServer(t, "paper", "26.2")
		p := mustPlan(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "chunky"})
		sameJSON(t, "manual", p.Manual, []ManualStep{{
			Notice: Notice{Kind: KindDepUnlisted, Params: map[string]string{"name": "Chunky", "file": "SomeLib.jar", "folder": "plugins"},
				Msg: "Chunky needs the file SomeLib.jar, which is not on Modrinth.", Hint: "Look on Chunky's page for where to get it, then put it in the server's plugins folder."},
			URL: "https://modrinth.com/plugin/chunky",
		}})

		writeFile(t, filepath.Join(srv.Dir, "plugins", "SomeLib.jar"), []byte("a library"))
		p = mustPlan(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "chunky"})
		if len(p.Manual) != 0 {
			t.Errorf("manual %+v", p.Manual)
		}
		sameJSON(t, "satisfied", p.Satisfied, []Satisfied{{Name: "SomeLib.jar", For: "Chunky", FileName: "SomeLib.jar"}})
	})
}

func TestPlanInstallPrereleasesOnlyWhenAllowed(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	p := mustPlan(t, l, newServer(t, "fabric", "26.3"), nil, InstallRequest{Source: Modrinth, Project: "zfastnoise", AllowPrerelease: true})
	wantSteps(t, p, "Fast Noise 1.1.0-beta.5+26.3 bjeF0iDC, ZConfig 1.0.0+26.x tsgt79sG")
	wantNotices(t, "warnings", p.Warnings, "prerelease: Fast Noise 1.1.0-beta.5+26.3 is a beta version and may be unstable.")
	if !p.Ready || p.Steps[0].Channel != "beta" || p.Steps[1].Channel != "release" || !slices.Equal(p.Steps[0].Requires, []string{"4qmvXRB9"}) {
		t.Errorf("plan %+v", p)
	}

	srv := newServer(t, "fabric", "26.2")
	p = mustPlan(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "zfastnoise", AllowPrerelease: true})
	wantSteps(t, p, "Fast Noise 1.0.40+26.2 9TGMeiu2")
	if len(p.Warnings) != 0 {
		t.Errorf("warnings %+v", p.Warnings)
	}

	vs, err := l.Versions(context.Background(), srv, Modrinth, "zfastnoise")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range vs {
		got = append(got, v.VersionID+" "+v.Channel)
	}
	if want := []string{"RxI4fDfx beta", "hggcKC1t beta", "9TGMeiu2 release"}; !slices.Equal(got, want) {
		t.Errorf("versions %v, want %v", got, want)
	}
}

func TestPlanInstallDependencyProblems(t *testing.T) {
	chunky := InstallRequest{Source: Modrinth, Project: "chunky"}
	fabric := func(t *testing.T) Server { return newServer(t, "fabric", "26.2") }
	t.Run("dependency Modrinth does not list", func(t *testing.T) {
		f := newFakes(t)
		f.patchVersion("4Eotm6ov", func(v obj) { v["dependencies"] = []any{mdep("required", "GONE0001", "")} })
		p := mustPlan(t, f.library(), fabric(t), nil, chunky)
		wantSteps(t, p, "Chunky 1.5.3 4Eotm6ov")
		wantNotices(t, "blockers", p.Blockers, "dependency_unavailable: Chunky needs a project that Modrinth does not list (GONE0001).")
		if p.Ready {
			t.Error("ready")
		}
	})
	t.Run("dependency with only pre-releases", func(t *testing.T) {
		f := newFakes(t)
		for _, id := range []string{"ewUK83HI", "UWwhUX3k"} {
			f.patchVersion(id, func(v obj) { v["version_type"] = "beta" })
		}
		l := f.library()
		srv := fabric(t)
		p := mustPlan(t, l, srv, nil, chunky)
		wantSteps(t, p, "Chunky 1.5.3 4Eotm6ov")
		wantNotices(t, "blockers", p.Blockers, "only_prerelease: Chunky needs Fabric API, which has only pre-release versions for Minecraft 26.2.")
		if !slices.Equal(p.Steps[0].Requires, []string{"P7dR8mSH"}) {
			t.Errorf("requires %v", p.Steps[0].Requires)
		}

		p = mustPlan(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "chunky", AllowPrerelease: true})
		wantSteps(t, p, "Chunky 1.5.3 4Eotm6ov, Fabric API 0.161.0+26.2 ewUK83HI")
		wantNotices(t, "warnings", p.Warnings, "prerelease: Fabric API 0.161.0+26.2 is a beta version and may be unstable.")
		if !p.Ready {
			t.Errorf("not ready: %+v", p.Blockers)
		}
	})
	t.Run("dependency without a version for the server", func(t *testing.T) {
		f := newFakes(t)
		for _, id := range []string{"ewUK83HI", "UWwhUX3k"} {
			f.patchVersion(id, func(v obj) { v["game_versions"] = []any{"26.1"} })
		}
		p := mustPlan(t, f.library(), fabric(t), nil, chunky)
		wantNotices(t, "blockers", p.Blockers, "dependency_unavailable: Chunky needs Fabric API, which has no version for Fabric servers on Minecraft 26.2.")
	})
	t.Run("client-only dependency", func(t *testing.T) {
		f := newFakes(t)
		f.patchVersion("4Eotm6ov", func(v obj) { v["dependencies"] = []any{mdep("required", "AANobbMI", "")} })
		p := mustPlan(t, f.library(), fabric(t), nil, chunky)
		wantSteps(t, p, "Chunky 1.5.3 4Eotm6ov")
		wantNotices(t, "warnings", p.Warnings, "dependency_client_only: Chunky lists Sodium as needed, but Sodium only runs in the game client, so the server does without it.")
		if !p.Ready || p.Steps[0].Requires != nil {
			t.Errorf("ready %v, requires %v", p.Ready, p.Steps[0].Requires)
		}
	})
	t.Run("dependency Hangar does not list", func(t *testing.T) {
		f := newFakes(t)
		f.patchHangarVersion("30418", func(v obj) {
			list(v["pluginDependencies"].(obj)["PAPER"])[0].(obj)["projectId"] = json.Number("99999")
		})
		p := mustPlan(t, f.library(), newServer(t, "paper", "26.2"), nil, InstallRequest{Source: Hangar, Project: "ViaRewind"})
		wantSteps(t, p, "ViaRewind 4.2.0 30418, ViaVersion 5.12.0 30415")
		wantNotices(t, "blockers", p.Blockers, "dependency_unavailable: ViaRewind needs a project that Hangar does not list (99999).")
	})
}
