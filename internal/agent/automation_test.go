package agent

// Wave 7 (0.4.0): schedules, sleep when nobody's playing, backup rules with
// copies somewhere else, and disk space.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/discord"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/offsite"
	"github.com/CIYAhq/playkeeper/internal/schedule"
	"github.com/CIYAhq/playkeeper/internal/sleep"
)

// clockBefore moves the agent's clock to lead before a whole minute and
// returns that minute.
func (e *agentEnv) clockBefore(lead time.Duration) time.Time {
	now := e.a.now()
	due := now.Add(lead + time.Minute).Truncate(time.Minute)
	e.skew.Add(int64(due.Add(-lead).Sub(now)))
	return due.UTC()
}

func (e *agentEnv) waitUpTo(d time.Duration, what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %s", what)
}

func (e *agentEnv) rconCommands() []string {
	e.rcon.mu.Lock()
	defer e.rcon.mu.Unlock()
	return append([]string(nil), e.rcon.commands...)
}

func onceAt(due time.Time) map[string]any {
	return map[string]any{"kind": "once", "date": due.Format("2006-01-02"), "at": due.Format("15:04"), "timeZone": "UTC"}
}

func TestSchedulesAreCheckedAuditedAndListed(t *testing.T) {
	e := newAgentEnv(t)
	e.addIdleServer()
	berlin := map[string]any{"kind": "daily", "at": "04:00", "timeZone": "Europe/Berlin"}
	for name, body := range map[string]map[string]any{
		"a stop command":  {"kind": "command", "timing": berlin, "payload": map[string]any{"command": "stop"}},
		"every 5 hours":   {"kind": "backup", "timing": map[string]any{"kind": "interval", "everyHours": 5, "at": "00:00", "timeZone": "UTC"}},
		"an unknown zone": {"kind": "backup", "timing": map[string]any{"kind": "daily", "at": "04:00", "timeZone": "Mars/Olympus"}},
		"no timing":       {"kind": "restart"},
		"warned too late": {"kind": "restart", "timing": berlin, "payload": map[string]any{"warnSeconds": []int{2}}},
	} {
		body["actor"] = "admin"
		if code, out := e.call("POST", e.sp("/schedules"), body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	code, out := e.call("POST", e.sp("/schedules"), map[string]any{"actor": "admin", "kind": "restart", "timing": berlin,
		"payload": map[string]any{"warnSeconds": []int{600, 300, 60}, "message": "Survival restarts in {minutes} minutes.", "skipIfPlaying": true}})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, out)
	}
	sid, _ := out["id"].(string)
	if sid == "" || out["nextRun"] == nil || out["summary"] == "" || out["createdBy"] != "admin" {
		t.Fatalf("created schedule: %v", out)
	}
	if code, out := e.call("GET", e.sp("/schedules"), nil); code != 200 || len(out["schedules"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, out)
	}
	if code, out := e.call("POST", e.sp("/schedules/"+sid), map[string]any{"actor": "owner", "enabled": false}); code != 200 || out["enabled"] != false || out["updatedBy"] != "owner" {
		t.Fatalf("switch off: %d %v", code, out)
	}
	code, out = e.call("POST", e.sp("/schedules/preview"), map[string]any{"kind": "backup", "timing": map[string]any{"kind": "interval", "everyHours": 6, "at": "00:00", "timeZone": "UTC"}})
	if code != 200 || out["valid"] != true || len(out["nextRuns"].([]any)) != 3 {
		t.Fatalf("preview: %d %v", code, out)
	}
	code, out = e.call("POST", e.sp("/schedules/preview"), map[string]any{"kind": "backup", "timing": map[string]any{"kind": "interval", "everyHours": 5, "at": "00:00", "timeZone": "UTC"}})
	if bad, _ := out["error"].(map[string]any); code != 200 || out["valid"] != false || bad["field"] != "timing.everyHours" {
		t.Fatalf("preview of a bad timing: %d %v", code, out)
	}
	if code, _ := e.call("POST", e.sp("/schedules/NOPE"), map[string]any{"actor": "admin", "enabled": true}); code != http.StatusBadRequest {
		t.Fatalf("bad schedule id: %d", code)
	}
	if code, _ := e.call("DELETE", e.sp("/schedules/"+sid+"?actor=admin"), nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action IN ('schedule.created', 'schedule.disabled', 'schedule.deleted') AND target = ?`, sid); n != 3 {
		t.Fatalf("audited %d schedule changes, want 3", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM schedules`); n != 0 {
		t.Fatalf("%d schedules left", n)
	}
}

func TestScheduledRestartWarnsPlayersThenRestarts(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	due := e.clockBefore(8 * time.Second)
	code, out := e.call("POST", e.sp("/schedules"), map[string]any{"actor": "admin", "kind": "restart", "timing": onceAt(due),
		"payload": map[string]any{"warnSeconds": []int{5}, "message": "Survival restarts in {minutes} minutes."}})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, out)
	}
	sid := out["id"].(string)
	e.waitUpTo(30*time.Second, "the scheduled restart", func() bool {
		return e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'restart' AND actor = ? AND status = 'succeeded'`, schedule.Actor(sid)) == 1
	})
	var warned, now bool
	for _, c := range e.rconCommands() {
		warned = warned || strings.HasPrefix(c, `tellraw @a {"text":"Survival restarts in `) && strings.Contains(c, "second")
		now = now || strings.Contains(c, "The server is restarting now.")
	}
	if !warned || !now {
		t.Fatalf("warnings sent: warned %v, now %v: %q", warned, now, e.rconCommands())
	}
	e.waitFor("online again", e.onlineIdle)
	code, out = e.call("GET", e.sp("/schedules/runs"), nil)
	runs, _ := out["runs"].([]any)
	if code != 200 || len(runs) != 1 {
		t.Fatalf("runs: %d %v", code, out)
	}
	if r := runs[0].(map[string]any); r["scheduleId"] != sid || r["result"] != "succeeded" || r["operationId"] == "" {
		t.Fatalf("run: %v", r)
	}
}

func TestScheduledCommandsGoStraightOverRCON(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	// A scheduled say is skipped while nobody is online to read it.
	e.rcon.setOnline("Alex")
	due := e.clockBefore(3 * time.Second)
	var ids []string
	for _, cmd := range []string{"save-all flush", "say Backup at 203.0.113.7 in 5 minutes"} {
		code, out := e.call("POST", e.sp("/schedules"), map[string]any{"actor": "admin", "kind": "command", "timing": onceAt(due), "payload": map[string]any{"command": cmd}})
		if code != http.StatusCreated {
			t.Fatalf("create %q: %d %v", cmd, code, out)
		}
		ids = append(ids, out["id"].(string))
	}
	e.waitUpTo(30*time.Second, "both commands", func() bool {
		return e.countRows(`SELECT COUNT(*) FROM schedule_runs WHERE result = 'succeeded'`) == 2
	})
	var saved, said bool
	for _, c := range e.rconCommands() {
		saved = saved || c == "save-all flush"
		said = said || strings.HasPrefix(c, "tellraw @a ") && strings.Contains(c, "Backup at 203.0.113.7 in 5 minutes")
	}
	if !saved || !said {
		t.Fatalf("sent %q", e.rconCommands())
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'console.command' AND actor = ? AND detail = 'save-all flush'`, schedule.Actor(ids[0])); n != 1 {
		t.Fatalf("audited the scheduled command %d times", n)
	}
	var detail string
	if err := e.a.db.QueryRow(`SELECT detail FROM audit WHERE action = 'console.command' AND actor = ?`, schedule.Actor(ids[1])).Scan(&detail); err != nil || strings.Contains(detail, "203.0.113.7") {
		t.Fatalf("audit of the say command: %q %v", detail, err)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM operations WHERE actor LIKE 'schedule:%'`); n != 0 {
		t.Fatalf("console commands made %d operations", n)
	}
}

func TestAutomaticBackupsAndRulesDeleteWhatTheyNoLongerKeep(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	code, out := e.call("POST", e.sp("/backup-rules"), map[string]any{"actor": "admin", "automatic": map[string]any{"enabled": true, "everyHours": 5, "onlyIfPlayed": true}})
	if code != http.StatusBadRequest || out["field"] != "automatic.everyHours" {
		t.Fatalf("every 5 hours: %d %v", code, out)
	}
	code, out = e.call("POST", e.sp("/backup-rules"), map[string]any{"actor": "admin", "timeZone": "Europe/Berlin",
		"automatic": map[string]any{"enabled": true, "everyHours": 6, "onlyIfPlayed": true},
		"rules":     map[string]any{"onHost": map[string]any{"last": 2}, "offSite": map[string]any{"daily": 14}}})
	if code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}
	auto := out["automatic"].(map[string]any)
	sid, _ := auto["scheduleId"].(string)
	if auto["enabled"] != true || auto["everyHours"] != float64(6) || sid == "" || auto["nextRun"] == nil || out["custom"] != true {
		t.Fatalf("view: %v", out)
	}
	code, out = e.call("GET", e.sp("/schedules"), nil)
	list := out["schedules"].([]any)
	if code != 200 || len(list) != 1 {
		t.Fatalf("schedules: %d %v", code, out)
	}
	if timing := list[0].(map[string]any)["timing"].(map[string]any); timing["kind"] != "interval" || timing["everyHours"] != float64(6) || timing["timeZone"] != "Europe/Berlin" {
		t.Fatalf("automatic backup schedule: %v", timing)
	}

	// The editor's totals, for rules not saved yet.
	code, out = e.call("POST", e.sp("/backup-rules/estimate"), map[string]any{"actor": "admin", "timeZone": "Europe/Berlin",
		"rules": map[string]any{"onHost": map[string]any{"hours": 24, "daily": 7, "weekly": 4}, "offSite": map[string]any{"daily": 14, "weekly": 8, "monthly": 12}}})
	if code != 200 {
		t.Fatalf("estimate: %d %v", code, out)
	}
	if on := out["onHost"].(map[string]any); on["count"] != float64(13) || len(on["rows"].([]any)) != 4 {
		t.Fatalf("on this machine, the defaults every 6 hours keep 13: %v", on)
	}
	if off := out["offSite"].(map[string]any); off["count"] != float64(29) {
		t.Fatalf("off the server, the defaults every 6 hours keep 29: %v", off)
	}
	if code, out = e.call("POST", e.sp("/backup-rules/estimate"), map[string]any{"actor": "admin", "rules": map[string]any{"onHost": map[string]any{"hours": 721}}}); code != http.StatusBadRequest || out["field"] != "onHost.hours" {
		t.Fatalf("721 hours: %d %v", code, out)
	}
	if code, out = e.call("GET", e.sp("/backup-rules"), nil); code != 200 || out["rules"].(map[string]any)["onHost"].(map[string]any)["last"] != float64(2) {
		t.Fatalf("an estimate saved the rules: %d %v", code, out["rules"])
	}
	code, out = e.call("POST", e.sp("/backup-rules"), map[string]any{"actor": "admin", "automatic": map[string]any{"enabled": false, "everyHours": 6, "onlyIfPlayed": true}})
	if code != 200 || out["automatic"].(map[string]any)["enabled"] != false {
		t.Fatalf("turn off: %d %v", code, out)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM schedules WHERE id = ? AND enabled = 0`, sid); n != 1 {
		t.Fatal("turning automatic backups off left their schedule on")
	}

	// The runner's backups, made now rather than every 6 hours.
	ss := scheduleServer{e.srv()}
	op := schedule.Operation{Kind: schedule.OpBackup, Actor: schedule.Actor(sid), ScheduleID: sid}
	run := func() (string, error) {
		for {
			opID, err := ss.Run(context.Background(), op)
			if !errors.Is(err, schedule.ErrBusy) {
				return opID, err
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	var made []string
	for i := 0; i < 3; i++ {
		opID, err := run()
		if err != nil {
			t.Fatalf("scheduled backup %d: %v", i, err)
		}
		done := e.waitOp(opID)
		made = append(made, done.Detail["backupId"].(string))
	}
	list2, err := e.srv().listBackups("")
	if err != nil {
		t.Fatal(err)
	}
	if len(list2) != 2 || list2[0].ID != made[2] || list2[1].ID != made[1] || list2[0].Kind != "scheduled" {
		t.Fatalf("kept %+v, made %v", list2, made)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'backup.deleted' AND actor = 'backup rules' AND target = ?`, made[0]); n != 1 {
		t.Fatalf("the rules deleted the oldest backup %d times", n)
	}
	op.OnlyIfPlayed = true
	if _, err := run(); !errors.Is(err, schedule.ErrNobodyPlayed) {
		t.Fatalf("a backup after nobody played: %v", err)
	}
}

