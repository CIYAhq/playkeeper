// Package backup writes, verifies and safely extracts Playkeeper world
// archives, and backs up a running server without stopping it (Take). It
// works on files, streams and a narrow console interface, so it can be tested
// without Docker or a Minecraft server.
//
// Archive layout (tar.gz):
//
//	playkeeper-backup/data/<allowlisted server files>
//	playkeeper-backup/manifest.json   (last entry; per-file SHA-256)
package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	FormatVersion = 1
	rootDir       = "playkeeper-backup"
	dataPrefix    = rootDir + "/data/"
	manifestName  = rootDir + "/manifest.json"
)

type FileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Format            int       `json:"format"`
	CreatedAt         time.Time `json:"createdAt"`
	PlaykeeperVersion string    `json:"playkeeperVersion"`
	SourceInstall     string    `json:"sourceInstall"`
	// Type is the server software (empty means paper, for archives made
	// before 0.3.0).
	Type             string            `json:"type,omitempty"`
	VersionID        string            `json:"versionId"`
	MinecraftVersion string            `json:"minecraftVersion"`
	PaperBuild       int               `json:"paperBuild"`
	Image            string            `json:"image"`
	LevelName        string            `json:"levelName"`
	Settings         map[string]string `json:"settings"`
	Consistency      string            `json:"consistency"`
	Files            []FileEntry       `json:"files"`
	TotalBytes       int64             `json:"totalBytes"`
}

// Limits bound what an (untrusted) archive may make the agent write. Create
// enforces the same limits, so it never writes an archive a restore refuses.
type Limits struct {
	MaxFiles         int
	MaxTotalBytes    int64
	MaxFileBytes     int64
	MaxPathLen       int
	MaxManifestBytes int
}

func DefaultLimits() Limits {
	return Limits{MaxFiles: 200_000, MaxTotalBytes: 64 << 30, MaxFileBytes: 16 << 30, MaxPathLen: 1024, MaxManifestBytes: 64 << 20}
}

// RefusedError is Create's error for a world a restore would refuse. File is
// the file to rename or remove, or empty when the world as a whole is over a
// limit.
type RefusedError struct {
	File   string
	Reason error
}

func (e *RefusedError) Error() string {
	what := "this world"
	if e.File != "" {
		what = shortQuote(e.File)
	}
	return fmt.Sprintf("cannot back up %s: a restore would refuse it: %v", what, e.Reason)
}

func (e *RefusedError) Unwrap() error { return e.Reason }

// archiveLimitError is a limit on the archive as a whole, not on one file.
type archiveLimitError string

func (e archiveLimitError) Error() string { return string(e) }

// fileTally applies the rules a restore enforces on each data file and on
// their running count and size. walk and Create share it so they agree.
type fileTally struct {
	lim   Limits
	files int
	total int64
}

func (t *fileTally) add(rel string, size int64) error {
	if n := len(dataPrefix) + len(rel); n > t.lim.MaxPathLen {
		return fmt.Errorf("entry name too long (%d bytes, limit %d)", n, t.lim.MaxPathLen)
	}
	if !validRel(rel) {
		return fmt.Errorf("entry %q has an unsafe path", rel)
	}
	if size < 0 || size > t.lim.MaxFileBytes {
		return fmt.Errorf("entry %q is too large (%d bytes, limit %d)", rel, size, t.lim.MaxFileBytes)
	}
	t.files++
	if t.files > t.lim.MaxFiles {
		return archiveLimitError(fmt.Sprintf("archive has more than %d files", t.lim.MaxFiles))
	}
	t.total += size
	if t.total > t.lim.MaxTotalBytes {
		return archiveLimitError(fmt.Sprintf("archive expands beyond the %d byte limit", t.lim.MaxTotalBytes))
	}
	return nil
}

// topFiles and topDirs are the only server files Playkeeper archives. Server
// jars, libraries and caches are re-downloaded from upstream; image-internal
// files (which contain the RCON password) and eula.txt are never archived.
var topFiles = []string{
	"server.properties", "whitelist.json", "ops.json", "banned-players.json", "banned-ips.json",
	"usercache.json", "bukkit.yml", "spigot.yml", "commands.yml", "help.yml", "permissions.yml",
	"version_history.json", "server-icon.png",
}

var topDirs = []string{"config", "plugins"}

var skipDirNames = map[string]bool{".paper-remapped": true}

// secretProperties are removed from the archived server.properties; the
// restoring host generates its own.
var secretProperties = []string{"rcon.password=", "management-server-secret="}

