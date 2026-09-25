package packs

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// maxDirectoryBytes bounds a zip's central directory, its table of
	// contents, which archive/zip holds in memory.
	maxDirectoryBytes = 32 << 20
	// maxNameBytes bounds the names in a zip.
	maxNameBytes = 1024

	directoryEndLen          = 22
	directory64LocLen        = 20
	directory64EndLen        = 56
	directoryHeaderLen       = 46
	directoryHeaderSignature = 0x02014b50
	directory64LocSignature  = 0x07064b50
	directory64EndSignature  = 0x06064b50

	flagEncrypted        = 0x1
	flagStrongEncryption = 0x40
	flagMaskedHeader     = 0x2000
)

// Inspect checks a pack zip of size bytes read from r, to be used as use
// (Data or Resource), and describes it. It refuses zips that are too large,
// have too many entries or too much data once unpacked, hold unsafe names,
// links, encrypted or damaged files, or aren't packs Minecraft would load
// as use; every refusal is an *Error. It decompresses every file to check
// it, which takes a few seconds for the largest packs; ctx stops it.
func Inspect(ctx context.Context, r io.ReaderAt, size int64, use Kind, lim Limits) (Info, error) {
	if use != Data && use != Resource {
		return Info{}, fmt.Errorf("packs: can't inspect a pack for use %q", use)
	}
	lim = lim.orDefaults()
	if max := lim.maxBytes(use); size > max {
		return Info{}, tooLarge(size, max, use)
	}
	if size < directoryEndLen {
		return Info{}, notZip()
	}
	records, err := checkDirectory(r, size, lim.MaxFiles)
	if err != nil {
		return Info{}, err
	}
	zr, err := zip.NewReader(r, size)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return Info{}, corrupt("", zipDetail(err))
	}
	if len(zr.File) != records {
		return Info{}, corrupt("", "its table of contents is malformed")
	}
	files, unpacked, err := checkEntries(zr.File, lim.MaxUnpackedBytes)
	if err != nil {
		return Info{}, err
	}

	var meta *zip.File
	for _, f := range zr.File {
		if f.Name == "pack.mcmeta" {
			meta = f
			break
		}
	}
	if meta == nil {
		return Info{}, noMcmeta(zr.File)
	}
	if meta.UncompressedSize64 > maxMcmetaBytes {
		return Info{}, badMcmeta("too_large", "")
	}
	var mb bytes.Buffer
	if err := readEntry(ctx, meta, &mb); err != nil {
		return Info{}, err
	}
	m, err := parseMcmeta(mb.Bytes(), use)
	if err != nil {
		return Info{}, err
	}
	kind, ok := contentKind(zr.File, m.overlays)
	if !ok {
		return Info{}, noContent()
	}
	if !kind.holds(use) {
		return Info{}, wrongKind(kind, use)
	}

	for _, f := range zr.File {
		if err := readEntry(ctx, f, io.Discard); err != nil {
			return Info{}, err
		}
	}
	h1, h256 := sha1.New(), sha256.New()
	if _, err := io.Copy(io.MultiWriter(h1, h256), ctxReader{ctx, io.NewSectionReader(r, 0, size)}); err != nil {
		if ctx.Err() != nil {
			return Info{}, ctx.Err()
		}
		return Info{}, fileFailed("read the pack", err)
	}
	return Info{
		Kind:          kind,
		Description:   m.description,
		Formats:       m.formats,
		FormatProblem: m.formatProblem,
		Features:      m.features,
		Size:          size,
		Files:         files,
		Unpacked:      unpacked,
		SHA1:          hex.EncodeToString(h1.Sum(nil)),
		SHA256:        hex.EncodeToString(h256.Sum(nil)),
	}, nil
}

// Stage copies an upload into an unnamed temporary file in dir for Inspect
// and Install to read, refusing an upload larger than Inspect accepts for
// use. Having no name, the file leaves nothing behind if Playkeeper stops.
// The caller closes it.
func Stage(dir string, body io.Reader, use Kind, lim Limits) (*os.File, int64, error) {
	max := lim.orDefaults().maxBytes(use)
	f, err := os.CreateTemp(dir, ".playkeeper-upload-*")
	if err != nil {
		return nil, 0, fileFailed("store the upload", err)
	}
	if err := os.Remove(f.Name()); err != nil {
		f.Close()
		return nil, 0, fileFailed("store the upload", err)
	}
	n, err := io.Copy(f, io.LimitReader(body, max+1))
	if err != nil {
		f.Close()
		return nil, 0, fileFailed("store the upload", err)
	}
	if n > max {
		f.Close()
		return nil, 0, tooLarge(-1, max, use)
	}
	return f, n, nil
}

