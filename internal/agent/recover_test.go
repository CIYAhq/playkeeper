package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/offsite"
)

// archiveDest is a destination holding one real backup as a copy.
type archiveDest struct {
	fakeDest
	path string
	list error
}

func (d *archiveDest) List(ctx context.Context) ([]offsite.Object, error) {
	if d.list != nil {
		return nil, d.list
	}
	return d.fakeDest.List(ctx)
}

func (d *archiveDest) Download(_ context.Context, dl offsite.Download) (offsite.Archive, error) {
	archive := strings.TrimSuffix(dl.Name, ".age")
	in, err := os.Open(d.path)
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
	return offsite.Archive{Name: archive, Path: out}, nil
}

func TestANewMachineBringsAServerBackFromItsCopiesWithTheRecoveryKey(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	id := e.backup()
	var file string
	if err := e.a.db.QueryRow(`SELECT file_name FROM backups WHERE id = ?`, id).Scan(&file); err != nil {
		t.Fatal(err)
	}
	keys, err := offsite.NewKeys(e.a.now())
	if err != nil {
		t.Fatal(err)
	}
	rf, err := keys.RecoveryFileFor("Survival", "playkeeper/survival/", e.a.now())
	if err != nil {
		t.Fatal(err)
	}
	dest := &archiveDest{fakeDest: fakeDest{stored: map[string]offsite.Copy{
		offsite.CopyName(file): {Name: offsite.CopyName(file), Archive: file, Size: 4096},
	}}, path: e.a.backupPath(file)}
	var (
		mu     sync.Mutex
		opened []offsite.Config
		with   []string
	)
	prev := openOffsite
	openOffsite = func(c offsite.Config, k offsite.Keys, _ offsite.Options) (offsiteDest, error) {
		mu.Lock()
		opened, with = append(opened, c), append(with, k.Current.Recipient)
		mu.Unlock()
		return dest, nil
	}
	t.Cleanup(func() { openOffsite = prev })

	const secret = "wJalrXUtnFEMI-example-secret"
	s3 := map[string]any{"type": "s3", "s3": map[string]any{"provider": "b2", "endpoint": "s3.eu-central-003.backblazeb2.com", "bucket": "siya-minecraft", "accessKeyId": "003a8f91c2"}}
	body := func(extra map[string]any) map[string]any {
		b := map[string]any{"actor": "admin", "recoveryKey": rf.Content.Reveal(), "config": s3, "secretKey": secret}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}

	if code, out := e.call("POST", "/v1/offsite/recover", body(map[string]any{"recoveryKey": "# not a key\nhello\n"})); code != http.StatusBadRequest || out["field"] != "recoveryKey" || strings.Contains(out["error"].(string), "hello") {
		t.Fatalf("a file that isn't a recovery key: %d %v", code, out)
	}
	if code, out := e.call("POST", "/v1/offsite/recover", body(map[string]any{"secretKey": ""})); code != http.StatusBadRequest || out["field"] != "secretKey" {
		t.Fatalf("no secret key: %d %v", code, out)
	}
	if code, _ := e.call("POST", "/v1/offsite/recover", body(map[string]any{"actor": ""})); code != http.StatusBadRequest {
		t.Fatalf("nobody named: %d", code)
	}

	code, out := e.call("POST", "/v1/offsite/recover", body(nil))
	copies, _ := out["copies"].([]any)
	if code != http.StatusOK || out["server"] != "Survival" || out["keys"] != float64(1) || out["place"] != "Backblaze B2" || out["madeAt"] == nil || len(copies) != 1 {
		t.Fatalf("list: %d %v", code, out)
	}
	mu.Lock()
	last := opened[len(opened)-1]
	usedKey := with[len(with)-1]
	mu.Unlock()
	if last.S3.Prefix != "playkeeper/survival/" || last.S3.Endpoint != "https://s3.eu-central-003.backblazeb2.com" || last.S3.SecretKey.Reveal() != secret || usedKey != keys.Current.Recipient {
		t.Fatalf("opened %+v with key %s", last.S3, usedKey)
	}
	if code, out := e.call("POST", "/v1/offsite/recover/restore", body(map[string]any{"name": "../" + file + ".age"})); code != http.StatusBadRequest {
		t.Fatalf("a name with a path: %d %v", code, out)
	}
	code, out = e.call("POST", "/v1/offsite/recover/restore", body(map[string]any{"name": copies[0].(map[string]any)["name"]}))
	if code != http.StatusAccepted {
		t.Fatalf("restore: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != "succeeded" || op.Detail["restoreId"] == nil || op.Detail["server"] != "Survival" || op.Detail["place"] != "Backblaze B2" {
		t.Fatalf("restore op: %+v", op)
	}
	code, out = e.call("GET", "/v1/restore/"+op.Detail["restoreId"].(string), nil)
	if code != http.StatusOK || out["serverId"] != nil && out["serverId"] != "" {
		t.Fatalf("staged restore: %d %v", code, out)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'offsite.recover.listed' AND result = 'succeeded' AND actor = 'admin'`); n != 1 {
		t.Fatalf("audited %d listings", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE detail LIKE ? OR detail LIKE '%AGE-SECRET-KEY%'`, "%"+secret+"%"); n != 0 {
		t.Fatalf("%d audit lines hold a secret", n)
	}

	// Over SFTP, an unconfirmed host key comes back whole to be confirmed.
	dest.list = &offsite.Error{Kind: offsite.KindHostKeyUnknown, Msg: "Playkeeper doesn't know this machine yet.",
		HostKey: &offsite.HostKey{Key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample", Type: "ssh-ed25519", Fingerprint: "SHA256:example"}}
	sftp := map[string]any{"type": "sftp", "sftp": map[string]any{"host": "vault.example.net", "port": 22, "user": "playkeeper", "folder": "backups/survival"}}
	code, out = e.call("POST", "/v1/offsite/recover", body(map[string]any{"config": sftp, "secretKey": "", "password": "hunter2 but longer"}))
	params, _ := out["params"].(map[string]any)
	if code == http.StatusOK || out["reason"] != "host_key_unknown" || params["hostKey"] != "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample" {
		raw, _ := json.Marshal(out)
		t.Fatalf("unknown host key: %d %s", code, raw)
	}
	mu.Lock()
	last = opened[len(opened)-1]
	mu.Unlock()
	if last.SFTP.Password.Reveal() != "hunter2 but longer" || last.SFTP.HostKey != "" || last.SFTP.Folder != "backups/survival" {
		t.Fatalf("opened %+v", last.SFTP)
	}

	// The folder the file names may be one copies left after it was
	// downloaded: making it there would bring none back.
	missing := func(folder string) *offsite.Error {
		return &offsite.Error{Kind: offsite.KindNoSuchFolder, Op: "list", Field: "folder", Folder: folder,
			Msg: "There is no folder " + folder + " on the other machine.", Hint: "Check the folder's name."}
	}
	fromFile := map[string]any{"type": "sftp", "sftp": map[string]any{"host": "vault.example.net", "port": 22, "user": "playkeeper"}}
	const notInFile = "There is no folder playkeeper/survival/ on the other machine, the folder the recovery key file names."
	dest.list = missing("playkeeper/survival/")
	code, out = e.call("POST", "/v1/offsite/recover", body(map[string]any{"config": fromFile, "secretKey": "", "password": "hunter2 but longer"}))
	if hint, _ := out["hint"].(string); code == http.StatusOK || out["field"] != "folder" || out["error"] != notInFile || !strings.Contains(hint, "Type that folder under Folder.") {
		t.Fatalf("the file's folder isn't there: %d %v", code, out)
	}
	code, out = e.call("POST", "/v1/offsite/recover/restore", body(map[string]any{"config": fromFile, "secretKey": "", "password": "hunter2 but longer", "name": copies[0].(map[string]any)["name"]}))
	if code != http.StatusAccepted {
		t.Fatalf("restore from the file's folder: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != "failed" || op.Error != notInFile {
		t.Fatalf("restore from the file's folder: %+v", op)
	}
	dest.list = missing("backups/survival")
	code, out = e.call("POST", "/v1/offsite/recover", body(map[string]any{"config": sftp, "secretKey": "", "password": "hunter2 but longer"}))
	if code == http.StatusOK || out["error"] != "There is no folder backups/survival on the other machine." || out["hint"] != "Check the folder's name." {
		t.Fatalf("a typed folder that isn't there: %d %v", code, out)
	}
}
