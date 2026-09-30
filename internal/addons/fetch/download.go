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
	// Also are more hashes the publisher lists; each must match too.
	Also []Sum
	Size int64 // 0 when unknown
	Max  int64
	// Progress, when set, is told how many bytes have arrived each time
	// more do.
	Progress func(received int64)
}

type progressWriter struct {
	n  int64
	fn func(int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.n += int64(len(b))
	p.fn(p.n)
	return len(b), nil
}

// Sum is one hash a publisher lists for a file.
type Sum struct {
	Algo string
	Hash string // hex
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
	v, err := want.verifier()
	if err != nil {
		return "", err
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
	if resp.ContentLength > v.limit {
		if want.Size > 0 && resp.ContentLength <= want.Max {
			return "", &SizeError{Want: want.Size, Got: resp.ContentLength}
		}
		return "", &TooLargeError{What: "the file", Limit: want.Max}
	}
	return v.save(dir, resp.Body, func(err error) error { return netError(u.Hostname(), err) })
}

// Save writes r into a new file in dir and returns its path once its size
// and hash match want, as Download checks a download: for a file Playkeeper
// carries itself. On any error nothing is left in dir.
func Save(r io.Reader, dir string, want Want) (string, error) {
	v, err := want.verifier()
	if err != nil {
		return "", err
	}
	return v.save(dir, r, func(err error) error { return err })
}

// verifier checks a file against a Want as it's written.
type verifier struct {
	want Want
	sums []Sum
	hs   []hash.Hash
	// limit is the most bytes read: the size the publisher lists, else Max.
	limit int64
}

func (want Want) verifier() (*verifier, error) {
	sums := append([]Sum{{want.Algo, want.Hash}}, want.Also...)
	hs := make([]hash.Hash, len(sums))
	for i, s := range sums {
		h, err := NewHash(s.Algo)
		if err != nil {
			return nil, err
		}
		if len(s.Hash) != 2*h.Size() {
			return nil, fmt.Errorf("the publisher lists no usable %s hash for this file", s.Algo)
		}
		hs[i] = h
	}
	limit := want.Max
	if want.Size > 0 {
		if want.Size > want.Max {
			return nil, &TooLargeError{What: "the file", Limit: want.Max}
		}
		limit = want.Size
	}
	return &verifier{want: want, sums: sums, hs: hs, limit: limit}, nil
}

// save writes r into a new file in dir and returns its path once its size
// and hashes match; readErr explains a failure reading r. On any error
// nothing is left in dir.
func (v *verifier) save(dir string, r io.Reader, readErr func(error) error) (path string, err error) {
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
	ws := []io.Writer{f}
	for _, h := range v.hs {
		ws = append(ws, h)
	}
	if v.want.Progress != nil {
		ws = append(ws, &progressWriter{fn: v.want.Progress})
	}
	n, err := io.Copy(io.MultiWriter(ws...), io.LimitReader(r, v.limit+1))
	if err != nil {
		return "", readErr(err)
	}
	switch {
	case v.want.Size > 0 && n != v.want.Size:
		return "", &SizeError{Want: v.want.Size, Got: n}
	case n > v.want.Max:
		return "", &TooLargeError{What: "the file", Limit: v.want.Max}
	}
	for i, s := range v.sums {
		if got := hex.EncodeToString(v.hs[i].Sum(nil)); got != strings.ToLower(s.Hash) {
			return "", &HashError{Algo: s.Algo, Want: strings.ToLower(s.Hash), Got: got}
		}
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}
