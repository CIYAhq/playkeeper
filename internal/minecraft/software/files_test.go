package software

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// testLibrary is a download as Fabric's metadata describes one.
func testLibrary(t *testing.T, f *fakeNet) (Artifact, []byte) {
	t.Helper()
	body := zipOf(t, "com/example/Lib.class", "library code")
	a := Artifact{URL: "https://maven.fabricmc.net/com/example/lib/1.0/lib-1.0.jar", Path: "libraries/com/example/lib/1.0/lib-1.0.jar",
		Size: int64(len(body)), Hash: Hash{Algorithm: SHA512, Value: hexSum(SHA512, body)}, Source: "Fabric's metadata API",
		Upstream: "Fabric", Hosts: []string{"maven.fabricmc.net"}}
	f.serve(a.URL, body)
	return a, body
}

// leftovers lists files under dir other than want, such as partial
// downloads.
func leftovers(t *testing.T, dir string, want ...string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		for _, w := range want {
			if filepath.ToSlash(rel) == w {
				return nil
			}
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDownload(t *testing.T) {
	ctx := context.Background()

	t.Run("keeps a file that matches", func(t *testing.T) {
		f := newFakeNet(t)
		a, body := testLibrary(t, f)
		dir := t.TempDir()
		if err := Download(ctx, f.client(), dir, a); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, filepath.FromSlash(a.Path))
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("got %d bytes, %v", len(got), err)
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o644 {
			t.Errorf("mode %v, want 0644", fi.Mode().Perm())
		}
		if l := leftovers(t, dir, a.Path); l != nil {
			t.Errorf("left %q behind", l)
		}
	})

	t.Run("does not download a matching file again", func(t *testing.T) {
		f := newFakeNet(t)
		a, _ := testLibrary(t, f)
		dir := t.TempDir()
		for range 2 {
			if err := Download(ctx, f.client(), dir, a); err != nil {
				t.Fatal(err)
			}
		}
		if n := f.hitCount(a.URL); n != 1 {
			t.Errorf("downloaded %d times, want 1", n)
		}
	})

	t.Run("replaces a file that does not match", func(t *testing.T) {
		f := newFakeNet(t)
		a, body := testLibrary(t, f)
		dir := t.TempDir()
		writeData(t, dir, a.Path, []byte("changed by a plugin"))
		if err := Download(ctx, f.client(), dir, a); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(a.Path))); !bytes.Equal(got, body) {
			t.Error("the file was not replaced")
		}
	})

	refused := []struct {
		name   string
		change func(a *Artifact, f *fakeNet, body []byte)
		kind   Kind
		msg    string
	}{
		{"tampered file", func(a *Artifact, f *fakeNet, body []byte) {
			f.serve(a.URL, append(body[:len(body)-1:len(body)-1], body[len(body)-1]^0xff))
		}, KindHashMismatch, "The downloaded lib-1.0.jar does not match the SHA-512 from Fabric's metadata API"},
		{"file of another size", func(a *Artifact, f *fakeNet, body []byte) { a.Size++ }, KindSizeMismatch,
			" bytes, but Fabric's metadata API says it should be "},
		{"file that streams past its size", func(a *Artifact, f *fakeNet, body []byte) {
			a.Size--
			f.handle(a.URL, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(body[:10])
				w.(http.Flusher).Flush()
				_, _ = w.Write(body[10:])
			})
		}, KindSizeMismatch, " bytes Fabric's metadata API says it should be. It was not used."},
		{"file larger than Playkeeper downloads", func(a *Artifact, f *fakeNet, body []byte) {
			a.Size = 0
			f.handle(a.URL, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", strconv.Itoa(maxArtifact+1))
				w.WriteHeader(http.StatusOK)
			})
		}, KindTooLarge, "The file lib-1.0.jar from Fabric is larger than the 256 MiB Playkeeper allows for one file, so it was not used."},
		{"listed size larger than Playkeeper downloads", func(a *Artifact, f *fakeNet, body []byte) { a.Size = maxArtifact + 1 }, KindTooLarge,
			"more than the 256 MiB Playkeeper downloads for one file."},
		{"negative listed size", func(a *Artifact, f *fakeNet, body []byte) { a.Size = -1 }, KindMalformed,
			"The size listed for lib-1.0.jar is not valid, so Playkeeper will not download it."},
		{"no valid checksum", func(a *Artifact, f *fakeNet, body []byte) { a.Hash.Value = "abc" }, KindMalformed,
			"Playkeeper has no valid checksum for lib-1.0.jar, so it will not download it."},
		{"another host", func(a *Artifact, f *fakeNet, body []byte) { a.URL = "https://evil.example.com/lib-1.0.jar" }, KindHostNotAllowed,
			"Fabric pointed Playkeeper to https://evil.example.com for lib-1.0.jar."},
		{"plain HTTP", func(a *Artifact, f *fakeNet, body []byte) { a.URL = strings.Replace(a.URL, "https:", "http:", 1) }, KindHostNotAllowed,
			"http://maven.fabricmc.net"},
		{"redirect to another host", func(a *Artifact, f *fakeNet, body []byte) {
			f.handle(a.URL, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "https://evil.example.com/lib-1.0.jar", http.StatusFound)
			})
		}, KindHostNotAllowed, "Fabric pointed Playkeeper to https://evil.example.com for lib-1.0.jar."},
		{"rate limited", func(a *Artifact, f *fakeNet, body []byte) { f.status(a.URL, 429) }, KindRateLimited,
			"Fabric is limiting requests from this host, so Playkeeper could not load lib-1.0.jar (HTTP 429)."},
		{"missing upstream", func(a *Artifact, f *fakeNet, body []byte) { f.status(a.URL, 404) }, KindNotFound, "Fabric does not have lib-1.0.jar."},
	}
	for _, tt := range refused {
		t.Run("refuses a "+tt.name, func(t *testing.T) {
			f := newFakeNet(t)
			a, body := testLibrary(t, f)
			f.handle("https://evil.example.com/lib-1.0.jar", func(w http.ResponseWriter, r *http.Request) { t.Error("downloaded from another host") })
			tt.change(&a, f, body)
			dir := t.TempDir()
			e := wantKind(t, Download(ctx, f.client(), dir, a), tt.kind)
			if !strings.Contains(e.Msg, tt.msg) {
				t.Errorf("got %q, want it to contain %q", e.Msg, tt.msg)
			}
			if l := leftovers(t, dir); l != nil {
				t.Errorf("left %q behind", l)
			}
		})
	}

	t.Run("refuses unsafe paths", func(t *testing.T) {
		f := newFakeNet(t)
		a, _ := testLibrary(t, f)
		for _, p := range []string{"../lib.jar", "/etc/lib.jar", "libraries/../../lib.jar", "lib.sh", ".lib.jar", "libraries/.git/lib.jar",
			`libraries\lib.jar`, "libraries//lib.jar", "", strings.Repeat("a", 600) + ".jar", "-lib.jar", "lib.jar/"} {
			a.Path = p
			wantKind(t, Download(ctx, f.client(), t.TempDir(), a), KindUnsafePath)
		}
		if n := f.hitCount(a.URL); n != 0 {
			t.Errorf("downloaded %d times", n)
		}
	})

	t.Run("refuses to follow a symbolic link", func(t *testing.T) {
		f := newFakeNet(t)
		a, _ := testLibrary(t, f)
		dir, outside := t.TempDir(), t.TempDir()
		if err := os.Symlink(outside, filepath.Join(dir, "libraries")); err != nil {
			t.Fatal(err)
		}
		e := wantKind(t, Download(ctx, f.client(), dir, a), KindUnsafePath)
		if !strings.Contains(e.Msg, "part of it is a symbolic link") {
			t.Errorf("got %q", e.Msg)
		}
		if l := leftovers(t, outside); l != nil {
			t.Errorf("wrote %q through the link", l)
		}
	})

	t.Run("refuses a file where a folder should be", func(t *testing.T) {
		f := newFakeNet(t)
		a, _ := testLibrary(t, f)
		dir := t.TempDir()
		writeData(t, dir, "libraries", []byte("not a folder"))
		e := wantKind(t, Download(ctx, f.client(), dir, a), KindUnsafePath)
		if !strings.Contains(e.Msg, "part of it is not a folder") {
			t.Errorf("got %q", e.Msg)
		}
	})
}

