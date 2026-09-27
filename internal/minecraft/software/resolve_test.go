package software

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func neoforgeInstallerURL(v string) string {
	return neoforgeMaven + "/" + v + "/neoforge-" + v + "-installer.jar"
}

// resolveFake serves everything Resolve reads for a type, from the
// fixtures. Fabric keeps the SHA-512 hashes its metadata lists.
func resolveFake(t *testing.T, typeID string) *fakeNet {
	t.Helper()
	f := newFakeNet(t)
	serveMojang(t, f, mojangFiles(t))
	switch typeID {
	case Purpur:
		servePurpur(t, f)
	case Fabric:
		serveFabric(t, f, true)
	case Quilt:
		serveQuilt(t, f)
	case NeoForge:
		serveNeoForgeMetadata(t, f)
		f.serve(neoforgeInstallerURL("26.2.0.88")+".sha512", []byte(strings.Repeat("ab", 64)))
		f.serve(neoforgeInstallerURL("21.1.251")+".sha512", []byte(strings.Repeat("CD", 64)+"  neoforge-21.1.251-installer.jar\n"))
	}
	return f
}

func resolve(t *testing.T, f *fakeNet, pin Pin) (Resolved, Plan) {
	t.Helper()
	r, err := f.sources().Resolve(context.Background(), pin)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Plan()
	if err != nil {
		t.Fatal(err)
	}
	return r, p
}

// describeDownloads lists a plan's downloads as "path algorithm host
// (source)".
func describeDownloads(p Plan) []string {
	var out []string
	for _, a := range p.Downloads {
		u, _ := url.Parse(a.URL)
		out = append(out, fmt.Sprintf("%s %s %s (%s)", a.Path, a.Hash.Algorithm, u.Host, a.Source))
	}
	return out
}

func wantStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\ngot  %q\nwant %q", what, got, want)
	}
}

// checksDownloads reports whether a plan checks exactly its downloads,
// against the hashes their upstreams publish.
func checksDownloads(p Plan) bool {
	var want []Check
	for _, a := range p.Downloads {
		want = append(want, pinned(a))
	}
	return slices.Equal(p.Checks, want)
}

func TestResolveVanilla(t *testing.T) {
	r, p := resolve(t, resolveFake(t, Vanilla), Pin{Type: Vanilla, MinecraftVersion: "26.2"})
	want := Artifact{URL: "https://piston-data.mojang.com/v1/objects/823e2250d24b3ddac457a60c92a6a941943fcd6a/server.jar", Size: 60894273,
		Hash: Hash{Algorithm: SHA1, Value: "823e2250d24b3ddac457a60c92a6a941943fcd6a"}, Source: "Mojang's version manifest",
		Upstream: "Mojang", Hosts: []string{"piston-meta.mojang.com", "piston-data.mojang.com"}}
	if r.Java != 25 || !reflect.DeepEqual(r.Server, want) {
		t.Errorf("got Java %d and server %+v", r.Java, r.Server)
	}
	wantStrings(t, "downloads", describeDownloads(p), []string{"minecraft_server.26.2.jar sha1 piston-data.mojang.com (Mojang's version manifest)"})
	wantStrings(t, "run env", p.Run.Env, []string{"TYPE=CUSTOM", "CUSTOM_SERVER=/data/minecraft_server.26.2.jar"})
	if p.Setup != nil || p.Launcher != nil || p.Derive != nil || p.Record != nil || p.Remove != nil || !checksDownloads(p) {
		t.Errorf("Vanilla runs Mojang's jar as it is, got %+v", p)
	}
	if p.Assurance.Level != LevelFull || p.Pin != r.Pin {
		t.Errorf("got assurance %+v and pin %+v", p.Assurance, p.Pin)
	}
}

