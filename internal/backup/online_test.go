package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeServer answers console commands the way a Paper or vanilla server does
// over RCON and records every command in order. A hook replaces a command's
// behaviour; reply is the real one.
type fakeServer struct {
	mu       sync.Mutex
	commands []string
	saving   bool
	stopped  bool
	hooks    map[string]func(ctx context.Context) (string, error)
}

func newFakeServer() *fakeServer {
	return &fakeServer{saving: true, hooks: map[string]func(context.Context) (string, error){}}
}

func (s *fakeServer) Command(ctx context.Context, cmd string) (string, error) {
	s.mu.Lock()
	s.commands = append(s.commands, cmd)
	hook := s.hooks[cmd]
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx)
	}
	return s.reply(cmd)
}

func (s *fakeServer) reply(cmd string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return "", errors.New("dial tcp 172.18.0.2:25575: connect: connection refused")
	}
	switch cmd {
	case "save-off":
		if !s.saving {
			return "Saving is already turned off", nil
		}
		s.saving = false
		return "Automatic saving is now disabled", nil
	case "save-all flush":
		return "Saving the game (this may take a moment!)Saved the game", nil
	case "save-on":
		if s.saving {
			return "Saving is already turned on", nil
		}
		s.saving = true
		return "Automatic saving is now enabled", nil
	}
	return "Unknown or incomplete command, see below for error", nil
}

func (s *fakeServer) IsRunning(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.stopped, nil
}

func (s *fakeServer) stop() {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
}

func (s *fakeServer) setSaving(on bool) {
	s.mu.Lock()
	s.saving = on
	s.mu.Unlock()
}

func (s *fakeServer) isSaving() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saving
}

func (s *fakeServer) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.commands)
}

type commanderFunc func(ctx context.Context, cmd string) (string, error)

func (f commanderFunc) Command(ctx context.Context, cmd string) (string, error) { return f(ctx, cmd) }

// harness is one backup of a fixture server, recording what Take reports.
type harness struct {
	t      *testing.T
	server *fakeServer
	opts   Options
	ctx    context.Context
	cancel context.CancelFunc
	inject func(step string) error
	phases []Phase
	pauses []bool
	clock  time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, server: newFakeServer(), clock: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	t.Cleanup(func() { h.cancel() })
	out := t.TempDir()
	if err := os.Mkdir(filepath.Join(out, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	h.opts = Options{
		DataDir:       fixtureDataDir(t),
		Archive:       filepath.Join(out, "backups", "playkeeper-world-20260925-120000-a1b2c3.tar.gz"),
		StagingDir:    filepath.Join(out, "staging"),
		Meta:          Manifest{CreatedAt: h.clock, MinecraftVersion: "26.1.2", VersionID: "paper-26.1.2", PaperBuild: 74},
		Reserve:       64 << 20,
		DiskFree:      func(string) (int64, error) { return 1 << 40, nil },
		State:         ServerRunning,
		Console:       h.server,
		IsRunning:     h.server.IsRunning,
		OnPauseChange: func(p bool) { h.pauses = append(h.pauses, p) },
		OnPhase:       func(p Phase) { h.phases = append(h.phases, p) },
		Timeouts:      Timeouts{Command: time.Second, Flush: 2 * time.Second, Resume: time.Second},
		Now: func() time.Time {
			h.clock = h.clock.Add(time.Second)
			return h.clock
		},
	}
	return h
}

func (h *harness) take() (*Result, error) {
	h.t.Helper()
	r, err := newRun(h.opts)
	if err != nil {
		h.t.Fatal(err)
	}
	r.retryDelay = time.Millisecond
	r.stage.retryDelay = time.Millisecond
	r.stage.inject = h.inject
	return r.take(h.ctx)
}

func (h *harness) partial() string {
	return filepath.Join(filepath.Dir(h.opts.Archive), "."+filepath.Base(h.opts.Archive)+".partial")
}

// leftovers lists what a backup left in the backups and staging directories,
// apart from the finished archive.
func (h *harness) leftovers() []string {
	var out []string
	for _, dir := range []string{filepath.Dir(h.opts.Archive), h.opts.StagingDir} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if p := filepath.Join(dir, e.Name()); p != h.opts.Archive {
				out = append(out, p)
			}
		}
	}
	return out
}

