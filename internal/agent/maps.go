package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/webmap"
)

// Each server's live map. squaremap draws it inside the game server; the
// agent installs it through the add-on library, writes its config before
// every start, asks it once to draw the land explored so far, and is the
// only hop between the panel and squaremap's web server in the container.

var reDomain = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

func init() {
	opLabels["map_enable"] = "turning on the map"
	opLabels["map_disable"] = "turning off the map"
}

// firstRenderWait bounds how long after a start the agent waits for
// squaremap to answer before asking it to draw the explored land; tests
// shorten it.
var firstRenderWait = 3 * time.Minute

const (
	// mapLiveTTL is how long a look at the server's container is reused,
	// so a screen of tiles costs one question to Docker.
	mapLiveTTL = 2 * time.Second
	// progressSettle is how long a full render is watched before the time
	// it has left is estimated.
	progressSettle = 15 * time.Second
)

// mapState is the agent's in-memory map bookkeeping, per server id.
type mapState struct {
	mu        sync.Mutex
	live      map[string]mapLive
	progress  map[string]progressMark
	rendering map[string]bool
}

// mapLive is what the agent last saw of a server's container.
type mapLive struct {
	addr      string
	online    bool
	startedAt time.Time
	at        time.Time
}

// progressMark is the first progress seen of a running full render.
type progressMark struct {
	total, done int
	at          time.Time
}

// mapRecord is a server's row in the maps table, which exists while the map
// is on. shareToken is the shared map's link token, from the last time
// sharing was switched on; it opens the map only while public is set.
type mapRecord struct {
	addons           []addons.Installed
	installedAt      time.Time
	public           bool
	publicPlayers    bool
	shareToken       string
	firstRenderAt    *time.Time
	restartWhenEmpty string
}

func (s *server) loadMap() (*mapRecord, error) {
	var raw, restart, token string
	var installed int64
	var public, players int
	var first sql.NullInt64
	err := s.db.QueryRow(`SELECT addons, installed_at, public, public_players, share_token, first_render_at, restart_when_empty FROM maps WHERE server_id = ?`, s.id).
		Scan(&raw, &installed, &public, &players, &token, &first, &restart)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m := &mapRecord{installedAt: time.UnixMilli(installed).UTC(), public: public != 0, publicPlayers: players != 0, shareToken: token, restartWhenEmpty: restart}
	if err := json.Unmarshal([]byte(raw), &m.addons); err != nil {
		return nil, fmt.Errorf("the map's add-on records cannot be read: %w", err)
	}
	if first.Valid {
		t := time.UnixMilli(first.Int64).UTC()
		m.firstRenderAt = &t
	}
	return m, nil
}

func (s *server) saveMap(m *mapRecord) error {
	raw, err := json.Marshal(m.addons)
	if err != nil {
		return err
	}
	var first any
	if m.firstRenderAt != nil {
		first = m.firstRenderAt.UnixMilli()
	}
	_, err = s.db.Exec(`INSERT OR REPLACE INTO maps(server_id, addons, installed_at, public, public_players, share_token, first_render_at, restart_when_empty) VALUES(?,?,?,?,?,?,?,?)`,
		s.id, string(raw), m.installedAt.UnixMilli(), m.public, m.publicPlayers, m.shareToken, first, m.restartWhenEmpty)
	return err
}

// sharePath is the shared map's path on any of the panel's addresses, or ""
// while it isn't shared.
func (m *mapRecord) sharePath() string {
	if m == nil || !m.public || !webmap.ValidShareToken(m.shareToken) {
		return ""
	}
	return "/map/" + m.shareToken
}

// serverType is the server software, with the pre-0.3.0 default.
func (s *server) serverType() string {
	if r, err := s.row(); err == nil && r.Type != "" {
		return r.Type
	}
	return api.TypePaper
}

func (s *server) gameOwned() bool { return os.Geteuid() == 0 }

func (s *server) webMap(typ, addr string) webmap.Map {
	m := webmap.Map{Dir: s.dataDir(), Type: typ, Addr: addr, Client: s.mapClient, Now: s.now}
	if s.gameOwned() {
		m.Owner = &webmap.Owner{UID: s.cfg.GameUID, GID: s.cfg.GameGID}
	}
	return m
}