func TestCleanRel(t *testing.T) {
	for _, p := range []string{"server.jar", "cache/mojang_26.2.jar", "libraries/net/fabricmc/fabric-loader/0.19.5/fabric-loader-0.19.5.jar",
		"libraries/org/quiltmc/quilt-json5/1.0.4+final/quilt-json5-1.0.4+final.jar"} {
		if !cleanRel(p, ".jar") {
			t.Errorf("cleanRel(%q) = false", p)
		}
	}
	for _, p := range []string{"", "/server.jar", "../server.jar", "a/../server.jar", "./server.jar", "a//b.jar", "a/", ".hidden.jar", "a/.b/c.jar",
		`a\b.jar`, "a\x00.jar", "-rf.jar", "a b.jar", "a/" + strings.Repeat("b", 161) + ".jar", strings.Repeat("a/", 260) + "b.jar", "server.jar.sh", "x.properties"} {
		if cleanRel(p, ".jar") {
			t.Errorf("cleanRel(%q) = true", p)
		}
	}
}

func TestReadZipEntry(t *testing.T) {
	dir := t.TempDir()
	writeData(t, dir, "twice.jar", zipOf(t, "META-INF/MANIFEST.MF", "a", "META-INF/MANIFEST.MF", "b"))
	writeData(t, dir, "big.jar", zipOf(t, "META-INF/MANIFEST.MF", strings.Repeat("x", 2048)))
	writeData(t, dir, "text.jar", []byte("not a zip"))
	writeData(t, dir, "ok.jar", zipOf(t, "META-INF/MANIFEST.MF", "Manifest-Version: 1.0\r\n"))
	if err := os.Symlink(filepath.Join(dir, "ok.jar"), filepath.Join(dir, "link.jar")); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		jar, entry string
		kind       Kind
		msg        string
	}{
		{"twice.jar", "META-INF/MANIFEST.MF", KindMalformed, "Playkeeper could not read twice.jar: it contains META-INF/MANIFEST.MF twice."},
		{"big.jar", "META-INF/MANIFEST.MF", KindMalformed, "Playkeeper could not read big.jar: its META-INF/MANIFEST.MF is larger than 1 KiB."},
		{"text.jar", "META-INF/MANIFEST.MF", KindMalformed, "Playkeeper could not read text.jar: it is not a readable jar."},
		{"ok.jar", "META-INF/download-context", KindMalformed, "Playkeeper could not read ok.jar: it has no META-INF/download-context."},
		{"link.jar", "META-INF/MANIFEST.MF", KindUnsafePath, "part of it is a symbolic link"},
	}
	for _, tt := range tests {
		_, err := readZipEntry(dir, tt.jar, tt.entry, 1024)
		if e := wantKind(t, err, tt.kind); !strings.Contains(e.Msg, tt.msg) {
			t.Errorf("%s: got %q, want %q", tt.jar, e.Msg, tt.msg)
		}
	}
	if b, err := readZipEntry(dir, "ok.jar", "META-INF/MANIFEST.MF", 1024); err != nil || string(b) != "Manifest-Version: 1.0\r\n" {
		t.Errorf("got %q, %v", b, err)
	}
}

