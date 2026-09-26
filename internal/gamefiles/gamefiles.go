// Package gamefiles is how the root agent reads and writes files in a
// server's data directory. The game, and every plugin or mod it runs, can
// write there, so nothing found in it is trusted:
//
//   - every path is resolved through one os.Root, so nothing outside the
//     directory can be reached;
//   - each folder on the way and the file itself are checked with Lstat, and
//     links, named pipes, sockets and devices are refused, not followed or
//     opened;
//   - files are opened without blocking and checked again on the handle, so
//     a file swapped in after the check is refused;
//   - reads are capped, and hashing reads only the size the file had when it
//     was opened and stops when its context ends;
//   - writes go to a new hidden file in the same folder that is then renamed
//     over the old one, and only what Playkeeper has just made is given to
//     the game's user, through its own handle.
//
// A link that stays inside the directory could only reach what the game can
// change anyway; it is refused too, so that a refusal names the file to fix.
// Hard links are not checked: the game runs in a container that sees only
// this directory, so it cannot link to anything outside it. The directory
// itself is trusted, because its parent folder belongs to root.
package gamefiles

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// MaxProperties caps server.properties, which is a few kilobytes.
const MaxProperties = 1 << 20

const propertiesName = "server.properties"

// maxEnsured caps what EnsureFile reads to compare; a larger file is
// replaced.
const maxEnsured = 64 << 10

// Owner is the game's user. What Playkeeper makes in the data directory is
// given to it, so the game can change it later.
type Owner struct{ UID, GID int }

// Dir is a server's data directory. Paths are slash-separated and relative
// to it, like "plugins/bStats/config.yml".
type Dir struct {
	root  *os.Root
	owner *Owner
	// hook, when a test sets it, runs between a check and the step the
	// check guards, so the test can swap the file in between.
	hook func(step, name string)
}

// Open opens dataDir. What Playkeeper makes is given to owner; with a nil
// owner it keeps the agent's user, as when the agent does not run as root.
func Open(dataDir string, owner *Owner) (*Dir, error) {
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, err
	}
	return &Dir{root: root, owner: owner}, nil
}

func (d *Dir) Close() error { return d.root.Close() }

// ReadFile returns name, a regular file of at most limit bytes.
func (d *Dir) ReadFile(name string, limit int64) ([]byte, error) {
	f, _, err := d.open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, tooLargeError(name, limit)
	}
	return b, nil
}

// ReadTail returns the last limit bytes of name, or all of it when it is
// smaller, for logs the game keeps writing to.
func (d *Dir) ReadTail(name string, limit int64) ([]byte, error) {
	f, st, err := d.open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.NewSectionReader(f, max(st.Size()-limit, 0), limit))
}

