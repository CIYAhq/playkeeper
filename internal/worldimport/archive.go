package worldimport

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Source is one uploaded archive. Name is the file name the user uploaded;
// the archive's contents appear in a folder of that name without its
// extension. Path is where the file is on disk.
type Source struct {
	Name string `json:"name"`
	Path string `json:"-"`
}

// Archive formats, recognised by their content rather than their name.
const (
	FormatZip   = "zip"
	FormatTarGz = "tar.gz"
	FormatTar   = "tar"
)

// ArchiveInfo describes one uploaded archive.
type ArchiveInfo struct {
	Name    string `json:"name"`
	Format  string `json:"format"`
	Bytes   int64  `json:"bytes"`
	Entries int    `json:"entries"`

	folder  string
	path    string
	modTime time.Time
}

const (
	// maxArchives is how many archives one import may combine, such as the
	// three world downloads Aternos offers for a Paper server.
	maxArchives  = 16
	maxNameBytes = 255
	// maxZipDirectory bounds the names, extra fields and comments of a
	// zip's central directory, which archive/zip holds in memory.
	maxZipDirectory = 128 << 20
	// captureBudget bounds the small files Inspect keeps in memory, such as
	// level.dat and server.properties.
	captureBudget = 64 << 20
)

var (
	archiveExts  = []string{".tar.gz", ".tgz", ".zip", ".tar"}
	bedrockExts  = []string{".mcworld", ".mcpack", ".mctemplate", ".mcaddon"}
	otherFormats = []struct{ ext, name string }{
		{".tar.xz", "tar.xz"}, {".txz", "tar.xz"}, {".tar.bz2", "tar.bz2"}, {".tbz2", "tar.bz2"},
		{".tar.zst", "tar.zst"}, {".7z", "7z"}, {".rar", "RAR"}, {".xz", "xz"}, {".bz2", "bzip2"},
		{".zst", "Zstandard"},
	}
)

// captureLimit is the largest size of a file with this name Inspect reads
// into memory, or 0 for files it doesn't read.
func captureLimit(base string) int64 {
	switch base {
	case "level.dat", "level.dat_old", "world_gen_settings.dat":
		return 4 << 20
	case "server.properties", "ops.json":
		return 1 << 20
	}
	return 0
}

// entry is one file or folder of an upload. Names start with the folder of
// the archive they come from, so entries of all archives sort together.
type entry struct {
	name  string
	size  int64
	csize int64 // compressed size in a zip, -1 in a tar
	arc   int32
	pos   int32 // index in the zip's file list, or header number in a tar
	dir   bool
}

// index lists every entry of an upload, sorted by name, with the contents
// of the few small files detection needs.
type index struct {
	arcs    []ArchiveInfo
	entries []entry
	data    map[string][]byte
}

func buildIndex(ctx context.Context, sources []Source, lim Limits) (*index, error) {
	if len(sources) > maxArchives {
		return nil, tooManyArchives()
	}
	ix := &index{data: map[string][]byte{}}
	used := map[string]bool{}
	budget := int64(captureBudget)
	for _, s := range sources {
		folder, err := checkName(s.Name)
		if err != nil {
			return nil, err
		}
		name := folder
		for i := 2; used[name]; i++ {
			name = fmt.Sprintf("%s (%d)", folder, i)
		}
		used[name] = true
		if err := ix.add(ctx, s, name, lim, &budget); err != nil {
			return nil, err
		}
	}
	if err := ix.finish(); err != nil {
		return nil, err
	}
	return ix, nil
}

