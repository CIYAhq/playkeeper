package agent

// Wave 7 (0.4.0): schedules, sleep when nobody's playing, backup rules with
// copies somewhere else, and disk space.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/offsite"
	"github.com/CIYAhq/playkeeper/internal/schedule"
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

func TestServerSleepsWhenEmptyAndWakesForAListedPlayer(t *testing.T) {
	prev := standInAddr
	standInAddr = func(int) string { return "127.0.0.1:0" }
	t.Cleanup(func() { standInAddr = prev })
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

// fakeDest is a destination for copies somewhere else that keeps them in
// memory.
type fakeDest struct {
	mu      sync.Mutex
	names   []string
	resumes []*offsite.UploadState
	deleted []string
	fail    error
	stored  map[string]offsite.Copy
}

func (d *fakeDest) Upload(_ context.Context, up offsite.Upload) (offsite.Copy, error) {
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

func (d *fakeDest) Abort(context.Context, *offsite.UploadState) error { return nil }

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

func (e *agentEnv) backup() string {
	e.t.Helper()
	code, out := e.call("POST", e.sp("/backups"), map[string]any{"actor": "admin"})
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
