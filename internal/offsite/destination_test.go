package offsite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// open returns a destination on the fake with small parts, no waiting
// between attempts and a spool folder of its own; mod may change the
// options.
func (f *fakeS3) open(keys Keys, mod func(*Options)) *Destination {
	f.t.Helper()
	opts := Options{SpoolDir: f.t.TempDir(), HTTPClient: f.httpClient()}
	if mod != nil {
		mod(&opts)
	}
	d, err := Open(Config{Type: TypeS3, S3: testConfig()}, keys, opts)
	if err != nil {
		f.t.Fatalf("Open: %v", err)
	}
	quick(d.b.(*s3Client))
	return d
}

// change replaces what the fake stores under key.
func (f *fakeS3) change(key string, fn func([]byte) []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.objects[key]
	o.data = fn(bytes.Clone(o.data))
}

func (f *fakeS3) unfinished() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.uploads)
}

func mustUpload(t *testing.T, d *Destination, file *testFile, name string) Copy {
	t.Helper()
	cp, err := d.Upload(context.Background(), file.upload(name))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	return cp
}

// restore asks for the backup in cp, with its record, in dir.
func restore(cp Copy, dir string) Download {
	return Download{Name: cp.Name, Size: cp.Size, SHA256: cp.SHA256, Recipient: cp.Recipient, ArchiveSHA256: cp.ArchiveSHA256, Dir: dir}
}

func sum256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// interruptedUpload starts copying a 300 KiB backup whose third part never
// arrives, and returns the state to resume it.
func interruptedUpload(t *testing.T, f *fakeS3, d *Destination, file *testFile) *UploadState {
	t.Helper()
	f.fail = failPart("3")
	_, err := d.Upload(context.Background(), file.upload(testName))
	f.fail = nil
	e := wantKind(t, err, KindNetwork)
	if !e.Retry || e.Resume == nil || e.Resume.S3 == nil || len(e.Resume.S3.Parts) != 2 {
		t.Fatalf("error %+v with resume %+v", e, e.Resume)
	}
	return e.Resume
}

func TestEncryptedRoundTrip(t *testing.T) {
	for _, size := range []int{1, 1000, ageChunk, ageChunk + 1, 300 << 10} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			ctx := context.Background()
			f := newFake(t)
			keys := newKeys(t)
			d := f.open(keys, nil)
			file := newTestFile(size, uint64(size))
			up := file.upload(testName)
			up.SHA256 = strings.ToUpper(up.SHA256)
			var last Progress
			up.Progress = func(p Progress) { last = p }
			cp, err := d.Upload(ctx, up)
			if err != nil {
				t.Fatal(err)
			}

			key := testPrefix + testCopy
			stored := f.object(key).data
			if cp.Name != testCopy || cp.Archive != testName || cp.ArchiveSHA256 != file.sha() || cp.Recipient != keys.Current.Recipient ||
				cp.Key != key || cp.Size != int64(len(stored)) || cp.SHA256 != sum256(stored) || cp.Checked != CheckedSHA256 || !cp.Uploaded {
				t.Errorf("copy %+v", cp)
			}
			if !bytes.HasPrefix(stored, []byte("age-encryption.org/v1\n-> X25519 ")) || int64(len(stored)) > sealedSize(int64(size)) {
				t.Errorf("stored %d bytes that aren't an age file of at most %d bytes", len(stored), sealedSize(int64(size)))
			}
			if size >= 32 && bytes.Contains(stored, file.data[:32]) {
				t.Error("the bucket holds the backup unencrypted")
			}
			if back, err := openWith(t, stored, keys.Current); err != nil || !bytes.Equal(back, file.data) {
				t.Fatalf("the age library doesn't open the copy: %v", err)
			}
			if last.Sent != cp.Size || last.Total != cp.Size {
				t.Errorf("last progress %+v", last)
			}
			if names := dirNames(t, d.spool); len(names) != 0 {
				t.Errorf("the spool folder still holds %q", names)
			}

			dir := t.TempDir()
			a, err := d.Download(ctx, restore(cp, dir))
			if err != nil {
				t.Fatal(err)
			}
			if a.Name != testName || a.Path != filepath.Join(dir, testName) || a.Size != int64(size) || a.SHA256 != file.sha() ||
				a.Recipient != keys.Current.Recipient || !a.Matched {
				t.Errorf("archive %+v", a)
			}
			if got, err := os.ReadFile(a.Path); err != nil || !bytes.Equal(got, file.data) {
				t.Fatal("the restored backup differs")
			}
			fi, err := os.Stat(a.Path)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("the restored backup has mode %v", fi.Mode())
			}
			if names := dirNames(t, dir); !slices.Equal(names, []string{testName}) {
				t.Errorf("the folder holds %q", names)
			}
		})
	}
}

