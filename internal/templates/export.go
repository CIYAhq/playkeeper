package templates

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

// Setup is a server's setup as Playkeeper records it: what Export reads.
type Setup struct {
	Name        string
	Description string
	// Type is the server's type; empty means paper, as for servers made
	// before 0.3.0.
	Type             string
	MinecraftVersion string
	// Build holds the type's build details, as in Server.Build.
	Build    map[string]string
	Settings Settings
	// HasIcon is set when the server has a server-list icon, which does
	// not travel.
	HasIcon bool
	// Addons are the server's rows of the addons table.
	Addons []addons.Installed
	// Folder is the add-on library's scan of the server's add-on folder
	// with those rows (Library.Scan with identify). When set, it decides
	// what travels: add-ons whose file is gone are left out, changed files
	// are noted, and files added by hand travel when Modrinth knows them.
	Folder *addons.ScanResult
	// Modpack is the modpack the server was created from.
	Modpack *ModpackSetup
	// Packs are the server's resource pack and data packs.
	Packs []PackSetup
	// OwnHosts are this Playkeeper's host names. Packs served from them
	// are left out: they point back at this server.
	OwnHosts []string
}

// ModpackSetup is the modpack a server was created from.
type ModpackSetup struct {
	Modpack
	// Includes are the add-ons the modpack installed. They travel with the
	// modpack rather than one by one.
	Includes []addons.Key
}

// PackSetup is one of a server's packs.
type PackSetup struct {
	Pack
	// Uploaded is set for a pack uploaded to the dashboard rather than
	// linked from a public address.
	Uploaded bool
}

// ExportOptions are the export dialog's choices. The zero value exports
// everything that can travel, with add-ons at their installed versions.
type ExportOptions struct {
	WithoutAddons   bool
	WithoutSettings bool
	WithoutPacks    bool
	// Latest names each add-on as "the newest version that fits" rather
	// than the version installed.
	Latest bool
}

// Report is what an export left out and why, and notes on what never
// travels.
type Report struct {
	LeftOut []Notice `json:"leftOut"`
	Notes   []Notice `json:"notes"`
}

// Export makes a template of a server's setup. What cannot travel is left
// out and reported; the template is always valid.
func Export(s Setup, opts ExportOptions) (*Template, *Report, error) {
	t := &Template{
		Format: Format, Name: cmp.Or(clean(s.Name, maxName), "My server"), Description: clean(s.Description, maxDescription),
		Game: Game, Server: Server{Type: cmp.Or(s.Type, "paper"), MinecraftVersion: s.MinecraftVersion},
	}
	if !validType(t.Server.Type) || !validMinecraft(t.Server.MinecraftVersion) {
		return nil, nil, fail(KindExportInvalid, kv("type", printable(t.Server.Type), "minecraft", printable(t.Server.MinecraftVersion)),
			"This server's type or Minecraft version is not recorded properly, so it cannot be shared as a template.",
			"Check the server's version in its settings, then try again.")
	}
	x := &exporter{s: s, opts: opts, t: t, fromPack: map[addons.Key]bool{}, rep: &Report{LeftOut: []Notice{}, Notes: []Notice{}}}
	x.build()
	if !opts.WithoutSettings {
		x.settings()
	}
	x.modpack()
	if !opts.WithoutAddons {
		x.addons()
	}
	if !opts.WithoutPacks {
		x.packs()
	}
	x.fit()
	x.notes()
	if err := t.Validate(); err != nil {
		return nil, nil, &Error{Notice: notice(KindExportInvalid, nil,
			"Playkeeper could not make a valid template of this server: "+err.Error(), "Please report this as a bug."), Err: err}
	}
	return t, x.rep, nil
}

type exporter struct {
	s         Setup
	opts      ExportOptions
	t         *Template
	rep       *Report
	fromPack  map[addons.Key]bool
	packMods  int
	addonNote map[addons.Key]Kind
}

func (x *exporter) leftOut(k Kind, params map[string]string, msg, hint string) {
	x.rep.LeftOut = append(x.rep.LeftOut, notice(k, params, msg, hint))
}

func (x *exporter) note(k Kind, params map[string]string, msg string) {
	x.rep.Notes = append(x.rep.Notes, notice(k, params, msg, ""))
}

