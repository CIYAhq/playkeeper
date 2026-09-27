package gamefiles

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Entry is one thing in a folder as Lstat describes it: a link or a special
// file is described as itself, never followed.
type Entry struct {
	Name    string
	Mode    fs.FileMode
	Size    int64
	ModTime time.Time
}

// List lists the folder name ("." for the data directory), sorted by name. It
// reads at most limit entries; more reports that the folder has others, which
// are left out. An entry that disappears while the folder is listed is left
// out too.
func (d *Dir) List(name string, limit int) (entries []Entry, more bool, err error) {
	fi, err := d.folder(name)
	if err != nil {
		return nil, false, err
	}
	d.step("list", name)
	f, err := d.openFolder(name, fi)
	if err != nil {
		return nil, false, err
	}
	names, err := f.Readdirnames(limit + 1)
	f.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	if len(names) > limit {
		names, more = names[:limit], true
	}
	slices.Sort(names)
	entries = make([]Entry, 0, len(names))
	for _, n := range names {
		st, err := d.root.Lstat(join(name, n))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		entries = append(entries, Entry{Name: n, Mode: st.Mode(), Size: st.Size(), ModTime: st.ModTime()})
	}
	return entries, more, nil
}

// MakeFolder makes the folder name, and the folders missing on the way, for
// the game's user. A name something already has is refused.
func (d *Dir) MakeFolder(name string) error {
	ps, err := split(name)
	if err != nil {
		return err
	}
	if _, err := d.folders(ps[:len(ps)-1], true); err != nil {
		return err
	}
	if err := d.absent(name); err != nil {
		return err
	}
	d.step("mkdir", name)
	err = d.root.Mkdir(name, 0o750)
	if errors.Is(err, fs.ErrExist) {
		return existsError(name)
	}
	if err != nil {
		return err
	}
	if d.owner == nil {
		return nil
	}
	fi, err := d.root.Lstat(name)
	if err != nil {
		return err
	}
	if err := folderError(name, fi); err != nil {
		return err
	}
	d.step("chown", name)
	f, err := d.openFolder(name, fi)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Chown(d.owner.UID, d.owner.GID)
}

// Move renames from to to. Whatever is at from moves as it is, without being
// followed, a link or a special file too, so that it can be put out of the
// way. The folders on the way to both must be real folders, to must not
// exist, and a folder can't move into itself. Something that appears at to
// meanwhile, such as a file a plugin writes, is refused rather than
// replaced, where the file system can tell in the same step.
func (d *Dir) Move(from, to string) error {
	fps, err := split(from)
	if err != nil {
		return err
	}
	tps, err := split(to)
	if err != nil {
		return err
	}
	if to == from || strings.HasPrefix(to, from+"/") {
		return intoItselfError(from, to)
	}
	ffi, err := d.folders(fps[:len(fps)-1], false)
	if err != nil {
		return err
	}
	tfi, err := d.folders(tps[:len(tps)-1], false)
	if err != nil {
		return err
	}
	if _, err := d.root.Lstat(from); err != nil {
		return err
	}
	if err := d.absent(to); err != nil {
		return err
	}
	d.step("move", from)
	err = d.renameNew(from, ffi, to, tfi)
	if errors.Is(err, errNoReplace) {
		return d.root.Rename(from, to)
	}
	return err
}

// Delete removes name: a file, a link or a special file itself (never what a
// link leads to), or a folder with everything in it. The folders on the way
// must be real folders, so nothing outside the data directory, or behind a
// link inside it, can be reached.
func (d *Dir) Delete(name string) error {
	ps, err := split(name)
	if err != nil {
		return err
	}
	if _, err := d.folders(ps[:len(ps)-1], false); err != nil {
		return err
	}
	fi, err := d.root.Lstat(name)
	if err != nil {
		return err
	}
	d.step("delete", name)
	if fi.IsDir() {
		return d.root.RemoveAll(name)
	}
	return d.root.Remove(name)
}

