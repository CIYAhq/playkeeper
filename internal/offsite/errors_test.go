package offsite

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	if err := testConfig().Validate(); err != nil {
		t.Fatalf("valid settings: %v", err)
	}
	for _, tc := range []struct {
		name  string
		mod   func(*Config)
		field string // "" if valid
	}{
		{"no endpoint", func(c *Config) { c.Endpoint = "" }, "endpoint"},
		{"http", func(c *Config) { c.Endpoint = "http://s3.test" }, "endpoint"},
		{"no scheme", func(c *Config) { c.Endpoint = "s3.test" }, "endpoint"},
		{"path", func(c *Config) { c.Endpoint = "https://s3.test/backups" }, "endpoint"},
		{"trailing slash", func(c *Config) { c.Endpoint = "https://s3.test/" }, ""},
		{"user", func(c *Config) { c.Endpoint = "https://me@s3.test" }, "endpoint"},
		{"query", func(c *Config) { c.Endpoint = "https://s3.test?a=b" }, "endpoint"},
		{"port", func(c *Config) { c.Endpoint = "https://s3.test:9000" }, ""},
		{"port zero", func(c *Config) { c.Endpoint = "https://s3.test:0" }, "endpoint"},
		{"port too large", func(c *Config) { c.Endpoint = "https://s3.test:70000" }, "endpoint"},
		{"underscore in host", func(c *Config) { c.Endpoint = "https://s3_test.example" }, "endpoint"},
		{"metadata address", func(c *Config) { c.Endpoint = "https://169.254.169.254" }, "endpoint"},
		{"IPv6 metadata address", func(c *Config) { c.Endpoint = "https://[fd00:ec2::254]" }, "endpoint"},
		{"multicast", func(c *Config) { c.Endpoint = "https://[ff02::1]" }, "endpoint"},
		{"unspecified", func(c *Config) { c.Endpoint = "https://0.0.0.0" }, "endpoint"},
		{"IP, path-style", func(c *Config) { c.Endpoint = "https://192.0.2.10:9000" }, ""},
		{"IP, virtual-hosted", func(c *Config) { c.Endpoint, c.PathStyle = "https://192.0.2.10", false }, "pathStyle"},
		{"no region", func(c *Config) { c.Region = "" }, "region"},
		{"upper-case region", func(c *Config) { c.Region = "EU-West-1" }, "region"},
		{"auto region", func(c *Config) { c.Region = "auto" }, ""},
		{"short bucket", func(c *Config) { c.Bucket = "ab" }, "bucket"},
		{"upper-case bucket", func(c *Config) { c.Bucket = "Backups" }, "bucket"},
		{"bucket starting with a hyphen", func(c *Config) { c.Bucket = "-backups" }, "bucket"},
		{"bucket with two dots", func(c *Config) { c.Bucket = "my..backups" }, "bucket"},
		{"bucket like an IP", func(c *Config) { c.Bucket = "192.168.1.1" }, "bucket"},
		{"dotted bucket, path-style", func(c *Config) { c.Bucket = "my.backups" }, ""},
		{"dotted bucket, virtual-hosted", func(c *Config) { c.Bucket, c.PathStyle = "my.backups", false }, "pathStyle"},
		{"no prefix", func(c *Config) { c.Prefix = "" }, ""},
		{"prefix without slash", func(c *Config) { c.Prefix = "playkeeper/survival" }, ""},
		{"absolute prefix", func(c *Config) { c.Prefix = "/playkeeper/" }, "prefix"},
		{"prefix with two slashes", func(c *Config) { c.Prefix = "playkeeper//survival/" }, "prefix"},
		{"prefix with ..", func(c *Config) { c.Prefix = "playkeeper/../other/" }, "prefix"},
		{"prefix with a space", func(c *Config) { c.Prefix = "my backups/" }, "prefix"},
		{"long prefix", func(c *Config) { c.Prefix = strings.Repeat("a", 201) }, "prefix"},
		{"no access key", func(c *Config) { c.AccessKeyID = "" }, "accessKeyId"},
		{"access key with a space", func(c *Config) { c.AccessKeyID = "AKID TEST" }, "accessKeyId"},
		{"no secret", func(c *Config) { c.SecretKey = Secret{} }, "secretKey"},
		{"secret with a line break", func(c *Config) { c.SecretKey = NewSecret(testSecret + "\n") }, "secretKey"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			tc.mod(&cfg)
			err := cfg.Validate()
			if tc.field == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			e := wantKind(t, err, KindInvalidConfig)
			if e.Field != tc.field || e.Op != "setup" || !strings.HasSuffix(e.Msg, ".") {
				t.Errorf("error %+v, want field %s", e, tc.field)
			}
			if _, err := New(cfg, nil, nil); err == nil {
				t.Error("New accepted the settings")
			}
		})
	}
}