func TestBundlerList(t *testing.T) {
	sum := hexSum(SHA256, []byte("brigadier"))
	tests := []struct {
		name, list string
		ok         bool
	}{
		{"valid", sum + "\tcom.mojang:brigadier:1.3.10\tcom/mojang/brigadier/1.3.10/brigadier-1.3.10.jar\n", true},
		{"uppercase hash", strings.ToUpper(sum) + "\tcom.mojang:brigadier:1.3.10\tcom/mojang/brigadier/1.3.10/brigadier-1.3.10.jar\r\n", true},
		{"two fields", sum + "\tcom/mojang/brigadier/1.3.10/brigadier-1.3.10.jar\n", false},
		{"path outside the libraries", sum + "\tevil\t../../../etc/cron.d/evil.jar\n", false},
		{"absolute path", sum + "\tevil\t/etc/evil.jar\n", false},
		{"short hash", sum[:40] + "\tcom.mojang:brigadier:1.3.10\tcom/mojang/brigadier/1.3.10/brigadier-1.3.10.jar\n", false},
	}
	for _, tt := range tests {
		dir := t.TempDir()
		writeData(t, dir, "server.jar", zipOf(t, "META-INF/libraries.list", tt.list))
		got, err := bundlerList(dir, "server.jar", "META-INF/libraries.list")
		if !tt.ok {
			wantKind(t, err, KindMalformed)
			continue
		}
		if err != nil || len(got) != 1 || got[0].sha256 != sum || got[0].path != "com/mojang/brigadier/1.3.10/brigadier-1.3.10.jar" {
			t.Errorf("%s: got %+v, %v", tt.name, got, err)
		}
	}
}

