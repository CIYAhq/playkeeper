package packs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/gamefiles"
)

// maxListed bounds how many entries List reads from a datapacks folder.
const maxListed = 10_000

var reDataPackName = regexp.MustCompile(`^[A-Za-z0-9_+][A-Za-z0-9_.+\-]{0,95}\.zip$`)

// DataPacks manages the data packs of one world. The server can write to
// its data directory, so every file there is read and written through
// internal/gamefiles.
type DataPacks struct {
	// DataDir is the server's data directory, which holds its worlds.
	DataDir string
	// Level is the world's folder in DataDir: the server's level-name.
	Level string
	// Owner, when set, owns the folders and files Install creates.
	Owner *gamefiles.Owner
	// Limits bound the packs Install accepts.
	Limits Limits
}

// DataPack is a data pack in a world's datapacks folder.
type DataPack struct {
	// Name is the pack's file or folder name.
	Name string `json:"name"`
	// ID names the pack in the server's datapack command.
	ID string `json:"id"`
	// Size is the zip's size in bytes, or 0 for a folder.
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	// Folder marks a pack unpacked into a folder rather than zipped.
	// Playkeeper lists such packs but leaves them alone.
	Folder bool `json:"folder,omitempty"`
}

// DataPackID is the ID by which the server's datapack command knows the
// data pack with the given file or folder name.
func DataPackID(name string) string { return "file/" + name }

// CheckName reports whether name is a file name Install accepts: a name
// ending in .zip, of at most 100 letters, digits, dots, dashes, underscores
// and plus signs, starting with neither a dot nor a dash.
func CheckName(name string) error {
	if reDataPackName.MatchString(name) {
		return nil
	}
	return &Error{
		Code:   CodeInvalidName,
		Params: map[string]any{"name": shortName(name)},
		Msg:    fmt.Sprintf("%s isn't a name Playkeeper can give a data pack.", shortQuote(name)),
		Hint:   "Use a name ending in .zip, of at most 100 letters, digits, dots, dashes, underscores and plus signs.",
	}
}