// Scheduled backups never stop a running server, so one that world saving
// can't be paused for is refused, and a refusal never passes unseen: each is
// a line in the recent activity with its reason and sends the backup-failed
// Discord alert if that's on, and the server's status carries the refusals
// for the World tab until a backup succeeds. A backup someone asked for, or a
// scheduled one that fails for another reason, isn't a refusal.
func TestARefusedScheduledBackupIsShownUntilABackupSucceeds(t *testing.T) {
	f := startFakeHook(t)
	e := newAgentEnv(t)
	e.stop()
	e.discordClient = f.client()
	e.start()
	e.create()
	e.connectDiscord()
	alertsOn := func(kinds ...string) {
		t.Helper()
		if code, out := e.call("PUT", "/v1/discord", map[string]any{"alerts": kinds, "liveStatus": false, "actor": "admin"}); code != 200 {
			t.Fatalf("alert settings: %d %v", code, out)
		}
	}
	backupFailedAlerts := func(from int) int {
		n := 0
		for _, a := range f.alertsSince(t, from) {
			if a.Kind == discord.KindBackupFailed {
				n++
			}
		}
		return n
	}
	idle := func() {
		t.Helper()
		e.waitFor("the operation lock free", func() bool {
			release, ok := e.srv().holdOpLock()
			if ok {
				release()
			}
			return ok
		})
	}
	const sid = "qrstuvwxyz"
	scheduled := func() *api.Operation {
		t.Helper()
		for {
			opID, err := (scheduleServer{e.srv()}).Run(context.Background(), schedule.Operation{Kind: schedule.OpBackup, Actor: schedule.Actor(sid), ScheduleID: sid})
			if errors.Is(err, schedule.ErrBusy) {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			if err == nil || opID == "" {
				t.Fatalf("a scheduled backup meant to fail: %q %v", opID, err)
			}
			op := e.waitOp(opID)
			if op.Status != api.OpFailed {
				t.Fatalf("scheduled backup: %+v", op)
			}
			return op
		}
	}
	refusals := func() []api.Activity {
		t.Helper()
		list, err := e.a.Activity(e.sid, 50)
		if err != nil {
			t.Fatal(err)
		}
		var out []api.Activity
		for _, a := range list {
			if a.Kind == "backup_refused" {
				out = append(out, a)
			}
		}
		return out
	}

	// A plugin answers save-off, so saving can't be paused. The backup-failed
	// alert is off.
	alertsOn("crash")
	e.rcon.setAnswer(func(cmd string) (string, bool) {
		if cmd == "save-off" {
			return `Unknown command. Type "/help" for help.`, true
		}
		return "", false
	})
	from := f.mark()
	first := scheduled()
	r1 := e.status().BackupRefused
	if first.Detail["errorKind"] != "unexpected_reply" || r1 == nil || r1.Count != 1 || r1.Kind != "unexpected_reply" || r1.Error != first.Error ||
		r1.Hint != first.Hint || r1.Hint == "" || r1.ScheduleID != sid || r1.OperationID != first.ID || !r1.Since.Equal(r1.At) {
		t.Fatalf("after the first refusal: %+v, status %+v", first, r1)
	}
	if got := refusals(); len(got) != 1 || got[0].Detail != "unexpected_reply" {
		t.Fatalf("the recent activity after the first refusal: %+v", got)
	}
	time.Sleep(700 * time.Millisecond)
	if n := backupFailedAlerts(from); n != 0 {
		t.Fatalf("%d backup-failed alerts while that alert is off", n)
	}

	// The next run finds the server starting, with the alert on.
	alertsOn("crash", "backup_failed")
	e.srv().setRunPhase(api.PhaseStarting, "")
	second := scheduled()
	e.srv().setRunPhase(api.PhaseOnline, "")
	r2 := e.status().BackupRefused
	if r2 == nil || r2.Count != 2 || r2.Kind != "not_online" || r2.OperationID != second.ID || !r2.Since.Equal(r1.At) || !r2.At.After(r1.At) {
		t.Fatalf("after the second refusal: %+v, status %+v", second, r2)
	}
	if got := refusals(); len(got) != 2 || got[0].Detail != "not_online" {
		t.Fatalf("the recent activity after the second refusal: %+v", got)
	}
	e.waitFor("the backup-failed alert", func() bool { return backupFailedAlerts(from) == 1 })

	// Neither a backup someone asked for nor a scheduled one without room
	// is a refusal.
	idle()
	if op := e.backupNow(nil); op.Status != api.OpFailed || op.Detail["errorKind"] != "unexpected_reply" {
		t.Fatalf("a backup asked for: %+v", op)
	}
	e.diskFree.Store(1 << 20)
	if op := scheduled(); op.Detail["errorKind"] != "insufficient_space" {
		t.Fatalf("a scheduled backup without room: %+v", op)
	}
	e.diskFree.Store(0)
	if r := e.status().BackupRefused; r == nil || r.Count != 2 || r.OperationID != second.ID {
		t.Fatalf("other failures changed the refusals: %+v", r)
	}
	if got := refusals(); len(got) != 2 {
		t.Fatalf("other failures are in the recent activity as refusals: %+v", got)
	}

	// "Back up now" stops the server for the backup, so it needs no pause,
	// and a backup that succeeds ends the refusals.
	idle()
	if op := e.backupNow(map[string]any{"stopped": true}); op.Status != api.OpSucceeded {
		t.Fatalf("a backup with the server stopped: %+v", op)
	}
	if r := e.status().BackupRefused; r != nil {
		t.Fatalf("a backup succeeded, and the refusals are still shown: %+v", r)
	}
	if got := refusals(); len(got) != 2 {
		t.Fatalf("the refusals left the recent activity: %+v", got)
	}
}

// joinStandIn tries to join the way a game client does and returns the text
// of the disconnect message.
func joinStandIn(t *testing.T, addr, name string) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	str := func(s string) []byte { return append(varint(len(s)), s...) }
	packet := func(fields ...[]byte) []byte {
		body := []byte{0x00}
		for _, f := range fields {
			body = append(body, f...)
		}
		return append(varint(len(body)), body...)
	}
	hello := packet(varint(775), str("localhost"), []byte{0x63, 0xdd}, varint(2))
	if _, err := c.Write(append(hello, packet(str(name), make([]byte, 16))...)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	n, err := readVarint(br)
	if err != nil {
		t.Fatalf("join as %s: %v", name, err)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(br, body); err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(body)
	if id, err := readVarint(r); err != nil || id != 0 {
		t.Fatalf("packet %d %v, want a login disconnect", id, err)
	}
	sl, err := readVarint(r)
	if err != nil || sl != r.Len() {
		t.Fatalf("disconnect reason of %d bytes in %d", sl, r.Len())
	}
	var text struct {
		Text string `json:"text"`
	}
	raw, _ := io.ReadAll(r)
	if err := json.Unmarshal(raw, &text); err != nil {
		t.Fatalf("disconnect reason %q: %v", raw, err)
	}
	return text.Text
}

func readVarint(r io.ByteReader) (int, error) {
	var v uint32
	for i := 0; i < 5; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		v |= uint32(b&0x7F) << (7 * i)
		if b&0x80 == 0 {
			return int(v), nil
		}
	}
	return 0, errors.New("varint too long")
}

// localStandIn puts stand-ins on a free local port for the rest of the test.
func localStandIn(t *testing.T) {
	prev := standInAddr
	standInAddr = func(int) string { return "127.0.0.1:0" }
	t.Cleanup(func() { standInAddr = prev })
}

// putToSleep turns sleep on and puts the server to sleep, with its
// stand-in on a free local port, as sleep-when-empty does once nobody has
// played for long enough.
func (e *agentEnv) putToSleep() {
	e.t.Helper()
	localStandIn(e.t)
	if code, out := e.call("POST", e.sp("/sleep"), map[string]any{"actor": "admin", "enabled": true, "idleMinutes": 5}); code != http.StatusOK {
		e.t.Fatalf("turn sleep on: %d %v", code, out)
	}
	s := e.srv()
	s.fallAsleep(s.sleepSettings())
	e.waitFor("the server asleep", func() bool { return e.status().Phase == api.PhaseAsleep && !e.a.busy() })
}

// holdOp runs an operation of kind that lasts until release is called, as
// a long backup does.
func (e *agentEnv) holdOp(kind string) (release func()) {
	e.t.Helper()
	done := make(chan struct{})
	if _, err := e.srv().beginOp(kind, "admin", func(context.Context, *opHandle) error {
		<-done
		return nil
	}); err != nil {
		e.t.Fatalf("%s: %v", kind, err)
	}
	var once sync.Once
	release = func() { once.Do(func() { close(done) }) }
	e.t.Cleanup(release)
	return release
}

func TestServerSleepsWhenEmptyAndWakesForAListedPlayer(t *testing.T) {
	localStandIn(t)
	e := newAgentEnv(t)
	e.create()
	if err := os.WriteFile(filepath.Join(e.dataDir(), "whitelist.json"), []byte(`[{"name":"Alex","uuid":"00000000-0000-0000-0000-00000000a1e7"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := e.call("POST", e.sp("/sleep"), map[string]any{"actor": "admin", "enabled": true, "idleMinutes": 3}); code != http.StatusBadRequest {
		t.Fatalf("3 minutes: %d %v", code, out)
	}
	code, out := e.call("POST", e.sp("/sleep"), map[string]any{"actor": "admin", "enabled": true, "idleMinutes": 5})
	if st, _ := out["sleep"].(map[string]any); code != 200 || st["enabled"] != true || st["idleMinutes"] != float64(5) {
		t.Fatalf("turn on: %d %v", code, out)
	}
	// Nobody plays for 5 minutes, after the 10 minutes a started server
	// always stays up; the sampler sees every minute of it.
	e.waitUpTo(20*time.Second, "the server to fall asleep", func() bool {
		if e.status().Phase == api.PhaseAsleep {
			return true
		}
		e.skew.Add(int64(time.Minute))
		time.Sleep(150 * time.Millisecond)
		return false
	})
	st := e.status()
	if st.Desired != api.DesiredSleeping || st.Sleep == nil || !st.Sleep.Listening || st.Sleep.AsleepSince == nil {
		t.Fatalf("asleep status: %+v %+v", st, st.Sleep)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'sleep' AND actor = 'sleep' AND status = 'succeeded'`); n != 1 {
		t.Fatalf("%d sleep operations", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'server_fell_asleep'`); n != 1 {
		t.Fatalf("%d fell-asleep events", n)
	}
	if m := e.a.Machine(context.Background()); m.SleepingMemoryMB != 1536 {
		t.Fatalf("sleeping memory %d MB", m.SleepingMemoryMB)
	}
	if code, out := e.call("GET", e.sp("/sleep?tz=Mars%2FOlympus"), nil); code != http.StatusBadRequest {
		t.Fatalf("sleep with an unknown time zone: %d %s", code, out)
	}
	berlin, _ := time.LoadLocation("Europe/Berlin")
	fellToday := 0.0
	if st.Sleep.AsleepSince.In(berlin).Format(time.DateOnly) == e.srv().now().In(berlin).Format(time.DateOnly) {
		fellToday = 1
	}
	if code, out := e.call("GET", e.sp("/sleep?tz=Europe%2FBerlin"), nil); code != http.StatusOK {
		t.Fatalf("sleep: %d %v", code, out)
	} else if today, _ := out["today"].(map[string]any); today["count"] != fellToday {
		t.Fatalf("sleep today: %v, want %v times", out, fellToday)
	}
	// The reconciler leaves a sleeping server alone.
	time.Sleep(300 * time.Millisecond)
	if _, running, err := e.srv().containerRunning(context.Background()); err != nil || running || e.status().Phase != api.PhaseAsleep {
		t.Fatalf("a sleeping server was started: %v %v %+v", running, err, e.status())
	}

	addr := e.srv().auto.standIn.Addr()
	ping, err := minecraft.Ping(addr, 2*time.Second)
	if err != nil || ping.Online != 0 || !strings.Contains(ping.VersionName, "26.1.2") {
		t.Fatalf("ping the stand-in: %+v %v", ping, err)
	}
	stranger := joinStandIn(t, addr, "Steve")
	time.Sleep(300 * time.Millisecond)
	if e.status().Desired != api.DesiredSleeping || e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'wake'`) != 0 {
		t.Fatal("a player who isn't on the list woke the server")
	}
	listed := joinStandIn(t, addr, "Alex")
	if stranger != listed || !strings.Contains(listed, "asleep") {
		t.Fatalf("replies differ or say too much: %q, %q", stranger, listed)
	}
	e.waitUpTo(20*time.Second, "the wake-up", func() bool {
		return e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'wake' AND actor = 'wake:Alex' AND status = 'succeeded'`) == 1
	})
	e.waitFor("online", func() bool { return e.status().Phase == api.PhaseOnline })
	if st := e.status(); st.Desired != api.DesiredRunning || st.Sleep.AsleepSince != nil {
		t.Fatalf("awake status: %+v %+v", st, st.Sleep)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'server_woke_up' AND player = 'Alex'`); n != 1 {
		t.Fatalf("%d woke-up events", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM sleep_periods WHERE end_ts IS NOT NULL AND woke_by = 'wake:Alex'`); n != 1 {
		t.Fatalf("%d finished sleep periods", n)
	}
}

// Each way a sleeping server wakes, or stays asleep, leaves its desired
// state, its stand-in, its sleep setting and its sleep periods agreeing:
// the reconciler starts a server meant to be running, the stand-in wakes
// only a server meant to be asleep, and the sleep loop looks after one
// whose stand-in isn't answering.
func TestSleepAndWakeTransitions(t *testing.T) {
	type state struct {
		desired   string
		listening bool
		sleepOn   bool
		phase     api.Phase
	}
	var (
		asleep = state{api.DesiredSleeping, true, true, api.PhaseAsleep}
		awake  = state{api.DesiredRunning, false, true, api.PhaseOnline}
	)
	read := func(e *agentEnv) state {
		s := e.srv()
		s.auto.mu.Lock()
		m := s.auto.standIn
		s.auto.mu.Unlock()
		return state{s.desired(), m != nil && m.Listening(), s.sleepSettings().Enabled, e.status().Phase}
	}
	dockerDown := func(e *agentEnv, prefix string) {
		e.fd.mu.Lock()
		e.fd.down = prefix
		e.fd.mu.Unlock()
	}
	wake := func(e *agentEnv) *api.Operation {
		e.t.Helper()
		s := e.srv()
		op, err := s.beginOp("wake", "wake:Alex", s.wakeOp("Alex"))
		if err != nil {
			e.t.Fatalf("wake: %v", err)
		}
		return e.waitOp(op.ID)
	}
	sleepOff := func(e *agentEnv) (int, map[string]any) {
		e.t.Helper()
		return e.call("POST", e.sp("/sleep"), map[string]any{"actor": "admin", "enabled": false, "idleMinutes": 5})
	}
	cases := []struct {
		name  string
		steps func(e *agentEnv)
		want  state
	}{
		{name: "a player wakes it", steps: func(e *agentEnv) {
			if o := wake(e); o.Status != api.OpSucceeded {
				e.t.Fatalf("wake: %+v", o)
			}
		}, want: awake},
		{name: "a wake whose start fails", steps: func(e *agentEnv) {
			e.fd.mu.Lock()
			e.fd.startErr = "driver failed programming external connectivity: Bind for 0.0.0.0:25565 failed: port is already allocated"
			e.fd.mu.Unlock()
			if o := wake(e); o.Status != api.OpFailed {
				e.t.Fatalf("wake: %+v", o)
			}
		}, want: asleep},
		{name: "a wake that fails while Docker can't say whether the server runs", steps: func(e *agentEnv) {
			dockerDown(e, "/containers/"+e.cname()+"/json")
			o := wake(e)
			dockerDown(e, "")
			if o.Status != api.OpFailed {
				e.t.Fatalf("wake: %+v", o)
			}
		}, want: asleep},
		{name: "a wake that finds the server software changed", steps: func(e *agentEnv) {
			s := e.srv()
			sc, _ := s.serverConfig()
			if err := os.WriteFile(s.jarPath(*sc), []byte("tampered"), 0o644); err != nil {
				e.t.Fatal(err)
			}
			if o := wake(e); o.Status != api.OpFailed || !strings.Contains(o.Error, "doesn't match what Playkeeper installed") {
				e.t.Fatalf("wake: %+v", o)
			}
		}, want: state{api.DesiredStopped, false, true, api.PhaseCrashed}},
		{name: "a player wakes it during a backup that outlasts its retries", steps: func(e *agentEnv) {
			release := e.holdOp("backup")
			began, second := make(chan struct{}), make(chan struct{})
			go func() {
				e.srv().wakeFor("Alex")
				close(began)
			}()
			s := e.srv()
			e.waitFor("the wake to wait", func() bool {
				s.auto.mu.Lock()
				defer s.auto.mu.Unlock()
				return s.auto.wakePending
			})
			// Another join meanwhile adds no second waiting wake.
			go func() {
				s.wakeFor("Steve")
				close(second)
			}()
			select {
			case <-second:
			case <-time.After(time.Second):
				e.t.Fatal("a second join waited too")
			}
			time.Sleep(wakeRetry + wakeRetry/2)
			if s.desired() != api.DesiredSleeping {
				e.t.Fatal("the server woke during the backup")
			}
			release()
			select {
			case <-began:
			case <-time.After(10 * time.Second):
				e.t.Fatal("the wake never began")
			}
			if n := e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'wake' AND actor = 'wake:Alex'`); n != 1 {
				e.t.Fatalf("%d wakes for Alex", n)
			}
		}, want: awake},
		{name: "started outside Playkeeper", steps: func(e *agentEnv) {
			e.fd.mu.Lock()
			c := e.fd.server()
			c.running, c.started, c.finished, c.exitCode = true, time.Now().UTC(), time.Time{}, 0
			e.fd.log(c, `[12:00:01 INFO]: Done (1.000s)! For help, type "help"`)
			e.fd.mu.Unlock()
			e.srv().resumeSleep(context.Background())
		}, want: awake},
		{name: "a player wakes it during a backup", steps: func(e *agentEnv) {
			release := e.holdOp("backup")
			began := make(chan struct{})
			go func() {
				e.srv().wakeFor("Alex")
				close(began)
			}()
			time.Sleep(300 * time.Millisecond)
			if e.srv().desired() != api.DesiredSleeping {
				e.t.Fatal("the server woke during the backup")
			}
			release()
			select {
			case <-began:
			case <-time.After(10 * time.Second):
				e.t.Fatal("the wake never began")
			}
		}, want: awake},
		{name: "sleep turned off", steps: func(e *agentEnv) {
			if code, out := sleepOff(e); code != http.StatusOK || out["operation"] == nil {
				e.t.Fatalf("sleep off: %d %v", code, out)
			}
		}, want: state{api.DesiredRunning, false, false, api.PhaseOnline}},
		{name: "sleep turned off during a backup", steps: func(e *agentEnv) {
			release := e.holdOp("backup")
			code, out := sleepOff(e)
			release()
			if code != http.StatusConflict || out["code"] != api.CodeBusy {
				e.t.Fatalf("sleep off during a backup: %d %v", code, out)
			}
		}, want: asleep},
		{name: "sleep turned off, and the server can't start", steps: func(e *agentEnv) {
			dockerDown(e, "/images/")
			code, out := sleepOff(e)
			op, _ := out["operation"].(map[string]any)
			if code != http.StatusOK || op == nil {
				dockerDown(e, "")
				e.t.Fatalf("sleep off: %d %v", code, out)
			}
			o := e.waitOp(op["id"].(string))
			dockerDown(e, "")
			if o.Status != api.OpFailed {
				e.t.Fatalf("start: %+v", o)
			}
		}, want: state{api.DesiredStopped, false, false, api.PhaseStopped}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAgentEnv(t)
			e.create()
			e.putToSleep()
			c.steps(e)
			e.waitFor("the operation over", func() bool { return !e.a.busy() })
			got := read(e)
			for deadline := time.Now().Add(5 * time.Second); got != c.want && time.Now().Before(deadline); got = read(e) {
				time.Sleep(50 * time.Millisecond)
			}
			if got != c.want {
				t.Fatalf("the server is left %+v, want %+v", got, c.want)
			}
			if open := e.countRows(`SELECT COUNT(*) FROM sleep_periods WHERE end_ts IS NULL`); (open == 1) != (c.want.desired == api.DesiredSleeping) {
				t.Fatalf("%d sleep periods open, with the server meant to be %s", open, c.want.desired)
			}
		})
	}
}

// A start that fails leaves nothing answering for a sleeping server, which
// isn't asleep any more: the start left it stopped. Wake up now and Start
// both start it this way.
func TestAFailedStartLeavesNothingAnsweringForTheServer(t *testing.T) {
	cases := []struct {
		name string
		// fail makes the start fail, and returns what undoes it.
		fail func(e *agentEnv) func()
	}{
		{name: "the image can't be pulled", fail: func(e *agentEnv) func() {
			e.fd.mu.Lock()
			e.fd.down = "/images/"
			e.fd.mu.Unlock()
			return func() {
				e.fd.mu.Lock()
				e.fd.down = ""
				e.fd.mu.Unlock()
			}
		}},
		{name: "the server software changed", fail: func(e *agentEnv) func() {
			s := e.srv()
			sc, _ := s.serverConfig()
			if err := os.WriteFile(s.jarPath(*sc), []byte("tampered"), 0o644); err != nil {
				e.t.Fatal(err)
			}
			return func() {}
		}},
		{name: "the container can't start", fail: func(e *agentEnv) func() {
			e.fd.mu.Lock()
			e.fd.startErr = "driver failed programming external connectivity: Bind for 0.0.0.0:25565 failed: port is already allocated"
			e.fd.mu.Unlock()
			return func() {
				e.fd.mu.Lock()
				e.fd.startErr = ""
				e.fd.mu.Unlock()
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAgentEnv(t)
			e.create()
			e.putToSleep()
			undo := c.fail(e)
			op := e.runOp("POST", "/start")
			undo()
			s := e.srv()
			s.auto.mu.Lock()
			m := s.auto.standIn
			s.auto.mu.Unlock()
			desired, listening := s.desired(), m != nil && m.Listening()
			open := e.countRows(`SELECT COUNT(*) FROM sleep_periods WHERE end_ts IS NULL`)
			if op.Status != api.OpFailed || desired != api.DesiredStopped || listening || open != 0 {
				t.Fatalf("after the failed start (%s): desired %s, stand-in listening %v, %d sleep periods open; want it stopped with nothing answering", op.Error, desired, listening, open)
			}
		})
	}
}

// Every operation the agent starts has a label for the busy message, so
// "Playkeeper is busy with …" never ends in nothing.
func TestEveryOperationHasABusyLabel(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	kinds := map[string]string{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "beginOp" && sel.Sel.Name != "beginMachineOp" && sel.Sel.Name != "startOp") {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				kind, _ := strconv.Unquote(lit.Value)
				kinds[kind] = fset.Position(lit.Pos()).String()
			}
			return true
		})
	}
	for _, kind := range []string{"backup", "sleep", "offsite-restore", "offsite-recover", "disk-cleanup"} {
		if kinds[kind] == "" {
			t.Fatalf("found no %s operation among %v", kind, kinds)
		}
	}
	for kind, at := range kinds {
		if opLabels[kind] == "" {
			t.Errorf("%s: the %s operation has no busy label", at, kind)
		}
	}
}

