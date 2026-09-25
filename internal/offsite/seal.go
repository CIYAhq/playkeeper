package offsite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"filippo.io/age"
)

// Spool files are copies being encrypted (.partial) or waiting to be sent
// (.age), named .offsite-<16 hex digits>.
const (
	spoolPrefix   = ".offsite-"
	partialSuffix = ".partial"
	ageChunk      = 64 << 10
)

func newSpoolID() string {
	var id [8]byte
	_, _ = rand.Read(id[:])
	return hex.EncodeToString(id[:])
}

func spoolID(name, suffix string) bool {
	id, ok := cutAffixes(name, spoolPrefix, suffix)
	if !ok || len(id) != 16 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func validSpoolName(name string) bool { return spoolID(name, ".age") }

func isSpoolFile(name string) bool { return spoolID(name, ".age") || spoolID(name, partialSuffix) }

// sealedSize is at least how large a backup of n bytes is once encrypted:
// age adds a 16-byte tag to every 64 KiB, a 16-byte nonce and a header of
// about 200 bytes for one key.
func sealedSize(n int64) int64 {
	return n + 16*max(1, (n+ageChunk-1)/ageChunk) + 16 + 512
}

type spooled struct {
	name string // in the spool folder
	f    *os.File
	size int64
	sha  string // hex SHA-256 of the encrypted copy
}

// meter hashes and counts what is read through it and keeps the first
// error that isn't the end of the data. age takes a short read for the
// last chunk and passes read errors through, so both are checked here.
type meter struct {
	r   io.Reader
	h   hash.Hash
	n   int64
	err error
}

func (m *meter) Read(p []byte) (int, error) {
	n, err := m.r.Read(p)
	m.n += int64(n)
	m.h.Write(p[:n])
	if err != nil && err != io.EOF && m.err == nil {
		m.err = err
	}
	return n, err
}

// sink counts what is written through it and keeps the first error.
type sink struct {
	w   io.Writer
	n   int64
	err error
}

func (s *sink) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	n, err := s.w.Write(p)
	s.n += int64(n)
	s.err = err
	return n, err
}

// seal encrypts the backup to the current key into a new spool file,
// checking on the way that it is the backup its record describes.
func (d *Destination) seal(ctx context.Context, up Upload) (*spooled, error) {
	need := sealedSize(up.Size) + d.reserve
	if free, err := d.diskFree(d.spool); err == nil && free < need {
		return nil, &Error{Kind: KindNotEnoughSpace, Op: opUpload, Name: up.Name, Need: need, Free: free,
			Msg:  fmt.Sprintf("Encrypting %s needs %s free on this machine, and %s is free.", up.Name, humanBytes(need), humanBytes(free)),
			Hint: "Free some space, for example on the Disk space page, then try again."}
	}
	id := newSpoolID()
	path := filepath.Join(d.spool, spoolPrefix+id+partialSuffix)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, spoolError(up.Name, err)
	}
	done := false
	defer func() {
		if !done {
			f.Close()
			os.Remove(path)
		}
	}()
	out, cipher := &sink{w: f}, sha256.New()
	w, err := age.Encrypt(io.MultiWriter(out, cipher), d.ids[0].id.Recipient())
	if err != nil {
		return nil, &Error{Kind: KindUnexpected, Op: opUpload, Name: up.Name, Err: err, Msg: "Playkeeper couldn't start encrypting the backup."}
	}
	in := &meter{r: ctxReader{ctx, io.NewSectionReader(up.File, 0, up.Size+1)}, h: sha256.New()}
	_, err = io.CopyBuffer(w, in, make([]byte, 256<<10))
	if err == nil {
		err = w.Close()
	}
	switch {
	case ctx.Err() != nil:
		return nil, ctxError(opUpload, up.Name, ctx.Err())
	case in.err != nil:
		return nil, &Error{Kind: KindUnexpected, Op: opUpload, Name: up.Name, Err: in.err,
			Msg: fmt.Sprintf("Playkeeper couldn't read the backup file %s.", up.Name), Hint: "Check the disk for errors."}
	case out.err != nil:
		return nil, spoolError(up.Name, out.err)
	case err != nil:
		return nil, &Error{Kind: KindUnexpected, Op: opUpload, Name: up.Name, Err: err, Msg: "Playkeeper couldn't encrypt the backup."}
	case in.n != up.Size || hex.EncodeToString(in.h.Sum(nil)) != up.SHA256:
		return nil, &Error{Kind: KindLocalChanged, Op: opUpload, Name: up.Name,
			Msg:  fmt.Sprintf("The backup file %s doesn't match the checksum recorded when it was made, so it wasn't copied.", up.Name),
			Hint: "It may have been changed or damaged on this disk. Make a new backup and copy that one."}
	}
	if err := f.Sync(); err != nil {
		return nil, spoolError(up.Name, err)
	}
	sealed := filepath.Join(d.spool, spoolPrefix+id+".age")
	if err := os.Rename(path, sealed); err != nil {
		return nil, spoolError(up.Name, err)
	}
	done = true
	return &spooled{name: filepath.Base(sealed), f: f, size: out.n, sha: hex.EncodeToString(cipher.Sum(nil))}, nil
}

func noSpace(err error) bool { return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) }

