package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons/firstparty"
	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/templates"
	"github.com/CIYAhq/playkeeper/internal/templates/checks"
)

// fakeAgent answers the calls a check makes, as a Playkeeper agent does.
type fakeAgent struct {
	mu          sync.Mutex
	ready       bool
	createFails bool
	slowDowns   int
	missing     string
	log         []string
	servers     []api.ServerStatus
	calls       []string
	deleted     []api.DeleteServerRequest
	exported    string
	// crossplay is what the server says of crossplay, nil on a release
	// without it; turning it on adds crossplayLog to the log.
	crossplay    *api.Crossplay
	crossplayLog []string
	// crossplaySlowDown makes the first crossplay operation fail with a
	// reason that passes, after crossplay went on.
	crossplaySlowDown bool
	// more are add-on files the server lists after its usual three.
	more []api.AddonFile
}

func (f *fakeAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch r.Method + " " + r.URL.Path {
	case "GET /v1/update":
		reply(api.UpdateInfo{Current: "0.4.2"})
	case "POST /v1/templates/plan":
		if ct := r.Header.Get("Content-Type"); ct != "text/plain" {
			http.Error(w, `{"error":"text/plain please"}`, http.StatusBadRequest)
			return
		}
		p := api.TemplatePlan{Ready: f.ready, Fingerprint: "fp-1", MemoryMB: 4096, Type: "paper", Build: "129"}
		if !f.ready {
			p.Blockers = []api.AddonNotice{{Kind: "modpack_unavailable", Message: "Minecraft 1.21.1 can't be created here."}}
		}
		reply(p)
	case "GET /v1/servers":
		reply(f.servers)
	case "POST /v1/servers":
		var req api.CreateServerRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !req.AcceptEULA || req.Template == nil || req.Template.Fingerprint != "fp-1" || req.MemoryMB != 4096 {
			http.Error(w, `{"error":"bad create"}`, http.StatusBadRequest)
			return
		}
		f.servers = append(f.servers, api.ServerStatus{ID: "s1", Name: req.Name})
		reply(api.Operation{ID: "create"})
	case "GET /v1/operations/create":
		if f.slowDowns > 0 {
			f.slowDowns--
			reply(api.Operation{ID: "create", Status: "failed", ServerID: "s1", Error: "Modrinth asked Playkeeper to slow down."})
			return
		}
		if f.createFails {
			reply(api.Operation{ID: "create", Status: "failed", ServerID: "s1", Error: "The server stopped while starting (exit code 1)."})
			return
		}
		reply(api.Operation{ID: "create", Status: "succeeded", ServerID: "s1"})
	case "POST /v1/servers/s1/start":
		reply(api.Operation{ID: "start"})
	case "GET /v1/servers/s1":
		reply(api.ServerStatus{ID: "s1", Phase: api.PhaseOnline})
	case "GET /v1/servers/s1/logs":
		// The agent keeps a server's newest 2,000 lines and answers a limit
		// it can't meet with its newest 500.
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 2000 {
			limit = 500
		}
		var lines []api.LogLine
		for _, l := range f.log[max(0, len(f.log)-limit):] {
			lines = append(lines, api.LogLine{Text: l})
		}
		reply(api.LogsResponse{Lines: lines})
	case "GET /v1/servers/s1/addons":
		files := []api.AddonFile{
			{FileName: "Chunky.jar", Addon: &api.Addon{Source: "modrinth", ProjectID: "fALzjamp", Slug: "chunky", Name: "Chunky", VersionNumber: "1.5.3"}},
			{FileName: "CoreProtect.jar", Addon: &api.Addon{Source: "modrinth", ProjectID: "Lu3KuzdV", Slug: "coreprotect", Name: "CoreProtect", VersionNumber: "24.1"}},
			{FileName: "Dep.jar", Addon: &api.Addon{Source: "modrinth", ProjectID: "P8hQ7pUj", Slug: "dep", Name: "A dependency", VersionNumber: "2.0", DependencyOf: "Lu3KuzdV"}},
		}
		files = append(files, f.more...)
		if f.missing != "" {
			files = slices.DeleteFunc(files, func(a api.AddonFile) bool { return a.Addon.Slug == f.missing })
		}
		reply(api.Addons{Files: files})
	case "GET /v1/servers/s1/template":
		reply(api.TemplateExport{File: f.exported})
	case "GET /v1/modpacks/curseforge/715572":
		reply(api.ModpackDetail{ModpackCard: api.ModpackCard{Downloads: 13639405}})
	case "GET /v1/servers/s1/crossplay":
		if f.crossplay == nil {
			http.Error(w, `{"error":"no such call"}`, http.StatusNotFound)
			return
		}
		reply(f.crossplay)
	case "POST /v1/servers/s1/crossplay":
		var req api.CrossplayRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if f.crossplay == nil || !f.crossplay.Available || !req.On || req.Actor == "" {
			http.Error(w, `{"error":"bad crossplay"}`, http.StatusBadRequest)
			return
		}
		f.crossplay.On, f.crossplay.Available = true, false
		f.log = append(f.log, f.crossplayLog...)
		reply(api.Operation{ID: "crossplay"})
	case "GET /v1/operations/crossplay":
		if f.crossplaySlowDown {
			f.crossplaySlowDown = false
			reply(api.Operation{ID: "crossplay", Status: "failed", Error: "Hangar asked Playkeeper to slow down."})
			return
		}
		reply(api.Operation{ID: "crossplay", Status: "succeeded"})
	case "POST /v1/servers/s1/stop":
		reply(api.Operation{ID: "stop"})
	case "GET /v1/operations/stop", "GET /v1/operations/delete":
		reply(api.Operation{Status: "succeeded"})
	case "POST /v1/servers/s1/delete":
		var req api.DeleteServerRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.deleted = append(f.deleted, req)
		reply(api.Operation{ID: "delete"})
	default:
		http.Error(w, `{"error":"no such call"}`, http.StatusNotFound)
	}
}

