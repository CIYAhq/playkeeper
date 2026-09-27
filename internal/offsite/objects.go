package offsite

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxObjects = 100_000

// Object is a copy found at the destination.
type Object struct {
	Name         string    `json:"name"`    // the copy's file name
	Archive      string    `json:"archive"` // the backup's file name: Name without .age
	Key          string    `json:"key"`     // its key in the bucket, or its path on the other machine
	Size         int64     `json:"size"`
	LastModified time.Time `json:"lastModified"`
	ETag         string    `json:"etag,omitempty"`
}

type headInfo struct {
	size     int64
	etag     string
	checksum string
	meta     string
}

// head reads what the bucket says about key. A HEAD answer has no body,
// so a refusal without a reason is asked again with a one-byte GET, whose
// error says why.
func (c *s3Client) head(ctx context.Context, op, name, key string) (headInfo, error) {
	resp, err := c.do(ctx, call{op: op, name: name, method: http.MethodHead, key: key,
		header: http.Header{"X-Amz-Checksum-Mode": {"ENABLED"}}, checksum: true}, nil)
	if err != nil {
		e := asError(err)
		if e.Code != "" || e.Status != http.StatusBadRequest && e.Status != http.StatusForbidden {
			return headInfo{}, err
		}
		_, gerr := c.do(ctx, call{op: op, name: name, method: http.MethodGet, key: key, header: http.Header{"Range": {"bytes=0-0"}}, once: true}, nil)
		switch {
		case gerr != nil && asError(gerr).Code != "":
			return headInfo{}, gerr
		case gerr == nil && e.Status == http.StatusBadRequest && !c.noChecksums.Load():
			// The service rejects x-amz-checksum-mode; ask without it.
			c.noChecksums.Store(true)
			return c.head(ctx, op, name, key)
		}
		return headInfo{}, err
	}
	size := resp.ContentLength
	if size < 0 {
		if size, err = strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); err != nil || size < 0 {
			return headInfo{}, unexpected(op, name, "The storage service didn't say how large the copy is.")
		}
	}
	return headInfo{size: size, etag: resp.Header.Get("ETag"), checksum: resp.Header.Get("X-Amz-Checksum-Sha256"), meta: resp.Header.Get(metaSHA256)}, nil
}

// list returns the copies in the prefix, sorted by name. Files that are
// not copies of backups, and folders inside the prefix, are left out.
func (c *s3Client) list(ctx context.Context) ([]Object, error) {
	var out []Object
	token := ""
	for page := 0; ; page++ {
		res, err := c.listPage(ctx, opList, c.prefix, token, 1000)
		if err != nil {
			return nil, err
		}
		for _, o := range res.objects {
			name, ok := strings.CutPrefix(o.Key, c.prefix)
			archive, isCopy := archiveOf(name)
			if !ok || !isCopy || o.Size < 0 {
				continue
			}
			o.Name, o.Archive = name, archive
			out = append(out, o)
		}
		if len(out) > maxObjects {
			return nil, &Error{Kind: KindTooLarge, Op: opList,
				Msg:  fmt.Sprintf("The folder holds more than %d copies, more than Playkeeper lists.", maxObjects),
				Hint: "Delete old copies at the storage service, or use another prefix."}
		}
		if !res.truncated {
			return out, nil
		}
		if res.next == "" || res.next == token {
			return nil, unexpected(opList, "", "The storage service's list of files didn't end.")
		}
		token = res.next
	}
}

type listResult struct {
	objects   []Object
	truncated bool
	next      string
}

func (c *s3Client) listPage(ctx context.Context, op, prefix, token string, maxKeys int) (listResult, error) {
	q := url.Values{"list-type": {"2"}, "delimiter": {"/"}, "max-keys": {strconv.Itoa(maxKeys)}, "encoding-type": {"url"}}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if token != "" {
		q.Set("continuation-token", token)
	}
	var res struct {
		IsTruncated           bool
		NextContinuationToken string
		EncodingType          string
		Contents              []struct {
			Key          string
			Size         int64
			LastModified string
			ETag         string
		}
	}
	if _, err := c.do(ctx, call{op: op, method: http.MethodGet, query: q},
		func(r *http.Response) error { return c.readXML(op, "", r, &res) }); err != nil {
		return listResult{}, err
	}
	out := listResult{truncated: res.IsTruncated, next: res.NextContinuationToken}
	for _, o := range res.Contents {
		key := o.Key
		if res.EncodingType == "url" {
			k, err := url.QueryUnescape(key)
			if err != nil {
				continue
			}
			key = k
		}
		t, _ := time.Parse(time.RFC3339, o.LastModified)
		out.objects = append(out.objects, Object{Key: key, Size: o.Size, LastModified: t, ETag: normETag(o.ETag)})
	}
	return out, nil
}

