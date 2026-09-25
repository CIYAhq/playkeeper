package share

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

func TestEnabled(t *testing.T) {
	for _, c := range []struct {
		on   bool
		typ  string
		want bool
	}{
		{true, "fabric", true}, {true, "quilt", true}, {true, "neoforge", true},
		{false, "fabric", false}, {false, "quilt", false}, {false, "neoforge", false},
		{true, "paper", false}, {true, "purpur", false}, {true, "vanilla", false}, {true, "", false}, {true, "Fabric", false},
	} {
		if got := Enabled(c.on, c.typ); got != c.want {
			t.Errorf("switch %t, %q: %t", c.on, c.typ, got)
		}
	}
}

// packServer is what the panel knows about a server for its public pages.
type packServer struct {
	on      bool // the owner's switch
	address string
	share   *Share
}

// publicPacks is the panel's public route for pack pages, in miniature:
// it asks Enabled on every request, and answers what Enabled refuses
// exactly as it answers a slug no server has.
func publicPacks(servers map[string]*packServer) http.Handler {
	find := func(w http.ResponseWriter, r *http.Request) *packServer {
		s := servers[r.PathValue("slug")]
		if s == nil || !Enabled(s.on, s.share.Type) {
			http.NotFound(w, r)
			return nil
		}
		return s
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/public/packs/{slug}", func(w http.ResponseWriter, r *http.Request) {
		s := find(w, r)
		if s == nil {
			return
		}
		p, err := s.share.Page(r.PathValue("slug"), s.address)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(p)
	})
	mux.HandleFunc("GET /api/public/packs/{slug}/{file}", func(w http.ResponseWriter, r *http.Request) {
		s := find(w, r)
		if s == nil {
			return
		}
		if r.PathValue("file") != FileName(r.PathValue("slug")) {
			http.NotFound(w, r)
			return
		}
		s.share.ServeFile(w, r, r.PathValue("slug"))
	})
	return mux
}

func get(h http.Handler, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func TestOffSwitch(t *testing.T) {
	f := newFake(t)
	sh := build(t, f.builder(), f.setup())
	srv := &packServer{on: true, address: "203.0.113.10", share: sh}
	h := publicPacks(map[string]*packServer{"adrenaline-smp": srv})

	page := get(h, http.MethodGet, "/api/public/packs/adrenaline-smp")
	if page.Code != http.StatusOK {
		t.Fatalf("page: %d", page.Code)
	}
	var p Page
	if err := json.Unmarshal(page.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	file := get(h, http.MethodGet, p.Download.URL)
	want, _ := sh.File()
	if file.Code != http.StatusOK || !bytes.Equal(file.Body.Bytes(), want) || int64(file.Body.Len()) != p.Download.Size {
		t.Errorf("file: %d, %d bytes", file.Code, file.Body.Len())
	}
	if w := get(h, http.MethodGet, "/api/public/packs/adrenaline-smp/other.mrpack"); w.Code != http.StatusNotFound {
		t.Errorf("another file name: %d", w.Code)
	}

	srv.on = false
	for _, c := range [][2]string{
		{"/api/public/packs/adrenaline-smp", "/api/public/packs/no-such-server"},
		{"/api/public/packs/adrenaline-smp/adrenaline-smp.mrpack", "/api/public/packs/no-such-server/no-such-server.mrpack"},
	} {
		off, unknown := get(h, http.MethodGet, c[0]), get(h, http.MethodGet, c[1])
		if off.Code != http.StatusNotFound || off.Code != unknown.Code || off.Body.String() != unknown.Body.String() ||
			!reflect.DeepEqual(off.Header(), unknown.Header()) {
			t.Errorf("%s with the switch off: %d %v %q; unknown: %d %v %q", c[0], off.Code, off.Header(), off.Body, unknown.Code, unknown.Header(), unknown.Body)
		}
	}

	// Signed-in users download the file with the switch off.
	w := httptest.NewRecorder()
	sh.ServeFile(w, httptest.NewRequest(http.MethodGet, "/api/servers/1/mods/share.mrpack", nil), "adrenaline-smp")
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), want) {
		t.Errorf("signed-in download: %d", w.Code)
	}

	// A server that no longer runs mods has no page, whatever the switch.
	srv.on = true
	srv.share.Type = "paper"
	if w := get(h, http.MethodGet, "/api/public/packs/adrenaline-smp"); w.Code != http.StatusNotFound {
		t.Errorf("a Paper server's page: %d", w.Code)
	}
}