// verified checks the archive against res and Verify, and that it holds no
// secrets.
func (h *harness) verified(res *Result) Manifest {
	h.t.Helper()
	b, err := os.ReadFile(h.opts.Archive)
	if err != nil {
		h.t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != res.SHA256 || int64(len(b)) != res.Size {
		h.t.Errorf("result says sha256 %s and %d bytes; the archive has %x and %d", res.SHA256, res.Size, sum, len(b))
	}
	if bytes.Contains(b, []byte("hunter2")) {
		h.t.Error("the archive leaks the RCON password")
	}
	m, err := Verify(bytes.NewReader(b), DefaultLimits())
	if err != nil {
		h.t.Fatalf("the archive does not verify: %v", err)
	}
	return m
}

func errorOf(t *testing.T, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("got %T (%v), want *Error", err, err)
	}
	return e
}

func paths(files []FileEntry) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func TestRunningServerIsBackedUpWithoutStopping(t *testing.T) {
	h := newHarness(t)
	res, err := h.take()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := h.server.sent(), []string{"save-off", "save-all flush", "save-on"}; !slices.Equal(got, want) {
		t.Errorf("commands %q, want %q", got, want)
	}
	if want := []Phase{PhaseCheckingSpace, PhasePausingSaves, PhaseSavingWorld, PhaseCopying, PhaseResumingSaves, PhaseArchiving}; !slices.Equal(h.phases, want) {
		t.Errorf("phases %v, want %v", h.phases, want)
	}
	if !slices.Equal(h.pauses, []bool{true, false}) {
		t.Errorf("pause changes %v, want [true false]", h.pauses)
	}
	if !h.server.isSaving() {
		t.Error("world saving was left off")
	}
	if res.Method != MethodOnlineCopy || res.Manifest.Consistency != consistencyCopy {
		t.Errorf("method %q, consistency %q", res.Method, res.Manifest.Consistency)
	}
	if res.Paused <= 0 || res.Took <= res.Paused || res.Staged == 0 || res.SavingWasOff {
		t.Errorf("result %+v", res)
	}
	m := h.verified(res)
	if m.Consistency != consistencyCopy || MethodOf(m) != MethodOnlineCopy || m.MinecraftVersion != "26.1.2" || m.LevelName != "world" {
		t.Errorf("manifest %+v", m)
	}
	_, stopped := createArchive(t, h.opts.DataDir)
	if got, want := paths(m.Files), paths(stopped.Files); !slices.Equal(got, want) {
		t.Errorf("online backup holds %q; a stopped one holds %q", got, want)
	}
	if left := h.leftovers(); len(left) > 0 {
		t.Errorf("left behind: %q", left)
	}
}

