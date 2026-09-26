// Package software knows, for each Minecraft server type besides Paper,
// where its versions are listed, what to download and how to verify it, and
// how the pinned itzg image installs and runs the result.
//
// Every type goes the same way. Sources.Catalog lists the versions to offer,
// by the same rules as Paper's catalog. Sources.Resolve turns the chosen Pin
// into artifacts with the hashes their upstreams publish, and Resolved.Plan
// turns those into downloads, container env and checks without touching the
// network. Download fetches each artifact and keeps it only if it matches;
// Prepare writes the code-free launch jar Fabric and Quilt servers start
// from; a setup-only container runs NeoForge's or Forge's installer; Finish
// verifies the result and returns the Manifest that every later start
// checks.
//
// Nothing here keeps state between calls or knows where servers live: the
// data directory, the HTTP client and the pin are always inputs.
package software

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
)

// Server type IDs, as in the minecraft.Types registry. Paper keeps its own
// path: minecraft.Fill and the agent's Paper setup container.
const (
	Vanilla  = "vanilla"
	Purpur   = "purpur"
	Fabric   = "fabric"
	Quilt    = "quilt"
	NeoForge = "neoforge"
	Forge    = "forge"
)

// Supported reports whether this package installs the given server type.
func Supported(typeID string) bool { return typeName(typeID) != "" }

func typeName(typeID string) string {
	switch typeID {
	case Vanilla:
		return "Vanilla"
	case Purpur:
		return "Purpur"
	case Fabric:
		return "Fabric"
	case Quilt:
		return "Quilt"
	case NeoForge:
		return "NeoForge"
	case Forge:
		return "Forge"
	}
	return ""
}

func unsupportedType(typeID string) error {
	return &Error{Kind: KindUnsupported, Msg: fmt.Sprintf("Playkeeper does not install %q servers this way.", typeID),
		Hint: "Choose Vanilla, Purpur, Fabric, Quilt, NeoForge or Forge.", Params: map[string]string{"type": typeID}}
}

// Pin is the exact software a server runs. It is stored in the server's
// config next to the Minecraft version; only its own type's field is set.
type Pin struct {
	Type             string `json:"type"`
	MinecraftVersion string `json:"minecraftVersion"`
	PurpurBuild      int    `json:"purpurBuild,omitempty"`
	FabricLoader     string `json:"fabricLoader,omitempty"`
	QuiltLoader      string `json:"quiltLoader,omitempty"`
	NeoForgeVersion  string `json:"neoforgeVersion,omitempty"`
	ForgeVersion     string `json:"forgeVersion,omitempty"`
}

// Validate checks a pin before it goes anywhere near a URL, a path or a
// container: a supported type, a Minecraft release from 1.21 on, and a
// well-formed build detail for that type and no other.
func (p Pin) Validate() error {
	if !Supported(p.Type) {
		return unsupportedType(p.Type)
	}
	if !offeredFamily(p.MinecraftVersion) {
		return &Error{Kind: KindUnsupported, Msg: fmt.Sprintf("Playkeeper runs Minecraft releases from 1.21 on, not %s.", strconv.Quote(p.MinecraftVersion)),
			Hint: "Choose the version again.", Params: map[string]string{"type": p.Type, "minecraftVersion": p.MinecraftVersion}}
	}
	bad := func(value, msg string) error {
		return &Error{Kind: KindUnsupported, Msg: strconv.Quote(value) + " " + msg + ".", Hint: "Choose the version again.",
			Params: map[string]string{"type": p.Type, "minecraftVersion": p.MinecraftVersion, "value": value}}
	}
	extra := Pin{Type: p.Type, MinecraftVersion: p.MinecraftVersion}
	switch p.Type {
	case Vanilla:
	case Purpur:
		if p.PurpurBuild <= 0 {
			return bad(strconv.Itoa(p.PurpurBuild), "is not a Purpur build number")
		}
		extra.PurpurBuild = p.PurpurBuild
	case Fabric:
		if !fabricLoaderOK(p.FabricLoader) {
			return bad(p.FabricLoader, "is not a Fabric loader version Playkeeper can run (it runs releases newer than "+fabricNewestShaded+")")
		}
		extra.FabricLoader = p.FabricLoader
	case Quilt:
		if !reQuiltLoader.MatchString(p.QuiltLoader) {
			return bad(p.QuiltLoader, "is not a Quilt loader version")
		}
		extra.QuiltLoader = p.QuiltLoader
	case NeoForge:
		if mc, _, ok := neoforgeMinecraft(p.NeoForgeVersion); !ok || mc != p.MinecraftVersion {
			return bad(p.NeoForgeVersion, "is not a NeoForge version for Minecraft "+p.MinecraftVersion)
		}
		extra.NeoForgeVersion = p.NeoForgeVersion
	case Forge:
		if !reForgeVersion.MatchString(p.ForgeVersion) {
			return bad(p.ForgeVersion, "is not a Forge version")
		}
		extra.ForgeVersion = p.ForgeVersion
	}
	if extra != p {
		return &Error{Kind: KindUnsupported, Msg: fmt.Sprintf("The version chosen for this %s server carries build details of another server type.", typeName(p.Type)),
			Hint: "Choose the version again.", Params: map[string]string{"type": p.Type}}
	}
	return nil
}

