package offsite

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

const testName = "survival-2026-09-25T120000Z.tar.gz"

func mustUpload(t *testing.T, c *Client, up Upload) Copy {
	t.Helper()
	cp, err := c.Upload(context.Background(), up)
	if err != nil {
		t.Fatalf("Upload: %v (%+v)", err, asError(err))
	}
	return cp
}

func wantKind(t *testing.T, err error, kind Kind) *Error {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %s", kind)
	}
	e := asError(err)
	if e.Kind != kind {
		t.Fatalf("error kind %s (%q), want %s", e.Kind, e.Msg, kind)
	}
	if strings.Contains(e.Msg+e.Hint+e.Error(), testSecret) {
		t.Fatalf("error shows the secret: %q", e.Msg)
	}
	return e
}

func TestUploadSingle(t *testing.T) {
	f := newFake(t)
	c, _ := f.client(nil)
	file := newTestFile(40<<10, 1)
	cp := mustUpload(t, c, file.upload(testName))

	key := testPrefix + testName
	if cp.Name != testName || cp.Key != key || cp.Size != 40<<10 || cp.SHA256 != file.sha() || !cp.Uploaded || cp.VerifiedAt.IsZero() {
		t.Errorf("copy = %+v", cp)
	}
	if cp.Checked != CheckedSHA256 || cp.ChecksumSHA256 != hexToB64(file.sha()) {
		t.Errorf("checked %q with checksum %q", cp.Checked, cp.ChecksumSHA256)
	}
	o := f.object(key)
	if o == nil || !bytes.Equal(o.data, file.data) || o.meta != file.sha() || cp.ETag != o.etag {
		t.Fatalf("stored object %+v", o)
	}
	if got, want := f.ops(), []string{"HeadObject " + key, "PutObject " + key, "HeadObject " + key}; !slices.Equal(got, want) {
		t.Errorf("requests %q, want %q", got, want)
	}
}

func TestUploadMultipart(t *testing.T) {
	f := newFake(t)
	c, _ := f.client(nil)
	file := newTestFile(300<<10, 2)
	var progress []Progress
	up := file.upload(testName)
	up.Progress = func(p Progress) { progress = append(progress, p) }
	cp := mustUpload(t, c, up)

	if cp.Checked != CheckedSHA256 || !strings.HasSuffix(cp.ChecksumSHA256, "-5") || !strings.HasSuffix(cp.ETag, "-5") {
		t.Errorf("copy = %+v", cp)
	}
	if o := f.object(testPrefix + testName); o == nil || !bytes.Equal(o.data, file.data) || o.meta != file.sha() {
		t.Fatal("stored object differs")
	}
	if f.count("CreateMultipartUpload") != 1 || f.count("UploadPart") != 5 || f.count("CompleteMultipartUpload") != 1 || len(f.uploads) != 0 {
		t.Errorf("requests %q", f.ops())
	}
	if len(progress) != 5 || progress[4].Sent != 300<<10 || progress[4].Total != 300<<10 {
		t.Fatalf("progress %+v", progress)
	}
	for i, p := range progress {
		if p.State == nil || len(p.State.Parts) != i+1 || p.State.UploadID == "" || p.State.PartSize != 64<<10 || !p.State.Checksums {
			t.Errorf("progress %d state %+v", i, p.State)
		}
	}
	if file.total != 2*int64(len(file.data)) {
		t.Errorf("read %d bytes, want each byte twice (%d)", file.total, 2*len(file.data))
	}
	if file.maxRead > 1<<20 {
		t.Errorf("largest read %d bytes; memory should not grow with the part", file.maxRead)
	}
}

func TestUploadCheckLevels(t *testing.T) {
	for _, tc := range []struct {
		name                string
		ignore, opaque      bool
		size                int
		want                string
		wantChecksumOnState bool
	}{
		{"single without checksums", true, false, 10 << 10, CheckedMD5, false},
		{"multipart without checksums", true, false, 200 << 10, CheckedMD5, false},
		{"single with opaque ETags", true, true, 10 << 10, CheckedSize, false},
		{"multipart with opaque ETags", true, true, 200 << 10, CheckedSize, false},
		{"multipart with opaque ETags and checksums", false, true, 200 << 10, CheckedSHA256, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.ignoreChecksums, f.opaqueETags = tc.ignore, tc.opaque
			c, _ := f.client(nil)
			cp := mustUpload(t, c, newTestFile(tc.size, 3).upload(testName))
			if cp.Checked != tc.want {
				t.Errorf("checked %q, want %q", cp.Checked, tc.want)
			}
		})
	}
}

