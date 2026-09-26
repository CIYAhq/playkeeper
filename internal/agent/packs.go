package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
	"github.com/CIYAhq/playkeeper/internal/packs"
)

// Data packs in a server's world, and the resource pack it offers players.
// Data packs are switched on and off through the console of the running
// server. Resource packs live in the machine's resource pack folder, from
// which the panel serves them to players' games; offering one sets the
// server's container environment, so players get it after the next start.

// maxPackFileName bounds the file name shown for a resource pack.
const maxPackFileName = 100

func (s *server) dataPacks(sc *api.ServerConfig) packs.DataPacks {
	var owner *gamefiles.Owner
	if o := s.gameOwner(); o != nil {
		owner = &gamefiles.Owner{UID: o.UID, GID: o.GID}
	}
	return packs.DataPacks{DataDir: s.dataDir(), Level: s.levelName(*sc), Owner: owner}
}

func (s *server) packConsole(sc *api.ServerConfig) packs.Console {
	return packs.Console{Commander: rconConsole{s}, ServerType: s.serverType(sc)}
}

func (a *Agent) packStore() packs.Store { return packs.Store{Dir: a.cfg.ResourcePacksDir()} }

// packConfig is the server's settings, for a pack request.
func (s *server) packConfig() (*api.ServerConfig, error) {
	sc, err := s.serverConfig()
	if err != nil {
		return nil, err
	}
	if sc == nil {
		return nil, errNotCreated()
	}
	return sc, nil
}

// packError maps a packs error to an HTTP response, keeping its code.
func packError(err error) error {
	var e *packs.Error
	if !errors.As(err, &e) {
		return err
	}
	status := http.StatusBadRequest
	switch e.Code {
	case packs.CodeFolderPack, packs.CodeNeedsFeatures, packs.CodeFeaturePack, packs.CodeUnsupportedServer, packs.CodeInvalidLevel, packs.CodeFileRefused:
		status = http.StatusConflict
	case packs.CodeNotFound, packs.CodeUnknownPack, packs.CodeNoIcon:
		status = http.StatusNotFound
	case packs.CodeTooLarge:
		status = http.StatusRequestEntityTooLarge
	case packs.CodeConsole, packs.CodeUnexpectedReply, packs.CodeNotApplied:
		status = http.StatusBadGateway
	case packs.CodeFileFailed:
		status = http.StatusInternalServerError
	}
	return &apiError{Status: status, Code: e.Code, Msg: e.Msg, Hint: e.Hint}
}

// problemText is err as one or two sentences for a notice.
func problemText(err error) string {
	var e *packs.Error
	if errors.As(err, &e) && e.Hint != "" {
		return e.Msg + " " + e.Hint
	}
	return err.Error()
}

func (s *server) offlineError(what string) error {
	return &apiError{Status: http.StatusConflict, Code: "offline", Msg: fmt.Sprintf("%s isn't online, so %s.", s.name(), what), Hint: "Start it, then try again."}
}

func writePNG(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(b)
}

// Data packs.

