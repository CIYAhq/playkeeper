package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/worldimport"
)

func init() {
	opLabels["world_import"] = "importing a world"
}

const (
	// maxWorldImports is how many uploads one machine keeps open.
	maxWorldImports = 4
	// maxImportFiles matches the archives worldimport combines in one import.
	maxImportFiles = 16
	// worldImportIdle is how long an upload nobody touches is kept.
	worldImportIdle = 24 * time.Hour
	// uploadIdle is how long an upload waits for its next bytes before it
	// gives up, so a dropped connection frees the file for the next try.
	uploadIdle = time.Minute
)

// importRegistry holds the world uploads in progress. They live in memory and
// in the staging folder, which the agent clears when it starts, so an upload
// doesn't survive an agent restart: the owner uploads again, as for restores.
type importRegistry struct {
	mu   sync.Mutex
	byID map[string]*worldImport
	// announce makes announcing files take turns, so each announce sees the
	// allowance the others left. It is taken before mu and the imports'
	// locks, never while holding one.
	announce sync.Mutex
}

// worldImport is one upload of world archives, for a new server or to replace
// a server's world. The lock order is the registry's, then the import's.
type worldImport struct {
	id        string
	serverID  string
	createdAt time.Time
	dir       string

	mu         sync.Mutex
	files      []*importFile
	inspection *worldimport.Inspection
	// busy is what the import is doing: "uploading", "inspecting", or
	// "applying" and "creating" while an operation uses it.
	busy    string
	touched time.Time
	gone    bool
}

type importFile struct {
	name     string
	size     int64
	received int64
	hash     hash.Hash
	sum      string
	// stop ends the request that writes the file, for one taking over.
	stop func()
}

func (imp *worldImport) uploadPath(n int) string {
	return filepath.Join(imp.dir, "uploads", strconv.Itoa(n)+".bin")
}

func (imp *worldImport) stagedDir() string { return filepath.Join(imp.dir, "data") }

var errImportGone = &apiError{Status: http.StatusNotFound, Code: api.CodeNotFound, Msg: "This upload isn't here anymore.", Hint: "Upload the world again."}

