package offsite

import (
	"bytes"
	"cmp"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"maps"
	"math/big"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testAccess = "AKIDPLAYKEEPERTEST"
	testSecret = "sEcReT/Key+ForTests0123456789abcdefghij"
	testBucket = "backups"
	testPrefix = "playkeeper/survival/"
)

var testTLS = sync.OnceValues(func() (tls.Certificate, *x509.CertPool) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "s3.test"},
		DNSNames:              []string{"s3.test", "*.s3.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
})

type fakeObject struct {
	data      []byte
	etag      string
	checksum  string
	meta      string
	modified  time.Time
	versionID string
	marker    bool
}

type fakePart struct {
	data     []byte
	etag     string
	checksum string
}

type fakeUpload struct {
	key       string
	checksums bool
	meta      string
	parts     map[int]fakePart
	initiated time.Time
}

// fakeFailure is what the fail hook makes the fake answer instead.
type fakeFailure struct {
	status    int
	code      string
	message   string
	header    http.Header
	drop      bool   // close the connection without an answer
	dropAfter bool   // carry out the request, then close the connection without an answer
	half      bool   // carry out the request, send half the answer's body, then close the connection
	in200     bool   // answer 200 with the <Error> in the body
	hang      bool   // answer nothing until the client gives up
	raw       string // answer status (200 if unset) with this body
}

// fakeS3 is an S3 service in memory. It checks every request's Signature
// Version 4 from what arrived on the wire, the payload SHA-256, Content-MD5
// and x-amz-checksum-sha256, like the real services do.
type fakeS3 struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	bucket string
	access string
	secret string
	region string
	now    func() time.Time

	objects  map[string]*fakeObject
	versions map[string][]*fakeObject // older versions and delete markers
	uploads  map[string]*fakeUpload
	seq      int

	versioning      bool
	noChecksums     bool // refuse x-amz-checksum headers, like a service without them
	ignoreChecksums bool // accept them but never store or return checksums
	opaqueETags     bool // ETags that are not MD5s
	locked          bool // refuse to delete versions, like Object Lock
	lieChecksum     bool // report a wrong checksum on HEAD
	noLength        bool // send GetObject bodies without Content-Length
	pageSize        int
	deny            map[string]bool
	fail            func(op string, r *http.Request) *fakeFailure
	log             []string
}

func newFake(t *testing.T) *fakeS3 {
	t.Helper()
	f := &fakeS3{t: t, bucket: testBucket, access: testAccess, secret: testSecret, region: "us-east-1", now: time.Now,
		objects: map[string]*fakeObject{}, versions: map[string][]*fakeObject{}, uploads: map[string]*fakeUpload{}, deny: map[string]bool{}}
	cert, _ := testTLS()
	f.srv = httptest.NewUnstartedServer(f)
	f.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	f.srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

// httpClient reaches the fake whatever host name a request is for.
func (f *fakeS3) httpClient() *http.Client {
	_, roots := testTLS()
	addr := f.srv.Listener.Addr().String()
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		},
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}}
}

func testConfig() S3Config {
	return S3Config{Provider: "minio", Endpoint: "https://s3.test", Region: "us-east-1", Bucket: testBucket, Prefix: testPrefix,
		AccessKeyID: testAccess, SecretKey: NewSecret(testSecret), PathStyle: true}
}

// client returns a client for the fake with small parts and no waiting
// between attempts; waits collects the waits it would have made.
func (f *fakeS3) client(mod func(*S3Config)) (*s3Client, *[]time.Duration) {
	f.t.Helper()
	cfg := testConfig()
	if mod != nil {
		mod(&cfg)
	}
	c, err := newS3(cfg, f.httpClient(), nil)
	if err != nil {
		f.t.Fatalf("newS3: %v", err)
	}
	return c, quick(c)
}

// quick gives c small parts and no waiting between attempts, and returns
// the waits it would have made.
func quick(c *s3Client) *[]time.Duration {
	c.partSize, c.minPart = 64<<10, 1
	var mu sync.Mutex
	waits := &[]time.Duration{}
	c.sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		*waits = append(*waits, d)
		mu.Unlock()
		return ctx.Err()
	}
	return waits
}