func TestNewAddsPrefixSlash(t *testing.T) {
	cfg := testConfig()
	cfg.Prefix = "playkeeper/survival"
	c, err := New(cfg, nil, nil)
	if err != nil || c.prefix != "playkeeper/survival/" {
		t.Fatalf("prefix %q, %v", c.prefix, err)
	}
}

func TestProviders(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Providers() {
		if seen[p.ID] || p.Name == "" || p.Hint == "" {
			t.Errorf("provider %+v", p)
		}
		seen[p.ID] = true
		if p.Endpoint == "" {
			continue
		}
		cfg := testConfig()
		cfg.Region = cmp.Or(p.Region, "eu-central-1")
		cfg.PathStyle = p.PathStyle
		cfg.Endpoint = strings.NewReplacer("{region}", cfg.Region, "{account}", "0123456789abcdef0123456789abcdef").Replace(p.Endpoint)
		if err := cfg.Validate(); err != nil {
			t.Errorf("%s with its defaults: %v", p.ID, err)
		}
	}
}

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{
		testName:                             true,
		"a.tar.gz":                           true,
		"World_1-2.tar.gz":                   true,
		".tar.gz":                            false,
		".hidden.tar.gz":                     false,
		"a..b.tar.gz":                        false,
		"../a.tar.gz":                        false,
		"sub/a.tar.gz":                       false,
		`sub\a.tar.gz`:                       false,
		"a.zip":                              false,
		"a.tar.gz.part":                      false,
		"a b.tar.gz":                         false,
		"é.tar.gz":                           false,
		strings.Repeat("a", 193) + ".tar.gz": true,
		strings.Repeat("a", 194) + ".tar.gz": false,
	} {
		if ValidName(name) != want {
			t.Errorf("ValidName(%q) = %v", name, !want)
		}
	}
}

// answer makes every request for op (any op if "") get failure instead.
func answer(op string, failure fakeFailure) func(string, *http.Request) *fakeFailure {
	return func(o string, r *http.Request) *fakeFailure {
		if op == "" || o == op {
			return &failure
		}
		return nil
	}
}

