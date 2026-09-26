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

	"github.com/CIYAhq/playkeeper/internal/addons"
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

// Create goes ahead only on a plan that is still ready. A plan that can no
// longer choose a version is refused, and fill, which that refusal keeps
// such a plan from, refuses it too rather than panic.
func TestTemplateCreateNeedsAVersion(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(map[string]any{"memoryMB": 1536})
	var exp api.TemplateExport
	e.decode("GET", e.sp("/template"), &exp)
	code, plan, _ := e.planTemplate(exp.File)
	if code != 200 || !plan.Ready {
		t.Fatalf("plan: %d %+v", code, plan)
	}
	// PaperMC stops listing versions, so the plan again chooses none.
	e.fill.set("", []fillVersionSpec{})
	e.a.catalog.mu.Lock()
	e.a.catalog.entries, e.a.catalog.at = nil, time.Time{}
	e.a.catalog.mu.Unlock()
	before := e.countRows(`SELECT COUNT(*) FROM servers`)
	if code, out := e.createFromTemplate(plan.Fingerprint, nil); code != http.StatusConflict {
		t.Fatalf("a plan without a version: %d %v", code, out)
	}
	if e.countRows(`SELECT COUNT(*) FROM servers`) != before {
		t.Fatal("no server is created")
	}

	ti := &templateImport{p: &templates.Plan{Type: templates.TypeChoice{ID: "fabric", Name: "Fabric"}}}
	var req api.CreateServerRequest
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("filling in a plan without a version panicked: %v", r)
			}
		}()
		err = ti.fill(&req)
	}()
	if err == nil || req.Type != "" || req.VersionID != "" || req.Modpack != nil {
		t.Fatalf("a plan without a version fills nothing in: %v %+v", err, req)
	}
}

func noticeKinds(ns []api.AddonNotice) []string {
	out := []string{}
	for _, n := range ns {
		out = append(out, n.Kind)
	}
	return out
}

