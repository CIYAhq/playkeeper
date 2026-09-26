package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"image"
	"image/png"
	"io"
	"maps"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/packs"
)

// fakeDataPacks is the datapack command of the fake Paper server, for the
// current server's world. Like the game, it finds the packs in the world's
// datapacks folder when it lists the available ones.
type fakeDataPacks struct {
	dir string

	mu sync.Mutex
	// found are the packs the last scan found; enabled are the enabled
	// ones, in order.
	found   map[string]bool
	enabled []string
	// needFeatures are packs that need an experimental feature the world
	// was created without.
	needFeatures map[string]bool
	reloads      int
	// strange are the datapack commands the fake doesn't know.
	strange []string
}

var reDatapackSwitch = regexp.MustCompile(`^minecraft:datapack (enable|disable) "(file/[^"\\]+)"( after "([^"\\]*)")?$`)

// paperList is Paper's answer to "datapack list enabled" or "datapack list
// available" (which) with the packs ids. Paper renders at most 33 pieces of
// a message's text and ends a longer one in "...", and each pack takes
// seven: ", ", "[", its ID, " (", its source, ")" and "]".
func paperList(which string, ids []string) string {
	pieces := []string{"There are ", strconv.Itoa(len(ids)), " data pack(s) " + which + ": "}
	for i, id := range ids {
		if i > 0 {
			pieces = append(pieces, ", ")
		}
		source := "world"
		if !strings.HasPrefix(id, "file/") {
			source = "built-in"
		}
		pieces = append(pieces, "[", id, " (", source, ")", "]")
	}
	if len(pieces) > 33 {
		return strings.Join(pieces[:33], "") + "..."
	}
	return strings.Join(pieces, "")
}

// dataPackConsole puts the datapack command on the current server's console.
func (e *agentEnv) dataPackConsole() *fakeDataPacks {
	e.t.Helper()
	fp := &fakeDataPacks{dir: filepath.Join(e.dataDir(), "world", "datapacks"), found: map[string]bool{}, needFeatures: map[string]bool{}}
	e.rcon.mu.Lock()
	e.rcon.answer = fp.answer
	e.rcon.mu.Unlock()
	e.t.Cleanup(func() {
		fp.mu.Lock()
		defer fp.mu.Unlock()
		if len(fp.strange) > 0 {
			e.t.Errorf("the datapack command got commands it doesn't know: %q", fp.strange)
		}
	})
	return fp
}

