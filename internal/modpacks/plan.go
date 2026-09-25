package modpacks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// Action is what a plan does with one file.
type Action string

const (
	ActionAdd     Action = "add"
	ActionReplace Action = "replace"
	ActionRemove  Action = "remove"
	ActionKeep    Action = "keep"
)

// Change is one file a plan adds, replaces or removes, or keeps as the user
// left it.
type Change struct {
	Path     string `json:"path"`
	Action   Action `json:"action"`
	Origin   Origin `json:"origin,omitempty"`
	Project  string `json:"project,omitempty"`
	Size     int64  `json:"size"`
	Optional bool   `json:"optional,omitempty"`
	// Yours is set when the file on the server is not what the pack put
	// there: the user changed it, or it was theirs to begin with. A replace
	// then overwrites the user's version, unless the request lists the path
	// in Keep; a keep leaves it as it is.
	Yours bool `json:"yours,omitempty"`
	// World files go into a world the server does not have yet. They are
	// not recorded, so nothing ever removes them.
	World bool `json:"world,omitempty"`
}

// Optional is a file the pack marks as optional on servers.
type Optional struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Project  string `json:"project,omitempty"`
	Size     int64  `json:"size"`
	Included bool   `json:"included"`
}

// Plan is what an install or update would do, for the user to confirm.
type Plan struct {
	// Pack is the version this installs.
	Pack addons.Installed `json:"pack"`
	// Current is the installed version an update replaces.
	Current      *addons.Installed `json:"current,omitempty"`
	Requirements Requirements      `json:"requirements"`
	// Changes lists the files added, replaced and removed, and the user's
	// versions kept; Unchanged counts the pack's files that stay as they
	// are.
	Changes   []Change            `json:"changes"`
	Unchanged int                 `json:"unchanged"`
	Optional  []Optional          `json:"optional"`
	Skipped   []Skipped           `json:"skipped"`
	Manual    []addons.ManualStep `json:"manual"`
	// Properties are server.properties settings the pack suggests.
	// Playkeeper never writes server.properties for a pack; the caller may
	// offer them.
	Properties   map[string]string `json:"properties,omitempty"`
	Blockers     []addons.Notice   `json:"blockers"`
	Warnings     []addons.Notice   `json:"warnings"`
	DownloadSize int64             `json:"downloadSize"`
	// Ready is true when nothing blocks the plan.
	Ready       bool   `json:"ready"`
	Fingerprint string `json:"fingerprint"`

	// The rest is unexported so that a plan which went through JSON cannot
	// be carried out: Install and Update always plan again.
	ops      []op
	excluded []Excluded
	pk       *pack
}

// op is one thing apply does, including keeping unchanged files.
type op struct {
	path   string
	action Action
	file   *packFile // nil for the record's files the pack dropped
	// sum is the hash (algo) of the file on the server when the plan was
	// made, checked again before it is replaced or removed.
	algo, sum   string
	size        int64
	yours       bool
	preexisting bool
}

