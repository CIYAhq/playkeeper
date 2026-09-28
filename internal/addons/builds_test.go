package addons

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
)

// BlueMap publishes each release as one build per platform, its Paper build
// seconds before its Spigot build, and Simple Voice Chat its Quilt build
// minutes before its Fabric build (modrinth/testdata). A server gets the
// build for its own platform.
func TestTheServersOwnBuildOfARelease(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	ctx := context.Background()
	for _, c := range []struct{ typ, project, want string }{
		{"paper", "bluemap", "5.28-paper"},
		{"purpur", "bluemap", "5.28-paper"},
		{"fabric", "bluemap", "5.28-fabric"},
		{"quilt", "simple-voice-chat", "quilt-2.6.22+26.2"},
		{"fabric", "simple-voice-chat", "fabric-2.6.22+26.2"},
	} {
		d, err := l.Details(ctx, newServer(t, c.typ, "26.2"), Modrinth, c.project)
		if err != nil {
			t.Fatalf("%s on %s: %v", c.project, c.typ, err)
		}
		if d.Latest == nil || d.Latest.VersionNumber != c.want || d.Notice != nil {
			t.Errorf("%s on %s: latest %+v (%+v), want %s", c.project, c.typ, d.Latest, d.Notice, c.want)
		}
	}

	paper := newServer(t, "paper", "26.2")
	vs, err := l.Versions(ctx, paper, Modrinth, "bluemap")
	if err != nil {
		t.Fatal(err)
	}
	var numbers []string
	for _, v := range vs {
		numbers = append(numbers, v.VersionNumber)
	}
	if want := []string{"5.28-paper", "5.28-spigot", "5.27-paper", "5.27-spigot"}; !slices.Equal(numbers, want) {
		t.Errorf("versions %q, want %q", numbers, want)
	}
	p, err := l.PlanInstall(ctx, paper, nil, InstallRequest{Source: Modrinth, Project: "bluemap"})
	if err != nil {
		t.Fatal(err)
	}
	wantSteps(t, p, "BlueMap 5.28-paper pILlMIlN")

	// A newer release built only for another platform still wins.
	f.patchVersion("pILlMIlN", func(v obj) { v["game_versions"] = []any{"26.3"} })
	d, err := l.Details(ctx, paper, Modrinth, "bluemap")
	if err != nil || d.Latest == nil || d.Latest.VersionNumber != "5.28-spigot" {
		t.Errorf("without 5.28-paper: latest %+v, %v", d.Latest, err)
	}
}

func TestUpdatesMoveToTheServersOwnBuild(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	ctx := context.Background()
	srv := newServer(t, "paper", "26.2")
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "bluemap", VersionID: "w7JKRF0Q"})
	installed = mustInstall(t, l, srv, installed, InstallRequest{Source: Modrinth, Project: "viaversion", VersionID: "ZH8459B6"})

	sts, err := l.CheckUpdates(ctx, srv, installed)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, st := range sts {
		got = append(got, st.Name+" "+st.Current.VersionNumber+" > "+st.Latest.VersionNumber)
	}
	if want := []string{"BlueMap 5.27-paper > 5.28-paper", "ViaVersion 5.11.0 > 5.12.0"}; !slices.Equal(got, want) || !sts[0].Available {
		t.Errorf("updates %q, want %q", got, want)
	}
	// One more lookup, for the files whose newest version is another
	// platform's build, and only for the server's own platform.
	var asked []string
	for _, r := range f.sentTo("modrinth", "/v2/version_files/update") {
		var body struct{ Hashes, Loaders []string }
		json.Unmarshal([]byte(r.body), &body)
		asked = append(asked, fmt.Sprintf("%d %v", len(body.Hashes), body.Loaders))
	}
	if want := []string{"2 [paper spigot bukkit]", "1 [paper]"}; !slices.Equal(asked, want) {
		t.Errorf("update lookups %q, want %q", asked, want)
	}
	p := mustPlanUpdate(t, l, srv, installed, UpdateRequest{Keys: []Key{{Modrinth, "swbUV1cr"}}})
	wantSteps(t, p, "BlueMap 5.28-paper pILlMIlN")

	quilt := newServer(t, "quilt", "26.2")
	voice := mustInstall(t, l, quilt, nil, InstallRequest{Source: Modrinth, Project: "simple-voice-chat", VersionID: "3SOh5iiX"})
	sts, err = l.CheckUpdates(ctx, quilt, voice)
	if err != nil {
		t.Fatal(err)
	}
	if len(sts) != 1 || sts[0].Latest == nil || sts[0].Latest.VersionNumber != "quilt-2.6.22+26.2" || !sts[0].Available {
		t.Errorf("voice chat on Quilt: %+v", sts)
	}
}

// Version numbers from Modrinth: BlueMap, Simple Voice Chat, Ash API,
// FastAsyncWorldEdit (the same number for both builds) and Let's Do Blooming
// Nature (two releases).
func TestBuildsOfOneRelease(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"5.28-paper", "5.28-spigot", true},
		{"quilt-2.6.22+26.2", "fabric-2.6.22+26.2", true},
		{"quilt-1.21.11-2.6.22", "fabric-1.21.11-2.6.22", true},
		{"3.0.2+1.20.1-quilt", "3.0.2+1.20.1-fabric", true},
		{"2.15.4", "2.15.4", true},
		{"5.28-paper", "5.27-spigot", false},
		{"1.0.12", "1.0.11", false},
		{"Paper", "Spigot", false},
	} {
		if same := (candidate{Number: c.a}).release() == (candidate{Number: c.b}).release(); same != c.same {
			t.Errorf("%q and %q: one release %v, want %v", c.a, c.b, same, c.same)
		}
	}
}