func (ix *index) add(ctx context.Context, s Source, folder string, lim Limits, budget *int64) error {
	f, err := os.Open(s.Path)
	if err != nil {
		return fmt.Errorf("worldimport: open upload: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("worldimport: open upload: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("worldimport: upload %s is not a regular file", s.Path)
	}
	head := make([]byte, 512)
	n, err := f.ReadAt(head, 0)
	if err != nil && err != io.EOF {
		return fmt.Errorf("worldimport: read upload: %w", err)
	}
	format, err := sniff(head[:n], s.Name)
	if err != nil {
		return err
	}
	info := ArchiveInfo{Name: s.Name, Format: format, Bytes: fi.Size(), folder: folder, path: s.Path, modTime: fi.ModTime()}
	arc := len(ix.arcs)
	if format == FormatZip {
		err = ix.addZip(ctx, f, &info, arc, lim, budget)
	} else {
		err = ix.addTar(ctx, f, &info, arc, lim, budget)
	}
	if err != nil {
		return err
	}
	ix.arcs = append(ix.arcs, info)
	return nil
}

// CheckUploadName refuses, with an *Error, a file name Inspect would refuse
// by name alone, such as one with a slash or a Bedrock world's extension, so
// an upload can be refused before its bytes arrive.
func CheckUploadName(name string) error {
	_, err := checkName(name)
	return err
}

// checkName validates the name of an uploaded file and returns the folder
// its contents appear in.
func checkName(name string) (string, error) {
	if !validName(name) {
		return "", nameError(name)
	}
	for _, ext := range bedrockExts {
		if hasSuffixFold(name, ext) {
			return "", bedrockError()
		}
	}
	for _, f := range otherFormats {
		if hasSuffixFold(name, f.ext) {
			return "", formatError(name, f.name)
		}
	}
	folder := name
	for _, ext := range archiveExts {
		if hasSuffixFold(name, ext) {
			folder = name[:len(name)-len(ext)]
			break
		}
	}
	folder = strings.TrimSpace(folder)
	if folder == "" || folder == "." || folder == ".." {
		folder = "upload"
	}
	return folder, nil
}

func validName(name string) bool {
	if name == "" || len(name) > maxNameBytes || !utf8.ValidString(name) || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if r == '/' || r == '\\' || unsafeRune(r) {
			return false
		}
	}
	return true
}

func hasSuffixFold(s, suffix string) bool {
	return len(s) >= len(suffix) && strings.EqualFold(s[len(s)-len(suffix):], suffix)
}

// sniff recognises the archive format from the first bytes of the file.
func sniff(head []byte, name string) (string, error) {
	has := func(sig string) bool { return bytes.HasPrefix(head, []byte(sig)) }
	switch {
	case len(head) == 0:
		return "", emptyError(name)
	case has("PK\x03\x04"), has("PK\x05\x06"), has("PK\x07\x08"):
		return FormatZip, nil
	case has("\x1f\x8b"):
		return FormatTarGz, nil
	case len(head) >= 262 && string(head[257:262]) == "ustar":
		return FormatTar, nil
	case has("7z\xbc\xaf\x27\x1c"):
		return "", formatError(name, "7z")
	case has("Rar!\x1a\x07"):
		return "", formatError(name, "RAR")
	case has("\xfd7zXZ\x00"):
		return "", formatError(name, "xz")
	case has("BZh"):
		return "", formatError(name, "bzip2")
	case has("\x28\xb5\x2f\xfd"):
		return "", formatError(name, "Zstandard")
	}
	return "", formatError(name, "")
}

var errZipFormat = errors.New("worldimport: not a valid zip archive")

// zipDirectory is where archive/zip will look for the central directory:
// the number of entries it declares and the offsets it may start at.
type zipDirectory struct {
	records uint64
	starts  []int64
}

