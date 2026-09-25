package offsite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const sampleSize = 64 << 10

// sftpClient keeps one server's copies in a folder on another machine.
// Every operation signs in on its own connection.
type sftpClient struct {
	cfg      SFTPConfig
	port     int
	dir      string
	pinned   ssh.PublicKey
	signer   ssh.Signer
	dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	now      func() time.Time
	attempts int
	stall    time.Duration
	segment  int64
	sleep    func(context.Context, time.Duration) error
}

func newSFTP(cfg SFTPConfig, dial func(ctx context.Context, network, addr string) (net.Conn, error), now func() time.Time) (*sftpClient, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	c := &sftpClient{cfg: cfg, port: cfg.Port, dir: path.Clean(cfg.Folder), dial: dial, now: now,
		attempts: 4, stall: 2 * time.Minute, segment: 8 << 20, sleep: sleepCtx}
	if c.port == 0 {
		c.port = 22
	}
	if cfg.HostKey != "" {
		c.pinned, _ = parseHostKey(cfg.HostKey)
	}
	if k := cfg.PrivateKey.Reveal(); k != "" {
		s, err := ssh.ParsePrivateKey([]byte(k))
		if err != nil {
			return nil, invalid("privateKey", "The private key is not an OpenSSH private key Playkeeper can read.", "Sign in with a key Playkeeper makes.")
		}
		c.signer = s
	}
	if c.dial == nil {
		d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second, Control: refuseAddr}
		c.dial = d.DialContext
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c, nil
}

func (c *sftpClient) path(name string) string { return c.dir + "/" + name }

func (c *sftpClient) keyOf(name string) string { return c.path(name) }

// partialName reports whether name is an unfinished upload's: a copy's or
// connection test's name between "." and ".partial".
func partialName(name string) bool {
	n, ok := cutAffixes(name, ".", partialSuffix)
	return ok && (validCopyName(n) || isProbeName(n))
}

// run does f on a new connection, and again on another one while the
// connection is what failed and trying again may work.
func (c *sftpClient) run(ctx context.Context, op, name string, f func(s *session) error) error {
	var err error
	for i := 0; i < c.attempts; i++ {
		if i > 0 {
			if serr := c.sleep(ctx, time.Second<<(i-1)); serr != nil {
				return ctxError(op, name, serr)
			}
		}
		err = c.once(ctx, op, name, f)
		if err == nil || !asError(err).Retry || ctx.Err() != nil {
			return err
		}
	}
	return err
}

func (c *sftpClient) once(ctx context.Context, op, name string, f func(s *session) error) error {
	s, _, err := c.connect(ctx, op, c.stall)
	if err != nil {
		return err
	}
	defer s.close()
	if err := f(s); err != nil {
		return c.opError(ctx, op, name, s, err)
	}
	return nil
}

func (c *sftpClient) opError(ctx context.Context, op, name string, s *session, err error) error {
	var (
		e  *Error
		st *sftp.StatusError
	)
	switch {
	case errors.As(err, &e):
		return e
	case ctx.Err() != nil:
		return ctxError(op, name, ctx.Err())
	case s.conn.stalled.Load():
		return stalledError(op, name, s.conn.d)
	case errors.Is(err, os.ErrNotExist):
		msg := "The file is not on the other machine."
		if name != "" {
			msg = fmt.Sprintf("The copy %s is not on the other machine.", name)
		}
		return &Error{Kind: KindNotFound, Op: op, Name: name, Err: err, Msg: msg, Hint: "It may have been deleted there."}
	case errors.Is(err, os.ErrPermission):
		return c.permissionError(op, name)
	case errors.As(err, &st):
		return &Error{Kind: KindUnexpected, Op: op, Name: name, Err: err,
			Msg:  fmt.Sprintf("The other machine's SFTP server refused the request (%s).", st.FxCode()),
			Hint: "Check the folder and its permissions on the other machine."}
	}
	return c.netError(op, name, err)
}

func (c *sftpClient) permissionError(op, name string) *Error {
	what := "use files"
	switch op {
	case opUpload:
		what = "write files"
	case opList:
		what = "list the files"
	case opDelete, opAbort:
		what = "delete files"
	case opDownload, opVerify:
		what = "read files"
	}
	return &Error{Kind: KindPermission, Op: op, Name: name, Field: "folder", User: c.cfg.User, Folder: c.cfg.Folder,
		Msg:  fmt.Sprintf("The user %s may not %s in the folder %s on the other machine.", c.cfg.User, what, c.cfg.Folder),
		Hint: fmt.Sprintf("On the other machine, let %s read, write and delete files in that folder, for example with chown.", c.cfg.User)}
}