// checkDirectory finds a zip's central directory as archive/zip does and
// checks it before zip.NewReader reads it. archive/zip reads entries until
// one is malformed and compares their number with the declared one only
// modulo 65,536, so a crafted zip could make it hold millions of entries in
// memory. The directory must end where the zip's end records start and
// hold exactly the declared number of entries. It returns that number.
func checkDirectory(r io.ReaderAt, size int64, maxFiles int) (int, error) {
	tail := make([]byte, min(size, 65*1024))
	if err := readFull(r, tail, size-int64(len(tail))); err != nil {
		return 0, fileFailed("read the pack", err)
	}
	p := -1
	for i := len(tail) - directoryEndLen; i >= 0; i-- {
		if tail[i] == 'P' && tail[i+1] == 'K' && tail[i+2] == 5 && tail[i+3] == 6 {
			p = i
			break
		}
	}
	if p < 0 || p+directoryEndLen+int(le16(tail[p+20:])) > len(tail) {
		return 0, notZip()
	}
	end := size - int64(len(tail)) + int64(p)
	eocd := tail[p:]
	disk, dirDisk := uint64(le16(eocd[4:])), uint64(le16(eocd[6:]))
	recordsHere, records := uint64(le16(eocd[8:])), uint64(le16(eocd[10:]))
	dirSize, dirOffset := uint64(le32(eocd[12:])), uint64(le32(eocd[16:]))

	if records == 0xffff || dirSize == 0xffff || dirOffset == 0xffffffff {
		if loc := end - directory64LocLen; loc >= 0 {
			var lb [directory64LocLen]byte
			if err := readFull(r, lb[:], loc); err != nil {
				return 0, fileFailed("read the pack", err)
			}
			at := int64(le64(lb[8:]))
			if le32(lb[:]) == directory64LocSignature && le32(lb[4:]) == 0 && le32(lb[16:]) == 1 && at >= 0 {
				if at > size-directory64EndLen {
					return 0, corrupt("", "its zip64 end record is outside the file")
				}
				var eb [directory64EndLen]byte
				if err := readFull(r, eb[:], at); err != nil {
					return 0, fileFailed("read the pack", err)
				}
				if le32(eb[:]) != directory64EndSignature {
					return 0, corrupt("", "its zip64 end record is malformed")
				}
				end = at
				disk, dirDisk = uint64(le32(eb[16:])), uint64(le32(eb[20:]))
				recordsHere, records = le64(eb[24:]), le64(eb[32:])
				dirSize, dirOffset = le64(eb[40:]), le64(eb[48:])
			}
		}
	}

	switch {
	case disk != 0 || dirDisk != 0 || recordsHere != records:
		return 0, corrupt("", "it is one part of a zip split into several files, which Minecraft can't read")
	case records > uint64(maxFiles):
		return 0, tooManyFiles(records, maxFiles)
	case dirSize > maxDirectoryBytes:
		return 0, corrupt("", "its table of contents is larger than "+formatSize(maxDirectoryBytes))
	case dirOffset > uint64(end) || dirSize != uint64(end)-dirOffset:
		return 0, corrupt("", "its table of contents isn't where the zip says it is")
	}

	br := bufio.NewReader(io.NewSectionReader(r, int64(dirOffset), int64(dirSize)))
	var hdr [directoryHeaderLen]byte
	var n uint64
	for left := int64(dirSize); left > 0; n++ {
		if n == records {
			return 0, corrupt("", "its table of contents lists more entries than it declares")
		}
		if left < directoryHeaderLen {
			return 0, corrupt("", "its table of contents is malformed")
		}
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return 0, fileFailed("read the pack", err)
		}
		if le32(hdr[:]) != directoryHeaderSignature {
			return 0, corrupt("", "its table of contents is malformed")
		}
		rest := int64(le16(hdr[28:])) + int64(le16(hdr[30:])) + int64(le16(hdr[32:]))
		if left -= directoryHeaderLen + rest; left < 0 {
			return 0, corrupt("", "its table of contents is malformed")
		}
		if _, err := br.Discard(int(rest)); err != nil {
			return 0, fileFailed("read the pack", err)
		}
	}
	if n != records {
		return 0, corrupt("", "its table of contents lists fewer entries than it declares")
	}
	return int(records), nil
}

