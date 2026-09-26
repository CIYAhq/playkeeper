package share

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
	"github.com/CIYAhq/playkeeper/internal/modpacks"
	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
)

func TestShareOfAPackAndTheUsersMods(t *testing.T) {
	f := newFake(t)
	sh := build(t, f.builder(), f.setup())

	if n := sh.Notice; n.Key != "share.notice.pack_one" || n.Text != "Friends need the pack plus Waystones" ||
		n.Params["pack"] != "Adrenaline" || n.Params["mod"] != "Waystones" {
		t.Errorf("notice: %+v", n)
	}
	wantPack := &Pack{Name: "Adrenaline", Version: "26.5.0+mc26.2.fabric", Source: addons.Modrinth,
		Page: "https://modrinth.com/modpack/adrenaline/version/EZaeTUP8", Need: Required, Label: Required.Label()}
	if !reflect.DeepEqual(sh.Pack, wantPack) {
		t.Errorf("pack: %+v", sh.Pack)
	}
	if sh.Key != f.setup().Key() || sh.Server != "Alex's server" || sh.Type != "fabric" || sh.MinecraftVersion != "26.2" || sh.LoaderVersion != "0.19.5" {
		t.Errorf("share: key %s, server %q, %s %s %s", sh.Key, sh.Server, sh.Type, sh.MinecraftVersion, sh.LoaderVersion)
	}

	mods := modsByPath(sh.Mods)
	for _, c := range []struct {
		path, name string
		from       From
		need       Need
		onServer   bool
	}{
		// Waystones works on both sides. Balm and Shogi alone could stay
		// on the server, but Waystones needs them.
		{"mods/waystones-fabric-26.2-26.2.0.12.jar", "Waystones", FromUser, Required, true},
		{"mods/balm-fabric-26.2-26.2.0.9.jar", "Balm", FromUser, Required, true},
		{"mods/shogi-fabric-26.2-26.2.0.7.jar", "Shogi", FromUser, Required, true},
		{"mods/Chunky-Fabric-1.5.3.jar", "Chunky", FromUser, Optional, true},
		{"mods/polymer-bundled-0.17.5+26.2.jar", "Polymer", FromUser, Optional, true},
		{"mods/voicechat-fabric-2.6.24+26.2.jar", "Simple Voice Chat", FromUser, Optional, true},
		{"mods/spark-1.10.187-fabric.jar", "spark", FromUser, ServerOnly, true},
		{"mods/better-fabric-console-mc26.1.2-2.0.1.jar", "Better Fabric Console", FromUser, ServerOnly, true},
		// The pack's env decides for its files: players get Sodium, and
		// ServerCore too, though Modrinth calls ServerCore server-only.
		{"mods/sodium-fabric-0.9.2+mc26.2.jar", "Sodium", FromPack, Required, false},
		{"mods/servercore-fabric-1.5.19+26.2.jar", "ServerCore", FromPack, Required, true},
		{"mods/fabric-api-0.161.0+26.2.jar", "Fabric API", FromPack, Required, true},
		{"mods/krypton-0.3.1.jar", "Krypton", FromPack, ServerOnly, true},
		{"mods/vmp-fabric-mc26.2-0.2.0+beta.7.236-all.jar", "Very Many Players (Fabric)", FromPack, ServerOnly, true},
	} {
		m, ok := mods[c.path]
		if !ok || m.Name != c.name || m.From != c.from || m.Need != c.need || m.Label.Key != c.need.Label().Key || m.OnServer != c.onServer ||
			m.InFile != (c.need != ServerOnly) || m.ByHand {
			t.Errorf("%s: %+v", c.path, m)
		}
	}
	if w := mods["mods/waystones-fabric-26.2-26.2.0.12.jar"]; w.Source != addons.Modrinth || w.Project != "LOpKHB2A" ||
		w.Version != "26.2.0.12+fabric-26.2" || w.Page != "https://modrinth.com/mod/waystones" || w.DependencyOf != "" {
		t.Errorf("Waystones: %+v", w)
	}
	if b := mods["mods/balm-fabric-26.2-26.2.0.9.jar"]; b.DependencyOf != "LOpKHB2A" {
		t.Errorf("Balm: %+v", b)
	}
	if s := mods["mods/sodium-fabric-0.9.2+mc26.2.jar"]; s.Source != addons.Modrinth || s.Project != "AANobbMI" ||
		s.Version != "mc26.2-0.9.2-fabric" || s.Page != "https://modrinth.com/mod/sodium" {
		t.Errorf("Sodium: %+v", s)
	}

	if len(sh.Mods) != 38 {
		t.Errorf("%d mods, want the user's 8 and the pack's 30", len(sh.Mods))
	}
	var first []string
	for _, m := range sh.Mods[:8] {
		first = append(first, m.Name)
	}
	if want := []string{"Balm", "Better Fabric Console", "Chunky", "Polymer", "Shogi", "Simple Voice Chat", "spark", "Waystones"}; !slices.Equal(first, want) {
		t.Errorf("the user's mods come first, by name: %q", first)
	}
	for _, m := range sh.Mods[8:] {
		if m.From != FromPack {
			t.Errorf("%s is among the pack's mods", m.Name)
		}
	}
	if !slices.IsSortedFunc(sh.Mods[8:], func(a, b Mod) int { return compareNames(a.Name, b.Name) }) {
		t.Error("the pack's mods are not sorted by name")
	}

	ix := sh.Index
	if ix.FormatVersion != 1 || ix.Game != "minecraft" || ix.Name != "Alex's server" || ix.Summary != "The mods for playing on Alex's server." ||
		!maps.Equal(ix.Dependencies, map[string]string{"minecraft": "26.2", "fabric-loader": "0.19.5"}) ||
		!regexp.MustCompile(`^26\.2-[0-9a-f]{8}$`).MatchString(ix.VersionID) {
		t.Errorf("index: %+v", ix)
	}
	if len(ix.Files) != 34 || !slices.IsSortedFunc(ix.Files, func(a, b mrpack.File) int { return strings.Compare(a.Path, b.Path) }) {
		t.Errorf("%d files, want the pack's 28 for players and 6 of the user's, by path", len(ix.Files))
	}
	files := filesByPath(ix.Files)
	for p, m := range mods {
		if _, ok := files[p]; ok != m.InFile {
			t.Errorf("%s: in the file %t, marked %t", p, ok, m.InFile)
		}
	}
	for p, env := range map[string]mrpack.Env{
		"mods/waystones-fabric-26.2-26.2.0.12.jar": {Client: mrpack.Required, Server: mrpack.Required},
		"mods/Chunky-Fabric-1.5.3.jar":             {Client: mrpack.Optional, Server: mrpack.Required},
		"mods/sodium-fabric-0.9.2+mc26.2.jar":      {Client: mrpack.Required, Server: mrpack.Unsupported},
		"mods/servercore-fabric-1.5.19+26.2.jar":   {Client: mrpack.Required, Server: mrpack.Required},
	} {
		if got := files[p].Env; got == nil || *got != env {
			t.Errorf("%s: env %+v, want %+v", p, got, env)
		}
	}
	for p, id := range map[string]string{
		"mods/waystones-fabric-26.2-26.2.0.12.jar": waystones,
		"mods/sodium-fabric-0.9.2+mc26.2.jar":      sodium,
		"mods/fabric-api-0.161.0+26.2.jar":         fabricAPI,
	} {
		want, got := f.file(id), files[p]
		if got.Hashes.SHA1 != want.Hashes.SHA1 || got.Hashes.SHA512 != want.Hashes.SHA512 || got.FileSize != want.Size ||
			!slices.Equal(got.Downloads, []string{want.URL}) {
			t.Errorf("%s: %+v, want Modrinth's file %+v", p, got, want)
		}
	}
	if len(sh.Yourself) != 0 {
		t.Errorf("Modrinth has every file, but friends are to get %+v", sh.Yourself)
	}
}

