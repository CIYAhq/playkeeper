package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft/software"
	"github.com/CIYAhq/playkeeper/internal/packs"
	"github.com/CIYAhq/playkeeper/internal/templates"
)

// templateBuildKey is the one key of a template's Server.Build Playkeeper
// writes and reads: the build the create flow pins (a Paper or Purpur
// build, a Fabric or Quilt loader, a NeoForge version).
const templateBuildKey = "build"

// kindTemplatePacks reports a template's resource pack, which an import
// leaves out: offering it to players is a setting of its own.
const kindTemplatePacks addons.Kind = "template_packs_left_out"

// templateSubstitutes are the types whose versions an import lists when the
// template's own type can't be created here, as the templates package
// substitutes them.
var templateSubstitutes = map[string]string{
	"vanilla": api.TypePaper, "purpur": api.TypePaper, api.TypePaper: "purpur", "fabric": "quilt", "quilt": "fabric",
}

// templateBuild is the build a server runs, as a template carries it.
func templateBuild(sc *api.ServerConfig) map[string]string {
	b := ""
	switch {
	case sc.Software != nil:
		b = pinBuild(software.Pin(*sc.Software))
	case sc.PaperBuild > 0:
		b = strconv.Itoa(sc.PaperBuild)
	}
	if b == "" {
		return nil
	}
	return map[string]string{templateBuildKey: b}
}

// templateSetup is the server's setup as an export reads it.
func (s *server) templateSetup(ctx context.Context) (templates.Setup, error) {
	sc, err := s.serverConfig()
	if err != nil {
		return templates.Setup{}, err
	}
	if sc == nil {
		return templates.Setup{}, errNotCreated()
	}
	typ, gp := s.serverType(sc), sc.Gameplay
	st := templates.Setup{
		Name: s.name(), Type: typ, MinecraftVersion: sc.MinecraftVersion, Build: templateBuild(sc),
		Settings: templates.Settings{
			Difficulty: gp.Difficulty, PVP: gp.PVP, GameMode: gp.GameMode, Hardcore: gp.Hardcore, ViewDistance: gp.ViewDistance,
			LevelType: gp.LevelType, MaxPlayers: sc.MaxPlayers, MOTD: sc.MOTD, PlayStyle: sc.PlayStyle, MemoryMB: sc.MemoryMB,
		},
		HasIcon: sc.IconUpdatedAt != nil,
	}
	if _, err := addons.TargetFor(typ); err == nil {
		if st.Addons, err = s.installedAddons(); err != nil {
			return templates.Setup{}, err
		}
		// Without identify the scan stays on this machine: add-ons whose
		// file is gone are left out, and a pack's own mods, which have no
		// rows, don't travel one by one next to the pack.
		srv := addons.Server{Dir: s.dataDir(), Type: typ, MinecraftVersion: sc.MinecraftVersion, Owner: s.gameOwner()}
		if lib := s.lib(); lib != nil {
			if scan, err := lib.Scan(ctx, srv, st.Addons, false); err == nil {
				st.Folder = scan
			}
		}
	}
	if m := sc.Modpack; m != nil && !m.Pending {
		rec, err := s.packRecord()
		if err != nil {
			return templates.Setup{}, err
		}
		if rec != nil {
			p := rec.Pack
			st.Modpack = &templates.ModpackSetup{Modpack: templates.Modpack{
				Source: p.Source, Project: p.ProjectID, Slug: p.Slug, Name: m.Name,
				Pin: templates.Pin{VersionID: p.VersionID, VersionNumber: p.VersionNumber, Channel: p.Channel, HashAlgo: p.HashAlgo, Hash: p.Hash},
			}}
		}
	}
	if o := sc.ResourcePack; o != nil && o.SHA1 != "" {
		st.Packs = append(st.Packs, templates.PackSetup{Uploaded: true, Pack: templates.Pack{
			Kind: templates.ResourcePack, Name: o.FileName, URL: o.URL, SHA1: o.SHA1, Required: o.Required, Prompt: o.Prompt,
		}})
		if u, err := url.Parse(o.URL); err == nil && u.Hostname() != "" {
			st.OwnHosts = append(st.OwnHosts, u.Hostname())
		}
	}
	if dps, err := s.installedDataPacks(sc); err == nil {
		for _, dp := range dps {
			st.Packs = append(st.Packs, templates.PackSetup{Pack: templates.Pack{Kind: templates.DataPack, Name: dp.Name}})
		}
	}
	return st, nil
}