func (s *server) addonServer(typ string, sc api.ServerConfig) addons.Server {
	srv := addons.Server{Dir: s.dataDir(), Type: typ, MinecraftVersion: sc.MinecraftVersion}
	if s.gameOwned() {
		srv.Owner = &addons.Owner{UID: s.cfg.GameUID, GID: s.cfg.GameGID}
	}
	return srv
}

// mapLive looks at the server's container: squaremap's address in it, and
// whether the server has finished starting. fresh skips the short reuse.
func (s *server) mapLive(ctx context.Context, fresh bool) mapLive {
	ms := &s.maps
	if !fresh {
		ms.mu.Lock()
		l, ok := ms.live[s.id]
		ms.mu.Unlock()
		if ok && time.Since(l.at) < mapLiveTTL {
			return l
		}
	}
	l := mapLive{at: time.Now()}
	c, running, err := s.containerRunning(ctx)
	if err == nil && running {
		if n, ok := c.NetworkSettings.Networks[networkName]; ok && n.IPAddress != "" {
			l.addr = s.opts.MapAddr(n.IPAddress)
		}
		l.startedAt, _ = c.State.Started()
		s.mu.Lock()
		l.online = s.runPhase == api.PhaseOnline
		s.mu.Unlock()
	}
	ms.mu.Lock()
	if ms.live == nil {
		ms.live = map[string]mapLive{}
	}
	ms.live[s.id] = l
	ms.mu.Unlock()
	return l
}

func (s *server) forgetMapLive() {
	s.maps.mu.Lock()
	delete(s.maps.live, s.id)
	delete(s.maps.progress, s.id)
	s.maps.mu.Unlock()
}

// pendingRestart: squaremap was installed after the running server started,
// so the server has not loaded it yet.
func (rec *mapRecord) pendingRestart(l mapLive) bool {
	return rec != nil && !l.startedAt.IsZero() && rec.installedAt.After(l.startedAt)
}

// mapNeedsRestart is ServerStatus.PendingRestart's part for the map.
func (s *server) mapNeedsRestart(c docker.ContainerJSON) bool {
	rec, err := s.loadMap()
	if err != nil || rec == nil {
		return false
	}
	t, ok := c.State.Started()
	return ok && rec.installedAt.After(t)
}

// mapLink is the shared map's address on the machine's friendly address,
// with the panel's port, or "" while the map isn't shared or the machine
// has no friendly address.
func (s *server) mapLink(rec *mapRecord) string {
	p := rec.sharePath()
	host := strings.ToLower(strings.TrimSpace(s.cfg.Domain))
	if p == "" || host == "" || !reDomain.MatchString(host) {
		return ""
	}
	if s.cfg.PanelPort != 443 {
		host = net.JoinHostPort(host, strconv.Itoa(s.cfg.PanelPort))
	}
	return "https://" + host + p
}

func (s *server) mapInfo(ctx context.Context) (api.MapInfo, error) {
	typ := s.serverType()
	info := api.MapInfo{Plugin: webmap.PluginName, EstimatedMinutes: webmap.EstimatedMinutes, EstimatedMegabytes: webmap.EstimatedMegabytes}
	_, lerr := webmap.LayoutFor(typ)
	info.Supported = lerr == nil
	rec, err := s.loadMap()
	if err != nil {
		return info, err
	}
	l := s.mapLive(ctx, true)
	st := s.webMap(typ, l.addr).Status(ctx, webmap.Check{Installed: rec != nil, Running: l.online, PendingRestart: rec.pendingRestart(l)})
	info.State, info.Params, info.Message, info.Hint = string(st.State), st.Params, st.Msg, st.Hint
	info.Areas, info.Bytes, info.LastDrawn, info.CheckedAt = st.Areas, st.Bytes, st.LastDrawn, st.CheckedAt
	if st.Progress != nil {
		info.Progress = &api.MapProgress{Done: st.Progress.Done, Total: st.Progress.Total, Percent: st.Progress.Percent}
	}
	if st.State == webmap.StateDrawing && st.Progress != nil {
		info.Progress.SecondsLeft = s.secondsLeft(st.Progress)
	} else {
		s.secondsLeft(nil)
	}
	info.Path, info.Link = rec.sharePath(), s.mapLink(rec)
	if rec != nil {
		info.Enabled = true
		info.Public, info.PublicPlayers = rec.public, rec.publicPlayers
		info.RestartWhenEmpty = rec.restartWhenEmpty != ""
		info.PluginVersion = pluginVersion(rec.addons)
	}
	return info, nil
}