// The file holds modrinth.index.json and nothing else: no overrides, no
// configs, nothing from the server's folder.
func TestTheFileHoldsOnlyTheIndex(t *testing.T) {
	f := newFake(t)
	sh := build(t, f.builder(), f.setup())
	b, err := sh.File()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 1 {
		var names []string
		for _, e := range zr.File {
			names = append(names, e.Name)
		}
		t.Fatalf("the file holds %q", names)
	}
	e := zr.File[0]
	if e.Name != mrpack.IndexName || e.Method != zip.Store || e.Flags != 0 || len(e.Extra) != 0 || e.Comment != "" || zr.Comment != "" ||
		!e.Modified.Equal(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("entry: %+v", e.FileHeader)
	}
	rc, err := e.Open()
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	ix, err := mrpack.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*ix, sh.Index) {
		t.Error("the file's index is not the share's")
	}
	for _, file := range ix.Files {
		if !modpacks.PlayerContent(file.Path) {
			t.Errorf("the file has %s, which is not a mod, resource pack or shader pack", file.Path)
		}
	}
}

func TestTheSameSetupGivesTheSameFile(t *testing.T) {
	f := newFake(t)
	b := f.builder()
	first := build(t, b, f.setup())

	// The same setup, listed in another order.
	again := f.setup()
	slices.Reverse(again.Addons)
	slices.Reverse(again.Pack.Files)
	slices.Reverse(again.Pack.Client)
	second := build(t, b, again)
	if first.Key != second.Key || !reflect.DeepEqual(first, second) {
		t.Error("the same setup in another order gave another share")
	}

	a, _ := first.File()
	a2, _ := first.File()
	c, _ := second.File()
	if !bytes.Equal(a, a2) || !bytes.Equal(a, c) {
		t.Error("the same setup gave different bytes")
	}
	// A share kept as JSON gives the same file.
	js, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	var kept Share
	if err := json.Unmarshal(js, &kept); err != nil {
		t.Fatal(err)
	}
	if k, _ := kept.File(); !bytes.Equal(a, k) {
		t.Error("a share read back from JSON gave different bytes")
	}

	for name, change := range map[string]func(*Setup){
		"an add-on removed":      func(s *Setup) { s.Addons = s.Addons[1:] },
		"another loader version": func(s *Setup) { s.LoaderVersion = "0.19.6" },
		"a pack file turned off": func(s *Setup) {
			s.Pack.Excluded = append(s.Pack.Excluded, modpacks.Excluded{Path: "mods/sodium-fabric-0.9.2+mc26.2.jar", Project: "AANobbMI"})
		},
		"another server name": func(s *Setup) { s.Name = "Alex's other server" },
	} {
		s := f.setup()
		change(&s)
		if s.Key() == first.Key {
			t.Errorf("%s: the key stayed the same", name)
		}
	}

	// Each build asks about the files once, and about their projects once.
	want := []string{"POST version_files sha512 38", "GET projects 38", "POST version_files sha512 38", "GET projects 38"}
	if got := f.log(); !slices.Equal(got, want) {
		t.Errorf("requests: %q", got)
	}
}

