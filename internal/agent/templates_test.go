package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/templates"
)

// planTemplate posts a template file, link or link data to the plan route.
func (e *agentEnv) planTemplate(data string) (int, api.TemplatePlan, map[string]any) {
	e.t.Helper()
	resp, err := http.Post(e.ts.URL+"/v1/templates/plan", "text/plain", strings.NewReader(data))
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var body bytes.Buffer
	body.ReadFrom(resp.Body)
	var plan api.TemplatePlan
	raw := map[string]any{}
	json.Unmarshal(body.Bytes(), &plan)
	json.Unmarshal(body.Bytes(), &raw)
	return resp.StatusCode, plan, raw
}

func (e *agentEnv) installAddon(project string) {
	e.t.Helper()
	var d api.AddonDetails
	e.decode("GET", e.sp("/addons/project/modrinth/"+project), &d)
	if d.Plan == nil || !d.Plan.Ready {
		e.t.Fatalf("no plan to install %s: %+v", project, d)
	}
	if op := e.addonOp("/addons/install", map[string]any{"source": "modrinth", "projectId": project, "fingerprint": d.Plan.Fingerprint, "actor": "admin"}); op.Status != api.OpSucceeded {
		e.t.Fatalf("install %s: %+v", project, op)
	}
}

// createFromTemplate asks for a server made from a planned template.
func (e *agentEnv) createFromTemplate(fingerprint string, extra map[string]any) (int, map[string]any) {
	e.t.Helper()
	body := map[string]any{"acceptEula": true, "memoryMB": 1536, "actor": "admin", "name": "Copy", "template": map[string]any{"fingerprint": fingerprint}}
	for k, v := range extra {
		body[k] = v
	}
	code, out := e.call("POST", "/v1/servers", body)
	if id, ok := out["serverId"].(string); ok {
		e.sid = id
	}
	return code, out
}

