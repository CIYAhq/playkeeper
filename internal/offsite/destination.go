package offsite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
)

// backend stores encrypted files: S3-compatible storage or SFTP. It knows
// nothing of keys.
type backend interface {
	put(ctx context.Context, o object) (Copy, error)
	abort(ctx context.Context, st *UploadState) error
	abortStale(ctx context.Context, olderThan time.Time, keep []*UploadState) (int, error)
	verify(ctx context.Context, cp Copy) (Copy, error)
	list(ctx context.Context) ([]Object, error)
	remove(ctx context.Context, name string) error
	get(ctx context.Context, op, name string, fn func(r io.Reader, size int64) error) error
	test(ctx context.Context, name string, body []byte) TestResult
	keyOf(name string) string
}

// spoolReserve is what encrypting or restoring a copy leaves free on this
// machine's disk, as the agent does for backups.
const spoolReserve = 512 << 20

// Destination is where one server's encrypted copies go, with that
// server's keys. It is safe for concurrent use, but two uploads of the
// same backup must not run at once.
type Destination struct {
	b        backend
	kind     string
	pinned   bool // over SFTP: the user has confirmed a host key
	keys     Keys
	ids      []keyed // the current key first
	spool    string
	now      func() time.Time
	diskFree func(dir string) (int64, error)
	reserve  int64
}

// Options are what Open needs besides the settings and the keys.
type Options struct {
	// SpoolDir is the folder where copies are encrypted before they are
	// sent: an existing absolute path that belongs to this server alone,
	// since AbortStale deletes old files there. It needs room for the
	// largest backup plus 512 MiB.
	SpoolDir string
	// HTTPClient sends S3 requests; NewHTTPClient() if nil.
	HTTPClient *http.Client
	// Dial connects to the SFTP server; if nil, a dialer that never
	// connects to link-local, multicast or cloud metadata addresses.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	Now  func() time.Time // time.Now if nil
	// DiskFree reports the bytes free in the file system holding a
	// folder; the real one's if nil.
	DiskFree func(dir string) (int64, error)
}

// Open checks the settings and the keys and returns the destination.
// Nothing is sent until an operation is called.
func Open(cfg Config, keys Keys, opts Options) (*Destination, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	ids, err := keys.identities()
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(opts.SpoolDir) {
		return nil, unexpected(opSetup, "", "Playkeeper needs an absolute folder to encrypt copies in.")
	}
	d := &Destination{kind: cfg.Type, keys: keys, ids: ids, spool: filepath.Clean(opts.SpoolDir),
		now: opts.Now, diskFree: opts.DiskFree, reserve: spoolReserve}
	if d.now == nil {
		d.now = time.Now
	}
	if d.diskFree == nil {
		d.diskFree = freeSpace
	}
	switch cfg.Type {
	case TypeS3:
		c, err := newS3(cfg.S3, opts.HTTPClient, d.now)
		if err != nil {
			return nil, err
		}
		d.b = c
	case TypeSFTP:
		c, err := newSFTP(cfg.SFTP, opts.Dial, d.now)
		if err != nil {
			return nil, err
		}
		d.b, d.pinned = c, c.pinned != nil
	}
	return d, nil
}

// Upload is a backup to copy.
type Upload struct {
	Name   string      // the backup's file name, such as playkeeper-world-20260925-120000-a1b2c3.tar.gz
	File   io.ReaderAt // the backup archive, read once to encrypt it
	Size   int64
	SHA256 string // hex SHA-256 from the backup's record
	// Resume is the state a failed upload's Error or the last Progress
	// carried, to continue that upload.
	Resume *UploadState
	// Progress, if set, is called after each part or segment is stored.
	// Save its State to resume if Playkeeper stops.
	Progress func(Progress)
}

