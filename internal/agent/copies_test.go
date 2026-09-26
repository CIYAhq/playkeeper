package agent

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/offsite"
)

// fetchDest is a destination whose downloads each step of a test decides.
type fetchDest struct {
	fakeDest
	dl       sync.Mutex
	download func(context.Context, offsite.Download) (offsite.Archive, error)
}

func (d *fetchDest) Download(ctx context.Context, dl offsite.Download) (offsite.Archive, error) {
	d.dl.Lock()
	fn := d.download
	d.dl.Unlock()
	return fn(ctx, dl)
}

func (d *fetchDest) answer(fn func(context.Context, offsite.Download) (offsite.Archive, error)) {
	d.dl.Lock()
	d.download = fn
	d.dl.Unlock()
}

func failWith(err error) func(context.Context, offsite.Download) (offsite.Archive, error) {
	return func(context.Context, offsite.Download) (offsite.Archive, error) { return offsite.Archive{}, err }
}

// fromBackup answers a download with the backup at path, as a copy that
// decrypted and matched its record.
func fromBackup(path string) func(context.Context, offsite.Download) (offsite.Archive, error) {
	return func(_ context.Context, dl offsite.Download) (offsite.Archive, error) {
		archive := strings.TrimSuffix(dl.Name, ".age")
		in, err := os.Open(path)
		if err != nil {
			return offsite.Archive{}, err
		}
		defer in.Close()
		out := filepath.Join(dl.Dir, archive)
		f, err := os.Create(out)
		if err != nil {
			return offsite.Archive{}, err
		}
		defer f.Close()
		if _, err := io.Copy(f, in); err != nil {
			return offsite.Archive{}, err
		}
		return offsite.Archive{Name: archive, Path: out, Matched: dl.ArchiveSHA256 != ""}, nil
	}
}

// withCopies starts a server with one backup, copied to dest.
func withCopies(t *testing.T, dest offsiteDest) (e *agentEnv, backupID, file string) {
	t.Helper()
	prev := openOffsite
	openOffsite = func(offsite.Config, offsite.Keys, offsite.Options) (offsiteDest, error) { return dest, nil }
	t.Cleanup(func() { openOffsite = prev })
	e = newAgentEnv(t)
	e.create()
	backupID = e.backup()
	if err := e.a.db.QueryRow(`SELECT file_name FROM backups WHERE id = ?`, backupID).Scan(&file); err != nil {
		t.Fatal(err)
	}
	s3 := map[string]any{"provider": "minio", "endpoint": "203.0.113.10:9000", "bucket": "worlds", "accessKeyId": "PKEXAMPLE"}
	code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "admin", "enabled": true, "config": map[string]any{"type": "s3", "s3": s3}, "secretKey": "wJalrXUtnFEMI-example-secret"})
	if code != http.StatusOK {
		t.Fatalf("turn copies on: %d %v", code, out)
	}
	e.waitFor("the copy", func() bool {
		return e.countRows(`SELECT COUNT(*) FROM offsite_copies WHERE backup_id = ?`, backupID) == 1
	})
	return e, backupID, file
}

func (e *agentEnv) staged() []string {
	entries, _ := os.ReadDir(e.a.cfg.StagingDir())
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	return names
}

