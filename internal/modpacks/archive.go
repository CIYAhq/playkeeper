package modpacks

import (
	"archive/zip"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
)

// archive is a pack's zip, checked as a whole before anything in it is
// used: one unsafe entry refuses the pack, even in a part Playkeeper would
// not read.
type archive struct {
	f     *os.File
	files map[string]*zip.File // regular files by path
	name  string               // the pack, for messages
}

func openArchive(path, name string, lim Limits) (*archive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	zr, err := zip.NewReader(f, fi.Size())
	if err != nil {
		f.Close()
		if errors.Is(err, zip.ErrInsecurePath) {
			return nil, unsafePath(name, err)
		}
		return nil, badPack(name, "is not a zip archive Playkeeper can read")
	}
	a := &archive{f: f, files: map[string]*zip.File{}, name: name}
	if err := a.check(zr, lim); err != nil {
		f.Close()
		return nil, err
	}
	return a, nil
}

func (a *archive) Close() error { return a.f.Close() }

func (a *archive) check(zr *zip.Reader, lim Limits) error {
	if len(zr.File) > lim.Entries {
		return fail(KindTooManyFiles, kv("pack", a.name, "limit", strconv.Itoa(lim.Entries)),
			fmt.Sprintf("%s's archive has more than %d entries, more than Playkeeper accepts.", a.name, lim.Entries),
			"Choose another pack. Nothing was installed.")
	}
	dirs := map[string]bool{}
	for _, e := range zr.File {
		name, isDir := strings.CutSuffix(e.Name, "/")
		mode := e.Mode()
		if !utf8.ValidString(e.Name) {
			return unsafePath(a.name, &mrpack.PathError{Path: e.Name, Reason: "is not UTF-8 text"})
		}
		if mode&(fs.ModeSymlink|fs.ModeDevice|fs.ModeCharDevice|fs.ModeNamedPipe|fs.ModeSocket|fs.ModeIrregular) != 0 {
			return unsafePath(a.name, &mrpack.PathError{Path: e.Name, Reason: "is a link or a special file"})
		}
		if err := mrpack.CheckPath(name); err != nil {
			return unsafePath(a.name, err)
		}
		if isDir || mode.IsDir() {
			dirs[name] = true
			continue
		}
		if a.files[name] != nil {
			return badPack(a.name, fmt.Sprintf("has the entry %q twice", printable(name)))
		}
		a.files[name] = e
	}
	paths := slices.Collect(maps.Keys(a.files))
	for p := range dirs {
		if a.files[p] != nil {
			return badPack(a.name, fmt.Sprintf("has both a file and a folder named %q", printable(p)))
		}
	}
	if clash := treeClash(paths); clash != "" {
		return badPack(a.name, fmt.Sprintf("has both a file and a folder named %q", printable(clash)))
	}
	return nil
}

// treeClash returns a path that is a file and also a folder holding
// another path, or "".
func treeClash(paths []string) string {
	files := make(map[string]bool, len(paths))
	for _, p := range paths {
		files[p] = true
	}
	for _, p := range paths {
		for i := strings.IndexByte(p, '/'); i >= 0; i = next(p, i) {
			if files[p[:i]] {
				return p[:i]
			}
		}
	}
	return ""
}

func next(p string, i int) int {
	j := strings.IndexByte(p[i+1:], '/')
	if j < 0 {
		return -1
	}
	return i + 1 + j
}

// layer returns the files under the folders prefixes by their path below
// them; a later folder's file wins over an earlier one's at the same path.
func (a *archive) layer(prefixes ...string) map[string]*zip.File {
	out := map[string]*zip.File{}
	for _, pre := range prefixes {
		for p, e := range a.files {
			if rel, ok := strings.CutPrefix(p, pre+"/"); ok && rel != "" {
				out[rel] = e
			}
		}
	}
	return out
}

// read returns a small file from the archive, such as the index.
func (a *archive) read(name string, max int64) ([]byte, error) {
	e := a.files[name]
	if e == nil {
		return nil, fs.ErrNotExist
	}
	if e.UncompressedSize64 > uint64(max) {
		return nil, &fetch.TooLargeError{What: name, Limit: max}
	}
	r, err := e.Open()
	if err != nil {
		return nil, badPack(a.name, "is damaged ("+err.Error()+")")
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, badPack(a.name, "is damaged ("+err.Error()+")")
	}
	if int64(len(b)) > max {
		return nil, &fetch.TooLargeError{What: name, Limit: max}
	}
	return b, nil
}

// copyEntry copies an entry of at most max bytes to w and returns its
// sha512 and sha1 hashes and size.
func copyEntry(e *zip.File, w io.Writer, max int64) (map[string]string, int64, error) {
	if e.UncompressedSize64 > uint64(max) {
		return nil, 0, &fetch.TooLargeError{What: e.Name, Limit: max}
	}
	r, err := e.Open()
	if err != nil {
		return nil, 0, err
	}
	defer r.Close()
	hs := newHashes("sha512", "sha1")
	ws := []io.Writer{w}
	for _, h := range hs {
		ws = append(ws, h)
	}
	n, err := io.Copy(io.MultiWriter(ws...), io.LimitReader(r, max+1))
	if err != nil {
		return nil, 0, err
	}
	if n > max {
		return nil, 0, &fetch.TooLargeError{What: e.Name, Limit: max}
	}
	return sums(hs), n, nil
}

func newHashes(algos ...string) map[string]hash.Hash {
	hs := make(map[string]hash.Hash, len(algos))
	for _, a := range algos {
		h, _ := fetch.NewHash(a)
		hs[a] = h
	}
	return hs
}

func sums(hs map[string]hash.Hash) map[string]string {
	out := make(map[string]string, len(hs))
	for a, h := range hs {
		out[a] = hex.EncodeToString(h.Sum(nil))
	}
	return out
}

func badPack(name, reason string) *addons.Error {
	return fail(KindBadPack, kv("pack", name, "reason", reason),
		fmt.Sprintf("%s cannot be installed: its archive %s.", name, reason),
		"Choose another version of the pack. Nothing was installed.")
}

func unsafePath(name string, err error) *addons.Error {
	return &addons.Error{Notice: notice(KindUnsafePath, kv("pack", name),
		fmt.Sprintf("%s contains an unsafe path, so Playkeeper will not install it: %s.", name, err.Error()),
		"Playkeeper refuses packs that could write outside the server's folder. Nothing was installed."), Err: err}
}