// through sends the client's requests to a handler, as the socket would.
type through struct{ h http.Handler }

func (t through) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.h.ServeHTTP(w, r)
	return w.Result(), nil
}

const survival = `{
  "playkeeperTemplate": 1,
  "name": "Survival",
  "game": "minecraft-java",
  "server": { "type": "paper", "minecraftVersion": "26.2" },
  "settings": {
    "memoryMB": 4096
  },
  "addons": [
    { "source": "modrinth", "project": "fALzjamp", "slug": "chunky", "name": "Chunky", "latest": true },
    { "source": "modrinth", "project": "Lu3KuzdV", "slug": "coreprotect", "name": "CoreProtect", "latest": true }
  ]
}
`

const curseforgePack = `{
  "playkeeperTemplate": 1,
  "name": "All the Mods 9",
  "game": "minecraft-java",
  "server": { "type": "forge", "minecraftVersion": "1.20.1" },
  "settings": {
    "memoryMB": 4096
  },
  "modpack": {
    "source": "curseforge",
    "project": "715572",
    "slug": "all-the-mods-9",
    "name": "All the Mods 9 - ATM9",
    "pin": {
      "versionId": "7097953",
      "versionNumber": "1.1.1",
      "channel": "release",
      "hashAlgo": "sha1",
      "hash": "bcf232e85b4d200b3e65bc02facd98a179b685dd"
    }
  }
}
`

