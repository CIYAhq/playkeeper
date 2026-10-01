package minecraft

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The list built into this build offers what a machine with no list of its
// own needs to create a server while PaperMC's API is down: stable
// versions, newest first and the newest recommended, each with a jar
// checksum and an address on PaperMC's download host.
func TestTheBuiltInPaperListOffersStableVersionsToDownloadByChecksum(t *testing.T) {
	entries, madeAt, err := BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if madeAt.IsZero() || madeAt.After(time.Now()) {
		t.Fatalf("the list says when it was made, in the past: %v", madeAt)
	}
	if len(entries) < 2 || !entries[0].Recommended {
		t.Fatalf("the newest stable version comes first, recommended: %+v", entries)
	}
	for i, e := range entries {
		if CompareMinecraft(e.MinecraftVersion, OldestRelease) < 0 || e.Java > ImageJava || e.Java == 0 {
			t.Errorf("%s isn't a version this Playkeeper runs (Java %d)", e.MinecraftVersion, e.Java)
		}
		if i > 0 && CompareMinecraft(entries[i-1].MinecraftVersion, e.MinecraftVersion) <= 0 {
			t.Errorf("%s is listed after %s", e.MinecraftVersion, entries[i-1].MinecraftVersion)
		}
		u, ok := PaperJarURL(e.MinecraftVersion, e.PaperBuild, e.JarSHA256)
		if !ok || !strings.HasPrefix(u, PaperDownloads+"/v1/objects/"+e.JarSHA256+"/") || e.ID != VersionID(e.MinecraftVersion) || e.Notes == "" {
			t.Errorf("%s build %d can't be downloaded by its checksum: %q %+v", e.MinecraftVersion, e.PaperBuild, u, e)
		}
	}
	for _, k := range knownBuilds {
		e, _, err := BuiltInRestoreBuild(k.mc, k.build)
		if err != nil || e.PaperBuild < k.build || (e.PaperBuild == k.build && e.JarSHA256 != k.sha256) {
			t.Errorf("a backup of Paper %s build %d restores from the list: %+v %v", k.mc, k.build, e, err)
		}
	}
}

// cmd/version-lists keeps each version's newest stable build and its newest
// build when that is newer, so the list offers and restores what the Fill
// API did when the list was made.
func TestTheBuiltInListOffersAndRestoresWhatFillDid(t *testing.T) {
	f, fill := startFakeFill(t, fillToday)
	made := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	l, err := fill.BuiltInList(context.Background(), made)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, v := range l.Versions {
		for _, b := range v.Builds {
			kept = append(kept, fmt.Sprintf("%s#%d", v.ID, b.ID))
		}
	}
	if want := "26.3#41 26.2#130 26.2#129 26.1.2#74 26.1.1#20 1.21.11#132 1.21.10#50 1.20.6#151 1.20.1#196"; strings.Join(kept, " ") != want {
		t.Fatalf("kept %v, want %s", kept, want)
	}
	live, err := fill.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	read, err := ParseBuiltInPaper(b)
	if err != nil {
		t.Fatal(err)
	}
	offered, at, err := read.Catalog()
	if err != nil || !at.Equal(made) || !reflect.DeepEqual(offered, live) {
		t.Fatalf("the list read back must offer what Fill did (%v, made %v):\n%+v\nwant %+v", err, at, offered, live)
	}
	for _, tc := range []struct {
		mc    string
		build int
	}{{"26.2", 100}, {"26.3", 41}, {"1.20.1", 195}, {"1.21.10", 50}} {
		want, err := fill.RestoreBuild(context.Background(), tc.mc, tc.build)
		if err != nil {
			t.Fatal(err)
		}
		if got, _, err := read.RestoreBuild(tc.mc, tc.build); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("restoring %s build %d from the list: %+v %v, want %+v", tc.mc, tc.build, got, err, want)
		}
	}
	for _, tc := range []struct {
		mc    string
		build int
	}{{"26.3", 40}, {"1.19.4", 550}, {"9.9", 1}} {
		if _, _, err := read.RestoreBuild(tc.mc, tc.build); err == nil {
			t.Errorf("the list has no build of %s to restore build %d with", tc.mc, tc.build)
		}
	}

	f.mu.Lock()
	f.jarHost = "mirror.example.com"
	f.mu.Unlock()
	if _, err := fill.BuiltInList(context.Background(), made); err == nil || !strings.Contains(err.Error(), "PaperJarURL needs updating") {
		t.Fatalf("a jar PaperMC serves elsewhere must stop the list being made, or machines couldn't download it while the API is down: %v", err)
	}
}

// PaperJarURL builds the address the Fill API gave for Paper 26.2 build
// 112 (as recorded on 13 Aug 2026), and nothing for what isn't a build.
func TestPaperJarURLIsWherePaperMCServesTheJar(t *testing.T) {
	sum := "bd3a58cf96874e5ea6643f5f6fe9b4f5bf9e34b795fa078c2f0ee8b98b2f907e"
	if u, ok := PaperJarURL("26.2", 112, sum); !ok || u != "https://fill-data.papermc.io/v1/objects/"+sum+"/paper-26.2-112.jar" {
		t.Fatalf("got %q", u)
	}
	for _, tc := range []struct {
		mc    string
		build int
		sum   string
	}{{"26.2", 0, sum}, {"26.2-rc-1", 3, sum}, {"26.2", 112, "not-a-checksum"}, {"../26.2", 112, sum}} {
		if u, ok := PaperJarURL(tc.mc, tc.build, tc.sum); ok {
			t.Errorf("%+v must have no address: %q", tc, u)
		}
	}
}

func TestABuiltInListWithNothingInItIsRefused(t *testing.T) {
	for _, b := range []string{``, `{}`, `{"madeAt":"2026-10-01T00:00:00Z","versions":[]}`, `{"versions":[{"id":"26.2"}]}`} {
		if _, err := ParseBuiltInPaper([]byte(b)); err == nil {
			t.Errorf("%q must be refused", b)
		}
	}
}