// Friends' launchers download only from Modrinth's CDN, and the pages the
// share sends friends to are on Modrinth, CurseForge or the hosts Modrinth
// allows in packs.
func TestLinksOnlyGoToAllowedHosts(t *testing.T) {
	f := newFake(t)
	f.edit(chunky, func(v obj) {
		file := v["files"].([]any)[0].(obj)
		file["url"] = strings.Replace(file["url"].(string), "cdn.modrinth.com", "cdn.modrinth.com.example.net", 1)
	})
	s := f.setup()
	s.Pack.Client = append(s.Pack.Client,
		clientFile("mods/plain-http-1.0.jar", "plain http", "http://cdn.modrinth.com/data/PLAInhtt/versions/PLAIver1/plain-http-1.0.jar"),
		clientFile("mods/ported-1.0.jar", "ported", "https://cdn.modrinth.com:8443/data/PORTed01/versions/PORTver1/ported-1.0.jar"),
		clientFile("mods/forgecdn-1.0.jar", "forgecdn", "https://mediafilez.forgecdn.net/files/1/2/forgecdn-1.0.jar"),
		clientFile("mods/github-1.0.jar", "github", "https://github.com/example/github-mod/releases/download/v1.0/github-1.0.jar"),
	)
	sh := build(t, f.builder(), s)

	for _, file := range sh.Index.Files {
		if len(file.Downloads) != 1 {
			t.Errorf("%s: downloads %q", file.Path, file.Downloads)
			continue
		}
		u, err := url.Parse(file.Downloads[0])
		if err != nil || u.Scheme != "https" || u.Host != modrinth.CDNHost || !strings.HasPrefix(u.Path, "/data/") || u.RawQuery != "" || u.User != nil {
			t.Errorf("%s: download %s", file.Path, file.Downloads[0])
		}
	}
	allowed := []string{"modrinth.com", "www.curseforge.com", "github.com", "raw.githubusercontent.com", "gitlab.com"}
	var pages []string
	for _, m := range sh.Mods {
		pages = append(pages, m.Page)
	}
	for _, y := range sh.Yourself {
		pages = append(pages, y.Page)
	}
	for _, p := range pages {
		if p == "" {
			continue
		}
		if u, err := url.Parse(p); err != nil || u.Scheme != "https" || u.Port() != "" || !slices.Contains(allowed, u.Host) {
			t.Errorf("a link to %s", p)
		}
	}

	yourself := map[string]Yourself{}
	for _, y := range sh.Yourself {
		yourself[y.Path] = y
	}
	if len(yourself) != 5 {
		t.Errorf("friends get %d files themselves, want Chunky and the four the pack lists", len(yourself))
	}
	if y := yourself["mods/Chunky-Fabric-1.5.3.jar"]; y.Reason.Key != "share.yourself.not_found" || y.Page != "https://modrinth.com/mod/chunky" || y.Need != Optional {
		t.Errorf("Chunky: %+v", y)
	}
	if y := yourself["mods/github-1.0.jar"]; y.Reason.Key != "share.yourself.other_host" ||
		y.Page != "https://github.com/example/github-mod/releases/download/v1.0/github-1.0.jar" {
		t.Errorf("the file on GitHub: %+v", y)
	}
	for _, p := range []string{"mods/plain-http-1.0.jar", "mods/ported-1.0.jar", "mods/forgecdn-1.0.jar"} {
		if y := yourself[p]; y.Reason.Key != "share.yourself.not_found" || y.Page != "" {
			t.Errorf("%s: %+v", p, y)
		}
	}
}

// A mod both the user and the pack have goes into the file once, as the
// user's copy, and needs what the most needed copy needs. Here the add-on
// library put in Fabric API for Waystones at the pack's path, and the user
// kept their own Cloth Config under another name.
func TestTheUsersCopyWins(t *testing.T) {
	f := newFake(t)
	s := f.setup()
	cloth := f.added(clothConfig, "")
	cloth.FileName = "cloth-config-fabric-26.2.155.jar"
	s.Addons = append(s.Addons, f.added(fabricAPI, "LOpKHB2A"), cloth)
	sh := build(t, f.builder(), s)

	copies := 0
	for _, m := range sh.Mods {
		if m.Project != "P7dR8mSH" && m.Project != "9s6osm5g" {
			continue
		}
		copies++
		if m.InFile != (m.From == FromUser) || m.ByHand || m.Need != Required {
			t.Errorf("%s from %s: %+v", m.Name, m.From, m)
		}
	}
	if copies != 4 {
		t.Errorf("%d copies of Fabric API and Cloth Config in the list, want the user's and the pack's of each", copies)
	}
	files := filesByPath(sh.Index.Files)
	if len(files) != 34 {
		t.Errorf("%d files, want the same 34", len(files))
	}
	if _, ok := files["mods/cloth-config-26.2.155.jar"]; ok {
		t.Error("the pack's copy of Cloth Config is in the file too")
	}
	// The pack gives players Cloth Config, so the user's copy is required.
	if c, ok := files["mods/cloth-config-fabric-26.2.155.jar"]; !ok || c.Env == nil || c.Env.Client != mrpack.Required {
		t.Errorf("the user's Cloth Config: %+v", c)
	}
	if n := sh.Notice; n.Text != "Friends need the pack plus Waystones" {
		t.Errorf("notice: %+v", n)
	}
}

// clientFile is a pack's file for players that only the pack knows about,
// with content's hashes, needed by players only.
func clientFile(path, content string, downloads ...string) modpacks.ClientFile {
	return modpacks.ClientFile{Path: path, SHA1: sha1hex([]byte(content)), SHA512: sha512hex([]byte(content)), Size: int64(len(content)),
		Origin: modpacks.Download, Env: &mrpack.Env{Client: mrpack.Required, Server: mrpack.Unsupported}, Downloads: downloads}
}

// modrinthFile is a Modrinth pack's file for players, as its index lists
// Modrinth's file.
func modrinthFile(path string, file modrinth.File, project string, client, server mrpack.Support) modpacks.ClientFile {
	return modpacks.ClientFile{Path: path, SHA1: file.Hashes.SHA1, SHA512: file.Hashes.SHA512, Size: file.Size, Origin: modpacks.Download,
		Env: &mrpack.Env{Client: client, Server: server}, Downloads: []string{file.URL}, Project: project}
}