func TestACopyIsCheckedAgainOrDeletedFromItsRow(t *testing.T) {
	dest := &fetchDest{fakeDest: fakeDest{stored: map[string]offsite.Copy{}}}
	e, backupID, file := withCopies(t, dest)
	name := offsite.CopyName(file)
	var sum string
	if err := e.a.db.QueryRow(`SELECT sha256 FROM backups WHERE id = ?`, backupID).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	row := func() map[string]any {
		t.Helper()
		code, out := e.call("GET", e.sp("/offsite/copies"), nil)
		if code != http.StatusOK {
			t.Fatalf("copies: %d %v", code, out)
		}
		for _, c := range out["copies"].([]any) {
			if c := c.(map[string]any); c["name"] == name {
				return c
			}
		}
		return nil
	}
	check := func() *api.Operation {
		t.Helper()
		code, out := e.call("POST", e.sp("/offsite/copies/"+url.PathEscape(name)+"/check"), map[string]any{"actor": "admin"})
		if code != http.StatusAccepted {
			t.Fatalf("check: %d %v", code, out)
		}
		return e.waitOp(out["id"].(string))
	}
	if c := row(); c == nil || c["sha256"] != sum || c["checkError"] != nil || c["checked"] != offsite.CheckedSize {
		t.Fatalf("the copy's row: %v", c)
	}

	const damaged = "The copy doesn't match the backup it was made from."
	dest.answer(failWith(&offsite.Error{Kind: offsite.KindVerifyFailed, Msg: damaged}))
	if op := check(); op.Status != api.OpFailed || op.Kind != "offsite-check" || op.Detail["name"] != name {
		t.Fatalf("checking a damaged copy: %+v", op)
	}
	if c := row(); c["checkError"] != damaged || c["checked"] != offsite.CheckedSize {
		t.Fatalf("a damaged copy's row: %v", c)
	}

	// Not reaching the storage says nothing about the copy itself.
	dest.answer(failWith(&offsite.Error{Kind: offsite.KindNetwork, Msg: "Couldn't reach the storage.", Retry: true}))
	if op := check(); op.Status != api.OpFailed || op.Error != "Couldn't reach the storage." {
		t.Fatalf("checking without the storage: %+v", op)
	}
	if c := row(); c["checkError"] != damaged {
		t.Fatalf("a check that never reached the copy changed its row: %v", c)
	}

	dest.answer(fromBackup(e.a.backupPath(file)))
	if op := check(); op.Status != api.OpSucceeded {
		t.Fatalf("checking a good copy: %+v", op)
	}
	if c := row(); c["checked"] != offsite.CheckedDecrypted || c["checkError"] != nil {
		t.Fatalf("a good copy's row: %v", c)
	}
	if left := e.staged(); len(left) != 0 {
		t.Fatalf("checks left %v behind", left)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'offsite.copy_checked' AND actor = 'admin' AND target = ?`, backupID); n != 2 {
		t.Fatalf("audited %d checks of the copy, want the damaged one and the good one", n)
	}

	copyPath := e.sp("/offsite/copies/" + url.PathEscape(name))
	if code, _ := e.call("DELETE", copyPath, nil); code != http.StatusBadRequest {
		t.Fatalf("deleting a copy with nobody named: %d", code)
	}
	if code, _ := e.call("DELETE", e.sp("/offsite/copies/"+url.PathEscape(file))+"?actor=admin", nil); code != http.StatusBadRequest {
		t.Fatalf("deleting something that isn't a copy: %d", code)
	}
	code, out := e.call("DELETE", copyPath+"?actor=admin", nil)
	if code != http.StatusOK || out["deleted"] != name {
		t.Fatalf("delete: %d %v", code, out)
	}
	dest.mu.Lock()
	deleted := append([]string(nil), dest.deleted...)
	dest.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != name {
		t.Fatalf("deleted %q where copies are kept, want %s", deleted, name)
	}
	if c := row(); c != nil {
		t.Fatalf("the deleted copy is still listed: %v", c)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'offsite.copy_deleted' AND actor = 'admin' AND target = ? AND result = 'succeeded'`, backupID); n != 1 {
		t.Fatalf("audited the deletion %d times", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM backups WHERE id = ?`, backupID); n != 1 {
		t.Fatal("deleting the copy deleted the backup on this machine")
	}
	if code, _ := e.call("DELETE", copyPath+"?actor=admin", nil); code != http.StatusNotFound {
		t.Fatalf("deleting it again: %d", code)
	}
	if code, _ := e.call("POST", copyPath+"/check", map[string]any{"actor": "admin"}); code != http.StatusNotFound {
		t.Fatalf("checking a deleted copy: %d", code)
	}
}