// A CurseForge pack's downloads come from the release, which has
// CurseForge's key: the checker's own sources can't ask CurseForge.
func TestACurseForgePackGetsItsDownloadsFromTheRelease(t *testing.T) {
	tpl, err := templates.Decode([]byte(curseforgePack))
	if err != nil {
		t.Fatal(err)
	}
	f := &templateFile{path: filepath.Join(t.TempDir(), "atm9.json"), raw: []byte(curseforgePack), t: tpl}
	agent := &fakeAgent{ready: true, log: []string{"[Server thread/INFO]: Done (37.81s)! For help, type \"help\""}}
	r := newTestChecker(t, agent).check(context.Background(), "atm9", f, false, false)
	if r.Status != statusPassing || r.Check.Modpack == nil || r.Check.Modpack.Downloads != 13639405 {
		t.Fatalf("got %s %q, modpack %+v", r.Status, r.Failure, r.Check.Modpack)
	}
}

// FTB StoneBlock 4 logs over 500 lines in the second after it says Done, as
// its mods look through loot tables, and its check still reads the Done line.
func TestACheckFindsDoneBeforeABigModpacksBurstOfLines(t *testing.T) {
	tpl, err := templates.Decode([]byte(curseforgePack))
	if err != nil {
		t.Fatal(err)
	}
	f := &templateFile{path: filepath.Join(t.TempDir(), "atm9.json"), raw: []byte(curseforgePack), t: tpl}
	log := []string{"[Server thread/INFO]: Done (8.12s)! For help, type \"help\""}
	for range 1200 {
		log = append(log, "[Server thread/WARN] [ali]: Loot table twilightforest:entities/naga belongs to no entity by its id")
	}
	agent := &fakeAgent{ready: true, log: log}
	r := newTestChecker(t, agent).check(context.Background(), "atm9", f, false, false)
	if r.Status != statusPassing || r.Check.DoneSeconds != 8.12 {
		t.Fatalf("got %s %q, check %+v", r.Status, r.Failure, r.Check)
	}
}

func pinnedExport(t *testing.T) string {
	t.Helper()
	tpl, err := templates.Decode([]byte(survival))
	if err != nil {
		t.Fatal(err)
	}
	tpl.Name = "check-survival"
	for i := range tpl.Addons {
		tpl.Addons[i].Latest = false
		tpl.Addons[i].Pin = &templates.Pin{VersionID: "v" + tpl.Addons[i].Slug, VersionNumber: "1.0", HashAlgo: "sha512", Hash: strings.Repeat("a", 128)}
	}
	tpl.Addons = append(tpl.Addons, templates.Addon{Source: "modrinth", Project: "P8hQ7pUj", Slug: "dep", Name: "A dependency", DependencyOf: "Lu3KuzdV",
		Pin: &templates.Pin{VersionID: "vdep", VersionNumber: "2.0", HashAlgo: "sha512", Hash: strings.Repeat("b", 128)}})
	b, _ := json.Marshal(tpl)
	return string(b)
}

func newTestChecker(t *testing.T, agent *fakeAgent) *checker {
	t.Helper()
	modrinth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("User-Agent"), "CIYAhq/playkeeper") {
			http.Error(w, "say who you are", http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, `{"downloads": 1234, "license": {"id": "GPL-3.0-only"}}`)
	}))
	t.Cleanup(modrinth.Close)
	s := newSources()
	s.modrinth = modrinth.URL
	return &checker{agent: agentclient.Via(through{agent}), sources: s, actor: "template-check", now: time.Now, poll: time.Millisecond, retryAfter: time.Millisecond, release: "0.4.2"}
}

func survivalFile(t *testing.T) *templateFile {
	t.Helper()
	tpl, err := templates.Decode([]byte(survival))
	if err != nil {
		t.Fatal(err)
	}
	return &templateFile{path: filepath.Join(t.TempDir(), "survival.json"), raw: []byte(survival), t: tpl}
}

