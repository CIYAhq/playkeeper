package offsite

import (
	"context"
	"strings"
	"testing"
	"time"
)

func steps(res TestResult) (ok, failed []string) {
	for _, ch := range res.Checks {
		if ch.OK {
			ok = append(ok, ch.Step)
		} else {
			failed = append(failed, ch.Step)
		}
	}
	return ok, failed
}

func TestConnectionTest(t *testing.T) {
	f := newFake(t)
	f.versioning = true
	c, _ := f.client(nil)
	res := c.Test(context.Background())
	ok, failed := steps(res)
	if !res.OK || strings.Join(ok, " ") != "write read list multipart delete" || failed != nil || res.Warning != "" || res.Skew != 0 {
		t.Fatalf("result %+v", res)
	}
	for _, ch := range res.Checks {
		if ch.Msg == "" || ch.Kind != "" || ch.Hint != "" {
			t.Errorf("check %+v", ch)
		}
	}

	var key string
	for _, op := range f.ops() {
		if k, ok := strings.CutPrefix(op, "PutObject "); ok {
			key = k
		}
	}
	name, found := strings.CutPrefix(key, testPrefix)
	if !found || !isProbeName(name) {
		t.Fatalf("test file %q", key)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.objects) != 0 || len(f.versions) != 0 || len(f.uploads) != 0 {
		t.Errorf("left behind: %d files, %d old versions, %d uploads", len(f.objects), len(f.versions), len(f.uploads))
	}
}

func TestConnectionTestFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setup    func(*fakeS3, *Config)
		failed   string // the failing steps
		ran      int    // how many checks ran
		kind     Kind
		msg      string
		leftover int // files left in the bucket
	}{
		{name: "read-only key", setup: func(f *fakeS3, _ *Config) { f.deny["PutObject"] = true }, failed: "write", ran: 1,
			kind: KindPermission, msg: "The access key isn't allowed to write files to this bucket."},
		{name: "wrong secret", setup: func(_ *fakeS3, cfg *Config) { cfg.SecretKey = NewSecret("x" + testSecret) }, failed: "write", ran: 1,
			kind: KindWrongKeys, msg: "The secret access key doesn't match the access key ID."},
		{name: "no bucket", setup: func(_ *fakeS3, cfg *Config) { cfg.Bucket = "missing" }, failed: "write", ran: 1,
			kind: KindNoSuchBucket, msg: "There is no bucket named missing at this storage service."},
		{name: "reading refused", setup: func(f *fakeS3, _ *Config) { f.deny["GetObject"] = true }, failed: "read", ran: 5,
			kind: KindPermission, msg: "The access key isn't allowed to read files in this bucket."},
		{name: "listing refused", setup: func(f *fakeS3, _ *Config) { f.deny["ListObjectsV2"] = true }, failed: "list", ran: 5,
			kind: KindPermission, msg: "The access key isn't allowed to list the files in this bucket."},
		{name: "listing lags", setup: func(f *fakeS3, _ *Config) {
			f.fail = answer("ListObjectsV2", fakeFailure{raw: "<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>"})
		}, failed: "list", ran: 5, kind: KindUnexpected, msg: "The folder's list didn't include the test file."},
		{name: "multipart refused", setup: func(f *fakeS3, _ *Config) { f.deny["CreateMultipartUpload"] = true }, failed: "multipart", ran: 5,
			kind: KindPermission, msg: "The access key isn't allowed to write files to this bucket."},
		{name: "deleting refused", setup: func(f *fakeS3, _ *Config) { f.deny["DeleteObject"] = true }, failed: "delete", ran: 5,
			kind: KindPermission, msg: "The access key isn't allowed to delete files in this bucket.", leftover: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			cfg := testConfig()
			tc.setup(f, &cfg)
			c, err := New(cfg, f.httpClient(), nil)
			if err != nil {
				t.Fatal(err)
			}
			c.sleep = func(ctx context.Context, d time.Duration) error { return nil }
			res := c.Test(context.Background())
			_, failed := steps(res)
			if res.OK || strings.Join(failed, " ") != tc.failed || len(res.Checks) != tc.ran {
				t.Fatalf("result %+v", res)
			}
			for _, ch := range res.Checks {
				if ch.Step != tc.failed {
					continue
				}
				if ch.Kind != tc.kind || ch.Msg != tc.msg || ch.Hint == "" || ch.Params["op"] == "" {
					t.Errorf("check %+v, want %s: %q", ch, tc.kind, tc.msg)
				}
				if strings.Contains(ch.Msg+ch.Hint, testSecret) {
					t.Error("the check shows the secret")
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if n := len(f.objects); n != tc.leftover || len(f.uploads) != 0 {
				t.Errorf("left %d files and %d uploads, want %d files", n, len(f.uploads), tc.leftover)
			}
		})
	}
}

func TestConnectionTestClockWarning(t *testing.T) {
	for _, tc := range []struct {
		name  string
		off   time.Duration
		warn  string
		fails bool
	}{
		{"on time", 0, "", false},
		{"a little off", 3 * time.Minute, "", false},
		{"behind", 7 * time.Minute, "This machine's clock is 7 minutes behind the storage service's", false},
		{"ahead", -7 * time.Minute, "This machine's clock is 7 minutes ahead of the storage service's", false},
		{"too far", 20 * time.Minute, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.now = func() time.Time { return time.Now().Add(tc.off) }
			c, _ := f.client(nil)
			res := c.Test(context.Background())
			if res.OK == tc.fails || !strings.HasPrefix(res.Warning, tc.warn) || (tc.warn == "") != (res.Warning == "") {
				t.Fatalf("result %+v", res)
			}
			if tc.fails {
				if len(res.Checks) != 1 || res.Checks[0].Kind != KindClockSkew || res.Checks[0].Params["skew"] != "20 minutes" {
					t.Errorf("checks %+v", res.Checks)
				}
				return
			}
			if d := res.Skew - tc.off; d < -2*time.Second || d > 2*time.Second {
				t.Errorf("skew %v, want about %v", res.Skew, tc.off)
			}
			if tc.warn != "" && !strings.Contains(res.Warning, "timedatectl") {
				t.Errorf("warning %q doesn't say how to fix it", res.Warning)
			}
		})
	}
}

func TestConnectionTestCancelled(t *testing.T) {
	f := newFake(t)
	c, _ := f.client(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := c.Test(ctx)
	if res.OK || len(res.Checks) != 1 || res.Checks[0].Kind != KindCanceled {
		t.Errorf("result %+v", res)
	}
	if len(f.ops()) != 0 {
		t.Errorf("sent %q", f.ops())
	}
}

func TestIsProbeName(t *testing.T) {
	for name, want := range map[string]bool{
		probePrefix + "0123456789abcdef.txt":       true,
		probePrefix + "0123456789abcdeg.txt":       false,
		probePrefix + "0123456789abcdef.tar.gz":    false,
		probePrefix + "0123.txt":                   false,
		"x" + probePrefix + "0123456789abcdef.txt": false,
		".txt": false,
	} {
		if isProbeName(name) != want {
			t.Errorf("isProbeName(%q) = %v", name, !want)
		}
	}
}
