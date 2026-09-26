package software

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestSupportedTypes(t *testing.T) {
	for _, typeID := range allTypes {
		a, ok := AssuranceOf(typeID)
		if !Supported(typeID) || !ok || a.Summary == "" || len(a.Algorithms) == 0 {
			t.Errorf("%s: supported %v, assurance %+v", typeID, Supported(typeID), a)
		}
	}
	for _, typeID := range []string{"paper", "folia", "spigot", "", "Vanilla"} {
		if _, ok := AssuranceOf(typeID); Supported(typeID) || ok {
			t.Errorf("%q is supported", typeID)
		}
	}
}

func TestPinValidate(t *testing.T) {
	for _, p := range []Pin{
		{Type: Vanilla, MinecraftVersion: "26.2"},
		{Type: Vanilla, MinecraftVersion: "1.21"},
		{Type: Purpur, MinecraftVersion: "26.2", PurpurBuild: 2633},
		{Type: Fabric, MinecraftVersion: "1.21.8", FabricLoader: "0.19.5"},
		{Type: Fabric, MinecraftVersion: "26.3", FabricLoader: "0.12.6"},
		{Type: Quilt, MinecraftVersion: "26.3", QuiltLoader: "0.31.0-beta.4"},
		{Type: NeoForge, MinecraftVersion: "26.2", NeoForgeVersion: "26.2.0.88"},
		{Type: NeoForge, MinecraftVersion: "1.21.1", NeoForgeVersion: "21.1.251"},
		{Type: NeoForge, MinecraftVersion: "1.21", NeoForgeVersion: "21.0.0-beta"},
		{Type: Forge, MinecraftVersion: "26.2", ForgeVersion: "65.1.3"},
		{Type: Forge, MinecraftVersion: "1.21.1", ForgeVersion: "52.1.0"},
	} {
		if err := p.Validate(); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	for _, tt := range []struct {
		pin Pin
		msg string
	}{
		{Pin{Type: "paper", MinecraftVersion: "26.2"}, `Playkeeper does not install "paper" servers this way.`},
		{Pin{Type: "spigot", MinecraftVersion: "1.21.1"}, `Playkeeper does not install "spigot" servers this way.`},
		{Pin{Type: Vanilla, MinecraftVersion: "1.20.6"}, `Playkeeper runs Minecraft releases from 1.21 on, not "1.20.6".`},
		{Pin{Type: Vanilla, MinecraftVersion: "26.4-snapshot-1"}, `not "26.4-snapshot-1".`},
		{Pin{Type: Vanilla, MinecraftVersion: "../../etc"}, `not "../../etc".`},
		{Pin{Type: Vanilla, MinecraftVersion: "26.2", PurpurBuild: 2633}, "The version chosen for this Vanilla server carries build details of another server type."},
		{Pin{Type: Purpur, MinecraftVersion: "26.2"}, `"0" is not a Purpur build number.`},
		{Pin{Type: Purpur, MinecraftVersion: "26.2", PurpurBuild: -1}, `"-1" is not a Purpur build number.`},
		{Pin{Type: Fabric, MinecraftVersion: "26.2", FabricLoader: "0.12.5"}, `"0.12.5" is not a Fabric loader version Playkeeper can run (it runs releases newer than 0.12.5).`},
		{Pin{Type: Fabric, MinecraftVersion: "26.2", FabricLoader: "0.19.5-beta.1"}, "is not a Fabric loader version"},
		{Pin{Type: Fabric, MinecraftVersion: "26.2", FabricLoader: "0.19.5; rm -rf /"}, "is not a Fabric loader version"},
		{Pin{Type: Fabric, MinecraftVersion: "26.2", FabricLoader: "0.19.5", QuiltLoader: "0.30.1"}, "carries build details of another server type"},
		{Pin{Type: Quilt, MinecraftVersion: "26.2"}, `"" is not a Quilt loader version.`},
		{Pin{Type: Quilt, MinecraftVersion: "26.2", QuiltLoader: "0.30.1/../../x"}, "is not a Quilt loader version"},
		{Pin{Type: NeoForge, MinecraftVersion: "26.2", NeoForgeVersion: "26.1.2.109"}, `"26.1.2.109" is not a NeoForge version for Minecraft 26.2.`},
		{Pin{Type: NeoForge, MinecraftVersion: "1.21.1", NeoForgeVersion: "21.1.251.1"}, "is not a NeoForge version for Minecraft 1.21.1"},
		{Pin{Type: NeoForge, MinecraftVersion: "26.2", NeoForgeVersion: "26.2.0.88\n"}, "is not a NeoForge version for Minecraft 26.2"},
		{Pin{Type: Forge, MinecraftVersion: "26.2"}, `"" is not a Forge version.`},
		{Pin{Type: Forge, MinecraftVersion: "26.2", ForgeVersion: "26.2-65.1.3"}, `"26.2-65.1.3" is not a Forge version.`},
		{Pin{Type: Forge, MinecraftVersion: "26.2", ForgeVersion: "65.1.3/../../x"}, "is not a Forge version"},
		{Pin{Type: Forge, MinecraftVersion: "26.2", ForgeVersion: "65.1"}, "is not a Forge version"},
		{Pin{Type: Forge, MinecraftVersion: "26.2", ForgeVersion: "65.1.3", NeoForgeVersion: "26.2.0.88"}, "carries build details of another server type"},
	} {
		if e := wantKind(t, tt.pin.Validate(), KindUnsupported); !strings.Contains(e.Msg, tt.msg) {
			t.Errorf("%+v: got %q, want %q", tt.pin, e.Msg, tt.msg)
		}
	}
}

// copyPlan copies a plan through JSON, the way a stored plan comes back.
func copyPlan(t *testing.T, p Plan) Plan {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var out Plan
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPlanValidation(t *testing.T) {
	_, fabric := resolve(t, resolveFake(t, Fabric), Pin{Type: Fabric, MinecraftVersion: "1.21.8", FabricLoader: "0.19.5"})
	_, neoforge := resolve(t, resolveFake(t, NeoForge), Pin{Type: NeoForge, MinecraftVersion: "26.2", NeoForgeVersion: "26.2.0.88"})
	_, forge := resolve(t, resolveFake(t, Forge), Pin{Type: Forge, MinecraftVersion: "26.2", ForgeVersion: "65.1.0"})
	if !reflect.DeepEqual(copyPlan(t, fabric), fabric) || !reflect.DeepEqual(copyPlan(t, neoforge), neoforge) || !reflect.DeepEqual(copyPlan(t, forge), forge) {
		t.Fatal("a plan changes when it is stored as JSON")
	}
	tests := []struct {
		name   string
		base   Plan
		change func(p *Plan)
		kind   Kind
	}{
		{"nothing to download", fabric, func(p *Plan) { p.Downloads = nil }, KindMalformed},
		{"download outside the server folder", fabric, func(p *Plan) { p.Downloads[0].Path = "../asm.jar" }, KindUnsafePath},
		{"download that is not a jar", fabric, func(p *Plan) { p.Downloads[0].Path = "libraries/run.sh" }, KindUnsafePath},
		{"same download twice", fabric, func(p *Plan) { p.Downloads = append(p.Downloads, p.Downloads[0]) }, KindMalformed},
		{"download that is not checked", fabric, func(p *Plan) { p.Checks = p.Checks[1:] }, KindMalformed},
		{"download without a valid hash", fabric, func(p *Plan) { p.Downloads[0].Hash.Value = "abc" }, KindMalformed},
		{"download without a source", fabric, func(p *Plan) { p.Downloads[0].Source = "" }, KindMalformed},
		{"download from another host", fabric, func(p *Plan) { p.Downloads[0].URL = "https://evil.example.com/asm.jar" }, KindHostNotAllowed},
		{"download over plain HTTP", fabric, func(p *Plan) { p.Downloads[0].URL = strings.Replace(p.Downloads[0].URL, "https:", "http:", 1) }, KindHostNotAllowed},
		{"check outside the server folder", fabric, func(p *Plan) { p.Checks = append(p.Checks, Check{Path: "../../etc/passwd", Hash: p.Checks[0].Hash}) }, KindUnsafePath},
		{"launch jar in a subfolder", fabric, func(p *Plan) { p.Launcher.Path = "libraries/launch.jar" }, KindUnsafePath},
		{"launch jar over a download", fabric, func(p *Plan) { p.Launcher.Path = "server.jar" }, KindUnsafePath},
		{"main class from a file not downloaded", fabric, func(p *Plan) { p.Launcher.MainClassFrom = "loader.jar" }, KindMalformed},
		{"two main classes", fabric, func(p *Plan) { p.Launcher.MainClass = "com.example.Main" }, KindMalformed},
		{"main class that is not a class", fabric, func(p *Plan) { p.Launcher.MainClassFrom, p.Launcher.MainClass = "", "Main; rm -rf /" }, KindMalformed},
		{"class path with a file not downloaded", fabric, func(p *Plan) { p.Launcher.ClassPath = append(p.Launcher.ClassPath, "libraries/evil.jar") }, KindMalformed},
		{"properties file in a subfolder", fabric, func(p *Plan) { p.Launcher.Properties = "config/fabric.properties" }, KindUnsafePath},
		{"properties with a NUL byte", fabric, func(p *Plan) { p.Launcher.PropertiesBody = "launch.mainClass=\x00" }, KindMalformed},
		{"setup container that starts the server", neoforge, func(p *Plan) { p.Setup.Env = p.Setup.Env[:3] }, KindMalformed},
		{"setup container that installs a file not downloaded", neoforge, func(p *Plan) { p.Setup.Env[1] = "NEOFORGE_INSTALLER=/data/other-installer.jar" }, KindMalformed},
		{"setup container that downloads the installer", neoforge, func(p *Plan) {
			p.Setup.Env[1] = "NEOFORGE_INSTALLER_URL=https://evil.example.com/installer.jar"
		}, KindMalformed},
		{"Forge setup container that starts the server", forge, func(p *Plan) { p.Setup.Env = p.Setup.Env[:3] }, KindMalformed},
		{"Forge setup container that installs a file not downloaded", forge, func(p *Plan) { p.Setup.Env[1] = "FORGE_INSTALLER=/data/other-installer.jar" }, KindMalformed},
		{"Forge setup container that downloads the installer", forge, func(p *Plan) {
			p.Setup.Env[1] = "FORGE_INSTALLER_URL=https://evil.example.com/installer.jar"
		}, KindMalformed},
		{"Forge setup container that picks its own version", forge, func(p *Plan) { p.Setup.Env[1] = "FORGE_VERSION=latest" }, KindMalformed},
		{"Forge server container that runs the installer's run.sh", forge, func(p *Plan) { p.Run.Env = []string{"TYPE=FORGE"} }, KindMalformed},
		{"setting with a line break", fabric, func(p *Plan) { p.Run.Env = append(p.Run.Env, "MOTD=hi\nRCON_PASSWORD=x") }, KindMalformed},
		{"setting with a lowercase name", fabric, func(p *Plan) { p.Run.Env = append(p.Run.Env, "motd=hi") }, KindMalformed},
		{"setting Playkeeper does not allow", fabric, func(p *Plan) { p.Run.Env = append(p.Run.Env, "JVM_OPTS=-javaagent:/data/agent.jar") }, KindMalformed},
		{"setting given twice", fabric, func(p *Plan) { p.Run.Env = append(p.Run.Env, "TYPE=NEOFORGE") }, KindMalformed},
		{"server container that is not custom", fabric, func(p *Plan) { p.Run.Env = []string{"TYPE=FABRIC"} }, KindMalformed},
		{"server container in setup mode", fabric, func(p *Plan) { p.Run.Env = append(p.Run.Env, "SETUP_ONLY=TRUE") }, KindMalformed},
		{"server container that downloads its jar", fabric, func(p *Plan) {
			p.Run.Env = []string{"TYPE=CUSTOM", "CUSTOM_SERVER=https://evil.example.com/server.jar"}
		}, KindMalformed},
		{"server container that starts a file outside the server folder", fabric, func(p *Plan) {
			p.Run.Env = []string{"TYPE=CUSTOM", "CUSTOM_SERVER=/data/../server.jar"}
		}, KindMalformed},
		{"server container that starts two things", neoforge, func(p *Plan) { p.Run.Env = append(p.Run.Env, "CUSTOM_SERVER=/data/server.jar") }, KindMalformed},
		{"unknown way to read hashes", neoforge, func(p *Plan) { p.Derive[0].Kind = "shell" }, KindMalformed},
		{"hashes read from a file not downloaded", neoforge, func(p *Plan) { p.Derive[0].From = "other.jar" }, KindMalformed},
		{"hashes for files outside the server folder", neoforge, func(p *Plan) { p.Derive[1].Into = "../libraries" }, KindUnsafePath},
		{"removes a file outside the server folder", neoforge, func(p *Plan) { p.Remove = append(p.Remove, "../../etc/passwd") }, KindUnsafePath},
		{"records a file outside the server folder", fabric, func(p *Plan) { p.Record = []string{"/etc/shadow"} }, KindUnsafePath},
		{"pin with another type's build", fabric, func(p *Plan) { p.Pin.QuiltLoader = "0.30.1" }, KindUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := copyPlan(t, tt.base)
			tt.change(&p)
			dir := t.TempDir()
			wantKind(t, Prepare(dir, p), tt.kind)
			_, err := Finish(dir, p)
			wantKind(t, err, tt.kind)
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("wrote %v", entries)
			}
		})
	}
}

