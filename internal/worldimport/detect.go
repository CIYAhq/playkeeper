package worldimport

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Inspection is what Inspect found in an upload. Plan and Stage work from
// it; it keeps the list of the upload's files in memory.
type Inspection struct {
	Archives []ArchiveInfo `json:"archives"`
	Worlds   []World       `json:"worlds"`
	Warnings []Message     `json:"warnings,omitempty"`

	ix  *index
	lim Limits
	// roots are all folders that hold a world or a separate dimension.
	roots map[string]bool
}

// Where a world comes from, as far as the upload tells.
const (
	OriginSingleplayer = "singleplayer"
	OriginServer       = "server"
	OriginUnknown      = "unknown"
)

// World is one Java Edition world found in an upload.
type World struct {
	// ID names the world in Options.World.
	ID      string `json:"id"`
	Archive string `json:"archive"`
	// Path is the world's folder inside the archive, "" for its top level.
	Path  string `json:"path"`
	Level *Level `json:"level,omitempty"`
	// LevelError says why level.dat couldn't be read when Level is nil.
	LevelError string `json:"levelError,omitempty"`
	Origin     string `json:"origin"`
	// Software is the server software of the folder around the world, such
	// as "paper" or "fabric", when it can be told.
	Software string `json:"software,omitempty"`
	// Default marks the world the old server's server.properties names, or
	// the only world of the upload.
	Default bool `json:"default,omitempty"`
	// Companions are the separate Nether, End and custom dimension folders
	// of a Paper or Spigot world, such as world_nether and world_the_end.
	Companions []string `json:"companions,omitempty"`
	Dimensions []string `json:"dimensions"`
	Players    int      `json:"players"`
	SizeBytes  int64    `json:"sizeBytes"`
	Files      int      `json:"files"`

	root   string
	dims   []dimFolder
	comps  []companion
	server string
	props  []byte
	ops    []byte
}

// dimFolder is a dimension inside a world folder.
type dimFolder struct {
	id     string
	folder string // relative to the world folder; "" for the Overworld before 26.1
}

// companion is a folder that holds one dimension of a Paper or Spigot
// world, named after the world with a suffix.
type companion struct {
	path     string
	id       string
	folder   string // DIM-1, DIM1 or dimensions/<namespace>/<path>
	suffix   string // _nether, _the_end or _<namespace>_<path>
	guessed  bool
	hasLevel bool
}

const (
	dimOverworld = "minecraft:overworld"
	dimNether    = "minecraft:the_nether"
	dimEnd       = "minecraft:the_end"
)

// Inspect reads the uploaded archives and lists the Java Edition worlds in
// them. It extracts nothing and refuses unsafe or damaged archives with an
// *Error. Zero fields of lim take their DefaultLimits value.
func Inspect(ctx context.Context, sources []Source, lim Limits) (*Inspection, error) {
	lim = lim.withDefaults()
	if len(sources) == 0 {
		return nil, errors.New("worldimport: no archive to inspect")
	}
	ix, err := buildIndex(ctx, sources, lim)
	if err != nil {
		return nil, err
	}
	in := &Inspection{Archives: ix.arcs, ix: ix, lim: lim, roots: map[string]bool{}}
	if err := in.detect(); err != nil {
		return nil, err
	}
	return in, nil
}

type candidate struct {
	path     string
	level    *Level
	levelErr string
	dims     []dimFolder
	comp     *companion
}