// findZipDirectory locates the central directory exactly as archive/zip
// does, so its size can be checked before archive/zip allocates for it.
func findZipDirectory(r io.ReaderAt, size int64) (zipDirectory, error) {
	var buf []byte
	endOff := int64(-1)
	for i, n := range []int64{1024, 65 * 1024} {
		n = min(n, size)
		buf = make([]byte, n)
		if _, err := r.ReadAt(buf, size-n); err != nil && err != io.EOF {
			return zipDirectory{}, err
		}
		if p := zipEndSignature(buf); p >= 0 {
			buf, endOff = buf[p:], size-n+int64(p)
			break
		}
		if i == 1 || n == size {
			return zipDirectory{}, errZipFormat
		}
	}
	le := binary.LittleEndian
	records := uint64(le.Uint16(buf[10:]))
	dirSize := uint64(le.Uint32(buf[12:]))
	dirOff := uint64(le.Uint32(buf[16:]))
	if int(le.Uint16(buf[20:])) > len(buf)-22 {
		return zipDirectory{}, errZipFormat
	}
	// archive/zip compares the 32-bit directory size with 0xffff too.
	if records == 0xffff || dirSize == 0xffff || dirOff == 0xffffffff {
		p, err := zip64Locator(r, endOff)
		if err == nil && p >= 0 {
			endOff = p
			records, dirSize, dirOff, err = zip64End(r, p)
		}
		if err != nil {
			return zipDirectory{}, err
		}
	}
	if dirSize > math.MaxInt64 || dirOff > math.MaxInt64 {
		return zipDirectory{}, errZipFormat
	}
	base := endOff - int64(dirSize) - int64(dirOff)
	start := base + int64(dirOff)
	if start < 0 || start >= size {
		return zipDirectory{}, errZipFormat
	}
	d := zipDirectory{records: records, starts: []int64{start}}
	// archive/zip falls back to a base offset of 0 when a header is there.
	if o := int64(dirOff); base > 0 && o != start && o < size {
		d.starts = append(d.starts, o)
	}
	return d, nil
}

func zipEndSignature(b []byte) int {
	for i := len(b) - 22; i >= 0; i-- {
		if b[i] == 'P' && b[i+1] == 'K' && b[i+2] == 0x05 && b[i+3] == 0x06 {
			if n := int(b[i+20]) | int(b[i+21])<<8; n+22+i > len(b) {
				return -1
			}
			return i
		}
	}
	return -1
}

func zip64Locator(r io.ReaderAt, endOff int64) (int64, error) {
	off := endOff - 20
	if off < 0 {
		return -1, nil
	}
	var b [20]byte
	if _, err := r.ReadAt(b[:], off); err != nil {
		return -1, err
	}
	le := binary.LittleEndian
	if le.Uint32(b[0:]) != 0x07064b50 || le.Uint32(b[4:]) != 0 || le.Uint32(b[16:]) != 1 {
		return -1, nil
	}
	return int64(le.Uint64(b[8:])), nil
}

func zip64End(r io.ReaderAt, off int64) (records, dirSize, dirOff uint64, err error) {
	var b [56]byte
	if _, err := r.ReadAt(b[:], off); err != nil {
		return 0, 0, 0, err
	}
	le := binary.LittleEndian
	if le.Uint32(b[0:]) != 0x06064b50 {
		return 0, 0, 0, errZipFormat
	}
	return le.Uint64(b[32:]), le.Uint64(b[40:]), le.Uint64(b[48:]), nil
}

// countZipHeaders counts central directory headers from start the way
// archive/zip reads them, until one is incomplete or lacks the signature.
// It stops once there are more than max, or their variable parts exceed
// maxZipDirectory bytes.
func countZipHeaders(r io.ReaderAt, size, start int64, max int) (n int, meta int64, err error) {
	br := bufio.NewReaderSize(io.NewSectionReader(r, start, size-start), 64<<10)
	var h [46]byte
	le := binary.LittleEndian
	for n <= max && meta <= maxZipDirectory {
		if _, err := io.ReadFull(br, h[:]); err != nil {
			return n, meta, endOK(err)
		}
		if le.Uint32(h[:]) != 0x02014b50 {
			return n, meta, nil
		}
		k := int(le.Uint16(h[28:])) + int(le.Uint16(h[30:])) + int(le.Uint16(h[32:]))
		if _, err := br.Discard(k); err != nil {
			return n, meta, endOK(err)
		}
		n++
		meta += int64(k)
	}
	return n, meta, nil
}

func endOK(err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return nil
	}
	return err
}

// openZip opens a zip archive once its central directory is known to fit
// in room entries.
func openZip(ctx context.Context, r io.ReaderAt, info *ArchiveInfo, lim Limits, room int) (*zip.Reader, error) {
	d, err := findZipDirectory(r, info.Bytes)
	if err != nil {
		return nil, readError(ctx, info, lim, err)
	}
	if d.records > uint64(room) {
		return nil, tooManyEntries(lim.MaxEntries)
	}
	for _, start := range d.starts {
		n, meta, err := countZipHeaders(r, info.Bytes, start, room)
		if err != nil {
			return nil, readError(ctx, info, lim, err)
		}
		if n > room || meta > maxZipDirectory {
			return nil, tooManyEntries(lim.MaxEntries)
		}
	}
	zr, err := zip.NewReader(r, info.Bytes)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, readError(ctx, info, lim, err)
	}
	if len(zr.File) > room {
		return nil, tooManyEntries(lim.MaxEntries)
	}
	return zr, nil
}

