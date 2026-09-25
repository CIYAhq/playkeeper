package templates

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

// Catalog is what this Playkeeper can create: the create-server flow's
// server types, each type's versions, and the memory a new server may have
// on the machine.
type Catalog struct {
	Types               []CatalogType
	MemoryOptionsMB     []int
	RecommendedMemoryMB int
}

// CatalogType is one server type of the registry.
type CatalogType struct {
	ID        string
	Name      string
	Available bool
	// Versions are what the create-server flow offers for the type.
	Versions []CatalogVersion
	// VersionsError says why the versions could not be listed.
	VersionsError string
}

// CatalogVersion is a version a server of the type can be created with.
type CatalogVersion struct {
	// ID is what the create-server request takes as its version.
	ID               string
	MinecraftVersion string
	Build            map[string]string
	// Experimental versions need the user's confirmation.
	Experimental bool
}

// Plan is what importing a template would create, for the user to confirm
// in the create-server flow. Nothing happens until then.
type Plan struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Type        TypeChoice `json:"type"`
	// Version is nil when none can be chosen; Blockers say why.
	Version *VersionChoice `json:"version,omitempty"`
	// Settings are the template's, without its memory suggestion, which
	// MemoryMB takes into account.
	Settings Settings `json:"settings"`
	// MemoryMB is the memory budget to preselect; the user may change it.
	MemoryMB int `json:"memoryMB,omitempty"`
	// Addons are installed in this order once the server exists, each with
	// InstallAddon.
	Addons  []PlannedAddon `json:"addons"`
	Modpack *Modpack       `json:"modpack,omitempty"`
	Packs   []PlannedPack  `json:"packs"`
	// Skipped are the parts of the template this Playkeeper cannot honour.
	Skipped  []Notice `json:"skipped"`
	Warnings []Notice `json:"warnings"`
	Blockers []Notice `json:"blockers"`
	Ready    bool     `json:"ready"`
	// Fingerprint covers what the template and the catalog decide: not the
	// name or memory, which the user may change. See Confirm.
	Fingerprint string `json:"fingerprint"`
}

// TypeChoice is the server type the new server gets.
type TypeChoice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Requested is the template's type, when this Playkeeper uses another.
	Requested string `json:"requested,omitempty"`
}

// VersionChoice is the catalog version the new server gets.
type VersionChoice struct {
	ID               string            `json:"id"`
	MinecraftVersion string            `json:"minecraftVersion"`
	Build            map[string]string `json:"build,omitempty"`
	Experimental     bool              `json:"experimental,omitempty"`
	// Requested is the template's Minecraft version, when it differs.
	Requested string `json:"requested,omitempty"`
	// RequestedBuild is the template's build details, when they differ
	// from the catalog's.
	RequestedBuild map[string]string `json:"requestedBuild,omitempty"`
}

// PlannedAddon is an add-on the import installs.
type PlannedAddon struct {
	Addon
	// Request is what InstallAddon asks the add-on library for.
	Request addons.InstallRequest `json:"request"`
	// Unpinned is set when the pinned version is not used because the new
	// server's Minecraft version differs from the template's.
	Unpinned bool `json:"unpinned,omitempty"`
	// PageURL is the add-on's page on its source, by project id, for
	// checking it before trusting it.
	PageURL string `json:"pageUrl,omitempty"`
}

// PlannedPack is a pack the import adds, with the host it comes from.
type PlannedPack struct {
	Pack
	Host string `json:"host"`
}

// PlanImport works out what a template would create on this Playkeeper:
// the type (or the nearest one it can create), the version (or the nearest
// runnable one, with a warning), the settings, the memory to suggest, each
// add-on to install and what cannot be honoured, and why. It reads no
// network: add-ons are resolved and their downloads verified by the add-on
// library once the user confirms.
func PlanImport(t *Template, c Catalog) (*Plan, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	p := &Plan{
		Name: t.Name, Description: t.Description, Settings: t.Settings, Modpack: t.Modpack,
		Addons: []PlannedAddon{}, Packs: []PlannedPack{}, Skipped: []Notice{}, Warnings: []Notice{}, Blockers: []Notice{},
	}
	p.Settings.MemoryMB = 0
	if ct := p.chooseType(t, c.Types); ct != nil {
		p.chooseVersion(t, *ct)
	}
	p.chooseMemory(t.Settings.MemoryMB, c)
	p.planAddons(t)
	p.planPacks(t)
	p.Ready = len(p.Blockers) == 0 && p.Version != nil
	p.Fingerprint = p.fingerprint()
	return p, nil
}