// hTemplate is the server's setup as a template, with the export dialog's
// choices: addons, settings or packs "off" leaves them out, and versions
// "latest" names each add-on's newest version that fits rather than the one
// installed.
func (s *server) hTemplate(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	latest := q.Get("versions") == "latest"
	st, err := s.templateSetup(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	// The panel names the signed-in account as the author.
	author, _ := validActor(q.Get("author"))
	t, rep, err := templates.Export(st, templates.ExportOptions{
		WithoutAddons: q.Get("addons") == "off", WithoutSettings: q.Get("settings") == "off", WithoutPacks: q.Get("packs") == "off", Latest: latest,
		Author: author, Created: s.now(),
	})
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	all, _, err := templates.Export(st, templates.ExportOptions{Latest: latest})
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	file, err := templates.MarshalFile(t)
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	out := api.TemplateExport{
		FileName: templates.FileName(t), File: string(file), Contents: templateContents(t), Available: templateContents(all),
		PacksHere: len(st.Packs), LeftOut: apiNotices(rep.LeftOut), Notes: apiNotices(rep.Notes),
	}
	if l, err := templates.NewLink(t); err == nil {
		out.Link, out.LinkLong = l.URL, l.Warning != nil
	}
	writeJSON(w, http.StatusOK, out)
}

// templateContents is what a template carries, as the dashboard shows it.
func templateContents(t *templates.Template) api.TemplateContents {
	st := t.Settings
	c := api.TemplateContents{
		Name: t.Name, Author: t.Author, Created: t.Created, Type: t.Server.Type, MinecraftVersion: t.Server.MinecraftVersion, Build: t.Server.Build[templateBuildKey],
		Settings: api.TemplateSettings{
			Difficulty: st.Difficulty, PVP: st.PVP, GameMode: st.GameMode, Hardcore: st.Hardcore, ViewDistance: st.ViewDistance,
			LevelType: st.LevelType, MaxPlayers: st.MaxPlayers, MOTD: st.MOTD, PlayStyle: st.PlayStyle, MemoryMB: st.MemoryMB,
		},
		Addons: make([]api.TemplateAddon, 0, len(t.Addons)), Packs: []string{},
	}
	for _, a := range t.Addons {
		ta := api.TemplateAddon{Source: string(a.Source), Name: a.Name}
		if a.Pin != nil && !a.Latest {
			ta.VersionNumber = a.Pin.VersionNumber
		}
		c.Addons = append(c.Addons, ta)
	}
	if m := t.Modpack; m != nil {
		c.Modpack = &api.TemplateAddon{Source: string(m.Source), Name: m.Name, VersionNumber: m.Pin.VersionNumber}
	}
	for _, kind := range []string{templates.ResourcePack, templates.DataPack} {
		for _, p := range t.Packs {
			if p.Kind != kind {
				continue
			}
			if kind == templates.ResourcePack {
				c.ResourcePacks++
			} else {
				c.DataPacks++
			}
			c.Packs = append(c.Packs, p.Name)
		}
	}
	return c
}

// hTemplatePlan says what a template would create on this machine. The body
// is a template file's text, a link, or the part after # in one. The
// template is kept for a while under the plan's fingerprint, which the
// create request names.
func (a *Agent) hTemplatePlan(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, templates.MaxFileSize+1))
	if err != nil {
		writeError(w, errInvalid("Could not read the template."))
		return
	}
	t, err := templates.Decode(data)
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	p, err := a.planTemplate(r.Context(), t)
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	a.templatePlans.put(p.Fingerprint, t, a.now())
	out := api.TemplatePlan{
		Contents: templateContents(t), Type: p.Type.ID, MemoryMB: p.MemoryMB, Skipped: apiNotices(p.Skipped),
		Warnings: apiNotices(p.Warnings), Blockers: apiNotices(p.Blockers), Ready: p.Ready, Fingerprint: p.Fingerprint,
	}
	if v := p.Version; v != nil {
		out.VersionID, out.MinecraftVersion, out.Build, out.Experimental = v.ID, v.MinecraftVersion, v.Build[templateBuildKey], v.Experimental
	}
	writeJSON(w, http.StatusOK, out)
}