// secondsLeft estimates the time a full render has left from how fast it
// drew since it was first seen; nil until that is known.
func (s *server) secondsLeft(p *webmap.Progress) *int {
	ms := &s.maps
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if p == nil {
		delete(ms.progress, s.id)
		return nil
	}
	if ms.progress == nil {
		ms.progress = map[string]progressMark{}
	}
	now := s.now()
	mark, ok := ms.progress[s.id]
	if !ok || p.Done < mark.done || p.Total != mark.total {
		ms.progress[s.id] = progressMark{total: p.Total, done: p.Done, at: now}
		return nil
	}
	elapsed := now.Sub(mark.at)
	drawn := p.Done - mark.done
	if drawn <= 0 || elapsed < progressSettle {
		return nil
	}
	left := int(elapsed.Seconds() * float64(p.Total-p.Done) / float64(drawn))
	return &left
}

func pluginVersion(recs []addons.Installed) string {
	for _, r := range recs {
		if r.ProjectID == webmap.ModrinthProjectID || r.ProjectID == webmap.HangarProjectID {
			return r.VersionNumber
		}
	}
	return ""
}

func mapErr(err error, status int) error {
	var e *webmap.Error
	if errors.As(err, &e) {
		return &apiError{Status: status, Code: api.CodeInvalid, Msg: e.Msg, Hint: e.Hint}
	}
	return err
}

func addonErr(err error) error {
	var e *addons.Error
	if errors.As(err, &e) {
		return &apiError{Status: http.StatusBadGateway, Code: api.CodeInvalid, Msg: e.Msg, Hint: e.Hint}
	}
	return err
}

// writeMapConfig writes squaremap's config before a start of a server whose
// map is on: squaremap reads it once at startup, and a restored backup or a
// hand edit may have changed it. A config that cannot be written stops the
// start, so squaremap never runs with its own defaults.
func (s *server) writeMapConfig() error {
	rec, err := s.loadMap()
	if err != nil || rec == nil {
		return err
	}
	set := webmap.Settings{Link: s.mapLink(rec)}
	if _, err := webmap.Config(set); err != nil {
		set.Link = ""
	}
	if err := s.webMap(s.serverType(), "").WriteConfig(set); err != nil {
		return mapErr(err, http.StatusInternalServerError)
	}
	return nil
}

// mapStarted runs after every successful start: a restart put off until
// nobody plays is done, and a map that was never drawn gets drawn.
func (s *server) mapStarted() {
	rec, err := s.loadMap()
	if err != nil || rec == nil {
		return
	}
	s.forgetMapLive()
	if rec.restartWhenEmpty != "" {
		s.db.Exec(`UPDATE maps SET restart_when_empty = '' WHERE server_id = ?`, s.id)
	}
	if rec.firstRenderAt != nil {
		return
	}
	ms := &s.maps
	ms.mu.Lock()
	if ms.rendering[s.id] {
		ms.mu.Unlock()
		return
	}
	if ms.rendering == nil {
		ms.rendering = map[string]bool{}
	}
	ms.rendering[s.id] = true
	ms.mu.Unlock()
	s.loops.Add(1)
	go func() {
		defer s.loops.Done()
		defer func() {
			ms.mu.Lock()
			delete(ms.rendering, s.id)
			ms.mu.Unlock()
		}()
		s.firstRender(s.ctx)
	}()
}

