package addons

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
)

const maxFileName = 128

// validFileName accepts a plain .jar name: no folders, control or format
// characters, not hidden, at most maxFileName bytes.
func validFileName(name string) bool {
	if len(name) < len("x.jar") || len(name) > maxFileName || !strings.HasSuffix(name, ".jar") || !utf8.ValidString(name) {
		return false
	}
	if name[0] == '.' || name[0] == ' ' || name[0] == '-' {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// validFolderName accepts a Bukkit plugin name as a folder inside plugins/.
func validFolderName(name string) bool {
	if name == "" || len(name) > 64 || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ' ' || r == '_' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

func badFileName(s Step) *Error {
	return fail(KindBadFileName, kv("name", s.Name, "file", printable(s.FileName)),
		fmt.Sprintf("%s offers a file named \"%s\", which Playkeeper will not write to the server.", s.Name, printable(s.FileName)),
		"Add-on files must have a plain .jar name without folders. Nothing was installed.")
}

func folderError(t Target, err error) *Error {
	return &Error{Notice: notice(KindFolderUnusable, kv("folder", t.Folder),
		"Playkeeper cannot use the server's "+t.Folder+" folder: "+err.Error()+".",
		"Make sure "+t.Folder+" is a normal folder inside the server's files (not a link), then try again."), Err: err}
}

// openFolder opens the server's add-on folder. It returns nil, nil when the
// folder does not exist and create is false. Links are refused, and os.Root
// keeps every later operation inside the folder.
func openFolder(srv Server, t Target, create bool) (*os.Root, error) {
	data, err := os.OpenRoot(srv.Dir)
	if err != nil {
		return nil, folderError(t, err)
	}
	defer data.Close()
	fi, err := data.Lstat(t.Folder)
	if errors.Is(err, fs.ErrNotExist) && create {
		if err := data.Mkdir(t.Folder, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, folderError(t, err)
		}
		if err := chown(data, t.Folder, srv.Owner); err != nil {
			return nil, folderError(t, err)
		}
		fi, err = data.Lstat(t.Folder)
	}
	switch {
	case errors.Is(err, fs.ErrNotExist) && !create:
		return nil, nil
	case err != nil:
		return nil, folderError(t, err)
	case !fi.IsDir():
		return nil, folderError(t, errors.New("it is not a folder"))
	}
	if openHook != nil {
		openHook(t.Folder)
	}
	// With the trailing "/." os.Root opens the folder with O_DIRECTORY, so a
	// named pipe the game swaps in fails at once instead of blocking.
	r, err := data.OpenRoot(t.Folder + "/.")
	if err != nil {
		return nil, folderError(t, err)
	}
	if st, err := r.Stat("."); err != nil || !os.SameFile(fi, st) {
		r.Close()
		return nil, folderError(t, errors.New("it was replaced while Playkeeper was opening it"))
	}
	return r, nil
}

func chown(r *os.Root, name string, o *Owner) error {
	if o == nil {
		return nil
	}
	return r.Lchown(name, o.UID, o.GID)
}

// errNotRegular refuses what is not a regular file, or no longer the one
// that was checked.
var errNotRegular = errors.New("not a regular file")

// openHook, when a test sets it, runs between the check of a file or of the
// add-on folder and its open, so the test can swap it in between.
var openHook func(name string)

// openFile opens name in r for reading. The game can swap any file in the
// folder at any moment, so the name is checked, opened once without
// waiting on a named pipe, and the handle must then be the regular file
// that was checked; everything afterwards reads through that handle.
func openFile(r *os.Root, name string) (*os.File, fs.FileInfo, error) {
	fi, err := r.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, errNotRegular
	}
	if openHook != nil {
		openHook(name)
	}
	f, err := r.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err == nil && (!st.Mode().IsRegular() || !os.SameFile(fi, st)) {
		err = errNotRegular
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, st, nil
}

// sumFile hashes the first size bytes of f with each algorithm. It stops
// when ctx ends, and fails with errNotRegular when f has shrunk.
func sumFile(ctx context.Context, f *os.File, size int64, algos ...string) (map[string]string, error) {
	ws := make([]io.Writer, len(algos))
	hs := make(map[string]hash.Hash, len(algos))
	for i, a := range algos {
		h, err := fetch.NewHash(a)
		if err != nil {
			return nil, err
		}
		ws[i], hs[a] = h, h
	}
	n, err := io.Copy(io.MultiWriter(ws...), ctxReader{ctx, io.NewSectionReader(f, 0, size)})
	if err != nil {
		return nil, err
	}
	if n != size {
		return nil, errNotRegular
	}
	out := make(map[string]string, len(algos))
	for a, h := range hs {
		out[a] = hex.EncodeToString(h.Sum(nil))
	}
	return out, nil
}

// unchanged reports whether f, a regular file of size bytes, still holds
// rec's file. The size is compared first, so a file the game grew to
// terabytes is never read; a record without a size is hashed only up to
// max bytes.
func unchanged(ctx context.Context, f *os.File, size int64, rec Installed, max int64) (bool, error) {
	if !validHash(rec.HashAlgo, rec.Hash) || rec.Size > 0 && size != rec.Size || rec.Size <= 0 && size > max {
		return false, nil
	}
	sums, err := sumFile(ctx, f, size, rec.HashAlgo)
	if errors.Is(err, errNotRegular) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return sums[rec.HashAlgo] == strings.ToLower(rec.Hash), nil
}

// ctxReader ends a read when its context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// place copies the verified file src into r as name without replacing
// anything: a hidden temporary copy first, then a hard link to the name.
func place(r *os.Root, src, name string, o *Owner) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := "." + name + ".playkeeper-" + randomHex()
	out, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	defer r.Remove(tmp)
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = chown(r, tmp, o)
	}
	if err != nil {
		return err
	}
	if err := r.Link(tmp, name); err == nil || errors.Is(err, fs.ErrExist) {
		return err
	}
	// Some filesystems have no hard links; check, then rename.
	if _, err := r.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
		return fs.ErrExist
	}
	return r.Rename(tmp, name)
}

func randomHex() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