func TestResponseErrors(t *testing.T) {
	list := func(ctx context.Context, c *Client) error {
		_, err := c.List(ctx)
		return err
	}
	upload := func(ctx context.Context, c *Client) error {
		_, err := c.Upload(ctx, newTestFile(1<<10, 50).upload(testName))
		return err
	}
	verify := func(ctx context.Context, c *Client) error {
		_, err := c.Verify(ctx, Copy{Name: testName, Size: 1})
		return err
	}
	full := "The storage account is full: its quota or storage cap is reached."
	for _, tc := range []struct {
		name   string
		setup  func(f *fakeS3, cfg *Config)
		run    func(context.Context, *Client) error // List if nil
		kind   Kind
		field  string
		msg    string
		retry  bool
		waits  []time.Duration
		params map[string]string
	}{
		{name: "wrong secret", setup: func(f *fakeS3, cfg *Config) { cfg.SecretKey = NewSecret("not" + testSecret) },
			kind: KindWrongKeys, field: "secretKey", msg: "The secret access key doesn't match the access key ID."},
		{name: "wrong secret on a HEAD", setup: func(f *fakeS3, cfg *Config) { cfg.SecretKey = NewSecret("not" + testSecret) }, run: upload,
			kind: KindWrongKeys, field: "secretKey", msg: "The secret access key doesn't match the access key ID."},
		{name: "unknown access key", setup: func(f *fakeS3, cfg *Config) { cfg.AccessKeyID = "AKIDUNKNOWN" },
			kind: KindWrongKeys, field: "accessKeyId", msg: "The storage service doesn't recognise the access key ID."},
		{name: "no such bucket", setup: func(f *fakeS3, cfg *Config) { cfg.Bucket = "missing" },
			kind: KindNoSuchBucket, field: "bucket", msg: "There is no bucket named missing at this storage service."},
		{name: "clock behind", setup: func(f *fakeS3, cfg *Config) { f.now = func() time.Time { return time.Now().Add(20 * time.Minute) } },
			kind: KindClockSkew, msg: "This machine's clock is 20 minutes behind the storage service's, so the service refused the request.",
			params: map[string]string{"skew": "20 minutes", "direction": "behind", "code": "RequestTimeTooSkewed", "status": "403"}},
		{name: "clock ahead", setup: func(f *fakeS3, cfg *Config) { f.now = func() time.Time { return time.Now().Add(-20 * time.Minute) } },
			kind: KindClockSkew, msg: "This machine's clock is 20 minutes ahead of the storage service's, so the service refused the request.",
			params: map[string]string{"skew": "20 minutes", "direction": "ahead"}},
		{name: "wrong region", setup: func(f *fakeS3, cfg *Config) { cfg.Region = "eu-west-1" },
			kind: KindWrongRegion, field: "region", msg: "The bucket is in region us-east-1, not eu-west-1.", params: map[string]string{"region": "us-east-1"}},
		{name: "moved to the bucket's region",
			setup: func(f *fakeS3, cfg *Config) {
				f.fail = answer("", fakeFailure{status: http.StatusMovedPermanently, code: "PermanentRedirect", header: http.Header{"X-Amz-Bucket-Region": {"eu-central-1"}}})
			},
			kind: KindWrongRegion, field: "region", msg: "The bucket is in region eu-central-1, not us-east-1."},
		{name: "list refused", setup: func(f *fakeS3, cfg *Config) { f.deny["ListObjectsV2"] = true },
			kind: KindPermission, msg: "The access key isn't allowed to list the files in this bucket."},
		{name: "write refused", setup: func(f *fakeS3, cfg *Config) { f.deny["PutObject"] = true }, run: upload,
			kind: KindPermission, msg: "The access key isn't allowed to write files to this bucket.", params: map[string]string{"op": "upload", "name": testName}},
		{name: "read refused", setup: func(f *fakeS3, cfg *Config) {
			f.put(testPrefix+testName, []byte("x"))
			f.deny["HeadObject"], f.deny["GetObject"] = true, true
		}, run: verify, kind: KindPermission, msg: "The access key isn't allowed to read files in this bucket."},
		{name: "HEAD refused without a reason", setup: func(f *fakeS3, cfg *Config) {
			f.put(testPrefix+testName, []byte("x"))
			f.deny["HeadObject"] = true
		}, run: verify,
			kind: KindPermission, msg: "The storage service refused access: the keys are wrong, or the access key isn't allowed to read files in this bucket."},
		{name: "account disabled", setup: func(f *fakeS3, cfg *Config) {
			f.fail = answer("", fakeFailure{status: http.StatusForbidden, code: "AllAccessDisabled", message: "All access to this object has been disabled"})
		}, kind: KindPermission, msg: "The storage service has disabled access to this account or bucket."},
		{name: "storage cap", setup: func(f *fakeS3, cfg *Config) {
			f.fail = answer("PutObject", fakeFailure{status: http.StatusForbidden, code: "AccessDenied", message: "Cannot upload files, storage cap exceeded."})
		}, run: upload, kind: KindStorageFull, msg: full},
		{name: "quota", setup: func(f *fakeS3, cfg *Config) {
			f.fail = answer("PutObject", fakeFailure{status: http.StatusForbidden, code: "QuotaExceeded", message: "Bucket quota exceeded"})
		}, run: upload, kind: KindStorageFull, msg: full},
		{name: "disk full", setup: func(f *fakeS3, cfg *Config) {
			f.fail = answer("PutObject", fakeFailure{status: http.StatusInsufficientStorage, code: "XMinioStorageFull", message: "Storage backend has reached its minimum free drive threshold."})
		}, run: upload, kind: KindStorageFull, msg: full},
		{name: "too large", setup: func(f *fakeS3, cfg *Config) {
			f.fail = answer("PutObject", fakeFailure{status: http.StatusBadRequest, code: "EntityTooLarge", message: "Your proposed upload exceeds the maximum allowed size"})
		}, run: upload, kind: KindTooLarge, msg: "The storage service refused the upload because it is too large."},
		{name: "service error", setup: func(f *fakeS3, cfg *Config) {
			f.fail = answer("", fakeFailure{status: http.StatusInternalServerError, code: "InternalError", message: "We encountered an internal error."})
		}, kind: KindServiceError, retry: true, msg: "The storage service had a problem (HTTP 500).", waits: []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}},
		{name: "slow down", setup: func(f *fakeS3, cfg *Config) {
			f.fail = answer("", fakeFailure{status: http.StatusServiceUnavailable, code: "SlowDown", message: "Please reduce your request rate.", header: http.Header{"Retry-After": {"7"}}})
		}, kind: KindRateLimited, retry: true, msg: "The storage service asked Playkeeper to slow down.", waits: []time.Duration{7 * time.Second, 7 * time.Second, 7 * time.Second}},
		{name: "long Retry-After", setup: func(f *fakeS3, cfg *Config) {
			f.fail = answer("", fakeFailure{status: http.StatusTooManyRequests, header: http.Header{"Retry-After": {"3600"}}})
		}, kind: KindRateLimited, retry: true, msg: "The storage service asked Playkeeper to slow down.", waits: []time.Duration{time.Minute, time.Minute, time.Minute}},
		{name: "unknown error echoing the secret", setup: func(f *fakeS3, cfg *Config) {
			f.fail = answer("", fakeFailure{status: http.StatusBadRequest, code: "OddThing", message: "Key " + testSecret + "\n\tis odd."})
		}, kind: KindUnexpected, msg: "The storage service refused the request with HTTP 400: OddThing: Key [hidden] is odd."},
		{name: "unknown error with a long message", setup: func(f *fakeS3, cfg *Config) {
			f.fail = answer("", fakeFailure{status: http.StatusConflict, code: "OddThing", message: strings.Repeat("x", 300)})
		}, kind: KindUnexpected, msg: "The storage service refused the request with HTTP 409: OddThing: " + strings.Repeat("x", 190) + "…."},
		{name: "redirect", setup: func(f *fakeS3, cfg *Config) {
			f.fail = answer("", fakeFailure{status: http.StatusTemporaryRedirect, code: "TemporaryRedirect", header: http.Header{"Location": {"https://elsewhere.test/"}}})
		}, kind: KindRedirect, field: "endpoint", msg: "The storage service tried to send the request to another address, which Playkeeper doesn't follow."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			cfg := testConfig()
			tc.setup(f, &cfg)
			calls := 0
			if hook := f.fail; hook != nil {
				f.fail = func(op string, r *http.Request) *fakeFailure {
					calls++
					return hook(op, r)
				}
			}
			c, err := New(cfg, f.httpClient(), nil)
			if err != nil {
				t.Fatal(err)
			}
			waits := &[]time.Duration{}
			c.sleep = func(ctx context.Context, d time.Duration) error {
				*waits = append(*waits, d)
				return nil
			}
			run := tc.run
			if run == nil {
				run = list
			}
			e := wantKind(t, run(context.Background(), c), tc.kind)
			if e.Msg != tc.msg || e.Field != tc.field || e.Retry != tc.retry || e.Hint == "" {
				t.Errorf("error %q (field %q, retry %v, hint %q), want %q (field %q, retry %v)", e.Msg, e.Field, e.Retry, e.Hint, tc.msg, tc.field, tc.retry)
			}
			if strings.Contains(e.Hint, testSecret) {
				t.Errorf("hint shows the secret")
			}
			if tc.waits != nil && !slices.Equal(*waits, tc.waits) {
				t.Errorf("waited %v, want %v", *waits, tc.waits)
			}
			if tc.waits == nil && len(*waits) != 0 {
				t.Errorf("retried an error that won't go away: waited %v", *waits)
			}
			p := e.Params()
			for k, v := range tc.params {
				if p[k] != v {
					t.Errorf("params %v, want %s=%q", p, k, v)
				}
			}
			if tc.kind == KindRedirect && calls != 1 {
				t.Errorf("%d requests: the redirect was followed", calls)
			}
		})
	}
}

