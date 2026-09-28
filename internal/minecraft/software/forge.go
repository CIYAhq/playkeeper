package software

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

const (
	forgeMaven      = "https://maven.minecraftforge.net/net/minecraftforge/forge"
	forgePromotions = "https://files.minecraftforge.net/net/minecraftforge/forge/promotions_slim.json"
)

// Forge's Maven names each build after its Minecraft version: 26.2-65.1.3
// is Forge 65.1.3 for Minecraft 26.2. A pin keeps the two apart.
var reForgeVersion = regexp.MustCompile(`^(0|[1-9][0-9]{0,3})\.(0|[1-9][0-9]{0,4})\.(0|[1-9][0-9]{0,5})$`)

func forgeMavenUpstream(hc *http.Client) upstream {
	return upstream{name: "Forge", hosts: []string{"maven.minecraftforge.net"}, hc: hc}
}

func forgeFilesUpstream(hc *http.Client) upstream {
	return upstream{name: "Forge", hosts: []string{"files.minecraftforge.net"}, hc: hc}
}

// forgeBuilds is Forge's version lists: every build by Minecraft version,
// newest first, and the build Forge recommends for each Minecraft version
// it has recommended one for.
type forgeBuilds struct {
	byMC        map[string][]string
	recommended map[string]string
}

// channel says how finished a build is. Forge marks no build as unfinished;
// it promotes a recommended build once a Minecraft version's builds have
// settled. Builds from that one on are stable, and the rest count as beta,
// like a Minecraft version with no recommended build yet.
func (fb forgeBuilds) channel(mc string) func(string) Channel {
	return func(v string) Channel {
		if rec, ok := fb.recommended[mc]; ok && compareVersions(v, rec) >= 0 {
			return Stable
		}
		return Beta
	}
}

func forgeVersions(ctx context.Context, hc *http.Client) (forgeBuilds, error) {
	u := forgeMavenUpstream(hc)
	const what = "its version list"
	b, err := u.read(ctx, forgeMaven+"/maven-metadata.xml", what, maxMetadata)
	if err != nil {
		return forgeBuilds{}, err
	}
	var m struct {
		Versions []string `xml:"versioning>versions>version"`
	}
	if err := xml.Unmarshal(b, &m); err != nil {
		return forgeBuilds{}, u.malformed(what, err)
	}
	var p struct {
		Promos map[string]string `json:"promos"`
	}
	f := forgeFilesUpstream(hc)
	if err := f.getJSON(ctx, forgePromotions, "its list of recommended builds", &p); err != nil {
		return forgeBuilds{}, err
	}
	fb := forgeBuilds{byMC: map[string][]string{}, recommended: map[string]string{}}
	for _, v := range m.Versions {
		mc, build, ok := strings.Cut(v, "-")
		if ok && offered(mc) && reForgeVersion.MatchString(build) && !slices.Contains(fb.byMC[mc], build) {
			fb.byMC[mc] = append(fb.byMC[mc], build)
		}
	}
	for _, vs := range fb.byMC {
		slices.SortFunc(vs, func(a, b string) int { return compareVersions(b, a) })
	}
	for k, v := range p.Promos {
		if mc, ok := strings.CutSuffix(k, "-recommended"); ok && slices.Contains(fb.byMC[mc], v) {
			fb.recommended[mc] = v
		}
	}
	return fb, nil
}

func forgeCatalog(ctx context.Context, hc *http.Client) ([]Release, error) {
	man, err := mojangManifest(ctx, hc)
	if err != nil {
		return nil, err
	}
	fb, err := forgeVersions(ctx, hc)
	if err != nil {
		return nil, err
	}
	mcs := make([]string, 0, len(fb.byMC))
	for mc := range fb.byMC {
		mcs = append(mcs, mc)
	}
	return offer(ctx, hc, man, Forge, mcs, func(mc string) (choice, bool, error) {
		vs := fb.byMC[mc]
		if len(vs) == 0 {
			return choice{}, false, nil
		}
		channel := fb.channel(mc)
		pick := vs[0]
		if i := slices.IndexFunc(vs, func(v string) bool { return channel(v) == Stable }); i >= 0 {
			pick = vs[i]
		}
		return choice{pin: Pin{Type: Forge, MinecraftVersion: mc, ForgeVersion: pick}, channel: channel(pick)}, true, nil
	})
}

