package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/offsite"
)

// keyedDest is a destination for copies opened with the server's keys: it
// records the key each upload is encrypted to, and the copies it makes carry
// it. An upload of an archive in hold reports the state in states, if any,
// then waits until the test closes its channel or the upload is stopped.
type keyedDest struct {
	*keyedStore
	recipient string
}

type keyedStore struct {
	fakeDest
	log     []keyedUpload
	hold    map[string]chan struct{}
	states  map[string]offsite.UploadState
	waiting chan string
}

type keyedUpload struct {
	archive, recipient string
	resume             *offsite.UploadState
}

func newKeyedStore() *keyedStore {
	return &keyedStore{fakeDest: fakeDest{stored: map[string]offsite.Copy{}}, hold: map[string]chan struct{}{},
		states: map[string]offsite.UploadState{}, waiting: make(chan string, 4)}
}

func (d keyedDest) Upload(ctx context.Context, up offsite.Upload) (offsite.Copy, error) {
	d.mu.Lock()
	d.log = append(d.log, keyedUpload{archive: up.Name, recipient: d.recipient, resume: up.Resume})
	hold, st, stops := d.hold[up.Name], d.states[up.Name], d.states[up.Name].Name != ""
	delete(d.hold, up.Name)
	d.mu.Unlock()
	if hold != nil {
		if stops && up.Progress != nil {
			st.Recipient = d.recipient
			up.Progress(offsite.Progress{Sent: storedBytes(&st), Total: up.Size, State: &st})
		}
		d.waiting <- up.Name
		select {
		case <-hold:
		case <-ctx.Done():
			return offsite.Copy{}, ctx.Err()
		}
	}
	cp, err := d.fakeDest.Upload(ctx, up)
	cp.Recipient = d.recipient
	return cp, err
}

func (d *keyedStore) uploads() []keyedUpload {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]keyedUpload(nil), d.log...)
}

// holdUpload makes the next upload of archive wait, having reported st if
// it names a copy, until release is called or the upload is stopped.
func (d *keyedStore) holdUpload(archive string, st offsite.UploadState) (release func()) {
	ch := make(chan struct{})
	d.mu.Lock()
	d.hold[archive], d.states[archive] = ch, st
	d.mu.Unlock()
	return func() { close(ch) }
}