func (x *exporter) build() {
	dropped := false
	for _, k := range sortedKeys(x.s.Build) {
		if v := x.s.Build[k]; validBuildKey(k) && validBuildValue(v) && len(x.t.Server.Build) < maxBuild {
			if x.t.Server.Build == nil {
				x.t.Server.Build = map[string]string{}
			}
			x.t.Server.Build[k] = v
		} else {
			dropped = true
		}
	}
	if dropped {
		x.leftOut(KindLeftOutBuild, nil, "Some build details of the server software were left out: a template cannot carry them.",
			"A new server gets the build its Playkeeper offers for this Minecraft version.")
	}
}

func (x *exporter) settings() {
	st := x.s.Settings
	st.PVP, st.Hardcore = copyBool(st.PVP), copyBool(st.Hardcore)
	formatted := strings.ContainsFunc(st.MOTD, func(r rune) bool { return r == '§' || unicode.IsControl(r) })
	st.MOTD = strings.TrimRight(strip(st.MOTD), " ")
	bad := badSettings(st)
	for _, f := range bad {
		clearSetting(&st, f)
		x.leftOut(KindLeftOutSetting, kv("setting", f), fmt.Sprintf("The %s was left out: a template cannot carry it as it is.", settingLabels[f]),
			"A new server uses its default instead.")
	}
	if formatted && !slices.Contains(bad, "motd") {
		x.leftOut(KindLeftOutFormatting, kv("setting", "motd"),
			"The colours and line breaks of the server list message were left out: a template carries it as one line of plain text.", "")
	}
	x.t.Settings = st
}

func copyBool(b *bool) *bool {
	if b == nil {
		return nil
	}
	v := *b
	return &v
}

func (x *exporter) modpack() {
	ms := x.s.Modpack
	if ms == nil {
		return
	}
	m := ms.Modpack
	if !validRef(m.Slug) {
		m.Slug = ""
	}
	m.Name = cmp.Or(clean(m.Name, maxLabel), m.Slug, m.Project)
	m.Pin = tidyPin(m.Pin)
	name := printable(m.Name)
	params := kv("name", name)
	switch {
	case m.Source != addons.Modrinth:
		x.leftOut(KindLeftOutModpack, params, fmt.Sprintf("The modpack %s was left out: templates carry modpacks from Modrinth only.", name), "")
		return
	case slices.Contains(nonModTypes, x.t.Server.Type):
		x.leftOut(KindLeftOutModpack, params, fmt.Sprintf("The modpack %s was left out: it does not fit a %s server.", name, typeName(x.t.Server.Type)), "")
		return
	}
	e := checkRef("modpack", name, m.Source, m.Project, m.Slug)
	if e == nil {
		e = checkText("modpack.name", name, m.Name, maxLabel, true)
	}
	if e == nil {
		e = checkPin("modpack.pin", name, m.Source, m.Pin)
	}
	if e != nil {
		x.leftOut(KindLeftOutModpack, params, fmt.Sprintf("The modpack %s was left out: Playkeeper's record of it is incomplete.", name), "")
		return
	}
	x.t.Modpack = &m
	for _, k := range ms.Includes {
		x.fromPack[k] = true
	}
}

func tidyPin(p Pin) Pin {
	p.Hash = strings.ToLower(p.Hash)
	p.VersionNumber = cmp.Or(clean(p.VersionNumber, maxLabel), clean(p.VersionID, maxLabel))
	if !slices.Contains(channels, p.Channel) {
		p.Channel = ""
	}
	return p
}

// candidate is an installed add-on that may travel.
type candidate struct {
	rec  addons.Installed
	note Kind
}

