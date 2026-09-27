package software

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Plan is how to install and run one pin on the pinned itzg image. The agent
// carries it out in order:
//
//  1. Download every artifact in Downloads.
//  2. Prepare, which writes the Launcher when there is one.
//  3. When Setup is set, run a setup-only container with Setup.Env.
//  4. Finish, which verifies the install and returns the Manifest to keep.
//  5. Run the server container with Run.Env, checking the Manifest before
//     every start.
//
// Container env holds only what depends on the server type. The agent adds
// the env it sets for every server: EULA, VERSION, memory, RCON and so on.
type Plan struct {
	Pin       Pin        `json:"pin"`
	Assurance Assurance  `json:"assurance"`
	Downloads []Artifact `json:"downloads"`
	Launcher  *Launcher  `json:"launcher,omitempty"`
	Setup     *Container `json:"setup,omitempty"`
	// Checks are the downloads with the hashes their upstreams publish.
	// Finish adds the checks it can only work out after the install.
	Checks []Check      `json:"checks"`
	Derive []Derivation `json:"derive,omitempty"`
	Record []string     `json:"record,omitempty"`
	Remove []string     `json:"remove,omitempty"`
	Run    Container    `json:"run"`
}

// Container is the part of a container's env that depends on the server
// type.
type Container struct {
	Env []string `json:"env"`
}

// Launcher is a jar with no code in it, written into the data directory the
// way the official Fabric and Quilt installers write it: a manifest that
// starts MainClass, or the Main-Class in the manifest of the jar at
// MainClassFrom, with ClassPath, and optionally one properties file.
type Launcher struct {
	Path           string   `json:"path"`
	MainClass      string   `json:"mainClass,omitempty"`
	MainClassFrom  string   `json:"mainClassFrom,omitempty"`
	ClassPath      []string `json:"classPath"`
	Properties     string   `json:"properties,omitempty"`
	PropertiesBody string   `json:"propertiesBody,omitempty"`
}

// DeriveKind names a list of hashes inside a file that is verified first.
type DeriveKind string

const (
	// DeriveBundler reads META-INF/libraries.list in Mojang's server jar:
	// the SHA-256 of every library it bundles.
	DeriveBundler DeriveKind = "mojang_bundler"
	// DerivePaperclip reads META-INF/download-context in a Paperclip jar
	// such as Purpur's: the SHA-256 of the Mojang server jar it patches.
	DerivePaperclip DeriveKind = "paperclip"
	// DeriveNeoForge reads the install profile, version file and launch
	// arguments inside NeoForge's installer.
	DeriveNeoForge DeriveKind = "neoforge_installer"
	// DeriveForge reads the same inside Forge's installer, which also
	// publishes the hashes of the files it builds.
	DeriveForge DeriveKind = "forge_installer"
)

// Derivation says where more hashes are once the download From is verified.
// The files they cover are under Into.
type Derivation struct {
	Kind DeriveKind `json:"kind"`
	From string     `json:"from"`
	Into string     `json:"into"`
}

// Manifest is what an install leaves behind: every file the server's
// software needs with the hash it must have, and the server container's env.
// The server container can write to the data directory, so the agent keeps
// the manifest elsewhere and checks it before every start.
type Manifest struct {
	Pin       Pin       `json:"pin"`
	Assurance Assurance `json:"assurance"`
	Checks    []Check   `json:"checks"`
	Run       Container `json:"run"`
}

// Verify checks the manifest, then every file in it.
func (m Manifest) Verify(dataDir string) error {
	if err := m.validate(); err != nil {
		return err
	}
	return Verify(dataDir, m.Checks)
}

// validate checks that the server container starts a file the manifest
// checks, and nothing else.
func (m Manifest) validate() error {
	if err := m.Pin.Validate(); err != nil {
		return err
	}
	if len(m.Checks) == 0 {
		return &Error{Kind: KindMissingFile, Msg: "Playkeeper has no record of this server's software files, so it cannot check them.",
			Hint: "Reinstall the server software.", Params: map[string]string{"type": m.Pin.Type}}
	}
	if f, ok := runFile(m.Run.Env); !ok || !slices.ContainsFunc(m.Checks, func(c Check) bool { return c.Path == f }) {
		return &Error{Kind: KindMalformed, Msg: "Playkeeper's record of this server's software is not valid: the server container would not start a checked file.",
			Hint: "Reinstall the server software.", Params: map[string]string{"type": m.Pin.Type}}
	}
	return nil
}

