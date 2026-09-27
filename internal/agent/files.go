package agent

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
	"github.com/CIYAhq/playkeeper/internal/packs"
)

// Each server's file browser. Every read and write goes through
// internal/gamefiles, which keeps it inside the server's data folder and
// refuses links and special files on the way. The handlers add what the
// dashboard needs on top: paths checked as they arrive, the world folders
// left alone while the game runs, no change while the server is busy, and an
// audit row for each change.

const (
	// maxListed caps the entries one folder lists.
	maxListed = 10_000
	// maxEditBytes is the largest file the editor opens and saves.
	maxEditBytes = 2 << 20
	// maxPathBytes and maxNameBytes bound a path and each name in it.
	maxPathBytes = 4096
	maxNameBytes = 255
	// maxBatch caps the files one move, delete or download names.
	maxBatch = 1000
)

var (
	// maxZipped caps the files and folders one download packs: the zip
	// keeps a record of each in memory until it ends, about 260 bytes, and
	// the agent has a few hundred megabytes for everything it does.
	maxZipped = 100_000
	// deleteAnswerAfter is how long a delete runs before its answer says
	// it carries on: a folder of millions of small files, such as a map's
	// tiles, takes minutes, longer than the panel waits for an answer.
	deleteAnswerAfter = 30 * time.Second
	// beforeChange runs before each path a change touches, for a test to
	// hold the change there.
	beforeChange = func(p string) {}
)

var reVersion = regexp.MustCompile(`^[0-9a-f]{64}$`)

// filePath checks a path the dashboard names in the server's files:
// slash-separated and relative, without dot segments, as fs.ValidPath has
// it. "" or "." is the server's folder itself, where rootOK allows it.
func filePath(raw string, rootOK bool) (string, error) {
	switch {
	case raw == "" || raw == ".":
		if rootOK {
			return ".", nil
		}
		return "", errInvalid("Name a file or folder in the server's files.")
	case len(raw) > maxPathBytes || !fs.ValidPath(raw) || strings.ContainsRune(raw, 0):
		return "", errInvalid("%s is not a path inside the server's files.", quotePath(raw))
	}
	for _, name := range strings.Split(raw, "/") {
		if len(name) > maxNameBytes {
			return "", errInvalid("%s has a name longer than %d bytes.", quotePath(raw), maxNameBytes)
		}
	}
	return raw, nil
}

// newName refuses a name for something new that has control characters,
// which the dashboard, a terminal or a log would show wrongly.
func newName(p string) error {
	if strings.IndexFunc(path.Base(p), unicode.IsControl) >= 0 {
		return errInvalid("%s has a control character in its name.", quotePath(p))
	}
	return nil
}

// quotePath quotes a path for a message, eliding the middle of a long one.
func quotePath(p string) string {
	if len(p) > 120 {
		p = p[:60] + "…" + p[len(p)-50:]
	}
	return strconv.Quote(p)
}

// shown is a path as the dashboard names it: "" for the server's folder.
func shown(p string) string {
	if p == "." {
		return ""
	}
	return p
}

func fileType(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return api.FileTypeLink
	case m.IsDir():
		return api.FileTypeFolder
	case m.IsRegular():
		return api.FileTypeFile
	}
	return api.FileTypeSpecial
}

// filesError explains a failure after the sentence saying what couldn't be
// done: a refusal of internal/gamefiles with its kind as the code and its
// params, for the dashboard; a path that isn't there anymore; a full disk.
func filesError(err error, couldNot, p string) error {
	var ge *gamefiles.Error
	switch {
	case errors.As(err, &ge):
		params := map[string]any{}
		for k, v := range ge.Params {
			params[k] = v
		}
		return &apiError{Status: http.StatusConflict, Code: string(ge.Kind), Msg: couldNot + " " + ge.Msg, Hint: ge.Hint, Params: params, Err: ge}
	case errors.Is(err, fs.ErrNotExist):
		return &apiError{Status: http.StatusNotFound, Code: api.CodeNotFound, Msg: couldNot + " " + quotePath(p) + " isn't in the server's files anymore.",
			Hint: "Reload the folder to see what is there now.", Params: map[string]any{"path": p}, Err: err}
	case errors.Is(err, syscall.ENOSPC):
		return &apiError{Status: http.StatusInsufficientStorage, Code: api.CodeInsufficientSpace, Msg: couldNot + " The disk is full.", Hint: "Free disk space, then try again.", Err: err}
	}
	return err
}

