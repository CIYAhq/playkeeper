package site

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // icons some projects keep as GIFs
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // Modrinth serves most icons as WebP

	"github.com/CIYAhq/playkeeper/internal/addons"
)

// Add-ons' icons: what each listed template installs, plugins, mods or its
// modpack, with the icon its Modrinth or Hangar project shows. They're
// fetched while the site builds (Options.Icons) and published as its own
// assets, since the Content-Security-Policy keeps images to the site's own.
// A project without an icon, or whose source doesn't answer, shows its
// initial instead, as every add-on does without a fetcher. Playkeeper's own
// plugins are on no registry: they always show their initial, and no
// fetcher is asked about them.

// iconSize is the side of the square each icon is published at: twice the
// largest it's shown, 40 pixels on a template's page.
const iconSize = 96

// maxIconFile is the largest icon file fetched.
const maxIconFile = 2 << 20

// Project is an add-on or modpack on the source it comes from.
type Project struct {
	// Source is modrinth, hangar, curseforge or playkeeper.
	Source, ID, Slug string
}

func (p Project) key() string { return p.Source + "-" + p.ID }

// IconFetcher returns the icon files of projects, by Project, leaving out
// those without one or whose source didn't answer.
type IconFetcher func(ctx context.Context, projects []Project) map[Project][]byte

// Installed is one thing a template installs, as its card and page show it.
type Installed struct {
	Name string
	// Icon is its project's icon, or nil for its initial.
	Icon *asset
}

// projects are what the template installs: its modpack, or its add-ons.
func (c *TemplateCard) projects() []Project {
	if m := c.Template.Modpack; m != nil {
		return []Project{{Source: string(m.Source), ID: m.Project, Slug: m.Slug}}
	}
	var out []Project
	for _, a := range c.Template.Addons {
		out = append(out, Project{Source: string(a.Source), ID: a.Project, Slug: a.Slug})
	}
	return out
}

// Installs are what the template installs, with their icons: its modpack,
// or its add-ons in its order.
func (c *TemplateCard) Installs() []Installed {
	if m := c.Template.Modpack; m != nil {
		return []Installed{{Name: firstOf(c.ModpackName(), m.Name), Icon: c.icons[c.projects()[0].key()]}}
	}
	out := make([]Installed, 0, len(c.Template.Addons))
	for i, p := range c.projects() {
		out = append(out, Installed{Name: c.Template.Addons[i].Name, Icon: c.icons[p.key()]})
	}
	return out
}

// IconOf is the icon of the template's add-on with this slug, or nil.
func (c *TemplateCard) IconOf(slug string) *asset {
	for _, p := range c.projects() {
		if p.Slug == slug {
			return c.icons[p.key()]
		}
	}
	return nil
}

// addIcons fetches the icons of what the listed templates install and
// publishes each as icons/<source>-<id>.png, and returns the projects left
// with their initial. Playkeeper's own plugins are neither fetched nor
// returned.
func (s *Site) addIcons() ([]string, error) {
	icons := map[string]*asset{}
	var want []Project
	seen := map[string]bool{}
	for _, c := range s.dir.Templates {
		c.icons = icons
		for _, p := range c.projects() {
			if !seen[p.key()] && p.Source != string(addons.Playkeeper) {
				seen[p.key()] = true
				want = append(want, p)
			}
		}
	}
	if s.opts.Icons == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	files := s.opts.Icons(ctx, want)
	var missing []string
	for _, p := range want {
		b, err := iconPNG(files[p])
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s (%v)", p.key(), err))
			continue
		}
		name := "icons/" + p.key() + ".png"
		a, err := newAsset(name, b)
		if err != nil {
			return nil, err
		}
		s.assets[name], icons[p.key()] = a, a
	}
	return missing, nil
}

// iconPNG turns an icon file into a PNG iconSize pixels square: shrunk or
// enlarged to fit, centred on transparency when it isn't square.
func iconPNG(file []byte) ([]byte, error) {
	if len(file) == 0 {
		return nil, errors.New("no icon")
	}
	src, _, err := image.Decode(bytes.NewReader(file))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return nil, errors.New("an empty image")
	}
	side := max(b.Dx(), b.Dy())
	w, h := b.Dx()*iconSize/side, b.Dy()*iconSize/side
	at := image.Pt((iconSize-w)/2, (iconSize-h)/2)
	dst := image.NewNRGBA(image.Rect(0, 0, iconSize, iconSize))
	// Pixel art stays sharp; anything larger is resampled smoothly.
	scaler := draw.Interpolator(draw.CatmullRom)
	if side*2 <= iconSize {
		scaler = draw.NearestNeighbor
	}
	scaler.Scale(dst, image.Rectangle{Min: at, Max: at.Add(image.Pt(w, h))}, src, b, draw.Src, nil)
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, dst); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// NetIcons fetches icons from Modrinth, all of its projects in one request,
// and Hangar, then each file, a few at a time. CurseForge needs a key, so
// its packs keep their initial.
func NetIcons(hc *http.Client, modrinth, hangar string) IconFetcher {
	return func(ctx context.Context, projects []Project) map[Project][]byte {
		links := map[Project]string{}
		var ids []string
		for _, p := range projects {
			if p.Source == "modrinth" {
				ids = append(ids, p.ID)
			}
		}
		if len(ids) > 0 {
			q, _ := json.Marshal(ids)
			var found []struct {
				ID   string `json:"id"`
				Icon string `json:"icon_url"`
			}
			if getJSON(ctx, hc, modrinth+"/projects?ids="+url.QueryEscape(string(q)), &found) == nil {
				for _, f := range found {
					for _, p := range projects {
						if p.Source == "modrinth" && p.ID == f.ID && f.Icon != "" {
							links[p] = f.Icon
						}
					}
				}
			}
		}
		for _, p := range projects {
			if p.Source != "hangar" {
				continue
			}
			var h struct {
				Avatar string `json:"avatarUrl"`
			}
			if getJSON(ctx, hc, hangar+"/projects/"+url.PathEscape(firstOf(p.Slug, p.ID)), &h) == nil && h.Avatar != "" {
				links[p] = h.Avatar
			}
		}
		out := map[Project][]byte{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		slots := make(chan struct{}, 8)
		for p, link := range links {
			wg.Add(1)
			go func() {
				defer wg.Done()
				slots <- struct{}{}
				defer func() { <-slots }()
				if b, err := getFile(ctx, hc, link); err == nil {
					mu.Lock()
					out[p] = b
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		return out
	}
}

const iconAgent = "CIYAhq/playkeeper site build (https://github.com/CIYAhq/playkeeper)"

func getJSON(ctx context.Context, hc *http.Client, u string, v any) error {
	b, err := getFile(ctx, hc, u)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// getFile reads u, up to maxIconFile bytes.
func getFile(ctx context.Context, hc *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", iconAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxIconFile+1))
	if err == nil && len(b) > maxIconFile {
		err = fmt.Errorf("%s is over %d bytes", u, maxIconFile)
	}
	return b, err
}
