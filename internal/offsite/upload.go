package offsite

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Upload is one backup archive to copy off the server.
type Upload struct {
	Name   string      // the archive's file name, which the copy keeps
	File   io.ReaderAt // the archive: read once to hash and once to send, never held in memory
	Size   int64
	SHA256 string // hex SHA-256 recorded when the backup was made
	// Resume continues an upload an earlier call left in Error.Resume or
	// Progress.State. A state for another file or prefix is ignored.
	Resume *UploadState
	// Progress, if set, is called after each part is stored.
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

// UploadState is an unfinished multipart upload. Save it as JSON and pass
// it back as Upload.Resume to continue, or to Abort to discard it.
type UploadState struct {
	Name      string    `json:"name"`
	Key       string    `json:"key"`
	UploadID  string    `json:"uploadId"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	PartSize  int64     `json:"partSize"`
	Checksums bool      `json:"checksums"` // parts carry x-amz-checksum-sha256
	Parts     []Part    `json:"parts"`
	StartedAt time.Time `json:"startedAt"`
}

// Part is one stored part of an upload.
type Part struct {
	Number int    `json:"number"`
	Size   int64  `json:"size"`
	ETag   string `json:"etag"`
	SHA256 string `json:"sha256"`
	MD5    string `json:"md5"`
}

func (s *UploadState) clone() *UploadState {
	c := *s
	c.Parts = slices.Clone(s.Parts)
	return &c
}

func (s *UploadState) fits(up Upload, key string, minPart int64) bool {
	return s.Name == up.Name && s.Key == key && s.Size == up.Size && strings.EqualFold(s.SHA256, up.SHA256) &&
		s.UploadID != "" && s.PartSize >= minPart && partCount(s.Size, s.PartSize) <= maxParts
}

// How a copy was checked against the backup.
const (
	CheckedSHA256 = "sha256" // the service reported a SHA-256 that matches
	CheckedMD5    = "md5"    // the service's ETag matches the backup's MD5
	CheckedSize   = "size"   // the service reported neither, so only the size was compared
)

// Copy is a backup's copy in the bucket.
type Copy struct {
	Name           string `json:"name"`
	Key            string `json:"key"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256"`
	ETag           string `json:"etag,omitempty"`
	ChecksumSHA256 string `json:"checksumSha256,omitempty"` // as the service reports it
	// Checked is CheckedSHA256, CheckedMD5 or CheckedSize. Whichever it is,
	// the service also checked each request's signed SHA-256 on arrival.
	Checked    string    `json:"checked"`
	VerifiedAt time.Time `json:"verifiedAt"`
	// Uploaded is false when an identical copy was already there.
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

func (c *Client) choosePartSize(size int64) int64 {
	ps := max(c.partSize, c.minPart)
	for partCount(size, ps) > maxParts {
		ps *= 2
	}
	return ps
}

func (c *Client) checkUpload(up Upload) error {
	switch {
	case !ValidName(up.Name):
		return &Error{Kind: KindInvalidConfig, Op: opUpload, Field: "name", Msg: "The backup's file name can't be used for a copy.",
			Hint: "Copies keep the archive's name: letters, digits, dots, hyphens and underscores, ending in .tar.gz."}
	case up.File == nil || up.Size <= 0:
		return unexpected(opUpload, up.Name, fmt.Sprintf("The backup file %s is empty or missing.", up.Name))
	case !validSHA256(up.SHA256):
		return unexpected(opUpload, up.Name, fmt.Sprintf("The backup %s has no SHA-256 recorded, so its copy couldn't be checked.", up.Name))
	case up.Size > maxObjectSize:
		return &Error{Kind: KindTooLarge, Op: opUpload, Name: up.Name, Msg: fmt.Sprintf("The backup %s is larger than 5 TiB, the most S3 storage accepts in one file.", up.Name)}
	}
	return nil
}

func (c *Client) changedError(name string, during bool) *Error {
	if during {
		return &Error{Kind: KindLocalChanged, Op: opUpload, Name: name,
			Msg:  fmt.Sprintf("The backup file %s changed while it was being copied, so the copy was stopped.", name),
			Hint: "Make sure nothing else writes to the backups folder, then try again."}
	}
	return &Error{Kind: KindLocalChanged, Op: opUpload, Name: name,
		Msg:  fmt.Sprintf("The backup file %s doesn't match the checksum recorded when it was made, so it wasn't copied.", name),
		Hint: "It may have been changed or damaged on this machine. Make a new backup and copy that one."}
}

func (c *Client) hashLocal(ctx context.Context, up Upload, off, n int64, also io.Writer) (digest, error) {
	d, err := hashSection(ctx, up.File, off, n, also)
	if err != nil {
		if ctx.Err() != nil {
			return d, c.transportError(opUpload, up.Name, ctx.Err())
		}
		return d, &Error{Kind: KindUnexpected, Op: opUpload, Name: up.Name, Err: err,
			Msg: fmt.Sprintf("Playkeeper couldn't read the backup file %s.", up.Name), Hint: "Check the disk for errors."}
	}
	if d.n != n {
		return d, c.changedError(up.Name, false)
	}
	return d, nil
}

// sentWrong tells a service that received different bytes because the
// file changed from one that received them garbled.
func (c *Client) sentWrong(ctx context.Context, err error, up Upload, off, n int64, d digest) error {
	if asError(err).Kind != KindChecksum {
		return err
	}
	again, herr := hashSection(ctx, up.File, off, n, nil)
	if herr != nil || again.n != n || !bytes.Equal(again.sha, d.sha) {
		return c.changedError(up.Name, true)
	}
	return err
}

func (c *Client) progress(up Upload, sent int64, st *UploadState) {
	if up.Progress == nil {
		return
	}
	var s *UploadState
	if st != nil {
		s = st.clone()
	}
	up.Progress(Progress{Sent: sent, Total: up.Size, State: s})
}

// Upload copies up to the bucket, or continues an interrupted upload, and
// then reads the copy's size and checksum back to verify it. A copy that
// doesn't match is deleted. If an identical copy is already there it is
// returned with Uploaded false; a different file under the same name is
// never overwritten (KindConflict).
//
// Archives up to the part size (64 MiB) go up in one request, larger ones
// in parts, one at a time. Each part is read twice, to hash it and to send
// it, so memory use doesn't grow with the archive. If the upload stops
// for a reason that may pass (the network, the service, cancellation),
// the Error carries Resume; otherwise the stored parts are discarded.
func (c *Client) Upload(ctx context.Context, up Upload) (Copy, error) {
	up.SHA256 = strings.ToLower(up.SHA256)
	if err := c.checkUpload(up); err != nil {
		return Copy{}, err
	}
	key := c.prefix + up.Name
	resume := up.Resume
	if resume != nil && !resume.fits(up, key, c.minPart) {
		resume = nil
	}
	h, err := c.head(ctx, opUpload, up.Name, key)
	switch {
	case err == nil:
		checked, problem := check(h, up.Size, up.SHA256, "", nil)
		if problem != "" {
			return Copy{}, &Error{Kind: KindConflict, Op: opUpload, Name: up.Name,
				Msg:  fmt.Sprintf("A different file named %s is already in the bucket: %s.", up.Name, problem),
				Hint: "Playkeeper never overwrites it. Delete it at the storage service, or use another prefix."}
		}
		if resume != nil {
			c.abortQuietly(ctx, resume)
		}
		return c.copyFrom(h, up.Name, key, up.SHA256, checked), nil
	case asError(err).Kind != KindNotFound:
		if resume != nil {
			e := asError(err)
			e.Resume = resume.clone()
			return Copy{}, e
		}
		return Copy{}, err
	}
	if resume == nil && up.Size <= c.partSize {
		return c.putSingle(ctx, up, key)
	}
	return c.putMultipart(ctx, up, key, resume)
}

func (c *Client) putSingle(ctx context.Context, up Upload, key string) (Copy, error) {
	d, err := c.hashLocal(ctx, up, 0, up.Size, nil)
	if err != nil {
		return Copy{}, err
	}
	if hex.EncodeToString(d.sha) != up.SHA256 {
		return Copy{}, c.changedError(up.Name, false)
	}
	h := http.Header{}
	h.Set("Content-Type", "application/gzip")
	h.Set("Content-Md5", b64(d.md5))
	h.Set(metaSHA256, up.SHA256)
	h.Set("X-Amz-Checksum-Sha256", b64(d.sha))
	_, err = c.do(ctx, call{op: opUpload, name: up.Name, method: http.MethodPut, key: key, header: h,
		body: up.File, size: up.Size, sha256: hex.EncodeToString(d.sha), checksum: true}, nil)
	if err != nil {
		return Copy{}, c.sentWrong(ctx, err, up, 0, up.Size, d)
	}
	c.progress(up, up.Size, nil)
	return c.verifyUpload(ctx, up, key, d, nil)
}

type listedPart struct {
	size     int64
	etag     string
	checksum string
}

func (c *Client) putMultipart(ctx context.Context, up Upload, key string, resume *UploadState) (Copy, error) {
	st, listed, err := c.startOrResume(ctx, up, key, resume)
	if err != nil {
		return Copy{}, err
	}
	saved := map[int]Part{}
	for _, p := range st.Parts {
		saved[p.Number] = p
	}
	st.Parts = nil
	whole := sha256.New()
	var sent int64
	for n := 1; n <= partCount(up.Size, st.PartSize); n++ {
		off := int64(n-1) * st.PartSize
		size := min(st.PartSize, up.Size-off)
		d, err := c.hashLocal(ctx, up, off, size, whole)
		if err != nil {
			return Copy{}, c.failUpload(ctx, st, err)
		}
		p, ok := reusable(saved[n], listed, n, size, d)
		if !ok {
			if p, err = c.uploadPart(ctx, up, st, n, off, size, d); err != nil {
				return Copy{}, c.failUpload(ctx, st, c.sentWrong(ctx, err, up, off, size, d))
			}
		}
		st.Parts = append(st.Parts, p)
		sent += size
		c.progress(up, sent, st)
	}
	sum := whole.Sum(nil)
	if hex.EncodeToString(sum) != up.SHA256 {
		return Copy{}, c.failUpload(ctx, st, c.changedError(up.Name, false))
	}
	if err := c.complete(ctx, up, st); err != nil {
		if asError(err).Kind != KindUploadGone {
			return Copy{}, c.failUpload(ctx, st, err)
		}
		// A retried request can find that its first try completed the
		// upload before the answer was lost.
		if _, herr := c.head(ctx, opUpload, up.Name, key); herr != nil {
			return Copy{}, c.failUpload(ctx, st, err)
		}
	}
	return c.verifyUpload(ctx, up, key, digest{sha: sum}, st.Parts)
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

func (c *Client) startOrResume(ctx context.Context, up Upload, key string, resume *UploadState) (*UploadState, map[int]listedPart, error) {
	if resume != nil {
		listed, err := c.listParts(ctx, up.Name, key, resume.UploadID)
		if err == nil {
			return resume.clone(), listed, nil
		}
		if k := asError(err).Kind; k != KindUploadGone && k != KindNotFound {
			e := asError(err)
			e.Resume = resume.clone()
			return nil, nil, e
		}
	}
	checksums, id, err := c.createUpload(ctx, opUpload, up.Name, key, "application/gzip", up.SHA256)
	if err != nil {
		return nil, nil, err
	}
	return &UploadState{Name: up.Name, Key: key, UploadID: id, Size: up.Size, SHA256: up.SHA256,
		PartSize: c.choosePartSize(up.Size), Checksums: checksums, StartedAt: c.now().UTC()}, nil, nil
}

// createUpload starts a multipart upload and reports whether its parts
// must carry SHA-256 checksums.
func (c *Client) createUpload(ctx context.Context, op, name, key, contentType, sha string) (bool, string, error) {
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

func (c *Client) uploadPart(ctx context.Context, up Upload, st *UploadState, n int, off, size int64, d digest) (Part, error) {
	h := http.Header{}
	h.Set("Content-Md5", b64(d.md5))
	if st.Checksums {
		h.Set("X-Amz-Checksum-Sha256", b64(d.sha))
	}
	q := url.Values{"partNumber": {strconv.Itoa(n)}, "uploadId": {st.UploadID}}
	resp, err := c.do(ctx, call{op: opUpload, name: up.Name, method: http.MethodPut, key: st.Key, query: q, header: h,
		body: up.File, off: off, size: size, sha256: hex.EncodeToString(d.sha)}, nil)
	if err != nil {
		return Part{}, err
	}
	etag := resp.Header.Get("ETag")
	if etag == "" || len(etag) > 1024 || !printable(etag) {
		return Part{}, unexpected(opUpload, up.Name, "The storage service didn't confirm a part with an ETag.")
	}
	return Part{Number: n, Size: size, ETag: etag, SHA256: hex.EncodeToString(d.sha), MD5: hex.EncodeToString(d.md5)}, nil
}

func (c *Client) listParts(ctx context.Context, name, key, uploadID string) (map[int]listedPart, error) {
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

func (c *Client) complete(ctx context.Context, up Upload, st *UploadState) error {
	doc := completeUpload{}
	for _, p := range st.Parts {
		cp := completePart{PartNumber: p.Number, ETag: p.ETag}
		if st.Checksums {
			cp.ChecksumSHA256 = hexToB64(p.SHA256)
		}
		doc.Parts = append(doc.Parts, cp)
	}
	body, err := xml.Marshal(doc)
	if err != nil {
		return unexpected(opUpload, up.Name, "Playkeeper couldn't build the request to finish the upload.")
	}
	sum := sha256.Sum256(body)
	_, err = c.do(ctx, call{op: opUpload, name: up.Name, method: http.MethodPost, key: st.Key, query: url.Values{"uploadId": {st.UploadID}},
		header: http.Header{"Content-Type": {"application/xml"}}, body: bytes.NewReader(body), size: int64(len(body)), sha256: hex.EncodeToString(sum[:])},
		func(r *http.Response) error {
			var res struct{ ETag string }
			return c.readXML(opUpload, up.Name, r, &res)
		})
	return err
}

func (c *Client) failUpload(ctx context.Context, st *UploadState, err error) error {
	e := asError(err)
	switch e.Kind {
	case KindNetwork, KindTLS, KindRateLimited, KindServiceError, KindCanceled, KindStorageFull, KindClockSkew, KindChecksum:
		e.Resume = st.clone()
	default:
		c.abortQuietly(ctx, st)
	}
	return e
}

func (c *Client) abortQuietly(ctx context.Context, st *UploadState) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_ = c.Abort(ctx, st)
}

// Abort discards an unfinished upload and the parts stored so far. An
// upload that is already gone is not an error.
func (c *Client) Abort(ctx context.Context, st *UploadState) error {
	if st == nil || st.UploadID == "" {
		return nil
	}
	if !validKey(st.Key) {
		return unexpected(opAbort, st.Name, "The saved upload doesn't name a file Playkeeper would have uploaded.")
	}
	_, err := c.do(ctx, call{op: opAbort, name: st.Name, method: http.MethodDelete, key: st.Key, query: url.Values{"uploadId": {st.UploadID}}}, nil)
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
	return (ValidName(name) || isProbeName(name)) && prefixProblem(dir) == ""
}

// AbortStale discards unfinished uploads under the prefix that started
// before olderThan, except those whose upload IDs are in keep (saved to
// resume). It returns how many it discarded.
func (c *Client) AbortStale(ctx context.Context, olderThan time.Time, keep []string) (int, error) {
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
			if !ok || strings.Contains(name, "/") || err != nil || !started.Before(olderThan) || slices.Contains(keep, u.UploadID) {
				continue
			}
			stale = append(stale, UploadState{Name: name, Key: u.Key, UploadID: u.UploadID})
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
		if !validKey(stale[i].Key) {
			continue
		}
		if err := c.Abort(ctx, &stale[i]); err != nil {
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
func (c *Client) verifyUpload(ctx context.Context, up Upload, key string, d digest, parts []Part) (Copy, error) {
	h, err := c.head(ctx, opVerify, up.Name, key)
	if err != nil {
		if asError(err).Kind == KindNotFound {
			return Copy{}, &Error{Kind: KindVerifyFailed, Op: opVerify, Name: up.Name,
				Msg:  fmt.Sprintf("The copy %s wasn't in the bucket right after it was uploaded.", up.Name),
				Hint: "Try again. If it keeps happening, check the bucket's lifecycle rules."}
		}
		return Copy{}, err
	}
	md5Hex := ""
	if parts == nil {
		md5Hex = hex.EncodeToString(d.md5)
	}
	checked, problem := check(h, up.Size, up.SHA256, md5Hex, parts)
	if problem != "" {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_ = c.deleteKey(dctx, up.Name, key)
		cancel()
		return Copy{}, &Error{Kind: KindVerifyFailed, Op: opVerify, Name: up.Name,
			Msg:  fmt.Sprintf("The copy of %s in the bucket doesn't match the backup (%s), so it was deleted.", up.Name, problem),
			Hint: "Try again. If it keeps happening, the storage service may be altering uploads."}
	}
	cp := c.copyFrom(h, up.Name, key, up.SHA256, checked)
	cp.Uploaded = true
	return cp, nil
}

// check compares what the bucket reports about a copy with the backup.
// It returns how closely they could be compared, or what differs.
func check(h headInfo, size int64, sha, md5Hex string, parts []Part) (string, string) {
	if h.size != size {
		return "", fmt.Sprintf("it is %d bytes, the backup %d", h.size, size)
	}
	if h.meta != "" && !strings.EqualFold(h.meta, sha) {
		return "", "it belongs to a different backup"
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

func (c *Client) copyFrom(h headInfo, name, key, sha, checked string) Copy {
	return Copy{Name: name, Key: key, Size: h.size, SHA256: sha, ETag: normETag(h.etag), ChecksumSHA256: h.checksum,
		Checked: checked, VerifiedAt: c.now().UTC()}
}