func (f *fakeS3) ops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.log)
}

func (f *fakeS3) count(op string) int {
	n := 0
	for _, l := range f.ops() {
		if strings.HasPrefix(l, op+" ") {
			n++
		}
	}
	return n
}

// put stores an object directly, as if something else had uploaded it.
func (f *fakeS3) put(key string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.store(key, &fakeObject{data: data, etag: f.etag(data)})
}

func (f *fakeS3) object(key string) *fakeObject {
	f.mu.Lock()
	defer f.mu.Unlock()
	if o := f.objects[key]; o != nil && !o.marker {
		return o
	}
	return nil
}

func (f *fakeS3) etag(data []byte) string {
	if f.opaqueETags {
		s := sha256.Sum256(append([]byte("opaque"), data...))
		return hex.EncodeToString(s[:16])
	}
	m := md5.Sum(data)
	return hex.EncodeToString(m[:])
}

func (f *fakeS3) store(key string, o *fakeObject) {
	if o.modified.IsZero() {
		o.modified = f.now()
	}
	if f.versioning {
		f.seq++
		o.versionID = "v" + strconv.Itoa(f.seq)
		if cur := f.objects[key]; cur != nil {
			f.versions[key] = append(f.versions[key], cur)
		}
	}
	f.objects[key] = o
}

type fakeError struct {
	status int
	code   string
	msg    string
	extra  string
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Date", f.now().UTC().Format(http.TimeFormat))
	key, bucketOK := f.route(r)
	op := opOf(r, key)
	var failure *fakeFailure
	if f.fail != nil {
		failure = f.fail(op, r)
	}
	switch {
	case failure == nil || failure.dropAfter || failure.half:
	case failure.raw != "":
		for k, vs := range failure.header {
			w.Header()[k] = vs
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(cmp.Or(failure.status, http.StatusOK))
		_, _ = io.WriteString(w, failure.raw)
		return
	case failure.hang:
		f.mu.Unlock()
		<-r.Context().Done()
		f.mu.Lock()
		return
	case failure.drop:
		drop(w)
		return
	default:
		for k, vs := range failure.header {
			w.Header()[k] = vs
		}
		status := failure.status
		if failure.in200 {
			status = http.StatusOK
		}
		writeError(w, fakeError{status: status, code: failure.code, msg: failure.message})
		return
	}
	if e := f.authenticate(r); e != nil {
		writeError(w, *e)
		return
	}
	if !bucketOK {
		writeError(w, fakeError{http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist", ""})
		return
	}
	if f.deny[op] {
		writeError(w, fakeError{http.StatusForbidden, "AccessDenied", "Access Denied", ""})
		return
	}
	if e := f.checkBody(r, body); e != nil {
		writeError(w, *e)
		return
	}
	f.log = append(f.log, op+" "+key)
	if failure != nil && (failure.dropAfter || failure.half) {
		rec := httptest.NewRecorder()
		f.serve(rec, r, op, key, body)
		if failure.half {
			maps.Copy(w.Header(), rec.Header())
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes()[:rec.Body.Len()/2])
		}
		drop(w)
		return
	}
	f.serve(w, r, op, key, body)
}

func drop(w http.ResponseWriter) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err == nil {
		conn.Close()
	}
}

func (f *fakeS3) route(r *http.Request) (string, bool) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	if b, ok := strings.CutSuffix(host, ".s3.test"); ok {
		return strings.TrimPrefix(r.URL.Path, "/"), b == f.bucket
	}
	b, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	return key, b == f.bucket
}

