package agent

import (
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
	"github.com/CIYAhq/playkeeper/internal/templates"
)

const voiceChatProject = "9eGKb6K1"

// withCuratedProjects adds some of the curated projects to the fake Modrinth:
// voice chat, CoreProtect and LuckPerms with a version for the server, and
// ViaVersion without one. EssentialsX isn't there at all.
func withCuratedProjects(f *fakeSources) {
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	f.addProject(&fakeProject{id: voiceChatProject, slug: "simple-voice-chat", title: "Simple Voice Chat", summary: "Proximity voice chat.", downloads: 90000000})
	f.addProject(&fakeProject{id: "Lu3KuzdV", slug: "coreprotect", title: "CoreProtect", summary: "Block logging and rollback.", downloads: 3000000})
	f.addProject(&fakeProject{id: "Vebnzrzj", slug: "luckperms", title: "LuckPerms", summary: "A permissions plugin.", downloads: 2900000})
	f.addProject(&fakeProject{id: "P1OZGk5p", slug: "viaversion", title: "ViaVersion", summary: "Newer clients on older servers.", downloads: 5000000})
	f.publish(voiceChatProject, "svc-v1", "2.6.4", at)
	f.publish("Lu3KuzdV", "cp-v1", "23.1", at)
	f.publish("Vebnzrzj", "lp-v1", "5.5.10", at)
}

func TestCuratedPicksAreTheOnesThatFitTheServer(t *testing.T) {
	e := newAgentEnv(t)
	f := e.withSources()
	withCuratedProjects(f)
	e.create()

	var got api.CuratedAddons
	e.decode("GET", e.sp("/addons/curated"), &got)
	var ids []string
	for _, p := range got.Picks {
		ids = append(ids, p.ID+" "+p.Card.Name)
	}
	if want := []string{"voice-chat Simple Voice Chat", "rollback CoreProtect", "pregenerate Chunky", "permissions LuckPerms"}; !slices.Equal(ids, want) {
		t.Fatalf("picks %q, want %q: only the ones with a version for Paper 26.1.2, in the list's order", ids, want)
	}
	voice := got.Picks[0]
	if len(voice.Ports) != 1 || voice.Ports[0] != (api.AddonPort{Protocol: "udp", Port: curated.VoiceChatPort}) || !strings.HasPrefix(voice.Permission, "https://") {
		t.Fatalf("voice chat says the port it needs and where its author allows this: %+v", voice)
	}
	if len(got.Picks[1].Ports) != 0 || got.Picks[1].Card.Installed {
		t.Fatalf("CoreProtect needs no port and isn't installed: %+v", got.Picks[1])
	}

	asked := f.hitCount("GET", "/v2/project/Lu3KuzdV")
	e.decode("GET", e.sp("/addons/curated"), &got)
	if f.hitCount("GET", "/v2/project/Lu3KuzdV") != asked {
		t.Error("which picks fit a type and version is asked again within the hour")
	}
}

