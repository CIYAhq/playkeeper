package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// withSources restarts the agent with an add-on library that reaches only
// the fake sources.
func (e *agentEnv) withSources() *fakeSources {
	e.t.Helper()
	f := newFakeSources(e.t)
	e.stop()
	e.addons = f.library(e.t.TempDir())
	e.start()
	return f
}

func (e *agentEnv) addonList() api.Addons {
	e.t.Helper()
	var out api.Addons
	e.decode("GET", e.sp("/addons"), &out)
	return out
}

// addonOp starts an install or update and waits for it to finish.
func (e *agentEnv) addonOp(path string, body any) *api.Operation {
	e.t.Helper()
	code, out := e.call("POST", e.sp(path), body)
	if code != 202 {
		e.t.Fatalf("POST %s: %d %v", path, code, out)
	}
	return e.waitOp(out["id"].(string))
}

// opDetail decodes one entry of an operation's detail.
func opDetail[T any](t *testing.T, op *api.Operation, key string) T {
	t.Helper()
	var v T
	b, _ := json.Marshal(op.Detail[key])
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("operation detail %s: %v", key, err)
	}
	return v
}

// fileStatus is each jar in the add-on folder with its status, and "pending"
// when it loads at the next restart.
func fileStatus(files []api.AddonFile) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		out[f.FileName] = f.Status
		if f.Pending {
			out[f.FileName] += " pending"
		}
	}
	return out
}