// SafeName turns the name of an uploaded file into one CheckName accepts,
// replacing what it refuses with underscores.
func SafeName(upload string) string {
	base := upload[strings.LastIndexAny(upload, `/\`)+1:]
	if len(base) >= 4 && strings.EqualFold(base[len(base)-4:], ".zip") {
		base = base[:len(base)-4]
	}
	var b strings.Builder
	gap := false
	for _, r := range base {
		allowed := r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.+-", r))
		switch {
		case !allowed:
			gap = true
		case b.Len() == 0 && (r == '.' || r == '-'):
		default:
			if gap && b.Len() > 0 {
				b.WriteByte('_')
			}
			gap = false
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > 96 {
		s = s[:96]
	}
	if s = strings.TrimRight(s, "."); s == "" {
		s = "datapack"
	}
	return s + ".zip"
}

// Install checks a data pack with Inspect and installs it as name in the
// world's datapacks folder. It creates the folder, and the world's folder,
// when missing: packs installed before a world exists shape how it is
// generated. It replaces an installed pack only when replace is set. A
// running server notices the pack only when told; see Console. Calls for
// one server must not overlap.
func (d DataPacks) Install(ctx context.Context, name string, src io.ReaderAt, size int64, replace bool) (Info, DataPack, error) {
	if err := CheckName(name); err != nil {
		return Info{}, DataPack{}, err
	}
	if err := checkLevel(d.Level); err != nil {
		return Info{}, DataPack{}, err
	}
	info, err := Inspect(ctx, src, size, Data, d.Limits)
	if err != nil {
		return Info{}, DataPack{}, err
	}
	files, err := gamefiles.Open(d.DataDir, d.Owner)
	if err != nil {
		return Info{}, DataPack{}, fileFailed("open the server's folder", err)
	}
	defer files.Close()
	target := d.Level + "/datapacks/" + name
	switch st, err := files.Lstat(target); {
	case err == nil && st.IsDir():
		return Info{}, DataPack{}, folderPack(name)
	case err == nil && st.Mode().IsRegular() && !replace:
		return Info{}, DataPack{}, alreadyInstalled(name)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return Info{}, DataPack{}, fileFailed("install the data pack", err)
	}
	err = files.WriteFrom(target, 0o644, func(w io.Writer) error {
		h := sha256.New()
		if _, err := io.Copy(io.MultiWriter(w, h), ctxReader{ctx, io.NewSectionReader(src, 0, size)}); err != nil {
			return err
		}
		if hex.EncodeToString(h.Sum(nil)) != info.SHA256 {
			return errors.New("the pack changed while it was being copied")
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return Info{}, DataPack{}, ctx.Err()
		}
		return Info{}, DataPack{}, fileFailed("install the data pack", err)
	}
	pack := DataPack{Name: name, ID: DataPackID(name), Size: size}
	if st, err := files.Lstat(target); err == nil {
		pack.ModTime = st.ModTime()
	}
	return info, pack, nil
}

// List returns the data packs in the world's datapacks folder, sorted by
// name, as the game finds them: zips whose names end in ".zip", and folders
// holding a pack.mcmeta file. Links are skipped, as the game skips them. A
// world without a datapacks folder has none; one with more than 10,000
// entries is refused.
func (d DataPacks) List() ([]DataPack, error) {
	if err := checkLevel(d.Level); err != nil {
		return nil, err
	}
	files, err := gamefiles.Open(d.DataDir, nil)
	if err != nil {
		return nil, fileFailed("open the server's folder", err)
	}
	defer files.Close()
	dir := d.Level + "/datapacks"
	entries, err := files.ReadDir(dir, maxListed)
	if errors.Is(err, fs.ErrNotExist) {
		return []DataPack{}, nil
	}
	if err != nil {
		return nil, fileFailed("read the world's datapacks folder", err)
	}
	packs := []DataPack{}
	for _, e := range entries {
		name := e.Name()
		fi, err := files.Lstat(dir + "/" + name)
		if err != nil {
			continue
		}
		pack := DataPack{Name: name, ID: DataPackID(name), ModTime: fi.ModTime()}
		switch {
		case fi.Mode().IsRegular() && strings.HasSuffix(name, ".zip"):
			pack.Size = fi.Size()
		case fi.IsDir():
			st, err := files.Lstat(dir + "/" + name + "/pack.mcmeta")
			if err != nil || !st.Mode().IsRegular() {
				continue
			}
			pack.Folder = true
		default:
			continue
		}
		packs = append(packs, pack)
	}
	return packs, nil
}

// Remove deletes an installed zip data pack. The server keeps the pack's
// data until it reloads, so disable the pack first; see Console.Disable.
func (d DataPacks) Remove(name string) error {
	if !validFileName(name) {
		return &Error{
			Code:   CodeInvalidName,
			Params: map[string]any{"name": shortName(name)},
			Msg:    fmt.Sprintf("%s isn't the name of a file in the datapacks folder.", shortQuote(name)),
		}
	}
	if err := checkLevel(d.Level); err != nil {
		return err
	}
	files, err := gamefiles.Open(d.DataDir, nil)
	if err != nil {
		return fileFailed("open the server's folder", err)
	}
	defer files.Close()
	target := d.Level + "/datapacks/" + name
	st, err := files.Lstat(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return notInstalled(name)
	case err != nil:
		return fileFailed("remove the data pack", err)
	case st.IsDir():
		return folderPack(name)
	case !strings.HasSuffix(name, ".zip"):
		return notInstalled(name)
	}
	if err := files.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fileFailed("remove the data pack", err)
	}
	return nil
}

// validFileName accepts a single file name without control characters.
func validFileName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && utf8.ValidString(name) &&
		!strings.ContainsAny(name, `/\`) && !strings.ContainsFunc(name, unicode.IsControl)
}

func checkLevel(level string) error {
	if validFileName(level) {
		return nil
	}
	return &Error{
		Code:   CodeInvalidLevel,
		Params: map[string]any{"level": shortName(level)},
		Msg:    fmt.Sprintf("The server's world folder, %s, isn't a plain folder name.", shortQuote(level)),
		Hint:   "Set level-name in server.properties to a plain folder name, such as world.",
	}
}

func alreadyInstalled(name string) *Error {
	return &Error{
		Code:   CodeAlreadyInstalled,
		Params: map[string]any{"name": shortName(name)},
		Msg:    fmt.Sprintf("A data pack named %s is already installed.", shortQuote(name)),
		Hint:   "Remove it first, or choose to replace it.",
	}
}

func notInstalled(name string) *Error {
	return &Error{
		Code:   CodeNotFound,
		Params: map[string]any{"name": shortName(name)},
		Msg:    fmt.Sprintf("No data pack named %s is installed.", shortQuote(name)),
	}
}

func folderPack(name string) *Error {
	return &Error{
		Code:   CodeFolderPack,
		Params: map[string]any{"name": shortName(name)},
		Msg:    fmt.Sprintf("%s in the datapacks folder is an unpacked folder, which Playkeeper doesn't change.", shortQuote(name)),
		Hint:   "Change the folder yourself with a file manager or over SFTP, or give the new pack another name.",
	}
}