func (fp *fakeDataPacks) answer(cmd string) (string, bool) {
	if !strings.HasPrefix(cmd, "minecraft:datapack ") && cmd != "minecraft:reload" {
		return "", false
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	switch cmd {
	case "minecraft:reload":
		fp.reloads++
		return "Reloading!", true
	case "minecraft:datapack list enabled":
		return paperList("enabled", slices.Concat([]string{"vanilla"}, fp.enabled, []string{"paper"})), true
	case "minecraft:datapack list available":
		fp.found = map[string]bool{}
		entries, _ := os.ReadDir(fp.dir)
		var ids []string
		for _, e := range entries {
			id := packs.DataPackID(e.Name())
			if strings.HasSuffix(e.Name(), ".zip") {
				fp.found[id] = true
				if !slices.Contains(fp.enabled, id) {
					ids = append(ids, id)
				}
			}
		}
		if len(ids) == 0 {
			return "There are no more data packs available", true
		}
		return paperList("available", ids), true
	}
	m := reDatapackSwitch.FindStringSubmatch(cmd)
	if m == nil || m[3] != "" && (m[1] != "enable" || m[4] != "") {
		fp.strange = append(fp.strange, cmd)
		return "", true
	}
	id, on := m[2], slices.Contains(fp.enabled, m[2])
	switch {
	case !fp.found[id] && !on:
		return fmt.Sprintf("Unknown data pack '%s'", id), true
	case m[1] == "enable" && on:
		return fmt.Sprintf("Pack '%s' is already enabled!", id), true
	case m[1] == "enable" && fp.needFeatures[id]:
		return fmt.Sprintf("Pack '%s' cannot be enabled, since required flags are not enabled in this world: minecraft:trade_rebalance!", id), true
	case m[1] == "enable" && m[3] != "":
		// The game checks the pack before the one to put it after, and no
		// pack is named "".
		return "Unknown data pack ''", true
	case m[1] == "enable":
		fp.enabled = append(fp.enabled, id)
		return fmt.Sprintf("Enabling data pack [%s (world)]", id), true
	case !on:
		return fmt.Sprintf("Pack '%s' is not enabled!", id), true
	}
	fp.enabled = slices.DeleteFunc(fp.enabled, func(s string) bool { return s == id })
	return fmt.Sprintf("Disabling data pack [%s (world)]", id), true
}

func (fp *fakeDataPacks) needs(id string) {
	fp.mu.Lock()
	fp.needFeatures[id] = true
	fp.mu.Unlock()
}

func (fp *fakeDataPacks) reloadCount() int {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return fp.reloads
}

// zipOf is a zip of files.
func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(files[name])
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func iconPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// dataPackZip is a data pack with a description, and with an icon when
// icon is set.
func dataPackZip(t *testing.T, description string, icon bool) []byte {
	t.Helper()
	files := map[string][]byte{
		"pack.mcmeta":                         fmt.Appendf(nil, `{"pack": {"description": %q, "pack_format": 48}}`, description),
		"data/test/function/hello.mcfunction": []byte("say hello\n"),
	}
	if icon {
		files["pack.png"] = iconPNG(t)
	}
	return zipOf(t, files)
}

func resourcePackZip(t *testing.T, description string, icon bool) []byte {
	t.Helper()
	files := map[string][]byte{
		"pack.mcmeta": fmt.Appendf(nil, `{"pack": {"description": %q, "pack_format": 34}}`, description),
		"assets/minecraft/textures/block/stone.png": iconPNG(t),
	}
	if icon {
		files["pack.png"] = iconPNG(t)
	}
	return zipOf(t, files)
}

func sha1Hex(b []byte) string {
	sum := sha1.Sum(b)
	return hex.EncodeToString(sum[:])
}

// decodeAs converts a decoded JSON object into a T.
func decodeAs[T any](t *testing.T, m map[string]any) T {
	t.Helper()
	var v T
	b, _ := json.Marshal(m)
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// addDataPack uploads a data pack as the panel does, with its file name.
func (e *agentEnv) addDataPack(fileName string, zip []byte) api.DataPacks {
	e.t.Helper()
	code, out := e.uploadTo(e.sp("/datapacks?name="+url.QueryEscape(fileName)), zip)
	if code != 200 {
		e.t.Fatalf("add data pack %s: %d %v", fileName, code, out)
	}
	return decodeAs[api.DataPacks](e.t, out)
}

func (e *agentEnv) dataPackList() api.DataPacks {
	e.t.Helper()
	var out api.DataPacks
	e.decode("GET", e.sp("/datapacks"), &out)
	return out
}

// switchDataPack enables or disables a pack and returns the list the route
// answers with.
func (e *agentEnv) switchDataPack(name, action string) api.DataPacks {
	e.t.Helper()
	code, out := e.call("POST", e.sp("/datapacks/"+name+"/"+action), map[string]any{"actor": "admin"})
	if code != 200 {
		e.t.Fatalf("%s %s: %d %v", action, name, code, out)
	}
	return decodeAs[api.DataPacks](e.t, out)
}

func packNamed(list api.DataPacks, name string) *api.DataPack {
	for i := range list.Packs {
		if list.Packs[i].Name == name {
			return &list.Packs[i]
		}
	}
	return nil
}

// enabledState is a pack's switch as a word, for messages.
func enabledState(p *api.DataPack) string {
	switch {
	case p == nil:
		return "missing"
	case p.Enabled == nil:
		return "unknown"
	case *p.Enabled:
		return "on"
	}
	return "off"
}

func (e *agentEnv) audits(action string) int {
	return e.countRows(`SELECT COUNT(*) FROM audit WHERE action = ? AND server_id = ? AND result = 'succeeded'`, action, e.sid)
}

// getBytes fetches an agent route's body.
func (e *agentEnv) getBytes(path string) (int, http.Header, []byte) {
	e.t.Helper()
	resp, err := http.Get(e.ts.URL + path)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

// On a running server an uploaded data pack is switched on at once, a
// replaced one is reloaded, and the owner switches packs on and off and
// removes them through the server's console.
func TestDataPacksOnARunningServer(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	fp := e.dataPackConsole()

	graves := dataPackZip(t, "Keeps your items", true)
	list := e.addDataPack("Graves v2.zip", graves)
	p := packNamed(list, "Graves_v2.zip")
	if list.Added != "Graves_v2.zip" || !list.Live || list.NotEnabled || enabledState(p) != "on" {
		t.Fatalf("the added pack: %+v, pack %s", list, enabledState(p))
	}
	if p.Description != "Keeps your items" || !p.Icon || p.Size != int64(len(graves)) || p.Folder || p.AddedAt.IsZero() {
		t.Fatalf("the pack's details: %+v", p)
	}
	if b, err := os.ReadFile(filepath.Join(e.dataDir(), "world", "datapacks", "Graves_v2.zip")); err != nil || !bytes.Equal(b, graves) {
		t.Fatalf("the pack in the world's datapacks folder: %v", err)
	}
	if n := e.rcon.count(`minecraft:datapack enable "file/Graves_v2.zip"`); n != 1 {
		t.Fatalf("the pack was enabled %d times", n)
	}
	code, hdr, icon := e.getBytes(e.sp("/datapacks/Graves_v2.zip/icon"))
	if code != 200 || hdr.Get("Content-Type") != "image/png" || !bytes.Equal(icon, iconPNG(t)) {
		t.Fatalf("the pack's icon: %d %s", code, hdr.Get("Content-Type"))
	}

	// Replacing an enabled pack reloads the server's data instead.
	list = e.addDataPack("Graves v2.zip", dataPackZip(t, "Keeps your items and XP", false))
	if p := packNamed(list, "Graves_v2.zip"); len(list.Packs) != 1 || p.Description != "Keeps your items and XP" || p.Icon || enabledState(p) != "on" {
		t.Fatalf("the replaced pack: %+v", list)
	}
	if fp.reloadCount() != 1 || e.rcon.count(`minecraft:datapack enable "file/Graves_v2.zip"`) != 1 {
		t.Fatalf("replacing an enabled pack: %d reloads", fp.reloadCount())
	}
	if code, _, _ := e.getBytes(e.sp("/datapacks/Graves_v2.zip/icon")); code != 404 {
		t.Fatalf("the icon of a pack without one: %d", code)
	}

	if p := packNamed(e.switchDataPack("Graves_v2.zip", "disable"), "Graves_v2.zip"); enabledState(p) != "off" {
		t.Fatalf("after disabling: %s", enabledState(p))
	}
	if p := packNamed(e.switchDataPack("Graves_v2.zip", "enable"), "Graves_v2.zip"); enabledState(p) != "on" {
		t.Fatalf("after enabling: %s", enabledState(p))
	}

	// A pack the world can't enable stays installed, switched off, and the
	// answer says why.
	fp.needs("file/Villager_Trades.zip")
	list = e.addDataPack("Villager Trades.zip", dataPackZip(t, "Rebalanced trades", false))
	if p := packNamed(list, "Villager_Trades.zip"); list.Added != "Villager_Trades.zip" || !list.NotEnabled || enabledState(p) != "off" ||
		!strings.Contains(list.Problem, "needs experimental features this world was created without: minecraft:trade_rebalance") {
		t.Fatalf("a pack that needs experimental features: %+v", list)
	}
	code, out := e.call("POST", e.sp("/datapacks/Villager_Trades.zip/enable"), map[string]any{"actor": "admin"})
	if code != 409 || out["code"] != packs.CodeNeedsFeatures {
		t.Fatalf("enabling it: %d %v", code, out)
	}

	// Removing an enabled pack disables it first.
	code, out = e.call("DELETE", e.sp("/datapacks/Graves_v2.zip?actor=admin"), nil)
	if code != 200 || len(out["packs"].([]any)) != 1 {
		t.Fatalf("remove: %d %v", code, out)
	}
	if n := e.rcon.count(`minecraft:datapack disable "file/Graves_v2.zip"`); n != 2 {
		t.Fatalf("the removed pack was disabled %d times in all", n)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir(), "world", "datapacks", "Graves_v2.zip")); !os.IsNotExist(err) {
		t.Fatalf("the removed pack's file: %v", err)
	}
	for action, want := range map[string]int{"datapack.added": 2, "datapack.replaced": 1, "datapack.disabled": 1, "datapack.enabled": 1, "datapack.removed": 1} {
		if n := e.audits(action); n != want {
			t.Errorf("%d %s audit entries, want %d", n, action, want)
		}
	}
	for path, want := range map[string]int{"/datapacks/Graves_v2.zip/enable": 404, "/datapacks/Graves_v2.zip/disable": 404} {
		if code, out := e.call("POST", e.sp(path), map[string]any{"actor": "admin"}); code != want || out["code"] != packs.CodeNotFound {
			t.Errorf("%s after removal: %d %v", path, code, out)
		}
	}
}

// Paper's lists of packs stop after four packs or so, yet seven uploaded
// packs all switch on, and each one's switch shows its state.
func TestManyDataPacks(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	fp := e.dataPackConsole()
	names := []string{"Multiplayer_sleep", "Graves", "More_mob_heads", "Coordinates_HUD", "Armor_statues", "Villager_workstations", "Double_shulker_shells"}
	for _, n := range names {
		list := e.addDataPack(n+".zip", dataPackZip(t, n, false))
		if p := packNamed(list, n+".zip"); !list.Live || list.NotEnabled || list.Problem != "" || enabledState(p) != "on" {
			t.Fatalf("adding %s: %+v, pack %s", n, list, enabledState(p))
		}
	}
	// Paper 26.2's answer with the same packs enabled.
	const paper = "There are 9 data pack(s) enabled: [vanilla (built-in)], [file/Multiplayer_sleep.zip (world)], [file/Graves.zip (world)], [file/More_mob_heads.zip (world)], [file/Coordinates_HUD.zip..."
	if got, _ := fp.answer("minecraft:datapack list enabled"); got != paper {
		t.Fatalf("the fake's list of enabled packs:\n%s\nwant Paper's:\n%s", got, paper)
	}

	states := func(list api.DataPacks) []string {
		var out []string
		for _, n := range names {
			out = append(out, n+" "+enabledState(packNamed(list, n+".zip")))
		}
		return out
	}
	allOnBut := func(off string) []string {
		var out []string
		for _, n := range names {
			out = append(out, n+map[bool]string{true: " off", false: " on"}[n == off])
		}
		return out
	}
	if list := e.dataPackList(); !list.Live || !slices.Equal(states(list), allOnBut("")) {
		t.Fatalf("the listed packs: live %v, %q", list.Live, states(list))
	}
	if got := states(e.switchDataPack("Double_shulker_shells.zip", "disable")); !slices.Equal(got, allOnBut("Double_shulker_shells")) {
		t.Fatalf("after switching the seventh off: %q", got)
	}
	if got := states(e.dataPackList()); !slices.Equal(got, allOnBut("Double_shulker_shells")) {
		t.Fatalf("listed after switching the seventh off: %q", got)
	}
	if got := states(e.switchDataPack("Double_shulker_shells.zip", "enable")); !slices.Equal(got, allOnBut("")) {
		t.Fatalf("after switching it on again: %q", got)
	}
}

// Folder packs are listed and left alone; the one Paper keeps in every
// world for its plugins isn't listed at all, and one whose name can't be
// sent to the console has no known state.
func TestFolderDataPacks(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.dataPackConsole()
	dir := filepath.Join(e.dataDir(), "world", "datapacks")
	for _, name := range []string{"bukkit", "Hand Made", "Tab\tMade"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, name, "pack.mcmeta"), []byte(`{"pack": {"description": "Made by hand", "pack_format": 48}}`), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "Hand Made", "pack.png"), iconPNG(t), 0o644)
	list := e.dataPackList()
	if len(list.Packs) != 2 || !list.Live {
		t.Fatalf("the listed packs: live %v, %+v", list.Live, list.Packs)
	}
	if p := list.Packs[0]; p.Name != "Hand Made" || !p.Folder || p.Description != "Made by hand" || !p.Icon || p.Size != 0 || enabledState(&p) != "off" {
		t.Fatalf("the folder pack: %+v", p)
	}
	if p := list.Packs[1]; p.Name != "Tab\tMade" || !p.Folder || enabledState(&p) != "unknown" {
		t.Fatalf("the folder pack with a tab in its name: %+v", p)
	}
	if code, out := e.call("DELETE", e.sp("/datapacks/"+url.PathEscape("Hand Made")+"?actor=admin"), nil); code != 409 || out["code"] != packs.CodeFolderPack {
		t.Fatalf("removing a folder pack: %d %v", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "Hand Made", "pack.mcmeta")); err != nil {
		t.Fatalf("the folder pack after a refused removal: %v", err)
	}
	if code, out := e.call("POST", e.sp("/datapacks/bukkit/disable"), map[string]any{"actor": "admin"}); code != 404 {
		t.Fatalf("Paper's own pack: %d %v", code, out)
	}
}