// Making a new key reaches the copy being made and the copies waiting:
// nothing that finishes after the new key is encrypted to the old one, and a
// copy stopped for the new key isn't a failed try.
func TestANewKeyReachesTheCopyBeingMade(t *testing.T) {
	// withKeyed has copies go to a keyedStore; the agent opens it anew each
	// round, with the keys current then.
	withKeyed := func(t *testing.T) (*agentEnv, *keyedStore) {
		store := newKeyedStore()
		prev := openOffsite
		openOffsite = func(_ offsite.Config, k offsite.Keys, _ offsite.Options) (offsiteDest, error) {
			return keyedDest{keyedStore: store, recipient: k.Current.Recipient}, nil
		}
		t.Cleanup(func() { openOffsite = prev })
		e := newAgentEnv(t)
		e.create()
		return e, store
	}
	turnOn := func(e *agentEnv) string {
		e.t.Helper()
		s3 := map[string]any{"provider": "minio", "endpoint": "203.0.113.10:9000", "bucket": "worlds", "accessKeyId": "PKEXAMPLE"}
		code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "admin", "enabled": true, "config": map[string]any{"type": "s3", "s3": s3}, "secretKey": "wJalrXUtnFEMI-example-secret"})
		key, _ := out["key"].(map[string]any)
		if code != http.StatusOK || key == nil {
			e.t.Fatalf("turn on: %d %v", code, out)
		}
		return key["recipient"].(string)
	}
	newKey := func(e *agentEnv) string {
		e.t.Helper()
		code, out := e.call("POST", e.sp("/offsite/new-key"), map[string]any{"actor": "owner"})
		rot, _ := out["rotation"].(map[string]any)
		if code != http.StatusOK || rot == nil {
			e.t.Fatalf("new key: %d %v", code, out)
		}
		return rot["recipient"].(string)
	}
	fileOf := func(e *agentEnv, id string) string {
		e.t.Helper()
		b, err := e.srv().getBackup(id)
		if err != nil {
			e.t.Fatal(err)
		}
		return b.FileName
	}
	waitUpload := func(e *agentEnv, store *keyedStore, archive string) {
		e.t.Helper()
		select {
		case got := <-store.waiting:
			if got != archive {
				e.t.Fatalf("the upload of %s waits, not %s", got, archive)
			}
		case <-time.After(15 * time.Second):
			e.t.Fatalf("the upload of %s never started", archive)
		}
	}
	// copied waits for the backups' copies and returns the key each is
	// encrypted to.
	copied := func(e *agentEnv, ids ...string) map[string]string {
		e.t.Helper()
		out := map[string]string{}
		e.waitFor("the copies", func() bool {
			for _, id := range ids {
				var raw string
				if e.a.db.QueryRow(`SELECT copy FROM offsite_copies WHERE backup_id = ?`, id).Scan(&raw) != nil {
					return false
				}
				var cp copyRecord
				_ = json.Unmarshal([]byte(raw), &cp)
				out[id] = cp.Recipient
			}
			return true
		})
		return out
	}
	// allWith checks that every upload from the from-th on is encrypted to
	// key, and that no try failed.
	allWith := func(e *agentEnv, store *keyedStore, from int, key string) {
		e.t.Helper()
		ups := store.uploads()
		if len(ups) <= from {
			e.t.Fatalf("%d uploads, none after the new key", len(ups))
		}
		for _, up := range ups[from:] {
			if up.recipient != key {
				e.t.Fatalf("%s was encrypted to %s after the new key %s", up.archive, up.recipient, key)
			}
		}
		if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'offsite.copy_failed'`); n != 0 || strings.Contains(e.warnings.String(), "a copy somewhere else failed") {
			e.t.Fatalf("a copy stopped for the new key counted as failed (%d audit lines): %s", n, e.warnings.String())
		}
		if n := e.countRows(`SELECT COUNT(*) FROM offsite_uploads`); n != 0 {
			e.t.Fatalf("%d copies still wait", n)
		}
	}

	t.Run("during a copy, with another backup waiting", func(t *testing.T) {
		e, store := withKeyed(t)
		first := e.backup()
		store.holdUpload(fileOf(e, first), offsite.UploadState{})
		old := turnOn(e)
		waitUpload(e, store, fileOf(e, first))
		second := e.backup()
		key := newKey(e)
		if got := copied(e, first, second); got[first] != key || got[second] != key || key == old {
			t.Fatalf("copies encrypted to %v, the new key is %s and the old %s", got, key, old)
		}
		allWith(e, store, 1, key)
	})

	t.Run("while the next copy is picked", func(t *testing.T) {
		e, store := withKeyed(t)
		older := e.backup()
		newer := e.backup()
		if _, err := e.a.db.Exec(`INSERT INTO offsite_uploads(server_id, backup_id, created_at) VALUES(?, ?, ?)`, e.sid, older, e.srv().now().Add(-time.Hour).UnixMilli()); err != nil {
			t.Fatal(err)
		}
		// The uploader waits right after it picks the older backup's copy,
		// once the newer one's is made in the same round.
		var holding atomic.Pointer[string]
		holding.Store(&older)
		picked, release := make(chan struct{}), make(chan struct{})
		prevHook := uploadClaimed
		uploadClaimed = func(job uploadJob) {
			if id := holding.Load(); id != nil && *id == job.backupID && holding.CompareAndSwap(id, nil) {
				close(picked)
				<-release
			}
		}
		t.Cleanup(func() { uploadClaimed = prevHook })
		old := turnOn(e)
		waitClosed(t, picked, "the older backup's copy to be picked")
		// A new key saved before the pick: the round, opened with the old
		// key, must not encrypt the copy to it.
		row, err := e.srv().loadOffsite()
		if err != nil {
			t.Fatal(err)
		}
		keys, rot, err := row.keys.Rotate(e.srv().now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.a.db.Exec(`UPDATE offsite SET keys = ? WHERE server_id = ?`, encodeKeys(keys), e.sid); err != nil {
			t.Fatal(err)
		}
		close(release)
		e.srv().kickOffsite()
		if got := copied(e, older, newer); got[newer] != old || got[older] != rot.Recipient {
			t.Fatalf("copies encrypted to %v; the newer was made before the new key %s, the older after", got, rot.Recipient)
		}
		allWith(e, store, 1, rot.Recipient)
	})

	t.Run("during a copy that saved where it stopped", func(t *testing.T) {
		e, store := withKeyed(t)
		id := e.backup()
		file := fileOf(e, id)
		store.holdUpload(file, offsite.UploadState{Archive: file, Name: offsite.CopyName(file), Size: 1000,
			S3: &offsite.S3Upload{Key: "k", UploadID: "u1", PartSize: 5 << 20, Parts: []offsite.Part{{Number: 1, Size: 400}}}})
		old := turnOn(e)
		waitUpload(e, store, file)
		e.waitFor("where the copy stopped to be saved", func() bool {
			return e.countRows(`SELECT COUNT(*) FROM offsite_uploads WHERE backup_id = ? AND state LIKE '%"u1"%'`, id) == 1
		})
		// Turning copies on can leave a kick waiting; only the new key's
		// own may start the round that starts the copy over.
		select {
		case <-e.srv().offsiteKick():
		default:
		}
		key := newKey(e)
		if got := copied(e, id); got[id] != key {
			t.Fatalf("the copy is encrypted to %s, not the new key %s", got[id], key)
		}
		ups := store.uploads()
		// The destination starts over a saved upload made with another key.
		if last := ups[len(ups)-1]; last.resume == nil || last.resume.Recipient != old || last.resume.S3 == nil || last.resume.S3.UploadID != "u1" {
			t.Fatalf("the copy went on from %+v, not from where it stopped with the old key %s", last.resume, old)
		}
		allWith(e, store, 1, key)
	})

	t.Run("between rounds", func(t *testing.T) {
		e, store := withKeyed(t)
		old := turnOn(e)
		key := newKey(e)
		id := e.backup()
		if got := copied(e, id); got[id] != key || key == old {
			t.Fatalf("the copy is encrypted to %s, the new key is %s", got[id], key)
		}
		allWith(e, store, 0, key)
	})
}

// A copy that was made is recorded even when the settings can't be read once
// it is, so the next round doesn't make it again. Only settings read after
// it that turn copies off, or send them elsewhere, leave it unrecorded, for
// the next round to sort out.
func TestAMadeCopyIsRecordedUnlessTheSettingsReadAfterItChanged(t *testing.T) {
	s3 := func(bucket string) map[string]any {
		return map[string]any{"provider": "minio", "endpoint": "203.0.113.10:9000", "bucket": bucket, "accessKeyId": "PKEXAMPLE"}
	}
	cases := []struct {
		name string
		// meanwhile changes agent.db while the copy is being made, without
		// the routes that would stop it.
		meanwhile func(e *agentEnv)
		recorded  bool
		warns     bool
	}{
		{name: "nothing changed", meanwhile: func(*agentEnv) {}, recorded: true},
		{name: "the settings can't be read once it is made", meanwhile: func(e *agentEnv) {
			if _, err := e.a.db.Exec(`UPDATE offsite SET config = '{' WHERE server_id = ?`, e.sid); err != nil {
				e.t.Fatal(err)
			}
		}, recorded: true, warns: true},
		{name: "the keys can't be read once it is made", meanwhile: func(e *agentEnv) {
			if _, err := e.a.db.Exec(`UPDATE offsite SET keys = '{' WHERE server_id = ?`, e.sid); err != nil {
				e.t.Fatal(err)
			}
		}, recorded: true, warns: true},
		{name: "copies were turned off meanwhile", meanwhile: func(e *agentEnv) {
			if _, err := e.a.db.Exec(`UPDATE offsite SET enabled = 0 WHERE server_id = ?`, e.sid); err != nil {
				e.t.Fatal(err)
			}
		}},
		{name: "copies go elsewhere since", meanwhile: func(e *agentEnv) {
			row, err := e.srv().loadOffsite()
			if err != nil {
				e.t.Fatal(err)
			}
			row.cfg.S3.Bucket = "worlds-elsewhere"
			if err := e.srv().saveOffsite(row); err != nil {
				e.t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newKeyedStore()
			prev := openOffsite
			openOffsite = func(_ offsite.Config, k offsite.Keys, _ offsite.Options) (offsiteDest, error) {
				return keyedDest{keyedStore: store, recipient: k.Current.Recipient}, nil
			}
			t.Cleanup(func() { openOffsite = prev })
			e := newAgentEnv(t)
			e.create()
			b, err := e.srv().getBackup(e.backup())
			if err != nil {
				t.Fatal(err)
			}
			release := store.holdUpload(b.FileName, offsite.UploadState{})
			if code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "admin", "enabled": true, "config": map[string]any{"type": "s3", "s3": s3("worlds")}, "secretKey": "wJalrXUtnFEMI-example-secret"}); code != http.StatusOK {
				t.Fatalf("turn on: %d %v", code, out)
			}
			select {
			case <-store.waiting:
			case <-time.After(15 * time.Second):
				t.Fatal("the copy was never started")
			}
			// Only what the uploader does next may start another round.
			select {
			case <-e.srv().offsiteKick():
			default:
			}
			c.meanwhile(e)
			release()
			e.waitFor("the uploader to be done with the copy", func() bool {
				s := e.srv()
				s.auto.mu.Lock()
				defer s.auto.mu.Unlock()
				return s.auto.claim == nil
			})
			recorded := e.countRows(`SELECT COUNT(*) FROM offsite_copies WHERE backup_id = ?`, b.ID) == 1
			queued := e.countRows(`SELECT COUNT(*) FROM offsite_uploads WHERE backup_id = ?`, b.ID) == 1
			if recorded != c.recorded || queued == c.recorded || len(store.uploads()) != 1 {
				t.Fatalf("the copy is recorded: %v, still queued: %v, after %d uploads; want recorded %v", recorded, queued, len(store.uploads()), c.recorded)
			}
			if warned := strings.Contains(e.warnings.String(), "the settings for copies somewhere else can't be read"); warned != c.warns {
				t.Fatalf("warned: %v, want %v: %s", warned, c.warns, e.warnings.String())
			}
			if c.recorded && e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'offsite.copied' AND target = ? AND detail LIKE ?`, b.ID, "% · MinIO") != 1 {
				t.Fatal("the copy's audit line doesn't say where it was made")
			}
		})
	}
}