func opOf(r *http.Request, key string) string {
	q := r.URL.Query()
	switch r.Method {
	case http.MethodPut:
		if q.Has("uploadId") {
			return "UploadPart"
		}
		return "PutObject"
	case http.MethodPost:
		if q.Has("uploads") {
			return "CreateMultipartUpload"
		}
		return "CompleteMultipartUpload"
	case http.MethodDelete:
		switch {
		case q.Has("uploadId"):
			return "AbortMultipartUpload"
		case q.Has("versionId"):
			return "DeleteObjectVersion"
		}
		return "DeleteObject"
	case http.MethodHead:
		return "HeadObject"
	}
	switch {
	case key == "" && q.Has("uploads"):
		return "ListMultipartUploads"
	case key == "" && q.Has("versions"):
		return "ListObjectVersions"
	case key == "":
		return "ListObjectsV2"
	case q.Has("uploadId"):
		return "ListParts"
	}
	return "GetObject"
}

// authenticate checks the request's signature the way a service does:
// from the path and query as sent, and the signed headers as received.
func (f *fakeS3) authenticate(r *http.Request) *fakeError {
	denied := func(code, msg string) *fakeError { return &fakeError{http.StatusForbidden, code, msg, ""} }
	for name, vs := range r.Header {
		if strings.Contains(strings.Join(vs, " "), f.secret) {
			f.t.Errorf("header %s carries the secret key", name)
		}
	}
	if strings.Contains(r.RequestURI, f.secret) {
		f.t.Errorf("the URL carries the secret key")
	}
	rest, ok := strings.CutPrefix(r.Header.Get("Authorization"), algorithm+" ")
	if !ok {
		return denied("AccessDenied", "Missing or unsupported Authorization")
	}
	fields := map[string]string{}
	for _, part := range strings.Split(rest, ", ") {
		k, v, _ := strings.Cut(part, "=")
		fields[k] = v
	}
	cred := strings.Split(fields["Credential"], "/")
	if len(cred) != 5 || cred[3] != "s3" || cred[4] != "aws4_request" {
		return &fakeError{http.StatusBadRequest, "AuthorizationHeaderMalformed", "The authorization header is malformed", ""}
	}
	if cred[0] != f.access {
		return denied("InvalidAccessKeyId", "The AWS Access Key Id you provided does not exist in our records.")
	}
	if cred[2] != f.region {
		return &fakeError{http.StatusBadRequest, "AuthorizationHeaderMalformed",
			fmt.Sprintf("The authorization header is malformed; the region '%s' is wrong; expecting '%s'", cred[2], f.region),
			"<Region>" + f.region + "</Region>"}
	}
	t, err := time.Parse(amzDate, r.Header.Get("X-Amz-Date"))
	if err != nil || cred[1] != t.Format("20060102") {
		return denied("AccessDenied", "Bad X-Amz-Date")
	}
	if d := f.now().Sub(t); d > 15*time.Minute || d < -15*time.Minute {
		return &fakeError{http.StatusForbidden, "RequestTimeTooSkewed", "The difference between the request time and the current time is too large.",
			"<RequestTime>" + t.Format(amzDate) + "</RequestTime><ServerTime>" + f.now().UTC().Format(time.RFC3339) + "</ServerTime>"}
	}
	signed := strings.Split(fields["SignedHeaders"], ";")
	for _, need := range []string{"host", "x-amz-date", "x-amz-content-sha256"} {
		if !slices.Contains(signed, need) {
			return denied("AccessDenied", "Header "+need+" is not signed")
		}
	}
	for name := range r.Header {
		n := strings.ToLower(name)
		if (strings.HasPrefix(n, "x-amz-") || n == "content-md5") && !slices.Contains(signed, n) {
			return denied("AccessDenied", "Header "+n+" is not signed")
		}
	}
	h := http.Header{}
	for _, n := range signed {
		if n == "host" {
			h["host"] = []string{r.Host}
			continue
		}
		vs := r.Header.Values(n)
		if len(vs) == 0 {
			return denied("AccessDenied", "Signed header "+n+" is missing")
		}
		h[n] = vs
	}
	rawPath, rawQuery, _ := strings.Cut(r.RequestURI, "?")
	path, err := url.PathUnescape(rawPath)
	if err != nil || uriEncode(path, false) != rawPath {
		f.t.Errorf("path %q is not sent canonically encoded", rawPath)
		return denied("SignatureDoesNotMatch", "Path not canonical")
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil || canonicalQuery(q) != rawQuery {
		f.t.Errorf("query %q is not sent canonically encoded", rawQuery)
		return denied("SignatureDoesNotMatch", "Query not canonical")
	}
	creq, _ := canonicalRequest(r.Method, path, q, h, r.Header.Get("X-Amz-Content-Sha256"))
	want := signature(NewSecret(f.secret), t, f.region, "s3", stringToSign(t, strings.Join(cred[1:], "/"), creq))
	if !hmac.Equal([]byte(want), []byte(fields["Signature"])) {
		return denied("SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided. Check your key and signing method.")
	}
	return nil
}

func (f *fakeS3) checkBody(r *http.Request, body []byte) *fakeError {
	sum := sha256.Sum256(body)
	if r.Header.Get("X-Amz-Content-Sha256") != hex.EncodeToString(sum[:]) {
		return &fakeError{http.StatusBadRequest, "XAmzContentSHA256Mismatch", "The provided 'x-amz-content-sha256' header does not match what was computed.", ""}
	}
	if v := r.Header.Get("Content-Md5"); v != "" {
		if m := md5.Sum(body); v != b64(m[:]) {
			return &fakeError{http.StatusBadRequest, "BadDigest", "The Content-MD5 you specified did not match what we received.", ""}
		}
	}
	for name := range r.Header {
		if f.noChecksums && strings.HasPrefix(name, "X-Amz-Checksum-") {
			return &fakeError{http.StatusBadRequest, "InvalidArgument", "x-amz-checksum-* headers are not supported", ""}
		}
	}
	if v := r.Header.Get("X-Amz-Checksum-Sha256"); v != "" && v != b64(sum[:]) {
		return &fakeError{http.StatusBadRequest, "BadDigest", "The SHA256 you specified did not match the calculated checksum.", ""}
	}
	return nil
}

func writeError(w http.ResponseWriter, e fakeError) {
	var msg bytes.Buffer
	_ = xml.EscapeText(&msg, []byte(e.msg))
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(e.status)
	fmt.Fprintf(w, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<Error><Code>%s</Code><Message>%s</Message>%s<RequestId>1</RequestId></Error>", e.code, msg.String(), e.extra)
}

func writeXML(w http.ResponseWriter, v any) {
	b, err := xml.Marshal(v)
	if err != nil {
		panic(err)
	}
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(b)
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request, op, key string, body []byte) {
	q := r.URL.Query()
	switch op {
	case "PutObject":
		o := &fakeObject{data: body, etag: f.etag(body), meta: r.Header.Get(metaSHA256)}
		if cs := r.Header.Get("X-Amz-Checksum-Sha256"); cs != "" && !f.ignoreChecksums {
			o.checksum = cs
			w.Header().Set("X-Amz-Checksum-Sha256", cs)
		}
		f.store(key, o)
		w.Header().Set("ETag", `"`+o.etag+`"`)
	case "HeadObject", "GetObject":
		o := f.objects[key]
		if o == nil || o.marker {
			writeError(w, fakeError{http.StatusNotFound, "NoSuchKey", "The specified key does not exist.", ""})
			return
		}
		w.Header().Set("ETag", `"`+o.etag+`"`)
		w.Header().Set("Last-Modified", o.modified.UTC().Format(http.TimeFormat))
		if o.meta != "" {
			w.Header().Set(metaSHA256, o.meta)
		}
		if r.Header.Get("X-Amz-Checksum-Mode") == "ENABLED" && o.checksum != "" {
			cs := o.checksum
			if f.lieChecksum {
				s := sha256.Sum256(nil)
				cs = b64(s[:])
			}
			w.Header().Set("X-Amz-Checksum-Sha256", cs)
		}
		data := o.data
		if r.Header.Get("Range") == "bytes=0-0" && len(data) > 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(data)))
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			if op == "GetObject" {
				_, _ = w.Write(data[:1])
			}
			return
		}
		if op == "HeadObject" || !f.noLength {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		}
		if op == "GetObject" {
			_, _ = w.Write(data)
		}
	case "DeleteObject":
		if cur := f.objects[key]; cur != nil && f.versioning {
			f.seq++
			f.versions[key] = append(f.versions[key], cur)
			f.objects[key] = &fakeObject{marker: true, versionID: "v" + strconv.Itoa(f.seq), modified: f.now()}
		} else if !f.versioning {
			delete(f.objects, key)
		}
		w.WriteHeader(http.StatusNoContent)
	case "DeleteObjectVersion":
		if f.locked {
			writeError(w, fakeError{http.StatusForbidden, "AccessDenied", "Access Denied because object protected by object lock.", ""})
			return
		}
		id := q.Get("versionId")
		if cur := f.objects[key]; cur != nil && cur.versionID == id {
			delete(f.objects, key)
		}
		f.versions[key] = slices.DeleteFunc(f.versions[key], func(o *fakeObject) bool { return o.versionID == id })
		if len(f.versions[key]) == 0 {
			delete(f.versions, key)
		}
		w.WriteHeader(http.StatusNoContent)
	case "ListObjectsV2":
		f.listObjects(w, q)
	case "ListObjectVersions":
		f.listVersions(w, q)
	case "CreateMultipartUpload":
		f.seq++
		id := fmt.Sprintf("up-%d~a.b_c+d/e=", f.seq)
		u := &fakeUpload{key: key, meta: r.Header.Get(metaSHA256), parts: map[int]fakePart{}, initiated: f.now()}
		if r.Header.Get("X-Amz-Checksum-Algorithm") == "SHA256" && !f.ignoreChecksums {
			u.checksums = true
			w.Header().Set("X-Amz-Checksum-Algorithm", "SHA256")
		}
		f.uploads[id] = u
		writeXML(w, struct {
			XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
			Bucket   string
			Key      string
			UploadID string `xml:"UploadId"`
		}{Bucket: f.bucket, Key: key, UploadID: id})
	case "UploadPart":
		u := f.uploads[q.Get("uploadId")]
		n, err := strconv.Atoi(q.Get("partNumber"))
		switch {
		case u == nil || u.key != key:
			writeError(w, fakeError{http.StatusNotFound, "NoSuchUpload", "The specified upload does not exist.", ""})
			return
		case err != nil || n < 1 || n > maxParts:
			writeError(w, fakeError{http.StatusBadRequest, "InvalidArgument", "Part number must be an integer between 1 and 10000, inclusive", ""})
			return
		case u.checksums && r.Header.Get("X-Amz-Checksum-Sha256") == "":
			writeError(w, fakeError{http.StatusBadRequest, "InvalidRequest", "The upload was created using a sha256 checksum. The part must include the checksum.", ""})
			return
		}
		p := fakePart{data: body, etag: f.etag(body)}
		if u.checksums {
			p.checksum = r.Header.Get("X-Amz-Checksum-Sha256")
			w.Header().Set("X-Amz-Checksum-Sha256", p.checksum)
		}
		u.parts[n] = p
		w.Header().Set("ETag", `"`+p.etag+`"`)
	case "ListParts":
		f.listParts(w, q, key)
	case "CompleteMultipartUpload":
		f.completeUpload(w, q, key, body)
	case "AbortMultipartUpload":
		if u := f.uploads[q.Get("uploadId")]; u == nil || u.key != key {
			writeError(w, fakeError{http.StatusNotFound, "NoSuchUpload", "The specified upload does not exist.", ""})
			return
		}
		delete(f.uploads, q.Get("uploadId"))
		w.WriteHeader(http.StatusNoContent)
	case "ListMultipartUploads":
		type upload struct {
			Key       string
			UploadID  string `xml:"UploadId"`
			Initiated string
		}
		var ups []upload
		for id, u := range f.uploads {
			if strings.HasPrefix(u.key, q.Get("prefix")) {
				ups = append(ups, upload{u.key, id, u.initiated.UTC().Format("2006-01-02T15:04:05.000Z")})
			}
		}
		slices.SortFunc(ups, func(a, b upload) int { return strings.Compare(a.Key+a.UploadID, b.Key+b.UploadID) })
		writeXML(w, struct {
			XMLName     xml.Name `xml:"ListMultipartUploadsResult"`
			Bucket      string
			IsTruncated bool
			Uploads     []upload `xml:"Upload"`
		}{Bucket: f.bucket, Uploads: ups})
	default:
		writeError(w, fakeError{http.StatusNotImplemented, "NotImplemented", "Not implemented by the fake", ""})
	}
}