// A stopped server takes and loses data packs without its console; it
// can't switch them on or off until it runs.
func TestDataPacksOnAStoppedServer(t *testing.T) {
	e := newAgentEnv(t)
	e.addIdleServer()
	list := e.addDataPack("Terralith.zip", dataPackZip(t, "Explore new biomes", false))
	if p := packNamed(list, "Terralith.zip"); list.Live || list.NotEnabled || list.Added != "Terralith.zip" || enabledState(p) != "unknown" {
		t.Fatalf("a pack added to a stopped server: %+v", list)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir(), "world", "datapacks", "Terralith.zip")); err != nil {
		t.Fatalf("the pack for the world the server generates: %v", err)
	}
	code, out := e.call("POST", e.sp("/datapacks/Terralith.zip/disable"), map[string]any{"actor": "admin"})
	if code != 409 || out["code"] != "offline" || !strings.Contains(out["error"].(string), "isn't online, so its data packs can't be switched on or off.") {
		t.Fatalf("switching a pack on a stopped server: %d %v", code, out)
	}
	if code, out := e.call("DELETE", e.sp("/datapacks/Terralith.zip?actor=admin"), nil); code != 200 || len(out["packs"].([]any)) != 0 {
		t.Fatalf("remove: %d %v", code, out)
	}
	e.rcon.mu.Lock()
	sent := len(e.rcon.commands)
	e.rcon.mu.Unlock()
	if sent != 0 {
		t.Fatalf("a stopped server's console got commands")
	}

	for name, tc := range map[string]struct {
		body   []byte
		header bool
		code   int
		want   string
	}{
		"no actor":      {dataPackZip(t, "x", false), false, 400, api.CodeInvalid},
		"not a zip":     {[]byte("just text"), true, 400, packs.CodeNotZip},
		"resource pack": {resourcePackZip(t, "x", false), true, 400, packs.CodeWrongKind},
	} {
		req, _ := http.NewRequest("POST", e.ts.URL+e.sp("/datapacks?name=x.zip"), bytes.NewReader(tc.body))
		if tc.header {
			req.Header.Set("X-Playkeeper-Actor", "admin")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body api.Error
		json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != tc.code || body.Code != tc.want {
			t.Errorf("%s: %d %+v", name, resp.StatusCode, body)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(e.dataDir(), "world", "datapacks")); len(entries) != 0 {
		t.Fatalf("refused uploads left files: %v", entries)
	}
	if n := e.audits("datapack.added"); n != 1 {
		t.Fatalf("%d datapack.added audit entries", n)
	}
}

func (e *agentEnv) resourcePack() api.ResourcePack {
	e.t.Helper()
	var out api.ResourcePack
	e.decode("GET", e.sp("/resourcepack"), &out)
	return out
}

// offerPack uploads a resource pack as the panel does, saying where
// players' games reach the panel.
func (e *agentEnv) offerPack(fileName string, zip []byte) api.ResourcePack {
	e.t.Helper()
	code, out := e.uploadTo(e.sp("/resourcepack?host=203.0.113.10&port=8443&name="+url.QueryEscape(fileName)), zip)
	if code != 200 {
		e.t.Fatalf("offer %s: %d %v", fileName, code, out)
	}
	return decodeAs[api.ResourcePack](e.t, out)
}

func (e *agentEnv) activePacks() []string {
	e.t.Helper()
	code, out := e.call("GET", "/v1/resource-packs/active", nil)
	if code != 200 {
		e.t.Fatalf("active packs: %d %v", code, out)
	}
	if out["sha1"] == nil {
		e.t.Fatalf("active packs are null: %v", out)
	}
	return decodeAs[api.ActiveResourcePacks](e.t, out).SHA1
}

// containerEnv is the resource pack environment of the current server's
// container.
func (e *agentEnv) containerEnv() map[string]string {
	e.t.Helper()
	c, err := e.a.docker.ContainerInspect(context.Background(), e.cname())
	if err != nil {
		e.t.Fatal(err)
	}
	return packEnv(c.Config.Env)
}

func (e *agentEnv) storedPacks() []string {
	e.t.Helper()
	entries, err := os.ReadDir(e.cfg.ResourcePacksDir())
	if err != nil && !os.IsNotExist(err) {
		e.t.Fatal(err)
	}
	names := []string{}
	for _, en := range entries {
		names = append(names, en.Name())
	}
	return names
}

// Players' games download resource packs from the dashboard on the pack's
// machine. A machine installed to join another dashboard has none, so it
// refuses a pack rather than offer players one they can't download.
func TestAMachineWithoutADashboardRefusesResourcePacks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		noPanel bool
		code    int
	}{
		{"a machine with its own dashboard", false, 200},
		{"a machine installed to join another dashboard", true, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgentEnvWith(t, func(e *agentEnv) { e.cfg.NoPanel = tc.noPanel })
			e.create()
			code, out := e.uploadTo(e.sp("/resourcepack?host=203.0.113.10&port=8443&name=Faithful.zip"), resourcePackZip(t, "Faithful 32x", true))
			if code != tc.code {
				t.Fatalf("upload: %d %v", code, out)
			}
			if tc.code == 200 {
				return
			}
			if out["code"] != api.CodeConflict || out["error"] != "Resource packs work only on the dashboard's machine for now." {
				t.Fatalf("the refusal: %v", out)
			}
			if got := e.storedPacks(); len(got) != 0 || e.resourcePack().Offer != nil || e.audits("resourcepack.offered") != 0 {
				t.Fatalf("a refused pack was kept: %v", got)
			}
		})
	}
}

