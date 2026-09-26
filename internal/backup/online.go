package backup

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Commander runs one console command on one server and returns its reply, as
// RCON does. It must return once ctx is done, and must not resend a command by
// itself: whether a command may be repeated is Take's decision.
type Commander interface {
	Command(ctx context.Context, cmd string) (string, error)
}

// Paper and vanilla-based servers (Fabric, Quilt, NeoForge) share these
// commands and English replies. Over RCON a command runs to completion on the
// server thread before its reply is sent, so the reply to "save-all flush"
// means every world file has been written and flushed.
const (
	cmdSaveOff   = "save-off"
	cmdSaveFlush = "save-all flush"
	cmdSaveOn    = "save-on"

	replySavingOff  = "Automatic saving is now disabled"
	replyAlreadyOff = "Saving is already turned off"
	replySaved      = "Saved the game"
	replySaveFailed = "Unable to save the game"
	replySavingOn   = "Automatic saving is now enabled"
	replyAlreadyOn  = "Saving is already turned on"
)

// State is whether the server is running when the backup starts.
type State int

const (
	ServerStopped State = iota + 1
	ServerRunning
)

// Method is how a backup was made, for storing with it and showing.
type Method string

const (
	// MethodStopped: the server was not running and was archived as it was.
	MethodStopped Method = "stopped"
	// MethodOnlineCopy: saving was paused while the world was copied to the
	// staging directory; the archive was written from the copy.
	MethodOnlineCopy Method = "online_copy"
	// MethodOnlineInPlace: with no room for a copy, saving stayed paused while
	// the archive was written.
	MethodOnlineInPlace Method = "online_in_place"
)

// Phase is the step a backup is in.
type Phase string

const (
	PhaseCheckingSpace Phase = "checking_space"
	PhasePausingSaves  Phase = "pausing_saves"
	PhaseSavingWorld   Phase = "saving_world"
	PhaseCopying       Phase = "copying"
	PhaseResumingSaves Phase = "resuming_saves"
	PhaseArchiving     Phase = "archiving"
)

const (
	consistencyStopped = "server stopped during archive"
	consistencyCopy    = "server running: world saved with save-all flush, saving paused while copying; players stayed online"
	consistencyInPlace = "server running: world saved with save-all flush, saving paused while archiving; players stayed online"
)

// MethodOf tells how an archive was made from its manifest, or returns "" for
// an archive whose Consistency Playkeeper did not write.
func MethodOf(m Manifest) Method {
	switch m.Consistency {
	case consistencyStopped:
		return MethodStopped
	case consistencyCopy:
		return MethodOnlineCopy
	case consistencyInPlace:
		return MethodOnlineInPlace
	}
	return ""
}

// Timeouts bound the console steps; zero fields take the defaults.
type Timeouts struct {
	// Command bounds one save-off or save-on (default 15 s).
	Command time.Duration
	// Flush bounds getting the server to confirm that the world is saved,
	// retries included (default 5 min). Large worlds on slow disks take a
	// while, and the server does little else meanwhile.
	Flush time.Duration
	// Resume bounds turning saving back on, retries included (default 45 s).
	Resume time.Duration
}

const maxRetryDelay = 4 * time.Second

// Options describe one backup of one server. Everything it touches is passed
// in, so the agent can back up several servers independently.
type Options struct {
	// DataDir is the server's data directory.
	DataDir string
	// Archive is the .tar.gz to write; it must not exist. It is written to a
	// hidden ".<name>.partial" file beside it and renamed when complete.
	Archive string
	// StagingDir receives a short-lived copy of the world, so saving is paused
	// only while copying. Empty means always archiving in place.
	StagingDir string
	// Meta supplies the manifest's descriptive fields. Take sets Consistency.
	Meta Manifest
	// Limits bound the archive; zero means DefaultLimits.
	Limits Limits
	// Reserve is the free space to leave on each filesystem written to.
	Reserve int64
	// DiskFree reports the free bytes of the filesystem holding a directory.
	DiskFree func(dir string) (int64, error)

	State State
	// Console reaches the server; required when State is ServerRunning.
	Console Commander
	// IsRunning tells a server that stopped from a console hiccup. Optional,
	// but without it a stopped server is reported as a console failure.
	IsRunning func(ctx context.Context) (bool, error)
	// OnPauseChange is called with true just before save-off is sent and with
	// false once saving is on again or the server has stopped. Persist it: a
	// caller that dies in between must call ResumeSaving when it is back.
	OnPauseChange func(paused bool)
	// OnPhase reports progress. Optional.
	OnPhase  func(Phase)
	Timeouts Timeouts
	// Now is the clock for the reported durations; nil means time.Now.
	Now func() time.Time
}