func serverFile(path, sha512 string, size int64, project string) modpacks.File {
	return modpacks.File{Path: path, HashAlgo: "sha512", Hash: sha512, Size: size, Origin: modpacks.Download, Project: project}
}

func TestModsFriendsGetThemselves(t *testing.T) {
	f := newFake(t)
	sodiumFile, chunkyFile, fpsFile, menuFile, coreFile := f.file(sodium), f.file(chunky), f.file(dynamicFPS), f.file(modMenu), f.file(serverCore)
	look := []byte("a resource pack only the pack's archive has")
	mystery := clientFile("mods/mystery-1.0.jar", "mystery", "https://cdn.modrinth.com/data/MYSTery1/versions/MYSTver1/mystery-1.0.jar")
	mystery.Env.Client, mystery.Project = mrpack.Unknown, "MYSTery1"
	r := &modpacks.Record{
		Pack: addons.Installed{Source: addons.Modrinth, ProjectID: "TSTpk001", Slug: "test-pack", Name: "Test Pack", VersionID: "TSTv0001",
			VersionNumber: "1.0.0", HashAlgo: "sha512"},
		Requirements: modpacks.Requirements{Type: "fabric", MinecraftVersion: "26.2", LoaderVersion: "0.19.5"},
		Client: []modpacks.ClientFile{
			// Not on Modrinth; the pack downloads it from GitHub.
			clientFile("mods/example-mod-1.0.jar", "example mod", "https://github.com/example/example-mod/releases/download/v1.0/example-mod-1.0.jar"),
			// Copied out of the pack's archive, and not on Modrinth.
			{Path: "resourcepacks/Test Look.zip", SHA1: sha1hex(look), SHA512: sha512hex(look), Size: int64(len(look)), Origin: modpacks.Override},
			// Copied out of the archive, but Modrinth has the same file.
			{Path: "mods/sodium-fabric-0.9.2+mc26.2.jar", SHA1: sodiumFile.Hashes.SHA1, SHA512: sodiumFile.Hashes.SHA512, Size: sodiumFile.Size,
				Origin: modpacks.Override},
			// In the archive, too large to hash within the limits.
			{Path: "shaderpacks/huge-shaders.zip", Size: 300 << 20, Origin: modpacks.Override},
			// The pack doesn't say; Modrinth does.
			modrinthFile("mods/Chunky-Fabric-1.5.3.jar", chunkyFile, "fALzjamp", mrpack.Unknown, mrpack.Required),
			// The pack says optional, though Modrinth says client-only.
			modrinthFile("mods/dynamic-fps-3.11.9+minecraft-26.2.0-fabric.jar", fpsFile, "LQ3K71Q1", mrpack.Optional, mrpack.Unsupported),
			// Turned off by the user.
			modrinthFile("mods/modmenu-20.0.2.jar", menuFile, "mOgUt4GM", mrpack.Required, mrpack.Unsupported),
			// Neither the pack nor Modrinth, which has no such file, says.
			mystery,
			// Not mods, resource packs or shader packs. Records never list
			// them for players, and shares never take them.
			clientFile("config/test-pack.json", "{}", "https://github.com/example/test-pack/raw/main/config/test-pack.json"),
			clientFile("options.txt", "fov:90", "https://github.com/example/test-pack/raw/main/options.txt"),
			clientFile("mods/nested/deep-1.0.jar", "deep", "https://github.com/example/test-pack/raw/main/mods/nested/deep-1.0.jar"),
		},
		Files: []modpacks.File{
			serverFile("mods/Chunky-Fabric-1.5.3.jar", chunkyFile.Hashes.SHA512, chunkyFile.Size, "fALzjamp"),
			serverFile("mods/servercore-fabric-1.5.19+26.2.jar", coreFile.Hashes.SHA512, coreFile.Size, "4WWQxlQP"),
			{Path: "config/test-pack.json", HashAlgo: "sha512", Hash: sha512hex([]byte("{}")), Size: 2, Origin: modpacks.Override},
		},
		Excluded: []modpacks.Excluded{{Path: "mods/modmenu-20.0.2.jar", Project: "mOgUt4GM"}},
	}
	old := []byte("an older Waystones")
	s := Setup{Name: "Test server", Type: "fabric", MinecraftVersion: "26.2", LoaderVersion: "0.19.5", Pack: r, Addons: []addons.Installed{
		// A version Modrinth no longer lists.
		{Source: addons.Modrinth, ProjectID: "LOpKHB2A", Slug: "waystones", Name: "Waystones", VersionID: "OLDway01", VersionNumber: "26.2.0.1+fabric-26.2",
			FileName: "waystones-fabric-26.2-26.2.0.1.jar", HashAlgo: "sha512", Hash: sha512hex(old), Size: int64(len(old)),
			Requires: []string{"MBAkmtvl", "bi4iCmsw", "P7dR8mSH"}},
		// A plugin from when the server ran Paper.
		{Source: addons.Hangar, ProjectID: "ViaVersion", Slug: "ViaVersion", Name: "ViaVersion", VersionID: "5.4.1", VersionNumber: "5.4.1",
			FileName: "ViaVersion-5.4.1.jar", HashAlgo: "sha256", Hash: strings.Repeat("ab", 32), Size: 5 << 20},
	}}
	sh := build(t, f.builder(), s)

	packPage := "https://modrinth.com/modpack/test-pack/version/TSTv0001"
	want := []struct {
		name, path, page string
		need             Need
		key, text        string
	}{
		{"example-mod-1.0", "mods/example-mod-1.0.jar", "https://github.com/example/example-mod/releases/download/v1.0/example-mod-1.0.jar", Required,
			"share.yourself.other_host", "example-mod-1.0 isn't on Modrinth. Download it from its link and put it in the game's mods folder."},
		{"huge-shaders", "shaderpacks/huge-shaders.zip", packPage, Required, "share.yourself.inside_pack",
			"huge-shaders only comes inside Test Pack's own download. Get it from the pack's page and put it in the game's shaderpacks folder."},
		{"Test Look", "resourcepacks/Test Look.zip", packPage, Required, "share.yourself.inside_pack",
			"Test Look only comes inside Test Pack's own download. Get it from the pack's page and put it in the game's resourcepacks folder."},
		{"Waystones", "mods/waystones-fabric-26.2-26.2.0.1.jar", "https://modrinth.com/mod/waystones", Required, "share.yourself.not_found",
			"Modrinth doesn't have this version of Waystones. Download it from its page and put it in the game's mods folder."},
	}
	if len(sh.Yourself) != len(want) {
		t.Errorf("friends get %d files themselves: %+v", len(sh.Yourself), sh.Yourself)
	}
	for i, w := range want {
		if i >= len(sh.Yourself) {
			break
		}
		y := sh.Yourself[i]
		if y.Name != w.name || y.Path != w.path || y.Page != w.page || y.Need != w.need || y.Reason.Key != w.key || y.Reason.Text != w.text {
			t.Errorf("%d: %+v, want %+v", i, y, w)
		}
	}

	files := filesByPath(sh.Index.Files)
	if len(files) != 4 {
		t.Errorf("the file has %d files, want Sodium, Chunky, Dynamic FPS and the mystery mod", len(files))
	}
	for p, c := range map[string]struct {
		env  mrpack.Env
		want modrinth.File
	}{
		"mods/sodium-fabric-0.9.2+mc26.2.jar":                 {mrpack.Env{Client: mrpack.Required, Server: mrpack.Unsupported}, sodiumFile},
		"mods/Chunky-Fabric-1.5.3.jar":                        {mrpack.Env{Client: mrpack.Optional, Server: mrpack.Required}, chunkyFile},
		"mods/dynamic-fps-3.11.9+minecraft-26.2.0-fabric.jar": {mrpack.Env{Client: mrpack.Optional, Server: mrpack.Unsupported}, fpsFile},
		"mods/mystery-1.0.jar": {mrpack.Env{Client: mrpack.Optional, Server: mrpack.Unsupported},
			modrinth.File{URL: mystery.Downloads[0], Size: mystery.Size, Hashes: modrinth.Hashes{SHA1: mystery.SHA1, SHA512: mystery.SHA512}}},
	} {
		got, ok := files[p]
		if !ok || got.Env == nil || *got.Env != c.env || !slices.Equal(got.Downloads, []string{c.want.URL}) || got.FileSize != c.want.Size ||
			got.Hashes.SHA1 != c.want.Hashes.SHA1 || got.Hashes.SHA512 != c.want.Hashes.SHA512 {
			t.Errorf("%s: %+v", p, got)
		}
	}

	mods := modsByPath(sh.Mods)
	for p, c := range map[string]struct {
		name     string
		need     Need
		inFile   bool
		onServer bool
	}{
		"plugins/ViaVersion-5.4.1.jar":                        {"ViaVersion", ServerOnly, false, true},
		"mods/waystones-fabric-26.2-26.2.0.1.jar":             {"Waystones", Required, false, true},
		"mods/example-mod-1.0.jar":                            {"example-mod-1.0", Required, false, false},
		"resourcepacks/Test Look.zip":                         {"Test Look", Required, false, false},
		"shaderpacks/huge-shaders.zip":                        {"huge-shaders", Required, false, false},
		"mods/sodium-fabric-0.9.2+mc26.2.jar":                 {"Sodium", Required, true, false},
		"mods/Chunky-Fabric-1.5.3.jar":                        {"Chunky", Optional, true, true},
		"mods/dynamic-fps-3.11.9+minecraft-26.2.0-fabric.jar": {"Dynamic FPS", Optional, true, false},
		"mods/mystery-1.0.jar":                                {"mystery-1.0", Unknown, true, false},
		"mods/servercore-fabric-1.5.19+26.2.jar":              {"ServerCore", ServerOnly, false, true},
	} {
		m, ok := mods[p]
		if !ok || m.Name != c.name || m.Need != c.need || m.Label.Key != c.need.Label().Key || m.InFile != c.inFile ||
			m.ByHand != (!c.inFile && c.need != ServerOnly) || m.OnServer != c.onServer {
			t.Errorf("%s: %+v", p, m)
		}
	}
	if len(sh.Mods) != 10 {
		var paths []string
		for _, m := range sh.Mods {
			paths = append(paths, m.Path)
		}
		t.Errorf("mods: %q", paths)
	}
	if n := sh.Notice; n.Key != "share.notice.pack_one" || n.Text != "Friends need the pack plus Waystones" {
		t.Errorf("notice: %+v", n)
	}
}

