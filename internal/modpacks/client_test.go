package modpacks

import (
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
)

func clientByPath(r Record) map[string]ClientFile {
	out := map[string]ClientFile{}
	for _, cf := range r.Client {
		out[cf.Path] = cf
	}
	return out
}

func TestClientFilesOfAModrinthPack(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	l.Limits.Unpacked = 200
	srv := newServer(t, "fabric", "26.2")
	big := []byte(strings.Repeat("a large resource pack\n", 10))
	id := f.addPack(testIndex(
		f.indexFile("mods/both-1.jar", []byte("both"), "required", "required"),
		f.indexFile("mods/client-1.jar", []byte("client"), "required", "unsupported"),
		f.indexFile("mods/server-1.jar", []byte("server"), "unsupported", "required"),
		f.indexFile("mods/opt-1.jar", []byte("optional"), "optional", "optional"),
		f.indexFile("mods/noenv-1.jar", []byte("everywhere")),
		f.indexFile("mods/odd-1.jar", []byte("odd"), "unknown", "sometimes"),
		f.indexFile("mods/replaced-1.jar", []byte("downloaded"), "required", "unsupported"),
		f.indexFile("resourcepacks/look-1.zip", []byte("resource pack"), "required", "unsupported"),
		f.indexFile("config/settings-1.json", []byte("{}"), "required", "required"),
		f.indexFile("mods/nested/deep-1.jar", []byte("nested"), "required", "required"),
	),
		entry{name: "overrides/mods/extra.jar", data: []byte("extra mod")},
		entry{name: "overrides/config/common.txt", data: []byte("common")},
		entry{name: "client-overrides/mods/replaced-1.jar", data: []byte("from client-overrides")},
		entry{name: "client-overrides/mods/client-extra.jar", data: []byte("client extra")},
		entry{name: "client-overrides/shaderpacks/big.zip", data: big},
		entry{name: "client-overrides/options.txt", data: []byte("fov:90")},
		entry{name: "server-overrides/mods/server-extra.jar", data: []byte("server extra")},
	)
	res := mustInstall(t, l, srv, InstallRequest{Ref: testRef(id)})
	rec := roundTrip(t, res.Record)
	got := clientByPath(rec)
	var paths []string
	for _, cf := range rec.Client {
		paths = append(paths, cf.Path)
	}
	wantList(t, "files for players", paths,
		"mods/both-1.jar", "mods/client-1.jar", "mods/client-extra.jar", "mods/extra.jar", "mods/noenv-1.jar",
		"mods/odd-1.jar", "mods/opt-1.jar", "mods/replaced-1.jar", "resourcepacks/look-1.zip", "shaderpacks/big.zip")

	both := got["mods/both-1.jar"]
	if both.Origin != Download || both.Project != "both" || both.SHA1 != sha1hex([]byte("both")) || both.SHA512 != sha512hex([]byte("both")) ||
		both.Size != 4 || len(both.Downloads) != 1 || !strings.HasPrefix(both.Downloads[0], f.cdn.URL+"/data/both/versions/") ||
		*both.Env != (mrpack.Env{Client: mrpack.Required, Server: mrpack.Required}) {
		t.Errorf("both: %+v", both)
	}
	if got["mods/noenv-1.jar"].Env != nil {
		t.Errorf("a file without env got %+v", got["mods/noenv-1.jar"].Env)
	}
	if odd := got["mods/odd-1.jar"].Env; *odd != (mrpack.Env{Client: mrpack.Unknown, Server: mrpack.Unknown}) {
		t.Errorf("odd env recorded as %+v", odd)
	}
	for p, want := range map[string]string{"mods/extra.jar": "extra mod", "mods/client-extra.jar": "client extra", "mods/replaced-1.jar": "from client-overrides"} {
		cf := got[p]
		if cf.Origin != Override || cf.SHA1 != sha1hex([]byte(want)) || cf.SHA512 != sha512hex([]byte(want)) || cf.Size != int64(len(want)) ||
			cf.Env != nil || cf.Downloads != nil || cf.Project != "" {
			t.Errorf("%s: %+v", p, cf)
		}
	}
	// Hashing the shader pack would read more of the archive than the
	// limits allow, so it is recorded without hashes.
	if sp := got["shaderpacks/big.zip"]; sp.Origin != Override || sp.SHA1 != "" || sp.SHA512 != "" || sp.Size != int64(len(big)) {
		t.Errorf("shader pack: %+v", sp)
	}
	wantTree(t, srv.Dir, "config/", "config/common.txt", "config/settings-1.json", "mods/", "mods/both-1.jar", "mods/extra.jar",
		"mods/nested/", "mods/nested/deep-1.jar", "mods/noenv-1.jar", "mods/odd-1.jar", "mods/opt-1.jar", "mods/server-1.jar",
		"mods/server-extra.jar")
}

