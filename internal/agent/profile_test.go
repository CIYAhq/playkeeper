package agent

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

func (e *agentEnv) addSession(player string, start time.Time, end *time.Time, uncertain bool) {
	e.t.Helper()
	var endTS any
	if end != nil {
		endTS = end.UnixMilli()
	}
	if _, err := e.a.db.Exec(`INSERT INTO sessions(server_id, player, uuid, start_ts, end_ts, end_reason, start_uncertain, end_uncertain, source) VALUES(?, ?, '', ?, ?, 'left', 0, ?, 'server_log')`,
		e.sid, player, start.UnixMilli(), endTS, boolInt(uncertain)); err != nil {
		e.t.Fatal(err)
	}
}

func TestProfileSumsAPlayersSessions(t *testing.T) {
	e := newAgentEnv(t)
	e.addIdleServer()
	loc, _ := time.LoadLocation("Europe/Berlin")
	now := e.a.now().In(loc)
	y, m, d := now.Date()
	at := func(daysAgo, hour int) time.Time { return time.Date(y, m, d-daysAgo, hour, 0, 0, 0, loc) }
	end := func(t time.Time, dur time.Duration) *time.Time { v := t.Add(dur); return &v }
	e.addSession("mara_k", at(20, 9), end(at(20, 9), 30*time.Minute), false)
	e.addSession("Mara_K", at(3, 19), end(at(3, 19), time.Hour), false)
	e.addSession("mara_k", at(1, 18), end(at(1, 18), 2*time.Hour), true)
	open := now.Add(-10 * time.Minute)
	e.addSession("Mara_K", open, nil, false)
	e.addSession("JunoFox", at(2, 20), end(at(2, 20), 4*time.Hour), false)

	code, out := e.call("GET", e.sp("/players/profile?name=MARA_K&tz=Europe/Berlin"), nil)
	if code != 200 {
		t.Fatalf("profile: %d %v", code, out)
	}
	var p struct {
		Name              string
		Online            bool
		OnlineSince       *time.Time
		FirstSeen         *time.Time
		Sessions          int
		PlaytimeSeconds   int64
		LongestSeconds    int64
		PlaytimeUncertain bool
		Days              []struct{ PlaytimeSeconds int64 }
		Mostly            string
		Recent            []struct{ Player string }
		Allowlisted       bool
	}
	b, _ := json.Marshal(out)
	json.Unmarshal(b, &p)
	if p.Name != "Mara_K" || p.Sessions != 4 || p.LongestSeconds != 7200 || !p.PlaytimeUncertain || p.Allowlisted {
		t.Fatalf("profile: %s", b)
	}
	if want := int64(30*60 + 3600 + 7200 + 600); p.PlaytimeSeconds < want || p.PlaytimeSeconds > want+60 {
		t.Fatalf("playtime %d, want about %d", p.PlaytimeSeconds, want)
	}
	if !p.Online || p.OnlineSince == nil || p.OnlineSince.UnixMilli() != open.UnixMilli() {
		t.Fatalf("online since %v, want %v", p.OnlineSince, open)
	}
	if p.FirstSeen == nil || !p.FirstSeen.Equal(at(20, 9)) {
		t.Fatalf("first seen %v", p.FirstSeen)
	}
	if len(p.Days) != 14 || p.Days[10].PlaytimeSeconds != 3600 || p.Days[12].PlaytimeSeconds < 7200 || p.Days[11].PlaytimeSeconds != 0 {
		t.Fatalf("days: %s", b)
	}
	if p.Mostly != "evening" || len(p.Recent) != 4 || p.Recent[0].Player != "Mara_K" {
		t.Fatalf("mostly %q, recent %v", p.Mostly, p.Recent)
	}

	// A player who never played but is on the allowlist has a page too.
	if err := os.MkdirAll(e.dataDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(e.dataDir(), "whitelist.json"), []byte(`[{"uuid": "069a79f4-44e9-4726-a5be-fca90e38aaf5", "name": "tobi2009"}]`), 0o640)
	os.WriteFile(filepath.Join(e.dataDir(), "ops.json"), []byte(`[{"uuid": "069a79f4-44e9-4726-a5be-fca90e38aaf5", "name": "tobi2009", "level": 4}]`), 0o640)
	code, out = e.call("GET", e.sp("/players/profile?name=Tobi2009&tz=UTC"), nil)
	if code != 200 || out["name"] != "tobi2009" || out["allowlisted"] != true || out["operator"] != true || out["sessions"] != float64(0) || out["uuid"] != "069a79f4-44e9-4726-a5be-fca90e38aaf5" || out["banned"] != nil {
		t.Fatalf("allowlisted player: %d %v", code, out)
	}

	// The server's ban list says who is banned; someone only on it has a page too.
	os.WriteFile(filepath.Join(e.dataDir(), "banned-players.json"), []byte(`[
		{"uuid": "069a79f4-44e9-4726-a5be-fca90e38aaf5", "name": "tobi2009", "created": "2026-09-25 23:00:00 +0000", "source": "Server", "expires": "forever", "reason": "Banned from the Playkeeper dashboard"},
		{"uuid": "5e1f0a3b-2c4d-4e6f-8a9b-0c1d2e3f4a5b", "name": "Griefer99", "created": "2026-09-25 23:00:00 +0000", "source": "Server", "expires": "forever", "reason": "Banned by an operator"}]`), 0o640)
	for _, name := range []string{"Tobi2009", "griefer99"} {
		if code, out := e.call("GET", e.sp("/players/profile?name="+name+"&tz=UTC"), nil); code != 200 || out["banned"] != true {
			t.Fatalf("banned %s: %d %v", name, code, out)
		}
	}
	if _, out := e.call("GET", e.sp("/players/profile?name=mara_k&tz=UTC"), nil); out["banned"] != nil {
		t.Fatalf("mara_k isn't banned: %v", out)
	}
	for _, c := range []struct {
		query string
		code  int
	}{
		{"?name=nobody_here&tz=UTC", 404},
		{"?name=mara_k&tz=Mars/Olympus", 400},
		{"?name=mara_k", 400},
		{"?name=mara%20k&tz=UTC", 400},
	} {
		if code, out := e.call("GET", e.sp("/players/profile"+c.query), nil); code != c.code {
			t.Errorf("%s: %d %v", c.query, code, out)
		}
	}
}

