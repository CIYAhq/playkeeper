package agent

import (
	"bytes"
	"context"
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
	"github.com/CIYAhq/playkeeper/internal/modpacks"
	"github.com/CIYAhq/playkeeper/internal/templates"
	"github.com/CIYAhq/playkeeper/internal/templates/library"
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
	code, out = e.callWhenFree("POST", e.sp("/template/retry"), map[string]any{"actor": "admin"})
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
	code, out = e.callWhenFree("POST", e.sp("/template/retry"), map[string]any{"actor": "admin"})
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
// the new server would run the pack's, not what the plan shows. So is one
// whose pin isn't the file the source offers for that version.
func TestTemplateModpackRunsOnTheTypeItNames(t *testing.T) {
	e := newAgentEnv(t)
	pack := e.up.servePack()
	e.up.serveFabricLists()
	planWith := func(typ, mc, hash string) api.TemplatePlan {
		t.Helper()
		file, err := templates.MarshalFile(&templates.Template{Format: templates.Format, Name: "Waystones", Game: templates.Game,
			Server: templates.Server{Type: typ, MinecraftVersion: mc},
			Modpack: &templates.Modpack{Source: addons.Modrinth, Project: fakePackID, Slug: "testpack", Name: "Waystones Pack",
				Pin: templates.Pin{VersionID: fakePackVersion, VersionNumber: "1.0.0", Channel: "release", HashAlgo: "sha512", Hash: hash}}})
		if err != nil {
			t.Fatal(err)
		}
		code, p, raw := e.planTemplate(string(file))
		if code != 200 {
			t.Fatalf("plan: %d %v", code, raw)
		}
		return p
	}
	plan := func(typ, mc string) api.TemplatePlan { t.Helper(); return planWith(typ, mc, pack.sha512) }
	if p := plan("vanilla", "26.2"); !p.Ready || p.Type != "vanilla" || len(p.Blockers) != 0 {
		t.Fatalf("a Vanilla pack on a Vanilla server: %+v", p)
	}
	altered := planWith("vanilla", "26.2", strings.Repeat("a", 128))
	if altered.Ready || !slices.Equal(noticeKinds(altered.Blockers), []string{string(templates.KindPinMismatch)}) ||
		altered.Blockers[0].Message != "The file Modrinth offers for Waystones Pack 1.0.0 isn't the one the template names." {
		t.Fatalf("a template pinning another file than Modrinth's: %+v", altered)
	}
	if code, out := e.createFromTemplate(altered.Fingerprint, nil); code == 202 {
		t.Fatalf("a blocked template must not create a server: %v", out)
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

// A plan goes ahead when the pack's source doesn't answer. If it answers
// only when the server is created, the create request checks the template's
// pin then, and creates nothing from a file the template doesn't name.
func TestTemplateCreateChecksThePinThePlanCouldNot(t *testing.T) {
	e := newAgentEnv(t)
	e.up.servePack()
	versions := "https://api.modrinth.com/v2/project/" + fakePackID + "/version"
	e.up.mu.Lock()
	list := e.up.routes[e.up.key(versions)]
	e.up.mu.Unlock()
	var asked atomic.Int32
	e.up.handle(versions, func(w http.ResponseWriter, r *http.Request) {
		// The plan asks first, then the create request's own plan.
		if asked.Add(1) <= 2 {
			http.Error(w, "Modrinth is down", http.StatusServiceUnavailable)
			return
		}
		w.Write(list)
	})
	file, err := templates.MarshalFile(&templates.Template{Format: templates.Format, Name: "Waystones", Game: templates.Game,
		Server: templates.Server{Type: "vanilla", MinecraftVersion: "26.2"},
		Modpack: &templates.Modpack{Source: addons.Modrinth, Project: fakePackID, Slug: "testpack", Name: "Waystones Pack",
			Pin: templates.Pin{VersionID: fakePackVersion, VersionNumber: "1.0.0", Channel: "release", HashAlgo: "sha512", Hash: strings.Repeat("a", 128)}}})
	if err != nil {
		t.Fatal(err)
	}
	code, p, raw := e.planTemplate(string(file))
	if code != 200 || !p.Ready {
		t.Fatalf("a plan while Modrinth doesn't answer: %d %v", code, raw)
	}
	before := e.countRows(`SELECT COUNT(*) FROM servers`)
	code, out := e.createFromTemplate(p.Fingerprint, nil)
	if code != http.StatusConflict || out["code"] != string(templates.KindPinMismatch) ||
		out["error"] != "The file Modrinth offers for Waystones Pack 1.0.0 isn't the one the template names." {
		t.Fatalf("creating once Modrinth answers: %d %v", code, out)
	}
	if n := asked.Load(); n < 3 || e.countRows(`SELECT COUNT(*) FROM servers`) != before {
		t.Fatalf("Modrinth was asked %d times, and no server may be created", n)
	}
}

// A modpack runs the Minecraft version it is made for, which the create
// flow may not list: it offers each line's newest release, 26.1.2 here and
// not 26.1.1. A template of such a pack plans and creates a server on the
// pack's version, as New server does with the pack itself.
func TestTemplateModpackPlansOnThePacksOwnVersion(t *testing.T) {
	e := newAgentEnv(t)
	pack := e.up.servePackOf(fakePackSpec{mc: "26.1.1", loader: "fabric-loader", loaderVersion: "0.17.2"})
	e.up.serveFabricLists()
	e.up.serve("https://meta.fabricmc.net/v2/versions/game", []byte(`[{"version":"26.2","stable":true},{"version":"26.1.2","stable":true},{"version":"26.1.1","stable":true}]`))
	entries, _, err := e.a.typeCatalog(context.Background(), "fabric")
	if err != nil || slices.ContainsFunc(entries, func(c api.CatalogEntry) bool { return c.MinecraftVersion == "26.1.1" }) {
		t.Fatalf("New server offers each line's newest release, not 26.1.1: %+v %v", entries, err)
	}
	file, err := templates.MarshalFile(&templates.Template{Format: templates.Format, Name: "Waystones", Game: templates.Game,
		Server: templates.Server{Type: "fabric", MinecraftVersion: "26.1.1"},
		Modpack: &templates.Modpack{Source: addons.Modrinth, Project: fakePackID, Slug: "testpack", Name: "Waystones Pack",
			Pin: templates.Pin{VersionID: fakePackVersion, VersionNumber: "1.0.0", Channel: "release", HashAlgo: "sha512", Hash: pack.sha512}}})
	if err != nil {
		t.Fatal(err)
	}
	code, p, raw := e.planTemplate(string(file))
	if code != 200 || !p.Ready || p.Type != "fabric" || p.MinecraftVersion != "26.1.1" || p.Build != "0.17.2" || len(p.Blockers) != 0 || len(p.Warnings) != 0 {
		t.Fatalf("plan: %d %+v %v", code, p, raw)
	}
	code, out := e.createFromTemplate(p.Fingerprint, nil)
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	e.waitOp(out["id"].(string))
	if sc, _ := e.srv().serverConfig(); sc.Modpack == nil || sc.Modpack.VersionID != fakePackVersion || sc.Software == nil || sc.Software.MinecraftVersion != "26.1.1" {
		t.Fatalf("the new server runs the pack's Minecraft version: %+v %+v", sc.Modpack, sc.Software)
	}
}

// A server made from a modpack shares the pack, not its mods one by one:
// they have no add-on records, and they aren't files added by hand. A mod
// that was is left out.
func TestTemplateOfAModpackServerCarriesThePack(t *testing.T) {
	e := newAgentEnv(t)
	e.addIdleServer()
	e.fabricForShare()
	mods := filepath.Join(e.dataDir(), "mods")
	for _, id := range []string{"waystones", "chunky", "mytweaks"} {
		jar := zipOf(t, map[string][]byte{"fabric.mod.json": []byte(`{"schemaVersion":1,"id":"` + id + `","version":"1.0.0","name":"` + id + `"}`)})
		writeTestFile(t, filepath.Join(mods, id+".jar"), jar, time.Time{})
	}
	s := e.srv()
	rec := modpacks.Record{
		Pack: addons.Installed{Source: addons.Modrinth, ProjectID: fakePackID, Slug: "testpack", Name: "Waystones Pack", VersionID: fakePackVersion,
			VersionNumber: "1.0.0", Channel: "release", HashAlgo: "sha512", Hash: strings.Repeat("a", 128)},
		Files: []modpacks.File{{Path: "mods/waystones.jar", Origin: modpacks.Download}, {Path: "mods/chunky.jar", Origin: modpacks.Download},
			{Path: "config/testpack.toml", Origin: modpacks.Override}},
	}
	if err := s.savePackRecord(rec); err != nil {
		t.Fatal(err)
	}
	sc, err := s.serverConfig()
	if err != nil {
		t.Fatal(err)
	}
	sc.Modpack = &api.ServerModpack{Source: "modrinth", ProjectID: fakePackID, VersionID: fakePackVersion, Name: "Waystones Pack", VersionNumber: "1.0.0", Mods: 2}
	if err := s.saveServerConfig(*sc); err != nil {
		t.Fatal(err)
	}
	var exp api.TemplateExport
	e.decode("GET", e.sp("/template"), &exp)
	if m := exp.Contents.Modpack; m == nil || m.Name != "Waystones Pack" || m.VersionNumber != "1.0.0" || len(exp.Contents.Addons) != 0 {
		t.Fatalf("the template carries the pack: %+v, add-ons %+v", exp.Contents.Modpack, exp.Contents.Addons)
	}
	if len(exp.LeftOut) != 1 || exp.LeftOut[0].Kind != string(templates.KindLeftOutUpload) || exp.LeftOut[0].Params["file"] != "mytweaks.jar" {
		t.Fatalf("only the mod added by hand is left out: %+v", exp.LeftOut)
	}
	if i := slices.IndexFunc(exp.Notes, func(n api.AddonNotice) bool { return n.Kind == string(templates.KindNoteModpackAddons) }); i < 0 || exp.Notes[i].Params["count"] != "2" {
		t.Fatalf("the pack's 2 mods travel with it: %+v", exp.Notes)
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

// chunkyTemplate is a Paper template with Chunky.
func chunkyTemplate(t *testing.T) string {
	t.Helper()
	file, err := templates.MarshalFile(&templates.Template{Format: templates.Format, Name: "Pregen", Game: templates.Game,
		Server: templates.Server{Type: "paper", MinecraftVersion: "26.1.2"},
		Addons: []templates.Addon{{Source: addons.Modrinth, Project: "fALzjamp", Slug: "chunky", Name: "Chunky", Latest: true}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(file)
}

// A server made from a template never forgets the template quietly. The
// record of what the template adds is written with the server and settles
// with the server's settings, and a start that finds it gone says so on the
// server page rather than settling as if nothing was left to install.
func TestTemplateRecordIsNeverLostSilently(t *testing.T) {
	const (
		installed = "installed" // the add-on is on the server and the import settled
		refused   = "refused"   // no server was made
		lost      = "lost"      // the server page says the record was lost, and nothing was installed
	)
	holdImages := func(e *agentEnv, on bool) {
		e.fd.mu.Lock()
		e.fd.holdImages = on
		e.fd.mu.Unlock()
	}
	// stopBeforeFirstStart creates the server and stops the agent while its
	// first start waits on the image, before anything is installed; between
	// the two, between runs.
	stopBeforeFirstStart := func(t *testing.T, e *agentEnv, create func() (int, string), between func()) {
		images := e.fd.called("GET /images/")
		holdImages(e, true)
		if code, _ := create(); code != 202 {
			t.Fatalf("create: %d", code)
		}
		e.waitFor("the first start to check the image", func() bool { return e.fd.called("GET /images/") > images })
		between()
		e.stop()
		holdImages(e, false)
		e.start()
	}
	for _, tc := range []struct {
		name string
		want string
		run  func(t *testing.T, e *agentEnv, create func() (int, string))
	}{
		{"the agent stops between creating the server and its first start", installed, func(t *testing.T, e *agentEnv, create func() (int, string)) {
			stopBeforeFirstStart(t, e, create, func() {})
		}},
		{"writing the template's record fails", refused, func(t *testing.T, e *agentEnv, create func() (int, string)) {
			if _, err := e.a.db.Exec(`CREATE TRIGGER no_record BEFORE INSERT ON template_installs BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
				t.Fatal(err)
			}
			if code, id := create(); code == 202 {
				e.waitOp(id)
			}
		}},
		{"recording that the import finished fails", installed, func(t *testing.T, e *agentEnv, create func() (int, string)) {
			if _, err := e.a.db.Exec(`CREATE TRIGGER unsettled BEFORE UPDATE OF config ON servers
				WHEN json_extract(OLD.config, '$.template.pending') AND json_extract(NEW.config, '$.template.pending') IS NULL
				BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
				t.Fatal(err)
			}
			code, id := create()
			if code != 202 {
				t.Fatalf("create: %d", code)
			}
			if op := e.waitOp(id); op.Status != api.OpFailed {
				t.Fatalf("the first start can't record that the import finished: %+v", op)
			}
			if _, err := e.a.db.Exec(`DROP TRIGGER unsettled`); err != nil {
				t.Fatal(err)
			}
			if op := e.runOp("POST", "/start"); op.Status != api.OpSucceeded {
				t.Fatalf("start: %+v", op)
			}
		}},
		{"the record is gone when the agent starts again", lost, func(t *testing.T, e *agentEnv, create func() (int, string)) {
			stopBeforeFirstStart(t, e, create, func() {
				if _, err := e.a.db.Exec(`DELETE FROM template_installs`); err != nil {
					t.Fatal(err)
				}
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgentEnv(t)
			publishPinnable(e.withSources())
			create := func() (int, string) {
				code, plan, raw := e.planTemplate(chunkyTemplate(t))
				if code != 200 || !plan.Ready {
					t.Fatalf("plan: %d %v", code, raw)
				}
				code, out := e.createFromTemplate(plan.Fingerprint, nil)
				id, _ := out["id"].(string)
				return code, id
			}
			tc.run(t, e, create)
			if tc.want == refused {
				if n := e.countRows(`SELECT COUNT(*) FROM servers`); n != 0 {
					t.Fatalf("a create whose record can't be written makes no server, but there are %d", n)
				}
				return
			}
			e.waitFor("the template's import to end", func() bool {
				sc, _ := e.srv().serverConfig()
				return sc != nil && sc.Template != nil && !sc.Template.Pending && e.onlineIdle()
			})
			recs, _ := e.srv().installedAddons()
			sc, _ := e.srv().serverConfig()
			audits := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'template.lost' AND server_id = ?`, e.sid)
			switch {
			case tc.want == installed && (len(recs) != 1 || recs[0].Name != "Chunky" || sc.Template.Lost || e.countRows(`SELECT COUNT(*) FROM template_installs`) != 0):
				t.Fatalf("the template's add-on must be installed and its import settled: %v %+v", recs, sc.Template)
			case tc.want == lost && (!sc.Template.Lost || len(recs) != 0 || audits != 1):
				t.Fatalf("the server page must say the template's record was lost: %+v, add-ons %v, %d audits", sc.Template, recs, audits)
			}
		})
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

// New server's library lists the templates this build opens, each with what
// it holds and its file, which plans like any other. A development build
// lists every one.
func TestTemplateLibraryListsWhatThisBuildOpens(t *testing.T) {
	e := newAgentEnv(t)
	var lib api.TemplateLibrary
	e.decode("GET", "/v1/templates/library", &lib)
	all, err := library.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Templates) != len(all) {
		t.Fatalf("a development build lists %d of the library's %d templates", len(lib.Templates), len(all))
	}
	for i, l := range lib.Templates {
		want := all[i]
		if l.ID != want.ID || l.Art != want.Art || l.Page != want.Page || l.Contents.Name != want.Name || l.Contents.Type == "" || l.Contents.MinecraftVersion == "" {
			t.Errorf("template %d is %+v, not the library's %s", i, l, want.ID)
		}
	}
	code, plan, raw := e.planTemplate(lib.Templates[0].File)
	if code != http.StatusOK || plan.Contents.Name != lib.Templates[0].Contents.Name {
		t.Errorf("planning %s: %d %v", lib.Templates[0].ID, code, raw)
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

// The fake CurseForge pack's ids, and a CurseForge API key for the machine.
const (
	cfPackID     = "9200001"
	cfPackFileID = "9300001"
	cfTestKey    = "fake-curseforge-key-0123456789abcdef"
)

// fakeCurseForgePack is a small Fabric pack on the fake CurseForge: a mod it
// downloads, a mod whose author allows downloads only through CurseForge's
// app, and a config file in its zip; and an older file of the pack itself
// that its author lets only CurseForge's app download.
type fakeCurseForgePack struct {
	sha1  string // of the pack's zip, as CurseForge lists it
	tools []byte // the mod it downloads
	// keptSHA1 is an older file of the pack, which its author lets only
	// CurseForge's app download.
	keptSHA1 string
}

func (f *fakeUpstream) serveCurseForgePack() *fakeCurseForgePack {
	f.t.Helper()
	const cf = "https://api.curseforge.com/v1"
	p := &fakeCurseForgePack{tools: []byte("fake mod stone tools")}
	manifest, err := json.Marshal(map[string]any{
		"minecraft":    map[string]any{"version": "26.2", "modLoaders": []any{map[string]any{"id": "fabric-0.17.2", "primary": true}}},
		"manifestType": "minecraftModpack", "manifestVersion": 1, "name": "Stone Pack", "version": "1.0", "author": "pip", "overrides": "overrides",
		"files": []any{map[string]any{"projectID": 9200002, "fileID": 9300002, "required": true}, map[string]any{"projectID": 9200003, "fileID": 9300003, "required": true}},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	zip := zipOf(f.t, map[string][]byte{"manifest.json": manifest, "overrides/config/stone.toml": []byte("stone = true\n")})
	p.sha1 = sha1Hex(zip)
	file := func(id, mod int, display, name string, body []byte, url string) map[string]any {
		return map[string]any{"id": id, "gameId": 432, "modId": mod, "isAvailable": true, "displayName": display, "fileName": name,
			"releaseType": 1, "fileStatus": 4, "hashes": []any{map[string]any{"value": sha1Hex(body), "algo": 1}},
			"fileDate": "2026-09-20T10:00:00Z", "fileLength": len(body), "downloadUrl": url, "gameVersions": []string{"26.2", "Fabric"}}
	}
	project := func(id, class int, section, name, slug string, distribution bool) map[string]any {
		return map[string]any{"id": id, "gameId": 432, "classId": class, "name": name, "slug": slug, "isAvailable": true, "allowModDistribution": distribution,
			"links": map[string]any{"websiteUrl": "https://www.curseforge.com/minecraft/" + section + "/" + slug}, "dateModified": "2026-09-20T10:00:00Z"}
	}
	packURL, toolsURL := "https://edge.forgecdn.net/files/9300/1/stone-pack-1.0.zip", "https://edge.forgecdn.net/files/9300/2/stone-tools-1.0.jar"
	pack, packProject := file(9300001, 9200001, "Stone Pack 1.0", "stone-pack-1.0.zip", zip, packURL), project(9200001, 4471, "modpacks", "Stone Pack", "stone-pack", true)
	packProject["latestFilesIndexes"] = []any{map[string]any{"gameVersion": "26.2", "fileId": 9300001, "filename": "stone-pack-1.0.zip", "releaseType": 1, "modLoader": 4}}
	f.serveJSON(cf+"/mods/"+cfPackID, map[string]any{"data": packProject})
	kept := file(9300009, 9200001, "Stone Pack 0.9", "stone-pack-0.9.zip", []byte("an older zip"), "")
	kept["fileDate"], p.keptSHA1 = "2026-08-20T10:00:00Z", sha1Hex([]byte("an older zip"))
	f.serveJSON(cf+"/mods/"+cfPackID+"/files", map[string]any{"data": []any{pack, kept}, "pagination": map[string]any{"index": 0, "pageSize": 50, "resultCount": 2, "totalCount": 2}})
	f.serveJSON(cf+"/mods/"+cfPackID+"/files/"+cfPackFileID, map[string]any{"data": pack})
	f.serveJSON(cf+"/mods/files", map[string]any{"data": []any{
		file(9300002, 9200002, "Stone Tools 1.0", "stone-tools-1.0.jar", p.tools, toolsURL),
		file(9300003, 9200003, "Kept Close 1.0", "kept-close-1.0.jar", []byte("fake mod kept close"), ""),
	}})
	f.serveJSON(cf+"/mods", map[string]any{"data": []any{
		project(9200002, 6, "mc-mods", "Stone Tools", "stone-tools", true), project(9200003, 6, "mc-mods", "Kept Close", "kept-close", false),
	}})
	f.serve(packURL, zip)
	f.serve(toolsURL, p.tools)
	return p
}

// curseForgeTemplate is a Fabric template with the fake CurseForge pack's
// file, pinned by hash.
func curseForgeTemplate(t *testing.T, fileID, hash string) string {
	t.Helper()
	file, err := templates.MarshalFile(&templates.Template{Format: templates.Format, Name: "Stone Pack", Game: templates.Game,
		Server:   templates.Server{Type: "fabric", MinecraftVersion: "26.2"},
		Settings: templates.Settings{MemoryMB: 1536},
		Modpack: &templates.Modpack{Source: modpacks.CurseForge, Project: cfPackID, Slug: "stone-pack", Name: "Stone Pack",
			Pin: templates.Pin{VersionID: fileID, VersionNumber: "Stone Pack 1.0", Channel: "release", HashAlgo: "sha1", Hash: hash}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(file)
}

// A template can carry a CurseForge modpack, but never a CurseForge key: a
// machine without one of its own says so and creates nothing. With one, the
// server installs the pack as New server does, keeping CurseForge's rules: a
// mod whose author allows downloads only through CurseForge's app is left
// for the user to add, never fetched another way. The server shares its
// pack as a template in turn, still without the key.
func TestTemplateCarriesACurseForgeModpack(t *testing.T) {
	e := newAgentEnv(t)
	pack := e.up.serveCurseForgePack()
	e.up.serveFabricLists()

	code, plan, raw := e.planTemplate(curseForgeTemplate(t, cfPackFileID, pack.sha1))
	if code != 200 || plan.Ready || !slices.Equal(noticeKinds(plan.Blockers), []string{string(modpacks.KindNoCurseForge)}) ||
		plan.Blockers[0].Message != "The template's modpack Stone Pack comes from CurseForge, and this Playkeeper has no CurseForge API key." {
		t.Fatalf("a machine without a CurseForge key: %d %+v %v", code, plan, raw)
	}
	if code, out := e.createFromTemplate(plan.Fingerprint, nil); code == 202 {
		t.Fatalf("a blocked template must not create a server: %v", out)
	}

	if err := os.WriteFile(e.a.curseForgeKeyFile(), []byte(cfTestKey), 0o600); err != nil {
		t.Fatal(err)
	}
	e.a.loadPacks()
	_, altered, _ := e.planTemplate(curseForgeTemplate(t, cfPackFileID, sha1Hex([]byte("another zip"))))
	if altered.Ready || !slices.Equal(noticeKinds(altered.Blockers), []string{string(templates.KindPinMismatch)}) ||
		altered.Blockers[0].Message != "The file CurseForge offers for Stone Pack Stone Pack 1.0 isn't the one the template names." {
		t.Fatalf("a template pinning another zip than CurseForge's: %+v", altered)
	}
	// Creating refuses a file CurseForge doesn't list, or one its author lets
	// only CurseForge's app download, so planning does too.
	for _, c := range []struct{ file, hash, kind, msg string }{
		{"9300999", pack.sha1, string(kindTemplatePackMissing), "Playkeeper can't install version Stone Pack 1.0 of Stone Pack, which the template names."},
		{"9300009", pack.keptSHA1, string(addons.KindExternal), "Stone Pack's author only allows downloading it through CurseForge's app, so Playkeeper cannot install it."},
	} {
		_, refused, _ := e.planTemplate(curseForgeTemplate(t, c.file, c.hash))
		if refused.Ready || !slices.Equal(noticeKinds(refused.Blockers), []string{c.kind}) || refused.Blockers[0].Message != c.msg {
			t.Fatalf("a template pinning file %s: %+v", c.file, refused)
		}
		if code, out := e.createFromTemplate(refused.Fingerprint, nil); code == 202 {
			t.Fatalf("a blocked template must not create a server: %v", out)
		}
	}
	code, plan, raw = e.planTemplate(curseForgeTemplate(t, cfPackFileID, pack.sha1))
	if code != 200 || !plan.Ready || plan.Type != "fabric" || plan.MinecraftVersion != "26.2" || plan.Contents.Modpack == nil ||
		plan.Contents.Modpack.Source != "curseforge" || plan.Contents.Modpack.VersionNumber != "Stone Pack 1.0" {
		t.Fatalf("a machine with a key: %d %+v %v", code, plan, raw)
	}
	code, out := e.createFromTemplate(plan.Fingerprint, nil)
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	// Fabric itself isn't on the fake upstream, so the first start stops at
	// its install, with the pack still to put in place.
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpFailed {
		t.Fatalf("the first start without Fabric: %+v", op)
	}
	sc, _ := e.srv().serverConfig()
	if m := sc.Modpack; m == nil || m.Source != "curseforge" || m.ProjectID != cfPackID || m.VersionID != cfPackFileID || !m.Pending ||
		sc.Software == nil || sc.Software.Type != "fabric" || sc.Software.FabricLoader != "0.17.2" {
		t.Fatalf("the new server runs the Fabric Loader the pack names and waits for its files: %+v %+v", sc.Modpack, sc.Software)
	}
	e.installedFabric()
	op := e.runOp("POST", "/start")
	if op.Status != api.OpSucceeded {
		t.Fatalf("start: %+v", op)
	}
	if got, err := os.ReadFile(filepath.Join(e.dataDir(), "mods", "stone-tools-1.0.jar")); err != nil || !bytes.Equal(got, pack.tools) {
		t.Fatalf("the mod CurseForge lets Playkeeper download: %q %v", got, err)
	}
	if got, _ := os.ReadFile(filepath.Join(e.dataDir(), "config", "stone.toml")); string(got) != "stone = true\n" {
		t.Fatalf("the pack's config: %q", got)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir(), "mods", "kept-close-1.0.jar")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a mod its author keeps to CurseForge's app is never fetched another way: %v", err)
	}
	manual, _ := json.Marshal(op.Detail["manual"])
	var steps []api.AddonNotice
	if err := json.Unmarshal(manual, &steps); err != nil || len(steps) != 1 || steps[0].Kind != string(addons.KindExternal) ||
		steps[0].URL != "https://www.curseforge.com/minecraft/mc-mods/kept-close/files/9300003" {
		t.Fatalf("the mod to add by hand, with its page: %s %v", manual, err)
	}
	if sc, _ = e.srv().serverConfig(); sc.Modpack.Pending {
		t.Fatalf("the pack is in place: %+v", sc.Modpack)
	}

	code, _, body := e.getBytes(e.sp("/template"))
	var exp api.TemplateExport
	if err := json.Unmarshal(body, &exp); code != 200 || err != nil {
		t.Fatalf("export: %d %v %s", code, err, body)
	}
	if m := exp.Contents.Modpack; m == nil || m.Source != "curseforge" || m.Name != "Stone Pack" || m.VersionNumber != "Stone Pack 1.0" || len(exp.LeftOut) != 0 {
		t.Fatalf("the server shares its CurseForge pack: %+v, left out %+v", exp.Contents.Modpack, exp.LeftOut)
	}
	shared, err := templates.DecodeLink(exp.Link)
	if err != nil {
		t.Fatal(err)
	}
	if m := shared.Modpack; m == nil || m.Source != modpacks.CurseForge || m.Project != cfPackID || m.Pin.VersionID != cfPackFileID || m.Pin.HashAlgo != "sha1" || m.Pin.Hash != pack.sha1 {
		t.Fatalf("the link's modpack: %+v", shared.Modpack)
	}
	linked, _ := json.Marshal(shared)
	if strings.Contains(string(body), cfTestKey) || strings.Contains(string(linked), cfTestKey) {
		t.Fatal("the template carries the machine's CurseForge key")
	}
	if code, again, raw := e.planTemplate(exp.Link); code != 200 || !again.Ready {
		t.Fatalf("the shared link plans again: %d %+v %v", code, again, raw)
	}
}