// Saving the settings for copies never writes the encryption keys: a save
// that read them before Make a new key doesn't put the old key back, and
// the first keys are stored only while there are none.
func TestSavingCopySettingsNeverPutsAnOldKeyBack(t *testing.T) {
	withCopies := func(t *testing.T) *agentEnv {
		prev := openOffsite
		dest := &fakeDest{stored: map[string]offsite.Copy{}}
		openOffsite = func(offsite.Config, offsite.Keys, offsite.Options) (offsiteDest, error) { return dest, nil }
		t.Cleanup(func() { openOffsite = prev })
		e := newAgentEnv(t)
		e.addIdleServer()
		return e
	}
	s3 := map[string]any{"type": "s3", "s3": map[string]any{"provider": "minio", "endpoint": "203.0.113.10:9000", "bucket": "worlds", "accessKeyId": "PKEXAMPLE"}}
	// Each save read the settings before the new key was made, and writes
	// them after it.
	saves := []struct {
		name   string
		change func(r *offsiteRow)
	}{
		{"the settings", func(r *offsiteRow) { r.enabled = false }},
		{"the key Playkeeper signs in with over SFTP", func(r *offsiteRow) {
			r.privateKey, r.sshPublic = "a private key", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample playkeeper"
		}},
	}
	for _, c := range saves {
		t.Run("a save of "+c.name+" racing a new key", func(t *testing.T) {
			e := withCopies(t)
			code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "owner", "enabled": true, "config": s3, "secretKey": "wJalrXUtnFEMI-example-secret"})
			key, _ := out["key"].(map[string]any)
			if code != http.StatusOK || key == nil {
				t.Fatalf("turn on: %d %v", code, out)
			}
			old := key["recipient"].(string)
			read, err := e.srv().loadOffsite()
			if err != nil {
				t.Fatal(err)
			}
			code, out = e.call("POST", e.sp("/offsite/new-key"), map[string]any{"actor": "owner"})
			rot, _ := out["rotation"].(map[string]any)
			if code != http.StatusOK || rot == nil {
				t.Fatalf("new key: %d %v", code, out)
			}
			c.change(&read)
			if err := e.srv().saveOffsite(read); err != nil {
				t.Fatal(err)
			}
			row, err := e.srv().loadOffsite()
			if err != nil {
				t.Fatal(err)
			}
			if row.keys.Current.Recipient != rot["recipient"] || len(row.keys.Old) != 1 || row.keys.Old[0].Recipient != old {
				t.Fatalf("after the save the key is %s with %d old ones; the new key is %s, the old %s", row.keys.Current.Recipient, len(row.keys.Old), rot["recipient"], old)
			}
		})
	}

	t.Run("first keys stored while another request stored its own", func(t *testing.T) {
		e := withCopies(t)
		if code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "owner", "enabled": false, "config": s3, "secretKey": "wJalrXUtnFEMI-example-secret"}); code != http.StatusOK {
			t.Fatalf("settings: %d %v", code, out)
		}
		theirs, err := offsite.NewKeys(e.a.now())
		if err != nil {
			t.Fatal(err)
		}
		ours, err := offsite.NewKeys(e.a.now())
		if err != nil {
			t.Fatal(err)
		}
		if got, err := e.srv().storeFirstKeys(theirs); err != nil || got.Current.Recipient != theirs.Current.Recipient {
			t.Fatalf("the first keys: %s, %v", got.Current.Recipient, err)
		}
		got, err := e.srv().storeFirstKeys(ours)
		row, lerr := e.srv().loadOffsite()
		if err != nil || lerr != nil || got.Current.Recipient != theirs.Current.Recipient || row.keys.Current.Recipient != theirs.Current.Recipient {
			t.Fatalf("keys stored second replaced the first: got %s, stored %s (%v, %v)", got.Current.Recipient, row.keys.Current.Recipient, err, lerr)
		}
	})

	t.Run("turning copies on stores the first keys", func(t *testing.T) {
		e := withCopies(t)
		code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "owner", "enabled": true, "config": s3, "secretKey": "wJalrXUtnFEMI-example-secret"})
		key, _ := out["key"].(map[string]any)
		row, err := e.srv().loadOffsite()
		if code != http.StatusOK || key == nil || err != nil || !row.hasKeys || row.keys.Current.Recipient != key["recipient"] {
			t.Fatalf("turn on: %d %v; stored %+v (%v)", code, out, row.keys.Current, err)
		}
	})
}

