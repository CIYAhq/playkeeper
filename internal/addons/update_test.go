package addons

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func mustPlanUpdate(t *testing.T, l *Library, srv Server, installed []Installed, req UpdateRequest) *Plan {
	t.Helper()
	p, err := l.PlanUpdate(context.Background(), srv, installed, req)
	if err != nil {
		t.Fatalf("planning the update: %v", err)
	}
	return p
}

func TestUpdateModrinth(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "viaversion", VersionID: "ZH8459B6"})
	old, latest := f.mfile("ZH8459B6"), f.mfile("FaishMnD")

	sts, err := l.CheckUpdates(context.Background(), srv, installed)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "status", sts, []UpdateStatus{{
		Source: Modrinth, ProjectID: "P1OZGk5p", Name: "ViaVersion",
		Current: VersionInfo{VersionID: "ZH8459B6", VersionNumber: "5.11.0", Channel: "release", Published: time.Date(2026, 7, 12, 16, 45, 13, 784294000, time.UTC),
			FileName: "ViaVersion-5.11.0.jar", Size: int64(len(old.data))},
		Latest: &VersionInfo{VersionID: "FaishMnD", VersionNumber: "5.12.0", Channel: "release", Published: time.Date(2026, 9, 18, 15, 1, 59, 741758000, time.UTC),
			FileName: "ViaVersion-5.12.0.jar", Size: int64(len(latest.data))},
		Available: true,
	}})
	var body struct {
		Hashes       []string
		Algorithm    string
		Loaders      []string
		GameVersions []string `json:"game_versions"`
		VersionTypes []string `json:"version_types"`
	}
	json.Unmarshal([]byte(f.sentTo("modrinth", "/v2/version_files/update")[0].body), &body)
	if !slices.Equal(body.Hashes, []string{sha512hex(old.data)}) || body.Algorithm != "sha512" || !slices.Equal(body.Loaders, []string{"paper", "spigot", "bukkit"}) ||
		!slices.Equal(body.GameVersions, []string{"26.2"}) || !slices.Equal(body.VersionTypes, []string{"release"}) {
		t.Errorf("update check sent %+v", body)
	}

	p := mustPlanUpdate(t, l, srv, installed, UpdateRequest{})
	wantSteps(t, p, "ViaVersion 5.12.0 FaishMnD")
	if p.Steps[0].Action != ActionUpdate || p.Steps[0].Replaces == nil || p.Steps[0].Replaces.VersionID != "ZH8459B6" || !p.Ready {
		t.Errorf("plan %+v", p)
	}
	sameJSON(t, "suggestions", p.Suggestions, []Suggestion{
		{Source: Modrinth, ProjectID: "NpvuJQoq", Name: "ViaBackwards", For: "ViaVersion"},
		{Source: Modrinth, ProjectID: "TbHIxhx5", Name: "ViaRewind", For: "ViaVersion"},
	})

	res, err := l.Update(context.Background(), srv, installed, UpdateRequest{Fingerprint: p.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Installed) != 1 || res.Installed[0].VersionID != "FaishMnD" || res.Installed[0].Hash != sha512hex(latest.data) ||
		len(res.Replaced) != 1 || res.Replaced[0].VersionID != "ZH8459B6" || !res.RestartNeeded {
		t.Errorf("result %+v", res)
	}
	if got := ls(t, filepath.Join(srv.Dir, "plugins")); !slices.Equal(got, []string{"ViaVersion-5.12.0.jar"}) {
		t.Errorf("plugins holds %v", got)
	}
}

// Snapshots keep their file name from build to build; the update replaces
// the file in place.
func TestUpdatePrereleaseWithTheSameFileName(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "viaversion", VersionID: "kFqsEumf"})

	sts, err := l.CheckUpdates(context.Background(), srv, installed)
	if err != nil {
		t.Fatal(err)
	}
	if !sts[0].Available || sts[0].Latest.VersionID != "TEgYlalY" {
		t.Errorf("status %+v", sts[0])
	}
	var body map[string]any
	json.Unmarshal([]byte(f.sentTo("modrinth", "/v2/version_files/update")[0].body), &body)
	if _, ok := body["version_types"]; ok {
		t.Errorf("pre-release update check limited to %v", body["version_types"])
	}

	res, err := l.Update(context.Background(), srv, installed, UpdateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(srv.Dir, "plugins")
	if got := ls(t, plugins); !slices.Equal(got, []string{"ViaVersion-5.12.1-SNAPSHOT.jar"}) {
		t.Errorf("plugins holds %v", got)
	}
	if string(readFile(t, filepath.Join(plugins, "ViaVersion-5.12.1-SNAPSHOT.jar"))) != string(f.mfile("TEgYlalY").data) {
		t.Error("the file was not replaced")
	}
	if res.Installed[0].VersionID != "TEgYlalY" || res.Installed[0].Channel != "beta" {
		t.Errorf("record %+v", res.Installed[0])
	}
}

