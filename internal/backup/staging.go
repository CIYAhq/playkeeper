package backup

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"time"
)

const (
	blockSize = 4096
	copyChunk = 8 << 20
	// copyAttempts bounds how often a file that changes or disappears while
	// being copied is tried.
	copyAttempts = 3
)

// Size is what a backup of a data directory holds.
type Size struct {
	Files int
	// Bytes is the archived content (server.properties without its secrets).
	Bytes int64
	// DiskBytes is what a staging copy occupies: each file rounded up to
	// whole 4 KiB blocks, plus a block per directory.
	DiskBytes int64
}

// ArchiveBytes bounds the archive's size: the content, the tar headers and
// manifest line of each file, and gzip framing for content that does not
// compress (most region data does not).
func (s Size) ArchiveBytes() int64 {
	return s.Bytes + s.Bytes/256 + int64(s.Files)*3072 + 1<<20
}

// Measure sizes a backup of dataDir without writing anything. Like Create, it
// returns a *RefusedError for a world a restore would refuse. Zero Limits
// mean DefaultLimits.
func Measure(dataDir string, lim Limits) (Size, error) {
	rels, err := archiveFiles(dataDir, LevelName(dataDir))
	if err != nil {
		return Size{}, err
	}
	tally := fileTally{lim: lim.orDefault()}
	dirs := map[string]bool{}
	var s Size
	for _, rel := range rels {
		size, err := archivedSize(dataDir, rel)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return s, err
		}
		if err := tally.add(rel, size); err != nil {
			return s, refusal(rel, err)
		}
		s.Files++
		s.Bytes += size
		s.DiskBytes += (size + blockSize - 1) / blockSize * blockSize
		for d := path.Dir(rel); d != "." && !dirs[d]; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	s.DiskBytes += int64(len(dirs)) * blockSize
	return s, nil
}

func (l Limits) orDefault() Limits {
	if l == (Limits{}) {
		return DefaultLimits()
	}
	return l
}

// stager copies the allowlisted files of a data directory to a staging
// directory, so a running server's saving is paused only while copying.
type stager struct {
	retryDelay time.Duration
	// inject lets tests fail or disturb a named step: "open <rel>" and
	// "copy <rel>" here, "archive" for each write of the archive.
	inject func(step string) error
	copied int64
}

var errChanged = errors.New("the file changed while it was being copied")

func (s *stager) step(name string) error {
	if s.inject == nil {
		return nil
	}
	return s.inject(name)
}

// copyAll copies the allowlisted files of dataDir into the empty directory
// dst. server.properties is copied without its secrets.
func (s *stager) copyAll(ctx context.Context, dataDir, dst string) error {
	rels, err := archiveFiles(dataDir, LevelName(dataDir))
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, rel := range rels {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.copyFile(ctx, root, rel, filepath.Join(dst, filepath.FromSlash(rel))); err != nil {
			return err
		}
	}
	return nil
}

// copyFile copies one file, again if it changed meanwhile. A file that has
// gone is left out, as it would be from an archive written a moment later,
// but only after looking again: Minecraft replaces level.dat and player files
// by renaming the old file away before renaming the new one in.
func (s *stager) copyFile(ctx context.Context, root *os.Root, rel, dst string) error {
	for attempt := 1; ; attempt++ {
		n, err := s.copyOnce(ctx, root, rel, dst)
		if err == nil {
			s.copied += n
			return nil
		}
		os.Remove(dst)
		gone, changed := errors.Is(err, fs.ErrNotExist), errors.Is(err, errChanged)
		if !gone && !changed {
			return err
		}
		if attempt == copyAttempts {
			if gone {
				return nil
			}
			return errFileChanging(rel)
		}
		if err := sleep(ctx, s.retryDelay); err != nil {
			return err
		}
	}
}

func (s *stager) copyOnce(ctx context.Context, root *os.Root, rel, dst string) (int64, error) {
	if err := s.step("open " + rel); err != nil {
		return 0, err
	}
	src, before, err := openRegular(root, rel)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	if err := s.step("copy " + rel); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return 0, err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := copyContent(ctx, out, src, rel, before.Size())
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	after, err := src.Stat()
	if err != nil {
		return 0, err
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return 0, errChanged
	}
	return n, os.Chtimes(dst, before.ModTime(), before.ModTime())
}

// copyContent copies size bytes of src in chunks, so ctx can stop a large
// file. io.CopyN between two files uses copy_file_range, which clones the
// data instead on filesystems with reflinks (btrfs, XFS).
func copyContent(ctx context.Context, out, src *os.File, rel string, size int64) (int64, error) {
	if rel == "server.properties" {
		b, err := io.ReadAll(src)
		if err != nil {
			return 0, err
		}
		if int64(len(b)) != size {
			return 0, errChanged
		}
		n, err := out.Write(SanitizeProperties(b))
		return int64(n), err
	}
	var n int64
	for n < size {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		m, err := io.CopyN(out, src, min(size-n, copyChunk))
		n += m
		if errors.Is(err, io.EOF) {
			return n, errChanged
		}
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// sameFilesystem reports whether a and b are on one filesystem. When that
// cannot be told it says yes, which only makes the space check stricter.
func sameFilesystem(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return true
	}
	sb, err := os.Stat(b)
	if err != nil {
		return true
	}
	da, okA := deviceOf(sa)
	db, okB := deviceOf(sb)
	return !okA || !okB || da == db
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