// The copy is the world as the flush wrote it; whatever the server writes
// once saving is back on must not reach the archive.
func TestStagingCopyIsTheFlushedWorld(t *testing.T) {
	h := newHarness(t)
	d := h.opts.DataDir
	h.server.hooks["save-all flush"] = func(context.Context) (string, error) {
		write(t, d, "world/region/r.0.0.mca", "flushed region")
		write(t, d, "world/region/r.1.0.mca", "region first written by the flush")
		return h.server.reply("save-all flush")
	}
	h.server.hooks["save-on"] = func(context.Context) (string, error) {
		reply, err := h.server.reply("save-on")
		write(t, d, "world/region/r.0.0.mca", "saved after the backup")
		write(t, d, "world/region/r.9.9.mca", "created after the backup")
		if err := os.Remove(filepath.Join(d, "world", "level.dat")); err != nil {
			t.Error(err)
		}
		return reply, err
	}
	res, err := h.take()
	if err != nil {
		t.Fatal(err)
	}
	h.verified(res)
	f, err := os.Open(h.opts.Archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dest := filepath.Join(t.TempDir(), "restored")
	if _, err := Extract(f, dest, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		"world/region/r.0.0.mca": "flushed region",
		"world/region/r.1.0.mca": "region first written by the flush",
		"world/level.dat":        "level-data",
	} {
		if got, err := os.ReadFile(filepath.Join(dest, rel)); err != nil || string(got) != want {
			t.Errorf("%s restored as %q (%v), want %q", rel, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "world", "region", "r.9.9.mca")); err == nil {
		t.Error("a file created after saving resumed is in the archive")
	}
}

// giveRoom makes the disk hold exactly the archive plus extra bytes.
func (h *harness) giveRoom(extra func(Size) int64) {
	h.t.Helper()
	size, err := Measure(h.opts.DataDir, DefaultLimits())
	if err != nil {
		h.t.Fatal(err)
	}
	free := size.ArchiveBytes() + h.opts.Reserve + extra(size)
	h.opts.DiskFree = func(string) (int64, error) { return free, nil }
}

func noExtra(Size) int64 { return 0 }

func TestInPlaceWhenThereIsNoRoomForACopy(t *testing.T) {
	h := newHarness(t)
	h.giveRoom(func(s Size) int64 { return s.DiskBytes })
	res, err := h.take()
	if err != nil || res.Method != MethodOnlineCopy {
		t.Fatalf("with room for the archive and the copy: %v, result %+v", err, res)
	}

	h = newHarness(t)
	h.giveRoom(func(s Size) int64 { return s.DiskBytes - 1 })
	h.server.hooks["save-on"] = func(context.Context) (string, error) {
		if _, err := os.Stat(h.partial()); err != nil {
			t.Errorf("saving resumed before the archive was written: %v", err)
		}
		if _, err := os.Stat(h.opts.Archive); err == nil {
			t.Error("the archive was in place before saving resumed")
		}
		return h.server.reply("save-on")
	}
	res, err = h.take()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := h.server.sent(), []string{"save-off", "save-all flush", "save-on"}; !slices.Equal(got, want) {
		t.Errorf("commands %q, want %q", got, want)
	}
	if want := []Phase{PhaseCheckingSpace, PhasePausingSaves, PhaseSavingWorld, PhaseArchiving, PhaseResumingSaves}; !slices.Equal(h.phases, want) {
		t.Errorf("phases %v, want %v", h.phases, want)
	}
	if res.Method != MethodOnlineInPlace || res.Staged != 0 {
		t.Errorf("result %+v", res)
	}
	if m := h.verified(res); m.Consistency != consistencyInPlace || MethodOf(m) != MethodOnlineInPlace {
		t.Errorf("consistency %q", m.Consistency)
	}
	if !h.server.isSaving() {
		t.Error("world saving was left off")
	}
	if left := h.leftovers(); len(left) > 0 {
		t.Errorf("left behind: %q", left)
	}
}

func TestRefusedBeforeAnythingIsPaused(t *testing.T) {
	t.Run("not enough disk space", func(t *testing.T) {
		h := newHarness(t)
		size, err := Measure(h.opts.DataDir, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		h.opts.DiskFree = func(string) (int64, error) { return 10 << 20, nil }
		_, err = h.take()
		e := errorOf(t, err)
		if e.Kind != KindInsufficientSpace || e.FreeBytes != 10<<20 || e.NeededBytes != size.ArchiveBytes()+h.opts.Reserve {
			t.Errorf("error %+v", e)
		}
		if !strings.HasPrefix(e.Msg, "Not enough disk space for a backup: 10.0 MiB free") || e.Hint == "" {
			t.Errorf("message %q, hint %q", e.Msg, e.Hint)
		}
		if len(h.server.sent()) > 0 || len(h.pauses) > 0 {
			t.Errorf("sent %q and paused %v before refusing", h.server.sent(), h.pauses)
		}
		if left := h.leftovers(); len(left) > 0 {
			t.Errorf("left behind: %q", left)
		}

		h = newHarness(t)
		h.giveRoom(func(Size) int64 { return -1 })
		_, err = h.take()
		if e := errorOf(t, err); e.Kind != KindInsufficientSpace || len(h.server.sent()) > 0 {
			t.Errorf("one byte short: error %+v, commands %q", e, h.server.sent())
		}
	})
	t.Run("a file a restore would refuse", func(t *testing.T) {
		h := newHarness(t)
		write(t, h.opts.DataDir, "world/region/r.0.0\n.mca", "data")
		_, err := h.take()
		var refused *RefusedError
		if e := errorOf(t, err); e.Kind != KindRefused || e.File != "world/region/r.0.0\n.mca" || !errors.As(err, &refused) {
			t.Errorf("error %+v", e)
		}
		if len(h.server.sent()) > 0 {
			t.Errorf("sent %q before refusing", h.server.sent())
		}
	})
}

func TestCheckSpaceRefusesLikeTakeAndWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.giveRoom(noExtra)
	if err := CheckSpace(h.ctx, h.opts); err != nil {
		t.Fatalf("with just enough room: %v", err)
	}
	h.giveRoom(func(Size) int64 { return -1 })
	if e := errorOf(t, CheckSpace(h.ctx, h.opts)); e.Kind != KindInsufficientSpace || e.Hint == "" {
		t.Errorf("one byte short: %+v", e)
	}
	h.giveRoom(noExtra)
	write(t, h.opts.DataDir, "world/region/r.0.0\n.mca", "data")
	if e := errorOf(t, CheckSpace(h.ctx, h.opts)); e.Kind != KindRefused {
		t.Errorf("a file a restore would refuse: %+v", e)
	}
	if len(h.server.sent()) > 0 || len(h.pauses) > 0 || len(h.phases) > 0 {
		t.Errorf("sent %q, paused %v, phases %v", h.server.sent(), h.pauses, h.phases)
	}
	if left := h.leftovers(); len(left) > 0 {
		t.Errorf("left behind: %q", left)
	}
}

func TestStoppedServerIsArchivedWithoutCommands(t *testing.T) {
	h := newHarness(t)
	h.opts.State = ServerStopped
	h.opts.Console = commanderFunc(func(_ context.Context, cmd string) (string, error) {
		t.Errorf("a stopped server was sent %q", cmd)
		return "", errors.New("not running")
	})
	res, err := h.take()
	if err != nil {
		t.Fatal(err)
	}
	if res.Method != MethodStopped || res.Paused != 0 || res.Staged != 0 {
		t.Errorf("result %+v", res)
	}
	if want := []Phase{PhaseCheckingSpace, PhaseArchiving}; !slices.Equal(h.phases, want) {
		t.Errorf("phases %v, want %v", h.phases, want)
	}
	if len(h.pauses) > 0 {
		t.Errorf("pause changes %v for a stopped server", h.pauses)
	}
	if m := h.verified(res); m.Consistency != consistencyStopped || MethodOf(m) != MethodStopped {
		t.Errorf("consistency %q", m.Consistency)
	}
	if left := h.leftovers(); len(left) > 0 {
		t.Errorf("left behind: %q", left)
	}

	h = newHarness(t)
	h.opts.State, h.opts.Console = ServerStopped, nil
	if _, err := h.take(); err != nil {
		t.Fatalf("a stopped server needs no console: %v", err)
	}
}

func TestSavingIsTurnedBackOnAfterEveryFailure(t *testing.T) {
	enospc := &fs.PathError{Op: "write", Path: "archive", Err: syscall.ENOSPC}
	tight := func(h *harness) { h.giveRoom(noExtra) }
	at := func(want string, do func()) func(string) error {
		return func(step string) error {
			if step == want {
				do()
			}
			return nil
		}
	}
	for _, c := range []struct {
		name  string
		setup func(h *harness)
		kind  ErrorKind
		// resumeFirst: saving is turned back on by the normal flow, before
		// the failure is noticed, rather than by the cleanup.
		resumeFirst bool
		commands    []string
	}{
		{name: "server cannot save", kind: KindSaveFailed, setup: func(h *harness) {
			h.server.hooks["save-all flush"] = func(context.Context) (string, error) {
				return "Saving the game (this may take a moment!)Unable to save the game (is there enough disk space?)", nil
			}
		}},
		{name: "save never confirmed", kind: KindSaveTimeout, setup: func(h *harness) {
			h.opts.Timeouts.Flush = 50 * time.Millisecond
			h.server.hooks["save-all flush"] = func(ctx context.Context) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			}
		}},
		{name: "unknown reply to save-off", kind: KindUnexpectedReply, commands: []string{"save-off", "save-on"}, setup: func(h *harness) {
			h.server.hooks["save-off"] = func(context.Context) (string, error) {
				return "Unknown or incomplete command, see below for error", nil
			}
		}},
		{name: "console fails on save-off", kind: KindConsoleUnavailable, commands: []string{"save-off", "save-on"}, setup: func(h *harness) {
			h.server.hooks["save-off"] = func(context.Context) (string, error) { return "", errors.New("read tcp: connection reset by peer") }
		}},
		{name: "console panics on save-off", kind: KindConsoleUnavailable, commands: []string{"save-off", "save-on"}, setup: func(h *harness) {
			h.server.hooks["save-off"] = func(context.Context) (string, error) { panic("assignment to entry in nil map") }
		}},
		{name: "disk fills up while copying", kind: KindDiskFull, setup: func(h *harness) {
			h.inject = func(step string) error {
				if step == "copy world/region/r.0.0.mca" {
					return enospc
				}
				return nil
			}
		}},
		{name: "disk fills up while archiving in place", kind: KindDiskFull, setup: func(h *harness) {
			tight(h)
			h.inject = func(step string) error {
				if step == "archive" {
					return enospc
				}
				return nil
			}
		}},
		{name: "world refused while archiving in place", kind: KindRefused, setup: func(h *harness) {
			tight(h)
			h.server.hooks["save-all flush"] = func(context.Context) (string, error) {
				write(h.t, h.opts.DataDir, "world/bad\nname.dat", "x")
				return h.server.reply("save-all flush")
			}
		}},
		{name: "world refused after copying", kind: KindRefused, resumeFirst: true, setup: func(h *harness) {
			h.server.hooks["save-all flush"] = func(context.Context) (string, error) {
				write(h.t, h.opts.DataDir, "world/bad\nname.dat", "x")
				return h.server.reply("save-all flush")
			}
		}},
		{name: "cancelled while saving", kind: KindCancelled, setup: func(h *harness) {
			h.server.hooks["save-all flush"] = func(ctx context.Context) (string, error) {
				h.cancel()
				return "", ctx.Err()
			}
		}},
		{name: "cancelled while copying", kind: KindCancelled, setup: func(h *harness) {
			h.inject = at("copy world/level.dat", h.cancel)
		}},
		{name: "operation runs out of time while saving", kind: KindCancelled, setup: func(h *harness) {
			h.cancel()
			h.ctx, h.cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			h.server.hooks["save-all flush"] = func(ctx context.Context) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			}
		}},
		{name: "server stops while saving", kind: KindServerStopped, setup: func(h *harness) {
			h.server.hooks["save-all flush"] = func(context.Context) (string, error) {
				h.server.stop()
				return "", io.EOF
			}
		}},
		{name: "server stops while copying", kind: KindServerStopped, resumeFirst: true, setup: func(h *harness) {
			h.inject = at("copy world/level.dat", h.server.stop)
		}},
		{name: "saving turned back on by something else", kind: KindSavingResumed, resumeFirst: true, setup: func(h *harness) {
			h.inject = at("copy world/level.dat", func() { h.server.setSaving(true) })
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			c.setup(h)
			var saveOnCtxErr error
			h.server.hooks["save-on"] = func(ctx context.Context) (string, error) {
				saveOnCtxErr = ctx.Err()
				if left := h.leftovers(); !c.resumeFirst && len(left) > 0 {
					t.Errorf("saving resumed before cleaning up %q", left)
				}
				return h.server.reply("save-on")
			}
			_, err := h.take()
			e := errorOf(t, err)
			if e.Kind != c.kind {
				t.Fatalf("kind %q (%v), want %q", e.Kind, err, c.kind)
			}
			if e.Msg == "" || strings.Contains(e.Msg, "hunter2") {
				t.Errorf("message %q", e.Msg)
			}
			want := c.commands
			if want == nil {
				want = []string{"save-off", "save-all flush", "save-on"}
			}
			if got := h.server.sent(); !slices.Equal(got, want) {
				t.Errorf("commands %q, want %q", got, want)
			}
			if saveOnCtxErr != nil {
				t.Errorf("save-on was sent with a finished context: %v", saveOnCtxErr)
			}
			if !slices.Equal(h.pauses, []bool{true, false}) {
				t.Errorf("pause changes %v, want [true false]", h.pauses)
			}
			if c.kind != KindServerStopped && !h.server.isSaving() {
				t.Error("world saving was left off")
			}
			if e.SavingPaused {
				t.Error("the error says saving may be paused, but it is on")
			}
			if left := h.leftovers(); len(left) > 0 {
				t.Errorf("left behind: %q", left)
			}
		})
	}
}

