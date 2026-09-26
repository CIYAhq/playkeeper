package worldimport

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

// Server types a world can be imported into. Spigot and CraftBukkit are
// missing on purpose: since 26.1 they keep worlds in a layout no other
// server reads.
const (
	TypePaper    = "paper"
	TypePurpur   = "purpur"
	TypeFolia    = "folia"
	TypeVanilla  = "vanilla"
	TypeFabric   = "fabric"
	TypeQuilt    = "quilt"
	TypeForge    = "forge"
	TypeNeoForge = "neoforge"
)

type family int

const (
	familyBukkit family = iota + 1
	familyVanilla
	familyModded
)

func familyOf(t string) (family, bool) {
	switch t {
	case TypePaper, TypePurpur, TypeFolia:
		return familyBukkit, true
	case TypeVanilla:
		return familyVanilla, true
	case TypeFabric, TypeQuilt, TypeForge, TypeNeoForge:
		return familyModded, true
	}
	return 0, false
}

// paperDataDir is where Paper 26.1 and newer keep world data that vanilla
// keeps in data/minecraft/.
const paperDataDir = "dimensions/minecraft/overworld/data/minecraft/"

// paperMovedFiles are the files Paper's migration guide moves from
// paperDataDir to data/minecraft/ for a vanilla, Fabric or Forge server:
// https://docs.papermc.io/paper/migration/ ("To Vanilla").
func paperMovedFiles() []string {
	return []string{"game_rules.dat", "scheduled_events.dat", "wandering_trader.dat", "weather.dat", "world_gen_settings.dat"}
}

// Target is the server a world is imported into.
type Target struct {
	Type             string `json:"type"`
	MinecraftVersion string `json:"minecraftVersion"`
	// LevelName is the server's level-name, the folder of its world; ""
	// means "world".
	LevelName string `json:"levelName"`
}

// Options are the owner's choices for an import.
type Options struct {
	// World is the ID of the world to import. It may be empty when the
	// upload holds only one.
	World string `json:"world"`
	// KeepAddons copies the old server's plugins for Paper and its forks,
	// or its mods and their configuration for Fabric, Quilt, Forge and
	// NeoForge.
	KeepAddons bool `json:"keepAddons"`
	// KeepPlayerLists copies the whitelist and the ban lists.
	KeepPlayerLists bool `json:"keepPlayerLists"`
	// KeepOperators copies ops.json, which makes its players operators.
	KeepOperators bool `json:"keepOperators"`
}

// Preview is what an import will do, for the owner to confirm.
type Preview struct {
	World   World         `json:"world"`
	Target  Target        `json:"target"`
	Version *VersionCheck `json:"version,omitempty"`
	// Folders are the world folders Stage writes, such as world and
	// world_nether. They replace the server's world folders.
	Folders []string `json:"folders"`
	// Files and Addons are the server files and folders Stage writes next
	// to them, such as whitelist.json and plugins.
	Files      []string    `json:"files,omitempty"`
	Addons     []string    `json:"addons,omitempty"`
	FileCount  int         `json:"fileCount"`
	SizeBytes  int64       `json:"sizeBytes"`
	Dimensions []Dimension `json:"dimensions"`
	DataPacks  []string    `json:"dataPacks,omitempty"`
	Players    int         `json:"players"`
	// Operators are the players ops.json makes operators, when it is kept.
	Operators []string `json:"operators,omitempty"`
	// Settings go into the server's server.properties; see ApplySettings.
	Settings        []Setting `json:"settings,omitempty"`
	RefusedSettings []string  `json:"refusedSettings,omitempty"`
	IgnoredSettings []string  `json:"ignoredSettings,omitempty"`
	LeftOut         []LeftOut `json:"leftOut,omitempty"`
	Warnings        []Message `json:"warnings,omitempty"`
	// Problems stop the import; Stage refuses while there are any.
	Problems []Message `json:"problems,omitempty"`
}

// OK reports whether the import can go ahead.
func (p *Preview) OK() bool { return len(p.Problems) == 0 }

