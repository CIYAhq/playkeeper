package fetch

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"strings"
)

// Want is what a download must be: the hash its publisher lists, its exact
// size when the publisher lists one, and the largest size accepted at all.
type Want struct {
	Algo string // sha512, sha256 or sha1
	Hash string // hex
	Size int64  // 0 when unknown
	Max  int64
}

// NewHash returns the hash function for a publisher's algorithm name.
func NewHash(algo string) (hash.Hash, error) {
	switch algo {
	case "sha512":
		return sha512.New(), nil
	case "sha256":
		return sha256.New(), nil
	case "sha1":
		return sha1.New(), nil
	}
	return nil, fmt.Errorf("unsupported hash algorithm %q", algo)
}

// Download fetches rawURL into a new file in dir and returns its path once
// its size and hash match want. On any error nothing is left in dir.
func Download(ctx context.Context, c *http.Client, hosts Hosts, userAgent, rawURL, dir string, want Want) (path string, err error) {
	u, err := hosts.Check(rawURL)
	if err != nil {
		return "", err
	}
	h, err := NewHash(want.Algo)
	if err != nil {
		return "", err
	}
	if len(want.Hash) != 2*h.Size() {
		return "", fmt.Errorf("the publisher lists no usable %s hash for this file", want.Algo)
	}
	limit := want.Max
	if want.Size > 0 {
		if want.Size > want.Max {
			return "", &TooLargeError{What: "the file", Limit: want.Max}
		}
		limit = want.Size
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := Guard(c, hosts).Do(req)
	if err != nil {
		return "", netError(u.Hostname(), err)
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return "", &StatusError{Service: u.Hostname(), Status: resp.StatusCode, Path: u.EscapedPath()}
	}
	if resp.ContentLength > limit {
		if want.Size > 0 && resp.ContentLength <= want.Max {
			return "", &SizeError{Want: want.Size, Got: resp.ContentLength}
		}
		return "", &TooLargeError{What: "the file", Limit: want.Max}
	}

	f, err := os.CreateTemp(dir, ".download-*.part")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", netError(u.Hostname(), err)
	}
	switch {
	case want.Size > 0 && n != want.Size:
		return "", &SizeError{Want: want.Size, Got: n}
	case n > want.Max:
		return "", &TooLargeError{What: "the file", Limit: want.Max}
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != strings.ToLower(want.Hash) {
		return "", &HashError{Algo: want.Algo, Want: strings.ToLower(want.Hash), Got: got}
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}