func (d *Destination) checkUpload(up Upload) error {
	switch {
	case !ValidName(up.Name):
		return &Error{Kind: KindInvalidConfig, Op: opUpload, Field: "name",
			Msg: "A backup's file name may only contain letters, digits, hyphens, underscores and dots, and ends in .tar.gz."}
	case up.File == nil || up.Size <= 0:
		return unexpected(opUpload, up.Name, fmt.Sprintf("The backup file %s is empty.", up.Name))
	case !validSHA256(up.SHA256):
		return unexpected(opUpload, up.Name, fmt.Sprintf("The backup %s has no SHA-256 in its record, so Playkeeper can't check it before copying.", up.Name))
	case d.kind == TypeS3 && sealedSize(up.Size) > maxObjectSize:
		return &Error{Kind: KindTooLarge, Op: opUpload, Name: up.Name,
			Msg: fmt.Sprintf("The backup %s is larger than 5 TiB once encrypted, the most S3 storage accepts in one file.", up.Name)}
	case d.kind == TypeSFTP && !d.pinned:
		return &Error{Kind: KindHostKeyUnknown, Op: opUpload, Name: up.Name, Field: "hostKey",
			Msg:  "The other machine's host key isn't confirmed yet, so nothing was copied.",
			Hint: "Run the connection test in the SFTP settings and confirm the fingerprint it shows."}
	}
	return nil
}

// Upload encrypts the backup to the current key and copies it to the
// destination, or continues the upload up.Resume describes.
//
// The backup is read once, checked against its record's SHA-256 and
// encrypted into the spool folder; only that encrypted file is sent. If
// the upload stops for a reason that may pass, the Error carries Resume
// and the encrypted file stays for the next try; otherwise it is deleted,
// with anything already stored. A copy that is already there is checked
// by opening it: if it holds this backup it is returned with Uploaded
// false, and it is never overwritten.
func (d *Destination) Upload(ctx context.Context, up Upload) (Copy, error) {
	up.SHA256 = strings.ToLower(up.SHA256)
	if err := d.checkUpload(up); err != nil {
		return Copy{}, err
	}
	name := CopyName(up.Name)
	sp, st, err := d.prepare(ctx, up, name)
	if err != nil {
		return Copy{}, err
	}
	cp, err := d.b.put(ctx, object{Name: name, File: sp.f, Size: sp.size, SHA256: sp.sha, Resume: st, Progress: up.Progress})
	if err == nil {
		d.discard(sp)
		cp.Archive, cp.ArchiveSHA256, cp.Recipient = up.Name, up.SHA256, d.ids[0].recipient
		return cp, nil
	}
	e := asError(err)
	if e.Kind == KindConflict {
		d.discard(sp)
		return d.adopt(ctx, up, name)
	}
	if e.Resume != nil {
		sp.f.Close()
		return Copy{}, e
	}
	d.discard(sp)
	return Copy{}, e
}

// prepare reopens the encrypted copy a saved state names, or encrypts the
// backup again, discarding an upload that can't continue.
func (d *Destination) prepare(ctx context.Context, up Upload, name string) (*spooled, *UploadState, error) {
	if r := up.Resume; r != nil && r.Archive == up.Name {
		if sp := d.reopen(r, up, name); sp != nil {
			return sp, r.clone(), nil
		}
		d.abortQuietly(ctx, r)
	}
	sp, err := d.seal(ctx, up)
	if err != nil {
		return nil, nil, err
	}
	return sp, &UploadState{Archive: up.Name, ArchiveSHA256: up.SHA256, Name: name, Spool: sp.name, Size: sp.size,
		SHA256: sp.sha, Recipient: d.ids[0].recipient, StartedAt: d.now().UTC()}, nil
}

func (d *Destination) reopen(r *UploadState, up Upload, name string) *spooled {
	if r.Name != name || !strings.EqualFold(r.ArchiveSHA256, up.SHA256) || r.Recipient != d.ids[0].recipient ||
		!validSpoolName(r.Spool) || r.Size <= 0 || !validSHA256(r.SHA256) {
		return nil
	}
	path := filepath.Join(d.spool, r.Spool)
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() || fi.Size() != r.Size {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	return &spooled{name: r.Spool, f: f, size: r.Size, sha: strings.ToLower(r.SHA256)}
}

func (d *Destination) discard(sp *spooled) {
	sp.f.Close()
	os.Remove(filepath.Join(d.spool, sp.name))
}

func (d *Destination) abortQuietly(ctx context.Context, st *UploadState) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_ = d.Abort(ctx, st)
}

