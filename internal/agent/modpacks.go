package agent

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/minecraft/software"
	"github.com/CIYAhq/playkeeper/internal/modpacks"
	"github.com/CIYAhq/playkeeper/internal/modpacks/curseforge"
)

// Modpacks: finding one in the create flow, creating a server from it, and
// the pack on a server afterwards.

// packs is the modpack library: Modrinth, and CurseForge when the machine
// has a key.
func (a *Agent) packs() *modpacks.Library {
	if a.opts.Modpacks != nil {
		return a.opts.Modpacks
	}
	return a.packLib.Load()
}

// curseForgeKeyFile is the owner's own CurseForge API key, readable by root
// only. When it exists it wins over the key the build carries; empty turns
// CurseForge off.
func (a *Agent) curseForgeKeyFile() string {
	return filepath.Join(a.cfg.AgentDir(), "curseforge-key")
}

// loadPacks builds the modpack library with the CurseForge key in effect,
// and forgets what the sources said before.
func (a *Agent) loadPacks() {
	key, err := curseforge.LoadKey(a.curseForgeKeyFile(), curseforge.BuildKey)
	problem := ""
	if err != nil {
		a.log.Warn("CurseForge modpacks are not offered", "err", err)
		problem = sentence(err.Error())
	}
	lib := modpacks.New(a.opts.UpstreamClient, key)
	lib.TempDir = a.cfg.StagingDir()
	a.packKeyMu.Lock()
	a.packKey, a.packKeyProblem = key, problem
	a.packKeyMu.Unlock()
	a.packLib.Store(lib)
	a.packSearches.clear()
	a.packDetails.clear()
	a.packPreviews.clear()
}

// packMemoryMB is the memory Playkeeper suggests for a pack of n mods, or 0
// when n is unknown.
func packMemoryMB(n int) int {
	switch {
	case n <= 0:
		return 0
	case n < 50:
		return 4096
	case n < 300:
		return 6144
	}
	return 8192
}

var rePackID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// parsePackRef checks a pack's source, project and version as they come
// from the dashboard. version may be empty.
func parsePackRef(source, project, version string) (modpacks.Ref, error) {
	if source != string(addons.Modrinth) && source != string(modpacks.CurseForge) {
		return modpacks.Ref{}, errInvalid("Modpacks come from Modrinth or CurseForge.")
	}
	if !rePackID.MatchString(project) || strings.Trim(project, ".") == "" {
		return modpacks.Ref{}, errInvalid("That is not a valid modpack id.")
	}
	if version != "" && (!rePackID.MatchString(version) || strings.Trim(version, ".") == "") {
		return modpacks.Ref{}, errInvalid("That is not a valid modpack version id.")
	}
	return modpacks.Ref{Source: addons.Source(source), Project: project, Version: version}, nil
}

func apiPackCard(c modpacks.Card) api.ModpackCard {
	out := api.ModpackCard{
		Source: string(c.Source), ProjectID: c.ProjectID, Slug: c.Slug, Name: c.Name, Author: c.Author, Summary: c.Summary,
		Downloads: c.Downloads, IconURL: webLink(c.IconURL), Updated: c.Updated, PageURL: webLink(c.PageURL),
		Types: c.Types, MinecraftVersions: c.MinecraftVersions, Mods: c.Mods, MemoryMB: packMemoryMB(c.Mods),
	}
	if out.Types == nil {
		out.Types = []string{}
	}
	if out.MinecraftVersions == nil {
		out.MinecraftVersions = []string{}
	}
	if c.Unavailable != nil {
		n := apiNotice(*c.Unavailable)
		out.Unavailable = &n
	}
	return out
}

func apiPackVersion(v modpacks.Version) api.ModpackVersion {
	out := api.ModpackVersion{ID: v.ID, Number: v.Number, Name: v.Name, Channel: v.Channel, Published: v.Published, Size: v.Size,
		Type: v.Type, MinecraftVersion: v.MinecraftVersion, Mods: v.Mods}
	if v.Unsupported != nil {
		n := apiNotice(*v.Unsupported)
		out.Unsupported = &n
	}
	return out
}

func (a *Agent) packSources() []string {
	out := []string{}
	for _, s := range a.packs().Sources() {
		out = append(out, string(s))
	}
	return out
}