func writeTestFile(t *testing.T, path string, data []byte, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

// A plugin installs with the dependency it needs, loads at the next restart,
// updates, and comes out again with the dependency nothing needs any more;
// a changed file or a dependency still in use is only removed when asked.
func TestAddonsInstallUpdateRemove(t *testing.T) {
	e := newAgentEnv(t)
	f := e.withSources()
	e.create()
	plugins := filepath.Join(e.dataDir(), "plugins")

	list := e.addonList()
	if list.Target.Kind != "plugin" || list.Target.Folder != "plugins" || list.Target.MinecraftVersion != "26.1.2" ||
		!slices.Equal(list.Target.Sources, []string{"modrinth", "hangar"}) || len(list.Files) != 0 || list.RestartNeeded {
		t.Fatalf("a new server's add-ons: %+v", list)
	}

	var found api.AddonBrowse
	e.decode("GET", e.sp("/addons/search?q=multiverse"), &found)
	var names []string
	for _, c := range found.Cards {
		names = append(names, c.Name)
		if c.Installed || c.Author != "sample-author" || !slices.Equal(c.Categories, []string{"management"}) {
			t.Errorf("card %+v", c)
		}
	}
	if !slices.Contains(names, "Multiverse-Portals") || !slices.Contains(names, "Multiverse-Core") || slices.Contains(names, "Chunky") || len(found.Unanswered) != 0 {
		t.Fatalf("searching for multiverse found %v (unanswered %v)", names, found.Unanswered)
	}

	var d api.AddonDetails
	e.decode("GET", e.sp("/addons/project/modrinth/mvportal"), &d)
	if d.Installed != nil || d.Latest == nil || d.Latest.VersionNumber != "5.0.0" || d.Notes != "Portals remember where they lead." || d.Plan == nil || !d.Plan.Ready {
		t.Fatalf("details before installing: %+v", d)
	}
	var steps []string
	for _, s := range d.Plan.Steps {
		steps = append(steps, s.Action+" "+s.Name+" "+s.VersionNumber+" for "+s.NeededBy)
	}
	if want := []string{"install Multiverse-Portals 5.0.0 for ", "install Multiverse-Core 5.0.1 for Multiverse-Portals"}; !slices.Equal(steps, want) {
		t.Fatalf("plan steps %q, want %q", steps, want)
	}

	op := e.addonOp("/addons/install", map[string]any{"source": "modrinth", "projectId": "mvportal", "fingerprint": d.Plan.Fingerprint, "actor": "admin"})
	if op.Status != api.OpSucceeded || op.Kind != "addon-install" {
		t.Fatalf("install: %+v", op)
	}
	var got []string
	for _, p := range opDetail[[]api.AddonProgress](t, op, "files") {
		got = append(got, p.Name+" "+p.State)
		if p.Received != p.Size || p.Size == 0 {
			t.Errorf("progress of %s: %d of %d bytes", p.Name, p.Received, p.Size)
		}
	}
	if want := []string{"Multiverse-Portals verified", "Multiverse-Core verified"}; !slices.Equal(got, want) {
		t.Fatalf("install progress %v, want %v", got, want)
	}
	if !opDetail[bool](t, op, "restartNeeded") {
		t.Fatal("installing on a running server did not say it needs a restart")
	}
	for _, name := range []string{"Multiverse-Portals-5.0.0.jar", "Multiverse-Core-5.0.1.jar"} {
		want := "mvp-v1"
		if strings.HasPrefix(name, "Multiverse-Core") {
			want = "mvc-v1"
		}
		_, data := f.jar(want)
		if b, err := os.ReadFile(filepath.Join(plugins, name)); err != nil || !bytes.Equal(b, data) {
			t.Fatalf("%s after the install: %v", name, err)
		}
	}
	if n := e.countRows(`SELECT COUNT(*) FROM addons WHERE server_id = ?`, e.sid); n != 2 {
		t.Fatalf("%d add-on records, want 2", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'addon.installed' AND result = 'succeeded' AND server_id = ?`, e.sid); n != 2 {
		t.Fatalf("%d install audit entries, want 2", n)
	}

	list = e.addonList()
	want := map[string]string{"Multiverse-Portals-5.0.0.jar": "managed pending", "Multiverse-Core-5.0.1.jar": "managed pending"}
	if got := fileStatus(list.Files); !maps.Equal(got, want) || !list.RestartNeeded {
		t.Fatalf("after the install: files %v restart %v, want %v and a restart", got, list.RestartNeeded, want)
	}
	for _, file := range list.Files {
		if a := file.Addon; a == nil || !strings.HasPrefix(file.FileName, a.Name+"-") || a.Summary == "" || a.IconURL == "" {
			t.Errorf("file %s has record %+v", file.FileName, file.Addon)
		}
	}
	found = api.AddonBrowse{}
	e.decode("GET", e.sp("/addons/search?q=multiverse"), &found)
	for _, c := range found.Cards {
		if !c.Installed {
			t.Errorf("%s does not show as installed", c.Name)
		}
	}

	var preview api.AddonRemovePreview
	e.decode("GET", e.sp("/addons/project/modrinth/mvcore00/removal"), &preview)
	if !slices.Equal(preview.NeededBy, []string{"Multiverse-Portals"}) || preview.Changed || preview.Missing {
		t.Fatalf("removal preview of the dependency: %+v", preview)
	}
	code, out := e.call("POST", e.sp("/addons/remove"), map[string]any{"source": "modrinth", "projectId": "mvcore00", "keepConfig": true, "actor": "admin"})
	if code != 409 || out["code"] != "needed_by" {
		t.Fatalf("removing a dependency in use: %d %v", code, out)
	}

	// A restart loads what was installed.
	code, out = e.call("POST", e.sp("/restart"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("restart: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("restart: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	list = e.addonList()
	want = map[string]string{"Multiverse-Portals-5.0.0.jar": "managed", "Multiverse-Core-5.0.1.jar": "managed"}
	if got := fileStatus(list.Files); !maps.Equal(got, want) || list.RestartNeeded {
		t.Fatalf("after the restart: files %v restart %v, want %v and no restart", got, list.RestartNeeded, want)
	}

	f.publish("mvcore00", "mvc-v2", "5.0.2", time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	var checks api.AddonChecks
	e.decode("GET", e.sp("/addons/checks"), &checks)
	updates := map[string]string{}
	for _, u := range checks.Updates {
		if u.Available && u.Latest != nil {
			updates[u.ProjectID] = u.Latest.VersionNumber
		}
	}
	if !maps.Equal(updates, map[string]string{"mvcore00": "5.0.2"}) || len(checks.Identified) != 0 {
		t.Fatalf("update checks: %+v", checks)
	}
	asked := f.hitCount("POST", "/v2/version_files/update")
	checks = api.AddonChecks{}
	e.decode("GET", e.sp("/addons/checks"), &checks)
	if n := f.hitCount("POST", "/v2/version_files/update"); n != asked {
		t.Fatalf("checking again asked Modrinth again (%d times, was %d)", n, asked)
	}
	var installed api.AddonDetails
	e.decode("GET", e.sp("/addons/project/modrinth/mvcore00"), &installed)
	if i := installed.Installed; i == nil || i.VersionNumber != "5.0.1" || !installed.UpdateAvailable || installed.Latest.VersionNumber != "5.0.2" || installed.Plan != nil {
		t.Fatalf("details of an installed add-on with an update: %+v", installed)
	}

	op = e.addonOp("/addons/update", map[string]any{"addons": []map[string]string{{"source": "modrinth", "projectId": "mvcore00"}}, "actor": "admin"})
	if op.Status != api.OpSucceeded || op.Kind != "addon-update" {
		t.Fatalf("update: %+v", op)
	}
	if p := opDetail[[]api.AddonProgress](t, op, "files"); len(p) != 1 || p[0].Was != "5.0.1" || p[0].VersionNumber != "5.0.2" || p[0].State != "verified" {
		t.Fatalf("update progress %+v", p)
	}
	if _, err := os.Stat(filepath.Join(plugins, "Multiverse-Core-5.0.1.jar")); !os.IsNotExist(err) {
		t.Fatalf("the old version is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(plugins, "Multiverse-Core-5.0.2.jar")); err != nil {
		t.Fatal(err)
	}
	var version, depOf string
	if err := e.a.db.QueryRow(`SELECT version_number, dependency_of FROM addons WHERE server_id = ? AND project_id = 'mvcore00'`, e.sid).Scan(&version, &depOf); err != nil ||
		version != "5.0.2" || depOf != "mvportal" {
		t.Fatalf("record after the update: %q for %q (%v)", version, depOf, err)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'addon.updated' AND detail = 'Multiverse-Core 5.0.1 → 5.0.2'`); n != 1 {
		t.Fatalf("%d update audit entries, want 1", n)
	}

	portals := filepath.Join(plugins, "Multiverse-Portals-5.0.0.jar")
	b, _ := os.ReadFile(portals)
	writeTestFile(t, portals, append(b, "edited by hand"...), time.Time{})
	if got := fileStatus(e.addonList().Files); got["Multiverse-Portals-5.0.0.jar"] != "modified" {
		t.Fatalf("a changed file shows as %q", got["Multiverse-Portals-5.0.0.jar"])
	}
	preview = api.AddonRemovePreview{}
	e.decode("GET", e.sp("/addons/project/modrinth/mvportal/removal"), &preview)
	if !preview.Changed || len(preview.Orphans) != 1 || preview.Orphans[0].ProjectID != "mvcore00" {
		t.Fatalf("removal preview of a changed plugin: %+v", preview)
	}
	code, out = e.call("POST", e.sp("/addons/remove"), map[string]any{"source": "modrinth", "projectId": "mvportal", "actor": "admin"})
	if code != 409 || out["code"] != "modified" {
		t.Fatalf("removing a changed file without asking: %d %v", code, out)
	}
	code, out = e.call("POST", e.sp("/addons/remove"), map[string]any{"source": "modrinth", "projectId": "mvportal", "changed": true, "actor": "admin",
		"orphans": []map[string]string{{"source": "modrinth", "projectId": "fALzjamp"}}})
	if code != 400 {
		t.Fatalf("removing an add-on that is not an orphan along with it: %d %v", code, out)
	}
	var removal api.AddonRemoval
	code, out = e.call("POST", e.sp("/addons/remove"), map[string]any{"source": "modrinth", "projectId": "mvportal", "changed": true, "actor": "admin",
		"orphans": []map[string]string{{"source": "modrinth", "projectId": "mvcore00"}}})
	if b, _ := json.Marshal(out); code != 200 || json.Unmarshal(b, &removal) != nil || !slices.Equal(removal.Removed, []string{"Multiverse-Portals", "Multiverse-Core"}) {
		t.Fatalf("removing the plugin and its orphan: %d %v", code, out)
	}
	if left, _ := filepath.Glob(filepath.Join(plugins, "*.jar")); len(left) != 0 {
		t.Fatalf("left in the plugins folder: %v", left)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM addons WHERE server_id = ?`, e.sid); n != 0 {
		t.Fatalf("%d records left", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'addon.removed' AND result = 'succeeded'`); n != 2 {
		t.Fatalf("%d removal audit entries, want 2", n)
	}
	if !e.addonList().RestartNeeded {
		t.Fatal("removing from a running server did not say it needs a restart")
	}
}

// A jar added by hand that Modrinth knows can be handed to Playkeeper; a
// record whose file is gone can be forgotten.
func TestAddonAdoptAndForget(t *testing.T) {
	e := newAgentEnv(t)
	f := e.withSources()
	e.addIdleServer()
	plugins := filepath.Join(e.dataDir(), "plugins")
	name, data := f.jar("chunky-v1")
	since := time.Now().Add(-time.Hour).Truncate(time.Second)
	writeTestFile(t, filepath.Join(plugins, name), data, since)
	writeTestFile(t, filepath.Join(plugins, "HomeGrown.jar"), pluginJar(t, "HomeGrown", "0.1"), time.Time{})

	list := e.addonList()
	if got := fileStatus(list.Files); !maps.Equal(got, map[string]string{name: "unknown", "HomeGrown.jar": "unknown"}) {
		t.Fatalf("files added by hand: %v", got)
	}
	var checks api.AddonChecks
	e.decode("GET", e.sp("/addons/checks"), &checks)
	if len(checks.Identified) != 1 || checks.Identified[0].FileName != name || checks.Identified[0].Addon == nil || checks.Identified[0].Addon.ProjectID != "fALzjamp" {
		t.Fatalf("identified: %+v", checks.Identified)
	}

	code, out := e.call("POST", e.sp("/addons/adopt"), map[string]any{"fileName": "HomeGrown.jar", "actor": "admin"})
	if code != 409 {
		t.Fatalf("adopting a file Modrinth does not know: %d %v", code, out)
	}
	code, out = e.call("POST", e.sp("/addons/adopt"), map[string]any{"fileName": "Gone.jar", "actor": "admin"})
	if code != 404 {
		t.Fatalf("adopting a file that is not there: %d %v", code, out)
	}
	code, out = e.call("POST", e.sp("/addons/adopt"), map[string]any{"fileName": name, "actor": "admin"})
	if code != 200 || out["projectId"] != "fALzjamp" || out["versionNumber"] != "1.4.40" {
		t.Fatalf("adopting Chunky: %d %v", code, out)
	}
	var installedAt int64
	if err := e.a.db.QueryRow(`SELECT installed_at FROM addons WHERE server_id = ? AND project_id = 'fALzjamp'`, e.sid).Scan(&installedAt); err != nil || installedAt != since.UnixMilli() {
		t.Fatalf("an adopted file counts as installed at %d, want its time in the folder %d (%v)", installedAt, since.UnixMilli(), err)
	}
	if got := fileStatus(e.addonList().Files); got[name] != "managed" {
		t.Fatalf("an adopted file shows as %q", got[name])
	}
	if code, out = e.call("POST", e.sp("/addons/adopt"), map[string]any{"fileName": name, "actor": "admin"}); code != 409 {
		t.Fatalf("adopting a managed file again: %d %v", code, out)
	}
	if e.countRows(`SELECT addons_changed_at IS NULL FROM servers WHERE id = ?`, e.sid) != 1 {
		t.Fatal("adopting a file that was already there asks for a restart")
	}

	forget := map[string]any{"source": "modrinth", "projectId": "fALzjamp", "actor": "admin"}
	if code, out = e.call("POST", e.sp("/addons/forget"), forget); code != 409 {
		t.Fatalf("forgetting an add-on whose file is there: %d %v", code, out)
	}
	if err := os.Remove(filepath.Join(plugins, name)); err != nil {
		t.Fatal(err)
	}
	list = e.addonList()
	if len(list.Missing) != 1 || list.Missing[0].Name != "Chunky" {
		t.Fatalf("missing after deleting the file: %+v", list.Missing)
	}
	if code, out = e.call("POST", e.sp("/addons/forget"), forget); code != 200 {
		t.Fatalf("forgetting a missing add-on: %d %v", code, out)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM addons`); n != 0 {
		t.Fatalf("%d records after forgetting", n)
	}
	if code, out = e.call("POST", e.sp("/addons/forget"), forget); code != 404 {
		t.Fatalf("forgetting twice: %d %v", code, out)
	}
	for _, action := range []string{"addon.adopted", "addon.forgotten"} {
		if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = ? AND result = 'succeeded'`, action); n != 1 {
			t.Errorf("%d %s audit entries, want 1", n, action)
		}
	}
}

func TestAddonRoutesRejectBadInput(t *testing.T) {
	e := newAgentEnv(t)
	f := e.withSources()
	e.addIdleServer()
	install := func(extra map[string]any) map[string]any {
		body := map[string]any{"source": "modrinth", "projectId": "mvcore00", "actor": "admin"}
		for k, v := range extra {
			body[k] = v
		}
		return body
	}
	var many []map[string]string
	for range 201 {
		many = append(many, map[string]string{"source": "modrinth", "projectId": "mvcore00"})
	}
	bad := []struct {
		name, method, path string
		body               any
		want               int
	}{
		{"unknown source", "GET", e.sp("/addons/project/curseforge/abc"), nil, 400},
		{"traversal project", "GET", e.sp("/addons/project/modrinth/..%2F..%2Fetc"), nil, 400},
		{"traversal removal", "GET", e.sp("/addons/project/modrinth/..%2Fx/removal"), nil, 400},
		{"unknown project", "GET", e.sp("/addons/project/modrinth/nosuchthing"), nil, 404},
		{"long search", "GET", e.sp("/addons/search?q=" + strings.Repeat("a", 101)), nil, 400},
		{"control character in search", "GET", e.sp("/addons/search?q=a%00b"), nil, 400},
		{"negative page", "GET", e.sp("/addons/search?page=-1"), nil, 400},
		{"page beyond the end", "GET", e.sp("/addons/search?page=801"), nil, 400},
		{"unknown category", "GET", e.sp("/addons/search?category=nope"), nil, 400},
		{"unknown order", "GET", e.sp("/addons/search?sort=chaos"), nil, 400},
		{"bad fingerprint", "POST", e.sp("/addons/install"), install(map[string]any{"fingerprint": "zz"}), 400},
		{"install without actor", "POST", e.sp("/addons/install"), install(map[string]any{"actor": ""}), 400},
		{"install from a file address", "POST", e.sp("/addons/install"), `{"source":"modrinth","projectId":"mvcore00","actor":"admin","url":"https://203.0.113.9/x.jar"}`, 400},
		{"install from an unknown source", "POST", e.sp("/addons/install"), install(map[string]any{"source": "curseforge"}), 400},
		{"install a traversal project", "POST", e.sp("/addons/install"), install(map[string]any{"projectId": "../../x"}), 400},
		{"too many updates", "POST", e.sp("/addons/update"), map[string]any{"addons": many, "actor": "admin"}, 400},
		{"traversal adopt", "POST", e.sp("/addons/adopt"), map[string]any{"fileName": "../../etc/x.jar", "actor": "admin"}, 400},
		{"adopt a file that is not a jar", "POST", e.sp("/addons/adopt"), map[string]any{"fileName": "server.properties", "actor": "admin"}, 400},
		{"remove what was never installed", "POST", e.sp("/addons/remove"), install(nil), 404},
		{"forget what was never installed", "POST", e.sp("/addons/forget"), install(nil), 404},
		{"icon without an address", "GET", "/v1/addons/icon", nil, 400},
		{"icon over plain HTTP", "GET", "/v1/addons/icon?url=" + url.QueryEscape("http://cdn.modrinth.com/x.png"), nil, 400},
		{"icon from another host", "GET", "/v1/addons/icon?url=" + url.QueryEscape("https://203.0.113.9/x.png"), nil, 400},
	}
	for _, c := range bad {
		if code, out := e.call(c.method, c.path, c.body); code != c.want {
			t.Errorf("%s: %s %s -> %d %v, want %d", c.name, c.method, c.path, code, out, c.want)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for hit := range f.hits {
		if strings.Contains(hit, "/cdn/") {
			t.Errorf("a refused request downloaded %s", hit)
		}
	}
	if _, err := os.Stat(filepath.Join(e.dataDir(), "plugins")); !os.IsNotExist(err) {
		t.Errorf("refused requests made the plugins folder: %v", err)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM operations WHERE kind LIKE 'addon-%'`); n != 0 {
		t.Errorf("refused requests started %d operations", n)
	}
}

// Icons come only from the sources' hosts and only as images, and are
// fetched once.
func TestAddonIconsAreCachedAndChecked(t *testing.T) {
	e := newAgentEnv(t)
	f := e.withSources()
	get := func(u string) (*http.Response, []byte) {
		t.Helper()
		resp, err := http.Get(e.ts.URL + "/v1/addons/icon?url=" + url.QueryEscape(u))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}
	icon := f.modrinth.URL + "/icons/mvcore00.png"
	for range 2 {
		resp, b := get(icon)
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || resp.Header.Get("Cache-Control") != "private, max-age=86400" ||
			resp.Header.Get("X-Content-Type-Options") != "nosniff" || !bytes.Equal(b, f.icons["/icons/mvcore00.png"]) {
			t.Fatalf("icon: %d %v (%d bytes)", resp.StatusCode, resp.Header, len(b))
		}
	}
	if n := f.hitCount("GET", "/icons/mvcore00.png"); n != 1 {
		t.Fatalf("the icon was fetched %d times, want once", n)
	}
	if resp, b := get(f.modrinth.URL + "/icons/evil.svg"); resp.StatusCode != 400 || !strings.Contains(string(b), "icon_refused") {
		t.Fatalf("an SVG icon: %d %s", resp.StatusCode, b)
	}
	if resp, _ := get(f.modrinth.URL + "/icons/none.png"); resp.StatusCode != 404 {
		t.Fatalf("a missing icon: %d", resp.StatusCode)
	}
}
