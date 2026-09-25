package software

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Algorithm is a hash algorithm an upstream publishes.
type Algorithm string

const (
	SHA512 Algorithm = "sha512"
	SHA256 Algorithm = "sha256"
	SHA1   Algorithm = "sha1"
	MD5    Algorithm = "md5"
)

func (a Algorithm) new() hash.Hash {
	switch a {
	case SHA512:
		return sha512.New()
	case SHA256:
		return sha256.New()
	case SHA1:
		return sha1.New()
	case MD5:
		return md5.New()
	}
	return nil
}

func (a Algorithm) hexLen() int {
	if h := a.new(); h != nil {
		return 2 * h.Size()
	}
	return 0
}

func (a Algorithm) label() string {
	switch a {
	case SHA512:
		return "SHA-512"
	case SHA256:
		return "SHA-256"
	case SHA1:
		return "SHA-1"
	case MD5:
		return "MD5"
	}
	return string(a)
}

// Hash is a digest in lowercase hex.
type Hash struct {
	Algorithm Algorithm `json:"algorithm"`
	Value     string    `json:"value"`
}

var reHex = regexp.MustCompile(`^[0-9a-f]+$`)

func (h Hash) valid() bool {
	n := h.Algorithm.hexLen()
	return n > 0 && len(h.Value) == n && reHex.MatchString(h.Value)
}

func parseHash(a Algorithm, s string) (Hash, bool) {
	h := Hash{Algorithm: a, Value: strings.ToLower(strings.TrimSpace(s))}
	return h, h.valid()
}

// Artifact is one file Playkeeper downloads itself, with the hash its
// upstream publishes for it. Path is where it goes, relative to the server's
// data directory (mounted at /data in the container).
type Artifact struct {
	URL  string `json:"url"`
	Path string `json:"path"`
	// Size is the exact size in bytes when the upstream publishes it.
	Size int64 `json:"size,omitempty"`
	Hash Hash  `json:"hash"`
	// Source says who published Hash, as it reads in a sentence:
	// "Fabric's Maven repository".
	Source   string   `json:"source"`
	Upstream string   `json:"upstream"`
	Hosts    []string `json:"hosts"`
}

// Origin says where a check's hash comes from.
type Origin string

const (
	// Pinned hashes are published by the upstream and known before anything
	// is downloaded.
	Pinned Origin = "pinned"
	// Derived hashes are read from inside a file that was verified first,
	// like the library list inside Mojang's server jar.
	Derived Origin = "derived"
	// Recorded hashes belong to files nobody publishes a hash for, written
	// by verified code during the install. Playkeeper hashes them right
	// after the verified install and checks them before every start.
	Recorded Origin = "recorded"
)

// Check is one file the server's software needs, with the hash it must have.
type Check struct {
	Path   string `json:"path"`
	Hash   Hash   `json:"hash"`
	Size   int64  `json:"size,omitempty"`
	Origin Origin `json:"origin"`
	Source string `json:"source"`
}

func pinned(a Artifact) Check {
	return Check{Path: a.Path, Hash: a.Hash, Size: a.Size, Origin: Pinned, Source: a.Source}
}

var reSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// cleanRel reports whether p is a relative, slash-separated path of plain
// names (no "..", no hidden or empty segments, no backslashes), of bounded
// length and, when exts are given, ending in one of them.
func cleanRel(p string, exts ...string) bool {
	if p == "" || len(p) > 512 || strings.ContainsAny(p, "\\\x00") || path.IsAbs(p) || path.Clean(p) != p {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if len(s) > 160 || !reSegment.MatchString(s) {
			return false
		}
	}
	if len(exts) == 0 {
		return true
	}
	for _, e := range exts {
		if strings.HasSuffix(p, e) {
			return true
		}
	}
	return false
}

func unsafePath(p, why string) error {
	return &Error{Kind: KindUnsafePath, Msg: fmt.Sprintf("Playkeeper refused to use %q in the server's folder: %s.", p, why),
		Hint:   "Nothing was changed. Reinstall the server software; if it keeps happening, report it to the Playkeeper developers.",
		Params: map[string]string{"file": p}}
}

// within returns rel inside dataDir, refusing unclean paths and symlinks in
// any existing part of it: the server container can write to the data
// directory, and Playkeeper must never follow a link it planted.
func within(dataDir, rel string) (string, error) {
	if !cleanRel(rel) {
		return "", unsafePath(rel, "it is not a plain relative path")
	}
	segs := strings.Split(rel, "/")
	p := dataDir
	for i, s := range segs {
		p = filepath.Join(p, s)
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return filepath.Join(dataDir, filepath.FromSlash(rel)), nil
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return "", unsafePath(rel, "part of it is a symbolic link")
		}
		if i < len(segs)-1 && !fi.IsDir() {
			return "", unsafePath(rel, "part of it is not a folder")
		}
	}
	return p, nil
}

