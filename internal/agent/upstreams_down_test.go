package agent

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup"
	"github.com/CIYAhq/playkeeper/internal/minecraft/software"
)

// The other types read their lists from their own upstreams and Mojang's,
// and install from download hosts apart from those lists for Vanilla,
// Fabric and Quilt. These tests take Mojang's version manifest down, which
// every type's list and every install reads, and still create a server.

const mojangManifest = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

// useBuiltInTypes makes the other types' lists built into Playkeeper what
// make version-lists would record from the fake upstreams now; with no
// types there are none.
func (e *agentEnv) useBuiltInTypes(made time.Time, types ...string) software.BuiltInLists {
	e.t.Helper()
	l := software.BuiltInLists{Types: map[string]software.BuiltInType{}}
	for _, typ := range types {
		rec, err := software.Sources{Client: e.up.client()}.RecordBuiltIn(context.Background(), typ, made)
		if err != nil {
			e.t.Fatal(err)
		}
		l.Types[typ] = rec
	}
	cat, res := builtInTypeCatalog, builtInTypeResolved
	e.t.Cleanup(func() { builtInTypeCatalog, builtInTypeResolved = cat, res })
	builtInTypeCatalog, builtInTypeResolved = l.Catalog, l.ResolvedFor
	return l
}

func (e *agentEnv) mojangDown() {
	e.up.handle(mojangManifest, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
}

func TestWhileMojangsVersionListIsDownAVanillaServerIsStillCreated(t *testing.T) {
	e := newAgentEnv(t)
	made := time.Date(2026, 10, 1, 10, 48, 57, 0, time.UTC)
	e.useBuiltInTypes(made, software.Vanilla)
	e.mojangDown()

	var cat api.Catalog
	e.decode("GET", "/v1/catalog?type=vanilla", &cat)
	if cat.VersionsError != "" || len(cat.Versions) != 2 || cat.Versions[0].ID != "vanilla-26.2" || !cat.VersionsCheckedAt.Equal(made) ||
		cat.VersionsFrom != "builtin" || cat.VersionsUpstream != "Mojang" {
		t.Fatalf("the versions come from the list built into Playkeeper, and say so: %+v", cat)
	}
	asked := e.up.hitCount(mojangManifest)
	e.decode("GET", "/v1/catalog?type=vanilla", &cat)
	if e.up.hitCount(mojangManifest) != asked {
		t.Fatal("Mojang is asked again only after a minute, not on every request")
	}

	e.createWith(vanilla262)
	sc, err := e.srv().serverConfig()
	if err != nil || sc.Software == nil || sc.Software.MinecraftVersion != "26.2" || sc.JarVerifiedAt == nil {
		t.Fatalf("the server runs Mojang's verified jar, downloaded from its download host: %+v %v", sc, err)
	}
	if e.up.hitCount(e.up.serverJarURL("26.2")) == 0 {
		t.Fatal("the jar comes from Mojang's download host")
	}
}

// The negative controls: without the list built into Playkeeper there are
// no versions; with the list but nothing to install them with, the install
// fails on Mojang's manifest, as every install did before.
func TestWithoutABuiltInListMojangsOutageLeavesNoVanillaServer(t *testing.T) {
	e := newAgentEnv(t)
	l := e.useBuiltInTypes(time.Now(), software.Vanilla)
	builtInTypeCatalog = func(string) ([]software.Release, time.Time, error) {
		return nil, time.Time{}, software.ErrNoBuiltInList
	}
	builtInTypeResolved = func(software.Pin) (software.Resolved, time.Time, bool) {
		return software.Resolved{}, time.Time{}, false
	}
	e.mojangDown()
	var cat api.Catalog
	e.decode("GET", "/v1/catalog?type=vanilla", &cat)
	if len(cat.Versions) != 0 || !strings.Contains(cat.VersionsError, "HTTP 503") {
		t.Fatalf("with nothing to fall back on there are no versions: %+v", cat)
	}

	builtInTypeCatalog = l.Catalog
	e.skew.Add(int64(upstreamRetry + time.Second))
	code, out := e.startCreate(vanilla262)
	if code != http.StatusAccepted {
		t.Fatalf("create: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "HTTP 503") {
		t.Fatalf("the install must fail on Mojang's manifest: %+v", op)
	}
}

// The end-to-end tests set BuiltInListsEnv, so an upstream's outage never
// fails them: the agent asks no upstream for a list or an install plan and
// uses the lists built into the build, as during an outage, while the jars
// still come from the download hosts.
func TestTheTestHarnessUsesTheBuiltInListsAndAsksNoUpstreamForThem(t *testing.T) {
	made := time.Date(2026, 10, 1, 11, 12, 27, 0, time.UTC)
	e := newAgentEnvWith(t, func(e *agentEnv) { e.tweak = func(o *Options) { o.BuiltInListsTest = true } })
	e.useBuiltInPaper(made, e.builtInVersion("26.2", "SUPPORTED", 129), e.builtInVersion("26.1.2", "UNSUPPORTED", 74))
	e.useBuiltInTypes(made, software.Vanilla)
	paperAsked, mojangAsked := e.fill.asked(), e.up.hitCount(mojangManifest)

	for _, typ := range []string{"paper", "vanilla"} {
		var cat api.Catalog
		e.decode("GET", "/v1/catalog?type="+typ, &cat)
		if len(cat.Versions) != 2 || cat.VersionsFrom != "builtin" || !cat.VersionsCheckedAt.Equal(made) {
			t.Fatalf("%s's versions come from the built-in list: %+v", typ, cat)
		}
	}
	e.createWith(map[string]any{"versionId": "paper-26.1.2"})
	e.createWith(vanilla262)
	if e.fill.asked() != paperAsked || e.up.hitCount(mojangManifest) != mojangAsked {
		t.Fatalf("no upstream is asked for a list: PaperMC %d more times, Mojang %d", e.fill.asked()-paperAsked, e.up.hitCount(mojangManifest)-mojangAsked)
	}
	if e.up.hitCount(e.up.serverJarURL("26.2")) == 0 {
		t.Fatal("the jar still comes from Mojang's download host")
	}

	// The negative control: without the switch, the same reads ask them.
	off := newAgentEnv(t)
	off.decode("GET", "/v1/catalog?type=vanilla", &api.Catalog{})
	if off.a.catalogInfo(t.Context(), ""); off.fill.asked() == 0 || off.up.hitCount(mojangManifest) == 0 {
		t.Fatal("without the switch the upstreams are asked")
	}
}

const (
	fabricGames   = "https://meta.fabricmc.net/v2/versions/game"
	fabricLoaders = "https://meta.fabricmc.net/v2/versions/loader"
)

// fabricMetaDown makes Fabric's metadata answer 503, with the build list
// built into Playkeeper Fabric Loader 0.17.2 alone.
func (e *agentEnv) fabricMetaDown() {
	e.t.Helper()
	for _, u := range []string{fabricGames, fabricLoaders} {
		e.up.handle(u, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	}
	old := builtInTypeBuilds
	e.t.Cleanup(func() { builtInTypeBuilds = old })
	builtInTypeBuilds = func(typ, mc string) ([]software.Build, time.Time, error) {
		if typ != software.Fabric {
			return nil, time.Time{}, software.ErrNoBuiltInList
		}
		return []software.Build{{Version: "0.17.2", Channel: software.Stable, Recommended: true, Pin: software.PinOf(typ, mc, "0.17.2")}}, time.Now(), nil
	}
}

// A backup or a modpack that names a build the build list this machine has
// doesn't know yet, while the type's upstream can't be asked, keeps the
// build it names: the list can't say it's no longer offered.
func TestARestoreAndAPackKeepTheBuildTheyNameWhileTheUpstreamIsDown(t *testing.T) {
	e := newAgentEnv(t)
	e.fabricMetaDown()
	rt, err := e.a.restoreTargetFor(t.Context(), backup.Manifest{Type: software.Fabric, MinecraftVersion: "26.2", Build: "0.18.0"})
	if err != nil || rt.pin.FabricLoader != "0.18.0" || rt.warning != "" {
		t.Fatalf("the backup's own loader: %+v %v", rt, err)
	}
	pt, err := e.a.packTarget(t.Context(), software.Fabric, "26.2", "0.18.0")
	if err != nil || pt.pin.FabricLoader != "0.18.0" {
		t.Fatalf("the pack's own loader: %+v %v", pt, err)
	}
	if pt, err := e.a.packTarget(t.Context(), software.Fabric, "26.2", ""); err != nil || pt.pin.FabricLoader != "0.17.2" {
		t.Fatalf("a pack that names no loader gets the list's recommended one: %+v %v", pt, err)
	}

	// The negative control: Fabric's own list, which doesn't offer 0.18.0,
	// moves the restore to the recommended loader and refuses the pack.
	live := newAgentEnv(t)
	live.up.serveFabricLists()
	rt, err = live.a.restoreTargetFor(t.Context(), backup.Manifest{Type: software.Fabric, MinecraftVersion: "26.2", Build: "0.18.0"})
	if err != nil || rt.pin.FabricLoader != "0.17.2" || !strings.Contains(rt.warning, "0.17.2") {
		t.Fatalf("with Fabric answering, the restore moves to its recommended loader: %+v %v", rt, err)
	}
	if _, err := live.a.packTarget(t.Context(), software.Fabric, "26.2", "0.18.0"); err == nil || !strings.Contains(err.Error(), "doesn't offer") {
		t.Fatalf("with Fabric answering, a loader it doesn't offer is refused: %v", err)
	}
}

// A build list whose upstream failed is served from what the machine has
// for a minute, as the version lists are, so a dead host holds up one
// build-picker load a minute, not every one.
func TestABuildListUpstreamThatFailedIsAskedAgainOnlyAfterAMinute(t *testing.T) {
	e := newAgentEnv(t)
	e.fabricMetaDown()
	ask := func() {
		t.Helper()
		if bs, _, err := e.a.typeBuilds(t.Context(), software.Fabric, "26.2"); err != nil || len(bs) != 1 {
			t.Fatalf("the built-in builds: %+v %v", bs, err)
		}
	}
	ask()
	asked := e.up.hitCount(fabricGames)
	ask()
	if n := e.up.hitCount(fabricGames); n != asked || asked == 0 {
		t.Fatalf("Fabric was asked %d times, then %d: once until a minute has passed", asked, n)
	}
	e.skew.Add(int64(upstreamRetry + time.Second))
	ask()
	if e.up.hitCount(fabricGames) == asked {
		t.Fatal("a minute on, Fabric is asked again")
	}
}

// What a machine resolved for a version is kept, so it installs that
// version again while the upstream's metadata is down, with no list built
// into Playkeeper: a reinstall, here.
func TestAVersionResolvedBeforeInstallsAgainWhileItsUpstreamIsDown(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(vanilla262)
	e.useBuiltInTypes(time.Now())
	e.mojangDown()
	reinstall := func() *api.Operation {
		t.Helper()
		code, out := e.callWhenFree("POST", e.sp("/software/reinstall"), map[string]any{"actor": "admin"})
		if code != http.StatusAccepted {
			t.Fatalf("reinstall: %d %v", code, out)
		}
		return e.waitOp(out["id"].(string))
	}
	if op := reinstall(); op.Status != api.OpSucceeded {
		t.Fatalf("the kept install plan reinstalls the server: %+v", op)
	}

	// The negative control: without the kept plan, the reinstall fails on
	// Mojang's manifest.
	path, ok := e.a.resolvedPath(software.Pin{Type: software.Vanilla, MinecraftVersion: "26.2"})
	if !ok {
		t.Fatal("no path for the kept plan")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if op := reinstall(); op.Status != api.OpFailed || !strings.Contains(op.Error, "HTTP 503") {
		t.Fatalf("without the kept plan the reinstall needs Mojang: %+v", op)
	}
}