func (x *exporter) addons() {
	var cands []candidate
	if f := x.s.Folder; f != nil {
		for _, e := range f.Entries {
			switch {
			case e.Status == addons.FileManaged && e.Installed != nil:
				cands = append(cands, candidate{rec: *e.Installed})
			case e.Status == addons.FileModified && e.Installed != nil:
				cands = append(cands, candidate{*e.Installed, KindNoteChanged})
			case e.Status == addons.FileIdentified && e.Identified != nil:
				cands = append(cands, candidate{*e.Identified, KindNoteIdentified})
			default:
				name := printable(cmp.Or(clean(e.Meta.Name, maxLabel), e.FileName))
				x.leftOut(KindLeftOutUpload, kv("name", name, "file", printable(e.FileName)),
					fmt.Sprintf("%s was added by hand, and Playkeeper cannot tell where it comes from, so it cannot travel.", name),
					"People who use the template can add it themselves.")
			}
		}
		for _, m := range f.Missing {
			name := printable(m.Name)
			x.leftOut(KindLeftOutMissing, kv("name", name, "file", printable(m.FileName)),
				fmt.Sprintf("%s was left out: its file is no longer in the %s folder.", name, f.Folder), "")
		}
	} else {
		for _, r := range x.s.Addons {
			cands = append(cands, candidate{rec: r})
		}
	}
	if len(cands) == 0 {
		return
	}
	target, err := addons.TargetFor(x.t.Server.Type)
	if err != nil {
		x.leftOut(KindLeftOutAddon, kv("type", x.t.Server.Type), "The server's add-ons were left out: "+err.Error(), "")
		return
	}

	var items []item
	x.addonNote = map[addons.Key]Kind{}
	seen := map[addons.Key]bool{}
	for _, c := range cands {
		key := c.rec.Key()
		if seen[key] {
			continue
		}
		seen[key] = true
		if x.fromPack[key] {
			x.packMods++
			continue
		}
		if a, ok := x.addon(c.rec, target); ok {
			items = append(items, item{a, c.rec.Requires})
			x.addonNote[key] = c.note
		}
	}
	items = dependencyOrder(items)
	if n := len(items) - MaxAddons; n > 0 {
		x.leftOut(KindLeftOutAddonsLimit, kv("count", strconv.Itoa(n), "limit", strconv.Itoa(MaxAddons)),
			fmt.Sprintf("%d add-ons were left out: a template holds up to %d.", n, MaxAddons), "Share a modpack instead.")
		items = items[:MaxAddons]
	}
	for _, it := range items {
		x.t.Addons = append(x.t.Addons, it.addon)
	}
	fixDependencies(x.t.Addons)
}

// fit leaves out add-ons from the end until the template is no larger than
// a template can be, which only hundreds of add-ons with long names reach.
// Add-ons come after those they need, so no add-on loses one it needs.
func (x *exporter) fit() {
	js, err := canonicalJSON(x.t)
	if err != nil || len(js) <= MaxFileSize {
		return
	}
	over, n := len(js)-MaxFileSize, 0
	for over > 0 && len(x.t.Addons) > 0 {
		last, _ := json.Marshal(x.t.Addons[len(x.t.Addons)-1])
		over -= len(last) + len(",")
		x.t.Addons = x.t.Addons[:len(x.t.Addons)-1]
		n++
	}
	fixDependencies(x.t.Addons)
	x.leftOut(KindLeftOutAddonsSize, kv("count", strconv.Itoa(n), "limit", maxFileLabel),
		fmt.Sprintf("%d add-ons were left out: with them, the template would be larger than the %s a template can be.", n, maxFileLabel),
		"Share a modpack instead.")
}

// addon turns an installed add-on into a template's, or reports why it
// cannot travel.
func (x *exporter) addon(rec addons.Installed, target addons.Target) (Addon, bool) {
	a := Addon{Source: rec.Source, Project: rec.ProjectID, DependencyOf: rec.DependencyOf}
	if validRef(rec.Slug) {
		a.Slug = rec.Slug
	}
	a.Name = cmp.Or(clean(rec.Name, maxLabel), a.Slug, clean(a.Project, maxLabel))
	if x.opts.Latest {
		a.Latest = true
	} else {
		p := tidyPin(Pin{VersionID: rec.VersionID, VersionNumber: rec.VersionNumber, Channel: rec.Channel, HashAlgo: rec.HashAlgo, Hash: rec.Hash})
		a.Pin = &p
	}
	name := printable(a.Name)
	params := kv("name", name, "source", a.Source.Name())
	switch {
	case a.Source != addons.Modrinth && a.Source != addons.Hangar:
		x.leftOut(KindLeftOutAddon, params, fmt.Sprintf("%s was left out: templates carry add-ons from Modrinth and Hangar only.", name), "")
		return a, false
	case !slices.Contains(target.Sources(), a.Source):
		x.leftOut(KindLeftOutAddon, params, fmt.Sprintf("%s was left out: %s has no add-ons for %s servers.", name, a.Source.Name(), target.Name()), "")
		return a, false
	}
	e := checkRef("", name, a.Source, a.Project, a.Slug)
	if e == nil {
		e = checkText("", name, a.Name, maxLabel, true)
	}
	if e == nil && a.Pin != nil {
		e = checkPin("", name, a.Source, *a.Pin)
	}
	if e != nil {
		x.leftOut(KindLeftOutAddon, params, fmt.Sprintf("%s was left out: Playkeeper's record of it is incomplete.", name),
			"Remove it and install it again from the library, then export again.")
		return a, false
	}
	return a, true
}

type item struct {
	addon    Addon
	requires []string
}

