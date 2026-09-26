package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/offsite"
)

var testS3 = map[string]any{"provider": "minio", "endpoint": "203.0.113.10:9000", "bucket": "worlds", "accessKeyId": "PKEXAMPLE"}

// stoppedPart is what an earlier try of backup file's copy left at an S3
// destination.
func stoppedPart(file string) offsite.UploadState {
	return offsite.UploadState{Archive: file, Name: offsite.CopyName(file), Size: 1000,
		S3: &offsite.S3Upload{Key: "k", UploadID: "u1", PartSize: 5 << 20, Parts: []offsite.Part{{Number: 1, Size: 400}}}}
}

// Only a backup that is gone leaves the copy queue, and what its last try
// left at the destination is discarded with it. A backup whose record or
// archive can't be read just now stays queued with where its copy stopped:
// the try counts as failed, is logged, and is tried again, and once the
// backup can be read it is copied.
func TestOnlyABackupThatIsGoneLeavesTheCopyQueue(t *testing.T) {
	cases := []struct {
		name string
		// breaks runs as the uploader claims the backup's copy, and returns
		// what undoes it, or nil.
		breaks func(e *agentEnv, id, path string) (undo func())
		gone   bool
		// logged is in what the failed try logs and shows.
		logged string
	}{
		{name: "its record deleted", gone: true, breaks: func(e *agentEnv, id, _ string) func() {
			_, _ = e.a.db.Exec(`DELETE FROM backups WHERE id = ?`, id)
			return nil
		}},
		{name: "its record can't be read", logged: "the backup's record can't be read", breaks: func(e *agentEnv, _, _ string) func() {
			_, _ = e.a.db.Exec(`ALTER TABLE backups RENAME COLUMN sha256 TO sha256_gone`)
			return func() { _, _ = e.a.db.Exec(`ALTER TABLE backups RENAME COLUMN sha256_gone TO sha256`) }
		}},
		{name: "its archive deleted", gone: true, breaks: func(_ *agentEnv, _, path string) func() {
			_ = os.Remove(path)
			return nil
		}},
		// A link to itself can't be opened, even by root.
		{name: "its archive can't be opened", logged: "the backup's archive can't be opened", breaks: func(_ *agentEnv, _, path string) func() {
			_ = os.Rename(path, path+".away")
			_ = os.Symlink(filepath.Base(path), path)
			return func() {
				_ = os.Remove(path)
				_ = os.Rename(path+".away", path)
			}
		}},
	}
	for _, c := range cases {
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
			raw, _ := json.Marshal(stoppedPart(b.FileName))
			if _, err := e.a.db.Exec(`INSERT INTO offsite_uploads(server_id, backup_id, state, created_at) VALUES(?, ?, ?, ?)`, e.sid, id, string(raw), time.Now().UnixMilli()); err != nil {
				t.Fatal(err)
			}
			var once sync.Once
			undo := make(chan func(), 1)
			prevHook := uploadClaimed
			uploadClaimed = func(job uploadJob) {
				if job.backupID == id {
					once.Do(func() { undo <- c.breaks(e, id, e.a.backupPath(b.FileName)) })
				}
			}
			t.Cleanup(func() { uploadClaimed = prevHook })
			if code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "admin", "enabled": true, "config": map[string]any{"type": "s3", "s3": testS3}, "secretKey": "wJalrXUtnFEMI-example-secret"}); code != http.StatusOK {
				t.Fatalf("turn on: %d %v", code, out)
			}
			queued := func() int { return e.countRows(`SELECT COUNT(*) FROM offsite_uploads WHERE backup_id = ?`, id) }
			if c.gone {
				e.waitFor("the copy to leave the queue", func() bool { return queued() == 0 && len(dest.abortedStates()) > 0 })
				aborted := dest.abortedStates()
				if got, _ := json.Marshal(aborted[0]); len(aborted) != 1 || string(got) != string(raw) || dest.uploads() != 0 {
					t.Fatalf("discarded %d unfinished copies, the first %s, not %s; %d uploads", len(aborted), got, raw, dest.uploads())
				}
				return
			}
			e.waitFor("the failed try", func() bool {
				return e.countRows(`SELECT COUNT(*) FROM offsite_uploads WHERE backup_id = ? AND attempts = 1`, id) == 1
			})
			e.waitFor("the uploader to be done with the copy", func() bool {
				s := e.srv()
				s.auto.mu.Lock()
				defer s.auto.mu.Unlock()
				return s.auto.claim == nil
			})
			fix := <-undo
			later := time.Now().Add(4 * time.Minute).UnixMilli()
			if n := e.countRows(`SELECT COUNT(*) FROM offsite_uploads WHERE backup_id = ? AND state = ? AND next_attempt > ? AND last_error LIKE ?`, id, string(raw), later, c.logged+"%"); n != 1 {
				fix()
				t.Fatal("the backup that couldn't be read lost where its copy stopped, or its try doesn't wait or say why")
			}
			if len(dest.abortedStates()) != 0 || !strings.Contains(e.warnings.String(), c.logged) ||
				e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'offsite.copy_failed' AND target = ?`, id) != 1 {
				fix()
				t.Fatalf("discarded %d unfinished copies; logged:\n%s", len(dest.abortedStates()), e.warnings.String())
			}
			fix()
			if code, _ := e.call("POST", e.sp("/offsite/retry"), map[string]any{"actor": "admin"}); code != http.StatusOK {
				t.Fatalf("retry: %d", code)
			}
			e.waitFor("the copy", func() bool { return e.countRows(`SELECT COUNT(*) FROM offsite_copies WHERE backup_id = ?`, id) == 1 })
		})
	}
}

// A backup is queued for its copy even when whether copies are on can't be
// read, and the log says so. Once the settings can be read, the uploader
// copies it, or empties the queue when copies were never on.
func TestABackupIsQueuedWhenWhetherCopiesAreOnCantBeRead(t *testing.T) {
	for _, on := range []bool{true, false} {
		name := "copies on"
		if !on {
			name = "copies never turned on"
		}
		t.Run(name, func(t *testing.T) {
			dest := &fakeDest{stored: map[string]offsite.Copy{}}
			prev := openOffsite
			openOffsite = func(offsite.Config, offsite.Keys, offsite.Options) (offsiteDest, error) { return dest, nil }
			t.Cleanup(func() { openOffsite = prev })
			e := newAgentEnv(t)
			e.create()
			if code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "admin", "enabled": on, "config": map[string]any{"type": "s3", "s3": testS3}, "secretKey": "wJalrXUtnFEMI-example-secret"}); code != http.StatusOK {
				t.Fatalf("settings: %d %v", code, out)
			}
			if _, err := e.a.db.Exec(`ALTER TABLE offsite RENAME COLUMN enabled TO enabled_gone`); err != nil {
				t.Fatal(err)
			}
			id := e.backup()
			queued := e.countRows(`SELECT COUNT(*) FROM offsite_uploads WHERE backup_id = ?`, id) == 1
			if _, err := e.a.db.Exec(`ALTER TABLE offsite RENAME COLUMN enabled_gone TO enabled`); err != nil {
				t.Fatal(err)
			}
			if !queued || !strings.Contains(e.warnings.String(), "whether copies somewhere else are on can't be read") {
				t.Fatalf("queued: %v; logged:\n%s", queued, e.warnings.String())
			}
			e.srv().kickOffsite()
			if on {
				e.waitFor("the copy", func() bool { return e.countRows(`SELECT COUNT(*) FROM offsite_copies WHERE backup_id = ?`, id) == 1 })
				return
			}
			e.waitFor("the queue to empty", func() bool { return e.countRows(`SELECT COUNT(*) FROM offsite_uploads`) == 0 })
			if n := dest.uploads(); n != 0 {
				t.Fatalf("%d uploads with copies off", n)
			}
		})
	}
}

// staleDest records the unfinished uploads each AbortStale is told to keep.
type staleDest struct {
	fakeDest
	kept [][]*offsite.UploadState
}

func (d *staleDest) AbortStale(_ context.Context, _ time.Time, keep []*offsite.UploadState) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.kept = append(d.kept, keep)
	return 0, nil
}

func (d *staleDest) keeps() [][]*offsite.UploadState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]*offsite.UploadState(nil), d.kept...)
}

// AbortStale discards every unfinished upload a day old that isn't in the
// copy queue, and the spool files with them. A round that can't read the
// queue to the end, as can happen once after a restart, skips it, rather
// than discard a copy days into its upload that it couldn't see queued.
func TestUnfinishedUploadsAreCleanedUpOnlyFromAQueueThatWasRead(t *testing.T) {
	const earlier, later = "aaaaaaaaaaaaaaaa", "zzzzzzzzzzzzzzzz"
	exec := func(e *agentEnv, qs ...string) {
		e.t.Helper()
		for _, q := range qs {
			if _, err := e.a.db.Exec(q); err != nil {
				e.t.Fatal(err)
			}
		}
	}
	cases := []struct {
		name string
		// breaks makes reading the queue fail, and returns what undoes it.
		breaks func(e *agentEnv) (undo func())
		cleans bool
	}{
		{name: "the queue is read", cleans: true, breaks: func(*agentEnv) func() { return func() {} }},
		{name: "the queue can't be read", breaks: func(e *agentEnv) func() {
			exec(e, `ALTER TABLE offsite_uploads RENAME TO offsite_uploads_gone`)
			return func() { exec(e, `ALTER TABLE offsite_uploads_gone RENAME TO offsite_uploads`) }
		}},
		// The later upload's state fails to read once the earlier one's has
		// been read, as rows.Err reports.
		{name: "the queue fails part way", breaks: func(e *agentEnv) func() {
			exec(e, `ALTER TABLE offsite_uploads RENAME TO offsite_uploads_real`,
				`CREATE VIEW offsite_uploads AS SELECT server_id, backup_id,
					CASE WHEN backup_id = '`+later+`' THEN json('{') ELSE state END AS state,
					attempts, next_attempt, last_error, error_hint, error_kind, error_params, created_at FROM offsite_uploads_real`)
			return func() {
				exec(e, `DROP VIEW offsite_uploads`, `ALTER TABLE offsite_uploads_real RENAME TO offsite_uploads`)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dest := &staleDest{fakeDest: fakeDest{stored: map[string]offsite.Copy{}}}
			e, _, _ := withCopies(t, dest)
			// Two copies wait to be tried again, each stopped part way.
			states := map[string]string{}
			for _, id := range []string{earlier, later} {
				raw, _ := json.Marshal(stoppedPart(id + ".tar.gz"))
				states[id] = string(raw)
				if _, err := e.a.db.Exec(`INSERT INTO offsite_uploads(server_id, backup_id, state, next_attempt, created_at) VALUES(?, ?, ?, ?, ?)`,
					e.sid, id, string(raw), time.Now().Add(time.Hour).UnixMilli(), time.Now().UnixMilli()); err != nil {
					t.Fatal(err)
				}
			}
			before := len(dest.keeps())
			undo := c.breaks(e)
			e.srv().offsiteRound(context.Background(), nil, "", map[string]bool{})
			undo()
			calls := dest.keeps()[before:]
			if !c.cleans {
				if len(calls) != 0 {
					t.Fatalf("AbortStale ran %d times, keeping %d uploads", len(calls), len(calls[0]))
				}
				return
			}
			if len(calls) != 1 || len(calls[0]) != 2 {
				t.Fatalf("AbortStale calls: %d", len(calls))
			}
			for i, id := range []string{earlier, later} {
				if got, _ := json.Marshal(calls[0][i]); string(got) != states[id] {
					t.Fatalf("AbortStale kept %s, not %s", got, states[id])
				}
			}
		})
	}
}

// credStore is a storage service whose secret can change. An upload opened
// with another secret than the service takes is refused, as is the next one
// when failNext is set; an upload of an archive in hold waits until released
// or stopped.
type credStore struct {
	fakeDest
	secret   string
	used     []string
	failNext bool
	hold     map[string]chan struct{}
	waiting  chan string
	stopped  chan string
}

type credDest struct {
	*credStore
	secret string
}

func (d credDest) Upload(ctx context.Context, up offsite.Upload) (offsite.Copy, error) {
	d.mu.Lock()
	d.used = append(d.used, d.secret)
	hold := d.hold[up.Name]
	delete(d.hold, up.Name)
	d.mu.Unlock()
	if hold != nil {
		d.waiting <- up.Name
		select {
		case <-hold:
		case <-ctx.Done():
			d.stopped <- up.Name
			return offsite.Copy{}, ctx.Err()
		}
	}
	d.mu.Lock()
	refused := d.secret != d.credStore.secret || d.failNext
	d.failNext = false
	d.mu.Unlock()
	if refused {
		return offsite.Copy{}, &offsite.Error{Kind: offsite.KindWrongKeys, Msg: "The storage service doesn't accept these keys."}
	}
	return d.fakeDest.Upload(ctx, up)
}

func (d *credStore) secrets() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.used...)
}

// Saving new connection settings or secrets stops the upload still using
// the old ones: it isn't a failed try, and the next try uses the new ones
// right away. A try that fails once the settings were saved since it began
// doesn't count either, so the new settings are tried at once, not after the
// wait an earlier failure would set.
func TestNewCredentialsStopTheUploadStillUsingTheOldOnes(t *testing.T) {
	sftp := map[string]any{"host": "203.0.113.20", "port": 22, "user": "playkeeper", "folder": "backups/survival"}
	type change struct {
		settings map[string]any
		// secret is what the storage service takes from the change on.
		secret string
	}
	cases := []struct {
		name  string
		setup map[string]any
		old   string
		// change is saved while the copy is being made.
		change change
		// stops is whether the save stops the copy being made; failsOnce
		// makes the copy's try fail once it goes on.
		stops, failsOnce bool
	}{
		{name: "the S3 secret changed", old: "old-secret",
			setup:  map[string]any{"config": map[string]any{"type": "s3", "s3": testS3}, "secretKey": "old-secret"},
			change: change{settings: map[string]any{"secretKey": "new-secret"}, secret: "new-secret"}, stops: true},
		{name: "the SFTP password changed", old: "old password",
			setup:  map[string]any{"config": map[string]any{"type": "sftp", "sftp": sftp}, "sftpAuth": "password", "password": "old password"},
			change: change{settings: map[string]any{"password": "new password"}, secret: "new password"}, stops: true},
		{name: "nothing changed", old: "old-secret",
			setup:  map[string]any{"config": map[string]any{"type": "s3", "s3": testS3}, "secretKey": "old-secret"},
			change: change{settings: map[string]any{"enabled": true}, secret: "old-secret"}},
		{name: "the try fails after a save", old: "old-secret",
			setup:  map[string]any{"config": map[string]any{"type": "s3", "s3": testS3}, "secretKey": "old-secret"},
			change: change{settings: map[string]any{"enabled": true}, secret: "old-secret"}, failsOnce: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &credStore{fakeDest: fakeDest{stored: map[string]offsite.Copy{}}, secret: c.old, hold: map[string]chan struct{}{},
				waiting: make(chan string, 4), stopped: make(chan string, 4)}
			prev := openOffsite
			openOffsite = func(cfg offsite.Config, _ offsite.Keys, _ offsite.Options) (offsiteDest, error) {
				secret := cfg.S3.SecretKey.Reveal()
				if cfg.Type == offsite.TypeSFTP {
					secret = cfg.SFTP.Password.Reveal()
				}
				return credDest{credStore: store, secret: secret}, nil
			}
			t.Cleanup(func() { openOffsite = prev })
			e := newAgentEnv(t)
			e.create()
			id := e.backup()
			b, err := e.srv().getBackup(id)
			if err != nil {
				t.Fatal(err)
			}
			release := make(chan struct{})
			store.mu.Lock()
			store.hold[b.FileName] = release
			store.mu.Unlock()
			body := map[string]any{"actor": "admin", "enabled": true}
			for k, v := range c.setup {
				body[k] = v
			}
			if code, out := e.call("POST", e.sp("/offsite"), body); code != http.StatusOK {
				t.Fatalf("turn on: %d %v", code, out)
			}
			select {
			case <-store.waiting:
			case <-time.After(15 * time.Second):
				t.Fatal("the copy was never started")
			}

			store.mu.Lock()
			store.secret, store.failNext = c.change.secret, c.failsOnce
			store.mu.Unlock()
			c.change.settings["actor"] = "admin"
			if code, out := e.call("POST", e.sp("/offsite"), c.change.settings); code != http.StatusOK {
				t.Fatalf("the change: %d %v", code, out)
			}
			select {
			case <-store.stopped:
				if !c.stops {
					t.Fatal("a save that changed nothing the copy uses stopped it")
				}
			case <-time.After(2 * time.Second):
				if c.stops {
					close(release)
					t.Fatal("the save didn't stop the copy still using the old settings")
				}
				close(release)
			}
			e.waitFor("the copy, or a failed try", func() bool {
				return e.countRows(`SELECT COUNT(*) FROM offsite_copies WHERE backup_id = ?`, id) == 1 ||
					e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'offsite.copy_failed'`) > 0
			})
			if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'offsite.copy_failed'`); n != 0 {
				t.Fatalf("%d failed tries audited, the try with the new settings waits: %v", n, store.secrets())
			}
			want := []string{c.old, c.change.secret}
			if !c.stops && !c.failsOnce {
				want = want[:1]
			}
			if got := store.secrets(); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("uploads with %q, want %q", got, want)
			}
		})
	}
}

// A failed try counts from the queue's count, which a save of the settings
// may have reset since the claim, and waits the longer the more tries
// failed. A try claimed before the settings were last saved doesn't count.
func TestAFailedTryCountsFromTheQueue(t *testing.T) {
	e, _, _ := withCopies(t, &fakeDest{stored: map[string]offsite.Copy{}})
	s := e.srv()
	var saved int64
	if err := e.a.db.QueryRow(`SELECT updated_at FROM offsite WHERE server_id = ?`, e.sid).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		attempts int
		// claimed is when the try was claimed, from the settings' save.
		claimed time.Duration
		want    int
		wait    time.Duration
	}{
		{name: "the first try", attempts: 0, claimed: time.Second, want: 1, wait: 5 * time.Minute},
		{name: "the third try", attempts: 2, claimed: time.Second, want: 3, wait: 20 * time.Minute},
		{name: "the eighth try", attempts: 7, claimed: time.Second, want: 8, wait: 6 * time.Hour},
		{name: "the twentieth try", attempts: 19, claimed: time.Second, want: 20, wait: 6 * time.Hour},
		{name: "claimed before the settings were saved", attempts: 3, claimed: -time.Second, want: 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			const id = "failingfailingfa"
			far := time.Now().Add(24 * time.Hour).UnixMilli()
			if _, err := e.a.db.Exec(`INSERT OR REPLACE INTO offsite_uploads(server_id, backup_id, attempts, next_attempt, created_at) VALUES(?, ?, ?, ?, ?)`,
				e.sid, id, c.attempts, far, time.Now().UnixMilli()); err != nil {
				t.Fatal(err)
			}
			began := s.now().UnixMilli()
			s.uploadFailed(context.Background(), uploadJob{backupID: id, claimedAt: saved + c.claimed.Milliseconds()},
				&offsite.Error{Kind: offsite.KindNetwork, Msg: "Couldn't reach the storage.", Retry: true})
			var attempts int
			var next int64
			if err := e.a.db.QueryRow(`SELECT attempts, next_attempt FROM offsite_uploads WHERE backup_id = ?`, id).Scan(&attempts, &next); err != nil {
				t.Fatal(err)
			}
			if c.wait == 0 {
				if attempts != c.want || next != far {
					t.Fatalf("after a try claimed before the save: %d tries, next at %d, want %d and %d", attempts, next, c.want, far)
				}
				return
			}
			if wait := time.Duration(next-began) * time.Millisecond; attempts != c.want || wait < c.wait || wait > c.wait+5*time.Second {
				t.Fatalf("%d tries, the next in %s; want %d, in %s", attempts, wait, c.want, c.wait)
			}
		})
	}
}

// Settings saved while no upload runs, between two copies of a round, reach
// the next copy: the round, opened with the settings it read, uploads nothing
// more, and the next round copies with the new ones.
func TestARoundWhoseSettingsChangedUploadsNothingMore(t *testing.T) {
	store := &credStore{fakeDest: fakeDest{stored: map[string]offsite.Copy{}}, secret: "old-secret", hold: map[string]chan struct{}{},
		waiting: make(chan string, 4), stopped: make(chan string, 4)}
	prev := openOffsite
	openOffsite = func(cfg offsite.Config, _ offsite.Keys, _ offsite.Options) (offsiteDest, error) {
		return credDest{credStore: store, secret: cfg.S3.SecretKey.Reveal()}, nil
	}
	t.Cleanup(func() { openOffsite = prev })
	e := newAgentEnv(t)
	e.create()
	id := e.backup()
	// The new secret is saved as the copy is claimed, without the route
	// that would stop an upload, as a save between two copies is.
	var once sync.Once
	prevHook := uploadClaimed
	uploadClaimed = func(job uploadJob) {
		once.Do(func() {
			s := e.srv()
			row, err := s.loadOffsite()
			if err != nil {
				t.Error(err)
				return
			}
			store.mu.Lock()
			store.secret = "new-secret"
			store.mu.Unlock()
			row.secret = "new-secret"
			if err := s.saveOffsite(row); err != nil {
				t.Error(err)
			}
			s.kickOffsite()
		})
	}
	t.Cleanup(func() { uploadClaimed = prevHook })
	if code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "admin", "enabled": true, "config": map[string]any{"type": "s3", "s3": testS3}, "secretKey": "old-secret"}); code != http.StatusOK {
		t.Fatalf("turn on: %d %v", code, out)
	}
	e.waitFor("the copy", func() bool { return e.countRows(`SELECT COUNT(*) FROM offsite_copies WHERE backup_id = ?`, id) == 1 })
	if got := store.secrets(); strings.Join(got, ",") != "new-secret" {
		t.Fatalf("uploads with %q, want only the new secret", got)
	}
}

// Copies turned off while no upload runs, between two copies of a round,
// stop the next copy: nothing more is sent, and nothing is recorded.
func TestCopiesTurnedOffBetweenTwoCopiesStopTheNext(t *testing.T) {
	store := &credStore{fakeDest: fakeDest{stored: map[string]offsite.Copy{}}, secret: "the-secret", hold: map[string]chan struct{}{},
		waiting: make(chan string, 4), stopped: make(chan string, 4)}
	prev := openOffsite
	openOffsite = func(cfg offsite.Config, _ offsite.Keys, _ offsite.Options) (offsiteDest, error) {
		return credDest{credStore: store, secret: cfg.S3.SecretKey.Reveal()}, nil
	}
	t.Cleanup(func() { openOffsite = prev })
	e := newAgentEnv(t)
	e.create()
	e.backup()
	// Copies are turned off as the copy is claimed, without the route that
	// would stop an upload, as a save between two copies is.
	var once sync.Once
	claimed := make(chan struct{})
	prevHook := uploadClaimed
	uploadClaimed = func(job uploadJob) {
		once.Do(func() {
			defer close(claimed)
			s := e.srv()
			row, err := s.loadOffsite()
			if err != nil {
				t.Error(err)
				return
			}
			row.enabled = false
			if err := s.saveOffsite(row); err != nil {
				t.Error(err)
			}
			s.kickOffsite()
		})
	}
	t.Cleanup(func() { uploadClaimed = prevHook })
	if code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "admin", "enabled": true, "config": map[string]any{"type": "s3", "s3": testS3}, "secretKey": "the-secret"}); code != http.StatusOK {
		t.Fatalf("turn on: %d %v", code, out)
	}
	select {
	case <-claimed:
	case <-time.After(10 * time.Second):
		t.Fatal("no copy was claimed")
	}
	e.waitFor("the queue emptied", func() bool { return e.countRows(`SELECT COUNT(*) FROM offsite_uploads`) == 0 })
	if got := store.secrets(); len(got) != 0 {
		t.Fatalf("%d uploads after copies were turned off, want none", len(got))
	}
	if n := e.countRows(`SELECT COUNT(*) FROM offsite_copies`); n != 0 {
		t.Fatalf("%d copies recorded after copies were turned off, want none", n)
	}
}
