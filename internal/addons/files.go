package addons

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"strings"
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
	switch {
	case errors.Is(err, fs.ErrNotExist) && create:
		if err := data.Mkdir(t.Folder, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, folderError(t, err)
		}
		if err := chown(data, t.Folder, srv.Owner); err != nil {
			return nil, folderError(t, err)
		}
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, folderError(t, err)
	case !fi.IsDir():
		return nil, folderError(t, errors.New("it is not a folder"))
	}
	r, err := data.OpenRoot(t.Folder)
	if err != nil {
		return nil, folderError(t, err)
	}
	return r, nil
}

func chown(r *os.Root, name string, o *Owner) error {
	if o == nil {
		return nil
	}
	return r.Lchown(name, o.UID, o.GID)
}

var errNotRegular = errors.New("not a regular file")

// sumFile hashes a regular file in r with each algorithm.
func sumFile(r *os.Root, name string, algos ...string) (map[string]string, int64, error) {
	fi, err := r.Lstat(name)
	if err != nil {
		return nil, 0, err
	}
	if !fi.Mode().IsRegular() {
		return nil, 0, errNotRegular
	}
	f, err := r.Open(name)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	ws := make([]io.Writer, len(algos))
	hs := make(map[string]hash.Hash, len(algos))
	for i, a := range algos {
		h, err := fetch.NewHash(a)
		if err != nil {
			return nil, 0, err
		}
		ws[i], hs[a] = h, h
	}
	n, err := io.Copy(io.MultiWriter(ws...), f)
	if err != nil {
		return nil, 0, err
	}
	out := make(map[string]string, len(algos))
	for a, h := range hs {
		out[a] = hex.EncodeToString(h.Sum(nil))
	}
	return out, n, nil
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