// dependencyOrder puts add-ons others need first, so that importing
// installs them at their own pinned versions before the add-ons that need
// them. Otherwise add-ons go by name.
func dependencyOrder(items []item) []item {
	idx := map[addons.Key]int{}
	for i, it := range items {
		idx[it.addon.Key()] = i
	}
	before := make([][]int, len(items))
	for i, it := range items {
		if p, ok := idx[addons.Key{Source: it.addon.Source, ProjectID: it.addon.DependencyOf}]; ok && it.addon.DependencyOf != "" && p != i {
			before[p] = append(before[p], i)
		}
		for _, r := range it.requires {
			if j, ok := idx[addons.Key{Source: it.addon.Source, ProjectID: r}]; ok && j != i {
				before[i] = append(before[i], j)
			}
		}
	}
	less := func(i, j int) bool {
		a, b := items[i].addon, items[j].addon
		return cmp.Or(
			strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)),
			strings.Compare(string(a.Source), string(b.Source)),
			strings.Compare(a.Project, b.Project),
		) < 0
	}
	placed := make([]bool, len(items))
	out := make([]item, 0, len(items))
	for len(out) < len(items) {
		best, fallback := -1, -1
		for i := range items {
			if placed[i] {
				continue
			}
			if fallback < 0 || less(i, fallback) {
				fallback = i
			}
			ready := !slices.ContainsFunc(before[i], func(j int) bool { return !placed[j] })
			if ready && (best < 0 || less(i, best)) {
				best = i
			}
		}
		// Add-ons that need each other in a circle go by name.
		if best < 0 {
			best = fallback
		}
		placed[best] = true
		out = append(out, items[best])
	}
	return out
}

// fixDependencies drops the links to add-ons that did not travel, and any
// circle of links.
func fixDependencies(as []Addon) {
	idx := map[addons.Key]int{}
	for i, a := range as {
		idx[a.Key()] = i
	}
	for i := range as {
		if _, ok := idx[addons.Key{Source: as[i].Source, ProjectID: as[i].DependencyOf}]; !ok || as[i].DependencyOf == as[i].Project {
			as[i].DependencyOf = ""
		}
	}
	for i := range as {
		at := i
		for range len(as) {
			if as[at].DependencyOf == "" {
				break
			}
			at = idx[addons.Key{Source: as[at].Source, ProjectID: as[at].DependencyOf}]
			if at == i {
				as[i].DependencyOf = ""
				break
			}
		}
	}
}

func (x *exporter) packs() {
	var resource, data int
	urls, names := map[string]bool{}, map[string]bool{}
	for i, ps := range x.s.Packs {
		p := ps.Pack
		p.Name = packName(p.Name, i)
		p.SHA1, p.SHA256 = strings.ToLower(p.SHA1), strings.ToLower(p.SHA256)
		name := printable(p.Name)
		params := kv("name", name)
		switch {
		case ps.Uploaded:
			x.leftOut(KindLeftOutPackUpload, params,
				fmt.Sprintf("The pack %s was left out: it was uploaded to this dashboard, and templates only carry packs from public addresses.", name),
				"Put it on a public site such as Modrinth, use it from there, then export again.")
			continue
		case !publicURL(p.URL) || x.ownHost(p.URL):
			x.leftOut(KindLeftOutPackAddress, params,
				fmt.Sprintf("The pack %s was left out: it is not at a public HTTPS address that others can use.", name),
				"Put it on a public site such as Modrinth, use it from there, then export again.")
			continue
		case p.SHA1 != "" && !validHash("sha1", p.SHA1) || p.SHA256 != "" && !validHash("sha256", p.SHA256) ||
			p.Kind == ResourcePack && p.SHA1 == "" || p.SHA1 == "" && p.SHA256 == "":
			x.leftOut(KindLeftOutPackHash, params,
				fmt.Sprintf("The pack %s was left out: Playkeeper has no checksum for it, so others could not check their download.", name), "")
			continue
		case urls[p.URL]:
			continue
		}
		switch {
		case p.Kind == ResourcePack && resource == 0:
			p.Prompt = clean(p.Prompt, maxPrompt)
			resource++
		case p.Kind == ResourcePack:
			x.leftOut(KindLeftOutPacksLimit, params, fmt.Sprintf("The resource pack %s was left out: a server has one resource pack.", name), "")
			continue
		case p.Kind == DataPack && names[strings.ToLower(p.Name)]:
			x.leftOut(KindLeftOutPacksLimit, params, fmt.Sprintf("A second data pack named %s was left out.", name), "")
			continue
		case p.Kind == DataPack && data < MaxDataPacks:
			p.Required, p.Prompt = false, ""
			names[strings.ToLower(p.Name)] = true
			data++
		case p.Kind == DataPack:
			x.leftOut(KindLeftOutPacksLimit, params, fmt.Sprintf("The data pack %s was left out: a template holds up to %d.", name, MaxDataPacks), "")
			continue
		default:
			x.leftOut(KindLeftOutPacksLimit, params, fmt.Sprintf("The pack %s was left out: it is neither a resource pack nor a data pack.", name), "")
			continue
		}
		urls[p.URL] = true
		x.t.Packs = append(x.t.Packs, p)
	}
}