func (s *server) openFiles() (*gamefiles.Dir, error) {
	d, err := s.gameFiles()
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &apiError{Status: http.StatusNotFound, Code: api.CodeNotFound, Msg: s.name() + " has no files yet.", Hint: "They appear once the server is set up."}
	}
	return d, err
}

// worldFolders are the folders the game keeps its world in: the level's
// own, and the Nether and the End beside it as Paper keeps them.
func (s *server) worldFolders(d *gamefiles.Dir) []string {
	level := "world"
	if sc, err := s.serverConfig(); err == nil && sc != nil {
		level = s.levelName(*sc)
	}
	return []string{level, level + "_nether", level + "_the_end"}
}

func inWorld(p string, worlds []string) bool {
	for _, w := range worlds {
		if p == w || strings.HasPrefix(p, w+"/") {
			return true
		}
	}
	return false
}

// gameRunning reports whether the server's game runs now. When Docker can't
// say, it counts as running, so that the world stays read-only.
func (s *server) gameRunning(ctx context.Context) bool {
	_, running, err := s.containerRunning(ctx)
	return running || err != nil
}

func (s *server) errWorldInUse(p string) error {
	return &apiError{Status: http.StatusConflict, Code: api.CodeWorldInUse, Params: map[string]any{"path": p},
		Msg:  s.name() + " is running, so its world is in use and " + quotePath(p) + " can't change now.",
		Hint: "Stop the server first: a world changed while the game writes it can break."}
}

// changeRefusal is why the file browser may not change paths now: the
// server is busy with an operation, such as a backup or a restore, or a path
// is in a world folder while the game runs.
func (s *server) changeRefusal(ctx context.Context, d *gamefiles.Dir, paths ...string) error {
	if s.busy() {
		return s.busyError()
	}
	worlds := s.worldFolders(d)
	for _, p := range paths {
		if inWorld(p, worlds) && s.gameRunning(ctx) {
			return s.errWorldInUse(p)
		}
	}
	return nil
}

// fileHold is the file browser's share of the server's operation lock. The
// first change to the server's files takes the lock and the last one to
// finish gives it back, so no start, backup or restore begins while one
// runs, such as a wake for a player who joins during a long delete, and
// changes don't refuse each other.
type fileHold struct {
	mu      sync.Mutex
	n       int
	release func()
	// save makes the editor's version check and its write one step, so
	// that of two saves of the same version the second is refused.
	save sync.Mutex
}

// holdFiles lets a change to paths go ahead, and holds off operations until
// release is called. It refuses while an operation holds the server, and
// when a path is in a world folder while the game runs; holding off starts
// keeps that true until the change is done.
func (s *server) holdFiles(ctx context.Context, d *gamefiles.Dir, paths ...string) (release func(), err error) {
	h := &s.fileHold
	h.mu.Lock()
	if h.n == 0 {
		rel, ok := s.holdOpLock()
		if !ok {
			h.mu.Unlock()
			return nil, s.busyError()
		}
		h.release = rel
	}
	h.n++
	h.mu.Unlock()
	release = sync.OnceFunc(func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.n--; h.n == 0 {
			h.release()
			h.release = nil
		}
	})
	if err := s.changeRefusal(ctx, d, paths...); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// changingFiles reports whether the file browser holds the operation lock.
func (s *server) changingFiles() bool {
	s.fileHold.mu.Lock()
	defer s.fileHold.mu.Unlock()
	return s.fileHold.n > 0
}

// envProperties are the server.properties keys the image writes from its
// environment each time the server starts.
var envProperties = func() map[string]string {
	m := map[string]string{
		"MOTD": "motd", "MAX_PLAYERS": "max-players", "ONLINE_MODE": "online-mode", "ENABLE_WHITELIST": "white-list",
		"ENFORCE_WHITELIST": "enforce-whitelist", "ENABLE_RCON": "enable-rcon", "RCON_PORT": "rcon.port",
		"RCON_PASSWORD_FILE": "rcon.password", "BROADCAST_RCON_TO_OPS": "broadcast-rcon-to-ops", "LOG_IPS": "log-ips",
		"ENABLE_QUERY": "enable-query", "LEVEL": "level-name", "SERVER_PORT": "server-port", "DIFFICULTY": "difficulty",
		"PVP": "pvp", "MODE": "gamemode", "HARDCORE": "hardcore", "VIEW_DISTANCE": "view-distance", "LEVEL_TYPE": "level-type",
	}
	for _, st := range packs.ClearSettings() {
		m[st.Env] = st.Property
	}
	return m
}()

