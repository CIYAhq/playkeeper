package offsite

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// object is an encrypted copy for a backend to store: the spool file
// Destination.Upload sealed.
type object struct {
	Name   string      // the copy's file name
	File   io.ReaderAt // the encrypted copy, read in pieces and never held in memory
	Size   int64
	SHA256 string // hex SHA-256 of the encrypted copy
	// Resume is the state to build on: a saved one to continue, or one
	// holding only what Destination.Upload fills in. A state for another
	// copy is ignored.
	Resume *UploadState
	// Progress, if set, is called after each part or segment is stored.
	Progress func(Progress)
}

// Progress reports how far an upload is.
type Progress struct {
	Sent  int64 `json:"sent"`
	Total int64 `json:"total"`
	// State is what to save to resume if Playkeeper stops now; nil for an
	// upload sent in one request.
	State *UploadState `json:"state,omitempty"`
}

// UploadState is an unfinished upload: the encrypted copy waiting in the
// spool folder, and how much of it the storage already has. Save it as
// JSON and pass it back as Upload.Resume to continue, or give it to
// Destination.Abort to discard it.
type UploadState struct {
	Archive       string    `json:"archive"`       // the backup's file name
	ArchiveSHA256 string    `json:"archiveSha256"` // the backup's SHA-256, from its record
	Name          string    `json:"name"`          // the copy's file name
	Spool         string    `json:"spool"`         // the encrypted copy's file name in the spool folder
	Size          int64     `json:"size"`          // of the encrypted copy
	SHA256        string    `json:"sha256"`        // of the encrypted copy
	Recipient     string    `json:"recipient"`     // the public key the copy is encrypted to
	StartedAt     time.Time `json:"startedAt"`
	// S3 or SFTP is what the storage holds so far, if anything.
	S3   *S3Upload   `json:"s3,omitempty"`
	SFTP *SFTPUpload `json:"sftp,omitempty"`
}

// S3Upload is an unfinished multipart upload.
type S3Upload struct {
	Key       string `json:"key"`
	UploadID  string `json:"uploadId"`
	PartSize  int64  `json:"partSize"`
	Checksums bool   `json:"checksums"` // parts carry x-amz-checksum-sha256
	Parts     []Part `json:"parts"`
}

// SFTPUpload is an unfinished file on the other machine.
type SFTPUpload struct {
	Partial string `json:"partial"` // its name in the folder
	Written int64  `json:"written"` // how many bytes the other machine confirmed
}

// Part is one stored part of a multipart upload.
type Part struct {
	Number int    `json:"number"`
	Size   int64  `json:"size"`
	ETag   string `json:"etag"`
	SHA256 string `json:"sha256"`
	MD5    string `json:"md5"`
}

func (s *UploadState) clone() *UploadState {
	c := *s
	if s.S3 != nil {
		u := *s.S3
		u.Parts = slices.Clone(s.S3.Parts)
		c.S3 = &u
	}
	if s.SFTP != nil {
		u := *s.SFTP
		c.SFTP = &u
	}
	return &c
}

// state returns the state an upload of o builds on: a copy of o.Resume if
// that is for o, or a new one.
func (o object) state(now time.Time) *UploadState {
	if r := o.Resume; r != nil && r.Name == o.Name && r.Size == o.Size && strings.EqualFold(r.SHA256, o.SHA256) {
		return r.clone()
	}
	return &UploadState{Name: o.Name, Size: o.Size, SHA256: o.SHA256, StartedAt: now.UTC()}
}

func (c *s3Client) fits(st *UploadState, key string) bool {
	u := st.S3
	return u != nil && u.Key == key && u.UploadID != "" && u.PartSize >= c.minPart && partCount(st.Size, u.PartSize) <= maxParts
}

// How a copy was checked.
const (
	CheckedSHA256 = "sha256" // the service reported a SHA-256 that matches
	CheckedMD5    = "md5"    // the service's ETag matches the copy's MD5
	CheckedSize   = "size"   // the service reported neither, so only the size was compared
	// CheckedSampled: every byte went through the SSH connection's
	// integrity check, the other machine has the whole size, and the
	// first and last 64 KiB read back unchanged.
	CheckedSampled = "sampled"
	// CheckedDecrypted: a copy that was already there was downloaded and
	// opened, and the backup inside matches.
	CheckedDecrypted = "decrypted"
)

