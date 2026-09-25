package modpacks

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/modpacks/curseforge"
	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
)

// Skipped is a file of the pack that does not go on the server.
type Skipped struct {
	Path string `json:"path"`
	// Reason is client_only, client_content, protected_path, world_files,
	// optional_off or user_removed.
	Reason addons.Kind `json:"reason"`
}

// pack is one version of a pack, downloaded, read and checked, with every
// file it would put on the server.
type pack struct {
	info       addons.Installed
	reqs       Requirements
	files      map[string]*packFile
	skipped    []Skipped
	manual     []addons.ManualStep
	blockers   []addons.Notice
	warnings   []addons.Notice
	properties map[string]string
	unknownEnv []string
	arch       *archive
	dir        string
}

// packFile is one file the pack would put on the server.
type packFile struct {
	path    string
	origin  Origin
	project string
	name    string
	// optional files are on by default when on is set.
	optional, on bool
	world        bool
	size         int64
	sums         map[string]string // by algorithm, lower-case hex
	urls         []string          // downloads, in the order to try
	hosts        fetch.Hosts       // where downloads and their redirects may go
	entry        *zip.File         // overrides
}

// algo is the strongest hash the pack lists for the file.
func (f *packFile) algo() string {
	if f.sums["sha512"] != "" {
		return "sha512"
	}
	return "sha1"
}

func (p *pack) close() {
	if p.arch != nil {
		p.arch.Close()
	}
	if p.dir != "" {
		os.RemoveAll(p.dir)
	}
}

func (p *pack) skip(path string, why addons.Kind) {
	p.skipped = append(p.skipped, Skipped{Path: path, Reason: why})
}

func (p *pack) block(n addons.Notice) {
	if !slices.ContainsFunc(p.blockers, func(b addons.Notice) bool { return b.Msg == n.Msg }) {
		p.blockers = append(p.blockers, n)
	}
}

func (p *pack) warn(n addons.Notice) {
	if !slices.ContainsFunc(p.warnings, func(w addons.Notice) bool { return w.Msg == n.Msg }) {
		p.warnings = append(p.warnings, n)
	}
}

// resolve downloads and reads one version of a pack.
func (l *Library) resolve(ctx context.Context, ref Ref, allowPre bool, world string) (*pack, error) {
	switch ref.Source {
	case addons.Modrinth:
		return l.resolveModrinth(ctx, ref, allowPre, world)
	case CurseForge:
		return l.resolveCurseForge(ctx, ref, allowPre, world)
	}
	return nil, fail(addons.KindInvalid, kv("field", "source"),
		"Packs come from Modrinth or CurseForge, not \""+printable(string(ref.Source))+"\".", "")
}

func (l *Library) resolveModrinth(ctx context.Context, ref Ref, allowPre bool, world string) (*pack, error) {
	proj, err := l.modrinthPack(ctx, ref.Project)
	if err != nil {
		return nil, err
	}
	if !proj.RunsOnServer() {
		name := printable(proj.Title)
		return nil, fail(addons.KindClientOnly, kv("pack", name), fmt.Sprintf("%s is for the game client only, not for servers.", name), "Choose another pack.")
	}
	var v *modrinth.Version
	if ref.Version != "" {
		if !validID(ref.Version) {
			return nil, invalid("version")
		}
		v, err = l.Modrinth.Version(ctx, ref.Version)
		if errors.Is(err, fetch.ErrNotFound) || err == nil && v.ProjectID != proj.ID {
			return nil, versionNotFound(printable(proj.Title), ref.Version)
		}
		if err != nil {
			return nil, upstream(addons.Modrinth, err)
		}
	} else {
		vs, err := l.Modrinth.ProjectVersions(ctx, proj.ID, modrinth.VersionFilter{})
		if err != nil {
			return nil, upstream(addons.Modrinth, err)
		}
		if v, err = l.pickModrinth(printable(proj.Title), vs, allowPre, "", ""); err != nil {
			return nil, err
		}
	}
	return l.readModrinth(ctx, proj, v, world)
}

// modrinthPack looks up a Modrinth project that must be a modpack.
func (l *Library) modrinthPack(ctx context.Context, idOrSlug string) (*modrinth.Project, error) {
	if !validID(idOrSlug) {
		return nil, invalid("project")
	}
	proj, err := l.Modrinth.Project(ctx, idOrSlug)
	if errors.Is(err, fetch.ErrNotFound) {
		return nil, notFound(addons.Modrinth, idOrSlug)
	}
	if err != nil {
		return nil, upstream(addons.Modrinth, err)
	}
	if proj.ProjectType != "modpack" {
		return nil, notModpack(printable(proj.Title), addons.Modrinth)
	}
	return proj, nil
}