func (c *sftpClient) noFolder(op, name string) *Error {
	return &Error{Kind: KindNoSuchFolder, Op: op, Name: name, Field: "folder", User: c.cfg.User, Folder: c.cfg.Folder,
		Msg:  fmt.Sprintf("There is no folder %s on the other machine.", c.cfg.Folder),
		Hint: "Create it there first. A folder without a leading slash is inside the home folder of the user Playkeeper signs in as."}
}

// free is how many bytes the other machine can still write in the
// folder, if its server says (the statvfs@openssh.com extension).
func (c *sftpClient) free(s *session) (int64, bool) {
	v, err := s.sftp.StatVFS(c.dir)
	if err != nil || v == nil {
		return 0, false
	}
	bs := v.Frsize
	if bs == 0 {
		bs = v.Bsize
	}
	if bs == 0 || v.Bavail > math.MaxInt64/bs {
		return 0, false
	}
	return int64(v.Bavail * bs), true
}

func (c *sftpClient) fullError(name string, need, free int64) *Error {
	return &Error{Kind: KindStorageFull, Op: opUpload, Name: name, Need: need, Free: free,
		Msg:  fmt.Sprintf("The other machine's disk has no room for %s: it needs %s, and %s is free.", name, humanBytes(need), humanBytes(free)),
		Hint: "Free space on the other machine, or keep fewer copies there."}
}

// writeFailed looks for a full disk behind a write the other machine
// refused, which SFTP reports only as a failure.
func (c *sftpClient) writeFailed(s *session, name string, need int64, err error) error {
	var st *sftp.StatusError
	if errors.As(err, &st) && st.FxCode() == sftp.ErrSSHFxFailure {
		if free, ok := c.free(s); ok && free < need {
			return c.fullError(name, need, free)
		}
	}
	return err
}

func unsupported(err error) bool {
	var st *sftp.StatusError
	return errors.As(err, &st) && st.FxCode() == sftp.ErrSSHFxOpUnsupported
}

// sftpResumable reports whether an upload that failed with k keeps its
// unfinished file to continue later; otherwise the file is deleted.
func sftpResumable(k Kind) bool {
	switch k {
	case KindNetwork, KindCanceled, KindStorageFull, KindHostKeyChanged, KindHostKeyUnknown, KindLoginRefused, KindPermission, KindNoSuchFolder:
		return true
	}
	return false
}

// put stores o in the folder under a hidden name, continuing from what
// the other machine already confirmed, then gives it its name. A file
// that already has the name is never overwritten (KindConflict).
//
// The copy goes in 8 MiB segments, each written with many requests in
// flight and confirmed before the next; memory use doesn't grow with the
// copy. Everything sent is hashed on the way and must match the copy's
// SHA-256, and the first and last 64 KiB are read back.
func (c *sftpClient) put(ctx context.Context, o object) (Copy, error) {
	o.SHA256 = strings.ToLower(o.SHA256)
	if err := checkObject(o); err != nil {
		return Copy{}, err
	}
	st := o.state(c.now())
	partial := "." + o.Name + partialSuffix
	if u := st.SFTP; u == nil || u.Partial != partial || u.Written < 0 || u.Written > o.Size {
		st.SFTP = &SFTPUpload{Partial: partial}
	}
	var cp Copy
	err := c.run(ctx, opUpload, o.Name, func(s *session) error {
		var err error
		cp, err = c.send(ctx, s, o, st)
		return err
	})
	if err == nil {
		return cp, nil
	}
	e := asError(err)
	if sftpResumable(e.Kind) {
		e.Resume = st.clone()
	} else {
		c.abortQuietly(ctx, st)
	}
	return Copy{}, e
}

// sized tells File.ReadFrom how much a segment holds, so it writes it
// with many requests in flight.
type sized struct {
	*meter
	size int64
}

func (s sized) Size() int64 { return s.size }

