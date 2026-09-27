package share

import (
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
	"github.com/CIYAhq/playkeeper/internal/modpacks"
	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
)

type obj = map[string]any

var testNow = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

// Versions in testdata/versions.json that tests add as the user's mods.
const (
	waystones = "HUacBshD" // needs Balm, Shogi and Fabric API
	balm      = "qLWYe5p7"
	shogi     = "wZdCqlCY"
	chunky    = "4Eotm6ov"
	spark     = "e3hsPc1o"
	polymer   = "561YnR5f"
	voiceChat = "Ls232EsW"
	console   = "Yn9f9f9K" // Better Fabric Console
)

// Versions of Adrenaline's files.
const (
	sodium      = "xJZxADzI"
	fabricAPI   = "ewUK83HI"
	dynamicFPS  = "pC2JjFw1"
	modMenu     = "WdLLrOzD"
	serverCore  = "edrtnY9v"
	ferriteCore = "d5ddUdiB"
	entityCull  = "RWjup6Jf"
	clothConfig = "Nv3xnWXd"
)

// fakeModrinth answers the two requests a share makes, files by hash and
// projects by id, from the answers recorded in testdata.
type fakeModrinth struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	projects map[string]obj // by id and by slug
	versions map[string]obj // by id
	requests []string
	// hook answers instead of the fixtures when it returns true.
	hook func(w http.ResponseWriter, r *http.Request) bool
}

func newFake(t *testing.T) *fakeModrinth {
	t.Helper()
	f := &fakeModrinth{t: t, projects: map[string]obj{}, versions: map[string]obj{}}
	var ps, vs []obj
	decodeFile(t, "testdata/projects.json", &ps)
	decodeFile(t, "testdata/versions.json", &vs)
	for _, p := range ps {
		f.projects[p["id"].(string)], f.projects[p["slug"].(string)] = p, p
	}
	for _, v := range vs {
		f.versions[v["id"].(string)] = v
	}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func decodeFile(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// builder returns a Builder that reaches only the fake. Its clock stands
// still, and it never waits for a rate limit.
func (f *fakeModrinth) builder() *Builder {
	return &Builder{Modrinth: modrinth.New(fetch.Options{BaseURL: f.srv.URL + "/v2", HTTP: f.srv.Client(), Now: func() time.Time { return testNow }})}
}

func (f *fakeModrinth) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hook != nil && f.hook(w, r) {
		f.requests = append(f.requests, r.Method+" "+r.URL.Path+" (hooked)")
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v2/version_files":
		var body struct {
			Hashes    []string `json:"hashes"`
			Algorithm string   `json:"algorithm"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid_input"}`, http.StatusBadRequest)
			return
		}
		f.requests = append(f.requests, fmt.Sprintf("POST version_files %s %d", body.Algorithm, len(body.Hashes)))
		out := obj{}
		for _, h := range body.Hashes {
			if v := f.byHash(body.Algorithm, h); v != nil {
				out[h] = v
			}
		}
		writeJSON(w, out)
	case r.Method == http.MethodGet && r.URL.Path == "/v2/projects":
		var ids []string
		if err := json.Unmarshal([]byte(r.URL.Query().Get("ids")), &ids); err != nil {
			http.Error(w, `{"error":"invalid_input"}`, http.StatusBadRequest)
			return
		}
		f.requests = append(f.requests, fmt.Sprintf("GET projects %d", len(ids)))
		out := []obj{}
		for _, id := range ids {
			if p := f.projects[id]; p != nil {
				out = append(out, p)
			}
		}
		writeJSON(w, out)
	default:
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
	}
}