// A template passes when its server reaches Done with its add-ons
// installed: the check records the versions, the build and the seconds, and
// removes the server and its key.
func TestACheckRecordsWhatInstalled(t *testing.T) {
	agent := &fakeAgent{ready: true, log: []string{"[Server thread/INFO]: Enabling Chunky v1.5.3", "[Server thread/INFO]: Done (12.51s)! For help, type \"help\""}}
	r := newTestChecker(t, agent).check(context.Background(), "survival", survivalFile(t), false, false)
	if r.Status != statusPassing {
		t.Fatalf("got %s: %s", r.Status, r.Failure)
	}
	want := &checks.Check{Status: checks.Passing, Checked: time.Now().UTC().Format(time.DateOnly), Release: "0.4.2", Build: "129", DoneSeconds: 12.51, Addons: []checks.Addon{
		{Name: "Chunky", Source: "modrinth", Slug: "chunky", Version: "1.5.3", Licence: "GPL-3.0-only", Downloads: 1234},
		{Name: "CoreProtect", Source: "modrinth", Slug: "coreprotect", Version: "24.1", Licence: "GPL-3.0-only", Downloads: 1234},
	}}
	got, _ := json.Marshal(r.Check)
	if w, _ := json.Marshal(want); string(got) != string(w) {
		t.Errorf("recorded %s\nwant     %s", got, w)
	}
	if len(agent.deleted) != 1 || !agent.deleted[0].ForgetKey || agent.deleted[0].Confirm != "check-survival" {
		t.Errorf("the server wasn't removed with its key: %+v", agent.deleted)
	}
}

const aiBuildBattle = `{
  "playkeeperTemplate": 1,
  "name": "AI Build Battle",
  "game": "minecraft-java",
  "server": { "type": "paper", "minecraftVersion": "1.21.8" },
  "settings": {
    "memoryMB": 4096
  },
  "addons": [
    { "source": "playkeeper", "project": "ai-build-battle", "slug": "ai-build-battle", "name": "AI Build Battle", "latest": true }
  ]
}
`

// One of Playkeeper's own plugins is recorded with the licence its registry
// gives and no downloads, since nothing counts them, and no source is asked
// about it.
func TestPlaykeepersOwnPluginIsRecordedFromItsRegistry(t *testing.T) {
	fp := firstparty.Lookup("ai-build-battle")
	jar, err := fp.Jar()
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := templates.Decode([]byte(aiBuildBattle))
	if err != nil {
		t.Fatal(err)
	}
	f := &templateFile{path: filepath.Join(t.TempDir(), "ai-build-battle.json"), raw: []byte(aiBuildBattle), t: tpl}
	agent := &fakeAgent{ready: true, log: []string{"[Server thread/INFO]: Done (9.02s)! For help, type \"help\""}, more: []api.AddonFile{
		{FileName: jar.FileName, Addon: &api.Addon{Source: "playkeeper", ProjectID: fp.ID, Slug: fp.Slug, Name: fp.Name, VersionNumber: jar.Version}},
	}}
	c := newTestChecker(t, agent)
	c.sources.hc = &http.Client{Transport: through{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("%s %s was requested", r.Method, r.URL)
		http.Error(w, "no requests", http.StatusTeapot)
	})}}
	r := c.check(context.Background(), "ai-build-battle", f, false, false)
	if r.Status != statusPassing {
		t.Fatalf("got %s: %s", r.Status, r.Failure)
	}
	want := checks.Addon{Name: fp.Name, Source: "playkeeper", Slug: fp.Slug, Version: jar.Version, Licence: fp.License}
	if len(r.Check.Addons) != 1 || r.Check.Addons[0] != want || want.Licence == "" {
		t.Errorf("recorded %+v, want %+v", r.Check.Addons, want)
	}
	if err := r.Check.Valid(); err != nil {
		t.Errorf("the check isn't one the site reads: %v", err)
	}
}

