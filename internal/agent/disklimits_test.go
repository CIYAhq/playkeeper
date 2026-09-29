package agent

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup"
	"github.com/CIYAhq/playkeeper/internal/schedule"
)

// diskLimits reads every disk limit, from a new scan.
func (e *agentEnv) diskLimits() []api.DiskLimit {
	e.t.Helper()
	var out []api.DiskLimit
	if code := e.callInto("GET", "/v1/disk-limits?fresh=1", nil, &out); code != 200 {
		e.t.Fatalf("disk limits: %d", code)
	}
	return out
}

// limitTo gives the env's server a disk limit of its own and returns what
// it takes, from the scan the limit's checks then start from.
func (e *agentEnv) limitTo(bytes int64) int64 {
	e.t.Helper()
	var set []api.DiskLimit
	body := map[string]any{"limits": []any{map[string]any{"id": "customer-6", "limitBytes": bytes, "servers": []string{e.sid}}}, "actor": "admin"}
	if code := e.callInto("PUT", "/v1/disk-limits", body, &set); code != 200 || len(set) != 1 {
		e.t.Fatalf("setting the disk limit: %d %+v", code, set)
	}
	return e.diskLimits()[0].UsedBytes
}

// The dashboard sets each customer's disk limit, which lasts, and reads what
// their servers take, as the Disk space page counts it.
func TestDiskLimitsLastAndSayWhatTheirServersTake(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	limit := map[string]any{"id": "customer-6", "limitBytes": 30 << 30, "servers": []string{e.sid}}
	for range 2 {
		var set []api.DiskLimit
		if code := e.callInto("PUT", "/v1/disk-limits", map[string]any{"limits": []any{limit}, "actor": "admin"}, &set); code != 200 || len(set) != 1 || set[0].ID != "customer-6" {
			t.Fatalf("setting a limit: %d %+v", code, set)
		}
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'disk_limits.set'`); n != 1 {
		t.Fatalf("%d audit entries for one change", n)
	}
	got := e.diskLimits()
	if len(got) != 1 || got[0].LimitBytes != 30<<30 || !slices.Equal(got[0].Servers, []string{e.sid}) || got[0].UsedBytes <= 0 {
		t.Fatalf("the limits: %+v", got)
	}
	e.stop()
	e.start()
	if got := e.diskLimits(); len(got) != 1 || got[0].ID != "customer-6" {
		t.Fatalf("after the agent restarted: %+v", got)
	}
	for name, limits := range map[string][]any{
		"a server twice": {limit, map[string]any{"id": "customer-7", "limitBytes": 1 << 30, "servers": []string{e.sid}}},
		"an id twice":    {limit, limit},
		"no size":        {map[string]any{"id": "customer-7", "limitBytes": 0, "servers": []string{}}},
		"a bad id":       {map[string]any{"id": "Customer 7", "limitBytes": 1 << 30, "servers": []string{}}},
		"a bad server":   {map[string]any{"id": "customer-7", "limitBytes": 1 << 30, "servers": []string{"../x"}}},
	} {
		if code, _ := e.call("PUT", "/v1/disk-limits", map[string]any{"limits": limits, "actor": "admin"}); code != 400 {
			t.Errorf("limits with %s: %d", name, code)
		}
	}
}

// A backup made on request or on schedule stops at the limit, and one that
// fits counts as soon as it's written, before a scan finds it.
func TestBackupsStopAtTheDiskLimit(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	size, err := backup.Measure(e.srv().dataDir(), archiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	need := size.ArchiveBytes()
	used := e.limitTo(1 << 40)
	e.limitTo(used + need/2)
	if op := e.backupNow(nil); op.Status != api.OpFailed || !strings.Contains(op.Error, "disk limit") || op.Hint == "" {
		t.Fatalf("a backup past the limit: %+v", op)
	}
	const sid = "qrstuvwxyz"
	for {
		_, err := (scheduleServer{e.srv()}).Run(context.Background(), schedule.Operation{Kind: schedule.OpBackup, Actor: schedule.Actor(sid), ScheduleID: sid})
		if errors.Is(err, schedule.ErrBusy) {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		break
	}
	e.waitFor("the refused scheduled backup", func() bool {
		r := e.status().BackupRefused
		return r != nil && r.Kind == api.CodeDiskLimit && strings.Contains(r.Error, "disk limit") && r.Hint != ""
	})

	// Room for two backups less a little: the first fits, and it counts
	// before a scan finds it, so the second doesn't.
	used = e.limitTo(used + 2*need - need/4)
	if op := e.backupNow(nil); op.Status != api.OpSucceeded {
		t.Fatalf("a backup that fits: %+v", op)
	}
	if op := e.backupNow(nil); op.Status != api.OpFailed || !strings.Contains(op.Error, "disk limit") {
		t.Fatalf("a second backup, before a scan found the first: %+v", op)
	}
}

// Files go in place only while they fit the limit. A file on its way counts,
// and so does one just put in place, before a scan finds it.
func TestUploadsStopAtTheDiskLimit(t *testing.T) {
	e, _ := idleFilesServer(t)
	used := e.limitTo(1 << 40)
	e.limitTo(used + 100<<10)
	up := e.openUpload("plugins")
	if code, out := e.announceFile(up, "big.jar", 200<<10, false); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("a file past the limit: %d %v", code, out)
	}
	if code, out := e.announceFile(up, "small.jar", 60<<10, false); code != 201 {
		t.Fatalf("a file that fits: %d %v", code, out)
	}
	if code, out := e.announceFile(up, "second.jar", 60<<10, false); code != 507 {
		t.Fatalf("a second file, beside one on its way: %d %v", code, out)
	}
	if code, v, out := e.uploadPiece(up, 0, 0, bytes.NewReader(bytes.Repeat([]byte("x"), 60<<10))); code != 200 || !v.Files[0].Placed {
		t.Fatalf("sending the file that fits: %d %+v %v", code, v, out)
	}
	if code, out := e.announceFile(up, "second.jar", 60<<10, false); code != 507 {
		t.Fatalf("a second file, before a scan found the first in place: %d %v", code, out)
	}
	e.limitTo(1 << 40)
	if code, out := e.announceFile(up, "second.jar", 60<<10, false); code != 201 {
		t.Fatalf("a second file with room: %d %v", code, out)
	}
}

// Worlds imported or restored from an upload, data and resource packs and
// pre-generation stop at the limit too; a server with no limit doesn't.
func TestImportsPacksAndPregenStopAtTheDiskLimit(t *testing.T) {
	e := newAgentEnv(t)
	e.withSources()
	e.create()
	used := e.limitTo(1 << 40)
	e.limitTo(used + 100)
	imp := e.openImport(e.sp("/world-imports"))
	if code, out := e.announce(imp, "world.zip", 64<<10); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("a world to import past the limit: %d %v", code, out)
	}
	if code, out := e.uploadTo(e.sp("/restore/upload"), bytes.Repeat([]byte("x"), 64<<10)); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("a world to restore past the limit: %d %v", code, out)
	}
	if code, out := e.whenFree(func() (int, map[string]any) {
		return e.uploadTo(e.sp("/datapacks?name=graves.zip"), dataPackZip(t, "Keeps your items", true))
	}); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("a data pack past the limit: %d %v", code, out)
	}
	if code, out := e.whenFree(func() (int, map[string]any) {
		return e.uploadTo(e.sp("/resourcepack?host=203.0.113.10&port=8443&name=Faithful.zip"), resourcePackZip(t, "Faithful 32x", true))
	}); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("a resource pack past the limit: %d %v", code, out)
	}
	if code, out := e.callWhenFree("POST", e.sp("/pregen/start"), map[string]any{"preset": "small", "pauseForPlayers": false, "actor": "admin"}); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("pre-generating past the limit: %d %v", code, out)
	}
	var none []api.DiskLimit
	if code := e.callInto("PUT", "/v1/disk-limits", map[string]any{"limits": []any{}, "actor": "admin"}, &none); code != 200 {
		t.Fatalf("clearing the limits: %d", code)
	}
	if code, out := e.announce(imp, "world.zip", 64<<10); code != 201 {
		t.Fatalf("a world to import without a limit: %d %v", code, out)
	}
}
