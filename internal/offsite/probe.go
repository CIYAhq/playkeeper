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

func isProbeName(name string) bool {
	id, ok := cutAffixes(name, probePrefix, ".txt")
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
	Step   string            `json:"step"` // write, read, list, multipart or delete
	OK     bool              `json:"ok"`
	Msg    string            `json:"msg"`
	Hint   string            `json:"hint,omitempty"`
	Kind   Kind              `json:"kind,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

// TestResult is the outcome of Test.
type TestResult struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
	// Skew is how far the service's clock is ahead of this machine's
	// (negative: behind), from its Date header; zero if unknown or under
	// two seconds.
	Skew time.Duration `json:"skew"`
	// Warning is a problem that doesn't fail the test yet.
	Warning string `json:"warning,omitempty"`
}

const stepTimeout = 30 * time.Second

// Test checks that the settings work for everything copies need. It writes
// a small file under the prefix, reads it back, looks for it in the list,
// starts and aborts a multipart upload, and deletes the file. If the write
// fails nothing else is tried; otherwise every step runs, and the file is
// always deleted. Each step has 30 seconds.
func (c *Client) Test(ctx context.Context) TestResult {
	var id [8]byte
	_, _ = rand.Read(id[:])
	name := probePrefix + hex.EncodeToString(id[:]) + ".txt"
	key := c.prefix + name
	body := []byte("Playkeeper connection test. Playkeeper deletes this file right away; if it is still here, you can delete it.\n")
	res := TestResult{OK: true}
	add := func(step, okMsg string, err error) bool {
		ch := Check{Step: step, OK: err == nil, Msg: okMsg}
		if err != nil {
			e := asError(err)
			ch.Msg, ch.Hint, ch.Kind, ch.Params = e.Msg, e.Hint, e.Kind, e.Params()
			res.OK = false
		}
		res.Checks = append(res.Checks, ch)
		return err == nil
	}
	step := func(f func(ctx context.Context) error) error {
		sctx, cancel := context.WithTimeout(ctx, stepTimeout)
		defer cancel()
		return f(sctx)
	}

	err := step(func(ctx context.Context) error {
		resp, err := c.putSmall(ctx, name, key, body)
		if err == nil {
			res.Skew = c.skewFrom(resp)
		}
		return err
	})
	if !add("write", "Wrote a small test file.", err) {
		return res
	}
	if res.Skew >= 5*time.Minute || res.Skew <= -5*time.Minute {
		dir := "behind"
		if res.Skew < 0 {
			dir = "ahead of"
		}
		res.Warning = fmt.Sprintf("This machine's clock is %s %s the storage service's; at 15 minutes, copies start failing. Turn on automatic time sync (sudo timedatectl set-ntp true).", humanDuration(res.Skew), dir)
	}

	add("read", "Read the test file back unchanged.", step(func(ctx context.Context) error {
		return c.readSmall(ctx, name, key, body)
	}))
	add("list", "Found the test file in the folder's list.", step(func(ctx context.Context) error {
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
	add("multipart", "Started and cancelled a multipart upload, as large backups need.", step(func(ctx context.Context) error {
		_, uploadID, err := c.createUpload(ctx, opUpload, name, key, "text/plain", "")
		if err != nil {
			return err
		}
		return c.Abort(ctx, &UploadState{Name: name, Key: key, UploadID: uploadID})
	}))
	add("delete", "Deleted the test file.", step(func(ctx context.Context) error {
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

func (c *Client) putSmall(ctx context.Context, name, key string, body []byte) (*http.Response, error) {
	sum, m := sha256.Sum256(body), md5.Sum(body)
	h := http.Header{}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Md5", b64(m[:]))
	h.Set("X-Amz-Checksum-Sha256", b64(sum[:]))
	return c.do(ctx, call{op: opUpload, name: name, method: http.MethodPut, key: key, header: h,
		body: bytes.NewReader(body), size: int64(len(body)), sha256: hex.EncodeToString(sum[:]), checksum: true}, nil)
}

func (c *Client) readSmall(ctx context.Context, name, key string, want []byte) error {
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

func (c *Client) skewFrom(resp *http.Response) time.Duration {
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