func (f *fakeS3) listObjects(w http.ResponseWriter, q url.Values) {
	prefix := q.Get("prefix")
	var keys []string
	for k, o := range f.objects {
		rest, ok := strings.CutPrefix(k, prefix)
		if ok && !o.marker && !(q.Get("delimiter") == "/" && strings.Contains(rest, "/")) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	if tok := q.Get("continuation-token"); tok != "" {
		i, _ := slices.BinarySearch(keys, tok)
		for i < len(keys) && keys[i] <= tok {
			i++
		}
		keys = keys[i:]
	}
	limit, _ := strconv.Atoi(q.Get("max-keys"))
	if f.pageSize > 0 && (limit == 0 || f.pageSize < limit) {
		limit = f.pageSize
	}
	type content struct {
		Key          string
		LastModified string
		ETag         string
		Size         int
	}
	res := struct {
		XMLName               xml.Name `xml:"ListBucketResult"`
		Name                  string
		Prefix                string
		IsTruncated           bool
		EncodingType          string `xml:",omitempty"`
		NextContinuationToken string `xml:",omitempty"`
		Contents              []content
	}{Name: f.bucket, Prefix: prefix, EncodingType: q.Get("encoding-type")}
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
		res.IsTruncated, res.NextContinuationToken = true, keys[limit-1]
	}
	for _, k := range keys {
		o := f.objects[k]
		name := k
		if res.EncodingType == "url" {
			name = url.QueryEscape(k)
		}
		res.Contents = append(res.Contents, content{name, o.modified.UTC().Format("2006-01-02T15:04:05.000Z"), `"` + o.etag + `"`, len(o.data)})
	}
	writeXML(w, res)
}

