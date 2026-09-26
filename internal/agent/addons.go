package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/api"
)

// A server's add-ons: plugins on Paper, installed from Modrinth and Hangar by
// the addons library. The library keeps no state; the addons table holds a
// record of every file Playkeeper put into a server's folder.

var (
	reProjectRef  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	reFingerprint = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

const (
	maxSearchText  = 100
	maxSearchPage  = 800
	maxUpdateKeys  = 200
	maxAddonIcon   = 2048
	addonChecksTTL = 15 * time.Minute
	browseTTL      = 2 * time.Minute
	browseEntries  = 100
	iconTTL        = 24 * time.Hour
	iconFailTTL    = 5 * time.Minute
	iconEntries    = 256
	iconBytes      = 32 << 20
)

func (a *Agent) lib() *addons.Library { return a.opts.Addons }

// parseAddonKey checks an add-on's source and project id from a request.
func parseAddonKey(source, project string) (addons.Key, error) {
	switch addons.Source(source) {
	case addons.Modrinth, addons.Hangar:
	default:
		return addons.Key{}, errInvalid("Add-ons come from Modrinth or Hangar.")
	}
	if !reProjectRef.MatchString(project) || project == "." || project == ".." {
		return addons.Key{}, errInvalid("That is not a valid project id.")
	}
	return addons.Key{Source: addons.Source(source), ProjectID: project}, nil
}

// updateKeys checks the add-ons an update request names.
func updateKeys(in []api.AddonKey) ([]addons.Key, error) {
	if len(in) > maxUpdateKeys {
		return nil, errInvalid("At most %d add-ons can be updated at once.", maxUpdateKeys)
	}
	var keys []addons.Key
	for _, k := range in {
		key, err := parseAddonKey(k.Source, k.ProjectID)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// confirmedPlan requires the fingerprint of the plan the user confirmed on
// every install and update: the library skips its check without one.
func confirmedPlan(fingerprint string) error {
	if !reFingerprint.MatchString(fingerprint) {
		return &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Msg: "This request doesn't include the plan you confirmed.",
			Hint: "Reload the page, check what it will do, and confirm again."}
	}
	return nil
}

// serverType is the type add-ons must fit: the server's recorded type.
func (s *server) serverType(sc *api.ServerConfig) string {
	if r, err := s.row(); err == nil && r.Type != "" {
		return r.Type
	}
	if sc != nil && sc.Type != "" {
		return sc.Type
	}
	return api.TypePaper
}

func (s *server) gameOwner() *addons.Owner {
	if os.Geteuid() != 0 {
		return nil
	}
	return &addons.Owner{UID: s.cfg.GameUID, GID: s.cfg.GameGID}
}

// addonContext is what every add-on operation needs: the server's settings,
// the server as the library sees it, and where its add-ons go.
func (s *server) addonContext() (*api.ServerConfig, addons.Server, addons.Target, error) {
	sc, err := s.serverConfig()
	if err != nil {
		return nil, addons.Server{}, addons.Target{}, err
	}
	if sc == nil {
		return nil, addons.Server{}, addons.Target{}, errNotCreated()
	}
	srv := addons.Server{Dir: s.dataDir(), Type: s.serverType(sc), MinecraftVersion: sc.MinecraftVersion, Owner: s.gameOwner()}
	t, err := addons.TargetFor(srv.Type)
	if err != nil {
		return nil, addons.Server{}, addons.Target{}, addonError(err)
	}
	return sc, srv, t, nil
}

func (s *server) hasDataDir() bool {
	fi, err := os.Lstat(s.dataDir())
	return err == nil && fi.IsDir()
}

// Records.

const addonColumns = `source, project_id, slug, name, summary, icon_url, version_id, version_number, channel, published,
	file_name, hash_algo, hash, size_bytes, dependency_of, requires, installed_at`

func (s *server) installedAddons() ([]addons.Installed, error) {
	rows, err := s.db.Query(`SELECT `+addonColumns+` FROM addons WHERE server_id = ? ORDER BY name COLLATE NOCASE, source, project_id`, s.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []addons.Installed{}
	for rows.Next() {
		var rec addons.Installed
		var src, requires string
		var published, installed int64
		if err := rows.Scan(&src, &rec.ProjectID, &rec.Slug, &rec.Name, &rec.Summary, &rec.IconURL, &rec.VersionID, &rec.VersionNumber,
			&rec.Channel, &published, &rec.FileName, &rec.HashAlgo, &rec.Hash, &rec.Size, &rec.DependencyOf, &requires, &installed); err != nil {
			return nil, err
		}
		rec.Source = addons.Source(src)
		if published > 0 {
			rec.Published = time.UnixMilli(published).UTC()
		}
		rec.InstalledAt = time.UnixMilli(installed).UTC()
		_ = json.Unmarshal([]byte(requires), &rec.Requires)
		out = append(out, rec)
	}
	return out, rows.Err()
}

// saveAddons stores the records an install or update returned and drops
// those of removed add-ons, in one transaction. changed records that what
// the server loads changed, so a running server needs a restart.
func (s *server) saveAddons(put []addons.Installed, drop []addons.Key, changed bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, k := range drop {
		if _, err := tx.Exec(`DELETE FROM addons WHERE server_id = ? AND source = ? AND project_id = ?`, s.id, string(k.Source), k.ProjectID); err != nil {
			return err
		}
	}
	for _, rec := range put {
		requires := rec.Requires
		if requires == nil {
			requires = []string{}
		}
		req, _ := json.Marshal(requires)
		var published int64
		if !rec.Published.IsZero() {
			published = rec.Published.UnixMilli()
		}
		if _, err := tx.Exec(`INSERT INTO addons(server_id, `+addonColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(server_id, source, project_id) DO UPDATE SET slug=excluded.slug, name=excluded.name, summary=excluded.summary,
			icon_url=excluded.icon_url, version_id=excluded.version_id, version_number=excluded.version_number, channel=excluded.channel,
			published=excluded.published, file_name=excluded.file_name, hash_algo=excluded.hash_algo, hash=excluded.hash,
			size_bytes=excluded.size_bytes, dependency_of=excluded.dependency_of, requires=excluded.requires, installed_at=excluded.installed_at`,
			s.id, string(rec.Source), rec.ProjectID, rec.Slug, rec.Name, rec.Summary, rec.IconURL, rec.VersionID, rec.VersionNumber, rec.Channel,
			published, rec.FileName, rec.HashAlgo, rec.Hash, rec.Size, rec.DependencyOf, string(req), rec.InstalledAt.UnixMilli()); err != nil {
			return err
		}
	}
	if changed {
		if _, err := tx.Exec(`UPDATE servers SET addons_changed_at = ? WHERE id = ?`, s.now().UnixMilli(), s.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *server) addonsChangedAt() (time.Time, bool) {
	var v sql.NullInt64
	if err := s.db.QueryRow(`SELECT addons_changed_at FROM servers WHERE id = ?`, s.id).Scan(&v); err != nil || !v.Valid {
		return time.Time{}, false
	}
	return time.UnixMilli(v.Int64).UTC(), true
}

// startedAt is when the running server's container started.
func (s *server) startedAt(ctx context.Context) (time.Time, bool) {
	c, running, err := s.containerRunning(ctx)
	if err != nil || !running {
		return time.Time{}, false
	}
	return c.State.Started()
}

// Conversions to the API's types.

func apiAddon(rec addons.Installed) api.Addon {
	return api.Addon{
		Source: string(rec.Source), ProjectID: rec.ProjectID, Slug: rec.Slug, Name: rec.Name, Summary: rec.Summary, IconURL: rec.IconURL,
		VersionID: rec.VersionID, VersionNumber: rec.VersionNumber, Channel: rec.Channel, Published: rec.Published,
		FileName: rec.FileName, Size: rec.Size, DependencyOf: rec.DependencyOf, InstalledAt: rec.InstalledAt,
	}
}

func apiAddons(recs []addons.Installed) []api.Addon {
	out := make([]api.Addon, 0, len(recs))
	for _, rec := range recs {
		out = append(out, apiAddon(rec))
	}
	return out
}

func apiNotice(n addons.Notice) api.AddonNotice {
	return api.AddonNotice{Kind: string(n.Kind), Params: n.Params, Message: n.Msg, Hint: n.Hint}
}

func apiNotices(ns []addons.Notice) []api.AddonNotice {
	out := make([]api.AddonNotice, 0, len(ns))
	for _, n := range ns {
		out = append(out, apiNotice(n))
	}
	return out
}

func apiManual(m addons.ManualStep) api.AddonNotice {
	n := apiNotice(m.Notice)
	n.URL = webLink(m.URL)
	return n
}

// noticeOf is the notice of an add-on library error, or nil for any other
// error.
func noticeOf(err error) *api.AddonNotice {
	var e *addons.Error
	if !errors.As(err, &e) {
		return nil
	}
	n := apiNotice(e.Notice)
	return &n
}

// webLink returns raw when it is a plain web address the UI may link to.
func webLink(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" || u.User != nil || len(raw) > 2048 {
		return ""
	}
	return u.String()
}

func apiVersion(v *addons.VersionInfo) *api.AddonVersion {
	if v == nil {
		return nil
	}
	return &api.AddonVersion{VersionID: v.VersionID, VersionNumber: v.VersionNumber, Channel: v.Channel, Published: v.Published,
		FileName: v.FileName, Size: v.Size, ExternalURL: webLink(v.ExternalURL)}
}

func apiCard(c addons.Card, installed []addons.Installed) api.AddonCard {
	cats := c.Categories
	if cats == nil {
		cats = []string{}
	}
	return api.AddonCard{
		Source: string(c.Source), ProjectID: c.ProjectID, Slug: c.Slug, Name: c.Name, Author: c.Author, Summary: c.Summary,
		Categories: cats, License: c.License, Downloads: c.Downloads, IconURL: c.IconURL, Updated: c.Updated, PageURL: webLink(c.PageURL),
		Installed: addons.InstalledCard(c, installed),
	}
}

func apiTarget(t addons.Target, mc string) api.AddonTarget {
	out := api.AddonTarget{Kind: t.Kind, Folder: t.Folder, Sources: []string{}, Categories: addons.Categories(t), MinecraftVersion: mc}
	for _, src := range t.Sources() {
		out.Sources = append(out.Sources, string(src))
	}
	return out
}

// neededByName is the name of the add-on a plan's dependency step is for.
func neededByName(steps []addons.Step, installed []addons.Installed, st addons.Step) string {
	if st.DependencyOf == "" {
		return ""
	}
	for _, o := range steps {
		if o.Source == st.Source && o.ProjectID == st.DependencyOf {
			return o.Name
		}
	}
	for _, rec := range installed {
		if rec.Source == st.Source && rec.ProjectID == st.DependencyOf {
			return rec.Name
		}
	}
	return ""
}

func apiPlan(p *addons.Plan, installed []addons.Installed) *api.AddonPlan {
	out := &api.AddonPlan{Steps: []api.AddonStep{}, Manual: []api.AddonNotice{}, Blockers: apiNotices(p.Blockers), Warnings: apiNotices(p.Warnings),
		Ready: p.Ready, Fingerprint: p.Fingerprint}
	for _, st := range p.Steps {
		as := api.AddonStep{Action: string(st.Action), Source: string(st.Source), ProjectID: st.ProjectID, Name: st.Name,
			VersionNumber: st.VersionNumber, Channel: st.Channel, FileName: st.FileName, Size: st.Size, NeededBy: neededByName(p.Steps, installed, st)}
		if st.Replaces != nil {
			as.Was = st.Replaces.VersionNumber
		}
		out.Steps = append(out.Steps, as)
	}
	for _, m := range p.Manual {
		out.Manual = append(out.Manual, apiManual(m))
	}
	return out
}

// apiFile is a jar in the add-on folder. A file Playkeeper put in place after
// the running server started loads at the next restart.
func apiFile(e addons.ScanEntry, running bool, started time.Time) api.AddonFile {
	f := api.AddonFile{FileName: e.FileName, Size: e.Size, Status: string(e.Status), Name: e.Meta.Name, Version: e.Meta.Version}
	if f.Name == "" && e.Meta.Kind == "plugin" {
		// A plugin's name in plugin.yml is also its ID.
		f.Name = e.Meta.ID
	}
	switch {
	case e.Installed != nil:
		a := apiAddon(*e.Installed)
		f.Addon = &a
		f.Pending = running && e.Installed.InstalledAt.After(started)
	case e.Identified != nil:
		a := apiAddon(*e.Identified)
		f.Addon = &a
	}
	return f
}

// addonError maps an add-on library error to an HTTP response, keeping its
// kind as the error code.
func addonError(err error) error {
	var e *addons.Error
	if !errors.As(err, &e) {
		return err
	}
	status := http.StatusConflict
	switch e.Kind {
	case addons.KindInvalid, addons.KindBadFileName, addons.KindIconRefused, addons.KindHostNotAllowed, addons.KindNotHTTPS:
		status = http.StatusBadRequest
	case addons.KindNotFound, addons.KindNotManaged:
		status = http.StatusNotFound
	case addons.KindRateLimited:
		status = http.StatusTooManyRequests
	case addons.KindUnreachable, addons.KindUpstream, addons.KindRedirectRefused:
		status = http.StatusBadGateway
	}
	return &apiError{Status: status, Code: string(e.Kind), Msg: e.Msg, Hint: e.Hint}
}

// Reading.

// hAddons lists what is in the server's add-on folder. It reads only the
// folder; the sources are asked in hAddonChecks.
func (s *server) hAddons(w http.ResponseWriter, r *http.Request) {
	sc, srv, t, err := s.addonContext()
	if err != nil {
		writeError(w, err)
		return
	}
	installed, err := s.installedAddons()
	if err != nil {
		writeError(w, err)
		return
	}
	out := api.Addons{Target: apiTarget(t, sc.MinecraftVersion), Files: []api.AddonFile{}, Missing: []api.Addon{}, Warnings: []api.AddonNotice{}}
	if sc.Modpack != nil && !sc.Modpack.Pending {
		m := *sc.Modpack
		out.Modpack = &m
	}
	pack, err := s.packFiles(t.Folder)
	if err != nil {
		writeError(w, err)
		return
	}
	started, running := s.startedAt(r.Context())
	if s.hasDataDir() {
		mapRecs := s.mapAddons(installed)
		res, err := s.lib().Scan(r.Context(), srv, append(slices.Clone(installed), mapRecs...), false)
		if err != nil {
			writeError(w, addonError(err))
			return
		}
		for _, e := range res.Entries {
			f := apiFile(e, running, started)
			if e.Installed != nil && isMapAddon(mapRecs, e.Installed.Key()) {
				// The Map tab asks for the restart that loads it.
				f.Addon.UsedBy, f.Pending = api.UsedByMap, false
			}
			if e.Installed == nil && pack[e.FileName] {
				f.Status = api.AddonFromPack
			}
			out.Files = append(out.Files, f)
		}
		// The Map tab says when the map's own files are gone.
		missing := slices.DeleteFunc(res.Missing, func(rec addons.Installed) bool { return isMapAddon(mapRecs, rec.Key()) })
		out.Missing, out.Warnings = apiAddons(missing), apiNotices(res.Warnings)
	} else {
		out.Missing = apiAddons(installed)
	}
	if changed, ok := s.addonsChangedAt(); ok && running {
		out.RestartNeeded = changed.After(started)
	}
	writeJSON(w, http.StatusOK, out)
}

// addonChecks caches what the sources said about a server's add-ons, keyed
// by the records and the folder's files, so opening the tab again does not
// ask them again.
type addonChecks struct {
	mu  sync.Mutex
	key string
	at  time.Time
	val *api.AddonChecks
}

// hAddonChecks asks the sources for newer versions of the installed add-ons,
// and Modrinth which of the files added by hand it knows.
func (s *server) hAddonChecks(w http.ResponseWriter, r *http.Request) {
	_, srv, t, err := s.addonContext()
	if err != nil {
		writeError(w, err)
		return
	}
	installed, err := s.installedAddons()
	if err != nil {
		writeError(w, err)
		return
	}
	pack, err := s.packFiles(t.Folder)
	if err != nil {
		writeError(w, err)
		return
	}
	out := &api.AddonChecks{Updates: []api.AddonUpdate{}, Identified: []api.AddonFile{}, CheckedAt: s.now().UTC()}
	if !s.hasDataDir() {
		writeJSON(w, http.StatusOK, out)
		return
	}
	// The map's files count as known, so none is offered to manage; their
	// updates are the map's.
	withMap := append(slices.Clone(installed), s.mapAddons(installed)...)
	res, err := s.lib().Scan(r.Context(), srv, withMap, false)
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	type file struct {
		Name   string
		Size   int64
		Status addons.FileStatus
	}
	type record struct{ Source, Project, Version, File string }
	var state struct {
		Files   []file
		Records []record
		Pack    []string
	}
	state.Pack = slices.Sorted(maps.Keys(pack))
	unknown := false
	for _, e := range res.Entries {
		state.Files = append(state.Files, file{e.FileName, e.Size, e.Status})
		unknown = unknown || e.Status == addons.FileUnknown && !pack[e.FileName]
	}
	for _, rec := range installed {
		state.Records = append(state.Records, record{string(rec.Source), rec.ProjectID, rec.VersionID, rec.FileName})
	}
	b, _ := json.Marshal(state)
	sum := sha256.Sum256(b)
	key := hex.EncodeToString(sum[:])

	c := &s.checks
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.val != nil && c.key == key && s.now().Sub(c.at) < addonChecksTTL {
		writeJSON(w, http.StatusOK, c.val)
		return
	}
	if len(installed) > 0 {
		updates, err := s.lib().CheckUpdates(r.Context(), srv, installed)
		if err != nil {
			writeError(w, addonError(err))
			return
		}
		for _, u := range updates {
			au := api.AddonUpdate{Source: string(u.Source), ProjectID: u.ProjectID, Latest: apiVersion(u.Latest), Available: u.Available}
			if u.Notice != nil {
				n := apiNotice(*u.Notice)
				au.Notice = &n
			}
			out.Updates = append(out.Updates, au)
		}
	}
	if unknown {
		ident, err := s.lib().Scan(r.Context(), srv, withMap, true)
		if err != nil {
			writeError(w, addonError(err))
			return
		}
		for _, e := range ident.Entries {
			if e.Status == addons.FileIdentified && !pack[e.FileName] {
				out.Identified = append(out.Identified, apiFile(e, false, time.Time{}))
			}
		}
	}
	c.key, c.at, c.val = key, s.now(), out
	writeJSON(w, http.StatusOK, out)
}

// browseCache keeps library pages for a little while, for every server of
// the same type and Minecraft version.
type browseCache struct {
	mu      sync.Mutex
	entries map[string]browseEntry
}

type browseEntry struct {
	res *addons.BrowseResults
	at  time.Time
}

func (c *browseCache) get(key string, now time.Time) *addons.BrowseResults {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok && now.Sub(e.at) < browseTTL {
		return e.res
	}
	return nil
}

func (c *browseCache) put(key string, res *addons.BrowseResults, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]browseEntry{}
	}
	for k, e := range c.entries {
		if now.Sub(e.at) >= browseTTL {
			delete(c.entries, k)
		}
	}
	for len(c.entries) >= browseEntries {
		oldest := ""
		for k, e := range c.entries {
			if oldest == "" || e.at.Before(c.entries[oldest].at) {
				oldest = k
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[key] = browseEntry{res: res, at: now}
}

// searchText checks the words typed into the library's search.
func searchText(q string) (string, error) {
	q = strings.TrimSpace(q)
	if utf8.RuneCountInString(q) > maxSearchText {
		return "", errInvalid("Searches can be at most %d characters.", maxSearchText)
	}
	for _, r := range q {
		if !unicode.IsPrint(r) {
			return "", errInvalid("Searches may not contain control characters.")
		}
	}
	return q, nil
}

// hAddonSearch is one page of the library for the server: every source
// with add-ons for it, merged.
func (s *server) hAddonSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	text, err := searchText(q.Get("q"))
	if err != nil {
		writeError(w, err)
		return
	}
	page := 0
	if v := q.Get("page"); v != "" {
		if page, err = strconv.Atoi(v); err != nil || page < 0 || page > maxSearchPage {
			writeError(w, errInvalid("That page of results is out of range."))
			return
		}
	}
	sc, srv, _, err := s.addonContext()
	if err != nil {
		writeError(w, err)
		return
	}
	query := addons.BrowseQuery{Text: text, Category: q.Get("category"), Sort: q.Get("sort"), Page: page}
	key := strings.Join([]string{srv.Type, sc.MinecraftVersion, query.Text, query.Category, query.Sort, strconv.Itoa(page)}, "\x00")
	res := s.browse.get(key, s.now())
	if res == nil {
		if res, err = s.lib().Browse(r.Context(), srv, query); err != nil {
			writeError(w, addonError(err))
			return
		}
		if len(res.Unanswered) == 0 {
			s.browse.put(key, res, s.now())
		}
	}
	installed, err := s.installedAddons()
	if err != nil {
		writeError(w, err)
		return
	}
	withMap := append(slices.Clone(installed), s.mapAddons(installed)...)
	out := api.AddonBrowse{Cards: []api.AddonCard{}, More: res.More, Unanswered: apiNotices(res.Unanswered)}
	for _, c := range res.Cards {
		out.Cards = append(out.Cards, apiCard(c, withMap))
	}
	writeJSON(w, http.StatusOK, out)
}

// hAddonDetails is one add-on for its detail sheet: the card, the newest
// version for the server, and either what installing it would do or, when
// it is installed, the record and whether an update is available.
func (s *server) hAddonDetails(w http.ResponseWriter, r *http.Request) {
	key, err := parseAddonKey(r.PathValue("source"), r.PathValue("project"))
	if err != nil {
		writeError(w, err)
		return
	}
	_, srv, _, err := s.addonContext()
	if err != nil {
		writeError(w, err)
		return
	}
	installed, err := s.installedAddons()
	if err != nil {
		writeError(w, err)
		return
	}
	mapRecs := s.mapAddons(installed)
	withMap := append(slices.Clone(installed), mapRecs...)
	lib := s.lib()
	d, err := lib.Details(r.Context(), srv, key.Source, key.ProjectID)
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	rec := addons.InstalledRecord(d.Card, withMap)
	if rec != nil && rec.Key() != (addons.Key{Source: d.Card.Source, ProjectID: d.Card.ProjectID}) {
		// The same add-on, installed from its listing on the other source:
		// that listing's versions are the ones that matter.
		if other, err := lib.Details(r.Context(), srv, rec.Source, rec.ProjectID); err == nil {
			d = other
		}
	}
	out := api.AddonDetails{Card: apiCard(d.Card, withMap), Latest: apiVersion(d.Latest), Notes: d.Notes, Ports: s.addonPorts(key)}
	if d.Notice != nil {
		n := apiNotice(*d.Notice)
		out.Notice = &n
	}
	switch {
	case rec != nil && isMapAddon(mapRecs, rec.Key()):
		a := apiAddon(*rec)
		a.UsedBy = api.UsedByMap
		out.Installed = &a
	case rec != nil:
		a := apiAddon(*rec)
		out.Installed = &a
		if p, err := lib.PreviewUninstall(r.Context(), srv, installed, rec.Key()); err == nil {
			out.Changed, out.Missing = p.Changed, p.Missing
		}
		l := d.Latest
		out.UpdateAvailable = l != nil && l.ExternalURL == "" && l.VersionID != rec.VersionID && (rec.Published.IsZero() || l.Published.After(rec.Published))
	case d.Latest != nil && d.Latest.ExternalURL == "":
		p, err := lib.PlanInstall(r.Context(), srv, installed, addons.InstallRequest{Source: d.Card.Source, Project: d.Card.ProjectID})
		if err != nil {
			n := noticeOf(err)
			if n == nil {
				writeError(w, err)
				return
			}
			out.PlanError = n
		} else {
			out.Plan = apiPlan(p, installed)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// hAddonRemovePreview says what removing an add-on would involve.
func (s *server) hAddonRemovePreview(w http.ResponseWriter, r *http.Request) {
	key, err := parseAddonKey(r.PathValue("source"), r.PathValue("project"))
	if err != nil {
		writeError(w, err)
		return
	}
	_, srv, _, err := s.addonContext()
	if err != nil {
		writeError(w, err)
		return
	}
	installed, err := s.installedAddons()
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.refuseMapAddons(installed, key); err != nil {
		writeError(w, err)
		return
	}
	p, err := s.lib().PreviewUninstall(r.Context(), srv, installed, key)
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	writeJSON(w, http.StatusOK, api.AddonRemovePreview{Addon: apiAddon(p.Record), NeededBy: p.NeededBy, Orphans: apiAddons(p.Orphans),
		ConfigFolder: p.ConfigFolder, Changed: p.Changed, Missing: p.Missing})
}

// Installing and updating run as the server's operation, so they never
// overlap a start, a backup or a Minecraft update, and their progress is the
// operation's detail.

func (s *server) hAddonInstall(w http.ResponseWriter, r *http.Request) {
	var req api.AddonInstallRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := parseAddonKey(req.Source, req.ProjectID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := confirmedPlan(req.Fingerprint); err != nil {
		writeError(w, err)
		return
	}
	if _, _, _, err := s.addonContext(); err != nil {
		writeError(w, err)
		return
	}
	if err := s.refuseMapKeys(key); err != nil {
		writeError(w, err)
		return
	}
	voice := voiceChat(key)
	if voice && !req.OpenPorts {
		writeError(w, errInvalid("Voice chat needs a UDP port of its own, so Playkeeper installs it only when it may open that port too."))
		return
	}
	op, err := s.beginOp("addon-install", actor, func(ctx context.Context, h *opHandle) error {
		err := s.addonJob(ctx, h, actor, req.Start, func(srv addons.Server, installed []addons.Installed, progress func(addons.Progress)) (*addons.Result, error) {
			return s.lib().Install(ctx, srv, installed, addons.InstallRequest{Source: key.Source, Project: key.ProjectID, Fingerprint: req.Fingerprint, OnProgress: progress})
		})
		if err != nil || !voice {
			return err
		}
		return s.openVoiceChat(ctx, h, actor)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// hAddonUpdatePlan says what an update would do, for the user to confirm.
func (s *server) hAddonUpdatePlan(w http.ResponseWriter, r *http.Request) {
	var req api.AddonUpdatePlanRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if _, err := validActor(req.Actor); err != nil {
		writeError(w, err)
		return
	}
	keys, err := updateKeys(req.Addons)
	if err != nil {
		writeError(w, err)
		return
	}
	_, srv, _, err := s.addonContext()
	if err != nil {
		writeError(w, err)
		return
	}
	installed, err := s.installedAddons()
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.refuseMapAddons(installed, keys...); err != nil {
		writeError(w, err)
		return
	}
	p, err := s.lib().PlanUpdate(r.Context(), srv, installed, addons.UpdateRequest{Keys: keys, Changed: req.Changed})
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	writeJSON(w, http.StatusOK, apiPlan(p, installed))
}

func (s *server) hAddonUpdate(w http.ResponseWriter, r *http.Request) {
	var req api.AddonUpdateRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	keys, err := updateKeys(req.Addons)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := confirmedPlan(req.Fingerprint); err != nil {
		writeError(w, err)
		return
	}
	if _, _, _, err := s.addonContext(); err != nil {
		writeError(w, err)
		return
	}
	if err := s.refuseMapKeys(keys...); err != nil {
		writeError(w, err)
		return
	}
	op, err := s.beginOp("addon-update", actor, func(ctx context.Context, h *opHandle) error {
		return s.addonJob(ctx, h, actor, req.Start, func(srv addons.Server, installed []addons.Installed, progress func(addons.Progress)) (*addons.Result, error) {
			return s.lib().Update(ctx, srv, installed, addons.UpdateRequest{Keys: keys, Changed: req.Changed, Fingerprint: req.Fingerprint, OnProgress: progress})
		})
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// addonJob runs an install or update and says whether the running server
// needs a restart to load it. With start, a stopped server is started once
// the files are in place; if they can't be, it stays stopped and keeps its
// crash explanation.
func (s *server) addonJob(ctx context.Context, h *opHandle, actor string, start bool, run func(addons.Server, []addons.Installed, func(addons.Progress)) (*addons.Result, error)) error {
	if err := s.installAddons(ctx, h, actor, run); err != nil {
		return err
	}
	_, running, err := s.containerRunning(ctx)
	if start {
		if err != nil {
			return err
		}
		if !running {
			return s.startNow(ctx, h)
		}
	}
	h.set("restartNeeded", running)
	return nil
}

// installAddons runs an install or update and stores its records. The files
// are in the folder only once every download matched its published hash.
func (s *server) installAddons(ctx context.Context, h *opHandle, actor string, run func(addons.Server, []addons.Installed, func(addons.Progress)) (*addons.Result, error)) error {
	_, srv, _, err := s.addonContext()
	if err != nil {
		return err
	}
	if err := s.ensureDirs(); err != nil {
		return err
	}
	installed, err := s.installedAddons()
	if err != nil {
		return err
	}
	h.phase("planning")
	t := &addonTracker{h: h, now: s.now, installed: installed}
	res, err := run(srv, installed, t.progress)
	if err != nil {
		t.fail()
		if n := noticeOf(err); n != nil {
			h.set("notice", *n)
			return &apiError{Msg: n.Message, Hint: n.Hint}
		}
		return err
	}
	if err := s.saveAddons(res.Installed, nil, true); err != nil {
		return err
	}
	was := map[addons.Key]string{}
	for _, old := range res.Replaced {
		was[old.Key()] = old.VersionNumber
	}
	for _, rec := range res.Installed {
		target := string(rec.Source) + ":" + rec.ProjectID
		if v, ok := was[rec.Key()]; ok {
			s.audit(actor, "addon.updated", target, "succeeded", fmt.Sprintf("%s %s → %s", rec.Name, v, rec.VersionNumber))
		} else {
			s.audit(actor, "addon.installed", target, "succeeded", rec.Name+" "+rec.VersionNumber)
		}
	}
	if len(res.Manual) > 0 {
		manual := make([]api.AddonNotice, 0, len(res.Manual))
		for _, m := range res.Manual {
			manual = append(manual, apiManual(m))
		}
		h.set("manual", manual)
	}
	return nil
}

// addonTracker publishes an install's progress as its operation's "files":
// every file of the plan, at most once a second while bytes arrive.
type addonTracker struct {
	h         *opHandle
	now       func() time.Time
	installed []addons.Installed
	files     []api.AddonProgress
	last      time.Time
}

func (t *addonTracker) progress(p addons.Progress) {
	if t.files == nil {
		if p.Plan == nil {
			return
		}
		t.files = make([]api.AddonProgress, 0, len(p.Plan.Steps))
		for _, st := range p.Plan.Steps {
			f := api.AddonProgress{Name: st.Name, VersionNumber: st.VersionNumber, NeededBy: neededByName(p.Plan.Steps, t.installed, st), Size: st.Size, State: "waiting"}
			if st.Replaces != nil {
				f.Was = st.Replaces.VersionNumber
			}
			t.files = append(t.files, f)
		}
		t.h.phase("downloading")
		t.publish()
	}
	if p.Step < 0 || p.Step >= len(t.files) {
		return
	}
	f := &t.files[p.Step]
	state, received := "downloading", p.Received
	if p.Verified {
		state, received = "verified", f.Size
	}
	changed := f.State != state
	f.State, f.Received = state, received
	if changed || t.now().Sub(t.last) >= time.Second {
		t.publish()
	}
}

func (t *addonTracker) fail() {
	if t.files == nil {
		return
	}
	for i := range t.files {
		if t.files[i].State == "downloading" {
			t.files[i].State = "failed"
		}
	}
	t.publish()
}

func (t *addonTracker) publish() {
	t.last = t.now()
	t.h.set("files", slices.Clone(t.files))
}

// Removing, adopting and forgetting are quick and take the operation lock
// only while they run.

func (s *server) hAddonRemove(w http.ResponseWriter, r *http.Request) {
	var req api.AddonRemoveRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := parseAddonKey(req.Source, req.ProjectID)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(req.Orphans) > maxUpdateKeys {
		writeError(w, errInvalid("Too many add-ons to remove at once."))
		return
	}
	var extra []addons.Key
	for _, o := range req.Orphans {
		k, err := parseAddonKey(o.Source, o.ProjectID)
		if err != nil {
			writeError(w, err)
			return
		}
		extra = append(extra, k)
	}
	release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	defer release()
	if err := s.machineBusy(); err != nil {
		writeError(w, err)
		return
	}
	_, srv, _, err := s.addonContext()
	if err != nil {
		writeError(w, err)
		return
	}
	installed, err := s.installedAddons()
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.refuseMapAddons(installed, append([]addons.Key{key}, extra...)...); err != nil {
		writeError(w, err)
		return
	}
	lib := s.lib()
	preview, err := lib.PreviewUninstall(r.Context(), srv, installed, key)
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	for _, k := range extra {
		if !slices.ContainsFunc(preview.Orphans, func(rec addons.Installed) bool { return rec.Key() == k }) {
			writeError(w, errInvalid("Only add-ons that nothing else needs can be removed along with %s.", preview.Record.Name))
			return
		}
	}
	// Voice chat's port closes before anything is removed: a removal that
	// can't close it removes nothing, so no start publishes a port with nothing
	// behind it. If voice chat then stays, so does its port.
	var voicePort int
	if voiceChat(key) || slices.ContainsFunc(extra, voiceChat) {
		if voicePort, err = s.closeVoiceChat(actor); err != nil {
			writeError(w, err)
			return
		}
	}
	target := string(key.Source) + ":" + key.ProjectID
	rm, err := lib.Uninstall(r.Context(), srv, installed, key, addons.UninstallOptions{RemoveConfig: !req.KeepConfig, Force: req.Force, Changed: req.Changed})
	if err != nil {
		s.reopenVoiceChat(voicePort, actor)
		s.audit(actor, "addon.removed", target, "refused", err.Error())
		writeError(w, addonError(err))
		return
	}
	drop := []addons.Key{key}
	removed := []addons.Installed{rm.Removed}
	warnings := rm.Warnings
	rest := slices.DeleteFunc(slices.Clone(installed), func(rec addons.Installed) bool { return rec.Key() == key })
	for _, k := range extra {
		orm, err := lib.Uninstall(r.Context(), srv, rest, k, addons.UninstallOptions{RemoveConfig: !req.KeepConfig})
		if err != nil {
			if n := noticeOf(err); n != nil {
				warnings = append(warnings, addons.Notice{Kind: addons.Kind(n.Kind), Params: n.Params, Msg: n.Message, Hint: n.Hint})
				continue
			}
			s.log.Warn("could not remove an add-on nothing needs", "server", s.id, "err", err)
			continue
		}
		drop = append(drop, k)
		removed = append(removed, orm.Removed)
		warnings = append(warnings, orm.Warnings...)
		rest = slices.DeleteFunc(rest, func(rec addons.Installed) bool { return rec.Key() == k })
	}
	if err := s.saveAddons(nil, drop, true); err != nil {
		writeError(w, err)
		return
	}
	out := api.AddonRemoval{Removed: []string{}, Warnings: apiNotices(warnings)}
	for _, rec := range removed {
		out.Removed = append(out.Removed, rec.Name)
		s.audit(actor, "addon.removed", string(rec.Source)+":"+rec.ProjectID, "succeeded", rec.Name+" "+rec.VersionNumber)
	}
	if !slices.ContainsFunc(drop, voiceChat) {
		s.reopenVoiceChat(voicePort, actor)
	}
	writeJSON(w, http.StatusOK, out)
}

// hAddonAdopt lets Playkeeper manage a file added by hand that Modrinth
// recognizes by its hash: from now on it can update and remove it.
func (s *server) hAddonAdopt(w http.ResponseWriter, r *http.Request) {
	var req api.AddonAdoptRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if req.FileName == "" || len(req.FileName) > 255 || strings.ContainsAny(req.FileName, `/\`) || !strings.HasSuffix(req.FileName, ".jar") {
		writeError(w, errInvalid("That is not a file in the add-on folder."))
		return
	}
	release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	defer release()
	if err := s.machineBusy(); err != nil {
		writeError(w, err)
		return
	}
	sc, srv, t, err := s.addonContext()
	if err != nil {
		writeError(w, err)
		return
	}
	installed, err := s.installedAddons()
	if err != nil {
		writeError(w, err)
		return
	}
	pack, err := s.packFiles(t.Folder)
	if err != nil {
		writeError(w, err)
		return
	}
	if pack[req.FileName] && sc.Modpack != nil {
		writeError(w, errConflict(req.FileName+" is part of "+sc.Modpack.Name+", so it stays with the pack.", ""))
		return
	}
	mapRecs := s.mapAddons(installed)
	res, err := s.lib().Scan(r.Context(), srv, append(slices.Clone(installed), mapRecs...), true)
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	i := slices.IndexFunc(res.Entries, func(e addons.ScanEntry) bool { return e.FileName == req.FileName })
	switch {
	case i < 0:
		writeError(w, &apiError{Status: http.StatusNotFound, Code: api.CodeNotFound, Msg: req.FileName + " is not in the " + t.Folder + " folder."})
		return
	case res.Entries[i].Installed != nil && isMapAddon(mapRecs, res.Entries[i].Installed.Key()):
		writeError(w, errMapAddon(res.Entries[i].Installed.Name))
		return
	case res.Entries[i].Installed != nil:
		writeError(w, errConflict("Playkeeper already manages "+req.FileName+".", ""))
		return
	case res.Entries[i].Status != addons.FileIdentified:
		writeError(w, errConflict("Modrinth does not recognize "+req.FileName+", so Playkeeper cannot manage it.", "It stays in the folder as it is."))
		return
	}
	rec := *res.Entries[i].Identified
	if isMapAddon(mapRecs, rec.Key()) {
		writeError(w, errMapAddon(rec.Name))
		return
	}
	if slices.ContainsFunc(installed, func(o addons.Installed) bool { return o.Key() == rec.Key() }) {
		writeError(w, errConflict(rec.Name+" is already managed by Playkeeper as another file.", "Remove one of the two copies first."))
		return
	}
	// The file has been there since before Playkeeper managed it, so a
	// running server may have loaded it already.
	if root, err := os.OpenRoot(s.dataDir()); err == nil {
		if fi, err := root.Lstat(t.Folder + "/" + rec.FileName); err == nil && fi.ModTime().Before(rec.InstalledAt) {
			rec.InstalledAt = fi.ModTime().UTC()
		}
		root.Close()
	}
	if err := s.saveAddons([]addons.Installed{rec}, nil, false); err != nil {
		writeError(w, err)
		return
	}
	s.audit(actor, "addon.adopted", string(rec.Source)+":"+rec.ProjectID, "succeeded", rec.Name+" "+rec.VersionNumber+" ("+rec.FileName+")")
	writeJSON(w, http.StatusOK, apiAddon(rec))
}

// hAddonForget drops the record of an add-on whose file is gone.
func (s *server) hAddonForget(w http.ResponseWriter, r *http.Request) {
	var req api.AddonForgetRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := parseAddonKey(req.Source, req.ProjectID)
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
	_, srv, _, err := s.addonContext()
	if err != nil {
		writeError(w, err)
		return
	}
	installed, err := s.installedAddons()
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.refuseMapAddons(installed, key); err != nil {
		writeError(w, err)
		return
	}
	p, err := s.lib().PreviewUninstall(r.Context(), srv, installed, key)
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	if !p.Missing {
		writeError(w, errConflict(p.Record.FileName+" is still in the folder.", "Remove the add-on instead."))
		return
	}
	if err := s.saveAddons(nil, []addons.Key{key}, false); err != nil {
		writeError(w, err)
		return
	}
	s.audit(actor, "addon.forgotten", string(key.Source)+":"+key.ProjectID, "succeeded", p.Record.Name+" ("+p.Record.FileName+" was gone)")
	writeJSON(w, http.StatusOK, api.AddonRemoval{Removed: []string{p.Record.Name}, Warnings: []api.AddonNotice{}})
}

// Icons. The browser loads add-on icons from the panel, which gets them
// here: only from the sources' CDNs, size-limited, and only as PNG, JPEG,
// WebP or GIF.

type iconCache struct {
	mu      sync.Mutex
	entries map[string]iconEntry
	bytes   int
}

type iconEntry struct {
	icon *addons.Icon
	err  error
	at   time.Time
}

func (e iconEntry) fresh(now time.Time) bool {
	if e.err != nil {
		return now.Sub(e.at) < iconFailTTL
	}
	return now.Sub(e.at) < iconTTL
}

func (e iconEntry) size() int {
	if e.icon == nil {
		return 0
	}
	return len(e.icon.Data)
}

func (c *iconCache) get(ctx context.Context, rawURL string, now time.Time, fetch func(context.Context, string) (*addons.Icon, error)) (*addons.Icon, error) {
	c.mu.Lock()
	if e, ok := c.entries[rawURL]; ok && e.fresh(now) {
		c.mu.Unlock()
		return e.icon, e.err
	}
	c.mu.Unlock()
	icon, err := fetch(ctx, rawURL)
	if err != nil && addons.KindOf(err) == "" {
		return nil, err
	}
	c.put(rawURL, iconEntry{icon: icon, err: err, at: now})
	return icon, err
}

func (c *iconCache) put(key string, e iconEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]iconEntry{}
	}
	if old, ok := c.entries[key]; ok {
		c.bytes -= old.size()
		delete(c.entries, key)
	}
	for len(c.entries) > 0 && (len(c.entries) >= iconEntries || c.bytes+e.size() > iconBytes) {
		oldest := ""
		for k, o := range c.entries {
			if oldest == "" || o.at.Before(c.entries[oldest].at) {
				oldest = k
			}
		}
		c.bytes -= c.entries[oldest].size()
		delete(c.entries, oldest)
	}
	c.entries[key] = e
	c.bytes += e.size()
}

func (a *Agent) hAddonIcon(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	if raw == "" || len(raw) > maxAddonIcon {
		writeError(w, errInvalid("invalid icon address"))
		return
	}
	icon, err := a.icons.get(r.Context(), raw, a.now(), a.lib().FetchIcon)
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	w.Header().Set("Content-Type", icon.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(icon.Data)))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(icon.Data)
}