func TestDestinationListVerifyDelete(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	d := f.open(newKeys(t), nil)
	cp := mustUpload(t, d, newTestFile(5<<10, 2), testName)

	objs, err := d.List(ctx)
	if err != nil || len(objs) != 1 || objs[0].Name != testCopy || objs[0].Archive != testName || objs[0].Size != cp.Size {
		t.Fatalf("List = %+v, %v", objs, err)
	}
	if v, err := d.Verify(ctx, cp); err != nil || v.Checked != CheckedSHA256 {
		t.Fatalf("Verify = %+v, %v", v, err)
	}
	for _, name := range []string{testName, "../" + testCopy, testPrefix + testCopy, ""} {
		wantKind(t, d.Delete(ctx, name), KindUnexpected)
	}
	for range 2 {
		if err := d.Delete(ctx, testCopy); err != nil {
			t.Fatal(err)
		}
	}
	if f.object(cp.Key) != nil {
		t.Error("the copy is still there")
	}
}

func TestUploadRefusesBadInput(t *testing.T) {
	f := newFake(t)
	d := f.open(newKeys(t), nil)
	file := newTestFile(1<<10, 3)
	huge := file.upload(testName)
	huge.Size = 5 << 40
	for _, tc := range []struct {
		name  string
		up    Upload
		kind  Kind
		field string
	}{
		{"a path", Upload{Name: "../" + testName, File: file, Size: 1 << 10, SHA256: file.sha()}, KindInvalidConfig, "name"},
		{"not a .tar.gz", Upload{Name: "world.zip", File: file, Size: 1 << 10, SHA256: file.sha()}, KindInvalidConfig, "name"},
		{"a copy's name", Upload{Name: testCopy, File: file, Size: 1 << 10, SHA256: file.sha()}, KindInvalidConfig, "name"},
		{"no file", Upload{Name: testName, Size: 1 << 10, SHA256: file.sha()}, KindUnexpected, ""},
		{"empty", Upload{Name: testName, File: file, SHA256: file.sha()}, KindUnexpected, ""},
		{"no checksum", Upload{Name: testName, File: file, Size: 1 << 10}, KindUnexpected, ""},
		{"not a checksum", Upload{Name: testName, File: file, Size: 1 << 10, SHA256: "not hex"}, KindUnexpected, ""},
		{"too large once encrypted", huge, KindTooLarge, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Upload(context.Background(), tc.up)
			if e := wantKind(t, err, tc.kind); e.Field != tc.field || e.Msg == "" {
				t.Errorf("error %+v", e)
			}
		})
	}
	if file.total != 0 || len(f.ops()) != 0 || len(dirNames(t, d.spool)) != 0 {
		t.Errorf("read %d bytes, sent %q, spooled %q", file.total, f.ops(), dirNames(t, d.spool))
	}
}

func TestUploadRefusesChangedBackup(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  func(file *testFile, up *Upload)
	}{
		{"another checksum", func(_ *testFile, up *Upload) { up.SHA256 = newTestFile(8<<10, 5).sha() }},
		{"longer than recorded", func(_ *testFile, up *Upload) { up.Size-- }},
		{"shorter than recorded", func(_ *testFile, up *Upload) { up.Size++ }},
		{"changing while encrypted", func(file *testFile, _ *Upload) { file.changeAfter = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			d := f.open(newKeys(t), nil)
			file := newTestFile(600<<10, 4)
			up := file.upload(testName)
			tc.mod(file, &up)
			_, err := d.Upload(context.Background(), up)
			e := wantKind(t, err, KindLocalChanged)
			if want := "The backup file " + testName + " doesn't match the checksum recorded when it was made, so it wasn't copied."; e.Msg != want {
				t.Errorf("message %q", e.Msg)
			}
			if len(f.ops()) != 0 || len(dirNames(t, d.spool)) != 0 {
				t.Errorf("sent %q, spooled %q", f.ops(), dirNames(t, d.spool))
			}
		})
	}
}

