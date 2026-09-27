package offsite

import (
	"context"
	"net/http"
	"os"
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
		testPrefix + "c-3.tar.gz.age",
		testPrefix + "a-1.tar.gz.age",
		testPrefix + "b-2.tar.gz.age",
		testPrefix + "d-4.tar.gz",
		testPrefix + "notes.txt",
		testPrefix + ".hidden.tar.gz.age",
		testPrefix + ".a-1.tar.gz.age.partial",
		testPrefix + "bad name.tar.gz.age",
		testPrefix + probePrefix + "0123456789abcdef.age",
		testPrefix + "sub/e-5.tar.gz.age",
		"playkeeper/creative/f-6.tar.gz.age",
		"playkeeper/survival.tar.gz.age",
	} {
		f.put(k, []byte(k))
	}
	got, err := c.list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range got {
		names = append(names, o.Name)
		if o.Key != testPrefix+o.Name || o.Archive+".age" != o.Name || o.Size != int64(len(o.Key)) || o.LastModified.IsZero() || o.ETag == "" || strings.Contains(o.ETag, `"`) {
			t.Errorf("object %+v", o)
		}
	}
	if want := []string{"a-1.tar.gz.age", "b-2.tar.gz.age", "c-3.tar.gz.age"}; !slices.Equal(names, want) {
		t.Errorf("listed %q, want %q", names, want)
	}
	if n := f.count("ListObjectsV2"); n != 5 {
		t.Errorf("listed in %d pages, want 5 pages of 2", n)
	}
}

func TestListEmpty(t *testing.T) {
	f := newFake(t)
	c, _ := f.client(nil)
	got, err := c.list(context.Background())
	if err != nil || len(got) != 0 {
		t.Errorf("list = %v, %v", got, err)
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
			_, err := c.list(context.Background())
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
	cp := mustPut(t, c, file.object(testCopy))
	key := testPrefix + testCopy

	cp.VerifiedAt = time.Time{}
	got, err := c.verify(ctx, cp)
	if err != nil || got.VerifiedAt.IsZero() || got.Key != key || got.Checked != cp.Checked {
		t.Fatalf("verify = %+v, %v", got, err)
	}

	for _, tc := range []struct {
		name   string
		change func(o *fakeObject)
		want   string
	}{
		{"shorter", func(o *fakeObject) { o.data = o.data[:len(o.data)-1] }, "it is 307199 bytes, not 307200"},
		{"replaced", func(o *fakeObject) { o.meta = newTestFile(1, 31).sha() }, "it was replaced by another file"},
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
			_, err := c.verify(ctx, cp)
			e := wantKind(t, err, KindVerifyFailed)
			if want := "The copy " + testCopy + " in the bucket has changed since it was uploaded (" + tc.want + ")."; e.Msg != want {
				t.Errorf("message %q, want %q", e.Msg, want)
			}
		})
	}

	f.mu.Lock()
	delete(f.objects, key)
	f.mu.Unlock()
	_, err = c.verify(ctx, cp)
	if e := wantKind(t, err, KindNotFound); e.Msg != "The copy "+testCopy+" is not in the bucket." {
		t.Errorf("message %q", e.Msg)
	}

	before := len(f.ops())
	for _, name := range []string{"../" + testCopy, testName} {
		_, err = c.verify(ctx, Copy{Name: name, Size: 1})
		wantKind(t, err, KindUnexpected)
	}
	if len(f.ops()) != before {
		t.Error("an invalid name reached the service")
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	key := testPrefix + testCopy

	t.Run("unversioned", func(t *testing.T) {
		f := newFake(t)
		c, _ := f.client(nil)
		f.put(key, []byte("x"))
		f.put(testPrefix+"other-1.tar.gz.age", []byte("y"))
		if err := c.remove(ctx, testCopy); err != nil {
			t.Fatal(err)
		}
		if f.object(key) != nil || f.object(testPrefix+"other-1.tar.gz.age") == nil {
			t.Errorf("objects left: %q", f.ops())
		}
		if err := c.remove(ctx, testCopy); err != nil {
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
		if err := c.remove(ctx, testCopy); err != nil {
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
		e := wantKind(t, c.remove(ctx, testCopy), KindLocked)
		if !strings.Contains(e.Msg, "is deleted, but the bucket refused to delete its older versions") || e.Hint == "" {
			t.Errorf("error %q, hint %q", e.Msg, e.Hint)
		}
		if objs, err := c.list(ctx); err != nil || len(objs) != 0 {
			t.Errorf("list = %v, %v", objs, err)
		}
	})

	t.Run("versions not listable", func(t *testing.T) {
		f := newFake(t)
		f.deny["ListObjectVersions"] = true
		c, _ := f.client(nil)
		f.put(key, []byte("x"))
		if err := c.remove(ctx, testCopy); err != nil || f.object(key) != nil {
			t.Errorf("remove = %v", err)
		}
	})

	t.Run("refused", func(t *testing.T) {
		f := newFake(t)
		f.deny["DeleteObject"] = true
		c, _ := f.client(nil)
		f.put(key, []byte("x"))
		e := wantKind(t, c.remove(ctx, testCopy), KindPermission)
		if e.Msg != "The access key isn't allowed to delete files in this bucket." || e.Name != testCopy {
			t.Errorf("error %+v", e)
		}
	})

	t.Run("invalid name", func(t *testing.T) {
		f := newFake(t)
		c, _ := f.client(nil)
		for _, name := range []string{"", "../" + testCopy, "sub/" + testCopy, "notes.txt", testName} {
			wantKind(t, c.remove(ctx, name), KindUnexpected)
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
