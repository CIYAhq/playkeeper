package site

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"html"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// mergedFS is the repository with extra files in it, listed in their
// folders too. The repository's own thumbnails are left out, so a test's
// thumbnails are the only ones.
type mergedFS struct {
	base  fs.FS
	extra fstest.MapFS
}

const thumbDir = "site/static/shots/templates"

// reCardName finds the template a template card opens, as its link says.
var reCardName = regexp.MustCompile(`<span class="visually-hidden"> the (.+?) template</span>`)

func (m mergedFS) Open(name string) (fs.File, error) {
	if f, ok := m.extra[name]; ok && !f.Mode.IsDir() {
		return m.extra.Open(name)
	}
	if strings.HasPrefix(name, thumbDir+"/") {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return m.base.Open(name)
}

func (m mergedFS) ReadDir(name string) ([]fs.DirEntry, error) {
	a, errA := fs.ReadDir(m.base, name)
	if name == thumbDir {
		a, errA = nil, nil
	}
	b, errB := fs.ReadDir(m.extra, name)
	if errA != nil && errB != nil {
		return nil, errA
	}
	seen := map[string]bool{}
	var out []fs.DirEntry
	for _, e := range append(a, b...) {
		if !seen[e.Name()] {
			seen[e.Name()] = true
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(x, y fs.DirEntry) int { return strings.Compare(x.Name(), y.Name()) })
	return out, nil
}

// fakeWebP and fakeAVIF are just enough of a w × h image for the site to
// read its size.
func fakeWebP(w, h int) []byte {
	b := make([]byte, 30)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], 22)
	copy(b[8:], "WEBPVP8X")
	binary.LittleEndian.PutUint32(b[16:], 10)
	b[24], b[25], b[26] = byte(w-1), byte((w-1)>>8), byte((w-1)>>16)
	b[27], b[28], b[29] = byte(h-1), byte((h-1)>>8), byte((h-1)>>16)
	return b
}

func isoBox(typ string, body ...[]byte) []byte {
	payload := bytes.Join(body, nil)
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(payload)))
	return append(append(b, typ...), payload...)
}

func fakeAVIF(w, h int) []byte {
	ispe := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(make([]byte, 4), uint32(w)), uint32(h))
	return append(isoBox("ftyp", []byte("avif\x00\x00\x00\x00avifmif1")), isoBox("meta", make([]byte, 4), isoBox("iprp", isoBox("ipco", isoBox("ispe", ispe))))...)
}

// thumbFiles are the four files site/tools/shots.py makes for a template.
func thumbFiles(id string) fstest.MapFS {
	out := fstest.MapFS{}
	for _, w := range thumbWidths {
		base := fmt.Sprintf("site/static/shots/templates/%s-%dw", id, w)
		out[base+".webp"] = &fstest.MapFile{Data: fakeWebP(w, w*10/16)}
		out[base+".avif"] = &fstest.MapFile{Data: fakeAVIF(w, w*10/16)}
	}
	return out
}

func buildWith(extra fstest.MapFS) (*Output, error) {
	return Build(Options{Root: mergedFS{os.DirFS("../.."), extra}, Settings: Default, Now: time.Now()})
}