// LevelName reads level-name from server.properties (default "world").
func LevelName(dataDir string) string {
	level, err := levelName(dataDir)
	if err != nil {
		return "world"
	}
	return level
}

// levelName is LevelName for an archive. A server.properties Playkeeper
// won't read, such as a link or a named pipe a plugin put there, is an error
// that names it: which world it names can't be known, and "world" could be
// another world, backed up without its settings.
func levelName(dataDir string) (string, error) {
	b, err := readProperties(dataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return "world", nil
	}
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "level-name="); ok && v != "" && validRel(v) && !strings.Contains(v, "/") {
			return v, nil
		}
	}
	return "world", nil
}

// Create archives the allowlisted contents of dataDir to w and returns the
// manifest written as the final entry. meta supplies descriptive fields. A
// file or world that lim would make a restore refuse is an error, and so is
// a server.properties Playkeeper won't read (see levelName).
func Create(w io.Writer, dataDir string, meta Manifest, lim Limits) (Manifest, error) {
	level, err := levelName(dataDir)
	if err != nil {
		return meta, err
	}
	meta.Format = FormatVersion
	meta.LevelName = level
	meta.Files = nil
	meta.TotalBytes = 0

	rels, err := archiveFiles(dataDir, level)
	if err != nil {
		return meta, err
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return meta, err
	}
	defer root.Close()

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	tally := fileTally{lim: lim}
	for _, rel := range rels {
		entry, err := writeFile(tw, root, rel, &tally)
		var refused *RefusedError
		if errors.As(err, &refused) {
			return meta, err
		}
		if err != nil {
			return meta, fmt.Errorf("cannot back up %s: %w", shortQuote(rel), err)
		}
		meta.Files = append(meta.Files, entry)
		meta.TotalBytes += entry.Size
	}
	mb, err := marshalManifest(meta, lim)
	if err != nil {
		return meta, err
	}
	hdr := &tar.Header{Name: manifestName, Mode: 0o644, Size: int64(len(mb)), ModTime: meta.CreatedAt, Typeflag: tar.TypeReg, Format: tar.FormatPAX}
	if err := tw.WriteHeader(hdr); err != nil {
		return meta, err
	}
	if _, err := tw.Write(mb); err != nil {
		return meta, err
	}
	if err := tw.Close(); err != nil {
		return meta, err
	}
	return meta, gz.Close()
}

// Check applies Create's refusals to the files in dataDir without reading
// them, so a world a restore would refuse is refused before the server stops
// for a backup. Files can change before Create runs, so Create checks again.
// The manifest is estimated without Create's descriptive fields, so Check
// never refuses a world Create accepts.
func Check(dataDir string, lim Limits) error {
	level, err := levelName(dataDir)
	if err != nil {
		return err
	}
	rels, err := archiveFiles(dataDir, level)
	if err != nil {
		return err
	}
	m := Manifest{Format: FormatVersion, LevelName: level}
	tally := fileTally{lim: lim}
	for _, rel := range rels {
		size, err := archivedSize(dataDir, rel)
		if err != nil {
			return fmt.Errorf("cannot back up %s: %w", shortQuote(rel), err)
		}
		if err := tally.add(rel, size); err != nil {
			return refusal(rel, err)
		}
		m.Files = append(m.Files, FileEntry{Path: rel, Size: size, SHA256: strings.Repeat("0", 2*sha256.Size)})
		m.TotalBytes += size
	}
	_, err = marshalManifest(m, lim)
	return err
}