func TestMavenPath(t *testing.T) {
	for coord, want := range map[string]string{
		"net.fabricmc:fabric-loader:0.19.5":                                "net/fabricmc/fabric-loader/0.19.5/fabric-loader-0.19.5.jar",
		"net.neoforged:mergetool:2.0.7:api":                                "net/neoforged/mergetool/2.0.7/mergetool-2.0.7-api.jar",
		"net.neoforged:neoform:1.21.1-20240808.144430:mappings@txt":        "net/neoforged/neoform/1.21.1-20240808.144430/neoform-1.21.1-20240808.144430-mappings.txt",
		"org.quiltmc:quilt-json5:1.0.4+final":                              "org/quiltmc/quilt-json5/1.0.4+final/quilt-json5-1.0.4+final.jar",
		"net.neoforged:minecraft-server-patched:26.2.0.88":                 "net/neoforged/minecraft-server-patched/26.2.0.88/minecraft-server-patched-26.2.0.88.jar",
		"net.minecraft:server:1.21.1-20240808.144430:srg":                  "net/minecraft/server/1.21.1-20240808.144430/server-1.21.1-20240808.144430-srg.jar",
		"net.fabricmc:sponge-mixin:0.17.4+mixin.0.8.7":                     "net/fabricmc/sponge-mixin/0.17.4+mixin.0.8.7/sponge-mixin-0.17.4+mixin.0.8.7.jar",
		"org.ow2.asm:asm:9.10.1":                                           "org/ow2/asm/asm/9.10.1/asm-9.10.1.jar",
		"net.neoforged:neoforge:21.1.251:server":                           "net/neoforged/neoforge/21.1.251/neoforge-21.1.251-server.jar",
		"net.minecraft:server:1.21.1-20240808.144430:mappings@txt":         "net/minecraft/server/1.21.1-20240808.144430/server-1.21.1-20240808.144430-mappings.txt",
		"net.neoforged:neoform:1.21.1-20240808.144430:mappings-merged@txt": "net/neoforged/neoform/1.21.1-20240808.144430/neoform-1.21.1-20240808.144430-mappings-merged.txt",
	} {
		if got, ok := mavenPath(coord); !ok || got != want {
			t.Errorf("mavenPath(%q) = %q, %v; want %q", coord, got, ok, want)
		}
	}
	for _, coord := range []string{"a:b", "a:b:c:d:e", "a:b:..", "..:b:c", "a:b:c@../x", "a:b/c:1", "a:b:c:", "a.:b:c", "a:b:c@", ":b:c", "a:b:1 2"} {
		if got, ok := mavenPath(coord); ok {
			t.Errorf("mavenPath(%q) = %q, want it refused", coord, got)
		}
	}
}

func TestManifestAttribute(t *testing.T) {
	m := "Manifest-Version: 1.0\r\nMain-Class: net.fabricmc.loader.impl.launch.server.FabricServ\r\n erLauncher\r\nclass-path: a.jar\r\n  b.jar\r\n\r\nName: x\r\nMain-Class: wrong\r\n"
	if got := manifestAttribute([]byte(m), "Main-Class"); got != "net.fabricmc.loader.impl.launch.server.FabricServerLauncher" {
		t.Errorf("Main-Class = %q", got)
	}
	if got := manifestAttribute([]byte(m), "Class-Path"); got != "a.jar b.jar" {
		t.Errorf("Class-Path = %q", got)
	}
	if got := manifestAttribute([]byte("Manifest-Version: 1.0\n\nMain-Class: late\n"), "Main-Class"); got != "" {
		t.Errorf("read %q from a later section", got)
	}
}

// readJar reads every entry of a jar, in order.
func readJar(t *testing.T, p string) ([]string, map[string]string) {
	t.Helper()
	r, err := zip.OpenReader(p)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var names []string
	out := map[string]string{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, f.Name)
		out[f.Name] = string(b)
	}
	return names, out
}