// managedProperties are the keys of server.properties that Playkeeper sets
// from the server's settings each time it starts, so a change to them in the
// file doesn't last.
func (s *server) managedProperties() []string {
	sc, err := s.serverConfig()
	if err != nil || sc == nil {
		return nil
	}
	spec, _ := s.containerSpec(*sc, false, nil)
	var out []string
	for _, kv := range spec.Env {
		name, _, _ := strings.Cut(kv, "=")
		if p, ok := envProperties[name]; ok {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (s *server) hFiles(w http.ResponseWriter, r *http.Request) {
	p, err := filePath(r.URL.Query().Get("path"), true)
	if err != nil {
		writeError(w, err)
		return
	}
	d, err := s.openFiles()
	if err != nil {
		writeError(w, err)
		return
	}
	defer d.Close()
	entries, more, err := d.List(p, maxListed)
	if err != nil {
		writeError(w, filesError(err, "The folder could not be opened.", p))
		return
	}
	out := api.Files{Path: shown(p), Entries: make([]api.FileEntry, 0, len(entries)), More: more, Running: s.gameRunning(r.Context()), Worlds: s.worldFolders(d)}
	for _, e := range entries {
		fe := api.FileEntry{Name: e.Name, Type: fileType(e.Mode), ModifiedAt: e.ModTime.UTC()}
		if fe.Type == api.FileTypeFile {
			fe.Size = e.Size
		}
		out.Entries = append(out.Entries, fe)
	}
	writeJSON(w, http.StatusOK, out)
}

func tooLargeToEdit(p string) error {
	return &apiError{Status: http.StatusConflict, Code: string(gamefiles.KindTooLarge), Params: map[string]any{"path": p, "limit": strconv.Itoa(maxEditBytes)},
		Msg: quotePath(p) + " is larger than 2 MB, the most the editor opens.", Hint: "Download it to open it on your computer."}
}

// isText reports whether b is text the editor can show and save as it is.
func isText(b []byte) bool { return utf8.Valid(b) && bytes.IndexByte(b, 0) < 0 }

func contentVersion(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *server) hFileContent(w http.ResponseWriter, r *http.Request) {
	p, err := filePath(r.URL.Query().Get("path"), false)
	if err != nil {
		writeError(w, err)
		return
	}
	d, err := s.openFiles()
	if err != nil {
		writeError(w, err)
		return
	}
	defer d.Close()
	f, st, err := d.OpenFile(p)
	if err != nil {
		writeError(w, filesError(err, "The file could not be opened.", p))
		return
	}
	defer f.Close()
	if st.Size() > maxEditBytes {
		writeError(w, tooLargeToEdit(p))
		return
	}
	b, err := io.ReadAll(io.LimitReader(f, maxEditBytes+1))
	if err != nil {
		writeError(w, err)
		return
	}
	if len(b) > maxEditBytes {
		writeError(w, tooLargeToEdit(p))
		return
	}
	running := s.gameRunning(r.Context())
	out := api.FileContent{FileInfo: api.FileInfo{Path: p, Size: int64(len(b)), ModifiedAt: st.ModTime().UTC(), Version: contentVersion(b)}, Running: running}
	if isText(b) {
		out.Text = string(b)
	} else {
		out.Binary = true
	}
	if running && inWorld(p, s.worldFolders(d)) {
		out.ReadOnly = api.CodeWorldInUse
	}
	if p == "server.properties" {
		out.Managed = s.managedProperties()
	}
	writeJSON(w, http.StatusOK, out)
}

func fileChanged(p string, gone bool) error {
	msg := quotePath(p) + " changed since you opened it."
	if gone {
		msg = quotePath(p) + " was deleted since you opened it."
	}
	return &apiError{Status: http.StatusConflict, Code: api.CodeFileChanged, Params: map[string]any{"path": p, "gone": gone}, Msg: msg,
		Hint: "Open it again to see what is there now, or save yours over it."}
}

func existsAlready(p string) error {
	return &apiError{Status: http.StatusConflict, Code: string(gamefiles.KindExists), Params: map[string]any{"path": p},
		Msg: quotePath(p) + " already exists.", Hint: "Choose another name."}
}

// hFileSave saves a file from the editor, its text as the body. With
// ?expect=new it makes a file that must not exist yet; with the SHA-256 the
// editor opened, it refuses once the file changed, such as when a plugin
// wrote its settings meanwhile; without, it saves over whatever is there.
func (s *server) hFileSave(w http.ResponseWriter, r *http.Request) {
	actor := actorFromHeader(r)
	if actor == "unknown" {
		writeError(w, errInvalid("X-Playkeeper-Actor header is required"))
		return
	}
	q := r.URL.Query()
	p, err := filePath(q.Get("path"), false)
	if err != nil {
		writeError(w, err)
		return
	}
	expect := q.Get("expect")
	switch {
	case expect == "new":
		err = newName(p)
	case expect != "" && !reVersion.MatchString(expect):
		err = errInvalid("expect is new, the SHA-256 the file was opened with, or empty.")
	}
	if err != nil {
		writeError(w, err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxEditBytes+1))
	switch {
	case err != nil:
		writeError(w, errInvalid("The file didn't arrive whole. Save it again."))
		return
	case len(body) > maxEditBytes:
		writeError(w, &apiError{Status: http.StatusRequestEntityTooLarge, Code: string(gamefiles.KindTooLarge), Params: map[string]any{"path": p, "limit": strconv.Itoa(maxEditBytes)},
			Msg: "The file would be larger than 2 MB, the most the editor saves.", Hint: "Make it smaller, or upload it instead."})
		return
	case !isText(body):
		writeError(w, errInvalid("Only text can be saved in the editor."))
		return
	}
	d, err := s.openFiles()
	if err != nil {
		writeError(w, err)
		return
	}
	defer d.Close()
	release, err := s.holdFiles(r.Context(), d, p)
	if err != nil {
		writeError(w, err)
		return
	}
	defer release()
	if err := s.saveText(d, p, body, expect); err != nil {
		writeError(w, filesError(err, "The file could not be saved.", p))
		return
	}
	out := api.FileInfo{Path: p, Size: int64(len(body)), ModifiedAt: s.now().UTC(), Version: contentVersion(body)}
	if fi, err := d.Lstat(p); err == nil {
		out.ModifiedAt = fi.ModTime().UTC()
	}
	action := "files.saved"
	if expect == "new" {
		action = "files.created"
	}
	s.audit(actor, action, p, "succeeded", p)
	writeJSON(w, http.StatusOK, out)
}

// saveText writes body to p as hFileSave describes, keeping the mode of the
// file it replaces.
func (s *server) saveText(d *gamefiles.Dir, p string, body []byte, expect string) error {
	perm := fs.FileMode(0o640)
	if expect == "new" {
		_, err := d.Lstat(p)
		if err == nil {
			return existsAlready(p)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		beforeChange(p)
		if err := d.CreateFile(p, body, perm); gamefiles.KindOf(err) != gamefiles.KindExists {
			return err
		}
		return existsAlready(p)
	}
	if expect != "" {
		s.fileHold.save.Lock()
		defer s.fileHold.save.Unlock()
	}
	f, st, err := d.OpenFile(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if expect != "" {
			return fileChanged(p, true)
		}
	case err != nil:
		return err
	default:
		perm = st.Mode().Perm()
		var cur []byte
		if expect != "" {
			cur, err = io.ReadAll(io.LimitReader(f, maxEditBytes+1))
		}
		f.Close()
		if err != nil {
			return err
		}
		if expect != "" && contentVersion(cur) != expect {
			return fileChanged(p, false)
		}
	}
	beforeChange(p)
	return d.WriteFile(p, body, perm)
}

func (s *server) hFileFolder(w http.ResponseWriter, r *http.Request) {
	var req api.FileFolderRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	p, err := filePath(req.Path, false)
	if err == nil {
		err = newName(p)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	d, err := s.openFiles()
	if err != nil {
		writeError(w, err)
		return
	}
	defer d.Close()
	release, err := s.holdFiles(r.Context(), d, p)
	if err != nil {
		writeError(w, err)
		return
	}
	defer release()
	beforeChange(p)
	if err := d.MakeFolder(p); err != nil {
		writeError(w, filesError(err, "The folder could not be made.", p))
		return
	}
	s.audit(actor, "files.folder_made", p, "succeeded", p)
	writeJSON(w, http.StatusCreated, api.FileEntry{Name: path.Base(p), Type: api.FileTypeFolder, ModifiedAt: s.now().UTC()})
}

// listed names the paths of a batch for its audit row: all of them, or as
// many as fit and how many more there are.
func listed(paths []string) string {
	var b strings.Builder
	for i, p := range paths {
		if b.Len() > 300 {
			b.WriteString(" +" + strconv.Itoa(len(paths)-i))
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p)
	}
	return b.String()
}

func (s *server) hFileMove(w http.ResponseWriter, r *http.Request) {
	var req api.FileMoveRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(req.Items) == 0 || len(req.Items) > maxBatch {
		writeError(w, errInvalid("Move between 1 and %d files at once.", maxBatch))
		return
	}
	items := make([]api.FileMove, len(req.Items))
	var all []string
	for i, it := range req.Items {
		from, err := filePath(it.From, false)
		if err != nil {
			writeError(w, err)
			return
		}
		to, err := filePath(it.To, false)
		if err == nil {
			err = newName(to)
		}
		if err != nil {
			writeError(w, err)
			return
		}
		items[i] = api.FileMove{From: from, To: to}
		all = append(all, from, to)
	}
	d, err := s.openFiles()
	if err != nil {
		writeError(w, err)
		return
	}
	defer d.Close()
	release, err := s.holdFiles(r.Context(), d, all...)
	if err != nil {
		writeError(w, err)
		return
	}
	defer release()
	var moved []api.FileMove
	var failed error
	for _, it := range items {
		beforeChange(it.From)
		if err := d.Move(it.From, it.To); err != nil {
			failed = filesError(err, "Could not move "+quotePath(path.Base(it.From))+".", it.From)
			break
		}
		moved = append(moved, it)
	}
	switch {
	case len(moved) == 1 && path.Dir(moved[0].From) == path.Dir(moved[0].To):
		s.audit(actor, "files.renamed", moved[0].From, "succeeded", moved[0].From+" → "+moved[0].To)
	case len(moved) == 1:
		s.audit(actor, "files.moved", moved[0].From, "succeeded", moved[0].From+" → "+moved[0].To)
	case len(moved) > 1:
		froms := make([]string, len(moved))
		for i, m := range moved {
			froms[i] = m.From
		}
		s.audit(actor, "files.moved", path.Dir(moved[0].From), "succeeded", listed(froms)+" → "+path.Dir(moved[0].To))
	}
	if failed != nil {
		writeError(w, failed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"moved": len(moved)})
}

// hFileDelete deletes files and folders. A delete still going after
// deleteAnswerAfter carries on after the answer, which says so, and holds
// off operations until it is done.
func (s *server) hFileDelete(w http.ResponseWriter, r *http.Request) {
	var req api.FileDeleteRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(req.Paths) == 0 || len(req.Paths) > maxBatch {
		writeError(w, errInvalid("Delete between 1 and %d files at once.", maxBatch))
		return
	}
	paths := make([]string, len(req.Paths))
	for i, raw := range req.Paths {
		if paths[i], err = filePath(raw, false); err != nil {
			writeError(w, err)
			return
		}
	}
	d, err := s.openFiles()
	if err != nil {
		writeError(w, err)
		return
	}
	release, err := s.holdFiles(r.Context(), d, paths...)
	if err != nil {
		d.Close()
		writeError(w, err)
		return
	}
	var progress atomic.Int64
	done := make(chan error, 1)
	go func() {
		defer d.Close()
		defer release()
		done <- s.deletePaths(d, paths, actor, &progress)
	}()
	select {
	case err := <-done:
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, api.FileDeleteResult{Deleted: len(paths)})
	case <-time.After(deleteAnswerAfter):
		writeJSON(w, http.StatusAccepted, api.FileDeleteResult{Deleted: int(progress.Load()), Continuing: true})
	}
}

// deletePaths deletes paths in turn, counting each in progress, until one
// can't be, and audits what it deleted.
func (s *server) deletePaths(d *gamefiles.Dir, paths []string, actor string, progress *atomic.Int64) error {
	var deleted []string
	var failed error
	for _, p := range paths {
		beforeChange(p)
		err := d.Delete(p)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			failed = filesError(err, "Could not delete "+quotePath(path.Base(p))+".", p)
			break
		}
		deleted = append(deleted, p)
		progress.Add(1)
	}
	switch len(deleted) {
	case 0:
	case 1:
		s.audit(actor, "files.deleted", deleted[0], "succeeded", deleted[0])
	default:
		s.audit(actor, "files.deleted", path.Dir(deleted[0]), "succeeded", listed(deleted))
	}
	if failed != nil {
		s.log.Info("a delete in the server's files stopped", "server", s.id, "err", failed)
	}
	return failed
}

// stored are the kinds of file that are compressed already, which a
// download packs as they are.
var stored = map[string]bool{
	".jar": true, ".zip": true, ".gz": true, ".tgz": true, ".xz": true, ".zst": true, ".bz2": true, ".7z": true, ".rar": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".ogg": true, ".mp3": true, ".mrpack": true,
	".mca": true, ".mcr": true, ".mcc": true, ".dat": true, ".dat_old": true, ".nbt": true, ".schem": true, ".schematic": true, ".litematic": true,
}

// abortDownload ends a download that can't finish, so the browser shows it
// failed instead of keeping a file that looks whole.
func abortDownload() { panic(http.ErrAbortHandler) }

// hFileDownload sends a file as it is, or folders and several files as one
// zip: each ?path= names one, and several must share a folder. The zip holds
// the regular files and folders in them; links and special files stay out.
// While the game runs, a file it writes can change as it is read. With
// ?check=1 it answers 204 for a download that would start, or the error it
// would give, which a link in the browser can't show.
func (s *server) hFileDownload(w http.ResponseWriter, r *http.Request) {
	actor := actorFromHeader(r)
	if actor == "unknown" {
		writeError(w, errInvalid("X-Playkeeper-Actor header is required"))
		return
	}
	check := r.URL.Query().Get("check") == "1"
	raw := r.URL.Query()["path"]
	if len(raw) == 0 || len(raw) > maxBatch {
		writeError(w, errInvalid("Download between 1 and %d files at once.", maxBatch))
		return
	}
	paths := make([]string, len(raw))
	for i, rp := range raw {
		p, err := filePath(rp, true)
		if err == nil && p == "." && len(raw) > 1 {
			err = errInvalid("The server's folder is downloaded on its own.")
		}
		if err == nil && i > 0 && path.Dir(p) != path.Dir(paths[0]) {
			err = errInvalid("Files downloaded together must be in the same folder.")
		}
		if err != nil {
			writeError(w, err)
			return
		}
		paths[i] = p
	}
	d, err := s.openFiles()
	if err != nil {
		writeError(w, err)
		return
	}
	defer d.Close()
	const couldNot = "The download could not start."
	if p := paths[0]; len(paths) == 1 && p != "." {
		fi, err := d.Lstat(p)
		if err != nil {
			writeError(w, filesError(err, couldNot, p))
			return
		}
		if !fi.IsDir() {
			if check {
				s.checkFile(w, d, p)
				return
			}
			s.sendFile(w, d, p, actor)
			return
		}
	}
	// Entries are named from the folder the paths are in, so a folder's zip
	// unpacks into a folder of its name.
	base := path.Dir(paths[0])
	target := paths[0]
	if len(paths) > 1 {
		target = base
	}
	for _, p := range paths {
		if p == "." {
			continue
		}
		if _, err := d.Lstat(p); err != nil {
			writeError(w, filesError(err, couldNot, p))
			return
		}
	}
	total := 0
	for _, p := range paths {
		n, err := d.Count(r.Context(), p, maxZipped-total)
		if err != nil {
			writeError(w, filesError(err, couldNot, p))
			return
		}
		if total += n; total > maxZipped {
			writeError(w, tooManyToZip(target, len(paths) > 1))
			return
		}
	}
	if check {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) { return flate.NewWriter(out, flate.BestSpeed) })
	zipped := 0
	for _, p := range paths {
		err := d.Walk(r.Context(), p, maxZipped, func(fp string, e gamefiles.Entry) error {
			rel := fp
			switch {
			case fp == ".":
				rel = ""
			case base != ".":
				rel = strings.TrimPrefix(fp, base+"/")
			}
			if rel == "" || !e.Mode.IsDir() && !e.Mode.IsRegular() {
				return nil
			}
			// Files the game made since the count can't take the zip past it.
			if zipped++; zipped > maxZipped {
				return errors.New("more files than the zip was counted for")
			}
			if e.Mode.IsDir() {
				_, err := zw.CreateHeader(&zip.FileHeader{Name: zipName(rel) + "/", Modified: e.ModTime, Method: zip.Store})
				return err
			}
			return zipFile(zw, d, fp, rel)
		})
		if err != nil {
			s.log.Warn("a download of the server's files stopped", "server", s.id, "path", p, "err", err)
			abortDownload()
		}
	}
	if err := zw.Close(); err != nil {
		abortDownload()
	}
	s.audit(actor, "files.downloaded", target, "succeeded", listed(paths))
}

// tooManyToZip refuses a download of p, or of several files in it, that
// would pack more than maxZipped files and folders.
func tooManyToZip(p string, several bool) error {
	what := quotePath(p) + " holds"
	switch {
	case several:
		what = "The files you chose hold"
	case p == ".":
		what = "The server's folder holds"
	}
	return &apiError{Status: http.StatusConflict, Code: string(gamefiles.KindTooMany), Params: map[string]any{"path": shown(p), "limit": strconv.Itoa(maxZipped)},
		Msg:  what + " more than " + thousands(maxZipped) + " files and folders, too many for one download.",
		Hint: "Download a smaller folder, or make a backup to keep a copy of the whole server."}
}

// thousands writes n with a comma between each three digits.
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// checkFile answers a check of a file's download: 204 if it would start.
func (s *server) checkFile(w http.ResponseWriter, d *gamefiles.Dir, p string) {
	f, _, err := d.OpenFile(p)
	if err != nil {
		writeError(w, filesError(err, "The download could not start.", p))
		return
	}
	f.Close()
	w.WriteHeader(http.StatusNoContent)
}

// sendFile sends one regular file as it was when opened.
func (s *server) sendFile(w http.ResponseWriter, d *gamefiles.Dir, p, actor string) {
	f, st, err := d.OpenFile(p)
	if err != nil {
		writeError(w, filesError(err, "The download could not start.", p))
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if err := copyWhole(w, f, st.Size(), p); err != nil {
		abortDownload()
	}
	s.audit(actor, "files.downloaded", p, "succeeded", p)
}

// copyWhole copies the size bytes p had when it was opened from r to w, and
// fails when fewer come, as from a file the game made shorter meanwhile.
func copyWhole(w io.Writer, r io.Reader, size int64, p string) error {
	n, err := io.Copy(w, io.LimitReader(r, size))
	if err == nil && n != size {
		err = fmt.Errorf("%s got shorter while it was read", quotePath(p))
	}
	return err
}

// zipFile adds the regular file p to a download as rel, as it was when
// opened. A file the game swapped for a link or deleted meanwhile stays out;
// one it made shorter while it was read stops the download, which would
// otherwise hold it cut short.
func zipFile(zw *zip.Writer, d *gamefiles.Dir, p, rel string) error {
	f, st, err := d.OpenFile(p)
	if gamefiles.KindOf(err) != "" || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	method := zip.Deflate
	if stored[strings.ToLower(path.Ext(rel))] {
		method = zip.Store
	}
	out, err := zw.CreateHeader(&zip.FileHeader{Name: zipName(rel), Modified: st.ModTime(), Method: method})
	if err != nil {
		return err
	}
	return copyWhole(out, f, st.Size(), p)
}

// reDevice matches the names Windows keeps for devices, whatever follows
// the first dot.
var reDevice = regexp.MustCompile(`(?i)^(con|prn|aux|nul|conin\$|conout\$|com[0-9¹²³]|lpt[0-9¹²³]) *$`)

// zipName is a path in the server's files as a zip names it, safe to unpack
// on any computer. The game can name files as Linux allows, but some
// unpackers take a backslash for a folder and a colon for a drive, which
// could write outside the folder unpacked into. Those, the other
// characters Windows refuses in a name, and a name's trailing dots and
// spaces become _, and a device's name such as NUL gets one in front.
func zipName(rel string) string {
	parts := strings.Split(rel, "/")
	for i, name := range parts {
		b := []rune(name)
		for j, c := range b {
			if c < 0x20 || c == 0x7f || strings.ContainsRune(`\:*?"<>|`, c) {
				b[j] = '_'
			}
		}
		for j := len(b) - 1; j >= 0 && (b[j] == '.' || b[j] == ' '); j-- {
			b[j] = '_'
		}
		name = string(b)
		if stem, _, _ := strings.Cut(name, "."); reDevice.MatchString(stem) {
			name = "_" + name
		}
		parts[i] = name
	}
	return strings.Join(parts, "/")
}