func TestTransportErrors(t *testing.T) {
	f := newFake(t)
	hanging := newFake(t)
	hanging.fail = answer("", fakeFailure{hang: true})
	plain := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(plain.Close)
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refusedAddr := closed.Addr().String()
	closed.Close()
	to := func(addr string) func(context.Context, string, string) (net.Conn, error) {
		return func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		}
	}
	dnsFails := func(notFound bool) func(context.Context, string, string) (net.Conn, error) {
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(addr)
			return nil, &net.OpError{Op: "dial", Net: network, Err: &net.DNSError{Err: "no such host", Name: host, IsNotFound: notFound, IsTemporary: !notFound}}
		}
	}
	_, roots := testTLS()
	for _, tc := range []struct {
		name     string
		mod      func(*Config)
		dial     func(context.Context, string, string) (net.Conn, error)
		noRoots  bool
		kind     Kind
		msg      string
		hint     string // a part of the hint
		retry    bool
		field    string
		timesOut bool
	}{
		{name: "connection refused", dial: to(refusedAddr), kind: KindNetwork, field: "endpoint",
			msg: "Nothing accepted the connection at the endpoint (connection refused)."},
		{name: "host not found, virtual-hosted", mod: func(c *Config) { c.PathStyle = false }, dial: dnsFails(true), kind: KindNetwork, field: "endpoint",
			msg: "The host name backups.s3.test could not be found.", hint: "turn on path-style addressing"},
		{name: "host not found, path-style", dial: dnsFails(true), kind: KindNetwork, field: "endpoint",
			msg: "The host name s3.test could not be found.", hint: "Check the endpoint address."},
		{name: "DNS failing", dial: dnsFails(false), kind: KindNetwork, field: "endpoint", retry: true,
			msg: "This machine couldn't look up the storage service's address (DNS)."},
		{name: "unknown certificate authority", dial: to(f.srv.Listener.Addr().String()), noRoots: true, kind: KindTLS, field: "endpoint",
			msg: "The storage service's certificate could not be verified, so nothing was sent."},
		{name: "certificate for another host", mod: func(c *Config) { c.Endpoint = "https://storage.example" }, dial: to(f.srv.Listener.Addr().String()),
			kind: KindTLS, field: "endpoint", msg: "The storage service's certificate could not be verified, so nothing was sent."},
		{name: "plain HTTP", dial: to(plain.Listener.Addr().String()), kind: KindTLS, field: "endpoint",
			msg: "The endpoint didn't answer with HTTPS."},
		{name: "no answer in time", dial: to(hanging.srv.Listener.Addr().String()), timesOut: true, kind: KindNetwork, retry: true,
			msg: "The storage service didn't answer in time."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			if tc.mod != nil {
				tc.mod(&cfg)
			}
			tr := &http.Transport{DialContext: tc.dial, TLSClientConfig: &tls.Config{RootCAs: roots}}
			if tc.noRoots {
				tr.TLSClientConfig = nil
			}
			c, err := New(cfg, &http.Client{Transport: tr}, nil)
			if err != nil {
				t.Fatal(err)
			}
			c.attempts = 1
			ctx := context.Background()
			if tc.timesOut {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			_, err = c.List(ctx)
			e := wantKind(t, err, tc.kind)
			if e.Msg != tc.msg || e.Retry != tc.retry || e.Field != tc.field || !strings.Contains(e.Hint, tc.hint) || e.Status != 0 {
				t.Errorf("error %q (retry %v, field %q, hint %q)", e.Msg, e.Retry, e.Field, e.Hint)
			}
		})
	}
}