func TestUploadNeedsSpaceToEncrypt(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	var asked []string
	d := f.open(newKeys(t), func(o *Options) {
		o.DiskFree = func(dir string) (int64, error) {
			asked = append(asked, dir)
			return 100 << 20, nil
		}
	})
	file := newTestFile(10<<10, 6)
	_, err := d.Upload(ctx, file.upload(testName))
	e := wantKind(t, err, KindNotEnoughSpace)
	need := sealedSize(10<<10) + spoolReserve
	if e.Need != need || e.Free != 100<<20 || e.Params()["need"] != "537 MB" || e.Params()["free"] != "105 MB" ||
		e.Msg != "Encrypting "+testName+" needs 537 MB free on this machine, and 105 MB is free." {
		t.Errorf("error %+v", e)
	}
	if file.total != 0 || len(f.ops()) != 0 || len(dirNames(t, d.spool)) != 0 || !slices.Equal(asked, []string{d.spool}) {
		t.Errorf("read %d bytes, sent %q, asked about %q", file.total, f.ops(), asked)
	}

	d.diskFree = func(string) (int64, error) { return 0, errors.ErrUnsupported }
	if _, err := d.Upload(ctx, file.upload(testName)); err != nil {
		t.Errorf("an upload where free space can't be measured failed: %v", err)
	}
}

func TestUploadFailureDiscardsEncryptedCopy(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
		deny string
	}{
		{"single request", 1 << 10, "PutObject"},
		{"multipart", 200 << 10, "CreateMultipartUpload"},
		{"a part", 200 << 10, "UploadPart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.deny[tc.deny] = true
			d := f.open(newKeys(t), nil)
			_, err := d.Upload(context.Background(), newTestFile(tc.size, 7).upload(testName))
			if e := wantKind(t, err, KindPermission); e.Resume != nil {
				t.Error("a refused upload can be resumed")
			}
			if len(dirNames(t, d.spool)) != 0 || f.unfinished() != 0 {
				t.Errorf("left %q in the spool folder and %d uploads", dirNames(t, d.spool), f.unfinished())
			}
		})
	}
}

func TestUploadResumesEncryptedCopy(t *testing.T) {
	for _, how := range []string{"after a network failure", "after Playkeeper stopped"} {
		t.Run(how, func(t *testing.T) {
			ctx := context.Background()
			f := newFake(t)
			keys := newKeys(t)
			d := f.open(keys, nil)
			file := newTestFile(300<<10, 8)
			var state *UploadState
			if how == "after a network failure" {
				state = interruptedUpload(t, f, d, file)
			} else {
				sctx, cancel := context.WithCancel(ctx)
				up := file.upload(testName)
				up.Progress = func(p Progress) {
					if p.State != nil && p.State.S3 != nil && len(p.State.S3.Parts) == 2 {
						saved, err := json.Marshal(p.State)
						if err != nil {
							t.Fatal(err)
						}
						state = &UploadState{}
						if err := json.Unmarshal(saved, state); err != nil {
							t.Fatal(err)
						}
						cancel()
					}
				}
				_, err := d.Upload(sctx, up)
				wantKind(t, err, KindCanceled)
			}
			if state == nil || state.Archive != testName || state.ArchiveSHA256 != file.sha() || state.Name != testCopy ||
				state.Recipient != keys.Current.Recipient || state.StartedAt.IsZero() || state.S3 == nil || len(state.S3.Parts) != 2 {
				t.Fatalf("state %+v", state)
			}
			if names := dirNames(t, d.spool); !slices.Equal(names, []string{state.Spool}) {
				t.Fatalf("the spool folder holds %q, want the encrypted copy %q", names, state.Spool)
			}

			read, before := file.total, len(f.ops())
			up := file.upload(testName)
			up.Resume = state
			cp, err := d.Upload(ctx, up)
			if err != nil {
				t.Fatal(err)
			}
			after := f.ops()[before:]
			if countOps(after, "UploadPart") != 3 || countOps(after, "CreateMultipartUpload") != 0 {
				t.Errorf("resuming sent %q", after)
			}
			if file.total != read {
				t.Error("resuming read and encrypted the backup again")
			}
			if !cp.Uploaded || cp.Size != state.Size || cp.SHA256 != state.SHA256 || cp.Recipient != keys.Current.Recipient || cp.ArchiveSHA256 != file.sha() {
				t.Errorf("copy %+v", cp)
			}
			if back, err := openWith(t, f.object(cp.Key).data, keys.Current); err != nil || !bytes.Equal(back, file.data) {
				t.Errorf("the resumed copy doesn't open to the backup: %v", err)
			}
			if names := dirNames(t, d.spool); len(names) != 0 {
				t.Errorf("the spool folder still holds %q", names)
			}
		})
	}
}