// Result describes a finished backup.
type Result struct {
	Manifest Manifest
	// SHA256 and Size are those of the archive file.
	SHA256 string
	Size   int64
	Method Method
	// Paused is how long world saving was paused; zero for a stopped server.
	Paused time.Duration
	// Took is the whole backup, from the space check to the archive in place.
	Took time.Duration
	// SavingWasOff means saving was already paused before the backup (after an
	// earlier failure, or by a console command). It is on now.
	SavingWasOff bool
	// Staged is the bytes copied to the staging directory.
	Staged int64
}

// Take backs up one server to o.Archive. A stopped server is archived as it
// is, without console commands. A running server keeps its players: Take
// pauses world saving, has the server write the whole world to disk, copies it
// to o.StagingDir (or archives it in place when there is no room for a copy),
// turns saving back on and archives the copy. Free space is checked before
// anything is paused.
//
// Saving is turned back on whatever happens, including errors, cancellation of
// ctx and panics, using a fresh context. Every error is an *Error, and after
// one no archive, partial archive or staging copy is left behind.
func Take(ctx context.Context, o Options) (*Result, error) {
	r, err := newRun(o)
	if err != nil {
		return nil, err
	}
	return r.take(ctx)
}

// CheckSpace is Take's first step for a stopped server on its own: it sizes
// the backup and checks free space, writing nothing. A caller that stops the
// server for its backup calls it first, so a backup that can't fit never
// stops the server. It returns the *Error Take would.
func CheckSpace(ctx context.Context, o Options) error {
	o.State, o.Console = ServerStopped, nil
	r, err := newRun(o)
	if err != nil {
		return err
	}
	size, err := Measure(r.o.DataDir, r.lim)
	if err != nil {
		return r.fail(ctx, err)
	}
	_, err = r.plan(size)
	return err
}

// ResumeSaving sends save-on once. It is for a caller whose backup failed with
// KindSavingPaused, or that died between OnPauseChange(true) and
// OnPauseChange(false); retry it until it returns nil. A server that is
// already saving counts as success.
func ResumeSaving(ctx context.Context, c Commander) error {
	reply, err := runCommand(ctx, c, cmdSaveOn)
	if err != nil {
		return err
	}
	if strings.Contains(reply, replySavingOn) || strings.Contains(reply, replyAlreadyOn) {
		return nil
	}
	return errUnexpectedReply(cmdSaveOn, reply)
}

// runCommand turns a panic in c into an error, so the steps that turn saving
// back on still run.
func runCommand(ctx context.Context, c Commander, cmd string) (reply string, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("the console command %q panicked: %v", cmd, p)
		}
	}()
	return c.Command(ctx, cmd)
}

// run is one backup in progress.
type run struct {
	o          Options
	lim        Limits
	stage      stager
	retryDelay time.Duration

	start, pausedAt, resumedAt time.Time
	// paused is set while save-off may have reached the server and save-on
	// is not confirmed.
	paused      bool
	offOK       bool
	wasOff      bool
	resumeTried bool
	stageDir    string
	tmp         string
	manifest    Manifest
	sum         string
	size        int64
}

