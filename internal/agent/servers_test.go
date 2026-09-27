package agent

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/store"
)

// A migrated server's container definition must be byte for byte what 0.2.0
// made, or the upgrade would restart it. The hash below is what 0.2.0's
// containerSpec returns for the same settings.
func TestMigratedServerKeepsItsExactContainerDefinition(t *testing.T) {
	cfg := config.Default()
	cfg.InstallID = "0f1e2d3c-golden"
	cfg.GameUID, cfg.GameGID = 999, 998
	a := &Agent{cfg: cfg, opts: Options{StopTimeout: 90 * time.Second}}
	sc := api.ServerConfig{VersionID: "paper-26.1.2", MinecraftVersion: "26.1.2", PaperBuild: 74, MemoryMB: 3072, HeapMB: 2304, LevelName: "world", MOTD: "Playkeeper update test", MaxPlayers: 7, Whitelist: true}
	v1 := &server{Agent: a, id: "abcdefghij", layout: layoutV1, gamePort: cfg.GamePort}
	if _, hash := v1.containerSpec(sc, false, nil); hash != "e9b31f7ea2587808" {
		t.Fatalf("a v1 server's definition changed: hash %s, 0.2.0 made e9b31f7ea2587808", hash)
	}
	hard := sc
	hard.Gameplay = api.Gameplay{Difficulty: "hard"}
	if spec, hash := v1.containerSpec(hard, false, nil); hash == "e9b31f7ea2587808" || env(spec, "DIFFICULTY") != "hard" {
		t.Fatal("a setting chosen in Playkeeper must change the definition, so the server restarts to use it")
	}
	v2 := &server{Agent: a, id: "abcdefghij", layout: layoutV2, gamePort: 25566}
	spec, _ := v2.containerSpec(sc, false, nil)
	if spec.Labels[labelServer] != "abcdefghij" || !strings.HasPrefix(spec.HostConfig.Binds[0], "/var/lib/playkeeper/servers/abcdefghij/data:") ||
		spec.HostConfig.PortBindings["25565/tcp"][0].HostPort != "25566" || v2.containerName() != "playkeeper-mc-abcdefghij" {
		t.Fatalf("a new server gets its own directory, label, port and container: %+v", spec)
	}
}