// claim marks the import busy with what, or refuses while it does something
// else. An upload takes over from an earlier one of the same import that is
// still waiting for bytes, such as one whose connection dropped.
func (imp *worldImport) claim(what string, now time.Time) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		imp.mu.Lock()
		if imp.gone {
			imp.mu.Unlock()
			return errImportGone
		}
		if imp.busy == "" {
			imp.busy, imp.touched = what, now
			imp.mu.Unlock()
			return nil
		}
		busy := imp.busy
		takeOver := busy == "uploading" && what == "uploading"
		if takeOver {
			for _, f := range imp.files {
				if f.stop != nil {
					f.stop()
				}
			}
		}
		imp.mu.Unlock()
		if !takeOver || time.Now().After(deadline) {
			return importBusy(busy)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (imp *worldImport) release() {
	imp.mu.Lock()
	imp.busy = ""
	imp.mu.Unlock()
}

func importBusy(busy string) error {
	msg := "The world is being imported."
	switch busy {
	case "uploading":
		msg = "The world is still uploading."
	case "inspecting":
		msg = "Playkeeper is checking the world."
	}
	return &apiError{Status: http.StatusConflict, Code: api.CodeBusy, Msg: msg, Hint: "Wait for it to finish, then try again."}
}

// newWorldImport opens an upload, first forgetting uploads nobody touched for
// a day.
func (a *Agent) newWorldImport(serverID string) (*worldImport, error) {
	now := a.now()
	var stale []*worldImport
	a.imports.mu.Lock()
	if a.imports.byID == nil {
		a.imports.byID = map[string]*worldImport{}
	}
	for id, imp := range a.imports.byID {
		imp.mu.Lock()
		if imp.busy == "" && now.Sub(imp.touched) > worldImportIdle {
			imp.gone = true
			delete(a.imports.byID, id)
			stale = append(stale, imp)
		}
		imp.mu.Unlock()
	}
	full := len(a.imports.byID) >= maxWorldImports
	var imp *worldImport
	if !full {
		id := randomSecret(8)
		imp = &worldImport{id: id, serverID: serverID, createdAt: now.UTC(), dir: filepath.Join(a.cfg.StagingDir(), "import-"+id), touched: now}
		a.imports.byID[id] = imp
	}
	a.imports.mu.Unlock()
	for _, s := range stale {
		os.RemoveAll(s.dir)
	}
	if full {
		return nil, errConflict("Too many world uploads are open on this machine.", "Finish or cancel one, then try again.")
	}
	if err := os.MkdirAll(filepath.Join(imp.dir, "uploads"), 0o700); err != nil {
		a.dropImport(imp)
		return nil, err
	}
	return imp, nil
}

func (a *Agent) worldImport(id string) (*worldImport, error) {
	if !reStageID.MatchString(id) {
		return nil, errInvalid("invalid upload id")
	}
	a.imports.mu.Lock()
	imp := a.imports.byID[id]
	a.imports.mu.Unlock()
	if imp == nil {
		return nil, errImportGone
	}
	return imp, nil
}

// dropImport forgets an upload and deletes its files.
func (a *Agent) dropImport(imp *worldImport) {
	a.imports.mu.Lock()
	if a.imports.byID[imp.id] == imp {
		delete(a.imports.byID, imp.id)
	}
	a.imports.mu.Unlock()
	imp.mu.Lock()
	imp.gone = true
	for _, f := range imp.files {
		if f.stop != nil {
			f.stop()
		}
	}
	imp.mu.Unlock()
	os.RemoveAll(imp.dir)
}

// uploadAllowance is how many more bytes uploads may announce: the upload
// limit restores use, less what open uploads announced and haven't sent yet.
func (a *Agent) uploadAllowance() int64 {
	a.imports.mu.Lock()
	list := make([]*worldImport, 0, len(a.imports.byID))
	for _, imp := range a.imports.byID {
		list = append(list, imp)
	}
	a.imports.mu.Unlock()
	var unsent int64
	for _, imp := range list {
		imp.mu.Lock()
		for _, f := range imp.files {
			unsent += f.size - f.received
		}
		imp.mu.Unlock()
	}
	return max(a.uploadLimit()-unsent, 0)
}

func (a *Agent) importView(imp *worldImport) api.WorldImport {
	allowance := a.uploadAllowance()
	imp.mu.Lock()
	defer imp.mu.Unlock()
	v := api.WorldImport{ID: imp.id, ServerID: imp.serverID, CreatedAt: imp.createdAt, Files: []api.WorldImportFile{}, LimitBytes: allowance, Inspection: imp.inspection}
	for i, f := range imp.files {
		v.Files = append(v.Files, api.WorldImportFile{Index: i, Name: f.name, Size: f.size, Received: f.received, SHA256: f.sum})
	}
	return v
}

// importErr turns a refusal of the worldimport package into a response.
func importErr(err error) error {
	var we *worldimport.Error
	if errors.As(err, &we) {
		return &apiError{Status: http.StatusUnprocessableEntity, Code: api.CodeInvalid, Msg: we.Msg, Hint: we.Hint}
	}
	return err
}

// blocked is the refusal for a preview with problems.
func blocked(p *worldimport.Preview) error {
	msgs := make([]string, 0, len(p.Problems))
	hint := ""
	for _, m := range p.Problems {
		msgs = append(msgs, m.Text)
		if hint == "" {
			hint = m.Hint
		}
	}
	return errConflict("This world can't be imported: "+strings.Join(msgs, " "), hint)
}

// hWorldImportNew opens an upload that replaces this server's world.
func (s *server) hWorldImportNew(w http.ResponseWriter, r *http.Request) {
	s.openWorldImport(w, r, s.id)
}

// hWorldImportNewServer opens an upload for a new server.
func (a *Agent) hWorldImportNewServer(w http.ResponseWriter, r *http.Request) {
	a.openWorldImport(w, r, "")
}

func (a *Agent) openWorldImport(w http.ResponseWriter, r *http.Request, serverID string) {
	if _, err := actionActor(r); err != nil {
		writeError(w, err)
		return
	}
	imp, err := a.newWorldImport(serverID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a.importView(imp))
}

func (a *Agent) hWorldImport(w http.ResponseWriter, r *http.Request) {
	imp, err := a.worldImport(r.PathValue("imp"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.importView(imp))
}

// hWorldImportFile announces an archive; its bytes follow with PUT.
func (a *Agent) hWorldImportFile(w http.ResponseWriter, r *http.Request) {
	var req api.WorldImportFileRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if _, err := validActor(req.Actor); err != nil {
		writeError(w, err)
		return
	}
	imp, err := a.worldImport(r.PathValue("imp"))
	if err != nil {
		writeError(w, err)
		return
	}
	if err := worldimport.CheckUploadName(req.Name); err != nil {
		writeError(w, importErr(err))
		return
	}
	if req.Size <= 0 {
		writeError(w, errInvalid("%s is empty.", strconv.Quote(req.Name)))
		return
	}
	if err := a.announceFile(imp, req.Name, req.Size); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a.importView(imp))
}

// announceFile adds a file to an upload if the upload allowance has room
// for it.
func (a *Agent) announceFile(imp *worldImport, name string, size int64) error {
	a.imports.announce.Lock()
	defer a.imports.announce.Unlock()
	if size > a.uploadAllowance() {
		return &apiError{Status: http.StatusRequestEntityTooLarge, Code: api.CodeInsufficientSpace, Msg: "The world is larger than the free disk space allows.", Hint: "Free disk space and try again."}
	}
	imp.mu.Lock()
	defer imp.mu.Unlock()
	switch {
	case imp.gone:
		return errImportGone
	case imp.busy != "" && imp.busy != "uploading":
		return importBusy(imp.busy)
	case len(imp.files) >= maxImportFiles:
		return errConflict(fmt.Sprintf("One import can combine at most %d archives.", maxImportFiles), "Pack the world into one .zip file and upload that.")
	}
	f, err := os.OpenFile(imp.uploadPath(len(imp.files)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	f.Close()
	imp.files = append(imp.files, &importFile{name: name, size: size, hash: sha256.New()})
	imp.inspection = nil
	imp.touched = a.now()
	return nil
}

// idleReader reads a request body until it sends nothing for idle, or the
// upload is stopped for another request that takes over.
type idleReader struct {
	r       io.Reader
	rc      *http.ResponseController
	idle    time.Duration
	stopped *atomic.Bool
}

var errUploadStopped = errors.New("another request took over the upload")

func (ir *idleReader) Read(p []byte) (int, error) {
	if ir.stopped.Load() {
		return 0, errUploadStopped
	}
	_ = ir.rc.SetReadDeadline(time.Now().Add(ir.idle))
	return ir.r.Read(p)
}

// countingWriter counts what it passes on.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// hWorldImportUpload receives the bytes of an announced archive from ?offset=,
// which must be where the last request stopped. What arrives is kept even if
// the connection drops, so the next request carries on from there.
func (a *Agent) hWorldImportUpload(w http.ResponseWriter, r *http.Request) {
	actor := actorFromHeader(r)
	if actor == "unknown" {
		writeError(w, errInvalid("X-Playkeeper-Actor header is required"))
		return
	}
	imp, err := a.worldImport(r.PathValue("imp"))
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
	if err := imp.claim("uploading", a.now()); err != nil {
		writeError(w, err)
		return
	}
	defer imp.release()
	imp.mu.Lock()
	if n >= len(imp.files) {
		imp.mu.Unlock()
		writeError(w, errNotFound("File"))
		return
	}
	f := imp.files[n]
	if offset != f.received {
		got := f.received
		imp.mu.Unlock()
		writeError(w, errConflict(fmt.Sprintf("The upload carries on from byte %d.", got), "Send the rest from there."))
		return
	}
	if f.received == f.size {
		imp.mu.Unlock()
		writeJSON(w, http.StatusOK, a.importView(imp))
		return
	}
	rc := http.NewResponseController(w)
	stopped := &atomic.Bool{}
	f.stop = func() {
		stopped.Store(true)
		_ = rc.SetReadDeadline(time.Now())
	}
	received, size, name, h := f.received, f.size, f.name, f.hash
	imp.mu.Unlock()

	counted := &countingWriter{w: h}
	var extra bool
	out, err := os.OpenFile(imp.uploadPath(n), os.O_WRONLY, 0)
	if err == nil {
		if err = out.Truncate(received); err == nil {
			_, err = out.Seek(received, io.SeekStart)
		}
		if err == nil {
			body := &idleReader{r: r.Body, rc: rc, idle: uploadIdle, stopped: stopped}
			// The hash sees a chunk only once the file took all of it, so
			// the count is what the file holds for sure.
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

	imp.mu.Lock()
	f.stop = nil
	f.received = received + counted.n
	if extra {
		f.received, f.hash, f.sum = 0, sha256.New(), ""
		_ = os.Truncate(imp.uploadPath(n), 0)
	}
	done := f.received == size && f.sum == ""
	if done {
		f.sum = hex.EncodeToString(f.hash.Sum(nil))
	}
	sum := f.sum
	imp.touched = a.now()
	imp.mu.Unlock()

	switch {
	case extra:
		writeError(w, errInvalid("%s is larger than announced.", strconv.Quote(name)))
		return
	case done:
		a.auditFor(imp.serverID, actor, "world_import.uploaded", imp.id, "received", fmt.Sprintf("%s, %s, sha256 %s", name, humanBytes(size), sum))
	case errors.Is(err, syscall.ENOSPC):
		writeError(w, &apiError{Status: http.StatusInsufficientStorage, Code: api.CodeInsufficientSpace, Msg: "The disk filled up during the upload.", Hint: "Free disk space, then carry on with the upload."})
		return
	case err != nil:
		writeError(w, &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Msg: fmt.Sprintf("The upload stopped at %s of %s.", humanBytes(received+counted.n), humanBytes(size)), Hint: "It carries on from there."})
		return
	}
	writeJSON(w, http.StatusOK, a.importView(imp))
}

// hWorldImportInspect reads the uploaded archives and lists the worlds in them.
func (a *Agent) hWorldImportInspect(w http.ResponseWriter, r *http.Request) {
	if _, err := actionActor(r); err != nil {
		writeError(w, err)
		return
	}
	imp, err := a.worldImport(r.PathValue("imp"))
	if err != nil {
		writeError(w, err)
		return
	}
	if err := imp.claim("inspecting", a.now()); err != nil {
		writeError(w, err)
		return
	}
	defer imp.release()
	imp.mu.Lock()
	var sources []worldimport.Source
	for i, f := range imp.files {
		if f.sum == "" {
			err = errConflict(fmt.Sprintf("%s hasn't finished uploading.", strconv.Quote(f.name)), "Wait for the upload to finish, then check the world.")
			break
		}
		sources = append(sources, worldimport.Source{Name: f.name, Path: imp.uploadPath(i)})
	}
	imp.mu.Unlock()
	if err == nil && len(sources) == 0 {
		err = errInvalid("Upload the world first.")
	}
	if err != nil {
		writeError(w, err)
		return
	}
	in, err := worldimport.Inspect(r.Context(), sources, a.importLimits(imp.dir))
	if err != nil {
		writeError(w, importErr(err))
		return
	}
	imp.mu.Lock()
	imp.inspection = in
	imp.mu.Unlock()
	writeJSON(w, http.StatusOK, a.importView(imp))
}

// importLimits are worldimport's limits with the total bounded by the disk
// space the machine can spare.
func (a *Agent) importLimits(dir string) worldimport.Limits {
	lim := worldimport.DefaultLimits()
	if free, _, err := a.opts.DiskUsage(dir); err == nil && free-minFreeAfterBackup < lim.MaxTotalBytes {
		// Zero would mean the default, so no space left is one byte.
		lim.MaxTotalBytes = max(free-minFreeAfterBackup, 1)
	}
	return lim
}

// inspected returns an upload's inspection, once it was checked and while no
// operation uses it.
func (a *Agent) inspected(id string) (*worldImport, *worldimport.Inspection, error) {
	imp, err := a.worldImport(id)
	if err != nil {
		return nil, nil, err
	}
	imp.mu.Lock()
	defer imp.mu.Unlock()
	switch {
	case imp.busy == "applying" || imp.busy == "creating":
		return nil, nil, importBusy(imp.busy)
	case imp.inspection == nil:
		return nil, nil, errConflict("Check the world first.", "")
	}
	imp.touched = a.now()
	return imp, imp.inspection, nil
}

// withDefaultWorld picks the world when the owner didn't: the upload's only
// world, or the one its server.properties names.
func withDefaultWorld(in *worldimport.Inspection, o worldimport.Options) worldimport.Options {
	if o.World != "" {
		return o
	}
	var def []string
	for _, w := range in.Worlds {
		if w.Default {
			def = append(def, w.ID)
		}
	}
	switch {
	case len(in.Worlds) == 1:
		o.World = in.Worlds[0].ID
	case len(def) == 1:
		o.World = def[0]
	}
	return o
}

func findWorld(in *worldimport.Inspection, id string) *worldimport.World {
	for i := range in.Worlds {
		if in.Worlds[i].ID == id {
			return &in.Worlds[i]
		}
	}
	return nil
}

// worldLabel names a world for the history and the audit log.
func worldLabel(w worldimport.World) string {
	name := ""
	if w.Level != nil {
		name = strings.TrimSpace(w.Level.Name)
	}
	if name == "" {
		name = filepath.Base(filepath.FromSlash(w.Path))
	}
	if name == "" || name == "." {
		name = w.Archive
	}
	if utf8.RuneCountInString(name) > 64 {
		name = string([]rune(name)[:63]) + "…"
	}
	return strconv.Quote(name)
}

func uploadNames(imp *worldImport) (names []string, sums []map[string]any) {
	imp.mu.Lock()
	defer imp.mu.Unlock()
	for _, f := range imp.files {
		names = append(names, f.name)
		sums = append(sums, map[string]any{"name": f.name, "sha256": f.sum})
	}
	return names, sums
}

// importVersions are the versions a new server from world w can run: the
// recommended version first, then the world's own where Playkeeper runs it
// (1.21 and newer on the image's Java) and it differs. A world newer than the
// recommended version is offered only its own version, if that runs here,
// and nothing until it does, since no version listed can load it. rec is the
// recommended version, which such a world is shown against.
func (a *Agent) importVersions(ctx context.Context, w *worldimport.World) (versions []api.WorldImportVersion, rec api.CatalogEntry, err error) {
	entries, _, err := a.versionCatalog(ctx)
	if err != nil {
		return nil, rec, &apiError{Status: http.StatusServiceUnavailable, Code: api.CodeInvalid, Msg: "Could not load the Minecraft versions from PaperMC: " + err.Error(), Hint: "Check that this server can reach fill.papermc.io, then try again."}
	}
	found := false
	for i := range entries {
		if entries[i].Recommended {
			rec, found = entries[i], true
			break
		}
	}
	for i := range entries {
		if !found && !entries[i].Experimental {
			rec, found = entries[i], true
		}
	}
	if !found {
		return nil, rec, &apiError{Status: http.StatusServiceUnavailable, Code: api.CodeInvalid, Msg: "PaperMC lists no stable Minecraft version right now.", Hint: "Try again later."}
	}
	own := ""
	if w != nil && w.Level != nil && !w.Level.Snapshot && (w.Level.Series == "" || w.Level.Series == "main") {
		own = w.Level.Version
	}
	recommended := api.WorldImportVersion{CatalogEntry: rec}
	if own == "" {
		return []api.WorldImportVersion{recommended}, rec, nil
	}
	switch c := minecraft.CompareMinecraft(own, rec.MinecraftVersion); {
	case c == 0:
		recommended.Keep = true
		return []api.WorldImportVersion{recommended}, rec, nil
	case c < 0:
		out := []api.WorldImportVersion{recommended}
		if keep, err := a.restoreBuild(ctx, own, 0); err == nil {
			out = append(out, api.WorldImportVersion{CatalogEntry: keep, Keep: true})
		}
		return out, rec, nil
	default:
		if keep, err := a.restoreBuild(ctx, own, 0); err == nil {
			return []api.WorldImportVersion{{CatalogEntry: keep, Keep: true}}, rec, nil
		}
		return nil, rec, nil
	}
}

// newServerPlan plans a new server from the upload on the chosen version, or
// the first one offered. A world no version can load yet is shown against
// the recommended version, with no version to choose.
func (a *Agent) newServerPlan(ctx context.Context, in *worldimport.Inspection, o worldimport.Options, versionID string) (*worldimport.Preview, []api.WorldImportVersion, api.WorldImportVersion, error) {
	versions, rec, err := a.importVersions(ctx, findWorld(in, o.World))
	if err != nil {
		return nil, nil, api.WorldImportVersion{}, err
	}
	var chosen api.WorldImportVersion
	if len(versions) > 0 {
		chosen = versions[0]
	}
	if versionID != "" {
		found := false
		for _, v := range versions {
			if v.ID == versionID {
				chosen, found = v, true
			}
		}
		if !found {
			return nil, nil, api.WorldImportVersion{}, errInvalid("Choose one of the listed versions.")
		}
	}
	target := chosen.MinecraftVersion
	if len(versions) == 0 {
		target = rec.MinecraftVersion
	}
	p, err := in.Plan(worldimport.Target{Type: worldimport.TypePaper, MinecraftVersion: target, LevelName: "world"}, o)
	if err != nil {
		return nil, nil, api.WorldImportVersion{}, importErr(err)
	}
	if len(versions) == 0 {
		notRunnableYet(p)
	}
	return p, versions, chosen, nil
}

// notRunnableYet rewords the problem of a world newer than every version
// offered: there is no newer one to choose until Paper has one.
func notRunnableYet(p *worldimport.Preview) {
	reword := func(m *worldimport.Message) {
		if m == nil || m.Kind != worldimport.KindWorldNewer {
			return
		}
		world, _ := m.Params["world"].(string)
		target, _ := m.Params["target"].(string)
		m.Text = fmt.Sprintf("This world was saved by Minecraft %s, which Playkeeper can't run yet. Minecraft can't load worlds from newer versions.", world)
		m.Hint = fmt.Sprintf("Try again once Paper has a stable build of %s, or upload a world saved by Minecraft %s or older.", world, target)
	}
	for i := range p.Problems {
		reword(&p.Problems[i])
	}
	if p.Version != nil {
		reword(p.Version.Problem)
	}
}

func upgrades(p *worldimport.Preview) bool {
	return p.Version != nil && p.Version.Compat == worldimport.CompatUpgrade
}

// replacePlan plans replacing server s's world with the upload.
func (s *server) replacePlan(in *worldimport.Inspection, o worldimport.Options) (*worldimport.Preview, *api.ServerConfig, error) {
	sc, err := s.serverConfig()
	if err != nil {
		return nil, nil, err
	}
	if sc == nil {
		return nil, nil, errNotCreated()
	}
	typ := sc.Type
	if typ == "" {
		typ = api.TypePaper
	}
	p, err := in.Plan(worldimport.Target{Type: typ, MinecraftVersion: sc.MinecraftVersion, LevelName: s.levelName(*sc)}, o)
	if err != nil {
		return nil, nil, importErr(err)
	}
	return p, sc, nil
}

// replacePreview is what replacing server s's world does, with the world it
// replaces.
func (s *server) replacePreview(imp *worldImport, in *worldimport.Inspection, o worldimport.Options) (api.WorldImportPreview, error) {
	p, sc, err := s.replacePlan(in, o)
	if err != nil {
		return api.WorldImportPreview{}, err
	}
	level := s.levelName(*sc)
	folders, err := worldimport.WorldFolders(s.dataDir(), level)
	if err != nil {
		return api.WorldImportPreview{}, importErr(err)
	}
	cw := &api.CurrentWorld{Exists: len(folders) > 0, LevelName: level, SizeBytes: folderBytes(s.dataDir(), folders)}
	name, world := s.name(), worldLabel(p.World)
	out := api.WorldImportPreview{ID: imp.id, ServerID: s.id, Preview: p, VersionID: sc.VersionID, CurrentWorld: cw, WillCreateRollback: cw.Exists, ConfirmPhrase: "import"}
	out.Steps = []string{"Unpack " + world + " while " + name + " keeps running", "Stop " + name + " (players are disconnected; downtime starts)"}
	if cw.Exists {
		out.ConfirmPhrase = "replace " + level
		out.Steps = append(out.Steps, "Save a rollback archive of the current world \""+level+"\"")
	}
	out.Steps = append(out.Steps,
		"Swap in "+world+"; "+name+" keeps its own settings",
		"Start "+labelFor(sc.MinecraftVersion)+" and wait until it is online",
		"If the imported world fails to start, put the previous world back automatically")
	return out, nil
}

func folderBytes(dir string, names []string) int64 {
	var total int64
	for _, n := range names {
		_ = filepath.WalkDir(filepath.Join(dir, n), func(_ string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				if info, err := d.Info(); err == nil {
					total += info.Size()
				}
			}
			return nil
		})
	}
	return total
}

// hWorldImportPreview says what importing the upload would do.
func (a *Agent) hWorldImportPreview(w http.ResponseWriter, r *http.Request) {
	var req api.WorldImportPreviewRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if _, err := validActor(req.Actor); err != nil {
		writeError(w, err)
		return
	}
	imp, in, err := a.inspected(r.PathValue("imp"))
	if err != nil {
		writeError(w, err)
		return
	}
	o := withDefaultWorld(in, req.Options)
	if imp.serverID != "" {
		s := a.serverByID(imp.serverID)
		if s == nil {
			writeError(w, errNotFound("Server"))
			return
		}
		pv, err := s.replacePreview(imp, in, o)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, pv)
		return
	}
	p, versions, chosen, err := a.newServerPlan(r.Context(), in, o, req.VersionID)
	if err != nil {
		writeError(w, err)
		return
	}
	_, mem, _ := a.memoryFor("")
	writeJSON(w, http.StatusOK, api.WorldImportPreview{ID: imp.id, Preview: p, Versions: versions, VersionID: chosen.ID, KeepsOriginal: upgrades(p), MemoryMB: mem})
}

// importSpace refuses an import that needs more disk space than is free.
func (a *Agent) importSpace(need int64) error {
	free, _, err := a.opts.DiskUsage(a.cfg.StagingDir())
	if err != nil || free >= need+minFreeAfterBackup {
		return nil
	}
	return &apiError{Status: http.StatusInsufficientStorage, Code: api.CodeInsufficientSpace,
		Msg:  fmt.Sprintf("Importing this world needs about %s of free disk space; %s is free.", humanBytes(need+minFreeAfterBackup), humanBytes(free)),
		Hint: "Free disk space and try again."}
}

// hWorldImportApply replaces the server's world with the upload.
func (a *Agent) hWorldImportApply(w http.ResponseWriter, r *http.Request) {
	var req api.WorldImportApplyRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	imp, in, err := a.inspected(r.PathValue("imp"))
	if err != nil {
		writeError(w, err)
		return
	}
	if imp.serverID == "" {
		writeError(w, errConflict("This upload is for a new server.", "Create the server from it instead."))
		return
	}
	s := a.serverByID(imp.serverID)
	if s == nil {
		writeError(w, errNotFound("Server"))
		return
	}
	o := withDefaultWorld(in, req.Options)
	pv, err := s.replacePreview(imp, in, o)
	if err != nil {
		writeError(w, err)
		return
	}
	if !pv.Preview.OK() {
		writeError(w, blocked(pv.Preview))
		return
	}
	if strings.TrimSpace(req.Confirm) != pv.ConfirmPhrase {
		s.audit(actor, "world_import.applied", imp.id, "refused", "confirmation phrase mismatch")
		writeError(w, errInvalid("Type \"%s\" to confirm the import.", pv.ConfirmPhrase))
		return
	}
	if err := a.importSpace(pv.Preview.SizeBytes + pv.CurrentWorld.SizeBytes); err != nil {
		writeError(w, err)
		return
	}
	if err := imp.claim("applying", a.now()); err != nil {
		writeError(w, err)
		return
	}
	op, err := s.beginOp("world_import", actor, func(ctx context.Context, h *opHandle) error {
		return s.importWorldOp(ctx, h, imp, in, o, actor)
	})
	if err != nil {
		imp.release()
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// hWorldImportCreate creates a server from the upload. The world is unpacked
// before the server exists, so a failure leaves no server without its world.
func (a *Agent) hWorldImportCreate(w http.ResponseWriter, r *http.Request) {
	var req api.WorldImportCreateRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if !req.AcceptEULA {
		a.audit(actor, "server.create", "server", "refused", "EULA not accepted")
		writeErr(w, http.StatusBadRequest, api.CodeEULARequired, "You must accept the Minecraft EULA before Playkeeper downloads or starts a server.", "Read https://www.minecraft.net/en-us/eula and tick the box to accept it.")
		return
	}
	name := ""
	if strings.TrimSpace(req.Name) != "" {
		if name, err = validName(req.Name); err != nil {
			writeError(w, err)
			return
		}
	}
	imp, in, err := a.inspected(r.PathValue("imp"))
	if err != nil {
		writeError(w, err)
		return
	}
	if imp.serverID != "" {
		writeError(w, errConflict("This upload replaces a server's world.", ""))
		return
	}
	o := withDefaultWorld(in, req.Options)
	p, _, chosen, err := a.newServerPlan(r.Context(), in, o, req.VersionID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !p.OK() {
		writeError(w, blocked(p))
		return
	}
	if err := a.validMemory(req.MemoryMB, ""); err != nil {
		writeError(w, err)
		return
	}
	keep := upgrades(p)
	need := p.SizeBytes
	if keep {
		need *= 2
	}
	if err := a.importSpace(need); err != nil {
		writeError(w, err)
		return
	}
	if err := imp.claim("creating", a.now()); err != nil {
		writeError(w, err)
		return
	}
	staged := imp.stagedDir()
	os.RemoveAll(staged)
	fail := func(err error) {
		os.RemoveAll(staged)
		imp.release()
		writeError(w, err)
	}
	target := worldimport.Target{Type: worldimport.TypePaper, MinecraftVersion: chosen.MinecraftVersion, LevelName: "world"}
	if p, err = in.Stage(r.Context(), staged, target, o); err != nil {
		fail(importErr(err))
		return
	}
	if err := writeImportedProperties(staged, nil, p.Settings); err != nil {
		fail(err)
		return
	}
	now := a.now().UTC()
	motd, _ := validMOTD("")
	maxPlayers, _ := validMaxPlayers(0)
	sc := withBuild(api.ServerConfig{
		Type: api.TypePaper, MemoryMB: req.MemoryMB, HeapMB: minecraft.HeapMB(req.MemoryMB), LevelName: "world", MOTD: motd, MaxPlayers: maxPlayers,
		Whitelist: true, EULAAcceptedAt: now, EULAAcceptedBy: actor, CreatedAt: now, Gameplay: gameplayFrom(p.Settings, api.Gameplay{}),
	}, chosen.CatalogEntry)
	var original *api.ServerConfig
	if keep && p.Version != nil && p.Version.World != "" {
		o := sc
		o.VersionID, o.MinecraftVersion, o.PaperBuild, o.JarSHA256 = minecraft.VersionID(p.Version.World), p.Version.World, 0, ""
		original = &o
	}
	_, op, err := a.addServer(newServerSpec{name: name, typ: api.TypePaper, config: sc, desired: api.DesiredStopped, actor: actor}, "create", func(s *server) func(ctx context.Context, h *opHandle) error {
		return func(ctx context.Context, h *opHandle) error {
			return s.createFromWorld(ctx, h, imp, p, sc, chosen.Label, original, actor)
		}
	})
	if err != nil {
		fail(err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// hWorldImportDelete cancels an upload and deletes its files.
func (a *Agent) hWorldImportDelete(w http.ResponseWriter, r *http.Request) {
	if _, err := validActor(r.URL.Query().Get("actor")); err != nil {
		writeError(w, err)
		return
	}
	imp, err := a.worldImport(r.PathValue("imp"))
	if err != nil {
		writeError(w, err)
		return
	}
	imp.mu.Lock()
	busy := imp.busy
	imp.mu.Unlock()
	if busy == "applying" || busy == "creating" {
		writeError(w, importBusy(busy))
		return
	}
	a.dropImport(imp)
	w.WriteHeader(http.StatusNoContent)
}

// gameplayFrom sets the game settings an imported world carries. The image
// writes them into server.properties at every start, so they live in the
// server's settings rather than only in the file.
func gameplayFrom(carry []worldimport.Setting, g api.Gameplay) api.Gameplay {
	for _, st := range carry {
		switch st.Key {
		case "difficulty":
			g.Difficulty = st.Value
		case "gamemode":
			g.GameMode = st.Value
		case "hardcore":
			v := st.Value == "true"
			g.Hardcore = &v
		}
	}
	return g
}

// currentProperties is the server's own server.properties, or nothing when
// it has none.
func (s *server) currentProperties() ([]byte, error) {
	d, err := s.gameFiles()
	if err != nil {
		return nil, err
	}
	defer d.Close()
	b, err := d.ReadProperties()
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// writeImportedProperties writes the server.properties that goes in with an
// imported world: current with the settings the world carries.
func writeImportedProperties(staged string, current []byte, carry []worldimport.Setting) error {
	d, err := gamefiles.Open(staged, nil)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.WriteProperties(worldimport.ApplySettings(current, carry))
}

// importRecord records a finished import in the operation, the history and
// the audit log.
func (s *server) importRecord(h *opHandle, imp *worldImport, p *worldimport.Preview, actor, extra string) {
	names, sums := uploadNames(imp)
	h.set("world", worldLabel(p.World))
	h.set("uploads", sums)
	version := "an unknown version"
	if lv := p.World.Level; lv != nil {
		h.set("worldVersion", lv.Version)
		h.set("dataVersion", lv.DataVersion)
		if lv.Version != "" {
			version = lv.Version
		}
	}
	detail := fmt.Sprintf("imported %s (Minecraft %s) from %s", worldLabel(p.World), version, strings.Join(names, ", ")) + extra
	s.recordEvent(s.now(), "world_imported", "", "playkeeper", detail)
	s.audit(actor, "world_import.applied", imp.id, "succeeded", detail)
}

// originalDue is the copy of the world as uploaded that a server made from
// an upload must save before its first start, which upgrades the world.
type originalDue struct {
	Config api.ServerConfig `json:"config"`
	Note   string           `json:"note"`
	Actor  string           `json:"actor"`
}

func (s *server) setOriginalDue(d *originalDue) error {
	raw := ""
	if d != nil {
		b, err := json.Marshal(d)
		if err != nil {
			return err
		}
		raw = string(b)
	}
	_, err := s.db.Exec(`UPDATE servers SET original_due = ? WHERE id = ?`, raw, s.id)
	return err
}

func (s *server) loadOriginalDue() (*originalDue, error) {
	var raw string
	err := s.db.QueryRow(`SELECT original_due FROM servers WHERE id = ?`, s.id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || err == nil && raw == "" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var d originalDue
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, fmt.Errorf("the copy of the world as uploaded that is due cannot be read: %w", err)
	}
	return &d, nil
}

// saveOriginal saves the world as uploaded as a verified backup, after which
// it is no longer due.
func (s *server) saveOriginal(d originalDue) (*api.Backup, error) {
	b, err := s.saveVerifiedRollback(d.Config, d.Actor, d.Note)
	if err != nil {
		return nil, err
	}
	if err := s.setOriginalDue(nil); err != nil {
		s.log.Warn("the world as uploaded was saved but is still marked due", "server", s.id, "backup", b.ID, "err", err)
	}
	return b, nil
}

// ensureOriginalSaved saves the world as uploaded before a start, when a
// server made from an upload could not save it as it was created. The start
// upgrades the world, so it waits until the copy is saved.
func (s *server) ensureOriginalSaved(h *opHandle, sc api.ServerConfig) error {
	d, err := s.loadOriginalDue()
	if err != nil || d == nil {
		return err
	}
	h.phase("saving_original")
	b, err := s.saveOriginal(*d)
	if err != nil {
		return s.originalNotSaved(sc.MinecraftVersion, err)
	}
	h.set("originalBackupId", b.ID)
	return nil
}

func (s *server) originalNotSaved(version string, err error) error {
	e := &apiError{
		Msg:  fmt.Sprintf("%s did not start, because its world as uploaded could not be saved before Minecraft %s upgrades it: %v", s.name(), version, err),
		Hint: "Free disk space, then press Start. Playkeeper saves the world as uploaded first.",
	}
	if r, ok := s.withRefusalHint(err).(*apiError); ok {
		e.Hint = r.Hint
	}
	return e
}

// createFromWorld is the first operation of a server made from an upload: the
// staged world moves into the empty data directory, the world as uploaded is
// saved as a backup when the server's version upgrades it, and the server
// starts. A server whose first start fails keeps its world, so the owner can
// try again or pick another version. When the copy of the world as uploaded
// can't be saved, the server doesn't start, and every later start saves it
// first.
func (s *server) createFromWorld(ctx context.Context, h *opHandle, imp *worldImport, p *worldimport.Preview, sc api.ServerConfig, label string, original *api.ServerConfig, actor string) error {
	s.audit(actor, "eula.accepted", "minecraft-eula", "recorded", "https://www.minecraft.net/en-us/eula")
	s.recordEvent(s.now(), "server_created", "", "playkeeper", label)
	h.set("import", imp.id)
	var due *originalDue
	if original != nil {
		// Recorded before the world moves in, so that no start upgrades it
		// before the copy is saved, whatever happens in between.
		due = &originalDue{Config: *original, Note: fmt.Sprintf("%s as uploaded, before Minecraft %s upgraded it", worldLabel(p.World), sc.MinecraftVersion), Actor: actor}
		if err := s.setOriginalDue(due); err != nil {
			imp.release()
			return err
		}
	}
	h.phase("installing_world")
	err := s.ensureDirs()
	if err == nil {
		var names []string
		if names, err = dirNames(imp.stagedDir()); err == nil {
			var moved []string
			moved, err = moveEntries(names, imp.stagedDir(), s.dataDir())
			for _, n := range moved {
				if cerr := chownTree(filepath.Join(s.dataDir(), n), s.cfg.GameUID, s.cfg.GameGID); cerr != nil {
					s.log.Warn("chown imported world", "err", cerr)
				}
			}
		}
	}
	if err != nil {
		if due != nil {
			if cerr := s.setOriginalDue(nil); cerr != nil {
				s.log.Warn("clear the copy of the world as uploaded that was due", "server", s.id, "err", cerr)
			}
		}
		imp.release()
		return &apiError{Msg: "Could not move the world into " + s.name() + ": " + err.Error(), Hint: "Delete this server and create it from the world again."}
	}
	extra := ""
	if due != nil {
		h.phase("saving_original")
		b, err := s.saveOriginal(*due)
		if err != nil {
			s.importRecord(h, imp, p, actor, "; the world as uploaded is saved before the first start")
			s.dropImport(imp)
			return s.originalNotSaved(sc.MinecraftVersion, err)
		}
		h.set("originalBackupId", b.ID)
		extra = "; the world as uploaded is backup " + b.ID
	}
	s.importRecord(h, imp, p, actor, extra)
	s.dropImport(imp)
	_ = s.setDesired(api.DesiredRunning)
	if err := s.startServer(ctx, h, sc); err != nil {
		s.startFailed(ctx)
		return err
	}
	return nil
}

func dirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// moveEntries moves each named entry from one directory to another, never
// over something already there, and returns what it moved.
func moveEntries(names []string, from, to string) ([]string, error) {
	var moved []string
	for _, n := range names {
		dst := filepath.Join(to, n)
		if _, err := os.Lstat(dst); err == nil {
			return moved, fmt.Errorf("%s is in the way", dst)
		} else if !errors.Is(err, os.ErrNotExist) {
			return moved, err
		}
		if err := renameDir(filepath.Join(from, n), dst); err != nil {
			return moved, err
		}
		moved = append(moved, n)
	}
	return moved, nil
}

// replacedNames are the entries of the data directory an import replaces:
// the world folders, the server files and add-on folders the upload brings,
// and server.properties, which the import rewrites.
func replacedNames(live string, folders []string, p *worldimport.Preview) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string, mustExist bool) {
		if seen[n] {
			return
		}
		if mustExist {
			if _, err := os.Lstat(filepath.Join(live, n)); err != nil {
				return
			}
		}
		seen[n] = true
		out = append(out, n)
	}
	for _, f := range folders {
		add(f, false)
	}
	for _, n := range append(append([]string{"server.properties"}, p.Files...), p.Addons...) {
		add(n, true)
	}
	sort.Strings(out)
	return out
}

// worldSwap tracks the entries an import moved out of the data directory
// (into asideDir) and into it, so it can put them back.
type worldSwap struct {
	live, asideDir, failedDir string
	aside, in                 []string
}

// undo moves the imported entries to failedDir and the previous ones back.
// If that fails nothing is deleted, and the error says where both copies are.
func (sw *worldSwap) undo(cause error) error {
	if len(sw.in) > 0 {
		err := os.MkdirAll(sw.failedDir, 0o700)
		if err == nil {
			var moved []string
			moved, err = moveEntries(sw.in, sw.live, sw.failedDir)
			sw.in = sw.in[len(moved):]
		}
		if err != nil {
			return fmt.Errorf("%v; moving the imported world out of the way also failed (%v), so nothing was deleted: it is in %s and the previous world at %s", cause, err, sw.live, sw.asideDir)
		}
	}
	moved, err := moveEntries(sw.aside, sw.asideDir, sw.live)
	sw.aside = sw.aside[len(moved):]
	if err != nil {
		return fmt.Errorf("%v; putting the previous world back also failed (%v), so nothing was deleted: the previous world is at %s and the imported world at %s", cause, err, sw.asideDir, sw.failedDir)
	}
	os.Remove(sw.asideDir)
	return nil
}

// importWorldOp replaces the server's world with the upload, like a restore:
// the world is unpacked while the server runs, a verified rollback archive of
// the current world is saved, only the world folders (and the files the
// upload brings) are swapped, so the server keeps its jar, settings and RCON
// secret, and an imported world that fails to start is swapped back out.
func (s *server) importWorldOp(ctx context.Context, h *opHandle, imp *worldImport, in *worldimport.Inspection, o worldimport.Options, actor string) (err error) {
	worldSafe := true
	staged := imp.stagedDir()
	defer func() {
		switch {
		case err == nil:
			s.dropImport(imp)
		case worldSafe:
			os.RemoveAll(staged)
			imp.release()
		default:
			imp.release()
		}
	}()
	h.set("import", imp.id)
	prev, err := s.serverConfig()
	if err != nil {
		return err
	}
	if prev == nil {
		return errNotCreated()
	}
	level := s.levelName(*prev)
	typ := prev.Type
	if typ == "" {
		typ = api.TypePaper
	}
	h.phase("unpacking")
	os.RemoveAll(staged)
	p, err := in.Stage(ctx, staged, worldimport.Target{Type: typ, MinecraftVersion: prev.MinecraftVersion, LevelName: level}, o)
	if err != nil {
		return importErr(err)
	}
	live := s.dataDir()
	start := s.now()
	_, wasRunning, _ := s.containerRunning(ctx)
	if err := s.stopServer(ctx, h); err != nil {
		return err
	}
	folders, err := worldimport.WorldFolders(live, level)
	if err != nil {
		s.startPrevious(ctx, h, prev, wasRunning)
		return importErr(err)
	}
	var rollback *api.Backup
	if len(folders) > 0 {
		h.phase("saving_rollback")
		rb, err := s.saveVerifiedRollback(*prev, actor, "Automatic rollback archive before importing "+worldLabel(p.World))
		if err != nil {
			s.startPrevious(ctx, h, prev, wasRunning)
			return s.withRefusalHint(fmt.Errorf("could not save a verified rollback archive of the current world, so nothing was replaced: %w", err))
		}
		rollback = rb
		h.set("rollbackBackupId", rb.ID)
	}
	h.phase("replacing_world")
	props, err := s.currentProperties()
	if err != nil {
		s.startPrevious(ctx, h, prev, wasRunning)
		return gameFileError(err, "The world was not imported, so nothing was replaced.")
	}
	if err := writeImportedProperties(staged, props, p.Settings); err != nil {
		s.startPrevious(ctx, h, prev, wasRunning)
		return err
	}
	incoming, err := dirNames(staged)
	if err != nil {
		s.startPrevious(ctx, h, prev, wasRunning)
		return err
	}
	ts := s.now().UTC().Format("20060102-150405")
	sw := &worldSwap{live: live, asideDir: live + ".import-aside-" + ts, failedDir: live + ".failed-import-" + ts}
	if err := os.Mkdir(sw.asideDir, 0o700); err != nil {
		s.startPrevious(ctx, h, prev, wasRunning)
		return err
	}
	worldSafe = false
	// undo puts the previous world back after a failure before the imported
	// world ever started, so the failed copy isn't worth keeping.
	undo := func(cause error) error {
		if err := sw.undo(cause); err != nil {
			return err
		}
		worldSafe = true
		os.RemoveAll(sw.failedDir)
		s.startPrevious(ctx, h, prev, wasRunning)
		return cause
	}
	moved, err := moveEntries(replacedNames(live, folders, p), live, sw.asideDir)
	sw.aside = moved
	if err != nil {
		return undo(fmt.Errorf("could not move the current world aside, so nothing was replaced: %w", err))
	}
	moved, err = moveEntries(incoming, staged, live)
	sw.in = moved
	if err != nil {
		return undo(fmt.Errorf("could not move the imported world into place, so the previous world was put back: %w", err))
	}
	for _, n := range sw.in {
		if err := chownTree(filepath.Join(live, n), s.cfg.GameUID, s.cfg.GameGID); err != nil {
			s.log.Warn("chown imported world", "err", err)
		}
	}
	sc := *prev
	sc.Gameplay = gameplayFrom(p.Settings, prev.Gameplay)
	if err := s.saveServerConfig(sc); err != nil {
		return undo(fmt.Errorf("could not record the imported world's settings, so the previous world was put back: %w", err))
	}
	_ = s.setDesired(api.DesiredRunning)
	if startErr := s.startServer(ctx, h, sc); startErr != nil {
		h.phase("reverting")
		_ = s.stopServer(ctx, h)
		if err := sw.undo(fmt.Errorf("the imported world did not start: %w", startErr)); err != nil {
			return err
		}
		worldSafe = true
		_ = s.saveServerConfig(*prev)
		if err := s.startServer(ctx, h, *prev); err != nil {
			return &apiError{Msg: "The imported world did not start (" + startErr.Error() + "). Your previous world was put back but did not start either: " + err.Error(), Hint: "Press Start on the Overview. The failed import was kept at " + sw.failedDir + " for inspection."}
		}
		return &apiError{Msg: "The imported world did not start (" + startErr.Error() + "). Your previous world was put back and is running.", Hint: "The failed import was kept at " + sw.failedDir + " for inspection."}
	}
	worldSafe = true
	os.RemoveAll(sw.asideDir)
	h.set("downtimeMs", s.now().Sub(start).Milliseconds())
	extra := ""
	if rollback != nil {
		extra = "; rollback archive " + rollback.ID
	}
	s.importRecord(h, imp, p, actor, extra)
	return nil
}