// planTemplate plans a template's import with this machine's versions and
// memory, leaving the template's resource pack out.
func (a *Agent) planTemplate(ctx context.Context, t *templates.Template) (*templates.Plan, error) {
	tt := *t
	tt.Packs = nil
	for _, pk := range t.Packs {
		if pk.Kind == templates.DataPack {
			tt.Packs = append(tt.Packs, pk)
		}
	}
	p, err := templates.PlanImport(&tt, a.templateCatalog(ctx, &tt))
	if err != nil {
		return nil, err
	}
	if n := len(t.Packs) - len(tt.Packs); n > 0 {
		p.Skipped = append(p.Skipped, addons.Notice{Kind: kindTemplatePacks, Params: map[string]string{"count": strconv.Itoa(n)},
			Msg: "The template's resource pack is left out.", Hint: "Add it on the World tab once the server exists."})
	}
	return p, nil
}

// templateDataPacks are the data packs a confirmed import downloads.
func templateDataPacks(p *templates.Plan) []templates.Pack {
	out := make([]templates.Pack, 0, len(p.Packs))
	for _, pp := range p.Packs {
		out = append(out, pp.Pack)
	}
	return out
}

// templateCatalog is what an import chooses from: this machine's types, the
// versions of the template's type (or of the type standing in for it), and
// the memory a new server can have.
func (a *Agent) templateCatalog(ctx context.Context, t *templates.Template) templates.Catalog {
	want := t.Server.Type
	if !typeAvailable(want) {
		want = templateSubstitutes[want]
	}
	opts, rec, _ := a.memoryFor("")
	c := templates.Catalog{MemoryOptionsMB: opts, RecommendedMemoryMB: rec}
	for _, st := range serverTypes() {
		ct := templates.CatalogType{ID: st.ID, Name: st.Name, Available: st.Available}
		if st.Available && st.ID == want {
			ct.Versions, ct.VersionsError = a.templateVersions(ctx, st.ID, t)
		}
		c.Types = append(c.Types, ct)
	}
	return c
}

// templateVersions are the versions of a type an import can pick, each with
// the build the create flow would pin. A build the template names for its
// own Minecraft version is a choice too when this machine can install it,
// so a pinned template gets the same software.
func (a *Agent) templateVersions(ctx context.Context, typ string, t *templates.Template) ([]templates.CatalogVersion, string) {
	entries, _, err := a.typeCatalog(ctx, typ)
	if err != nil {
		var ae *apiError
		if errors.As(softwareError(err), &ae) {
			return nil, ae.Msg
		}
		return nil, err.Error()
	}
	vs := make([]templates.CatalogVersion, 0, len(entries)+1)
	same := -1
	for i, e := range entries {
		vs = append(vs, templates.CatalogVersion{ID: e.ID, MinecraftVersion: e.MinecraftVersion, Build: entryBuild(e), Experimental: e.Experimental})
		if same < 0 && e.MinecraftVersion == t.Server.MinecraftVersion {
			same = i
		}
	}
	want := t.Server.Build[templateBuildKey]
	if same < 0 || want == "" || typ != t.Server.Type || typ == api.TypePaper || !software.Supported(typ) || want == entries[same].Build {
		return vs, ""
	}
	e := entries[same]
	bs, _, err := a.typeBuilds(ctx, typ, e.MinecraftVersion)
	if i := slices.IndexFunc(bs, func(b software.Build) bool { return b.Version == want }); err == nil && i >= 0 {
		vs = append(vs, templates.CatalogVersion{ID: e.ID, MinecraftVersion: e.MinecraftVersion, Build: map[string]string{templateBuildKey: want},
			Experimental: e.Experimental || bs[i].Channel != software.Stable})
	}
	return vs, ""
}