// Level says how far a type's software is verified against hashes its
// upstream publishes.
type Level string

const (
	// LevelFull: every file the server runs is checked against a hash its
	// upstream publishes, before it is used.
	LevelFull Level = "full"
	// LevelWeakHash: every file is checked, but the type's own jar only has
	// an MD5, published next to the jar.
	LevelWeakHash Level = "weak_hash"
	// LevelRecorded: the downloads are checked, but the installer builds
	// files on this host that nobody publishes a hash for. Playkeeper
	// records their hashes right after the verified install.
	LevelRecorded Level = "recorded_outputs"
)

// Assurance describes, for the dashboard and for people reviewing
// Playkeeper, how a type's software is verified.
type Assurance struct {
	Level      Level       `json:"level"`
	Algorithms []Algorithm `json:"algorithms"`
	Summary    string      `json:"summary"`
}

// AssuranceOf describes how a server type's software is verified.
func AssuranceOf(typeID string) (Assurance, bool) {
	switch typeID {
	case Vanilla:
		return Assurance{Level: LevelFull, Algorithms: []Algorithm{SHA1},
			Summary: "Mojang's server jar is checked against the SHA-1 in Mojang's version manifest before it is used. SHA-1 is the only hash Mojang publishes."}, true
	case Purpur:
		return Assurance{Level: LevelWeakHash, Algorithms: []Algorithm{SHA256, SHA1, MD5},
			Summary: "Purpur publishes only an MD5 checksum, from the same server as the jar. Playkeeper checks it, and downloads Mojang's server jar itself, checked against Mojang's SHA-1 and the SHA-256 the Purpur jar expects. MD5 catches corrupted downloads but cannot rule out a jar forged on purpose."}, true
	case Fabric:
		return Assurance{Level: LevelFull, Algorithms: []Algorithm{SHA512, SHA1},
			Summary: "Every library is checked against a SHA-512 from Fabric (its metadata API, or its Maven repository for the loader and mappings) and Mojang's server jar against Mojang's SHA-1, before anything runs. Playkeeper writes the launch jar itself; it contains no code."}, true
	case Quilt:
		return Assurance{Level: LevelFull, Algorithms: []Algorithm{SHA512, SHA1},
			Summary: "Every library is checked against the SHA-512 file next to it in Quilt's or Fabric's Maven repository, and Mojang's server jar against Mojang's SHA-1, before anything runs. Quilt's metadata API lists hashes too, but they do not match the files Quilt serves, so Playkeeper does not use them. Playkeeper writes the launch jar itself; it contains no code."}, true
	case NeoForge:
		return Assurance{Level: LevelRecorded, Algorithms: []Algorithm{SHA512, SHA256, SHA1},
			Summary: "The installer is checked against the SHA-512 in NeoForge's Maven repository, every library it installs against the SHA-1 listed inside the verified installer, and Minecraft's own libraries against the SHA-256 list inside Mojang's verified server jar. The installer then builds the patched Minecraft jar on this host. Nobody publishes a hash for that jar, so Playkeeper records its hash right after the verified install and checks it before every start."}, true
	case Forge:
		return Assurance{Level: LevelFull, Algorithms: []Algorithm{SHA512, SHA256, SHA1},
			Summary: "The installer is checked against the SHA-512 in Forge's Maven repository, every library it installs and the shim jar the server starts from against the SHA-1 listed inside the verified installer, and Minecraft's own libraries against the SHA-256 list inside Mojang's verified server jar. The installer then builds the patched Minecraft jar on this host, which must match the SHA-1 Forge publishes inside the installer, before its first start and every start after."}, true
	}
	return Assurance{}, false
}

// Sources reads the upstreams through one HTTP client and keeps nothing
// between calls, so one Sources serves every server.
type Sources struct {
	Client *http.Client
}