// Copy is a backup's encrypted copy at the destination.
type Copy struct {
	Name          string `json:"name"`          // the copy's file name: the archive's, with .age added
	Archive       string `json:"archive"`       // the backup's file name
	ArchiveSHA256 string `json:"archiveSha256"` // the backup's SHA-256
	Recipient     string `json:"recipient"`     // the public key the copy is encrypted to
	// Key is where the copy is: its key in the bucket, or its path on the
	// other machine.
	Key            string `json:"key"`
	Size           int64  `json:"size"`   // of the encrypted copy
	SHA256         string `json:"sha256"` // of the encrypted copy
	ETag           string `json:"etag,omitempty"`
	ChecksumSHA256 string `json:"checksumSha256,omitempty"` // as the service reports it
	// Checked is one of the Checked constants. On S3 the service also
	// checked each request's signed SHA-256 on arrival.
	Checked    string    `json:"checked"`
	VerifiedAt time.Time `json:"verifiedAt"`
	// Uploaded is false when the copy was already there.
	Uploaded bool `json:"uploaded"`
}

type digest struct {
	sha, md5 []byte
	n        int64
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// hashSection hashes n bytes of f at off, also writing them to also.
func hashSection(ctx context.Context, f io.ReaderAt, off, n int64, also io.Writer) (digest, error) {
	s, m := sha256.New(), md5.New()
	ws := []io.Writer{s, m}
	if also != nil {
		ws = append(ws, also)
	}
	got, err := io.CopyBuffer(io.MultiWriter(ws...), ctxReader{ctx, io.NewSectionReader(f, off, n)}, make([]byte, 256<<10))
	return digest{sha: s.Sum(nil), md5: m.Sum(nil), n: got}, err
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func hexToB64(h string) string {
	b, err := hex.DecodeString(h)
	if err != nil {
		return ""
	}
	return b64(b)
}

func validSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func partCount(size, partSize int64) int {
	return int((size + partSize - 1) / partSize)
}

func (c *s3Client) choosePartSize(size int64) int64 {
	ps := max(c.partSize, c.minPart)
	for partCount(size, ps) > maxParts {
		ps *= 2
	}
	return ps
}

// checkObject refuses what Destination.Upload never hands a backend.
func checkObject(o object) error {
	switch {
	case !validCopyName(o.Name) && !isProbeName(o.Name):
		return unexpected(opUpload, o.Name, "That is not the name of a backup's copy.")
	case o.File == nil || o.Size <= 0 || !validSHA256(o.SHA256):
		return unexpected(opUpload, o.Name, fmt.Sprintf("The encrypted copy %s is empty or has no checksum.", o.Name))
	}
	return nil
}

// ctxError is the error for an operation its context stopped.
func ctxError(op, name string, err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: KindNetwork, Op: op, Name: name, Retry: true, Err: err,
			Msg:  opName(op) + " took longer than allowed, so it was stopped.",
			Hint: "Try again. A slow connection makes large copies take a while."}
	}
	return &Error{Kind: KindCanceled, Op: op, Name: name, Err: err, Msg: opName(op) + " was cancelled before it finished."}
}

// changedError is about the encrypted copy in the spool folder: it no
// longer matches what was sealed, before or while it was sent.
func changedError(name string, during bool) *Error {
	if during {
		return &Error{Kind: KindLocalChanged, Op: opUpload, Name: name,
			Msg:  fmt.Sprintf("The encrypted copy %s changed on this machine while it was being sent, so the upload was stopped.", name),
			Hint: "Make sure nothing else writes to Playkeeper's spool folder, then try again."}
	}
	return &Error{Kind: KindLocalChanged, Op: opUpload, Name: name,
		Msg:  fmt.Sprintf("The encrypted copy %s prepared on this machine doesn't match its checksum, so it wasn't sent.", name),
		Hint: "It may have been changed or damaged on this disk. Try again: Playkeeper encrypts the backup again."}
}

func hashLocal(ctx context.Context, o object, off, n int64, also io.Writer) (digest, error) {
	d, err := hashSection(ctx, o.File, off, n, also)
	if err != nil {
		if ctx.Err() != nil {
			return d, ctxError(opUpload, o.Name, ctx.Err())
		}
		return d, &Error{Kind: KindUnexpected, Op: opUpload, Name: o.Name, Err: err,
			Msg: fmt.Sprintf("Playkeeper couldn't read the encrypted copy %s on this machine.", o.Name), Hint: "Check the disk for errors."}
	}
	if d.n != n {
		return d, changedError(o.Name, false)
	}
	return d, nil
}