func TestVoiceChatOpensItsPortAndClosesItWhenRemoved(t *testing.T) {
	e := newAgentEnv(t)
	f := e.withSources()
	withCuratedProjects(f)
	e.create()
	e.waitFor("online", func() bool { return e.status().Phase == api.PhaseOnline })

	var d api.AddonDetails
	e.decode("GET", e.sp("/addons/project/modrinth/"+voiceChatProject), &d)
	if d.Plan == nil || !d.Plan.Ready || len(d.Ports) != 1 || d.Ports[0].Port != curated.VoiceChatPort {
		t.Fatalf("voice chat's details: %+v", d)
	}
	install := map[string]any{"source": "modrinth", "projectId": voiceChatProject, "fingerprint": d.Plan.Fingerprint, "actor": "admin"}
	if code, out := e.call("POST", e.sp("/addons/install"), install); code != 400 || !strings.Contains(out["error"].(string), "UDP port") {
		t.Fatalf("voice chat must not install without leave to open its port: %d %v", code, out)
	}
	if jars, _ := filepath.Glob(filepath.Join(e.dataDir(), "plugins", "*.jar")); len(jars) != 0 {
		t.Fatalf("a refused install left %v", jars)
	}

	e.a.opts.UDPPortInUse = func(p int) bool { return p == curated.VoiceChatPort }
	install["openPorts"] = true
	op := e.addonOp("/addons/install", install)
	if op.Status != api.OpSucceeded || opDetail[int](t, op, "voiceChatPort") != curated.VoiceChatPort+1 || opDetail[bool](t, op, "restartNeeded") {
		t.Fatalf("install and open the port: %+v", op)
	}
	port := curated.VoiceChatPort + 1
	sc, _ := e.srv().serverConfig()
	if sc.VoiceChatPort != port {
		t.Fatalf("the server keeps voice chat's port, one nothing else uses: %d", sc.VoiceChatPort)
	}
	b, err := os.ReadFile(filepath.Join(e.dataDir(), "plugins", "voicechat", "voicechat-server.properties"))
	if err != nil || !strings.Contains(string(b), "port=24455\n") || !strings.Contains(string(b), "bind_address=*\n") {
		t.Fatalf("voice chat's settings: %q %v", b, err)
	}
	if got := e.published(); !slices.Equal(got, []string{"24455/udp→24455", "25565/tcp→" + strconv.Itoa(e.srv().gamePort)}) {
		t.Fatalf("the running server was restarted to publish the port: %v", got)
	}
	e.waitFor("online again", func() bool { return e.status().Phase == api.PhaseOnline })

	if code, out := e.call("POST", e.sp("/addons/remove"), map[string]any{"source": "modrinth", "projectId": voiceChatProject, "keepConfig": true, "actor": "admin"}); code != 200 {
		t.Fatalf("remove: %d %v", code, out)
	}
	if sc, _ := e.srv().serverConfig(); sc.VoiceChatPort != 0 {
		t.Fatalf("removing voice chat forgets its port: %d", sc.VoiceChatPort)
	}
	restart := e.addonOp("/restart", map[string]any{"actor": "admin"})
	if restart.Status != api.OpSucceeded {
		t.Fatalf("restart: %+v", restart)
	}
	if got := e.published(); !slices.Equal(got, []string{"25565/tcp→" + strconv.Itoa(e.srv().gamePort)}) {
		t.Fatalf("after the restart the container publishes only the game port: %v", got)
	}
}

