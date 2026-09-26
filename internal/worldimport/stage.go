package worldimport

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Stage writes what Plan describes into dir, which must not exist yet. dir
// then holds the world folders, and the server files and add-on folders the
// owner chose to keep, exactly as they go into the server's data directory.
// Stage refuses with an *Error of kind import_blocked while the preview has
// problems, and of kind archive_changed when an upload changed since
// Inspect. If anything fails, dir is removed again.
func (in *Inspection) Stage(ctx context.Context, dir string, t Target, o Options) (*Preview, error) {
	p, ops, err := in.plan(t, o)
	if err != nil {
		return nil, err
	}
	if !p.OK() {
		return p, blockedError(p.Problems)
	}
	if err := os.Mkdir(dir, 0o750); err != nil {
		return p, fmt.Errorf("worldimport: create staging directory: %w", err)
	}
	if err := in.write(ctx, dir, ops); err != nil {
		os.RemoveAll(dir)
		return p, err
	}
	return p, nil
}

func blockedError(problems []Message) *Error {
	kinds := make([]string, len(problems))
	for i, m := range problems {
		kinds[i] = m.Kind
	}
	msg := problems[0].Text
	if len(problems) > 1 {
		msg += fmt.Sprintf(" The preview lists %s.", plural(len(problems)-1, "more problem", "more problems"))
	}
	return refuse(KindBlocked, problems[0].Hint, msg, "problems", kinds)
}

// stageError is a failure to write the staging directory, which is not the
// upload's fault.
type stageError struct{ err error }

func (e *stageError) Error() string { return "worldimport: write staging directory: " + e.err.Error() }
func (e *stageError) Unwrap() error { return e.err }

// write copies each archive's planned entries; an entry can go to several
// places, like the level.dat that Paper's Nether and End folders get a copy
// of.
func (in *Inspection) write(ctx context.Context, dir string, ops []copyOp) error {
	byArc := make([]map[int][]string, len(in.ix.arcs))
	for _, op := range ops {
		a := in.ix.entries[op.e].arc
		if byArc[a] == nil {
			byArc[a] = map[int][]string{}
		}
		byArc[a][op.e] = append(byArc[a][op.e], op.dest)
	}
	for a, want := range byArc {
		if len(want) == 0 {
			continue
		}
		if err := in.writeArchive(ctx, dir, &in.ix.arcs[a], want); err != nil {
			return err
		}
	}
	return nil
}

func (in *Inspection) writeArchive(ctx context.Context, dir string, info *ArchiveInfo, want map[int][]string) error {
	f, err := os.Open(info.path)
	if errors.Is(err, fs.ErrNotExist) {
		return changedError(info.Name)
	}
	if err != nil {
		return fmt.Errorf("worldimport: open upload: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("worldimport: open upload: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Size() != info.Bytes || !fi.ModTime().Equal(info.modTime) {
		return changedError(info.Name)
	}
	if info.Format == FormatZip {
		return in.writeZip(ctx, dir, f, info, want)
	}
	return in.writeTar(ctx, dir, f, info, want)
}

// writeZip re-reads the zip's directory and checks every planned entry
// against what Inspect saw before writing it.
func (in *Inspection) writeZip(ctx context.Context, dir string, f *os.File, info *ArchiveInfo, want map[int][]string) error {
	zr, err := openZip(ctx, f, info, in.lim, in.lim.MaxEntries)
	if err != nil {
		return err
	}
	if len(zr.File) != info.Entries {
		return changedError(info.Name)
	}
	order := make([]int, 0, len(want))
	for i := range want {
		order = append(order, i)
	}
	sort.Slice(order, func(a, b int) bool { return in.ix.entries[order[a]].pos < in.ix.entries[order[b]].pos })
	for _, i := range order {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := in.ix.entries[i]
		zf := zr.File[e.pos]
		name, isDir, err := zipEntry(zf, info, in.lim)
		if err != nil || isDir || info.folder+"/"+name != e.name ||
			int64(zf.UncompressedSize64) != e.size || int64(zf.CompressedSize64) != e.csize {
			return changedError(info.Name)
		}
		rc, err := zf.Open()
		if err != nil {
			return readError(ctx, info, in.lim, err)
		}
		err = writeEntry(ctx, dir, want[i], e.size, rc)
		rc.Close()
		if err != nil {
			return readError(ctx, info, in.lim, err)
		}
	}
	return nil
}

// writeTar reads the tar from the start again, since a tar can't be read
// out of order, and checks every header against what Inspect saw.
func (in *Inspection) writeTar(ctx context.Context, dir string, f *os.File, info *ArchiveInfo, want map[int][]string) error {
	byPos := make(map[int]int, len(want))
	for i := range want {
		byPos[int(in.ix.entries[i].pos)] = i
	}
	found, headers := 0, 0
	err := walkTar(ctx, f, info, in.lim, func(pos int, h *tar.Header, r io.Reader) error {
		headers = pos + 1
		i, ok := byPos[pos]
		if !ok {
			return nil
		}
		e := in.ix.entries[i]
		name, isDir, err := tarEntry(h, in.lim)
		if err != nil || isDir || info.folder+"/"+name != e.name || h.Size != e.size {
			return changedError(info.Name)
		}
		found++
		return writeEntry(ctx, dir, want[i], e.size, r)
	})
	if err != nil {
		return err
	}
	if found != len(want) || headers != info.Entries {
		return changedError(info.Name)
	}
	return nil
}

func writeEntry(ctx context.Context, root string, dests []string, size int64, r io.Reader) error {
	if err := writeFile(ctx, root, dests[0], size, r); err != nil {
		return err
	}
	for _, d := range dests[1:] {
		if err := copyStaged(ctx, root, dests[0], d, size); err != nil {
			return err
		}
	}
	return nil
}

var errSizeMismatch = errors.New("worldimport: file is longer than the archive says")

// writeFile writes exactly size bytes from r to a new file at dest inside
// root. Errors of the staging directory come back as *stageError; anything
// else is an error reading the upload.
func writeFile(ctx context.Context, root, dest string, size int64, r io.Reader) error {
	p, err := stagedPath(root, dest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return &stageError{err}
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return &stageError{err}
	}
	n, err := io.Copy(stageWriter{f}, ctxReader{ctx, io.LimitReader(r, size+1)})
	if cerr := f.Close(); err == nil && cerr != nil {
		err = &stageError{cerr}
	}
	if err == nil && n != size {
		err = errSizeMismatch
	}
	return err
}

func copyStaged(ctx context.Context, root, from, to string, size int64) error {
	src, err := stagedPath(root, from)
	if err != nil {
		return err
	}
	f, err := os.Open(src)
	if err != nil {
		return &stageError{err}
	}
	defer f.Close()
	err = writeFile(ctx, root, to, size, f)
	var se *stageError
	if err != nil && !errors.As(err, &se) && ctx.Err() == nil {
		return &stageError{err}
	}
	return err
}

// stagedPath resolves dest inside root. Planned paths are clean already;
// this keeps a mistake from ever writing outside root.
func stagedPath(root, dest string) (string, error) {
	p := filepath.Join(root, filepath.FromSlash(dest))
	if !strings.HasPrefix(p, filepath.Clean(root)+string(filepath.Separator)) {
		return "", &stageError{fmt.Errorf("%q is outside the staging directory", dest)}
	}
	return p, nil
}

type stageWriter struct{ f *os.File }

func (w stageWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	if err != nil {
		return n, &stageError{err}
	}
	return n, nil
}