// mkdirs creates rel's parent directories one at a time, for the same
// reason within refuses symlinks.
func mkdirs(dataDir, rel string) error {
	dir := path.Dir(rel)
	if dir == "." {
		return nil
	}
	p := dataDir
	for _, s := range strings.Split(dir, "/") {
		p = filepath.Join(p, s)
		if err := os.Mkdir(p, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return unsafePath(rel, "part of it is not a folder")
		}
	}
	return nil
}

// hashFile hashes a regular file inside dataDir.
func hashFile(dataDir, rel string, a Algorithm) (sum string, n int64, err error) {
	p, err := within(dataDir, rel)
	if err != nil {
		return "", 0, err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return "", 0, err
	}
	if !fi.Mode().IsRegular() {
		return "", 0, unsafePath(rel, "it is not a regular file")
	}
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := a.new()
	if h == nil {
		return "", 0, fmt.Errorf("unknown hash algorithm %q", a)
	}
	n, err = io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Download fetches a into dataDir over HTTPS from one of a.Hosts, refusing
// redirects anywhere else, and keeps it only if its size and hash match. It
// writes to a temporary file next to the target and renames it into place,
// so a failed or tampered download never leaves a file that could be run.
// A file that is already in place and matches is not downloaded again.
func Download(ctx context.Context, hc *http.Client, dataDir string, a Artifact) error {
	if !cleanRel(a.Path, ".jar") {
		return unsafePath(a.Path, "downloads must be .jar files with a plain relative name")
	}
	if !a.Hash.valid() {
		return &Error{Kind: KindMalformed, Msg: fmt.Sprintf("Playkeeper has no valid checksum for %s, so it will not download it.", path.Base(a.Path)),
			Hint: "Choose the version again so Playkeeper can load its checksum.", Params: map[string]string{"file": a.Path}}
	}
	if a.Size < 0 {
		return &Error{Kind: KindMalformed, Msg: fmt.Sprintf("The size listed for %s is not valid, so Playkeeper will not download it.", path.Base(a.Path)),
			Hint: "Choose the version again so Playkeeper can load its details.", Params: map[string]string{"file": a.Path}}
	}
	if a.Size > maxArtifact {
		return &Error{Kind: KindTooLarge, Msg: fmt.Sprintf("The file %s is listed as %d bytes, more than the %s Playkeeper downloads for one file.", path.Base(a.Path), a.Size, size(maxArtifact)),
			Hint: "Choose another version.", Params: map[string]string{"file": a.Path}}
	}
	target, err := within(dataDir, a.Path)
	if err != nil {
		return err
	}
	if sum, n, err := hashFile(dataDir, a.Path, a.Hash.Algorithm); err == nil && sum == a.Hash.Value && (a.Size == 0 || n == a.Size) {
		return nil
	}
	if err := mkdirs(dataDir, a.Path); err != nil {
		return err
	}
	name := path.Base(a.Path)
	u := upstream{name: a.Upstream, hosts: a.Hosts, hc: hc}
	resp, err := u.open(ctx, a.URL, name)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	limit := int64(maxArtifact)
	if a.Size > 0 {
		limit = a.Size
	}
	if resp.ContentLength > limit || (a.Size > 0 && resp.ContentLength >= 0 && resp.ContentLength != a.Size) {
		return sizeMismatch(a, resp.ContentLength)
	}
	f, err := os.CreateTemp(filepath.Dir(target), "."+name+".*.partial")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	h := a.Hash.Algorithm.new()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &Error{Kind: KindUnreachable, Msg: fmt.Sprintf("The download of %s from %s broke off (%v).", name, a.Upstream, err),
			Hint: "Check this host's connection, then try again.", Params: map[string]string{"file": a.Path, "upstream": a.Upstream}, Err: err}
	}
	if n > limit || (a.Size > 0 && n != a.Size) {
		return sizeMismatch(a, n)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.Hash.Value {
		return &Error{Kind: KindHashMismatch,
			Msg: fmt.Sprintf("The downloaded %s does not match the %s from %s (got %s, want %s). It was deleted and not used.",
				name, a.Hash.Algorithm.label(), a.Source, short(got), short(a.Hash.Value)),
			Hint:   "This can mean a corrupted download or a tampered mirror. Try again.",
			Params: map[string]string{"file": a.Path, "algorithm": string(a.Hash.Algorithm), "got": got, "want": a.Hash.Value, "source": a.Source}}
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

// sizeMismatch explains a download whose size is wrong. got is only exact when
// it is not above the limit: reading stops one byte past it.
func sizeMismatch(a Artifact, got int64) error {
	name := path.Base(a.Path)
	params := map[string]string{"file": a.Path, "want": strconv.FormatInt(a.Size, 10)}
	var kind Kind
	var msg string
	switch {
	case a.Size == 0:
		kind, msg = KindTooLarge, fmt.Sprintf("The file %s from %s is larger than the %s Playkeeper allows for one file, so it was not used.", name, a.Upstream, size(maxArtifact))
	case got > a.Size:
		kind, msg = KindSizeMismatch, fmt.Sprintf("The file %s from %s is larger than the %d bytes %s says it should be. It was not used.", name, a.Upstream, a.Size, a.Source)
	default:
		kind, msg = KindSizeMismatch, fmt.Sprintf("The file %s from %s is %d bytes, but %s says it should be %d bytes. It was not used.", name, a.Upstream, got, a.Source, a.Size)
		params["got"] = strconv.FormatInt(got, 10)
	}
	return &Error{Kind: kind, Msg: msg, Hint: "This can mean a corrupted download or a tampered mirror. Try again.", Params: params}
}

// Verify checks files against their hashes and sizes, stopping at the first
// one that is missing or differs.
func Verify(dataDir string, checks []Check) error {
	for _, c := range checks {
		if err := verifyOne(dataDir, c); err != nil {
			return err
		}
	}
	return nil
}

func verifyOne(dataDir string, c Check) error {
	params := map[string]string{"file": c.Path, "origin": string(c.Origin), "algorithm": string(c.Hash.Algorithm), "want": c.Hash.Value}
	if !c.Hash.valid() {
		return &Error{Kind: KindMalformed, Msg: fmt.Sprintf("Playkeeper has no valid checksum for %s.", c.Path),
			Hint: "Reinstall the server software.", Params: params}
	}
	sum, n, err := hashFile(dataDir, c.Path, c.Hash.Algorithm)
	if errors.Is(err, fs.ErrNotExist) {
		return &Error{Kind: KindMissingFile, Msg: fmt.Sprintf("The server software file %s is missing.", c.Path),
			Hint: "Reinstall the server software.", Params: params}
	}
	if err != nil {
		return err
	}
	params["got"] = sum
	if c.Size > 0 && n != c.Size {
		params["size"] = strconv.FormatInt(n, 10)
		return &Error{Kind: KindSizeMismatch, Msg: fmt.Sprintf("The file %s is %d bytes, but %s says it should be %d bytes.", c.Path, n, c.Source, c.Size),
			Hint: "The file may be corrupted or changed. Reinstall the server software.", Params: params}
	}
	if sum != c.Hash.Value {
		return &Error{Kind: KindHashMismatch, Msg: fmt.Sprintf("The file %s does not match the %s from %s (got %s, want %s).",
			c.Path, c.Hash.Algorithm.label(), c.Source, short(sum), short(c.Hash.Value)),
			Hint: "The file may be corrupted or changed. Reinstall the server software.", Params: params}
	}
	return nil
}

const recordedSource = "Playkeeper's record, made right after the verified install"

// record hashes files that have no upstream hash, right after a verified
// install.
func record(dataDir string, paths []string) ([]Check, error) {
	var out []Check
	for _, p := range paths {
		sum, _, err := hashFile(dataDir, p, SHA256)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &Error{Kind: KindMissingFile, Msg: fmt.Sprintf("The install did not create %s.", p),
				Hint: "Check the console for errors from the installer, then reinstall the server software.", Params: map[string]string{"file": p}}
		}
		if err != nil {
			return nil, err
		}
		out = append(out, Check{Path: p, Hash: Hash{Algorithm: SHA256, Value: sum}, Origin: Recorded, Source: recordedSource})
	}
	return out, nil
}

// writeFile writes data to rel inside dataDir through a temporary file and a
// rename.
func writeFile(dataDir, rel string, data []byte) error {
	target, err := within(dataDir, rel)
	if err != nil {
		return err
	}
	if err := mkdirs(dataDir, rel); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(target), "."+path.Base(rel)+".*.partial")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

func removeFile(dataDir, rel string) error {
	p, err := within(dataDir, rel)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