func TestCancelled(t *testing.T) {
	f := newFake(t)
	c, _ := f.client(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.List(ctx)
	if e := wantKind(t, err, KindCanceled); e.Msg != "Listing the copies was cancelled before it finished." || e.Retry {
		t.Errorf("error %q", e.Msg)
	}
}

func TestRefuseAddr(t *testing.T) {
	for addr, refused := range map[string]bool{
		"169.254.169.254:443":          true,
		"[::ffff:169.254.169.254]:443": true,
		"[fe80::1]:443":                true,
		"[fd00:ec2::254]:443":          true,
		"100.100.100.200:443":          true,
		"100.100.100.100:443":          false,
		"[fd00::1]:443":                false,
		"224.0.0.251:443":              true,
		"[ff02::1]:443":                true,
		"0.0.0.0:443":                  true,
		"[::]:443":                     true,
		"127.0.0.1:443":                false,
		"10.1.2.3:443":                 false,
		"203.0.113.9:443":              false,
		"[2001:db8::1]:443":            false,
		"not-an-ip:443":                true,
	} {
		err := refuseAddr("tcp", addr, nil)
		var re *refusedAddrError
		if refused != errors.As(err, &re) {
			t.Errorf("refuseAddr(%s) = %v", addr, err)
		}
	}
}

func TestNewHTTPClient(t *testing.T) {
	hc := NewHTTPClient()
	tr, ok := hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport %T", hc.Transport)
	}
	if tr.Proxy != nil || tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion < tls.VersionTLS12 || tr.TLSClientConfig.InsecureSkipVerify ||
		tr.TLSHandshakeTimeout == 0 || tr.ResponseHeaderTimeout == 0 || hc.Timeout != 0 {
		t.Errorf("transport settings %+v, timeout %v", tr, hc.Timeout)
	}

	// The dialer refuses before connecting; the kernel would refuse a TCP
	// connection to a multicast address too, so nothing leaves the machine.
	_, err := tr.DialContext(context.Background(), "tcp", "224.0.0.251:443")
	var re *refusedAddrError
	if !errors.As(err, &re) {
		t.Fatalf("dialling a multicast address: %v", err)
	}

	cfg := testConfig()
	inner := tr.DialContext
	tr = tr.Clone()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return inner(ctx, network, "224.0.0.251:443")
	}
	c, err := New(cfg, &http.Client{Transport: tr}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.sleep = func(context.Context, time.Duration) error { t.Error("retried"); return nil }
	_, err = c.List(context.Background())
	e := wantKind(t, err, KindInvalidConfig)
	if e.Field != "endpoint" || e.Msg != "The endpoint's host name points to 224.0.0.251, a link-local, multicast or cloud metadata address Playkeeper never connects to." {
		t.Errorf("error %+v", e)
	}
}