func TestClientFilesOfACurseForgePack(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "", "")
	rec := roundTrip(t, mustInstall(t, l, srv, InstallRequest{Ref: cfRef("")}).Record)
	var paths []string
	for _, cf := range rec.Client {
		paths = append(paths, cf.Path)
	}
	wantList(t, "files for players", paths,
		"mods/cloth-config-26.3.155-fabric.jar", "mods/fabric-api-0.141.0+26.3.jar", "mods/ferritecore-9.0.0-fabric.jar",
		"mods/lithium-fabric-0.25.3+mc26.3.jar", "mods/placeholder-api-3.1.0+26.3.jar", "mods/sodium-fabric-0.9.2+mc26.3.jar",
		"resourcepacks/Chat-Reporting-Helper-26.3.zip", "resourcepacks/Translations-for-Sodium-26.3.zip")
	got := clientByPath(rec)
	sodium := generated("sodium-fabric-0.9.2+mc26.3.jar")
	if s := got["mods/sodium-fabric-0.9.2+mc26.3.jar"]; s.Name != "Sodium" || s.Version != "sodium-fabric-0.9.2+mc26.3.jar" ||
		s.Project != "394468" || s.FileID != "8888037" || s.SHA1 != sha1hex(sodium) || s.SHA512 != "" || s.Size != int64(len(sodium)) ||
		!s.Required || !slices.Equal(s.Sides, []string{"client"}) || s.Origin != Download || s.Env != nil || s.Downloads != nil ||
		s.Page != "https://www.curseforge.com/minecraft/mc-mods/sodium/files/8888037" {
		t.Errorf("sodium: %+v", s)
	}
	if api := got["mods/fabric-api-0.141.0+26.3.jar"]; !slices.Equal(api.Sides, []string{"client", "server"}) {
		t.Errorf("fabric api sides %q", api.Sides)
	}
	// FerriteCore's author forbids other downloads and tags no side; the
	// record still names it, for a friend to fetch by hand.
	if fc := got["mods/ferritecore-9.0.0-fabric.jar"]; fc.Sides != nil || fc.Name != "FerriteCore" ||
		fc.Page != "https://www.curseforge.com/minecraft/mc-mods/ferritecore/files/7806040" {
		t.Errorf("ferritecore: %+v", fc)
	}
	if rp := got["resourcepacks/Chat-Reporting-Helper-26.3.zip"]; rp.Name != "Chat Reporting Helper" || rp.Sides != nil || !rp.Required {
		t.Errorf("resource pack: %+v", rp)
	}
}

func TestPlayerContent(t *testing.T) {
	for p, want := range map[string]bool{
		"mods/sodium.jar":               true,
		"resourcepacks/faithful.zip":    true,
		"shaderpacks/complementary.zip": true,
		"mods/.hidden.jar":              false,
		"mods/.jar":                     false,
		"mods/sub/deep.jar":             false,
		"mods/readme.txt":               false,
		"mods/pack.zip":                 false,
		"resourcepacks/pack.jar":        false,
		"config/sodium.jar":             false,
		"mods":                          false,
		"mods/":                         false,
		"/mods/abs.jar":                 false,
		"mods/../escape.jar":            false,
		"mods/bad\\name.jar":            false,
		"mods/line\nbreak.jar":          false,
		"shaderpacks/":                  false,
	} {
		if got := PlayerContent(p); got != want {
			t.Errorf("PlayerContent(%q) = %v, want %v", p, got, want)
		}
	}
}