// A plugin that fails to enable fails the template, even though the server
// comes online, and the server is still removed.
func TestAPluginThatFailsToEnableFailsTheCheck(t *testing.T) {
	agent := &fakeAgent{ready: true, log: []string{
		"[Server thread/ERROR]: Error occurred while enabling IridiumSkyblock v4.1.5 (Is it up to date?)",
		"[Server thread/INFO]: Done (18.0s)! For help, type \"help\"",
	}}
	r := newTestChecker(t, agent).check(context.Background(), "survival", survivalFile(t), false, false)
	if r.Status != statusFailing || !strings.Contains(r.Failure, "Error occurred while enabling IridiumSkyblock") {
		t.Fatalf("got %s: %s", r.Status, r.Failure)
	}
	if r.Check == nil || r.Check.Status != checks.Failing || r.Check.Valid() != nil {
		t.Errorf("a failing check is recorded as one: %+v", r.Check)
	}
	if len(agent.deleted) != 1 {
		t.Error("the failed check's server stayed")
	}
}

// A server that stops during its first start fails the template with what
// its log says, and is still removed.
func TestAFailedCreateSaysWhatTheLogSays(t *testing.T) {
	agent := &fakeAgent{ready: true, createFails: true, log: []string{
		"[main/WARN]: Mod resolution failed",
		"[main/ERROR]: Incompatible mods found!",
		" - Mod 'Ledger' (ledger) 1.3.23 requires version 1.13.9+kotlin.2.3.10 or later of fabric-language-kotlin, which is missing!",
	}}
	r := newTestChecker(t, agent).check(context.Background(), "survival", survivalFile(t), false, false)
	if r.Status != statusFailing || !strings.Contains(r.Failure, "exit code 1") || !strings.Contains(r.Failure, "fabric-language-kotlin, which is missing") {
		t.Fatalf("got %s: %s", r.Status, r.Failure)
	}
	if len(agent.deleted) != 1 {
		t.Error("the failed server stayed")
	}
}

// A plan the release refuses fails before any server is made.
func TestAPlanThatIsNotReadyFailsWithItsReason(t *testing.T) {
	agent := &fakeAgent{ready: false}
	r := newTestChecker(t, agent).check(context.Background(), "survival", survivalFile(t), false, false)
	if r.Status != statusFailing || !strings.Contains(r.Failure, "can't be created here") {
		t.Fatalf("got %s: %s", r.Status, r.Failure)
	}
	if slices.Contains(agent.calls, "POST /v1/servers") {
		t.Error("a server was created from a plan that wasn't ready")
	}
}

// -pin writes the template back with each add-on at the exact version the
// server's export names, its dependencies pinned after them, one per line.
func TestPinWritesTheExactVersions(t *testing.T) {
	agent := &fakeAgent{ready: true, log: []string{"Done (12.5s)!"}, exported: pinnedExport(t)}
	r := newTestChecker(t, agent).check(context.Background(), "survival", survivalFile(t), false, true)
	if r.Status != statusPassing {
		t.Fatalf("got %s: %s", r.Status, r.Failure)
	}
	got := string(r.pinned)
	for _, want := range []string{
		`"name": "Survival",`,
		`{ "source": "modrinth", "project": "fALzjamp", "slug": "chunky", "name": "Chunky", "pin": { "versionId": "vchunky", "versionNumber": "1.0", "hashAlgo": "sha512", "hash": "` + strings.Repeat("a", 128) + `" } },`,
		`"dependencyOf": "Lu3KuzdV" }`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the pinned template doesn't say %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"latest"`) {
		t.Errorf("a pinned template still asks for the newest versions:\n%s", got)
	}
	pinned, err := templates.Decode(r.pinned)
	if err != nil {
		t.Fatalf("the pinned template doesn't read: %v", err)
	}
	// The site needs the check to list the committed template's add-ons, its
	// dependencies too, in the same order.
	var inTemplate, inCheck []string
	for _, a := range pinned.Addons {
		inTemplate = append(inTemplate, a.Name)
	}
	for _, a := range r.Check.Addons {
		inCheck = append(inCheck, a.Name)
	}
	if !slices.Equal(inTemplate, inCheck) || r.Check.Addons[2].Version != "2.0" {
		t.Errorf("the check lists %v, and the pinned template %v", inCheck, inTemplate)
	}
}