// bundledLibs are the libraries the stand-in Mojang server jars bundle.
func bundledLibs(mc string) map[string]string {
	return map[string]string{
		"com/mojang/brigadier/1.3.10/brigadier-1.3.10.jar": "brigadier for " + mc,
		"org/slf4j/slf4j-api/2.0.17/slf4j-api-2.0.17.jar":  "slf4j for " + mc,
	}
}

// installFake serves Mojang with small stand-in server jars for mcs, so
// installs can be carried out end to end. It returns the jars.
func installFake(t *testing.T, mcs ...string) (*fakeNet, map[string][]byte) {
	t.Helper()
	f := newFakeNet(t)
	files := mojangFiles(t)
	jars := map[string][]byte{}
	for _, mc := range mcs {
		java := 25
		if strings.HasPrefix(mc, "1.") {
			java = 21
		}
		jars[mc] = fakeServerJar(t, mc, bundledLibs(mc))
		files[mc] = mojangVersionFile(t, f, mc, java, jars[mc])
	}
	serveMojang(t, f, files)
	return f, jars
}

func downloadAll(t *testing.T, f *fakeNet, dir string, p Plan) {
	t.Helper()
	for _, a := range p.Downloads {
		if err := Download(context.Background(), f.client(), dir, a); err != nil {
			t.Fatal(err)
		}
	}
}