func newRun(o Options) (*run, error) {
	base := filepath.Base(o.Archive)
	switch {
	case o.DataDir == "":
		return nil, errMisuse("no data directory was given")
	case !strings.HasSuffix(base, ".tar.gz") || strings.HasPrefix(base, "."):
		return nil, errMisuse("the archive must be a .tar.gz file")
	case o.DiskFree == nil:
		return nil, errMisuse("no way to check free disk space was given")
	case o.State != ServerStopped && o.State != ServerRunning:
		return nil, errMisuse("the server state is unknown")
	case o.State == ServerRunning && o.Console == nil:
		return nil, errMisuse("a running server needs a console")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	t := &o.Timeouts
	if t.Command <= 0 {
		t.Command = 15 * time.Second
	}
	if t.Flush <= 0 {
		t.Flush = 5 * time.Minute
	}
	if t.Resume <= 0 {
		t.Resume = 45 * time.Second
	}
	const retry = 250 * time.Millisecond
	return &run{o: o, lim: o.Limits.orDefault(), retryDelay: retry, stage: stager{retryDelay: retry}}, nil
}

func (r *run) take(ctx context.Context) (res *Result, err error) {
	r.start = r.o.Now()
	defer func() {
		if res == nil {
			err = r.undo(ctx, err)
		}
	}()
	if _, err := os.Lstat(r.o.Archive); err == nil {
		return nil, errMisuse(r.o.Archive + " already exists")
	}
	r.phase(PhaseCheckingSpace)
	size, err := Measure(r.o.DataDir, r.lim)
	if err != nil {
		return nil, r.fail(ctx, err)
	}
	staged, err := r.plan(size)
	if err != nil {
		return nil, err
	}
	if r.o.State == ServerStopped {
		r.phase(PhaseArchiving)
		if err := r.archive(ctx, r.o.DataDir, consistencyStopped); err != nil {
			return nil, err
		}
		return r.commit(ctx, MethodStopped)
	}

	if err := r.pause(ctx); err != nil {
		return nil, err
	}
	if err := r.flush(ctx); err != nil {
		return nil, err
	}
	if !staged {
		r.phase(PhaseArchiving)
		if err := r.archive(ctx, r.o.DataDir, consistencyInPlace); err != nil {
			return nil, err
		}
		if err := r.endPause(ctx); err != nil {
			return nil, err
		}
		return r.commit(ctx, MethodOnlineInPlace)
	}
	r.phase(PhaseCopying)
	if err := r.stage.copyAll(ctx, r.o.DataDir, r.stageDir); err != nil {
		return nil, r.fail(ctx, err)
	}
	if err := r.endPause(ctx); err != nil {
		return nil, err
	}
	r.phase(PhaseArchiving)
	if err := r.archive(ctx, r.stageDir, consistencyCopy); err != nil {
		return nil, err
	}
	return r.commit(ctx, MethodOnlineCopy)
}

// undo cleans up after a failed or panicking backup. It removes the staging
// copy and partial archive first, so their space is free before the server
// saves again, then turns saving back on if it may still be off.
func (r *run) undo(ctx context.Context, cause error) error {
	if r.tmp != "" {
		os.Remove(r.tmp)
	}
	if r.stageDir != "" {
		os.RemoveAll(r.stageDir)
	}
	if !r.paused || r.resumeTried {
		return cause
	}
	if _, err := r.resume(ctx, cause); err != nil {
		return err
	}
	return cause
}

// plan checks free space before anything is paused, and decides whether a
// running server's world is copied to a new staging directory first.
func (r *run) plan(size Size) (staged bool, err error) {
	dir := filepath.Dir(r.o.Archive)
	need := size.ArchiveBytes() + r.o.Reserve
	free, err := r.o.DiskFree(dir)
	if err != nil {
		return false, errFailed("Playkeeper could not check the free disk space", err)
	}
	if r.o.State == ServerRunning && r.o.StagingDir != "" && r.roomForCopy(dir, free, need, size.DiskBytes) {
		if d, err := os.MkdirTemp(r.o.StagingDir, "online-backup-"); err == nil {
			r.stageDir = d
			return true, nil
		}
	}
	if free < need {
		return false, errInsufficientSpace(free, need)
	}
	return false, nil
}

// roomForCopy reports whether a staging copy fits beside the archive.
func (r *run) roomForCopy(archiveDir string, free, need, copyNeed int64) bool {
	if err := os.MkdirAll(r.o.StagingDir, 0o700); err != nil {
		return false
	}
	if sameFilesystem(archiveDir, r.o.StagingDir) {
		return free >= need+copyNeed
	}
	stagingFree, err := r.o.DiskFree(r.o.StagingDir)
	return err == nil && free >= need && stagingFree >= copyNeed+r.o.Reserve
}

func (r *run) pause(ctx context.Context) error {
	r.phase(PhasePausingSaves)
	r.setPaused(true)
	reply, err := r.command(ctx, cmdSaveOff, r.o.Timeouts.Command)
	if err != nil {
		return r.consoleFailed(ctx, err)
	}
	switch {
	case strings.Contains(reply, replySavingOff):
	case strings.Contains(reply, replyAlreadyOff):
		r.wasOff = true
	default:
		return errUnexpectedReply(cmdSaveOff, reply)
	}
	r.offOK = true
	return nil
}

// flush has the server write the whole world to disk. A lost reply is
// followed by another save-all flush: commands run one at a time on the server
// thread, so a later reply also covers the first save.
func (r *run) flush(ctx context.Context) error {
	r.phase(PhaseSavingWorld)
	deadline := time.Now().Add(r.o.Timeouts.Flush)
	delay := r.retryDelay
	for {
		reply, err := r.command(ctx, cmdSaveFlush, time.Until(deadline))
		if err == nil {
			switch {
			case strings.Contains(reply, replySaveFailed):
				return errSaveFailed(reply)
			case strings.Contains(reply, replySaved):
				return nil
			default:
				return errUnexpectedReply(cmdSaveFlush, reply)
			}
		}
		if ctx.Err() != nil {
			return errCancelled(ctx.Err())
		}
		if r.serverStopped(ctx) {
			return errServerStopped(nil)
		}
		wait := min(delay, time.Until(deadline))
		if wait <= 0 {
			return r.errSaveTimeout()
		}
		if err := sleep(ctx, wait); err != nil {
			return errCancelled(err)
		}
		delay = min(2*delay, maxRetryDelay)
	}
}

// endPause turns saving back on once the world is copied or archived. Saving
// found already on means something else (a restart, a plugin, a console
// command) turned it on meanwhile, so the copy may not be one moment's world.
func (r *run) endPause(ctx context.Context) error {
	r.phase(PhaseResumingSaves)
	early, err := r.resume(ctx, nil)
	if err != nil {
		return err
	}
	if early {
		return errSavingResumed()
	}
	return nil
}

// resume sends save-on until the server confirms saving is on, for up to
// Timeouts.Resume and with a context that ctx's cancellation does not reach.
// cause is why the backup failed, or nil. early reports that the first
// attempt found saving already on.
func (r *run) resume(ctx context.Context, cause error) (early bool, err error) {
	r.resumeTried = true
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.o.Timeouts.Resume)
	defer cancel()
	delay := r.retryDelay
	var last error
	for attempt := 1; ; attempt++ {
		reply, err := r.command(ctx, cmdSaveOn, r.o.Timeouts.Command)
		if err == nil {
			on, already := strings.Contains(reply, replySavingOn), strings.Contains(reply, replyAlreadyOn)
			if !on && !already {
				return false, r.errStillPaused(cause, errUnexpectedReply(cmdSaveOn, reply))
			}
			r.setPaused(false)
			return already && attempt == 1, nil
		}
		last = err
		if r.serverStopped(ctx) {
			r.setPaused(false)
			return false, errServerStopped(cause)
		}
		if sleep(ctx, delay) != nil {
			return false, r.errStillPaused(cause, last)
		}
		delay = min(2*delay, maxRetryDelay)
	}
}