func TestServeFile(t *testing.T) {
	f := newFake(t)
	sh := build(t, f.builder(), f.setup())
	want, _ := sh.File()
	w := httptest.NewRecorder()
	sh.ServeFile(w, httptest.NewRequest(http.MethodGet, "/api/public/packs/adrenaline-smp/adrenaline-smp.mrpack", nil), "adrenaline-smp")
	h := w.Header()
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), want) || h.Get("Content-Type") != "application/x-modrinth-modpack+zip" ||
		h.Get("Content-Disposition") != "attachment; filename=adrenaline-smp.mrpack" || h.Get("Cache-Control") != "no-store" ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Last-Modified") != "" {
		t.Errorf("%d %v", w.Code, h)
	}
	w = httptest.NewRecorder()
	sh.ServeFile(w, httptest.NewRequest(http.MethodHead, "/api/public/packs/adrenaline-smp/adrenaline-smp.mrpack", nil), "adrenaline-smp")
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD: %d, %d bytes, %v", w.Code, w.Body.Len(), w.Header())
	}
}

func TestPage(t *testing.T) {
	f := newFake(t)
	sh := build(t, f.builder(), f.setup())
	asked := len(f.log())
	p, err := sh.Page("adrenaline-smp", "203.0.113.10:25565")
	if err != nil {
		t.Fatal(err)
	}
	file, _ := sh.File()
	if len(f.log()) != asked {
		t.Errorf("the page asked Modrinth: %q", f.log()[asked:])
	}

	if p.Server != "Alex's server" || p.MinecraftVersion != "26.2" || p.Loader != "fabric" || p.LoaderName != "Fabric" || p.LoaderVersion != "0.19.5" ||
		p.Pack != sh.Pack || !reflect.DeepEqual(p.Notice, sh.Notice) || p.Address != "203.0.113.10:25565" {
		t.Errorf("page: %+v", p)
	}
	if want := (Download{URL: "/api/public/packs/adrenaline-smp/adrenaline-smp.mrpack", Name: "adrenaline-smp.mrpack", Size: int64(len(file)),
		Type: "application/x-modrinth-modpack+zip"}); p.Download != want {
		t.Errorf("download: %+v", p.Download)
	}

	var steps []string
	for _, s := range p.Steps {
		steps = append(steps, s.Key+": "+s.Text)
	}
	if want := []string{
		"share.step.download: Download adrenaline-smp.mrpack.",
		"share.step.import: Import it in the Modrinth App or Prism Launcher.",
		"share.step.play_join: Press Play, then join 203.0.113.10:25565.",
	}; !slices.Equal(steps, want) {
		t.Errorf("steps: %q", steps)
	}
	if len(p.Launchers) != 2 || p.Launchers[0].ID != "modrinth-app" || p.Launchers[0].Site != "https://modrinth.com/app" ||
		p.Launchers[1].ID != "prism" || p.Launchers[1].Site != "https://prismlauncher.org" {
		t.Errorf("launchers: %+v", p.Launchers)
	}
	for _, l := range p.Launchers {
		if len(l.Steps) != 3 {
			t.Errorf("%s: %d steps", l.ID, len(l.Steps))
		}
		for _, s := range l.Steps {
			if !strings.HasPrefix(s.Key, "share.launcher.") || strings.Contains(s.Text, "{") || s.Params != nil && s.Params["file"] != "adrenaline-smp.mrpack" {
				t.Errorf("%s: %+v", l.ID, s)
			}
		}
	}
	if s := p.Launchers[1].Steps[1]; s.Text != "Click Browse, pick adrenaline-smp.mrpack and click OK. Pasting the download link works too." {
		t.Errorf("Prism: %q", s.Text)
	}

	// The page lists what friends get, and nothing that stays on the server.
	if len(p.Mods) != 34 {
		t.Errorf("%d mods on the page, want the 34 in the file", len(p.Mods))
	}
	for _, m := range p.Mods {
		if m.Need == ServerOnly || !m.InFile || m.Label.Text == "" {
			t.Errorf("on the page: %+v", m)
		}
	}
	js, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"spark", "Better Fabric Console", "Krypton", "Very Many Players", "sha512", "cdn.modrinth.com"} {
		if bytes.Contains(js, []byte(name)) {
			t.Errorf("the public page mentions %s", name)
		}
	}
}