// hModpackSearch is one page of packs from one source.
func (a *Agent) hModpackSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	text, err := searchText(q.Get("q"))
	if err != nil {
		writeError(w, err)
		return
	}
	query := modpacks.Query{Source: addons.Modrinth, Text: text, Type: q.Get("type"), MinecraftVersion: q.Get("version"), Sort: q.Get("sort")}
	if s := q.Get("source"); s != "" {
		query.Source = addons.Source(s)
	}
	for _, f := range []struct {
		name string
		to   *int
	}{{"offset", &query.Offset}, {"limit", &query.Limit}} {
		if v := q.Get(f.name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				writeError(w, errInvalid("%s must be a number.", f.name))
				return
			}
			*f.to = n
		}
	}
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d", query.Source, query.Text, query.Type, query.MinecraftVersion, query.Sort, query.Offset, query.Limit)
	if res, ok := a.packSearches.get(key, a.now()); ok {
		writeJSON(w, http.StatusOK, res)
		return
	}
	res, err := a.packs().Search(r.Context(), query)
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	out := &api.ModpackResults{Cards: make([]api.ModpackCard, 0, len(res.Cards)), Total: res.Total, Offset: res.Offset, Limit: res.Limit, Sources: a.packSources()}
	for _, c := range res.Cards {
		out.Cards = append(out.Cards, apiPackCard(c))
	}
	a.packSearches.put(key, out, a.now())
	writeJSON(w, http.StatusOK, out)
}

// packDetail is a pack's details sheet, kept for a little while so opening
// it and then creating a server from it asks the source once.
func (a *Agent) packDetail(ctx context.Context, ref modpacks.Ref) (*api.ModpackDetail, error) {
	key := string(ref.Source) + "\x00" + ref.Project
	if d, ok := a.packDetails.get(key, a.now()); ok {
		return d, nil
	}
	d, err := a.packs().Detail(ctx, ref.Source, ref.Project)
	if err != nil {
		return nil, addonError(err)
	}
	out := &api.ModpackDetail{ModpackCard: apiPackCard(d.Card), SourceURL: webLink(d.SourceURL), IssuesURL: webLink(d.IssuesURL), WikiURL: webLink(d.WikiURL),
		Headline: d.Headline, Versions: make([]api.ModpackVersion, 0, len(d.Versions))}
	for _, v := range d.Versions {
		out.Versions = append(out.Versions, apiPackVersion(v))
	}
	if n := modpacks.Newest(d.Versions); n != nil {
		out.Newest = n.ID
		out.Mods, out.MemoryMB = n.Mods, packMemoryMB(n.Mods)
	}
	a.packDetails.put(key, out, a.now())
	return out, nil
}