// tamper changes the last byte of a file, keeping its size.
func tamper(t *testing.T, dir, rel string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)-1] ^= 1
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(dir, rel string) bool {
	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil
}

func origins(m Manifest) map[Origin]int {
	out := map[Origin]int{}
	for _, c := range m.Checks {
		out[c.Origin]++
	}
	return out
}

func TestInstallVanilla(t *testing.T) {
	f, jars := installFake(t, "26.2")
	_, p := resolve(t, f, Pin{Type: Vanilla, MinecraftVersion: "26.2"})
	dir := t.TempDir()
	downloadAll(t, f, dir, p)
	if err := Prepare(dir, p); err != nil {
		t.Fatal(err)
	}
	m, err := Finish(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "minecraft_server.26.2.jar")); !bytes.Equal(got, jars["26.2"]) {
		t.Error("the server jar is not Mojang's")
	}
	if !slices.Equal(m.Checks, p.Checks) || !reflect.DeepEqual(m.Run, p.Run) || m.Pin != p.Pin || !reflect.DeepEqual(m.Assurance, p.Assurance) {
		t.Errorf("got manifest %+v", m)
	}
	if err := m.Verify(dir); err != nil {
		t.Fatal(err)
	}
	tamper(t, dir, "minecraft_server.26.2.jar")
	if e := wantKind(t, m.Verify(dir), KindHashMismatch); e.Params["origin"] != string(Pinned) {
		t.Errorf("got params %v", e.Params)
	}
}

func TestFinishBeforeDownload(t *testing.T) {
	f, _ := installFake(t, "26.2")
	_, p := resolve(t, f, Pin{Type: Vanilla, MinecraftVersion: "26.2"})
	_, err := Finish(t.TempDir(), p)
	wantKind(t, err, KindMissingFile)
}

func TestManifestValidation(t *testing.T) {
	dir := t.TempDir()
	writeData(t, dir, "server.jar", []byte("server"))
	good := Manifest{Pin: Pin{Type: Vanilla, MinecraftVersion: "26.2"},
		Checks: []Check{{Path: "server.jar", Hash: Hash{Algorithm: SHA1, Value: hexSum(SHA1, []byte("server"))}, Origin: Pinned, Source: "Mojang's version manifest"}},
		Run:    Container{Env: []string{"TYPE=CUSTOM", "CUSTOM_SERVER=/data/server.jar"}}}
	if err := good.Verify(dir); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(m *Manifest)
		kind   Kind
	}{
		{"no checks", func(m *Manifest) { m.Checks = nil }, KindMissingFile},
		{"server container that starts a file not checked", func(m *Manifest) { m.Run.Env[1] = "CUSTOM_SERVER=/data/other.jar" }, KindMalformed},
		{"server container that downloads its jar", func(m *Manifest) { m.Run.Env[1] = "CUSTOM_SERVER=https://evil.example.com/server.jar" }, KindMalformed},
		{"server container with another setting", func(m *Manifest) { m.Run.Env = append(m.Run.Env, "JVM_OPTS=-javaagent:/data/agent.jar") }, KindMalformed},
		{"server container that is not custom", func(m *Manifest) { m.Run.Env[0] = "TYPE=NEOFORGE" }, KindMalformed},
		{"pin that is not valid", func(m *Manifest) { m.Pin.MinecraftVersion = "1.20.4" }, KindUnsupported},
	}
	for _, tt := range tests {
		m := good
		m.Checks, m.Run.Env = slices.Clone(good.Checks), slices.Clone(good.Run.Env)
		tt.change(&m)
		if err := m.Verify(dir); err == nil {
			t.Errorf("%s: Verify accepted it", tt.name)
		} else {
			wantKind(t, err, tt.kind)
		}
	}
}

// purpurJar is a stand-in Paperclip jar that expects server as Mojang's jar
// under the given name.
func purpurJar(t *testing.T, server []byte, name string) []byte {
	t.Helper()
	return zipOf(t, "META-INF/MANIFEST.MF", "Manifest-Version: 1.0\r\nMain-Class: io.papermc.paperclip.Main\r\n\r\n",
		"META-INF/download-context", hexSum(SHA256, server)+"\thttps://piston-data.mojang.com/v1/objects/"+hexSum(SHA1, server)+"/server.jar\t"+name+"\n")
}

func TestInstallPurpur(t *testing.T) {
	install := func(t *testing.T, jar func(t *testing.T, server []byte) []byte) (string, Plan, []byte) {
		t.Helper()
		f, jars := installFake(t, "26.2")
		purpur := jar(t, jars["26.2"])
		f.serve(purpurAPI+"/26.2/2633", editJSON(t, readFixture(t, "purpur/26.2-2633.json"), func(v any) any {
			obj(v)["md5"] = hexSum(MD5, purpur)
			return v
		}))
		f.serve(purpurAPI+"/26.2/2633/download", purpur)
		_, p := resolve(t, f, Pin{Type: Purpur, MinecraftVersion: "26.2", PurpurBuild: 2633})
		dir := t.TempDir()
		downloadAll(t, f, dir, p)
		if err := Prepare(dir, p); err != nil {
			t.Fatal(err)
		}
		return dir, p, jars["26.2"]
	}

	dir, p, server := install(t, func(t *testing.T, server []byte) []byte { return purpurJar(t, server, "mojang_26.2.jar") })
	m, err := Finish(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	want := append(slices.Clone(p.Checks), Check{Path: "cache/mojang_26.2.jar", Hash: Hash{Algorithm: SHA256, Value: hexSum(SHA256, server)},
		Origin: Derived, Source: "the download list inside the verified Purpur jar"})
	if !slices.Equal(m.Checks, want) {
		t.Errorf("got checks %+v", m.Checks)
	}
	if err := m.Verify(dir); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name string
		jar  func(t *testing.T, server []byte) []byte
		kind Kind
		msg  string
	}{
		{"expects Mojang's jar elsewhere", func(t *testing.T, server []byte) []byte { return purpurJar(t, server, "mojang_26.1.jar") }, KindMalformed,
			"Playkeeper could not read purpur-26.2-2633.jar: it expects Mojang's server jar at cache/mojang_26.1.jar, not where Playkeeper put it."},
		{"expects another Mojang jar", func(t *testing.T, _ []byte) []byte {
			return purpurJar(t, []byte("another server jar"), "mojang_26.2.jar")
		}, KindHashMismatch,
			"cache/mojang_26.2.jar does not match the SHA-256 from the download list inside the verified Purpur jar"},
		{"has no download list", func(t *testing.T, _ []byte) []byte {
			return zipOf(t, "META-INF/MANIFEST.MF", "Manifest-Version: 1.0\r\n\r\n")
		}, KindMalformed,
			"it has no META-INF/download-context"},
		{"names a path", func(t *testing.T, server []byte) []byte { return purpurJar(t, server, "../mojang_26.2.jar") }, KindMalformed,
			"its META-INF/download-context lists an invalid hash or file name"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir, p, _ := install(t, tt.jar)
			_, err := Finish(dir, p)
			if e := wantKind(t, err, tt.kind); !strings.Contains(e.Msg, tt.msg) {
				t.Errorf("got %q, want %q", e.Msg, tt.msg)
			}
		})
	}
}