// adopt checks a file already at the destination under the backup's copy
// name. If it opens with the server's keys and holds this backup, an
// earlier upload finished without being recorded, and that copy is the
// backup's.
func (d *Destination) adopt(ctx context.Context, up Upload, name string) (Copy, error) {
	conflict := func(msg string, err error) (Copy, error) {
		return Copy{}, &Error{Kind: KindConflict, Op: opUpload, Name: name, Err: err, Msg: msg,
			Hint: "Playkeeper never overwrites it. Give each server its own prefix or folder, or delete the file there."}
	}
	got, err := d.open(ctx, opUpload, name, want{}, func() (io.Writer, error) { return io.Discard, nil })
	switch e := asError(err); {
	case err == nil:
	case e.Kind == KindKeyMismatch:
		return conflict(fmt.Sprintf("A copy named %s is already %s, made with an encryption key this server doesn't have.", name, placeOf(d.kind)), e)
	case e.Kind == KindVerifyFailed:
		return conflict(fmt.Sprintf("A file named %s is already %s, and it is damaged or isn't a Playkeeper copy.", name, placeOf(d.kind)), e)
	default:
		return Copy{}, e
	}
	if got.size != up.Size || got.sha != up.SHA256 {
		return conflict(fmt.Sprintf("A copy of a different backup named %s is already %s.", name, placeOf(d.kind)), nil)
	}
	return Copy{Name: name, Archive: up.Name, ArchiveSHA256: up.SHA256, Recipient: got.recipient, Key: d.b.keyOf(name),
		Size: got.cipherSize, SHA256: got.cipherSHA, Checked: CheckedDecrypted, VerifiedAt: d.now().UTC()}, nil
}

// Verify checks, without downloading it, that a copy is still at the
// destination as its record says: its size, and on S3 its checksums.
func (d *Destination) Verify(ctx context.Context, cp Copy) (Copy, error) { return d.b.verify(ctx, cp) }

// List returns the copies at the destination, sorted by name. Other
// files are left out.
func (d *Destination) List(ctx context.Context) ([]Object, error) { return d.b.list(ctx) }

// Delete deletes the copy name. A copy that isn't there is not an error.
func (d *Destination) Delete(ctx context.Context, name string) error {
	if !validCopyName(name) {
		return unexpected(opDelete, name, "That is not the name of a backup's copy.")
	}
	return d.b.remove(ctx, name)
}

// Abort discards an unfinished upload: what the destination holds of it,
// and its encrypted copy in the spool folder.
func (d *Destination) Abort(ctx context.Context, st *UploadState) error {
	if st == nil {
		return nil
	}
	err := d.b.abort(ctx, st)
	if validSpoolName(st.Spool) {
		if rerr := os.Remove(filepath.Join(d.spool, st.Spool)); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) && err == nil {
			err = &Error{Kind: KindUnexpected, Op: opAbort, Name: st.Name, Err: rerr,
				Msg:  fmt.Sprintf("Playkeeper couldn't delete the encrypted copy %s from its spool folder.", st.Name),
				Hint: "Check the disk for errors."}
		}
	}
	return err
}

// AbortStale cleans up after uploads that won't continue: unfinished
// uploads at the destination that started before olderThan, and files
// left in the spool folder since then, except those of the states in
// keep. Pass every state saved to resume, including running uploads'. It
// returns how many it removed.
func (d *Destination) AbortStale(ctx context.Context, olderThan time.Time, keep []*UploadState) (int, error) {
	n, err := d.b.abortStale(ctx, olderThan, keep)
	kept := map[string]bool{}
	for _, st := range keep {
		if st != nil {
			kept[st.Spool] = true
		}
	}
	entries, rerr := os.ReadDir(d.spool)
	if rerr != nil {
		return n, err
	}
	for _, e := range entries {
		if !isSpoolFile(e.Name()) || kept[e.Name()] {
			continue
		}
		path := filepath.Join(d.spool, e.Name())
		if fi, serr := os.Lstat(path); serr == nil && fi.Mode().IsRegular() && fi.ModTime().Before(olderThan) && os.Remove(path) == nil {
			n++
		}
	}
	return n, err
}

// Test checks that the settings work for everything copies need, with a
// small file encrypted to the current key, and always deletes that file.
func (d *Destination) Test(ctx context.Context) TestResult {
	var body bytes.Buffer
	w, err := age.Encrypt(&body, d.ids[0].id.Recipient())
	if err == nil {
		_, err = io.WriteString(w, probeText)
	}
	if err == nil {
		err = w.Close()
	}
	if err != nil {
		res := TestResult{OK: true}
		res.add("write", "", &Error{Kind: KindUnexpected, Op: opTest, Err: err, Msg: "Playkeeper couldn't encrypt the test file."})
		return res
	}
	return d.b.test(ctx, newProbeName(), body.Bytes())
}