func TestPageWithoutAnAddress(t *testing.T) {
	f := newFake(t)
	sh := build(t, f.builder(), f.setup())
	for _, address := range []string{"", "not an address", "play.example.org:99999", "203.0.113.10:0", "http://203.0.113.10", "<b>203.0.113.10</b>"} {
		p, err := sh.Page("adrenaline-smp", address)
		if err != nil {
			t.Fatal(err)
		}
		if last := p.Steps[2]; p.Address != "" || last.Key != "share.step.play" || last.Text != "Press Play, then join the server." {
			t.Errorf("%q: address %q, last step %+v", address, p.Address, last)
		}
	}
}

func TestPageRefusesBadSlugs(t *testing.T) {
	f := newFake(t)
	sh := build(t, f.builder(), f.setup())
	for _, slug := range []string{"", "Adrenaline", "../adrenaline", "adrenaline smp", "-adrenaline", "adrenaline/x", strings.Repeat("a", 41), "adrenaline.mrpack"} {
		_, err := sh.Page(slug, "")
		wantKind(t, err, addons.KindInvalid)
		if FilePath(slug) != "" || FileName(slug) != "server.mrpack" {
			t.Errorf("%q: %q, %q", slug, FilePath(slug), FileName(slug))
		}
	}
	for _, slug := range []string{"a", "0", "adrenaline-smp", strings.Repeat("a", 40)} {
		if _, err := sh.Page(slug, ""); err != nil || FileName(slug) != slug+".mrpack" || FilePath(slug) != "/api/public/packs/"+slug+"/"+slug+".mrpack" {
			t.Errorf("%q: %v", slug, err)
		}
	}
}

func TestSteps(t *testing.T) {
	var got []string
	for _, s := range Steps("cobblemon.mrpack", "cobblemon.alex.playkeeper.io", true) {
		got = append(got, s.Text)
	}
	if want := []string{"Open the link and download the file.", "Import it in the Modrinth App or Prism Launcher.",
		"Press Play, then join cobblemon.alex.playkeeper.io."}; !slices.Equal(got, want) {
		t.Errorf("share dialog: %q", got)
	}
}

func TestJoinAddress(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.10":                       "203.0.113.10",
		"203.0.113.10:25565":                 "203.0.113.10:25565",
		" cobblemon.alex.playkeeper.io ":     "cobblemon.alex.playkeeper.io",
		"cobblemon.alex.playkeeper.io:25566": "cobblemon.alex.playkeeper.io:25566",
		"[2001:db8::1]:25565":                "[2001:db8::1]:25565",
		"2001:db8::1":                        "2001:db8::1",
		"":                                   "",
		"play.example.org:":                  "",
		"play.example.org:65536":             "",
		"play.example.org:port":              "",
		"-play.example.org":                  "",
		"play..example.org":                  "",
		"bücher.example":                     "",
		"play.example.org/join":              "",
		strings.Repeat("a.", 140) + "org":    "",
	} {
		if got := joinAddress(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