func (in *Inspection) detect() error {
	folders, err := in.levelFolders()
	if err != nil {
		return err
	}
	var cands []*candidate
	bedrock := 0
	for _, p := range folders {
		in.roots[p] = true
		if in.ix.isBedrock(p) {
			bedrock++
			continue
		}
		c := &candidate{path: p, dims: in.ix.dimensions(p)}
		c.level, c.levelErr = in.readLevel(p)
		c.comp = companionOf(c)
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		if bedrock > 0 {
			return bedrockError()
		}
		return in.noWorld()
	}
	if bedrock > 0 {
		in.Warnings = append(in.Warnings, note(KindBedrockIgnored, "",
			fmt.Sprintf("The upload also holds %s, which Playkeeper ignores because Java servers can't load them.", plural(bedrock, "Bedrock Edition world", "Bedrock Edition worlds")),
			"count", bedrock))
	}

	var worlds []*World
	var loose []*candidate
	for _, c := range cands {
		if c.comp == nil {
			worlds = append(worlds, in.newWorld(c))
		} else {
			loose = append(loose, c)
		}
	}
	var unpaired []*candidate
	for _, c := range loose {
		if w := pairByName(worlds, c.comp); w != nil {
			w.comps = append(w.comps, *c.comp)
		} else {
			unpaired = append(unpaired, c)
		}
	}
	for _, c := range unpaired {
		if len(worlds) == 1 && !worlds[0].hasDim(c.comp.id) {
			c.comp.guessed = true
			worlds[0].comps = append(worlds[0].comps, *c.comp)
			continue
		}
		c.comp = nil
		worlds = append(worlds, in.newWorld(c))
	}
	in.looseCompanions(worlds)

	for _, w := range worlds {
		in.finishWorld(w)
	}
	if len(worlds) == 1 {
		worlds[0].Default = true
	}
	sort.SliceStable(worlds, func(i, j int) bool {
		if worlds[i].Default != worlds[j].Default {
			return worlds[i].Default
		}
		return worlds[i].ID < worlds[j].ID
	})
	for _, w := range worlds {
		in.Worlds = append(in.Worlds, *w)
	}
	return nil
}