// Deleting a server deletes its recovery key with it. While copies only that
// key opens are kept somewhere else, or still made, and the key was never
// downloaded, the delete is refused with the reason, unless it is confirmed.
func TestDeletingAServerAsksBeforeItDeletesTheOnlyKeyToItsCopies(t *testing.T) {
	turnOn := func(e *agentEnv) {
		e.t.Helper()
		s3 := map[string]any{"provider": "b2", "endpoint": "s3.eu-central-003.backblazeb2.com", "bucket": "siya-minecraft", "accessKeyId": "003a8f91c2"}
		if code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "owner", "enabled": true, "config": map[string]any{"type": "s3", "s3": s3}, "secretKey": "wJalrXUtnFEMI-example-secret"}); code != http.StatusOK {
			e.t.Fatalf("turn on: %d %v", code, out)
		}
	}
	turnOff := func(e *agentEnv) {
		e.t.Helper()
		if code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "owner", "enabled": false}); code != http.StatusOK {
			e.t.Fatalf("turn off: %d %v", code, out)
		}
	}
	copyKept := func(e *agentEnv) {
		e.t.Helper()
		id := e.backup()
		turnOn(e)
		e.waitFor("the copy", func() bool { return e.countRows(`SELECT COUNT(*) FROM offsite_copies WHERE backup_id = ?`, id) == 1 })
	}
	downloadKey := func(e *agentEnv) {
		e.t.Helper()
		req, _ := http.NewRequest("GET", e.ts.URL+e.sp("/offsite/recovery-key"), nil)
		req.Header.Set("X-Playkeeper-Actor", "owner")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			e.t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			e.t.Fatalf("recovery key: %d", resp.StatusCode)
		}
	}
	cases := []struct {
		name    string
		setup   func(e *agentEnv)
		confirm bool
		copies  float64
		refused bool
	}{
		{name: "a copy kept, the key never downloaded", setup: copyKept, copies: 1, refused: true},
		{name: "copies on but none made yet, the key never downloaded", setup: turnOn, refused: true},
		{name: "a copy kept after copies were turned off, the key never downloaded", setup: func(e *agentEnv) { copyKept(e); turnOff(e) }, copies: 1, refused: true},
		{name: "a copy kept, the key downloaded", setup: func(e *agentEnv) { copyKept(e); downloadKey(e) }},
		{name: "a copy kept, the key never downloaded, and the delete confirmed", setup: copyKept, confirm: true},
		{name: "copies turned off before any was made", setup: func(e *agentEnv) { turnOn(e); turnOff(e) }},
		{name: "copies never turned on", setup: func(*agentEnv) {}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prev := openOffsite
			dest := &fakeDest{stored: map[string]offsite.Copy{}}
			openOffsite = func(offsite.Config, offsite.Keys, offsite.Options) (offsiteDest, error) { return dest, nil }
			t.Cleanup(func() { openOffsite = prev })
			e := newAgentEnv(t)
			e.create()
			c.setup(e)
			code, out := e.call("POST", e.sp("/delete"), map[string]any{"actor": "admin", "confirm": e.srv().name(), "forgetKey": c.confirm})
			if c.refused {
				params, _ := out["params"].(map[string]any)
				if code != http.StatusConflict || out["reason"] != "recovery_key_not_saved" || params["place"] != "Backblaze B2" || params["copies"] != c.copies {
					t.Fatalf("delete: %d %v", code, out)
				}
				if e.a.serverByID(e.sid) == nil || e.countRows(`SELECT COUNT(*) FROM offsite WHERE server_id = ? AND keys != ''`, e.sid) != 1 {
					t.Fatal("a refused delete deleted the server or its key")
				}
				if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'server.deleted' AND result = 'refused' AND detail = 'its recovery key was never downloaded'`); n != 1 {
					t.Fatalf("audited %d refusals", n)
				}
				return
			}
			if code != http.StatusAccepted {
				t.Fatalf("delete: %d %v", code, out)
			}
			if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded || e.a.serverByID(e.sid) != nil {
				t.Fatalf("delete op: %+v", op)
			}
		})
	}
}

// slowDest hands over its copy of a real backup only after takes, as a home
// NAS on a slow link does, and stops when the download is cancelled.
type slowDest struct {
	archiveDest
	takes time.Duration
}

func (d *slowDest) Download(ctx context.Context, dl offsite.Download) (offsite.Archive, error) {
	t := time.NewTimer(d.takes)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return offsite.Archive{}, ctx.Err()
	case <-t.C:
	}
	return d.archiveDest.Download(ctx, dl)
}

// A restore of a copy, on this machine or from a recovery key on a new one,
// takes as long as the copy takes to come: the download's stall timeout and
// Cancel stop it, not the operations' deadline. Other operations keep it.
func TestRestoresFromCopiesOutlastTheOperationDeadline(t *testing.T) {
	const takes = 1500 * time.Millisecond
	// shortDeadline shortens the deadline once the test's setup is done.
	shortDeadline := func(t *testing.T) {
		prev := opTimeout
		opTimeout = 300 * time.Millisecond
		t.Cleanup(func() { opTimeout = prev })
	}
	withSlow := func(t *testing.T) (*agentEnv, *slowDest, string) {
		dest := &slowDest{archiveDest: archiveDest{fakeDest: fakeDest{stored: map[string]offsite.Copy{}}}, takes: takes}
		prev := openOffsite
		openOffsite = func(offsite.Config, offsite.Keys, offsite.Options) (offsiteDest, error) { return dest, nil }
		t.Cleanup(func() { openOffsite = prev })
		e := newAgentEnv(t)
		e.create()
		b, err := e.srv().getBackup(e.backup())
		if err != nil {
			t.Fatal(err)
		}
		dest.path = e.a.backupPath(b.FileName)
		return e, dest, b.FileName
	}
	outlasts := func(t *testing.T, e *agentEnv, id string) {
		t.Helper()
		began := time.Now()
		op := e.waitOp(id)
		if op.Status != "succeeded" || op.Detail["restoreId"] == nil || time.Since(began) < takes-100*time.Millisecond {
			t.Fatalf("a restore whose copy takes %s: %+v after %s", takes, op, time.Since(began))
		}
	}
	cutShort := func(t *testing.T, e *agentEnv, id string) {
		t.Helper()
		if op := e.waitOp(id); op.Status != "failed" || !strings.Contains(op.Error, "deadline") {
			t.Fatalf("an operation that outlasts the deadline: %+v", op)
		}
	}
	outlast := func(ctx context.Context, _ *opHandle) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(takes):
			return nil
		}
	}

	t.Run("restoring a copy", func(t *testing.T) {
		e, _, file := withSlow(t)
		s3 := map[string]any{"provider": "minio", "endpoint": "203.0.113.10:9000", "bucket": "worlds", "accessKeyId": "PKEXAMPLE"}
		if code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "admin", "enabled": true, "config": map[string]any{"type": "s3", "s3": s3}, "secretKey": "wJalrXUtnFEMI-example-secret"}); code != http.StatusOK {
			t.Fatalf("turn on: %d %v", code, out)
		}
		e.waitFor("the copy", func() bool { return e.countRows(`SELECT COUNT(*) FROM offsite_copies WHERE file_name = ?`, file) == 1 })
		shortDeadline(t)
		code, out := e.call("POST", e.sp("/offsite/restore"), map[string]any{"actor": "admin", "name": offsite.CopyName(file)})
		if code != http.StatusAccepted {
			t.Fatalf("restore: %d %v", code, out)
		}
		outlasts(t, e, out["id"].(string))
	})

	t.Run("restoring from a recovery key", func(t *testing.T) {
		e, dest, file := withSlow(t)
		dest.mu.Lock()
		dest.stored[offsite.CopyName(file)] = offsite.Copy{Name: offsite.CopyName(file), Archive: file, Size: 4096}
		dest.mu.Unlock()
		keys, err := offsite.NewKeys(e.a.now())
		if err != nil {
			t.Fatal(err)
		}
		rf, err := keys.RecoveryFileFor("Survival", "playkeeper/survival/", e.a.now())
		if err != nil {
			t.Fatal(err)
		}
		shortDeadline(t)
		code, out := e.call("POST", "/v1/offsite/recover/restore", map[string]any{"actor": "admin", "recoveryKey": rf.Content.Reveal(), "name": offsite.CopyName(file),
			"config":    map[string]any{"type": "s3", "s3": map[string]any{"provider": "b2", "endpoint": "s3.eu-central-003.backblazeb2.com", "bucket": "siya-minecraft", "accessKeyId": "003a8f91c2"}},
			"secretKey": "wJalrXUtnFEMI-example-secret"})
		if code != http.StatusAccepted {
			t.Fatalf("restore from the recovery key: %d %v", code, out)
		}
		outlasts(t, e, out["id"].(string))
	})

	t.Run("a backup", func(t *testing.T) {
		e := newAgentEnv(t)
		e.create()
		shortDeadline(t)
		op, err := e.srv().beginOp("backup", "admin", outlast)
		if err != nil {
			t.Fatal(err)
		}
		cutShort(t, e, op.ID)
	})

	t.Run("a machine operation", func(t *testing.T) {
		e := newAgentEnv(t)
		e.create()
		shortDeadline(t)
		op, err := e.a.beginMachineOp("disk-cleanup", "admin", outlast)
		if err != nil {
			t.Fatal(err)
		}
		cutShort(t, e, op.ID)
	})
}
