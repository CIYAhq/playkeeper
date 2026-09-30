package site

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

var (
	reOGImage = regexp.MustCompile(`<meta property="og:image" content="([^"]+)">`)
	reOGAlt   = regexp.MustCompile(`<meta property="og:image:alt" content="([^"]+)">`)
)

func ogOf(page string) (img, alt string) {
	if m := reOGImage.FindStringSubmatch(page); m != nil {
		img = m[1]
	}
	if m := reOGAlt.FindStringSubmatch(page); m != nil {
		alt = m[1]
	}
	return img, alt
}

func buildPreviews(t *testing.T, extra fstest.MapFS) *Output {
	t.Helper()
	o, err := Build(Options{Root: mergedFS{os.DirFS("../.."), extra}, Settings: Default, Now: time.Now(), Previews: true})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// Each listed template's page and its /t/<id>, which Copy link shares, show
// the template's own preview, and each category without a guide its own; a
// page with a preview of its own keeps it.
func TestDirectoryPagesHaveTheirOwnPreviews(t *testing.T) {
	o := buildPreviews(t, nil)
	built := pages(o)
	seen := map[string]string{}
	for _, e := range indexOf(t, o).Templates {
		img, alt := ogOf(built[e.Page])
		if !strings.HasPrefix(img, Default.BaseURL+"/assets/og/templates/"+e.ID+".") {
			t.Errorf("%s's preview is %s", e.Page, img)
			continue
		}
		if open, _ := ogOf(built["/t/"+e.ID]); open != img {
			t.Errorf("/t/%s's preview is %s, not its page's", e.ID, open)
		}
		if !strings.HasPrefix(alt, strings.ReplaceAll(e.Name, "'", "&#39;")+", a Minecraft server template on Playkeeper: ") {
			t.Errorf("%s's preview is described as %q", e.Page, alt)
		}
		b := o.Files[strings.TrimPrefix(img, Default.BaseURL+"/")]
		c, err := png.DecodeConfig(bytes.NewReader(b))
		if err != nil || c.Width != previewW || c.Height != previewH {
			t.Errorf("%s: %v, %d × %d", img, err, c.Width, c.Height)
		}
		if other, ok := seen[string(b)]; ok {
			t.Errorf("%s has the same preview as %s", e.ID, other)
		}
		seen[string(b)] = e.ID
	}
	categories := 0
	own := ownPages(t)
	for p, html := range built {
		id, ok := strings.CutPrefix(p, "/templates/")
		if !ok || strings.Contains(id, "/") || id == "page" {
			continue
		}
		img, _ := ogOf(html)
		if own[p] {
			if strings.Contains(img, "/assets/og/categories/") {
				t.Errorf("%s, a page of the site's own, has a category's preview, %s", p, img)
			}
			continue
		}
		categories++
		if _, err := os.Stat("../../site/pages/templates/" + id + ".html"); err == nil {
			if strings.Contains(img, "/assets/og/categories/") {
				t.Errorf("%s, a guide, lost its own preview to %s", p, img)
			}
		} else if !strings.HasPrefix(img, Default.BaseURL+"/assets/og/categories/"+id+".") {
			t.Errorf("%s's preview is %s", p, img)
		}
	}
	if categories == 0 {
		t.Fatal("no category page was built")
	}
	if img, _ := ogOf(built["/templates"]); !strings.Contains(img, "/assets/og/templates.") {
		t.Errorf("/templates's preview is %s, not its own", img)
	}
}

// A template's thumbnail is its preview's picture, as it's its card's.
func TestAPreviewShowsTheThumbnail(t *testing.T) {
	extra := thumbFiles("towny")
	for _, w := range thumbWidths {
		b, err := os.ReadFile("testdata/thumb-" + itoa(w) + "w.webp")
		if err != nil {
			t.Fatal(err)
		}
		extra["site/static/shots/templates/towny-"+itoa(w)+"w.webp"] = &fstest.MapFile{Data: b}
	}
	o := buildPreviews(t, extra)
	img, _ := ogOf(pages(o)["/templates/towny/towny"])
	pic, err := png.Decode(bytes.NewReader(o.Files[strings.TrimPrefix(img, Default.BaseURL+"/")]))
	if err != nil {
		t.Fatal(err)
	}
	// testdata's thumbnails are one colour: rgb(200, 60, 40).
	if c := color.NRGBAModel.Convert(pic.At(picX+picW/2, picY+picH/2)).(color.NRGBA); c.R < 190 || c.G > 70 || c.B > 50 {
		t.Errorf("Towny's preview shows %v in its picture, not its thumbnail", c)
	}
}

// The words stay clear of the picture, however long the name.
func TestPreviewTitlesFitBesideThePicture(t *testing.T) {
	p, err := newPreviewer(os.DirFS("../.."))
	if err != nil {
		t.Fatal(err)
	}
	pic := image.NewNRGBA(image.Rect(0, 0, picW, picH))
	for _, title := range []string{"Towny", "Survival with EssentialsX", "COBBLEVERSE - Pokemon Adventure", "Supercalifragilisticexpialidocious", "Wide Wide Wide Wide Wide Wide"} {
		for _, meta := range []string{"NeoForge 1.21.1 · 12 GB · Crossplay", "12 templates · Paper, Purpur, Fabric, Quilt, NeoForge, Forge and Vanilla"} {
			b, err := p.draw(preview{Eyebrow: "Server template", Title: title, Meta: meta, Picture: pic})
			if err != nil {
				t.Errorf("%q: %v", title, err)
				continue
			}
			clearOfPicture(t, p, b, title+" / "+meta)
		}
	}
}

// clearOfPicture checks that the strip between a preview's words, which end
// by x = 592, and its card's shadow, from x = 635, is the frame alone.
func clearOfPicture(t *testing.T, p *previewer, b []byte, what string) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	for y := 140; y < 520; y++ {
		for x := 596; x < 632; x++ {
			r1, g1, b1, _ := img.At(x, y).RGBA()
			r2, g2, b2, _ := p.frame.At(x, y).RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 {
				t.Errorf("%q reaches the picture at (%d, %d)", what, x, y)
				return
			}
		}
	}
}