// ReadJSON decodes name, at most limit bytes, into v.
func (d *Dir) ReadJSON(name string, limit int64, v any) error {
	b, err := d.ReadFile(name, limit)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// ReadProperties returns server.properties.
func (d *Dir) ReadProperties() ([]byte, error) {
	return d.ReadFile(propertiesName, MaxProperties)
}

// WriteProperties replaces server.properties with b.
func (d *Dir) WriteProperties(b []byte) error {
	if len(b) > MaxProperties {
		return refuse(KindTooLarge, propertiesName, "The new server.properties would be larger than "+sizeText(MaxProperties)+", the most Playkeeper writes.",
			"", "limit", strconv.Itoa(MaxProperties))
	}
	return d.WriteFile(propertiesName, b, 0o640)
}

// WriteFile replaces name with b. Missing folders on the way are made. The
// file is written to a new hidden file in the same folder first and then
// renamed over name, so the game never reads half a file, and what was at
// name is replaced, never written through.
func (d *Dir) WriteFile(name string, b []byte, perm fs.FileMode) error {
	ps, err := split(name)
	if err != nil {
		return err
	}
	if _, err := d.folders(ps[:len(ps)-1], true); err != nil {
		return err
	}
	if fi, err := d.root.Lstat(name); err == nil {
		if err := fileError(name, fi); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	tmp := path.Join(path.Dir(name), "."+path.Base(name)+".playkeeper-"+randomHex())
	d.step("create", tmp)
	f, err := d.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil && d.owner != nil {
		err = f.Chown(d.owner.UID, d.owner.GID)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = d.root.Rename(tmp, name)
	}
	if err != nil {
		d.root.Remove(tmp)
	}
	return err
}

// EnsureFile makes sure name holds b. The file is left as it is when keep
// accepts what it holds (with a nil keep, when that is b), and written
// otherwise; a file too large to compare is replaced.
func (d *Dir) EnsureFile(name string, b []byte, perm fs.FileMode, keep func([]byte) bool) error {
	if keep == nil {
		keep = func(cur []byte) bool { return bytes.Equal(cur, b) }
	}
	cur, err := d.ReadFile(name, max(maxEnsured, int64(len(b))))
	switch {
	case err == nil && keep(cur):
		return nil
	case err == nil, errors.Is(err, fs.ErrNotExist), KindOf(err) == KindTooLarge:
		return d.WriteFile(name, b, perm)
	}
	return err
}

// SHA256 returns the hex SHA-256 of name, a regular file of at most limit
// bytes. It reads only the size the file had when it was opened and stops
// when ctx ends, so a file the game keeps growing, or a sparse file of
// terabytes, cannot keep the agent busy.
func (d *Dir) SHA256(ctx context.Context, name string, limit int64) (string, error) {
	f, st, err := d.open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	size := st.Size()
	if size > limit {
		return "", tooLargeError(name, limit)
	}
	d.step("read", name)
	h := sha256.New()
	n, err := io.Copy(h, &ctxReader{ctx: ctx, r: io.LimitReader(f, size)})
	if err != nil {
		return "", err
	}
	if n != size {
		return "", changedError(name)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ReadDir lists the folder name ("." for the data directory), sorted by
// name, and refuses one with more than limit entries. Entry types are as
// listed; a file opened afterwards is checked again.
func (d *Dir) ReadDir(name string, limit int) ([]fs.DirEntry, error) {
	fi, err := d.folder(name)
	if err != nil {
		return nil, err
	}
	d.step("list", name)
	f, err := d.openFolder(name, fi)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	es, err := f.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(es) > limit {
		return nil, tooManyError(name, limit)
	}
	slices.SortFunc(es, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return es, nil
}

// open opens name for reading. The folders on the way and the file are
// checked before, and the handle after: os.Root follows links that stay
// inside it whatever the flags say, and the game can swap the file in
// between.
func (d *Dir) open(name string) (*os.File, fs.FileInfo, error) {
	ps, err := split(name)
	if err != nil {
		return nil, nil, err
	}
	if _, err := d.folders(ps[:len(ps)-1], false); err != nil {
		return nil, nil, err
	}
	fi, err := d.root.Lstat(name)
	if err == nil {
		err = fileError(name, fi)
	}
	if err != nil {
		return nil, nil, err
	}
	d.step("open", name)
	f, err := d.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err == nil && (!st.Mode().IsRegular() || !os.SameFile(fi, st)) {
		err = changedError(name)
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, st, nil
}

// folder checks the folder name and every folder on the way to it.
func (d *Dir) folder(name string) (fs.FileInfo, error) {
	if name == "." {
		return d.root.Lstat(".")
	}
	ps, err := split(name)
	if err != nil {
		return nil, err
	}
	return d.folders(ps, false)
}

// folders checks ps[0], ps[0]/ps[1] and so on from the top: each must be a
// folder, not a link. Missing ones are made when create is set. It returns
// what Lstat said about the last one.
func (d *Dir) folders(ps []string, create bool) (fs.FileInfo, error) {
	var fi fs.FileInfo
	for i := range ps {
		p := path.Join(ps[:i+1]...)
		var err error
		fi, err = d.root.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) && create {
			fi, err = d.mkdir(p)
		}
		if err == nil {
			err = folderError(p, fi)
		}
		if err != nil {
			return nil, err
		}
	}
	return fi, nil
}

// mkdir makes the folder p and gives it to the owner through a handle on the
// folder it made. A folder that appeared meanwhile is not Playkeeper's and
// keeps its owner.
func (d *Dir) mkdir(p string) (fs.FileInfo, error) {
	err := d.root.Mkdir(p, 0o750)
	if errors.Is(err, fs.ErrExist) {
		return d.root.Lstat(p)
	}
	if err != nil {
		return nil, err
	}
	fi, err := d.root.Lstat(p)
	if err != nil || d.owner == nil || !fi.IsDir() {
		return fi, err
	}
	d.step("chown", p)
	f, err := d.openFolder(p, fi)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return fi, f.Chown(d.owner.UID, d.owner.GID)
}

// openFolder opens the folder p, which Lstat described as fi, and checks
// that the handle is that folder.
func (d *Dir) openFolder(p string, fi fs.FileInfo) (*os.File, error) {
	f, err := d.root.OpenFile(p, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err == nil && (!st.IsDir() || !os.SameFile(fi, st)) {
		err = changedError(p)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (d *Dir) step(step, name string) {
	if d.hook != nil {
		d.hook(step, name)
	}
}

// split checks a path from Playkeeper's code and returns its parts.
func split(name string) ([]string, error) {
	if !fs.ValidPath(name) || name == "." {
		return nil, badNameError(name)
	}
	return strings.Split(name, "/"), nil
}

// fileError refuses anything but a regular file.
func fileError(p string, fi fs.FileInfo) error {
	switch mode := fi.Mode(); {
	case mode&fs.ModeSymlink != 0:
		return linkError(p)
	case mode.IsDir():
		return notFileError(p)
	case !mode.IsRegular():
		return specialError(p, mode)
	}
	return nil
}

// folderError refuses anything but a folder.
func folderError(p string, fi fs.FileInfo) error {
	switch mode := fi.Mode(); {
	case mode&fs.ModeSymlink != 0:
		return linkError(p)
	case !mode.IsDir():
		return notFolderError(p)
	}
	return nil
}

// ctxReader ends a long read when its context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func randomHex() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