// A template whose add-on didn't install fails, and -pin writes nothing for
// it, even though its server's export had pins.
func TestAFailingCheckWritesNoPins(t *testing.T) {
	agent := &fakeAgent{ready: true, log: []string{"Done (12.5s)!"}, exported: pinnedExport(t), missing: "coreprotect"}
	f := survivalFile(t)
	if err := os.WriteFile(f.path, f.raw, 0o644); err != nil {
		t.Fatal(err)
	}
	r := newTestChecker(t, agent).check(context.Background(), "survival", f, false, true)
	if r.Status != statusFailing || !strings.Contains(r.Failure, "CoreProtect didn't install") || r.pinned != nil {
		t.Fatalf("got %s (%s), pinned %d bytes", r.Status, r.Failure, len(r.pinned))
	}
}

// -bump plans the template with its add-ons at their newest versions, and
// leaves out the dependencies the last pin brought in.
func TestFloatedAsksForTheNewestVersions(t *testing.T) {
	tpl, err := templates.Decode([]byte(pinnedExport(t)))
	if err != nil {
		t.Fatal(err)
	}
	f := floated(tpl)
	if len(f.Addons) != 2 {
		t.Fatalf("floated kept %d add-ons, want the 2 chosen ones", len(f.Addons))
	}
	for _, a := range f.Addons {
		if a.Pin != nil || !a.Latest {
			t.Errorf("%s is still pinned", a.Name)
		}
	}
	if tpl.Addons[0].Pin == nil {
		t.Error("floated changed the template it was given")
	}
}

// A template that failed because a source asked to slow down is tried
// again once, and a real failure keeps its own reason under -verify.
func TestRunTriesAgainAfterARateLimitAndKeepsRealReasons(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "site", "data", "templates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"survival.json": survival, "cards.json": `{"survival": {"art": "a.svg"}}`} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	agent := &fakeAgent{ready: true, log: []string{"Done (12.5s)!"}, slowDowns: 1}
	results, err := run(context.Background(), newTestChecker(t, agent), options{root: root, write: true})
	if err != nil || len(results) != 1 || results[0].Status != statusPassing {
		t.Fatalf("a rate-limited template wasn't tried again: %+v %v", results, err)
	}

	agent = &fakeAgent{ready: false}
	results, err = run(context.Background(), newTestChecker(t, agent), options{root: root, verify: true})
	if err != nil || len(results) != 1 || !strings.Contains(results[0].Failure, "can't be created here") {
		t.Fatalf("-verify hid the real reason: %+v %v", results, err)
	}
}