// archiveFiles lists the allowlisted files in dataDir, sorted, and requires
// the level's world among them.
func archiveFiles(dataDir, level string) ([]string, error) {
	var rels []string
	for _, f := range topFiles {
		if st, err := os.Lstat(filepath.Join(dataDir, f)); err == nil && st.Mode().IsRegular() {
			rels = append(rels, f)
		}
	}
	dirs := append([]string{level, level + "_nether", level + "_the_end"}, topDirs...)
	for _, d := range dirs {
		root := filepath.Join(dataDir, d)
		st, err := os.Lstat(root)
		if err != nil || !st.IsDir() {
			continue
		}
		err = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() && skipDirNames[e.Name()] {
				return filepath.SkipDir
			}
			if e.Type().IsRegular() {
				rel, err := filepath.Rel(dataDir, p)
				if err != nil {
					return err
				}
				rels = append(rels, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(rels) == 0 || !containsPrefix(rels, level+"/") {
		return nil, fmt.Errorf("no world named %q found in %s", level, dataDir)
	}
	sort.Strings(rels)
	return rels, nil
}

// archivedSize is rel's size in an archive, where server.properties loses its
// secret lines. Check runs while the server does, so server.properties is
// read through gamefiles: the game can swap in a link or a named pipe.
func archivedSize(dataDir, rel string) (int64, error) {
	if rel == "server.properties" {
		b, err := readProperties(dataDir)
		if err != nil {
			return 0, err
		}
		return int64(len(SanitizeProperties(b))), nil
	}
	st, err := os.Lstat(filepath.Join(dataDir, filepath.FromSlash(rel)))
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// refusal is the RefusedError for a file the tally refused, naming the file
// unless the world as a whole is over a limit.
func refusal(rel string, err error) *RefusedError {
	var whole archiveLimitError
	if errors.As(err, &whole) {
		return &RefusedError{Reason: err}
	}
	return &RefusedError{File: rel, Reason: err}
}

func marshalManifest(m Manifest, lim Limits) ([]byte, error) {
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(mb) > lim.MaxManifestBytes {
		return nil, &RefusedError{Reason: fmt.Errorf("its manifest would be %d bytes (limit %d)", len(mb), lim.MaxManifestBytes)}
	}
	return mb, nil
}

func containsPrefix(list []string, p string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// shortQuote quotes a path for an error message, eliding the middle of a very
// long one.
func shortQuote(p string) string {
	if len(p) > 120 {
		p = p[:60] + "…" + p[len(p)-50:]
	}
	return strconv.Quote(p)
}

// openRegular opens rel below root for reading, refusing anything but a
// regular file. The server keeps running during an online backup, so a file
// may have been swapped for a symlink out of root (refused by os.Root) or a
// FIFO (opened non-blocking, so it cannot hang the backup) since listing.
func openRegular(root *os.Root, rel string) (*os.File, fs.FileInfo, error) {
	f, err := root.OpenFile(filepath.FromSlash(rel), readFlags, 0)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, nil, fmt.Errorf("%s is not a regular file", shortQuote(rel))
	}
	return f, st, nil
}

func writeFile(tw *tar.Writer, root *os.Root, rel string, tally *fileTally) (FileEntry, error) {
	f, st, err := openRegular(root, rel)
	if err != nil {
		return FileEntry{}, err
	}
	defer f.Close()
	var content io.Reader = f
	size, modTime := st.Size(), st.ModTime()
	if rel == "server.properties" {
		b, err := io.ReadAll(f)
		if err != nil {
			return FileEntry{}, err
		}
		b = SanitizeProperties(b)
		content, size = bytes.NewReader(b), int64(len(b))
	}
	if err := tally.add(rel, size); err != nil {
		return FileEntry{}, refusal(rel, err)
	}
	hdr := &tar.Header{Name: dataPrefix + rel, Mode: 0o644, Size: size, ModTime: modTime, Typeflag: tar.TypeReg, Format: tar.FormatPAX}
	if err := tw.WriteHeader(hdr); err != nil {
		return FileEntry{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tw, h), io.LimitReader(content, size))
	if err != nil {
		return FileEntry{}, err
	}
	if n != size {
		return FileEntry{}, fmt.Errorf("file changed size while archiving (%d != %d)", n, size)
	}
	return FileEntry{Path: rel, Size: size, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// SanitizeProperties drops secret-bearing server.properties lines.
func SanitizeProperties(b []byte) []byte {
	var out bytes.Buffer
	for _, line := range strings.SplitAfter(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		secret := false
		for _, p := range secretProperties {
			if strings.HasPrefix(trimmed, p) {
				secret = true
			}
		}
		if !secret {
			out.WriteString(line)
		}
	}
	return out.Bytes()
}

// Verify streams an archive and checks gzip integrity, entry safety and every
// file hash against the manifest. It writes nothing to disk.
func Verify(r io.Reader, lim Limits) (Manifest, error) {
	return walk(r, lim, nil)
}

// Extract verifies and extracts the archive into destDir (which must not
// exist). On any error the caller should delete destDir.
func Extract(r io.Reader, destDir string, lim Limits) (Manifest, error) {
	if _, err := os.Lstat(destDir); err == nil {
		return Manifest{}, fmt.Errorf("extract destination %s already exists", destDir)
	}
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return Manifest{}, err
	}
	return walk(r, lim, func(rel string, size int64, src io.Reader) (string, error) {
		return extractFile(destDir, rel, size, src)
	})
}

// extractFile writes one entry below destDir. walk refuses unsafe names
// first; this is the second, independent check that nothing lands outside.
func extractFile(destDir, rel string, size int64, src io.Reader) (string, error) {
	target := filepath.Join(destDir, filepath.FromSlash(rel))
	if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
		return "", fmt.Errorf("entry %q escapes the destination", rel)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return "", err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(src, size))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n != size {
		return "", fmt.Errorf("entry %q is truncated", rel)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type sink func(rel string, size int64, r io.Reader) (sha string, err error)

var ErrNoManifest = errors.New("not a Playkeeper backup: manifest.json missing")

func walk(r io.Reader, lim Limits, write sink) (Manifest, error) {
	var m Manifest
	gz, err := gzip.NewReader(bufio.NewReaderSize(r, 256<<10))
	if err != nil {
		return m, fmt.Errorf("not a gzip archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]FileEntry{}
	tally := fileTally{lim: lim}
	var manifestRaw []byte
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return m, fmt.Errorf("archive is corrupt: %w", err)
		}
		if manifestRaw != nil {
			return m, errors.New("archive has entries after the manifest")
		}
		name := hdr.Name
		if len(name) > lim.MaxPathLen {
			return m, fmt.Errorf("entry name too long (%d bytes)", len(name))
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if name != rootDir+"/" && name != dataPrefix && !(strings.HasPrefix(name, dataPrefix) && validRel(strings.TrimSuffix(strings.TrimPrefix(name, dataPrefix), "/"))) {
				return m, fmt.Errorf("unexpected directory entry %q", name)
			}
			continue
		case tar.TypeReg:
		default:
			return m, fmt.Errorf("entry %q has unsupported type %q (links and devices are refused)", name, string(hdr.Typeflag))
		}
		if name == manifestName {
			if hdr.Size > int64(lim.MaxManifestBytes) {
				return m, errors.New("manifest too large")
			}
			manifestRaw, err = io.ReadAll(io.LimitReader(tr, hdr.Size))
			if err != nil {
				return m, fmt.Errorf("archive is corrupt: %w", err)
			}
			continue
		}
		rel, ok := strings.CutPrefix(name, dataPrefix)
		if !ok {
			return m, fmt.Errorf("entry %q is outside the backup data directory", name)
		}
		if _, dup := seen[rel]; dup {
			return m, fmt.Errorf("duplicate entry %q", rel)
		}
		if err := tally.add(rel, hdr.Size); err != nil {
			return m, err
		}
		var sum string
		if write != nil {
			sum, err = write(rel, hdr.Size, tr)
		} else {
			h := sha256.New()
			var n int64
			n, err = io.Copy(h, io.LimitReader(tr, hdr.Size))
			if err == nil && n != hdr.Size {
				err = fmt.Errorf("entry %q is truncated", rel)
			}
			sum = hex.EncodeToString(h.Sum(nil))
		}
		if err != nil {
			return m, fmt.Errorf("archive is corrupt: %w", err)
		}
		seen[rel] = FileEntry{Path: rel, Size: hdr.Size, SHA256: sum}
	}
	// Read to the end of the gzip stream so its CRC-32 and length trailer are
	// checked; tar stops at its end marker before the trailer.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return m, fmt.Errorf("archive is corrupt: %w", err)
	}
	if manifestRaw == nil {
		return m, ErrNoManifest
	}
	dec := json.NewDecoder(bytes.NewReader(manifestRaw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("manifest is invalid: %w", err)
	}
	if m.Format != FormatVersion {
		return m, fmt.Errorf("backup format %d is not supported by this Playkeeper (expects %d)", m.Format, FormatVersion)
	}
	if len(m.Files) != len(seen) {
		return m, fmt.Errorf("manifest lists %d files but archive contains %d", len(m.Files), len(seen))
	}
	for _, f := range m.Files {
		got, ok := seen[f.Path]
		if !ok {
			return m, fmt.Errorf("file %q listed in manifest is missing", f.Path)
		}
		if got.Size != f.Size || got.SHA256 != f.SHA256 {
			return m, fmt.Errorf("file %q does not match its recorded checksum", f.Path)
		}
	}
	if m.LevelName == "" || !validRel(m.LevelName) || strings.Contains(m.LevelName, "/") {
		return m, errors.New("manifest has an invalid level name")
	}
	if !containsPrefix(sortedKeys(seen), m.LevelName+"/") {
		return m, fmt.Errorf("archive does not contain the world %q", m.LevelName)
	}
	return m, nil
}

func sortedKeys(m map[string]FileEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// validRel accepts clean, relative, slash-separated paths without traversal,
// empty or hidden-dot components, or control characters.
func validRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || path.Clean(p) != p {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