func (c *sftpClient) send(ctx context.Context, s *session, o object, st *UploadState) (Copy, error) {
	final, partial := c.path(o.Name), c.path(st.SFTP.Partial)
	if err := c.absent(s, o.Name, final); err != nil {
		return Copy{}, err
	}
	f, err := s.sftp.OpenFile(partial, os.O_WRONLY|os.O_CREATE)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Copy{}, c.noFolder(opUpload, o.Name)
	case err != nil:
		return Copy{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Copy{}, err
	}
	start := min(st.SFTP.Written, fi.Size())
	if free, ok := c.free(s); ok && free < o.Size-start {
		return Copy{}, c.fullError(o.Name, o.Size-start, free)
	}
	if err := f.Truncate(start); err != nil {
		return Copy{}, err
	}
	st.SFTP.Written = start
	whole := sha256.New()
	if _, err := hashLocal(ctx, o, 0, start, whole); err != nil {
		return Copy{}, err
	}
	for off := start; off < o.Size; {
		n := min(c.segment, o.Size-off)
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return Copy{}, err
		}
		m := &meter{r: ctxReader{ctx, io.NewSectionReader(o.File, off, n)}, h: whole}
		if _, err := f.ReadFrom(sized{m, n}); err != nil {
			switch {
			case ctx.Err() != nil:
				return Copy{}, ctxError(opUpload, o.Name, ctx.Err())
			case m.err != nil:
				return Copy{}, &Error{Kind: KindUnexpected, Op: opUpload, Name: o.Name, Err: m.err,
					Msg: fmt.Sprintf("Playkeeper couldn't read the encrypted copy %s on this machine.", o.Name), Hint: "Check the disk for errors."}
			}
			return Copy{}, c.writeFailed(s, o.Name, o.Size-off, err)
		}
		if m.n != n {
			return Copy{}, changedError(o.Name, true)
		}
		off += n
		st.SFTP.Written = off
		progress(o, off, st)
	}
	if hex.EncodeToString(whole.Sum(nil)) != o.SHA256 {
		_ = s.sftp.Remove(partial)
		st.SFTP.Written = 0
		return Copy{}, changedError(o.Name, true)
	}
	if err := f.Sync(); err != nil && !unsupported(err) {
		return Copy{}, err
	}
	if err := f.Close(); err != nil {
		return Copy{}, err
	}
	same, err := c.sample(s, partial, o)
	if err != nil {
		return Copy{}, err
	}
	if !same {
		_ = s.sftp.Remove(partial)
		st.SFTP.Written = 0
		return Copy{}, &Error{Kind: KindVerifyFailed, Op: opVerify, Name: o.Name,
			Msg:  fmt.Sprintf("The copy %s on the other machine didn't match what was sent, so it was deleted.", o.Name),
			Hint: "Try again. If it keeps happening, check the other machine's disk."}
	}
	if err := c.absent(s, o.Name, final); err != nil {
		return Copy{}, err
	}
	if err := s.sftp.Rename(partial, final); err != nil {
		return Copy{}, err
	}
	return Copy{Name: o.Name, Key: final, Size: o.Size, SHA256: o.SHA256, Checked: CheckedSampled, VerifiedAt: c.now().UTC(), Uploaded: true}, nil
}

// absent makes sure nothing has the copy's name yet.
func (c *sftpClient) absent(s *session, name, p string) error {
	_, err := s.sftp.Lstat(p)
	switch {
	case err == nil:
		return &Error{Kind: KindConflict, Op: opUpload, Name: name,
			Msg:  fmt.Sprintf("A file named %s is already in the folder on the other machine.", name),
			Hint: "Playkeeper never overwrites it."}
	case errors.Is(err, os.ErrNotExist):
		return nil
	}
	return err
}

// sample reports whether the file at p on the other machine has the
// copy's size, and its first and last 64 KiB match the spool file's.
func (c *sftpClient) sample(s *session, p string, o object) (bool, error) {
	fi, err := s.sftp.Stat(p)
	if err != nil {
		return false, err
	}
	if fi.Size() != o.Size {
		return false, nil
	}
	f, err := s.sftp.Open(p)
	if err != nil {
		return false, err
	}
	defer f.Close()
	offs := []int64{0}
	if o.Size > sampleSize {
		offs = append(offs, o.Size-sampleSize)
	}
	for _, off := range offs {
		n := min(sampleSize, o.Size-off)
		remote, local := make([]byte, n), make([]byte, n)
		if err := readFullAt(f, remote, off); err != nil {
			return false, err
		}
		if err := readFullAt(o.File, local, off); err != nil {
			return false, &Error{Kind: KindUnexpected, Op: opUpload, Name: o.Name, Err: err,
				Msg: fmt.Sprintf("Playkeeper couldn't read the encrypted copy %s on this machine.", o.Name), Hint: "Check the disk for errors."}
		}
		if !bytes.Equal(remote, local) {
			return false, nil
		}
	}
	return true, nil
}