// Place moves staged, a file of Playkeeper's own outside the data directory,
// to name: the folders missing on the way are made for the game's user, and
// the file is given to it with perm. What is at name is replaced only when
// replace is set, and only if it is a regular file; without replace,
// something that appears at name meanwhile is refused, as Move refuses it.
// The file is renamed into a handle on its folder, checked to be that
// folder, so a link swapped in on the way can't take it anywhere else;
// where the two folders are on different file systems it is copied the way
// WriteFrom writes.
func (d *Dir) Place(name, staged string, perm fs.FileMode, replace bool) error {
	ps, err := split(name)
	if err != nil {
		return err
	}
	parent := path.Dir(name)
	pfi, err := d.folders(ps[:len(ps)-1], true)
	if err != nil {
		return err
	}
	if parent == "." {
		if pfi, err = d.root.Lstat("."); err != nil {
			return err
		}
	}
	if fi, err := d.root.Lstat(name); err == nil {
		if err := fileError(name, fi); err != nil {
			return err
		}
		if !replace {
			return existsError(name)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	src, err := os.OpenFile(staged, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := src.Chmod(perm); err != nil {
		return err
	}
	if d.owner != nil {
		if err := src.Chown(d.owner.UID, d.owner.GID); err != nil {
			return err
		}
	}
	d.step("place", name)
	pf, err := d.openFolder(parent, pfi)
	if err != nil {
		return err
	}
	defer pf.Close()
	err = renameInto(staged, pf, path.Base(name), replace)
	if errors.Is(err, fs.ErrExist) {
		return existsError(name)
	}
	if !errors.Is(err, errOtherFileSystem) {
		return err
	}
	if err := d.writeFrom(name, perm, !replace, func(w io.Writer) error {
		_, err := io.Copy(w, src)
		return err
	}); err != nil {
		return err
	}
	return os.Remove(staged)
}

// errOtherFileSystem is renameInto's answer when the staged file and the
// folder are on different file systems.
var errOtherFileSystem = errors.New("the file is on another file system")

// maxDepth is how many folders down Walk and Count go from where they
// start. The game and its plugins keep files a dozen or so folders deep, as
// a map keeps its tiles, and each folder on the way is checked again for
// each one below it, so a far deeper chain, which only something planting
// it makes, is refused rather than walked.
const maxDepth = 64

// Walk calls fn for name and, when it is a folder, everything in it, depth
// first in name order: each folder before what is in it. Links and special
// files are passed too, for fn to skip; Walk never follows or opens them. A
// folder with more than limit entries is refused, and so are folders more
// than maxDepth down. Walk stops at the first error fn returns, and when
// ctx ends.
func (d *Dir) Walk(ctx context.Context, name string, limit int, fn func(p string, e Entry) error) error {
	var fi fs.FileInfo
	var err error
	if name == "." {
		fi, err = d.root.Lstat(".")
	} else {
		fi, err = d.Lstat(name)
	}
	if err != nil {
		return err
	}
	return d.walk(ctx, name, name, 0, Entry{Name: path.Base(name), Mode: fi.Mode(), Size: fi.Size(), ModTime: fi.ModTime()}, limit, fn)
}

// Count counts name and everything in it, links and special files too, as
// Walk would pass them, but not the data directory itself. It stops once
// there are more than limit and returns limit+1, and refuses folders more
// than maxDepth down, as Walk does. A folder opened in the root describes
// each entry relative to its own handle, whether or not the file system
// lists types, so an entry costs one call rather than List's look-up of
// it from the top.
func (d *Dir) Count(ctx context.Context, name string, limit int) (int, error) {
	var fi fs.FileInfo
	var err error
	n := 0
	if name == "." {
		fi, err = d.root.Lstat(".")
	} else {
		fi, err = d.Lstat(name)
		n = 1
	}
	switch {
	case err != nil:
		return 0, err
	case n > limit:
		return limit + 1, nil
	case !fi.IsDir():
		return n, nil
	}
	if err := d.count(ctx, name, name, 0, limit, &n); err != nil {
		return 0, err
	}
	return n, nil
}

func (d *Dir) count(ctx context.Context, start, p string, depth, limit int, n *int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > maxDepth {
		return tooDeepError(start, maxDepth)
	}
	es, err := d.ReadDir(p, limit-*n)
	if KindOf(err) == KindTooMany {
		*n = limit + 1
		return nil
	}
	if err != nil {
		return err
	}
	*n += len(es)
	for _, e := range es {
		if !e.IsDir() {
			continue
		}
		if err := d.count(ctx, start, join(p, e.Name()), depth+1, limit, n); err != nil || *n > limit {
			return err
		}
	}
	return nil
}

func (d *Dir) walk(ctx context.Context, start, p string, depth int, e Entry, limit int, fn func(p string, e Entry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := fn(p, e); err != nil || !e.Mode.IsDir() {
		return err
	}
	if depth > maxDepth {
		return tooDeepError(start, maxDepth)
	}
	entries, more, err := d.List(p, limit)
	if err != nil {
		return err
	}
	if more {
		return tooManyError(p, limit)
	}
	for _, c := range entries {
		if err := d.walk(ctx, start, join(p, c.Name), depth+1, c, limit, fn); err != nil {
			return err
		}
	}
	return nil
}

// absent refuses a name something already has, saying so of a link or a
// special file, which a plugin may have put there.
func (d *Dir) absent(name string) error {
	fi, err := d.root.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case fi.Mode()&fs.ModeSymlink != 0:
		return linkError(name)
	case !fi.IsDir() && !fi.Mode().IsRegular():
		return specialError(name, fi.Mode())
	}
	return existsError(name)
}

// join is p/name, or name alone in the data directory.
func join(p, name string) string {
	if p == "." {
		return name
	}
	return p + "/" + name
}