// fabricInstall downloads a Fabric 26.3 plan into a new data directory.
func fabricInstall(t *testing.T, change func(f *fakeNet)) (string, Plan) {
	t.Helper()
	f, _ := installFake(t, "26.3")
	serveFabric(t, f, false)
	if change != nil {
		change(f)
	}
	_, p := resolve(t, f, Pin{Type: Fabric, MinecraftVersion: "26.3", FabricLoader: "0.19.5"})
	dir := t.TempDir()
	downloadAll(t, f, dir, p)
	return dir, p
}

func TestInstallFabric(t *testing.T) {
	dir, p := fabricInstall(t, nil)
	if err := Prepare(dir, p); err != nil {
		t.Fatal(err)
	}
	names, files := readJar(t, filepath.Join(dir, "fabric-server-launch.jar"))
	m := []byte(files["META-INF/MANIFEST.MF"])
	if strings.Join(names, " ") != "META-INF/MANIFEST.MF fabric-server-launch.properties" ||
		manifestAttribute(m, "Main-Class") != "net.fabricmc.loader.impl.launch.server.FabricServerLauncher" ||
		manifestAttribute(m, "Class-Path") != strings.Join(p.Launcher.ClassPath, " ") ||
		files["fabric-server-launch.properties"] != "launch.mainClass=net.fabricmc.loader.impl.launch.knot.KnotServer\n" {
		t.Errorf("launch jar %q: %q", names, files)
	}
	man, err := Finish(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := origins(man); !maps.Equal(got, map[Origin]int{Pinned: len(p.Downloads), Recorded: 1}) {
		t.Errorf("got checks by origin %v", got)
	}
	jar, _ := os.ReadFile(filepath.Join(dir, "fabric-server-launch.jar"))
	if last := man.Checks[len(man.Checks)-1]; last.Path != "fabric-server-launch.jar" || last.Hash != (Hash{Algorithm: SHA256, Value: hexSum(SHA256, jar)}) {
		t.Errorf("got recorded check %+v", last)
	}

	b, err := json.Marshal(man)
	if err != nil {
		t.Fatal(err)
	}
	var stored Manifest
	if err := json.Unmarshal(b, &stored); err != nil || !reflect.DeepEqual(stored, man) {
		t.Fatalf("the manifest changes when it is stored as JSON (%v)", err)
	}
	if err := stored.Verify(dir); err != nil {
		t.Fatal(err)
	}
	tamper(t, dir, "fabric-server-launch.jar")
	e := wantKind(t, stored.Verify(dir), KindHashMismatch)
	if e.Params["origin"] != string(Recorded) || !strings.Contains(e.Msg, recordedSource) {
		t.Errorf("got %q, params %v", e.Msg, e.Params)
	}
}

func TestPrepareRefuses(t *testing.T) {
	t.Run("a library changed after download", func(t *testing.T) {
		dir, p := fabricInstall(t, nil)
		tamper(t, dir, p.Launcher.ClassPath[0])
		wantKind(t, Prepare(dir, p), KindHashMismatch)
		if exists(dir, "fabric-server-launch.jar") {
			t.Error("wrote the launch jar")
		}
	})
	t.Run("a library that was not downloaded", func(t *testing.T) {
		dir, p := fabricInstall(t, nil)
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(p.Launcher.ClassPath[2]))); err != nil {
			t.Fatal(err)
		}
		wantKind(t, Prepare(dir, p), KindMissingFile)
	})
	t.Run("a loader that names no main class", func(t *testing.T) {
		dir, p := fabricInstall(t, func(f *fakeNet) {
			const loader = "https://maven.fabricmc.net/net/fabricmc/fabric-loader/0.19.5/fabric-loader-0.19.5.jar"
			jar := zipOf(t, "META-INF/MANIFEST.MF", "Manifest-Version: 1.0\r\nMain-Class: not a class\r\n\r\n")
			f.serve(loader, jar)
			f.serve(loader+".sha512", []byte(hexSum(SHA512, jar)))
		})
		e := wantKind(t, Prepare(dir, p), KindMalformed)
		if !strings.Contains(e.Msg, "fabric-loader-0.19.5.jar: its manifest names no valid Main-Class.") {
			t.Errorf("got %q", e.Msg)
		}
	})
}