func (f *fakeModrinth) byHash(algo, h string) obj {
	for _, id := range slices.Sorted(maps.Keys(f.versions)) {
		v := f.versions[id]
		for _, x := range v["files"].([]any) {
			if hs := x.(obj)["hashes"].(obj); hs[algo] == strings.ToLower(h) {
				return v
			}
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (f *fakeModrinth) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// edit changes a recorded version before a test builds a share.
func (f *fakeModrinth) edit(id string, change func(v obj)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f.versions[id])
}

func (f *fakeModrinth) version(id string) modrinth.Version {
	f.t.Helper()
	var v modrinth.Version
	f.convert(f.versions[id], &v)
	return v
}

func (f *fakeModrinth) project(id string) modrinth.Project {
	f.t.Helper()
	var p modrinth.Project
	f.convert(f.projects[id], &p)
	return p
}

func (f *fakeModrinth) convert(from obj, to any) {
	f.t.Helper()
	if from == nil {
		f.t.Fatal("no such fixture")
	}
	b, err := json.Marshal(from)
	if err == nil {
		err = json.Unmarshal(b, to)
	}
	if err != nil {
		f.t.Fatal(err)
	}
}

// file is the version's file friends' launchers download.
func (f *fakeModrinth) file(id string) modrinth.File {
	f.t.Helper()
	v := f.version(id)
	file, ok := v.PrimaryFile()
	if !ok {
		f.t.Fatalf("version %s has no file", id)
	}
	return file
}

// added is the add-on library's record of a version the user added, or
// that was added for dependencyOf.
func (f *fakeModrinth) added(id, dependencyOf string) addons.Installed {
	f.t.Helper()
	v := f.version(id)
	p := f.project(v.ProjectID)
	file := f.file(id)
	var requires []string
	for _, d := range v.Dependencies {
		if d.DependencyType == modrinth.Required && d.ProjectID != "" {
			requires = append(requires, d.ProjectID)
		}
	}
	return addons.Installed{
		Source: addons.Modrinth, ProjectID: p.ID, Slug: p.Slug, Name: p.Title, VersionID: v.ID, VersionNumber: v.VersionNumber,
		Channel: v.VersionType, Published: v.DatePublished, FileName: file.Filename, HashAlgo: "sha512", Hash: file.Hashes.SHA512,
		Size: file.Size, DependencyOf: dependencyOf, Requires: requires, InstalledAt: testNow,
	}
}

// setup is a Fabric server running Adrenaline, with Waystones (and the
// Balm and Shogi it needs), Chunky, spark, Polymer, Simple Voice Chat and
// Better Fabric Console added by the user.
func (f *fakeModrinth) setup() Setup {
	f.t.Helper()
	return Setup{
		Name: "Alex's server", Type: "fabric", MinecraftVersion: "26.2", LoaderVersion: "0.19.5",
		Pack: adrenaline(f.t),
		Addons: []addons.Installed{
			f.added(waystones, ""), f.added(balm, "LOpKHB2A"), f.added(shogi, "LOpKHB2A"), f.added(chunky, ""),
			f.added(spark, ""), f.added(polymer, ""), f.added(voiceChat, ""), f.added(console, ""),
		},
	}
}

// adrenaline is the record the modpack library keeps for Adrenaline
// 26.5.0 on a server: the files on the server, and the files for players
// with their env, as the pack's index lists them.
func adrenaline(t *testing.T) *modpacks.Record {
	t.Helper()
	b, err := os.ReadFile("../testdata/packs/adrenaline/modrinth.index.json")
	if err != nil {
		t.Fatal(err)
	}
	ix, err := mrpack.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	r := &modpacks.Record{
		Pack: addons.Installed{
			Source: addons.Modrinth, ProjectID: "BYN9yKrV", Slug: "adrenaline", Name: "Adrenaline", VersionID: "EZaeTUP8",
			VersionNumber: "26.5.0+mc26.2.fabric", Channel: "release", FileName: "Adrenaline 26.5.0+mc26.2.fabric.mrpack", HashAlgo: "sha512",
		},
		Requirements: modpacks.Requirements{Type: "fabric", MinecraftVersion: "26.2", LoaderVersion: "0.19.5"},
	}
	for _, f := range ix.Files {
		project := strings.Split(f.Downloads[0], "/")[4]
		if f.Server() != mrpack.Unsupported {
			r.Files = append(r.Files, modpacks.File{Path: f.Path, HashAlgo: "sha512", Hash: f.Hashes.SHA512, Size: f.FileSize,
				Origin: modpacks.Download, Project: project, Optional: f.Server() == mrpack.Optional})
		}
		if f.Client() != mrpack.Unsupported {
			var env *mrpack.Env
			if f.Env != nil {
				e := *f.Env
				env = &e
			}
			r.Client = append(r.Client, modpacks.ClientFile{Path: f.Path, SHA1: f.Hashes.SHA1, SHA512: f.Hashes.SHA512, Size: f.FileSize,
				Origin: modpacks.Download, Env: env, Downloads: f.Downloads, Project: project})
		}
	}
	return r
}

func build(t *testing.T, b *Builder, s Setup) *Share {
	t.Helper()
	sh, err := b.Build(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	return sh
}

// wantKind checks that err is the add-on library's error of kind k.
func wantKind(t *testing.T, err error, k addons.Kind) *addons.Error {
	t.Helper()
	var e *addons.Error
	if !errors.As(err, &e) || e.Kind != k {
		t.Fatalf("got %v, want an error of kind %s", err, k)
	}
	if e.Msg == "" || !strings.HasSuffix(e.Msg, ".") {
		t.Errorf("%s: message %q is not a sentence", k, e.Msg)
	}
	return e
}

func modsByPath(ms []Mod) map[string]Mod {
	out := map[string]Mod{}
	for _, m := range ms {
		out[m.Path] = m
	}
	return out
}

func filesByPath(fs []mrpack.File) map[string]mrpack.File {
	out := map[string]mrpack.File{}
	for _, f := range fs {
		out[f.Path] = f
	}
	return out
}

func sha1hex(b []byte) string {
	s := sha1.Sum(b)
	return hex.EncodeToString(s[:])
}

func sha512hex(b []byte) string {
	s := sha512.Sum512(b)
	return hex.EncodeToString(s[:])
}