// A resource pack is stored by its hash and offered to players from the
// next start, with the owner's settings; the machine keeps each pack while
// a server or its running container still offers it.
func TestResourcePackOffer(t *testing.T) {
	e := newAgentEnv(t)
	e.create()

	faithful := resourcePackZip(t, "Faithful 32x", true)
	code, out := e.uploadTo(e.sp("/resourcepack?host=localhost&port=8443&name=Faithful.zip"), faithful)
	if code != 400 || out["code"] != packs.CodeInvalidHost {
		t.Fatalf("a pack players couldn't download: %d %v", code, out)
	}
	if code, out := e.uploadTo(e.sp("/resourcepack?host=203.0.113.10&port=8443"), dataPackZip(t, "x", false)); code != 400 || out["code"] != packs.CodeWrongKind {
		t.Fatalf("a data pack as the resource pack: %d %v", code, out)
	}
	if got := e.storedPacks(); len(got) != 0 {
		t.Fatalf("refused uploads stored %v", got)
	}

	a := sha1Hex(faithful)
	v := e.offerPack("Faithful 32x.zip", faithful)
	o := v.Offer
	if o == nil || o.SHA1 != a || o.URL != "http://203.0.113.10:8443/resource-packs/"+a+".zip" || o.FileName != "Faithful 32x.zip" ||
		o.Size != int64(len(faithful)) || o.Description != "Faithful 32x" || !o.Icon || o.Required || o.Prompt != "" || o.AddedAt.IsZero() || !v.Pending {
		t.Fatalf("the offer: %+v", v)
	}
	st, err := os.Stat(filepath.Join(e.cfg.ResourcePacksDir(), a+".zip"))
	if err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("the stored pack: %v %v", st, err)
	}
	if dir, _ := os.Stat(e.cfg.ResourcePacksDir()); dir.Mode().Perm() != 0o755 {
		t.Fatalf("the pack folder: %v", dir.Mode())
	}
	if !e.status().PendingRestart {
		t.Fatal("offering a pack must ask for a restart")
	}
	if got := e.activePacks(); !slices.Equal(got, []string{a}) {
		t.Fatalf("active packs: %v", got)
	}
	if code, hdr, icon := e.getBytes(e.sp("/resourcepack/icon")); code != 200 || hdr.Get("Content-Type") != "image/png" || !bytes.Equal(icon, iconPNG(t)) {
		t.Fatalf("the pack's icon: %d", code)
	}

	code, out = e.call("POST", e.sp("/resourcepack/settings"), map[string]any{"required": true, "prompt": " Grab the pack! ", "actor": "admin"})
	if o := decodeAs[api.ResourcePack](t, out).Offer; code != 200 || !o.Required || o.Prompt != "Grab the pack!" {
		t.Fatalf("settings: %d %v", code, out)
	}
	if code, out := e.call("POST", e.sp("/resourcepack/settings"), map[string]any{"required": true, "prompt": "100% vanilla", "actor": "admin"}); code != 400 || out["code"] != packs.CodeInvalidPrompt {
		t.Fatalf("a prompt the image would expand: %d %v", code, out)
	}
	e.call("POST", e.sp("/resourcepack/settings"), map[string]any{"required": true, "prompt": "Grab the pack!", "actor": "admin"})
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'resourcepack.changed' AND target = 'Faithful 32x.zip' AND detail = 'players must accept it, message changed'`); n != 1 || e.audits("resourcepack.changed") != 1 {
		t.Fatalf("settings audit entries: %d of %d", n, e.audits("resourcepack.changed"))
	}

	e.serverOp("/restart")
	want := map[string]string{
		"RESOURCE_PACK": o.URL, "RESOURCE_PACK_SHA1": a, "RESOURCE_PACK_ENFORCE": "true",
		"RESOURCE_PACK_PROMPT": `"Grab the pack!"`, "RESOURCE_PACK_ID": packs.ResourcePackID(a),
	}
	if got := e.containerEnv(); !maps.Equal(got, want) {
		t.Fatalf("the restarted server's environment: %v", got)
	}
	if e.resourcePack().Pending || e.status().PendingRestart {
		t.Fatal("the restarted server offers the pack")
	}

	// A new pack takes the old one's settings. The old one stays served
	// while the running server offers it.
	sphax := resourcePackZip(t, "Sphax", false)
	b := sha1Hex(sphax)
	v = e.offerPack("Sphax.zip", sphax)
	if o := v.Offer; o.SHA1 != b || !o.Required || o.Prompt != "Grab the pack!" || o.Icon || !v.Pending {
		t.Fatalf("the new offer: %+v", v)
	}
	if code, out := e.call("GET", e.sp("/resourcepack/icon"), nil); code != 404 || out["code"] != packs.CodeNoIcon {
		t.Fatalf("the icon of a pack without one: %d %v", code, out)
	}
	if got, want := e.activePacks(), slices.Sorted(slices.Values([]string{a, b})); !slices.Equal(got, want) {
		t.Fatalf("active packs while the old one runs: %v", got)
	}
	if got := e.storedPacks(); len(got) != 2 {
		t.Fatalf("stored packs while the old one runs: %v", got)
	}
	e.serverOp("/restart")
	if got := e.activePacks(); !slices.Equal(got, []string{b}) {
		t.Fatalf("active packs after the restart: %v", got)
	}
	e.a.prunePacks(context.Background())
	if got := e.storedPacks(); !slices.Equal(got, []string{b + ".zip"}) {
		t.Fatalf("stored packs after pruning: %v", got)
	}

	// Removing the offer clears the settings from the next start.
	code, out = e.call("DELETE", e.sp("/resourcepack?actor=admin"), nil)
	if v := decodeAs[api.ResourcePack](t, out); code != 200 || v.Offer != nil || !v.Pending {
		t.Fatalf("remove: %d %v", code, out)
	}
	if code, _ := e.call("DELETE", e.sp("/resourcepack?actor=admin"), nil); code != 200 || e.audits("resourcepack.removed") != 1 {
		t.Fatalf("removing again: %d, %d audit entries", code, e.audits("resourcepack.removed"))
	}
	if code, out := e.call("POST", e.sp("/resourcepack/settings"), map[string]any{"required": false, "actor": "admin"}); code != 409 || out["code"] != "no_resource_pack" {
		t.Fatalf("settings without a pack: %d %v", code, out)
	}
	if got := e.storedPacks(); len(got) != 1 {
		t.Fatalf("the pack the running server offers must stay: %v", got)
	}
	e.serverOp("/restart")
	cleared := map[string]string{"RESOURCE_PACK": "", "RESOURCE_PACK_SHA1": "", "RESOURCE_PACK_ENFORCE": "", "RESOURCE_PACK_PROMPT": "", "RESOURCE_PACK_ID": ""}
	if got := e.containerEnv(); !maps.Equal(got, cleared) || e.resourcePack().Pending || e.status().PendingRestart {
		t.Fatalf("after removing the pack and restarting: %v", got)
	}
	if got := e.activePacks(); len(got) != 0 {
		t.Fatalf("active packs: %v", got)
	}
	e.a.prunePacks(context.Background())
	if got := e.storedPacks(); len(got) != 0 {
		t.Fatalf("stored packs: %v", got)
	}
	if e.audits("resourcepack.offered") != 2 {
		t.Fatalf("%d resourcepack.offered audit entries", e.audits("resourcepack.offered"))
	}
}

// packCA is a certificate authority that players' games trust in a test.
type packCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newPackCA(t *testing.T) *packCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Playkeeper Test Root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &packCA{cert: cert, key: key}
}

func (ca *packCA) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

// save saves a certificate for name, valid for life from now, where Issue
// saves the machine's certificate.
func (ca *packCA) save(t *testing.T, dir, name string, life time.Duration) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(life),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	b := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})...)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name+".pem")); err != nil {
		t.Fatal(err)
	}
}

func (e *agentEnv) containerID() string {
	e.t.Helper()
	c, err := e.a.docker.ContainerInspect(context.Background(), e.cname())
	if err != nil {
		e.t.Fatal(err)
	}
	return c.ID
}

// A pack's link uses HTTPS only while the panel serves a certificate that
// players' games trust for exactly the link's host, valid for a week more.
// The link follows certificates that arrive or lapse from the next start:
// a running server keeps offering its link until it restarts.
func TestResourcePackLinksUseHTTPSWithATrustedCertificate(t *testing.T) {
	ca, stranger := newPackCA(t), newPackCA(t)
	e := newAgentEnvWith(t, func(e *agentEnv) {
		e.tweak = func(o *Options) { o.CertRoots = ca.pool() }
	})
	e.create()
	dir, day := e.cfg.CertsDir(), 24*time.Hour
	const host = "mc.example.com"
	faithful := resourcePackZip(t, "Faithful 32x", false)
	sum := sha1Hex(faithful)
	plain := "http://" + host + ":8443/resource-packs/" + sum + ".zip"
	secure := "https://" + host + ":8443/resource-packs/" + sum + ".zip"
	offer := func() api.ResourcePack {
		t.Helper()
		code, out := e.uploadTo(e.sp("/resourcepack?host="+host+"&port=8443&name=Faithful.zip"), faithful)
		if code != 200 {
			t.Fatalf("offer: %d %v", code, out)
		}
		return decodeAs[api.ResourcePack](t, out)
	}

	for what, save := range map[string]func(){
		"no certificate":        func() {},
		"another authority":     func() { stranger.save(t, dir, host, 90*day) },
		"another name":          func() { ca.save(t, dir, "www.example.com", 90*day) },
		"less than a week left": func() { ca.save(t, dir, host, 6*day) },
	} {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		save()
		if v := offer(); v.Offer == nil || v.Offer.URL != plain {
			t.Fatalf("%s: the link must be plain HTTP: %+v", what, v.Offer)
		}
	}
	e.serverOp("/restart")
	if got := e.containerEnv()["RESOURCE_PACK"]; got != plain {
		t.Fatalf("the started server's link: %s", got)
	}

	id := e.containerID()
	ca.save(t, dir, host, 90*day)
	if v := e.resourcePack(); v.Offer == nil || v.Offer.URL != secure || !v.Pending || !e.status().PendingRestart {
		t.Fatalf("once a trusted certificate arrived, the next start offers HTTPS: %+v", v)
	}
	if e.containerID() != id || e.containerEnv()["RESOURCE_PACK"] != plain {
		t.Fatal("a running server keeps its link until it restarts")
	}
	e.serverOp("/restart")
	if got := e.containerEnv()["RESOURCE_PACK"]; got != secure || e.resourcePack().Pending {
		t.Fatalf("the restarted server's link: %s", got)
	}
	if v := offer(); v.Offer.URL != secure || v.Pending {
		t.Fatalf("a pack offered with the certificate: %+v", v)
	}

	id = e.containerID()
	ca.save(t, dir, host, 6*day)
	if v := e.resourcePack(); v.Offer.URL != plain || !v.Pending {
		t.Fatalf("with less than a week left, the next start offers plain HTTP: %+v", v)
	}
	if e.containerID() != id || e.containerEnv()["RESOURCE_PACK"] != secure {
		t.Fatal("a running server keeps its link until it restarts")
	}
	e.serverOp("/stop")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	e.serverOp("/start")
	if got := e.containerEnv()["RESOURCE_PACK"]; got != plain || e.resourcePack().Pending {
		t.Fatalf("a server started without the certificate: %s", got)
	}
}

// A server that never offered a pack keeps its definition, so an update
// to this version doesn't ask every server for a restart.
func TestNoResourcePackLeavesTheEnvironmentAlone(t *testing.T) {
	if env, err := resourcePackEnv(nil); env != nil || err != nil {
		t.Fatalf("no offer: %v %v", env, err)
	}
	if env, err := resourcePackEnv(&api.ResourcePackOffer{}); len(env) != 5 || !slices.Contains(env, "RESOURCE_PACK=") || err != nil {
		t.Fatalf("an offer that was removed: %v %v", env, err)
	}
}

// A stored offer whose settings can't be built, from a hand-edited
// database or checks that got stricter, never clears the pack or reads as
// applied: the server goes on offering what it did, the machine keeps
// serving that pack, and the Packs page says what's wrong and how to fix it.
func TestResourcePackOfferThatCantBeBuilt(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	faithful, sphax := resourcePackZip(t, "Faithful 32x", false), resourcePackZip(t, "Sphax", false)
	a, b := sha1Hex(faithful), sha1Hex(sphax)
	e.offerPack("Faithful.zip", faithful)
	e.serverOp("/restart")
	offered := e.containerEnv()
	if offered["RESOURCE_PACK_SHA1"] != a {
		t.Fatalf("the offered pack: %v", offered)
	}
	// The image writes the settings into server.properties.
	props, err := os.OpenFile(filepath.Join(e.dataDir(), "server.properties"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(props, "resource-pack=http\\://203.0.113.10\\:8443/resource-packs/%s.zip\n", a)
	props.Close()
	edit := func(change func(sc *api.ServerConfig)) {
		t.Helper()
		sc, err := e.srv().serverConfig()
		if err != nil {
			t.Fatal(err)
		}
		change(sc)
		if err := e.srv().saveServerConfig(*sc); err != nil {
			t.Fatal(err)
		}
	}
	keeps := func(when string, want ...string) {
		t.Helper()
		e.a.prunePacks(context.Background())
		slices.Sort(want)
		stored := []string{}
		for _, sum := range want {
			stored = append(stored, sum+".zip")
		}
		if got := e.activePacks(); !slices.Equal(got, want) {
			t.Fatalf("active packs %s: %v", when, got)
		}
		if got := e.storedPacks(); !slices.Equal(got, stored) {
			t.Fatalf("stored packs %s: %v", when, got)
		}
	}

	badPrompt := "The message shown to players must be one line of at most 200 characters, without percent signs or backslashes. Change the message players see, or remove the pack."
	edit(func(sc *api.ServerConfig) {
		sc.ResourcePack.Prompt = "100% vanilla"
		sc.Gameplay.Difficulty = "hard"
	})
	if v := e.resourcePack(); v.Offer == nil || v.Offer.SHA1 != a || v.Pending || v.Problem != badPrompt {
		t.Fatalf("an offer that can't be built: %+v", v)
	}
	if !e.status().PendingRestart {
		t.Fatal("the new difficulty must ask for a restart")
	}
	// A new pack takes the message along, so it can't be offered either.
	if v := e.offerPack("Sphax.zip", sphax); v.Offer.SHA1 != b || v.Pending || v.Problem != badPrompt {
		t.Fatalf("a new pack with the old message: %+v", v)
	}

	// Recreating the container keeps the pack settings it had.
	e.serverOp("/restart")
	c, err := e.a.docker.ContainerInspect(context.Background(), e.cname())
	if err != nil || !slices.Contains(c.Config.Env, "DIFFICULTY=hard") {
		t.Fatalf("the server wasn't recreated: %v %v", c.Config.Env, err)
	}
	if got := packEnv(c.Config.Env); !maps.Equal(got, offered) {
		t.Fatalf("the recreated server's pack settings: %v", got)
	}
	if v := e.resourcePack(); v.Pending || v.Problem != badPrompt || e.status().PendingRestart {
		t.Fatalf("after the restart: %+v, restart pending %v", v, e.status().PendingRestart)
	}
	keeps("after the restart", a, b)

	// Without a container to keep them from, the pack settings in
	// server.properties are left as they are rather than cleared, and the
	// machine keeps serving the pack they name.
	edit(func(sc *api.ServerConfig) { sc.ResourcePack.URL = "ftp://x.example/p.zip" })
	if v := e.resourcePack(); v.Problem != `"ftp://x.example/p.zip" isn't a resource pack URL a server can offer. Upload the pack again, or remove it.` {
		t.Fatalf("an offer at a URL players can't use: %+v", v)
	}
	e.serverOp("/stop")
	if err := e.a.docker.ContainerRemove(context.Background(), e.cname(), true); err != nil {
		t.Fatal(err)
	}
	e.serverOp("/start")
	if got := e.containerEnv(); len(got) != 0 {
		t.Fatalf("a new container's pack settings: %v", got)
	}
	keeps("without pack settings", a, b)

	// Uploading the pack again fixes the URL, a new message the rest. The
	// running server offers the old pack until it restarts.
	if v := e.offerPack("Sphax.zip", sphax); v.Problem != badPrompt {
		t.Fatalf("the pack uploaded again: %+v", v)
	}
	code, out := e.call("POST", e.sp("/resourcepack/settings"), map[string]any{"required": false, "prompt": "Grab the pack!", "actor": "admin"})
	if v := decodeAs[api.ResourcePack](t, out); code != 200 || v.Problem != "" || !v.Pending {
		t.Fatalf("a message players can see: %d %v", code, out)
	}
	keeps("until the restart", a, b)
	e.serverOp("/restart")
	if got := e.containerEnv(); got["RESOURCE_PACK_SHA1"] != b || got["RESOURCE_PACK_PROMPT"] != `"Grab the pack!"` || e.resourcePack().Pending {
		t.Fatalf("the fixed offer: %v", got)
	}
	keeps("once the fixed offer runs", b)
}