func (s *server) hDataPacks(w http.ResponseWriter, r *http.Request) {
	sc, err := s.packConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := s.dataPackList(r.Context(), sc)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// installedDataPacks lists the world's data packs, without the folder pack
// Paper keeps in every world for its plugins' recipes and advancements.
func (s *server) installedDataPacks(sc *api.ServerConfig) ([]packs.DataPack, error) {
	list, err := s.dataPacks(sc).List()
	if err != nil {
		return nil, packError(err)
	}
	switch s.serverType(sc) {
	case "paper", "purpur":
		list = slices.DeleteFunc(list, func(p packs.DataPack) bool { return p.Folder && p.Name == "bukkit" })
	}
	return list, nil
}

// dataPackList lists the data packs in the world's datapacks folder and,
// while the server is online, which of them it has enabled.
func (s *server) dataPackList(ctx context.Context, sc *api.ServerConfig) (api.DataPacks, error) {
	d := s.dataPacks(sc)
	list, err := s.installedDataPacks(sc)
	if err != nil {
		return api.DataPacks{}, err
	}
	out := api.DataPacks{Packs: make([]api.DataPack, 0, len(list))}
	var enabled map[string]bool
	if s.online(ctx) {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		enabled, out.Live = s.enabledDataPacks(cctx, sc, list)
		cancel()
	}
	for _, p := range list {
		sum, _ := d.Summary(ctx, p.Name)
		dp := api.DataPack{Name: p.Name, Description: sum.Description, Icon: sum.Icon, Size: p.Size, Folder: p.Folder, AddedAt: p.ModTime.UTC()}
		if on, ok := enabled[p.ID]; ok {
			dp.Enabled = &on
		}
		out.Packs = append(out.Packs, dp)
	}
	return out, nil
}

// enabledDataPacks asks the running server which of the packs in list it
// has enabled, one at a time, leaving out packs whose IDs can't be sent to
// its console. It reports false when the server can't say.
func (s *server) enabledDataPacks(ctx context.Context, sc *api.ServerConfig, list []packs.DataPack) (map[string]bool, bool) {
	con := s.packConsole(sc)
	enabled := make(map[string]bool, len(list))
	for _, p := range list {
		on, err := con.IsEnabled(ctx, p.ID)
		if errors.Is(err, packs.ErrInvalidID) {
			continue
		}
		if err != nil {
			return nil, false
		}
		enabled[p.ID] = on
	}
	return enabled, true
}

// findDataPack is the installed data pack name.
func (s *server) findDataPack(sc *api.ServerConfig, name string) (packs.DataPack, error) {
	list, err := s.installedDataPacks(sc)
	if err != nil {
		return packs.DataPack{}, err
	}
	for _, p := range list {
		if p.Name == name {
			return p, nil
		}
	}
	return packs.DataPack{}, &apiError{Status: http.StatusNotFound, Code: packs.CodeNotFound, Msg: fmt.Sprintf("No data pack named %q is installed.", name)}
}

// hDataPackAdd installs an uploaded data pack, replacing one of the same
// name, and switches it on while the server is online. A stopped server
// switches a new pack on when it starts.
func (s *server) hDataPackAdd(w http.ResponseWriter, r *http.Request) {
	actor := actorFromHeader(r)
	if actor == "unknown" {
		writeError(w, errInvalid("X-Playkeeper-Actor header is required"))
		return
	}
	name := packs.SafeName(r.URL.Query().Get("name"))
	f, n, err := packs.Stage(s.cfg.StagingDir(), r.Body, packs.Data, packs.Limits{})
	if err != nil {
		writeError(w, packError(err))
		return
	}
	defer f.Close()
	release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	defer release()
	sc, err := s.packConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.ensureDirs(); err != nil {
		writeError(w, err)
		return
	}
	ctx := r.Context()
	d := s.dataPacks(sc)
	_, err = s.findDataPack(sc, name)
	replacing := err == nil
	if _, _, err := d.Install(ctx, name, f, n, true); err != nil {
		writeError(w, packError(err))
		return
	}
	action := "datapack.added"
	if replacing {
		action = "datapack.replaced"
	}
	s.audit(actor, action, name, "succeeded", "")
	var problem error
	if s.online(ctx) {
		problem = s.applyDataPack(ctx, sc, name)
	}
	out, err := s.dataPackList(ctx, sc)
	if err != nil {
		writeError(w, err)
		return
	}
	out.Added = name
	if problem != nil {
		out.NotEnabled, out.Problem = true, problemText(problem)
	}
	writeJSON(w, http.StatusOK, out)
}

// applyDataPack switches a just installed data pack on in the running
// server, or makes the server reload a replaced pack that was on.
func (s *server) applyDataPack(ctx context.Context, sc *api.ServerConfig, name string) error {
	ctx, cancel := context.WithTimeout(ctx, s.opts.DataPackWait)
	defer cancel()
	con := s.packConsole(sc)
	id := packs.DataPackID(name)
	enabled, err := con.IsEnabled(ctx, id)
	if err != nil {
		return err
	}
	if enabled {
		return con.Reload(ctx)
	}
	if err := con.Enable(ctx, id); err != nil {
		return err
	}
	return con.WaitEnabled(ctx, id, true, time.Second)
}

func (s *server) hDataPackEnable(w http.ResponseWriter, r *http.Request) {
	s.switchDataPack(w, r, true)
}

func (s *server) hDataPackDisable(w http.ResponseWriter, r *http.Request) {
	s.switchDataPack(w, r, false)
}

// switchDataPack switches a data pack on or off in the running server and
// waits until the server has reloaded its data.
func (s *server) switchDataPack(w http.ResponseWriter, r *http.Request, on bool) {
	actor, err := actionActor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	defer release()
	sc, err := s.packConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	p, err := s.findDataPack(sc, r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	if !s.online(r.Context()) {
		writeError(w, s.offlineError("its data packs can't be switched on or off"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.opts.DataPackWait)
	defer cancel()
	con := s.packConsole(sc)
	action := "datapack.enabled"
	if on {
		err = con.Enable(ctx, p.ID)
	} else {
		action = "datapack.disabled"
		err = con.Disable(ctx, p.ID)
	}
	if err == nil {
		err = con.WaitEnabled(ctx, p.ID, on, time.Second)
	}
	if err != nil {
		writeError(w, packError(err))
		return
	}
	s.audit(actor, action, p.Name, "succeeded", "")
	out, err := s.dataPackList(r.Context(), sc)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// hDataPackRemove deletes a zipped data pack, switching it off first while
// the server is online.
func (s *server) hDataPackRemove(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	defer release()
	sc, err := s.packConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	d := s.dataPacks(sc)
	p, err := s.findDataPack(sc, r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	if p.Folder {
		writeError(w, packError(d.Remove(p.Name)))
		return
	}
	if s.online(r.Context()) {
		if err := s.switchOff(r.Context(), sc, p.ID); err != nil {
			writeError(w, packError(err))
			return
		}
	}
	if err := d.Remove(p.Name); err != nil {
		writeError(w, packError(err))
		return
	}
	s.audit(actor, "datapack.removed", p.Name, "succeeded", "")
	out, err := s.dataPackList(r.Context(), sc)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// switchOff switches the data pack id off in the running server when it is
// on.
func (s *server) switchOff(ctx context.Context, sc *api.ServerConfig, id string) error {
	ctx, cancel := context.WithTimeout(ctx, s.opts.DataPackWait)
	defer cancel()
	con := s.packConsole(sc)
	enabled, err := con.IsEnabled(ctx, id)
	if err != nil || !enabled {
		return err
	}
	if err := con.Disable(ctx, id); err != nil {
		return err
	}
	return con.WaitEnabled(ctx, id, false, time.Second)
}

func (s *server) hDataPackIcon(w http.ResponseWriter, r *http.Request) {
	sc, err := s.packConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	b, err := s.dataPacks(sc).Icon(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, packError(err))
		return
	}
	writePNG(w, b)
}

// The resource pack.

// resourcePackEnv is the image's environment for the server's resource
// pack. It is empty until Playkeeper first offers one, so the server's
// definition stays as it was; once it has, an offer without a pack sets the
// variables to empty, which clears them from server.properties.
func resourcePackEnv(o *api.ResourcePackOffer) []string {
	if o == nil {
		return nil
	}
	settings := packs.ClearSettings()
	if o.SHA1 != "" {
		if set, err := offerOf(o).Settings(); err == nil {
			settings = set
		}
	}
	env := make([]string, len(settings))
	for i, st := range settings {
		env[i] = st.Env + "=" + st.Value
	}
	return env
}

func offerOf(o *api.ResourcePackOffer) packs.Offer {
	return packs.Offer{URL: o.URL, SHA1: o.SHA1, Required: o.Required, Prompt: o.Prompt}
}

// packEnv picks the resource pack variables out of a container's
// environment.
func packEnv(env []string) map[string]string {
	out := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		if slices.ContainsFunc(packs.ClearSettings(), func(st packs.Setting) bool { return st.Env == k }) {
			out[k] = v
		}
	}
	return out
}

// rePanelPack matches the URL of a pack the panel serves, as a
// server.properties file holds it.
var rePanelPack = regexp.MustCompile(regexp.QuoteMeta(packs.PathPrefix) + `[0-9a-f]{40}\.zip$`)

// restoredPackOffer is the resource pack offer of a server whose world was
// just restored into dataDir, given the offer it had. Backups don't hold
// resource packs, so the server keeps offering the pack it offered. A
// restored server.properties can name a pack the panel no longer serves,
// which players' games would fail to download: the offer then clears it.
func restoredPackOffer(prev *api.ResourcePackOffer, dataDir string) *api.ResourcePackOffer {
	if prev == nil && rePanelPack.MatchString(readProperties(dataDir)["resource-pack"]) {
		return &api.ResourcePackOffer{}
	}
	return prev
}

func (s *server) hResourcePack(w http.ResponseWriter, r *http.Request) {
	sc, err := s.packConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.resourcePackView(r.Context(), sc))
}

// resourcePackView is the pack the server offers, and whether the running
// server offers something else until it restarts.
func (s *server) resourcePackView(ctx context.Context, sc *api.ServerConfig) api.ResourcePack {
	var out api.ResourcePack
	if o := sc.ResourcePack; o != nil && o.SHA1 != "" {
		offer := *o
		out.Offer = &offer
	}
	if c, running, err := s.containerRunning(ctx); err == nil && running {
		out.Pending = !maps.Equal(packEnv(c.Config.Env), packEnv(resourcePackEnv(sc.ResourcePack)))
	}
	return out
}

// packFileName is an uploaded file's name for display: its base name,
// without control characters, of at most 100 characters.
func packFileName(upload string) string {
	base := upload[strings.LastIndexAny(upload, `/\`)+1:]
	base = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == utf8.RuneError {
			return -1
		}
		return r
	}, base))
	if r := []rune(base); len(r) > maxPackFileName {
		base = string(r[:maxPackFileName])
	}
	if base == "" {
		return "resource-pack.zip"
	}
	return base
}

// hResourcePackSet stores an uploaded resource pack and offers it to
// players instead of the previous one, keeping that offer's settings. The
// panel says where players' games reach it: host, port and https.
func (s *server) hResourcePackSet(w http.ResponseWriter, r *http.Request) {
	actor := actorFromHeader(r)
	if actor == "unknown" {
		writeError(w, errInvalid("X-Playkeeper-Actor header is required"))
		return
	}
	q := r.URL.Query()
	port, _ := strconv.Atoi(q.Get("port"))
	origin := packs.Origin{Host: q.Get("host"), Port: port, HTTPS: q.Get("https") == "true"}
	if _, err := origin.PackURL(strings.Repeat("0", 40)); err != nil {
		writeError(w, packError(err))
		return
	}
	f, n, err := packs.Stage(s.cfg.StagingDir(), r.Body, packs.Resource, packs.Limits{})
	if err != nil {
		writeError(w, packError(err))
		return
	}
	defer f.Close()
	release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	defer release()
	sc, err := s.packConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	ctx := r.Context()
	s.packMu.Lock()
	defer s.packMu.Unlock()
	info, err := s.packStore().Put(ctx, f, n)
	if err != nil {
		writeError(w, packError(err))
		return
	}
	url, err := origin.PackURL(info.SHA1)
	if err != nil {
		writeError(w, packError(err))
		return
	}
	offer := api.ResourcePackOffer{
		SHA1: info.SHA1, FileName: packFileName(q.Get("name")), Size: info.Size, Description: info.Description,
		Icon: packs.Summarize(ctx, f, n, packs.Resource).Icon, AddedAt: s.now().UTC(), URL: url,
	}
	if prev := sc.ResourcePack; prev != nil && prev.SHA1 != "" {
		offer.Required, offer.Prompt = prev.Required, prev.Prompt
	}
	sc.ResourcePack = &offer
	if err := s.saveServerConfig(*sc); err != nil {
		writeError(w, err)
		return
	}
	s.audit(actor, "resourcepack.offered", offer.FileName, "succeeded", "")
	s.prunePacksLocked(ctx)
	writeJSON(w, http.StatusOK, s.resourcePackView(ctx, sc))
}

// hResourcePackSettings changes whether players must accept the pack, and
// the message they see.
func (s *server) hResourcePackSettings(w http.ResponseWriter, r *http.Request) {
	var req api.ResourcePackSettingsRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	defer release()
	sc, err := s.packConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	cur := sc.ResourcePack
	if cur == nil || cur.SHA1 == "" {
		writeError(w, &apiError{Status: http.StatusConflict, Code: "no_resource_pack", Msg: s.name() + " doesn't offer a resource pack.", Hint: "Upload one first."})
		return
	}
	next := *cur
	next.Required, next.Prompt = req.Required, strings.TrimSpace(req.Prompt)
	if _, err := offerOf(&next).Settings(); err != nil {
		writeError(w, packError(err))
		return
	}
	var changes []string
	if next.Required != cur.Required {
		changes = append(changes, map[bool]string{true: "players must accept it", false: "players may decline it"}[next.Required])
	}
	if next.Prompt != cur.Prompt {
		changes = append(changes, "message changed")
	}
	if len(changes) > 0 {
		sc.ResourcePack = &next
		if err := s.saveServerConfig(*sc); err != nil {
			writeError(w, err)
			return
		}
		s.audit(actor, "resourcepack.changed", cur.FileName, "succeeded", strings.Join(changes, ", "))
	}
	writeJSON(w, http.StatusOK, s.resourcePackView(r.Context(), sc))
}

// hResourcePackRemove stops offering the resource pack.
func (s *server) hResourcePackRemove(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	defer release()
	sc, err := s.packConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	if cur := sc.ResourcePack; cur != nil && cur.SHA1 != "" {
		s.packMu.Lock()
		defer s.packMu.Unlock()
		sc.ResourcePack = &api.ResourcePackOffer{}
		if err := s.saveServerConfig(*sc); err != nil {
			writeError(w, err)
			return
		}
		s.audit(actor, "resourcepack.removed", cur.FileName, "succeeded", "")
		s.prunePacksLocked(r.Context())
	}
	writeJSON(w, http.StatusOK, s.resourcePackView(r.Context(), sc))
}

func (s *server) hResourcePackIcon(w http.ResponseWriter, r *http.Request) {
	sc, err := s.packConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	o := sc.ResourcePack
	if o == nil || o.SHA1 == "" {
		writeError(w, errNotFound("Resource pack"))
		return
	}
	b, err := s.packStore().Icon(r.Context(), o.SHA1)
	if err != nil {
		writeError(w, packError(err))
		return
	}
	writePNG(w, b)
}

// hActiveResourcePacks lists the packs the panel serves players' games.
func (a *Agent) hActiveResourcePacks(w http.ResponseWriter, r *http.Request) {
	sums, err := a.offeredPacks(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.ActiveResourcePacks{SHA1: sums})
}

// offeredPacks are the SHA-1 hashes of the resource packs servers offer,
// and of those their containers still offer until they are recreated. It
// fails when it can't tell what a container offers.
func (a *Agent) offeredPacks(ctx context.Context) ([]string, error) {
	set := map[string]bool{}
	for _, s := range a.serverList() {
		sc, err := s.serverConfig()
		if err != nil {
			return nil, err
		}
		if sc != nil && sc.ResourcePack != nil && sc.ResourcePack.SHA1 != "" {
			set[sc.ResourcePack.SHA1] = true
		}
		c, err := s.docker.ContainerInspect(ctx, s.containerName())
		switch {
		case docker.IsNotFound(err):
		case err != nil:
			return nil, s.dockerErr(err)
		default:
			if sum := packEnv(c.Config.Env)["RESOURCE_PACK_SHA1"]; sum != "" {
				set[sum] = true
			}
		}
	}
	return append([]string{}, slices.Sorted(maps.Keys(set))...), nil
}

// prunePacks deletes the resource packs nothing offers any more.
func (a *Agent) prunePacks(ctx context.Context) {
	a.packMu.Lock()
	defer a.packMu.Unlock()
	a.prunePacksLocked(ctx)
}

// prunePacksLocked is prunePacks for a caller holding packMu. It leaves the
// folder alone when it can't tell what every server offers.
func (a *Agent) prunePacksLocked(ctx context.Context) {
	sums, err := a.offeredPacks(ctx)
	if err != nil {
		a.log.Warn("resource packs not pruned", "err", err)
		return
	}
	if err := a.packStore().Prune(func(sum string) bool { return slices.Contains(sums, sum) }); err != nil {
		a.log.Error("pruning resource packs failed", "err", err)
	}
}