func TestDayPartsFollowTheLocalClock(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	parts := map[string]int64{}
	// 04:00 to 13:00 local: an hour of night, seven of morning, one of afternoon.
	countDayParts(time.Date(2026, 9, 20, 4, 0, 0, 0, loc), time.Date(2026, 9, 20, 13, 0, 0, 0, loc), loc, parts)
	if parts["night"] != 3600 || parts["morning"] != 7*3600 || parts["afternoon"] != 3600 {
		t.Fatalf("parts: %v", parts)
	}
	// Across midnight, and across the night the clocks go back.
	parts = map[string]int64{}
	countDayParts(time.Date(2026, 10, 31, 21, 0, 0, 0, loc), time.Date(2026, 11, 1, 6, 0, 0, 0, loc), loc, parts)
	if parts["evening"] != 3600 || parts["night"] != 8*3600 || parts["morning"] != 3600 {
		t.Fatalf("parts across the clock change: %v", parts)
	}
}

func TestMessageBanAndAllowlistCommands(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.rcon.setOnline("mara_k")
	code, out := e.call("POST", e.sp("/players/message"), map[string]any{"name": "mara_k", "message": "Dinner in 5 <3", "actor": "admin"})
	if code != 200 {
		t.Fatalf("message: %d %v", code, out)
	}
	e.rcon.mu.Lock()
	last := e.rcon.commands[len(e.rcon.commands)-1]
	e.rcon.mu.Unlock()
	if last != `tellraw mara_k {"color":"gray","italic":true,"translate":"commands.message.display.incoming","with":["Server","Dinner in 5 <3"]}` {
		t.Fatalf("message command: %s", last)
	}
	if e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'player.message' AND target = 'mara_k' AND result = 'succeeded' AND detail = ''`) != 1 ||
		e.countRows(`SELECT COUNT(*) FROM audit WHERE detail LIKE '%Dinner%'`) != 0 {
		t.Fatal("a message is audited without what it said")
	}
	for _, c := range []struct {
		name, msg string
		code      int
	}{
		{"JunoFox", "hi", 409},
		{"mara_k", "two\nlines", 400},
		{"mara_k", "   ", 400},
		{"mara_k", strings.Repeat("a", 201), 400},
		{"mara k", "hi", 400},
	} {
		if code, out := e.call("POST", e.sp("/players/message"), map[string]any{"name": c.name, "message": c.msg, "actor": "admin"}); code != c.code {
			t.Errorf("message %q to %q: %d %v", c.msg, c.name, code, out)
		}
	}

	code, out = e.call("POST", e.sp("/ban"), map[string]any{"name": "mara_k", "actor": "admin"})
	if code != 200 || out["message"] != "Banned mara_k: Banned from the Playkeeper dashboard" {
		t.Fatalf("ban: %d %v", code, out)
	}
	if e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'player.banned' AND target = 'mara_k' AND result = 'succeeded'`) != 1 {
		t.Fatal("a ban is audited")
	}

	code, out = e.call("POST", e.sp("/whitelist"), map[string]any{"name": "JunoFox", "actor": "admin"})
	if code != 200 || out["added"] != true {
		t.Fatalf("allowlist add: %d %v", code, out)
	}
	code, out = e.call("POST", e.sp("/whitelist"), map[string]any{"name": "JunoFox", "actor": "admin"})
	if code != 200 || out["added"] != false || out["message"] != "Player is already whitelisted" {
		t.Fatalf("allowlist add again: %d %v", code, out)
	}
}

