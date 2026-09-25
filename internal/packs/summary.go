package packs

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"time"
)

const (
	// maxIconBytes and maxIconSide bound the pack.png Icon returns.
	maxIconBytes = 256 << 10
	maxIconSide  = 1024
	// leftoverAge is how old a temporary file in the resource pack folder
	// must be before Prune deletes it.
	leftoverAge = time.Hour
)

// Summary is what a list shows of a pack.
type Summary struct {
	// Description is the pack's description as plain text, or empty when
	// its pack.mcmeta can't be read.
	Description string
	// Icon reports whether the pack has a pack.png small enough for Icon
	// to read. Icon still checks that it is a picture.
	Icon bool
}

// Summarize reads the description of the pack zip of size bytes read from
// r, used as use, and looks for its icon. Unlike Inspect it reads only
// pack.mcmeta, so it is quick enough for lists, and it checks nothing
// else: a zip it can't read has an empty Summary.
func Summarize(ctx context.Context, r io.ReaderAt, size int64, use Kind) Summary {
	var s Summary
	zr, ok := openZip(r, size)
	if !ok {
		return s
	}
	if f := zipEntry(zr, "pack.mcmeta"); f != nil {
		if b, ok := readSmall(ctx, f, maxMcmetaBytes); ok {
			s.Description = describe(b, use)
		}
	}
	if f := zipEntry(zr, "pack.png"); f != nil && f.Mode().IsRegular() && f.UncompressedSize64 <= maxIconBytes {
		s.Icon = true
	}
	return s
}

// PackIcon returns the pack.png of the pack zip of size bytes read from r,
// when it is a PNG picture of at most 256 KiB and 1,024 pixels a side.
// Otherwise it fails with an error matching ErrNoIcon.
func PackIcon(ctx context.Context, r io.ReaderAt, size int64) ([]byte, error) {
	zr, ok := openZip(r, size)
	if !ok {
		return nil, noIcon()
	}
	f := zipEntry(zr, "pack.png")
	if f == nil {
		return nil, noIcon()
	}
	b, ok := readSmall(ctx, f, maxIconBytes)
	if !ok {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, noIcon()
	}
	return checkIcon(b)
}

// Summary reads the description of the installed data pack name, a zip or
// a folder, and looks for its icon.
func (d DataPacks) Summary(ctx context.Context, name string) (Summary, error) {
	root, target, dir, err := d.openPack(name)
	if err != nil {
		return Summary{}, err
	}
	defer root.Close()
	if dir {
		var s Summary
		if b, ok := readSmallFile(root, target+"/pack.mcmeta", maxMcmetaBytes); ok {
			s.Description = describe(b, Data)
		}
		if st, err := root.Lstat(target + "/pack.png"); err == nil && st.Mode().IsRegular() && st.Size() <= maxIconBytes {
			s.Icon = true
		}
		return s, nil
	}
	f, st, err := openRegular(root, target)
	if err != nil {
		return Summary{}, notInstalled(name)
	}
	defer f.Close()
	return Summarize(ctx, f, st.Size(), Data), nil
}

// Icon returns the icon of the installed data pack name, as PackIcon does.
func (d DataPacks) Icon(ctx context.Context, name string) ([]byte, error) {
	root, target, dir, err := d.openPack(name)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if dir {
		b, ok := readSmallFile(root, target+"/pack.png", maxIconBytes)
		if !ok {
			return nil, noIcon()
		}
		return checkIcon(b)
	}
	f, st, err := openRegular(root, target)
	if err != nil {
		return nil, notInstalled(name)
	}
	defer f.Close()
	return PackIcon(ctx, f, st.Size())
}

// openPack opens the server's folder and finds the installed data pack
// name in it: its path in the folder, and whether it is a folder pack.
// The caller closes root.
func (d DataPacks) openPack(name string) (root *os.Root, target string, dir bool, err error) {
	if !validFileName(name) {
		return nil, "", false, &Error{
			Code:   CodeInvalidName,
			Params: map[string]any{"name": shortName(name)},
			Msg:    fmt.Sprintf("%s isn't the name of a file in the datapacks folder.", shortQuote(name)),
		}
	}
	if err := checkLevel(d.Level); err != nil {
		return nil, "", false, err
	}
	root, err = os.OpenRoot(d.DataDir)
	if err != nil {
		return nil, "", false, fileFailed("open the server's folder", err)
	}
	target = d.Level + "/datapacks/" + name
	st, err := root.Lstat(target)
	switch {
	case err == nil && st.IsDir():
		return root, target, true, nil
	case err == nil && st.Mode().IsRegular() && strings.HasSuffix(name, ".zip"):
		return root, target, false, nil
	case err == nil || errors.Is(err, fs.ErrNotExist):
		err = notInstalled(name)
	default:
		err = fileFailed("read the data pack", err)
	}
	root.Close()
	return nil, "", false, err
}