func (r *run) command(ctx context.Context, cmd string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return runCommand(ctx, r.o.Console, cmd)
}

// consoleFailed explains a console command that returned an error.
func (r *run) consoleFailed(ctx context.Context, err error) *Error {
	switch {
	case ctx.Err() != nil:
		return errCancelled(ctx.Err())
	case r.serverStopped(ctx):
		return errServerStopped(nil)
	default:
		return r.errConsole(err)
	}
}

// serverStopped asks o.IsRunning whether the server has stopped. Without a
// probe or an answer it assumes not.
func (r *run) serverStopped(ctx context.Context) bool {
	if r.o.IsRunning == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, r.o.Timeouts.Command)
	defer cancel()
	running, err := r.o.IsRunning(ctx)
	return err == nil && !running
}

func (r *run) setPaused(p bool) {
	if p == r.paused {
		return
	}
	r.paused = p
	if p {
		r.pausedAt = r.o.Now()
	} else {
		r.resumedAt = r.o.Now()
	}
	if r.o.OnPauseChange != nil {
		r.o.OnPauseChange(p)
	}
}

func (r *run) phase(p Phase) {
	if r.o.OnPhase != nil {
		r.o.OnPhase(p)
	}
}

// archive writes the archive of dir to a partial file beside o.Archive.
func (r *run) archive(ctx context.Context, dir, consistency string) error {
	tmp := filepath.Join(filepath.Dir(r.o.Archive), "."+filepath.Base(r.o.Archive)+".partial")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return r.fail(ctx, err)
	}
	r.tmp = tmp
	defer f.Close()
	h := sha256.New()
	bw := bufio.NewWriterSize(&archiveWriter{ctx: ctx, s: &r.stage, w: io.MultiWriter(f, h)}, 1<<20)
	meta := r.o.Meta
	meta.Consistency = consistency
	m, err := Create(bw, dir, meta, r.lim)
	if err == nil {
		err = bw.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	var st os.FileInfo
	if err == nil {
		st, err = f.Stat()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return r.fail(ctx, err)
	}
	r.manifest, r.sum, r.size = m, hex.EncodeToString(h.Sum(nil)), st.Size()
	return nil
}