func (a *Agent) hModpackDetail(w http.ResponseWriter, r *http.Request) {
	ref, err := parsePackRef(r.PathValue("source"), r.PathValue("project"), "")
	if err != nil {
		writeError(w, err)
		return
	}
	d, err := a.packDetail(r.Context(), ref)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// hModpackPreview reads a pack version itself, as a new server would get
// it: the loader version it needs, the files it brings and anything that
// stops Playkeeper from installing it. Only the pack's archive is
// downloaded, into an empty folder that is removed afterwards.
func (a *Agent) hModpackPreview(w http.ResponseWriter, r *http.Request) {
	ref, err := parsePackRef(r.PathValue("source"), r.PathValue("project"), r.PathValue("version"))
	if err != nil {
		writeError(w, err)
		return
	}
	key := string(ref.Source) + "\x00" + ref.Project + "\x00" + ref.Version
	if p, ok := a.packPreviews.get(key, a.now()); ok {
		writeJSON(w, http.StatusOK, a.withPackPorts(p))
		return
	}
	select {
	case a.packPreviewSlots <- struct{}{}:
		defer func() { <-a.packPreviewSlots }()
	case <-r.Context().Done():
		writeError(w, r.Context().Err())
		return
	}
	dir, err := os.MkdirTemp(a.cfg.StagingDir(), "pack-preview-")
	if err != nil {
		writeError(w, err)
		return
	}
	defer os.RemoveAll(dir)
	pl, err := a.packs().PlanInstall(r.Context(), modpacks.Server{Server: addons.Server{Dir: dir}}, nil, modpacks.InstallRequest{Ref: ref})
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	out := &api.ModpackPreview{Type: pl.Requirements.Type, MinecraftVersion: pl.Requirements.MinecraftVersion, LoaderVersion: pl.Requirements.LoaderVersion,
		DownloadSize: pl.DownloadSize, Ready: pl.Ready, Blockers: apiNotices(pl.Blockers), Warnings: apiNotices(pl.Warnings), Manual: []api.AddonNotice{}}
	if java := minecraft.JavaFor(out.MinecraftVersion); out.MinecraftVersion != "" && java != minecraft.NewestJava {
		out.Java = java
	}
	for _, c := range pl.Changes {
		if c.Action == modpacks.ActionAdd {
			out.Files++
		}
	}
	for _, m := range pl.Manual {
		out.Manual = append(out.Manual, apiManual(m))
	}
	p := packPreview{preview: out, voiceChat: voiceChatInPack(pl)}
	a.packPreviews.put(key, p, a.now())
	writeJSON(w, http.StatusOK, a.withPackPorts(p))
}

// packPreview is a pack version's preview as it's kept for a while, with
// whether the pack brings voice chat: the port it would get depends on the
// machine's servers at the time (withPackPorts).
type packPreview struct {
	preview   *api.ModpackPreview
	voiceChat bool
}

// packCreateTarget is what a server created from a pack starts out as: the
// type and Minecraft version its source lists for the version, with that
// type's recommended build until the pack itself names its loader at the
// first start.
func (a *Agent) packCreateTarget(ctx context.Context, m api.ModpackRef) (restoreTarget, *api.ServerModpack, error) {
	ref, err := parsePackRef(m.Source, m.ProjectID, m.VersionID)
	if err != nil {
		return restoreTarget{}, nil, err
	}
	d, err := a.packDetail(ctx, ref)
	if err != nil {
		return restoreTarget{}, nil, err
	}
	if d.Unavailable != nil {
		return restoreTarget{}, nil, errInvalid("%s", d.Unavailable.Message)
	}
	id := ref.Version
	if id == "" {
		id = d.Newest
	}
	i := slices.IndexFunc(d.Versions, func(v api.ModpackVersion) bool { return v.ID == id })
	if id == "" || i < 0 {
		return restoreTarget{}, nil, errInvalid("Playkeeper can't install that version of %s. Pick the pack again.", d.Name)
	}
	v := d.Versions[i]
	if v.Unsupported != nil {
		return restoreTarget{}, nil, &apiError{Status: http.StatusBadRequest, Code: v.Unsupported.Kind, Msg: v.Unsupported.Message, Hint: v.Unsupported.Hint}
	}
	if v.Type == "" || v.MinecraftVersion == "" || !typeAvailable(v.Type) {
		return restoreTarget{}, nil, errInvalid("%s doesn't say which server it runs on, so Playkeeper can't create one for it.", d.Name)
	}
	rt, err := a.packTarget(ctx, v.Type, v.MinecraftVersion, "")
	if err != nil {
		return restoreTarget{}, nil, err
	}
	return rt, &api.ServerModpack{Source: d.Source, ProjectID: d.ProjectID, VersionID: v.ID, Name: d.Name, VersionNumber: v.Number,
		PageURL: d.PageURL, IconURL: d.IconURL, Mods: v.Mods, Pending: true, OpenPorts: m.OpenPorts}, nil
}

// packTarget is the software a pack runs on: the loader version it names,
// or the type's recommended build when it names none. A pack's loader may
// be a beta; the pack's authors chose it.
func (a *Agent) packTarget(ctx context.Context, typ, mc, loader string) (restoreTarget, error) {
	if !typeAvailable(typ) || typ == api.TypePaper {
		return restoreTarget{}, errInvalid("Playkeeper runs modpacks on Fabric, Quilt, NeoForge and Vanilla servers.")
	}
	rt := restoreTarget{typ: typ, pin: software.Pin{Type: typ, MinecraftVersion: mc}}
	channel := software.Stable
	if typ != software.Vanilla {
		bs, _, err := a.typeBuilds(ctx, typ, mc)
		if err != nil {
			return restoreTarget{}, softwareError(err)
		}
		i := -1
		if loader != "" {
			i = slices.IndexFunc(bs, func(b software.Build) bool { return b.Version == loader })
			if i < 0 {
				return restoreTarget{}, &apiError{Msg: fmt.Sprintf("The pack needs %s %s, which %s doesn't offer for Minecraft %s.", loaderName(typ), loader, typeName(typ), mc),
					Hint: "Pick another version of the pack, or ask its authors."}
			}
		} else if i = slices.IndexFunc(bs, func(b software.Build) bool { return b.Recommended }); i < 0 && len(bs) > 0 {
			i = 0
		}
		if i < 0 {
			return restoreTarget{}, errInvalid("%s has no build for Minecraft %s.", typeName(typ), mc)
		}
		rt.pin, channel = bs[i].Pin, bs[i].Channel
	}
	if err := rt.pin.Validate(); err != nil {
		return restoreTarget{}, softwareError(err)
	}
	p := api.SoftwarePin(rt.pin)
	rt.entry = api.CatalogEntry{ID: typ + "-" + mc, Label: typeName(typ) + " " + mc, MinecraftVersion: mc,
		Channel: string(channel), Experimental: channel != software.Stable, Supported: true, Software: &p, Build: pinBuild(rt.pin)}
	return rt, nil
}

// loaderName is what a type's build is called: "Fabric Loader".
func loaderName(typ string) string {
	switch typ {
	case software.Fabric:
		return "Fabric Loader"
	case software.Quilt:
		return "Quilt Loader"
	}
	return typeName(typ)
}

// installPendingPack puts the pack a server was created from on it before
// its first start: it reads the pack, pins the loader the pack names,
// installs that software, then downloads every file the pack lists and
// checks it against the pack's hash before anything is written. Until it
// succeeds, every start tries again.
func (s *server) installPendingPack(ctx context.Context, h *opHandle, sc *api.ServerConfig) error {
	p := *sc.Modpack
	h.phase("preparing_modpack")
	s.setRunPhase(api.PhaseDownloading, "")
	srv := modpacks.Server{Server: addons.Server{Dir: s.dataDir(), Owner: s.gameOwner()}, World: sc.LevelName}
	req := modpacks.InstallRequest{
		Ref: modpacks.Ref{Source: addons.Source(p.Source), Project: p.ProjectID, Version: p.VersionID},
		OnProgress: func(pr modpacks.Progress) {
			h.set("packFiles", pr.Done)
			h.set("packFilesTotal", pr.Total)
		},
	}
	prep, err := s.packs().PrepareInstall(ctx, srv, nil, req)
	if err != nil {
		return packFailure(h, err)
	}
	defer prep.Close()
	if len(prep.Plan.Blockers) > 0 {
		return packFailure(h, &addons.Error{Notice: prep.Plan.Blockers[0]})
	}
	reqs := prep.Plan.Requirements
	rt, err := s.packTarget(ctx, reqs.Type, reqs.MinecraftVersion, reqs.LoaderVersion)
	if err != nil {
		return err
	}
	next := rt.config(*sc)
	if err := s.saveServerConfig(next); err != nil {
		return err
	}
	*sc = next
	if err := s.ensureSoftware(ctx, h, sc); err != nil {
		return err
	}
	h.phase("installing_modpack")
	if err := s.applyPackSettings(prep.Plan.Properties); err != nil {
		return err
	}
	res, err := prep.Apply(ctx)
	if err != nil {
		return packFailure(h, err)
	}
	if err := s.packVoiceChat(h, sc, p, prep.Plan); err != nil {
		return err
	}
	if err := s.savePackRecord(res.Record); err != nil {
		return err
	}
	p.Pending, p.OpenPorts, p.VersionID, p.VersionNumber, p.Mods = false, false, res.Record.Pack.VersionID, res.Record.Pack.VersionNumber, packMods(res.Record)
	sc.Modpack = &p
	if err := s.saveServerConfig(*sc); err != nil {
		return err
	}
	s.audit(h.op.Actor, "modpack.installed", p.Source+":"+p.ProjectID, "succeeded", p.Name+" "+p.VersionNumber)
	s.recordEvent(s.now(), "modpack_installed", "", "playkeeper", fmt.Sprintf("%s %s: %d files checked", p.Name, p.VersionNumber, len(res.Record.Files)))
	if len(res.Manual) > 0 {
		manual := make([]api.AddonNotice, 0, len(res.Manual))
		for _, m := range res.Manual {
			manual = append(manual, apiManual(m))
		}
		h.set("manual", manual)
	}
	return nil
}

// applyPackSettings puts the server.properties settings a pack suggests
// (only ones about how the game plays; see modpacks.suggestible) in the
// server's server.properties, keeping its other lines. The file is read and
// written through gamefiles: a plugin or mod can put a link there, and root
// must not copy what it leads to into a file the game can read.
func (s *server) applyPackSettings(props map[string]string) error {
	if len(props) == 0 {
		return nil
	}
	d, err := s.gameFiles()
	if err != nil {
		return err
	}
	defer d.Close()
	cur, err := d.ReadProperties()
	if errors.Is(err, fs.ErrNotExist) {
		cur, err = nil, nil
	}
	if err == nil {
		err = d.WriteProperties(mergeProperties(cur, props))
	}
	if err != nil {
		return gameFileError(err, "The modpack's settings could not be saved, so the server was not started.")
	}
	return nil
}

// packFailure turns a pack's refusal into the operation's error, keeping
// its notice for the dashboard.
func packFailure(h *opHandle, err error) error {
	if n := noticeOf(err); n != nil {
		h.set("notice", *n)
		return &apiError{Msg: n.Message, Hint: n.Hint}
	}
	return err
}

// packFiles are the files in the add-on folder that the server's modpack
// put there, by name: the pack keeps them up to date, not the add-on
// library. Files that were on the server before the pack stay the user's.
func (s *server) packFiles(folder string) (map[string]bool, error) {
	rec, err := s.packRecord()
	if err != nil || rec == nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, f := range rec.Files {
		if dir, name := path.Split(f.Path); dir == folder+"/" && !f.Preexisting {
			out[name] = true
		}
	}
	return out, nil
}

// packMods counts the mods among a pack's files on the server.
func packMods(rec modpacks.Record) int {
	n := 0
	for _, f := range rec.Files {
		if strings.HasPrefix(f.Path, "mods/") && strings.HasSuffix(f.Path, ".jar") {
			n++
		}
	}
	return n
}

// packRecord is what Playkeeper stored for the pack on the server; nil when
// it has none.
func (s *server) packRecord() (*modpacks.Record, error) {
	var v string
	err := s.db.QueryRow(`SELECT record FROM modpacks WHERE server_id = ?`, s.id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec modpacks.Record
	if err := json.Unmarshal([]byte(v), &rec); err != nil {
		return nil, fmt.Errorf("the stored modpack record is damaged: %w", err)
	}
	return &rec, nil
}

func (s *server) savePackRecord(rec modpacks.Record) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return writePackRecord(s.db, s.id, rec, b, s.now())
}

// writePackRecord stores, through ex, rec as the record of the modpack
// files on the server with id; b is rec as JSON.
func writePackRecord(ex execer, id string, rec modpacks.Record, b []byte, at time.Time) error {
	now := at.UnixMilli()
	_, err := ex.Exec(`INSERT INTO modpacks(server_id, source, project_id, record, installed_at, updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(server_id) DO UPDATE SET source = excluded.source, project_id = excluded.project_id, record = excluded.record, updated_at = excluded.updated_at`,
		id, string(rec.Pack.Source), rec.Pack.ProjectID, string(b), now, now)
	return err
}

// packRecordJSON is the stored record of the server's modpack files, as
// JSON; nil when it has none.
func (s *server) packRecordJSON() (json.RawMessage, error) {
	var v string
	err := s.db.QueryRow(`SELECT record FROM modpacks WHERE server_id = ?`, s.id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(v), nil
}

// manifestModpack is the backup manifest setting that records the server's
// modpack: "none", or the pack and the files it put on the server as JSON
// (packBackup). Archives made before it was recorded don't say. It is a
// setting rather than a manifest field, so earlier Playkeeper versions still
// read the archive.
const manifestModpack = "modpack"

// packBackup is what a backup records of the server's modpack.
type packBackup struct {
	Modpack api.ServerModpack `json:"modpack"`
	Record  modpacks.Record   `json:"record"`
}

// packSetting is the manifest setting for the server's modpack, or "" when
// its record can't be read, which leaves the backup not saying.
func (s *server) packSetting(sc api.ServerConfig) string {
	if sc.Modpack == nil || sc.Modpack.Pending {
		return "none"
	}
	rec, err := s.packRecord()
	if err == nil && rec != nil {
		b, merr := json.Marshal(packBackup{Modpack: *sc.Modpack, Record: *rec})
		if merr == nil {
			return string(b)
		}
		err = merr
	}
	s.log.Warn("the modpack's record could not be read, so the backup doesn't say which pack the server ran", "server", s.id, "err", err)
	return ""
}

// restoredModpack gives a restored server the modpack its backup records,
// setting records it from the manifest, and returns the record of the pack's
// files to store with it; nil leaves the server without one. A backup that
// doesn't say leaves a server that could run a pack saying so, rather than
// keeping a pack its files may not have.
func restoredModpack(sc *api.ServerConfig, setting string, recorded bool) json.RawMessage {
	sc.Modpack, sc.ModpackUnknown = nil, false
	if recorded && setting == "none" {
		return nil
	}
	var pk packBackup
	if recorded && json.Unmarshal([]byte(setting), &pk) == nil && validPackBackup(pk) {
		if rec, err := json.Marshal(pk.Record); err == nil {
			mp := pk.Modpack
			mp.Pending = false
			sc.Modpack = &mp
			return rec
		}
	}
	if t, err := addons.TargetFor(cmp.Or(sc.Type, api.TypePaper)); err != nil || t.Kind != "plugin" {
		sc.ModpackUnknown = true
	}
	return nil
}

// validPackBackup reports whether a backup's record of its modpack is one
// Playkeeper could have made: a pack from Modrinth or CurseForge whose files
// are inside the server's folder. An uploaded archive can say anything.
func validPackBackup(pk packBackup) bool {
	m := pk.Modpack
	if _, err := parsePackRef(m.Source, m.ProjectID, m.VersionID); err != nil || string(pk.Record.Pack.Source) != m.Source || pk.Record.Pack.ProjectID != m.ProjectID {
		return false
	}
	for _, f := range pk.Record.Files {
		if !filepath.IsLocal(f.Path) {
			return false
		}
	}
	return true
}

// saveWithPack saves the server's settings and the record of its modpack's
// files in one transaction, as a restore swaps both with the world; a nil
// record removes the server's.
func (s *server) saveWithPack(sc api.ServerConfig, rec json.RawMessage) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := saveConfig(tx, s.id, sc); err != nil {
		return err
	}
	if len(rec) == 0 {
		_, err = tx.Exec(`DELETE FROM modpacks WHERE server_id = ?`, s.id)
	} else {
		var r modpacks.Record
		if err = json.Unmarshal(rec, &r); err == nil {
			err = writePackRecord(tx, s.id, r, rec, s.now())
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ttlCache keeps a few answers from the pack sources for a little while.
type ttlCache[V any] struct {
	ttl time.Duration
	max int

	mu      sync.Mutex
	entries map[string]ttlEntry[V]
}

type ttlEntry[V any] struct {
	v  V
	at time.Time
}

func newTTLCache[V any](ttl time.Duration, max int) *ttlCache[V] {
	return &ttlCache[V]{ttl: ttl, max: max, entries: map[string]ttlEntry[V]{}}
}

func (c *ttlCache[V]) get(key string, now time.Time) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok && now.Sub(e.at) < c.ttl {
		return e.v, true
	}
	var zero V
	return zero, false
}

func (c *ttlCache[V]) put(key string, v V, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if now.Sub(e.at) >= c.ttl {
			delete(c.entries, k)
		}
	}
	for len(c.entries) >= c.max {
		oldest := ""
		for k, e := range c.entries {
			if oldest == "" || e.at.Before(c.entries[oldest].at) {
				oldest = k
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[key] = ttlEntry[V]{v: v, at: now}
}

func (c *ttlCache[V]) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.entries)
}