func TestInstallQuilt(t *testing.T) {
	f, _ := installFake(t, "26.3")
	serveQuilt(t, f)
	_, p := resolve(t, f, Pin{Type: Quilt, MinecraftVersion: "26.3", QuiltLoader: "0.30.1"})
	dir := t.TempDir()
	downloadAll(t, f, dir, p)
	if err := Prepare(dir, p); err != nil {
		t.Fatal(err)
	}
	names, files := readJar(t, filepath.Join(dir, "quilt-server-launch.jar"))
	m := []byte(files["META-INF/MANIFEST.MF"])
	if strings.Join(names, " ") != "META-INF/MANIFEST.MF" ||
		manifestAttribute(m, "Main-Class") != "org.quiltmc.loader.impl.launch.server.QuiltServerLauncher" ||
		manifestAttribute(m, "Class-Path") != strings.Join(p.Launcher.ClassPath, " ") {
		t.Errorf("launch jar %q: %q", names, files)
	}
	man, err := Finish(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := origins(man); !maps.Equal(got, map[Origin]int{Pinned: len(p.Downloads), Recorded: 1}) {
		t.Errorf("got checks by origin %v", got)
	}
	if err := man.Verify(dir); err != nil {
		t.Fatal(err)
	}
}

// neoforgeInstaller builds a stand-in NeoForge installer from the install
// profile and version file fixtures of version v, changed by edit. Every
// library's SHA-1 and size are set for the content the stand-in install
// writes, which it returns by library path.
func neoforgeInstaller(t *testing.T, v string, edit func(profile map[string]any)) ([]byte, map[string]string) {
	t.Helper()
	libs := map[string]string{}
	fakeLibs := func(doc any) any {
		for _, l := range obj(doc)["libraries"].([]any) {
			a := obj(l, "downloads", "artifact")
			p := a["path"].(string)
			libs[p] = "library " + p
			a["sha1"], a["size"] = hexSum(SHA1, []byte(libs[p])), len(libs[p])
		}
		return doc
	}
	profile := editJSON(t, readFixture(t, "neoforge/install_profile-"+v+".json"), func(doc any) any {
		fakeLibs(doc)
		if edit != nil {
			edit(obj(doc))
		}
		return doc
	})
	version := editJSON(t, readFixture(t, "neoforge/version-"+v+".json"), fakeLibs)
	return zipOf(t, "install_profile.json", string(profile), "version.json", string(version), "data/unix_args.txt", neoforgeArgs(t, v)), libs
}

func neoforgeArgs(t *testing.T, v string) string {
	if v == "26.2.0.88" {
		return string(readFixture(t, "neoforge/unix_args-26.2.0.88.txt"))
	}
	return "-DlibraryDirectory=libraries\n--launchTarget forgeserver\n"
}

type neoforgeInstall struct {
	dir            string
	plan           Plan
	libs           map[string]string
	v, mc, fixture string
}

// newNeoForgeInstall downloads the plan for NeoForge v, whose installer is
// built from the fixtures of version fixture.
func newNeoForgeInstall(t *testing.T, v, mc, fixture string, edit func(profile map[string]any)) *neoforgeInstall {
	t.Helper()
	f, _ := installFake(t, mc)
	installer, libs := neoforgeInstaller(t, fixture, edit)
	f.serve(neoforgeInstallerURL(v), installer)
	f.serve(neoforgeInstallerURL(v)+".sha512", []byte(hexSum(SHA512, installer)))
	_, p := resolve(t, f, Pin{Type: NeoForge, MinecraftVersion: mc, NeoForgeVersion: v})
	in := &neoforgeInstall{dir: t.TempDir(), plan: p, libs: libs, v: v, mc: mc, fixture: fixture}
	downloadAll(t, f, in.dir, p)
	if err := Prepare(in.dir, p); err != nil {
		t.Fatal(err)
	}
	return in
}

// runInstaller does on disk what NeoForge's installer does in the setup
// container: it installs the libraries, extracts the launch arguments and
// Minecraft's own libraries, writes the files it builds and leaves its log.
func (in *neoforgeInstall) runInstaller(t *testing.T, built ...string) {
	t.Helper()
	for p, body := range in.libs {
		writeData(t, in.dir, "libraries/"+p, []byte(body))
	}
	for p, body := range bundledLibs(in.mc) {
		writeData(t, in.dir, "libraries/"+p, []byte(body))
	}
	writeData(t, in.dir, neoforgeArgsPath(in.v), []byte(neoforgeArgs(t, in.fixture)))
	for _, p := range built {
		writeData(t, in.dir, p, []byte("built on this host: "+p))
	}
	writeData(t, in.dir, "neoforge-"+in.v+"-installer.jar.log", []byte("installer log"))
}

const neoforgePatched = "libraries/net/neoforged/minecraft-server-patched/26.2.0.88/minecraft-server-patched-26.2.0.88.jar"

func TestInstallNeoForge(t *testing.T) {
	in := newNeoForgeInstall(t, "26.2.0.88", "26.2", "26.2.0.88", nil)
	in.runInstaller(t, neoforgePatched)
	m, err := Finish(in.dir, in.plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range in.plan.Remove {
		if exists(in.dir, gone) {
			t.Errorf("%s was not removed", gone)
		}
	}
	want := map[Origin]int{Pinned: 1, Derived: len(in.libs) + 1 + len(bundledLibs("26.2")), Recorded: 1}
	if got := origins(m); !maps.Equal(got, want) {
		t.Errorf("got checks by origin %v, want %v", got, want)
	}
	byPath := map[string]Check{}
	for _, c := range m.Checks {
		if _, dup := byPath[c.Path]; dup {
			t.Errorf("%s is checked twice", c.Path)
		}
		byPath[c.Path] = c
	}
	server := byPath["libraries/net/minecraft/server/26.2/server-26.2.jar"]
	args := byPath[neoforgeArgsPath("26.2.0.88")]
	patched := byPath[neoforgePatched]
	brigadier := byPath["libraries/com/mojang/brigadier/1.3.10/brigadier-1.3.10.jar"]
	loader := byPath["libraries/net/neoforged/fancymodloader/loader/11.0.16/loader-11.0.16.jar"]
	if server.Origin != Pinned || server.Hash.Algorithm != SHA1 ||
		args.Origin != Derived || args.Hash.Value != hexSum(SHA256, []byte(neoforgeArgs(t, "26.2.0.88"))) ||
		patched.Origin != Recorded || patched.Hash.Algorithm != SHA256 ||
		brigadier.Origin != Derived || brigadier.Source != "the library list inside Mojang's verified server jar" ||
		loader.Hash.Algorithm != SHA1 || loader.Size == 0 || loader.Source != neoforgeLibrarySource {
		t.Errorf("got server %+v\nargs %+v\npatched %+v\nbrigadier %+v\nloader %+v", server, args, patched, brigadier, loader)
	}
	if _, ok := byPath["neoforge-26.2.0.88-installer.jar"]; ok {
		t.Error("the removed installer is still checked")
	}
	wantStrings(t, "run env", m.Run.Env, []string{"TYPE=CUSTOM", "CUSTOM_JAR_EXEC=@libraries/net/neoforged/neoforge/26.2.0.88/unix_args.txt"})
	if err := m.Verify(in.dir); err != nil {
		t.Fatal(err)
	}
	tamper(t, in.dir, neoforgePatched)
	if e := wantKind(t, m.Verify(in.dir), KindHashMismatch); e.Params["origin"] != string(Recorded) {
		t.Errorf("got params %v", e.Params)
	}
}

func TestInstallNeoForgeForMinecraft1211(t *testing.T) {
	in := newNeoForgeInstall(t, "21.1.251", "1.21.1", "21.1.251", nil)
	patched := "libraries/net/neoforged/neoforge/21.1.251/neoforge-21.1.251-server.jar"
	srg := "libraries/net/minecraft/server/1.21.1-20240808.144430/server-1.21.1-20240808.144430-srg.jar"
	in.runInstaller(t, patched, srg)
	m, err := Finish(in.dir, in.plan)
	if err != nil {
		t.Fatal(err)
	}
	var recorded []string
	for _, c := range m.Checks {
		if c.Origin == Recorded {
			recorded = append(recorded, c.Path)
		}
	}
	wantStrings(t, "recorded", recorded, []string{srg, patched})
	wantStrings(t, "run env", m.Run.Env, []string{"TYPE=CUSTOM", "CUSTOM_JAR_EXEC=@libraries/net/neoforged/neoforge/21.1.251/unix_args.txt"})
	if err := m.Verify(in.dir); err != nil {
		t.Fatal(err)
	}
}

func TestInstallNeoForgeRefuses(t *testing.T) {
	run := func(t *testing.T, in *neoforgeInstall) { in.runInstaller(t, neoforgePatched) }
	runThen := func(change func(t *testing.T, dir string)) func(t *testing.T, in *neoforgeInstall) {
		return func(t *testing.T, in *neoforgeInstall) {
			run(t, in)
			change(t, in.dir)
		}
	}
	tamperWith := func(rel string) func(t *testing.T, in *neoforgeInstall) {
		return runThen(func(t *testing.T, dir string) { tamper(t, dir, rel) })
	}
	processorArgs := func(edit func(args []any)) func(p map[string]any) {
		return func(p map[string]any) {
			for _, pr := range p["processors"].([]any) {
				edit(pr.(map[string]any)["args"].([]any))
			}
		}
	}
	tests := []struct {
		name    string
		fixture string
		edit    func(profile map[string]any)
		install func(t *testing.T, in *neoforgeInstall)
		kind    Kind
		msg     string
	}{
		{"a library is missing", "26.2.0.88", nil, runThen(func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "libraries/net/neoforged/mergetool/2.0.7/mergetool-2.0.7-api.jar")); err != nil {
				t.Fatal(err)
			}
		}), KindMissingFile, "The server software file libraries/net/neoforged/mergetool/2.0.7/mergetool-2.0.7-api.jar is missing."},
		{"a library was changed", "26.2.0.88", nil, tamperWith("libraries/net/neoforged/fancymodloader/loader/11.0.16/loader-11.0.16.jar"),
			KindHashMismatch, "does not match the SHA-1 from the library list inside the verified NeoForge installer"},
		{"one of Minecraft's libraries was changed", "26.2.0.88", nil, tamperWith("libraries/com/mojang/brigadier/1.3.10/brigadier-1.3.10.jar"),
			KindHashMismatch, "does not match the SHA-256 from the library list inside Mojang's verified server jar"},
		{"the launch arguments were changed", "26.2.0.88", nil, tamperWith(neoforgeArgsPath("26.2.0.88")),
			KindHashMismatch, "does not match the SHA-256 from the launch arguments inside the verified NeoForge installer"},
		{"the patched jar was not built", "26.2.0.88", nil, func(t *testing.T, in *neoforgeInstall) { in.runInstaller(t) },
			KindMissingFile, "The install did not create " + neoforgePatched + "."},
		{"the installer is for another version", "21.1.251", nil, nil,
			KindMalformed, `it installs "neoforge-21.1.251" for Minecraft "1.21.1", not NeoForge 26.2.0.88 for Minecraft 26.2.`},
		{"the installer puts its launch arguments elsewhere", "26.2.0.88", processorArgs(func(args []any) {
			for i, a := range args {
				if a == "{ROOT}/libraries/net/neoforged/neoforge/26.2.0.88/unix_args.txt" {
					args[i] = "{ROOT}/unix_args.txt"
				}
			}
		}), run, KindMalformed, "it does not put its launch arguments at libraries/net/neoforged/neoforge/26.2.0.88/unix_args.txt."},
		{"the installer lists a library outside the libraries folder", "26.2.0.88", func(p map[string]any) {
			obj(p["libraries"].([]any)[0], "downloads", "artifact")["path"] = "../../../etc/cron.d/evil.jar"
		}, run, KindMalformed, `its library "net.neoforged.fancymodloader:earlydisplay:11.0.16" has no valid path, SHA-1 or size.`},
		{"the installer does not name the patched jar", "26.2.0.88", func(p map[string]any) { delete(obj(p, "data"), "PATCHED") }, run,
			KindMalformed, "its install profile does not name the patched Minecraft jar."},
		{"the installer names an unsafe output", "26.2.0.88", func(p map[string]any) { obj(p, "data", "PATCHED")["server"] = "[../../x:y:z]" }, run,
			KindMalformed, "its install profile names an invalid file for PATCHED."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newNeoForgeInstall(t, "26.2.0.88", "26.2", tt.fixture, tt.edit)
			if tt.install != nil {
				tt.install(t, in)
			}
			_, err := Finish(in.dir, in.plan)
			if e := wantKind(t, err, tt.kind); !strings.Contains(e.Msg, tt.msg) {
				t.Errorf("got %q, want %q", e.Msg, tt.msg)
			}
			if !exists(in.dir, "neoforge-26.2.0.88-installer.jar") {
				t.Error("the installer was removed although the install failed")
			}
		})
	}
}