// Dimension is one dimension of the staged world.
type Dimension struct {
	ID     string `json:"id"`
	Folder string `json:"folder"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
}

// LeftOut is a group of files of the upload that Stage doesn't write.
type LeftOut struct {
	Kind     string   `json:"kind"`
	Files    int      `json:"files"`
	Bytes    int64    `json:"bytes"`
	Examples []string `json:"examples,omitempty"`
	Text     string   `json:"text"`
}

// Plan compares the chosen world with the target server and returns what
// an import would write, leave out and warn about, without writing
// anything. Problems in the preview stop the import; an *Error means the
// request itself can't be served.
func (in *Inspection) Plan(t Target, o Options) (*Preview, error) {
	p, _, err := in.plan(t, o)
	return p, err
}

// Roles of the files Stage writes.
const (
	roleWorld = iota + 1
	roleAddon
	roleServerFile
)

type copyOp struct {
	e    int
	dest string
	role int
}

type ownerKind int

const (
	ownWorld ownerKind = iota + 1
	ownCompanion
	ownServer
	ownOther
)

type owner struct {
	kind ownerKind
	comp *companion
}

type planner struct {
	in           *Inspection
	w            *World
	lv           *Level
	t            Target
	o            Options
	fam          family
	modern       bool
	targetModern bool
	staleMain    []string
	staleComp    map[string]bool
	split        map[string]string
	moved        map[string]bool
	owners       map[string]owner
	ops          []copyOp
	left         map[string]*LeftOut
	p            *Preview
}

func (in *Inspection) plan(t Target, o Options) (*Preview, []copyOp, error) {
	t, fam, err := checkTarget(t)
	if err != nil {
		return nil, nil, err
	}
	w, err := in.pick(o.World)
	if err != nil {
		return nil, nil, err
	}
	pl := &planner{
		in: in, w: w, lv: w.Level, t: t, o: o, fam: fam,
		staleComp: map[string]bool{}, split: map[string]string{}, moved: map[string]bool{},
		left: map[string]*LeftOut{},
		p:    &Preview{World: *w, Target: t, Folders: []string{}, Dimensions: []Dimension{}},
	}
	pl.checkLevel()
	pl.analyze()
	pl.setOwners()
	pl.assign()
	pl.levelCopies()
	if err := pl.finish(); err != nil {
		return nil, nil, err
	}
	return pl.p, pl.ops, nil
}

func checkTarget(t Target) (Target, family, error) {
	fam, ok := familyOf(t.Type)
	if !ok {
		return t, 0, refuse(KindTargetType, "Choose a Paper, Purpur, Folia, Vanilla, Fabric, Quilt, Forge or NeoForge server.",
			fmt.Sprintf("Playkeeper can't import a world into a server of type %s.", quoted(t.Type)), "type", clip(t.Type))
	}
	if !validVersion(t.MinecraftVersion) {
		return t, 0, refuse(KindTargetVersion, "Choose the server's Minecraft version first.",
			fmt.Sprintf("%s is not a Minecraft version.", quoted(t.MinecraftVersion)), "version", clip(t.MinecraftVersion))
	}
	if t.LevelName == "" {
		t.LevelName = "world"
	}
	if !validLevelName(t.LevelName) {
		return t, 0, levelNameError(t.LevelName)
	}
	return t, fam, nil
}

func levelNameError(n string) *Error {
	return refuse(KindTargetLevelName, "Change level-name in the server's settings to a plain folder name, such as world.",
		fmt.Sprintf("The server's level-name %s can't be used as the name of its world folder.", quoted(n)), "levelName", clip(n))
}

func validVersion(v string) bool {
	if v == "" || len(v) > 32 || v[0] < '0' || v[0] > '9' {
		return false
	}
	for _, c := range []byte(v) {
		if !(c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z' || c == '.' || c == '-' || c == '_' || c == '+') {
			return false
		}
	}
	return true
}

func validLevelName(n string) bool {
	if n == "" || len(n) > 100 || !utf8.ValidString(n) || n == "." || n == ".." || strings.TrimSpace(n) != n {
		return false
	}
	switch strings.ToLower(n) {
	case "plugins", "mods", "config", "defaultconfigs", "libraries", "versions", "logs", "crash-reports",
		"ops.json", "whitelist.json", "banned-players.json", "banned-ips.json", "server.properties":
		return false
	}
	return strings.IndexFunc(n, func(r rune) bool { return r == '/' || r == '\\' || unsafeRune(r) }) < 0
}

func (in *Inspection) pick(id string) (*World, error) {
	if id == "" {
		if len(in.Worlds) == 1 {
			return &in.Worlds[0], nil
		}
		return nil, refuse(KindChooseWorld, "Choose one of the worlds the upload holds.",
			fmt.Sprintf("The upload holds %d worlds. Choose the one to import.", len(in.Worlds)), "count", len(in.Worlds))
	}
	for i := range in.Worlds {
		if in.Worlds[i].ID == id {
			return &in.Worlds[i], nil
		}
	}
	return nil, refuse(KindUnknownWorld, "Choose one of the worlds the upload holds.",
		fmt.Sprintf("The upload has no world %s.", quoted(id)), "world", clip(id))
}

func (pl *planner) problem(m Message) { pl.p.Problems = append(pl.p.Problems, m) }
func (pl *planner) warn(m Message)    { pl.p.Warnings = append(pl.p.Warnings, m) }

func (pl *planner) checkLevel() {
	if pl.lv == nil {
		detail := pl.w.LevelError
		pl.problem(note(KindLevelUnreadable, "Upload the world again. If its level.dat is damaged, take level.dat from an older backup of the world.",
			"This world can't be imported because Playkeeper can't read its level.dat. "+detail, "detail", detail))
		return
	}
	v := CheckVersion(pl.lv, pl.t.MinecraftVersion)
	pl.p.Version = &v
	if v.Problem != nil {
		pl.problem(*v.Problem)
	}
	pl.p.Warnings = append(pl.p.Warnings, v.Warnings...)
}

// modernTarget reports whether a Minecraft version keeps worlds in the
// layout 26.1 introduced.
func modernTarget(v string) bool {
	if dv, ok := releaseData(v); ok {
		return dv >= modernLayoutData
	}
	return compareVersions(v, "26.1") >= 0
}

func legacyFolder(folder string) bool { return folder == "" || folder == "DIM-1" || folder == "DIM1" }

// analyze decides how the world's folders map onto the target's layout:
// which copies are stale, whether Paper's separate Nether and End folders
// are split out or merged in, and what can't be converted at all.
func (pl *planner) analyze() {
	w, L := pl.w, pl.t.LevelName
	pl.modern = pl.lv != nil && pl.lv.DataVersion >= modernLayoutData
	pl.targetModern = modernTarget(pl.t.MinecraftVersion)
	modernHas := map[string]string{}
	for _, d := range w.dims {
		if !legacyFolder(d.folder) {
			modernHas[d.id] = d.folder
		}
	}
	if pl.modern {
		for _, d := range w.dims {
			if !legacyFolder(d.folder) {
				continue
			}
			if kept, ok := modernHas[d.id]; ok {
				pl.staleMainDim(d, pl.inWorld(kept))
				continue
			}
			if d.id == dimOverworld {
				pl.spigotLayout()
			} else {
				pl.problem(note(KindMixedLayout, "Upload the world as the server last saved it, without older copies of its folders.",
					fmt.Sprintf("This world was saved by Minecraft 26.1 or newer, but %s is still in the older %s folder. Playkeeper can't tell how to convert it.", dimName(d.id), d.folder),
					"dimension", d.id))
			}
		}
		// A separate Nether, End or custom dimension merges into the world's
		// own layout (see companionFile), unless the world holds its own copy.
		for _, c := range w.comps {
			if kept, ok := modernHas[c.id]; ok {
				pl.staleComp[c.path] = true
				pl.staleWarning(c.id, pl.inWorld(kept), pl.in.display(c.path))
			}
		}
	} else {
		for _, d := range w.dims {
			if vanillaDim(d.id) && !legacyFolder(d.folder) {
				pl.problem(note(KindMixedLayout, "Upload the world as the server last saved it, without copies from a newer Minecraft version.",
					fmt.Sprintf("This world was saved by a Minecraft version older than 26.1, but %s is in %s, a folder only newer versions use. Playkeeper can't tell which copy is current.", dimName(d.id), d.folder),
					"dimension", d.id))
			}
		}
		for _, c := range w.comps {
			if vanillaDim(c.id) && !legacyFolder(c.folder) {
				pl.problem(note(KindMixedLayout, "Upload the Nether and the End the server saved together with this world.",
					fmt.Sprintf("This world was saved by a Minecraft version older than 26.1, but %s in %s was saved by 26.1 or newer. Playkeeper can't combine them.", dimName(c.id), quoted(pl.in.display(c.path))),
					"dimension", c.id))
				continue
			}
			for _, d := range w.dims {
				if d.id == c.id {
					pl.staleMainDim(d, pl.in.display(c.path))
				}
			}
		}
	}

	if pl.modern && pl.fam != familyBukkit {
		for _, f := range paperMovedFiles() {
			if pl.in.ix.hasFile(w.root + "/" + paperDataDir + f) {
				pl.moved[f] = true
			}
		}
		if len(pl.moved) > 0 {
			pl.warn(note(KindPaperFiles, "",
				fmt.Sprintf("This world comes from a Paper server, which keeps settings such as game rules and weather in the Overworld's folder. Playkeeper moves them to data/minecraft, where %s servers look for them.", typeName(pl.t.Type)),
				"target", pl.t.Type))
		}
	}

	if pl.fam == familyBukkit && !pl.targetModern && !pl.modern {
		for _, d := range w.dims {
			if (d.folder == "DIM-1" || d.folder == "DIM1") && !pl.staleRel(d.folder) && !w.hasCompanion(d.id) {
				pl.split[d.folder] = L + map[string]string{"DIM-1": "_nether", "DIM1": "_the_end"}[d.folder]
			}
		}
		if len(pl.split) > 0 {
			pl.warn(note(KindBukkitSplit, "",
				fmt.Sprintf("Paper keeps the Nether and the End in their own folders, %s_nether and %s_the_end. Playkeeper moves them there.", L, L),
				"levelName", L))
		}
	}

	if pl.lv != nil && pl.lv.DataVersion > 0 && !pl.modern && pl.targetModern {
		pl.warn(note(KindLayoutUpgrade, "Big worlds can take several minutes; let the first start finish.",
			fmt.Sprintf("Minecraft %s keeps worlds in a newer folder layout. The server converts this world when it first starts.", pl.t.MinecraftVersion),
			"target", pl.t.MinecraftVersion))
	}
}

func (pl *planner) spigotLayout() {
	for _, m := range pl.p.Problems {
		if m.Kind == KindSpigotLayout {
			return
		}
	}
	pl.problem(note(KindSpigotLayout, "Convert the world to the vanilla layout with Spigot's own instructions first, then upload it again.",
		"This world was saved by Spigot or CraftBukkit on Minecraft 26.1 or newer. They keep worlds in their own folder layout, which other servers can't read."))
}

// staleMainDim leaves out an older copy of a dimension inside the world
// folder, because a newer copy is kept.
func (pl *planner) staleMainDim(d dimFolder, kept string) {
	if d.folder == "" {
		pl.staleMain = append(pl.staleMain, "region", "entities", "poi")
	} else {
		pl.staleMain = append(pl.staleMain, d.folder)
	}
	dropped := d.folder
	if dropped == "" {
		dropped = "region"
	}
	pl.staleWarning(d.id, kept, pl.inWorld(dropped))
}

func (pl *planner) staleWarning(id, kept, dropped string) {
	pl.warn(note(KindStaleDimension, "",
		fmt.Sprintf("The upload has two copies of %s: %s and %s. Playkeeper keeps %s, the one the server used last, and leaves out the other.", dimName(id), quoted(kept), quoted(dropped), quoted(kept)),
		"dimension", id, "kept", clip(kept), "dropped", clip(dropped)))
}

// inWorld names a folder inside the chosen world the way the user sees it.
func (pl *planner) inWorld(folder string) string {
	return path.Join(pl.in.display(pl.w.root), folder)
}

func (pl *planner) staleRel(rel string) bool {
	for _, s := range pl.staleMain {
		if rel == s || strings.HasPrefix(rel, s+"/") {
			return true
		}
	}
	return false
}

func (pl *planner) setOwners() {
	w := pl.w
	pl.owners = map[string]owner{}
	for r := range pl.in.roots {
		pl.owners[r] = owner{kind: ownOther}
	}
	if w.server != "" {
		pl.owners[w.server] = owner{kind: ownServer}
	}
	pl.owners[w.root] = owner{kind: ownWorld}
	for i := range w.comps {
		pl.owners[w.comps[i].path] = owner{kind: ownCompanion, comp: &w.comps[i]}
	}
}

// ownerOf finds the innermost world, companion or server folder a file is
// in, and that folder.
func (pl *planner) ownerOf(name string) (owner, string) {
	for p := path.Dir(name); p != "."; p = path.Dir(p) {
		if o, ok := pl.owners[p]; ok {
			return o, p
		}
	}
	return owner{}, ""
}

func (pl *planner) assign() {
	for i, e := range pl.in.ix.entries {
		if e.dir {
			continue
		}
		if junkPath(e.name) {
			pl.leave(LeftSystemFiles, i)
			continue
		}
		o, base := pl.ownerOf(e.name)
		switch o.kind {
		case ownWorld:
			pl.worldFile(i, e.name[len(base)+1:])
		case ownCompanion:
			pl.companionFile(i, o.comp, e.name[len(base)+1:])
		case ownServer:
			pl.serverFile(i, e.name[len(base)+1:])
		case ownOther:
			pl.leave(LeftOtherWorlds, i)
		default:
			pl.leave(LeftOther, i)
		}
	}
}

func (pl *planner) keep(i int, dest string, role int) {
	pl.ops = append(pl.ops, copyOp{e: i, dest: dest, role: role})
}

func (pl *planner) leave(kind string, i int) {
	e := pl.in.ix.entries[i]
	l := pl.left[kind]
	if l == nil {
		l = &LeftOut{Kind: kind}
		pl.left[kind] = l
	}
	l.Files++
	l.Bytes = addSat(l.Bytes, e.size)
	if len(l.Examples) < 3 {
		l.Examples = append(l.Examples, clip(rel(e.name)))
	}
}

func (pl *planner) worldFile(i int, rel string) {
	L := pl.t.LevelName
	dir, base := path.Split(rel)
	first, _, _ := strings.Cut(rel, "/")
	switch {
	case rel == "session.lock":
		pl.leave(LeftSessionLock, i)
	case pl.staleRel(rel):
		pl.leave(LeftStaleCopies, i)
	case dir == paperDataDir && pl.moved[base]:
		pl.keep(i, L+"/data/minecraft/"+base, roleWorld)
	case dir == "data/minecraft/" && pl.moved[base]:
		pl.leave(LeftStaleCopies, i)
	case pl.split[first] != "":
		pl.keep(i, pl.split[first]+"/"+rel, roleWorld)
	default:
		pl.keep(i, L+"/"+rel, roleWorld)
	}
}

func (pl *planner) companionFile(i int, c *companion, rel string) {
	L := pl.t.LevelName
	switch {
	case rel == "session.lock":
		pl.leave(LeftSessionLock, i)
	case pl.staleComp[c.path]:
		pl.leave(LeftStaleCopies, i)
	case pl.fam == familyBukkit && !pl.modern:
		pl.keep(i, L+c.suffix+"/"+rel, roleWorld)
	case rel == c.folder || strings.HasPrefix(rel, c.folder+"/"):
		// A world saved by 26.1 or newer keeps the Nether and the End in
		// dimensions/minecraft, even on Paper; one downloaded from an older
		// layout moves there.
		if pl.modern && legacyFolder(c.folder) {
			rel = modernFolder(c.id) + strings.TrimPrefix(rel, c.folder)
		}
		pl.keep(i, L+"/"+rel, roleWorld)
	default:
		pl.leave(LeftCompanionFiles, i)
	}
}

func (pl *planner) serverFile(i int, rel string) {
	first, rest, nested := strings.Cut(rel, "/")
	lower := strings.ToLower(rel)
	switch {
	case first == "plugins" && nested:
		switch {
		case pl.fam != familyBukkit || !pl.o.KeepAddons:
			pl.leave(LeftAddons, i)
		case strings.HasPrefix(rest, ".paper-remapped/"):
			pl.leave(LeftCaches, i)
		default:
			pl.keep(i, rel, roleAddon)
		}
	case first == "mods" && nested:
		if pl.fam == familyModded && pl.o.KeepAddons {
			pl.keep(i, rel, roleAddon)
		} else {
			pl.leave(LeftAddons, i)
		}
	case (first == "config" || first == "defaultconfigs") && nested && pl.fam == familyModded && pl.o.KeepAddons &&
		pl.in.ix.hasDir(pl.w.server+"/mods"):
		pl.keep(i, rel, roleAddon)
	case !nested && (rel == "whitelist.json" || rel == "banned-players.json" || rel == "banned-ips.json"):
		if pl.o.KeepPlayerLists {
			pl.keep(i, rel, roleServerFile)
		} else {
			pl.leave(LeftPlayerLists, i)
		}
	case !nested && rel == "ops.json":
		if pl.o.KeepOperators {
			pl.keep(i, rel, roleServerFile)
		} else {
			pl.leave(LeftOperators, i)
		}
	case first == "logs" || first == "crash-reports" || first == "debug" ||
		!nested && (strings.HasSuffix(lower, ".log") || strings.HasSuffix(lower, ".log.gz")):
		pl.leave(LeftLogs, i)
	case first == "libraries" || first == "versions" || first == "bundler" || !nested && strings.HasSuffix(lower, ".jar"):
		pl.leave(LeftServerSoftware, i)
	case first == "cache" || first == ".cache" || first == ".fabric" || first == ".quilt" || first == ".mixin.out" ||
		!nested && (rel == "usercache.json" || rel == "version_history.json" || rel == ".console_history"):
		pl.leave(LeftCaches, i)
	case first == "config" || first == "defaultconfigs" || !nested && serverConfigFile(lower):
		pl.leave(LeftServerConfig, i)
	default:
		pl.leave(LeftOther, i)
	}
}

func serverConfigFile(lower string) bool {
	switch lower {
	case "eula.txt", "user_jvm_args.txt", "unix_args.txt", "win_args.txt":
		return true
	}
	if strings.HasPrefix(lower, ".rcon-cli") {
		return true
	}
	switch path.Ext(lower) {
	case ".yml", ".yaml", ".properties", ".toml", ".json", ".json5", ".conf", ".cfg", ".ini",
		".sh", ".bat", ".cmd", ".ps1", ".command":
		return true
	}
	return false
}

// levelCopies gives every separate Nether and End folder of a Paper server
// before 26.1 a copy of the world's level.dat, so it keeps the world's seed
// and game rules instead of starting with defaults.
func (pl *planner) levelCopies() {
	if pl.fam != familyBukkit || pl.targetModern {
		return
	}
	var folders []string
	for _, dest := range pl.split {
		folders = append(folders, dest)
	}
	for _, c := range pl.w.comps {
		if !c.hasLevel && !pl.staleComp[c.path] {
			folders = append(folders, pl.t.LevelName+c.suffix)
		}
	}
	sort.Strings(folders)
	ix := pl.in.ix
	for _, name := range []string{"level.dat", "level.dat_old"} {
		i := ix.search(pl.w.root + "/" + name)
		if i == len(ix.entries) || ix.entries[i].name != pl.w.root+"/"+name || ix.entries[i].dir {
			continue
		}
		for _, f := range folders {
			pl.keep(i, f+"/"+name, roleWorld)
		}
		return
	}
}

func (pl *planner) finish() error {
	ix, lim, p, L := pl.in.ix, pl.in.lim, pl.p, pl.t.LevelName
	sort.SliceStable(pl.ops, func(a, b int) bool { return pl.ops[a].dest < pl.ops[b].dest })
	dests := make(map[string]bool, len(pl.ops))
	for _, op := range pl.ops {
		if dests[op.dest] {
			return destConflict(op.dest)
		}
		dests[op.dest] = true
	}
	for _, op := range pl.ops {
		for d := path.Dir(op.dest); d != "."; d = path.Dir(d) {
			if dests[d] {
				return destConflict(d)
			}
		}
	}

	perArc := make([]int64, len(ix.arcs))
	dims := map[string]*Dimension{}
	seen := map[string]bool{}
	var tooLarge *entry
	terrain, opsKept := false, false
	for _, op := range pl.ops {
		e := ix.entries[op.e]
		p.FileCount++
		p.SizeBytes = addSat(p.SizeBytes, e.size)
		perArc[e.arc] = addSat(perArc[e.arc], e.size)
		if e.size > lim.MaxFileBytes && tooLarge == nil {
			tooLarge = &e
		}
		if e.csize >= 0 && e.size > entryAllowance(e.csize, lim.MaxRatio) {
			return ratioError(ix.arcs[e.arc].Name, lim.MaxRatio)
		}
		top, inside, _ := strings.Cut(op.dest, "/")
		switch op.role {
		case roleWorld:
			if !seen[top] {
				seen[top] = true
				p.Folders = append(p.Folders, top)
			}
			if id, folder, ok := dimOf(inside); ok {
				key := id + "\x00" + top + "/" + folder
				d := dims[key]
				if d == nil {
					d = &Dimension{ID: id, Folder: strings.TrimSuffix(top+"/"+folder, "/")}
					dims[key] = d
				}
				d.Files++
				d.Bytes = addSat(d.Bytes, e.size)
				if id == dimOverworld && strings.HasSuffix(inside, ".mca") && strings.Contains("/"+inside, "/region/") {
					terrain = true
				}
			}
		case roleAddon:
			if !seen[top] {
				seen[top] = true
				p.Addons = append(p.Addons, top)
			}
		case roleServerFile:
			p.Files = append(p.Files, op.dest)
			opsKept = opsKept || op.dest == "ops.json"
		}
	}
	for i, n := range perArc {
		if n > ratioAllowance(ix.arcs[i].Bytes, lim.MaxRatio) {
			return ratioError(ix.arcs[i].Name, lim.MaxRatio)
		}
	}
	for _, d := range dims {
		p.Dimensions = append(p.Dimensions, *d)
	}
	sort.Slice(p.Dimensions, func(i, j int) bool {
		a, b := p.Dimensions[i], p.Dimensions[j]
		if ra, rb := dimRank(a.ID), dimRank(b.ID); ra != rb {
			return ra < rb
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Folder < b.Folder
	})
	sort.Strings(p.Folders)
	sort.Strings(p.Addons)

	if p.FileCount > lim.MaxFiles {
		pl.problem(note(KindTooManyFiles, "Leave out what the server doesn't need, or ask for a higher limit.",
			fmt.Sprintf("The import would write %d files, more than the %d Playkeeper writes in one import.", p.FileCount, lim.MaxFiles),
			"files", p.FileCount, "limit", lim.MaxFiles))
	}
	if p.SizeBytes > lim.MaxTotalBytes {
		pl.problem(note(KindTooLarge, "Free up disk space on the server, or leave out plugins, mods and other worlds.",
			fmt.Sprintf("The import would write %s, more than the %s this server has room for.", humanBytes(p.SizeBytes), humanBytes(lim.MaxTotalBytes)),
			"bytes", p.SizeBytes, "limit", lim.MaxTotalBytes))
	}
	if tooLarge != nil {
		pl.problem(note(KindFileTooLarge, "Remove that file from the upload if the world doesn't need it.",
			fmt.Sprintf("%s is %s, larger than the %s Playkeeper writes for one file.", quoted(rel(tooLarge.name)), humanBytes(tooLarge.size), humanBytes(lim.MaxFileBytes)),
			"path", clip(rel(tooLarge.name)), "bytes", tooLarge.size, "limit", lim.MaxFileBytes))
	}

	pl.warnings(terrain, opsKept)
	p.DataPacks = pl.dataPacks()
	p.Players = pl.w.Players
	if opsKept {
		p.Operators = operatorNames(pl.w.ops)
	}
	p.Settings, p.RefusedSettings, p.IgnoredSettings = CarrySettings(pl.w.props, pl.lv)
	for _, kind := range []string{LeftSessionLock, LeftSystemFiles, LeftLogs, LeftCaches, LeftServerSoftware, LeftServerConfig,
		LeftAddons, LeftPlayerLists, LeftOperators, LeftStaleCopies, LeftCompanionFiles, LeftOtherWorlds, LeftOther} {
		if l := pl.left[kind]; l != nil {
			l.Text = leftOutText(kind, l.Files, l.Bytes, L)
			p.LeftOut = append(p.LeftOut, *l)
		}
	}
	return nil
}

// dataPacks lists the packs in the world's datapacks folder that level.dat
// doesn't turn off: Minecraft turns on a pack it finds there for the first
// time, so level.dat alone misses packs added since the last save. Paper
// writes its own "bukkit" pack for plugins; it isn't the world's.
func (pl *planner) dataPacks() []string {
	off := map[string]bool{"file/bukkit": true}
	if pl.lv != nil {
		for _, id := range pl.lv.DisabledPacks {
			off[id] = true
		}
	}
	var out []string
	dir := pl.w.root + "/datapacks"
	for _, name := range pl.in.ix.children(dir) {
		p := dir + "/" + name
		pack := pl.in.ix.hasFile(p+"/pack.mcmeta") || strings.HasSuffix(strings.ToLower(name), ".zip") && pl.in.ix.hasFile(p)
		if id := "file/" + name; pack && !off[id] {
			out = append(out, id)
		}
	}
	return out
}

func destConflict(dest string) *Error {
	return refuse(KindDuplicate, "Upload the world without the extra copy of this file.",
		fmt.Sprintf("Two parts of the upload would both be written to %s.", quoted(dest)), "path", clip(dest))
}

func (pl *planner) warnings(terrain, opsKept bool) {
	w, lv, p, target := pl.w, pl.lv, pl.p, typeName(pl.t.Type)
	if lv != nil {
		if lv.FromBackup {
			pl.warn(note(KindLevelBackup, "",
				"level.dat is damaged, so Playkeeper uses level.dat_old, the copy Minecraft keeps from the save before. Settings kept in level.dat, such as the time of day, may be slightly older."))
		}
		if lv.ownerInLevel && lv.Owner != "" && !pl.in.ix.hasFile(w.root+"/playerdata/"+lv.Owner+".dat") {
			pl.warn(note(KindOwnerData, "Open the world once in singleplayer with a current Minecraft version and save it; that stores the player where servers read it.",
				"This singleplayer world keeps its player's inventory and position only in level.dat, which a server doesn't read. That player starts fresh on the server.",
				"owner", lv.Owner))
		}
		if brands := modBrands(lv.Brands); len(brands) > 0 && !pl.sameLoader(brands) {
			pl.warn(note(KindModded, "Use a server with the same mods if you need their blocks and items.",
				fmt.Sprintf("This world ran with mods (%s). Blocks, items and dimensions from those mods are lost on a %s server.", strings.Join(brands, ", "), target),
				"brands", brands, "target", pl.t.Type))
		}
		if len(lv.Features) > 0 {
			pl.warn(note(KindExperimental, "",
				fmt.Sprintf("This world has experimental features switched on (%s). They stay on, but experimental features can change or break with Minecraft updates.", strings.Join(lv.Features, ", ")),
				"features", lv.Features))
		}
		if lv.Hardcore {
			pl.warn(note(KindHardcore, "",
				"This is a hardcore world: players who die become spectators. Playkeeper sets hardcore=true on the server so it stays that way."))
		}
	}
	offline := 0
	for _, f := range pl.in.ix.playerFiles(w.root) {
		if len(f) == 40 && f[14] == '3' && f[8] == '-' {
			offline++
		}
	}
	if offline > 0 {
		pl.warn(note(KindOfflinePlayers, "This happens with worlds from servers that ran with online-mode off. Their progress can only be moved to their real accounts with a separate conversion tool.",
			fmt.Sprintf("%s in this world played with offline-mode accounts. With online-mode on, they join as different players and start fresh.", plural(offline, "player", "players")),
			"count", offline))
	}
	var custom []string
	for _, d := range p.Dimensions {
		if !vanillaDim(d.ID) && !contains(custom, d.ID) {
			custom = append(custom, d.ID)
		}
	}
	if len(custom) > 0 {
		pl.warn(note(KindCustomDimensions, "Add the data pack or mod that creates them to the server.",
			fmt.Sprintf("This world has custom dimensions (%s). Their files are kept, but they only load with the data pack or mod that adds them.", strings.Join(custom, ", ")),
			"dimensions", custom))
	}
	if !terrain {
		pl.warn(note(KindNoTerrain, "Check that you uploaded the whole world folder, including its region folder.",
			"The upload has no terrain for the Overworld, so the server generates new terrain around spawn."))
	}
	if len(p.Addons) > 0 {
		pl.warn(note(KindAddonsKept, "Check that each one supports this server's Minecraft version before relying on it.",
			fmt.Sprintf("The old server's %s are copied too.", strings.Join(p.Addons, " and ")),
			"folder", strings.Join(p.Addons, ",")))
	}
	if opsKept {
		names := operatorNames(w.ops)
		text := "ops.json is copied, so the players in it become operators with full control of the server."
		if len(names) > 0 {
			text = fmt.Sprintf("ops.json is copied, so these players become operators with full control of the server: %s.", strings.Join(names, ", "))
		}
		pl.warn(note(KindOperatorsKept, "Keep ops.json only if you trust everyone in it.", text, "operators", names))
	}
	var merged []string
	for _, c := range w.comps {
		if c.guessed && !pl.staleComp[c.path] {
			pl.warn(note(KindCompanionGuessed, "If that's wrong, upload the world and that folder separately.",
				fmt.Sprintf("Playkeeper treats %s as %s of this world because it is the only world in the upload.", quoted(pl.in.display(c.path)), dimName(c.id)),
				"folder", clip(pl.in.display(c.path)), "dimension", c.id))
		}
		if (pl.fam != familyBukkit || pl.modern) && !pl.staleComp[c.path] {
			merged = append(merged, pl.in.display(c.path))
		}
	}
	if len(merged) > 0 {
		pl.warn(note(KindMerged, "",
			fmt.Sprintf("This world comes from a Paper or Spigot server, which keeps dimensions in separate folders (%s). Playkeeper merges them into the world folder, where %s servers keep them.", strings.Join(merged, ", "), target),
			"folders", merged))
	}
}

// sameLoader reports whether the target runs the mod loader the world ran
// with.
func (pl *planner) sameLoader(brands []string) bool {
	if pl.fam != familyModded {
		return false
	}
	for _, b := range brands {
		if strings.EqualFold(b, pl.t.Type) {
			return true
		}
	}
	return false
}

// modBrands picks the mod loaders from a world's server brands.
func modBrands(brands []string) []string {
	var out []string
	for _, b := range brands {
		switch strings.ToLower(b) {
		case "fabric", "quilt", "forge", "neoforge":
			if !contains(out, strings.ToLower(b)) {
				out = append(out, strings.ToLower(b))
			}
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func typeName(t string) string {
	switch t {
	case TypePaper:
		return "Paper"
	case TypePurpur:
		return "Purpur"
	case TypeFolia:
		return "Folia"
	case TypeVanilla:
		return "Vanilla"
	case TypeFabric:
		return "Fabric"
	case TypeQuilt:
		return "Quilt"
	case TypeForge:
		return "Forge"
	case TypeNeoForge:
		return "NeoForge"
	}
	return t
}

// operatorNames reads the player names from ops.json.
func operatorNames(b []byte) []string {
	var ops []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(b, &ops) != nil {
		return nil
	}
	var out []string
	for _, op := range ops {
		if len(out) == maxListedKeys {
			break
		}
		if n := cleanText(op.Name, 32); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func leftOutText(kind string, files int, bytes int64, level string) string {
	size := plural(files, "file", "files") + ", " + humanBytes(bytes)
	switch kind {
	case LeftSessionLock:
		return "session.lock is left out; it only marks a world as open."
	case LeftSystemFiles:
		return fmt.Sprintf("Files your computer added, such as __MACOSX and .DS_Store, are left out (%s).", size)
	case LeftLogs:
		return fmt.Sprintf("Logs and crash reports are left out (%s).", size)
	case LeftCaches:
		return fmt.Sprintf("Caches are left out; the server rebuilds them (%s).", size)
	case LeftServerSoftware:
		return fmt.Sprintf("The old server's software is left out; this server brings its own (%s).", size)
	case LeftServerConfig:
		return fmt.Sprintf("The old server's configuration is left out; this server keeps its own settings, apart from the ones listed (%s).", size)
	case LeftAddons:
		return fmt.Sprintf("Plugins and mods are left out (%s). Choose to keep them if this server should run them.", size)
	case LeftPlayerLists:
		return fmt.Sprintf("The whitelist and ban lists are left out (%s). Choose to keep them to bring them along.", size)
	case LeftOperators:
		return "The operator list, ops.json, is left out. Choose to keep it to bring it along."
	case LeftStaleCopies:
		return fmt.Sprintf("Older copies of world folders are left out (%s).", size)
	case LeftCompanionFiles:
		return fmt.Sprintf("Files Paper or Spigot kept next to the separate Nether and End folders are left out; %s doesn't use them (%s).", level, size)
	case LeftOtherWorlds:
		return fmt.Sprintf("Other worlds in the upload are left out (%s).", size)
	}
	return fmt.Sprintf("Other files outside the world are left out (%s).", size)
}