func TestResolvePurpur(t *testing.T) {
	r, p := resolve(t, resolveFake(t, Purpur), Pin{Type: Purpur, MinecraftVersion: "26.2", PurpurBuild: 2633})
	want := &Artifact{URL: "https://api.purpurmc.org/v2/purpur/26.2/2633/download", Path: "purpur-26.2-2633.jar",
		Hash: Hash{Algorithm: MD5, Value: "3f3c933f2d518af98571c7461fa42278"}, Source: "Purpur's build API", Upstream: "Purpur", Hosts: []string{"api.purpurmc.org"}}
	if !reflect.DeepEqual(r.Software, want) {
		t.Errorf("got %+v", r.Software)
	}
	wantStrings(t, "downloads", describeDownloads(p), []string{
		"purpur-26.2-2633.jar md5 api.purpurmc.org (Purpur's build API)",
		"cache/mojang_26.2.jar sha1 piston-data.mojang.com (Mojang's version manifest)",
	})
	wantStrings(t, "run env", p.Run.Env, []string{"TYPE=CUSTOM", "CUSTOM_SERVER=/data/purpur-26.2-2633.jar"})
	if !reflect.DeepEqual(p.Derive, []Derivation{{Kind: DerivePaperclip, From: "purpur-26.2-2633.jar", Into: "cache"}}) {
		t.Errorf("got derivations %+v", p.Derive)
	}
	if p.Setup != nil || p.Launcher != nil || !checksDownloads(p) || p.Assurance.Level != LevelWeakHash {
		t.Errorf("got %+v", p)
	}
}

func TestResolveFabric(t *testing.T) {
	f := resolveFake(t, Fabric)
	r, p := resolve(t, f, Pin{Type: Fabric, MinecraftVersion: "1.21.8", FabricLoader: "0.19.5"})
	const lib = "libraries/"
	wantStrings(t, "downloads", describeDownloads(p), []string{
		lib + "org/ow2/asm/asm/9.10.1/asm-9.10.1.jar sha512 maven.fabricmc.net (Fabric's metadata API)",
		lib + "org/ow2/asm/asm-analysis/9.10.1/asm-analysis-9.10.1.jar sha512 maven.fabricmc.net (Fabric's metadata API)",
		lib + "org/ow2/asm/asm-commons/9.10.1/asm-commons-9.10.1.jar sha512 maven.fabricmc.net (Fabric's metadata API)",
		lib + "org/ow2/asm/asm-tree/9.10.1/asm-tree-9.10.1.jar sha512 maven.fabricmc.net (Fabric's metadata API)",
		lib + "org/ow2/asm/asm-util/9.10.1/asm-util-9.10.1.jar sha512 maven.fabricmc.net (Fabric's metadata API)",
		lib + "net/fabricmc/sponge-mixin/0.17.4+mixin.0.8.7/sponge-mixin-0.17.4+mixin.0.8.7.jar sha512 maven.fabricmc.net (Fabric's metadata API)",
		lib + "net/fabricmc/intermediary/1.21.8/intermediary-1.21.8.jar sha512 maven.fabricmc.net (Fabric's Maven repository)",
		lib + "net/fabricmc/fabric-loader/0.19.5/fabric-loader-0.19.5.jar sha512 maven.fabricmc.net (Fabric's Maven repository)",
		"server.jar sha1 piston-data.mojang.com (Mojang's version manifest)",
	})

	var profile struct {
		Libraries []metaLibrary `json:"libraries"`
	}
	if err := json.Unmarshal(readFixture(t, "fabric/server-1.21.8-0.19.5.json"), &profile); err != nil {
		t.Fatal(err)
	}
	if got, want := r.Libraries[0], profile.Libraries[0]; got.Hash.Value != want.SHA512 || got.Size != want.Size {
		t.Errorf("asm has SHA-512 %s and size %d, want the %s and %d Fabric lists", short(got.Hash.Value), got.Size, short(want.SHA512), want.Size)
	}
	asm := "https://maven.fabricmc.net/org/ow2/asm/asm/9.10.1/asm-9.10.1.jar.sha512"
	loader := "https://maven.fabricmc.net/net/fabricmc/fabric-loader/0.19.5/fabric-loader-0.19.5.jar.sha512"
	if f.hitCount(asm) != 0 || f.hitCount(loader) != 1 {
		t.Errorf("loaded the SHA-512 files of asm %d and of the loader %d times, want 0 and 1", f.hitCount(asm), f.hitCount(loader))
	}

	wantLoader := lib + "net/fabricmc/fabric-loader/0.19.5/fabric-loader-0.19.5.jar"
	if r.Loader != wantLoader || r.LaunchMainClass != "net.fabricmc.loader.impl.launch.knot.KnotServer" {
		t.Errorf("got loader %q and main class %q", r.Loader, r.LaunchMainClass)
	}
	var classPath []string
	for _, a := range r.Libraries {
		classPath = append(classPath, a.Path)
	}
	wantLauncher := &Launcher{Path: "fabric-server-launch.jar", MainClassFrom: wantLoader, ClassPath: classPath,
		Properties: "fabric-server-launch.properties", PropertiesBody: "launch.mainClass=net.fabricmc.loader.impl.launch.knot.KnotServer\n"}
	if !reflect.DeepEqual(p.Launcher, wantLauncher) {
		t.Errorf("got launcher %+v", p.Launcher)
	}
	wantStrings(t, "recorded", p.Record, []string{"fabric-server-launch.jar"})
	wantStrings(t, "run env", p.Run.Env, []string{"TYPE=CUSTOM", "CUSTOM_SERVER=/data/fabric-server-launch.jar"})
	if p.Setup != nil || p.Derive != nil || !checksDownloads(p) || p.Assurance.Level != LevelFull {
		t.Errorf("got %+v", p)
	}
}

