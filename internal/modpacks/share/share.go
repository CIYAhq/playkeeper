// Package share builds what friends need to play on a modded server: a label
// per mod saying whether friends' games need it, the notice at the top of the
// Mods tab ("Friends need the pack plus Waystones"), a client .mrpack made
// from the server's setup, and the data of the public /packs/<slug> page.
//
// The .mrpack holds one file, modrinth.index.json: each mod's path, size,
// hashes and download address on Modrinth's CDN, and the Minecraft and loader
// versions, so friends' launchers download the mods from Modrinth themselves.
// It has no overrides, no configs and no server-only mods. Nothing in it is
// read from the server's disk: it is made from the pack's record and the
// add-on library's records, and the list of files taken from the server is
// empty. Mods the file can't link, such as CurseForge files or files inside
// the pack's own archive, are listed for friends to get themselves, with a
// link.
//
// The same setup always gives the same file, byte for byte. Building a share
// asks Modrinth about the files; a built share answers pages and downloads
// without asking anyone, so callers keep it until Setup.Key changes.
package share

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
	"github.com/CIYAhq/playkeeper/internal/modpacks"
	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
	"github.com/CIYAhq/playkeeper/internal/version"
)

// Kinds this package adds to the add-on library's.
const (
	KindUnsupported addons.Kind = "share_unsupported"
	KindNoVersion   addons.Kind = "share_version_unknown"
)

// Setup is the server's setup a share is made from.
type Setup struct {
	// Name is the server's name; friends' launchers name the instance after
	// it.
	Name             string
	Type             string // fabric, quilt or neoforge
	MinecraftVersion string
	LoaderVersion    string
	// Pack is the modpack's record, or nil without one.
	Pack *modpacks.Record
	// Addons are the add-on library's records for the server: the mods the
	// user added and their dependencies.
	Addons []addons.Installed
}

// Key identifies what a share is made from, without asking anyone. Keep a
// share and serve it until the key changes; a new Playkeeper version changes
// it too, since it may build shares differently.
func (s Setup) Key() string {
	c := s
	c.Addons = sortedBy(s.Addons, func(i addons.Installed) string { return string(i.Source) + "\x00" + i.ProjectID + "\x00" + i.FileName })
	if s.Pack != nil {
		r := *s.Pack
		r.Files = sortedBy(r.Files, func(f modpacks.File) string { return f.Path })
		r.Client = sortedBy(r.Client, func(f modpacks.ClientFile) string { return f.Path })
		r.Excluded = sortedBy(r.Excluded, func(e modpacks.Excluded) string { return e.Path })
		c.Pack = &r
	}
	b, _ := json.Marshal(struct {
		Playkeeper string
		Setup      Setup
	}{version.Version, c})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func sortedBy[T any](s []T, key func(T) string) []T {
	s = slices.Clone(s)
	slices.SortStableFunc(s, func(a, b T) int { return cmp.Compare(key(a), key(b)) })
	return s
}

// Limits bound a share.
type Limits struct {
	Files int   // mods, resource packs and shader packs in the setup
	Index int64 // the file's modrinth.index.json
}

// DefaultLimits returns the limits used for every limit left at zero.
func DefaultLimits() Limits { return Limits{Files: 5000, Index: 8 << 20} }

// Builder makes shares. One Builder serves every server on a machine.
type Builder struct {
	Modrinth *modrinth.Client
	Limits   Limits
}

func (b *Builder) limits() Limits {
	lim, d := b.Limits, DefaultLimits()
	if lim.Files <= 0 {
		lim.Files = d.Files
	}
	if lim.Index <= 0 {
		lim.Index = d.Index
	}
	return lim
}

// Share is what friends get from a server's setup.
type Share struct {
	// Key is the Setup.Key the share was made from.
	Key              string `json:"key"`
	Server           string `json:"server"`
	Type             string `json:"type"`
	MinecraftVersion string `json:"minecraftVersion"`
	LoaderVersion    string `json:"loaderVersion"`
	// Pack is nil without a modpack.
	Pack   *Pack `json:"pack,omitempty"`
	Notice Text  `json:"notice"`
	// Mods are every mod, resource pack and shader pack on the server or in
	// the pack's files for players: the user's first, then the pack's, each
	// by name. Server-only ones are included, so this is for signed-in
	// users; the public page lists only what friends get.
	Mods []Mod `json:"mods"`
	// Yourself are the mods friends need that the file can't link.
	Yourself []Yourself `json:"yourself,omitempty"`
	// Index is the file's modrinth.index.json.
	Index mrpack.Index `json:"index"`
}

// Pack is the server's modpack as friends see it.
type Pack struct {
	Name    string        `json:"name"`
	Version string        `json:"version"`
	Source  addons.Source `json:"source"`
	Page    string        `json:"page,omitempty"`
	// Need is Required when friends need any of its mods, else Unknown,
	// Optional or ServerOnly, in that order.
	Need  Need `json:"need"`
	Label Text `json:"label"`
}

// From says where a mod in a setup comes from.
type From string

const (
	FromUser From = "user" // the add-on library: "Added by you"
	FromPack From = "pack" // the modpack's record
)

// Mod is one mod, resource pack or shader pack and what friends need of it.
type Mod struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	// Path is where it goes in the game's folder, such as
	// mods/sodium-fabric-0.9.2+mc26.2.jar.
	Path string `json:"path"`
	From From   `json:"from"`
	// Source and Project name the add-on record for the user's mods. For
	// the pack's they are the file's project on CurseForge for a CurseForge
	// pack's downloads, else on Modrinth when Modrinth has the file.
	Source  addons.Source `json:"source,omitempty"`
	Project string        `json:"project,omitempty"`
	// DependencyOf is copied from the add-on library's record.
	DependencyOf string `json:"dependencyOf,omitempty"`
	Page         string `json:"page,omitempty"`
	// OnServer is false for the pack's files that only players' games get.
	OnServer bool `json:"onServer"`
	Need     Need `json:"need"`
	Label    Text `json:"label"`
	// InFile is set when the friends' file includes it, and ByHand when
	// friends get it themselves (see Share.Yourself).
	InFile bool `json:"inFile"`
	ByHand bool `json:"byHand,omitempty"`
}