// archiveWriter stops Create once ctx is done.
type archiveWriter struct {
	ctx context.Context
	s   *stager
	w   io.Writer
}

func (a *archiveWriter) Write(p []byte) (int, error) {
	if err := a.ctx.Err(); err != nil {
		return 0, err
	}
	if err := a.s.step("archive"); err != nil {
		return 0, err
	}
	return a.w.Write(p)
}

// commit renames the finished archive into place.
func (r *run) commit(ctx context.Context, m Method) (*Result, error) {
	if err := os.Rename(r.tmp, r.o.Archive); err != nil {
		return nil, r.fail(ctx, err)
	}
	r.tmp = ""
	syncDir(filepath.Dir(r.o.Archive))
	if r.stageDir != "" {
		os.RemoveAll(r.stageDir)
		r.stageDir = ""
	}
	res := &Result{Manifest: r.manifest, SHA256: r.sum, Size: r.size, Method: m, Took: r.o.Now().Sub(r.start),
		SavingWasOff: r.wasOff, Staged: r.stage.copied}
	if !r.pausedAt.IsZero() {
		res.Paused = r.resumedAt.Sub(r.pausedAt)
	}
	return res, nil
}

// syncDir makes a rename in dir durable; failing only weakens that.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// fail turns an error from a file step into an *Error.
func (r *run) fail(ctx context.Context, err error) *Error {
	var e *Error
	var refused *RefusedError
	switch {
	case errors.As(err, &e):
		return e
	case errors.As(err, &refused):
		return r.errRefused(refused)
	case ctx.Err() != nil:
		return errCancelled(ctx.Err())
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return errDiskFull(err)
	default:
		return errFailed("The backup failed", err)
	}
}