func TestResolveQuilt(t *testing.T) {
	r, p := resolve(t, resolveFake(t, Quilt), Pin{Type: Quilt, MinecraftVersion: "26.3", QuiltLoader: "0.30.1"})
	const lib = "libraries/"
	wantStrings(t, "downloads", describeDownloads(p), []string{
		lib + "net/fabricmc/sponge-mixin/0.17.3+mixin.0.8.7/sponge-mixin-0.17.3+mixin.0.8.7.jar sha512 maven.fabricmc.net (Fabric's Maven repository)",
		lib + "org/quiltmc/quilt-json5/1.0.4+final/quilt-json5-1.0.4+final.jar sha512 maven.quiltmc.org (Quilt's Maven repository)",
		lib + "org/ow2/asm/asm/9.10.1/asm-9.10.1.jar sha512 maven.fabricmc.net (Fabric's Maven repository)",
		lib + "org/ow2/asm/asm-analysis/9.10.1/asm-analysis-9.10.1.jar sha512 maven.fabricmc.net (Fabric's Maven repository)",
		lib + "org/ow2/asm/asm-commons/9.10.1/asm-commons-9.10.1.jar sha512 maven.fabricmc.net (Fabric's Maven repository)",
		lib + "org/ow2/asm/asm-tree/9.10.1/asm-tree-9.10.1.jar sha512 maven.fabricmc.net (Fabric's Maven repository)",
		lib + "org/ow2/asm/asm-util/9.10.1/asm-util-9.10.1.jar sha512 maven.fabricmc.net (Fabric's Maven repository)",
		lib + "org/quiltmc/quilt-config/1.3.3/quilt-config-1.3.3.jar sha512 maven.quiltmc.org (Quilt's Maven repository)",
		lib + "org/quiltmc/quilt-loader/0.30.1/quilt-loader-0.30.1.jar sha512 maven.quiltmc.org (Quilt's Maven repository)",
		"server.jar sha1 piston-data.mojang.com (Mojang's version manifest)",
	})
	if r.Loader != lib+"org/quiltmc/quilt-loader/0.30.1/quilt-loader-0.30.1.jar" || r.LauncherMainClass != "org.quiltmc.loader.impl.launch.server.QuiltServerLauncher" {
		t.Errorf("got loader %q and launcher class %q", r.Loader, r.LauncherMainClass)
	}
	if l := p.Launcher; l == nil || l.Path != "quilt-server-launch.jar" || l.MainClass != r.LauncherMainClass || l.MainClassFrom != "" || l.Properties != "" || len(l.ClassPath) != 9 {
		t.Errorf("got launcher %+v", p.Launcher)
	}
	wantStrings(t, "run env", p.Run.Env, []string{"TYPE=CUSTOM", "CUSTOM_SERVER=/data/quilt-server-launch.jar"})
	if !checksDownloads(p) || p.Assurance.Level != LevelFull {
		t.Errorf("got %+v", p)
	}
}