// -write records each check, and -verify fails a template whose committed
// check lists other versions than the ones that installed.
func TestRunWritesAndVerifiesChecks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "site", "data", "templates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"survival.json": survival, "later.json": survival, "cards.json": `{"survival": {"art": "a.svg"}, "later": {"art": "a.svg", "opensFrom": "0.4.4"}}`} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	agent := &fakeAgent{ready: true, log: []string{"Done (12.5s)!"}}
	c := newTestChecker(t, agent)
	results, err := run(context.Background(), c, options{root: root, write: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].ID != "later" || results[0].Status != statusSkipped || results[1].Status != statusPassing {
		t.Fatalf("results: %+v", results)
	}
	written, err := checks.Read(os.DirFS(root), "site/data/checks")
	if err != nil || written["survival"] == nil || written["survival"].Addons[0].Version != "1.5.3" || written["later"] != nil {
		t.Fatalf("written checks: %+v, %v", written, err)
	}

	written["survival"].Addons[0].Version = "1.4.0"
	if err := checks.Write(filepath.Join(root, "site", "data", "checks"), "survival", written["survival"]); err != nil {
		t.Fatal(err)
	}
	results, err = run(context.Background(), newTestChecker(t, agent), options{root: root, only: []string{"survival"}, verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != statusFailing || !strings.Contains(results[0].Failure, "chunky 1.4.0") {
		t.Fatalf("a check listing other versions passed: %+v", results)
	}

	written["survival"].Addons[0].Version = "1.5.3"
	written["survival"].Crossplay = true
	if err := checks.Write(filepath.Join(root, "site", "data", "checks"), "survival", written["survival"]); err != nil {
		t.Fatal(err)
	}
	results, err = run(context.Background(), newTestChecker(t, agent), options{root: root, only: []string{"survival"}, verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != statusFailing || !strings.Contains(results[0].Failure, "crossplay turns on, and it turned off") {
		t.Fatalf("a check saying crossplay turns on passed a run where it didn't: %+v", results)
	}
}

// Where the release offers crossplay, a passing template's check turns it on
// and records whether Geyser started beside its add-ons. Crossplay never
// fails the template itself, and a release without it records nothing.
func TestACheckTurnsOnCrossplay(t *testing.T) {
	done := "[Server thread/INFO]: Done (12.51s)! For help, type \"help\""
	for name, tc := range map[string]struct {
		crossplay *api.Crossplay
		after     []string
		want      bool
		why       string
	}{
		"a release without crossplay":   {nil, nil, false, ""},
		"a server it isn't offered for": {&api.Crossplay{Port: 19132}, nil, false, ""},
		"Geyser starts":                 {&api.Crossplay{Available: true, Port: 19132}, []string{"[Geyser-Spigot] Started Geyser on UDP port 19132", done}, true, ""},
		"Geyser fails to enable":        {&api.Crossplay{Available: true, Port: 19132}, []string{"[Server thread/ERROR]: Error occurred while enabling Geyser-Spigot v2.11.3-SNAPSHOT (Is it up to date?)", done}, false, "Error occurred while enabling Geyser-Spigot"},
		"Geyser never says it started":  {&api.Crossplay{Available: true, Port: 19132}, []string{done}, false, "Geyser never said it started"},
	} {
		agent := &fakeAgent{ready: true, log: []string{done}, crossplay: tc.crossplay, crossplayLog: tc.after}
		r := newTestChecker(t, agent).check(context.Background(), "survival", survivalFile(t), false, false)
		if r.Status != statusPassing {
			t.Errorf("%s: the template is %s: %s", name, r.Status, r.Failure)
			continue
		}
		if r.Check.Crossplay != tc.want || (tc.why == "") != (r.CrossplayFailure == "") || !strings.Contains(r.CrossplayFailure, tc.why) {
			t.Errorf("%s: crossplay %v, %q", name, r.Check.Crossplay, r.CrossplayFailure)
		}
		if len(agent.deleted) != 1 {
			t.Errorf("%s: the server wasn't removed", name)
		}
	}
}

// When turning crossplay on fails for a reason that passes but leaves it on,
// the check's second try checks Geyser rather than turning it on again.
func TestACrossplayLeftOnByARetriedFailureIsChecked(t *testing.T) {
	done := "[Server thread/INFO]: Done (12.51s)! For help, type \"help\""
	agent := &fakeAgent{ready: true, log: []string{done}, crossplay: &api.Crossplay{Available: true, Port: 19132}, crossplaySlowDown: true,
		crossplayLog: []string{"[Geyser-Spigot] Started Geyser on UDP port 19132", done}}
	r := newTestChecker(t, agent).check(context.Background(), "survival", survivalFile(t), false, false)
	if r.Status != statusPassing || !r.Check.Crossplay || r.CrossplayFailure != "" {
		t.Fatalf("got %s, crossplay %v, %q", r.Status, r.Check != nil && r.Check.Crossplay, r.CrossplayFailure)
	}
	if n := strings.Count(strings.Join(agent.calls, "\n"), "POST /v1/servers/s1/crossplay"); n != 1 {
		t.Errorf("crossplay was turned on %d times, want once", n)
	}
}