func TestKickAndBanAnswerOnceThePlayerHasLeft(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	defer func(d time.Duration) { leaveWait = d }(leaveWait)
	leaveWait = 5 * time.Second
	for _, path := range []string{"/kick", "/ban"} {
		e.rcon.setOnline("mara_k")
		e.addSession("mara_k", e.a.now().Add(-time.Minute), nil, false)
		start := time.Now()
		code, out := e.call("POST", e.sp(path), map[string]any{"name": "Mara_K", "actor": "admin"})
		if took := time.Since(start); code != 200 || took > 4*time.Second {
			t.Fatalf("%s: %d %v after %v, want an answer once they left", path, code, out, took)
		}
		if _, out := e.call("GET", e.sp("/players/profile?name=mara_k&tz=UTC"), nil); out["online"] != false {
			t.Fatalf("right after %s the profile says %v", path, out)
		}
	}

	// A player whose session never closes holds the answer up for leaveWait at most.
	leaveWait = 200 * time.Millisecond
	e.rcon.setOnline("JunoFox")
	e.addSession("JunoFox", e.a.now().Add(-time.Minute), nil, false)
	start := time.Now()
	(&server{Agent: e.a, id: e.sid}).letGo(context.Background(), "JunoFox")
	if took := time.Since(start); took < 150*time.Millisecond || took > 2*time.Second {
		t.Fatalf("waited %v for a player who stayed, want about %v", took, leaveWait)
	}

	// The last sample of who is online forgets them too, until the next one;
	// a status ping's sample may name only some of them.
	before := &api.PlayerSnapshot{Online: 3, Max: 10, Names: []string{"Mara_K", "JunoFox"}, Source: "status ping"}
	s := &server{Agent: e.a, id: "elsewhere", players: before}
	s.letGo(context.Background(), "mara_k")
	if !slices.Equal(s.players.Names, []string{"JunoFox"}) || s.players.Online != 2 || len(before.Names) != 2 {
		t.Fatalf("sample after a kick: %+v, was %+v", s.players, before)
	}
	s.letGo(context.Background(), "Tobi2009")
	if !slices.Equal(s.players.Names, []string{"JunoFox"}) || s.players.Online != 2 {
		t.Fatalf("sample after banning someone offline: %+v", s.players)
	}
}