// modrinthTarget is the server type and Minecraft version a Modrinth pack
// version says it is for, with why Playkeeper cannot run it, if it cannot.
// The pack's index has the final say.
func (l *Library) modrinthTarget(name string, v *modrinth.Version) (string, string, *addons.Notice) {
	typ := ""
	for _, t := range []string{"fabric", "quilt", "neoforge"} {
		if slices.Contains(v.Loaders, t) && slices.Contains(l.types(), t) {
			typ = t
			break
		}
	}
	switch {
	case typ != "":
	case slices.Contains(v.Loaders, "forge"):
		return "forge", "", &forge(name).Notice
	case len(v.Loaders) == 0 || slices.Contains(v.Loaders, "minecraft") || slices.Contains(v.Loaders, "vanilla"):
		typ = "vanilla"
	default:
		n := notice(KindTypeUnavailable, kv("pack", name, "type", printable(strings.Join(v.Loaders, ", "))),
			fmt.Sprintf("This version of %s needs a server type Playkeeper cannot run (%s).", name, printable(strings.Join(v.Loaders, ", "))),
			"Choose a version for Fabric, Quilt or NeoForge.")
		return "", "", &n
	}
	if !slices.Contains(l.types(), typ) {
		n := notice(KindTypeUnavailable, kv("pack", name, "type", typ), fmt.Sprintf("%s needs a %s server, and Playkeeper cannot run %s servers yet.", name, typeName(typ), typeName(typ)), "")
		return typ, "", &n
	}
	var why *addons.Notice
	for _, mc := range v.GameVersions {
		if why = l.minecraftUnsupported(name, mc); why == nil {
			return typ, mc, nil
		}
	}
	if why == nil {
		n := notice(KindMinecraft, kv("pack", name, "minecraft", ""), fmt.Sprintf("This version of %s does not say which Minecraft version it is for.", name), "Choose another version.")
		why = &n
	}
	return typ, "", why
}

// pickModrinth chooses the newest version Playkeeper can run, a release
// unless allowPre, optionally for one Minecraft version and server type.
func (l *Library) pickModrinth(name string, vs []modrinth.Version, allowPre bool, mc, typ string) (*modrinth.Version, error) {
	var pre, forgeOnly *modrinth.Version
	var firstWhy *addons.Notice
	for i := range vs {
		v := &vs[i]
		t, m, why := l.modrinthTarget(name, v)
		if why != nil {
			if why.Kind == KindForge && forgeOnly == nil {
				forgeOnly = v
			}
			if firstWhy == nil {
				firstWhy = why
			}
			continue
		}
		if _, ok := mrpackFile(v); !ok || mc != "" && m != mc || typ != "" && t != typ {
			continue
		}
		if channel(v.VersionType) == "release" {
			return v, nil
		}
		if pre == nil {
			pre = v
		}
	}
	return pickResult(name, pre, allowPre, firstWhy, mc, typ)
}

func pickResult[V any](name string, pre *V, allowPre bool, firstWhy *addons.Notice, mc, typ string) (*V, error) {
	switch {
	case pre != nil && allowPre:
		return pre, nil
	case pre != nil:
		return nil, fail(addons.KindOnlyPrerelease, kv("pack", name),
			fmt.Sprintf("%s has only beta or alpha versions Playkeeper can run.", name),
			"Pre-releases can be unstable. Allow pre-releases, or choose a version yourself.")
	case mc != "":
		return nil, fail(addons.KindNoVersion, kv("pack", name, "minecraft", mc, "type", typ),
			fmt.Sprintf("%s has no version for %s on Minecraft %s.", name, typeName(typ), mc), "")
	case firstWhy != nil:
		return nil, &addons.Error{Notice: *firstWhy}
	}
	return nil, fail(addons.KindNoVersion, kv("pack", name), fmt.Sprintf("%s has no version Playkeeper can install.", name), "Choose another pack.")
}

// mrpackFile is a Modrinth pack version's .mrpack: the primary one, else the
// first.
func mrpackFile(v *modrinth.Version) (modrinth.File, bool) {
	var first *modrinth.File
	for i := range v.Files {
		f := &v.Files[i]
		if f.FileType != "" || !strings.HasSuffix(strings.ToLower(f.Filename), ".mrpack") {
			continue
		}
		if f.Primary {
			return *f, true
		}
		if first == nil {
			first = f
		}
	}
	if first == nil {
		return modrinth.File{}, false
	}
	return *first, true
}

func channel(versionType string) string {
	switch versionType {
	case "beta", "alpha":
		return versionType
	}
	return "release"
}