// oldDatabase writes agent.db as 0.2.0 left it, with its single server.
func oldDatabase(t *testing.T, e *agentEnv, sc api.ServerConfig, since time.Time) {
	t.Helper()
	path := filepath.Join(e.cfg.AgentDir(), "agent.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Remove(path + suffix)
	}
	db, err := store.Open(path, migrations[:1])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfgJSON, _ := json.Marshal(sc)
	now := time.Now().UnixMilli()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO kv(key, value) VALUES('server_config', ?), ('desired_state', 'running'), ('collecting_since', ?)`, []any{string(cfgJSON), since.Format(time.RFC3339Nano)}},
		{`INSERT INTO events(ts, kind, player, source, detail, dedup_key, ingested_at) VALUES(?, 'join', 'Friend', 'server_log', '', 'old-join', ?)`, []any{now - 60000, now}},
		{`INSERT INTO sessions(player, start_ts, end_ts, end_reason, source) VALUES('Friend', ?, ?, 'left', 'server_log')`, []any{now - 60000, now - 30000}},
		{`INSERT INTO backups(id, kind, created_at, file_name, size_bytes, sha256, manifest, verified, created_by) VALUES('20260924-120000-abcdef', 'manual', ?, 'playkeeper-world-20260924-120000-abcdef.tar.gz', 10, ?, '{}', 1, 'admin')`, []any{now - 50000, strings.Repeat("a", 64)}},
		{`INSERT INTO samples(ts, state, players_online) VALUES(?, 'online', 1), (?, 'online', 0)`, []any{now - 20000, now - 5000}},
		{`INSERT INTO audit(ts, actor, action, target, result) VALUES(?, 'admin', 'create', 'server', 'succeeded'), (?, 'playkeeper', 'update.updated', '0.2.0', 'updated')`, []any{now - 90000, now - 80000}},
		{`INSERT INTO operations(id, kind, status, actor, started_at, finished_at) VALUES('00000000000000aa', 'create', 'succeeded', 'admin', ?, ?), ('00000000000000bb', 'update', 'succeeded', 'admin', ?, ?)`, []any{now - 90000, now - 85000, now - 80000, now - 70000}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
}

// An install made by 0.2.0 has one server: its world in server/data and its
// container playkeeper-minecraft, running. 0.3.0 records it as a server of
// layout v1 and keeps it running untouched, and new servers go next to it.
func TestSingleServerInstallMigratesWithoutRestarting(t *testing.T) {
	e := newAgentEnv(t)
	e.stop()
	sc := api.ServerConfig{VersionID: "paper-26.1.2", MinecraftVersion: "26.1.2", PaperBuild: 74, MemoryMB: 1536, HeapMB: 1024, LevelName: "world",
		MOTD: "Playkeeper update test", MaxPlayers: 7, Whitelist: true, CreatedAt: time.Now().Add(-time.Hour).UTC(), EULAAcceptedBy: "admin"}
	oldDatabase(t, e, sc, time.Now().Add(-time.Hour))
	world := filepath.Join(e.cfg.ServerDataDir(), "world")
	if err := os.MkdirAll(world, 0o750); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(world, "level.dat"), []byte("the world from 0.2.0"), 0o640)
	os.WriteFile(e.cfg.RCONSecretPath(), []byte("legacysecret"), 0o600)
	os.WriteFile(filepath.Join(filepath.Dir(e.cfg.ServerDataDir()), "rcon_password"), []byte("legacysecret"), 0o400)
	// The running container, made by 0.2.0's definition.
	legacy := &server{Agent: &Agent{cfg: e.cfg, opts: Options{StopTimeout: 5 * time.Second}}, layout: layoutV1, gamePort: e.cfg.GamePort}
	spec, hash := legacy.containerSpec(sc, false, nil)
	spec = withoutGCLog(spec)
	e.fd.mu.Lock()
	e.fd.images[minecraft.Image] = true
	e.fd.mu.Unlock()
	dc := docker.New(e.cfg.DockerSocket)
	id, err := dc.ContainerCreate(context.Background(), legacyContainerName, spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := dc.ContainerStart(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	before, _ := dc.ContainerInspect(context.Background(), id)
	startedBefore, _ := before.State.Started()
	creates := e.fd.called("POST /containers/create")

	e.start()
	servers := e.a.serverList()
	if len(servers) != 1 {
		t.Fatalf("want the one migrated server, got %d", len(servers))
	}
	e.sid = servers[0].id
	s := e.srv()
	if s.layout != layoutV1 || s.containerName() != legacyContainerName || s.dataDir() != e.cfg.ServerDataDir() || s.gamePort != e.cfg.GamePort {
		t.Fatalf("the migrated server must keep 0.2.0's container, directory and port: %+v", s)
	}
	e.waitFor("the migrated server online", func() bool { return e.status().Phase == api.PhaseOnline })
	time.Sleep(300 * time.Millisecond)
	st := e.status()
	if st.PendingRestart || st.StartedAt == nil || !st.StartedAt.Equal(startedBefore) {
		t.Fatalf("the running server must not be restarted or need a restart: pending=%v started %v, was %v", st.PendingRestart, st.StartedAt, startedBefore)
	}
	if n := e.fd.called("POST /containers/" + legacyContainerName + "/stop"); n != 0 {
		t.Fatalf("the migrated server was stopped %d time(s)", n)
	}
	if n := e.fd.called("DELETE /containers/"); n != 0 || e.fd.called("POST /containers/create") != creates {
		t.Fatal("the migrated server's container was replaced")
	}
	if c, _ := dc.ContainerInspect(context.Background(), id); c.Config.Labels[labelSpec] != hash {
		t.Fatal("the container's definition changed")
	}
	if st.Name != "My server" || st.Slug != "my-server" || st.Config.MOTD != "Playkeeper update test" || st.Config.MaxPlayers != 7 {
		t.Fatalf("the server and its settings: %+v %+v", st, st.Config)
	}
	if b, _ := os.ReadFile(filepath.Join(world, "level.dat")); string(b) != "the world from 0.2.0" {
		t.Fatal("the world was changed")
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM backups WHERE server_id = ?`:                                          1,
		`SELECT COUNT(*) FROM events WHERE server_id = ? AND kind = 'join'`:                         1,
		`SELECT COUNT(*) FROM sessions WHERE server_id = ?`:                                         1,
		`SELECT COUNT(*) FROM audit WHERE server_id = ? AND action = 'create'`:                      1,
		`SELECT COUNT(*) FROM operations WHERE server_id = ? AND kind = 'create'`:                   1,
		`SELECT COUNT(*) FROM samples WHERE server_id = ? AND ts < ?`:                               2,
		`SELECT COUNT(*) FROM audit WHERE server_id = '' AND action = 'update.updated' AND ? != ''`: 1,
		`SELECT COUNT(*) FROM operations WHERE server_id = '' AND kind = 'update' AND ? != ''`:      1,
	} {
		args := []any{e.sid}
		if strings.Contains(q, "ts <") {
			args = append(args, time.Now().Add(-time.Second).UnixMilli())
		}
		if got := e.countRows(q, args...); got != want {
			t.Errorf("%s: %d, want %d", q, got, want)
		}
	}
	if n := e.countRows(`SELECT COUNT(*) FROM kv WHERE key IN ('server_config', 'desired_state', 'collecting_since', 'log_cursor')`); n != 0 {
		t.Fatalf("%d single-server keys left in kv", n)
	}
	var backups []api.Backup
	e.decode("GET", e.sp("/backups"), &backups)
	if len(backups) != 1 || backups[0].ServerID != e.sid {
		t.Fatalf("the backup from 0.2.0 belongs to the migrated server: %+v", backups)
	}

	// A second agent start finds the server recorded and migrates nothing again.
	e.stop()
	e.start()
	if list := e.a.serverList(); len(list) != 1 || list[0].id != e.sid {
		t.Fatalf("a restart must keep the one server: %v", list)
	}
	e.waitFor("online after the agent restart", func() bool { return e.status().Phase == api.PhaseOnline })

	// New servers go next to it, in the new layout, on the next port.
	legacyID := e.sid
	e.createWith(map[string]any{"name": "Creative"})
	s2 := e.srv()
	if s2.layout != layoutV2 || s2.gamePort != e.cfg.GamePort+1 || s2.containerName() != containerPrefix+s2.id {
		t.Fatalf("a new server: layout %s port %d container %s", s2.layout, s2.gamePort, s2.containerName())
	}
	e.sid = legacyID
	if st := e.status(); st.Phase != api.PhaseOnline || st.PendingRestart {
		t.Fatalf("the migrated server after adding one: %+v", st)
	}
}