func failPart(n string) func(string, *http.Request) *fakeFailure {
	return func(op string, r *http.Request) *fakeFailure {
		if op == "UploadPart" && r.URL.Query().Get("partNumber") == n {
			return &fakeFailure{drop: true}
		}
		return nil
	}
}

// interrupted uploads a 300 KiB file whose third part never arrives.
func interrupted(t *testing.T, f *fakeS3, c *Client, file *testFile) *UploadState {
	t.Helper()
	f.fail = failPart("3")
	_, err := c.Upload(context.Background(), file.upload(testName))
	f.fail = nil
	e := wantKind(t, err, KindNetwork)
	if !e.Retry || e.Resume == nil || len(e.Resume.Parts) != 2 || e.Resume.Key != testPrefix+testName {
		t.Fatalf("error %+v with resume %+v", e, e.Resume)
	}
	if len(f.uploads) != 1 {
		t.Fatalf("the service holds %d uploads, want the unfinished one", len(f.uploads))
	}
	return e.Resume
}

func TestUploadResume(t *testing.T) {
	f := newFake(t)
	c, waits := f.client(nil)
	file := newTestFile(300<<10, 4)
	state := interrupted(t, f, c, file)
	if want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}; !slices.Equal(*waits, want) {
		t.Errorf("waited %v between attempts, want %v", *waits, want)
	}

	before := len(f.ops())
	up := file.upload(testName)
	up.Resume = state
	cp := mustUpload(t, c, up)
	after := f.ops()[before:]
	if n := countOps(after, "UploadPart"); n != 3 || countOps(after, "CreateMultipartUpload") != 0 || countOps(after, "ListParts") != 1 {
		t.Errorf("resuming sent %q", after)
	}
	if !cp.Uploaded || cp.Checked != CheckedSHA256 || !bytes.Equal(f.object(testPrefix+testName).data, file.data) {
		t.Errorf("copy %+v", cp)
	}
}

func countOps(ops []string, op string) int {
	n := 0
	for _, o := range ops {
		if strings.HasPrefix(o, op+" ") {
			n++
		}
	}
	return n
}

func TestUploadResumeFindsUnsavedParts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		opaque    bool
		wantParts int
	}{
		// Part 2 was stored but its answer never saved: its MD5 ETag shows it is intact.
		{"md5 etags", false, 3},
		// With opaque ETags and no checksums only saved parts can be trusted.
		{"opaque etags", true, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.opaqueETags, f.ignoreChecksums = tc.opaque, true
			c, _ := f.client(nil)
			file := newTestFile(300<<10, 5)
			state := interrupted(t, f, c, file)
			state.Parts = state.Parts[:1]

			before := len(f.ops())
			up := file.upload(testName)
			up.Resume = state
			mustUpload(t, c, up)
			if n := countOps(f.ops()[before:], "UploadPart"); n != tc.wantParts {
				t.Errorf("uploaded %d parts, want %d", n, tc.wantParts)
			}
			if !bytes.Equal(f.object(testPrefix+testName).data, file.data) {
				t.Error("stored object differs")
			}
		})
	}
}

func TestUploadResumeAfterUploadGone(t *testing.T) {
	f := newFake(t)
	c, _ := f.client(nil)
	file := newTestFile(300<<10, 6)
	state := interrupted(t, f, c, file)
	f.mu.Lock()
	clear(f.uploads)
	f.mu.Unlock()

	up := file.upload(testName)
	up.Resume = state
	mustUpload(t, c, up)
	if f.count("CreateMultipartUpload") != 2 {
		t.Errorf("requests %q, want a fresh upload", f.ops())
	}
}

func TestUploadIgnoresStateForAnotherFile(t *testing.T) {
	f := newFake(t)
	c, _ := f.client(nil)
	file := newTestFile(300<<10, 7)
	state := interrupted(t, f, c, file)
	other := newTestFile(300<<10, 8)
	up := other.upload("creative-2026-09-25T120000Z.tar.gz")
	up.Resume = state
	mustUpload(t, c, up)
	if f.count("ListParts") != 0 || f.count("CreateMultipartUpload") != 2 {
		t.Errorf("requests %q", f.ops())
	}
}

func TestUploadRefusesFileNotMatchingItsChecksum(t *testing.T) {
	for _, size := range []int{10 << 10, 300 << 10} {
		f := newFake(t)
		c, _ := f.client(nil)
		file := newTestFile(size, 9)
		up := file.upload(testName)
		up.SHA256 = newTestFile(size, 10).sha()
		_, err := c.Upload(context.Background(), up)
		e := wantKind(t, err, KindLocalChanged)
		if !strings.Contains(e.Msg, "doesn't match the checksum recorded") || e.Resume != nil {
			t.Errorf("error %q, resume %v", e.Msg, e.Resume)
		}
		if f.count("PutObject") != 0 || f.object(testPrefix+testName) != nil || len(f.uploads) != 0 {
			t.Errorf("size %d: requests %q, uploads left %d", size, f.ops(), len(f.uploads))
		}
	}
}

