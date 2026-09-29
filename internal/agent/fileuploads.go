package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// Uploads into a server's folder carry on after a dropped connection, as
// world uploads do: each file is announced, its bytes arrive in pieces from
// the byte the machine says it has and are kept in the staging folder, and
// the file is put in place once all of them have. An upload lives in memory
// and in the staging folder, which the agent clears when it starts.

const (
	// maxFileUploads is how many unfinished uploads into servers' folders a
	// machine keeps open, and maxServerUploads how many of them one server
	// does, so that the admins of one server can't take them all.
	maxFileUploads   = 8
	maxServerUploads = 4
	// maxUploadFiles caps the files one upload announces.
	maxUploadFiles = 1000
	// fileUploadIdle is how long an upload nobody touches is kept.
	fileUploadIdle = time.Hour
)

// fileUploads holds the uploads into servers' folders. Announcing a file
// takes the world imports' announce lock, so that uploads of both kinds see
// the space the others left.
type fileUploads struct {
	mu   sync.Mutex
	byID map[string]*fileUpload
}

// fileUpload is one upload into a folder. The lock order is the registry's,
// then the upload's.
type fileUpload struct {
	id, serverID, folder string
	createdAt            time.Time
	dir                  string

	mu      sync.Mutex
	files   []*uploadFile
	touched time.Time
	gone    bool
	// writing is set while a request writes bytes; stop ends that request,
	// for one that takes over.
	writing bool
	stop    func()
}

type uploadFile struct {
	name     string
	size     int64
	received int64
	replace  bool
	// placing is set while a request puts the file in place, so that
	// another skips it rather than try too.
	placing bool
	placed  bool
	err     string
}

func (up *fileUpload) stagedPath(n int) string {
	return filepath.Join(up.dir, strconv.Itoa(n)+".bin")
}

// unfinished reports whether the upload has a file to put in place, or none
// announced yet; a finished one doesn't count against the limits.
func (up *fileUpload) unfinished() bool {
	up.mu.Lock()
	defer up.mu.Unlock()
	for _, f := range up.files {
		if !f.placed {
			return true
		}
	}
	return len(up.files) == 0
}

var errUploadGone = &apiError{Status: http.StatusNotFound, Code: api.CodeNotFound, Msg: "This upload isn't here anymore.", Hint: "Choose the files again."}

