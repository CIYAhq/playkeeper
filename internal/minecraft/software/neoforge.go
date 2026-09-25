package software

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const neoforgeMaven = "https://maven.neoforged.net/releases/net/neoforged/neoforge"

// NeoForge versions name their Minecraft version: 21.1.251 is for 1.21.1
// and 21.0.167 for 1.21; from Minecraft 26.1 on, 26.2.0.88 is for 26.2 and
// 26.1.2.109 for 26.1.2.
var (
	reNeoForgeOld = regexp.MustCompile(`^(2[01])\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-beta)?$`)
	reNeoForgeNew = regexp.MustCompile(`^([1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-beta)?$`)
)

func neoforgeUpstream(hc *http.Client) upstream {
	return upstream{name: "NeoForge", hosts: []string{"maven.neoforged.net"}, hc: hc}
}

// neoforgeMinecraft returns the Minecraft version a NeoForge version is
// for, and whether it is a beta.
func neoforgeMinecraft(v string) (mc string, beta, ok bool) {
	if len(v) > 32 {
		return "", false, false
	}
	if m := reNeoForgeOld.FindStringSubmatch(v); m != nil {
		mc = "1." + m[1]
		if m[2] != "0" {
			mc += "." + m[2]
		}
		return mc, m[4] != "", true
	}
	if m := reNeoForgeNew.FindStringSubmatch(v); m != nil {
		if major, _ := strconv.Atoi(m[1]); major < 26 {
			return "", false, false
		}
		mc = m[1] + "." + m[2]
		if m[3] != "0" {
			mc += "." + m[3]
		}
		return mc, m[5] != "", true
	}
	return "", false, false
}

func neoforgeChannel(v string) Channel {
	if _, beta, _ := neoforgeMinecraft(v); beta {
		return Beta
	}
	return Stable
}

// neoforgeVersions lists NeoForge versions by Minecraft version, newest
// first.
func neoforgeVersions(ctx context.Context, hc *http.Client) (map[string][]string, error) {
	u := neoforgeUpstream(hc)
	const what = "its version list"
	b, err := u.read(ctx, neoforgeMaven+"/maven-metadata.xml", what, maxMetadata)
	if err != nil {
		return nil, err
	}
	var m struct {
		Versions []string `xml:"versioning>versions>version"`
	}
	if err := xml.Unmarshal(b, &m); err != nil {
		return nil, u.malformed(what, err)
	}
	out := map[string][]string{}
	for _, v := range m.Versions {
		if mc, _, ok := neoforgeMinecraft(v); ok && offeredFamily(mc) && !slices.Contains(out[mc], v) {
			out[mc] = append(out[mc], v)
		}
	}
	for _, vs := range out {
		slices.SortFunc(vs, func(a, b string) int { return compareVersions(b, a) })
	}
	return out, nil
}

func neoforgeCatalog(ctx context.Context, hc *http.Client) ([]Release, error) {
	man, err := mojangManifest(ctx, hc)
	if err != nil {
		return nil, err
	}
	byMC, err := neoforgeVersions(ctx, hc)
	if err != nil {
		return nil, err
	}
	mcs := make([]string, 0, len(byMC))
	for mc := range byMC {
		mcs = append(mcs, mc)
	}
	return offer(ctx, hc, man, NeoForge, mcs, func(mc string) (choice, bool, error) {
		vs := byMC[mc]
		if len(vs) == 0 {
			return choice{}, false, nil
		}
		pick := vs[0]
		if i := slices.IndexFunc(vs, func(v string) bool { return neoforgeChannel(v) == Stable }); i >= 0 {
			pick = vs[i]
		}
		return choice{pin: Pin{Type: NeoForge, MinecraftVersion: mc, NeoForgeVersion: pick}, channel: neoforgeChannel(pick)}, true, nil
	})
}

func neoforgeVersionList(ctx context.Context, hc *http.Client, mc string) ([]Build, error) {
	byMC, err := neoforgeVersions(ctx, hc)
	if err != nil {
		return nil, err
	}
	if len(byMC[mc]) == 0 {
		return nil, noBuilds(NeoForge, mc)
	}
	return builds(Pin{Type: NeoForge, MinecraftVersion: mc}, byMC[mc], neoforgeChannel,
		func(p *Pin, v string) { p.NeoForgeVersion = v }), nil
}

func neoforgeResolve(ctx context.Context, hc *http.Client, r *Resolved) error {
	u := neoforgeUpstream(hc)
	v := r.Pin.NeoForgeVersion
	name := "neoforge-" + v + "-installer.jar"
	fileURL := neoforgeMaven + "/" + v + "/" + name
	h, err := u.checksum(ctx, fileURL, SHA512, name)
	if isNotFound(err) {
		return &Error{Kind: KindNotFound, Msg: fmt.Sprintf("NeoForge has no version %s.", v),
			Hint: "Choose another version.", Params: u.params("maven.neoforged.net", "neoforgeVersion", v)}
	}
	if err != nil {
		return err
	}
	r.Software = &Artifact{URL: fileURL, Path: name, Hash: h, Source: "NeoForge's Maven repository", Upstream: u.name, Hosts: u.hosts}
	return nil
}

