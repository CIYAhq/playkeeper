package offsite

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"
)

const probePrefix = "playkeeper-connection-test-"

// probeText is what the connection test's file holds, encrypted like a
// copy, for whoever finds it.
const probeText = "Playkeeper connection test. Playkeeper deletes this file right away; if it is still here, you can delete it.\n"

func newProbeName() string {
	var id [8]byte
	_, _ = rand.Read(id[:])
	return probePrefix + hex.EncodeToString(id[:]) + ".age"
}

func isProbeName(name string) bool {
	id, ok := cutAffixes(name, probePrefix, ".age")
	if !ok || len(id) != 16 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func cutAffixes(s, prefix, suffix string) (string, bool) {
	if len(s) < len(prefix)+len(suffix) || s[:len(prefix)] != prefix || s[len(s)-len(suffix):] != suffix {
		return "", false
	}
	return s[len(prefix) : len(s)-len(suffix)], true
}

// Check is one step of the connection test.
type Check struct {
	// Step is, for S3: write, read, list, multipart or delete; for SFTP:
	// connect, folder, write, rename, read, list or delete.
	Step   string            `json:"step"`
	OK     bool              `json:"ok"`
	Msg    string            `json:"msg"`
	Hint   string            `json:"hint,omitempty"`
	Kind   Kind              `json:"kind,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

// TestResult is the outcome of Destination.Test.
type TestResult struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
	// Skew is how far the S3 service's clock is ahead of this machine's
	// (negative: behind), from its Date header; zero if unknown or under
	// two seconds.
	Skew time.Duration `json:"skew"`
	// HostKey is the host key the other machine presented over SFTP, once
	// the connection got that far. When the connect step fails with
	// KindHostKeyUnknown, show its fingerprint for the user to confirm, then
	// save its Key as SFTPConfig.HostKey.
	HostKey *HostKey `json:"hostKey,omitempty"`
	// Warning is a problem that doesn't fail the test yet.
	Warning string `json:"warning,omitempty"`
}

func (r *TestResult) add(step, okMsg string, err error) bool {
	ch := Check{Step: step, OK: err == nil, Msg: okMsg}
	if err != nil {
		e := asError(err)
		ch.Msg, ch.Hint, ch.Kind, ch.Params = e.Msg, e.Hint, e.Kind, e.Params()
		r.OK = false
	}
	r.Checks = append(r.Checks, ch)
	return err == nil
}

const stepTimeout = 30 * time.Second

func step(ctx context.Context, f func(ctx context.Context) error) error {
	sctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	return f(sctx)
}

// test checks that the settings work for everything copies need. It writes
// body under the prefix as name, reads it back, looks for it in the list,
// starts and aborts a multipart upload, and deletes the file. If the write
// fails nothing else is tried; otherwise every step runs, and the file is
// always deleted. Each step has 30 seconds.
func (c *s3Client) test(ctx context.Context, name string, body []byte) TestResult {
	key := c.prefix + name
	res := TestResult{OK: true}
	err := step(ctx, func(ctx context.Context) error {
		resp, err := c.putSmall(ctx, name, key, body)
		if err == nil {
			res.Skew = c.skewFrom(resp)
		}
		return err
	})
	if !res.add("write", "Wrote a small encrypted test file.", err) {
		return res
	}
	if res.Skew >= 5*time.Minute || res.Skew <= -5*time.Minute {
		dir := "behind"
		if res.Skew < 0 {
			dir = "ahead of"
		}
		res.Warning = fmt.Sprintf("This machine's clock is %s %s the storage service's; at 15 minutes, copies start failing. Turn on automatic time sync (sudo timedatectl set-ntp true).", humanDuration(res.Skew), dir)
	}

	res.add("read", "Read the test file back unchanged.", step(ctx, func(ctx context.Context) error {
		return c.readSmall(ctx, name, key, body)
	}))
	res.add("list", "Found the test file in the folder's list.", step(ctx, func(ctx context.Context) error {
		r, err := c.listPage(ctx, opList, key, "", 10)
		if err != nil {
			return err
		}
		for _, o := range r.objects {
			if o.Key == key {
				return nil
			}
		}
		return &Error{Kind: KindUnexpected, Op: opList, Name: name, Msg: "The folder's list didn't include the test file.",
			Hint: "Listing may lag at this provider. Rules for copies off the server rely on it to find old copies."}
	}))
	res.add("multipart", "Started and cancelled a multipart upload, as large backups need.", step(ctx, func(ctx context.Context) error {
		_, uploadID, err := c.createUpload(ctx, opUpload, name, key, "application/octet-stream", "")
		if err != nil {
			return err
		}
		return c.abort(ctx, &UploadState{Name: name, S3: &S3Upload{Key: key, UploadID: uploadID}})
	}))
	res.add("delete", "Deleted the test file.", step(ctx, func(ctx context.Context) error {
		if err := c.deleteKey(ctx, name, key); err != nil {
			return err
		}
		_, err := c.head(ctx, opDelete, name, key)
		switch {
		case err == nil:
			return &Error{Kind: KindUnexpected, Op: opDelete, Name: name, Msg: "The test file was still there after deleting it.",
				Hint: "Check the bucket's settings; rules for copies off the server need deleted files to go."}
		case asError(err).Kind == KindNotFound:
			return nil
		}
		return err
	}))
	return res
}

func (c *s3Client) putSmall(ctx context.Context, name, key string, body []byte) (*http.Response, error) {
	sum, m := sha256.Sum256(body), md5.Sum(body)
	h := http.Header{}
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Md5", b64(m[:]))
	h.Set("X-Amz-Checksum-Sha256", b64(sum[:]))
	return c.do(ctx, call{op: opUpload, name: name, method: http.MethodPut, key: key, header: h,
		body: bytes.NewReader(body), size: int64(len(body)), sha256: hex.EncodeToString(sum[:]), checksum: true}, nil)
}

func (c *s3Client) readSmall(ctx context.Context, name, key string, want []byte) error {
	_, err := c.do(ctx, call{op: opDownload, name: name, method: http.MethodGet, key: key}, func(r *http.Response) error {
		got, err := io.ReadAll(io.LimitReader(r.Body, int64(len(want))+1))
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return &Error{Kind: KindVerifyFailed, Op: opDownload, Name: name, Msg: "The test file came back different from what was written.",
				Hint: "Something between this machine and the storage service changes files. Check the endpoint."}
		}
		return nil
	})
	return err
}

func (c *s3Client) skewFrom(resp *http.Response) time.Duration {
	server, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		return 0
	}
	d := server.Sub(c.now()).Round(time.Second)
	if d > -2*time.Second && d < 2*time.Second {
		return 0
	}
	return d
}