// Download is a copy to restore, with what its record says. On a new
// machine restoring from a recovery key there is no record: take Name and
// Size from List and leave the rest empty.
type Download struct {
	Name          string // the copy's file name
	Size          int64  // of the encrypted copy
	SHA256        string // of the encrypted copy, if recorded
	Recipient     string // the key it was encrypted to, if recorded
	ArchiveSHA256 string // the backup's SHA-256, if recorded
	Dir           string // the absolute folder to restore the backup into
}

// Archive is a backup restored from a copy.
type Archive struct {
	Name      string `json:"name"` // the backup's file name
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Recipient string `json:"recipient"` // the key that opened the copy
	// Matched reports whether the backup matched Download.ArchiveSHA256.
	// Without a record it is false, and the backup is only known to be
	// encrypted to one of the server's public keys, which aren't secret,
	// so unpack it with the usual checks.
	Matched bool `json:"matched"`
}

// Download restores a copy into dl.Dir under the backup's name: it
// downloads it once, decrypts it on the way, checks it against its
// record, and only then gives the file its name. It never replaces a file.
func (d *Destination) Download(ctx context.Context, dl Download) (Archive, error) {
	archive, ok := archiveOf(dl.Name)
	switch {
	case !ok:
		return Archive{}, unexpected(opDownload, dl.Name, "That is not the name of a backup's copy.")
	case dl.Size <= 0 || dl.SHA256 != "" && !validSHA256(dl.SHA256) || dl.ArchiveSHA256 != "" && !validSHA256(dl.ArchiveSHA256):
		return Archive{}, unexpected(opDownload, dl.Name, fmt.Sprintf("The record of the copy %s has no usable size or checksum.", dl.Name))
	case !filepath.IsAbs(dl.Dir):
		return Archive{}, unexpected(opDownload, dl.Name, "Playkeeper needs an absolute folder to restore the backup into.")
	}
	final := filepath.Join(dl.Dir, archive)
	exists := func() error {
		if _, err := os.Lstat(final); err == nil {
			return &Error{Kind: KindConflict, Op: opDownload, Name: dl.Name,
				Msg:  fmt.Sprintf("The backup %s is already on this machine.", archive),
				Hint: "Restore from that file, or delete it first."}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return localWriteError(opDownload, dl.Name, err)
		}
		return nil
	}
	if err := exists(); err != nil {
		return Archive{}, err
	}
	need := dl.Size + d.reserve
	if free, err := d.diskFree(dl.Dir); err == nil && free < need {
		return Archive{}, &Error{Kind: KindNotEnoughSpace, Op: opDownload, Name: dl.Name, Need: need, Free: free,
			Msg:  fmt.Sprintf("Restoring %s needs %s free on this machine, and %s is free.", archive, humanBytes(need), humanBytes(free)),
			Hint: "Free some space, for example on the Disk space page, then try again."}
	}
	f, err := os.CreateTemp(dl.Dir, spoolPrefix+"*"+partialSuffix)
	if err != nil {
		return Archive{}, localWriteError(opDownload, dl.Name, err)
	}
	tmp := f.Name()
	defer func() {
		if f != nil {
			f.Close()
		}
		if tmp != "" {
			os.Remove(tmp)
		}
	}()
	got, err := d.open(ctx, opDownload, dl.Name, want{size: dl.Size, sha: dl.SHA256, recipient: dl.Recipient}, func() (io.Writer, error) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		return f, f.Truncate(0)
	})
	if err != nil {
		return Archive{}, err
	}
	if dl.ArchiveSHA256 != "" && !strings.EqualFold(got.sha, dl.ArchiveSHA256) {
		return Archive{}, &Error{Kind: KindVerifyFailed, Op: opDownload, Name: dl.Name,
			Msg:  fmt.Sprintf("The backup inside the copy %s doesn't match the backup's record, so it wasn't restored.", dl.Name),
			Hint: "The copy may be of another backup with the same name. Restore another copy."}
	}
	if err := f.Sync(); err != nil {
		return Archive{}, localWriteError(opDownload, dl.Name, err)
	}
	cerr := f.Close()
	f = nil
	if cerr != nil {
		return Archive{}, localWriteError(opDownload, dl.Name, cerr)
	}
	if err := exists(); err != nil {
		return Archive{}, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return Archive{}, localWriteError(opDownload, dl.Name, err)
	}
	tmp = ""
	return Archive{Name: archive, Path: final, Size: got.size, SHA256: got.sha, Recipient: got.recipient,
		Matched: dl.ArchiveSHA256 != ""}, nil
}