// sentWrong tells a service that received different bytes because the
// spool file changed from one that received them garbled.
func sentWrong(ctx context.Context, err error, o object, off, n int64, d digest) error {
	if asError(err).Kind != KindChecksum {
		return err
	}
	again, herr := hashSection(ctx, o.File, off, n, nil)
	if herr != nil || again.n != n || !bytes.Equal(again.sha, d.sha) {
		return changedError(o.Name, true)
	}
	return err
}

func progress(o object, sent int64, st *UploadState) {
	if o.Progress == nil {
		return
	}
	var s *UploadState
	if st != nil {
		s = st.clone()
	}
	o.Progress(Progress{Sent: sent, Total: o.Size, State: s})
}

// s3Resumable reports whether an upload that failed with k is kept to
// continue later; otherwise the stored parts are discarded.
func s3Resumable(k Kind) bool {
	switch k {
	case KindNetwork, KindTLS, KindRateLimited, KindServiceError, KindCanceled, KindStorageFull, KindClockSkew, KindChecksum:
		return true
	}
	return false
}

// put stores o in the bucket, or continues an interrupted upload, and then
// reads the copy's size and checksum back to verify it. A copy that
// doesn't match is deleted. If an identical copy is already there it is
// returned with Uploaded false; a different file under the same name is
// never overwritten (KindConflict).
//
// Copies up to the part size (64 MiB) go up in one request, larger ones in
// parts, one at a time. Each part is read twice, to hash it and to send
// it, so memory use doesn't grow with the copy. If the upload stops for a
// reason that may pass (the network, the service, cancellation), the Error
// carries Resume; otherwise the stored parts are discarded.
func (c *s3Client) put(ctx context.Context, o object) (Copy, error) {
	o.SHA256 = strings.ToLower(o.SHA256)
	if err := checkObject(o); err != nil {
		return Copy{}, err
	}
	if o.Size > maxObjectSize {
		return Copy{}, &Error{Kind: KindTooLarge, Op: opUpload, Name: o.Name,
			Msg: fmt.Sprintf("The copy %s is larger than 5 TiB, the most S3 storage accepts in one file.", o.Name)}
	}
	key := c.prefix + o.Name
	st := o.state(c.now())
	if st.S3 != nil && !c.fits(st, key) {
		st.S3 = nil
	}
	h, err := c.head(ctx, opUpload, o.Name, key)
	switch {
	case err == nil:
		if st.S3 != nil {
			c.abortQuietly(ctx, st)
		}
		checked, problem := check(h, o.Size, o.SHA256, "", nil)
		if problem == "" && checked == CheckedSize && !strings.EqualFold(h.meta, o.SHA256) {
			problem = "Playkeeper can't tell whether it is this copy"
		}
		if problem != "" {
			return Copy{}, &Error{Kind: KindConflict, Op: opUpload, Name: o.Name,
				Msg:  fmt.Sprintf("A different file named %s is already in the bucket: %s.", o.Name, problem),
				Hint: "Playkeeper never overwrites it. Delete it at the storage service, or use another prefix."}
		}
		return c.copyFrom(h, o.Name, key, o.SHA256, checked), nil
	case asError(err).Kind != KindNotFound:
		e := asError(err)
		e.Resume = st.clone()
		return Copy{}, e
	}
	if st.S3 == nil && o.Size <= c.partSize {
		return c.putSingle(ctx, o, key, st)
	}
	return c.putMultipart(ctx, o, key, st)
}