// A CurseForge pack's files say what friends need only where CurseForge's
// tags do; the rest is Modrinth's word for the same file, or Unknown.
// CurseForge files are never linked, only the same files on Modrinth.
func TestCurseForgePack(t *testing.T) {
	f := newFake(t)
	api, ferrite, cull := f.file(fabricAPI), f.file(ferriteCore), f.file(entityCull)
	both := []string{"client", "server"}
	cf := func(path, sha1 string, size int64, project, fileID, name, version, page string, sides []string, required bool) modpacks.ClientFile {
		return modpacks.ClientFile{Path: path, SHA1: sha1, Size: size, Origin: modpacks.Download, Project: project, FileID: fileID, Name: name,
			Version: version, Page: page, Required: required, Sides: sides}
	}
	mods := "https://www.curseforge.com/minecraft/mc-mods/"
	tweaks := []byte("the pack's own tweaks")
	r := &modpacks.Record{
		Pack: addons.Installed{Source: modpacks.CurseForge, ProjectID: "900123", Slug: "friends-fabric-pack", Name: "Friends Fabric Pack",
			VersionID: "7000001", VersionNumber: "Friends Fabric Pack 1.0", HashAlgo: "sha1"},
		Requirements: modpacks.Requirements{Type: "fabric", MinecraftVersion: "26.2", LoaderVersion: "0.19.5"},
		Client: []modpacks.ClientFile{
			// The same file as on Modrinth, tagged for both sides.
			cf("mods/fabric-api-0.161.0+26.2.jar", api.Hashes.SHA1, api.Size, "306612", "8913512", "Fabric API", "0.161.0+26.2",
				mods+"fabric-api/files/8913512", both, true),
			// Tagged for players, and not on Modrinth.
			cf("mods/sodium-fabric-0.9.2+mc26.2.jar", sha1hex([]byte("Sodium as CurseForge has it")), 1885572, "394468", "8888037", "Sodium",
				"mc26.2-0.9.2", mods+"sodium/files/8888037", []string{"client"}, true),
			// Tagged for both sides, and not on Modrinth: nobody says.
			cf("mods/lithium-fabric-0.25.3+mc26.2.jar", sha1hex([]byte("Lithium as CurseForge has it")), 912850, "360438", "8895969", "Lithium",
				"mc26.2-0.25.3", mods+"lithium/files/8895969", both, true),
			// Not tagged, and on Modrinth, which says either side will do.
			cf("mods/ferritecore-9.0.0-fabric.jar", ferrite.Hashes.SHA1, ferrite.Size, "459857", "7806040", "FerriteCore", "9.0.0",
				mods+"ferritecore/files/7806040", nil, true),
			// Tagged for the server.
			cf("mods/servercore-fabric-1.5.19+26.2.jar", sha1hex([]byte("ServerCore as CurseForge has it")), 909932, "550579", "8800001",
				"ServerCore", "1.5.19+26.2", mods+"servercore/files/8800001", []string{"server"}, true),
			// Tagged for both sides, client-only on Modrinth, and optional in
			// the pack.
			cf("mods/entityculling-fabric-1.11.2-mc26.2.jar", cull.Hashes.SHA1, cull.Size, "448233", "8870001", "Entity Culling", "1.11.2",
				mods+"entityculling/files/8870001", both, false),
			// An optional mod for players, with a page that only looks like
			// CurseForge's.
			cf("mods/zoomify-2.14.6+26.2.jar", sha1hex([]byte("Zoomify")), 1200000, "574741", "8870002", "Zoomify", "2.14.6",
				"https://www.curseforge.com.example.net/minecraft/mc-mods/zoomify/files/8870002", []string{"client"}, false),
			// An optional resource pack; CurseForge doesn't tag these.
			cf("resourcepacks/Translations-for-Sodium-26.2.zip", sha1hex([]byte("Translations for Sodium")), 40000, "867964", "8917489",
				"Translations for Sodium", "26.2", "https://www.curseforge.com/minecraft/texture-packs/translations-for-sodium/files/8917489", nil, false),
			// In the pack's archive.
			{Path: "mods/pack-tweaks-1.0.jar", SHA1: sha1hex(tweaks), SHA512: sha512hex(tweaks), Size: int64(len(tweaks)), Origin: modpacks.Override},
		},
	}
	sh := build(t, f.builder(), Setup{Name: "Friends", Type: "fabric", MinecraftVersion: "26.2", LoaderVersion: "0.19.5", Pack: r})

	if sh.Pack.Source != modpacks.CurseForge || sh.Pack.Need != Required || sh.Pack.Page != "https://www.curseforge.com/minecraft/modpacks/friends-fabric-pack/files/7000001" {
		t.Errorf("pack: %+v", sh.Pack)
	}
	if n := sh.Notice; n.Key != "share.notice.pack" || n.Text != "Friends need the pack" {
		t.Errorf("notice: %+v", n)
	}
	got := modsByPath(sh.Mods)
	for p, need := range map[string]Need{
		"mods/fabric-api-0.161.0+26.2.jar":               Optional,
		"mods/sodium-fabric-0.9.2+mc26.2.jar":            Required,
		"mods/lithium-fabric-0.25.3+mc26.2.jar":          Unknown,
		"mods/ferritecore-9.0.0-fabric.jar":              ServerOnly,
		"mods/servercore-fabric-1.5.19+26.2.jar":         ServerOnly,
		"mods/entityculling-fabric-1.11.2-mc26.2.jar":    Optional,
		"mods/zoomify-2.14.6+26.2.jar":                   Optional,
		"resourcepacks/Translations-for-Sodium-26.2.zip": Optional,
		"mods/pack-tweaks-1.0.jar":                       Required,
	} {
		if m := got[p]; m.Need != need {
			t.Errorf("%s: %s, want %s", p, m.Need, need)
		}
	}
	if len(sh.Mods) != 9 {
		t.Errorf("%d mods", len(sh.Mods))
	}
	if m := got["mods/fabric-api-0.161.0+26.2.jar"]; m.Name != "Fabric API" || m.Version != "0.161.0+26.2" || m.Source != modpacks.CurseForge ||
		m.Project != "306612" || m.Page != mods+"fabric-api/files/8913512" || !m.InFile {
		t.Errorf("Fabric API: %+v", m)
	}

	files := filesByPath(sh.Index.Files)
	if len(files) != 2 {
		t.Errorf("the file has %d files, want Fabric API and Entity Culling from Modrinth", len(files))
	}
	for p, want := range map[string]modrinth.File{"mods/fabric-api-0.161.0+26.2.jar": api, "mods/entityculling-fabric-1.11.2-mc26.2.jar": cull} {
		got := files[p]
		if !slices.Equal(got.Downloads, []string{want.URL}) || got.Hashes.SHA1 != want.Hashes.SHA1 || got.Hashes.SHA512 != want.Hashes.SHA512 ||
			got.Env == nil || got.Env.Client != mrpack.Optional {
			t.Errorf("%s: %+v", p, got)
		}
	}

	wantYourself := []struct {
		name, page string
		need       Need
		text       string
	}{
		{"Lithium", mods + "lithium/files/8895969", Unknown, "Lithium comes from CurseForge. Download it there and put it in the game's mods folder."},
		{"pack-tweaks-1.0", "https://www.curseforge.com/minecraft/modpacks/friends-fabric-pack/files/7000001", Required,
			"pack-tweaks-1.0 only comes inside Friends Fabric Pack's own download. Get it from the pack's page and put it in the game's mods folder."},
		{"Sodium", mods + "sodium/files/8888037", Required, "Sodium comes from CurseForge. Download it there and put it in the game's mods folder."},
		{"Translations for Sodium", "https://www.curseforge.com/minecraft/texture-packs/translations-for-sodium/files/8917489", Optional,
			"Translations for Sodium comes from CurseForge. Download it there and put it in the game's resourcepacks folder."},
		{"Zoomify", "", Optional, "Zoomify comes from CurseForge. Download it there and put it in the game's mods folder."},
	}
	if len(sh.Yourself) != len(wantYourself) {
		t.Fatalf("friends get %+v themselves", sh.Yourself)
	}
	for i, w := range wantYourself {
		if y := sh.Yourself[i]; y.Name != w.name || y.Page != w.page || y.Need != w.need || y.Reason.Text != w.text {
			t.Errorf("%d: %+v, want %+v", i, y, w)
		}
	}

	want := []string{"POST version_files sha512 1", "POST version_files sha1 8", "GET projects 3"}
	if log := f.log(); !slices.Equal(log, want) {
		t.Errorf("requests: %q", log)
	}
}