func incomplete(pin Pin) error {
	return &Error{Kind: KindMalformed, Msg: fmt.Sprintf("Playkeeper has not looked up everything a %s server needs.", typeName(pin.Type)),
		Hint: "Choose the version again.", Params: map[string]string{"type": pin.Type}}
}

var reEnv = regexp.MustCompile(`^[A-Z][A-Z0-9_]*=[^\x00\r\n]*$`)

// containerFiles returns the files in the data directory that a container
// env makes the itzg image install or start. Only the settings plans use
// are allowed, once each: several other itzg settings take a URL and would
// download and run what it names.
func containerFiles(env []string) ([]string, bool) {
	var files []string
	seen := map[string]bool{}
	for _, e := range env {
		if !reEnv.MatchString(e) || len(e) > 1024 {
			return nil, false
		}
		k, v, _ := strings.Cut(e, "=")
		if seen[k] {
			return nil, false
		}
		seen[k] = true
		var rel string
		ok := false
		switch k {
		case "TYPE":
			ok = v == "CUSTOM" || v == "NEOFORGE" || v == "FORGE"
		case "SETUP_ONLY", "NEOFORGE_FORCE_REINSTALL", "FORGE_FORCE_REINSTALL":
			ok = v == "TRUE"
		case "CUSTOM_SERVER", "NEOFORGE_INSTALLER", "FORGE_INSTALLER":
			rel, ok = strings.CutPrefix(v, "/data/")
			ok = ok && cleanRel(rel, ".jar")
		case "CUSTOM_JAR_EXEC":
			rel, ok = strings.CutPrefix(v, "@")
			ok = ok && cleanRel(rel, ".txt")
		}
		if !ok {
			return nil, false
		}
		if rel != "" {
			files = append(files, rel)
		}
	}
	return files, true
}

// runFile returns the one file a server container env starts, if it is an
// env a server container may have.
func runFile(env []string) (string, bool) {
	files, ok := containerFiles(env)
	if !ok || len(files) != 1 || !slices.Contains(env, "TYPE=CUSTOM") || slices.Contains(env, "SETUP_ONLY=TRUE") {
		return "", false
	}
	return files[0], true
}

