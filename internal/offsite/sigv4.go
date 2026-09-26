package offsite

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	algorithm   = "AWS4-HMAC-SHA256"
	amzDate     = "20060102T150405Z"
	emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

type credentials struct {
	accessKeyID string
	secret      Secret
	region      string
	service     string
}

// uriEncode percent-encodes every byte except the unreserved characters
// (A-Z a-z 0-9 - . _ ~), and keeps "/" when encodeSlash is false, as
// Signature Version 4 requires.
func uriEncode(s string, encodeSlash bool) string {
	const hexUpper = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' || (c == '/' && !encodeSlash) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexUpper[c>>4])
		b.WriteByte(hexUpper[c&15])
	}
	return b.String()
}

// canonicalQuery encodes q sorted by name, then value; a parameter without
// a value is written as "name=".
func canonicalQuery(q url.Values) string {
	var pairs []string
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	slices.SortFunc(pairs, func(a, b string) int {
		ak, av, _ := strings.Cut(a, "=")
		bk, bv, _ := strings.Cut(b, "=")
		if c := strings.Compare(ak, bk); c != 0 {
			return c
		}
		return strings.Compare(av, bv)
	})
	return strings.Join(pairs, "&")
}

// canonicalRequest is the canonical request for S3: the path (decoded) is
// URI-encoded once and not normalized, and every header in h is signed.
// It returns the request and the signed header names.
func canonicalRequest(method, path string, query url.Values, h http.Header, payloadHash string) (string, string) {
	lower := map[string][]string{}
	for name, vs := range h {
		n := strings.ToLower(name)
		lower[n] = append(lower[n], vs...)
	}
	names := make([]string, 0, len(lower))
	for n := range lower {
		names = append(names, n)
	}
	slices.Sort(names)
	var headers strings.Builder
	for _, n := range names {
		vals := make([]string, len(lower[n]))
		for i, v := range lower[n] {
			vals[i] = strings.Join(strings.Fields(v), " ")
		}
		headers.WriteString(n + ":" + strings.Join(vals, ",") + "\n")
	}
	if path == "" {
		path = "/"
	}
	signed := strings.Join(names, ";")
	return strings.Join([]string{method, uriEncode(path, false), canonicalQuery(query), headers.String(), signed, payloadHash}, "\n"), signed
}

func scope(t time.Time, region, service string) string {
	return t.UTC().Format("20060102") + "/" + region + "/" + service + "/aws4_request"
}

func stringToSign(t time.Time, scope, creq string) string {
	sum := sha256.Sum256([]byte(creq))
	return algorithm + "\n" + t.UTC().Format(amzDate) + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func signature(secret Secret, t time.Time, region, service, sts string) string {
	k := hmacSHA256([]byte("AWS4"+secret.Reveal()), t.UTC().Format("20060102"))
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	return hex.EncodeToString(hmacSHA256(k, sts))
}

// signedHeader reports whether a request header is signed: the ones S3
// checks, and never those a proxy or the transport may change.
func signedHeader(name string) bool {
	switch n := strings.ToLower(name); n {
	case "content-md5", "content-type", "range", "date":
		return true
	default:
		return strings.HasPrefix(n, "x-amz-")
	}
}

// sign sets X-Amz-Date, X-Amz-Content-Sha256 and Authorization on r.
// payloadHash is the hex SHA-256 of the body. r.URL.Path is the decoded
// path; sign sets RawPath and RawQuery to exactly what it signs.
func (c credentials) sign(r *http.Request, payloadHash string, now time.Time) {
	now = now.UTC()
	r.Header.Set("X-Amz-Date", now.Format(amzDate))
	r.Header.Set("X-Amz-Content-Sha256", payloadHash)
	h := http.Header{"Host": {r.URL.Host}}
	for name, vs := range r.Header {
		if signedHeader(name) {
			h[name] = vs
		}
	}
	q := r.URL.Query()
	r.URL.RawPath = uriEncode(r.URL.Path, false)
	r.URL.RawQuery = canonicalQuery(q)
	creq, signed := canonicalRequest(r.Method, r.URL.Path, q, h, payloadHash)
	sc := scope(now, c.region, c.service)
	sig := signature(c.secret, now, c.region, c.service, stringToSign(now, sc, creq))
	r.Header.Set("Authorization", algorithm+" Credential="+c.accessKeyID+"/"+sc+", SignedHeaders="+signed+", Signature="+sig)
}
