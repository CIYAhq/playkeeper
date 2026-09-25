package offsite

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type vectorContext struct {
	Credentials struct {
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
	} `json:"credentials"`
	Normalize bool      `json:"normalize"`
	Region    string    `json:"region"`
	Service   string    `json:"service"`
	SignBody  bool      `json:"sign_body"`
	Timestamp time.Time `json:"timestamp"`
}

// parseVectorRequest reads a request.txt: a request line whose target may
// contain spaces, headers (continuation lines start with whitespace), and
// the body after an empty line.
func parseVectorRequest(t *testing.T, raw string) (method, target string, h http.Header, body string) {
	t.Helper()
	head, body, _ := strings.Cut(raw, "\n\n")
	lines := strings.Split(head, "\n")
	first := lines[0]
	sp1, sp2 := strings.IndexByte(first, ' '), strings.LastIndexByte(first, ' ')
	if sp1 < 0 || sp2 <= sp1 {
		t.Fatalf("bad request line %q", first)
	}
	method, target = first[:sp1], first[sp1+1:sp2]
	h = http.Header{}
	last := ""
	for _, l := range lines[1:] {
		switch {
		case l == "":
		case l[0] == ' ' || l[0] == '\t':
			vs := h[last]
			vs[len(vs)-1] += " " + strings.TrimSpace(l)
		default:
			name, value, ok := strings.Cut(l, ":")
			if !ok {
				t.Fatalf("bad header line %q", l)
			}
			last = http.CanonicalHeaderKey(name)
			h[last] = append(h[last], value)
		}
	}
	return method, target, h, body
}

func readVectorFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimRight(string(b), "\n")
}

func TestSigV4Vectors(t *testing.T) {
	dirs, err := filepath.Glob("testdata/sigv4/*/context.json")
	if err != nil || len(dirs) != 28 {
		t.Fatalf("found %d vectors (%v), want 28", len(dirs), err)
	}
	for _, ctxFile := range dirs {
		dir := filepath.Dir(ctxFile)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			var vc vectorContext
			if err := json.Unmarshal([]byte(readVectorFile(t, dir, "context.json")), &vc); err != nil {
				t.Fatal(err)
			}
			if vc.Normalize && strings.Contains(filepath.Base(dir), "unnormalized") {
				t.Fatal("an unnormalized vector asks for normalizing")
			}
			raw, err := os.ReadFile(filepath.Join(dir, "request.txt"))
			if err != nil {
				t.Fatal(err)
			}
			method, target, h, body := parseVectorRequest(t, string(raw))
			sum := sha256.Sum256([]byte(body))
			payload := hex.EncodeToString(sum[:])
			h.Set("X-Amz-Date", vc.Timestamp.UTC().Format(amzDate))
			if vc.SignBody {
				h.Set("X-Amz-Content-Sha256", payload)
			}
			rawPath, rawQuery, _ := strings.Cut(target, "?")
			path, err := url.PathUnescape(rawPath)
			if err != nil {
				t.Fatal(err)
			}
			query, err := url.ParseQuery(rawQuery)
			if err != nil {
				t.Fatal(err)
			}

			creq, _ := canonicalRequest(method, path, query, h, payload)
			if want := readVectorFile(t, dir, "header-canonical-request.txt"); creq != want {
				t.Fatalf("canonical request:\n%s\nwant:\n%s", creq, want)
			}
			sts := stringToSign(vc.Timestamp, scope(vc.Timestamp, vc.Region, vc.Service), creq)
			if want := readVectorFile(t, dir, "header-string-to-sign.txt"); sts != want {
				t.Fatalf("string to sign:\n%s\nwant:\n%s", sts, want)
			}
			sig := signature(NewSecret(vc.Credentials.SecretAccessKey), vc.Timestamp, vc.Region, vc.Service, sts)
			if want := readVectorFile(t, dir, "header-signature.txt"); sig != want {
				t.Fatalf("signature %s, want %s", sig, want)
			}
		})
	}
}

// The four examples in the S3 documentation's "Signature calculations for
// the Authorization header", signed the way requests are sent.
func TestSigV4S3Examples(t *testing.T) {
	creds := credentials{accessKeyID: "AKIAIOSFODNN7EXAMPLE", secret: NewSecret("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"), region: "us-east-1", service: "s3"}
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	const cred = "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, "
	for _, tc := range []struct {
		name, method, url string
		header            map[string]string
		payload           string
		wantURI           string
		wantAuth          string
	}{
		{
			name: "get object", method: "GET", url: "https://examplebucket.s3.amazonaws.com/test.txt",
			header: map[string]string{"Range": "bytes=0-9"}, payload: emptySHA256, wantURI: "/test.txt",
			wantAuth: cred + "SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		{
			name: "put object", method: "PUT", url: "https://examplebucket.s3.amazonaws.com/test$file.text",
			header:  map[string]string{"Date": "Fri, 24 May 2013 00:00:00 GMT", "X-Amz-Storage-Class": "REDUCED_REDUNDANCY"},
			payload: "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072", wantURI: "/test%24file.text",
			wantAuth: cred + "SignedHeaders=date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class, Signature=98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
		},
		{
			name: "get bucket lifecycle", method: "GET", url: "https://examplebucket.s3.amazonaws.com/?lifecycle",
			payload: emptySHA256, wantURI: "/?lifecycle=",
			wantAuth: cred + "SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
		},
		{
			name: "list objects", method: "GET", url: "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J",
			payload: emptySHA256, wantURI: "/?max-keys=2&prefix=J",
			wantAuth: cred + "SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := http.NewRequest(tc.method, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.header {
				r.Header.Set(k, v)
			}
			r.Header.Set("User-Agent", "not signed")
			creds.sign(r, tc.payload, at)
			if got := r.Header.Get("Authorization"); got != tc.wantAuth {
				t.Errorf("Authorization:\n%s\nwant:\n%s", got, tc.wantAuth)
			}
			if got := r.URL.RequestURI(); got != tc.wantURI {
				t.Errorf("sent URI %q, want %q", got, tc.wantURI)
			}
			if got := r.Header.Get("X-Amz-Date"); got != "20130524T000000Z" {
				t.Errorf("X-Amz-Date %q", got)
			}
		})
	}
}

func TestURIEncode(t *testing.T) {
	for _, tc := range []struct{ in, path, value string }{
		{"abc-._~XYZ09", "abc-._~XYZ09", "abc-._~XYZ09"},
		{"a b", "a%20b", "a%20b"},
		{"a/b", "a/b", "a%2Fb"},
		{"a+b=c&d", "a%2Bb%3Dc%26d", "a%2Bb%3Dc%26d"},
		{"*$", "%2A%24", "%2A%24"},
		{"ሴ", "%E1%88%B4", "%E1%88%B4"},
		{"50%", "50%25", "50%25"},
	} {
		if got := uriEncode(tc.in, false); got != tc.path {
			t.Errorf("uriEncode(%q, path) = %q, want %q", tc.in, got, tc.path)
		}
		if got := uriEncode(tc.in, true); got != tc.value {
			t.Errorf("uriEncode(%q, value) = %q, want %q", tc.in, got, tc.value)
		}
	}
}

func TestCanonicalQueryOrder(t *testing.T) {
	q := url.Values{"Param": {"b", "a"}, "Param-3": {"x"}, "uploads": {""}, "prefix": {"a b/c"}}
	want := "Param=a&Param=b&Param-3=x&prefix=a%20b%2Fc&uploads="
	if got := canonicalQuery(q); got != want {
		t.Errorf("canonicalQuery = %q, want %q", got, want)
	}
}