func TestSavingIsTurnedBackOnAfterAPanic(t *testing.T) {
	for name, setup := range map[string]func(h *harness){
		"while copying": func(h *harness) {
			h.opts.OnPhase = func(p Phase) {
				if p == PhaseCopying {
					panic("boom")
				}
			}
		},
		"while archiving in place": func(h *harness) {
			h.giveRoom(noExtra)
			h.inject = func(step string) error {
				if step == "archive" {
					panic("boom")
				}
				return nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			setup(h)
			func() {
				defer func() {
					if recover() == nil {
						t.Error("the panic did not reach the caller")
					}
				}()
				h.take()
			}()
			if got, want := h.server.sent(), []string{"save-off", "save-all flush", "save-on"}; !slices.Equal(got, want) {
				t.Errorf("commands %q, want %q", got, want)
			}
			if !h.server.isSaving() || !slices.Equal(h.pauses, []bool{true, false}) {
				t.Errorf("saving %v, pause changes %v", h.server.isSaving(), h.pauses)
			}
			if left := h.leftovers(); len(left) > 0 {
				t.Errorf("left behind: %q", left)
			}
		})
	}
}

func TestSavingThatCannotBeResumedIsReported(t *testing.T) {
	for name, flush := range map[string]func(h *harness) func(context.Context) (string, error){
		"after copying": nil,
		"after a failed save": func(h *harness) func(context.Context) (string, error) {
			return func(context.Context) (string, error) {
				h.server.reply("save-all flush")
				return "Saving the game (this may take a moment!)Unable to save the game (is there enough disk space?)", nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.opts.Timeouts.Resume = 100 * time.Millisecond
			if flush != nil {
				h.server.hooks["save-all flush"] = flush(h)
			}
			h.server.hooks["save-on"] = func(context.Context) (string, error) { return "", errors.New("read tcp: i/o timeout") }
			_, err := h.take()
			e := errorOf(t, err)
			if e.Kind != KindSavingPaused || !e.SavingPaused {
				t.Fatalf("error %+v", e)
			}
			if !strings.HasPrefix(e.Msg, "World saving is paused on the server") || !strings.Contains(e.Hint, "save-on") {
				t.Errorf("message %q, hint %q", e.Msg, e.Hint)
			}
			if flush != nil && !strings.Contains(e.Msg, "could not save the world") {
				t.Errorf("the message does not say why the backup failed: %q", e.Msg)
			}
			if !slices.Equal(h.pauses, []bool{true}) {
				t.Errorf("pause changes %v, want [true]: saving is still paused", h.pauses)
			}
			if n := strings.Count(strings.Join(h.server.sent(), ","), "save-on"); n < 2 {
				t.Errorf("save-on was tried %d times", n)
			}
			if left := h.leftovers(); len(left) > 0 {
				t.Errorf("left behind: %q", left)
			}

			delete(h.server.hooks, "save-on")
			if err := ResumeSaving(context.Background(), h.server); err != nil || !h.server.isSaving() {
				t.Fatalf("ResumeSaving: %v, saving %v", err, h.server.isSaving())
			}
		})
	}
}

