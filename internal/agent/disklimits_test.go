package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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

// What an operation under way holds counts against the limit until it ends.
func TestWhatAnOperationHoldsCountsAgainstTheLimit(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	used := e.limitTo(1 << 40)
	e.limitTo(used + 100<<10)
	ctx := context.Background()
	done, err := e.a.holdDiskLimit(ctx, e.sid, 60<<10)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.a.diskLimitRefusal(ctx, e.sid, 60<<10); err == nil {
		t.Fatal("room beside what another operation holds")
	}
	done(false)
	if err := e.a.diskLimitRefusal(ctx, e.sid, 60<<10); err != nil {
		t.Fatalf("once the hold ended: %v", err)
	}
}

// Applying an uploaded world holds the world it adds.
func TestAnAppliedImportHoldsTheWorldItAdds(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.limitTo(1 << 40)
	archive, _ := paperServerUpload(t)
	imp := e.uploadWorld(e.sp("/world-imports"), "paper-server.zip", archive)
	pv := e.importPreview(imp, map[string]any{})
	used := e.limitTo(1 << 40)
	e.limitTo(used + pv.Preview.SizeBytes/2)
	if code, out := e.callWhenFree("POST", importPath(imp, "/apply"), map[string]any{"confirm": pv.ConfirmPhrase, "actor": "admin"}); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("applying a world past the limit: %d %v", code, out)
	}
}

// compressibleBackup gives the env's world a file that compresses to almost
// nothing and backs it up, so the backup's archive unpacks to far more than
// it takes.
func (e *agentEnv) compressibleBackup() (api.Backup, []byte) {
	e.t.Helper()
	world := filepath.Join(e.srv().dataDir(), "world")
	if err := os.MkdirAll(world, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(world, "filler.dat"), make([]byte, 2<<20), 0o644); err != nil {
		e.t.Fatal(err)
	}
	op := e.backupNow(nil)
	id, _ := op.Detail["backupId"].(string)
	if op.Status != api.OpSucceeded || id == "" {
		e.t.Fatalf("backing up: %+v", op)
	}
	var backups []api.Backup
	if code := e.callInto("GET", e.sp("/backups"), nil, &backups); code != 200 {
		e.t.Fatalf("backups: %d", code)
	}
	for _, b := range backups {
		if b.ID != id {
			continue
		}
		raw, err := os.ReadFile(e.srv().backupPath(b.FileName))
		if err != nil {
			e.t.Fatal(err)
		}
		if len(raw) > 256<<10 {
			e.t.Fatalf("the backup's archive takes %d bytes", len(raw))
		}
		return b, raw
	}
	e.t.Fatalf("backup %s isn't listed: %+v", id, backups)
	return api.Backup{}, nil
}