// firstRender waits for squaremap to answer, then asks it once to draw the
// land explored so far. squaremap resumes an interrupted full render by
// itself, so a request that went out is not sent again.
func (s *server) firstRender(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, firstRenderWait)
	defer cancel()
	typ := s.serverType()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		l := s.mapLive(ctx, true)
		if !l.online {
			return
		}
		st := s.webMap(typ, l.addr).Status(ctx, webmap.Check{Installed: true, Running: true})
		if st.State == webmap.StateDrawing || st.State == webmap.StateReady {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
	if err := webmap.StartDrawing(ctx, rconConsole{s}); err != nil {
		s.log.Warn("could not ask squaremap to draw the map", "server", s.id, "err", err)
		return
	}
	now := s.now().UTC()
	s.db.Exec(`UPDATE maps SET first_render_at = ? WHERE server_id = ?`, now.UnixMilli(), s.id)
	s.recordEvent(now, "map_drawing", "", "playkeeper", "squaremap draws the explored land")
}

// restartMapWhenEmpty restarts an online server nobody is playing on when
// its owner put off the restart that loads the map.
func (s *server) restartMapWhenEmpty(online bool, snap *api.PlayerSnapshot) {
	if !online || snap == nil || snap.Online > 0 {
		return
	}
	rec, err := s.loadMap()
	if err != nil || rec == nil || rec.restartWhenEmpty == "" || s.busy() {
		return
	}
	if _, err := s.restart(rec.restartWhenEmpty); err != nil {
		return
	}
	s.db.Exec(`UPDATE maps SET restart_when_empty = '' WHERE server_id = ?`, s.id)
}

func (s *server) playersOnline() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.players == nil {
		return 0
	}
	return s.players.Online
}

// Handlers.