func TestUploadFileChangingWhileSent(t *testing.T) {
	for _, tc := range []struct {
		name        string
		size        int
		changeAfter int64
	}{
		{"single", 40 << 10, 40 << 10},
		// Parts 1 and 2 are hashed and sent, then part 3 is hashed: 320 KiB
		// read. The byte that flips is in part 3, which is sent next.
		{"multipart", 300 << 10, 320 << 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			c, _ := f.client(nil)
			file := newTestFile(tc.size, 11)
			up := file.upload(testName)
			file.changeAfter = tc.changeAfter
			_, err := c.Upload(context.Background(), up)
			e := wantKind(t, err, KindLocalChanged)
			if !strings.Contains(e.Msg, "changed while it was being copied") || e.Resume != nil {
				t.Errorf("error %q, resume %v", e.Msg, e.Resume)
			}
			if f.object(testPrefix+testName) != nil || len(f.uploads) != 0 {
				t.Error("a copy or unfinished upload was left behind")
			}
		})
	}
}

func TestUploadWithoutChecksumSupport(t *testing.T) {
	for _, size := range []int{10 << 10, 300 << 10} {
		f := newFake(t)
		f.noChecksums = true
		c, _ := f.client(nil)
		file := newTestFile(size, 12)
		cp := mustUpload(t, c, file.upload(testName))
		if cp.Checked != CheckedMD5 || !c.noChecksums.Load() || !bytes.Equal(f.object(testPrefix+testName).data, file.data) {
			t.Errorf("size %d: copy %+v", size, cp)
		}
	}
}

func TestUploadCompleteRetried(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure fakeFailure
	}{
		{"error in a 200 answer", fakeFailure{in200: true, code: "InternalError", message: "We encountered an internal error. Please try again."}},
		{"answer lost after completing", fakeFailure{dropAfter: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			c, _ := f.client(nil)
			failed := false
			f.fail = func(op string, r *http.Request) *fakeFailure {
				if op == "CompleteMultipartUpload" && !failed {
					failed = true
					return &tc.failure
				}
				return nil
			}
			file := newTestFile(300<<10, 13)
			cp := mustUpload(t, c, file.upload(testName))
			if !cp.Uploaded || cp.Checked != CheckedSHA256 || !bytes.Equal(f.object(testPrefix+testName).data, file.data) {
				t.Errorf("copy %+v", cp)
			}
		})
	}
}

func TestUploadDeletesCopyThatFailsVerification(t *testing.T) {
	f := newFake(t)
	f.lieChecksum = true
	c, _ := f.client(nil)
	_, err := c.Upload(context.Background(), newTestFile(10<<10, 14).upload(testName))
	e := wantKind(t, err, KindVerifyFailed)
	if !strings.Contains(e.Msg, "its SHA-256 differs") || !strings.Contains(e.Msg, "so it was deleted") {
		t.Errorf("message %q", e.Msg)
	}
	if f.object(testPrefix+testName) != nil {
		t.Error("the bad copy is still there")
	}
}

func TestUploadFindsIdenticalCopy(t *testing.T) {
	f := newFake(t)
	c, _ := f.client(nil)
	file := newTestFile(300<<10, 15)
	mustUpload(t, c, file.upload(testName))
	before := len(f.ops())
	cp := mustUpload(t, c, file.upload(testName))
	if cp.Uploaded || cp.Checked != CheckedSize || cp.Size != 300<<10 {
		t.Errorf("copy %+v", cp)
	}
	if got := f.ops()[before:]; !slices.Equal(got, []string{"HeadObject " + testPrefix + testName}) {
		t.Errorf("second upload sent %q", got)
	}

	small := newTestFile(10<<10, 16)
	mustUpload(t, c, small.upload("small-2026-09-25T120000Z.tar.gz"))
	if cp := mustUpload(t, c, small.upload("small-2026-09-25T120000Z.tar.gz")); cp.Uploaded || cp.Checked != CheckedSHA256 {
		t.Errorf("single-request copy found again: %+v", cp)
	}
}

