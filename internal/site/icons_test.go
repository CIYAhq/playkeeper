package site

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// picture is a w × h image of one colour, as a file in format.
func picture(t *testing.T, format string, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.NRGBA{R: 30, G: 140, B: 60, A: 255})
		}
	}
	var b bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&b, img)
	case "jpeg":
		err = jpeg.Encode(&b, img, nil)
	case "gif":
		err = gif.Encode(&b, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Each listed template's card and page show what it installs with its
// project's icon, published as the site's own 96-pixel PNG; a project
// without one shows its initial.
func TestWhatTemplatesInstallShowsWithIcons(t *testing.T) {
	var without Project
	var asked []Project
	fetch := func(_ context.Context, ps []Project) map[Project][]byte {
		asked = ps
		out := map[Project][]byte{}
		for i, p := range ps {
			if i == 0 {
				without = p
				continue
			}
			out[p] = picture(t, []string{"png", "jpeg", "gif"}[i%3], 120, 60)
		}
		return out
	}
	o, err := Build(Options{Root: os.DirFS("../.."), Settings: Default, Now: time.Now(), Icons: fetch})
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) < 10 || len(o.NoIcons) != 1 || !strings.HasPrefix(o.NoIcons[0], without.key()+" ") {
		t.Fatalf("asked for %d icons, and %v kept their initial; want one, %s", len(asked), o.NoIcons, without.key())
	}
	built := pages(o)
	idx := indexOf(t, o)
	hub := built["/templates"]
	icon := func(p Project) string { return `src="/assets/icons/` + p.key() + `.` }
	for i, e := range idx.Templates {
		if len(e.Addons) != len(e.Icons) {
			t.Errorf("the index has %d add-ons for %s and %d icons", len(e.Addons), e.ID, len(e.Icons))
		}
		card := cardOf(hub, e.Page)
		if i < PerPage && card == "" {
			t.Errorf("/templates has no card for %s", e.ID)
		}
		for j, name := range e.Addons {
			if j < 3 && i < PerPage && !strings.Contains(card, "<span>"+name+"</span>") {
				t.Errorf("%s's card doesn't list %s", e.ID, name)
			}
			if e.Icons[j] >= len(idx.Icons) {
				t.Errorf("%s's icon %d isn't in the index", e.ID, e.Icons[j])
				continue
			}
			if j < 3 && i < PerPage {
				mark := `class="ai ai-initial"`
				if e.Icons[j] >= 0 {
					mark = `src="` + idx.Icons[e.Icons[j]] + `"`
				}
				if !strings.Contains(card, mark) {
					t.Errorf("%s's card shows %s without %s", e.ID, name, mark)
				}
			}
		}
		if n := len(e.Addons); n > 3 && i < PerPage && !strings.Contains(card, "+"+itoa(n-3)+" more") {
			t.Errorf("%s's card doesn't say it installs %d more", e.ID, n-3)
		}
	}
	for _, p := range asked {
		src := icon(p)
		used := strings.Contains(hub, src) || slices.ContainsFunc(idx.Templates, func(e indexTemplate) bool { return strings.Contains(built[e.Page], src) })
		if p == without {
			if used {
				t.Errorf("a page shows an icon for %s, which has none", p.key())
			}
			continue
		}
		if !used {
			t.Errorf("no page shows %s's icon", p.key())
		}
		var f []byte
		for name, b := range o.Files {
			if reIcon.MatchString(name) && strings.HasPrefix(name, "assets/icons/"+p.key()+".") {
				f = b
			}
		}
		img, err := png.Decode(bytes.NewReader(f))
		if err != nil {
			t.Errorf("%s's icon: %v", p.key(), err)
			continue
		}
		if b := img.Bounds(); b.Dx() != iconSize || b.Dy() != iconSize {
			t.Errorf("%s's icon is %v, want %d square", p.key(), b, iconSize)
		}
		// 120 × 60 fits as 96 × 48, centred: clear above it, the icon in the middle.
		if _, _, _, a := img.At(48, 10).RGBA(); a != 0 {
			t.Errorf("%s's icon isn't clear above a wide picture", p.key())
		}
		if _, _, _, a := img.At(48, 48).RGBA(); a == 0 {
			t.Errorf("%s's icon is clear in the middle", p.key())
		}
	}
	// A template's page shows each add-on's icon beside its name.
	for _, e := range idx.Templates {
		page := built[e.Page]
		if strings.Contains(page, "What it installs") && !strings.Contains(page, `class="addon-mark addon-icon"`) && !strings.Contains(page, `class="addon-mark" aria-hidden="true"`) {
			t.Errorf("%s lists what it installs without icons or initials", e.Page)
		}
	}
}

