package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/curated"
)

// withCrossplayProjects adds Geyser to the fake Modrinth, as a beta the way
// GeyserMC publishes every build, and Floodgate to the fake Hangar under
// its real project id.
func withCrossplayProjects(f *fakeSources) {
	at := time.Date(2026, 9, 25, 13, 55, 0, 0, time.UTC)
	f.addProject(&fakeProject{id: "wKkoqHrH", slug: "geyser", title: "Geyser", summary: "Bedrock players on Java servers.", downloads: 1354362})
	f.publish("wKkoqHrH", "geyser-b1247", "2.11.3-b1247", at).channel = "beta"
	f.addHangarPlugin(17, "GeyserMC", "Floodgate", "239", "2.2.5-b141", "floodgate-spigot.jar", at)
}

func (e *agentEnv) crossplay() api.Crossplay {
	e.t.Helper()
	var out api.Crossplay
	e.decode("GET", e.sp("/crossplay"), &out)
	return out
}

func (e *agentEnv) crossplayOp(on bool) *api.Operation {
	e.t.Helper()
	return e.addonOp("/crossplay", map[string]any{"on": on, "actor": "admin"})
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// One switch gives a running Paper server crossplay: a UDP port nothing
// else uses, published from the container, Geyser and Floodgate installed
// with that port and Floodgate sign-in, Bedrock players' chat let through,
// and where they join on the status. The Plugins tab can't install or
// remove either plugin meanwhile. Turning it off takes both out and closes
// the port.
func TestCrossplaySwitchOpensItsPortAndInstallsBothPlugins(t *testing.T) {
	e := newAgentEnv(t)
	withCrossplayProjects(e.withSources())
	e.a.opts.UDPPortInUse = func(p int) bool { return p == curated.CrossplayPort }
	e.create()
	e.waitFor("online", e.onlineIdle)

	before := e.crossplay()
	if before.On || !before.Available || before.Port != curated.CrossplayPort+1 || before.Notice != nil || len(before.Plugins) != 0 || before.Prefix != "." {
		t.Fatalf("crossplay before: %+v", before)
	}
	op := e.crossplayOp(true)
	if op.Status != api.OpSucceeded || opDetail[int](t, op, "port") != curated.CrossplayPort+1 {
		t.Fatalf("turn crossplay on: %+v", op)
	}
	port := curated.CrossplayPort + 1
	sc, _ := e.srv().serverConfig()
	if sc.CrossplayPort != port {
		t.Fatalf("the server keeps crossplay's port: %d", sc.CrossplayPort)
	}
	recs, _ := e.srv().installedAddons()
	var got []string
	for _, r := range recs {
		got = append(got, string(r.Source)+" "+r.Name+" "+r.VersionNumber+" "+r.Channel)
	}
	if !slices.Equal(got, []string{"hangar Floodgate 2.2.5-b141 release", "modrinth Geyser 2.11.3-b1247 beta"}) {
		t.Fatalf("records %q", got)
	}
	geyser := readTestFile(t, filepath.Join(e.dataDir(), curated.GeyserConfigPath))
	floodgate := readTestFile(t, filepath.Join(e.dataDir(), curated.FloodgateConfigPath))
	if !strings.Contains(geyser, "  port: "+strconv.Itoa(port)+"\n") || !strings.Contains(geyser, "auth-type: floodgate") || !strings.Contains(floodgate, "username-prefix: \".\"") {
		t.Fatalf("Geyser's config %q, Floodgate's %q", geyser, floodgate)
	}
	e.waitFor("online with crossplay", e.onlineIdle)
	if want := []string{strconv.Itoa(port) + "/udp→" + strconv.Itoa(port), "25565/tcp→" + strconv.Itoa(e.srv().gamePort)}; !slices.Equal(e.published(), want) {
		t.Fatalf("the running server was restarted to publish the port: %v", e.published())
	}
	if e.containerEnvVar("ENFORCE_SECURE_PROFILE") != "FALSE" {
		t.Fatal("Bedrock players' chat isn't let through")
	}
	if b := e.status().Bedrock; b == nil || b.Port != port || b.Host != "" {
		t.Fatalf("status says Bedrock players join at %+v", b)
	}
	after := e.crossplay()
	if !after.On || after.Port != port || len(after.Plugins) != 2 {
		t.Fatalf("crossplay after: %+v", after)
	}
	if e.audits("crossplay.port_opened") != 1 {
		t.Fatalf("the port opened %d times", e.audits("crossplay.port_opened"))
	}

	if code, out := e.callWhenFree("POST", e.sp("/crossplay"), map[string]any{"on": true, "actor": "admin"}); code != 409 {
		t.Fatalf("crossplay on twice: %d %v", code, out)
	}
	var tpl api.TemplateExport
	e.decode("GET", e.sp("/template"), &tpl)
	if len(tpl.Contents.Addons) != 0 || len(tpl.Available.Addons) != 0 || !slices.ContainsFunc(tpl.LeftOut, func(n api.AddonNotice) bool { return n.Kind == "left_out_crossplay" }) {
		t.Fatalf("a template of the server carries %+v and leaves out %+v", tpl.Contents.Addons, tpl.LeftOut)
	}
	if code, out := e.callWhenFree("POST", e.sp("/addons/install"), map[string]any{"source": "modrinth", "projectId": "wKkoqHrH", "fingerprint": otherPlan, "actor": "admin"}); code != 409 || !strings.Contains(out["hint"].(string), "Turn on crossplay") {
		t.Fatalf("Geyser installed from the Plugins tab: %d %v", code, out)
	}
	if code, out := e.callWhenFree("POST", e.sp("/addons/remove"), map[string]any{"source": "hangar", "projectId": "17", "actor": "admin"}); code != 409 || !strings.Contains(out["error"].(string), "Floodgate is part of crossplay") {
		t.Fatalf("Floodgate removed from the Plugins tab: %d %v", code, out)
	}

	off := e.crossplayOp(false)
	if off.Status != api.OpSucceeded {
		t.Fatalf("turn crossplay off: %+v", off)
	}
	e.waitFor("online without crossplay", e.onlineIdle)
	sc, _ = e.srv().serverConfig()
	recs, _ = e.srv().installedAddons()
	jars, _ := filepath.Glob(filepath.Join(e.dataDir(), "plugins", "*.jar"))
	if sc.CrossplayPort != 0 || len(recs) != 0 || len(jars) != 0 || e.status().Bedrock != nil {
		t.Fatalf("crossplay off left port %d, records %+v, jars %v", sc.CrossplayPort, recs, jars)
	}
	if !slices.Equal(e.published(), []string{"25565/tcp→" + strconv.Itoa(e.srv().gamePort)}) || e.containerEnvVar("ENFORCE_SECURE_PROFILE") != "" {
		t.Fatalf("after crossplay goes off the container publishes %v, secure profiles %q", e.published(), e.containerEnvVar("ENFORCE_SECURE_PROFILE"))
	}
	if _, err := os.Stat(filepath.Join(e.dataDir(), curated.FloodgateConfigPath)); err != nil {
		t.Errorf("Floodgate's settings went with it: %v", err)
	}
	if e.audits("crossplay.port_closed") != 1 {
		t.Fatalf("the port closed %d times", e.audits("crossplay.port_closed"))
	}
}

// The public page says where Bedrock players join while the server has
// crossplay: the page's own address, with the UDP port.
func TestThePublicPageSaysWhereBedrockPlayersJoin(t *testing.T) {
	e := newAgentEnv(t)
	withCrossplayProjects(e.withSources())
	e.create()
	e.withPageAddress()
	e.waitFor("online", e.onlineIdle)
	e.waitFor("the page", func() bool {
		code, p, _ := e.page(pageTestHost)
		return code == 200 && len(p.Servers) == 1
	})
	if _, p, _ := e.page(pageTestHost); p.Servers[0].Bedrock != nil {
		t.Fatalf("the page without crossplay: %+v", p.Servers[0])
	}
	if op := e.crossplayOp(true); op.Status != api.OpSucceeded {
		t.Fatalf("crossplay on: %+v", op)
	}
	_, p, _ := e.page(pageTestHost)
	if len(p.Servers) != 1 || p.Servers[0].Bedrock == nil || *p.Servers[0].Bedrock != (api.BedrockJoin{Host: pageTestHost, Port: curated.CrossplayPort}) {
		t.Fatalf("the page with crossplay: %+v", p)
	}
}

// Geyser and Floodgate put in the plugins folder by hand are used as they
// are. GeyserMC's own download of Geyser calls itself Geyser-Spigot, so only
// its file name says it's Geyser. Turning crossplay off leaves both, since
// Playkeeper didn't install them.
func TestCrossplayUsesPluginsPutThereByHand(t *testing.T) {
	e := newAgentEnv(t)
	withCrossplayProjects(e.withSources())
	e.create()
	e.waitFor("online", e.onlineIdle)
	plugins := filepath.Join(e.dataDir(), "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	hand := map[string][]byte{
		"Geyser-2.11.3-b1247.jar": pluginJar(t, "Geyser-Spigot", "2.11.3-b1247"),
		"floodgate-spigot.jar":    pluginJar(t, "floodgate", "2.2.5-b140"),
	}
	for name, data := range hand {
		if err := os.WriteFile(filepath.Join(plugins, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if op := e.crossplayOp(true); op.Status != api.OpSucceeded {
		t.Fatalf("crossplay on with both plugins put there by hand: %+v", op)
	}
	recs, _ := e.srv().installedAddons()
	if c := e.crossplay(); !c.On || len(recs) != 0 {
		t.Fatalf("crossplay %+v installed %+v over the copies put there by hand", c, recs)
	}
	e.waitFor("online with crossplay", e.onlineIdle)
	if op := e.crossplayOp(false); op.Status != api.OpSucceeded {
		t.Fatalf("crossplay off: %+v", op)
	}
	for name, data := range hand {
		if got, err := os.ReadFile(filepath.Join(plugins, name)); err != nil || !bytes.Equal(got, data) {
			t.Errorf("%s after crossplay went off: %v", name, err)
		}
	}
}

// Turning crossplay off without Docker's answer on whether the server runs
// changes nothing, since a running server would go on letting Bedrock
// players in.
func TestCrossplayOffThatCantCheckTheServerChangesNothing(t *testing.T) {
	e := newAgentEnv(t)
	withCrossplayProjects(e.withSources())
	e.create()
	e.waitFor("online", e.onlineIdle)
	if op := e.crossplayOp(true); op.Status != api.OpSucceeded {
		t.Fatalf("crossplay on: %+v", op)
	}
	e.waitFor("online with crossplay", e.onlineIdle)
	started := e.startedAt()
	down := func(prefix string) {
		e.fd.mu.Lock()
		e.fd.down = prefix
		e.fd.mu.Unlock()
	}
	down("/containers/" + e.cname() + "/json")
	op := e.crossplayOp(false)
	down("")
	if op.Status != api.OpFailed || !strings.HasPrefix(op.Error, "Crossplay is still on: Playkeeper couldn't tell whether the server is running") || op.Hint != "Try again once Docker answers." {
		t.Fatalf("crossplay off with Docker not answering: %+v", op)
	}
	sc, _ := e.srv().serverConfig()
	recs, _ := e.srv().installedAddons()
	jars, _ := filepath.Glob(filepath.Join(e.dataDir(), "plugins", "*.jar"))
	if sc.CrossplayPort == 0 || len(recs) != 2 || len(jars) != 2 || !e.startedAt().Equal(started) {
		t.Fatalf("crossplay off that failed left port %d, records %+v, jars %v; restarted %v", sc.CrossplayPort, recs, jars, !e.startedAt().Equal(started))
	}
}

// With crossplay on, the Players tab adds a Bedrock player to the allowlist
// by their Xbox gamertag after Floodgate's dot, through Floodgate's own
// command, and waits for Floodgate to write the list, as it did on a real
// Paper 26.2 server: nothing said over RCON, the entry a moment later.
func TestBedrockPlayersJoinTheAllowlistThroughFloodgate(t *testing.T) {
	e := newAgentEnv(t)
	withCrossplayProjects(e.withSources())
	e.create()
	e.waitFor("online", e.onlineIdle)
	add := map[string]any{"actor": "admin", "name": ".Notch"}
	if code, out := e.callWhenFree("POST", e.sp("/whitelist"), add); code != 400 {
		t.Fatalf("a Bedrock name without crossplay: %d %v", code, out)
	}
	if op := e.crossplayOp(true); op.Status != api.OpSucceeded {
		t.Fatalf("crossplay on: %+v", op)
	}
	e.waitFor("online with crossplay", e.onlineIdle)

	list := filepath.Join(e.dataDir(), "whitelist.json")
	notch := `[{"uuid":"00000000-0000-0000-0009-01fb54b26482","name":".Notch"}]`
	e.rcon.answer = func(cmd string) (string, bool) {
		switch cmd {
		case "fwhitelist add Notch":
			go func() {
				time.Sleep(300 * time.Millisecond)
				os.WriteFile(list, []byte(notch), 0o644)
			}()
			return "", true
		case "fwhitelist remove Notch":
			os.WriteFile(list, []byte("[]"), 0o644)
			return "", true
		case "fwhitelist add Nobody":
			return "", true
		}
		return "", false
	}
	defer func(w time.Duration) { bedrockListWait = w }(bedrockListWait)
	bedrockListWait = 2 * time.Second

	code, out := e.callWhenFree("POST", e.sp("/whitelist"), add)
	if code != 200 || out["added"] != true || !strings.Contains(out["message"].(string), ".Notch") || len(out["whitelist"].([]any)) != 1 {
		t.Fatalf("add a Bedrock player: %d %v", code, out)
	}
	if code, out := e.callWhenFree("POST", e.sp("/whitelist"), add); code != 200 || out["added"] == true {
		t.Fatalf("add them again: %d %v", code, out)
	}
	if code, out := e.callWhenFree("POST", e.sp("/whitelist"), map[string]any{"actor": "admin", "name": ".Nobody"}); code != 422 || !strings.Contains(out["error"].(string), "no Bedrock player called Nobody") {
		t.Fatalf("a gamertag Floodgate can't find: %d %v", code, out)
	}
	if code, out := e.callWhenFree("DELETE", e.sp("/whitelist/.Notch")+"?actor=admin", nil); code != 200 || len(out["whitelist"].([]any)) != 0 {
		t.Fatalf("remove a Bedrock player: %d %v", code, out)
	}
	for _, bad := range []string{".", ".no spaces", ".way-too-long-for-a-gamertag", "..Notch"} {
		if code, _ := e.callWhenFree("POST", e.sp("/whitelist"), map[string]any{"actor": "admin", "name": bad}); code != 400 {
			t.Errorf("%q: %d, want 400", bad, code)
		}
	}
	if e.rcon.count("fwhitelist add Notch") != 1 {
		t.Errorf("Floodgate was asked %d times to add Notch", e.rcon.count("fwhitelist add Notch"))
	}
}

// A crossplay install that fails closes the port it opened and leaves
// neither plugin behind; one that can't have a port installs nothing.
func TestCrossplayThatFailsClosesItsPort(t *testing.T) {
	e := newAgentEnv(t)
	f := e.withSources()
	withCrossplayProjects(f)
	e.create()
	e.waitFor("online", e.onlineIdle)
	left := func() (int, int, int) {
		recs, _ := e.srv().installedAddons()
		jars, _ := filepath.Glob(filepath.Join(e.dataDir(), "plugins", "*.jar"))
		sc, _ := e.srv().serverConfig()
		return len(recs), len(jars), sc.CrossplayPort
	}

	e.a.opts.UDPPortInUse = func(int) bool { return true }
	if op := e.crossplayOp(true); op.Status != api.OpFailed || !strings.Contains(op.Error, "already in use") {
		t.Fatalf("crossplay with no free port: %+v", op)
	}
	if recs, jars, port := left(); recs != 0 || jars != 0 || port != 0 || e.audits("crossplay.port_opened") != 0 {
		t.Fatalf("crossplay without a port left %d records, %d jars and port %d", recs, jars, port)
	}

	e.a.opts.UDPPortInUse = func(int) bool { return false }
	putBack := f.withdraw("geyser-b1247")
	op := e.crossplayOp(true)
	putBack()
	if op.Status != api.OpFailed {
		t.Fatalf("crossplay while Geyser has no version: %+v", op)
	}
	if recs, jars, port := left(); recs != 0 || jars != 0 || port != 0 || e.audits("crossplay.port_opened") != 1 || e.audits("crossplay.port_closed") != 1 {
		t.Fatalf("crossplay that couldn't plan Geyser left %d records, %d jars and port %d; the port opened %d and closed %d times",
			recs, jars, port, e.audits("crossplay.port_opened"), e.audits("crossplay.port_closed"))
	}

	// Geyser installs first; Floodgate's download doesn't match its hash.
	f.mu.Lock()
	fg := f.hangarProject("17").versions[0]
	fg.served = slices.Clone(fg.data)
	fg.served[len(fg.served)-1] ^= 0xff
	f.mu.Unlock()
	op = e.crossplayOp(true)
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "does not match") {
		t.Fatalf("crossplay whose Floodgate download is wrong: %+v", op)
	}
	if recs, jars, port := left(); recs != 0 || jars != 0 || port != 0 || e.audits("crossplay.port_closed") != 2 {
		t.Fatalf("crossplay whose second plugin failed left %d records, %d jars and port %d", recs, jars, port)
	}

	f.mu.Lock()
	f.hangarProject("17").versions[0].served = nil
	f.mu.Unlock()
	if op := e.crossplayOp(true); op.Status != api.OpSucceeded {
		t.Fatalf("crossplay once both download: %+v", op)
	}
	if recs, jars, port := left(); recs != 2 || jars != 2 || port != curated.CrossplayPort {
		t.Fatalf("crossplay on: %d records, %d jars, port %d", recs, jars, port)
	}
}

// Crossplay is for Paper and Purpur; elsewhere the switch says why it can't
// be turned on, and turning it on is refused.
func TestCrossplayIsOnlyForPaperAndPurpur(t *testing.T) {
	e := newAgentEnv(t)
	withCrossplayProjects(e.withSources())
	e.createWith(vanilla262)
	c := e.crossplay()
	if c.On || c.Available || c.Notice == nil || c.Notice.Kind != string(curated.KindNotForType) || !strings.Contains(c.Notice.Message, "Paper and Purpur") {
		t.Fatalf("crossplay on Fabric: %+v", c)
	}
	if code, out := e.callWhenFree("POST", e.sp("/crossplay"), map[string]any{"on": true, "actor": "admin"}); code != 409 || !strings.Contains(out["error"].(string), "Paper and Purpur") {
		t.Fatalf("crossplay turned on on Fabric: %d %v", code, out)
	}
}

// Voice chat and crossplay on one machine never share a UDP port, on the
// same server or on two.
func TestVoiceChatAndCrossplayNeverShareAPort(t *testing.T) {
	e := newAgentEnv(t)
	f := e.withSources()
	withCuratedProjects(f)
	withCrossplayProjects(f)
	e.create()
	e.waitFor("online", e.onlineIdle)
	sc, _ := e.srv().serverConfig()
	sc.VoiceChatPort = curated.CrossplayPort
	if err := e.srv().saveServerConfig(*sc); err != nil {
		t.Fatal(err)
	}
	if port, err := e.a.freeCrossplayPort(e.sid); err != nil || port != curated.CrossplayPort+1 {
		t.Fatalf("crossplay beside voice chat on %d gets %d, %v", curated.CrossplayPort, port, err)
	}
	sc.VoiceChatPort, sc.CrossplayPort = 0, curated.VoiceChatPort
	if err := e.srv().saveServerConfig(*sc); err != nil {
		t.Fatal(err)
	}
	if port, err := e.a.freeVoicePort(e.sid, curated.VoiceChatPort); err != nil || port != curated.VoiceChatPort+1 {
		t.Fatalf("voice chat beside crossplay on %d gets %d, %v", curated.VoiceChatPort, port, err)
	}
	if port, err := e.a.freeCrossplayPort(e.sid); err != nil || port != curated.CrossplayPort {
		t.Fatalf("the server's own crossplay port stays its own: %d, %v", port, err)
	}
	if port, err := e.a.freeCrossplayPort(""); err != nil || port != curated.CrossplayPort {
		t.Fatalf("a new server's crossplay port: %d, %v", port, err)
	}
	release := e.a.voicePorts.hold(curated.CrossplayPort, "other")
	defer release()
	if port, err := e.a.freeCrossplayPort(e.sid); err != nil || port != curated.CrossplayPort+1 {
		t.Fatalf("a port held for another server is taken: %d, %v", port, err)
	}
}

// A restore gives crossplay back its port: the server's own, or one nothing
// else uses; a backup from before crossplay leaves it off. Geyser's config
// gets the port before the server starts.
func TestRestoreKeepsCrossplaysPort(t *testing.T) {
	e := newAgentEnv(t)
	withCrossplayProjects(e.withSources())
	inUse := map[int]bool{}
	e.a.opts.UDPPortInUse = func(p int) bool { return inUse[p] }
	e.create()
	e.waitFor("online", e.onlineIdle)
	backup := func() string {
		t.Helper()
		code, out := e.callWhenFree("POST", e.sp("/backups"), map[string]any{"actor": "admin"})
		if code != 202 {
			t.Fatalf("backup: %d %v", code, out)
		}
		op := e.waitOp(out["id"].(string))
		if op.Status != api.OpSucceeded {
			t.Fatalf("backup: %+v", op)
		}
		e.waitFor("online after the backup", e.onlineIdle)
		return op.Detail["backupId"].(string)
	}
	restore := func(id string) {
		t.Helper()
		code, preview := e.callWhenFree("POST", e.sp("/backups/"+id+"/restore"), map[string]any{"actor": "admin"})
		if code != 200 {
			t.Fatalf("stage: %d %v", code, preview)
		}
		if op := e.applyRestore(preview["id"].(string), preview["confirmPhrase"].(string)); op.Status != api.OpSucceeded {
			t.Fatalf("restore: %+v", op)
		}
		e.waitFor("online after the restore", e.onlineIdle)
	}
	has := func(when string, port int) {
		t.Helper()
		want := []string{"25565/tcp→" + strconv.Itoa(e.srv().gamePort)}
		if port > 0 {
			want = append([]string{strconv.Itoa(port) + "/udp→" + strconv.Itoa(port)}, want...)
			if g := readTestFile(t, filepath.Join(e.dataDir(), curated.GeyserConfigPath)); !strings.Contains(g, "  port: "+strconv.Itoa(port)+"\n") {
				t.Fatalf("%s: Geyser's config %q", when, g)
			}
		}
		if sc, _ := e.srv().serverConfig(); sc.CrossplayPort != port || !slices.Equal(e.published(), want) {
			t.Fatalf("%s: port %d, published %v, want %d", when, sc.CrossplayPort, e.published(), port)
		}
	}

	without := backup()
	if op := e.crossplayOp(true); op.Status != api.OpSucceeded {
		t.Fatalf("crossplay on: %+v", op)
	}
	e.waitFor("online with crossplay", e.onlineIdle)
	with := backup()
	list, _ := e.srv().listBackups(`id = ?`, with)
	if len(list) != 1 {
		t.Fatalf("backups %+v", list)
	}

	restore(with)
	has("restored with crossplay", curated.CrossplayPort)
	restore(without)
	has("restored from before crossplay", 0)
	inUse[curated.CrossplayPort] = true
	restore(with)
	has("crossplay restored again, its port now used", curated.CrossplayPort+1)
	if recs, _ := e.srv().installedAddons(); !slices.ContainsFunc(recs, func(r addons.Installed) bool { return r.Name == "Geyser" }) {
		t.Fatalf("the records of crossplay's plugins: %+v", recs)
	}
}