// Yourself is a mod friends get by hand, because the file can't link it.
type Yourself struct {
	Name string `json:"name"`
	// Path is where it goes in the game's folder.
	Path string `json:"path"`
	// Page is where to download it: its page on Modrinth or CurseForge, its
	// address on the host its pack lists, or the pack's page. It may be
	// empty when there is nowhere to send friends.
	Page   string `json:"page,omitempty"`
	Need   Need   `json:"need"`
	Reason Text   `json:"reason"`
}

// Supported reports whether a server type has mods to share: Fabric, Quilt
// and NeoForge.
func Supported(serverType string) bool {
	switch serverType {
	case "fabric", "quilt", "neoforge":
		return true
	}
	return false
}

// hashBatch bounds the hashes in one lookup. Modrinth answers with whole
// versions, changelogs included, and the client accepts 16 MiB an answer.
const hashBatch = 100

// Build makes the share for a server's setup. It asks Modrinth which of the
// files it has, with their sides and dependencies, in a few batched requests.
func (b *Builder) Build(ctx context.Context, s Setup) (*Share, error) {
	if !Supported(s.Type) {
		return nil, fail(KindUnsupported, kv("type", printable(s.Type)),
			"Only Fabric, Quilt and NeoForge servers have mods to share with friends.",
			"Friends join Paper, Purpur and vanilla servers with the plain game.")
	}
	if !mrpack.ValidToken(s.MinecraftVersion) || !mrpack.ValidToken(s.LoaderVersion) {
		return nil, fail(KindNoVersion, kv("minecraft", printable(s.MinecraftVersion), "loader", printable(s.LoaderVersion)),
			"Playkeeper doesn't know which Minecraft and loader versions this server runs, so it can't make the friends' file.",
			"Check the server's version in its settings.")
	}
	lim := b.limits()
	items := addonItems(s.Addons)
	if s.Pack != nil {
		items = append(items, packItems(s.Pack)...)
	}
	if len(items) > lim.Files {
		return nil, fail(modpacks.KindTooManyFiles, kv("limit", strconv.Itoa(lim.Files)),
			fmt.Sprintf("This server has more than %d mods, resource packs and shader packs, more than Playkeeper puts in one friends' file.", lim.Files),
			"Send friends the modpack's page instead.")
	}
	if err := b.lookUp(ctx, items); err != nil {
		return nil, err
	}
	for _, it := range items {
		it.resolve()
	}
	raise(items)

	sh := &Share{Key: s.Key(), Server: serverName(s.Name), Type: s.Type, MinecraftVersion: s.MinecraftVersion, LoaderVersion: s.LoaderVersion}
	if s.Pack != nil {
		sh.Pack = packInfo(s.Pack, items)
	}
	files := sh.place(items)
	sh.Notice = notice(sh.Pack, items)
	sh.Index = index(sh, files)
	ix, err := indexJSON(&sh.Index)
	if err != nil {
		return nil, err
	}
	if int64(len(ix)) > lim.Index {
		return nil, fail(addons.KindTooLarge, kv("limit", fetch.Size(lim.Index)),
			fmt.Sprintf("The friends' file would be larger than the %s Playkeeper serves.", fetch.Size(lim.Index)),
			"Remove add-ons friends don't need, or send them the modpack's page instead.")
	}
	if _, err := mrpack.Parse(ix); err != nil {
		return nil, fmt.Errorf("share: the friends' file fails Playkeeper's own check: %w", err)
	}
	return sh, nil
}