func (s *server) hMap(w http.ResponseWriter, r *http.Request) {
	info, err := s.mapInfo(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// hMapProxy passes the map page's read-only requests to squaremap in the
// server's container: worlds, players and tiles, nothing else.
func (s *server) hMapProxy(w http.ResponseWriter, r *http.Request) {
	rec, err := s.loadMap()
	if err != nil {
		writeError(w, err)
		return
	}
	if rec == nil {
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "The map is not turned on.", "Turn it on in the Map tab.")
		return
	}
	l := s.mapLive(r.Context(), false)
	http.StripPrefix("/v1/servers/"+s.id+"/map", s.webMap(s.serverType(), l.addr)).ServeHTTP(w, r)
}

func (s *server) hMapEnable(w http.ResponseWriter, r *http.Request) {
	actor, err := actionActor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	sc, err := s.serverConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	if sc == nil {
		writeError(w, errNotCreated())
		return
	}
	typ := s.serverType()
	l, err := webmap.LayoutFor(typ)
	if err != nil {
		writeError(w, mapErr(err, http.StatusBadRequest))
		return
	}
	if rec, err := s.loadMap(); err != nil {
		writeError(w, err)
		return
	} else if rec != nil {
		writeError(w, errConflict("The map is already on.", ""))
		return
	}
	op, err := s.beginOp("map_enable", actor, func(ctx context.Context, h *opHandle) error {
		return s.enableMap(ctx, h, typ, l)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// enableMap installs squaremap, then restarts a running server to load it
// unless people are playing, who get to choose when.
func (s *server) enableMap(ctx context.Context, h *opHandle, typ string, l webmap.Layout) error {
	if rec, err := s.loadMap(); err != nil {
		return err
	} else if rec != nil {
		return errConflict("The map is already on.", "")
	}
	sc, err := s.serverConfig()
	if err != nil {
		return err
	}
	if sc == nil {
		return errNotCreated()
	}
	h.phase("installing")
	srv := s.addonServer(typ, *sc)
	res, err := s.addonLib.Install(ctx, srv, nil, addons.InstallRequest{Source: addons.Source(l.Source), Project: l.ProjectID})
	if err != nil {
		return addonErr(err)
	}
	now := s.now().UTC()
	rec := &mapRecord{addons: res.Installed, installedAt: now}
	if err := s.saveMap(rec); err != nil {
		if rerr := s.removeMapAddons(srv, res.Installed, false); rerr != nil {
			s.log.Warn("could not remove squaremap after failing to record it", "server", s.id, "err", rerr)
		}
		return err
	}
	s.forgetMapLive()
	ver := pluginVersion(res.Installed)
	h.set("plugin", webmap.PluginName)
	h.set("version", ver)
	s.recordEvent(now, "map_enabled", "", "playkeeper", strings.TrimSpace(webmap.PluginName+" "+ver))
	_, running, err := s.containerRunning(ctx)
	if err != nil || !running || s.playersOnline() > 0 {
		return nil
	}
	h.phase("restarting")
	if err := s.stopServer(ctx, h); err != nil {
		return err
	}
	if err := s.startServer(ctx, h, *sc); err != nil {
		s.startFailed(ctx)
		return err
	}
	return nil
}

func (s *server) hMapDisable(w http.ResponseWriter, r *http.Request) {
	var req api.MapDisableRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if rec, err := s.loadMap(); err != nil {
		writeError(w, err)
		return
	} else if rec == nil {
		writeError(w, errConflict("The map is already off.", ""))
		return
	}
	op, err := s.beginOp("map_disable", actor, func(ctx context.Context, h *opHandle) error {
		return s.disableMap(ctx, h, req.DeleteMap)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// disableMap removes squaremap and what it needed, and with deleteMap what
// it drew, then restarts a running server to unload it. The shared link
// stops working with the record.
func (s *server) disableMap(ctx context.Context, h *opHandle, deleteMap bool) error {
	rec, err := s.loadMap()
	if err != nil || rec == nil {
		return err
	}
	sc, err := s.serverConfig()
	if err != nil {
		return err
	}
	if sc == nil {
		return errNotCreated()
	}
	typ := s.serverType()
	h.phase("removing")
	srv := s.addonServer(typ, *sc)
	if err := s.removeMapAddons(srv, rec.addons, deleteMap); err != nil {
		return err
	}
	if deleteMap {
		if l, err := webmap.LayoutFor(typ); err == nil {
			if err := removeInData(s.dataDir(), l.Dir); err != nil {
				return &apiError{Msg: "squaremap was removed, but what it drew could not be deleted: " + err.Error(), Hint: "Delete the " + l.Dir + " folder in the server's files."}
			}
		}
	}
	if _, err := s.db.Exec(`DELETE FROM maps WHERE server_id = ?`, s.id); err != nil {
		return err
	}
	s.forgetMapLive()
	h.set("deletedMap", deleteMap)
	detail := "drawn map kept"
	if deleteMap {
		detail = "drawn map deleted"
	}
	s.recordEvent(s.now(), "map_disabled", "", "playkeeper", detail)
	_, running, err := s.containerRunning(ctx)
	if err != nil || !running {
		return nil
	}
	h.phase("restarting")
	if err := s.stopServer(ctx, h); err != nil {
		return err
	}
	if err := s.startServer(ctx, h, *sc); err != nil {
		s.startFailed(ctx)
		return err
	}
	return nil
}

// removeMapAddons uninstalls squaremap first, then what was installed for
// it. removeConfig also deletes squaremap's plugin folder.
func (s *server) removeMapAddons(srv addons.Server, recs []addons.Installed, removeConfig bool) error {
	left := slices.Clone(recs)
	slices.SortStableFunc(left, func(a, b addons.Installed) int {
		switch {
		case a.DependencyOf == "" && b.DependencyOf != "":
			return -1
		case a.DependencyOf != "" && b.DependencyOf == "":
			return 1
		}
		return 0
	})
	for len(left) > 0 {
		rec := left[0]
		if _, err := s.addonLib.Uninstall(srv, left, rec.Key(), addons.UninstallOptions{RemoveConfig: removeConfig && rec.DependencyOf == "", Force: true}); err != nil {
			return addonErr(err)
		}
		left = left[1:]
	}
	return nil
}

// removeInData deletes rel inside the server's data directory without
// following links out of it.
func removeInData(dataDir, rel string) error {
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer root.Close()
	return root.RemoveAll(rel)
}

func (s *server) hMapShare(w http.ResponseWriter, r *http.Request) {
	var req api.MapShareRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if req.Public == nil && req.Players == nil {
		writeError(w, errInvalid("Say which switch to change."))
		return
	}
	// Switching sharing on makes a new link token, so a link from an earlier
	// time it was on stops working; a map that is already shared keeps its
	// link.
	res, err := s.db.Exec(`UPDATE maps SET
  share_token = CASE WHEN ?1 = 1 AND (public = 0 OR share_token = '') THEN ?2 ELSE share_token END,
  public = COALESCE(?1, public),
  public_players = COALESCE(?3, public_players)
WHERE server_id = ?4`, req.Public, webmap.NewShareToken(), req.Players, s.id)
	if err != nil {
		writeError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, errConflict("Turn on the map first.", ""))
		return
	}
	info, err := s.mapInfo(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	s.audit(actor, "map.share", "map", "changed", fmt.Sprintf("link %s, players %s", onOff(info.Public), onOff(info.PublicPlayers)))
	writeJSON(w, http.StatusOK, info)
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// hMapRestartLater restarts the server to load the map once nobody is
// playing, instead of now.
func (s *server) hMapRestartLater(w http.ResponseWriter, r *http.Request) {
	actor, err := actionActor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	res, err := s.db.Exec(`UPDATE maps SET restart_when_empty = ? WHERE server_id = ?`, actor, s.id)
	if err != nil {
		writeError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, errConflict("Turn on the map first.", ""))
		return
	}
	s.audit(actor, "map.restart_later", "map", "scheduled", "restarts when nobody is playing")
	info, err := s.mapInfo(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// The shared map, under its link token. A map that is turned off or not
// shared, a server that is stopped or has not loaded the map, and a token
// that is old, unknown or malformed all get the same answer, which never
// names a server.

func writeMapUnavailable(w http.ResponseWriter) {
	writeErr(w, http.StatusNotFound, api.CodeNotFound, "This map isn't available.", "Ask whoever shared it for a new link.")
}

// serverByMapToken is the server whose map has the link token token, shared
// or not. Every map's token is compared, each in constant time.
func (a *Agent) serverByMapToken(token string) *server {
	rows, err := a.db.Query(`SELECT server_id, share_token FROM maps`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var id string
	for rows.Next() {
		var sid, stored string
		if rows.Scan(&sid, &stored) == nil && webmap.ShareTokenMatches(stored, token) {
			id = sid
		}
	}
	if rows.Err() != nil || id == "" {
		return nil
	}
	return a.serverByID(id)
}

// sharedMap is the server whose map anyone may open with token right now.
func (a *Agent) sharedMap(ctx context.Context, token string) (*server, *mapRecord, mapLive, bool) {
	if !webmap.ValidShareToken(token) {
		return nil, nil, mapLive{}, false
	}
	s := a.serverByMapToken(token)
	if s == nil {
		return nil, nil, mapLive{}, false
	}
	rec, err := s.loadMap()
	if err != nil || rec == nil || !rec.public {
		return nil, nil, mapLive{}, false
	}
	l := s.mapLive(ctx, false)
	if !l.online || l.addr == "" || rec.pendingRestart(l) {
		return nil, nil, mapLive{}, false
	}
	return s, rec, l, true
}

func (a *Agent) hPublicMap(w http.ResponseWriter, r *http.Request) {
	s, rec, _, ok := a.sharedMap(r.Context(), r.PathValue("token"))
	if !ok {
		writeMapUnavailable(w)
		return
	}
	writeJSON(w, http.StatusOK, api.PublicMap{Name: s.name(), Players: rec.publicPlayers})
}

// hPublicMapProxy serves the shared map's worlds, tiles and the server's
// icon, and its players only while the second switch is on. While it is
// off, players is an empty list, so a page that loaded before it was
// switched off keeps working, and squaremap is not asked.
func (a *Agent) hPublicMapProxy(w http.ResponseWriter, r *http.Request) {
	token, rest := r.PathValue("token"), r.PathValue("rest")
	s, rec, l, ok := a.sharedMap(r.Context(), token)
	if !ok || !publicMapPath(rest) {
		writeMapUnavailable(w)
		return
	}
	switch {
	case rest == "players" && !rec.publicPlayers:
		writeJSON(w, http.StatusOK, webmap.Players{Players: []webmap.Player{}, UpdatedAt: s.now().UTC()})
	case rest == "icon":
		b, err := s.readIcon()
		if err != nil {
			writeMapUnavailable(w)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(b)
	default:
		http.StripPrefix("/v1/public-maps/"+token, s.webMap(s.serverType(), l.addr)).ServeHTTP(w, r)
	}
}

func publicMapPath(rest string) bool {
	switch rest {
	case "worlds", "icon", "players":
		return true
	}
	return strings.HasPrefix(rest, "tiles/")
}