// A reply lost on the way back is not a failure: another save-all flush is
// safe, and save-on that finds saving on after a lost reply succeeded.
func TestLostRepliesAreRetried(t *testing.T) {
	h := newHarness(t)
	flushes, ons := 0, 0
	h.server.hooks["save-all flush"] = func(context.Context) (string, error) {
		if flushes++; flushes == 1 {
			return "", io.ErrUnexpectedEOF
		}
		return h.server.reply("save-all flush")
	}
	h.server.hooks["save-on"] = func(context.Context) (string, error) {
		reply, err := h.server.reply("save-on")
		if ons++; ons == 1 {
			return "", io.ErrUnexpectedEOF
		}
		return reply, err
	}
	res, err := h.take()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := h.server.sent(), []string{"save-off", "save-all flush", "save-all flush", "save-on", "save-on"}; !slices.Equal(got, want) {
		t.Errorf("commands %q, want %q", got, want)
	}
	h.verified(res)
}

func TestSlowSaveIsWaitedFor(t *testing.T) {
	h := newHarness(t)
	h.opts.Now = time.Now
	h.server.hooks["save-all flush"] = func(ctx context.Context) (string, error) {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return h.server.reply("save-all flush")
	}
	res, err := h.take()
	if err != nil {
		t.Fatal(err)
	}
	if res.Paused < 200*time.Millisecond || res.Took < res.Paused {
		t.Errorf("paused %v, took %v", res.Paused, res.Took)
	}
}