func (l *Library) readModrinth(ctx context.Context, proj *modrinth.Project, v *modrinth.Version, world string) (*pack, error) {
	name := printable(proj.Title)
	if _, _, why := l.modrinthTarget(name, v); why != nil && why.Kind == KindForge {
		return nil, &addons.Error{Notice: *why}
	}
	file, ok := mrpackFile(v)
	if !ok {
		return nil, fail(addons.KindNoVersion, kv("pack", name, "version", printable(v.VersionNumber)),
			fmt.Sprintf("Version %s of %s has no .mrpack file.", printable(v.VersionNumber), name), "Choose another version.")
	}
	lim := l.limits()
	p := &pack{
		info: addons.Installed{
			Source: addons.Modrinth, ProjectID: proj.ID, Slug: proj.Slug, Name: name, IconURL: proj.IconURL,
			VersionID: v.ID, VersionNumber: printable(v.VersionNumber), Channel: channel(v.VersionType), Published: v.DatePublished,
			FileName: printable(file.Filename), HashAlgo: "sha512", Hash: strings.ToLower(file.Hashes.SHA512), Size: file.Size,
		},
		files: map[string]*packFile{}, properties: map[string]string{},
	}
	want := fetch.Want{Algo: "sha512", Hash: file.Hashes.SHA512, Size: file.Size, Max: lim.Pack}
	if len(file.Hashes.SHA1) == 40 {
		want.Also = []fetch.Sum{{Algo: "sha1", Hash: file.Hashes.SHA1}}
	}
	if err := l.fetchArchive(ctx, p, l.modrinthFiles(), file.URL, want, lim); err != nil {
		p.close()
		return nil, err
	}
	if err := l.readIndex(p, world, lim); err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

// fetchArchive downloads the pack's archive into a new temporary folder and
// opens it.
func (l *Library) fetchArchive(ctx context.Context, p *pack, hosts fetch.Hosts, rawURL string, want fetch.Want, lim Limits) error {
	if len(want.Hash) == 0 {
		return fail(addons.KindNoHash, kv("pack", p.info.Name, "source", sourceName(p.info.Source)),
			fmt.Sprintf("%s lists no usable hash for %s, so Playkeeper cannot check the download.", sourceName(p.info.Source), p.info.Name),
			"Choose another version.")
	}
	dir, err := os.MkdirTemp(l.TempDir, "playkeeper-modpack-")
	if err != nil {
		return tempError(err)
	}
	p.dir = dir
	file, err := fetch.Download(ctx, l.HTTP, hosts, l.userAgent(), rawURL, dir, want)
	if err != nil {
		return downloadError(p.info.Name, p.info.FileName, sourceName(p.info.Source), err, lim.Pack)
	}
	p.arch, err = openArchive(file, p.info.Name, lim)
	return err
}

func (l *Library) readIndex(p *pack, world string, lim Limits) error {
	b, err := p.arch.read(mrpack.IndexName, lim.Index)
	if err != nil {
		return indexError(p.info.Name, mrpack.IndexName, err)
	}
	ix, err := mrpack.Parse(b)
	var pe *mrpack.PathError
	switch {
	case errors.As(err, &pe):
		return unsafePath(p.info.Name, err)
	case err != nil:
		return &addons.Error{Notice: notice(KindBadPack, kv("pack", p.info.Name, "reason", err.Error()),
			fmt.Sprintf("%s cannot be installed: %s.", p.info.Name, err.Error()), "Choose another version of the pack. Nothing was installed."), Err: err}
	}
	loader, loaderVersion, err := ix.Loader()
	if err != nil {
		return badPack(p.info.Name, strings.TrimPrefix(err.Error(), "the pack's index "))
	}
	if p.reqs, err = l.requirements(p.info.Name, loader, loaderVersion, ix.Dependencies[mrpack.Minecraft]); err != nil {
		return err
	}
	hosts := slices.Concat(l.packHosts(), l.packRedirects())
	for i := range ix.Files {
		l.addIndexFile(p, &ix.Files[i], world, hosts)
	}
	if err := l.addOverrides(p, p.arch.layer(mrpack.Overrides, mrpack.ServerOverrides), world, lim); err != nil {
		return err
	}
	return l.finishPack(p, lim)
}

func (l *Library) addIndexFile(p *pack, f *mrpack.File, world string, hosts fetch.Hosts) {
	env := f.Server()
	switch env {
	case mrpack.Unsupported:
		p.skip(f.Path, addons.KindClientOnly)
		return
	case mrpack.Required, mrpack.Optional:
	default:
		p.unknownEnv = append(p.unknownEnv, f.Path)
	}
	c := classify(f.Path, world)
	switch c {
	case classClient:
		p.skip(f.Path, KindClientContent)
		return
	case classProtected:
		p.skip(f.Path, KindProtected)
		return
	}
	var urls []string
	for _, raw := range f.Downloads {
		if u, err := url.Parse(raw); err == nil && l.packHosts().Allows(u) {
			urls = append(urls, raw)
		}
	}
	if len(urls) == 0 {
		host := "an address Playkeeper cannot check"
		if u, err := url.Parse(f.Downloads[0]); err == nil && u.Hostname() != "" {
			host = printable(u.Hostname())
		}
		p.block(notice(addons.KindHostNotAllowed, kv("pack", p.info.Name, "file", printable(f.Path), "host", host),
			fmt.Sprintf("%s would download %s from %s, which is not a host modpacks may use.", p.info.Name, printable(f.Path), host),
			"Playkeeper only downloads pack files from the hosts Modrinth's pack format allows. Nothing was installed."))
		return
	}
	p.files[f.Path] = &packFile{
		path: f.Path, origin: Download, project: modrinthProject(urls), name: path.Base(f.Path),
		optional: env == mrpack.Optional, on: true, world: c == classWorld, size: f.FileSize,
		sums: map[string]string{"sha512": f.Hashes.SHA512, "sha1": f.Hashes.SHA1}, urls: urls, hosts: hosts,
	}
}

// modrinthProject reads the project id out of a Modrinth CDN address,
// cdn.modrinth.com/data/<project>/versions/<version>/<file>.
func modrinthProject(urls []string) string {
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() != modrinth.CDNHost {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
		if len(parts) >= 4 && parts[0] == "data" && parts[2] == "versions" && validID(parts[1]) {
			return parts[1]
		}
	}
	return ""
}

// addOverrides adds the files copied out of the archive; they replace
// downloads at the same path.
func (l *Library) addOverrides(p *pack, entries map[string]*zip.File, world string, lim Limits) error {
	for _, rel := range slices.Sorted(maps.Keys(entries)) {
		e := entries[rel]
		c := classify(rel, world)
		switch c {
		case classClient:
			p.skip(rel, KindClientContent)
			continue
		case classProtected:
			if rel == "server.properties" {
				l.readProperties(p, e)
			}
			p.skip(rel, KindProtected)
			continue
		}
		if e.UncompressedSize64 > uint64(lim.File) {
			p.block(fileTooLarge(p.info.Name, rel, lim.File))
			continue
		}
		sums, n, err := copyEntry(e, io.Discard, lim.File)
		if err != nil {
			return badPack(p.info.Name, fmt.Sprintf("is damaged at %q (%v)", printable(rel), err))
		}
		p.files[rel] = &packFile{path: rel, origin: Override, name: path.Base(rel), on: true, world: c == classWorld, size: n, sums: sums, entry: e}
	}
	return nil
}

func (l *Library) readProperties(p *pack, e *zip.File) {
	r, err := e.Open()
	if err != nil {
		return
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, 64<<10))
	if err != nil {
		return
	}
	props, dropped := suggestions(b)
	maps.Copy(p.properties, props)
	if len(dropped) > 0 {
		p.warn(notice(KindProperties, kv("pack", p.info.Name, "settings", strings.Join(dropped, ", ")),
			fmt.Sprintf("%s ships server settings that Playkeeper does not take from packs: %s.", p.info.Name, strings.Join(dropped, ", ")),
			"Set them in the server's settings if you want them."))
	}
}

