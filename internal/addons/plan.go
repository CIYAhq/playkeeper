package addons

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
)

// InstallRequest asks for one add-on on one server.
type InstallRequest struct {
	Source Source `json:"source"`
	// Project is the project's id or slug.
	Project string `json:"project"`
	// VersionID picks a version; empty means the newest release that fits.
	VersionID string `json:"versionId,omitempty"`
	// AllowPrerelease lets the add-on and its dependencies be beta or alpha
	// versions when no release fits.
	AllowPrerelease bool `json:"allowPrerelease,omitempty"`
	// Fingerprint is the Plan.Fingerprint the user confirmed. Install
	// refuses when the plan has changed since; empty skips the check.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Action is what a step does to the server's folder.
type Action string

const (
	ActionInstall Action = "install"
	ActionUpdate  Action = "update"
)

// Step is one file a plan downloads, verifies and puts into the folder.
type Step struct {
	Action        Action    `json:"action"`
	Source        Source    `json:"source"`
	ProjectID     string    `json:"projectId"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	IconURL       string    `json:"iconUrl,omitempty"`
	VersionID     string    `json:"versionId"`
	VersionNumber string    `json:"versionNumber"`
	Channel       string    `json:"channel"`
	Published     time.Time `json:"published"`
	FileName      string    `json:"fileName"`
	Size          int64     `json:"size"`
	HashAlgo      string    `json:"hashAlgo"`
	Hash          string    `json:"hash"`
	// DependencyOf is the project id of the add-on this one is needed for;
	// empty for the add-ons the user asked for.
	DependencyOf string `json:"dependencyOf,omitempty"`
	// Requires lists the project ids (same source) this version needs.
	Requires []string `json:"requires,omitempty"`
	// Replaces is the installed add-on an update replaces.
	Replaces *Installed `json:"replaces,omitempty"`
	// url is unexported so that a plan which went through JSON cannot be
	// carried out: Install and Update always plan again.
	url string
}

func (s Step) key() Key { return Key{s.Source, s.ProjectID} }

func (s Step) record(now time.Time) Installed {
	return Installed{
		Source: s.Source, ProjectID: s.ProjectID, Slug: s.Slug, Name: s.Name, IconURL: s.IconURL,
		VersionID: s.VersionID, VersionNumber: s.VersionNumber, Channel: s.Channel, Published: s.Published,
		FileName: s.FileName, HashAlgo: s.HashAlgo, Hash: strings.ToLower(s.Hash), Size: s.Size,
		DependencyOf: s.DependencyOf, Requires: s.Requires, InstalledAt: now,
	}
}

// Satisfied is a required dependency the server already has.
type Satisfied struct {
	Name string `json:"name"`
	// For is the add-on that needs it.
	For string `json:"for"`
	// FileName is the file in the folder that provides it.
	FileName string `json:"fileName"`
	// Managed is true when Playkeeper installed that file.
	Managed bool `json:"managed"`
}

// ManualStep is something Playkeeper cannot do for the user, with the page
// to do it from when there is one.
type ManualStep struct {
	Notice
	URL string `json:"url,omitempty"`
}

// Suggestion is an optional dependency that adds features to an add-on.
type Suggestion struct {
	Source    Source `json:"source"`
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	For       string `json:"for"`
}

// Plan is what an install or update would do, for the user to confirm.
type Plan struct {
	Target Target `json:"target"`
	// Steps are the files to download, the add-ons asked for first.
	Steps       []Step       `json:"steps"`
	Satisfied   []Satisfied  `json:"satisfied"`
	Manual      []ManualStep `json:"manual"`
	Suggestions []Suggestion `json:"suggestions"`
	// Blockers stop the plan: conflicts, files in the way, dependencies
	// that cannot be had.
	Blockers []Notice `json:"blockers"`
	Warnings []Notice `json:"warnings"`
	// Ready is true when the plan has steps and nothing blocks it.
	Ready       bool   `json:"ready"`
	Fingerprint string `json:"fingerprint"`
}

func (p *Plan) fingerprint() string {
	b, _ := json.Marshal(struct {
		Steps     []Step
		Satisfied []Satisfied
		Manual    []ManualStep
		Blockers  []Notice
	}{p.Steps, p.Satisfied, p.Manual, p.Blockers})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// PlanInstall works out what installing an add-on would do: the version, the
// required dependencies for the same loader and Minecraft version, what the
// server already has, conflicts with installed add-ons, and anything the
// user has to do by hand. installed are the add-ons Playkeeper manages on
// the server. Nothing is downloaded or written.
func (l *Library) PlanInstall(ctx context.Context, srv Server, installed []Installed, req InstallRequest) (*Plan, error) {
	t, err := l.check(srv, req.Source, req.Project, req.VersionID)
	if err != nil {
		return nil, err
	}
	p, err := l.project(ctx, req.Source, req.Project)
	if err != nil {
		return nil, err
	}
	if p.clientOnly {
		return nil, clientOnly(p)
	}
	for _, rec := range installed {
		if rec.Key() == (Key{p.Source, p.ID}) {
			return nil, fail(KindAlreadyInstalled, kv("name", rec.Name, "version", rec.VersionNumber),
				fmt.Sprintf("%s is already installed on this server (version %s).", rec.Name, rec.VersionNumber),
				"Use Update to change its version.")
		}
	}
	c, err := l.choose(ctx, srv, t, p, req.VersionID, req.AllowPrerelease)
	if err != nil {
		return nil, err
	}
	r, err := l.resolver(ctx, srv, t, installed, req.Source == Modrinth, false, req.AllowPrerelease)
	if err != nil {
		return nil, err
	}
	if prov, ok := r.inv.provides(c.key(), c.Name, c.Slug); ok {
		r.block(duplicate(c.Name, c.Source, prov))
	}
	r.add(c, ActionInstall, nil, nil)
	if err := r.resolve(ctx); err != nil {
		return nil, err
	}
	return r.finish(), nil
}

// resolver builds one plan.
type resolver struct {
	l        *Library
	srv      Server
	t        Target
	inv      *inventory
	allowPre bool
	plan     *Plan

	planned   map[Key]int // index into plan.Steps
	deps      [][]dep     // per step
	queue     []int       // steps whose dependencies are not resolved yet
	suggested map[Key]bool
	incompat  []incompat
	// mproj caches Modrinth projects by id; nil marks one Modrinth does
	// not list.
	mproj map[string]*modrinth.Project
}

type incompat struct {
	step      int
	projectID string
}

func (l *Library) resolver(ctx context.Context, srv Server, t Target, installed []Installed, identify, verify, allowPre bool) (*resolver, error) {
	inv, warnings, err := l.inventory(ctx, srv, t, installed, identify, verify)
	if err != nil {
		return nil, err
	}
	return &resolver{
		l: l, srv: srv, t: t, inv: inv, allowPre: allowPre,
		plan: &Plan{
			Target: t, Steps: []Step{}, Satisfied: []Satisfied{}, Manual: []ManualStep{},
			Suggestions: []Suggestion{}, Blockers: []Notice{}, Warnings: warnings,
		},
		planned: map[Key]int{}, suggested: map[Key]bool{}, mproj: map[string]*modrinth.Project{},
	}, nil
}

func (r *resolver) block(n Notice) {
	if !slices.ContainsFunc(r.plan.Blockers, func(b Notice) bool { return b.Kind == n.Kind && b.Msg == n.Msg }) {
		r.plan.Blockers = append(r.plan.Blockers, n)
	}
}

func (r *resolver) warn(n Notice) {
	if !slices.ContainsFunc(r.plan.Warnings, func(w Notice) bool { return w.Kind == n.Kind && w.Msg == n.Msg }) {
		r.plan.Warnings = append(r.plan.Warnings, n)
	}
}

func (r *resolver) manual(m ManualStep) {
	if !slices.ContainsFunc(r.plan.Manual, func(o ManualStep) bool { return o.Kind == m.Kind && o.Msg == m.Msg }) {
		r.plan.Manual = append(r.plan.Manual, m)
	}
}

// add puts candidate c into the plan, for parent (nil for an add-on the user
// asked for) or replacing an installed add-on.
func (r *resolver) add(c candidate, a Action, parent *Step, replaces *Installed) {
	if c.External != "" {
		r.manual(external(c, parent, r.t))
		return
	}
	if c.Channel != release {
		r.warn(notice(KindPrerelease, kv("name", c.Name, "version", c.Number, "channel", c.Channel),
			fmt.Sprintf("%s %s is a %s version and may be unstable.", c.Name, c.Number, c.Channel), ""))
	}
	s := Step{
		Action: a, Source: c.Source, ProjectID: c.ProjectID(), Slug: c.Slug, Name: c.Name, IconURL: c.IconURL,
		VersionID: c.VersionID, VersionNumber: c.Number, Channel: c.Channel, Published: c.Published,
		FileName: c.FileName, Size: c.Size, HashAlgo: c.HashAlgo, Hash: c.Hash, Replaces: replaces, url: c.URL,
	}
	switch {
	case parent != nil:
		s.DependencyOf = parent.ProjectID
	case replaces != nil:
		s.DependencyOf = replaces.DependencyOf
	}
	r.planned[s.key()] = len(r.plan.Steps)
	r.plan.Steps = append(r.plan.Steps, s)
	r.deps = append(r.deps, c.deps)
	r.queue = append(r.queue, len(r.plan.Steps)-1)
}

// resolve follows the dependencies of every step, then looks for conflicts.
func (r *resolver) resolve(ctx context.Context) error {
	for len(r.queue) > 0 {
		i := r.queue[0]
		r.queue = r.queue[1:]
		if err := r.resolveDeps(ctx, i); err != nil {
			return err
		}
	}
	return r.conflicts(ctx)
}

func (r *resolver) resolveDeps(ctx context.Context, i int) error {
	s := r.plan.Steps[i]
	if s.Source == Modrinth {
		if err := r.versionOnly(ctx, i); err != nil {
			return err
		}
		var ids []string
		for _, d := range r.deps[i] {
			if d.projectID != "" {
				ids = append(ids, d.projectID)
			}
		}
		if err := r.fetchProjects(ctx, ids); err != nil {
			return err
		}
	}
	var requires []string
	for _, d := range r.deps[i] {
		switch d.typ {
		case modrinth.Required:
			need, err := r.required(ctx, s, d)
			if err != nil {
				return err
			}
			if need && !slices.Contains(requires, d.projectID) {
				requires = append(requires, d.projectID)
			}
		case modrinth.Optional:
			r.optional(s, d)
		case modrinth.Incompatible:
			if d.projectID != "" {
				r.incompat = append(r.incompat, incompat{i, d.projectID})
			}
		}
	}
	r.plan.Steps[i].Requires = requires
	return nil
}

// versionOnly finds the projects of step i's Modrinth dependencies that name
// only a version.
func (r *resolver) versionOnly(ctx context.Context, i int) error {
	var vids []string
	for _, d := range r.deps[i] {
		if d.projectID == "" && validRef(d.versionID) {
			vids = append(vids, d.versionID)
		}
	}
	if len(vids) == 0 {
		return nil
	}
	vs, err := r.l.Modrinth.Versions(ctx, vids)
	if err != nil {
		return upstream(Modrinth, err)
	}
	for j, d := range r.deps[i] {
		for _, v := range vs {
			if d.projectID == "" && d.versionID == v.ID {
				r.deps[i][j].projectID = v.ProjectID
			}
		}
	}
	return nil
}

// required handles one required dependency of s and reports whether the
// installed s will need that project.
func (r *resolver) required(ctx context.Context, s Step, d dep) (bool, error) {
	if d.projectID == "" {
		if lf := r.inv.byName[d.name]; lf != nil {
			r.satisfied(d.name, s, lf.provider())
			return false, nil
		}
		if prov, ok := r.inv.provides(Key{}, d.name); ok {
			r.satisfied(d.name, s, prov)
			return false, nil
		}
		r.manual(unlisted(s, d, r.t))
		return false, nil
	}
	key := Key{s.Source, d.projectID}
	if key == s.key() {
		return false, nil
	}
	if _, ok := r.planned[key]; ok {
		return true, nil
	}
	var p *project
	switch s.Source {
	case Modrinth:
		mp := r.mproj[d.projectID]
		if mp == nil {
			r.block(depGone(s, d))
			return false, nil
		}
		// Multi-loader versions list what every loader needs, e.g. Fabric
		// API on a version that also runs on Paper.
		if !overlaps(mp.Loaders, r.t.Loaders) {
			return false, nil
		}
		p = modrinthProject(mp)
	case Hangar:
		if prov, ok := r.inv.provides(key, d.name); ok {
			r.satisfied(d.name, s, prov)
			return true, nil
		}
		hp, err := r.l.project(ctx, Hangar, d.projectID)
		if KindOf(err) == KindNotFound {
			r.block(depGone(s, d))
			return false, nil
		}
		if err != nil {
			return false, err
		}
		p = hp
	}
	if prov, ok := r.inv.provides(key, p.Name, p.Slug, d.name); ok {
		r.satisfied(p.Name, s, prov)
		return true, nil
	}
	if p.clientOnly {
		r.warn(notice(KindDepClientOnly, kv("name", s.Name, "dependency", p.Name),
			fmt.Sprintf("%s lists %s as needed, but %s only runs in the game client, so the server does without it.", s.Name, p.Name, p.Name), ""))
		return false, nil
	}
	c, blocker, err := r.version(ctx, s, p, d.versionID)
	if err != nil {
		return false, err
	}
	if blocker != nil {
		r.block(*blocker)
		return true, nil
	}
	r.add(c, ActionInstall, &s, nil)
	return true, nil
}

// version picks the version of dependency p: the pinned one when it fits,
// else the newest release (or pre-release, when allowed).
func (r *resolver) version(ctx context.Context, parent Step, p *project, pinned string) (candidate, *Notice, error) {
	mc := r.srv.MinecraftVersion
	if pinned != "" {
		c, ok, err := r.l.exact(ctx, r.t, mc, p, pinned)
		if err != nil {
			return candidate{}, nil, err
		}
		if ok {
			return c, nil, nil
		}
	}
	cands, err := r.l.candidates(ctx, r.t, mc, p)
	if err != nil {
		return candidate{}, nil, err
	}
	c, e := pick(p, cands, r.allowPre, r.srv, r.t)
	if e == nil {
		return c, nil, nil
	}
	var n Notice
	if e.Kind == KindOnlyPrerelease {
		n = notice(KindOnlyPrerelease, kv("name", parent.Name, "dependency", p.Name, "minecraft", mc),
			fmt.Sprintf("%s needs %s, which has only pre-release versions for Minecraft %s.", parent.Name, p.Name, mc),
			"Pre-releases can be unstable. Allow pre-releases to install it anyway.")
	} else {
		n = notice(KindDepMissing, kv("name", parent.Name, "dependency", p.Name, "minecraft", mc, "type", r.t.Type),
			fmt.Sprintf("%s needs %s, which has no version for %s servers on Minecraft %s.", parent.Name, p.Name, r.t.Name(), mc),
			fmt.Sprintf("Wait for %s to support Minecraft %s, or choose another version of %s.", p.Name, mc, parent.Name))
	}
	return candidate{}, &n, nil
}

func (r *resolver) optional(s Step, d dep) {
	if d.projectID == "" {
		return
	}
	key := Key{s.Source, d.projectID}
	if _, ok := r.planned[key]; ok || r.suggested[key] {
		return
	}
	name := d.name
	if s.Source == Modrinth {
		mp := r.mproj[d.projectID]
		if mp == nil || !mp.RunsOnServer() || !overlaps(mp.Loaders, r.t.Loaders) || !slices.Contains(mp.GameVersions, r.srv.MinecraftVersion) {
			return
		}
		name = mp.Title
	}
	if _, ok := r.inv.provides(key, name); ok || name == "" {
		return
	}
	r.suggested[key] = true
	r.plan.Suggestions = append(r.plan.Suggestions, Suggestion{Source: s.Source, ProjectID: d.projectID, Name: name, For: s.Name})
}

func (r *resolver) satisfied(name string, s Step, prov provider) {
	sat := Satisfied{Name: name, For: s.Name, FileName: prov.file, Managed: prov.managed}
	if !slices.Contains(r.plan.Satisfied, sat) {
		r.plan.Satisfied = append(r.plan.Satisfied, sat)
	}
}

func (r *resolver) fetchProjects(ctx context.Context, ids []string) error {
	var missing []string
	for _, id := range ids {
		if _, ok := r.mproj[id]; !ok && !slices.Contains(missing, id) && validRef(id) {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	ps, err := r.l.Modrinth.Projects(ctx, missing)
	if err != nil {
		return upstream(Modrinth, err)
	}
	for _, id := range missing {
		r.mproj[id] = nil
	}
	for i := range ps {
		r.mproj[ps[i].ID] = &ps[i]
	}
	return nil
}

// conflicts checks Modrinth's "incompatible" dependencies both ways: what
// the planned versions cannot run with, and what installed add-ons say they
// cannot run with.
func (r *resolver) conflicts(ctx context.Context) error {
	var ids []string
	for _, in := range r.incompat {
		ids = append(ids, in.projectID)
	}
	if err := r.fetchProjects(ctx, ids); err != nil {
		return err
	}
	for _, in := range r.incompat {
		s := r.plan.Steps[in.step]
		key := Key{s.Source, in.projectID}
		var names []string
		if mp := r.mproj[in.projectID]; mp != nil {
			names = []string{mp.Title, mp.Slug}
		}
		if j, ok := r.planned[key]; ok {
			o := r.plan.Steps[j]
			r.block(notice(KindConflict, kv("name", s.Name, "other", o.Name),
				fmt.Sprintf("%s cannot run together with %s, which this plan also needs.", s.Name, o.Name),
				"Choose another version, or another add-on."))
			continue
		}
		if prov, ok := r.inv.provides(key, names...); ok {
			r.block(conflict(s.Name, prov.name, prov.file))
		}
	}

	var hasModrinth bool
	for _, s := range r.plan.Steps {
		hasModrinth = hasModrinth || s.Source == Modrinth
	}
	if !hasModrinth {
		return nil
	}
	type holder struct {
		name, file string
		deps       []modrinth.Dependency
	}
	var holders []holder
	byVersion := map[string]*Installed{}
	var vids []string
	for _, lf := range r.inv.files {
		switch {
		case lf.rec != nil && lf.rec.Source == Modrinth && validRef(lf.rec.VersionID):
			if _, ok := r.planned[lf.rec.Key()]; !ok {
				byVersion[lf.rec.VersionID] = lf.rec
				vids = append(vids, lf.rec.VersionID)
			}
		case lf.rec == nil && lf.ident != nil:
			holders = append(holders, holder{lf.display(), lf.name, lf.ident.Dependencies})
		}
	}
	if len(vids) > 0 {
		vs, err := r.l.Modrinth.Versions(ctx, vids)
		if err != nil {
			return upstream(Modrinth, err)
		}
		for _, v := range vs {
			if rec := byVersion[v.ID]; rec != nil {
				holders = append(holders, holder{rec.Name, rec.FileName, v.Dependencies})
			}
		}
	}
	for _, h := range holders {
		for _, d := range h.deps {
			if d.DependencyType != modrinth.Incompatible {
				continue
			}
			if j, ok := r.planned[Key{Modrinth, d.ProjectID}]; ok {
				r.block(conflict(r.plan.Steps[j].Name, h.name, h.file))
			}
		}
	}
	return nil
}

// finish checks the steps' files against the folder and seals the plan.
func (r *resolver) finish() *Plan {
	p := r.plan
	max := r.l.maxFileSize()
	replaced := map[string]bool{}
	for _, s := range p.Steps {
		if s.Replaces != nil {
			replaced[s.Replaces.FileName] = true
		}
	}
	seen := map[string]bool{}
	for _, s := range p.Steps {
		switch {
		case !validFileName(s.FileName):
			r.block(badFileName(s).Notice)
		case !validHash(s.HashAlgo, s.Hash):
			r.block(notice(KindNoHash, kv("name", s.Name, "file", s.FileName, "source", s.Source.Name()),
				fmt.Sprintf("%s lists no usable hash for %s, so Playkeeper cannot check the download.", s.Source.Name(), s.FileName),
				"Choose another version."))
		case s.Size > max:
			r.block(tooLarge(s, max).Notice)
		case seen[s.FileName]:
			r.block(notice(KindDuplicate, kv("name", s.Name, "file", s.FileName),
				fmt.Sprintf("Two add-ons in this plan use the file name %s.", s.FileName), "Install them one at a time."))
		case r.inv.names[s.FileName] && !replaced[s.FileName]:
			r.block(fileExists(s, r.t))
		}
		seen[s.FileName] = true
		if _, err := r.l.fileHosts(s.Source).Check(s.url); err != nil {
			r.block(hostNotAllowed(s).Notice)
		}
		if old := s.Replaces; old != nil {
			switch lf := r.inv.byName[old.FileName]; {
			case lf == nil:
				r.warn(notice(KindNotFound, kv("name", old.Name, "file", old.FileName),
					fmt.Sprintf("%s is missing from the %s folder, so this installs it again.", old.FileName, r.t.Folder), ""))
			case lf.modified:
				r.block(modified(*old, "replace"))
			}
		}
	}
	p.Suggestions = slices.DeleteFunc(p.Suggestions, func(sg Suggestion) bool {
		_, ok := r.planned[Key{sg.Source, sg.ProjectID}]
		return ok
	})
	p.Ready = len(p.Blockers) == 0 && len(p.Steps) > 0
	p.Fingerprint = p.fingerprint()
	return p
}

func validHash(algo, h string) bool {
	n := map[string]int{"sha512": 128, "sha256": 64, "sha1": 40}[algo]
	if n == 0 || len(h) != n {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil
}

// norm reduces a name for matching across sources and jar descriptors:
// "ViaVersion", "viaversion" and "Via-Version" are the same.
func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// safeLink returns raw when it is a plain web address the UI may link to.
func safeLink(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" || u.User != nil || len(raw) > 2048 {
		return ""
	}
	return u.String()
}

func duplicate(name string, src Source, prov provider) Notice {
	if prov.managed && prov.source != src {
		return notice(KindDuplicate, kv("name", name, "file", prov.file, "source", prov.source.Name()),
			fmt.Sprintf("%s is already installed from %s.", name, prov.source.Name()),
			fmt.Sprintf("Remove it first to switch to the copy on %s.", src.Name()))
	}
	return notice(KindDuplicate, kv("name", name, "file", prov.file),
		fmt.Sprintf("%s is already on this server as %s.", name, prov.file),
		fmt.Sprintf("Remove %s first if you want Playkeeper to install and update %s.", prov.file, name))
}

func conflict(name, other, file string) Notice {
	return notice(KindConflict, kv("name", name, "other", other, "file", file),
		fmt.Sprintf("%s cannot run together with %s (%s), which is on this server.", name, other, file),
		fmt.Sprintf("Remove %s first if you want %s.", other, name))
}

func fileExists(s Step, t Target) Notice {
	return notice(KindFileExists, kv("name", s.Name, "file", s.FileName, "folder", t.Folder),
		fmt.Sprintf("The %s folder already has a file named %s.", t.Folder, s.FileName),
		"Playkeeper never overwrites other files. Rename or remove that file first.")
}

func modified(rec Installed, verb string) Notice {
	return notice(KindModified, kv("name", rec.Name, "file", rec.FileName),
		fmt.Sprintf("%s has changed since Playkeeper installed it, so Playkeeper will not %s it.", rec.FileName, verb),
		"If you changed it on purpose, manage it by hand in the file manager.")
}

func depGone(s Step, d dep) Notice {
	return notice(KindDepMissing, kv("name", s.Name, "dependency", d.projectID, "source", s.Source.Name()),
		fmt.Sprintf("%s needs a project that %s does not list (%s).", s.Name, s.Source.Name(), d.projectID),
		"Look on the add-on's page for how to get it, or choose another version.")
}

func external(c candidate, parent *Step, t Target) ManualStep {
	link := safeLink(c.External)
	host := "another site"
	if u, err := url.Parse(link); err == nil && u.Host != "" {
		host = u.Hostname()
	}
	if parent == nil {
		// Some Hangar versions are named after the project, e.g. Geyser's.
		label := c.Name
		if c.Number != "" && c.Number != c.Name {
			label += " " + c.Number
		}
		return ManualStep{Notice: notice(KindExternal, kv("name", c.Name, "version", c.Number, "host", host, "folder", t.Folder),
			fmt.Sprintf("%s is only offered on %s, so Playkeeper cannot install it for you.", label, host),
			fmt.Sprintf("Download it from that page and upload it to the %s folder.", t.Folder)), URL: link}
	}
	return ManualStep{Notice: notice(KindDepExternal, kv("name", parent.Name, "dependency", c.Name, "host", host, "folder", t.Folder),
		fmt.Sprintf("%s needs %s, which is only offered on %s.", parent.Name, c.Name, host),
		fmt.Sprintf("Download it from that page and upload it to the %s folder.", t.Folder)), URL: link}
}

// unlisted is a required dependency that is not a project on the source.
func unlisted(s Step, d dep, t Target) ManualStep {
	name := printable(d.name)
	if s.Source == Hangar {
		link := safeLink(d.external)
		return ManualStep{Notice: notice(KindDepExternal, kv("name", s.Name, "dependency", name, "folder", t.Folder),
			fmt.Sprintf("%s needs %s, which is not on Hangar.", s.Name, name),
			fmt.Sprintf("Download %s yourself and upload it to the %s folder.", name, t.Folder)), URL: link}
	}
	link := ""
	if validRef(s.Slug) {
		link = modrinthPage(t, s.Slug)
	}
	return ManualStep{Notice: notice(KindDepUnlisted, kv("name", s.Name, "file", name, "folder", t.Folder),
		fmt.Sprintf("%s needs the file %s, which is not on Modrinth.", s.Name, name),
		fmt.Sprintf("Look on %s's page for where to get it, then upload it to the %s folder.", s.Name, t.Folder)), URL: link}
}