// A restored world keeps the pack the server offers, since backups don't
// hold packs; a server restored from a backup that names a pack the panel
// served doesn't offer it. Deleting a server lets its pack go.
func TestResourcePackAcrossRestoreAndDelete(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(map[string]any{"name": "Survival"})
	survival := e.sid
	faithful := resourcePackZip(t, "Faithful 32x", false)
	a := sha1Hex(faithful)
	e.offerPack("Faithful.zip", faithful)
	// The image writes the offer into server.properties, which backups hold.
	props := "level-name=world\nresource-pack=http\\://203.0.113.10\\:8443/resource-packs/" + a + ".zip\nrequire-resource-pack=true\n"
	if err := os.WriteFile(filepath.Join(e.dataDir(), "server.properties"), []byte(props), 0o644); err != nil {
		t.Fatal(err)
	}
	id, phrase := e.backupAndStage()
	sphax := resourcePackZip(t, "Sphax", false)
	b := sha1Hex(sphax)
	e.offerPack("Sphax.zip", sphax)
	if op := e.applyRestore(id, phrase); op.Status != api.OpSucceeded {
		t.Fatalf("restore: %+v", op)
	}
	e.waitFor("online after the restore", e.onlineIdle)
	if o := e.resourcePack().Offer; o == nil || o.SHA1 != b {
		t.Fatalf("the restored server's offer: %+v", o)
	}
	if got := e.containerEnv()["RESOURCE_PACK_SHA1"]; got != b {
		t.Fatalf("the restored server offers %q", got)
	}

	list, _ := e.srv().listBackups(`kind = 'manual'`)
	archive, err := os.ReadFile(filepath.Join(e.cfg.BackupsDir(), list[0].FileName))
	if err != nil {
		t.Fatal(err)
	}
	e.sid = ""
	code, preview := e.uploadTo("/v1/restore/upload", archive)
	if code != 200 {
		t.Fatalf("upload: %d %v", code, preview)
	}
	code, out := e.call("POST", "/v1/restore/"+preview["id"].(string)+"/apply", map[string]any{"confirm": "restore", "acceptEula": true, "actor": "admin"})
	if code != 202 {
		t.Fatalf("apply: %d %v", code, out)
	}
	e.sid = out["serverId"].(string)
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("restore as a new server: %+v", op)
	}
	e.waitFor("the new server online", e.onlineIdle)
	if v := e.resourcePack(); v.Offer != nil || v.Pending {
		t.Fatalf("the new server's offer: %+v", v)
	}
	if got := e.containerEnv(); got["RESOURCE_PACK"] != "" || len(got) != 5 {
		t.Fatalf("the new server must clear the pack its properties name: %v", got)
	}

	e.sid = survival
	code, out = e.call("POST", e.sp("/delete"), map[string]any{"confirm": "Survival", "actor": "admin"})
	if code != 202 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("delete: %+v", op)
	}
	if got := e.storedPacks(); len(got) != 0 {
		t.Fatalf("packs nothing offers after the delete: %v", got)
	}
}

// Restored properties only lose a pack the panel served.
func TestRestoredPackOffer(t *testing.T) {
	dir := t.TempDir()
	sum := strings.Repeat("ab", 20)
	offer := &api.ResourcePackOffer{SHA1: sum}
	for name, tc := range map[string]struct {
		prev  *api.ResourcePackOffer
		props string
		want  *api.ResourcePackOffer
	}{
		"offered":         {offer, "resource-pack=http\\://203.0.113.10\\:8443/resource-packs/" + strings.Repeat("cd", 20) + ".zip\n", offer},
		"panel pack":      {nil, "resource-pack=http\\://203.0.113.10\\:8443/resource-packs/" + sum + ".zip\n", &api.ResourcePackOffer{}},
		"another website": {nil, "resource-pack=https\\://cdn.example.com/packs/faithful.zip\n", nil},
		"no pack":         {nil, "resource-pack=\n", nil},
		"no properties":   {nil, "", nil},
	} {
		path := filepath.Join(dir, "server.properties")
		os.Remove(path)
		if tc.props != "" {
			os.WriteFile(path, []byte(tc.props), 0o644)
		}
		got := restoredPackOffer(tc.prev, dir)
		if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
			t.Errorf("%s: %+v", name, got)
		}
	}
}