// publishPinnable publishes versions whose ids are shaped like Modrinth's,
// which a template can pin.
func publishPinnable(f *fakeSources) {
	f.publish("mvcore00", "MVCORE52", "5.0.2", time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	f.publish("mvportal", "MVPORT51", "5.0.1", time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC), "mvcore00")
	f.publish("fALzjamp", "CHUNKY41", "1.4.41", time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
}

func addonNames(as []api.TemplateAddon) []string {
	out := []string{}
	for _, a := range as {
		out = append(out, strings.TrimSpace(a.Name+" "+a.VersionNumber))
	}
	slices.Sort(out)
	return out
}

func TestTemplateExportAndCreate(t *testing.T) {
	e := newAgentEnv(t)
	publishPinnable(e.withSources())
	e.createWith(map[string]any{"name": "Survival", "memoryMB": 1536, "motd": "Pip's place", "maxPlayers": 8, "playStyle": "friends",
		"gameplay": map[string]any{"difficulty": "hard", "pvp": false, "viewDistance": 12}})
	e.installAddon("mvportal")
	e.installAddon("fALzjamp")

	var exp api.TemplateExport
	e.decode("GET", e.sp("/template"), &exp)
	c := exp.Contents
	want := []string{"Chunky 1.4.41", "Multiverse-Core 5.0.2", "Multiverse-Portals 5.0.1"}
	if c.Name != "Survival" || c.Type != "paper" || c.MinecraftVersion != "26.1.2" || c.Build != "74" || !slices.Equal(addonNames(c.Addons), want) {
		t.Fatalf("the template: %+v, left out %+v", c, exp.LeftOut)
	}
	st := c.Settings
	if st.Difficulty != "hard" || st.PVP == nil || *st.PVP || st.ViewDistance != 12 || st.MaxPlayers != 8 || st.MOTD != "Pip's place" ||
		st.PlayStyle != "friends" || st.MemoryMB != 1536 {
		t.Fatalf("its settings: %+v", st)
	}
	if exp.FileName != "survival"+templates.FileExtension || !strings.HasPrefix(exp.Link, templates.ShareURL+"#") || exp.LinkLong || exp.PacksHere != 0 {
		t.Fatalf("the file and link: %q %q %v", exp.FileName, exp.Link, exp.LinkLong)
	}
	if tf, err := templates.ParseFile([]byte(exp.File)); err != nil || tf.Name != "Survival" || len(tf.Addons) != 3 {
		t.Fatalf("the file: %v %+v", err, tf)
	}

	var some api.TemplateExport
	e.decode("GET", e.sp("/template?addons=off&settings=off"), &some)
	if len(some.Contents.Addons) != 0 || some.Contents.Settings != (api.TemplateSettings{}) || len(some.Available.Addons) != 3 || some.Available.Settings.Difficulty != "hard" {
		t.Fatalf("leaving parts out: %+v, with everything %+v", some.Contents, some.Available)
	}
	var latest api.TemplateExport
	e.decode("GET", e.sp("/template?versions=latest"), &latest)
	if got := addonNames(latest.Contents.Addons); !slices.Equal(got, []string{"Chunky", "Multiverse-Core", "Multiverse-Portals"}) {
		t.Fatalf("latest compatible versions: %v", got)
	}

	code, plan, raw := e.planTemplate(exp.File)
	if code != 200 || plan.Type != "paper" || plan.VersionID != "paper-26.1.2" || plan.Build != "74" || !plan.Ready || plan.Fingerprint == "" ||
		plan.MemoryMB == 0 || len(plan.Blockers) != 0 || !slices.Equal(addonNames(plan.Contents.Addons), want) {
		t.Fatalf("plan: %d %+v %v", code, plan, raw)
	}
	if code, byLink, _ := e.planTemplate(exp.Link); code != 200 || byLink.Fingerprint != plan.Fingerprint {
		t.Fatalf("the link plans the same: %d %+v", code, byLink)
	}

	code, out := e.createFromTemplate(plan.Fingerprint, nil)
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded {
		t.Fatalf("create from the template: %+v", op)
	}
	e.waitFor("online", func() bool { return e.status().Phase == api.PhaseOnline })
	if op.Detail["addons"] != float64(3) || op.Detail["addonsTotal"] != float64(3) {
		t.Errorf("progress: %v", op.Detail)
	}
	recs, err := e.srv().installedAddons()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, rec := range recs {
		got = append(got, rec.Name+" "+rec.VersionNumber)
		if _, err := os.Stat(filepath.Join(e.dataDir(), "plugins", rec.FileName)); err != nil {
			t.Errorf("%s: %v", rec.FileName, err)
		}
		if rec.ProjectID == "mvcore00" && rec.DependencyOf != "mvportal" {
			t.Errorf("Multiverse-Core is Multiverse-Portals' dependency again: %+v", rec)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("the new server's add-ons: %v", got)
	}
	sc, _ := e.srv().serverConfig()
	gp := sc.Gameplay
	if sc.Template == nil || sc.Template.Name != "Survival" || sc.Template.Pending || sc.PaperBuild != 74 || sc.MaxPlayers != 8 || sc.MOTD != "Pip's place" ||
		sc.PlayStyle != "friends" || gp.Difficulty != "hard" || gp.PVP == nil || *gp.PVP || gp.ViewDistance != 12 {
		t.Fatalf("the new server: %+v %+v", sc, sc.Template)
	}
	if e.countRows(`SELECT COUNT(*) FROM template_installs`) != 0 || e.audits("addon.installed") != 3 {
		t.Fatalf("the import is finished and audited: %d rows, %d audits", e.countRows(`SELECT COUNT(*) FROM template_installs`), e.audits("addon.installed"))
	}
}

func TestCreateFromTemplateRefusesAChangedPlan(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(map[string]any{"memoryMB": 1536})
	var exp api.TemplateExport
	e.decode("GET", e.sp("/template"), &exp)
	code, plan, _ := e.planTemplate(exp.File)
	if code != 200 || !plan.Ready || plan.Build != "74" {
		t.Fatalf("plan: %d %+v", code, plan)
	}
	// PaperMC publishes a new build of 26.1.2, so a new server would not get
	// the build the user saw.
	vs := defaultFill()
	for i := range vs {
		if vs[i].id == "26.1.2" {
			vs[i].builds = []fillBuildSpec{{80, "STABLE"}}
		}
	}
	e.fill.set("", vs)
	e.a.catalog.mu.Lock()
	e.a.catalog.at = time.Time{}
	e.a.catalog.mu.Unlock()
	before := e.countRows(`SELECT COUNT(*) FROM servers`)
	code, out := e.createFromTemplate(plan.Fingerprint, nil)
	if code != http.StatusConflict || !strings.Contains(out["error"].(string), "changed since you confirmed") {
		t.Fatalf("a changed plan: %d %v", code, out)
	}
	if e.countRows(`SELECT COUNT(*) FROM servers`) != before {
		t.Fatal("no server is created")
	}
}

// flakyTransport fails every request while down is set.
type flakyTransport struct {
	base http.RoundTripper
	down atomic.Bool
}

func (f *flakyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.down.Load() {
		return nil, errors.New("connection refused")
	}
	return f.base.RoundTrip(r)
}

func TestCreateFromTemplateTriesAgainOnTheNextStart(t *testing.T) {
	e := newAgentEnv(t)
	f := newFakeSources(t)
	publishPinnable(f)
	flaky := &flakyTransport{base: f.client.Transport}
	client := &http.Client{Transport: flaky}
	lib := f.library(t.TempDir())
	lib.Modrinth = modrinth.New(fetch.Options{BaseURL: f.modrinth.URL + "/v2", HTTP: client})
	lib.HTTP = client
	e.stop()
	e.addons = lib
	e.start()
	e.createWith(map[string]any{"memoryMB": 1536})
	e.installAddon("fALzjamp")
	var exp api.TemplateExport
	e.decode("GET", e.sp("/template"), &exp)
	_, plan, _ := e.planTemplate(exp.File)

	flaky.down.Store(true)
	code, out := e.createFromTemplate(plan.Fingerprint, nil)
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpFailed {
		t.Fatalf("Modrinth does not answer: %+v", op)
	}
	sc, _ := e.srv().serverConfig()
	if recs, _ := e.srv().installedAddons(); sc.Template == nil || !sc.Template.Pending || len(recs) != 0 {
		t.Fatalf("the add-ons wait for the next start: %+v %v", sc.Template, recs)
	}

	flaky.down.Store(false)
	if op := e.runOp("POST", "/start"); op.Status != api.OpSucceeded {
		t.Fatalf("start: %+v", op)
	}
	recs, _ := e.srv().installedAddons()
	sc, _ = e.srv().serverConfig()
	if len(recs) != 1 || recs[0].Name != "Chunky" || sc.Template.Pending || e.countRows(`SELECT COUNT(*) FROM template_installs`) != 0 {
		t.Fatalf("after the next start: %v %+v", recs, sc.Template)
	}
}

func TestTemplateRequestsAreChecked(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(map[string]any{"memoryMB": 1536})
	var exp api.TemplateExport
	e.decode("GET", e.sp("/template"), &exp)
	_, plan, _ := e.planTemplate(exp.File)
	for _, tc := range []struct {
		name string
		data string
	}{
		{"not a template", "hello"},
		{"a damaged link", exp.Link[:len(exp.Link)-8]},
		{"too large", strings.Repeat("a", templates.MaxFileSize+10)},
	} {
		if code, _, raw := e.planTemplate(tc.data); code < 400 || code >= 500 || raw["error"] == nil {
			t.Errorf("%s: %d %v", tc.name, code, raw)
		}
	}
	before := e.countRows(`SELECT COUNT(*) FROM servers`)
	for _, tc := range []struct {
		name        string
		fingerprint string
		extra       map[string]any
	}{
		{"a template this machine never planned", "0123456789abcdef", nil},
		{"a template with a type", plan.Fingerprint, map[string]any{"type": "fabric"}},
		{"a template with settings", plan.Fingerprint, map[string]any{"motd": "Mine"}},
		{"a template with a modpack", plan.Fingerprint, map[string]any{"modpack": map[string]any{"source": "modrinth", "projectId": "AABBCCDD", "versionId": "EEFFGGHH"}}},
	} {
		if code, out := e.createFromTemplate(tc.fingerprint, tc.extra); code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", tc.name, code, out)
		}
	}
	if e.countRows(`SELECT COUNT(*) FROM servers`) != before {
		t.Fatal("no server is created")
	}
}