// entryBuild is a catalog version's build, as a template carries it.
func entryBuild(e api.CatalogEntry) map[string]string {
	b := e.Build
	if b == "" && e.PaperBuild > 0 {
		b = strconv.Itoa(e.PaperBuild)
	}
	if b == "" {
		return nil
	}
	return map[string]string{templateBuildKey: b}
}

// templateImport is a confirmed import: the template and its plan.
type templateImport struct {
	t *templates.Template
	p *templates.Plan
}

// confirmTemplate plans the template planned under fingerprint again and
// checks that the plan is still the one the user confirmed.
func (a *Agent) confirmTemplate(ctx context.Context, fingerprint string) (*templateImport, error) {
	t, ok := a.templatePlans.get(fingerprint, a.now())
	if !ok {
		return nil, &apiError{Status: http.StatusConflict, Code: api.CodePlanChanged, Msg: "Playkeeper no longer has that template.", Hint: "Choose it again."}
	}
	p, err := a.planTemplate(ctx, t)
	if err != nil {
		return nil, addonError(err)
	}
	if err := p.Confirm(fingerprint); err != nil {
		return nil, addonError(err)
	}
	return &templateImport{t: t, p: p}, nil
}

// fill sets what the template decides on a create request: the type,
// version and build, or the modpack that decides them, and the settings.
func (ti *templateImport) fill(req *api.CreateServerRequest) {
	p := ti.p
	if m := p.Modpack; m != nil {
		req.Modpack = &api.ModpackRef{Source: string(m.Source), ProjectID: m.Project, VersionID: m.Pin.VersionID}
	} else {
		req.Type, req.VersionID = p.Type.ID, p.Version.ID
		if p.Type.ID != api.TypePaper {
			req.Build = p.Version.Build[templateBuildKey]
		}
	}
	st := p.Settings
	req.PlayStyle, req.MOTD, req.MaxPlayers = st.PlayStyle, st.MOTD, st.MaxPlayers
	gp := api.Gameplay{Difficulty: st.Difficulty, PVP: st.PVP, GameMode: st.GameMode, Hardcore: st.Hardcore, ViewDistance: st.ViewDistance, LevelType: st.LevelType}
	if gp != (api.Gameplay{}) {
		req.Gameplay = &gp
	}
}

// installPendingTemplate installs the add-ons of the template a server was
// created from before its first start, one by one through the add-on
// library, which checks each download against the hash its source
// publishes. An add-on the source no longer offers or that doesn't fit is
// skipped and reported; a failure such as an unreachable source stops the
// start, and the next start installs the rest.
func (s *server) installPendingTemplate(ctx context.Context, h *opHandle, sc *api.ServerConfig) error {
	planned, remaining, dataPacks, err := s.templateInstall()
	if err != nil {
		return err
	}
	var skips []templateSkip
	if len(remaining) > 0 {
		h.phase("installing_addons")
		s.setRunPhase(api.PhaseDownloading, "")
		done := len(planned) - len(remaining)
		h.set("addons", done)
		h.set("addonsTotal", len(planned))
		for len(remaining) > 0 {
			skip, err := s.installTemplateAddon(ctx, h, remaining[0], false)
			if err != nil {
				return err
			}
			if skip != nil {
				skips = append(skips, *skip)
				h.set("skipped", skipNotices(skips))
			}
			remaining = remaining[1:]
			if err := s.saveTemplateRemaining(remaining); err != nil {
				return err
			}
			done++
			h.set("addons", done)
		}
	}
	if err := s.linkTemplateDependencies(planned); err != nil {
		return err
	}
	packSkips, err := s.installTemplatePacks(ctx, h, sc, dataPacks, skips)
	if err != nil {
		return err
	}
	skips = append(skips, packSkips...)
	return s.settleTemplate(sc, skips)
}

