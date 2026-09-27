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
// of. What it writes counts against MaxTotalBytes as it is written, and
// what it reads from an archive against the archive's ratio allowance,
// whatever the archive's headers say.
func (in *Inspection) write(ctx context.Context, dir string, ops []copyOp) error {
	byArc := make([]map[int][]string, len(in.ix.arcs))
	for _, op := range ops {
		a := in.ix.entries[op.e].arc
		if byArc[a] == nil {
			byArc[a] = map[int][]string{}
		}
		byArc[a][op.e] = append(byArc[a][op.e], op.dest)
	}
	b := &stageBudget{left: in.lim.MaxTotalBytes, limit: in.lim.MaxTotalBytes}
	for a, want := range byArc {
		if len(want) == 0 {
			continue
		}
		if err := in.writeArchive(ctx, dir, &in.ix.arcs[a], want, b); err != nil {
			return err
		}
	}
	return nil
}

func (in *Inspection) writeArchive(ctx context.Context, dir string, info *ArchiveInfo, want map[int][]string, b *stageBudget) error {
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
	arc := &readCap{n: ratioAllowance(info.Bytes, in.lim.MaxRatio), err: ratioError(info.Name, in.lim.MaxRatio)}
	if info.Format == FormatZip {
		return in.writeZip(ctx, dir, f, info, want, arc, b)
	}
	return in.writeTar(ctx, dir, f, info, want, arc, b)
}

// writeZip re-reads the zip's directory and checks every planned entry
// against what Inspect saw before writing it. Each entry may expand only to
// its own allowance, counted in the bytes it gives.
func (in *Inspection) writeZip(ctx context.Context, dir string, f *os.File, info *ArchiveInfo, want map[int][]string, arc *readCap, b *stageBudget) error {
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
		own := &readCap{n: entryAllowance(e.csize, in.lim.MaxRatio), err: arc.err}
		err = writeEntry(ctx, dir, want[i], e.size, &capReader{r: rc, caps: []*readCap{own, arc}}, b)
		rc.Close()
		if err != nil {
			return readError(ctx, info, in.lim, err)
		}
	}
	return nil
}

// writeTar reads the tar from the start again, since a tar can't be read
// out of order, and checks every header against what Inspect saw.
func (in *Inspection) writeTar(ctx context.Context, dir string, f *os.File, info *ArchiveInfo, want map[int][]string, arc *readCap, b *stageBudget) error {
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
		return writeEntry(ctx, dir, want[i], e.size, &capReader{r: r, caps: []*readCap{arc}}, b)
	})
	if err != nil {
		return err
	}
	if found != len(want) || headers != info.Entries {
		return changedError(info.Name)
	}
	return nil
}

func writeEntry(ctx context.Context, root string, dests []string, size int64, r io.Reader, b *stageBudget) error {
	if err := writeFile(ctx, root, dests[0], size, r, b); err != nil {
		return err
	}
	for _, d := range dests[1:] {
		if err := copyStaged(ctx, root, dests[0], d, size, b); err != nil {
			return err
		}
	}
	return nil
}

// readCap is how many more bytes a reader may give, and the error once it
// has given more.
type readCap struct {
	n   int64
	err error
}

// capReader reads from r while every cap allows, counting what it reads
// against each.
type capReader struct {
	r    io.Reader
	caps []*readCap
}

func (c *capReader) Read(p []byte) (int, error) {
	for _, cp := range c.caps {
		if cp.n < int64(len(p))-1 {
			p = p[:cp.n+1]
		}
	}
	n, err := c.r.Read(p)
	for _, cp := range c.caps {
		if cp.n -= int64(n); cp.n < 0 {
			return 0, cp.err
		}
	}
	return n, err
}

// stageBudget is what Stage may still write of MaxTotalBytes, copies
// included.
type stageBudget struct{ left, limit int64 }

func writtenTooLarge(limit int64) *Error {
	return refuse(KindTooLarge, "Free up disk space on the server, or leave out plugins, mods and other worlds.",
		fmt.Sprintf("The import would write more than the %s this server has room for.", humanBytes(limit)),
		"limit", limit)
}

var errSizeMismatch = errors.New("worldimport: file is longer than the archive says")

// writeFile writes exactly size bytes from r to a new file at dest inside
// root. Errors of the staging directory come back as *stageError; anything
// else is an error reading the upload.
func writeFile(ctx context.Context, root, dest string, size int64, r io.Reader, b *stageBudget) error {
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
	n, err := io.Copy(stageWriter{f, b}, ctxReader{ctx, io.LimitReader(r, size+1)})
	if cerr := f.Close(); err == nil && cerr != nil {
		err = &stageError{cerr}
	}
	if err == nil && n != size {
		err = errSizeMismatch
	}
	return err
}

func copyStaged(ctx context.Context, root, from, to string, size int64, b *stageBudget) error {
	src, err := stagedPath(root, from)
	if err != nil {
		return err
	}
	f, err := os.Open(src)
	if err != nil {
		return &stageError{err}
	}
	defer f.Close()
	err = writeFile(ctx, root, to, size, f, b)
	var se *stageError
	var we *Error
	if err != nil && !errors.As(err, &se) && !errors.As(err, &we) && ctx.Err() == nil {
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

// stageWriter writes to a staged file, counting what it writes against
// the import's MaxTotalBytes before it writes it.
type stageWriter struct {
	f *os.File
	b *stageBudget
}

func (w stageWriter) Write(p []byte) (int, error) {
	if w.b.left -= int64(len(p)); w.b.left < 0 {
		return 0, writtenTooLarge(w.b.limit)
	}
	n, err := w.f.Write(p)
	if err != nil {
		return n, &stageError{err}
	}
	return n, nil
}