// finishPack checks the pack's files against the limits.
func (l *Library) finishPack(p *pack, lim Limits) error {
	if len(p.files) > lim.Files {
		return fail(KindTooManyFiles, kv("pack", p.info.Name, "limit", strconv.Itoa(lim.Files)),
			fmt.Sprintf("%s would put more than %d files on the server, more than Playkeeper accepts.", p.info.Name, lim.Files),
			"Choose another pack. Nothing was installed.")
	}
	if clash := treeClash(slices.Collect(maps.Keys(p.files))); clash != "" {
		return badPack(p.info.Name, fmt.Sprintf("puts both a file and a folder at %q", printable(clash)))
	}
	var downloads, unpacked int64
	for _, f := range slices.Sorted(maps.Keys(p.files)) {
		pf := p.files[f]
		if pf.origin == Override {
			unpacked += pf.size
			continue
		}
		downloads += pf.size
		if pf.size > lim.File {
			p.block(fileTooLarge(p.info.Name, pf.path, lim.File))
		}
	}
	if unpacked > lim.Unpacked {
		return fail(addons.KindTooLarge, kv("pack", p.info.Name, "limit", fetch.Size(lim.Unpacked)),
			fmt.Sprintf("%s's archive unpacks to more than the %s Playkeeper accepts.", p.info.Name, fetch.Size(lim.Unpacked)),
			"Choose another pack. Nothing was installed.")
	}
	if downloads > lim.Downloads {
		p.block(notice(addons.KindTooLarge, kv("pack", p.info.Name, "limit", fetch.Size(lim.Downloads)),
			fmt.Sprintf("%s would download more than the %s Playkeeper accepts for one pack.", p.info.Name, fetch.Size(lim.Downloads)),
			"Choose a smaller pack."))
	}
	if n := len(p.unknownEnv); n > 0 {
		slices.Sort(p.unknownEnv)
		p.warn(notice(KindUnverifiedEnv, kv("pack", p.info.Name, "count", strconv.Itoa(n), "first", printable(p.unknownEnv[0])),
			fmt.Sprintf("%s does not say whether %d of its files (such as %s) belong on a server, so Playkeeper includes them.", p.info.Name, n, printable(p.unknownEnv[0])),
			"If the server fails to start, one of them may be for the game client only."))
	}
	return nil
}

func (l *Library) resolveCurseForge(ctx context.Context, ref Ref, allowPre bool, world string) (*pack, error) {
	mod, err := l.curseForgePack(ctx, ref.Project)
	if err != nil {
		return nil, err
	}
	name := printable(mod.Name)
	var f *curseforge.File
	if ref.Version != "" {
		fileID, err := strconv.ParseInt(ref.Version, 10, 64)
		if err != nil || fileID <= 0 {
			return nil, invalid("version")
		}
		f, err = l.CurseForge.File(ctx, mod.ID, fileID)
		if errors.Is(err, fetch.ErrNotFound) || err == nil && f.ModID != mod.ID {
			return nil, versionNotFound(name, ref.Version)
		}
		if err != nil {
			return nil, upstream(CurseForge, err)
		}
	} else {
		files, _, err := l.CurseForge.ModFiles(ctx, mod.ID, curseforge.FilesQuery{PageSize: curseforge.MaxPageSize})
		if err != nil {
			return nil, upstream(CurseForge, err)
		}
		if f, err = l.pickCurseForge(name, files, allowPre, "", ""); err != nil {
			return nil, err
		}
	}
	return l.readCurseForge(ctx, mod, f, world)
}