// InstallRequest asks to put a pack on a server.
type InstallRequest struct {
	Ref
	// Include turns files on or off by path: optional files (Modrinth's are
	// on and CurseForge's off unless listed) and, on update, pack files the
	// user deleted (off unless listed).
	Include map[string]bool `json:"include,omitempty"`
	// Keep lists files the pack would overwrite (changes with Yours set)
	// that should stay as they are.
	Keep []string `json:"keep,omitempty"`
	// AllowPrerelease lets Playkeeper pick a beta or alpha version when no
	// release fits. A version asked for by id is used as it is.
	AllowPrerelease bool `json:"allowPrerelease,omitempty"`
	// Fingerprint is the Plan.Fingerprint the user confirmed. Install
	// refuses when the plan has changed since; empty skips the check.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// UpdateRequest asks to move an installed pack to another version.
type UpdateRequest struct {
	// Version is the version to move to; empty means the newest one for the
	// same Minecraft version and server type.
	Version         string          `json:"version,omitempty"`
	Include         map[string]bool `json:"include,omitempty"`
	Keep            []string        `json:"keep,omitempty"`
	AllowPrerelease bool            `json:"allowPrerelease,omitempty"`
	Fingerprint     string          `json:"fingerprint,omitempty"`
}

// PlanInstall works out what installing a pack on the server would do: the
// server type and versions it needs, every file it adds and every file of
// the user's it would overwrite, what it leaves out and why, and what the
// user has to do by hand. current is the pack already on the server, if
// any: a server has one pack at a time. Only the pack's archive is
// downloaded; nothing is written to the server.
func (l *Library) PlanInstall(ctx context.Context, srv Server, current *Record, req InstallRequest) (*Plan, error) {
	pl, err := l.planInstall(ctx, srv, current, req)
	if err != nil {
		return nil, err
	}
	pl.pk.close()
	return pl, nil
}

func (l *Library) planInstall(ctx context.Context, srv Server, current *Record, req InstallRequest) (*Plan, error) {
	if current != nil {
		return nil, fail(KindPackInstalled, kv("pack", current.Pack.Name, "version", current.Pack.VersionNumber),
			fmt.Sprintf("%s is already installed on this server (version %s).", current.Pack.Name, current.Pack.VersionNumber),
			"Update it, or remove it before installing another pack.")
	}
	root, err := openServer(srv)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	p, err := l.resolve(ctx, req.Ref, req.AllowPrerelease, srv.world())
	if err != nil {
		return nil, err
	}
	pl, err := l.plan(root, srv, p, nil, req.Include, req.Keep)
	if err != nil {
		p.close()
		return nil, err
	}
	return pl, nil
}

// PlanUpdate works out what moving the pack to another version would do:
// the files added, replaced and removed, and which files the user changed
// would be overwritten. The caller should back the server up before
// carrying it out.
func (l *Library) PlanUpdate(ctx context.Context, srv Server, rec Record, req UpdateRequest) (*Plan, error) {
	pl, err := l.planUpdate(ctx, srv, rec, req)
	if err != nil {
		return nil, err
	}
	pl.pk.close()
	return pl, nil
}

func (l *Library) planUpdate(ctx context.Context, srv Server, rec Record, req UpdateRequest) (*Plan, error) {
	root, err := openServer(srv)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	target := req.Version
	if target == "" {
		chk, err := l.CheckUpdate(ctx, rec, req.AllowPrerelease)
		if err != nil {
			return nil, err
		}
		if chk.Latest == nil {
			return nil, upToDate(rec)
		}
		target = chk.Latest.ID
	}
	if target == rec.Pack.VersionID {
		return nil, upToDate(rec)
	}
	p, err := l.resolve(ctx, Ref{Source: rec.Pack.Source, Project: rec.Pack.ProjectID, Version: target}, true, srv.world())
	if err != nil {
		return nil, err
	}
	pl, err := l.plan(root, srv, p, &rec, req.Include, req.Keep)
	if err != nil {
		p.close()
		return nil, err
	}
	return pl, nil
}

func upToDate(rec Record) *addons.Error {
	return fail(addons.KindUpToDate, kv("pack", rec.Pack.Name, "version", rec.Pack.VersionNumber),
		fmt.Sprintf("%s is up to date (version %s).", rec.Pack.Name, rec.Pack.VersionNumber), "")
}

// openServer opens the server's folder; every later file operation stays
// inside it.
func openServer(srv Server) (*os.Root, error) {
	if srv.Dir == "" {
		return nil, fail(addons.KindInvalid, kv("field", "dir"), "The server has no folder.", "")
	}
	r, err := os.OpenRoot(srv.Dir)
	if err != nil {
		return nil, serverFolderError(err)
	}
	return r, nil
}

func serverFolderError(err error) *addons.Error {
	return &addons.Error{Notice: notice(addons.KindFolderUnusable, kv("folder", "server"),
		"Playkeeper cannot use the server's folder: "+err.Error()+".",
		"Check that the server's folder exists and is a normal folder, then try again."), Err: err}
}

// state is what the server has at a path.
type state struct {
	exists, regular bool
	// blocked is a parent folder of the path that is a file or a link.
	blocked string
	sums    map[string]string
	size    int64
}

// inspect looks at a path on the server without following links and hashes
// a regular file with each algorithm.
func inspect(root *os.Root, p string, algos ...string) (state, error) {
	for i := strings.IndexByte(p, '/'); i >= 0; i = next(p, i) {
		fi, err := root.Lstat(p[:i])
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return state{}, nil
		case err != nil:
			return state{}, err
		case !fi.IsDir():
			return state{exists: true, blocked: p[:i]}, nil
		}
	}
	fi, err := root.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return state{}, nil
	case err != nil:
		return state{}, err
	case !fi.Mode().IsRegular():
		return state{exists: true}, nil
	}
	f, err := root.Open(p)
	if err != nil {
		return state{}, err
	}
	defer f.Close()
	hs := newHashes(algos...)
	ws := make([]io.Writer, 0, len(hs))
	for _, h := range hs {
		ws = append(ws, h)
	}
	n, err := io.Copy(io.MultiWriter(ws...), f)
	if err != nil {
		return state{}, err
	}
	return state{exists: true, regular: true, sums: sums(hs), size: n}, nil
}