// A running map pre-generation keeps an empty server awake, as an operation
// does, so it sleeps once the task is paused or over. Until Chunky reports
// on the task, as after the agent starts, the task runs unless it was
// paused.
func TestSleepWaitsForTheMapPreGeneration(t *testing.T) {
	// restart starts the agent again, checking on the task only when asked.
	restart := func(e *agentEnv) {
		e.stop()
		e.tweak = func(o *Options) { o.PregenInterval = time.Hour }
		e.start()
	}
	cases := []struct {
		name   string
		steps  func(e *agentEnv, fc *fakeChunky)
		sleeps bool
	}{
		{name: "running", steps: func(*agentEnv, *fakeChunky) {}},
		{name: "running, before Chunky reports", steps: func(e *agentEnv, _ *fakeChunky) { restart(e) }},
		{name: "paused", steps: func(e *agentEnv, _ *fakeChunky) { e.pregenAct("pause") }, sleeps: true},
		{name: "paused, before Chunky reports", steps: func(e *agentEnv, _ *fakeChunky) {
			e.pregenAct("pause")
			restart(e)
		}, sleeps: true},
		{name: "paused from the console", steps: func(e *agentEnv, fc *fakeChunky) {
			fc.answer("chunky pause world")
			e.waitFor("Chunky to report the pause", func() bool { return e.pregen().State == "paused" })
		}, sleeps: true},
		{name: "finished", steps: func(e *agentEnv, fc *fakeChunky) {
			fc.finish(7 * time.Minute)
			e.waitFor("the finished task", func() bool { return e.pregen().State == "finished" })
		}, sleeps: true},
		{name: "cancelled", steps: func(e *agentEnv, _ *fakeChunky) { e.pregenAct("cancel") }, sleeps: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			localStandIn(t)
			e := newAgentEnv(t)
			e.withSources()
			e.create()
			fc := e.chunky()
			e.startPregen("small", true)
			if code, out := e.call("POST", e.sp("/sleep"), map[string]any{"actor": "admin", "enabled": true, "idleMinutes": 5}); code != http.StatusOK {
				t.Fatalf("turn sleep on: %d %v", code, out)
			}
			c.steps(e, fc)
			// Nobody plays. The clock skips the 10 minutes a started server
			// stays up, then the sampler sees every minute.
			e.skew.Add(int64(10 * time.Minute))
			fell := func() bool { return e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'sleep'`) > 0 }
			if c.sleeps {
				e.waitUpTo(20*time.Second, "the server to fall asleep", func() bool {
					if fell() {
						return true
					}
					e.skew.Add(int64(time.Minute))
					time.Sleep(150 * time.Millisecond)
					return false
				})
				e.waitFor("the server asleep", func() bool { return e.status().Phase == api.PhaseAsleep && !e.a.busy() })
				return
			}
			// Twice the 5 minutes of the setting.
			for range 10 {
				e.skew.Add(int64(time.Minute))
				time.Sleep(150 * time.Millisecond)
			}
			s := e.srv()
			s.auto.mu.Lock()
			hold := s.auto.decision.Hold
			s.auto.mu.Unlock()
			if running, _ := fc.state(); fell() || !running || hold != sleep.HoldBusy {
				t.Fatalf("with the map pre-generating, the server fell asleep %v, Chunky runs %v, sleep holds for %q", fell(), running, hold)
			}
			if _, reported := s.pg.lastState(); reported != !strings.Contains(c.name, "before Chunky reports") {
				t.Fatalf("Chunky reported on the task %v", reported)
			}
		})
	}
}

// A scheduled restart's countdown keeps an empty server awake, as its
// operation does, so the restart its players were warned of happens. With
// the countdown called off, or no schedule running, the server falls asleep.
func TestSleepWaitsForAScheduledRestartsCountdown(t *testing.T) {
	cases := []struct {
		name    string
		restart bool
		callOff bool
		sleeps  bool
	}{
		{name: "a restart counting down", restart: true},
		{name: "a restart called off during its countdown", restart: true, callOff: true, sleeps: true},
		{name: "no schedule running", sleeps: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			localStandIn(t)
			e := newAgentEnv(t)
			e.create()
			if code, out := e.call("POST", e.sp("/sleep"), map[string]any{"actor": "admin", "enabled": true, "idleMinutes": sleep.MinIdleMinutes}); code != http.StatusOK {
				t.Fatalf("turn sleep on: %d %v", code, out)
			}
			counting := func() bool {
				_, out := e.call("GET", e.sp("/schedules"), nil)
				cur, _ := out["current"].(map[string]any)
				return cur != nil && cur["restartAt"] != nil
			}
			sid := ""
			if c.restart {
				// The countdown lasts 10 seconds on the wall clock, however
				// far the agent's clock skips.
				due := e.clockBefore(12 * time.Second)
				code, out := e.call("POST", e.sp("/schedules"), map[string]any{"actor": "admin", "kind": "restart", "timing": onceAt(due),
					"payload": map[string]any{"warnSeconds": []int{10}, "message": "Survival restarts in {minutes} minutes."}})
				if code != http.StatusCreated {
					t.Fatalf("create: %d %v", code, out)
				}
				sid = out["id"].(string)
				e.waitUpTo(20*time.Second, "the countdown", counting)
			}
			if c.callOff {
				if code, out := e.call("POST", e.sp("/schedules/"+sid), map[string]any{"actor": "admin", "enabled": false}); code != http.StatusOK {
					t.Fatalf("switch off: %d %v", code, out)
				}
				e.waitFor("the countdown called off", func() bool { return !counting() })
			}
			// Nobody plays. The clock skips the 10 minutes a started server
			// stays up, then the sampler sees every minute.
			e.skew.Add(int64(10 * time.Minute))
			fell := func() bool { return e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'sleep'`) > 0 }
			if c.sleeps {
				e.waitUpTo(20*time.Second, "the server to fall asleep", func() bool {
					if fell() {
						return true
					}
					e.skew.Add(int64(time.Minute))
					time.Sleep(150 * time.Millisecond)
					return false
				})
				e.waitFor("the server asleep", func() bool { return e.status().Phase == api.PhaseAsleep && !e.a.busy() })
				return
			}
			// Twice the shortest idle time, within the countdown.
			for range 2 * sleep.MinIdleMinutes {
				e.skew.Add(int64(time.Minute))
				time.Sleep(150 * time.Millisecond)
			}
			s := e.srv()
			s.auto.mu.Lock()
			hold := s.auto.decision.Hold
			s.auto.mu.Unlock()
			if fell() || !counting() || hold != sleep.HoldBusy {
				t.Fatalf("during the countdown, the server fell asleep %v, the countdown runs %v, sleep holds for %q", fell(), counting(), hold)
			}
			e.waitUpTo(30*time.Second, "the scheduled restart", func() bool {
				return e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'restart' AND actor = ? AND status = 'succeeded'`, schedule.Actor(sid)) == 1
			})
			e.waitUpTo(10*time.Second, "the run recorded", func() bool {
				return e.countRows(`SELECT COUNT(*) FROM schedule_runs WHERE schedule_id = ? AND result = 'succeeded'`, sid) == 1
			})
			if fell() {
				t.Fatal("the server fell asleep before its scheduled restart")
			}
		})
	}
}

// setSleepLooks makes fn run as the sleep operation looks again, until the
// test's later cleanups have run, the agent's included when set before it
// starts.
func setSleepLooks(t *testing.T, fn func()) {
	sleepLooks = fn
	t.Cleanup(func() { sleepLooks = func() {} })
}

// The sleep operation looks again before it stops the server, and is called
// off, the server staying awake, when since the sleep watch decided sleep
// was turned off or set to wait longer, or someone joined. Saving the
// setting takes the operation lock, so while the sleep runs, turning sleep
// off changes nothing, as during a backup: a server is never left asleep
// with sleep off.
func TestSleepLooksAgainBeforeItStopsTheServer(t *testing.T) {
	type state struct {
		desired   string
		listening bool
		sleep     sleep.Settings
		phase     api.Phase
		op        string
	}
	on, off, longer := sleep.Settings{Enabled: true, IdleMinutes: 5}, sleep.Settings{IdleMinutes: 5}, sleep.Settings{Enabled: true, IdleMinutes: 60}
	asleep := state{api.DesiredSleeping, true, on, api.PhaseAsleep, api.OpSucceeded}
	awake := func(set sleep.Settings) state {
		return state{api.DesiredRunning, false, set, api.PhaseOnline, api.OpCancelled}
	}
	read := func(e *agentEnv) state {
		s := e.srv()
		s.auto.mu.Lock()
		m := s.auto.standIn
		s.auto.mu.Unlock()
		var op string
		_ = e.a.db.QueryRow(`SELECT status FROM operations WHERE kind = 'sleep'`).Scan(&op)
		return state{s.desired(), m != nil && m.Listening(), s.sleepSettings(), e.status().Phase, op}
	}
	setSleep := func(e *agentEnv, set sleep.Settings) (int, map[string]any) {
		return e.call("POST", e.sp("/sleep"), map[string]any{"actor": "admin", "enabled": set.Enabled, "idleMinutes": set.IdleMinutes})
	}
	// saved saves the setting as the Sleep page does, which wakes nothing
	// while the server is awake.
	saved := func(set sleep.Settings) func(e *agentEnv) {
		return func(e *agentEnv) {
			if code, out := setSleep(e, set); code != http.StatusOK || out["operation"] != nil {
				e.t.Errorf("save %+v: %d %v", set, code, out)
			}
		}
	}
	cases := []struct {
		name string
		// decided runs after the sleep watch decided and before the
		// operation begins; looking, as the operation looks again.
		decided, looking func(e *agentEnv)
		want             state
	}{
		{name: "nothing changed", want: asleep},
		{name: "sleep turned off after it decided", decided: saved(off), want: awake(off)},
		{name: "a longer idle time after it decided", decided: saved(longer), want: awake(longer)},
		{name: "someone joined as it looks again", looking: func(e *agentEnv) { e.rcon.setOnline("Alex") }, want: awake(on)},
		{name: "sleep turned off while it runs", looking: func(e *agentEnv) {
			if code, out := setSleep(e, off); code != http.StatusConflict || out["code"] != api.CodeBusy {
				e.t.Errorf("sleep off while the server falls asleep: %d %v", code, out)
			}
		}, want: asleep},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var e *agentEnv
			var once sync.Once
			setSleepLooks(t, func() {
				if c.looking != nil {
					once.Do(func() { c.looking(e) })
				}
			})
			localStandIn(t)
			e = newAgentEnv(t)
			e.create()
			if code, out := setSleep(e, on); code != http.StatusOK {
				t.Fatalf("turn sleep on: %d %v", code, out)
			}
			s := e.srv()
			set := s.sleepSettings()
			if c.decided != nil {
				c.decided(e)
			}
			s.fallAsleep(set)
			e.waitFor("the operation over", func() bool { return !e.a.busy() })
			got := read(e)
			for deadline := time.Now().Add(5 * time.Second); got != c.want && time.Now().Before(deadline); got = read(e) {
				time.Sleep(50 * time.Millisecond)
			}
			if got != c.want {
				t.Fatalf("the server is left %+v, want %+v", got, c.want)
			}
			fell := e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'server_fell_asleep'`)
			open := e.countRows(`SELECT COUNT(*) FROM sleep_periods WHERE end_ts IS NULL`)
			if want := c.want.desired == api.DesiredSleeping; (fell == 1) != want || (open == 1) != want {
				t.Fatalf("%d fell-asleep events and %d sleep periods open, with the server meant to be %s", fell, open, c.want.desired)
			}
		})
	}
}