func forgeVersionList(ctx context.Context, hc *http.Client, mc string) ([]Build, error) {
	fb, err := forgeVersions(ctx, hc)
	if err != nil {
		return nil, err
	}
	if len(fb.byMC[mc]) == 0 {
		return nil, noBuilds(Forge, mc)
	}
	return builds(Pin{Type: Forge, MinecraftVersion: mc}, fb.byMC[mc], fb.channel(mc),
		func(p *Pin, v string) { p.ForgeVersion = v }), nil
}

// forgeBuild is a build that Forge's installer makes: one of Forge's, or one
// of NeoForge's versions for Minecraft 1.20.1, which kept Forge's installer
// and its names under the group net.neoforged.
type forgeBuild struct {
	group, mc, version string
}

// forgeBuildOf is the build a Forge pin names, or a NeoForge pin for
// Minecraft 1.20.1.
func forgeBuildOf(p Pin) (forgeBuild, bool) {
	switch {
	case p.Type == Forge:
		return forgeBuild{group: "net.minecraftforge", mc: p.MinecraftVersion, version: p.ForgeVersion}, true
	case p.Type == NeoForge && reNeoForgeForge.MatchString(p.NeoForgeVersion):
		return forgeBuild{group: "net.neoforged", mc: p.MinecraftVersion, version: p.NeoForgeVersion}, true
	}
	return forgeBuild{}, false
}

func (b forgeBuild) name() string {
	if b.group == "net.neoforged" {
		return "NeoForge"
	}
	return "Forge"
}

// id is how Maven and the installer name the build: "26.2-65.1.3".
func (b forgeBuild) id() string { return b.mc + "-" + b.version }

// coord is the Maven coordinate of one of the build's files.
func (b forgeBuild) coord(classifier string) string {
	return b.group + ":forge:" + b.id() + ":" + classifier
}

// shim reports whether the installer has the server start from a shim jar,
// as Forge's do from Minecraft 1.20.3 (Forge 49) on. The earlier ones, and
// NeoForge's for 1.20.1, have the launch arguments start FML's bootstrap
// launcher from the libraries instead, and look for Mojang's server jar
// under its plain name.
func (b forgeBuild) shim() bool {
	return b.group == "net.minecraftforge" && minecraft.CompareMinecraft(b.mc, "1.20.3") >= 0
}

// serverJarPath is where the installer looks for Mojang's server jar.
func (b forgeBuild) serverJarPath() string {
	name := "server-" + b.mc
	if b.shim() {
		name += "-bundled"
	}
	return "libraries/net/minecraft/server/" + b.mc + "/" + name + ".jar"
}

func (b forgeBuild) argsPath() string {
	return "libraries/" + strings.ReplaceAll(b.group, ".", "/") + "/forge/" + b.id() + "/unix_args.txt"
}

func forgeResolve(ctx context.Context, hc *http.Client, r *Resolved) error {
	u := forgeMavenUpstream(hc)
	b, _ := forgeBuildOf(r.Pin)
	id := b.id()
	name := "forge-" + id + "-installer.jar"
	fileURL := forgeMaven + "/" + id + "/" + name
	h, err := u.checksum(ctx, fileURL, SHA512, name)
	if isNotFound(err) {
		return &Error{Kind: KindNotFound, Msg: fmt.Sprintf("Forge has no version %s for Minecraft %s.", r.Pin.ForgeVersion, r.Pin.MinecraftVersion),
			Hint: "Choose another version.", Params: u.params("maven.minecraftforge.net", "forgeVersion", r.Pin.ForgeVersion)}
	}
	if err != nil {
		return err
	}
	r.Software = &Artifact{URL: fileURL, Path: name, Hash: h, Source: "Forge's Maven repository", Upstream: u.name, Hosts: u.hosts}
	return nil
}