// plan compares the pack with the server and, on update, the record of the
// version installed now.
func (l *Library) plan(root *os.Root, srv Server, p *pack, old *Record, include map[string]bool, keep []string) (*Plan, error) {
	pl := &Plan{
		Pack: p.info, Requirements: p.reqs, Changes: []Change{}, Optional: []Optional{}, Skipped: slices.Clone(p.skipped),
		Manual: append([]addons.ManualStep{}, p.manual...), Properties: p.properties,
		Blockers: append([]addons.Notice{}, p.blockers...), Warnings: append([]addons.Notice{}, p.warnings...), pk: p,
	}
	if pl.Skipped == nil {
		pl.Skipped = []Skipped{}
	}
	if len(pl.Properties) == 0 {
		pl.Properties = nil
	}
	oldFiles := map[string]*File{}
	var removed []Excluded // pack files the user deleted
	if old != nil {
		cur := old.Pack
		pl.Current = &cur
		for i := range old.Files {
			f := &old.Files[i]
			if !recorded(f, srv.world()) {
				continue
			}
			oldFiles[f.Path] = f
			st, err := inspect(root, f.Path)
			if err != nil {
				return nil, serverFolderError(err)
			}
			if !st.exists {
				removed = append(removed, Excluded{Path: f.Path, Project: f.Project})
			}
		}
		removed = append(removed, old.Excluded...)
	}
	if err := l.checkServer(pl, root, srv); err != nil {
		return nil, err
	}
	for _, rel := range slices.Sorted(maps.Keys(p.files)) {
		f := p.files[rel]
		on, why := true, addons.Kind("")
		if f.optional {
			on = f.on && !matches(removed, f)
		} else if matches(removed, f) {
			on, why = false, KindUserRemoved
		}
		if v, ok := include[rel]; ok {
			on = v
		}
		if f.optional {
			pl.Optional = append(pl.Optional, Optional{Path: rel, Name: f.name, Project: f.project, Size: f.size, Included: on})
			why = KindOptionalOff
		}
		if !on {
			pl.Skipped = append(pl.Skipped, Skipped{Path: rel, Reason: why})
			pl.excluded = append(pl.excluded, Excluded{Path: rel, Project: f.project})
			continue
		}
		if err := l.planFile(pl, root, f, oldFiles[rel], old != nil, slices.Contains(keep, rel)); err != nil {
			return nil, err
		}
	}
	for _, rel := range slices.Sorted(maps.Keys(oldFiles)) {
		if p.files[rel] == nil {
			if err := planDropped(pl, root, oldFiles[rel]); err != nil {
				return nil, err
			}
		}
	}
	if old == nil {
		l.extraMods(pl, root, p)
	}
	slices.SortStableFunc(pl.Skipped, func(a, b Skipped) int { return strings.Compare(a.Path, b.Path) })
	slices.SortStableFunc(pl.ops, func(a, b op) int { return strings.Compare(a.path, b.path) })
	for _, o := range pl.ops {
		c := Change{Path: o.path, Action: o.action, Yours: o.yours}
		if f := o.file; f != nil {
			c.Origin, c.Project, c.Size, c.Optional, c.World = f.origin, f.project, f.size, f.optional, f.world
			if f.origin == Download && (o.action == ActionAdd || o.action == ActionReplace) {
				pl.DownloadSize += f.size
			}
		}
		if o.action == ActionKeep && !o.yours {
			pl.Unchanged++
			continue
		}
		pl.Changes = append(pl.Changes, c)
	}
	pl.Ready = len(pl.Blockers) == 0
	pl.Fingerprint = pl.fingerprint()
	return pl, nil
}