// Quilt's metadata lists hashes that do not match the files it serves, so
// the SHA-512 next to each file in the Maven repository wins.
func TestResolveQuiltIgnoresListedHashes(t *testing.T) {
	f := resolveFake(t, Quilt)
	serveLoaderProfile(t, f, quiltMeta+"/versions/loader/26.3/0.30.1/server/json",
		editJSON(t, readFixture(t, "quilt/server-26.3-0.30.1.json"), func(v any) any {
			for _, l := range obj(v)["libraries"].([]any) {
				l.(map[string]any)["sha512"], l.(map[string]any)["size"] = strings.Repeat("0", 128), 1
			}
			return v
		}), true)
	r, _ := resolve(t, f, Pin{Type: Quilt, MinecraftVersion: "26.3", QuiltLoader: "0.30.1"})
	for _, a := range r.Libraries {
		if a.Hash.Value == strings.Repeat("0", 128) || a.Size != 0 || !strings.HasSuffix(a.Source, "Maven repository") {
			t.Errorf("%s: got hash %s, size %d from %s", a.Path, short(a.Hash.Value), a.Size, a.Source)
		}
	}
	if got, want := r.Libraries[8].Hash.Value, hexSum(SHA512, fakeLibrary(t, "org.quiltmc:quilt-loader:0.30.1")); got != want {
		t.Errorf("the loader has SHA-512 %s, want %s", short(got), short(want))
	}
}

func TestResolveNeoForge(t *testing.T) {
	f := resolveFake(t, NeoForge)
	r, p := resolve(t, f, Pin{Type: NeoForge, MinecraftVersion: "26.2", NeoForgeVersion: "26.2.0.88"})
	want := &Artifact{URL: neoforgeInstallerURL("26.2.0.88"), Path: "neoforge-26.2.0.88-installer.jar",
		Hash: Hash{Algorithm: SHA512, Value: strings.Repeat("ab", 64)}, Source: "NeoForge's Maven repository", Upstream: "NeoForge", Hosts: []string{"maven.neoforged.net"}}
	if !reflect.DeepEqual(r.Software, want) {
		t.Errorf("got %+v", r.Software)
	}
	wantStrings(t, "downloads", describeDownloads(p), []string{
		"neoforge-26.2.0.88-installer.jar sha512 maven.neoforged.net (NeoForge's Maven repository)",
		"libraries/net/minecraft/server/26.2/server-26.2.jar sha1 piston-data.mojang.com (Mojang's version manifest)",
	})
	if p.Setup == nil {
		t.Fatal("NeoForge needs a setup container")
	}
	wantStrings(t, "setup env", p.Setup.Env, []string{"TYPE=NEOFORGE", "NEOFORGE_INSTALLER=/data/neoforge-26.2.0.88-installer.jar", "NEOFORGE_FORCE_REINSTALL=TRUE", "SETUP_ONLY=TRUE"})
	wantStrings(t, "run env", p.Run.Env, []string{"TYPE=CUSTOM", "CUSTOM_JAR_EXEC=@libraries/net/neoforged/neoforge/26.2.0.88/unix_args.txt"})
	wantStrings(t, "removed", p.Remove, []string{"neoforge-26.2.0.88-installer.jar", "neoforge-26.2.0.88-installer.jar.log"})
	wantDerive := []Derivation{
		{Kind: DeriveNeoForge, From: "neoforge-26.2.0.88-installer.jar", Into: "libraries"},
		{Kind: DeriveBundler, From: "libraries/net/minecraft/server/26.2/server-26.2.jar", Into: "libraries"},
	}
	if !reflect.DeepEqual(p.Derive, wantDerive) || p.Launcher != nil || !checksDownloads(p) || p.Assurance.Level != LevelRecorded {
		t.Errorf("got %+v", p)
	}

	r, p = resolve(t, f, Pin{Type: NeoForge, MinecraftVersion: "1.21.1", NeoForgeVersion: "21.1.251"})
	if r.Java != 21 || r.Software.Hash.Value != strings.Repeat("cd", 64) || p.Downloads[1].Path != "libraries/net/minecraft/server/1.21.1/server-1.21.1.jar" {
		t.Errorf("got Java %d, installer hash %s and server jar at %s", r.Java, short(r.Software.Hash.Value), p.Downloads[1].Path)
	}
}