// installTemplateAddon installs one of the template's add-ons through the
// add-on library, which checks each download against the hash its source
// publishes. An add-on the source no longer offers or that doesn't fit comes
// back as a skip with its reason; a source that can't be reached stops.
// changed says the running server must restart to load it.
func (s *server) installTemplateAddon(ctx context.Context, h *opHandle, pa templates.PlannedAddon, changed bool) (*templateSkip, error) {
	lib := s.lib()
	if lib == nil {
		return nil, errors.New("the add-on library is not available")
	}
	_, srv, _, err := s.addonContext()
	if err != nil {
		return nil, err
	}
	installed, err := s.installedAddons()
	if err != nil {
		return nil, err
	}
	res, err := templates.InstallAddon(ctx, lib, srv, installed, pa)
	if err != nil {
		return nil, packFailure(h, err)
	}
	switch res.Status {
	case templates.AddonInstalled:
		if err := s.saveAddons(res.Records, nil, changed); err != nil {
			return nil, err
		}
		for _, rec := range res.Records {
			s.audit(h.op.Actor, "addon.installed", string(rec.Source)+":"+rec.ProjectID, "succeeded", rec.Name+" "+rec.VersionNumber)
		}
		return nil, nil
	case templates.AddonSkipped:
		if sourceDown(res.Reason.Kind) {
			return nil, packFailure(h, &addons.Error{Notice: *res.Reason})
		}
		n := apiNotice(*res.Reason)
		n.Params = maps.Clone(n.Params)
		if n.Params == nil {
			n.Params = map[string]string{}
		}
		if n.Params["name"] == "" {
			n.Params["name"] = pa.Name
		}
		s.audit(h.op.Actor, "addon.skipped", string(pa.Source)+":"+pa.Project, "skipped", pa.Name+": "+n.Message)
		return &templateSkip{Addon: &pa, Notice: n}, nil
	}
	return nil, fmt.Errorf("the template's add-on %s ended in an unknown state", pa.Name)
}

// linkTemplateDependencies records which installed add-ons the template's
// add-ons need, so removing one keeps the others working.
func (s *server) linkTemplateDependencies(planned []templates.PlannedAddon) error {
	installed, err := s.installedAddons()
	if err != nil {
		return err
	}
	if links := templates.LinkDependencies(installed, &templates.Plan{Addons: planned}); len(links) > 0 {
		return s.saveAddons(links, nil, false)
	}
	return nil
}