func TestServersRunSideBySide(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(map[string]any{"name": "Survival", "playStyle": "friends"})
	survival := e.sid
	e.createWith(map[string]any{"name": "Creative"})
	creative := e.sid
	a, b := e.a.serverByID(survival), e.a.serverByID(creative)
	if a.gamePort == b.gamePort || a.containerName() == b.containerName() || a.dataDir() == b.dataDir() {
		t.Fatal("two servers must not share a port, container or world")
	}
	for _, s := range []*server{a, b} {
		if st := s.Status(context.Background()); st.Phase != api.PhaseOnline {
			t.Fatalf("%s: %s", st.Name, st.Phase)
		}
	}
	code, out := e.call("GET", "/v1/servers", nil)
	if code != 200 {
		t.Fatalf("list: %d %v", code, out)
	}
	var list []api.ServerStatus
	e.decode("GET", "/v1/servers", &list)
	if len(list) != 2 || list[0].Name != "Survival" || list[1].Name != "Creative" || list[0].Slug != "survival" || list[0].Config.PlayStyle != "friends" {
		t.Fatalf("servers in the order they were made: %+v", list)
	}

	// Names are unique, and memory is shared: a third server does not fit.
	if code, out := e.startCreate(map[string]any{"name": "creative"}); code != 409 || !strings.Contains(out["error"].(string), "already exists") {
		t.Fatalf("a second server named creative: %d %v", code, out)
	}
	if code, out := e.startCreate(map[string]any{"name": "Skyblock"}); code != 409 || !strings.Contains(out["error"].(string), "not enough memory") {
		t.Fatalf("a server that does not fit next to the others: %d %v", code, out)
	}
	cat := e.a.catalogInfo(context.Background(), "")
	if len(cat.MemoryOptionsMB) != 0 || len(cat.Servers) != 2 || cat.SuggestedPort != e.cfg.GamePort+2 {
		t.Fatalf("the catalog for a new server: %+v", cat)
	}
	if cat := e.a.catalogInfo(context.Background(), survival); len(cat.MemoryOptionsMB) == 0 || len(cat.Servers) != 1 {
		t.Fatalf("a server's own settings may use its share: %+v", cat)
	}

	// One server backs up while the other restarts; the same server stays exclusive.
	e.fd.mu.Lock()
	e.fd.bootDelay = 300 * time.Millisecond
	e.fd.mu.Unlock()
	code, backup := e.call("POST", "/v1/servers/"+survival+"/backups", map[string]any{"actor": "admin", "note": "side by side"})
	if code != 202 {
		t.Fatalf("backup: %d %v", code, backup)
	}
	code, restart := e.call("POST", "/v1/servers/"+creative+"/restart", map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("another server must not wait for the backup: %d %v", code, restart)
	}
	if code, out := e.call("POST", "/v1/servers/"+survival+"/stop", map[string]any{"actor": "admin"}); code != 409 || !strings.Contains(out["error"].(string), "Survival is busy with a backup") {
		t.Fatalf("the server backing up is busy: %d %v", code, out)
	}
	for _, id := range []string{backup["id"].(string), restart["id"].(string)} {
		if op := e.waitOp(id); op.Status != api.OpSucceeded {
			t.Fatalf("op: %+v", op)
		}
	}
	if op, _ := e.a.loadOperation(backup["id"].(string)); op.ServerID != survival {
		t.Fatalf("the backup operation must name its server: %+v", op)
	}
	backups, _ := a.listBackups("")
	if len(backups) != 1 || backups[0].ServerID != survival || !strings.HasPrefix(backups[0].FileName, "playkeeper-survival-") {
		t.Fatalf("Survival's backups: %+v", backups)
	}
	if others, _ := b.listBackups(""); len(others) != 0 {
		t.Fatalf("Creative has no backups: %+v", others)
	}
	if code, _ := e.call("GET", "/v1/servers/"+creative+"/backups/"+backups[0].ID+"/download", nil); code != 404 {
		t.Fatalf("a server cannot reach another server's backup: %d", code)
	}
	e.waitFor("both online", func() bool {
		return a.Status(context.Background()).Phase == api.PhaseOnline && b.Status(context.Background()).Phase == api.PhaseOnline && !e.a.busy()
	})

	// Each server keeps its own players and sessions.
	e.fd.target = b.containerName()
	e.fd.addLog("[12:01:00 INFO]: Builder joined the game")
	e.waitFor("Creative's join", func() bool { return b.firstSteps().FriendJoined == "Builder" })
	if a.firstSteps().FriendJoined != "" {
		t.Fatal("a join on Creative must not count for Survival")
	}
	var activity []api.Activity
	e.decode("GET", "/v1/activity?limit=50", &activity)
	kinds := map[string]string{}
	for _, x := range activity {
		kinds[x.ServerID+":"+x.Kind] = x.Player
	}
	for _, want := range []string{survival + ":created", creative + ":created", survival + ":backup", creative + ":restarted", creative + ":joined"} {
		if _, ok := kinds[want]; !ok {
			t.Errorf("activity has no %s: %v", want, kinds)
		}
	}
	if kinds[creative+":joined"] != "Builder" {
		t.Errorf("the join names its player: %v", kinds)
	}

	// Deleting a server removes its container, world and backups, not the other's.
	if code, out := e.call("POST", "/v1/servers/"+creative+"/delete", map[string]any{"confirm": "Survival", "actor": "admin"}); code != 400 {
		t.Fatalf("delete with another server's name: %d %v", code, out)
	}
	code, out = e.call("POST", "/v1/servers/"+creative+"/delete", map[string]any{"confirm": "Creative", "actor": "admin"})
	if code != 202 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("delete op: %+v", op)
	}
	if e.a.serverByID(creative) != nil || e.fd.containerCount(containerPrefix+creative) != 0 {
		t.Fatal("the deleted server is still there")
	}
	if _, err := os.Stat(filepath.Join(e.cfg.DataDir, "servers", creative)); !os.IsNotExist(err) {
		t.Fatalf("the deleted server's files are still there: %v", err)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM sessions WHERE server_id = ?`, creative); n != 0 {
		t.Fatalf("%d sessions of the deleted server left", n)
	}
	if st := a.Status(context.Background()); st.Phase != api.PhaseOnline {
		t.Fatalf("Survival must keep running: %s", st.Phase)
	}
	if n := len(e.a.serverList()); n != 1 {
		t.Fatalf("servers after the delete: %d", n)
	}
}

func TestGameSettingsApplyWithARestart(t *testing.T) {
	e := newAgentEnv(t)
	off := false
	e.createWith(map[string]any{"name": "Creative", "playStyle": "creative", "gameplay": map[string]any{"difficulty": "peaceful", "gameMode": "creative", "pvp": off, "levelType": "flat"}})
	spec := e.fd.byName[e.cname()].cfg
	for k, want := range map[string]string{"DIFFICULTY": "peaceful", "MODE": "creative", "PVP": "false", "LEVEL_TYPE": "minecraft:flat"} {
		if got := env(spec, k); got != want {
			t.Errorf("%s=%q, want %q", k, got, want)
		}
	}
	if env(spec, "HARDCORE") != "" || env(spec, "VIEW_DISTANCE") != "" {
		t.Fatal("settings nobody chose must not be set")
	}
	st := e.status()
	if st.Gameplay.Difficulty != "peaceful" || st.Gameplay.GameMode != "creative" || *st.Gameplay.PVP || st.Gameplay.ViewDistance != 10 || st.Config.PlayStyle != "creative" {
		t.Fatalf("the settings in effect: %+v", st.Gameplay)
	}
	code, out := e.call("POST", e.sp("/settings"), map[string]any{"gameplay": map[string]any{"difficulty": "hard", "viewDistance": 12}, "actor": "admin"})
	if code != 200 || !e.status().PendingRestart {
		t.Fatalf("a changed setting waits for a restart: %d %v", code, out)
	}
	code, out = e.call("POST", e.sp("/settings"), map[string]any{"name": "Build world", "restart": true, "actor": "admin"})
	if code != 202 || out["operation"] == nil {
		t.Fatalf("save and restart: %d %v", code, out)
	}
	if op := e.waitOp(out["operation"].(map[string]any)["id"].(string)); op.Status != api.OpSucceeded || op.Kind != "restart" {
		t.Fatalf("restart: %+v", op)
	}
	e.waitFor("online with the new settings", e.onlineIdle)
	spec = e.fd.byName[e.cname()].cfg
	if env(spec, "DIFFICULTY") != "hard" || env(spec, "VIEW_DISTANCE") != "12" || env(spec, "MODE") != "creative" {
		t.Fatalf("the restarted server runs the new settings: %v", spec.Env)
	}
	if st := e.status(); st.PendingRestart || st.Name != "Build world" || st.Slug != "creative" {
		t.Fatalf("after the restart: pending=%v name=%q slug=%q", st.PendingRestart, st.Name, st.Slug)
	}
	audit, _ := e.a.listAudit(20)
	found := false
	for _, x := range audit {
		if x.Action == "settings.changed" && strings.Contains(x.Detail, "difficulty peaceful→hard") && x.ServerID == e.sid {
			found = true
		}
	}
	if !found {
		t.Fatalf("the audit log says what changed: %+v", audit)
	}
}

// A settings change is checked in full before anything is saved, and neither
// it nor a new icon is saved while an operation holds the server.
func TestSettingsChangesAreWholeAndWaitForNoOperation(t *testing.T) {
	e := newAgentEnv(t)
	e.addIdleServer()
	s := e.srv()
	name := s.name()
	if code, out := e.call("POST", e.sp("/settings"), map[string]any{"name": "Renamed", "maxPlayers": 0, "actor": "admin"}); code != 400 {
		t.Fatalf("an invalid change: %d %v", code, out)
	}
	if s.name() != name {
		t.Fatalf("a refused change renamed the server to %q", s.name())
	}

	var icon bytes.Buffer
	if err := png.Encode(&icon, image.NewRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	release, ok := s.holdOpLock()
	if !ok {
		t.Fatal("the server is busy")
	}
	code, out := e.call("POST", e.sp("/settings"), map[string]any{"motd": "Changed meanwhile", "actor": "admin"})
	iconCode, iconOut := e.uploadTo(e.sp("/icon"), icon.Bytes())
	release()
	if code != 409 || iconCode != 409 {
		t.Fatalf("changes while an operation holds the server: settings %d %v, icon %d %v", code, out, iconCode, iconOut)
	}
	if sc, _ := s.serverConfig(); sc.MOTD == "Changed meanwhile" || sc.IconUpdatedAt != nil {
		t.Fatalf("a refused change was saved: %+v", sc)
	}
	if code, out := e.call("POST", e.sp("/settings"), map[string]any{"motd": "Changed after", "actor": "admin"}); code != 200 {
		t.Fatalf("a change once the server is free: %d %v", code, out)
	}
	if code, out := e.uploadTo(e.sp("/icon"), icon.Bytes()); code != 200 {
		t.Fatalf("an icon once the server is free: %d %v", code, out)
	}
}

// padPNG puts a text chunk after a PNG's header so the file is size bytes.
func padPNG(t *testing.T, b []byte, size int) []byte {
	t.Helper()
	const afterHeader = 8 + 8 + 13 + 4
	data := append([]byte("Comment\x00"), bytes.Repeat([]byte("x"), size-len(b)-12-8)...)
	chunk := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	chunk = append(append(chunk, "tEXt"...), data...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))
	return slices.Concat(b[:afterHeader], chunk, b[afterHeader:])
}

// An icon that is not a 64 × 64 PNG of at most 64 KB is refused with its own
// code before anything is written, and one of exactly 64 KB is saved and can
// be read back, so an upload never ends in an icon that 404s.
func TestIconsAreCheckedBeforeTheyAreWritten(t *testing.T) {
	e := newAgentEnv(t)
	e.addIdleServer()
	s := e.srv()
	encode := func(side int) []byte {
		var b bytes.Buffer
		if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, side, side))); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	small := encode(64)
	for name, c := range map[string]struct {
		body []byte
		says string
	}{
		"too large":  {padPNG(t, small, 64<<10+1), "This one is larger."},
		"not a PNG":  {[]byte("GIF89a not a picture"), "This one is not a PNG."},
		"128 × 128":  {encode(128), "This one is 128 × 128."},
		"unreadable": {small[:len(small)-20], "This one could not be read."},
	} {
		code, out := e.uploadTo(e.sp("/icon"), c.body)
		if msg, _ := out["error"].(string); code != 400 || out["code"] != api.CodeIconInvalid || msg != "Icons need to be 64 × 64 PNG pictures of at most 64 KB. "+c.says {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if _, err := os.Lstat(filepath.Join(s.dataDir(), iconFile)); !os.IsNotExist(err) {
		t.Fatalf("a refused icon was written: %v", err)
	}
	if sc, _ := s.serverConfig(); sc.IconUpdatedAt != nil {
		t.Fatalf("a refused icon was recorded: %+v", sc.IconUpdatedAt)
	}

	full := padPNG(t, small, 64<<10)
	if code, out := e.uploadTo(e.sp("/icon"), full); code != 200 {
		t.Fatalf("an icon of exactly 64 KB: %d %v", code, out)
	}
	resp, err := http.Get(e.ts.URL + e.sp("/icon"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got, _ := io.ReadAll(resp.Body); resp.StatusCode != 200 || !bytes.Equal(got, full) {
		t.Fatalf("reading back the icon just saved: %d, %d bytes", resp.StatusCode, len(got))
	}
}

func TestEffectiveSettingsComeFromTheServerWhenNotChosen(t *testing.T) {
	props := map[string]string{"difficulty": "2", "pvp": "false", "view-distance": "12", "gamemode": "adventure", "level-type": "minecraft:amplified", "hardcore": "true"}
	g := effectiveGameplay(api.Gameplay{}, props)
	if g.Difficulty != "normal" || *g.PVP || g.ViewDistance != 12 || g.GameMode != "adventure" || g.LevelType != "amplified" || !*g.Hardcore {
		t.Fatalf("from server.properties: %+v", g)
	}
	g = effectiveGameplay(api.Gameplay{Difficulty: "peaceful"}, props)
	if g.Difficulty != "peaceful" {
		t.Fatalf("a chosen setting wins: %+v", g)
	}
	if g := effectiveGameplay(api.Gameplay{}, nil); g.Difficulty != "easy" || !*g.PVP || g.ViewDistance != 10 || g.GameMode != "survival" {
		t.Fatalf("Minecraft's defaults: %+v", g)
	}
}

// A Playkeeper backup restores as a new server next to the ones there are,
// after the EULA is accepted for it.
func TestBackupRestoresAsANewServer(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(map[string]any{"name": "Survival"})
	survival := e.sid
	os.WriteFile(filepath.Join(e.dataDir(), "world", "marker.txt"), []byte("nonce-survival"), 0o644)
	code, out := e.call("POST", e.sp("/backups"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("backup: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("backup: %+v", op)
	}
	list, _ := e.srv().listBackups("")
	archive, err := os.ReadFile(filepath.Join(e.cfg.BackupsDir(), list[0].FileName))
	if err != nil {
		t.Fatal(err)
	}
	e.sid = ""
	code, preview := e.uploadTo("/v1/restore/upload", archive)
	if code != 200 || preview["needsEula"] != true || preview["confirmPhrase"] != "restore" || preview["serverId"] != nil {
		t.Fatalf("a restore that makes a new server: %d %v", code, preview)
	}
	warnings, _ := preview["warnings"].([]any)
	for _, w := range warnings {
		if s, _ := w.(string); strings.Contains(s, "EULA") {
			t.Fatalf("the dashboard asks for the EULA from needsEula until its box is ticked, so it is not also a warning: %q", s)
		}
	}
	id := preview["id"].(string)
	if code, out := e.call("POST", "/v1/restore/"+id+"/apply", map[string]any{"confirm": "restore", "actor": "admin"}); code != 400 || out["code"] != api.CodeEULARequired {
		t.Fatalf("without the EULA: %d %v", code, out)
	}
	if n := len(e.a.serverList()); n != 1 {
		t.Fatalf("a refused restore made a server: %d", n)
	}
	code, out = e.call("POST", "/v1/restore/"+id+"/apply", map[string]any{"confirm": "restore", "acceptEula": true, "actor": "admin"})
	if code != 202 {
		t.Fatalf("apply: %d %v", code, out)
	}
	e.sid = out["serverId"].(string)
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("restore: %+v", op)
	}
	e.waitFor("the restored server online", e.onlineIdle)
	st := e.status()
	if e.sid == survival || st.Name != "Survival 2" || e.srv().layout != layoutV2 {
		t.Fatalf("the restored server: id %s name %q", e.sid, st.Name)
	}
	if b, _ := os.ReadFile(filepath.Join(e.dataDir(), "world", "marker.txt")); string(b) != "nonce-survival" {
		t.Fatalf("the restored world: %q", b)
	}
}

// A Playkeeper update needs every server idle, and holds them while it runs.
func TestUpdateWaitsForEveryServer(t *testing.T) {
	e, _, _ := updateEnv(t)
	e.create()
	e.fd.mu.Lock()
	e.fd.bootDelay = 400 * time.Millisecond
	e.fd.mu.Unlock()
	code, out := e.call("POST", e.sp("/restart"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("restart: %d %v", code, out)
	}
	if code, out := e.call("POST", "/v1/update/apply", map[string]any{"version": "0.2.1", "actor": "admin"}); code != 409 || !strings.Contains(out["error"].(string), "is busy with restarting") {
		t.Fatalf("an update while a server restarts: %d %v", code, out)
	}
	e.waitOp(out["id"].(string))
	e.waitFor("idle", e.onlineIdle)
	op := e.applyUpdate("0.2.1")
	if op.Status != api.OpRunning || op.ServerID != "" {
		t.Fatalf("the update is a machine operation: %+v", op)
	}
	if code, out := e.call("POST", "/v1/servers", map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin"}); code != 409 || !strings.Contains(out["error"].(string), "installing update") {
		t.Fatalf("no server is created while the update installs: %d %v", code, out)
	}
}

// While a machine-wide operation runs, a new server or a restore as a new
// server is refused and leaves nothing behind: no server that holds memory
// and a port, or that could start by itself afterwards.
func TestNoServerIsAddedWhileTheMachineIsBusy(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(map[string]any{"name": "Survival"})
	code, out := e.call("POST", e.sp("/backups"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("backup: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("backup: %+v", op)
	}
	list, _ := e.srv().listBackups("")
	archive, err := os.ReadFile(filepath.Join(e.cfg.BackupsDir(), list[0].FileName))
	if err != nil {
		t.Fatal(err)
	}
	code, preview := e.uploadTo("/v1/restore/upload", archive)
	if code != 200 {
		t.Fatalf("upload: %d %v", code, preview)
	}
	e.waitFor("Survival idle", e.onlineIdle)

	release := make(chan struct{})
	if _, err := e.a.beginMachineOp("update", "admin", func(ctx context.Context, h *opHandle) error {
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, out = e.call("POST", "/v1/servers", map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "name": "Creative", "actor": "admin"})
	restoreCode, restoreOut := e.call("POST", "/v1/restore/"+preview["id"].(string)+"/apply", map[string]any{"confirm": "restore", "acceptEula": true, "actor": "admin"})
	close(release)
	if code != 409 || out["code"] != api.CodeBusy || restoreCode != 409 || restoreOut["code"] != api.CodeBusy {
		t.Fatalf("while the machine is busy: create %d %v, restore %d %v", code, out, restoreCode, restoreOut)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM servers`); n != 1 || len(e.a.serverList()) != 1 {
		t.Fatalf("refused requests left %d servers", n)
	}
}