func (c *s3Client) putSingle(ctx context.Context, o object, key string, st *UploadState) (Copy, error) {
	fail := func(err error) (Copy, error) {
		if e := asError(err); s3Resumable(e.Kind) {
			e.Resume = st.clone()
			return Copy{}, e
		}
		return Copy{}, err
	}
	d, err := hashLocal(ctx, o, 0, o.Size, nil)
	if err != nil {
		return fail(err)
	}
	if hex.EncodeToString(d.sha) != o.SHA256 {
		return Copy{}, changedError(o.Name, false)
	}
	h := http.Header{}
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Md5", b64(d.md5))
	h.Set(metaSHA256, o.SHA256)
	h.Set("X-Amz-Checksum-Sha256", b64(d.sha))
	_, err = c.do(ctx, call{op: opUpload, name: o.Name, method: http.MethodPut, key: key, header: h,
		body: o.File, size: o.Size, sha256: hex.EncodeToString(d.sha), checksum: true}, nil)
	if err != nil {
		return fail(sentWrong(ctx, err, o, 0, o.Size, d))
	}
	progress(o, o.Size, nil)
	return c.verifyUpload(ctx, o, key, d, nil)
}

type listedPart struct {
	size     int64
	etag     string
	checksum string
}

func (c *s3Client) putMultipart(ctx context.Context, o object, key string, st *UploadState) (Copy, error) {
	listed, err := c.startOrResume(ctx, o, key, st)
	if err != nil {
		return Copy{}, err
	}
	u := st.S3
	saved := map[int]Part{}
	for _, p := range u.Parts {
		saved[p.Number] = p
	}
	u.Parts = nil
	whole := sha256.New()
	var sent int64
	for n := 1; n <= partCount(o.Size, u.PartSize); n++ {
		off := int64(n-1) * u.PartSize
		size := min(u.PartSize, o.Size-off)
		d, err := hashLocal(ctx, o, off, size, whole)
		if err != nil {
			return Copy{}, c.failUpload(ctx, st, err)
		}
		p, ok := reusable(saved[n], listed, n, size, d)
		if !ok {
			if p, err = c.uploadPart(ctx, o, u, n, off, size, d); err != nil {
				return Copy{}, c.failUpload(ctx, st, sentWrong(ctx, err, o, off, size, d))
			}
		}
		u.Parts = append(u.Parts, p)
		sent += size
		progress(o, sent, st)
	}
	sum := whole.Sum(nil)
	if hex.EncodeToString(sum) != o.SHA256 {
		return Copy{}, c.failUpload(ctx, st, changedError(o.Name, false))
	}
	if err := c.complete(ctx, o, u); err != nil {
		if asError(err).Kind != KindUploadGone {
			return Copy{}, c.failUpload(ctx, st, err)
		}
		// A retried request can find that its first try completed the
		// upload before the answer was lost.
		if _, herr := c.head(ctx, opUpload, o.Name, key); herr != nil {
			return Copy{}, c.failUpload(ctx, st, err)
		}
	}
	return c.verifyUpload(ctx, o, key, digest{sha: sum}, u.Parts)
}

// reusable reports whether part n is already stored with this content,
// judged by its checksum, an MD5 ETag, or the ETag saved with this
// content's hash.
func reusable(saved Part, listed map[int]listedPart, n int, size int64, d digest) (Part, bool) {
	lp, ok := listed[n]
	if !ok || lp.size != size {
		return Part{}, false
	}
	shaHex, md5Hex := hex.EncodeToString(d.sha), hex.EncodeToString(d.md5)
	etag := normETag(lp.etag)
	var match bool
	switch {
	case lp.checksum != "":
		match = lp.checksum == b64(d.sha)
	case etag == md5Hex:
		match = true
	default:
		match = saved.Number == n && saved.SHA256 == shaHex && saved.ETag != "" && normETag(saved.ETag) == etag
	}
	if !match {
		return Part{}, false
	}
	return Part{Number: n, Size: size, ETag: lp.etag, SHA256: shaHex, MD5: md5Hex}, true
}

func normETag(s string) string { return strings.ToLower(strings.Trim(s, `"`)) }

// startOrResume continues st's multipart upload, returning the parts the
// service has, or starts a new one in st.S3.
func (c *s3Client) startOrResume(ctx context.Context, o object, key string, st *UploadState) (map[int]listedPart, error) {
	if st.S3 != nil {
		listed, err := c.listParts(ctx, o.Name, key, st.S3.UploadID)
		if err == nil {
			return listed, nil
		}
		if k := asError(err).Kind; k != KindUploadGone && k != KindNotFound {
			e := asError(err)
			e.Resume = st.clone()
			return nil, e
		}
		st.S3 = nil
	}
	checksums, id, err := c.createUpload(ctx, opUpload, o.Name, key, "application/octet-stream", o.SHA256)
	if err != nil {
		return nil, c.failUpload(ctx, st, err)
	}
	st.S3 = &S3Upload{Key: key, UploadID: id, PartSize: c.choosePartSize(o.Size), Checksums: checksums}
	return nil, nil
}