// curseForgePack looks up a CurseForge project that must be a Minecraft
// modpack.
func (l *Library) curseForgePack(ctx context.Context, project string) (*curseforge.Mod, error) {
	if l.CurseForge == nil {
		return nil, noCurseForge()
	}
	id, err := strconv.ParseInt(project, 10, 64)
	if err != nil || id <= 0 {
		return nil, invalid("project")
	}
	mod, err := l.CurseForge.Mod(ctx, id)
	if errors.Is(err, fetch.ErrNotFound) {
		return nil, notFound(CurseForge, project)
	}
	if err != nil {
		return nil, upstream(CurseForge, err)
	}
	if mod.ClassID != curseforge.ClassModpacks || mod.GameID != curseforge.GameMinecraft {
		return nil, notModpack(printable(mod.Name), CurseForge)
	}
	return mod, nil
}

// curseForgeTarget is what a CurseForge pack file says it needs. Either may
// be empty when the file does not say; the manifest has the final say.
func (l *Library) curseForgeTarget(name string, f *curseforge.File) (string, string, *addons.Notice) {
	typ, mc := "", ""
	for _, g := range f.GameVersions {
		switch strings.ToLower(g) {
		case "fabric", "quilt", "neoforge":
			if typ == "" || typ == "forge" {
				typ = strings.ToLower(g)
			}
		case "forge":
			if typ == "" {
				typ = "forge"
			}
		default:
			if releaseVersion.MatchString(g) && (mc == "" || minecraft.CompareMinecraft(g, mc) > 0) {
				mc = g
			}
		}
	}
	if typ == "forge" {
		return typ, mc, &forge(name).Notice
	}
	if typ != "" && !slices.Contains(l.types(), typ) {
		n := notice(KindTypeUnavailable, kv("pack", name, "type", typ), fmt.Sprintf("%s needs a %s server, and Playkeeper cannot run %s servers yet.", name, typeName(typ), typeName(typ)), "")
		return typ, mc, &n
	}
	if mc != "" {
		if why := l.minecraftUnsupported(name, mc); why != nil {
			return typ, mc, why
		}
	}
	return typ, mc, nil
}

// usableFile reports whether a CurseForge pack file can be picked at all.
func usableFile(f *curseforge.File) bool {
	switch f.FileStatus {
	case curseforge.StatusRejected, curseforge.StatusMalwareDetected, curseforge.StatusDeleted:
		return false
	}
	return f.IsAvailable && !f.IsServerPack
}

func (l *Library) pickCurseForge(name string, files []curseforge.File, allowPre bool, mc, typ string) (*curseforge.File, error) {
	files = slices.Clone(files)
	slices.SortStableFunc(files, func(a, b curseforge.File) int { return b.FileDate.Compare(a.FileDate) })
	var pre *curseforge.File
	var firstWhy *addons.Notice
	for i := range files {
		f := &files[i]
		if !usableFile(f) {
			continue
		}
		t, m, why := l.curseForgeTarget(name, f)
		if why != nil {
			if firstWhy == nil {
				firstWhy = why
			}
			continue
		}
		if mc != "" && m != mc || typ != "" && t != typ {
			continue
		}
		if f.ReleaseType == curseforge.Release {
			return f, nil
		}
		if pre == nil {
			pre = f
		}
	}
	return pickResult(name, pre, allowPre, firstWhy, mc, typ)
}

func cfChannel(releaseType int) string {
	switch releaseType {
	case curseforge.Beta:
		return "beta"
	case curseforge.Alpha:
		return "alpha"
	}
	return "release"
}