// checkEntries checks every entry's name, type, encryption and compression,
// and returns the number of files and their size once unpacked.
func checkEntries(entries []*zip.File, maxUnpacked int64) (int, int64, error) {
	seen := make(map[string]bool, len(entries))
	files, unpacked := 0, int64(0)
	for _, f := range entries {
		if problem := nameProblem(f.Name); problem != "" {
			return 0, 0, unsafePath(f.Name, problem)
		}
		key := strings.TrimSuffix(f.Name, "/")
		if seen[key] {
			return 0, 0, unsafePath(f.Name, "duplicate")
		}
		seen[key] = true
		isDir := strings.HasSuffix(f.Name, "/")
		mode := f.Mode()
		switch {
		case mode&fs.ModeSymlink != 0:
			return 0, 0, unsupportedEntry(f.Name, "symlink")
		case mode.Type()&^fs.ModeDir != 0:
			return 0, 0, unsupportedEntry(f.Name, "special")
		case mode.IsDir() && !isDir:
			return 0, 0, corrupt(f.Name, "it is marked as a folder, but its name doesn't end with a slash")
		case f.Flags&(flagEncrypted|flagStrongEncryption|flagMaskedHeader) != 0:
			return 0, 0, encrypted(f.Name)
		case f.Method != zip.Store && f.Method != zip.Deflate:
			return 0, 0, unsupportedCompression(f.Name, f.Method)
		case isDir && f.UncompressedSize64 != 0:
			return 0, 0, corrupt(f.Name, "it is a folder that holds data")
		case isDir:
			continue
		}
		files++
		if f.UncompressedSize64 > uint64(maxUnpacked-unpacked) {
			return 0, 0, tooMuchData(maxUnpacked)
		}
		unpacked += int64(f.UncompressedSize64)
	}
	return files, unpacked, nil
}