func TestCreateFromTemplateDownloadsItsDataPacks(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(map[string]any{"memoryMB": 1536})
	var exp api.TemplateExport
	e.decode("GET", e.sp("/template"), &exp)
	tf, err := templates.ParseFile([]byte(exp.File))
	if err != nil {
		t.Fatal(err)
	}
	tweaks, terrain := dataPackZip(t, "Tweaks", false), dataPackZip(t, "Terrain", false)
	e.up.serve("https://packs.example.com/tweaks.zip", tweaks)
	// The host now serves another file than the one the template names.
	e.up.serve("https://packs.example.com/terrain.zip", dataPackZip(t, "Other terrain", false))
	tf.Packs = []templates.Pack{
		{Kind: templates.DataPack, Name: "Tweaks", URL: "https://packs.example.com/tweaks.zip", SHA1: sha1Hex(tweaks)},
		{Kind: templates.DataPack, Name: "Terrain", URL: "https://packs.example.com/terrain.zip", SHA1: sha1Hex(terrain)},
		{Kind: templates.ResourcePack, Name: "Textures", URL: "https://packs.example.com/textures.zip", SHA1: sha1Hex([]byte("textures"))},
	}
	file, err := templates.MarshalFile(tf)
	if err != nil {
		t.Fatal(err)
	}
	code, plan, raw := e.planTemplate(string(file))
	if code != 200 || !plan.Ready || !slices.Contains(noticeKinds(plan.Warnings), string(templates.KindDataPacks)) ||
		!slices.Equal(noticeKinds(plan.Skipped), []string{string(kindTemplatePacks)}) {
		t.Fatalf("plan: %d %+v %v", code, plan, raw)
	}

	code, out := e.createFromTemplate(plan.Fingerprint, nil)
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded {
		t.Fatalf("create from the template: %+v", op)
	}
	dir := filepath.Join(e.dataDir(), "world", "datapacks")
	if b, err := os.ReadFile(filepath.Join(dir, "Tweaks.zip")); err != nil || !bytes.Equal(b, tweaks) {
		t.Fatalf("the template's data pack: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Terrain.zip")); !os.IsNotExist(err) {
		t.Fatalf("a data pack that doesn't match its checksum is installed: %v", err)
	}
	var skipped []api.AddonNotice
	b, _ := json.Marshal(op.Detail["skipped"])
	json.Unmarshal(b, &skipped)
	if len(skipped) != 1 || skipped[0].Kind != string(templates.KindPackHash) || skipped[0].Params["name"] != "Terrain" ||
		op.Detail["packs"] != float64(2) || op.Detail["packsTotal"] != float64(2) {
		t.Fatalf("the skipped data pack and progress: %v", op.Detail)
	}
	if n := e.up.hitCount("https://packs.example.com/textures.zip"); n != 0 {
		t.Fatalf("the resource pack was downloaded %d times", n)
	}
	sc, _ := e.srv().serverConfig()
	if sc.Template == nil || sc.Template.Pending || len(sc.Template.Skipped) != 1 || sc.Template.Skipped[0].Params["name"] != "Terrain" {
		t.Fatalf("the import is finished, and the skipped pack stays listed: %+v", sc.Template)
	}
	if e.audits("datapack.added") != 1 || e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'datapack.skipped' AND server_id = ?`, e.sid) != 1 {
		t.Fatal("both data packs are audited")
	}

	// The host serves the pack the template names again: Try again puts it in.
	e.up.serve("https://packs.example.com/terrain.zip", terrain)
	code, out = e.call("POST", e.sp("/template/retry"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("try again: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded || op.Kind != "template-retry" {
		t.Fatalf("try again: %+v", op)
	}
	if _, err := os.Stat(filepath.Join(dir, "Terrain.zip")); err != nil {
		t.Fatalf("the data pack after trying again: %v", err)
	}
	sc, _ = e.srv().serverConfig()
	if len(sc.Template.Skipped) != 0 || e.countRows(`SELECT COUNT(*) FROM template_installs`) != 0 {
		t.Fatalf("nothing is left to try: %+v", sc.Template)
	}
}

// withdraw takes a published version off the fake Modrinth, as when its
// author deletes it; the function it returns puts it back.
func (f *fakeSources) withdraw(id string) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.versions, func(v *fakeVersion) bool { return v.id == id })
	if i < 0 {
		f.t.Fatalf("no version %s", id)
	}
	v := f.versions[i]
	f.versions = slices.Delete(f.versions, i, i+1)
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.versions = append(f.versions, v)
	}
}

// A template add-on its source no longer offers is skipped, and stays on
// the server's record after the first start (the status carries it) until
// Try again installs it; the running server then needs a restart.
func TestSkippedTemplateAddonsStayUntilTriedAgain(t *testing.T) {
	e := newAgentEnv(t)
	f := e.withSources()
	publishPinnable(f)
	e.createWith(map[string]any{"memoryMB": 1536})
	e.installAddon("fALzjamp")
	e.installAddon("mvportal")
	var exp api.TemplateExport
	e.decode("GET", e.sp("/template"), &exp)
	_, plan, _ := e.planTemplate(exp.File)
	putBack := f.withdraw("CHUNKY41")

	code, out := e.createFromTemplate(plan.Fingerprint, nil)
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("create from the template: %+v", op)
	}
	e.waitFor("online", func() bool { return e.status().Phase == api.PhaseOnline })
	st := e.status()
	if tpl := st.Config.Template; tpl == nil || tpl.Pending || len(tpl.Skipped) != 1 || tpl.Skipped[0].Params["name"] != "Chunky" || tpl.Skipped[0].Message == "" {
		t.Fatalf("the status after the first start: %+v", st.Config.Template)
	}
	if recs, _ := e.srv().installedAddons(); len(recs) != 2 {
		t.Fatalf("the other add-ons are installed: %v", recs)
	}

	putBack()
	code, out = e.call("POST", e.sp("/template/retry"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("try again: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded || op.Detail["restartNeeded"] != true {
		t.Fatalf("try again: %+v", op)
	}
	recs, _ := e.srv().installedAddons()
	var names []string
	for _, rec := range recs {
		names = append(names, rec.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"Chunky", "Multiverse-Core", "Multiverse-Portals"}) {
		t.Fatalf("after trying again: %v", names)
	}
	if tpl := e.status().Config.Template; len(tpl.Skipped) != 0 || e.countRows(`SELECT COUNT(*) FROM template_installs`) != 0 {
		t.Fatalf("nothing is left to try: %+v", tpl)
	}
	if !e.addonList().RestartNeeded {
		t.Fatal("the running server is asked to restart for the new plugin")
	}
	if code, out := e.call("POST", e.sp("/template/retry"), map[string]any{"actor": "admin"}); code != 409 {
		t.Fatalf("trying again with nothing left: %d %v", code, out)
	}
}

// A template carries a Vanilla modpack on a Vanilla server. One naming
// another type or Minecraft version than its modpack runs on is blocked:
// the new server would run the pack's, not what the plan shows.
func TestTemplateModpackRunsOnTheTypeItNames(t *testing.T) {
	e := newAgentEnv(t)
	e.up.servePack()
	e.up.serveFabricLists()
	plan := func(typ, mc string) api.TemplatePlan {
		t.Helper()
		file, err := templates.MarshalFile(&templates.Template{Format: templates.Format, Name: "Waystones", Game: templates.Game,
			Server: templates.Server{Type: typ, MinecraftVersion: mc},
			Modpack: &templates.Modpack{Source: addons.Modrinth, Project: fakePackID, Slug: "testpack", Name: "Waystones Pack",
				Pin: templates.Pin{VersionID: fakePackVersion, VersionNumber: "1.0.0", Channel: "release", HashAlgo: "sha512", Hash: strings.Repeat("a", 128)}}})
		if err != nil {
			t.Fatal(err)
		}
		code, p, raw := e.planTemplate(string(file))
		if code != 200 {
			t.Fatalf("plan: %d %v", code, raw)
		}
		return p
	}
	if p := plan("vanilla", "26.2"); !p.Ready || p.Type != "vanilla" || len(p.Blockers) != 0 {
		t.Fatalf("a Vanilla pack on a Vanilla server: %+v", p)
	}
	p := plan("fabric", "26.2")
	if p.Ready || !slices.Equal(noticeKinds(p.Blockers), []string{string(kindTemplatePackType)}) ||
		p.Blockers[0].Message != "The template names a Fabric server, but its modpack Waystones Pack runs on Vanilla." {
		t.Fatalf("a Vanilla pack in a Fabric template: %+v", p)
	}
	if code, out := e.createFromTemplate(p.Fingerprint, nil); code == 202 {
		t.Fatalf("a blocked template must not create a server: %v", out)
	}
	p = plan("vanilla", "26.1.2")
	if p.Ready || p.MinecraftVersion != "26.1.2" || !slices.Equal(noticeKinds(p.Blockers), []string{string(kindTemplatePackVersion)}) ||
		p.Blockers[0].Message != "The template names Minecraft 26.1.2, but its modpack Waystones Pack is made for Minecraft 26.2." {
		t.Fatalf("a pack for Minecraft 26.2 in a template for 26.1.2: %+v", p)
	}
	if code, out := e.createFromTemplate(p.Fingerprint, nil); code == 202 {
		t.Fatalf("a blocked template must not create a server: %v", out)
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
	if code, out := e.createFromTemplate("0123456789abcdef", nil); code != http.StatusConflict || out["code"] != "plan_changed" {
		t.Errorf("a template this machine never planned: %d %v", code, out)
	}
	for _, tc := range []struct {
		name        string
		fingerprint string
		extra       map[string]any
	}{
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

// A template names no one, as a sign-in name is half of the login: an export
// writes no author whatever the request says, and planning a file whose
// author was filled in by hand doesn't pass it on. Both say on which day the
// template was made.
func TestTemplatesNameNoOne(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	today := e.a.now().UTC().Format(time.DateOnly)
	code, _, body := e.getBytes(e.sp("/template?author=siya"))
	var exp api.TemplateExport
	if err := json.Unmarshal(body, &exp); code != 200 || err != nil {
		t.Fatalf("export: %d %v %s", code, err, body)
	}
	if exp.Contents.Created != today || strings.Contains(string(body), "siya") || strings.Contains(exp.File, `"author"`) {
		t.Fatalf("the export names no one and says its day: %s", body)
	}
	if l, err := templates.DecodeLink(exp.Link); err != nil || l.Author != "" {
		t.Fatalf("the export's link names no one: %+v %v", l, err)
	}

	signed, err := templates.ParseFile([]byte(exp.File))
	if err != nil {
		t.Fatal(err)
	}
	signed.Author = "siya"
	file, err := templates.MarshalFile(signed)
	if err != nil {
		t.Fatal(err)
	}
	code, plan, raw := e.planTemplate(string(file))
	if shown, _ := json.Marshal(raw); code != 200 || !plan.Ready || plan.Contents.Created != today || strings.Contains(string(shown), "siya") {
		t.Fatalf("planning a file that names its author shows no name: %d %s", code, shown)
	}
}