func (f *fakeS3) listVersions(w http.ResponseWriter, q url.Values) {
	type version struct {
		Key       string
		VersionID string `xml:"VersionId"`
		IsLatest  bool
	}
	var res struct {
		XMLName     xml.Name `xml:"ListVersionsResult"`
		IsTruncated bool
		Versions    []version `xml:"Version"`
		Markers     []version `xml:"DeleteMarker"`
	}
	add := func(k string, o *fakeObject, latest bool) {
		id := o.versionID
		if id == "" {
			id = "null"
		}
		if o.marker {
			res.Markers = append(res.Markers, version{k, id, latest})
		} else {
			res.Versions = append(res.Versions, version{k, id, latest})
		}
	}
	for k, o := range f.objects {
		if strings.HasPrefix(k, q.Get("prefix")) {
			add(k, o, true)
		}
	}
	for k, vs := range f.versions {
		if strings.HasPrefix(k, q.Get("prefix")) {
			for _, o := range vs {
				add(k, o, false)
			}
		}
	}
	writeXML(w, res)
}

func (f *fakeS3) listParts(w http.ResponseWriter, q url.Values, key string) {
	u := f.uploads[q.Get("uploadId")]
	if u == nil || u.key != key {
		writeError(w, fakeError{http.StatusNotFound, "NoSuchUpload", "The specified upload does not exist.", ""})
		return
	}
	var nums []int
	marker, _ := strconv.Atoi(q.Get("part-number-marker"))
	for n := range u.parts {
		if n > marker {
			nums = append(nums, n)
		}
	}
	slices.Sort(nums)
	type part struct {
		PartNumber     int
		ETag           string
		Size           int
		ChecksumSHA256 string `xml:",omitempty"`
	}
	res := struct {
		XMLName              xml.Name `xml:"ListPartsResult"`
		IsTruncated          bool
		NextPartNumberMarker int
		Parts                []part `xml:"Part"`
	}{}
	limit := 1000
	if f.pageSize > 0 {
		limit = f.pageSize
	}
	if len(nums) > limit {
		nums = nums[:limit]
		res.IsTruncated, res.NextPartNumberMarker = true, nums[limit-1]
	}
	for _, n := range nums {
		p := u.parts[n]
		res.Parts = append(res.Parts, part{n, `"` + p.etag + `"`, len(p.data), p.checksum})
	}
	writeXML(w, res)
}

