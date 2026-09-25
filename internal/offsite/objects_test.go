package offsite

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestList(t *testing.T) {
	f := newFake(t)
	f.pageSize = 2
	c, _ := f.client(nil)
	for _, k := range []string{
		testPrefix + "c-3.tar.gz",
		testPrefix + "a-1.tar.gz",
		testPrefix + "b-2.tar.gz",
		testPrefix + "notes.txt",
		testPrefix + ".hidden.tar.gz",
		testPrefix + "bad name.tar.gz",
		testPrefix + "sub/d-4.tar.gz",
		"playkeeper/creative/e-5.tar.gz",
		"playkeeper/survival.tar.gz",
	} {
		f.put(k, []byte(k))
	}
	got, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range got {
		names = append(names, o.Name)
		if o.Key != testPrefix+o.Name || o.Size != int64(len(o.Key)) || o.LastModified.IsZero() || o.ETag == "" || strings.Contains(o.ETag, `"`) {
			t.Errorf("object %+v", o)
		}
	}
	if want := []string{"a-1.tar.gz", "b-2.tar.gz", "c-3.tar.gz"}; !slices.Equal(names, want) {
		t.Errorf("listed %q, want %q", names, want)
	}
	if n := f.count("ListObjectsV2"); n != 3 {
		t.Errorf("listed in %d pages, want 3 pages of 2", n)
	}
}

func TestListEmpty(t *testing.T) {
	f := newFake(t)
	c, _ := f.client(nil)
	got, err := c.List(context.Background())
	if err != nil || len(got) != 0 {
		t.Errorf("List = %v, %v", got, err)
	}
}

func TestListBadAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"list that never ends", `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>same</NextContinuationToken></ListBucketResult>`,
			"The storage service's list of files didn't end."},
		{"not XML", "Hello", "The storage service sent an answer Playkeeper doesn't understand."},
		{"too large", "<ListBucketResult>" + strings.Repeat(" ", maxXMLBody) + "</ListBucketResult>",
			"The storage service sent an unexpectedly large answer."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			c, _ := f.client(nil)
			calls := 0
			f.fail = func(op string, r *http.Request) *fakeFailure {
				calls++
				return &fakeFailure{raw: tc.raw}
			}
			_, err := c.List(context.Background())
			if e := wantKind(t, err, KindUnexpected); e.Msg != tc.want {
				t.Errorf("message %q, want %q", e.Msg, tc.want)
			}
			if calls > 2 {
				t.Errorf("sent %d requests", calls)
			}
		})
	}
}