func spoolError(name string, err error) *Error {
	if noSpace(err) {
		return &Error{Kind: KindNotEnoughSpace, Op: opUpload, Name: name, Err: err,
			Msg:  fmt.Sprintf("This machine's disk filled up while encrypting %s.", name),
			Hint: "Free some space, for example on the Disk space page, then try again."}
	}
	return &Error{Kind: KindUnexpected, Op: opUpload, Name: name, Err: err,
		Msg:  fmt.Sprintf("Playkeeper couldn't write the encrypted copy of %s in its spool folder.", name),
		Hint: "Check that the folder exists and that the disk has no errors."}
}

func localWriteError(op, name string, err error) *Error {
	if noSpace(err) {
		return &Error{Kind: KindNotEnoughSpace, Op: op, Name: name, Err: err,
			Msg:  fmt.Sprintf("This machine's disk filled up while restoring the copy %s.", name),
			Hint: "Free some space, for example on the Disk space page, then try again."}
	}
	return &Error{Kind: KindUnexpected, Op: op, Name: name, Err: err,
		Msg:  fmt.Sprintf("Playkeeper couldn't write the backup from the copy %s on this machine.", name),
		Hint: "Check that the folder exists and that the disk has no errors."}
}

// want is what a copy's record says about it; zero values are unknown.
type want struct {
	size      int64  // of the encrypted copy
	sha       string // of the encrypted copy
	recipient string // the key it was encrypted to
}

// opened describes a copy that was downloaded and decrypted.
type opened struct {
	size       int64  // of the backup inside
	sha        string // of the backup inside
	cipherSize int64
	cipherSHA  string
	recipient  string // the key that opened it
}

// open downloads the copy name, decrypts it with the server's keys into
// the writer fresh returns (called again if the download is retried), and
// checks it against w. Nothing is trusted before age has authenticated it,
// and nothing more than the record's size is read.
func (d *Destination) open(ctx context.Context, op, name string, w want, fresh func() (io.Writer, error)) (opened, error) {
	var res opened
	err := d.b.get(ctx, op, name, func(r io.Reader, size int64) error {
		if w.size > 0 && size >= 0 && size != w.size {
			return d.mismatch(op, name, fmt.Sprintf("it is %d bytes, not %d", size, w.size))
		}
		limit := int64(maxObjectSize)
		switch {
		case w.size > 0:
			limit = w.size
		case size >= 0:
			limit = size
		}
		dst, err := fresh()
		if err != nil {
			return localWriteError(op, name, err)
		}
		var hit string
		ids := make([]age.Identity, len(d.ids))
		for i, k := range d.ids {
			ids[i] = tracked{keyed: k, hit: &hit}
		}
		in, out, plain := &meter{r: io.LimitReader(r, limit+1), h: sha256.New()}, &sink{w: dst}, sha256.New()
		pr, err := age.Decrypt(in, ids...)
		if err == nil {
			_, err = io.CopyBuffer(io.MultiWriter(out, plain), pr, make([]byte, 256<<10))
		}
		switch {
		case in.err != nil:
			return in.err
		case out.err != nil:
			return localWriteError(op, name, out.err)
		case in.n > limit:
			return d.mismatch(op, name, "it is larger than its record says")
		case err != nil:
			return d.undecryptable(op, name, w.recipient, err)
		case w.size > 0 && in.n != w.size:
			return d.mismatch(op, name, fmt.Sprintf("it is %d bytes, not %d", in.n, w.size))
		case w.sha != "" && !strings.EqualFold(hex.EncodeToString(in.h.Sum(nil)), w.sha):
			return d.mismatch(op, name, "its SHA-256 differs")
		}
		res = opened{size: out.n, sha: hex.EncodeToString(plain.Sum(nil)), cipherSize: in.n,
			cipherSHA: hex.EncodeToString(in.h.Sum(nil)), recipient: hit}
		return nil
	})
	return res, err
}

func (d *Destination) mismatch(op, name, problem string) *Error {
	return &Error{Kind: KindVerifyFailed, Op: op, Name: name,
		Msg:  fmt.Sprintf("The copy %s %s doesn't match its record (%s).", name, placeOf(d.kind), problem),
		Hint: "It may have been changed or damaged there. Delete it, and copy the backup again if it is still on this machine."}
}

// undecryptable explains why age refused a copy. A copy none of the keys
// opens is another key's when its record names a key this server doesn't
// have, or has no record; with one of this server's keys on record, it
// was changed.
func (d *Destination) undecryptable(op, name, recipient string, err error) *Error {
	var none *age.NoIdentityMatchError
	if errors.As(err, &none) && !d.keys.Has(recipient) {
		return &Error{Kind: KindKeyMismatch, Op: op, Name: name, Err: err,
			Msg:  fmt.Sprintf("None of this server's encryption keys opens the copy %s.", name),
			Hint: "It was made with another key, such as another server's. The recovery key file from when it was made opens it."}
	}
	return &Error{Kind: KindVerifyFailed, Op: op, Name: name, Err: err,
		Msg:  fmt.Sprintf("The copy %s %s is damaged or was changed after it was uploaded, so it can't be opened.", name, placeOf(d.kind)),
		Hint: "Delete it, and copy the backup again if it is still on this machine."}
}