// Catalog lists the versions to offer for a server type, newest first.
func (s Sources) Catalog(ctx context.Context, typeID string) ([]Release, error) {
	switch typeID {
	case Vanilla:
		return vanillaCatalog(ctx, s.Client)
	case Purpur:
		return purpurCatalog(ctx, s.Client)
	case Fabric:
		return fabricCatalog(ctx, s.Client)
	case Quilt:
		return quiltCatalog(ctx, s.Client)
	case NeoForge:
		return neoforgeCatalog(ctx, s.Client)
	case Forge:
		return forgeCatalog(ctx, s.Client)
	}
	return nil, unsupportedType(typeID)
}

// Builds lists the builds of a type's software for one Minecraft version,
// newest first, with the newest stable one recommended: Purpur builds,
// Fabric or Quilt loaders, or NeoForge or Forge versions. Vanilla has none.
func (s Sources) Builds(ctx context.Context, typeID, mc string) ([]Build, error) {
	if !offeredFamily(mc) {
		return nil, (Pin{Type: typeID, MinecraftVersion: mc}).Validate()
	}
	switch typeID {
	case Vanilla:
		return []Build{}, nil
	case Purpur:
		return purpurBuildList(ctx, s.Client, mc)
	case Fabric:
		return fabricLoaderList(ctx, s.Client, mc)
	case Quilt:
		return quiltLoaderList(ctx, s.Client, mc)
	case NeoForge:
		return neoforgeVersionList(ctx, s.Client, mc)
	case Forge:
		return forgeVersionList(ctx, s.Client, mc)
	}
	return nil, unsupportedType(typeID)
}

// Resolved is what the upstreams publish for one pin: every artifact with
// its hash, and the few facts a plan needs. Plan turns it into downloads,
// container env and checks.
type Resolved struct {
	Pin Pin `json:"pin"`
	// Java is the Java version Mojang says the Minecraft version needs.
	Java int `json:"java"`
	// Server is Mojang's server jar for the Minecraft version.
	Server Artifact `json:"server"`
	// Software is the type's own download: the Purpur jar, or the NeoForge
	// or Forge installer.
	Software *Artifact `json:"software,omitempty"`
	// Libraries are Fabric's or Quilt's libraries in class path order, and
	// Loader is the loader jar's path among them.
	Libraries []Artifact `json:"libraries,omitempty"`
	Loader    string     `json:"loader,omitempty"`
	// LaunchMainClass is the class Fabric's launcher starts;
	// LauncherMainClass is Quilt's launcher class.
	LaunchMainClass   string `json:"launchMainClass,omitempty"`
	LauncherMainClass string `json:"launcherMainClass,omitempty"`
}

// Resolve looks up everything needed to install a pin, with the hash every
// file must have.
func (s Sources) Resolve(ctx context.Context, pin Pin) (Resolved, error) {
	if err := pin.Validate(); err != nil {
		return Resolved{}, err
	}
	mv, err := mojangRelease(ctx, s.Client, pin.MinecraftVersion)
	if err != nil {
		return Resolved{}, err
	}
	r := Resolved{Pin: pin, Java: mv.Java, Server: mv.Server}
	switch pin.Type {
	case Vanilla:
	case Purpur:
		err = purpurResolve(ctx, s.Client, &r)
	case Fabric:
		err = fabricResolve(ctx, s.Client, &r)
	case Quilt:
		err = quiltResolve(ctx, s.Client, &r)
	case NeoForge:
		err = neoforgeResolve(ctx, s.Client, &r)
	case Forge:
		err = forgeResolve(ctx, s.Client, &r)
	}
	if err != nil {
		return Resolved{}, err
	}
	return r, nil
}

// Plan turns what the upstreams publish into the install: what Playkeeper
// downloads, the launch jar it writes, the setup-only container, the checks
// and the server container's env. It does no I/O.
func (r Resolved) Plan() (Plan, error) {
	if err := r.Pin.Validate(); err != nil {
		return Plan{}, err
	}
	var p Plan
	var err error
	switch r.Pin.Type {
	case Vanilla:
		p, err = vanillaPlan(r)
	case Purpur:
		p, err = purpurPlan(r)
	case Fabric:
		p, err = fabricPlan(r)
	case Quilt:
		p, err = quiltPlan(r)
	case NeoForge:
		p, err = neoforgePlan(r)
	case Forge:
		p, err = forgePlan(r)
	}
	if err != nil {
		return Plan{}, err
	}
	p.Pin = r.Pin
	p.Assurance, _ = AssuranceOf(r.Pin.Type)
	for _, a := range p.Downloads {
		p.Checks = append(p.Checks, pinned(a))
	}
	if err := p.validate(); err != nil {
		return Plan{}, err
	}
	return p, nil
}