func TestNotice(t *testing.T) {
	user := func(name, project, dependencyOf string, need Need) *item {
		return &item{Mod: Mod{Name: name, Path: "mods/" + project + ".jar", From: FromUser, Project: project, DependencyOf: dependencyOf, Need: need}}
	}
	pack := func(need Need) *item { return &item{Mod: Mod{Name: "A pack mod", From: FromPack, Need: need}} }
	waystones := user("Waystones", "LOpKHB2A", "", Required)
	balm := user("Balm", "MBAkmtvl", "LOpKHB2A", Required)
	create := user("Create", "LNytGWDc", "", Required)
	delight := user("Farmer's Delight", "R2OftAxM", "", Required)
	required, optional, serverOnly := &Pack{Name: "Adrenaline", Need: Required}, &Pack{Name: "Adrenaline", Need: Optional}, &Pack{Name: "Adrenaline", Need: ServerOnly}
	for _, c := range []struct {
		name  string
		pack  *Pack
		items []*item
		key   string
		text  string
	}{
		{"nothing", nil, nil, "share.notice.none", "Friends can join without mods"},
		{"server-only mods", nil, []*item{user("spark", "l6YH9Als", "", ServerOnly)}, "share.notice.none", "Friends can join without mods"},
		{"an optional mod", nil, []*item{user("Chunky", "fALzjamp", "", Optional)}, "share.notice.optional", "Friends can join without mods, and some are optional"},
		{"a mod nobody knows about", nil, []*item{user("Mystery", "MYSTery1", "", Unknown)}, "share.notice.optional", "Friends can join without mods, and some are optional"},
		{"one mod and its dependency", nil, []*item{balm, waystones}, "share.notice.one", "Friends need Waystones"},
		{"two mods", nil, []*item{waystones, balm, create}, "share.notice.two", "Friends need Create and Waystones"},
		{"three mods", nil, []*item{waystones, delight, create}, "share.notice.more", "Friends need Create and 2 other mods"},
		{"the pack", required, []*item{pack(Required)}, "share.notice.pack", "Friends need the pack"},
		{"the pack and a mod", required, []*item{pack(Required), waystones, balm}, "share.notice.pack_one", "Friends need the pack plus Waystones"},
		{"the pack and two mods", required, []*item{waystones, create}, "share.notice.pack_two", "Friends need the pack plus Create and Waystones"},
		{"the pack and three mods", required, []*item{delight, waystones, create}, "share.notice.pack_more", "Friends need the pack plus Create and 2 other mods"},
		{"an optional pack and a mod", optional, []*item{pack(Optional), waystones}, "share.notice.one", "Friends need Waystones"},
		{"an optional pack", optional, []*item{pack(Optional)}, "share.notice.optional", "Friends can join without mods, and some are optional"},
		{"a server-only pack", serverOnly, []*item{pack(ServerOnly)}, "share.notice.none", "Friends can join without mods"},
		// Balm is named when Waystones, which it was added for, stays on
		// the server; mods the user chose come first.
		{"a dependency of a mod friends don't need", nil, []*item{user("Waystones", "LOpKHB2A", "", Optional), balm, create},
			"share.notice.two", "Friends need Create and Balm"},
	} {
		got := notice(c.pack, c.items)
		if got.Key != c.key || got.Text != c.text {
			t.Errorf("%s: %+v, want %s %q", c.name, got, c.key, c.text)
		}
	}
}