// verify reads a copy's size and checksum from the bucket and compares
// them with what put returned. It doesn't download the copy.
func (c *s3Client) verify(ctx context.Context, cp Copy) (Copy, error) {
	if !validCopyName(cp.Name) {
		return Copy{}, unexpected(opVerify, cp.Name, "That is not the name of a backup's copy.")
	}
	key := c.prefix + cp.Name
	h, err := c.head(ctx, opVerify, cp.Name, key)
	if err != nil {
		return Copy{}, err
	}
	problem := ""
	switch {
	case h.size != cp.Size:
		problem = fmt.Sprintf("it is %d bytes, not %d", h.size, cp.Size)
	case h.meta != "" && cp.SHA256 != "" && !strings.EqualFold(h.meta, cp.SHA256):
		problem = "it was replaced by another file"
	case cp.ChecksumSHA256 != "" && h.checksum != "" && h.checksum != cp.ChecksumSHA256:
		problem = "its SHA-256 changed"
	case cp.ETag != "" && h.etag != "" && normETag(h.etag) != normETag(cp.ETag):
		problem = "its ETag changed"
	}
	if problem != "" {
		return Copy{}, &Error{Kind: KindVerifyFailed, Op: opVerify, Name: cp.Name,
			Msg:  fmt.Sprintf("The copy %s in the bucket has changed since it was uploaded (%s).", cp.Name, problem),
			Hint: "Copy the backup again if it is still on this machine."}
	}
	cp.Key = key
	cp.VerifiedAt = c.now().UTC()
	return cp, nil
}

// remove deletes the copy name, and older versions of it if the bucket
// keeps versions, so the space is freed. Deleting a copy that isn't there
// is not an error. KindLocked means the copy is gone from the listing but
// the bucket refused to delete its older versions.
func (c *s3Client) remove(ctx context.Context, name string) error {
	if !validCopyName(name) {
		return unexpected(opDelete, name, "That is not the name of a backup's copy.")
	}
	return c.deleteKey(ctx, name, c.prefix+name)
}

func (c *s3Client) deleteKey(ctx context.Context, name, key string) error {
	if _, err := c.do(ctx, call{op: opDelete, name: name, method: http.MethodDelete, key: key}, nil); err != nil && asError(err).Kind != KindNotFound {
		return err
	}
	versions, err := c.versions(ctx, name, key)
	if err != nil {
		switch asError(err).Kind {
		case KindUnexpected, KindPermission, KindNotFound:
			// No versioning support, or a key that may not list versions:
			// nothing more this key can delete.
			return nil
		}
		return err
	}
	for _, v := range versions {
		_, err := c.do(ctx, call{op: opDelete, name: name, method: http.MethodDelete, key: key, query: url.Values{"versionId": {v}}}, nil)
		if err == nil || asError(err).Kind == KindNotFound {
			continue
		}
		if e := asError(err); e.Kind == KindPermission {
			return &Error{Kind: KindLocked, Op: opDelete, Name: name, Status: e.Status, Code: e.Code, Err: e,
				Msg:  fmt.Sprintf("The copy %s is deleted, but the bucket refused to delete its older versions.", name),
				Hint: "The bucket may lock files (Object Lock), or the key may not delete versions. They stay, and are billed, until the lock or a lifecycle rule removes them."}
		}
		return err
	}
	return nil
}

// versions lists the version IDs, including delete markers, of exactly key.
func (c *s3Client) versions(ctx context.Context, name, key string) ([]string, error) {
	var ids []string
	keyMarker, idMarker := "", ""
	for page := 0; ; page++ {
		q := url.Values{"versions": {""}, "prefix": {key}, "max-keys": {"1000"}}
		if keyMarker != "" {
			q.Set("key-marker", keyMarker)
			q.Set("version-id-marker", idMarker)
		}
		type version struct {
			Key       string
			VersionID string `xml:"VersionId"`
		}
		var res struct {
			IsTruncated         bool
			NextKeyMarker       string
			NextVersionIDMarker string    `xml:"NextVersionIdMarker"`
			Versions            []version `xml:"Version"`
			Markers             []version `xml:"DeleteMarker"`
		}
		if _, err := c.do(ctx, call{op: opDelete, name: name, method: http.MethodGet, query: q},
			func(r *http.Response) error { return c.readXML(opDelete, name, r, &res) }); err != nil {
			return nil, err
		}
		for _, v := range append(res.Versions, res.Markers...) {
			if v.Key == key && v.VersionID != "" && printable(v.VersionID) && len(v.VersionID) <= 1024 {
				ids = append(ids, v.VersionID)
			}
		}
		if !res.IsTruncated {
			return ids, nil
		}
		if res.NextKeyMarker == "" || res.NextKeyMarker == keyMarker && res.NextVersionIDMarker == idMarker || page >= 20 {
			return nil, unexpected(opDelete, name, "The storage service's list of versions didn't end.")
		}
		keyMarker, idMarker = res.NextKeyMarker, res.NextVersionIDMarker
	}
}

func (c *s3Client) keyOf(name string) string { return c.prefix + name }

// get streams the copy name to fn with the size the service reports (-1
// if it doesn't). fn reads what it needs; if it returns an error that is
// not an *Error, the download is tried again and fn gets a fresh stream.
func (c *s3Client) get(ctx context.Context, op, name string, fn func(r io.Reader, size int64) error) error {
	if !validCopyName(name) {
		return unexpected(op, name, "That is not the name of a backup's copy.")
	}
	_, err := c.do(ctx, call{op: op, name: name, method: http.MethodGet, key: c.prefix + name}, func(r *http.Response) error {
		return fn(r.Body, r.ContentLength)
	})
	return err
}