func TestFinishRefusesToStartUncheckedFiles(t *testing.T) {
	in := newNeoForgeInstall(t, "26.2.0.88", "26.2", "26.2.0.88", nil)
	in.runInstaller(t, neoforgePatched)
	in.plan.Run.Env = []string{"TYPE=CUSTOM", "CUSTOM_JAR_EXEC=@libraries/user_jvm_args.txt"}
	_, err := Finish(in.dir, in.plan)
	if e := wantKind(t, err, KindMalformed); !strings.Contains(e.Msg, "the server container would not start a checked file") {
		t.Errorf("got %q", e.Msg)
	}
	if !exists(in.dir, "neoforge-26.2.0.88-installer.jar") {
		t.Error("the installer was removed although the install failed")
	}
}

// forgeInstaller builds a stand-in Forge installer from the install profile
// and version file fixtures of build id, changed by edit. Every library's
// SHA-1 and size, and every SHA-1 the profile publishes for a file it
// builds, are set for the content the stand-in install writes, which it
// returns by path in the data directory.
func forgeInstaller(t *testing.T, id string, edit func(profile map[string]any)) ([]byte, map[string]string) {
	t.Helper()
	files := map[string]string{}
	fakeLibs := func(doc any) any {
		for _, l := range obj(doc)["libraries"].([]any) {
			a := obj(l, "downloads", "artifact")
			if a["url"] == "" {
				continue
			}
			p := a["path"].(string)
			files["libraries/"+p] = "library " + p
			a["sha1"], a["size"] = hexSum(SHA1, []byte(files["libraries/"+p])), len(files["libraries/"+p])
		}
		return doc
	}
	profile := editJSON(t, readFixture(t, "forge/install_profile-"+id+".json"), func(doc any) any {
		fakeLibs(doc)
		data := obj(doc, "data")
		for k, v := range data {
			sha, ok := data[k+"_SHA"]
			if !ok {
				continue
			}
			coord := v.(map[string]any)["server"].(string)
			p, _ := mavenPath(strings.Trim(coord, "[]"))
			files["libraries/"+p] = "built on this host: " + p
			sha.(map[string]any)["server"] = "'" + hexSum(SHA1, []byte(files["libraries/"+p])) + "'"
		}
		if edit != nil {
			edit(obj(doc))
		}
		return doc
	})
	version := editJSON(t, readFixture(t, "forge/version-"+id+".json"), fakeLibs)
	shim := "net/minecraftforge/forge/" + id + "/forge-" + id + "-shim.jar"
	files["forge-"+id+"-shim.jar"] = files["libraries/"+shim]
	files["libraries/net/minecraftforge/forge/"+id+"/unix_args.txt"] = string(readFixture(t, "forge/unix_args-"+id+".txt"))
	return zipOf(t, "install_profile.json", string(profile), "version.json", string(version), "data/unix_args.txt", string(readFixture(t, "forge/unix_args-"+id+".txt"))), files
}