// createUpload starts a multipart upload and reports whether its parts
// must carry SHA-256 checksums.
func (c *s3Client) createUpload(ctx context.Context, op, name, key, contentType, sha string) (bool, string, error) {
	h := http.Header{}
	h.Set("Content-Type", contentType)
	if sha != "" {
		h.Set(metaSHA256, sha)
	}
	h.Set("X-Amz-Checksum-Algorithm", "SHA256")
	var res struct {
		UploadID string `xml:"UploadId"`
	}
	resp, err := c.do(ctx, call{op: op, name: name, method: http.MethodPost, key: key, query: url.Values{"uploads": {""}}, header: h, checksum: true},
		func(r *http.Response) error { return c.readXML(op, name, r, &res) })
	if err != nil {
		return false, "", err
	}
	if res.UploadID == "" || len(res.UploadID) > 1024 || !printable(res.UploadID) {
		return false, "", unexpected(op, name, "The storage service didn't return a usable upload ID.")
	}
	return resp.Request.Header.Get("X-Amz-Checksum-Algorithm") != "", res.UploadID, nil
}

func (c *s3Client) uploadPart(ctx context.Context, o object, u *S3Upload, n int, off, size int64, d digest) (Part, error) {
	h := http.Header{}
	h.Set("Content-Md5", b64(d.md5))
	if u.Checksums {
		h.Set("X-Amz-Checksum-Sha256", b64(d.sha))
	}
	q := url.Values{"partNumber": {strconv.Itoa(n)}, "uploadId": {u.UploadID}}
	resp, err := c.do(ctx, call{op: opUpload, name: o.Name, method: http.MethodPut, key: u.Key, query: q, header: h,
		body: o.File, off: off, size: size, sha256: hex.EncodeToString(d.sha)}, nil)
	if err != nil {
		return Part{}, err
	}
	etag := resp.Header.Get("ETag")
	if etag == "" || len(etag) > 1024 || !printable(etag) {
		return Part{}, unexpected(opUpload, o.Name, "The storage service didn't confirm a part with an ETag.")
	}
	return Part{Number: n, Size: size, ETag: etag, SHA256: hex.EncodeToString(d.sha), MD5: hex.EncodeToString(d.md5)}, nil
}

func (c *s3Client) listParts(ctx context.Context, name, key, uploadID string) (map[int]listedPart, error) {
	parts := map[int]listedPart{}
	marker := ""
	for page := 0; ; page++ {
		q := url.Values{"uploadId": {uploadID}, "max-parts": {"1000"}}
		if marker != "" {
			q.Set("part-number-marker", marker)
		}
		var res struct {
			IsTruncated          bool
			NextPartNumberMarker string
			Parts                []struct {
				PartNumber     int
				ETag           string
				Size           int64
				ChecksumSHA256 string
			} `xml:"Part"`
		}
		if _, err := c.do(ctx, call{op: opUpload, name: name, method: http.MethodGet, key: key, query: q},
			func(r *http.Response) error { return c.readXML(opUpload, name, r, &res) }); err != nil {
			return nil, err
		}
		for _, p := range res.Parts {
			if p.PartNumber >= 1 && p.PartNumber <= maxParts {
				parts[p.PartNumber] = listedPart{size: p.Size, etag: p.ETag, checksum: p.ChecksumSHA256}
			}
		}
		if !res.IsTruncated {
			return parts, nil
		}
		if res.NextPartNumberMarker == "" || res.NextPartNumberMarker == marker || page >= maxParts/1000 {
			return nil, unexpected(opUpload, name, "The storage service's list of stored parts didn't end.")
		}
		marker = res.NextPartNumberMarker
	}
}

type completeUpload struct {
	XMLName xml.Name       `xml:"http://s3.amazonaws.com/doc/2006-03-01/ CompleteMultipartUpload"`
	Parts   []completePart `xml:"Part"`
}

type completePart struct {
	PartNumber     int
	ETag           string
	ChecksumSHA256 string `xml:",omitempty"`
}