func TestLimits(t *testing.T) {
	f := newFake(t)
	s := f.setup()
	b := f.builder()

	b.Limits.Files = 37
	_, err := b.Build(context.Background(), s)
	if e := wantKind(t, err, modpacks.KindTooManyFiles); e.Params["limit"] != "37" {
		t.Errorf("params: %v", e.Params)
	}
	if log := f.log(); len(log) != 0 {
		t.Errorf("Modrinth was asked about a setup over the limit: %q", log)
	}

	b.Limits.Files = 38
	sh := build(t, b, s)
	ix, err := indexJSON(&sh.Index)
	if err != nil {
		t.Fatal(err)
	}
	b.Limits.Index = int64(len(ix))
	build(t, b, s)
	b.Limits.Index = int64(len(ix)) - 1
	_, err = b.Build(context.Background(), s)
	if e := wantKind(t, err, addons.KindTooLarge); e.Params["limit"] != fetch.Size(int64(len(ix))-1) {
		t.Errorf("params: %v", e.Params)
	}

	for _, lim := range []Limits{{}, {Files: -1, Index: -1}} {
		if got := (&Builder{Limits: lim}).limits(); got != DefaultLimits() {
			t.Errorf("%+v gave %+v", lim, got)
		}
	}
}

// Modrinth answers with whole versions, so hashes go in batches of 100.
func TestLookUpsAreBatched(t *testing.T) {
	f := newFake(t)
	var ins []addons.Installed
	for i := range 250 {
		ins = append(ins, addons.Installed{Source: addons.Modrinth, ProjectID: fmt.Sprintf("PRJ%05d", i), Slug: fmt.Sprintf("mod-%03d", i),
			Name: fmt.Sprintf("Mod %03d", i), FileName: fmt.Sprintf("mod-%03d.jar", i), HashAlgo: "sha512", Hash: sha512hex(fmt.Appendf(nil, "mod %d", i))})
	}
	sh := build(t, f.builder(), Setup{Name: "Big server", Type: "quilt", MinecraftVersion: "26.2", LoaderVersion: "0.30.0", Addons: ins})
	want := []string{"POST version_files sha512 100", "POST version_files sha512 100", "POST version_files sha512 50"}
	for range 5 {
		want = append(want, "GET projects 50")
	}
	if log := f.log(); !slices.Equal(log, want) {
		t.Errorf("requests: %q", log)
	}
	if len(sh.Yourself) != 250 || len(sh.Index.Files) != 0 || sh.Notice.Key != "share.notice.optional" ||
		!maps.Equal(sh.Index.Dependencies, map[string]string{"minecraft": "26.2", "quilt-loader": "0.30.0"}) {
		t.Errorf("share: %d by hand, %d in the file, notice %s, dependencies %v", len(sh.Yourself), len(sh.Index.Files), sh.Notice.Key, sh.Index.Dependencies)
	}
	if y := sh.Yourself[0]; y.Name != "Mod 000" || y.Page != "https://modrinth.com/mod/mod-000" || y.Need != Unknown {
		t.Errorf("first: %+v", y)
	}
	if b, _ := indexJSON(&sh.Index); !bytes.Contains(b, []byte(`"files": []`)) {
		t.Errorf("an empty file list is not written as []: %s", b)
	}
}