// A template's thumbnail takes the place of its pixel-art scene on its card,
// its page and its category's picture, as a <picture> with both widths in
// AVIF and WebP, asked for early where it's the first thing a page shows. A
// template without one keeps its scene.
func TestThumbnailsTakeTheScenesPlace(t *testing.T) {
	plain := build(t, Default)
	idx := indexOf(t, plain)
	first := idx.Templates[0].ID
	withThumb := map[string]bool{"towny": true, first: true, "survival-with-friends": true, "cobblemon": true}
	extra := fstest.MapFS{}
	for id := range withThumb {
		for k, v := range thumbFiles(id) {
			extra[k] = v
		}
	}
	o, err := buildWith(extra)
	if err != nil {
		t.Fatal(err)
	}
	built := pages(o)
	hub := built["/templates"]
	for i, e := range indexOf(t, o).Templates {
		card := cardOf(hub, e.Page)
		photo := strings.Contains(card, `class="dcard-art is-photo"><picture><source type="image/avif" srcset="/assets/shots/templates/`+e.ID+`-480w.`)
		if i < PerPage && photo != withThumb[e.ID] {
			t.Errorf("/templates shows %s's thumbnail %v, want %v", e.ID, photo, withThumb[e.ID])
		}
		named := len(e.Thumb) == len(thumbWidths)
		for i, w := range thumbWidths {
			named = named && strings.Contains(e.Thumb[i], fmt.Sprintf("%s-%dw.", e.ID, w))
		}
		if withThumb[e.ID] != (len(e.Thumb) > 0) || (len(e.Thumb) > 0 && !named) {
			t.Errorf("the index gives %s the thumbnail %v", e.ID, e.Thumb)
		}
		page := built[e.Page]
		hero := strings.Contains(page, `class="tpage-art hero-rise is-photo"><picture>`)
		if hero != withThumb[e.ID] {
			t.Errorf("%s shows its thumbnail at the top %v, want %v", e.Page, hero, withThumb[e.ID])
		}
		if hero && !strings.Contains(page, `<link rel="preload" as="image" type="image/avif" imagesrcset="/assets/shots/templates/`+e.ID+`-480w.`) {
			t.Errorf("%s doesn't ask for its thumbnail early", e.Page)
		}
	}
	if !strings.Contains(built["/templates/towny"], `class="cat-scene is-photo"`) || !strings.Contains(built["/templates/towny"], `media="(min-width: 1024px)" fetchpriority="high">`) {
		t.Error("/templates/towny's picture isn't Towny's thumbnail, asked for early on screens that show it")
	}
	// So does the template card on the landing page, the guides and the
	// modpack pages.
	cards, err := loadTemplateCards(os.DirFS("../.."), "site/data/templates")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for id, c := range cards {
		byName[c.Name] = id
	}
	photos := map[string]int{}
	for p, page := range built {
		for _, c := range strings.Split(page, `<article class="tpl-card reveal">`)[1:] {
			c, _, _ = strings.Cut(c, "</article>")
			m := reCardName.FindStringSubmatch(c)
			if m == nil || byName[html.UnescapeString(m[1])] == "" {
				t.Errorf("a template card on %s doesn't name a template: %s", p, c)
				continue
			}
			id := byName[html.UnescapeString(m[1])]
			photo := strings.Contains(c, `class="tpl-art is-photo"><picture><source type="image/avif" srcset="/assets/shots/templates/`+id+`-480w.`)
			if photo != withThumb[id] {
				t.Errorf("%s's card on %s shows its thumbnail %v, want %v", id, p, photo, withThumb[id])
			}
			if photo {
				photos[p]++
				for _, w := range thumbWidths {
					for _, ext := range []string{"avif", "webp"} {
						if !regexp.MustCompile(fmt.Sprintf(`/assets/shots/templates/%s-%dw\.[0-9a-f]+\.%s %dw`, id, w, ext, w)).MatchString(c) {
							t.Errorf("%s's card on %s doesn't offer its %dw %s", id, p, w, ext)
						}
					}
				}
			}
		}
	}
	for _, p := range []string{"/", "/guides/play-minecraft-with-friends", "/guides/modded-minecraft-server", "/modpacks/cobblemon-server"} {
		if photos[p] == 0 {
			t.Errorf("no template card on %s shows a thumbnail", p)
		}
	}
	// Every page's pictures keep the site's rules for screenshots.
	for p, html := range built {
		pictures := rePicture.FindAllStringSubmatch(html, -1)
		if n := strings.Count(html, `class="shot-img`); n != len(pictures) {
			t.Errorf("%s has %d screenshots, %d of them in a <picture> with sizes", p, n, len(pictures))
		}
		preloads := map[string]string{}
		for _, m := range rePreload.FindAllStringSubmatch(html, -1) {
			preloads[m[1]] = m[2]
		}
		eager := 0
		for _, m := range pictures {
			if m[6] == ` fetchpriority="high"` {
				eager++
				if preloads[m[1]] != m[5] {
					t.Errorf("%s shows %s first without asking for it early with its sizes", p, m[1])
				}
			}
		}
		if eager != len(preloads) {
			t.Errorf("%s shows %d pictures first and asks for %d early", p, eager, len(preloads))
		}
	}
}