// Confirm checks, when the user confirms an import, that the plan can be
// carried out and is still the one they saw. Plan the import again from
// the template and pass the Fingerprint that was shown.
func (p *Plan) Confirm(fingerprint string) error {
	switch {
	case len(p.Blockers) > 0:
		return &Error{Notice: p.Blockers[0]}
	case !p.Ready:
		return fail(KindVersionsUnavailable, nil, "Playkeeper cannot choose a version for this server.", "")
	case fingerprint != p.Fingerprint:
		return fail(addons.KindPlanChanged, nil, "What this template would create has changed since you confirmed it.",
			"Review it again, then confirm.")
	}
	return nil
}

func (p *Plan) warn(n Notice)  { p.Warnings = append(p.Warnings, n) }
func (p *Plan) block(n Notice) { p.Blockers = append(p.Blockers, n) }
func (p *Plan) skip(n Notice)  { p.Skipped = append(p.Skipped, n) }

// substitutes are the types that can stand in for one this Playkeeper
// cannot create, and why they fit.
var substitutes = map[string]struct{ id, why string }{
	"vanilla": {"paper", "Paper runs the same game."},
	"purpur":  {"paper", "Purpur is built on Paper, so Paper loads the same plugins."},
	"paper":   {"purpur", "Purpur is built on Paper and loads its plugins."},
	"fabric":  {"quilt", "Quilt loads most Fabric mods."},
	"quilt":   {"fabric", "Mods made only for Quilt are skipped."},
}

func (p *Plan) chooseType(t *Template, types []CatalogType) *CatalogType {
	find := func(id string) *CatalogType {
		for i := range types {
			if types[i].ID == id {
				return &types[i]
			}
		}
		return nil
	}
	want := t.Server.Type
	ct := find(want)
	name := typeName(want)
	if ct != nil {
		name = cmp.Or(ct.Name, name)
	}
	p.Type = TypeChoice{ID: want, Name: name}
	if ct != nil && ct.Available {
		return ct
	}
	if s, ok := substitutes[want]; ok && t.Modpack == nil {
		if alt := find(s.id); alt != nil && alt.Available {
			p.Type = TypeChoice{ID: alt.ID, Name: cmp.Or(alt.Name, typeName(alt.ID)), Requested: want}
			p.warn(notice(KindTypeSubstituted, kv("type", name, "substitute", p.Type.Name),
				fmt.Sprintf("This Playkeeper cannot create %s servers yet, so the new server runs %s. %s", name, p.Type.Name, s.why), ""))
			return alt
		}
	}
	switch {
	case ct == nil:
		p.block(notice(KindTypeUnknown, kv("type", name),
			fmt.Sprintf("This Playkeeper does not know the server type \"%s\" that the template uses.", name),
			"Update Playkeeper: a newer version may support it."))
	case t.Modpack != nil:
		mp := printable(t.Modpack.Name)
		p.block(notice(KindTypeUnavailable, kv("type", name, "modpack", mp),
			fmt.Sprintf("The template's modpack %s needs a %s server, which this Playkeeper cannot create yet.", mp, name),
			"Update Playkeeper: a newer version may support it."))
	default:
		p.block(notice(KindTypeUnavailable, kv("type", name),
			fmt.Sprintf("This Playkeeper cannot create %s servers yet.", name),
			"Update Playkeeper, or ask for a template of a type you can create."))
	}
	return nil
}