// neoforgePlan runs NeoForge's verified installer in a setup-only container,
// with Mojang's verified server jar already where the installer looks for
// it. The installer downloads the libraries its profile lists and checks
// each against the SHA-1 there; Finish checks them again, together with
// Minecraft's own libraries, and records the patched Minecraft jar the
// installer builds, which has no published hash.
func neoforgePlan(r Resolved) (Plan, error) {
	if r.Software == nil {
		return Plan{}, incomplete(r.Pin)
	}
	mc, v := r.Pin.MinecraftVersion, r.Pin.NeoForgeVersion
	installer, server := *r.Software, r.Server
	server.Path = "libraries/net/minecraft/server/" + mc + "/server-" + mc + ".jar"
	return Plan{
		Downloads: []Artifact{installer, server},
		Setup: &Container{Env: []string{"TYPE=NEOFORGE", "NEOFORGE_INSTALLER=/data/" + installer.Path,
			"NEOFORGE_FORCE_REINSTALL=TRUE", "SETUP_ONLY=TRUE"}},
		Derive: []Derivation{
			{Kind: DeriveNeoForge, From: installer.Path, Into: "libraries"},
			{Kind: DeriveBundler, From: server.Path, Into: "libraries"},
		},
		Remove: []string{installer.Path, installer.Path + ".log"},
		Run:    Container{Env: []string{"TYPE=CUSTOM", "CUSTOM_JAR_EXEC=@" + neoforgeArgsPath(v)}},
	}, nil
}

func neoforgeArgsPath(v string) string {
	return "libraries/net/neoforged/neoforge/" + v + "/unix_args.txt"
}

type neoforgeLibrary struct {
	Name      string `json:"name"`
	Downloads struct {
		Artifact struct {
			Path string `json:"path"`
			SHA1 string `json:"sha1"`
			Size int64  `json:"size"`
		} `json:"artifact"`
	} `json:"downloads"`
}

type neoforgeProfile struct {
	Version   string `json:"version"`
	Minecraft string `json:"minecraft"`
	Data      map[string]struct {
		Server string `json:"server"`
	} `json:"data"`
	Processors []struct {
		Sides []string `json:"sides"`
		Args  []string `json:"args"`
	} `json:"processors"`
	Libraries []neoforgeLibrary `json:"libraries"`
}

type neoforgeVersionFile struct {
	ID           string            `json:"id"`
	InheritsFrom string            `json:"inheritsFrom"`
	Libraries    []neoforgeLibrary `json:"libraries"`
}

const neoforgeLibrarySource = "the library list inside the verified NeoForge installer"

// neoforgeInstallerChecks reads the verified installer: every library it
// installs under into with its SHA-1 and size, the launch arguments it
// extracts, and the files its processors build, which have no published
// hash and are returned to be recorded.
func neoforgeInstallerChecks(root *os.Root, installer, into string, pin Pin) ([]Check, []string, error) {
	v, mc := pin.NeoForgeVersion, pin.MinecraftVersion
	readJSON := func(name string, out any) error {
		b, err := readZipEntry(root, installer, name, 4<<20)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, out); err != nil {
			return jarError(installer, "its "+name+" is not valid JSON", err)
		}
		return nil
	}
	var prof neoforgeProfile
	if err := readJSON("install_profile.json", &prof); err != nil {
		return nil, nil, err
	}
	var ver neoforgeVersionFile
	if err := readJSON("version.json", &ver); err != nil {
		return nil, nil, err
	}
	id := "neoforge-" + v
	if prof.Version != id || prof.Minecraft != mc || ver.ID != id || ver.InheritsFrom != mc {
		return nil, nil, jarError(installer, fmt.Sprintf("it installs %q for Minecraft %q, not NeoForge %s for Minecraft %s", prof.Version, prof.Minecraft, v, mc), nil)
	}
	var checks []Check
	for _, l := range append(prof.Libraries, ver.Libraries...) {
		a := l.Downloads.Artifact
		h, ok := parseHash(SHA1, a.SHA1)
		if !ok || a.Size <= 0 || !cleanRel(a.Path, ".jar") {
			return nil, nil, jarError(installer, fmt.Sprintf("its library %q has no valid path, SHA-1 or size", l.Name), nil)
		}
		checks = append(checks, Check{Path: into + "/" + a.Path, Hash: h, Size: a.Size, Origin: Derived, Source: neoforgeLibrarySource})
	}
	if into+"/net/neoforged/neoforge/"+v+"/unix_args.txt" != neoforgeArgsPath(v) || !extractsArgs(prof, v) {
		return nil, nil, jarError(installer, "it does not put its launch arguments at "+neoforgeArgsPath(v), nil)
	}
	args, err := readZipEntry(root, installer, "data/unix_args.txt", 1<<20)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(args)
	checks = append(checks, Check{Path: neoforgeArgsPath(v), Hash: Hash{Algorithm: SHA256, Value: hex.EncodeToString(sum[:])},
		Size: int64(len(args)), Origin: Derived, Source: "the launch arguments inside the verified NeoForge installer"})

	keys := make([]string, 0, len(prof.Data))
	for k := range prof.Data {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var outputs []string
	patched := false
	for _, k := range keys {
		s := prof.Data[k].Server
		if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
			continue
		}
		p, ok := mavenPath(s[1 : len(s)-1])
		if !ok {
			return nil, nil, jarError(installer, fmt.Sprintf("its install profile names an invalid file for %s", k), nil)
		}
		rel := into + "/" + p
		if k == "PATCHED" {
			patched = true
		} else if present, err := regularFile(root, rel); err != nil {
			return nil, nil, err
		} else if !present {
			continue
		}
		outputs = append(outputs, rel)
	}
	if !patched {
		return nil, nil, jarError(installer, "its install profile does not name the patched Minecraft jar", nil)
	}
	return checks, outputs, nil
}

// extractsArgs reports whether a server-side processor extracts the
// installer's data/unix_args.txt to where the server container reads it.
func extractsArgs(prof neoforgeProfile, v string) bool {
	want := "{ROOT}/" + neoforgeArgsPath(v)
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

// regularFile reports whether rel exists inside the data directory as a
// regular file.
func regularFile(root *os.Root, rel string) (bool, error) {
	if err := inside(root, rel); err != nil {
		return false, err
	}
	fi, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return fi.Mode().IsRegular(), nil
}