// claim lets one request at a time write an upload's bytes. A request takes
// over from an earlier one still waiting for bytes, such as one whose
// connection dropped.
func (up *fileUpload) claim(now time.Time) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		up.mu.Lock()
		if up.gone {
			up.mu.Unlock()
			return errUploadGone
		}
		if !up.writing {
			up.writing, up.touched = true, now
			up.mu.Unlock()
			return nil
		}
		if up.stop != nil {
			up.stop()
		}
		up.mu.Unlock()
		if time.Now().After(deadline) {
			return &apiError{Status: http.StatusConflict, Code: api.CodeBusy, Msg: "The upload is still sending another piece.", Hint: "Try again in a moment."}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (up *fileUpload) release() {
	up.mu.Lock()
	up.writing, up.stop = false, nil
	up.mu.Unlock()
}

// fileUploadsUnsent is how many bytes open uploads announced and haven't
// sent yet.
func (a *Agent) fileUploadsUnsent() int64 {
	a.uploads.mu.Lock()
	list := make([]*fileUpload, 0, len(a.uploads.byID))
	for _, up := range a.uploads.byID {
		list = append(list, up)
	}
	a.uploads.mu.Unlock()
	var unsent int64
	for _, up := range list {
		up.mu.Lock()
		for _, f := range up.files {
			unsent += f.size - f.received
		}
		up.mu.Unlock()
	}
	return unsent
}

// staleFileUploads forgets the uploads nobody touched for an hour, other than
// keep, and returns them for their files to be deleted. The caller holds the
// registry's lock.
func (a *Agent) staleFileUploads(now time.Time, keep *fileUpload) []*fileUpload {
	var stale []*fileUpload
	for id, up := range a.uploads.byID {
		if up == keep {
			continue
		}
		up.mu.Lock()
		if !up.writing && now.Sub(up.touched) > fileUploadIdle {
			up.gone = true
			delete(a.uploads.byID, id)
			stale = append(stale, up)
		}
		up.mu.Unlock()
	}
	return stale
}

func removeFileUploads(list []*fileUpload) {
	for _, up := range list {
		os.RemoveAll(up.dir)
	}
}

// dropFileUpload forgets an upload and deletes what it kept.
func (a *Agent) dropFileUpload(up *fileUpload) {
	a.uploads.mu.Lock()
	if a.uploads.byID[up.id] == up {
		delete(a.uploads.byID, up.id)
	}
	a.uploads.mu.Unlock()
	up.mu.Lock()
	up.gone = true
	if up.stop != nil {
		up.stop()
	}
	up.mu.Unlock()
	os.RemoveAll(up.dir)
}

// fileUpload finds this server's upload id.
func (s *server) fileUpload(id string) (*fileUpload, error) {
	if !reStageID.MatchString(id) {
		return nil, errInvalid("invalid upload id")
	}
	s.uploads.mu.Lock()
	up := s.uploads.byID[id]
	s.uploads.mu.Unlock()
	if up == nil || up.serverID != s.id {
		return nil, errUploadGone
	}
	return up, nil
}

func (a *Agent) fileUploadView(up *fileUpload) api.FileUpload {
	allowance := a.uploadAllowance()
	up.mu.Lock()
	defer up.mu.Unlock()
	v := api.FileUpload{ID: up.id, Folder: shown(up.folder), CreatedAt: up.createdAt, Files: []api.FileUploadFile{}, LimitBytes: allowance}
	for i, f := range up.files {
		v.Files = append(v.Files, api.FileUploadFile{Index: i, Name: f.name, Size: f.size, Received: f.received, Placed: f.placed, Error: f.err})
	}
	return v
}

// hFileUploadNew opens an upload into one of the server's folders.
func (s *server) hFileUploadNew(w http.ResponseWriter, r *http.Request) {
	var req api.FileUploadRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if _, err := validActor(req.Actor); err != nil {
		writeError(w, err)
		return
	}
	folder, err := filePath(req.Folder, true)
	if err != nil {
		writeError(w, err)
		return
	}
	now := s.now()
	s.uploads.mu.Lock()
	if s.uploads.byID == nil {
		s.uploads.byID = map[string]*fileUpload{}
	}
	stale := s.staleFileUploads(now, nil)
	all, mine := 0, 0
	for _, up := range s.uploads.byID {
		if up.unfinished() {
			all++
			if up.serverID == s.id {
				mine++
			}
		}
	}
	var up *fileUpload
	if all < maxFileUploads && mine < maxServerUploads {
		id := randomSecret(8)
		up = &fileUpload{id: id, serverID: s.id, folder: folder, createdAt: now.UTC(), dir: filepath.Join(s.cfg.StagingDir(), "files-"+id), touched: now}
		s.uploads.byID[id] = up
	}
	s.uploads.mu.Unlock()
	removeFileUploads(stale)
	switch {
	case all >= maxFileUploads:
		writeError(w, errConflict("Too many uploads are open on this machine.", "Wait for one to finish, or cancel it, then try again."))
		return
	case up == nil:
		writeError(w, errConflict(s.name()+" has too many uploads open.", "Wait for one to finish, or cancel it, then try again."))
		return
	}
	if err := os.MkdirAll(up.dir, 0o700); err != nil {
		s.dropFileUpload(up)
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.fileUploadView(up))
}

func (s *server) hFileUpload(w http.ResponseWriter, r *http.Request) {
	up, err := s.fileUpload(r.PathValue("up"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.fileUploadView(up))
}

func (s *server) hFileUploadDelete(w http.ResponseWriter, r *http.Request) {
	if _, err := validActor(r.URL.Query().Get("actor")); err != nil {
		writeError(w, err)
		return
	}
	up, err := s.fileUpload(r.PathValue("up"))
	if err != nil {
		writeError(w, err)
		return
	}
	s.dropFileUpload(up)
	w.WriteHeader(http.StatusNoContent)
}

// hFileUploadFile announces a file, its name in the upload's folder: a
// folder dropped into the browser names the folders it makes. A file of the
// same name is replaced only when the request says so.
func (s *server) hFileUploadFile(w http.ResponseWriter, r *http.Request) {
	var req api.FileUploadFileRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	up, err := s.fileUpload(r.PathValue("up"))
	if err != nil {
		writeError(w, err)
		return
	}
	name, err := filePath(req.Name, false)
	if err == nil && strings.IndexFunc(name, unicode.IsControl) >= 0 {
		err = errInvalid("%s has a control character in its name.", quotePath(name))
	}
	if err == nil && req.Size < 0 {
		err = errInvalid("%s has no size.", quotePath(name))
	}
	var dest string
	if err == nil {
		dest, err = filePath(join(up.folder, name), false)
	}
	if err == nil {
		err = s.uploadRefusal(r.Context(), dest, req.Replace)
	}
	n := 0
	if err == nil {
		n, err = s.announceUpload(r.Context(), up, name, req.Size, req.Replace)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if req.Size == 0 {
		s.placeUpload(r.Context(), up, n, actor)
	}
	writeJSON(w, http.StatusCreated, s.fileUploadView(up))
}

// join is folder/name, or name alone in the server's folder.
func join(folder, name string) string {
	if folder == "." {
		return name
	}
	return folder + "/" + name
}

// uploadRefusal is why a file can't be uploaded to dest, said before its
// bytes are sent: what changeRefusal says, or something at dest the upload
// may not replace, anything but a regular file or one it wasn't asked to.
func (s *server) uploadRefusal(ctx context.Context, dest string, replace bool) error {
	d, err := s.openFiles()
	if err != nil {
		return err
	}
	defer d.Close()
	if err := s.changeRefusal(ctx, d, dest); err != nil {
		return err
	}
	fi, err := d.Lstat(dest)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return filesError(err, "The file can't be uploaded there.", dest)
	case !fi.Mode().IsRegular():
		return errConflict(quotePath(dest)+" is a folder, a link or a special file, so an upload can't replace it.", "Move it out of the way first.")
	case !replace:
		return existsAlready(dest)
	}
	return nil
}

// announceUpload adds a file to an upload if the space open uploads may use
// has room for it, and returns its index.
func (s *server) announceUpload(ctx context.Context, up *fileUpload, name string, size int64, replace bool) (int, error) {
	s.imports.announce.Lock()
	defer s.imports.announce.Unlock()
	s.uploads.mu.Lock()
	stale := s.staleFileUploads(s.now(), up)
	s.uploads.mu.Unlock()
	removeFileUploads(stale)
	if size > s.uploadAllowance() {
		return 0, &apiError{Status: http.StatusRequestEntityTooLarge, Code: api.CodeInsufficientSpace, Msg: quotePath(name) + " is larger than the free disk space allows.", Hint: "Free disk space and try again."}
	}
	if err := s.diskLimitRefusal(ctx, s.id, size); err != nil {
		return 0, err
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	switch {
	case up.gone:
		return 0, errUploadGone
	case len(up.files) >= maxUploadFiles:
		return 0, errConflict(fmt.Sprintf("One upload takes at most %d files.", maxUploadFiles), "Upload the rest in another one.")
	}
	for _, f := range up.files {
		if f.name == name {
			return 0, errConflict(quotePath(name)+" is in this upload already.", "")
		}
	}
	n := len(up.files)
	f, err := os.OpenFile(up.stagedPath(n), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	f.Close()
	up.files = append(up.files, &uploadFile{name: name, size: size, replace: replace})
	up.touched = s.now()
	return n, nil
}

// placeUpload puts file n of an upload in place once all of it has arrived.
// What stops it now, such as a backup running or the game using the world,
// is kept as the file's error, and its bytes stay for another try. Once the
// upload is cancelled nothing more of it goes in place, even a file whose
// last bytes a request was still sending.
func (s *server) placeUpload(ctx context.Context, up *fileUpload, n int, actor string) {
	up.mu.Lock()
	f := up.files[n]
	name, size, replace, skip := f.name, f.size, f.replace, up.gone || f.placed || f.placing || f.received < f.size
	f.placing = !skip
	up.mu.Unlock()
	if skip {
		return
	}
	dest := join(up.folder, name)
	err := s.place(ctx, dest, up.stagedPath(n), replace)
	if err == nil {
		// Counted as written before it stops counting as on its way.
		s.noteDiskWrite(s.id, size)
	}
	up.mu.Lock()
	f.placing, f.placed, f.err = false, err == nil, ""
	if err != nil {
		f.err = filesError(err, "It could not be put in place.", dest).Error()
	}
	up.touched = s.now()
	up.mu.Unlock()
	if err != nil {
		s.log.Info("an uploaded file could not be put in place", "server", s.id, "path", dest, "err", err)
		return
	}
	s.audit(actor, "files.uploaded", dest, "succeeded", dest)
}

func (s *server) place(ctx context.Context, dest, staged string, replace bool) error {
	d, err := s.openFiles()
	if err != nil {
		return err
	}
	defer d.Close()
	release, err := s.holdFiles(ctx, d, dest)
	if err != nil {
		return err
	}
	defer release()
	beforeChange(dest)
	return d.Place(dest, staged, 0o640, replace)
}

// hFileUploadPut receives the bytes of an announced file from ?offset=,
// which must be where the last request stopped. What arrives is kept even if
// the connection drops, so the next request carries on from there. Once the
// file is whole it is put in place; a request from its last byte, with no
// bytes, tries that again after it failed.
func (s *server) hFileUploadPut(w http.ResponseWriter, r *http.Request) {
	actor := actorFromHeader(r)
	if actor == "unknown" {
		writeError(w, errInvalid("X-Playkeeper-Actor header is required"))
		return
	}
	up, err := s.fileUpload(r.PathValue("up"))
	if err != nil {
		writeError(w, err)
		return
	}
	n, nerr := strconv.Atoi(r.PathValue("n"))
	offset, oerr := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if nerr != nil || oerr != nil || n < 0 || offset < 0 {
		writeError(w, errInvalid("Say which file and from which byte."))
		return
	}
	if err := up.claim(s.now()); err != nil {
		writeError(w, err)
		return
	}
	defer up.release()
	up.mu.Lock()
	if n >= len(up.files) {
		up.mu.Unlock()
		writeError(w, errNotFound("File"))
		return
	}
	f := up.files[n]
	if offset != f.received {
		got := f.received
		up.mu.Unlock()
		writeError(w, errConflict(fmt.Sprintf("The upload carries on from byte %d.", got), "Send the rest from there."))
		return
	}
	if f.received == f.size {
		up.mu.Unlock()
		s.placeUpload(r.Context(), up, n, actor)
		writeJSON(w, http.StatusOK, s.fileUploadView(up))
		return
	}
	rc := http.NewResponseController(w)
	stopped := &atomic.Bool{}
	up.stop = func() {
		stopped.Store(true)
		_ = rc.SetReadDeadline(time.Now())
	}
	received, size, name := f.received, f.size, f.name
	up.mu.Unlock()

	counted := &countingWriter{w: io.Discard}
	var extra bool
	out, err := os.OpenFile(up.stagedPath(n), os.O_WRONLY, 0)
	if err == nil {
		if err = out.Truncate(received); err == nil {
			_, err = out.Seek(received, io.SeekStart)
		}
		if err == nil {
			body := &idleReader{r: r.Body, rc: rc, idle: uploadIdle, stopped: stopped}
			_, err = io.Copy(io.MultiWriter(out, counted), io.LimitReader(body, size-received))
			if err == nil && received+counted.n == size {
				var one [1]byte
				k, _ := body.Read(one[:])
				extra = k > 0
			}
		}
		if terr := out.Truncate(received + counted.n); err == nil {
			err = terr
		}
		out.Close()
	}

	up.mu.Lock()
	up.stop = nil
	f.received = received + counted.n
	if extra {
		f.received = 0
		_ = os.Truncate(up.stagedPath(n), 0)
	}
	done := f.received == size
	up.touched = s.now()
	up.mu.Unlock()

	switch {
	case extra:
		writeError(w, errInvalid("%s is larger than announced.", quotePath(name)))
		return
	case done:
		s.placeUpload(r.Context(), up, n, actor)
	case errors.Is(err, syscall.ENOSPC):
		writeError(w, &apiError{Status: http.StatusInsufficientStorage, Code: api.CodeInsufficientSpace, Msg: "The disk filled up during the upload.", Hint: "Free disk space, then carry on with the upload."})
		return
	case err != nil:
		writeError(w, &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Msg: fmt.Sprintf("The upload stopped at %s of %s.", humanBytes(received+counted.n), humanBytes(size)), Hint: "It carries on from there."})
		return
	}
	writeJSON(w, http.StatusOK, s.fileUploadView(up))
}