func (l *Library) readCurseForge(ctx context.Context, mod *curseforge.Mod, f *curseforge.File, world string) (*pack, error) {
	name := printable(mod.Name)
	if _, _, why := l.curseForgeTarget(name, f); why != nil && why.Kind == KindForge {
		return nil, &addons.Error{Notice: *why}
	}
	switch {
	case f.IsServerPack:
		return nil, fail(addons.KindInvalid, kv("pack", name),
			fmt.Sprintf("That file is %s's server pack; Playkeeper installs the pack itself and leaves out what servers do not need.", name),
			"Choose one of the pack's other files.")
	case f.FileStatus == curseforge.StatusMalwareDetected:
		return nil, fail(KindMalware, kv("pack", name), fmt.Sprintf("CurseForge flagged this version of %s as malware.", name), "Do not install it.")
	case !usableFile(f):
		return nil, fail(KindUnavailable, kv("pack", name), fmt.Sprintf("This version of %s is no longer available on CurseForge.", name), "Choose another version.")
	case f.DownloadURL == "":
		return nil, &addons.Error{Notice: notice(addons.KindExternal, kv("pack", name, "host", "CurseForge"),
			fmt.Sprintf("%s's author only allows downloading it through CurseForge's app, so Playkeeper cannot install it.", name),
			"Choose another pack.")}
	}
	var icon string
	if mod.Logo != nil {
		icon = mod.Logo.ThumbnailURL
	}
	lim := l.limits()
	p := &pack{
		info: addons.Installed{
			Source: CurseForge, ProjectID: strconv.FormatInt(mod.ID, 10), Slug: mod.Slug, Name: name, IconURL: icon,
			VersionID: strconv.FormatInt(f.ID, 10), VersionNumber: printable(f.DisplayName), Channel: cfChannel(f.ReleaseType),
			Published: f.FileDate, FileName: printable(f.FileName), HashAlgo: "sha1", Hash: f.SHA1(), Size: f.FileLength,
		},
		files: map[string]*packFile{}, properties: map[string]string{},
	}
	want := fetch.Want{Algo: "sha1", Hash: f.SHA1(), Size: f.FileLength, Max: lim.Pack}
	if err := l.fetchArchive(ctx, p, l.curseForgeFiles(), f.DownloadURL, want, lim); err != nil {
		p.close()
		return nil, err
	}
	if err := l.readManifest(ctx, p, world, lim); err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

func (l *Library) readManifest(ctx context.Context, p *pack, world string, lim Limits) error {
	b, err := p.arch.read(curseforge.ManifestName, lim.Index)
	if err != nil {
		return indexError(p.info.Name, curseforge.ManifestName, err)
	}
	m, err := curseforge.ParseManifest(b)
	if err != nil {
		return &addons.Error{Notice: notice(KindBadPack, kv("pack", p.info.Name, "reason", err.Error()),
			fmt.Sprintf("%s cannot be installed: %s.", p.info.Name, err.Error()), "Choose another version of the pack. Nothing was installed."), Err: err}
	}
	loader, loaderVersion, err := m.Loader()
	if err != nil {
		return badPack(p.info.Name, strings.TrimPrefix(err.Error(), "the pack's manifest.json "))
	}
	if p.reqs, err = l.requirements(p.info.Name, loader, loaderVersion, m.Minecraft.Version); err != nil {
		return err
	}
	if len(m.Files) > lim.Files {
		return fail(KindTooManyFiles, kv("pack", p.info.Name, "limit", strconv.Itoa(lim.Files)),
			fmt.Sprintf("%s would put more than %d files on the server, more than Playkeeper accepts.", p.info.Name, lim.Files),
			"Choose another pack. Nothing was installed.")
	}
	var fileIDs, projectIDs []int64
	for _, mf := range m.Files {
		fileIDs, projectIDs = append(fileIDs, mf.FileID), append(projectIDs, mf.ProjectID)
	}
	files, err := l.CurseForge.Files(ctx, fileIDs)
	if err != nil {
		return upstream(CurseForge, err)
	}
	mods, err := l.CurseForge.Mods(ctx, projectIDs)
	if err != nil {
		return upstream(CurseForge, err)
	}
	fileByID := map[int64]*curseforge.File{}
	for i := range files {
		fileByID[files[i].ID] = &files[i]
	}
	modByID := map[int64]*curseforge.Mod{}
	for i := range mods {
		modByID[mods[i].ID] = &mods[i]
	}
	for _, mf := range m.Files {
		l.addCurseForgeFile(p, mf, fileByID[mf.FileID], modByID[mf.ProjectID])
	}
	if err := l.addOverrides(p, p.arch.layer(m.Overrides), world, lim); err != nil {
		return err
	}
	return l.finishPack(p, lim)
}

func (l *Library) addCurseForgeFile(p *pack, mf curseforge.ManifestFile, f *curseforge.File, m *curseforge.Mod) {
	id := strconv.FormatInt(mf.ProjectID, 10)
	display, page := "project "+id, "https://www.curseforge.com/projects/"+id
	if m != nil {
		display, page = printable(m.Name), m.ProjectPage()
	}
	gone := f == nil || f.ModID != mf.ProjectID || m == nil || !f.IsAvailable || f.FileStatus == curseforge.StatusDeleted || f.FileStatus == curseforge.StatusRejected
	switch {
	case f != nil && f.ModID == mf.ProjectID && f.FileStatus == curseforge.StatusMalwareDetected:
		p.block(notice(KindMalware, kv("pack", p.info.Name, "name", display, "file", printable(f.FileName)),
			fmt.Sprintf("CurseForge flagged %s (%s), which %s uses, as malware.", printable(f.FileName), display, p.info.Name),
			"Do not install this version of the pack."))
		return
	case gone && !mf.Required:
		return
	case gone:
		p.manual = append(p.manual, addons.ManualStep{Notice: notice(KindUnavailable, kv("pack", p.info.Name, "name", display),
			fmt.Sprintf("%s, which %s uses, is no longer available on CurseForge.", display, p.info.Name),
			"The pack may not work without it. Look on its page for a replacement."), URL: page})
		return
	}
	switch m.ClassID {
	case curseforge.ClassMods:
	case curseforge.ClassResourcePacks:
		p.skip("resourcepacks/"+printable(f.FileName), KindClientContent)
		return
	case curseforge.ClassShaders:
		p.skip("shaderpacks/"+printable(f.FileName), KindClientContent)
		return
	default:
		if mf.Required {
			p.manual = append(p.manual, addons.ManualStep{Notice: notice(KindOtherContent, kv("pack", p.info.Name, "name", display, "file", printable(f.FileName)),
				fmt.Sprintf("%s uses %s (%s), which is not a mod, so Playkeeper does not install it on servers.", p.info.Name, display, printable(f.FileName)),
				"If the server needs it, download it from its page and add it by hand."), URL: m.FilePage(f.ID)})
		}
		return
	}
	target := "mods/" + f.FileName
	switch {
	case f.ClientOnly():
		p.skip("mods/"+printable(f.FileName), addons.KindClientOnly)
		return
	case !plainJar(f.FileName):
		p.block(notice(addons.KindBadFileName, kv("pack", p.info.Name, "name", display, "file", printable(f.FileName)),
			fmt.Sprintf("%s offers a file named \"%s\", which Playkeeper will not write to the server.", display, printable(f.FileName)),
			"Mod files must have a plain .jar name without folders. Nothing was installed."))
		return
	case f.DownloadURL == "":
		if mf.Required {
			p.manual = append(p.manual, addons.ManualStep{Notice: notice(addons.KindExternal, kv("pack", p.info.Name, "name", display, "file", printable(f.FileName), "folder", "mods"),
				fmt.Sprintf("%s's author only allows downloads through CurseForge's app, so Playkeeper cannot download %s for you.", display, printable(f.FileName)),
				"Download it from that page and upload it to the mods folder."), URL: m.FilePage(f.ID)})
		}
		return
	case f.SHA1() == "":
		p.block(notice(addons.KindNoHash, kv("pack", p.info.Name, "file", printable(f.FileName), "source", "CurseForge"),
			fmt.Sprintf("CurseForge lists no usable hash for %s, so Playkeeper cannot check the download.", printable(f.FileName)),
			"Choose another version of the pack."))
		return
	case p.files[target] != nil:
		p.block(notice(addons.KindDuplicate, kv("pack", p.info.Name, "file", printable(f.FileName)),
			fmt.Sprintf("%s lists two mods with the file name %s.", p.info.Name, printable(f.FileName)), "Choose another version of the pack."))
		return
	}
	if _, err := l.curseForgeFiles().Check(f.DownloadURL); err != nil {
		host := "an address Playkeeper cannot check"
		if he := (*fetch.HostError)(nil); errors.As(err, &he) && he.Host != "" {
			host = printable(he.Host)
		}
		p.block(notice(addons.KindHostNotAllowed, kv("pack", p.info.Name, "file", printable(f.FileName), "host", host),
			fmt.Sprintf("CurseForge lists %s at %s, which is not one of CurseForge's file hosts.", printable(f.FileName), host),
			"Nothing was installed."))
		return
	}
	p.files[target] = &packFile{
		path: target, origin: Download, project: id, name: display, optional: !mf.Required, on: mf.Required,
		size: f.FileLength, sums: map[string]string{"sha1": f.SHA1()}, urls: []string{f.DownloadURL}, hosts: l.curseForgeFiles(),
	}
}

// plainJar accepts a mod file name from CurseForge: a .jar without folders,
// hidden names or odd characters.
func plainJar(name string) bool {
	if len(name) < len("x.jar") || len(name) > 128 || !strings.HasSuffix(name, ".jar") || name[0] == '.' || name[0] == '-' || name[0] == ' ' {
		return false
	}
	if strings.ContainsAny(name, `/\:*?"<>|`) {
		return false
	}
	return mrpack.CheckPath("mods/"+name) == nil
}

func indexError(name, file string, err error) error {
	var tl *fetch.TooLargeError
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return badPack(name, "has no "+file)
	case errors.As(err, &tl):
		return badPack(name, fmt.Sprintf("has a %s larger than the %s Playkeeper reads", file, fetch.Size(tl.Limit)))
	}
	return err
}