// validate checks a plan before anything acts on it, since plans may be
// stored or passed between Playkeeper's parts: every path plain and inside
// the data directory, every download on its upstream's hosts with a hash,
// and a server container that runs the verified files as they are.
func (p Plan) validate() error {
	if err := p.Pin.Validate(); err != nil {
		return err
	}
	bad := func(format string, a ...any) error {
		return &Error{Kind: KindMalformed, Msg: "The install plan for this server is not valid: " + fmt.Sprintf(format, a...) + ".",
			Hint:   "Choose the version again. If it keeps happening, report it to the Playkeeper developers.",
			Params: map[string]string{"type": p.Pin.Type}}
	}
	if len(p.Downloads) == 0 {
		return bad("it downloads nothing")
	}
	downloaded := map[string]bool{}
	for _, a := range p.Downloads {
		if !cleanRel(a.Path, ".jar") {
			return unsafePath(a.Path, "downloads must be .jar files with a plain relative name")
		}
		if downloaded[a.Path] {
			return bad("it downloads %s twice", a.Path)
		}
		downloaded[a.Path] = true
		if !a.Hash.valid() || a.Size < 0 || a.Size > maxArtifact || a.Source == "" || a.Upstream == "" || len(a.Hosts) == 0 {
			return bad("%s has no valid checksum", a.Path)
		}
		if _, err := (upstream{name: a.Upstream, hosts: a.Hosts}).checkURL(a.URL, path.Base(a.Path)); err != nil {
			return err
		}
		if !slices.Contains(p.Checks, pinned(a)) {
			return bad("it does not check %s", a.Path)
		}
	}
	for _, c := range p.Checks {
		if !cleanRel(c.Path) {
			return unsafePath(c.Path, "it is not a plain relative path")
		}
		if !c.Hash.valid() {
			return bad("%s has no valid checksum", c.Path)
		}
	}
	if l := p.Launcher; l != nil {
		if !cleanRel(l.Path, ".jar") || strings.Contains(l.Path, "/") || downloaded[l.Path] {
			return unsafePath(l.Path, "the launch jar must be a new .jar file at the top of the server's folder")
		}
		if (l.MainClass == "") == (l.MainClassFrom == "") || (l.MainClass != "" && !reJavaClass.MatchString(l.MainClass)) ||
			(l.MainClassFrom != "" && !downloaded[l.MainClassFrom]) {
			return bad("the launch jar has no valid main class")
		}
		if len(l.ClassPath) == 0 {
			return bad("the launch jar has an empty class path")
		}
		for _, cp := range l.ClassPath {
			if !downloaded[cp] {
				return bad("the launch jar's class path names %s, which is not downloaded", cp)
			}
		}
		if l.Properties != "" && (!cleanRel(l.Properties, ".properties") || strings.Contains(l.Properties, "/")) {
			return unsafePath(l.Properties, "it is not a plain .properties name")
		}
		if (l.Properties == "" && l.PropertiesBody != "") || len(l.PropertiesBody) > 4<<10 || strings.ContainsRune(l.PropertiesBody, 0) {
			return bad("the launch jar's properties are not valid")
		}
	}
	if p.Setup != nil {
		files, ok := containerFiles(p.Setup.Env)
		if !ok {
			return bad("its setup container has settings Playkeeper does not allow")
		}
		if !slices.Contains(p.Setup.Env, "SETUP_ONLY=TRUE") {
			return bad("its setup container would start the server")
		}
		for _, f := range files {
			if !downloaded[f] {
				return bad("its setup container installs %s, which is not downloaded", f)
			}
		}
	}
	if _, ok := containerFiles(p.Run.Env); !ok {
		return bad("its server container has settings Playkeeper does not allow")
	}
	if _, ok := runFile(p.Run.Env); !ok {
		return bad("the server container must run the verified files as they are")
	}
	for _, d := range p.Derive {
		switch d.Kind {
		case DeriveBundler, DerivePaperclip, DeriveNeoForge, DeriveForge:
		default:
			return bad("it reads hashes in an unknown way (%q)", d.Kind)
		}
		if !downloaded[d.From] {
			return bad("it reads hashes from %s, which is not downloaded", d.From)
		}
		if !cleanRel(d.Into) {
			return unsafePath(d.Into, "it is not a plain relative path")
		}
	}
	for _, rel := range append(slices.Clone(p.Record), p.Remove...) {
		if !cleanRel(rel) {
			return unsafePath(rel, "it is not a plain relative path")
		}
	}
	return nil
}