func (e *agentEnv) decode(method, path string, out any) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		e.t.Fatal(err)
	}
}

func (e *agentEnv) uploadTo(path string, archive []byte) (int, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.ts.URL+path, bytes.NewReader(archive))
	req.Header.Set("X-Playkeeper-Actor", "admin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// The tick rate comes from Paper's tps command.
func TestTickRate(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.waitFor("a tick rate", func() bool {
		st := e.status()
		return st.Resources != nil && st.Resources.TPS != nil && *st.Resources.TPS == 20
	})
}

// The World tab shows the world's size: every dimension counts, logs don't.
func TestWorldSizeIsMeasured(t *testing.T) {
	e := newAgentEnv(t)
	e.addIdleServer()
	s := e.srv()
	for path, size := range map[string]int{
		"world/region/r.0.0.mca":              3000,
		"world_nether/DIM-1/region/r.0.0.mca": 200,
		"world_the_end/level.dat":             50,
		"logs/latest.log":                     999,
	} {
		full := filepath.Join(s.dataDir(), path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s.measureWorld(time.Now().Add(time.Hour), "world")
	if st := e.status(); st.WorldBytes == nil || *st.WorldBytes != 3250 {
		t.Fatalf("world size = %v, want 3250", st.WorldBytes)
	}
}

// Deleting a server moves its files aside before it deletes anything, so a
// server whose files can't be moved keeps its backups.
func TestDeleteKeepsBackupsWhenTheFilesCannotBeMoved(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	code, out := e.call("POST", e.sp("/backups"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("backup: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("backup op: %+v", op)
	}
	s := e.srv()
	backups, _ := s.listBackups("")
	if len(backups) != 1 {
		t.Fatalf("backups: %+v", backups)
	}
	renameDir = func(from, to string) error {
		if strings.Contains(to, ".deleting-") {
			return errors.New("injected rename failure")
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { renameDir = os.Rename })
	code, out = e.call("POST", e.sp("/delete"), map[string]any{"confirm": s.name(), "actor": "admin"})
	if code != 202 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpFailed {
		t.Fatalf("the delete must fail when the files can't be moved: %+v", op)
	}
	if e.a.serverByID(e.sid) == nil {
		t.Fatal("the server must still be there")
	}
	left, _ := s.listBackups("")
	if len(left) != 1 {
		t.Fatalf("the backup record is gone: %+v", left)
	}
	for _, f := range []string{s.backupPath(left[0].FileName), s.backupPath(left[0].FileName) + ".sha256"} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("the backup file is gone: %v", err)
		}
	}
}

// A deleted server's loops end with it, so nothing keeps sampling or
// following a server that no longer exists.
func TestDeletedServerLeavesNothingRunning(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	s := e.srv()
	code, out := e.call("POST", e.sp("/delete"), map[string]any{"confirm": s.name(), "actor": "admin"})
	if code != 202 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("delete op: %+v", op)
	}
	if s.ctx.Err() == nil {
		t.Fatal("the deleted server's loops are still running")
	}
	time.Sleep(3 * e.a.opts.SampleInterval)
	if n := e.countRows(`SELECT COUNT(*) FROM samples WHERE server_id = ?`, e.sid); n != 0 {
		t.Fatalf("%d samples recorded for the deleted server", n)
	}
}