func TestVerify(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	c, _ := f.client(nil)
	file := newTestFile(300<<10, 30)
	cp := mustUpload(t, c, file.upload(testName))
	key := testPrefix + testName

	cp.VerifiedAt = time.Time{}
	got, err := c.Verify(ctx, cp)
	if err != nil || got.VerifiedAt.IsZero() || got.Key != key || got.Checked != cp.Checked {
		t.Fatalf("Verify = %+v, %v", got, err)
	}

	for _, tc := range []struct {
		name   string
		change func(o *fakeObject)
		want   string
	}{
		{"shorter", func(o *fakeObject) { o.data = o.data[:len(o.data)-1] }, "it is 307199 bytes, the backup 307200"},
		{"another backup's", func(o *fakeObject) { o.meta = newTestFile(1, 31).sha() }, "it belongs to a different backup"},
		{"checksum changed", func(o *fakeObject) { o.checksum = hexToB64(file.sha()) }, "its SHA-256 changed"},
		{"etag changed", func(o *fakeObject) { o.etag = "0123456789abcdef0123456789abcdef-5" }, "its ETag changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.mu.Lock()
			saved := *f.objects[key]
			tc.change(f.objects[key])
			f.mu.Unlock()
			defer func() {
				f.mu.Lock()
				*f.objects[key] = saved
				f.mu.Unlock()
			}()
			_, err := c.Verify(ctx, cp)
			e := wantKind(t, err, KindVerifyFailed)
			if want := "The copy " + testName + " in the bucket no longer matches the backup (" + tc.want + ")."; e.Msg != want {
				t.Errorf("message %q, want %q", e.Msg, want)
			}
		})
	}

	f.mu.Lock()
	delete(f.objects, key)
	f.mu.Unlock()
	_, err = c.Verify(ctx, cp)
	if e := wantKind(t, err, KindNotFound); e.Msg != "The copy "+testName+" is not in the bucket." {
		t.Errorf("message %q", e.Msg)
	}

	before := len(f.ops())
	_, err = c.Verify(ctx, Copy{Name: "../" + testName, Size: 1})
	wantKind(t, err, KindUnexpected)
	if len(f.ops()) != before {
		t.Error("an invalid name reached the service")
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	key := testPrefix + testName

	t.Run("unversioned", func(t *testing.T) {
		f := newFake(t)
		c, _ := f.client(nil)
		f.put(key, []byte("x"))
		f.put(testPrefix+"other-1.tar.gz", []byte("y"))
		if err := c.Delete(ctx, testName); err != nil {
			t.Fatal(err)
		}
		if f.object(key) != nil || f.object(testPrefix+"other-1.tar.gz") == nil {
			t.Errorf("objects left: %q", f.ops())
		}
		if err := c.Delete(ctx, testName); err != nil {
			t.Errorf("deleting a copy that is gone: %v", err)
		}
	})

	t.Run("versioned", func(t *testing.T) {
		f := newFake(t)
		f.versioning = true
		c, _ := f.client(nil)
		f.put(key, []byte("first"))
		f.put(key, []byte("second"))
		f.put(key+".old", []byte("a neighbour sharing the key as a prefix"))
		if err := c.Delete(ctx, testName); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.objects[key] != nil || len(f.versions[key]) != 0 {
			t.Errorf("left %+v and versions %+v", f.objects[key], f.versions[key])
		}
		if f.objects[key+".old"] == nil {
			t.Error("the neighbour was deleted")
		}
	})

	t.Run("locked", func(t *testing.T) {
		f := newFake(t)
		f.versioning, f.locked = true, true
		c, _ := f.client(nil)
		f.put(key, []byte("first"))
		e := wantKind(t, c.Delete(ctx, testName), KindLocked)
		if !strings.Contains(e.Msg, "is deleted, but the bucket refused to delete its older versions") || e.Hint == "" {
			t.Errorf("error %q, hint %q", e.Msg, e.Hint)
		}
		if objs, err := c.List(ctx); err != nil || len(objs) != 0 {
			t.Errorf("List = %v, %v", objs, err)
		}
	})

	t.Run("versions not listable", func(t *testing.T) {
		f := newFake(t)
		f.deny["ListObjectVersions"] = true
		c, _ := f.client(nil)
		f.put(key, []byte("x"))
		if err := c.Delete(ctx, testName); err != nil || f.object(key) != nil {
			t.Errorf("Delete = %v", err)
		}
	})

	t.Run("refused", func(t *testing.T) {
		f := newFake(t)
		f.deny["DeleteObject"] = true
		c, _ := f.client(nil)
		f.put(key, []byte("x"))
		e := wantKind(t, c.Delete(ctx, testName), KindPermission)
		if e.Msg != "The access key isn't allowed to delete files in this bucket." || e.Name != testName {
			t.Errorf("error %+v", e)
		}
	})

	t.Run("invalid name", func(t *testing.T) {
		f := newFake(t)
		c, _ := f.client(nil)
		for _, name := range []string{"", "../" + testName, "sub/" + testName, "notes.txt"} {
			wantKind(t, c.Delete(ctx, name), KindUnexpected)
		}
		if len(f.ops()) != 0 {
			t.Errorf("sent %q", f.ops())
		}
	})
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestDownload(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	c, waits := f.client(nil)
	file := newTestFile(200<<10, 40)
	size, sha := int64(len(file.data)), file.sha()
	mustUpload(t, c, file.upload(testName))

	dir := t.TempDir()
	path, err := c.Download(ctx, testName, size, strings.ToUpper(sha), dir)
	if err != nil || path != filepath.Join(dir, testName) {
		t.Fatalf("Download = %q, %v", path, err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, file.data) {
		t.Fatal("the download differs")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, %v", info.Mode(), err)
	}
	if names := dirNames(t, dir); !slices.Equal(names, []string{testName}) {
		t.Errorf("files %q", names)
	}

	before := len(f.ops())
	_, err = c.Download(ctx, testName, size, sha, dir)
	if e := wantKind(t, err, KindConflict); !strings.Contains(e.Msg, "already on this machine") {
		t.Errorf("message %q", e.Msg)
	}
	if len(f.ops()) != before {
		t.Error("downloaded although the file exists")
	}

	t.Run("broken off and retried", func(t *testing.T) {
		broke := false
		f.fail = func(op string, r *http.Request) *fakeFailure {
			if op == "GetObject" && !broke {
				broke = true
				return &fakeFailure{half: true}
			}
			return nil
		}
		defer func() { f.fail = nil }()
		*waits = nil
		dir := t.TempDir()
		path, err := c.Download(ctx, testName, size, sha, dir)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); !bytes.Equal(got, file.data) || !broke || len(*waits) != 1 {
			t.Errorf("download of %d bytes after %d retries", len(got), len(*waits))
		}
	})

	for _, tc := range []struct {
		name     string
		setup    func()
		dlName   string
		size     int64
		kind     Kind
		want     string
		noLength bool
	}{
		{"tampered", func() { f.put(testPrefix+"tampered-1.tar.gz", newTestFile(int(size), 41).data) }, "tampered-1.tar.gz", size, KindVerifyFailed,
			"The downloaded copy of tampered-1.tar.gz doesn't match the backup (its SHA-256 differs), so it was discarded.", false},
		{"wrong size", nil, testName, size - 1, KindVerifyFailed,
			"The downloaded copy of " + testName + " doesn't match the backup (it is 204800 bytes, the backup 204799), so it was discarded.", false},
		{"longer, without a length", nil, testName, size - 1, KindVerifyFailed,
			"The downloaded copy of " + testName + " doesn't match the backup (it is at least 204800 bytes, the backup 204799), so it was discarded.", true},
		{"missing", nil, "missing-1.tar.gz", size, KindNotFound, "The copy missing-1.tar.gz is not in the bucket.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != nil {
				tc.setup()
			}
			f.mu.Lock()
			f.noLength = tc.noLength
			f.mu.Unlock()
			defer func() {
				f.mu.Lock()
				f.noLength = false
				f.mu.Unlock()
			}()
			dir := t.TempDir()
			_, err := c.Download(ctx, tc.dlName, tc.size, sha, dir)
			if e := wantKind(t, err, tc.kind); e.Msg != tc.want {
				t.Errorf("message %q, want %q", e.Msg, tc.want)
			}
			if names := dirNames(t, dir); len(names) != 0 {
				t.Errorf("left %q behind", names)
			}
		})
	}

	t.Run("bad input", func(t *testing.T) {
		dir := t.TempDir()
		before := len(f.ops())
		for _, args := range []struct {
			name string
			size int64
			sha  string
		}{
			{"../" + testName, size, sha},
			{testName, -1, sha},
			{testName, size, "abc"},
			{testName, size, strings.Repeat("g", 64)},
		} {
			_, err := c.Download(ctx, args.name, args.size, args.sha, dir)
			wantKind(t, err, KindUnexpected)
		}
		if len(f.ops()) != before || len(dirNames(t, dir)) != 0 {
			t.Error("bad input reached the service or the disk")
		}
	})

	t.Run("folder missing", func(t *testing.T) {
		_, err := c.Download(ctx, testName, size, sha, filepath.Join(t.TempDir(), "missing"))
		if e := wantKind(t, err, KindUnexpected); e.Msg != "Playkeeper couldn't create a file for the download." {
			t.Errorf("message %q", e.Msg)
		}
	})
}