func fileTooLarge(pack, file string, max int64) addons.Notice {
	return notice(addons.KindTooLarge, kv("pack", pack, "file", printable(file), "limit", fetch.Size(max)),
		fmt.Sprintf("%s's file %s is larger than the %s Playkeeper accepts for one file.", pack, printable(file), fetch.Size(max)),
		"Choose another version of the pack.")
}

func invalid(field string) *addons.Error {
	return fail(addons.KindInvalid, kv("field", field), "The pack's "+field+" is not valid.", "")
}

func notFound(src addons.Source, project string) *addons.Error {
	return fail(addons.KindNotFound, kv("source", sourceName(src), "project", printable(project)),
		fmt.Sprintf("%s has no project \"%s\".", sourceName(src), printable(project)), "Search for the pack again.")
}

func versionNotFound(name, version string) *addons.Error {
	return fail(addons.KindNotFound, kv("pack", name, "version", printable(version)),
		fmt.Sprintf("%s has no version \"%s\".", name, printable(version)), "Choose a version from the list.")
}

func notModpack(name string, src addons.Source) *addons.Error {
	return fail(KindNotModpack, kv("name", name, "source", sourceName(src)),
		fmt.Sprintf("%s is not a modpack on %s.", name, sourceName(src)), "Install it from the add-ons page instead.")
}

func noCurseForge() *addons.Error {
	return fail(KindNoCurseForge, nil, "CurseForge is not available on this Playkeeper: it has no CurseForge API key.",
		"Choose a pack from Modrinth. The owner can add a CurseForge API key to offer CurseForge packs.")
}

func tempError(err error) *addons.Error {
	return &addons.Error{Notice: notice(addons.KindFolderUnusable, kv("folder", "temp"),
		"Playkeeper could not make a temporary folder for downloads: "+err.Error()+".",
		"Check that the machine has free disk space."), Err: err}
}