// Icon returns the icon of the stored pack whose SHA-1 hash is sum, as
// PackIcon does.
func (s Store) Icon(ctx context.Context, sum string) ([]byte, error) {
	if !validSHA1(sum) {
		return nil, invalidSHA1(sum)
	}
	f, st, err := s.open(sum)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &Error{Code: CodeNotFound, Params: map[string]any{"sha1": sum}, Msg: "That resource pack isn't stored."}
	}
	if err != nil {
		return nil, fileFailed("read the resource pack", err)
	}
	defer f.Close()
	return PackIcon(ctx, f, st.Size())
}

// Prune deletes the stored packs for which keep returns false, and
// temporary files an interrupted Put left behind more than an hour ago.
// Other files are left alone.
func (s Store) Prune(keep func(sum string) bool) error {
	root, err := os.OpenRoot(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fileFailed("open the resource pack folder", err)
	}
	defer root.Close()
	f, err := root.Open(".")
	if err != nil {
		return fileFailed("read the resource pack folder", err)
	}
	entries, err := f.ReadDir(maxListed)
	f.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return fileFailed("read the resource pack folder", err)
	}
	var errs []error
	for _, e := range entries {
		name := e.Name()
		sum, isPack := strings.CutSuffix(name, ".zip")
		switch {
		case isPack && validSHA1(sum):
			if keep(sum) {
				continue
			}
		case strings.HasPrefix(name, ".") && strings.Contains(name, ".zip.playkeeper-"):
			if fi, err := e.Info(); err != nil || time.Since(fi.ModTime()) < leftoverAge {
				continue
			}
		default:
			continue
		}
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fileFailed("remove unused resource packs", errors.Join(errs...))
	}
	return nil
}

// openZip reads a zip's table of contents after the checks Inspect makes
// of it.
func openZip(r io.ReaderAt, size int64) (*zip.Reader, bool) {
	records, err := checkDirectory(r, size, DefaultLimits().MaxFiles)
	if err != nil {
		return nil, false
	}
	zr, err := zip.NewReader(r, size)
	if (err != nil && !errors.Is(err, zip.ErrInsecurePath)) || len(zr.File) != records {
		return nil, false
	}
	return zr, true
}

// zipEntry is the first entry named name, as the game finds it.
func zipEntry(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// readSmall decompresses f when it is a plain, unencrypted file of at most
// max bytes.
func readSmall(ctx context.Context, f *zip.File, max int64) ([]byte, bool) {
	if !f.Mode().IsRegular() || f.Flags&(flagEncrypted|flagStrongEncryption) != 0 || f.UncompressedSize64 > uint64(max) {
		return nil, false
	}
	var b bytes.Buffer
	if err := readEntry(ctx, f, &b); err != nil || int64(b.Len()) > max {
		return nil, false
	}
	return b.Bytes(), true
}

// readSmallFile reads the plain file name in root when it has at most max
// bytes.
func readSmallFile(root *os.Root, name string, max int64) ([]byte, bool) {
	f, st, err := openRegular(root, name)
	if err != nil || st.Size() > max {
		return nil, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(b)) > max {
		return nil, false
	}
	return b, true
}

// openRegular opens the plain file name in root. Opening without blocking
// and checking the type afterwards keeps a FIFO or device from hanging or
// misleading the caller.
func openRegular(root *os.Root, name string) (*os.File, fs.FileInfo, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err == nil && !st.Mode().IsRegular() {
		err = errors.New("not a regular file")
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, st, nil
}

// describe is the description in a pack.mcmeta, or empty when the game
// couldn't read it.
func describe(b []byte, use Kind) string {
	m, err := parseMcmeta(b, use)
	if err != nil {
		return ""
	}
	return m.description
}

// checkIcon returns b when it is a whole PNG picture of at most 1,024
// pixels a side.
func checkIcon(b []byte) ([]byte, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxIconSide || cfg.Height > maxIconSide {
		return nil, noIcon()
	}
	if _, err := png.Decode(bytes.NewReader(b)); err != nil {
		return nil, noIcon()
	}
	return b, nil
}

func noIcon() *Error {
	return &Error{Code: CodeNoIcon, Msg: "The pack has no icon Playkeeper can show."}
}