func TestUploadNeverOverwritesDifferentFile(t *testing.T) {
	f := newFake(t)
	c, _ := f.client(nil)
	f.put(testPrefix+testName, []byte("someone else's file"))
	_, err := c.Upload(context.Background(), newTestFile(10<<10, 17).upload(testName))
	e := wantKind(t, err, KindConflict)
	if !strings.Contains(e.Msg, "A different file named "+testName) {
		t.Errorf("message %q", e.Msg)
	}
	if string(f.object(testPrefix+testName).data) != "someone else's file" || f.count("PutObject") != 0 {
		t.Error("the other file was touched")
	}
}

func TestUploadRejectsBadInput(t *testing.T) {
	f := newFake(t)
	c, _ := f.client(nil)
	file := newTestFile(1<<10, 18)
	for _, tc := range []struct {
		name string
		mod  func(*Upload)
		kind Kind
	}{
		{"path in name", func(u *Upload) { u.Name = "../x.tar.gz" }, KindInvalidConfig},
		{"wrong extension", func(u *Upload) { u.Name = "x.zip" }, KindInvalidConfig},
		{"empty", func(u *Upload) { u.Size = 0 }, KindUnexpected},
		{"no checksum", func(u *Upload) { u.SHA256 = "abc" }, KindUnexpected},
	} {
		up := file.upload(testName)
		tc.mod(&up)
		_, err := c.Upload(context.Background(), up)
		if e := asError(err); err == nil || e.Kind != tc.kind {
			t.Errorf("%s: error %v, want %s", tc.name, err, tc.kind)
		}
	}
	if len(f.ops()) != 0 {
		t.Errorf("bad input reached the service: %q", f.ops())
	}
}

func TestUploadCancelledKeepsResumeState(t *testing.T) {
	f := newFake(t)
	c, _ := f.client(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	file := newTestFile(300<<10, 19)
	up := file.upload(testName)
	up.Progress = func(p Progress) {
		if p.Sent >= 128<<10 {
			cancel()
		}
	}
	_, err := c.Upload(ctx, up)
	e := wantKind(t, err, KindCanceled)
	if e.Msg != "The upload was cancelled before it finished." || e.Resume == nil || len(e.Resume.Parts) != 2 {
		t.Errorf("error %q, resume %+v", e.Msg, e.Resume)
	}
	if f.count("AbortMultipartUpload") != 0 {
		t.Error("a cancelled upload was aborted instead of kept for resuming")
	}

	if err := c.Abort(context.Background(), e.Resume); err != nil || len(f.uploads) != 0 {
		t.Errorf("Abort: %v, %d uploads left", err, len(f.uploads))
	}
	if err := c.Abort(context.Background(), e.Resume); err != nil {
		t.Errorf("aborting again: %v", err)
	}
}

func TestUploadStalled(t *testing.T) {
	f := newFake(t)
	c, _ := f.client(nil)
	c.stall, c.attempts = 100*time.Millisecond, 1
	f.fail = func(op string, r *http.Request) *fakeFailure {
		if op == "HeadObject" {
			return &fakeFailure{hang: true}
		}
		return nil
	}
	_, err := c.Upload(context.Background(), newTestFile(1<<10, 20).upload(testName))
	e := wantKind(t, err, KindNetwork)
	if !strings.Contains(e.Msg, "stalled") || !e.Retry {
		t.Errorf("error %q", e.Msg)
	}
}

func TestAbortStale(t *testing.T) {
	f := newFake(t)
	c, _ := f.client(nil)
	old, recent := time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)
	f.mu.Lock()
	for id, u := range map[string]*fakeUpload{
		"old":       {key: testPrefix + "a-1.tar.gz", initiated: old},
		"old-probe": {key: testPrefix + probePrefix + "0123456789abcdef.txt", initiated: old},
		"kept":      {key: testPrefix + "b-2.tar.gz", initiated: old},
		"recent":    {key: testPrefix + "c-3.tar.gz", initiated: recent},
		"nested":    {key: testPrefix + "sub/d-4.tar.gz", initiated: old},
		"other":     {key: "playkeeper/creative/e-5.tar.gz", initiated: old},
		"foreign":   {key: testPrefix + "notes.txt", initiated: old},
	} {
		u.parts = map[int]fakePart{}
		f.uploads[id] = u
	}
	f.mu.Unlock()

	n, err := c.AbortStale(context.Background(), time.Now().Add(-24*time.Hour), []string{"kept"})
	if err != nil || n != 2 {
		t.Fatalf("AbortStale = %d, %v; want 2", n, err)
	}
	var left []string
	for id := range f.uploads {
		left = append(left, id)
	}
	slices.Sort(left)
	if want := []string{"foreign", "kept", "nested", "other", "recent"}; !slices.Equal(left, want) {
		t.Errorf("uploads left %q, want %q", left, want)
	}
}