// zipEntry checks one zip entry and returns its clean name, which is ""
// for the archive's root folder.
func zipEntry(f *zip.File, info *ArchiveInfo, lim Limits) (name string, dir bool, err error) {
	mode := f.Mode()
	if mode&fs.ModeSymlink != 0 {
		return "", false, linkError(f.Name)
	}
	if mode&(fs.ModeDevice|fs.ModeCharDevice|fs.ModeNamedPipe|fs.ModeSocket|fs.ModeIrregular) != 0 {
		return "", false, specialError(f.Name)
	}
	dir = mode.IsDir() || strings.HasSuffix(f.Name, "/") || strings.HasSuffix(f.Name, `\`)
	if name, err = cleanName(f.Name, lim); err != nil {
		return "", false, err
	}
	if name == "" {
		if dir {
			return "", true, nil
		}
		return "", false, unsafeError(f.Name)
	}
	if dir {
		return name, true, nil
	}
	if f.Flags&0x1 != 0 {
		return "", false, encryptedError(info.Name)
	}
	if f.Method != zip.Store && f.Method != zip.Deflate {
		return "", false, compressionError(info.Name, f.Method)
	}
	if f.UncompressedSize64 > math.MaxInt64 || f.CompressedSize64 > math.MaxInt64 {
		return "", false, corruptError(info.Name)
	}
	return name, false, nil
}

func (ix *index) addZip(ctx context.Context, f *os.File, info *ArchiveInfo, arc int, lim Limits, budget *int64) error {
	zr, err := openZip(ctx, f, info, lim, lim.MaxEntries-len(ix.entries))
	if err != nil {
		return err
	}
	info.Entries = len(zr.File)
	first := len(ix.entries)
	var spans [][2]int64
	for i, zf := range zr.File {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		name, dir, err := zipEntry(zf, info, lim)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		e := entry{name: info.folder + "/" + name, csize: -1, arc: int32(arc), pos: int32(i), dir: dir}
		if !dir {
			e.size, e.csize = int64(zf.UncompressedSize64), int64(zf.CompressedSize64)
			if e.csize > 0 {
				off, err := zf.DataOffset()
				if err != nil {
					return readError(ctx, info, lim, err)
				}
				if off < 0 || off > info.Bytes-e.csize {
					return truncatedError(info.Name)
				}
				spans = append(spans, [2]int64{off, off + e.csize})
			}
		}
		ix.entries = append(ix.entries, e)
	}
	if overlapping(spans) {
		return overlapError(info.Name)
	}
	for _, e := range ix.entries[first:] {
		if !wanted(e, *budget) {
			continue
		}
		rc, err := zr.File[e.pos].Open()
		if err != nil {
			return readError(ctx, info, lim, err)
		}
		b, err := readExactly(rc, e.size)
		rc.Close()
		if err != nil {
			return readError(ctx, info, lim, err)
		}
		ix.data[e.name] = b
		*budget -= e.size
	}
	return nil
}

// overlapping reports whether any two spans of file data share bytes, the
// trick behind zip bombs that expand one piece of data many times.
func overlapping(spans [][2]int64) bool {
	slices.SortFunc(spans, func(a, b [2]int64) int {
		switch {
		case a[0] < b[0]:
			return -1
		case a[0] > b[0]:
			return 1
		}
		return 0
	})
	end := int64(math.MinInt64)
	for _, s := range spans {
		if s[0] < end {
			return true
		}
		end = max(end, s[1])
	}
	return false
}

func wanted(e entry, budget int64) bool {
	if e.dir {
		return false
	}
	limit := captureLimit(path.Base(e.name))
	return limit > 0 && e.size <= limit && e.size <= budget
}

// readExactly reads a file of the given size and then to its end, so a
// zip checksum is verified and a file longer than declared is noticed.
func readExactly(r io.Reader, size int64) ([]byte, error) {
	b := make([]byte, size)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	var one [1]byte
	n, err := io.ReadFull(r, one[:])
	if n > 0 {
		return nil, errors.New("worldimport: file longer than its declared size")
	}
	if err != io.EOF {
		return nil, err
	}
	return b, nil
}

var errStreamLimit = errors.New("worldimport: archive expands beyond its limit")

// limitReader stops a decompressed stream at n bytes with errStreamLimit,
// unless the stream ends right there.
type limitReader struct {
	r io.Reader
	n int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		var one [1]byte
		n, err := io.ReadFull(l.r, one[:])
		if n > 0 {
			return 0, errStreamLimit
		}
		return 0, err
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

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

// ratioAllowance is how many bytes an archive of n bytes may expand to.
func ratioAllowance(n, ratio int64) int64 {
	const slack = 1 << 20
	if n > (math.MaxInt64-slack)/ratio {
		return math.MaxInt64
	}
	return n*ratio + slack
}

// entryAllowance is how many bytes a zip entry of csize compressed bytes may
// expand to: any file up to 1 MB, and a larger one up to ratio times csize.
func entryAllowance(csize, ratio int64) int64 {
	if csize >= (math.MaxInt64-ratio)/ratio {
		return math.MaxInt64
	}
	return max(1<<20, (csize+1)*ratio-1)
}

// walkTar reads a tar or tar.gz archive from the start and calls fn with
// each header and a reader for its data. It reads to the end of the gzip
// stream so its checksum is verified.
func walkTar(ctx context.Context, f *os.File, info *ArchiveInfo, lim Limits, fn func(i int, h *tar.Header, r io.Reader) error) error {
	var src io.Reader = bufio.NewReaderSize(ctxReader{ctx, io.NewSectionReader(f, 0, info.Bytes)}, 256<<10)
	if info.Format == FormatTarGz {
		zr, err := gzip.NewReader(src)
		if err != nil {
			return readError(ctx, info, lim, err)
		}
		defer zr.Close()
		src = &limitReader{r: zr, n: ratioAllowance(info.Bytes, lim.MaxRatio)}
	}
	tr := tar.NewReader(src)
	for i := 0; ; i++ {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil && !errors.Is(err, tar.ErrInsecurePath) {
			if i == 0 && errors.Is(err, tar.ErrHeader) {
				return formatError(info.Name, "")
			}
			return readError(ctx, info, lim, err)
		}
		if err := fn(i, h, tr); err != nil {
			return readError(ctx, info, lim, err)
		}
	}
	if _, err := io.Copy(io.Discard, src); err != nil {
		return readError(ctx, info, lim, err)
	}
	return nil
}

// tarEntry checks one tar header and returns its clean name, which is ""
// for the archive's root folder and for headers that are no entry.
func tarEntry(h *tar.Header, lim Limits) (name string, dir bool, err error) {
	switch h.Typeflag {
	case tar.TypeXGlobalHeader:
		return "", false, nil
	case tar.TypeReg, tar.TypeGNUSparse:
		if sparse(h) {
			return "", false, sparseError(h.Name)
		}
	case tar.TypeDir:
		dir = true
	case tar.TypeSymlink, tar.TypeLink:
		return "", false, linkError(h.Name)
	default:
		return "", false, specialError(h.Name)
	}
	if h.Size < 0 {
		return "", false, errors.New("worldimport: negative size in tar header")
	}
	if name, err = cleanName(h.Name, lim); err != nil {
		return "", false, err
	}
	if name == "" && !dir {
		return "", false, unsafeError(h.Name)
	}
	return name, dir, nil
}

// sparse reports whether a tar member is a sparse file, in GNU's old format
// or as PAX records: its holes read as zeros that aren't in the archive, so
// it can expand far past it.
func sparse(h *tar.Header) bool {
	if h.Typeflag == tar.TypeGNUSparse {
		return true
	}
	for k := range h.PAXRecords {
		if strings.HasPrefix(k, "GNU.sparse.") {
			return true
		}
	}
	return false
}

func (ix *index) addTar(ctx context.Context, f *os.File, info *ArchiveInfo, arc int, lim Limits, budget *int64) error {
	room := lim.MaxEntries - len(ix.entries)
	return walkTar(ctx, f, info, lim, func(i int, h *tar.Header, r io.Reader) error {
		info.Entries = i + 1
		if i >= room {
			return tooManyEntries(lim.MaxEntries)
		}
		name, dir, err := tarEntry(h, lim)
		if err != nil || name == "" {
			return err
		}
		e := entry{name: info.folder + "/" + name, csize: -1, arc: int32(arc), pos: int32(i), dir: dir}
		if !dir {
			e.size = h.Size
		}
		ix.entries = append(ix.entries, e)
		if wanted(e, *budget) {
			b, err := readExactly(r, e.size)
			if err != nil {
				return err
			}
			ix.data[e.name] = b
			*budget -= e.size
		}
		return nil
	})
}

// readError turns an error from reading an archive into what the user
// sees. Errors of the disk the upload is on stay internal.
func readError(ctx context.Context, info *ArchiveInfo, lim Limits, err error) error {
	var e *Error
	var se *stageError
	var pe *fs.PathError
	switch {
	case errors.As(err, &e):
		return e
	case errors.As(err, &se):
		return se
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.As(err, &pe):
		return fmt.Errorf("worldimport: read %s: %w", info.Name, err)
	case errors.Is(err, errStreamLimit):
		return ratioError(info.Name, lim.MaxRatio)
	case errors.Is(err, io.ErrUnexpectedEOF):
		return truncatedError(info.Name)
	}
	return corruptError(info.Name)
}

// cleanName turns the name of an archive entry into a clean relative path,
// refusing anything that could land outside the folder it unpacks to.
func cleanName(raw string, lim Limits) (string, error) {
	if len(raw) > 4*lim.MaxPathLen {
		return "", pathTooLongError(raw, lim.MaxPathLen)
	}
	name := strings.ReplaceAll(strings.ToValidUTF8(raw, "_"), `\`, "/")
	if strings.HasPrefix(name, "/") || len(name) >= 2 && name[1] == ':' && isASCIILetter(name[0]) {
		return "", unsafeError(raw)
	}
	var parts []string
	for _, p := range strings.Split(name, "/") {
		switch p {
		case "", ".":
			continue
		case "..":
			return "", unsafeError(raw)
		}
		if strings.IndexFunc(p, unsafeRune) >= 0 {
			return "", unsafeError(raw)
		}
		if len(p) > maxNameBytes {
			return "", pathTooLongError(raw, lim.MaxPathLen)
		}
		parts = append(parts, p)
	}
	clean := strings.Join(parts, "/")
	if len(clean) > lim.MaxPathLen {
		return "", pathTooLongError(raw, lim.MaxPathLen)
	}
	return clean, nil
}

func isASCIILetter(b byte) bool { return b|0x20 >= 'a' && b|0x20 <= 'z' }

// finish sorts the entries and refuses names that appear twice or are both
// a file and a folder.
func (ix *index) finish() error {
	slices.SortFunc(ix.entries, func(a, b entry) int { return strings.Compare(a.name, b.name) })
	out := ix.entries[:0]
	for _, e := range ix.entries {
		if n := len(out); n > 0 && out[n-1].name == e.name {
			if out[n-1].dir && e.dir {
				continue
			}
			return duplicateError(ix.arcs[e.arc].Name, rel(e.name))
		}
		out = append(out, e)
	}
	ix.entries = out
	for _, e := range ix.entries {
		if e.dir {
			continue
		}
		if lo, hi := ix.under(e.name); lo < hi {
			return conflictError(ix.arcs[e.arc].Name, rel(e.name))
		}
	}
	return nil
}

func (ix *index) search(name string) int {
	return sort.Search(len(ix.entries), func(i int) bool { return ix.entries[i].name >= name })
}

// under returns the range of entries inside dir, or of all entries for "".
func (ix *index) under(dir string) (lo, hi int) {
	if dir == "" {
		return 0, len(ix.entries)
	}
	return ix.search(dir + "/"), ix.search(dir + "0")
}

func (ix *index) find(name string) (entry, bool) {
	if i := ix.search(name); i < len(ix.entries) && ix.entries[i].name == name {
		return ix.entries[i], true
	}
	return entry{}, false
}

func (ix *index) hasFile(name string) bool {
	e, ok := ix.find(name)
	return ok && !e.dir
}

// hasDir reports a folder, whether the archive lists it or only files in it.
func (ix *index) hasDir(name string) bool {
	if lo, hi := ix.under(name); lo < hi {
		return true
	}
	e, ok := ix.find(name)
	return ok && e.dir
}

// stat counts the files among entries[lo:hi] and their bytes.
func (ix *index) stat(lo, hi int) (files int, bytes int64) {
	for _, e := range ix.entries[lo:hi] {
		if !e.dir {
			files++
			bytes = addSat(bytes, e.size)
		}
	}
	return files, bytes
}

// children lists the names directly inside dir, sorted.
func (ix *index) children(dir string) []string {
	lo, hi := ix.under(dir)
	prefix := len(dir) + 1
	if dir == "" {
		prefix = 0
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range ix.entries[lo:hi] {
		child, _, _ := strings.Cut(e.name[prefix:], "/")
		if !seen[child] {
			seen[child] = true
			out = append(out, child)
		}
	}
	sort.Strings(out)
	return out
}

// archiveOf returns the archive a path of the upload comes from.
func (ix *index) archiveOf(p string) *ArchiveInfo {
	folder, _, _ := strings.Cut(p, "/")
	for i := range ix.arcs {
		if ix.arcs[i].folder == folder {
			return &ix.arcs[i]
		}
	}
	return nil
}

// rel returns a path of the upload as it is inside its archive.
func rel(p string) string {
	_, r, _ := strings.Cut(p, "/")
	return r
}

func addSat(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func methodName(m uint16) string {
	switch m {
	case 9:
		return "Deflate64"
	case 12:
		return "bzip2"
	case 14:
		return "LZMA"
	case 93:
		return "Zstandard"
	case 95:
		return "XZ"
	case 98:
		return "PPMd"
	case 99:
		return "AES"
	}
	return fmt.Sprintf("method %d", m)
}

func nameError(name string) *Error {
	if name == "" {
		return refuse(KindArchiveName, "Upload the world again as a .zip or .tar.gz file.",
			"The uploaded file has no name.", "name", "")
	}
	return refuse(KindArchiveName, "Rename the file to something short, like world.zip, and upload it again.",
		fmt.Sprintf("Playkeeper can't use the file name %s: it is too long or contains characters such as slashes or control characters.", quoted(name)),
		"name", clip(name))
}

func formatError(name, format string) *Error {
	if format == "" {
		return refuse(KindArchiveFormat, "Upload the world as a .zip or .tar.gz file. If you downloaded it from a host, download it again.",
			fmt.Sprintf("%s is not a .zip or .tar.gz archive, so Playkeeper can't open it.", quoted(name)),
			"name", clip(name), "format", "")
	}
	return refuse(KindArchiveFormat, "Unpack it on your computer, pack the world folder into a .zip file and upload that.",
		fmt.Sprintf("%s is a %s archive, which Playkeeper can't open.", quoted(name), format),
		"name", clip(name), "format", format)
}

func bedrockError() *Error {
	return refuse(KindBedrock, "Upload a Java Edition world instead. A Bedrock world has to be converted with a separate tool before a Java server can load it.",
		"This is a Bedrock Edition world. Playkeeper runs Java Edition servers, which can't load Bedrock worlds.")
}

func emptyError(name string) *Error {
	return refuse(KindArchiveCorrupt, "Upload the file again; it may not have finished uploading.",
		fmt.Sprintf("%s is empty.", quoted(name)), "name", clip(name))
}

func corruptError(name string) *Error {
	return refuse(KindArchiveCorrupt, "Download the world again and upload the new file. If you packed it yourself, create the archive again.",
		fmt.Sprintf("%s is damaged or incomplete, so Playkeeper can't read it.", quoted(name)), "name", clip(name))
}

func truncatedError(name string) *Error {
	return refuse(KindArchiveTruncated, "The download or the upload was probably cut off. Download the world again and upload the new file.",
		fmt.Sprintf("%s is incomplete: it ends before the files it lists do.", quoted(name)), "name", clip(name))
}

func tooManyArchives() *Error {
	return refuse(KindTooManyArchives, "Upload only the world folders you need, or pack them into one .zip file.",
		fmt.Sprintf("One import can combine at most %d archives.", maxArchives), "limit", maxArchives)
}

func tooManyEntries(limit int) *Error {
	return refuse(KindTooManyEntries, "Upload only the world folders instead of the whole server folder, and leave out logs and backups.",
		fmt.Sprintf("The upload lists more than %d files and folders, which is more than Playkeeper reads in one import.", limit),
		"limit", limit)
}

func unsafeError(p string) *Error {
	return refuse(KindUnsafePath, "Create the archive again from the world folder on your computer.",
		fmt.Sprintf("The archive contains an entry named %s, which points outside the folder it unpacks to. Playkeeper refuses such archives because they can overwrite other files.", quoted(p)),
		"path", clip(p))
}

func pathTooLongError(p string, limit int) *Error {
	return refuse(KindPathTooLong, "Shorten the folder names inside the archive and upload it again.",
		fmt.Sprintf("The archive contains a path longer than %d bytes: %s.", limit, quoted(p)),
		"path", clip(p), "limit", limit)
}

func linkError(p string) *Error {
	return refuse(KindLink, "Create the archive again without links, for example from a copy of the world folder.",
		fmt.Sprintf("The archive contains a link, %s. Playkeeper doesn't unpack links because they can point to files outside the world.", quoted(p)),
		"path", clip(p))
}

func sparseError(p string) *Error {
	return refuse(KindSparseFile, "Create the archive again without the sparse option, for example with tar -czf.",
		fmt.Sprintf("The archive stores %s as a sparse file, whose zeros aren't in the archive. Playkeeper doesn't unpack sparse files, since a small archive can expand them to fill a disk.", quoted(p)),
		"path", clip(p))
}

func specialError(p string) *Error {
	return refuse(KindSpecialFile, "Create the archive again from the world folder only.",
		fmt.Sprintf("The archive contains %s, which is neither a file nor a folder (for example a device or a pipe).", quoted(p)),
		"path", clip(p))
}

func encryptedError(name string) *Error {
	return refuse(KindEncrypted, "Create the archive again without a password.",
		fmt.Sprintf("%s is protected with a password. Playkeeper can't read encrypted archives.", quoted(name)),
		"name", clip(name))
}

func compressionError(name string, method uint16) *Error {
	m := methodName(method)
	return refuse(KindCompression, "Create the archive again as a normal .zip file (Deflate compression) or as a .tar.gz file.",
		fmt.Sprintf("%s uses %s compression, which Playkeeper can't read.", quoted(name), m),
		"name", clip(name), "method", m)
}

func ratioError(name string, limit int64) *Error {
	return refuse(KindRatio, "If this is a real world, create the archive again with normal compression and leave out large log files.",
		fmt.Sprintf("%s expands to more than %d times its own size. Archives built to fill up a disk look like this, so Playkeeper stopped reading it.", quoted(name), limit),
		"name", clip(name), "limit", limit)
}

func duplicateError(name, p string) *Error {
	return refuse(KindDuplicate, "Create the archive again from the world folder.",
		fmt.Sprintf("%s contains %s more than once.", quoted(name), quoted(p)), "path", clip(p))
}

func conflictError(name, p string) *Error {
	return refuse(KindDuplicate, "Create the archive again from the world folder.",
		fmt.Sprintf("%s contains both a file and a folder named %s.", quoted(name), quoted(p)), "path", clip(p))
}

func overlapError(name string) *Error {
	return refuse(KindOverlap, "Create the archive again with a normal zip tool.",
		fmt.Sprintf("%s contains files that share the same data, a trick used to build archives that expand without limit. Playkeeper refuses it.", quoted(name)),
		"name", clip(name))
}

func changedError(name string) *Error {
	return refuse(KindArchiveChanged, "Upload the world again and check the preview before applying it.",
		fmt.Sprintf("%s changed after Playkeeper inspected it.", quoted(name)), "name", clip(name))
}