// Prepare writes the plan's launch jar, after checking the downloads it
// names. It runs after Download and before any container; plans without a
// Launcher need nothing.
func Prepare(dataDir string, p Plan) error {
	if err := p.validate(); err != nil {
		return err
	}
	l := p.Launcher
	if l == nil {
		return nil
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	need := append(slices.Clone(l.ClassPath), l.MainClassFrom)
	var checks []Check
	for _, c := range p.Checks {
		if slices.Contains(need, c.Path) {
			checks = append(checks, c)
		}
	}
	if err := verify(root, checks); err != nil {
		return err
	}
	main := l.MainClass
	if l.MainClassFrom != "" {
		m, err := readZipEntry(root, l.MainClassFrom, "META-INF/MANIFEST.MF", 1<<20)
		if err != nil {
			return err
		}
		if main = manifestAttribute(m, "Main-Class"); !reJavaClass.MatchString(main) {
			return jarError(l.MainClassFrom, "its manifest names no valid Main-Class", nil)
		}
	}
	jar, err := launchJar(main, l.ClassPath, l.Properties, l.PropertiesBody)
	if err != nil {
		return err
	}
	return writeFile(root, l.Path, jar)
}

// Finish checks an install once every step before it has run: the downloads
// again, then the files that lists inside the verified downloads give hashes
// for. It records the hashes of files the install built that nobody
// publishes a hash for, removes what the server does not need, and returns
// the Manifest to keep. When it fails, install again from the downloads.
func Finish(dataDir string, p Plan) (Manifest, error) {
	if err := p.validate(); err != nil {
		return Manifest{}, err
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return Manifest{}, err
	}
	defer root.Close()
	if err := verify(root, p.Checks); err != nil {
		return Manifest{}, err
	}
	checks := slices.Clone(p.Checks)
	toRecord := slices.Clone(p.Record)
	for _, d := range p.Derive {
		cs, rec, err := derive(root, p, d)
		if err != nil {
			return Manifest{}, err
		}
		if err := verify(root, cs); err != nil {
			return Manifest{}, err
		}
		checks = append(checks, cs...)
		toRecord = append(toRecord, rec...)
	}
	recorded, err := record(root, toRecord)
	if err != nil {
		return Manifest{}, err
	}
	checks = append(checks, recorded...)
	var kept []Check
	for _, c := range checks {
		if !slices.Contains(p.Remove, c.Path) && !slices.Contains(kept, c) {
			kept = append(kept, c)
		}
	}
	m := Manifest{Pin: p.Pin, Assurance: p.Assurance, Checks: kept, Run: p.Run}
	if err := m.validate(); err != nil {
		return Manifest{}, err
	}
	for _, rel := range p.Remove {
		if err := removeFile(root, rel); err != nil {
			return Manifest{}, err
		}
	}
	return m, nil
}

// derive reads the hashes one derivation lists. The file they are read from
// is verified before derive runs.
func derive(root *os.Root, p Plan, d Derivation) ([]Check, []string, error) {
	switch d.Kind {
	case DeriveBundler:
		libs, err := bundlerList(root, d.From, "META-INF/libraries.list")
		if err != nil {
			return nil, nil, err
		}
		checks := make([]Check, 0, len(libs))
		for _, l := range libs {
			checks = append(checks, Check{Path: d.Into + "/" + l.path, Hash: Hash{Algorithm: SHA256, Value: l.sha256},
				Origin: Derived, Source: "the library list inside Mojang's verified server jar"})
		}
		return checks, nil, nil
	case DerivePaperclip:
		c, err := paperclipCheck(root, d.From, d.Into)
		if err != nil {
			return nil, nil, err
		}
		if !slices.ContainsFunc(p.Downloads, func(a Artifact) bool { return a.Path == c.Path }) {
			return nil, nil, jarError(d.From, "it expects Mojang's server jar at "+c.Path+", not where Playkeeper put it", nil)
		}
		return []Check{c}, nil, nil
	case DeriveNeoForge:
		return neoforgeInstallerChecks(root, d.From, d.Into, p.Pin)
	case DeriveForge:
		checks, err := forgeInstallerChecks(root, d.From, d.Into, p.Pin)
		return checks, nil, err
	}
	return nil, nil, fmt.Errorf("unknown derivation %q", d.Kind)
}

// paperclipCheck reads the SHA-256 a Paperclip jar expects of Mojang's
// server jar from its META-INF/download-context: one "sha256<TAB>url<TAB>
// file name" line.
func paperclipCheck(root *os.Root, jar, into string) (Check, error) {
	const entry = "META-INF/download-context"
	b, err := readZipEntry(root, jar, entry, 4<<10)
	if err != nil {
		return Check{}, err
	}
	f := strings.Split(strings.TrimSpace(string(b)), "\t")
	if len(f) != 3 {
		return Check{}, jarError(jar, "its "+entry+" does not have three tab-separated fields", nil)
	}
	h, ok := parseHash(SHA256, f[0])
	if !ok || strings.Contains(f[2], "/") || !cleanRel(f[2], ".jar") {
		return Check{}, jarError(jar, "its "+entry+" lists an invalid hash or file name", nil)
	}
	return Check{Path: into + "/" + f[2], Hash: h, Origin: Derived, Source: "the download list inside the verified Purpur jar"}, nil
}