func TestSavingAlreadyOffIsTurnedOn(t *testing.T) {
	h := newHarness(t)
	h.server.setSaving(false)
	res, err := h.take()
	if err != nil {
		t.Fatal(err)
	}
	if !res.SavingWasOff || !h.server.isSaving() {
		t.Errorf("saving was off before: %v; on now: %v", res.SavingWasOff, h.server.isSaving())
	}
}

func TestMethodOf(t *testing.T) {
	if got := MethodOf(Manifest{Consistency: "server stopped during archive"}); got != MethodStopped {
		t.Errorf("a backup from Playkeeper 0.2 is %q, want %q", got, MethodStopped)
	}
	if got := MethodOf(Manifest{Consistency: "crash-consistent snapshot"}); got != "" {
		t.Errorf("an archive from elsewhere is %q", got)
	}
}

func TestResumeSaving(t *testing.T) {
	for reply, ok := range map[string]bool{
		"Automatic saving is now enabled":                    true,
		"Saving is already turned on":                        true,
		"Unknown or incomplete command, see below for error": false,
		"": false,
	} {
		err := ResumeSaving(context.Background(), commanderFunc(func(_ context.Context, cmd string) (string, error) {
			if cmd != "save-on" {
				t.Errorf("sent %q", cmd)
			}
			return reply, nil
		}))
		if (err == nil) != ok {
			t.Errorf("reply %q: error %v", reply, err)
		}
	}
	if err := ResumeSaving(context.Background(), commanderFunc(func(context.Context, string) (string, error) {
		return "", errors.New("connection refused")
	})); err == nil {
		t.Error("a console error was taken for success")
	}
}

func TestTakeRefusesBadOptions(t *testing.T) {
	for name, change := range map[string]func(o *Options){
		"no data directory":         func(o *Options) { o.DataDir = "" },
		"archive not a .tar.gz":     func(o *Options) { o.Archive += ".zip" },
		"hidden archive name":       func(o *Options) { o.Archive = filepath.Join(filepath.Dir(o.Archive), ".x.tar.gz") },
		"no disk check":             func(o *Options) { o.DiskFree = nil },
		"unknown state":             func(o *Options) { o.State = 0 },
		"running without a console": func(o *Options) { o.Console = nil },
		"archive exists": func(o *Options) {
			if err := os.WriteFile(o.Archive, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			change(&h.opts)
			_, err := Take(context.Background(), h.opts)
			if e := errorOf(t, err); e.Kind != KindFailed || len(h.server.sent()) > 0 {
				t.Errorf("error %+v, commands %q", e, h.server.sent())
			}
		})
	}
}