// matches reports whether a pack file is one the user does not want: the
// same path, or the same project in another file.
func matches(ex []Excluded, f *packFile) bool {
	return slices.ContainsFunc(ex, func(e Excluded) bool {
		return e.Path == f.path || e.Project != "" && e.Project == f.project
	})
}

func (l *Library) planFile(pl *Plan, root *os.Root, f *packFile, old *File, update, keep bool) error {
	algo := f.algo()
	algos := []string{algo}
	if old != nil && old.HashAlgo != algo && (old.HashAlgo == "sha1" || old.HashAlgo == "sha512") {
		algos = append(algos, old.HashAlgo)
	}
	st, err := inspect(root, f.path, algos...)
	if err != nil {
		return serverFolderError(err)
	}
	o := op{path: f.path, file: f, algo: algo, sum: st.sums[algo], size: st.size}
	switch {
	case st.blocked != "":
		pl.Blockers = append(pl.Blockers, inTheWay(pl.Pack.Name, st.blocked, f.path))
		return nil
	case f.world && (update || st.exists || topExists(root, f.path)):
		pl.Skipped = append(pl.Skipped, Skipped{Path: f.path, Reason: KindWorld})
		return nil
	case !st.exists:
		o.action = ActionAdd
	case !st.regular:
		pl.Blockers = append(pl.Blockers, inTheWay(pl.Pack.Name, f.path, f.path))
		return nil
	case st.sums[algo] == f.sums[algo]:
		o.action = ActionKeep
		o.preexisting = old == nil || old.Preexisting
	case old != nil && st.sums[old.HashAlgo] == old.Hash:
		o.action = ActionReplace
		o.preexisting = old.Preexisting
	default:
		// The server has the user's version of the file.
		o.yours = true
		switch {
		case old != nil && f.sums[old.HashAlgo] == old.Hash:
			o.action, o.preexisting = ActionKeep, old.Preexisting
		case keep:
			o.action, o.preexisting = ActionKeep, true
		default:
			o.action = ActionReplace
		}
	}
	pl.ops = append(pl.ops, o)
	return nil
}

// planDropped handles a file of the installed version that the new version
// no longer has: removed when unchanged, left alone otherwise.
func planDropped(pl *Plan, root *os.Root, f *File) error {
	st, err := inspect(root, f.Path, f.HashAlgo)
	switch {
	case err != nil:
		return serverFolderError(err)
	case !st.exists || st.blocked != "":
		return nil
	case !st.regular || f.Preexisting || st.sums[f.HashAlgo] != f.Hash:
		pl.ops = append(pl.ops, op{path: f.Path, action: ActionKeep, yours: true})
	default:
		pl.ops = append(pl.ops, op{path: f.Path, action: ActionRemove, algo: f.HashAlgo, sum: f.Hash, size: st.size})
	}
	return nil
}

// topExists reports whether the top folder of a path exists, for world
// files: the world is either new or left alone as a whole.
func topExists(root *os.Root, p string) bool {
	top, _, _ := strings.Cut(p, "/")
	_, err := root.Lstat(top)
	return err == nil
}

// checkServer compares what the pack needs with the server.
func (l *Library) checkServer(pl *Plan, root *os.Root, srv Server) error {
	r := pl.Requirements
	if srv.Type != "" && srv.Type != r.Type || srv.MinecraftVersion != "" && srv.MinecraftVersion != r.MinecraftVersion {
		hint := ""
		if srv.Type == "paper" || srv.Type == "purpur" {
			hint = fmt.Sprintf("Plugins in the plugins folder do not load on a %s server.", typeName(r.Type))
		}
		pl.Warnings = append(pl.Warnings, notice(KindTypeChange,
			kv("pack", pl.Pack.Name, "type", r.Type, "minecraft", r.MinecraftVersion, "serverType", srv.Type, "serverMinecraft", srv.MinecraftVersion),
			fmt.Sprintf("%s needs a %s server on Minecraft %s; this server runs %s %s, and the install switches it.",
				pl.Pack.Name, typeName(r.Type), r.MinecraftVersion, typeName(srv.Type), printable(srv.MinecraftVersion)), hint))
	}
	if srv.MinecraftVersion == "" || minecraft.CompareMinecraft(r.MinecraftVersion, srv.MinecraftVersion) >= 0 {
		return nil
	}
	if _, err := root.Lstat(srv.world()); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return serverFolderError(err)
	}
	pl.Blockers = append(pl.Blockers, notice(KindDowngrade, kv("pack", pl.Pack.Name, "minecraft", r.MinecraftVersion, "serverMinecraft", srv.MinecraftVersion),
		fmt.Sprintf("%s is for Minecraft %s, older than this server's %s, and Minecraft cannot open a world in an older version.", pl.Pack.Name, r.MinecraftVersion, srv.MinecraftVersion),
		fmt.Sprintf("Choose a version of the pack for Minecraft %s or newer, or create a new server for it.", srv.MinecraftVersion)))
	return nil
}