func TestSecretNeverShown(t *testing.T) {
	cfg := testConfig()
	var out bytes.Buffer
	fmt.Fprintf(&out, "%v %+v %#v %s %q %x %d %v\n", cfg, cfg, cfg, cfg.SecretKey, cfg.SecretKey, cfg.SecretKey, cfg.SecretKey, &cfg)
	for _, v := range []any{cfg, cfg.SecretKey, map[string]any{"secret": cfg.SecretKey}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(b)
	}
	slog.New(slog.NewJSONHandler(&out, nil)).Info("settings", "cfg", cfg, "secret", cfg.SecretKey)
	slog.New(slog.NewTextHandler(&out, nil)).Info("settings", "cfg", cfg, "secret", cfg.SecretKey)
	c, err := New(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&out, "%v %+v %#v\n", c, c, c)
	bad := cfg
	bad.SecretKey = NewSecret(testSecret + "\x00")
	_, err = New(bad, nil, nil)
	fmt.Fprintf(&out, "%v %+v\n", err, err)

	if s := out.String(); strings.Contains(s, testSecret) || !strings.Contains(s, "[hidden]") {
		t.Fatalf("output shows the secret, or never says it is hidden:\n%s", s)
	}
	if strings.Contains(out.String(), "secretKey\":") {
		t.Error("the secret is marshalled as a JSON field")
	}
	if cfg.SecretKey.Reveal() != testSecret || !cfg.SecretKey.IsSet() {
		t.Error("Reveal lost the secret")
	}
	if s := NewSecret(""); s.IsSet() || s.String() != "" || s.Reveal() != "" {
		t.Errorf("empty secret %v", s)
	}
}

func TestErrorParams(t *testing.T) {
	e := &Error{Kind: KindClockSkew, Op: opUpload, Name: testName, Status: 403, Code: "RequestTimeTooSkewed", Skew: -90 * time.Second}
	want := map[string]string{"op": "upload", "name": testName, "status": "403", "code": "RequestTimeTooSkewed", "skew": "90 seconds", "direction": "ahead"}
	if got := e.Params(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Params = %v, want %v", got, want)
	}
	if got := (&Error{Op: opList}).Params(); len(got) != 1 || got["op"] != "list" {
		t.Errorf("Params = %v", got)
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                             "0 seconds",
		time.Second:                   "1 second",
		1500 * time.Millisecond:       "2 seconds",
		90 * time.Second:              "90 seconds",
		2 * time.Minute:               "2 minutes",
		-20 * time.Minute:             "20 minutes",
		119 * time.Minute:             "119 minutes",
		2 * time.Hour:                 "2 hours",
		47 * time.Hour:                "47 hours",
		48 * time.Hour:                "2 days",
		10*24*time.Hour + 5*time.Hour: "10 days",
	} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
