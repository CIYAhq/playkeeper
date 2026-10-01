package software

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// The lists built into this build have every type, each with releases a
// machine can offer and, for each release, what installing it needs, all
// without asking any upstream: Mojang's server jar and the type's own
// downloads, each on a host its upstream serves from, with its hash.
func TestTheBuiltInListsInstallEveryTypeWithoutItsMetadata(t *testing.T) {
	for _, typ := range allTypes {
		rels, at, err := Sources{}.BuiltInCatalog(typ)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if at.IsZero() || at.After(time.Now()) || len(rels) == 0 {
			t.Fatalf("%s: %d releases, made at %v", typ, len(rels), at)
		}
		for _, r := range rels {
			res, rat, ok := Sources{}.BuiltInResolved(r.Pin)
			if !ok || !rat.Equal(at) || res.Java != r.Java || res.Java == 0 {
				t.Errorf("%s: nothing to install %s with (Java %d, release says %d)", typ, r.ID, res.Java, r.Java)
				continue
			}
			p, err := res.Plan()
			if err != nil {
				t.Errorf("%s: %v", r.ID, err)
				continue
			}
			for _, a := range p.Downloads {
				if !strings.HasPrefix(a.URL, "https://") || !servesFrom(a.Upstream, a.Hosts) {
					t.Errorf("%s downloads %s from %s (%v)", r.ID, a.Path, a.URL, a.Hosts)
				}
			}
			bs, bat, err := Sources{}.BuiltInBuilds(typ, r.MinecraftVersion)
			if typ != Vanilla && (err != nil || !bat.Equal(at) || !slices.ContainsFunc(bs, func(b Build) bool { return b.Pin == r.Pin })) {
				t.Errorf("%s: no builds to choose from, the one it pins among them (%v)", r.ID, err)
			}
		}
	}
	if _, _, ok := (Sources{}).BuiltInResolved(Pin{Type: Fabric, MinecraftVersion: "26.2", FabricLoader: "0.13.0"}); ok {
		t.Error("a pin the list doesn't offer isn't in it")
	}
}

// cmd/version-lists records what Catalog, Resolve and Builds found, and a
// machine reading the list back offers, installs and lets the owner choose
// a build exactly as they did.
func TestTheBuiltInListsOfferAndInstallWhatTheUpstreamsDid(t *testing.T) {
	made := time.Date(2026, 10, 1, 10, 48, 57, 0, time.UTC)
	for _, typ := range []string{Vanilla, Purpur} {
		t.Run(typ, func(t *testing.T) {
			f := catalogFake(t, typ, false, false)
			if typ == Purpur {
				servePurpurBuilds(t, f)
			}
			rec, err := f.sources().RecordBuiltIn(context.Background(), typ, made)
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(BuiltInLists{Types: map[string]BuiltInType{typ: rec}})
			if err != nil {
				t.Fatal(err)
			}
			l, err := ParseBuiltInLists(b)
			if err != nil {
				t.Fatal(err)
			}
			live, err := f.sources().Catalog(context.Background(), typ)
			if err != nil {
				t.Fatal(err)
			}
			got, at, err := l.Catalog(typ)
			if err != nil || !at.Equal(made) || !reflect.DeepEqual(got, live) {
				t.Fatalf("the list read back offers what Catalog did (%v, made %v):\n%+v\nwant %+v", err, at, got, live)
			}
			for _, r := range live {
				want, err := f.sources().Resolve(context.Background(), r.Pin)
				if err != nil {
					t.Fatal(err)
				}
				if res, _, ok := l.ResolvedFor(r.Pin); !ok || !reflect.DeepEqual(res, want) {
					t.Errorf("%s installs from the list as Resolve found it: %+v, want %+v", r.ID, res, want)
				}
				wantBuilds, err := f.sources().Builds(context.Background(), typ, r.MinecraftVersion)
				if err != nil {
					t.Fatal(err)
				}
				if bs, _, err := l.BuildsOf(typ, r.MinecraftVersion); typ != Vanilla && (err != nil || !reflect.DeepEqual(bs, wantBuilds)) {
					t.Errorf("%s's builds from the list: %+v %v, want %+v", r.ID, bs, err, wantBuilds)
				}
			}
			if _, _, err := l.Catalog(Fabric); !errors.Is(err, ErrNoBuiltInList) {
				t.Errorf("a type the list doesn't have: %v", err)
			}
		})
	}
}

// servePurpurBuilds serves the build of every release Purpur's catalog
// offers from the fixtures, as Resolve reads it.
func servePurpurBuilds(t *testing.T, f *fakeNet) {
	for mc, n := range map[string]string{"26.3": "2641", "26.2": "2633", "26.1.2": "2592", "1.21.11": "2568", "1.20.6": "2233"} {
		f.serve(purpurAPI+"/"+mc+"/"+n, editJSON(t, readFixture(t, "purpur/26.2-2633.json"), func(v any) any {
			obj(v)["version"], obj(v)["build"] = mc, n
			return v
		}))
	}
}

// The negative controls: a list a machine couldn't install from as it is,
// as a hand edit or a broken generator could leave it, is refused whole.
func TestABuiltInListAMachineCouldNotInstallFromIsRefused(t *testing.T) {
	f := catalogFake(t, Vanilla, false, false)
	rec, err := f.sources().RecordBuiltIn(context.Background(), Vanilla, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*BuiltInType){
		"a download on a host Mojang doesn't serve from": func(t *BuiltInType) {
			t.Resolved[0].Server.URL = "https://mirror.example.com/server.jar"
			t.Resolved[0].Server.Hosts = []string{"mirror.example.com"}
		},
		"a download without a hash":       func(t *BuiltInType) { t.Resolved[0].Server.Hash = Hash{} },
		"a release with nothing resolved": func(t *BuiltInType) { t.Resolved = t.Resolved[1:] },
		"two releases recommended": func(t *BuiltInType) {
			for i := range t.Releases {
				t.Releases[i].Recommended = true
			}
		},
		"a pin of another type": func(t *BuiltInType) { t.Releases[0].Pin.Type = Fabric },
		"no date":               func(t *BuiltInType) { t.MadeAt = time.Time{} },
	} {
		var bad BuiltInType
		b, _ := json.Marshal(rec)
		if err := json.Unmarshal(b, &bad); err != nil {
			t.Fatal(err)
		}
		edit(&bad)
		b, _ = json.Marshal(BuiltInLists{Types: map[string]BuiltInType{Vanilla: bad}})
		if _, err := ParseBuiltInLists(b); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	b, _ := json.Marshal(BuiltInLists{Types: map[string]BuiltInType{"spigot": rec}})
	if _, err := ParseBuiltInLists(b); err == nil {
		t.Error("a type Playkeeper doesn't install must be refused")
	}
	if !slices.ContainsFunc(rec.Resolved, func(r Resolved) bool { return r.Check() == nil }) {
		t.Fatal("the recorded list itself must be usable")
	}
}