// item is one mod while a share is made.
type item struct {
	Mod
	sha1, sha512 string
	size         int64
	origin       modpacks.Origin
	downloads    []string // the pack's own addresses for the file
	// own is what the pack or the source says; Unknown asks Modrinth.
	own Need
	// curseForge marks a CurseForge pack's download, which its manifest
	// may require or not.
	curseForge, required bool
	requires             []string // Modrinth projects a user's mod needs
	version              *modrinth.Version
	file                 *modrinth.File // the version's file with the same hash
	project              *modrinth.Project
	url                  string // the file on Modrinth's CDN, or ""
}

func addonItems(ins []addons.Installed) []*item {
	var items []*item
	for _, in := range ins {
		it := &item{
			Mod: Mod{Name: printable(in.Name), Version: printable(in.VersionNumber), Path: "mods/" + in.FileName, From: FromUser,
				Source: in.Source, Project: in.ProjectID, DependencyOf: in.DependencyOf, OnServer: true},
			size: in.Size, own: Unknown,
		}
		switch in.Source {
		case addons.Modrinth:
			it.requires = in.Requires
			it.Page = modrinthPage("mod", in.Slug, in.ProjectID)
		case addons.Hangar:
			// Hangar only has server plugins.
			it.own = ServerOnly
		}
		it.setHash(in.HashAlgo, in.Hash)
		items = append(items, it)
	}
	return items
}

// packItems lists the pack's files for players, then its other mods on the
// server, leaving out the files the user turned off or deleted.
func packItems(r *modpacks.Record) []*item {
	excluded, onServer := map[string]bool{}, map[string]bool{}
	for _, e := range r.Excluded {
		excluded[e.Path] = true
	}
	for _, f := range r.Files {
		onServer[f.Path] = true
	}
	cf := r.Pack.Source == modpacks.CurseForge
	seen := map[string]bool{}
	var items []*item
	for _, c := range r.Client {
		if seen[c.Path] || excluded[c.Path] || !modpacks.PlayerContent(c.Path) {
			continue
		}
		seen[c.Path] = true
		it := &item{
			Mod:  Mod{Path: c.Path, From: FromPack, OnServer: onServer[c.Path]},
			size: c.Size, origin: c.Origin, downloads: c.Downloads, own: PackNeed(c.Env),
		}
		it.setHash("sha1", c.SHA1)
		it.setHash("sha512", c.SHA512)
		switch {
		case cf && c.Origin == modpacks.Download:
			it.curseForge, it.required = true, c.Required
			it.own = CurseForgeNeed(folderOf(c.Path), c.Sides, c.Required)
			it.Name, it.Version, it.Page = printable(c.Name), printable(c.Version), curseForgeLink(c.Page)
			it.Source, it.Project = modpacks.CurseForge, c.Project
		case !cf && validID(c.Project):
			it.Source, it.Project = addons.Modrinth, c.Project
		}
		items = append(items, it)
	}
	for _, f := range r.Files {
		if seen[f.Path] || excluded[f.Path] || !modpacks.PlayerContent(f.Path) {
			continue
		}
		seen[f.Path] = true
		// The pack keeps these from players' games.
		it := &item{Mod: Mod{Path: f.Path, From: FromPack, OnServer: true}, size: f.Size, origin: f.Origin, own: ServerOnly}
		it.setHash(f.HashAlgo, f.Hash)
		if !cf && validID(f.Project) {
			it.Source, it.Project = addons.Modrinth, f.Project
		}
		items = append(items, it)
	}
	return items
}