// Removing voice chat closes its port first: a removal that can't close it
// removes nothing, so no later start publishes a port with nothing behind
// it, and one whose file can't be removed gives the port back.
func TestVoiceChatRemovalClosesItsPortFirst(t *testing.T) {
	e := newAgentEnv(t)
	withCuratedProjects(e.withSources())
	e.a.opts.UDPPortInUse = func(int) bool { return false }
	e.create()
	e.waitFor("online", e.onlineIdle)
	var d api.AddonDetails
	e.decode("GET", e.sp("/addons/project/modrinth/"+voiceChatProject), &d)
	if op := e.addonOp("/addons/install", map[string]any{"source": "modrinth", "projectId": voiceChatProject, "fingerprint": d.Plan.Fingerprint,
		"openPorts": true, "actor": "admin"}); op.Status != api.OpSucceeded {
		t.Fatalf("install voice chat: %+v", op)
	}
	plugins := filepath.Join(e.dataDir(), "plugins")
	left := func() (int, int, int) {
		recs, _ := e.srv().installedAddons()
		jars, _ := filepath.Glob(filepath.Join(plugins, "*.jar"))
		sc, _ := e.srv().serverConfig()
		return len(recs), len(jars), sc.VoiceChatPort
	}
	remove := map[string]any{"source": "modrinth", "projectId": voiceChatProject, "keepConfig": true, "actor": "admin"}

	if _, err := e.a.db.Exec(`CREATE TRIGGER stuck_config BEFORE UPDATE OF config ON servers BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	if code, out := e.call("POST", e.sp("/addons/remove"), remove); code < 400 {
		t.Fatalf("a removal whose port can't be closed: %d %v", code, out)
	}
	if recs, jars, port := left(); recs != 1 || jars != 1 || port != curated.VoiceChatPort {
		t.Fatalf("a removal whose port can't be closed left %d records, %d jars and port %d, want voice chat as it was", recs, jars, port)
	}
	if _, err := e.a.db.Exec(`DROP TRIGGER stuck_config`); err != nil {
		t.Fatal(err)
	}

	if os.Geteuid() != 0 {
		if err := os.Chmod(plugins, 0o500); err != nil {
			t.Fatal(err)
		}
		code, out := e.call("POST", e.sp("/addons/remove"), remove)
		os.Chmod(plugins, 0o755)
		if code < 400 {
			t.Fatalf("a removal whose file can't be removed: %d %v", code, out)
		}
		if recs, jars, port := left(); recs != 1 || jars != 1 || port != curated.VoiceChatPort {
			t.Fatalf("voice chat stayed, so its port must too: %d records, %d jars, port %d", recs, jars, port)
		}
	}

	if code, out := e.call("POST", e.sp("/addons/remove"), remove); code != 200 {
		t.Fatalf("remove: %d %v", code, out)
	}
	if recs, jars, port := left(); recs != 0 || jars != 0 || port != 0 {
		t.Fatalf("after removing voice chat: %d records, %d jars, port %d", recs, jars, port)
	}
}

// published are the ports the server's container publishes.
func (e *agentEnv) published() []string {
	e.fd.mu.Lock()
	defer e.fd.mu.Unlock()
	c := e.fd.byName[e.cname()]
	if c == nil {
		return nil
	}
	var out []string
	for p, b := range c.cfg.HostConfig.PortBindings {
		out = append(out, p+"→"+b[0].HostPort)
	}
	slices.Sort(out)
	return out
}

// A restore gives voice chat back its UDP port: the server's own, or one
// nothing else uses. A backup from before voice chat leaves the port closed.
func TestRestoreKeepsVoiceChatsPort(t *testing.T) {
	e := newAgentEnv(t)
	withCuratedProjects(e.withSources())
	inUse := map[int]bool{}
	e.a.opts.UDPPortInUse = func(p int) bool { return inUse[p] }
	e.create()
	e.waitFor("online", e.onlineIdle)
	backup := func() string {
		t.Helper()
		code, out := e.call("POST", e.sp("/backups"), map[string]any{"actor": "admin"})
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
		code, preview := e.call("POST", e.sp("/backups/"+id+"/restore"), map[string]any{"actor": "admin"})
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
			b, err := os.ReadFile(filepath.Join(e.dataDir(), "plugins", "voicechat", "voicechat-server.properties"))
			if err != nil || !strings.Contains(string(b), "port="+strconv.Itoa(port)+"\n") {
				t.Fatalf("%s: voice chat's settings: %q %v", when, b, err)
			}
		}
		if sc, _ := e.srv().serverConfig(); sc.VoiceChatPort != port || !slices.Equal(e.published(), want) {
			t.Fatalf("%s: port %d, published %v, want %d", when, sc.VoiceChatPort, e.published(), port)
		}
	}

	without := backup()
	var d api.AddonDetails
	e.decode("GET", e.sp("/addons/project/modrinth/"+voiceChatProject), &d)
	if op := e.addonOp("/addons/install", map[string]any{"source": "modrinth", "projectId": voiceChatProject, "fingerprint": d.Plan.Fingerprint,
		"openPorts": true, "actor": "admin"}); op.Status != api.OpSucceeded {
		t.Fatalf("install voice chat: %+v", op)
	}
	e.waitFor("online with voice chat", e.onlineIdle)
	with := backup()

	restore(with)
	has("restored with voice chat", curated.VoiceChatPort)
	restore(without)
	has("restored from before voice chat", 0)
	inUse[curated.VoiceChatPort] = true
	restore(with)
	has("voice chat restored again, its port now used", curated.VoiceChatPort+1)

	list, _ := e.srv().listBackups(`id = ?`, with)
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
	has("a new server from the backup", curated.VoiceChatPort+2)
}

// voiceChatTemplate is a Paper template with Simple Voice Chat.
func voiceChatTemplate(t *testing.T) string {
	t.Helper()
	file, err := templates.MarshalFile(&templates.Template{Format: templates.Format, Name: "Talk", Game: templates.Game,
		Server: templates.Server{Type: "paper", MinecraftVersion: "26.1.2"},
		Addons: []templates.Addon{{Source: addons.Modrinth, Project: voiceChatProject, Slug: "simple-voice-chat", Name: "Simple Voice Chat", Latest: true}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(file)
}

func TestTemplateVoiceChatGetsItsPort(t *testing.T) {
	e := newAgentEnv(t)
	withCuratedProjects(e.withSources())
	e.a.opts.UDPPortInUse = func(p int) bool { return p == curated.VoiceChatPort }
	code, plan, raw := e.planTemplate(voiceChatTemplate(t))
	i := slices.IndexFunc(plan.Warnings, func(n api.AddonNotice) bool { return n.Kind == string(kindTemplateVoiceChat) })
	if code != 200 || !plan.Ready || i < 0 || !strings.Contains(plan.Warnings[i].Message, "UDP 24455") {
		t.Fatalf("the plan says voice chat opens a port, and which: %d %+v %v", code, plan, raw)
	}

	code, out := e.createFromTemplate(plan.Fingerprint, nil)
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded || opDetail[int](t, op, "voiceChatPort") != 24455 {
		t.Fatalf("create from the template: %+v", op)
	}
	e.waitFor("online", func() bool { return e.status().Phase == api.PhaseOnline })
	if sc, _ := e.srv().serverConfig(); sc.VoiceChatPort != 24455 {
		t.Fatalf("the new server keeps voice chat's port: %d", sc.VoiceChatPort)
	}
	b, err := os.ReadFile(filepath.Join(e.dataDir(), "plugins", "voicechat", "voicechat-server.properties"))
	if err != nil || !strings.Contains(string(b), "port=24455\n") {
		t.Fatalf("voice chat's settings: %q %v", b, err)
	}
	if got := e.published(); !slices.Equal(got, []string{"24455/udp→24455", "25565/tcp→" + strconv.Itoa(e.srv().gamePort)}) {
		t.Fatalf("the first start publishes voice chat's port: %v", got)
	}
	if e.audits("addon.port_opened") != 1 {
		t.Fatal("opening the port is audited")
	}
}

// Voice chat that a template's first start skipped gets its port when Try
// again installs it; the running server restarts to publish it.
func TestTemplateVoiceChatTriedAgainGetsItsPort(t *testing.T) {
	e := newAgentEnv(t)
	f := e.withSources()
	withCuratedProjects(f)
	e.a.opts.UDPPortInUse = func(int) bool { return false }
	_, plan, _ := e.planTemplate(voiceChatTemplate(t))
	putBack := f.withdraw("svc-v1")
	code, out := e.createFromTemplate(plan.Fingerprint, nil)
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("create from the template: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	if sc, _ := e.srv().serverConfig(); sc.VoiceChatPort != 0 {
		t.Fatalf("voice chat was skipped, so no port opens: %d", sc.VoiceChatPort)
	}

	putBack()
	code, out = e.call("POST", e.sp("/template/retry"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("try again: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded || op.Detail["restartNeeded"] != true || opDetail[int](t, op, "voiceChatPort") != curated.VoiceChatPort {
		t.Fatalf("try again: %+v", op)
	}
	b, err := os.ReadFile(filepath.Join(e.dataDir(), "plugins", "voicechat", "voicechat-server.properties"))
	if err != nil || !strings.Contains(string(b), "port=24454\n") {
		t.Fatalf("voice chat's settings: %q %v", b, err)
	}
	if restart := e.addonOp("/restart", map[string]any{"actor": "admin"}); restart.Status != api.OpSucceeded {
		t.Fatalf("restart: %+v", restart)
	}
	if got := e.published(); !slices.Equal(got, []string{"24454/udp→24454", "25565/tcp→" + strconv.Itoa(e.srv().gamePort)}) {
		t.Fatalf("the restart publishes voice chat's port: %v", got)
	}
}