// packName makes a pack's name fit to be a file name.
func packName(s string, i int) string {
	s = clean(s, maxLabel+len(".zip"))
	if strings.HasSuffix(strings.ToLower(s), ".zip") {
		s = s[:len(s)-len(".zip")]
	}
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, s)
	s = strings.TrimRight(strings.TrimLeft(s, ".- "), ". ")
	if r := []rune(s); len(r) > maxLabel {
		s = strings.TrimRight(string(r[:maxLabel]), ". ")
	}
	if s == "" {
		return fmt.Sprintf("pack-%d", i+1)
	}
	return s
}

func (x *exporter) ownHost(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return true
	}
	h := strings.ToLower(u.Hostname())
	for _, o := range x.s.OwnHosts {
		o = strings.ToLower(strings.TrimSpace(o))
		if pu, err := url.Parse(o); err == nil && pu.Host != "" {
			o = pu.Hostname()
		} else if host, _, err := net.SplitHostPort(o); err == nil {
			o = host
		}
		o = strings.Trim(o, "[]")
		if o != "" && (h == o || strings.HasSuffix(h, "."+o)) {
			return true
		}
	}
	return false
}

func (x *exporter) notes() {
	x.note(KindNoteWorld, nil, "The world stays on this server: a server made from the template starts with fresh land.")
	x.note(KindNotePlayers, nil, "The allowlist, operators, bans, address and ports stay private.")
	if len(x.t.Addons) > 0 {
		what := "Plugin"
		if t, err := addons.TargetFor(x.t.Server.Type); err == nil && t.Kind == "mod" {
			what = "Mod"
		}
		x.note(KindNoteAddonConfig, kv("kind", strings.ToLower(what)),
			what+" settings stay on this server: a new server starts with each one's defaults.")
		if x.opts.Latest {
			x.note(KindNoteLatest, nil, "Add-ons are named without versions: a new server gets the newest version of each that fits it.")
		}
	}
	if x.packMods > 0 {
		x.note(KindNoteModpackAddons, kv("count", strconv.Itoa(x.packMods), "name", printable(x.t.Modpack.Name)),
			fmt.Sprintf("The %d mods from the modpack %s travel with the modpack.", x.packMods, printable(x.t.Modpack.Name)))
	}
	for _, a := range x.t.Addons {
		name := printable(a.Name)
		switch x.addonNote[a.Key()] {
		case KindNoteChanged:
			x.note(KindNoteChanged, kv("name", name),
				fmt.Sprintf("%s has changed on this server since Playkeeper installed it; the template names the version Playkeeper installed.", name))
		case KindNoteIdentified:
			x.note(KindNoteIdentified, kv("name", name),
				fmt.Sprintf("%s was added by hand; Modrinth knows the file, so it travels like the others.", name))
		}
	}
	if x.s.HasIcon {
		x.leftOut(KindLeftOutIcon, nil, "The server icon was left out: templates carry no images.", "")
	}
}

// clean makes a line of text fit for a template: formatting codes and
// invisible characters removed, spaces tidied, and cut to max characters.
func clean(s string, max int) string {
	out := strings.Join(strings.Fields(strip(s)), " ")
	if r := []rune(out); len(r) > max {
		out = strings.TrimSpace(string(r[:max]))
	}
	return out
}

// strip removes Minecraft's formatting codes (§ and the character after
// it) and invisible characters, and turns line breaks and other spacing
// into spaces.
func strip(s string) string {
	var b strings.Builder
	code := false
	for _, r := range s {
		switch {
		case code:
			code = false
		case r == '§':
			code = true
		case isFormat(r) || r == utf8.RuneError:
		case unicode.IsControl(r) || unicode.IsSpace(r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// typeName is a server type's name for messages.
func typeName(id string) string {
	if t, err := addons.TargetFor(id); err == nil {
		return t.Name()
	}
	if id == "vanilla" {
		return "Vanilla"
	}
	return printable(id)
}