// A thumbnail that isn't one of a template's four files stops the build.
func TestThumbnailMistakesStopTheBuild(t *testing.T) {
	for name, edit := range map[string]func(fstest.MapFS){
		"a thumbnail for no template": func(m fstest.MapFS) {
			for k, v := range thumbFiles("no-such-template") {
				m[k] = v
			}
		},
		"a thumbnail that isn't 16:10": func(m fstest.MapFS) {
			m["site/static/shots/templates/towny-480w.webp"] = &fstest.MapFile{Data: fakeWebP(480, 320)}
		},
		"a width short": func(m fstest.MapFS) { delete(m, "site/static/shots/templates/towny-960w.avif") },
		"another width": func(m fstest.MapFS) {
			m["site/static/shots/templates/towny-1600w.webp"] = &fstest.MapFile{Data: fakeWebP(1600, 1000)}
			m["site/static/shots/templates/towny-1600w.avif"] = &fstest.MapFile{Data: fakeAVIF(1600, 1000)}
		},
		"a file that isn't a thumbnail": func(m fstest.MapFS) {
			m["site/static/shots/templates/towny.webp"] = &fstest.MapFile{Data: fakeWebP(960, 600)}
		},
	} {
		m := thumbFiles("towny")
		edit(m)
		if _, err := buildWith(m); err == nil {
			t.Errorf("%s builds", name)
		}
	}
	if _, err := buildWith(thumbFiles("towny")); err != nil {
		t.Errorf("Towny's four thumbnail files: %v", err)
	}
}

// A category's guide keeps what its own page says: its intro, its side
// box's words, its Keep reading band and the link to its questions.
func TestACategoryGuideKeepsItsOwnParts(t *testing.T) {
	built := pages(build(t, Default))
	entries, err := os.ReadDir("../../site/pages/templates")
	if err != nil {
		t.Fatal(err)
	}
	actions := regexp.MustCompile(`\{\{.*?\}\}`)
	part := func(src, name string) string {
		m := regexp.MustCompile(`(?s)\{\{define "` + name + `"\}\}(.*?)\{\{end\}\}`).FindStringSubmatch(src)
		if m == nil {
			return ""
		}
		return m[1]
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		b, err := os.ReadFile("../../site/pages/templates/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		src, path := string(b), "/templates/"+strings.TrimSuffix(e.Name(), ".html")
		page := built[path]
		if page == "" {
			continue
		}
		checked++
		if short := strings.TrimSpace(actions.ReplaceAllString(part(src, "short"), "")); short != "" {
			if first, _, _ := strings.Cut(short, ". "); !strings.Contains(page, first) {
				t.Errorf("%s doesn't open with its own intro, %q", path, first)
			}
		}
		if side := strings.TrimSpace(part(src, "side-text")); side != "" && !strings.Contains(side, "{{") && !strings.Contains(page, side) {
			t.Errorf("%s's side box doesn't say %q", path, side)
		}
		if part(src, "keep-reading") != "" && !strings.Contains(page, `<section class="band band-chalk related"`) {
			t.Errorf("%s has no Keep reading band", path)
		}
		if strings.Contains(page, `id="questions"`) && !strings.Contains(page, `<li><a href="#questions">Questions</a></li>`) {
			t.Errorf("%s's contents don't link its questions", path)
		}
	}
	if checked == 0 {
		t.Fatal("no category guide was built")
	}
}