// forgePlan runs Forge's verified installer in a setup-only container, with
// Mojang's verified server jar already where the installer looks for it.
// The installer downloads the libraries its profile lists and checks each
// against the SHA-1 there, then builds the patched Minecraft jar and checks
// it against the SHA-1 its profile publishes. Finish checks all of them
// again, together with Minecraft's own libraries and the shim jar the
// server starts from.
func forgePlan(r Resolved) (Plan, error) {
	if r.Software == nil {
		return Plan{}, incomplete(r.Pin)
	}
	b, _ := forgeBuildOf(r.Pin)
	installer, server := *r.Software, r.Server
	server.Path = b.serverJarPath()
	return Plan{
		Downloads: []Artifact{installer, server},
		Setup: &Container{Env: []string{"TYPE=FORGE", "FORGE_INSTALLER=/data/" + installer.Path,
			"FORGE_FORCE_REINSTALL=TRUE", "SETUP_ONLY=TRUE"}},
		Derive: []Derivation{
			{Kind: DeriveForge, From: installer.Path, Into: "libraries"},
			{Kind: DeriveBundler, From: server.Path, Into: "libraries"},
		},
		Remove: []string{installer.Path, installer.Path + ".log"},
		Run:    Container{Env: []string{"TYPE=CUSTOM", "CUSTOM_JAR_EXEC=@" + b.argsPath()}},
	}, nil
}

type forgeLibrary struct {
	Name      string `json:"name"`
	Downloads struct {
		Artifact struct {
			Path string `json:"path"`
			SHA1 string `json:"sha1"`
			Size int64  `json:"size"`
			URL  string `json:"url"`
		} `json:"artifact"`
	} `json:"downloads"`
}

type forgeProfile struct {
	Version       string `json:"version"`
	Minecraft     string `json:"minecraft"`
	Path          string `json:"path"`
	ServerJarPath string `json:"serverJarPath"`
	Data          map[string]struct {
		Server string `json:"server"`
	} `json:"data"`
	Processors []struct {
		Sides []string `json:"sides"`
		Args  []string `json:"args"`
	} `json:"processors"`
	Libraries []forgeLibrary `json:"libraries"`
}

type forgeVersionFile struct {
	ID           string         `json:"id"`
	InheritsFrom string         `json:"inheritsFrom"`
	Libraries    []forgeLibrary `json:"libraries"`
}

// source names a list inside the build's verified installer as the source
// of a hash: "the library list inside the verified Forge installer".
func (b forgeBuild) source(list string) string {
	return list + " inside the verified " + b.name() + " installer"
}

// forgeInstallerChecks reads the verified installer: every library it
// installs under into with its SHA-1 and size, the shim jar the server
// starts from when it has one, the launch arguments it extracts, and the
// SHA-1 it publishes for each file its processors build, the patched
// Minecraft jar among them.
func forgeInstallerChecks(root *os.Root, installer, into string, b forgeBuild) ([]Check, error) {
	readJSON := func(name string, out any) error {
		data, err := readZipEntry(root, installer, name, 4<<20)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, out); err != nil {
			return jarError(installer, "its "+name+" is not valid JSON", err)
		}
		return nil
	}
	var prof forgeProfile
	if err := readJSON("install_profile.json", &prof); err != nil {
		return nil, err
	}
	var ver forgeVersionFile
	if err := readJSON("version.json", &ver); err != nil {
		return nil, err
	}
	name := b.mc + "-forge-" + b.version
	if prof.Version != name || prof.Minecraft != b.mc || ver.ID != name || ver.InheritsFrom != b.mc {
		return nil, jarError(installer, fmt.Sprintf("it installs %q for Minecraft %q, not %s %s for Minecraft %s", prof.Version, prof.Minecraft, b.name(), b.version, b.mc), nil)
	}
	if strings.NewReplacer("{LIBRARY_DIR}", "libraries", "{MINECRAFT_VERSION}", b.mc).Replace(prof.ServerJarPath) != b.serverJarPath() {
		return nil, jarError(installer, "it expects Mojang's server jar somewhere other than "+b.serverJarPath(), nil)
	}
	libs := b.source("the library list")
	// The client's patched jar is listed for launchers; a server install
	// never has it.
	client := b.coord("client")
	shimCoord := b.coord("shim")
	var checks []Check
	var shim *Check
	for _, l := range append(prof.Libraries, ver.Libraries...) {
		a := l.Downloads.Artifact
		if l.Name == client && a.URL == "" {
			continue
		}
		h, ok := parseHash(SHA1, a.SHA1)
		if !ok || a.Size <= 0 || !cleanRel(a.Path, ".jar", ".zip") {
			return nil, jarError(installer, fmt.Sprintf("its library %q has no valid path, SHA-1 or size", l.Name), nil)
		}
		c := Check{Path: into + "/" + a.Path, Hash: h, Size: a.Size, Origin: Derived, Source: libs}
		checks = append(checks, c)
		if l.Name == shimCoord && shim == nil {
			shim = &c
		}
	}
	shimName := ""
	if b.shim() {
		shimRel, ok := mavenPath(shimCoord)
		if prof.Path != shimCoord || !ok || shim == nil || shim.Path != into+"/"+shimRel {
			return nil, jarError(installer, "it does not start the server from its shim jar, "+path.Base(shimRel), nil)
		}
		shimName = path.Base(shimRel)
		checks = append(checks, Check{Path: shimName, Hash: shim.Hash, Size: shim.Size, Origin: Derived, Source: libs})
	}

	if !extractsForgeArgs(prof, b.argsPath()) {
		return nil, jarError(installer, "it does not put its launch arguments at "+b.argsPath(), nil)
	}
	args, err := readZipEntry(root, installer, "data/unix_args.txt", 1<<20)
	if err != nil {
		return nil, err
	}
	switch {
	case shimName != "" && !startsJar(string(args), shimName):
		return nil, jarError(installer, "its launch arguments do not start "+shimName, nil)
	case shimName == "" && (prof.Path != "" || slices.Contains(strings.Fields(string(args)), "-jar")):
		return nil, jarError(installer, "it starts the server from a jar of its own, not from the libraries it installs", nil)
	}
	sum := sha256.Sum256(args)
	checks = append(checks, Check{Path: b.argsPath(), Hash: Hash{Algorithm: SHA256, Value: hex.EncodeToString(sum[:])},
		Size: int64(len(args)), Origin: Derived, Source: b.source("the launch arguments")})

	outputs, err := forgeOutputs(installer, into, prof, b.source("the install profile"))
	if err != nil {
		return nil, err
	}
	return append(checks, outputs...), nil
}