func TestServersWithoutModsToShare(t *testing.T) {
	f := newFake(t)
	for _, typ := range []string{"paper", "purpur", "vanilla", "", "Fabric"} {
		s := f.setup()
		s.Type = typ
		_, err := f.builder().Build(context.Background(), s)
		wantKind(t, err, KindUnsupported)
	}
	for _, c := range []struct{ minecraft, loader string }{{"26.2", ""}, {"", "0.19.5"}, {"26.2; reboot", "0.19.5"}, {"26.2", "0.19.5\nop Alex"}} {
		s := f.setup()
		s.MinecraftVersion, s.LoaderVersion = c.minecraft, c.loader
		_, err := f.builder().Build(context.Background(), s)
		e := wantKind(t, err, KindNoVersion)
		if strings.ContainsAny(e.Params["minecraft"]+e.Params["loader"], "\n") {
			t.Errorf("params: %q", e.Params)
		}
	}
	if log := f.log(); len(log) != 0 {
		t.Errorf("Modrinth was asked: %q", log)
	}
	for typ, want := range map[string]string{"fabric": "fabric-loader", "quilt": "quilt-loader", "neoforge": "neoforge"} {
		if got := loaderID(typ); got != want {
			t.Errorf("%s: %s", typ, got)
		}
	}
}

func TestModrinthErrors(t *testing.T) {
	for _, c := range []struct {
		name   string
		hook   func(w http.ResponseWriter, r *http.Request) bool
		kind   addons.Kind
		params map[string]string
	}{
		{"rate limited", func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			return true
		}, addons.KindRateLimited, map[string]string{"source": "Modrinth", "seconds": "30"}},
		{"failing", func(w http.ResponseWriter, r *http.Request) bool {
			w.WriteHeader(http.StatusInternalServerError)
			return true
		}, addons.KindUpstream, map[string]string{"source": "Modrinth", "status": "500"}},
		{"failing on projects", func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != "/v2/projects" {
				return false
			}
			w.WriteHeader(http.StatusBadGateway)
			return true
		}, addons.KindUpstream, map[string]string{"source": "Modrinth", "status": "502"}},
	} {
		f := newFake(t)
		f.hook = c.hook
		_, err := f.builder().Build(context.Background(), f.setup())
		e := wantKind(t, err, c.kind)
		for k, v := range c.params {
			if e.Params[k] != v {
				t.Errorf("%s: params %v", c.name, e.Params)
			}
		}
	}

	f := newFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.builder().Build(ctx, f.setup()); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled build: %v", err)
	}
}