// Pixel art is drawn unit by unit, from rects and paths of straight lines,
// and a scene with anything else stops the build.
func TestPixelArtDrawsEachUnit(t *testing.T) {
	img, err := pixelArt([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 4 3" shape-rendering="crispEdges"><path fill="#FF0000" d="M0 0h4v1h-4zM1 1h2v1h-2z"/><rect x="3" y="2" width="1" height="1" fill="#00f"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	red, blue, none := color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 0, 255, 255}, color.NRGBA{}
	for _, c := range []struct {
		x, y int
		want color.NRGBA
	}{{0, 0, red}, {3, 0, red}, {0, 1, none}, {1, 1, red}, {2, 1, red}, {3, 1, none}, {3, 2, blue}, {0, 2, none}} {
		if got := img.NRGBAAt(c.x, c.y); got != c.want {
			t.Errorf("(%d, %d) is %v, want %v", c.x, c.y, got, c.want)
		}
	}
	for _, bad := range []string{
		`<svg viewBox="0 0 4 3"><circle cx="1" cy="1" r="1" fill="#000"/></svg>`,
		`<svg viewBox="0 0 4 3"><path fill="red" d="M0 0h1v1h-1z"/></svg>`,
		`<svg viewBox="0 0 4 3"><path fill="#000" opacity="0.5" d="M0 0h1v1h-1z"/></svg>`,
		`<svg viewBox="0 0 4 3"><path fill="#000" d="C0 0 1 1 2 2z"/></svg>`,
	} {
		if _, err := pixelArt([]byte(bad)); err == nil {
			t.Errorf("%s draws", bad)
		}
	}
	// Every scene a card can show draws, and isn't empty.
	var cards map[string]struct {
		Art string `json:"art"`
	}
	b, err := os.ReadFile("../../site/data/templates/cards.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &cards); err != nil {
		t.Fatal(err)
	}
	for id, c := range cards {
		svg, err := os.ReadFile("../../web/src/assets/" + strings.TrimPrefix(c.Art, "app/"))
		if err != nil {
			t.Fatal(err)
		}
		img, err := pixelArt(svg)
		if err != nil {
			t.Errorf("%s's scene %s: %v", id, c.Art, err)
			continue
		}
		if img.NRGBAAt(0, 0).A == 0 && img.NRGBAAt(img.Bounds().Dx()-1, img.Bounds().Dy()-1).A == 0 {
			t.Errorf("%s's scene %s drew nothing in its corners", id, c.Art)
		}
	}
}