// installTemplatePacks downloads the template's data packs into the world
// through PackClient. A pack's host is anyone's, so a pack that can't be
// downloaded or doesn't match its checksum is skipped and reported rather
// than stopping the start. before are the skips so far, for the progress.
func (s *server) installTemplatePacks(ctx context.Context, h *opHandle, sc *api.ServerConfig, list []templates.Pack, before []templateSkip) ([]templateSkip, error) {
	if len(list) == 0 {
		return nil, nil
	}
	if err := s.ensureDirs(); err != nil {
		return nil, err
	}
	h.phase("installing_addons")
	s.setRunPhase(api.PhaseDownloading, "")
	h.set("packsTotal", len(list))
	d := s.dataPacks(sc)
	var skips []templateSkip
	for i, pk := range list {
		h.set("packs", i)
		err := s.installTemplatePack(ctx, d, pk)
		if err == nil {
			s.audit(h.op.Actor, "datapack.added", pk.Name, "succeeded", "from the template")
			continue
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		n := templatePackNotice(err)
		n.Params["name"] = pk.Name
		skips = append(skips, templateSkip{Pack: &pk, Notice: n})
		h.set("skipped", skipNotices(append(slices.Clone(before), skips...)))
		s.audit(h.op.Actor, "datapack.skipped", pk.Name, "skipped", n.Message)
	}
	h.set("packs", len(list))
	return skips, nil
}

// templateSkip is an add-on or data pack of the template a server was made
// from that could not be installed, and why. The server page lists them
// with Try again.
type templateSkip struct {
	Addon  *templates.PlannedAddon `json:"addon,omitempty"`
	Pack   *templates.Pack         `json:"pack,omitempty"`
	Notice api.AddonNotice         `json:"notice"`
}

func skipNotices(skips []templateSkip) []api.AddonNotice {
	out := make([]api.AddonNotice, 0, len(skips))
	for _, sk := range skips {
		out = append(out, sk.Notice)
	}
	return out
}

// settleTemplate records that the template's install is over: what it
// skipped stays, for the server page and Try again, and the rest of the
// record goes.
func (s *server) settleTemplate(sc *api.ServerConfig, skips []templateSkip) error {
	if len(skips) == 0 {
		if _, err := s.db.Exec(`DELETE FROM template_installs WHERE server_id = ?`, s.id); err != nil {
			return err
		}
	} else {
		b, err := json.Marshal(skips)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE template_installs SET remaining = '[]', packs = '[]', skipped = ? WHERE server_id = ?`, string(b), s.id); err != nil {
			return err
		}
	}
	t := *sc.Template
	t.Pending = false
	t.Skipped = nil
	if len(skips) > 0 {
		t.Skipped = skipNotices(skips)
	}
	sc.Template = &t
	return s.saveServerConfig(*sc)
}

// templateSkips are the template's add-ons and data packs that were skipped.
func (s *server) templateSkips() ([]templateSkip, error) {
	var v string
	err := s.db.QueryRow(`SELECT skipped FROM template_installs WHERE server_id = ?`, s.id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []templateSkip
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil, fmt.Errorf("the stored template add-ons are damaged: %w", err)
	}
	return out, nil
}

// hTemplateRetry tries the template's skipped add-ons and data packs again,
// the way the first start did. What installs loads at the next restart.
func (s *server) hTemplateRetry(w http.ResponseWriter, r *http.Request) {
	actor, err := actionActor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	skips, err := s.templateSkips()
	if err != nil {
		writeError(w, err)
		return
	}
	sc, err := s.serverConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	if len(skips) == 0 || sc == nil || sc.Template == nil || sc.Template.Pending {
		writeError(w, errConflict("Nothing from the template is waiting to be installed.", ""))
		return
	}
	op, err := s.beginOp("template-retry", actor, func(ctx context.Context, h *opHandle) error {
		return s.retryTemplate(ctx, h, skips)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (s *server) retryTemplate(ctx context.Context, h *opHandle, skips []templateSkip) error {
	sc, err := s.serverConfig()
	if err != nil || sc == nil || sc.Template == nil {
		return errConflict("The server no longer has its template's record.", "")
	}
	planned, _, _, err := s.templateInstall()
	if err != nil {
		return err
	}
	_, running, _ := s.containerRunning(ctx)
	h.phase("installing_addons")
	var addonTries []templates.PlannedAddon
	var packTries []templates.Pack
	for _, sk := range skips {
		switch {
		case sk.Addon != nil:
			addonTries = append(addonTries, *sk.Addon)
		case sk.Pack != nil:
			packTries = append(packTries, *sk.Pack)
		}
	}
	var still []templateSkip
	h.set("addonsTotal", len(addonTries))
	for i, pa := range addonTries {
		h.set("addons", i)
		skip, err := s.installTemplateAddon(ctx, h, pa, running)
		if err != nil {
			return err
		}
		if skip != nil {
			still = append(still, *skip)
			h.set("skipped", skipNotices(still))
		}
	}
	h.set("addons", len(addonTries))
	if err := s.linkTemplateDependencies(planned); err != nil {
		return err
	}
	packSkips, err := s.installTemplatePacks(ctx, h, sc, packTries, still)
	if err != nil {
		return err
	}
	still = append(still, packSkips...)
	h.set("restartNeeded", running && len(still) < len(skips))
	return s.settleTemplate(sc, still)
}

// templatePackNotice is why a template's data pack was skipped, keeping
// the code of the download's or the pack's own error.
func templatePackNotice(err error) api.AddonNotice {
	n := api.AddonNotice{Kind: string(templates.KindPackUnreachable), Message: err.Error()}
	var ae *addons.Error
	var pe *packs.Error
	switch {
	case errors.As(err, &ae):
		n = apiNotice(ae.Notice)
		n.Params = maps.Clone(n.Params)
	case errors.As(err, &pe):
		n = api.AddonNotice{Kind: pe.Code, Message: pe.Msg, Hint: pe.Hint}
		for k, v := range pe.Params {
			if n.Params == nil {
				n.Params = map[string]string{}
			}
			n.Params[k] = fmt.Sprint(v)
		}
	}
	if n.Params == nil {
		n.Params = map[string]string{}
	}
	return n
}

func (s *server) installTemplatePack(ctx context.Context, d packs.DataPacks, pk templates.Pack) error {
	f, n, err := templates.FetchPack(ctx, s.opts.PackClient, s.cfg.StagingDir(), pk, packs.Limits{})
	if err != nil {
		return err
	}
	defer f.Close()
	_, _, err = d.Install(ctx, packs.SafeName(pk.Name), f, n, true)
	return err
}

// sourceDown is true for the add-on library's refusals that say a source
// didn't answer, rather than that the add-on can't be installed.
func sourceDown(k addons.Kind) bool {
	return k == addons.KindUnreachable || k == addons.KindUpstream || k == addons.KindRateLimited
}

// templateInstall is the template's add-ons, all of them and those still
// to install, and its data packs.
func (s *server) templateInstall() (planned, remaining []templates.PlannedAddon, dataPacks []templates.Pack, err error) {
	var p, r, d string
	err = s.db.QueryRow(`SELECT planned, remaining, packs FROM template_installs WHERE server_id = ?`, s.id).Scan(&p, &r, &d)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	if err := json.Unmarshal([]byte(p), &planned); err != nil {
		return nil, nil, nil, fmt.Errorf("the stored template add-ons are damaged: %w", err)
	}
	if err := json.Unmarshal([]byte(r), &remaining); err != nil {
		return nil, nil, nil, fmt.Errorf("the stored template add-ons are damaged: %w", err)
	}
	if err := json.Unmarshal([]byte(d), &dataPacks); err != nil {
		return nil, nil, nil, fmt.Errorf("the stored template data packs are damaged: %w", err)
	}
	return planned, remaining, dataPacks, nil
}

func (s *server) saveTemplateInstall(planned []templates.PlannedAddon, dataPacks []templates.Pack) error {
	b, err := json.Marshal(append([]templates.PlannedAddon{}, planned...))
	if err != nil {
		return err
	}
	d, err := json.Marshal(append([]templates.Pack{}, dataPacks...))
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO template_installs(server_id, planned, remaining, packs, created_at) VALUES(?,?,?,?,?)
		ON CONFLICT(server_id) DO UPDATE SET planned = excluded.planned, remaining = excluded.remaining, packs = excluded.packs`,
		s.id, string(b), string(b), string(d), s.now().UnixMilli())
	return err
}

func (s *server) saveTemplateRemaining(remaining []templates.PlannedAddon) error {
	b, err := json.Marshal(append([]templates.PlannedAddon{}, remaining...))
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE template_installs SET remaining = ? WHERE server_id = ?`, string(b), s.id)
	return err
}