func TestUploadStartsOverWithoutUsableEncryptedCopy(t *testing.T) {
	spoolPath := func(d *Destination, st *UploadState) string { return filepath.Join(d.spool, st.Spool) }
	for _, tc := range []struct {
		name string
		// change returns the destination to resume with.
		change func(t *testing.T, d *Destination, st *UploadState, keys Keys) (*Destination, Keys)
	}{
		{"encrypted copy gone", func(t *testing.T, d *Destination, st *UploadState, keys Keys) (*Destination, Keys) {
			if err := os.Remove(spoolPath(d, st)); err != nil {
				t.Fatal(err)
			}
			return d, keys
		}},
		{"encrypted copy cut short", func(t *testing.T, d *Destination, st *UploadState, keys Keys) (*Destination, Keys) {
			if err := os.Truncate(spoolPath(d, st), st.Size-1); err != nil {
				t.Fatal(err)
			}
			return d, keys
		}},
		{"key rotated", func(t *testing.T, d *Destination, st *UploadState, keys Keys) (*Destination, Keys) {
			rotated, _, err := keys.Rotate(keyTime.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			d2, err := Open(Config{Type: TypeS3, S3: testConfig()}, rotated, Options{SpoolDir: d.spool, HTTPClient: d.b.(*s3Client).hc})
			if err != nil {
				t.Fatal(err)
			}
			quick(d2.b.(*s3Client))
			return d2, rotated
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			keys := newKeys(t)
			d := f.open(keys, nil)
			file := newTestFile(300<<10, 9)
			st := interruptedUpload(t, f, d, file)
			d, keys = tc.change(t, d, st, keys)

			up := file.upload(testName)
			up.Resume = st
			cp, err := d.Upload(context.Background(), up)
			if err != nil {
				t.Fatal(err)
			}
			if f.count("CreateMultipartUpload") != 2 || f.count("AbortMultipartUpload") != 1 || f.unfinished() != 0 {
				t.Errorf("sent %q", f.ops())
			}
			if file.total != 2*int64(len(file.data)) {
				t.Errorf("read %d bytes of the backup, want it encrypted again", file.total)
			}
			if cp.Recipient != keys.Current.Recipient || !cp.Uploaded {
				t.Errorf("copy %+v", cp)
			}
			if back, err := openWith(t, f.object(cp.Key).data, keys.Current); err != nil || !bytes.Equal(back, file.data) {
				t.Errorf("the copy doesn't open to the backup: %v", err)
			}
			if names := dirNames(t, d.spool); len(names) != 0 {
				t.Errorf("the spool folder still holds %q", names)
			}
		})
	}

	t.Run("encrypted copy changed", func(t *testing.T) {
		f := newFake(t)
		d := f.open(newKeys(t), nil)
		file := newTestFile(300<<10, 10)
		st := interruptedUpload(t, f, d, file)
		data, err := os.ReadFile(spoolPath(d, st))
		if err != nil {
			t.Fatal(err)
		}
		data[len(data)-100] ^= 1
		if err := os.WriteFile(spoolPath(d, st), data, 0o600); err != nil {
			t.Fatal(err)
		}
		up := file.upload(testName)
		up.Resume = st
		_, err = d.Upload(context.Background(), up)
		e := wantKind(t, err, KindLocalChanged)
		if !strings.Contains(e.Msg, "prepared on this machine doesn't match its checksum") || e.Resume != nil {
			t.Errorf("error %+v", e)
		}
		if f.unfinished() != 0 || f.object(testPrefix+testCopy) != nil || len(dirNames(t, d.spool)) != 0 {
			t.Errorf("left %d uploads and %q in the spool folder", f.unfinished(), dirNames(t, d.spool))
		}
	})
}

func TestAbortDiscardsUploadAndEncryptedCopy(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	d := f.open(newKeys(t), nil)
	st := interruptedUpload(t, f, d, newTestFile(300<<10, 11))

	beside := filepath.Join(filepath.Dir(d.spool), spoolPrefix+"0123456789abcdef.age")
	if err := os.WriteFile(beside, []byte("not Playkeeper's"), 0o600); err != nil {
		t.Fatal(err)
	}
	crafted := st.clone()
	crafted.Spool, crafted.S3 = "../"+filepath.Base(beside), nil
	if err := d.Abort(ctx, crafted); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(beside); err != nil {
		t.Errorf("Abort deleted a file outside the spool folder: %v", err)
	}

	for range 2 {
		if err := d.Abort(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Abort(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if f.unfinished() != 0 || len(dirNames(t, d.spool)) != 0 {
		t.Errorf("left %d uploads and %q in the spool folder", f.unfinished(), dirNames(t, d.spool))
	}
}

func TestUploadAdoptsEarlierCopy(t *testing.T) {
	for _, size := range []int{40 << 10, 200 << 10} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			ctx := context.Background()
			f := newFake(t)
			keys := newKeys(t)
			d := f.open(keys, nil)
			file := newTestFile(size, 12)
			first := mustUpload(t, d, file, testName)
			stored := bytes.Clone(f.object(first.Key).data)

			rotated, _, err := keys.Rotate(keyTime.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			for _, dest := range []*Destination{d, f.open(rotated, nil)} {
				before := len(f.ops())
				cp, err := dest.Upload(ctx, file.upload(testName))
				if err != nil {
					t.Fatal(err)
				}
				after := f.ops()[before:]
				if cp.Uploaded || cp.Checked != CheckedDecrypted || cp.Name != testCopy || cp.Key != first.Key || cp.Size != first.Size ||
					cp.SHA256 != first.SHA256 || cp.Recipient != keys.Current.Recipient || cp.Archive != testName ||
					cp.ArchiveSHA256 != file.sha() || cp.VerifiedAt.IsZero() {
					t.Errorf("copy %+v, first %+v", cp, first)
				}
				if countOps(after, "PutObject")+countOps(after, "CreateMultipartUpload")+countOps(after, "UploadPart") != 0 || countOps(after, "GetObject") != 1 {
					t.Errorf("sent %q", after)
				}
				if names := dirNames(t, dest.spool); len(names) != 0 {
					t.Errorf("the spool folder still holds %q", names)
				}
			}
			if !bytes.Equal(f.object(first.Key).data, stored) {
				t.Error("the copy was changed")
			}
		})
	}
}

func TestUploadNeverOverwritesAnotherFile(t *testing.T) {
	other := newKeys(t)
	file := newTestFile(20<<10, 13)
	for _, tc := range []struct {
		name   string
		stored func(own Keys) []byte
		msg    string
	}{
		{"a copy of another backup", func(own Keys) []byte { return sealWith(t, newTestFile(20<<10, 14).data, own.Current.Recipient) },
			"A copy of a different backup named " + testCopy + " is already in the bucket."},
		{"a copy made with another key", func(Keys) []byte { return sealWith(t, file.data, other.Current.Recipient) },
			"A copy named " + testCopy + " is already in the bucket, made with an encryption key this server doesn't have."},
		{"not a copy", func(Keys) []byte { return []byte("some other program's file") },
			"A file named " + testCopy + " is already in the bucket, and it is damaged or isn't a Playkeeper copy."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			keys := newKeys(t)
			d := f.open(keys, nil)
			stored := tc.stored(keys)
			f.put(testPrefix+testCopy, stored)
			_, err := d.Upload(context.Background(), file.upload(testName))
			e := wantKind(t, err, KindConflict)
			if e.Msg != tc.msg || !strings.Contains(e.Hint, "never overwrites") {
				t.Errorf("error %q, %q", e.Msg, e.Hint)
			}
			if !bytes.Equal(f.object(testPrefix+testCopy).data, stored) || f.count("PutObject") != 0 || f.unfinished() != 0 {
				t.Errorf("the file was touched: %q", f.ops())
			}
			if names := dirNames(t, d.spool); len(names) != 0 {
				t.Errorf("the spool folder still holds %q", names)
			}
		})
	}
}

func TestDownloadRefusals(t *testing.T) {
	other := newKeys(t)
	file := newTestFile(20<<10, 15)
	flip := func(at func([]byte) int) func([]byte) []byte {
		return func(b []byte) []byte {
			i := at(b)
			if b[i] == 'A' {
				b[i] = 'B'
			} else {
				b[i] = 'A'
			}
			return b
		}
	}
	wrappedKey := func(b []byte) int {
		lines := bytes.SplitN(b, []byte("\n"), 3)
		return len(lines[0]) + len(lines[1]) + 2
	}
	headerMAC := func(b []byte) int { return bytes.Index(b, []byte("\n--- ")) + 5 }
	payload := func(b []byte) int { return len(b) - 20 }
	cut := func(b []byte) []byte { return b[:len(b)-10] }
	longer := func(b []byte) []byte { return append(b, "more"...) }
	noRecord := func(dl *Download, stored int64) { *dl = Download{Name: dl.Name, Size: stored, Dir: dl.Dir} }
	const (
		damaged    = "The copy " + testCopy + " in the bucket is damaged or was changed after it was uploaded, so it can't be opened."
		otherKey   = "None of this server's encryption keys opens the copy " + testCopy + "."
		unmatched  = "The copy " + testCopy + " in the bucket doesn't match its record ("
		notACopy   = "That is not the name of a backup's copy."
		noSizeOrCS = "The record of the copy " + testCopy + " has no usable size or checksum."
	)
	for _, tc := range []struct {
		name     string
		other    bool                // restore with another server's keys
		change   func([]byte) []byte // what happens to the copy in the bucket
		dl       func(dl *Download, stored int64)
		noLength bool
		exists   bool
		free     int64
		kind     Kind
		msg      string
	}{
		{name: "another server's keys", other: true, kind: KindKeyMismatch, msg: otherKey},
		{name: "another server's keys, without a record", other: true, dl: noRecord, kind: KindKeyMismatch, msg: otherKey},
		{name: "replaced by a copy for another key", change: func([]byte) []byte { return sealWith(t, file.data, other.Current.Recipient) },
			kind: KindVerifyFailed, msg: damaged},
		{name: "encrypted data changed", change: flip(payload), kind: KindVerifyFailed, msg: damaged},
		{name: "encrypted data changed, without a record", change: flip(payload), dl: noRecord, kind: KindVerifyFailed, msg: damaged},
		{name: "wrapped file key changed", change: flip(wrappedKey), kind: KindVerifyFailed, msg: damaged},
		{name: "wrapped file key changed, without a record", change: flip(wrappedKey), dl: noRecord, kind: KindKeyMismatch, msg: otherKey},
		{name: "header MAC changed", change: flip(headerMAC), kind: KindVerifyFailed, msg: damaged},
		{name: "header MAC changed, without a record", change: flip(headerMAC), dl: noRecord, kind: KindVerifyFailed, msg: damaged},
		{name: "cut short", change: cut, kind: KindVerifyFailed, msg: unmatched + "it is "},
		{name: "cut short, without a record", change: cut, dl: noRecord, kind: KindVerifyFailed, msg: damaged},
		{name: "longer", change: longer, kind: KindVerifyFailed, msg: unmatched + "it is "},
		{name: "longer, without a record", change: longer, dl: noRecord, kind: KindVerifyFailed, msg: damaged},
		{name: "longer, without a length", change: longer, noLength: true, kind: KindVerifyFailed, msg: unmatched + "it is larger than its record says)."},
		{name: "another checksum on record", dl: func(dl *Download, _ int64) { dl.SHA256 = sum256(nil) }, kind: KindVerifyFailed,
			msg: unmatched + "its SHA-256 differs)."},
		{name: "another backup on record", dl: func(dl *Download, _ int64) { dl.ArchiveSHA256 = sum256(nil) }, kind: KindVerifyFailed,
			msg: "The backup inside the copy " + testCopy + " doesn't match the backup's record, so it wasn't restored."},
		{name: "not in the bucket", dl: func(dl *Download, _ int64) { dl.Name = CopyName(otherName) }, kind: KindNotFound,
			msg: "The copy " + CopyName(otherName) + " is not in the bucket."},
		{name: "already restored", exists: true, kind: KindConflict, msg: "The backup " + testName + " is already on this machine."},
		{name: "no room", free: 1 << 20, kind: KindNotEnoughSpace, msg: "Restoring " + testName + " needs "},
		{name: "a backup's name", dl: func(dl *Download, _ int64) { dl.Name = testName }, kind: KindUnexpected, msg: notACopy},
		{name: "a path", dl: func(dl *Download, _ int64) { dl.Name = "../" + testCopy }, kind: KindUnexpected, msg: notACopy},
		{name: "no size", dl: func(dl *Download, _ int64) { dl.Size = 0 }, kind: KindUnexpected, msg: noSizeOrCS},
		{name: "a broken checksum", dl: func(dl *Download, _ int64) { dl.SHA256 = "abc" }, kind: KindUnexpected, msg: noSizeOrCS},
		{name: "a relative folder", dl: func(dl *Download, _ int64) { dl.Dir = "restore" }, kind: KindUnexpected,
			msg: "Playkeeper needs an absolute folder to restore the backup into."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			own := newKeys(t)
			cp := mustUpload(t, f.open(own, nil), file, testName)
			if tc.change != nil {
				f.change(cp.Key, tc.change)
			}
			f.mu.Lock()
			f.noLength = tc.noLength
			f.mu.Unlock()
			keys := own
			if tc.other {
				keys = other
			}
			d := f.open(keys, func(o *Options) {
				if tc.free > 0 {
					o.DiskFree = func(string) (int64, error) { return tc.free, nil }
				}
			})
			dir := t.TempDir()
			var want []string
			if tc.exists {
				want = []string{testName}
				if err := os.WriteFile(filepath.Join(dir, testName), []byte("mine"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			dl := restore(cp, dir)
			if tc.dl != nil {
				tc.dl(&dl, int64(len(f.object(cp.Key).data)))
			}
			_, err := d.Download(context.Background(), dl)
			e := wantKind(t, err, tc.kind)
			if !strings.HasPrefix(e.Msg, tc.msg) || e.Hint == "" && tc.kind != KindUnexpected {
				t.Errorf("error %q (%q), want %q", e.Msg, e.Hint, tc.msg)
			}
			if strings.Contains(e.Msg+e.Hint, "AGE-SECRET-KEY") {
				t.Error("the error shows a key")
			}
			if names := dirNames(t, dir); !slices.Equal(names, want) {
				t.Errorf("the folder holds %q", names)
			}
			if tc.exists {
				if got, _ := os.ReadFile(filepath.Join(dir, testName)); string(got) != "mine" {
					t.Error("the file already there was replaced")
				}
			}
		})
	}
}

func TestDownloadRetriesBrokenTransfer(t *testing.T) {
	f := newFake(t)
	d := f.open(newKeys(t), nil)
	file := newTestFile(200<<10, 16)
	cp := mustUpload(t, d, file, testName)
	broke := false
	f.fail = func(op string, _ *http.Request) *fakeFailure {
		if op == "GetObject" && !broke {
			broke = true
			return &fakeFailure{half: true}
		}
		return nil
	}
	dir := t.TempDir()
	a, err := d.Download(context.Background(), restore(cp, dir))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(a.Path)
	if err != nil || !bytes.Equal(got, file.data) || !broke || f.count("GetObject") != 2 || !a.Matched {
		t.Errorf("restored %d bytes after %d downloads", len(got), f.count("GetObject"))
	}
	if names := dirNames(t, dir); !slices.Equal(names, []string{testName}) {
		t.Errorf("the folder holds %q", names)
	}
}

func TestRotatedKeysOpenOlderCopies(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	first := newKeys(t)
	older, newer := newTestFile(3<<10, 17), newTestFile(5<<10, 18)
	oldCopy := mustUpload(t, f.open(first, nil), older, testName)
	keys, _, err := first.Rotate(keyTime.Add(24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	d := f.open(keys, nil)
	newCopy := mustUpload(t, d, newer, otherName)
	if oldCopy.Recipient != first.Current.Recipient || newCopy.Recipient != keys.Current.Recipient || newCopy.Recipient == oldCopy.Recipient {
		t.Fatalf("recipients %s, %s", oldCopy.Recipient, newCopy.Recipient)
	}

	for _, tc := range []struct {
		cp   Copy
		file *testFile
	}{{oldCopy, older}, {newCopy, newer}} {
		a, err := d.Download(ctx, restore(tc.cp, t.TempDir()))
		if err != nil {
			t.Fatalf("restoring %s with the rotated keys: %v", tc.cp.Name, err)
		}
		if got, _ := os.ReadFile(a.Path); !bytes.Equal(got, tc.file.data) || a.Recipient != tc.cp.Recipient || !a.Matched {
			t.Errorf("restored %+v", a)
		}
	}
	_, err = f.open(first, nil).Download(ctx, restore(newCopy, t.TempDir()))
	wantKind(t, err, KindKeyMismatch)

	// A new machine with only the recovery key file and the bucket's
	// settings, and a service that doesn't send lengths.
	rf, err := keys.RecoveryFile("Survival", keyTime.Add(25*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := ParseRecoveryFile(strings.NewReader(rf.Content.Reveal()))
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.noLength = true
	f.mu.Unlock()
	fresh := f.open(recovered, nil)
	objs, err := fresh.List(ctx)
	if err != nil || len(objs) != 2 {
		t.Fatalf("List = %+v, %v", objs, err)
	}
	files := map[string]*testFile{testName: older, otherName: newer}
	for _, o := range objs {
		a, err := fresh.Download(ctx, Download{Name: o.Name, Size: o.Size, Dir: t.TempDir()})
		if err != nil {
			t.Fatalf("restoring %s from the recovery key: %v", o.Name, err)
		}
		if got, _ := os.ReadFile(a.Path); !bytes.Equal(got, files[o.Archive].data) || a.Matched || a.SHA256 != files[o.Archive].sha() {
			t.Errorf("restored %+v", a)
		}
	}
}

func TestConnectionTestEncryptsItsFile(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	keys := newKeys(t)
	d := f.open(keys, nil)
	if res := d.Test(ctx); !res.OK || len(res.Checks) != 5 {
		t.Fatalf("test %+v", res)
	}

	f.deny["DeleteObject"] = true
	res := d.Test(ctx)
	if last := res.Checks[len(res.Checks)-1]; res.OK || last.Step != "delete" || last.Kind != KindPermission {
		t.Fatalf("test %+v", res)
	}
	var left []byte
	f.mu.Lock()
	for k, o := range f.objects {
		if name, ok := strings.CutPrefix(k, testPrefix); ok && isProbeName(name) {
			left = o.data
		}
	}
	f.mu.Unlock()
	if bytes.Contains(left, []byte("connection test")) {
		t.Error("the test file is stored unencrypted")
	}
	if back, err := openWith(t, left, keys.Current); err != nil || string(back) != probeText {
		t.Errorf("the test file left behind opens to %q, %v", back, err)
	}
}

func TestAbortStaleCleansSpoolFolder(t *testing.T) {
	f := newFake(t)
	d := f.open(newKeys(t), nil)
	old, cutoff := time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	spoolName := func(suffix string) string { return spoolPrefix + newSpoolID() + suffix }
	oldSealed, oldPartial, recent, kept, folder := spoolName(".age"), spoolName(partialSuffix), spoolName(".age"), spoolName(".age"), spoolName(".age")
	others := []string{"notes.txt", spoolPrefix + "not-hex-at-all!.age", spoolName(".tmp")}
	for _, name := range append([]string{oldSealed, oldPartial, recent, kept}, others...) {
		path := filepath.Join(d.spool, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if name != recent {
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Mkdir(filepath.Join(d.spool, folder), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(d.spool, folder), old, old); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.uploads["stale"] = &fakeUpload{key: testPrefix + CopyName(otherName), parts: map[int]fakePart{}, initiated: old}
	f.uploads["running"] = &fakeUpload{key: testPrefix + testCopy, parts: map[int]fakePart{}, initiated: old}
	f.mu.Unlock()

	n, err := d.AbortStale(context.Background(), cutoff, []*UploadState{{Spool: kept, S3: &S3Upload{UploadID: "running"}}, nil})
	if err != nil || n != 3 {
		t.Fatalf("AbortStale = %d, %v", n, err)
	}
	want := append([]string{recent, kept, folder}, others...)
	slices.Sort(want)
	if got := dirNames(t, d.spool); !slices.Equal(got, want) {
		t.Errorf("the spool folder holds %q, want %q", got, want)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.uploads) != 1 || f.uploads["running"] == nil {
		t.Errorf("uploads left: %q", slices.Collect(maps.Keys(f.uploads)))
	}
}

func TestOpenRefusals(t *testing.T) {
	keys := newKeys(t)
	spool := t.TempDir()
	plain := testConfig()
	plain.Endpoint = "http://s3.test"
	for _, tc := range []struct {
		name  string
		cfg   Config
		keys  Keys
		spool string
		kind  Kind
		field string
	}{
		{"no type", Config{S3: testConfig()}, keys, spool, KindInvalidConfig, "type"},
		{"an unknown type", Config{Type: "ftp", S3: testConfig()}, keys, spool, KindInvalidConfig, "type"},
		{"bad storage settings", Config{Type: TypeS3, S3: plain}, keys, spool, KindInvalidConfig, "endpoint"},
		{"no SFTP settings", Config{Type: TypeSFTP, S3: testConfig()}, keys, spool, KindInvalidConfig, "host"},
		{"no keys", Config{Type: TypeS3, S3: testConfig()}, Keys{}, spool, KindInvalidConfig, "encryptionKey"},
		{"no spool folder", Config{Type: TypeS3, S3: testConfig()}, keys, "", KindUnexpected, ""},
		{"a relative spool folder", Config{Type: TypeS3, S3: testConfig()}, keys, "spool", KindUnexpected, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Open(tc.cfg, tc.keys, Options{SpoolDir: tc.spool})
			if e := wantKind(t, err, tc.kind); e.Field != tc.field {
				t.Errorf("field %q, want %q", e.Field, tc.field)
			}
		})
	}
}