// forgeOutputs is the files the installer's processors build, each with the
// SHA-1 the install profile publishes for it (PATCHED and PATCHED_SHA).
// The patched Minecraft jar must be one of them.
func forgeOutputs(installer, into string, prof forgeProfile, source string) ([]Check, error) {
	keys := make([]string, 0, len(prof.Data))
	for k := range prof.Data {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var out []Check
	patched := false
	for _, k := range keys {
		sha, published := prof.Data[k+"_SHA"]
		if !published {
			continue
		}
		s := prof.Data[k].Server
		if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
			return nil, jarError(installer, fmt.Sprintf("its install profile names an invalid file for %s", k), nil)
		}
		p, ok := mavenPath(s[1 : len(s)-1])
		h, hashOK := parseHash(SHA1, strings.Trim(sha.Server, "'"))
		if !ok || !hashOK {
			return nil, jarError(installer, fmt.Sprintf("its install profile names an invalid file or SHA-1 for %s", k), nil)
		}
		out = append(out, Check{Path: into + "/" + p, Hash: h, Origin: Derived, Source: source})
		patched = patched || k == "PATCHED"
	}
	if !patched {
		return nil, jarError(installer, "its install profile publishes no SHA-1 for the patched Minecraft jar", nil)
	}
	return out, nil
}

// extractsForgeArgs reports whether a server-side processor extracts the
// installer's data/unix_args.txt to where the server container reads it.
func extractsForgeArgs(prof forgeProfile, rel string) bool {
	want := "{ROOT}/" + rel
	for _, p := range prof.Processors {
		if len(p.Sides) > 0 && !slices.Contains(p.Sides, "server") {
			continue
		}
		for i := 0; i+3 < len(p.Args); i++ {
			if p.Args[i] == "--from" && p.Args[i+1] == "data/unix_args.txt" && p.Args[i+2] == "--to" && p.Args[i+3] == want {
				return true
			}
		}
	}
	return false
}

// startsJar reports whether Java launch arguments start exactly one jar, and
// that it is jar.
func startsJar(args, jar string) bool {
	f := strings.Fields(args)
	n := 0
	for i, a := range f {
		if a == "-jar" {
			n++
			if i+1 >= len(f) || f[i+1] != jar {
				return false
			}
		}
	}
	return n == 1
}