// Who may wake a sleeping server by joining: with the allowlist on, the
// players on it and operators; with it off, anyone who isn't banned, as
// anyone else may join. If the ban list or server.properties can't be read,
// the allowlist rule holds.
func TestWhoMayWakeASleepingServer(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(e.dataDir(), name)
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
		if body == "" {
			return
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("whitelist.json", `[{"name":"Alex","uuid":"00000000-0000-0000-0000-00000000a1e7"}]`)
	write("ops.json", `[{"name":"Oscar","uuid":"00000000-0000-0000-0000-0000000000c5","level":4}]`)
	const bans = `[{"name":"Griefer","uuid":"00000000-0000-0000-0000-00000000bad1"}]`
	// Playkeeper's Ban leaves a player on the allowlist.
	const listedBans = `[{"name":"Alex","uuid":"00000000-0000-0000-0000-00000000a1e7"},{"name":"Oscar","uuid":"00000000-0000-0000-0000-0000000000c5"}]`
	cases := []struct {
		name       string
		properties string // "" leaves server.properties out
		bans       string // "" leaves banned-players.json out
		wakes      map[string]bool
	}{
		{name: "allowlist off", properties: "motd=Survival\nwhite-list=false\n", bans: bans,
			wakes: map[string]bool{"Steve": true, "Alex": true, "Oscar": true, "Griefer": false, "griefer": false}},
		{name: "allowlist off, no ban list", properties: "white-list=false\n",
			wakes: map[string]bool{"Steve": true, "Griefer": true}},
		{name: "no white-list line", properties: "motd=Survival\n", bans: bans,
			wakes: map[string]bool{"Steve": true, "Griefer": false}},
		{name: "allowlist off, ban list unreadable", properties: "white-list=false\n", bans: `{"not a list`,
			wakes: map[string]bool{"Steve": false, "Griefer": false, "Alex": true, "Oscar": true}},
		{name: "allowlist on", properties: "motd=Survival\nwhite-list=true\n", bans: bans,
			wakes: map[string]bool{"Steve": false, "Alex": true, "alex": true, "Oscar": true}},
		{name: "allowlist on, in capitals", properties: "white-list=TRUE\n",
			wakes: map[string]bool{"Steve": false, "Alex": true}},
		{name: "no server.properties", bans: bans,
			wakes: map[string]bool{"Steve": false, "Alex": true, "Oscar": true}},
		{name: "allowlist on, banned though listed or an operator", properties: "white-list=true\n", bans: listedBans,
			wakes: map[string]bool{"Alex": false, "alex": false, "Oscar": false, "Steve": false}},
		{name: "no server.properties, banned though listed", bans: listedBans,
			wakes: map[string]bool{"Alex": false, "Oscar": false}},
		{name: "allowlist on, ban list unreadable", properties: "white-list=true\n", bans: `{"not a list`,
			wakes: map[string]bool{"Alex": true, "Oscar": true, "Steve": false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			write("server.properties", c.properties)
			write("banned-players.json", c.bans)
			for name, want := range c.wakes {
				if got := e.srv().mayWake(name); got != want {
					t.Errorf("%s wakes it: %v, want %v", name, got, want)
				}
			}
		})
	}
}

// fakeDest is a destination for copies somewhere else that keeps them in
// memory.
type fakeDest struct {
	mu      sync.Mutex
	names   []string
	resumes []*offsite.UploadState
	deleted []string
	aborted []*offsite.UploadState
	fail    error
	stored  map[string]offsite.Copy
}

func (d *fakeDest) Upload(ctx context.Context, up offsite.Upload) (offsite.Copy, error) {
	// As at a real destination, an upload stopped before it starts sends
	// nothing.
	if err := ctx.Err(); err != nil {
		return offsite.Copy{}, err
	}
	d.mu.Lock()
	d.names = append(d.names, up.Name)
	d.resumes = append(d.resumes, up.Resume)
	err := d.fail
	d.fail = nil
	d.mu.Unlock()
	if err != nil {
		return offsite.Copy{}, err
	}
	if up.Progress != nil {
		up.Progress(offsite.Progress{Sent: up.Size, Total: up.Size})
	}
	cp := offsite.Copy{Name: offsite.CopyName(up.Name), Archive: up.Name, ArchiveSHA256: up.SHA256, Key: "playkeeper/survival/" + offsite.CopyName(up.Name),
		Size: up.Size + 200, SHA256: strings.Repeat("ab", 32), Checked: offsite.CheckedSize, VerifiedAt: time.Now(), Uploaded: true}
	d.mu.Lock()
	d.stored[cp.Name] = cp
	d.mu.Unlock()
	return cp, nil
}

func (d *fakeDest) Delete(_ context.Context, name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deleted = append(d.deleted, name)
	delete(d.stored, name)
	return nil
}

func (d *fakeDest) Abort(_ context.Context, st *offsite.UploadState) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.aborted = append(d.aborted, st)
	return nil
}