type forgeInstall struct {
	dir   string
	plan  Plan
	files map[string]string
	mc    string
}

// newForgeInstall downloads the plan for Forge v for Minecraft mc, whose
// installer is built from the fixtures of build fixture.
func newForgeInstall(t *testing.T, mc, v, fixture string, edit func(profile map[string]any)) *forgeInstall {
	t.Helper()
	f, _ := installFake(t, mc)
	installer, files := forgeInstaller(t, fixture, edit)
	f.serve(forgeInstallerURL(mc+"-"+v), installer)
	f.serve(forgeInstallerURL(mc+"-"+v)+".sha512", []byte(hexSum(SHA512, installer)))
	_, p := resolve(t, f, Pin{Type: Forge, MinecraftVersion: mc, ForgeVersion: v})
	in := &forgeInstall{dir: t.TempDir(), plan: p, files: files, mc: mc}
	downloadAll(t, f, in.dir, p)
	if err := Prepare(in.dir, p); err != nil {
		t.Fatal(err)
	}
	return in
}

// runInstaller does on disk what Forge's installer does in the setup
// container: it installs the libraries and the shim jar, extracts the launch
// arguments and Minecraft's own libraries, and writes the files it builds,
// except those named in skip.
func (in *forgeInstall) runInstaller(t *testing.T, skip ...string) {
	t.Helper()
	for p, body := range in.files {
		if !slices.Contains(skip, p) {
			writeData(t, in.dir, p, []byte(body))
		}
	}
	for p, body := range bundledLibs(in.mc) {
		writeData(t, in.dir, "libraries/"+p, []byte(body))
	}
}

const (
	forgePatched = "libraries/net/minecraftforge/forge/26.2-65.1.0/forge-26.2-65.1.0-server.jar"
	forgeArgs    = "libraries/net/minecraftforge/forge/26.2-65.1.0/unix_args.txt"
)

