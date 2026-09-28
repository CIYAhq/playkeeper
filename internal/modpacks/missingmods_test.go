package modpacks

import "testing"

// Missing Mods Checker, which Better MC 4 and 2 ship on Modrinth to have
// players download the mods only CurseForge offers, opens a window on a
// server, which stops its start. It stays off the server, and the mods its
// list names come from CurseForge: with a step for one only CurseForge's
// app may download, and none for one CurseForge tags for players' games or
// one that isn't on CurseForge. Without a CurseForge key, each one on
// CurseForge gets a step.
func TestMissingModsCheckerStaysOffAndItsModsComeFromCurseForge(t *testing.T) {
	f := newFakes(t)
	list := `[
		{"displayName": "Lithium", "pattern": "lithium-fabric-0.25.3+mc26.3.jar", "url": "https://www.curseforge.com/minecraft/mc-mods/lithium/download/8895969", "destination": "mods"},
		{"displayName": "FerriteCore", "url": "https://www.curseforge.com/minecraft/mc-mods/ferritecore/download/7806040", "destination": "mods"},
		{"displayName": "Sodium", "url": "https://www.curseforge.com/minecraft/mc-mods/sodium/download/8888037", "destination": "mods"},
		{"displayName": "Elsewhere", "url": "https://example.com/elsewhere.jar", "destination": "mods"}
	]`
	id := f.addPack(testIndex(f.indexFile("mods/a-1.jar", []byte("a"), "required", "required")),
		entry{name: "overrides/mods/missingmodschecker-1.0.1.jar", data: []byte("checker")},
		entry{name: "overrides/config/missing_mods_checker.json", data: []byte(list)})
	srv := newServer(t, "fabric", "26.2")
	req := InstallRequest{Ref: testRef(id)}
	p := mustPlan(t, f.library(), srv, req)
	wantList(t, "changes", changeList(p.Changes),
		"add config/missing_mods_checker.json", "add mods/a-1.jar", "add mods/lithium-fabric-0.25.3+mc26.3.jar")
	wantList(t, "skipped", skippedList(p.Skipped),
		"client_only mods/missingmodschecker-1.0.1.jar", "client_only mods/sodium-fabric-0.9.2+mc26.3.jar")
	wantList(t, "manual", manualList(p.Manual),
		"external_download: FerriteCore's author only allows downloads through CurseForge's app, so Playkeeper cannot download ferritecore-9.0.0-fabric.jar for you. (https://www.curseforge.com/minecraft/mc-mods/ferritecore/download/7806040)")
	mustInstall(t, f.library(), srv, req)
	if readFile(t, srv, "mods/lithium-fabric-0.25.3+mc26.3.jar") != string(generated("lithium-fabric-0.25.3+mc26.3.jar")) {
		t.Error("Lithium on the server isn't CurseForge's file")
	}

	l := f.library()
	l.CurseForge = nil
	p = mustPlan(t, l, newServer(t, "fabric", "26.2"), req)
	wantList(t, "changes without a CurseForge key", changeList(p.Changes), "add config/missing_mods_checker.json", "add mods/a-1.jar")
	var kinds []string
	for _, m := range p.Manual {
		kinds = append(kinds, string(m.Kind)+" "+m.URL)
	}
	wantList(t, "manual without a CurseForge key", kinds,
		"curseforge_unavailable https://www.curseforge.com/minecraft/mc-mods/lithium/download/8895969",
		"curseforge_unavailable https://www.curseforge.com/minecraft/mc-mods/ferritecore/download/7806040",
		"curseforge_unavailable https://www.curseforge.com/minecraft/mc-mods/sodium/download/8888037")
}