func TestUpdateHangar(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Hangar, Project: "ViaBackwards", VersionID: "27686"})
	if len(installed) != 2 || installed[1].VersionID != "30415" {
		t.Fatalf("installed %+v", installed)
	}

	sts, err := l.CheckUpdates(context.Background(), srv, installed)
	if err != nil {
		t.Fatal(err)
	}
	if !sts[0].Available || sts[0].Latest.VersionID != "30417" || sts[1].Available || sts[1].Latest.VersionID != "30415" {
		t.Errorf("statuses %+v", sts)
	}

	p := mustPlanUpdate(t, l, srv, installed, UpdateRequest{})
	wantSteps(t, p, "ViaBackwards 5.12.0 30417")
	sameJSON(t, "satisfied", p.Satisfied, []Satisfied{{Name: "ViaVersion", For: "ViaBackwards", FileName: "ViaVersion-5.12.0.jar", Managed: true}})

	res, err := l.Update(context.Background(), srv, installed, UpdateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := ls(t, filepath.Join(srv.Dir, "plugins")); !slices.Equal(got, []string{"ViaBackwards-5.12.0.jar", "ViaVersion-5.12.0.jar"}) {
		t.Errorf("plugins holds %v", got)
	}
	rec := res.Installed[0]
	if rec.VersionID != "30417" || rec.HashAlgo != "sha256" || rec.DependencyOf != "" || !slices.Equal(rec.Requires, []string{"31"}) {
		t.Errorf("record %+v", rec)
	}
}

func TestUpdateRefusesAChangedFile(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "viaversion", VersionID: "ZH8459B6"})
	jar := filepath.Join(srv.Dir, "plugins", "ViaVersion-5.11.0.jar")
	writeFile(t, jar, append(readFile(t, jar), "patched"...))
	downloads := len(f.sent("cdn"))

	p := mustPlanUpdate(t, l, srv, installed, UpdateRequest{})
	wantNotices(t, "blockers", p.Blockers, "modified: ViaVersion-5.11.0.jar has changed since Playkeeper installed it, so Playkeeper will not replace it.")
	_, err := l.Update(context.Background(), srv, installed, UpdateRequest{})
	wantKind(t, err, KindModified)
	if got := ls(t, filepath.Dir(jar)); !slices.Equal(got, []string{"ViaVersion-5.11.0.jar"}) || len(f.sent("cdn")) != downloads {
		t.Errorf("plugins holds %v", got)
	}
}

func TestUpdateChoices(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "viaversion"})
	vv := Key{Modrinth, "P1OZGk5p"}

	_, err := l.PlanUpdate(context.Background(), srv, installed, UpdateRequest{Keys: []Key{vv}})
	if e := wantKind(t, err, KindUpToDate); e.Msg != "ViaVersion is already at version 5.12.0." {
		t.Errorf("message %q", e.Msg)
	}

	p := mustPlanUpdate(t, l, srv, installed, UpdateRequest{Keys: []Key{vv}, AllowPrerelease: true})
	wantSteps(t, p, "ViaVersion 5.12.1-SNAPSHOT+1069 TEgYlalY")
	wantNotices(t, "warnings", p.Warnings, "prerelease: ViaVersion 5.12.1-SNAPSHOT+1069 is a beta version and may be unstable.")

	p = mustPlanUpdate(t, l, srv, installed, UpdateRequest{Keys: []Key{vv}, VersionID: "ZH8459B6"})
	wantSteps(t, p, "ViaVersion 5.11.0 ZH8459B6")
	_, err = l.PlanUpdate(context.Background(), srv, installed, UpdateRequest{Keys: []Key{vv}, VersionID: "FaishMnD"})
	wantKind(t, err, KindUpToDate)

	_, err = l.PlanUpdate(context.Background(), srv, installed, UpdateRequest{Keys: []Key{{Modrinth, "fALzjamp"}}})
	wantKind(t, err, KindNotManaged)
	_, err = l.PlanUpdate(context.Background(), srv, installed, UpdateRequest{Keys: []Key{vv, {Modrinth, "fALzjamp"}}, VersionID: "ZH8459B6"})
	wantKind(t, err, KindInvalid)

	installed = mustInstall(t, l, srv, installed, InstallRequest{Source: Modrinth, Project: "chunky"})
	_, err = l.PlanUpdate(context.Background(), srv, installed, UpdateRequest{})
	if e := wantKind(t, err, KindUpToDate); e.Msg != "Every add-on is up to date." {
		t.Errorf("message %q", e.Msg)
	}
}