var reIcon = regexp.MustCompile(`^assets/icons/[a-z]+-[A-Za-z0-9]+\.[0-9a-f]{8}\.png$`)

// Without a fetcher, every add-on shows its initial and the site links no
// icon.
func TestWithoutIconsEachShowsItsInitial(t *testing.T) {
	o := build(t, Default)
	for p, html := range pages(o) {
		if strings.Contains(html, "/assets/icons/") {
			t.Errorf("%s links an icon, with none fetched", p)
		}
	}
	if !strings.Contains(pages(o)["/templates"], `class="ai ai-initial"`) {
		t.Error("/templates shows no add-on's initial")
	}
}

// NetIcons asks Modrinth about all its projects at once and Hangar about
// each, then fetches the files, leaving out what isn't there or is too big.
func TestNetIconsFetchesFromModrinthAndHangar(t *testing.T) {
	var queries []string
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/modrinth/projects", func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("ids"))
		if r.Header.Get("User-Agent") != iconAgent {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "aaa", "icon_url": srv.URL + "/cdn/aaa.png"},
			{"id": "bbb", "icon_url": nil},
			{"id": "big", "icon_url": srv.URL + "/cdn/big.png"},
		})
	})
	mux.HandleFunc("/hangar/projects/Geyser", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"avatarUrl": srv.URL + "/cdn/geyser.png"})
	})
	mux.HandleFunc("/cdn/aaa.png", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(picture(t, "png", 64, 64)) })
	mux.HandleFunc("/cdn/geyser.png", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(picture(t, "png", 32, 32)) })
	mux.HandleFunc("/cdn/big.png", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(make([]byte, maxIconFile+1)) })
	fetch := NetIcons(srv.Client(), srv.URL+"/modrinth", srv.URL+"/hangar")
	ps := []Project{{Source: "modrinth", ID: "aaa"}, {Source: "modrinth", ID: "bbb"}, {Source: "modrinth", ID: "big"}, {Source: "hangar", ID: "12", Slug: "Geyser"}, {Source: "curseforge", ID: "925200"}}
	got := fetch(context.Background(), ps)
	if len(queries) != 1 || queries[0] != `["aaa","bbb","big"]` {
		t.Errorf("Modrinth was asked %q, want every project at once", queries)
	}
	var have []string
	for p := range got {
		have = append(have, p.key())
	}
	slices.Sort(have)
	if want := []string{"hangar-12", "modrinth-aaa"}; !slices.Equal(have, want) {
		t.Errorf("fetched %v, want %v", have, want)
	}
}

// iconPNG makes any icon a square PNG; what isn't an image is refused.
func TestIconPNGSquaresAnyPicture(t *testing.T) {
	for _, f := range []string{"png", "jpeg", "gif"} {
		b, err := iconPNG(picture(t, f, 16, 40))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		c, _, err := image.DecodeConfig(bytes.NewReader(b))
		if err != nil || c.Width != iconSize || c.Height != iconSize {
			t.Errorf("%s: %v, %dx%d", f, err, c.Width, c.Height)
		}
	}
	for _, bad := range [][]byte{nil, []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")} {
		if _, err := iconPNG(bad); err == nil {
			t.Errorf("%q made an icon", bad)
		}
	}
}