// nameProblem says what is unsafe about an entry name, or "" if nothing.
func nameProblem(name string) string {
	switch {
	case name == "":
		return "empty"
	case len(name) > maxNameBytes:
		return "too_long"
	case !utf8.ValidString(name):
		return "encoding"
	case strings.ContainsFunc(name, unicode.IsControl):
		return "control"
	case strings.Contains(name, `\`):
		return "backslash"
	case name[0] == '/' || len(name) >= 2 && name[1] == ':' && ('a' <= name[0]|0x20 && name[0]|0x20 <= 'z'):
		return "absolute"
	}
	for _, seg := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		switch seg {
		case "":
			return "empty_segment"
		case ".":
			return "dot_segment"
		case "..":
			return "traversal"
		}
	}
	return ""
}

// contentKind is what a pack's files hold: data in data/, assets in
// assets/, at the top of the zip or in one of its overlays. Only files in
// a namespace folder, such as data/minecraft/, count.
func contentKind(entries []*zip.File, overlays []string) (Kind, bool) {
	isOverlay := make(map[string]bool, len(overlays))
	for _, d := range overlays {
		isOverlay[d] = true
	}
	var data, assets bool
	for _, f := range entries {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		top, rest, _ := strings.Cut(f.Name, "/")
		if top != "data" && top != "assets" && isOverlay[top] {
			top, rest, _ = strings.Cut(rest, "/")
		}
		if !strings.Contains(rest, "/") {
			continue
		}
		switch top {
		case "data":
			data = true
		case "assets":
			assets = true
		}
	}
	switch {
	case data && assets:
		return Both, true
	case data:
		return Data, true
	case assets:
		return Resource, true
	}
	return "", false
}

// readEntry decompresses f into w, which checks its size and checksum.
func readEntry(ctx context.Context, f *zip.File, w io.Writer) error {
	rc, err := f.Open()
	if err != nil {
		return corrupt(f.Name, zipDetail(err))
	}
	defer rc.Close()
	if _, err := io.Copy(w, ctxReader{ctx, rc}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return corrupt(f.Name, zipDetail(err))
	}
	return nil
}

// ctxReader stops reading once ctx is done.
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

// readFull reads len(b) bytes at off: io.ReaderAt lets a complete read end
// with io.EOF.
func readFull(r io.ReaderAt, b []byte, off int64) error {
	n, err := r.ReadAt(b, off)
	if n == len(b) {
		return nil
	}
	if err == nil || errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return err
}

func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
func le64(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }

// zipDetail explains an archive/zip error.
func zipDetail(err error) string {
	var bad flate.CorruptInputError
	switch {
	case errors.Is(err, zip.ErrChecksum):
		return "its checksum doesn't match its contents"
	case errors.As(err, &bad):
		return "its compressed data is damaged"
	case errors.Is(err, zip.ErrFormat), errors.Is(err, io.ErrUnexpectedEOF):
		return "it is cut short or malformed"
	}
	return err.Error()
}

func notZip() *Error {
	return &Error{
		Code: CodeNotZip,
		Msg:  "The file isn't a zip file.",
		Hint: "Packs are .zip files. If you have a folder, zip what's inside it; if you have a .rar or .7z file, extract it and zip the pack instead.",
	}
}

// corrupt is the error for a damaged zip; entry is the damaged entry, if
// known, and detail explains the damage.
func corrupt(entry, detail string) *Error {
	e := &Error{
		Code:   CodeCorrupt,
		Params: map[string]any{"detail": detail},
		Msg:    sentence("The zip is damaged: " + detail),
		Hint:   "Download the pack again; the copy you have may be incomplete or damaged.",
	}
	if entry != "" {
		e.Params["entry"] = shortName(entry)
		e.Msg = sentence(fmt.Sprintf("The entry %s in the zip is damaged: %s", shortQuote(entry), detail))
	}
	return e
}

func tooManyFiles(n uint64, max int) *Error {
	return &Error{
		Code:   CodeTooManyFiles,
		Params: map[string]any{"files": n, "max": max},
		Msg:    fmt.Sprintf("The zip holds %s files and folders, more than the %s Playkeeper accepts.", thousands(int64(min(n, 1<<62))), thousands(int64(max))),
		Hint:   "Check that you picked the right file; packs rarely hold this many files.",
	}
}

func tooMuchData(max int64) *Error {
	return &Error{
		Code:   CodeTooMuchData,
		Params: map[string]any{"max": max},
		Msg:    fmt.Sprintf("The zip's files add up to more than %s once unpacked, the most Playkeeper accepts.", formatSize(max)),
		Hint:   "Check that you picked the right file; a pack this large is unusual.",
	}
}

// unsafePath is the error for an entry name that could harm a program that
// unpacks the zip, or that Minecraft can't read. problem is what
// nameProblem found, or "duplicate".
func unsafePath(entry, problem string) *Error {
	q := shortQuote(entry)
	hint := hintRedownload
	var msg string
	switch problem {
	case "empty":
		msg = "The zip holds an entry with no name."
	case "too_long":
		msg = fmt.Sprintf("The zip holds an entry whose name is longer than %s bytes: %s.", thousands(maxNameBytes), q)
	case "encoding":
		msg = fmt.Sprintf("The zip holds an entry whose name isn't valid UTF-8 text, which Minecraft can't read: %s.", q)
	case "control":
		msg = fmt.Sprintf("The zip holds an entry whose name contains control characters: %s.", q)
	case "backslash":
		msg = fmt.Sprintf(`The zip holds an entry whose name separates folders with backslashes (\), which Minecraft doesn't understand: %s.`, q)
		hint = "Zip the pack again with a tool that uses forward slashes, such as 7-Zip or the one built into your system."
	case "absolute":
		msg = fmt.Sprintf("The zip holds an entry with an absolute path, which could be written outside the folder the zip is unpacked into: %s.", q)
	case "empty_segment":
		msg = fmt.Sprintf("The zip holds an entry whose path has an empty folder name (two slashes in a row): %s.", q)
	case "dot_segment":
		msg = fmt.Sprintf(`The zip holds an entry whose path has a folder named ".": %s.`, q)
	case "traversal":
		msg = fmt.Sprintf(`The zip holds an entry whose path climbs out of the pack with "..", which could be written outside the folder the zip is unpacked into: %s.`, q)
	case "duplicate":
		msg = fmt.Sprintf("The zip holds two entries named %s, so different programs could read different files.", q)
	}
	return &Error{
		Code:   CodeUnsafePath,
		Params: map[string]any{"entry": shortName(entry), "problem": problem},
		Msg:    msg,
		Hint:   hint,
	}
}

// unsupportedEntry is the error for an entry that is neither a file nor a
// folder. typ is "symlink" or "special".
func unsupportedEntry(entry, typ string) *Error {
	msg := fmt.Sprintf("The zip holds a symbolic link, %s. Packs may only hold files and folders.", shortQuote(entry))
	if typ == "special" {
		msg = fmt.Sprintf("The zip holds %s, which is a device, pipe or other special file. Packs may only hold files and folders.", shortQuote(entry))
	}
	return &Error{
		Code:   CodeUnsupportedEntry,
		Params: map[string]any{"entry": shortName(entry), "type": typ},
		Msg:    msg,
		Hint:   hintRedownload,
	}
}

func encrypted(entry string) *Error {
	return &Error{
		Code:   CodeEncrypted,
		Params: map[string]any{"entry": shortName(entry)},
		Msg:    fmt.Sprintf("The zip is protected with a password (%s is encrypted), and Minecraft can't read encrypted packs.", shortQuote(entry)),
		Hint:   "Zip the pack again without a password.",
	}
}

func unsupportedCompression(entry string, method uint16) *Error {
	return &Error{
		Code:   CodeUnsupportedCompression,
		Params: map[string]any{"entry": shortName(entry), "method": method},
		Msg:    fmt.Sprintf("The entry %s in the zip is compressed with a method Minecraft can't read (method %d). Minecraft reads only zips compressed with Deflate, the standard method, or not compressed at all.", shortQuote(entry), method),
		Hint:   "Zip the pack again with standard settings: in 7-Zip, pick the zip format and the Deflate method.",
	}
}

// noMcmeta is the error for a zip without a pack.mcmeta at its top. It
// recognizes the usual mistakes: a pack zipped with its folder, a
// misnamed pack.mcmeta, a mod or plugin, and a zip holding the pack's zip.
func noMcmeta(entries []*zip.File) *Error {
	var folder, found, nested, modID, modNoun string
	for _, f := range entries {
		dir, rest, inFolder := strings.Cut(f.Name, "/")
		lower := strings.ToLower(f.Name)
		switch {
		case inFolder && rest == "pack.mcmeta":
			if folder == "" {
				folder = dir
			}
		case !inFolder && (lower == "pack.mcmeta" || strings.HasPrefix(lower, "pack.mcmeta.")):
			if found == "" {
				found = f.Name
			}
		case !inFolder && strings.HasSuffix(lower, ".zip"):
			if nested == "" {
				nested = f.Name
			}
		}
		if id, noun := modOf(f.Name); id != "" && modID == "" {
			modID, modNoun = id, noun
		}
	}
	e := &Error{Code: CodeNoMcmeta, Params: map[string]any{}}
	switch {
	case folder != "":
		e.Params["folder"] = shortName(folder)
		e.Msg = fmt.Sprintf("The zip has no pack.mcmeta file at its top: the pack is inside the folder %s.", shortQuote(folder))
		e.Hint = "Open that folder, select everything inside it and zip those files, so pack.mcmeta is at the top of the zip."
	case found != "":
		e.Params["found"] = shortName(found)
		e.Msg = fmt.Sprintf("The zip has no pack.mcmeta file at its top, only %s.", shortQuote(found))
		e.Hint = "Rename that file to pack.mcmeta, in lowercase and without another extension, and zip the pack again."
	case modID != "":
		e.Params["mod"] = modID
		e.Msg = fmt.Sprintf("This zip looks like %s, not a pack.", modNoun)
		e.Hint = "Add it as a mod or plugin instead."
	case nested != "":
		e.Params["nested"] = shortName(nested)
		e.Msg = fmt.Sprintf("The zip has no pack.mcmeta file at its top, but it holds another zip, %s, which may be the pack.", shortQuote(nested))
		e.Hint = "Extract the zip and add the pack inside it instead."
	default:
		e.Msg = "The zip has no pack.mcmeta file at its top, so it isn't a Minecraft pack."
		e.Hint = "Check that you picked the pack itself, not a mod, a world or a bundle of several files."
	}
	return e
}

// modOf names the kind of mod or plugin whose zip holds name, if any.
func modOf(name string) (id, noun string) {
	switch name {
	case "fabric.mod.json":
		return "fabric", "a Fabric mod"
	case "quilt.mod.json":
		return "quilt", "a Quilt mod"
	case "META-INF/mods.toml":
		return "forge", "a Forge mod"
	case "META-INF/neoforge.mods.toml":
		return "neoforge", "a NeoForge mod"
	case "paper-plugin.yml":
		return "paper", "a Paper plugin"
	case "plugin.yml":
		return "bukkit", "a Paper or Spigot plugin"
	}
	return "", ""
}

func noContent() *Error {
	return &Error{
		Code: CodeNoContent,
		Msg:  "The pack has no files in a data or assets folder, so it would change nothing in the game.",
		Hint: "Check that you zipped the whole pack, with its data or assets folder next to pack.mcmeta.",
	}
}

func wrongKind(kind, use Kind) *Error {
	hint := "Add it under Data packs instead."
	if use == Data {
		hint = "Add it under Resource packs instead."
	}
	return &Error{
		Code:   CodeWrongKind,
		Params: map[string]any{"kind": kind, "expected": use},
		Msg:    fmt.Sprintf("This zip is %s, not %s.", kindName(kind), kindName(use)),
		Hint:   hint,
	}
}