func TestResolveErrors(t *testing.T) {
	vanilla := Pin{Type: Vanilla, MinecraftVersion: "26.2"}
	fabric := Pin{Type: Fabric, MinecraftVersion: "1.21.8", FabricLoader: "0.19.5"}
	quilt := Pin{Type: Quilt, MinecraftVersion: "26.3", QuiltLoader: "0.30.1"}
	neoforge := Pin{Type: NeoForge, MinecraftVersion: "26.2", NeoForgeVersion: "26.2.0.88"}
	editVersionFile := func(edit func(v map[string]any)) func(t *testing.T, f *fakeNet) {
		return func(t *testing.T, f *fakeNet) {
			files := mojangFiles(t)
			files["26.2"] = editJSON(t, files["26.2"], func(v any) any { edit(obj(v)); return v })
			serveMojang(t, f, files)
		}
	}
	serverURL := func(u string) func(t *testing.T, f *fakeNet) {
		return editVersionFile(func(v map[string]any) { obj(v, "downloads", "server")["url"] = u })
	}
	editProfile := func(profileURL, fixture string, edit func(p map[string]any)) func(t *testing.T, f *fakeNet) {
		return func(t *testing.T, f *fakeNet) {
			serveLoaderProfile(t, f, profileURL, editJSON(t, readFixture(t, fixture), func(v any) any { edit(obj(v)); return v }), true)
		}
	}
	editFabric := func(edit func(p map[string]any)) func(t *testing.T, f *fakeNet) {
		return editProfile(fabricMeta+"/versions/loader/1.21.8/0.19.5/server/json", "fabric/server-1.21.8-0.19.5.json", edit)
	}
	library := func(p map[string]any, i int) map[string]any { return p["libraries"].([]any)[i].(map[string]any) }
	editPurpur := func(build string, edit func(b map[string]any)) func(t *testing.T, f *fakeNet) {
		return func(t *testing.T, f *fakeNet) {
			f.serve(purpurAPI+"/26.2/"+build, editJSON(t, readFixture(t, "purpur/26.2-2633.json"), func(v any) any { edit(obj(v)); return v }))
		}
	}
	const fabricLoaderURL = "https://maven.fabricmc.net/net/fabricmc/fabric-loader/0.19.5/fabric-loader-0.19.5.jar"

	tests := []struct {
		name  string
		pin   Pin
		setup func(t *testing.T, f *fakeNet)
		kind  Kind
		msg   string
	}{
		{"Mojang version file does not match its manifest", vanilla, func(t *testing.T, f *fakeNet) {
			f.serve("https://piston-meta.mojang.com/v1/packages/33c420747ce582e48dff1d8c5d8e67e5bb6257c9/26.2.json", append(readFixture(t, "mojang/26.2.json"), ' '))
		}, KindHashMismatch, "Mojang's version file for Minecraft 26.2 does not match the SHA-1 in Mojang's version manifest"},
		{"server jar on another host", vanilla, serverURL("https://evil.example.com/server.jar"), KindHostNotAllowed,
			"Mojang pointed Playkeeper to https://evil.example.com for the server jar of Minecraft 26.2. Playkeeper only downloads from piston-meta.mojang.com, piston-data.mojang.com over HTTPS, so it refused."},
		{"server jar over plain HTTP", vanilla, serverURL("http://piston-data.mojang.com/v1/objects/823e/server.jar"), KindHostNotAllowed, "http://piston-data.mojang.com"},
		{"server jar behind credentials", vanilla, serverURL("https://user:secret@piston-data.mojang.com/server.jar"), KindHostNotAllowed, "so it refused"},
		{"server jar on another port", vanilla, serverURL("https://piston-data.mojang.com:8443/server.jar"), KindHostNotAllowed, "https://piston-data.mojang.com:8443"},
		{"server jar without a size", vanilla, editVersionFile(func(v map[string]any) { obj(v, "downloads", "server")["size"] = 0 }), KindMalformed,
			"it lists no server jar with a SHA-1 and size"},
		{"version file for another version", vanilla, editVersionFile(func(v map[string]any) { v["id"] = "26.1" }), KindMalformed, `it describes version "26.1"`},
		{"release that needs a newer Java", Pin{Type: Vanilla, MinecraftVersion: needsJava27}, func(t *testing.T, f *fakeNet) {
			files := mojangFiles(t)
			files[needsJava27] = mojangVersionFile(t, f, needsJava27, 27, []byte("server"))
			serveMojang(t, f, files, mojangEntry{ID: needsJava27, Type: "release", URL: "https://piston-meta.mojang.com/v1/packages/26.4.json"})
		}, KindUnsupported, "Minecraft 26.4 needs Java 27; this Playkeeper runs Java 25."},
		{"not a Mojang release", Pin{Type: Vanilla, MinecraftVersion: "1.21.99"}, nil, KindNotFound, "Mojang does not list Minecraft 1.21.99 as a release."},
		{"Mojang limits requests", vanilla, func(t *testing.T, f *fakeNet) { f.status(mojangManifestURL, 429) }, KindRateLimited, "(HTTP 429)"},
		{"Purpur build that failed", Pin{Type: Purpur, MinecraftVersion: "26.2", PurpurBuild: 2625},
			editPurpur("2625", func(b map[string]any) { b["build"], b["result"], b["md5"] = "2625", "FAILURE", nil }), KindNotFound,
			"Purpur build 2625 for Minecraft 26.2 did not finish, so it has no jar to download."},
		{"Purpur build it does not have", Pin{Type: Purpur, MinecraftVersion: "26.2", PurpurBuild: 9999}, nil, KindNotFound, "Purpur does not have build 9999 for Minecraft 26.2."},
		{"Purpur answers with another build", Pin{Type: Purpur, MinecraftVersion: "26.2", PurpurBuild: 2632}, editPurpur("2632", func(map[string]any) {}), KindMalformed,
			`it describes build "2633" for Minecraft "26.2"`},
		{"Purpur build without an MD5", Pin{Type: Purpur, MinecraftVersion: "26.2", PurpurBuild: 2633}, editPurpur("2633", func(b map[string]any) { b["md5"] = "" }),
			KindMalformed, "it has no valid MD5 checksum"},
		{"Fabric library on another host", fabric, editFabric(func(p map[string]any) { library(p, 0)["url"] = "https://evil.example.com/maven/" }), KindHostNotAllowed,
			"Fabric pointed Playkeeper to https://evil.example.com for asm-9.10.1.jar."},
		{"Fabric library that is not a Maven coordinate", fabric, editFabric(func(p map[string]any) { library(p, 0)["name"] = "org.ow2.asm:asm:../../../x" }),
			KindMalformed, "is not a valid Maven coordinate"},
		{"Fabric profile for another Minecraft version", fabric, editFabric(func(p map[string]any) { p["inheritsFrom"] = "1.21.7" }), KindMalformed,
			`it is for Minecraft "1.21.7"`},
		{"Fabric profile without the loader", fabric, editFabric(func(p map[string]any) { p["libraries"] = p["libraries"].([]any)[:7] }), KindMalformed,
			"it does not include net.fabricmc:fabric-loader:0.19.5"},
		{"Fabric profile without a main class", fabric, editFabric(func(p map[string]any) { p["mainClass"] = "" }), KindMalformed, "it names no valid main class"},
		{"Fabric loader checksum unreadable", fabric, func(t *testing.T, f *fakeNet) { f.serve(fabricLoaderURL+".sha512", []byte("<html>not found</html>")) },
			KindMalformed, "Fabric sent the SHA-512 of fabric-loader-0.19.5.jar in a form Playkeeper could not read."},
		{"Fabric loader checksum missing", fabric, func(t *testing.T, f *fakeNet) { f.status(fabricLoaderURL+".sha512", 404) }, KindNotFound,
			"Fabric does not have the SHA-512 of fabric-loader-0.19.5.jar."},
		{"Fabric loader too old for a code-free launch jar", Pin{Type: Fabric, MinecraftVersion: "1.21.8", FabricLoader: "0.12.5"}, nil, KindUnsupported,
			`"0.12.5" is not a Fabric loader version Playkeeper can run`},
		{"Quilt library over plain HTTP", quilt, editProfile(quiltMeta+"/versions/loader/26.3/0.30.1/server/json", "quilt/server-26.3-0.30.1.json", func(p map[string]any) {
			library(p, 1)["url"] = "http://maven.quiltmc.org/repository/release/"
		}), KindHostNotAllowed, "Quilt pointed Playkeeper to http://maven.quiltmc.org for quilt-json5-1.0.4+final.jar."},
		{"Quilt profile without a launcher class", quilt, editProfile(quiltMeta+"/versions/loader/26.3/0.30.1/server/json", "quilt/server-26.3-0.30.1.json", func(p map[string]any) {
			delete(p, "launcherMainClass")
		}), KindMalformed, "it names no valid launcher class"},
		{"NeoForge version it does not have", Pin{Type: NeoForge, MinecraftVersion: "26.2", NeoForgeVersion: "26.2.0.99"}, nil, KindNotFound, "NeoForge has no version 26.2.0.99."},
		{"NeoForge limits requests", neoforge, func(t *testing.T, f *fakeNet) { f.status(neoforgeInstallerURL("26.2.0.88")+".sha512", 429) }, KindRateLimited,
			"NeoForge is limiting requests from this host"},
		{"NeoForge checksum too long", neoforge, func(t *testing.T, f *fakeNet) {
			f.serve(neoforgeInstallerURL("26.2.0.88")+".sha512", []byte(strings.Repeat("ab", 1024)))
		}, KindTooLarge, "NeoForge sent more than 1 KiB for the SHA-512 of neoforge-26.2.0.88-installer.jar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := resolveFake(t, tt.pin.Type)
			if tt.setup != nil {
				tt.setup(t, f)
			}
			_, err := f.sources().Resolve(context.Background(), tt.pin)
			e := wantKind(t, err, tt.kind)
			if !strings.Contains(e.Msg, tt.msg) {
				t.Errorf("got %q, want it to contain %q", e.Msg, tt.msg)
			}
			if strings.Contains(e.Msg+e.Hint+fmt.Sprint(e.Params), "secret") {
				t.Errorf("the error shows the credentials in the URL: %q %v", e.Msg, e.Params)
			}
		})
	}
}

func TestResolveChecksThePinFirst(t *testing.T) {
	f := resolveFake(t, Fabric)
	_, err := f.sources().Resolve(context.Background(), Pin{Type: Fabric, MinecraftVersion: "26.2", FabricLoader: "0.19.5/../../../x"})
	wantKind(t, err, KindUnsupported)
	if n := f.hitCount(mojangManifestURL); n != 0 {
		t.Errorf("loaded Mojang's manifest %d times for a pin that is not valid", n)
	}
}