func (it *item) setHash(algo, h string) {
	h = strings.ToLower(h)
	switch {
	case strings.EqualFold(algo, "sha512") && isHex(h, 128):
		it.sha512 = h
	case strings.EqualFold(algo, "sha1") && isHex(h, 40):
		it.sha1 = h
	}
}

// lookUp asks Modrinth which of the files it has, and for their projects.
func (b *Builder) lookUp(ctx context.Context, items []*item) error {
	var h512, h1 []string
	for _, it := range items {
		switch {
		case it.sha512 != "":
			h512 = append(h512, it.sha512)
		case it.sha1 != "":
			h1 = append(h1, it.sha1)
		}
	}
	by512, err := b.byHash(ctx, "sha512", h512)
	if err != nil {
		return err
	}
	by1, err := b.byHash(ctx, "sha1", h1)
	if err != nil {
		return err
	}
	var ids []string
	for _, it := range items {
		var (
			v  modrinth.Version
			ok bool
		)
		switch {
		case it.sha512 != "":
			v, ok = by512[it.sha512]
		case it.sha1 != "":
			v, ok = by1[it.sha1]
		}
		if ok {
			if f := sameFile(&v, it.sha1, it.sha512); f != nil {
				it.version, it.file = &v, f
			}
		}
		if id := it.modrinthProject(); validID(id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	ps, err := b.Modrinth.Projects(ctx, slices.Compact(ids))
	if err != nil {
		return modpacks.Upstream(addons.Modrinth, err)
	}
	byID := make(map[string]*modrinth.Project, len(ps))
	for i := range ps {
		byID[ps[i].ID] = &ps[i]
	}
	for _, it := range items {
		it.project = byID[it.modrinthProject()]
	}
	return nil
}

func (b *Builder) byHash(ctx context.Context, algo string, hashes []string) (map[string]modrinth.Version, error) {
	slices.Sort(hashes)
	out := map[string]modrinth.Version{}
	for chunk := range slices.Chunk(slices.Compact(hashes), hashBatch) {
		vs, err := b.Modrinth.VersionsFromHashes(ctx, algo, chunk)
		if err != nil {
			return nil, modpacks.Upstream(addons.Modrinth, err)
		}
		for h, v := range vs {
			out[strings.ToLower(h)] = v
		}
	}
	return out, nil
}

// sameFile finds the version's file with the item's hash.
func sameFile(v *modrinth.Version, sha1, sha512 string) *modrinth.File {
	for i := range v.Files {
		f := &v.Files[i]
		if sha512 != "" && strings.EqualFold(f.Hashes.SHA512, sha512) || sha512 == "" && strings.EqualFold(f.Hashes.SHA1, sha1) {
			return f
		}
	}
	return nil
}

func (it *item) modrinthProject() string {
	switch {
	case it.version != nil:
		return it.version.ProjectID
	case it.Source == addons.Modrinth:
		return it.Project
	}
	return ""
}

// resolve settles the item's need, name, page and link from what the pack,
// the source and Modrinth say.
func (it *item) resolve() {
	it.Need = it.own
	if it.own == Unknown {
		it.Need = ModrinthNeed(it.version, it.project)
		if it.curseForge {
			it.Need = capped(it.Need, it.required)
		}
	}
	if p := it.project; p != nil {
		if it.Name == "" {
			it.Name = printable(p.Title)
		}
		if it.Page == "" || !it.curseForge {
			it.Page = cmp.Or(modrinthPage(p.ProjectType, p.Slug, p.ID), it.Page)
		}
		if !it.curseForge && it.From == FromPack {
			it.Source, it.Project = addons.Modrinth, p.ID
		}
	}
	if v := it.version; v != nil && it.Version == "" {
		it.Version = printable(v.VersionNumber)
	}
	if it.Name == "" {
		it.Name = printable(strings.TrimSuffix(path.Base(it.Path), path.Ext(it.Path)))
	}
	if f := it.file; f != nil && cdn(f.URL) && isHex(strings.ToLower(f.Hashes.SHA1), 40) && isHex(strings.ToLower(f.Hashes.SHA512), 128) {
		if name := "mods/" + f.Filename; it.From == FromUser && !modpacks.PlayerContent(it.Path) && modpacks.PlayerContent(name) {
			it.Path = name
		}
		it.url, it.size = f.URL, f.Size
		it.sha1, it.sha512 = strings.ToLower(f.Hashes.SHA1), strings.ToLower(f.Hashes.SHA512)
		return
	}
	if it.sha1 != "" && it.sha512 != "" {
		if i := slices.IndexFunc(it.downloads, cdn); i >= 0 {
			it.url = it.downloads[i]
		}
	}
}

// raise gives each mod's required dependencies at least the mod's own need:
// friends need Balm for Waystones, though Balm alone could stay on the
// server.
func raise(items []*item) {
	byProject := map[string][]*item{}
	projectOf := map[string]string{}
	for _, it := range items {
		if p := it.modrinthProject(); p != "" {
			byProject[p] = append(byProject[p], it)
		}
		if it.version != nil {
			projectOf[it.version.ID] = it.version.ProjectID
		}
	}
	for changed := true; changed; {
		changed = false
		for _, it := range items {
			if it.Need.rank() == 0 {
				continue
			}
			for _, p := range it.dependencies(projectOf) {
				for _, d := range byProject[p] {
					if d.Need.rank() < it.Need.rank() {
						d.Need, changed = it.Need, true
					}
				}
			}
		}
	}
}

func (it *item) dependencies(projectOf map[string]string) []string {
	var out []string
	if it.From == FromUser && it.Source == addons.Modrinth {
		out = append(out, it.requires...)
	}
	if it.version != nil {
		for _, d := range it.version.Dependencies {
			if d.DependencyType != modrinth.Required {
				continue
			}
			if p := cmp.Or(d.ProjectID, projectOf[d.VersionID]); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// place puts each mod friends may need into the file or the list to get by
// hand, and fills Mods. The user's mods go first, so their version of a
// project the pack also has is the one friends get.
func (sh *Share) place(items []*item) []mrpack.File {
	slices.SortFunc(items, func(a, b *item) int {
		return cmp.Or(cmp.Compare(last(a.From == FromPack), last(b.From == FromPack)), cmp.Compare(a.Path, b.Path))
	})
	taken := map[string]bool{}
	files := []mrpack.File{}
	for _, it := range items {
		if it.Need == ServerOnly || !modpacks.PlayerContent(it.Path) {
			continue
		}
		keys := []string{"path:" + strings.ToLower(it.Path)}
		if p := it.modrinthProject(); p != "" {
			keys = append(keys, "modrinth:"+p)
		}
		if slices.ContainsFunc(keys, func(k string) bool { return taken[k] }) {
			continue
		}
		for _, k := range keys {
			taken[k] = true
		}
		if it.url != "" {
			it.InFile = true
			files = append(files, it.indexFile())
			continue
		}
		it.ByHand = true
		sh.Yourself = append(sh.Yourself, it.yourself(sh.Pack))
	}
	sh.Mods = make([]Mod, 0, len(items))
	for _, it := range items {
		it.Label = it.Need.Label()
		sh.Mods = append(sh.Mods, it.Mod)
	}
	slices.SortFunc(sh.Mods, func(a, b Mod) int {
		return cmp.Or(cmp.Compare(last(a.From == FromPack), last(b.From == FromPack)), compareNames(a.Name, b.Name), cmp.Compare(a.Path, b.Path))
	})
	slices.SortFunc(sh.Yourself, func(a, b Yourself) int {
		return cmp.Or(compareNames(a.Name, b.Name), cmp.Compare(a.Path, b.Path))
	})
	return files
}

func compareNames(a, b string) int {
	return cmp.Or(cmp.Compare(strings.ToLower(a), strings.ToLower(b)), cmp.Compare(a, b))
}

// last sorts what is true after what is false.
func last(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (it *item) indexFile() mrpack.File {
	env := &mrpack.Env{Client: mrpack.Optional, Server: mrpack.Unsupported}
	if it.Need == Required {
		env.Client = mrpack.Required
	}
	if it.OnServer {
		env.Server = mrpack.Required
	}
	return mrpack.File{Path: it.Path, Hashes: mrpack.Hashes{SHA1: it.sha1, SHA512: it.sha512}, Env: env,
		Downloads: []string{it.url}, FileSize: it.size}
}

func (it *item) yourself(pk *Pack) Yourself {
	y := Yourself{Name: it.Name, Path: it.Path, Need: it.Need}
	folder := folderOf(it.Path)
	switch other := otherHost(it.downloads); {
	case it.curseForge:
		y.Page = it.Page
		y.Reason = text("share.yourself.curseforge", "{name} is only on CurseForge. Download it there and put it in the game's {folder} folder.",
			"name", it.Name, "folder", folder)
	case it.origin == modpacks.Override && pk != nil:
		y.Page = pk.Page
		y.Reason = text("share.yourself.inside_pack", "{name} only comes inside {pack}'s own download. Get it from the pack's page and put it in the game's {folder} folder.",
			"name", it.Name, "pack", pk.Name, "folder", folder)
	case other != "":
		y.Page = other
		y.Reason = text("share.yourself.other_host", "{name} isn't on Modrinth. Download it from its link and put it in the game's {folder} folder.",
			"name", it.Name, "folder", folder)
	default:
		y.Page = it.Page
		y.Reason = text("share.yourself.not_found", "Modrinth doesn't have this version of {name}. Download it from its page and put it in the game's {folder} folder.",
			"name", it.Name, "folder", folder)
	}
	return y
}

// otherHost is the first of a pack's addresses for a file on the hosts
// Modrinth allows in packs.
func otherHost(downloads []string) string {
	for _, d := range downloads {
		if u, err := url.Parse(d); err == nil && fetch.Hosts(mrpack.Hosts()).Allows(u) && u.Port() == "" {
			return d
		}
	}
	return ""
}

func packInfo(r *modpacks.Record, items []*item) *Pack {
	p := &Pack{Name: cmp.Or(printable(r.Pack.Name), "The modpack"), Version: printable(r.Pack.VersionNumber),
		Source: r.Pack.Source, Page: packPage(r.Pack), Need: ServerOnly}
	order := []Need{ServerOnly, Optional, Unknown, Required}
	for _, it := range items {
		if it.From == FromPack && slices.Index(order, it.Need) > slices.Index(order, p.Need) {
			p.Need = it.Need
		}
	}
	p.Label = p.Need.Label()
	return p
}

// notice is the line at the top of the Mods tab. It names the mods the user
// added that friends need, leaving out dependencies of mods it names.
func notice(pk *Pack, items []*item) Text {
	needed := map[string]bool{}
	optional := false
	for _, it := range items {
		if it.From == FromUser && it.Need == Required {
			needed[it.Project] = true
		}
		optional = optional || it.Need == Optional || it.Need == Unknown
	}
	var mods []*item
	for _, it := range items {
		if it.From == FromUser && it.Need == Required && !(it.DependencyOf != "" && needed[it.DependencyOf]) {
			mods = append(mods, it)
		}
	}
	slices.SortFunc(mods, func(a, b *item) int {
		return cmp.Or(cmp.Compare(last(a.DependencyOf != ""), last(b.DependencyOf != "")), compareNames(a.Name, b.Name), cmp.Compare(a.Path, b.Path))
	})
	var mod, other, count string
	if len(mods) > 0 {
		mod, count = mods[0].Name, strconv.Itoa(len(mods)-1)
	}
	if len(mods) > 1 {
		other = mods[1].Name
	}
	if pk != nil && pk.Need == Required {
		switch len(mods) {
		case 0:
			return text("share.notice.pack", "Friends need the pack", "pack", pk.Name)
		case 1:
			return text("share.notice.pack_one", "Friends need the pack plus {mod}", "pack", pk.Name, "mod", mod)
		case 2:
			return text("share.notice.pack_two", "Friends need the pack plus {mod} and {other}", "pack", pk.Name, "mod", mod, "other", other)
		}
		return text("share.notice.pack_more", "Friends need the pack plus {mod} and {count} other mods", "pack", pk.Name, "mod", mod, "count", count)
	}
	switch {
	case len(mods) == 1:
		return text("share.notice.one", "Friends need {mod}", "mod", mod)
	case len(mods) == 2:
		return text("share.notice.two", "Friends need {mod} and {other}", "mod", mod, "other", other)
	case len(mods) > 2:
		return text("share.notice.more", "Friends need {mod} and {count} other mods", "mod", mod, "count", count)
	case optional:
		return text("share.notice.optional", "Friends can join without mods, and some are optional")
	}
	return text("share.notice.none", "Friends can join without mods")
}

// index is the file's modrinth.index.json, with the files by path. Its
// versionId comes from its content, so the same setup gives the same file.
func index(sh *Share, files []mrpack.File) mrpack.Index {
	slices.SortFunc(files, func(a, b mrpack.File) int { return cmp.Compare(a.Path, b.Path) })
	ix := mrpack.Index{
		FormatVersion: mrpack.FormatVersion, Game: "minecraft", Name: sh.Server,
		Summary: "The mods for playing on " + sh.Server + ".", Files: files,
		Dependencies: map[string]string{mrpack.Minecraft: sh.MinecraftVersion, loaderID(sh.Type): sh.LoaderVersion},
	}
	b, _ := json.Marshal(ix)
	sum := sha256.Sum256(b)
	ix.VersionID = sh.MinecraftVersion + "-" + hex.EncodeToString(sum[:4])
	return ix
}

func loaderID(serverType string) string {
	switch serverType {
	case "quilt":
		return mrpack.QuiltLoader
	case "neoforge":
		return mrpack.NeoForge
	}
	return mrpack.FabricLoader
}

// cdn accepts a download address on Modrinth's CDN.
func cdn(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == modrinth.CDNHost && u.User == nil && u.RawQuery == "" && u.Fragment == "" &&
		strings.HasPrefix(u.Path, "/data/") && !strings.ContainsAny(raw, " \t\r\n")
}

func modrinthPage(projectType, slug, id string) string {
	ref := slug
	if !validID(ref) {
		ref = id
	}
	if !validID(ref) {
		return ""
	}
	switch projectType {
	case "mod", "resourcepack", "shader", "datapack", "plugin", "modpack":
	default:
		projectType = "project"
	}
	return "https://modrinth.com/" + projectType + "/" + ref
}

// curseForgeLink accepts a page on CurseForge's website.
func curseForgeLink(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" ||
		!(u.Host == "curseforge.com" || strings.HasSuffix(u.Host, ".curseforge.com")) {
		return ""
	}
	return raw
}

func packPage(in addons.Installed) string {
	switch in.Source {
	case addons.Modrinth:
		page := modrinthPage("modpack", in.Slug, in.ProjectID)
		if page != "" && validID(in.VersionID) {
			page += "/version/" + in.VersionID
		}
		return page
	case modpacks.CurseForge:
		if validID(in.Slug) && digits(in.VersionID) {
			return "https://www.curseforge.com/minecraft/modpacks/" + in.Slug + "/files/" + in.VersionID
		}
		if digits(in.ProjectID) {
			return "https://www.curseforge.com/projects/" + in.ProjectID
		}
	}
	return ""
}

func folderOf(p string) string {
	dir, _, _ := strings.Cut(p, "/")
	return dir
}

func serverName(name string) string {
	return cmp.Or(printable(strings.TrimSpace(name)), "Minecraft server")
}

// printable makes text from a source or a pack safe to show: no control or
// format characters, no line breaks, at most 80 characters.
func printable(s string) string {
	s = strings.ToValidUTF8(s, "?")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return '?'
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80]) + "…"
	}
	return s
}

// validID accepts a project or version id or slug to put in an address.
func validID(s string) bool {
	if s == "" || len(s) > 64 || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func digits(s string) bool {
	return s != "" && len(s) <= 20 && strings.Trim(s, "0123456789") == ""
}

func isHex(s string, n int) bool {
	return len(s) == n && strings.Trim(s, "0123456789abcdef") == ""
}

func kv(pairs ...string) map[string]string {
	m := make(map[string]string, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return m
}

func fail(k addons.Kind, params map[string]string, msg, hint string) *addons.Error {
	return &addons.Error{Notice: addons.Notice{Kind: k, Params: params, Msg: msg, Hint: hint}}
}