func TestLaunchJar(t *testing.T) {
	var classPath []string
	for i := range 30 {
		classPath = append(classPath, "libraries/org/example/library-"+strconv.Itoa(i)+"/1.0.0/library-"+strconv.Itoa(i)+"-1.0.0.jar")
	}
	jar, err := launchJar("net.fabricmc.loader.impl.launch.server.FabricServerLauncher", classPath, "fabric-server-launch.properties",
		"launch.mainClass=net.fabricmc.loader.impl.launch.knot.KnotServer\n")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := launchJar("net.fabricmc.loader.impl.launch.server.FabricServerLauncher", classPath, "fabric-server-launch.properties",
		"launch.mainClass=net.fabricmc.loader.impl.launch.knot.KnotServer\n")
	if !bytes.Equal(jar, again) {
		t.Error("the launch jar is not the same byte for byte each time")
	}
	dir := t.TempDir()
	writeData(t, dir, "launch.jar", jar)
	names, files := readJar(t, filepath.Join(dir, "launch.jar"))
	if strings.Join(names, " ") != "META-INF/MANIFEST.MF fabric-server-launch.properties" {
		t.Errorf("entries %q", names)
	}
	m := files["META-INF/MANIFEST.MF"]
	if !strings.HasPrefix(m, "Manifest-Version: 1.0\r\nMain-Class: net.fabricmc.loader.impl.launch.server.FabricServerLauncher\r\nClass-Path: ") || !strings.HasSuffix(m, "\r\n\r\n") {
		t.Errorf("manifest %q", m)
	}
	for _, l := range strings.Split(m, "\r\n") {
		if len(l) > 72 {
			t.Errorf("manifest line of %d bytes: %q", len(l), l)
		}
	}
	if got := manifestAttribute([]byte(m), "Class-Path"); got != strings.Join(classPath, " ") {
		t.Errorf("Class-Path = %q", got)
	}
	if files["fabric-server-launch.properties"] != "launch.mainClass=net.fabricmc.loader.impl.launch.knot.KnotServer\n" {
		t.Errorf("properties %q", files["fabric-server-launch.properties"])
	}
}

func TestVerify(t *testing.T) {
	dir := t.TempDir()
	body := []byte("server jar")
	writeData(t, dir, "server.jar", body)
	writeData(t, dir, "libraries/lib.jar", body)
	if err := os.Symlink(filepath.Join(dir, "server.jar"), filepath.Join(dir, "link.jar")); err != nil {
		t.Fatal(err)
	}
	good := Check{Path: "server.jar", Hash: Hash{Algorithm: SHA1, Value: hexSum(SHA1, body)}, Size: int64(len(body)), Origin: Pinned, Source: "Mojang's version manifest"}
	if err := Verify(dir, []Check{good}); err != nil {
		t.Fatal(err)
	}
	with := func(change func(c *Check)) []Check {
		c := good
		change(&c)
		return []Check{good, c}
	}
	tests := []struct {
		name   string
		checks []Check
		kind   Kind
		msg    string
	}{
		{"missing file", with(func(c *Check) { c.Path = "minecraft_server.jar" }), KindMissingFile, "The server software file minecraft_server.jar is missing."},
		{"other content", with(func(c *Check) { c.Hash.Value = hexSum(SHA1, []byte("other")) }), KindHashMismatch,
			"The file server.jar does not match the SHA-1 from Mojang's version manifest (got "},
		{"other size", with(func(c *Check) { c.Size++ }), KindSizeMismatch, "The file server.jar is 10 bytes, but Mojang's version manifest says it should be 11 bytes."},
		{"symbolic link", with(func(c *Check) { c.Path = "link.jar" }), KindUnsafePath, "part of it is a symbolic link"},
		{"folder", with(func(c *Check) { c.Path = "libraries" }), KindUnsafePath, "it is not a regular file"},
		{"no valid checksum", with(func(c *Check) { c.Hash.Algorithm = "crc32" }), KindMalformed, "Playkeeper has no valid checksum for server.jar."},
		{"unsafe path", with(func(c *Check) { c.Path = "../server.jar" }), KindUnsafePath, "it is not a plain relative path"},
	}
	for _, tt := range tests {
		e := wantKind(t, Verify(dir, tt.checks), tt.kind)
		if !strings.Contains(e.Msg, tt.msg) {
			t.Errorf("%s: got %q, want %q", tt.name, e.Msg, tt.msg)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "server.jar")); errors.Is(err, fs.ErrNotExist) {
		t.Error("Verify removed a file")
	}
}
