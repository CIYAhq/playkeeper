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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/templates"
	"github.com/CIYAhq/playkeeper/internal/templates/checks"
)

// fakeAgent answers the calls a check makes, as a Playkeeper agent does.
type fakeAgent struct {
	mu       sync.Mutex
	ready    bool
	log      []string
	servers  []api.ServerStatus
	calls    []string
	deleted  []api.DeleteServerRequest
	exported string
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
		reply(api.Operation{ID: "create", Status: "succeeded", ServerID: "s1"})
	case "POST /v1/servers/s1/start":
		reply(api.Operation{ID: "start"})
	case "GET /v1/servers/s1":
		reply(api.ServerStatus{ID: "s1", Phase: api.PhaseOnline})
	case "GET /v1/servers/s1/logs":
		var lines []api.LogLine
		for _, l := range f.log {
			lines = append(lines, api.LogLine{Text: l})
		}
		reply(api.LogsResponse{Lines: lines})
	case "GET /v1/servers/s1/addons":
		reply(api.Addons{Files: []api.AddonFile{
			{FileName: "Chunky.jar", Addon: &api.Addon{Source: "modrinth", ProjectID: "fALzjamp", Slug: "chunky", Name: "Chunky", VersionNumber: "1.5.3"}},
			{FileName: "CoreProtect.jar", Addon: &api.Addon{Source: "modrinth", ProjectID: "Lu3KuzdV", Slug: "coreprotect", Name: "CoreProtect", VersionNumber: "24.1"}},
		}})
	case "GET /v1/servers/s1/template":
		reply(api.TemplateExport{File: f.exported})
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
	return &checker{agent: agentclient.Via(through{agent}), sources: s, actor: "template-check", now: time.Now, poll: time.Millisecond, release: "0.4.2"}
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
	if _, err := templates.Decode(r.pinned); err != nil {
		t.Errorf("the pinned template doesn't read: %v", err)
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
}