func (f *fakeS3) completeUpload(w http.ResponseWriter, q url.Values, key string, body []byte) {
	id := q.Get("uploadId")
	u := f.uploads[id]
	if u == nil || u.key != key {
		writeError(w, fakeError{http.StatusNotFound, "NoSuchUpload", "The specified upload does not exist.", ""})
		return
	}
	var doc struct {
		Parts []struct {
			PartNumber     int
			ETag           string
			ChecksumSHA256 string
		} `xml:"Part"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil || len(doc.Parts) == 0 {
		writeError(w, fakeError{http.StatusBadRequest, "MalformedXML", "The XML you provided was not well-formed", ""})
		return
	}
	var data []byte
	shas, md5s := sha256.New(), md5.New()
	for i, p := range doc.Parts {
		got, ok := u.parts[p.PartNumber]
		switch {
		case i > 0 && p.PartNumber <= doc.Parts[i-1].PartNumber:
			writeError(w, fakeError{http.StatusBadRequest, "InvalidPartOrder", "The list of parts was not in ascending order.", ""})
			return
		case !ok || normETag(p.ETag) != got.etag || u.checksums && p.ChecksumSHA256 != got.checksum:
			writeError(w, fakeError{http.StatusBadRequest, "InvalidPart", "One or more of the specified parts could not be found.", ""})
			return
		}
		data = append(data, got.data...)
		s, m := sha256.Sum256(got.data), md5.Sum(got.data)
		shas.Write(s[:])
		md5s.Write(m[:])
	}
	o := &fakeObject{data: data, meta: u.meta, etag: hex.EncodeToString(md5s.Sum(nil)) + "-" + strconv.Itoa(len(doc.Parts))}
	if f.opaqueETags {
		o.etag = f.etag(data)
	}
	if u.checksums {
		o.checksum = b64(shas.Sum(nil)) + "-" + strconv.Itoa(len(doc.Parts))
	}
	f.store(key, o)
	delete(f.uploads, id)
	writeXML(w, struct {
		XMLName        xml.Name `xml:"CompleteMultipartUploadResult"`
		Bucket         string
		Key            string
		ETag           string
		ChecksumSHA256 string `xml:",omitempty"`
	}{Bucket: f.bucket, Key: key, ETag: `"` + o.etag + `"`, ChecksumSHA256: o.checksum})
}

// testFile is a backup archive in memory that records how it is read.
type testFile struct {
	mu      sync.Mutex
	data    []byte
	maxRead int
	total   int64
	// changeAfter, if positive, flips a byte once that many bytes were read.
	changeAfter int64
}

func newTestFile(size int, seed uint64) *testFile {
	r := mrand.New(mrand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	return &testFile{data: data}
}

func (f *testFile) ReadAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maxRead = max(f.maxRead, len(p))
	if off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	f.total += int64(n)
	if f.changeAfter > 0 && f.total >= f.changeAfter {
		f.data[len(f.data)/2] ^= 0xff
		f.changeAfter = 0
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *testFile) sha() string {
	s := sha256.Sum256(f.data)
	return hex.EncodeToString(s[:])
}

func (f *testFile) upload(name string) Upload {
	return Upload{Name: name, File: f, Size: int64(len(f.data)), SHA256: f.sha()}
}

// object hands f to a backend as if it were an encrypted copy.
func (f *testFile) object(name string) object {
	return object{Name: name, File: f, Size: int64(len(f.data)), SHA256: f.sha()}
}