func (c *s3Client) complete(ctx context.Context, o object, u *S3Upload) error {
	doc := completeUpload{}
	for _, p := range u.Parts {
		cp := completePart{PartNumber: p.Number, ETag: p.ETag}
		if u.Checksums {
			cp.ChecksumSHA256 = hexToB64(p.SHA256)
		}
		doc.Parts = append(doc.Parts, cp)
	}
	body, err := xml.Marshal(doc)
	if err != nil {
		return unexpected(opUpload, o.Name, "Playkeeper couldn't build the request to finish the upload.")
	}
	sum := sha256.Sum256(body)
	_, err = c.do(ctx, call{op: opUpload, name: o.Name, method: http.MethodPost, key: u.Key, query: url.Values{"uploadId": {u.UploadID}},
		header: http.Header{"Content-Type": {"application/xml"}}, body: bytes.NewReader(body), size: int64(len(body)), sha256: hex.EncodeToString(sum[:])},
		func(r *http.Response) error {
			var res struct{ ETag string }
			return c.readXML(opUpload, o.Name, r, &res)
		})
	return err
}

func (c *s3Client) failUpload(ctx context.Context, st *UploadState, err error) error {
	e := asError(err)
	if s3Resumable(e.Kind) {
		e.Resume = st.clone()
		return e
	}
	c.abortQuietly(ctx, st)
	return e
}

func (c *s3Client) abortQuietly(ctx context.Context, st *UploadState) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_ = c.abort(ctx, st)
}

// abort discards an unfinished multipart upload and the parts stored so
// far. An upload that is already gone is not an error.
func (c *s3Client) abort(ctx context.Context, st *UploadState) error {
	if st == nil || st.S3 == nil || st.S3.UploadID == "" {
		return nil
	}
	if !validKey(st.S3.Key) {
		return unexpected(opAbort, st.Name, "The saved upload doesn't name a file Playkeeper would have uploaded.")
	}
	_, err := c.do(ctx, call{op: opAbort, name: st.Name, method: http.MethodDelete, key: st.S3.Key, query: url.Values{"uploadId": {st.S3.UploadID}}}, nil)
	if err != nil {
		if k := asError(err).Kind; k == KindUploadGone || k == KindNotFound {
			return nil
		}
	}
	return err
}

// validKey reports whether key is a folder that is a valid prefix followed
// by a copy's or connection test's file name.
func validKey(key string) bool {
	i := strings.LastIndexByte(key, '/')
	dir, name := key[:i+1], key[i+1:]
	return (validCopyName(name) || isProbeName(name)) && prefixProblem(dir) == ""
}

// abortStale discards unfinished uploads under the prefix that started
// before olderThan, except those in keep (saved to resume). It returns how
// many it discarded.
func (c *s3Client) abortStale(ctx context.Context, olderThan time.Time, keep []*UploadState) (int, error) {
	var kept []string
	for _, st := range keep {
		if st != nil && st.S3 != nil {
			kept = append(kept, st.S3.UploadID)
		}
	}
	var stale []UploadState
	keyMarker, idMarker := "", ""
	for page := 0; ; page++ {
		q := url.Values{"uploads": {""}, "max-uploads": {"1000"}}
		if c.prefix != "" {
			q.Set("prefix", c.prefix)
		}
		if keyMarker != "" {
			q.Set("key-marker", keyMarker)
			q.Set("upload-id-marker", idMarker)
		}
		var res struct {
			IsTruncated        bool
			NextKeyMarker      string
			NextUploadIdMarker string
			Uploads            []struct {
				Key       string
				UploadID  string `xml:"UploadId"`
				Initiated string
			} `xml:"Upload"`
		}
		if _, err := c.do(ctx, call{op: opAbort, method: http.MethodGet, query: q},
			func(r *http.Response) error { return c.readXML(opAbort, "", r, &res) }); err != nil {
			return 0, err
		}
		for _, u := range res.Uploads {
			name, ok := strings.CutPrefix(u.Key, c.prefix)
			started, err := time.Parse(time.RFC3339, u.Initiated)
			if !ok || strings.Contains(name, "/") || err != nil || !started.Before(olderThan) || slices.Contains(kept, u.UploadID) {
				continue
			}
			stale = append(stale, UploadState{Name: name, S3: &S3Upload{Key: u.Key, UploadID: u.UploadID}})
		}
		if !res.IsTruncated {
			break
		}
		if res.NextKeyMarker == "" || res.NextKeyMarker == keyMarker && res.NextUploadIdMarker == idMarker || page >= 100 {
			return 0, unexpected(opAbort, "", "The storage service's list of unfinished uploads didn't end.")
		}
		keyMarker, idMarker = res.NextKeyMarker, res.NextUploadIdMarker
	}
	n := 0
	var first error
	for i := range stale {
		if !validKey(stale[i].S3.Key) {
			continue
		}
		if err := c.abort(ctx, &stale[i]); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		n++
	}
	return n, first
}