// extraMods warns about mods already on the server that are not the pack's.
func (l *Library) extraMods(pl *Plan, root *os.Root, p *pack) {
	fi, err := root.Lstat("mods")
	if err != nil || !fi.IsDir() {
		return
	}
	d, err := root.Open("mods")
	if err != nil {
		return
	}
	defer d.Close()
	entries, _ := d.ReadDir(2001)
	var extra []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".jar") && !strings.HasPrefix(n, ".") && p.files["mods/"+n] == nil {
			extra = append(extra, n)
		}
	}
	if len(extra) == 0 {
		return
	}
	slices.Sort(extra)
	pl.Warnings = append(pl.Warnings, notice(KindExtraMods, kv("pack", pl.Pack.Name, "count", strconv.Itoa(len(extra)), "first", printable(extra[0])),
		fmt.Sprintf("The mods folder already has %d mods that are not part of %s, such as %s. They stay, and may clash with the pack's mods.", len(extra), pl.Pack.Name, printable(extra[0])),
		"Remove the ones the pack does not need before starting the server."))
}

func inTheWay(pack, blocker, p string) addons.Notice {
	return notice(KindInTheWay, kv("pack", pack, "path", printable(blocker), "file", printable(p)),
		fmt.Sprintf("%s needs to write %s, but %s on the server is a folder, a link or a special file.", pack, printable(p), printable(blocker)),
		"Move it out of the way in the file manager, then try again.")
}

func (pl *Plan) fingerprint() string {
	b, _ := json.Marshal(struct {
		Pack         addons.Installed
		Requirements Requirements
		Changes      []Change
		Unchanged    int
		Optional     []Optional
		Skipped      []Skipped
		Manual       []addons.ManualStep
		Properties   map[string]string
		Blockers     []addons.Notice
	}{pl.Pack, pl.Requirements, pl.Changes, pl.Unchanged, pl.Optional, pl.Skipped, pl.Manual, pl.Properties, pl.Blockers})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// UpdateCheck says whether a newer version of an installed pack exists.
type UpdateCheck struct {
	Current addons.Installed `json:"current"`
	// Latest is the newest version for the same Minecraft version and
	// server type, or nil when the installed version is the newest.
	Latest *Version `json:"latest,omitempty"`
	// Newest is the pack's newest version Playkeeper can run, when it needs
	// another Minecraft version or server type: moving to it changes the
	// server, and a newer Minecraft version upgrades the world for good.
	Newest *Version `json:"newest,omitempty"`
}

// CheckUpdate looks for newer versions of an installed pack. Pre-releases
// count when allowPre is set or the installed version is one.
func (l *Library) CheckUpdate(ctx context.Context, rec Record, allowPre bool) (*UpdateCheck, error) {
	allowPre = allowPre || rec.Pack.Channel != "release"
	vs, err := l.versions(ctx, rec.Pack.Source, rec.Pack.ProjectID, "")
	if err != nil {
		return nil, err
	}
	out := &UpdateCheck{Current: rec.Pack}
	newer := func(v *Version) bool {
		return v.ID != rec.Pack.VersionID && v.Published.After(rec.Pack.Published)
	}
	for i := range vs {
		v := &vs[i]
		if v.Unsupported != nil || !newer(v) || v.Channel != "release" && !allowPre {
			continue
		}
		same := v.MinecraftVersion == rec.Requirements.MinecraftVersion && (v.Type == "" || v.Type == rec.Requirements.Type)
		if same && out.Latest == nil {
			out.Latest = v
		}
		if !same && out.Newest == nil && (out.Latest == nil || v.Published.After(out.Latest.Published)) {
			out.Newest = v
		}
	}
	if out.Newest != nil && out.Latest != nil && !out.Newest.Published.After(out.Latest.Published) {
		out.Newest = nil
	}
	return out, nil
}