func TestUpdateReinstallsAMissingFile(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "chunky"})
	jar := filepath.Join(srv.Dir, "plugins", "Chunky-Bukkit-1.5.3.jar")
	if err := os.Remove(jar); err != nil {
		t.Fatal(err)
	}

	p := mustPlanUpdate(t, l, srv, installed, UpdateRequest{})
	wantSteps(t, p, "Chunky 1.5.3 MdY6JATr")
	wantNotices(t, "warnings", p.Warnings, "not_found: Chunky-Bukkit-1.5.3.jar is missing from the plugins folder, so this installs it again.")
	if _, err := l.Update(context.Background(), srv, installed, UpdateRequest{}); err != nil {
		t.Fatal(err)
	}
	if string(readFile(t, jar)) != string(f.mfile("MdY6JATr").data) {
		t.Error("the file was not put back")
	}
}

func TestCheckUpdatesExplainsMissingVersions(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	fabricAPI := f.mfile("ewUK83HI")
	gone := Installed{Source: Modrinth, ProjectID: "GONE0001", Name: "Gone", VersionID: "GONEV001", Channel: "release", FileName: "gone.jar", HashAlgo: "sha512", Hash: sha512hex([]byte("gone"))}
	oldPlugin := Installed{Source: Hangar, ProjectID: "99999", Name: "Old Plugin", VersionID: "1", Channel: "release", FileName: "old.jar", HashAlgo: "sha256", Hash: sha256hex([]byte("old"))}
	fabricRec := Installed{Source: Modrinth, ProjectID: "P7dR8mSH", Name: "Fabric API", VersionID: "ewUK83HI", VersionNumber: "0.161.0+26.2", Channel: "release",
		FileName: fabricAPI.name, HashAlgo: "sha512", Hash: sha512hex(fabricAPI.data), Size: int64(len(fabricAPI.data))}

	for _, tc := range []struct {
		name      string
		typ, mc   string
		installed []Installed
		want      []string
	}{
		{"newer Minecraft", "paper", "26.4", mustInstall(t, l, newServer(t, "paper", "26.2"), nil, InstallRequest{Source: Modrinth, Project: "viaversion"}),
			[]string{"no_compatible_version: ViaVersion has no version for Paper servers on Minecraft 26.4."}},
		{"gone from the sources", "paper", "26.2", []Installed{gone, oldPlugin},
			[]string{"not_found: Gone is no longer listed on Modrinth.", "not_found: Old Plugin is no longer listed on Hangar."}},
		{"only pre-releases", "fabric", "26.4-snapshot-1", []Installed{fabricRec},
			[]string{"only_prerelease: Fabric API has only pre-release versions for Minecraft 26.4-snapshot-1 (newest: 0.161.1+26.4)."}},
		{"Hangar add-on on a mod server", "fabric", "26.2", []Installed{oldPlugin},
			[]string{"source_unsupported: Hangar only has plugins, and this server runs mods."}},
	} {
		sts, err := l.CheckUpdates(context.Background(), newServer(t, tc.typ, tc.mc), tc.installed)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		var got []string
		for _, st := range sts {
			if st.Latest != nil || st.Available || st.Notice == nil {
				t.Errorf("%s: status %+v", tc.name, st)
				continue
			}
			got = append(got, string(st.Notice.Kind)+": "+st.Notice.Msg)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}

	_, err := l.PlanUpdate(context.Background(), newServer(t, "fabric", "26.4-snapshot-1"), []Installed{fabricRec}, UpdateRequest{Keys: []Key{fabricRec.Key()}})
	wantKind(t, err, KindOnlyPrerelease)
}