// levelFolders lists the folders that hold a level.dat or level.dat_old.
func (in *Inspection) levelFolders() ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, e := range in.ix.entries {
		if e.dir {
			continue
		}
		if base := path.Base(e.name); base != "level.dat" && base != "level.dat_old" {
			continue
		}
		dir := path.Dir(e.name)
		if seen[dir] || junkPath(dir) {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
		if len(out) > in.lim.MaxWorlds {
			return nil, refuse(KindTooManyWorlds, "Upload only the world you want to import.",
				fmt.Sprintf("The upload holds more than %d worlds, more than Playkeeper handles in one import.", in.lim.MaxWorlds),
				"limit", in.lim.MaxWorlds)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (in *Inspection) noWorld() error {
	hint := "Upload the world folder itself, the folder around it, or the whole server folder."
	for _, e := range in.ix.entries {
		if !e.dir && strings.HasSuffix(e.name, ".mca") {
			hint = "The upload has region files but no level.dat. Upload the whole world folder, including its level.dat file."
			break
		}
	}
	return refuse(KindNoWorld, hint,
		"Playkeeper found no Java Edition world in the upload. A world is a folder with a level.dat file in it.")
}

// isBedrock recognises a Bedrock Edition world: it keeps chunks in a db
// folder and its level.dat isn't gzip-compressed.
func (ix *index) isBedrock(p string) bool {
	if !ix.hasDir(p+"/db") && !ix.hasFile(p+"/levelname.txt") {
		return false
	}
	b, ok := ix.data[p+"/level.dat"]
	return !ok || len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b
}

// readLevel reads the world's level.dat, falling back to level.dat_old.
func (in *Inspection) readLevel(p string) (*Level, string) {
	var problem string
	for _, name := range []string{"level.dat", "level.dat_old"} {
		e, ok := in.ix.find(p + "/" + name)
		if !ok || e.dir {
			continue
		}
		msg := ""
		if b, ok := in.ix.data[e.name]; !ok {
			if limit := captureLimit(name); e.size > limit {
				msg = fmt.Sprintf("%s is larger than %s, more than Playkeeper reads.", name, humanBytes(limit))
			} else {
				msg = fmt.Sprintf("%s wasn't read because the upload holds too many large level.dat files.", name)
			}
		} else if lv, err := ParseLevel(b, in.lim.MaxLevelBytes); err != nil {
			msg = levelProblem(name, err)
		} else {
			lv.FromBackup = name == "level.dat_old"
			if lv.Seed == "" {
				lv.Seed = in.seed(p)
			}
			return lv, ""
		}
		if problem == "" {
			problem = msg
		}
	}
	return nil, problem
}

// seed reads the seed from world_gen_settings.dat, where Minecraft 26.1
// and newer keep it; Paper keeps its copy in the Overworld's data folder.
func (in *Inspection) seed(p string) string {
	for _, f := range []string{paperDataDir + "world_gen_settings.dat", "data/minecraft/world_gen_settings.dat"} {
		if b, ok := in.ix.data[p+"/"+f]; ok {
			if s := readSeed(b, in.lim.MaxLevelBytes); s != "" {
				return s
			}
		}
	}
	return ""
}

// dimensions lists the dimensions inside a world folder, vanilla ones
// first.
func (ix *index) dimensions(p string) []dimFolder {
	lo, hi := ix.under(p)
	seen := map[string]bool{}
	var out []dimFolder
	for _, e := range ix.entries[lo:hi] {
		if e.dir {
			continue
		}
		id, folder, ok := dimOf(e.name[len(p)+1:])
		if ok && !seen[id+"\x00"+folder] {
			seen[id+"\x00"+folder] = true
			out = append(out, dimFolder{id: id, folder: folder})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := dimRank(out[i].id), dimRank(out[j].id); a != b {
			return a < b
		}
		if out[i].id != out[j].id {
			return out[i].id < out[j].id
		}
		return out[i].folder < out[j].folder
	})
	return out
}

// dimOf tells which dimension a file inside a world folder belongs to, and
// that dimension's folder. Before 26.1 the Overworld lives in the world
// folder itself and the Nether and End in DIM-1 and DIM1; since 26.1 every
// dimension lives in dimensions/<namespace>/<path>, as custom dimensions
// always did.
func dimOf(rel string) (id, folder string, ok bool) {
	first, rest, _ := strings.Cut(rel, "/")
	switch first {
	case "region", "entities", "poi":
		return dimOverworld, "", rest != ""
	case "DIM-1", "DIM1":
		kind, inside, _ := strings.Cut(rest, "/")
		if inside == "" || !dimContent(kind) {
			return "", "", false
		}
		if first == "DIM-1" {
			return dimNether, first, true
		}
		return dimEnd, first, true
	case "dimensions":
		ns, rest, _ := strings.Cut(rest, "/")
		segs := strings.Split(rest, "/")
		for i := 1; i+1 < len(segs); i++ {
			if dimContent(segs[i]) {
				p := strings.Join(segs[:i], "/")
				return ns + ":" + p, "dimensions/" + ns + "/" + p, ns != ""
			}
		}
	}
	return "", "", false
}

func dimContent(name string) bool {
	switch name {
	case "region", "entities", "poi", "data":
		return true
	}
	return false
}

func dimRank(id string) int {
	switch id {
	case dimOverworld:
		return 0
	case dimNether:
		return 1
	case dimEnd:
		return 2
	}
	return 3
}

func vanillaDim(id string) bool { return dimRank(id) < 3 }

// dimName names a dimension for a message.
func dimName(id string) string {
	switch id {
	case dimOverworld:
		return "the Overworld"
	case dimNether:
		return "the Nether"
	case dimEnd:
		return "the End"
	}
	return id
}

// modernFolder is the folder Minecraft 26.1 and newer keep dimension id in.
func modernFolder(id string) string {
	ns, p, _ := strings.Cut(id, ":")
	return "dimensions/" + ns + "/" + p
}

// companionOf recognises a folder that holds nothing but one Nether, End or
// custom dimension, like the world_nether and world_the_end folders of a
// Paper or Spigot server, or a Nether or End downloaded on its own, in
// either layout.
func companionOf(c *candidate) *companion {
	if len(c.dims) != 1 {
		return nil
	}
	d := c.dims[0]
	var suffix string
	switch {
	case d.id == dimNether && (d.folder == "DIM-1" || d.folder == modernFolder(dimNether)):
		suffix = "_nether"
	case d.id == dimEnd && (d.folder == "DIM1" || d.folder == modernFolder(dimEnd)):
		suffix = "_the_end"
	case strings.HasPrefix(d.folder, "dimensions/") && !vanillaDim(d.id):
		ns, p, _ := strings.Cut(d.id, ":")
		if strings.Contains(p, "/") {
			return nil
		}
		suffix = "_" + ns + "_" + p
	default:
		return nil
	}
	return &companion{path: c.path, id: d.id, folder: d.folder, suffix: suffix, hasLevel: true}
}

// pairByName finds the world a companion folder belongs to by its name,
// preferring a world in the same folder.
func pairByName(worlds []*World, c *companion) *World {
	base := trimCopySuffix(path.Base(c.path))
	if len(base) <= len(c.suffix) || !strings.HasSuffix(base, c.suffix) {
		return nil
	}
	want := base[:len(base)-len(c.suffix)]
	var match, siblings []*World
	for _, w := range worlds {
		if trimCopySuffix(path.Base(w.root)) != want || w.hasCompanion(c.id) {
			continue
		}
		match = append(match, w)
		if path.Dir(w.root) == path.Dir(c.path) {
			siblings = append(siblings, w)
		}
	}
	switch {
	case len(siblings) == 1:
		return siblings[0]
	case len(match) == 1:
		return match[0]
	}
	return nil
}

// trimCopySuffix removes the " (2)" a browser or Playkeeper adds to the
// name of a second download with the same name.
func trimCopySuffix(s string) string {
	if !strings.HasSuffix(s, ")") {
		return s
	}
	i := strings.LastIndexByte(s, '(')
	if i < 0 || i == len(s)-2 || strings.Trim(s[i+1:len(s)-1], "0123456789") != "" {
		return s
	}
	return strings.TrimRight(s[:i], " ")
}

func (w *World) hasCompanion(id string) bool {
	for _, c := range w.comps {
		if c.id == id {
			return true
		}
	}
	return false
}

func (w *World) hasDim(id string) bool {
	for _, d := range w.dims {
		if d.id == id {
			return true
		}
	}
	return w.hasCompanion(id)
}

// looseCompanions attaches world_nether and world_the_end folders that came
// without a level.dat, such as a Nether downloaded on its own, in either
// layout.
func (in *Inspection) looseCompanions(worlds []*World) {
	markers := []companion{
		{id: dimNether, folder: "DIM-1", suffix: "_nether"},
		{id: dimEnd, folder: "DIM1", suffix: "_the_end"},
		{id: dimNether, folder: modernFolder(dimNether), suffix: "_nether"},
		{id: dimEnd, folder: modernFolder(dimEnd), suffix: "_the_end"},
	}
	seen := map[string]bool{}
	for _, e := range in.ix.entries {
		for _, m := range markers {
			i := strings.Index(e.name, "/"+m.folder+"/")
			if i < 0 {
				continue
			}
			p := e.name[:i]
			if seen[p] || in.insideRoot(p) || junkPath(p) {
				continue
			}
			seen[p] = true
			c := &companion{path: p, id: m.id, folder: m.folder, suffix: m.suffix}
			if w := pairByName(worlds, c); w != nil {
				w.comps = append(w.comps, *c)
				in.roots[p] = true
				continue
			}
			in.Warnings = append(in.Warnings, note(KindOrphanDimension,
				"If it belongs to a world, upload it together with that world's folder, named like world_nether next to world.",
				fmt.Sprintf("%s looks like the %s of a Paper or Spigot world, but Playkeeper found no world it belongs to, so it is ignored.", quoted(in.display(p)), strings.TrimPrefix(dimName(c.id), "the ")),
				"folder", clip(in.display(p))))
		}
	}
}

func (in *Inspection) insideRoot(p string) bool {
	for ; p != "." && p != ""; p = path.Dir(p) {
		if in.roots[p] {
			return true
		}
	}
	return false
}

// display names a folder of the upload the way the user knows it: its path
// inside the archive, or the archive's name for its top level.
func (in *Inspection) display(p string) string {
	if r := rel(p); r != "" {
		return r
	}
	if a := in.ix.archiveOf(p); a != nil {
		return a.Name
	}
	return p
}

func (in *Inspection) newWorld(c *candidate) *World {
	w := &World{ID: c.path, Path: rel(c.path), Level: c.level, LevelError: c.levelErr, root: c.path, dims: c.dims}
	if a := in.ix.archiveOf(c.path); a != nil {
		w.Archive = a.Name
	}
	if parent := path.Dir(c.path); strings.Contains(c.path, "/") && in.ix.isServerFolder(parent) {
		w.server = parent
		w.Software = in.ix.software(parent)
		w.props = in.ix.data[parent+"/server.properties"]
		w.ops = in.ix.data[parent+"/ops.json"]
	}
	return w
}

// finishWorld fills in what the UI shows about a world once its companions
// are known.
func (in *Inspection) finishWorld(w *World) {
	ix := in.ix
	w.Files, w.SizeBytes = ix.stat(ix.under(w.root))
	ids := map[string]bool{}
	for _, d := range w.dims {
		ids[d.id] = true
	}
	for _, c := range w.comps {
		in.roots[c.path] = true
		files, bytes := ix.stat(ix.under(c.path))
		w.Files += files
		w.SizeBytes = addSat(w.SizeBytes, bytes)
		w.Companions = append(w.Companions, in.display(c.path))
		ids[c.id] = true
	}
	for id := range ids {
		w.Dimensions = append(w.Dimensions, id)
	}
	sort.Slice(w.Dimensions, func(i, j int) bool {
		if a, b := dimRank(w.Dimensions[i]), dimRank(w.Dimensions[j]); a != b {
			return a < b
		}
		return w.Dimensions[i] < w.Dimensions[j]
	})
	w.Players = len(ix.playerFiles(w.root))
	switch {
	case w.server != "" || len(w.comps) > 0:
		w.Origin = OriginServer
	case w.Level != nil && w.Level.Owner != "":
		w.Origin = OriginSingleplayer
	default:
		w.Origin = OriginUnknown
	}
	if w.props != nil {
		name, ok := propertyValue(w.props, "level-name")
		if !ok {
			name = "world"
		}
		w.Default = w.root == w.server+"/"+name
	}
}

// playerFiles lists the player data files of a world: playerdata/*.dat
// before 26.1, players/data/*.dat since.
func (ix *index) playerFiles(root string) []string {
	var out []string
	for _, dir := range []string{root + "/playerdata", root + "/players/data"} {
		lo, hi := ix.under(dir)
		for _, e := range ix.entries[lo:hi] {
			name := e.name[len(dir)+1:]
			if !e.dir && !strings.Contains(name, "/") && strings.HasSuffix(name, ".dat") {
				out = append(out, name)
			}
		}
	}
	return out
}

// isServerFolder recognises the folder of a server around a world by the
// files a server keeps next to its worlds.
func (ix *index) isServerFolder(p string) bool {
	for _, f := range []string{"server.properties", "eula.txt", "bukkit.yml", "spigot.yml", "paper.yml", "purpur.yml",
		"ops.json", "whitelist.json", "usercache.json", "banned-players.json", "banned-ips.json"} {
		if ix.hasFile(p + "/" + f) {
			return true
		}
	}
	for _, d := range []string{"plugins", "mods", "libraries", "logs", "crash-reports", "config/paper-global.yml"} {
		if ix.hasDir(p+"/"+d) || ix.hasFile(p+"/"+d) {
			return true
		}
	}
	for _, c := range ix.children(p) {
		if strings.HasSuffix(strings.ToLower(c), ".jar") && ix.hasFile(p+"/"+c) {
			return true
		}
	}
	return false
}

// software tells the server software from the files in its folder, or ""
// when they don't say.
func (ix *index) software(p string) string {
	has := func(f string) bool { return ix.hasFile(p + "/" + f) }
	dir := func(d string) bool { return ix.hasDir(p + "/" + d) }
	switch {
	case has("purpur.yml"):
		return "purpur"
	case has("config/paper-global.yml"), has("paper.yml"):
		return "paper"
	case has("spigot.yml"), has("bukkit.yml"):
		return "spigot"
	case dir("libraries/net/neoforged"):
		return "neoforge"
	case dir("libraries/net/minecraftforge"):
		return "forge"
	case dir(".quilt"), has("quilt-server-launch.jar"):
		return "quilt"
	case dir(".fabric"), has("fabric-server-launch.jar"), has("fabric-server-launcher.properties"):
		return "fabric"
	case dir("plugins"), dir("mods"):
		return ""
	case has("server.properties"):
		return "vanilla"
	}
	return ""
}

// junkName recognises files an operating system adds to folders and
// archives, such as __MACOSX and .DS_Store.
func junkName(base string) bool {
	switch base {
	case "__MACOSX", ".DS_Store", "Thumbs.db", "desktop.ini", ".Spotlight-V100", ".Trashes", ".fseventsd",
		"$RECYCLE.BIN", "System Volume Information":
		return true
	}
	return strings.HasPrefix(base, "._")
}

func junkPath(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if junkName(part) {
			return true
		}
	}
	return false
}