func TestInstallForge(t *testing.T) {
	in := newForgeInstall(t, "26.2", "65.1.0", "26.2-65.1.0", nil)
	in.runInstaller(t)
	m, err := Finish(in.dir, in.plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range in.plan.Remove {
		if exists(in.dir, gone) {
			t.Errorf("%s was not removed", gone)
		}
	}
	if got := origins(m); got[Recorded] != 0 || got[Pinned] != 1 || got[Derived] < len(in.files)+len(bundledLibs("26.2")) {
		t.Errorf("got checks by origin %v for %d files the install writes", got, len(in.files))
	}
	byPath := map[string]Check{}
	for _, c := range m.Checks {
		if _, dup := byPath[c.Path]; dup {
			t.Errorf("%s is checked twice", c.Path)
		}
		byPath[c.Path] = c
	}
	for p := range in.files {
		if _, ok := byPath[p]; !ok {
			t.Errorf("%s is not checked", p)
		}
	}
	server := byPath["libraries/net/minecraft/server/26.2/server-26.2-bundled.jar"]
	args := byPath[forgeArgs]
	patched := byPath[forgePatched]
	shim := byPath["forge-26.2-65.1.0-shim.jar"]
	brigadier := byPath["libraries/com/mojang/brigadier/1.3.10/brigadier-1.3.10.jar"]
	fml := byPath["libraries/net/minecraftforge/fmlloader/26.2-65.1.0/fmlloader-26.2-65.1.0.jar"]
	if server.Origin != Pinned || server.Hash.Algorithm != SHA1 ||
		args.Origin != Derived || args.Hash.Value != hexSum(SHA256, readFixture(t, "forge/unix_args-26.2-65.1.0.txt")) ||
		patched.Origin != Derived || patched.Hash.Value != hexSum(SHA1, []byte(in.files[forgePatched])) || patched.Source != forgeOutputSource ||
		shim.Origin != Derived || shim.Hash != byPath["libraries/net/minecraftforge/forge/26.2-65.1.0/forge-26.2-65.1.0-shim.jar"].Hash || shim.Size == 0 ||
		brigadier.Origin != Derived || brigadier.Source != "the library list inside Mojang's verified server jar" ||
		fml.Hash.Algorithm != SHA1 || fml.Size == 0 || fml.Source != forgeLibrarySource {
		t.Errorf("got server %+v\nargs %+v\npatched %+v\nshim %+v\nbrigadier %+v\nfml %+v", server, args, patched, shim, brigadier, fml)
	}
	if _, ok := byPath["libraries/net/minecraftforge/forge/26.2-65.1.0/forge-26.2-65.1.0-client.jar"]; ok {
		t.Error("the client's patched jar is checked on a server")
	}
	if _, ok := byPath["forge-26.2-65.1.0-installer.jar"]; ok {
		t.Error("the removed installer is still checked")
	}
	if m.Assurance.Level != LevelFull {
		t.Errorf("got assurance %s, want full", m.Assurance.Level)
	}
	wantStrings(t, "run env", m.Run.Env, []string{"TYPE=CUSTOM", "CUSTOM_JAR_EXEC=@" + forgeArgs})
	if err := m.Verify(in.dir); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{forgePatched, "forge-26.2-65.1.0-shim.jar"} {
		tamper(t, in.dir, rel)
		if e := wantKind(t, m.Verify(in.dir), KindHashMismatch); e.Params["file"] != rel || e.Params["origin"] != string(Derived) {
			t.Errorf("got params %v", e.Params)
		}
		tamper(t, in.dir, rel)
	}
}

func TestInstallForgeForMinecraft1211(t *testing.T) {
	in := newForgeInstall(t, "1.21.1", "52.1.0", "1.21.1-52.1.0", nil)
	in.runInstaller(t)
	m, err := Finish(in.dir, in.plan)
	if err != nil {
		t.Fatal(err)
	}
	var built []string
	for _, c := range m.Checks {
		if c.Source == forgeOutputSource {
			built = append(built, c.Path)
		}
	}
	wantStrings(t, "built", built, []string{
		"libraries/net/minecraft/server/1.21.1/server-1.21.1-official.jar",
		"libraries/net/minecraft/server/1.21.1/server-1.21.1-unpacked.jar",
		"libraries/net/minecraft/server/1.21.1/server-1.21.1-mappings.tsrg",
		"libraries/net/minecraftforge/forge/1.21.1-52.1.0/forge-1.21.1-52.1.0-server.jar",
	})
	wantStrings(t, "run env", m.Run.Env, []string{"TYPE=CUSTOM", "CUSTOM_JAR_EXEC=@libraries/net/minecraftforge/forge/1.21.1-52.1.0/unix_args.txt"})
	if err := m.Verify(in.dir); err != nil {
		t.Fatal(err)
	}
}

func TestInstallForgeRefuses(t *testing.T) {
	run := func(t *testing.T, in *forgeInstall) { in.runInstaller(t) }
	runThen := func(change func(t *testing.T, dir string)) func(t *testing.T, in *forgeInstall) {
		return func(t *testing.T, in *forgeInstall) {
			run(t, in)
			change(t, in.dir)
		}
	}
	tamperWith := func(rel string) func(t *testing.T, in *forgeInstall) {
		return runThen(func(t *testing.T, dir string) { tamper(t, dir, rel) })
	}
	processorArgs := func(edit func(args []any)) func(p map[string]any) {
		return func(p map[string]any) {
			for _, pr := range p["processors"].([]any) {
				edit(pr.(map[string]any)["args"].([]any))
			}
		}
	}
	tests := []struct {
		name    string
		fixture string
		edit    func(profile map[string]any)
		install func(t *testing.T, in *forgeInstall)
		kind    Kind
		msg     string
	}{
		{"a library is missing", "26.2-65.1.0", nil, runThen(func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "libraries/net/minecraftforge/eventbus/7.0.5/eventbus-7.0.5.jar")); err != nil {
				t.Fatal(err)
			}
		}), KindMissingFile, "The server software file libraries/net/minecraftforge/eventbus/7.0.5/eventbus-7.0.5.jar is missing."},
		{"a library was changed", "26.2-65.1.0", nil, tamperWith("libraries/net/minecraftforge/fmlloader/26.2-65.1.0/fmlloader-26.2-65.1.0.jar"),
			KindHashMismatch, "does not match the SHA-1 from the library list inside the verified Forge installer"},
		{"the shim jar the server starts was changed", "26.2-65.1.0", nil, tamperWith("forge-26.2-65.1.0-shim.jar"),
			KindHashMismatch, "The file forge-26.2-65.1.0-shim.jar does not match the SHA-1 from the library list inside the verified Forge installer"},
		{"one of Minecraft's libraries was changed", "26.2-65.1.0", nil, tamperWith("libraries/com/mojang/brigadier/1.3.10/brigadier-1.3.10.jar"),
			KindHashMismatch, "does not match the SHA-256 from the library list inside Mojang's verified server jar"},
		{"the launch arguments were changed", "26.2-65.1.0", nil, tamperWith(forgeArgs),
			KindHashMismatch, "does not match the SHA-256 from the launch arguments inside the verified Forge installer"},
		{"the patched jar does not match Forge's hash", "26.2-65.1.0", nil, tamperWith(forgePatched),
			KindHashMismatch, "The file " + forgePatched + " does not match the SHA-1 from the install profile inside the verified Forge installer"},
		{"the patched jar was not built", "26.2-65.1.0", nil, func(t *testing.T, in *forgeInstall) { in.runInstaller(t, forgePatched) },
			KindMissingFile, "The server software file " + forgePatched + " is missing."},
		{"the installer is for another version", "1.21.1-52.1.0", nil, nil,
			KindMalformed, `it installs "1.21.1-forge-52.1.0" for Minecraft "1.21.1", not Forge 65.1.0 for Minecraft 26.2.`},
		{"the installer expects Mojang's jar elsewhere", "26.2-65.1.0", func(p map[string]any) {
			p["serverJarPath"] = "{LIBRARY_DIR}/net/minecraft/server/{MINECRAFT_VERSION}/server-{MINECRAFT_VERSION}.jar"
		}, run, KindMalformed, "it expects Mojang's server jar somewhere other than libraries/net/minecraft/server/26.2/server-26.2-bundled.jar."},
		{"the installer puts its launch arguments elsewhere", "26.2-65.1.0", processorArgs(func(args []any) {
			for i, a := range args {
				if a == "{ROOT}/"+forgeArgs {
					args[i] = "{ROOT}/unix_args.txt"
				}
			}
		}), run, KindMalformed, "it does not put its launch arguments at " + forgeArgs + "."},
		{"the installer starts another jar than its shim", "26.2-65.1.0", func(p map[string]any) { p["path"] = "net.minecraftforge:forge:26.2-65.1.0:universal" }, run,
			KindMalformed, "it does not start the server from its shim jar, forge-26.2-65.1.0-shim.jar."},
		{"the installer lists a library outside the libraries folder", "26.2-65.1.0", func(p map[string]any) {
			obj(p["libraries"].([]any)[0], "downloads", "artifact")["path"] = "../../../etc/cron.d/evil.jar"
		}, run, KindMalformed, `its library "com.github.jponge:lzma-java:1.3" has no valid path, SHA-1 or size.`},
		{"the installer publishes no hash for the patched jar", "26.2-65.1.0", func(p map[string]any) { delete(obj(p, "data"), "PATCHED_SHA") }, run,
			KindMalformed, "its install profile publishes no SHA-1 for the patched Minecraft jar."},
		{"the installer names an unsafe output", "26.2-65.1.0", func(p map[string]any) { obj(p, "data", "PATCHED")["server"] = "[../../x:y:z]" }, run,
			KindMalformed, "its install profile names an invalid file or SHA-1 for PATCHED."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newForgeInstall(t, "26.2", "65.1.0", tt.fixture, tt.edit)
			if tt.install != nil {
				tt.install(t, in)
			}
			_, err := Finish(in.dir, in.plan)
			if e := wantKind(t, err, tt.kind); !strings.Contains(e.Msg, tt.msg) {
				t.Errorf("got %q, want %q", e.Msg, tt.msg)
			}
			if !exists(in.dir, "forge-26.2-65.1.0-installer.jar") {
				t.Error("the installer was removed although the install failed")
			}
		})
	}
}

func TestStartsJar(t *testing.T) {
	for args, want := range map[string]bool{
		"-Djava.net.preferIPv6Addresses=system -jar forge-shim.jar": true,
		"-XX:+UseCompactObjectHeaders\n-jar\tforge-shim.jar\n":      true,
		"-jar other.jar":                     false,
		"-jar forge-shim.jar -jar other.jar": false,
		"-cp libraries/a.jar net.minecraftforge.bootstrap.ForgeBootstrap": false,
		"-jar": false,
	} {
		if got := startsJar(args, "forge-shim.jar"); got != want {
			t.Errorf("startsJar(%q) = %v, want %v", args, got, want)
		}
	}
}