func readFullAt(r io.ReaderAt, b []byte, off int64) error {
	n, err := r.ReadAt(b, off)
	if n == len(b) {
		return nil
	}
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	return err
}

func (c *sftpClient) abortQuietly(ctx context.Context, st *UploadState) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_ = c.abort(ctx, st)
}

// abort deletes an unfinished upload's file. One that is already gone is
// not an error.
func (c *sftpClient) abort(ctx context.Context, st *UploadState) error {
	if st == nil || st.SFTP == nil || st.SFTP.Partial == "" {
		return nil
	}
	if !partialName(st.SFTP.Partial) {
		return unexpected(opAbort, st.Name, "The saved upload doesn't name a file Playkeeper would have uploaded.")
	}
	return c.run(ctx, opAbort, st.Name, func(s *session) error {
		if err := s.sftp.Remove(c.path(st.SFTP.Partial)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

// abortStale deletes unfinished uploads' files in the folder last written
// before olderThan, except those of the states in keep.
func (c *sftpClient) abortStale(ctx context.Context, olderThan time.Time, keep []*UploadState) (int, error) {
	kept := map[string]bool{}
	for _, st := range keep {
		if st != nil && st.SFTP != nil {
			kept[st.SFTP.Partial] = true
		}
	}
	n := 0
	err := c.run(ctx, opAbort, "", func(s *session) error {
		entries, err := s.sftp.ReadDir(c.dir)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return c.noFolder(opAbort, "")
		case err != nil:
			return err
		}
		var first error
		for _, e := range entries {
			if !e.Mode().IsRegular() || !partialName(e.Name()) || kept[e.Name()] || !e.ModTime().Before(olderThan) {
				continue
			}
			if err := s.sftp.Remove(c.path(e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				if first == nil {
					first = err
				}
				continue
			}
			n++
		}
		return first
	})
	return n, err
}

// verify checks that the copy is still there with its recorded size.
func (c *sftpClient) verify(ctx context.Context, cp Copy) (Copy, error) {
	if !validCopyName(cp.Name) {
		return Copy{}, unexpected(opVerify, cp.Name, "That is not the name of a backup's copy.")
	}
	p := c.path(cp.Name)
	err := c.run(ctx, opVerify, cp.Name, func(s *session) error {
		fi, err := s.sftp.Stat(p)
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() || fi.Size() != cp.Size {
			return &Error{Kind: KindVerifyFailed, Op: opVerify, Name: cp.Name,
				Msg:  fmt.Sprintf("The copy %s on the other machine has changed since it was uploaded (it is %d bytes, not %d).", cp.Name, fi.Size(), cp.Size),
				Hint: "Copy the backup again if it is still on this machine."}
		}
		return nil
	})
	if err != nil {
		return Copy{}, err
	}
	cp.Key, cp.VerifiedAt = p, c.now().UTC()
	return cp, nil
}

// list returns the copies in the folder, sorted by name.
func (c *sftpClient) list(ctx context.Context) ([]Object, error) {
	var out []Object
	err := c.run(ctx, opList, "", func(s *session) error {
		entries, err := s.sftp.ReadDir(c.dir)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return c.noFolder(opList, "")
		case err != nil:
			return err
		}
		out = out[:0]
		for _, e := range entries {
			if archive, ok := archiveOf(e.Name()); ok && e.Mode().IsRegular() {
				out = append(out, Object{Name: e.Name(), Archive: archive, Key: c.path(e.Name()), Size: e.Size(), LastModified: e.ModTime().UTC()})
			}
		}
		if len(out) > maxObjects {
			return &Error{Kind: KindTooLarge, Op: opList,
				Msg:  fmt.Sprintf("The folder holds more than %d copies, more than Playkeeper lists.", maxObjects),
				Hint: "Delete old copies on the other machine, or use another folder."}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b Object) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// remove deletes the copy name. One that isn't there is not an error.
func (c *sftpClient) remove(ctx context.Context, name string) error {
	if !validCopyName(name) {
		return unexpected(opDelete, name, "That is not the name of a backup's copy.")
	}
	return c.run(ctx, opDelete, name, func(s *session) error {
		if err := s.sftp.Remove(c.path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

var errReadStopped = errors.New("stopped reading")

// get streams the copy name to fn with its size. If fn returns an error
// that is not an *Error, it is tried again on a new connection.
func (c *sftpClient) get(ctx context.Context, op, name string, fn func(r io.Reader, size int64) error) error {
	if !validCopyName(name) {
		return unexpected(op, name, "That is not the name of a backup's copy.")
	}
	return c.run(ctx, op, name, func(s *session) error {
		f, err := s.sftp.Open(c.path(name))
		if err != nil {
			return err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		pr, pw := io.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			// WriteTo ends without an error at a read that fails with
			// io.EOF, as one sent while the connection closes does.
			n, err := f.WriteTo(pw)
			if err == nil && n < fi.Size() {
				err = io.ErrUnexpectedEOF
			}
			pw.CloseWithError(err)
		}()
		err = fn(pr, fi.Size())
		pr.CloseWithError(errReadStopped)
		<-done
		return err
	})
}

// test checks that the settings work for everything copies need: it
// signs in, finds the folder, writes body under a hidden name, renames
// it to name, reads it back, looks for it in the list and deletes it. If
// signing in, the folder or the write fails, nothing else is tried; both
// names are always deleted. Each step may wait 30 seconds for an answer.
func (c *sftpClient) test(ctx context.Context, name string, body []byte) TestResult {
	res := TestResult{OK: true}
	s, hk, err := c.connect(ctx, opTest, stepTimeout)
	res.HostKey = hk
	if !res.add("connect", fmt.Sprintf("Connected to the other machine, which has the confirmed host key, and signed in as %s.", c.cfg.User), err) {
		return res
	}
	defer s.close()
	check := func(err error) error {
		if err == nil {
			return nil
		}
		return c.opError(ctx, opTest, name, s, err)
	}
	if !res.add("folder", "Found the folder.", check(c.testFolder(s, &res))) {
		return res
	}
	partial, final := c.path("."+name+partialSuffix), c.path(name)
	if !res.add("write", "Wrote a small encrypted test file.", check(c.testWrite(s, name, partial, body))) {
		_ = s.sftp.Remove(partial)
		return res
	}
	at := final
	if !res.add("rename", "Gave the test file its final name, as a finished copy gets.", check(s.sftp.Rename(partial, final))) {
		at = partial
	}
	res.add("read", "Read the test file back unchanged.", check(c.testRead(s, name, at, body)))
	res.add("list", "Found the test file in the folder's list.", check(c.testList(s, name, path.Base(at))))
	res.add("delete", "Deleted the test file.", check(c.testDelete(s, name, partial, final)))
	return res
}

func (c *sftpClient) testFolder(s *session, res *TestResult) error {
	fi, err := s.sftp.Stat(c.dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return c.noFolder(opTest, "")
	case err != nil:
		return err
	case !fi.IsDir():
		return &Error{Kind: KindNoSuchFolder, Op: opTest, Field: "folder", Folder: c.cfg.Folder,
			Msg: fmt.Sprintf("%s on the other machine is a file, not a folder.", c.cfg.Folder), Hint: "Choose a folder for the copies."}
	}
	if free, ok := c.free(s); ok && free < 1<<30 {
		res.Warning = fmt.Sprintf("The other machine has only %s free in that folder, which may not be enough for the copies.", humanBytes(free))
	}
	return nil
}

func (c *sftpClient) testWrite(s *session, name, p string, body []byte) error {
	f, err := s.sftp.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return c.writeFailed(s, name, int64(len(body)), err)
	}
	return f.Close()
}

func (c *sftpClient) testRead(s *session, name, p string, want []byte) error {
	f, err := s.sftp.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	got, err := io.ReadAll(io.LimitReader(f, int64(len(want))+1))
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return &Error{Kind: KindVerifyFailed, Op: opTest, Name: name, Msg: "The test file came back different from what was written.",
			Hint: "Check the other machine's disk, and that nothing else changes files in the folder."}
	}
	return nil
}

func (c *sftpClient) testList(s *session, name, base string) error {
	entries, err := s.sftp.ReadDir(c.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == base {
			return nil
		}
	}
	return &Error{Kind: KindUnexpected, Op: opTest, Name: name, Msg: "The folder's list didn't include the test file.",
		Hint: "Rules for copies off the server rely on the list to find old copies. Check the folder on the other machine."}
}

func (c *sftpClient) testDelete(s *session, name, partial, final string) error {
	for _, p := range []string{final, partial} {
		if err := s.sftp.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if _, err := s.sftp.Lstat(final); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return &Error{Kind: KindUnexpected, Op: opTest, Name: name, Msg: "The test file was still there after deleting it.",
			Hint: "Rules for copies off the server need deleted files to go. Check the folder on the other machine."}
	}
	return nil
}
