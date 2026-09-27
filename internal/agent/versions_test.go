package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/sizing"
)

func (e *agentEnv) changeVersion(body map[string]any) (int, map[string]any) {
	e.t.Helper()
	body["actor"] = "admin"
	return e.call("POST", e.sp("/version"), body)
}

func TestCatalogIsLiveFromPaperMCAndExperimentalNeedsConsent(t *testing.T) {
	e := newAgentEnv(t)
	cat := e.a.catalogInfo(t.Context(), "")
	if cat.VersionsError != "" || len(cat.Versions) != 3 {
		t.Fatalf("catalog: %+v", cat)
	}
	if v := cat.Versions[0]; v.ID != "paper-26.3" || !v.Experimental || v.Recommended {
		t.Fatalf("26.3 has only alpha builds and must be offered as experimental: %+v", v)
	}
	if v := cat.Versions[1]; v.ID != "paper-26.2" || !v.Recommended || v.PaperBuild != 129 {
		t.Fatalf("26.2 is the latest stable version and must be recommended: %+v", v)
	}
	req := map[string]any{"acceptEula": true, "versionId": "paper-26.3", "memoryMB": 1536, "actor": "admin"}
	if code, out := e.call("POST", "/v1/servers", req); code != 400 || !strings.Contains(out["error"].(string), "experimental") {
		t.Fatalf("an experimental version must not be created without consent: %d %v", code, out)
	}
	if n := len(e.a.serverList()); n != 0 {
		t.Fatal("nothing may be created")
	}
	req["acceptExperimental"] = true
	code, out := e.startCreate(req)
	if code != 202 {
		t.Fatalf("with consent it is created: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("create: %+v", op)
	}
	sc, _ := e.srv().serverConfig()
	if sc.MinecraftVersion != "26.3" || sc.PaperBuild != 41 || len(sc.JarSHA256) != 64 {
		t.Fatalf("the build and its checksum must be pinned: %+v", sc)
	}

	down := newAgentEnv(t)
	down.fill.set("", []fillVersionSpec{})
	if cat := down.a.catalogInfo(t.Context(), ""); cat.VersionsError == "" || len(cat.Versions) != 0 {
		t.Fatalf("an empty list from PaperMC must be reported: %+v", cat)
	}
}

// The catalog sizes memory for what the server runs, as the sizing guide
// does: an existing server by the plugins or mods in its folder, a new one
// from a pack or template by the mods or plugins it brings, and any other
// new one by its type.
func TestTheCatalogSizesMemoryForWhatTheServerRuns(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string // for a new server; "" for the existing one
		typ   string // the existing server's type
		jars  int    // plugins or mods in its folder
		want  sizing.Workload
	}{
		{"a new vanilla server", "type=vanilla", "", 0, sizing.Vanilla},
		{"a new Paper server", "type=paper", "", 0, sizing.Vanilla},
		{"a Paper server with a few plugins", "", api.TypePaper, 3, sizing.Vanilla},
		{"a Paper server with many plugins", "", api.TypePaper, 25, sizing.AddOns},
		{"a new Fabric server", "type=fabric", "", 0, sizing.AddOns},
		{"a Fabric server with mods", "", "fabric", 8, sizing.AddOns},
		{"a NeoForge server with mods", "", "neoforge", 12, sizing.AddOns},
		{"a new server from a pack of 180 mods", "type=neoforge&mods=180", "", 0, sizing.Modpack},
		{"a new server from a pack that doesn't say its mods", "type=fabric", "", 0, sizing.AddOns},
		{"a new server from a Paper template with 3 plugins", "type=paper&plugins=3", "", 0, sizing.Vanilla},
		{"a new server from a Paper template with 30 plugins", "type=paper&plugins=30", "", 0, sizing.AddOns},
		{"a new server from a Fabric template with 5 mods", "type=fabric&mods=5", "", 0, sizing.AddOns},
		{"a new server from a Fabric template with 60 mods", "type=fabric&mods=60", "", 0, sizing.Modpack},
		{"a server made from a modpack", "", "fabric", 60, sizing.Modpack},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgentEnv(t)
			path := "/v1/catalog?" + tc.query
			if tc.query == "" {
				e.addIdleServer()
				s := e.srv()
				sc, _ := s.serverConfig()
				sc.Type = tc.typ
				if err := s.saveServerConfig(*sc); err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(s.dataDir(), addonDir(*sc))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				for i := range tc.jars {
					if err := os.WriteFile(filepath.Join(dir, "addon-"+strconv.Itoa(i)+".jar"), []byte("jar"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				path = "/v1/catalog?server=" + e.sid
			}
			var cat api.Catalog
			e.decode("GET", path, &cat)
			if cat.Sizing.Workload != string(tc.want) || len(cat.Sizing.Budgets) != len(cat.MemoryOptionsMB) || len(cat.MemoryOptionsMB) == 0 {
				t.Fatalf("the catalog sizes for %q with %v, want %q", cat.Sizing.Workload, cat.Sizing.Budgets, tc.want)
			}
			for _, b := range cat.Sizing.Budgets {
				if players, err := sizing.PlayersFor(tc.want, b.MemoryMB); err != nil || b.Players != players {
					t.Fatalf("%d MB is for %d players, want the guide's %d for %s", b.MemoryMB, b.Players, players, tc.want)
				}
			}
			if want := memorySizing(tc.want, cat.MemoryOptionsMB).Suggestions; !reflect.DeepEqual(cat.Sizing.Suggestions, want) {
				t.Fatalf("suggestions %v, want %v", cat.Sizing.Suggestions, want)
			}
		})
	}
	e := newAgentEnv(t)
	for _, q := range []string{"type=fabric&mods=lots", "type=paper&plugins=-1"} {
		if code, out := e.call("GET", "/v1/catalog?"+q, nil); code != 400 {
			t.Fatalf("%s: a number of add-ons that isn't a whole number: %d %v", q, code, out)
		}
	}
}

func TestTheCatalogSaysWhatTheSizingGuideSaysAboutEachMemoryOption(t *testing.T) {
	e := newAgentEnv(t)
	var cat api.Catalog
	e.decode("GET", "/v1/catalog", &cat)
	want := api.MemorySizing{
		Workload: "vanilla",
		Budgets: []api.MemoryBudget{
			{MemoryMB: 1536, HeapMB: 1024, Players: 0}, {MemoryMB: 2048, HeapMB: 1536, Players: 4},
			{MemoryMB: 3072, HeapMB: 2304, Players: 4},
		},
		Suggestions: []api.MemorySuggestion{
			{Players: 4, MemoryMB: 2048}, {Players: 10, MemoryMB: 4096},
			{Players: 20, MemoryMB: 6144}, {Players: 40, MemoryMB: 8192},
		},
	}
	if !reflect.DeepEqual(cat.MemoryOptionsMB, []int{1536, 2048, 3072}) || !reflect.DeepEqual(cat.Sizing, want) {
		t.Fatalf("a 4 GB machine's options and what the guide says about them:\n%v\n%+v\nwant %+v", cat.MemoryOptionsMB, cat.Sizing, want)
	}

	got := memorySizing(sizing.Vanilla, []int{1536, 2048, 3072, 4096, 6144})
	var players []int
	for _, b := range got.Budgets {
		players = append(players, b.Players)
	}
	if !reflect.DeepEqual(players, []int{0, 4, 4, 10, 20}) || got.Budgets[3].HeapMB != 3072 {
		t.Fatalf("4 GB is for up to 10 players and gives Java 3 GB: %+v", got.Budgets)
	}
	for _, b := range memorySizing(sizing.Modpack, []int{1536, 4096, 6144}).Budgets {
		if b.Players != 0 {
			t.Fatalf("no option below 8 GB fits a modpack: %+v", b)
		}
	}
	if none := memorySizing("minigames", []int{1536}); none.Budgets == nil || none.Suggestions == nil || len(none.Budgets)+len(none.Suggestions) != 0 {
		t.Fatalf("a workload the guide doesn't know gets no advice, as empty lists: %+v", none)
	}
}

func TestVersionChangesNeverGoBack(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	for _, tc := range []struct {
		body map[string]any
		code int
		want string
	}{
		{map[string]any{"versionId": "paper-26.1.2"}, 409, "not newer"},
		{map[string]any{"versionId": "paper-26.3"}, 400, "experimental"},
		{map[string]any{"versionId": "latest"}, 400, "Unknown server version"},
		{map[string]any{"versionId": "paper-26.2", "extra": 1}, 400, "unknown field"},
	} {
		if code, out := e.changeVersion(tc.body); code != tc.code || !strings.Contains(out["error"].(string), tc.want) {
			t.Errorf("%v: want %d %q, got %d %v", tc.body, tc.code, tc.want, code, out)
		}
	}
	if sc, _ := e.srv().serverConfig(); sc.MinecraftVersion != "26.1.2" {
		t.Fatalf("a refused change must not touch the server: %+v", sc)
	}
	sc, _ := e.srv().serverConfig()
	for _, target := range []api.CatalogEntry{{MinecraftVersion: "1.21.11", PaperBuild: 132}, {MinecraftVersion: "26.1.2", PaperBuild: 70}} {
		err := checkNewer(*sc, target)
		if err == nil || (target.MinecraftVersion == "1.21.11" && !strings.Contains(err.Error(), "cannot go back from 26.1.2 to 1.21.11")) {
			t.Errorf("going to %s build %d must be refused: %v", target.MinecraftVersion, target.PaperBuild, err)
		}
	}
	if err := checkNewer(*sc, api.CatalogEntry{MinecraftVersion: "26.1.2", PaperBuild: 80}); err != nil {
		t.Errorf("a newer build of the same version is an update: %v", err)
	}
}

func TestNewerStableMatchesTheDashboard(t *testing.T) {
	versions := []api.CatalogEntry{
		{MinecraftVersion: "26.3", PaperBuild: 10, Experimental: true, Supported: true},
		{MinecraftVersion: "26.2.1", PaperBuild: 41, Supported: true},
		{MinecraftVersion: "26.2", PaperBuild: 10, Supported: true},
		{MinecraftVersion: "26.1.2", PaperBuild: 80, Supported: true},
		{MinecraftVersion: "26.1.2", PaperBuild: 60, Supported: true},
		{MinecraftVersion: "1.21.11", PaperBuild: 10, Supported: true},
	}
	vanilla := func(v string) api.CatalogEntry {
		return api.CatalogEntry{MinecraftVersion: v, Supported: true, Software: &api.SoftwarePin{Type: "vanilla", MinecraftVersion: v}}
	}
	mixed := []api.CatalogEntry{vanilla("26.3"), {MinecraftVersion: "26.2", PaperBuild: 10, Supported: true}}
	for _, c := range []struct {
		typ, cur string
		versions []api.CatalogEntry
		want     string
	}{
		{"", "26.1.2", versions, "26.2.1#41"},
		{"", "26.2", versions, "26.2.1#41"},
		{"", "26.2.1", versions, ""},
		{"", "26.3", versions, ""},
		{"", "26.1.2", []api.CatalogEntry{{MinecraftVersion: "26.2", PaperBuild: 10, Supported: true}, {MinecraftVersion: "26.2", PaperBuild: 12, Supported: true}}, "26.2#12"},
		{"", "26.1.2", []api.CatalogEntry{{MinecraftVersion: "26.2", PaperBuild: 10}}, ""},
		{"", "26.1.2", mixed, "26.2#10"},
		{"vanilla", "26.1.2", mixed, "26.3#0"},
	} {
		cur := api.ServerConfig{MinecraftVersion: c.cur, PaperBuild: 74}
		if c.typ != "" {
			cur.Type, cur.PaperBuild, cur.Software = c.typ, 0, &api.SoftwarePin{Type: c.typ, MinecraftVersion: c.cur}
		}
		got := ""
		if e, ok := newerStable(cur, c.versions); ok {
			got = e.MinecraftVersion + "#" + strconv.Itoa(e.PaperBuild)
		}
		if got != c.want {
			t.Errorf("%s from %s: got %q, want %q", nonEmptyOr(c.typ, "paper"), c.cur, got, c.want)
		}
	}
}

func TestVersionChangeBacksUpFirstAndStartsTheNewVersion(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	code, out := e.changeVersion(map[string]any{"versionId": "paper-26.2"})
	if code != 202 {
		t.Fatalf("change: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded {
		t.Fatalf("op: %+v", op)
	}
	backups, _ := e.srv().listBackups(`kind = 'rollback'`)
	if len(backups) != 1 || backups[0].Verified == nil || !*backups[0].Verified || !strings.Contains(backups[0].Note, "before updating from Paper 26.1.2 to 26.2") || op.Detail["backupId"] != backups[0].ID {
		t.Fatalf("a verified backup must be taken first: %+v", backups)
	}
	sc, _ := e.srv().serverConfig()
	if sc.MinecraftVersion != "26.2" || sc.PaperBuild != 129 || sc.JarSHA256 == "" || sc.JarVerifiedAt == nil {
		t.Fatalf("config: %+v", sc)
	}
	e.waitFor("online on 26.2", func() bool { return e.status().Phase == api.PhaseOnline })
	e.fd.mu.Lock()
	jar := env(e.fd.byName[e.cname()].cfg, "CUSTOM_SERVER")
	e.fd.mu.Unlock()
	if jar != "/data/paper-26.2-129.jar" {
		t.Fatalf("the server must run the new jar: %s", jar)
	}
	if code, out := e.changeVersion(map[string]any{"versionId": "paper-26.1.2"}); code != 409 || !strings.Contains(out["error"].(string), "cannot go back from 26.2 to 26.1.2") {
		t.Fatalf("after the update, the old version is refused: %d %v", code, out)
	}
}

func TestVersionThatDoesNotStartPutsTheWorldBack(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	level := filepath.Join(e.dataDir(), "world", "level.dat")
	before, err := os.ReadFile(level)
	if err != nil {
		t.Fatal(err)
	}
	e.fd.mu.Lock()
	e.fd.bootFailsOn = "26.2"
	e.fd.mu.Unlock()
	code, out := e.changeVersion(map[string]any{"versionId": "paper-26.2"})
	if code != 202 {
		t.Fatalf("change: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "put the backup from before the update back") || !strings.Contains(op.Error, "runs 26.1.2 again") {
		t.Fatalf("op: %+v", op)
	}
	if after, _ := os.ReadFile(level); string(after) != string(before) {
		t.Fatalf("the world the failed start upgraded must be replaced by the backup: %q, want %q", after, before)
	}
	sc, _ := e.srv().serverConfig()
	if sc.MinecraftVersion != "26.1.2" || sc.PaperBuild != 74 {
		t.Fatalf("the previous version must be configured again: %+v", sc)
	}
	e.waitFor("online on 26.1.2", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
	matches, _ := filepath.Glob(e.dataDir() + ".failed-update-*")
	if len(matches) != 0 {
		t.Fatalf("the upgraded copy must be removed once the backup is back: %v", matches)
	}
}

func TestServersFrom010KeepTheirPinnedChecksum(t *testing.T) {
	if sum, err := jarChecksum(api.ServerConfig{MinecraftVersion: "26.1.2", PaperBuild: 74}); err != nil || len(sum) != 64 {
		t.Fatalf("a 0.1.0 server must still verify its jar: %q %v", sum, err)
	}
	if _, err := jarChecksum(api.ServerConfig{MinecraftVersion: "26.2", PaperBuild: 129}); err == nil {
		t.Fatal("a build without a recorded or known checksum must not run")
	}
}