// upstream explains an error from Modrinth's or CurseForge's API.
func upstream(src addons.Source, err error) error {
	var e *addons.Error
	if err == nil || errors.As(err, &e) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	name := sourceName(src)
	var (
		rl *fetch.RateLimitError
		se *fetch.StatusError
		ne *fetch.NetError
		tl *fetch.TooLargeError
	)
	switch {
	case errors.As(err, &rl):
		secs := strconv.Itoa(int(rl.RetryAfter.Round(time.Second) / time.Second))
		return &addons.Error{Notice: notice(addons.KindRateLimited, kv("source", name, "seconds", secs),
			name+" asked Playkeeper to slow down.", "Try again in "+secs+" seconds."), Err: err}
	case errors.As(err, &se) && src == CurseForge && (se.Status == 401 || se.Status == 403):
		return &addons.Error{Notice: notice(KindKeyRefused, kv("source", name, "status", strconv.Itoa(se.Status)),
			"CurseForge refused Playkeeper's API key.",
			"The key may have been revoked. The owner can set a new CurseForge API key, or choose packs from Modrinth meanwhile."), Err: err}
	case errors.As(err, &se):
		return &addons.Error{Notice: notice(addons.KindUpstream, kv("source", name, "status", strconv.Itoa(se.Status)),
			fmt.Sprintf("%s answered with an error (HTTP %d).", name, se.Status),
			"Try again in a few minutes. If it keeps happening, "+name+" may be having problems."), Err: err}
	case errors.As(err, &ne):
		return &addons.Error{Notice: notice(addons.KindUnreachable, kv("source", name), "Playkeeper could not reach "+name+".",
			"Check that this machine can reach the internet, then try again."), Err: err}
	case errors.As(err, &tl):
		return &addons.Error{Notice: notice(addons.KindUpstream, kv("source", name, "status", "200"),
			name+" sent a larger answer than Playkeeper accepts.", "Try again later."), Err: err}
	}
	return &addons.Error{Notice: notice(addons.KindUpstream, kv("source", name, "status", ""),
		name+" could not be used: "+err.Error()+".", "Try again in a few minutes."), Err: err}
}

// downloadError explains why a verified download failed. Nothing has
// touched the server's folder when this is returned.
func downloadError(pack, file, from string, err error, max int64) error {
	var (
		he *fetch.HashError
		ze *fetch.SizeError
		tl *fetch.TooLargeError
		ho *fetch.HostError
		re *fetch.RedirectError
		se *fetch.StatusError
		ne *fetch.NetError
	)
	params := kv("pack", pack, "file", printable(file))
	tampered := "This can mean a damaged download or a tampered copy. Nothing was installed; try again later."
	switch {
	case errors.As(err, &he):
		params["algo"] = he.Algo
		return &addons.Error{Notice: notice(addons.KindHashMismatch, params,
			fmt.Sprintf("The download of %s does not match the %s hash %s lists, so Playkeeper did not install %s.", printable(file), he.Algo, from, pack), tampered), Err: err}
	case errors.As(err, &ze):
		return &addons.Error{Notice: notice(addons.KindSizeMismatch, params,
			fmt.Sprintf("The download of %s is not the size %s lists, so Playkeeper did not install %s.", printable(file), from, pack), tampered), Err: err}
	case errors.As(err, &tl):
		params["limit"] = fetch.Size(max)
		return &addons.Error{Notice: notice(addons.KindTooLarge, params,
			fmt.Sprintf("%s is larger than the %s Playkeeper accepts.", printable(file), fetch.Size(max)), "Nothing was installed."), Err: err}
	case errors.As(err, &re):
		params["host"] = printable(re.To)
		return &addons.Error{Notice: notice(addons.KindRedirectRefused, params,
			fmt.Sprintf("The download of %s was redirected to %s, which is not a host packs may download from.", printable(file), printable(re.To)),
			"Nothing was installed."), Err: err}
	case errors.As(err, &ho):
		params["host"] = printable(ho.Host)
		return &addons.Error{Notice: notice(addons.KindHostNotAllowed, params,
			fmt.Sprintf("%s would be downloaded from %s, which is not a host packs may download from.", printable(file), printable(ho.Host)),
			"Nothing was installed."), Err: err}
	case errors.As(err, &se):
		params["host"], params["status"] = printable(se.Service), strconv.Itoa(se.Status)
		return &addons.Error{Notice: notice(addons.KindUpstream, params,
			fmt.Sprintf("%s answered with an error (HTTP %d) for %s.", printable(se.Service), se.Status, printable(file)),
			"Try again in a few minutes. Nothing was installed."), Err: err}
	case errors.As(err, &ne):
		params["host"] = printable(ne.Service)
		return &addons.Error{Notice: notice(addons.KindUnreachable, params,
			fmt.Sprintf("Playkeeper could not download %s from %s.", printable(file), printable(ne.Service)),
			"Check that this machine can reach the internet, then try again. Nothing was installed."), Err: err}
	}
	var e *addons.Error
	if errors.As(err, &e) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &addons.Error{Notice: notice(addons.KindUpstream, params,
		fmt.Sprintf("Playkeeper could not download %s: %v.", printable(file), err), "Try again later. Nothing was installed."), Err: err}
}