// verifyUpload reads back a fresh upload and deletes it if it doesn't
// match. parts is nil for a single-request upload, whose MD5 is in d.
func (c *s3Client) verifyUpload(ctx context.Context, o object, key string, d digest, parts []Part) (Copy, error) {
	h, err := c.head(ctx, opVerify, o.Name, key)
	if err != nil {
		if asError(err).Kind == KindNotFound {
			return Copy{}, &Error{Kind: KindVerifyFailed, Op: opVerify, Name: o.Name,
				Msg:  fmt.Sprintf("The copy %s wasn't in the bucket right after it was uploaded.", o.Name),
				Hint: "Try again. If it keeps happening, check the bucket's lifecycle rules."}
		}
		return Copy{}, err
	}
	md5Hex := ""
	if parts == nil {
		md5Hex = hex.EncodeToString(d.md5)
	}
	checked, problem := check(h, o.Size, o.SHA256, md5Hex, parts)
	if problem != "" {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_ = c.deleteKey(dctx, o.Name, key)
		cancel()
		return Copy{}, &Error{Kind: KindVerifyFailed, Op: opVerify, Name: o.Name,
			Msg:  fmt.Sprintf("The copy %s in the bucket doesn't match what was sent (%s), so it was deleted.", o.Name, problem),
			Hint: "Try again. If it keeps happening, the storage service may be altering uploads."}
	}
	cp := c.copyFrom(h, o.Name, key, o.SHA256, checked)
	cp.Uploaded = true
	return cp, nil
}

// check compares what the bucket reports about a copy with the encrypted
// copy that was sent. It returns how closely they could be compared, or
// what differs.
func check(h headInfo, size int64, sha, md5Hex string, parts []Part) (string, string) {
	if h.size != size {
		return "", fmt.Sprintf("it is %d bytes, not %d", h.size, size)
	}
	if h.meta != "" && !strings.EqualFold(h.meta, sha) {
		return "", "it is a different file"
	}
	if h.checksum != "" {
		got, _, composite := strings.Cut(h.checksum, "-")
		want := []string{hexToB64(sha)}
		if parts != nil {
			want = append(want, compositeSHA256(parts))
		}
		if slices.Contains(want, got) {
			return CheckedSHA256, ""
		}
		if !composite || parts != nil {
			return "", "its SHA-256 differs"
		}
	}
	etag := normETag(h.etag)
	switch {
	case parts == nil && md5Hex != "" && etag == md5Hex:
		return CheckedMD5, ""
	case parts != nil && etag == compositeMD5(parts):
		return CheckedMD5, ""
	}
	return CheckedSize, ""
}

// compositeSHA256 is S3's checksum of a multipart upload: the SHA-256 of
// the parts' SHA-256 digests, without its "-<parts>" suffix.
func compositeSHA256(parts []Part) string {
	s := sha256.New()
	for _, p := range parts {
		b, _ := hex.DecodeString(p.SHA256)
		s.Write(b)
	}
	return b64(s.Sum(nil))
}

// compositeMD5 is the ETag S3 usually gives a multipart upload.
func compositeMD5(parts []Part) string {
	m := md5.New()
	for _, p := range parts {
		b, _ := hex.DecodeString(p.MD5)
		m.Write(b)
	}
	return hex.EncodeToString(m.Sum(nil)) + "-" + strconv.Itoa(len(parts))
}

func (c *s3Client) copyFrom(h headInfo, name, key, sha, checked string) Copy {
	return Copy{Name: name, Key: key, Size: h.size, SHA256: sha, ETag: normETag(h.etag), ChecksumSHA256: h.checksum,
		Checked: checked, VerifiedAt: c.now().UTC()}
}