// claimingTotal rewrites an archive's manifest to claim its world takes n
// bytes, leaving its files as they are.
func claimingTotal(t *testing.T, archive []byte, n int64) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	tw := tar.NewWriter(zw)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(hdr.Name, "/manifest.json") {
			var m backup.Manifest
			if err := json.Unmarshal(body, &m); err != nil {
				t.Fatal(err)
			}
			m.TotalBytes = n
			if body, err = json.Marshal(m); err != nil {
				t.Fatal(err)
			}
			hdr.Size = int64(len(body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// A restore counts the world it unpacks to, not its archive, by the sizes of
// the files the archive holds rather than what its manifest claims. An
// upload is refused once it's staged, leaving no stage, and a staged restore
// is held when it's applied, from an upload or a backup of the server's own.
func TestRestoresCountTheWorldTheyUnpackTo(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	b, raw := e.compressibleBackup()
	stages := func() int {
		entries, _ := os.ReadDir(e.cfg.StagingDir())
		return len(entries)
	}

	used := e.limitTo(1 << 40)
	e.limitTo(used + int64(len(raw)) + 256<<10)
	before := stages()
	for name, archive := range map[string][]byte{"the archive": raw, "an archive claiming 1 byte": claimingTotal(t, raw, 1)} {
		if code, out := e.uploadTo(e.sp("/restore/upload"), archive); code != 507 || codeOf(out) != api.CodeDiskLimit {
			t.Fatalf("restoring %s past the limit: %d %v", name, code, out)
		}
	}
	if n := stages(); n != before {
		t.Fatalf("%d stages after the refused restores, %d before", n, before)
	}

	e.limitTo(1 << 40)
	staged := map[string]string{}
	stage := func(code int, out map[string]any) {
		t.Helper()
		id, _ := out["id"].(string)
		confirm, _ := out["confirmPhrase"].(string)
		if code != 200 || id == "" {
			t.Fatalf("staging a restore: %d %v", code, out)
		}
		staged[id] = confirm
	}
	stage(e.uploadTo(e.sp("/restore/upload"), raw))
	var out map[string]any
	code := e.callInto("POST", e.sp("/backups/"+b.ID+"/restore"), map[string]any{"actor": "admin"}, &out)
	stage(code, out)
	used = e.limitTo(1 << 40)
	e.limitTo(used + 1<<20)
	for id, confirm := range staged {
		if code, out := e.callWhenFree("POST", "/v1/restore/"+id+"/apply", map[string]any{"confirm": confirm, "actor": "admin"}); code != 507 || codeOf(out) != api.CodeDiskLimit {
			t.Fatalf("applying a staged restore past the limit: %d %v", code, out)
		}
	}
}

// The rollback archive a restore saves of the world it replaces is kept, so
// it has to fit the limit like any backup: a restore with room for its world
// but not for that archive stops before replacing anything.
func TestARestoresRollbackArchiveHasToFit(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	b, _ := e.compressibleBackup()
	var out map[string]any
	if code := e.callInto("POST", e.sp("/backups/"+b.ID+"/restore"), map[string]any{"actor": "admin"}, &out); code != 200 {
		t.Fatalf("staging a restore: %d %v", code, out)
	}
	id, _ := out["id"].(string)
	confirm, _ := out["confirmPhrase"].(string)
	f, err := readStageFile(e.a.stageDir(id))
	if err != nil {
		t.Fatal(err)
	}
	used := e.limitTo(1 << 40)
	e.limitTo(used + unpackedBytes(f.Manifest) + 256<<10)
	code, out := e.callWhenFree("POST", "/v1/restore/"+id+"/apply", map[string]any{"confirm": confirm, "actor": "admin"})
	opID, _ := out["id"].(string)
	if code != 202 || opID == "" {
		t.Fatalf("applying a restore with room for its world: %d %v", code, out)
	}
	if op := e.waitOp(opID); op.Status != api.OpFailed || !strings.Contains(op.Error, "disk limit") || !strings.Contains(op.Error, "nothing was replaced") {
		t.Fatalf("a restore with no room for its rollback archive: %+v", op)
	}
}

// A backup whose world can't be measured still counts, as everything the
// server takes.
func TestABackupThatCantBeMeasuredStillCounts(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	props := filepath.Join(e.srv().dataDir(), "server.properties")
	if err := os.Remove(props); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", props); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Measure(e.srv().dataDir(), archiveLimits()); err == nil {
		t.Fatal("a server.properties that's a link measured")
	}
	used := e.limitTo(1 << 40)
	e.limitTo(used + 1024)
	if op := e.backupNow(nil); op.Status != api.OpFailed || !strings.Contains(op.Error, "disk limit") {
		t.Fatalf("a backup that can't be measured, past the limit: %+v", op)
	}
}

// An installed data pack counts before a scan finds it.
func TestAnInstalledDataPackCounts(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	zip := dataPackZip(t, "Keeps your items", true)
	used := e.limitTo(1 << 40)
	e.limitTo(used + int64(len(zip))*3/2)
	e.addDataPack("graves.zip", zip)
	if code, out := e.whenFree(func() (int, map[string]any) {
		return e.uploadTo(e.sp("/datapacks?name=more-graves.zip"), zip)
	}); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("a second data pack, before a scan found the first: %d %v", code, out)
	}
}

// Pre-generation under way counts what its area may still take, until it
// ends.
func TestPregenUnderWayCountsAgainstTheLimit(t *testing.T) {
	e := newAgentEnv(t)
	e.withSources()
	e.create()
	e.chunky()
	var small api.PregenPreset
	for _, p := range e.pregen().Presets {
		if p.ID == "small" {
			small = p
		}
	}
	if small.DiskBytes <= 0 {
		t.Fatalf("the small preset: %+v", small)
	}
	// Room for the most its area may take, which is at most twice the
	// preset's middle estimate, and then for the middle estimate again only
	// once it has ended.
	used := e.limitTo(1 << 40)
	e.limitTo(used + 2*small.DiskBytes + 64<<10)
	e.startPregen("small", false)
	ctx := context.Background()
	if err := e.a.diskLimitRefusal(ctx, e.sid, small.DiskBytes); err == nil {
		t.Fatal("room beside pre-generation under way")
	}
	e.pregenAct("cancel")
	if err := e.a.diskLimitRefusal(ctx, e.sid, small.DiskBytes); err != nil {
		t.Fatalf("once pre-generation ended: %v", err)
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