func (p *Plan) chooseVersion(t *Template, ct CatalogType) {
	name, want := p.Type.Name, t.Server.MinecraftVersion
	if ct.VersionsError != "" || len(ct.Versions) == 0 {
		msg := fmt.Sprintf("Playkeeper could not list the versions of %s.", name)
		if ct.VersionsError != "" {
			msg = fmt.Sprintf("Playkeeper could not list the versions of %s: %s.", name, strings.TrimSuffix(ct.VersionsError, "."))
		}
		p.block(notice(KindVersionsUnavailable, kv("type", name), msg, "Check that this machine can reach the internet, then try again."))
		return
	}
	v, same := nearest(ct.Versions, want, t.Server.Build)
	if t.Modpack != nil && !same {
		mp := printable(t.Modpack.Name)
		p.block(notice(KindModpackUnavailable, kv("modpack", mp, "type", name, "minecraft", want),
			fmt.Sprintf("The modpack %s is made for Minecraft %s, which this Playkeeper cannot create for %s servers.", mp, want, name),
			"Ask whoever shared the template for one with a version you can create."))
		return
	}
	p.Version = &VersionChoice{ID: v.ID, MinecraftVersion: v.MinecraftVersion, Build: v.Build, Experimental: v.Experimental}
	if !same {
		p.Version.Requested = want
		p.warn(notice(KindVersionSubstituted, kv("type", name, "requested", want, "minecraft", v.MinecraftVersion),
			fmt.Sprintf("Playkeeper cannot create %s servers on Minecraft %s here, so the new server gets %s, the nearest version it can.", name, want, v.MinecraftVersion),
			"Worlds and add-ons made for one version do not always work on another."))
	}
	if len(t.Server.Build) > 0 && !maps.Equal(v.Build, t.Server.Build) {
		p.Version.RequestedBuild = t.Server.Build
	}
	if v.Experimental {
		p.warn(notice(KindVersionExperimental, kv("type", name, "minecraft", v.MinecraftVersion),
			fmt.Sprintf("Minecraft %s is an experimental version of %s: it may be unstable, and its worlds may not open in later versions.", v.MinecraftVersion, name),
			"You are asked to confirm it when the server is created."))
	}
}

// mcVersion is a Minecraft version for ordering: 1.21.11 and 26.1.2 as
// numbers; snapshots, pre-releases and release candidates of a version
// (26.1-snapshot-1, 1.21.5-rc1) come before it.
type mcVersion struct {
	nums []int
	pre  bool
}

func parseMinecraft(s string) (mcVersion, bool) {
	base, _, pre := strings.Cut(s, "-")
	parts := strings.Split(base, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return mcVersion{}, false
	}
	v := mcVersion{pre: pre}
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || part != strconv.Itoa(n) || n < 0 {
			return mcVersion{}, false
		}
		v.nums = append(v.nums, n)
	}
	return v, true
}

func (a mcVersion) compare(b mcVersion) int {
	at := func(ns []int, i int) int {
		if i < len(ns) {
			return ns[i]
		}
		return 0
	}
	for i := range max(len(a.nums), len(b.nums)) {
		if c := cmp.Compare(at(a.nums, i), at(b.nums, i)); c != 0 {
			return c
		}
	}
	switch {
	case a.pre && !b.pre:
		return -1
	case !a.pre && b.pre:
		return 1
	}
	return 0
}

// sameLine is true for versions of one line, such as 1.21.4 and 1.21.11.
func (a mcVersion) sameLine(b mcVersion) bool {
	return a.nums[0] == b.nums[0] && a.nums[1] == b.nums[1]
}

