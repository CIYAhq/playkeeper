package agent

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
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