func (d *fakeDest) abortedStates() []*offsite.UploadState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*offsite.UploadState(nil), d.aborted...)
}

func (d *fakeDest) AbortStale(context.Context, time.Time, []*offsite.UploadState) (int, error) {
	return 0, nil
}

func (d *fakeDest) Test(context.Context) offsite.TestResult { return offsite.TestResult{OK: true} }

func (d *fakeDest) List(context.Context) ([]offsite.Object, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []offsite.Object
	for _, cp := range d.stored {
		out = append(out, offsite.Object{Name: cp.Name, Archive: cp.Archive, Key: cp.Key, Size: cp.Size})
	}
	return out, nil
}

func (d *fakeDest) Download(context.Context, offsite.Download) (offsite.Archive, error) {
	return offsite.Archive{}, errors.New("not in this test")
}

func (d *fakeDest) uploads() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.names)
}

// backup makes a backup and returns its id. An operation lets go of the
// server's lock just after it reports that it finished, and a finished copy
// holds the lock for a moment to apply the backup rules, so a busy answer is
// asked again.
func (e *agentEnv) backup() string {
	e.t.Helper()
	var code int
	var out map[string]any
	e.waitFor("the server to take a backup", func() bool {
		code, out = e.call("POST", e.sp("/backups"), map[string]any{"actor": "admin"})
		return out["code"] != "busy"
	})
	if code != http.StatusAccepted {
		e.t.Fatalf("backup: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded {
		e.t.Fatalf("backup failed: %+v", op)
	}
	return op.Detail["backupId"].(string)
}

func TestCopiesSomewhereElseUploadRetryAndFollowTheRules(t *testing.T) {
	dest := &fakeDest{stored: map[string]offsite.Copy{}}
	prev := openOffsite
	openOffsite = func(offsite.Config, offsite.Keys, offsite.Options) (offsiteDest, error) { return dest, nil }
	t.Cleanup(func() { openOffsite = prev })
	e := newAgentEnv(t)
	e.create()
	first := e.backup()

	const secret = "wJalrXUtnFEMI-example-secret"
	s3 := map[string]any{"provider": "minio", "endpoint": "203.0.113.10:9000", "bucket": "worlds", "accessKeyId": "PKEXAMPLE"}
	if code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "admin", "enabled": true, "config": map[string]any{"type": "s3", "s3": s3}}); code != http.StatusBadRequest {
		t.Fatalf("no secret key: %d %v", code, out)
	}
	code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "admin", "enabled": true, "config": map[string]any{"type": "s3", "s3": s3}, "secretKey": secret})
	if code != 200 {
		t.Fatalf("turn on: %d %v", code, out)
	}
	view := out["s3"].(map[string]any)
	if view["endpoint"] != "https://203.0.113.10:9000" || view["pathStyle"] != true || view["region"] != "us-east-1" || view["secretKeySet"] != true || !strings.HasPrefix(view["prefix"].(string), "playkeeper/") {
		t.Fatalf("saved settings: %v", view)
	}
	if key, _ := out["key"].(map[string]any); key == nil || !strings.HasPrefix(key["recipient"].(string), "age1") || key["savedAt"] != nil {
		t.Fatalf("key: %v", out["key"])
	}
	e.waitFor("the first copy", func() bool { return e.countRows(`SELECT COUNT(*) FROM offsite_copies WHERE backup_id = ?`, first) == 1 })
	code, out = e.call("GET", e.sp("/offsite"), nil)
	if last, _ := out["lastCopy"].(map[string]any); code != 200 || last == nil || last["backupId"] != first || out["copies"] != float64(1) || out["queued"] != float64(0) {
		t.Fatalf("after the first copy: %d %v", code, out)
	}

	// A copy that fails waits and carries on from where it stopped.
	resume := &offsite.UploadState{Archive: "x.tar.gz", Name: "x.tar.gz.age", Size: 1000, S3: &offsite.S3Upload{Key: "k", UploadID: "u1", PartSize: 5 << 20, Parts: []offsite.Part{{Number: 1, Size: 400}}}}
	dest.mu.Lock()
	dest.fail = &offsite.Error{Kind: offsite.KindNetwork, Msg: "Couldn't reach the storage.", Hint: "Check the address.", Retry: true, Resume: resume}
	dest.mu.Unlock()
	code, out = e.call("POST", e.sp("/backup-rules"), map[string]any{"actor": "admin", "rules": map[string]any{"onHost": map[string]any{"keepAll": true}, "offSite": map[string]any{"last": 1}, "includeManual": true}})
	if code != 200 {
		t.Fatalf("rules: %d %v", code, out)
	}
	second := e.backup()
	e.waitFor("the failed copy", func() bool {
		return e.countRows(`SELECT COUNT(*) FROM offsite_uploads WHERE backup_id = ? AND attempts = 1`, second) == 1
	})
	_, out = e.call("GET", e.sp("/offsite"), nil)
	pending, _ := out["pending"].(map[string]any)
	if pending == nil || pending["backupId"] != second || pending["backupCreatedAt"] == nil || pending["error"] != "Couldn't reach the storage." || pending["errorKind"] != "network" || pending["sent"] != float64(400) || pending["nextAttempt"] == nil {
		t.Fatalf("pending: %v", out["pending"])
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'offsite.copy_failed' AND target = ?`, second); n != 1 {
		t.Fatalf("audited the failure %d times", n)
	}
	if code, _ := e.call("POST", e.sp("/offsite/retry"), map[string]any{"actor": "admin"}); code != 200 {
		t.Fatalf("retry: %d", code)
	}
	e.waitFor("the second copy", func() bool {
		return e.countRows(`SELECT COUNT(*) FROM offsite_copies WHERE backup_id = ?`, second) == 1
	})
	e.waitFor("the rules to delete the first copy", func() bool { return e.countRows(`SELECT COUNT(*) FROM offsite_copies`) == 1 })
	dest.mu.Lock()
	lastResume := dest.resumes[len(dest.resumes)-1]
	deleted := append([]string(nil), dest.deleted...)
	dest.mu.Unlock()
	if lastResume == nil || lastResume.S3 == nil || lastResume.S3.UploadID != "u1" {
		t.Fatalf("the retry started over: %+v", lastResume)
	}
	firstFile := ""
	_ = e.a.db.QueryRow(`SELECT file_name FROM backups WHERE id = ?`, first).Scan(&firstFile)
	if len(deleted) != 1 || deleted[0] != offsite.CopyName(firstFile) {
		t.Fatalf("deleted at the destination %q, want the first copy of %s", deleted, firstFile)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM offsite_copies`); n != 1 {
		t.Fatalf("%d copies recorded, the rules keep 1", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'offsite.copy_deleted' AND actor = 'backup rules' AND target = ?`, first); n != 1 {
		t.Fatalf("audited the deletion %d times", n)
	}

	// Nobody named, no key: every download is attributable.
	anon, err := http.Get(e.ts.URL + e.sp("/offsite/recovery-key"))
	if err != nil {
		t.Fatal(err)
	}
	anonBody, _ := io.ReadAll(anon.Body)
	anon.Body.Close()
	if anon.StatusCode != http.StatusBadRequest || strings.Contains(string(anonBody), "AGE-SECRET-KEY") {
		t.Fatalf("recovery key without an actor: %d %q", anon.StatusCode, anonBody)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM offsite WHERE key_saved_at IS NOT NULL`); n != 0 {
		t.Fatal("a refused download marked the key as saved")
	}

	// The recovery key file: never cached, audited without its content.
	req, _ := http.NewRequest("GET", e.ts.URL+e.sp("/offsite/recovery-key"), nil)
	req.Header.Set("X-Playkeeper-Actor", "owner")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment;") ||
		!strings.Contains(string(body), "AGE-SECRET-KEY-") {
		t.Fatalf("recovery key: %d %v %q", resp.StatusCode, resp.Header, body)
	}
	var detail string
	if err := e.a.db.QueryRow(`SELECT detail FROM audit WHERE action = 'offsite.recovery_key.downloaded' AND actor = 'owner'`).Scan(&detail); err != nil || strings.Contains(detail, "AGE-SECRET-KEY") {
		t.Fatalf("audit of the download: %q %v", detail, err)
	}
	_, out = e.call("GET", e.sp("/offsite"), nil)
	if key := out["key"].(map[string]any); key["savedAt"] == nil {
		t.Fatalf("the download wasn't noted: %v", key)
	}
	code, out = e.call("POST", e.sp("/offsite/new-key"), map[string]any{"actor": "admin"})
	if rot, _ := out["rotation"].(map[string]any); code != 200 || rot == nil || rot["recipient"] == "" || rot["recipient"] == rot["oldRecipient"] {
		t.Fatalf("new key: %d %v", code, out)
	}
	if key := out["offsite"].(map[string]any)["key"].(map[string]any); key["savedAt"] != nil || key["oldKeys"] != float64(1) {
		t.Fatalf("after a new key: %v", key)
	}

	// Secrets stay in agent.db's offsite row and nowhere else it shows.
	_, out = e.call("GET", e.sp("/offsite"), nil)
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "AGE-SECRET-KEY") {
		t.Fatalf("the settings show a secret: %s", raw)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE detail LIKE ? OR detail LIKE '%AGE-SECRET-KEY%'`, "%"+secret+"%"); n != 0 {
		t.Fatalf("%d audit lines hold a secret", n)
	}

	// Turning copies off empties the queue.
	dest.mu.Lock()
	dest.fail = &offsite.Error{Kind: offsite.KindServiceError, Msg: "The storage failed.", Retry: true}
	dest.mu.Unlock()
	e.backup()
	e.waitFor("the third copy to fail", func() bool { return e.countRows(`SELECT COUNT(*) FROM offsite_uploads WHERE attempts = 1`) == 1 })
	if code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "admin", "enabled": false}); code != 200 || out["enabled"] != false {
		t.Fatalf("turn off: %d %v", code, out)
	}
	e.waitFor("the queue to empty", func() bool { return e.countRows(`SELECT COUNT(*) FROM offsite_uploads`) == 0 })
	if dest.uploads() != 4 {
		t.Fatalf("%d uploads, want 4", dest.uploads())
	}
}

// stoppingDest is a destination whose first upload stores a part, then
// waits for the agent to stop and fails with nothing to resume from, as an
// upload does when it is cancelled before the storage says more.
type stoppingDest struct {
	fakeDest
	part  *offsite.UploadState
	saved chan struct{}
}

func (d *stoppingDest) Upload(ctx context.Context, up offsite.Upload) (offsite.Copy, error) {
	d.mu.Lock()
	first := len(d.names) == 0
	if first {
		d.names, d.resumes = append(d.names, up.Name), append(d.resumes, up.Resume)
	}
	d.mu.Unlock()
	if !first {
		return d.fakeDest.Upload(ctx, up)
	}
	up.Progress(offsite.Progress{Sent: 400, Total: up.Size, State: d.part})
	close(d.saved)
	<-ctx.Done()
	return offsite.Copy{}, ctx.Err()
}

// A copy the agent stopped in carries on, when it starts again, from the
// part the storage already holds.
func TestACopyTheAgentStoppedInResumesFromItsSavedPart(t *testing.T) {
	part := &offsite.UploadState{Archive: "x.tar.gz", Name: "x.tar.gz.age", Size: 1000, S3: &offsite.S3Upload{Key: "k", UploadID: "u1", PartSize: 5 << 20, Parts: []offsite.Part{{Number: 1, Size: 400}}}}
	dest := &stoppingDest{fakeDest: fakeDest{stored: map[string]offsite.Copy{}}, part: part, saved: make(chan struct{})}
	prev := openOffsite
	openOffsite = func(offsite.Config, offsite.Keys, offsite.Options) (offsiteDest, error) { return dest, nil }
	t.Cleanup(func() { openOffsite = prev })
	e := newAgentEnv(t)
	e.create()
	id := e.backup()
	s3 := map[string]any{"provider": "minio", "endpoint": "203.0.113.10:9000", "bucket": "worlds", "accessKeyId": "PKEXAMPLE"}
	if code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "admin", "enabled": true, "config": map[string]any{"type": "s3", "s3": s3}, "secretKey": "wJalrXUtnFEMI-example-secret"}); code != 200 {
		t.Fatalf("turn on: %d %v", code, out)
	}
	waitClosed(t, dest.saved, "the first part to be stored")
	e.stop()
	e.start()
	e.waitFor("the copy", func() bool { return e.countRows(`SELECT COUNT(*) FROM offsite_copies WHERE backup_id = ?`, id) == 1 })
	dest.mu.Lock()
	resumes := append([]*offsite.UploadState(nil), dest.resumes...)
	dest.mu.Unlock()
	if len(resumes) != 2 || resumes[1] == nil || resumes[1].S3 == nil || resumes[1].S3.UploadID != "u1" || len(resumes[1].S3.Parts) != 1 {
		t.Fatalf("after the restart the copy didn't carry on from the stored part: %+v", resumes)
	}
}

// unfinishedCopies are the settings that turn copies on to S3 and to SFTP,
// each with what an earlier try left at the destination: an S3 multipart
// upload or an SFTP partial file.
var unfinishedCopies = []struct {
	name  string
	setup map[string]any
	state *offsite.UploadState
}{
	{"S3", map[string]any{"config": map[string]any{"type": "s3", "s3": map[string]any{"provider": "minio", "endpoint": "203.0.113.10:9000", "bucket": "worlds", "accessKeyId": "PKEXAMPLE"}},
		"secretKey": "wJalrXUtnFEMI-example-secret"},
		&offsite.UploadState{Archive: "x.tar.gz", Name: "x.tar.gz.age", Size: 1000, S3: &offsite.S3Upload{Key: "k", UploadID: "u1", PartSize: 5 << 20, Parts: []offsite.Part{{Number: 1, Size: 400}}}}},
	{"SFTP", map[string]any{"config": map[string]any{"type": "sftp", "sftp": map[string]any{"host": "203.0.113.20", "port": 22, "user": "playkeeper", "folder": "backups/survival"}},
		"sftpAuth": "password", "password": "an example password"},
		&offsite.UploadState{Archive: "x.tar.gz", Name: "x.tar.gz.age", Size: 1000, SFTP: &offsite.SFTPUpload{Partial: "backups/survival/x.tar.gz.age.partial", Written: 400}}},
}

// A copy whose archive is no longer on this machine leaves the queue, and
// what an earlier try left at the destination, an S3 multipart upload or an
// SFTP partial file, is discarded with it.
func TestACopyWhoseArchiveIsGoneDiscardsWhatItLeftAtTheDestination(t *testing.T) {
	for _, c := range unfinishedCopies {
		t.Run(c.name, func(t *testing.T) {
			dest := &fakeDest{stored: map[string]offsite.Copy{}}
			prev := openOffsite
			openOffsite = func(offsite.Config, offsite.Keys, offsite.Options) (offsiteDest, error) { return dest, nil }
			t.Cleanup(func() { openOffsite = prev })
			e := newAgentEnv(t)
			e.create()
			id := e.backup()
			b, err := e.srv().getBackup(id)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(c.state)
			if _, err := e.a.db.Exec(`INSERT INTO offsite_uploads(server_id, backup_id, state, created_at) VALUES(?, ?, ?, ?)`, e.sid, id, string(raw), time.Now().UnixMilli()); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(e.a.backupPath(b.FileName)); err != nil {
				t.Fatal(err)
			}
			body := map[string]any{"actor": "admin", "enabled": true}
			for k, v := range c.setup {
				body[k] = v
			}
			if code, out := e.call("POST", e.sp("/offsite"), body); code != 200 {
				t.Fatalf("turn on: %d %v", code, out)
			}
			e.waitFor("the unfinished copy to be discarded", func() bool { return len(dest.abortedStates()) > 0 })
			aborted := dest.abortedStates()
			got, _ := json.Marshal(aborted[0])
			if len(aborted) != 1 || string(got) != string(raw) {
				t.Fatalf("discarded %d unfinished copies, the first %s, not %s", len(aborted), got, raw)
			}
			if n := e.countRows(`SELECT COUNT(*) FROM offsite_uploads WHERE backup_id = ?`, id); n != 0 || dest.uploads() != 0 {
				t.Fatalf("the copy of a missing archive: %d queued, %d uploads", n, dest.uploads())
			}
		})
	}
}

// Only the newest backups wait for their copy. The oldest leaves a full
// queue when a backup joins it, and what an earlier try of its copy left at
// the destination, an S3 multipart upload or an SFTP partial file, is
// discarded with it; what the backups still waiting left stays.
func TestABackupDroppedFromAFullQueueDiscardsWhatItLeftAtTheDestination(t *testing.T) {
	for _, c := range unfinishedCopies {
		t.Run(c.name, func(t *testing.T) {
			dest := &fakeDest{stored: map[string]offsite.Copy{}}
			prev := openOffsite
			openOffsite = func(offsite.Config, offsite.Keys, offsite.Options) (offsiteDest, error) { return dest, nil }
			t.Cleanup(func() { openOffsite = prev })
			e := newAgentEnv(t)
			e.create()
			body := map[string]any{"actor": "admin", "enabled": true}
			for k, v := range c.setup {
				body[k] = v
			}
			if code, out := e.call("POST", e.sp("/offsite"), body); code != 200 {
				t.Fatalf("turn on: %d %v", code, out)
			}
			// A full queue of copies waiting to be tried again: the two
			// oldest stopped part way.
			dropped, _ := json.Marshal(c.state)
			next := *c.state
			next.Archive, next.Name = "y.tar.gz", "y.tar.gz.age"
			kept, _ := json.Marshal(next)
			states := map[int]string{0: string(dropped), 1: string(kept)}
			now := e.srv().now()
			for i := range offsiteMaxQueue {
				if _, err := e.a.db.Exec(`INSERT INTO offsite_uploads(server_id, backup_id, state, next_attempt, created_at) VALUES(?, ?, ?, ?, ?)`,
					e.sid, fmt.Sprintf("waiting-%02d", i), states[i], now.Add(time.Hour).UnixMilli(), now.Add(time.Duration(i-offsiteMaxQueue)*time.Hour).UnixMilli()); err != nil {
					t.Fatal(err)
				}
			}
			id := e.backup()
			e.waitFor("the dropped copy to be discarded", func() bool { return len(dest.abortedStates()) > 0 })
			aborted := dest.abortedStates()
			got, _ := json.Marshal(aborted[0])
			if len(aborted) != 1 || string(got) != string(dropped) {
				t.Fatalf("discarded %d unfinished copies, the first %s, not %s", len(aborted), got, dropped)
			}
			if n := e.countRows(`SELECT COUNT(*) FROM offsite_uploads WHERE backup_id = 'waiting-00'`); n != 0 {
				t.Fatal("the oldest backup still waits for its copy")
			}
			if n := e.countRows(`SELECT COUNT(*) FROM offsite_uploads WHERE backup_id = 'waiting-01' AND state = ?`, string(kept)); n != 1 {
				t.Fatal("a backup still waiting lost where its copy stopped")
			}
			e.waitFor("the new backup's copy", func() bool { return e.countRows(`SELECT COUNT(*) FROM offsite_copies WHERE backup_id = ?`, id) == 1 })
		})
	}
}

// The copy the uploader picked stays queued until the uploader is done with
// it. A backup that joins the full queue meanwhile drops only the oldest of
// the others and discards what that one left at the destination; the picked
// copy then carries on from where its last try stopped. Turning copies off
// once the next copy is picked stops it before it sends anything.
func TestTheCopyBeingMadeStaysQueuedWhenABackupJoinsAFullQueue(t *testing.T) {
	for _, c := range unfinishedCopies {
		t.Run(c.name, func(t *testing.T) {
			// The uploader waits right after claiming the held backup's
			// copy, until the test lets it go or the claim is cancelled.
			var holding atomic.Pointer[string]
			claimed, release := make(chan string, 1), make(chan struct{}, 1)
			prevHook := uploadClaimed
			uploadClaimed = func(job uploadJob) {
				if id := holding.Load(); id != nil && *id == job.backupID && holding.CompareAndSwap(id, nil) {
					claimed <- job.backupID
					select {
					case <-release:
					case <-job.ctx.Done():
					}
				}
			}
			t.Cleanup(func() { uploadClaimed = prevHook })
			hold := func(id string) { holding.Store(&id) }
			waitClaim := func(id string) {
				t.Helper()
				select {
				case <-claimed:
				case <-time.After(15 * time.Second):
					t.Fatalf("the uploader never picked the copy of %s", id)
				}
			}
			dest := &fakeDest{stored: map[string]offsite.Copy{}}
			prev := openOffsite
			openOffsite = func(offsite.Config, offsite.Keys, offsite.Options) (offsiteDest, error) { return dest, nil }
			t.Cleanup(func() { openOffsite = prev })
			e := newAgentEnv(t)
			e.create()
			body := map[string]any{"actor": "admin", "enabled": true}
			for k, v := range c.setup {
				body[k] = v
			}
			if code, out := e.call("POST", e.sp("/offsite"), body); code != 200 {
				t.Fatalf("turn on: %d %v", code, out)
			}

			// The first try of a backup's copy stops part way.
			saved, _ := json.Marshal(c.state)
			dest.mu.Lock()
			dest.fail = &offsite.Error{Kind: offsite.KindNetwork, Msg: "The storage stopped answering.", Resume: c.state}
			dest.mu.Unlock()
			id := e.backup()
			e.waitFor("the first try to stop part way", func() bool {
				return e.countRows(`SELECT COUNT(*) FROM offsite_uploads WHERE backup_id = ? AND attempts = 1 AND state = ?`, id, string(saved)) == 1
			})

			// Twelve other backups wait for their copy, the oldest with a
			// part stored. The backup's copy, second oldest, is due again
			// and the uploader picks it.
			old := *c.state
			old.Archive, old.Name = "w.tar.gz", "w.tar.gz.age"
			dropped, _ := json.Marshal(old)
			now := e.srv().now()
			for i := range offsiteMaxQueue {
				state := ""
				if i == 0 {
					state = string(dropped)
				}
				if _, err := e.a.db.Exec(`INSERT INTO offsite_uploads(server_id, backup_id, state, next_attempt, created_at) VALUES(?, ?, ?, ?, ?)`,
					e.sid, fmt.Sprintf("waiting-%02d", i), state, now.Add(time.Hour).UnixMilli(), now.Add(time.Duration(i-14)*time.Hour).UnixMilli()); err != nil {
					t.Fatal(err)
				}
			}
			hold(id)
			if _, err := e.a.db.Exec(`UPDATE offsite_uploads SET next_attempt = 0, created_at = ? WHERE backup_id = ?`, now.Add(-13*time.Hour-30*time.Minute).UnixMilli(), id); err != nil {
				t.Fatal(err)
			}
			e.srv().kickOffsite()
			waitClaim(id)

			// A backup joins the queue: the oldest waiting backup leaves it,
			// and the picked copy stays with where its last try stopped.
			joined := e.backup()
			if n := e.countRows(`SELECT COUNT(*) FROM offsite_uploads WHERE backup_id = ? AND state = ?`, id, string(saved)); n != 1 {
				t.Fatal("the queue dropped the copy being made, or where its last try stopped")
			}
			if n := e.countRows(`SELECT COUNT(*) FROM offsite_uploads WHERE backup_id = 'waiting-00'`); n != 0 {
				t.Fatal("the oldest backup still waits for its copy")
			}
			e.waitFor("the dropped copy to be discarded", func() bool { return len(dest.abortedStates()) > 0 })

			// The picked copy carries on from its stored part and finishes.
			hold(joined)
			release <- struct{}{}
			waitClaim(joined)
			if n := e.countRows(`SELECT COUNT(*) FROM offsite_copies WHERE backup_id = ?`, id); n != 1 {
				t.Fatal("the copy being made didn't finish")
			}
			dest.mu.Lock()
			resumes := append([]*offsite.UploadState(nil), dest.resumes...)
			dest.mu.Unlock()
			if len(resumes) != 2 {
				t.Fatalf("%d tries of the copy, not 2", len(resumes))
			}
			if got, _ := json.Marshal(resumes[1]); string(got) != string(saved) {
				t.Fatalf("the copy carried on from %s, not %s", got, saved)
			}
			if got, _ := json.Marshal(dest.abortedStates()); string(got) != "["+string(dropped)+"]" {
				t.Fatalf("discarded %s, not only %s", got, dropped)
			}

			// Copies are turned off once the joined backup's copy is picked.
			if code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "admin", "enabled": false}); code != 200 {
				t.Fatalf("turn off: %d %v", code, out)
			}
			release <- struct{}{}
			e.waitFor("the queue to empty", func() bool { return e.countRows(`SELECT COUNT(*) FROM offsite_uploads`) == 0 })
			if n := dest.uploads(); n != 2 {
				t.Fatalf("the copy of %s went ahead after copies were turned off: %d tries in all", joined, n)
			}
		})
	}
}

func TestDiskSpaceShowsWhatToFreeAndDeletesOnlyWhatWasChosen(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	logs := filepath.Join(e.dataDir(), "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(logs, "2026-01-02-1.log.gz")
	recent := filepath.Join(logs, "2026-09-01-1.log.gz")
	for _, p := range []string{old, recent} {
		if err := os.WriteFile(p, bytes.Repeat([]byte("log line\n"), 4096), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	past := e.a.now().Add(-60 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if code, out := e.call("GET", "/v1/disk?tz=Mars/Olympus", nil); code != http.StatusBadRequest || out["field"] != "timeZone" {
		t.Fatalf("bad time zone: %d %v", code, out)
	}
	code, out := e.call("GET", "/v1/disk?tz=Europe/Berlin", nil)
	if code != 200 {
		t.Fatalf("scan: %d %v", code, out)
	}
	servers := out["servers"].([]any)
	if len(servers) != 1 || servers[0].(map[string]any)["id"] != e.sid {
		t.Fatalf("servers: %v", servers)
	}
	var id string
	for _, c := range out["candidates"].([]any) {
		c := c.(map[string]any)
		if c["reason"] == "old_log" && strings.HasSuffix(c["path"].(string), "2026-01-02-1.log.gz") {
			id = c["id"].(string)
		}
		if strings.HasSuffix(c["path"].(string), "2026-09-01-1.log.gz") {
			t.Fatalf("a recent log is offered: %v", c)
		}
	}
	if id == "" {
		t.Fatalf("the old log isn't offered: %v", out["candidates"])
	}
	for name, body := range map[string]map[string]any{
		"nothing chosen":  {"ids": []string{}},
		"a malformed id":  {"ids": []string{"../../etc"}},
		"an unknown way":  {"ways": []string{"everything"}},
		"an unknown zone": {"ids": []string{id}, "timeZone": "Mars/Olympus"},
	} {
		body["actor"] = "admin"
		if code, out := e.call("POST", "/v1/disk/clean", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	notOffered := strings.Repeat("0", 32)
	code, out = e.call("POST", "/v1/disk/clean", map[string]any{"actor": "admin", "ids": []string{id, notOffered}, "timeZone": "Europe/Berlin"})
	if code != http.StatusAccepted {
		t.Fatalf("clean: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	problems, _ := op.Detail["problems"].([]any)
	if op.Status != api.OpSucceeded || op.Detail["deleted"] != float64(1) || op.Detail["freed"].(float64) <= 0 || len(problems) != 1 {
		t.Fatalf("clean-up: %+v", op)
	}
	if p := problems[0].(map[string]any); p["id"] != notOffered || p["status"] != "not_offered" {
		t.Fatalf("problem: %v", p)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("the old log is still there: %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("the recent log is gone: %v", err)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'disk.cleaned' AND target = 'machine' AND actor = 'admin'`); n != 1 {
		t.Fatalf("audited the clean-up %d times", n)
	}

	// One button deletes all of a way, as the scan finds it then.
	older := filepath.Join(logs, "2026-01-03-1.log.gz")
	if err := os.WriteFile(older, []byte("log line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatal(err)
	}
	code, out = e.call("POST", "/v1/disk/clean", map[string]any{"actor": "admin", "ways": []string{"old_logs"}})
	if code != http.StatusAccepted {
		t.Fatalf("clean a way: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded || op.Detail["deleted"] != float64(1) {
		t.Fatalf("clean-up of a way: %+v", op)
	}
	if _, err := os.Stat(older); !os.IsNotExist(err) {
		t.Fatalf("the way's log is still there: %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("the recent log is gone: %v", err)
	}
}