// nearest picks the catalog version for a template's Minecraft version and
// reports whether it is the same one. The same version comes with the
// template's build when the catalog has it, else a stable build. Otherwise
// it is the nearest stable version: newer ones of the same line first
// (1.21.4 goes to 1.21.5 before 1.21.3), then older ones of it, then the
// nearest newer and older lines.
func nearest(vs []CatalogVersion, want string, build map[string]string) (CatalogVersion, bool) {
	var same []CatalogVersion
	for _, v := range vs {
		if v.MinecraftVersion == want {
			same = append(same, v)
		}
	}
	if len(same) > 0 {
		if i := slices.IndexFunc(same, func(v CatalogVersion) bool { return len(build) > 0 && maps.Equal(v.Build, build) }); i >= 0 {
			return same[i], true
		}
		if i := slices.IndexFunc(same, func(v CatalogVersion) bool { return !v.Experimental }); i >= 0 {
			return same[i], true
		}
		return same[0], true
	}
	type cand struct {
		v  CatalogVersion
		mv mcVersion
	}
	var stable, all []cand
	for _, v := range vs {
		if mv, ok := parseMinecraft(v.MinecraftVersion); ok {
			all = append(all, cand{v, mv})
			if !v.Experimental {
				stable = append(stable, cand{v, mv})
			}
		}
	}
	pool := stable
	if len(pool) == 0 {
		pool = all
	}
	if len(pool) == 0 {
		return vs[0], false
	}
	w, ok := parseMinecraft(want)
	if !ok {
		// Old-style snapshots such as 25w14a have no order here.
		best := pool[0]
		for _, c := range pool[1:] {
			if c.mv.compare(best.mv) > 0 {
				best = c
			}
		}
		return best.v, false
	}
	pick := func(line, newer bool) (CatalogVersion, bool) {
		best := -1
		for i, c := range pool {
			d := c.mv.compare(w)
			if line && !c.mv.sameLine(w) || newer && d < 0 || !newer && d >= 0 {
				continue
			}
			if best < 0 || newer && c.mv.compare(pool[best].mv) < 0 || !newer && c.mv.compare(pool[best].mv) > 0 {
				best = i
			}
		}
		if best < 0 {
			return CatalogVersion{}, false
		}
		return pool[best].v, true
	}
	for _, try := range [][2]bool{{true, true}, {true, false}, {false, true}, {false, false}} {
		if v, ok := pick(try[0], try[1]); ok {
			return v, false
		}
	}
	return pool[0].v, false
}

func (p *Plan) chooseMemory(want int, c Catalog) {
	var opts []int
	for _, mb := range c.MemoryOptionsMB {
		if mb > 0 {
			opts = append(opts, mb)
		}
	}
	slices.Sort(opts)
	opts = slices.Compact(opts)
	if len(opts) == 0 {
		p.block(notice(KindNoMemory, nil, "This machine has no memory left for another server.",
			"Give another server less memory in its settings, then try again."))
		return
	}
	largest := opts[len(opts)-1]
	switch {
	case want == 0 && slices.Contains(opts, c.RecommendedMemoryMB):
		p.MemoryMB = c.RecommendedMemoryMB
	case want == 0:
		p.MemoryMB = opts[0]
	case want > largest:
		p.MemoryMB = largest
		p.warn(notice(KindMemoryReduced, kv("suggested", strconv.Itoa(want), "max", strconv.Itoa(largest)),
			fmt.Sprintf("The template suggests %s of memory, but this machine can give a new server at most %s.", memory(want), memory(largest)),
			"The server may run short of memory with many players or mods."))
	default:
		i, _ := slices.BinarySearch(opts, want)
		p.MemoryMB = opts[i]
	}
}