func TestStoppedServerAllowlistIsWrittenDirectly(t *testing.T) {
	e := newAgentEnv(t)
	e.addIdleServer()
	const uuid = "069a79f4-44e9-4726-a5be-fca90e38aaf5"
	code, out := e.call("POST", e.sp("/whitelist"), map[string]any{"name": "mara_k", "uuid": uuid, "actor": "invite:abcdefghijkmnpqr"})
	if code != 200 || out["added"] != true {
		t.Fatalf("add to a stopped server: %d %v", code, out)
	}
	b, err := os.ReadFile(filepath.Join(e.dataDir(), "whitelist.json"))
	if err != nil || !strings.Contains(string(b), `"uuid": "`+uuid+`"`) || !strings.Contains(string(b), `"name": "mara_k"`) {
		t.Fatalf("whitelist.json: %s %v", b, err)
	}
	code, out = e.call("POST", e.sp("/whitelist"), map[string]any{"name": "mara_k", "uuid": uuid, "actor": "admin"})
	if code != 200 || out["added"] != false {
		t.Fatalf("add again: %d %v", code, out)
	}
	if list, _ := out["whitelist"].([]any); len(list) != 1 {
		t.Fatalf("whitelist: %v", out["whitelist"])
	}
	if code, out := e.call("POST", e.sp("/whitelist"), map[string]any{"name": "JunoFox", "actor": "admin"}); code != 409 {
		t.Fatalf("a name alone needs the running server's own check: %d %v", code, out)
	}
	if code, out := e.call("POST", e.sp("/whitelist"), map[string]any{"name": "JunoFox", "uuid": "not-a-uuid", "actor": "admin"}); code != 400 {
		t.Fatalf("bad uuid: %d %v", code, out)
	}
	if e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'whitelist.add' AND actor = 'invite:abcdefghijkmnpqr' AND result = 'succeeded'`) != 1 {
		t.Fatal("the direct write is audited")
	}
}

// The game can plant links in its own files. Adding someone to a stopped
// server's list, and reading who is banned, never follow them out of the
// data directory.
func TestStoppedServerAllowlistDoesNotFollowLinks(t *testing.T) {
	e := newAgentEnv(t)
	e.addIdleServer()
	host := e.hostFiles()
	before := tree(t, host)
	if err := os.MkdirAll(e.dataDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	plant := func(name, to string) {
		t.Helper()
		p := filepath.Join(e.dataDir(), name)
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(to, p); err != nil {
			t.Fatal(err)
		}
	}
	const uuid = "069a79f4-44e9-4726-a5be-fca90e38aaf5"

	plant("whitelist.json.new", filepath.Join(host, "tls", "key.pem"))
	if code, out := e.call("POST", e.sp("/whitelist"), map[string]any{"name": "mara_k", "uuid": uuid, "actor": "admin"}); code != 200 || out["added"] != true {
		t.Fatalf("add with a link beside whitelist.json: %d %v", code, out)
	}

	plant("whitelist.json", filepath.Join(host, "panel.db"))
	code, out := e.call("POST", e.sp("/whitelist"), map[string]any{"name": "JunoFox", "uuid": "5e1f0a3b-2c4d-4e6f-8a9b-0c1d2e3f4a5b", "actor": "admin"})
	if msg, _ := out["error"].(string); code != 409 || !strings.Contains(msg, "whitelist.json in the server's files is a link") {
		t.Errorf("add with a link at whitelist.json: %d %v", code, out)
	}

	plant("banned-players.json", filepath.Join(host, "panel.db"))
	if _, err := e.srv().bannedNames(); err == nil || !strings.Contains(err.Error(), "banned-players.json in the server's files is a link") {
		t.Errorf("the ban list was read through a link: %v", err)
	}

	if after := tree(t, host); !maps.Equal(after, before) {
		t.Fatalf("Playkeeper's files changed:\n%v\nwas\n%v", after, before)
	}
}