func memory(mb int) string {
	switch {
	case mb%1024 == 0:
		return fmt.Sprintf("%d GB", mb/1024)
	case mb%512 == 0:
		return fmt.Sprintf("%.1f GB", float64(mb)/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}

func (p *Plan) planAddons(t *Template) {
	if len(t.Addons) == 0 {
		return
	}
	var target addons.Target
	if p.Version != nil {
		var err error
		if target, err = addons.TargetFor(p.Type.ID); err != nil {
			p.skip(notice(KindAddonUnsupported, kv("type", p.Type.Name),
				fmt.Sprintf("The template's add-ons are skipped: Playkeeper cannot install add-ons on %s servers yet.", p.Type.Name), ""))
			return
		}
	}
	unpin := p.Version != nil && p.Version.MinecraftVersion != t.Server.MinecraftVersion
	unpinned := 0
	for _, a := range dependenciesFirst(t.Addons) {
		if p.Version != nil && !slices.Contains(target.Sources(), a.Source) {
			name := printable(a.Name)
			p.skip(notice(KindAddonUnsupported, kv("name", name, "source", a.Source.Name(), "type", p.Type.Name),
				fmt.Sprintf("%s is skipped: it comes from %s, which has no add-ons for %s servers.", name, a.Source.Name(), p.Type.Name), ""))
			continue
		}
		pa := PlannedAddon{Addon: a, Unpinned: unpin && a.Pin != nil, PageURL: pageURL(a)}
		pa.Request = request(a, pa.Unpinned)
		if pa.Unpinned {
			unpinned++
		}
		p.Addons = append(p.Addons, pa)
	}
	if unpinned > 0 {
		mc := p.Version.MinecraftVersion
		p.warn(notice(KindAddonsUnpinned, kv("count", strconv.Itoa(unpinned), "requested", t.Server.MinecraftVersion, "minecraft", mc),
			fmt.Sprintf("The template's add-on versions are for Minecraft %s, so Playkeeper installs the newest version of each that fits %s.", t.Server.MinecraftVersion, mc),
			"Add-ons without a version for "+mc+" yet are skipped when the server is created."))
	}
}

// dependenciesFirst orders add-ons so that each comes before the add-ons
// that need it, keeping the template's order otherwise.
func dependenciesFirst(as []Addon) []Addon {
	children := map[addons.Key][]int{}
	for i, a := range as {
		if a.DependencyOf != "" {
			k := addons.Key{Source: a.Source, ProjectID: a.DependencyOf}
			children[k] = append(children[k], i)
		}
	}
	placed := make([]bool, len(as))
	out := make([]Addon, 0, len(as))
	var place func(int)
	place = func(i int) {
		if placed[i] {
			return
		}
		placed[i] = true
		for _, c := range children[as[i].Key()] {
			place(c)
		}
		out = append(out, as[i])
	}
	for i := range as {
		place(i)
	}
	return out
}

// request is what to ask the add-on library for: the pinned version, or
// the newest release that fits.
func request(a Addon, unpinned bool) addons.InstallRequest {
	req := addons.InstallRequest{Source: a.Source, Project: a.Project}
	if a.Pin != nil && !unpinned {
		req.VersionID = a.Pin.VersionID
		req.AllowPrerelease = a.Pin.Channel == "beta" || a.Pin.Channel == "alpha"
	}
	return req
}

func pageURL(a Addon) string {
	if a.Source == addons.Modrinth {
		return "https://modrinth.com/project/" + a.Project
	}
	return ""
}

func (p *Plan) planPacks(t *Template) {
	var hosts []string
	data := 0
	for _, pk := range t.Packs {
		host := ""
		if u, err := url.Parse(pk.URL); err == nil {
			host = strings.ToLower(u.Hostname())
		}
		p.Packs = append(p.Packs, PlannedPack{Pack: pk, Host: host})
		name := printable(pk.Name)
		switch pk.Kind {
		case ResourcePack:
			p.warn(notice(KindResourcePack, kv("name", name, "host", host),
				fmt.Sprintf("The resource pack %s comes from %s, a website Playkeeper cannot vouch for.", name, host),
				"Keep it only if you trust whoever shared the template. It is checked against the template's SHA-1 checksum."))
		case DataPack:
			data++
			if !slices.Contains(hosts, host) {
				hosts = append(hosts, host)
			}
		}
	}
	if data > 0 {
		what := "1 data pack"
		if data > 1 {
			what = strconv.Itoa(data) + " data packs"
		}
		p.warn(notice(KindDataPacks, kv("count", strconv.Itoa(data), "hosts", strings.Join(hosts, ", ")),
			fmt.Sprintf("The template adds %s from %s. Data packs change how the game plays and can run commands in the world.", what, joinNames(hosts)),
			"Keep them only if you trust whoever shared the template. Each download is checked against the template's checksum."))
	}
}

func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func (p *Plan) fingerprint() string {
	b, _ := json.Marshal(struct {
		Type     TypeChoice
		Version  *VersionChoice
		Settings Settings
		Addons   []PlannedAddon
		Modpack  *Modpack
		Packs    []PlannedPack
		Skipped  []Notice
		Blockers []Notice
	}{p.Type, p.Version, p.Settings, p.Addons, p.Modpack, p.Packs, p.Skipped, p.Blockers})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}
