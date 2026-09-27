package site

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Shot is a screenshot as a page shows it: an <img> of WebP files at the
// widths site/tools/shots.py made, in a <picture> with the same widths in
// AVIF for the browsers that take it.
type Shot struct {
	// Src is the file a browser without srcset shows.
	Src string
	// Width and Height are the narrowest file's size, for the page's layout.
	Width, Height int
	// Srcset and Avif list the files and their widths; Sizes says how wide
	// the page shows the screenshot, so the browser picks the file its
	// screen needs.
	Srcset, Avif, Sizes string
}

// Preload is a screenshot the page shows first, which its head asks for
// before the page is laid out: the AVIF files, with the same Sizes as the
// page's, and Media when only some screens show it.
type Preload struct {
	Avif, Sizes, Media string
}

// reShot names a screenshot's file: shots/<name>-<width>w.avif or .webp.
var reShot = regexp.MustCompile(`^shots/(.+)-(\d+)w\.(avif|webp)$`)

// shot is screenshot name's files, for a page that shows it at sizes.
func (s *Site) shot(name, sizes string) (*Shot, error) {
	if strings.TrimSpace(sizes) == "" {
		return nil, fmt.Errorf("screenshot %s has no Sizes: say how wide the page shows it", name)
	}
	type file struct {
		width int
		a     *asset
	}
	files := map[string][]file{}
	for key, a := range s.assets {
		m := reShot.FindStringSubmatch(key)
		if m == nil || m[1] != name {
			continue
		}
		w, _ := strconv.Atoi(m[2])
		if a.Width != w {
			return nil, fmt.Errorf("%s is %d pixels wide, not the %d its name says", key, a.Width, w)
		}
		files[m[3]] = append(files[m[3]], file{w, a})
	}
	webp, avif := files["webp"], files["avif"]
	if len(webp) == 0 {
		return nil, fmt.Errorf("no screenshot shots/%s-<width>w.webp; site/tools/shots.py makes them", name)
	}
	byWidth := func(a, b file) int { return a.width - b.width }
	slices.SortFunc(webp, byWidth)
	slices.SortFunc(avif, byWidth)
	if len(avif) != len(webp) {
		return nil, fmt.Errorf("screenshot %s has %d WebP files but %d AVIF", name, len(webp), len(avif))
	}
	for i := range webp {
		if avif[i].width != webp[i].width || avif[i].a.Height != webp[i].a.Height {
			return nil, fmt.Errorf("screenshot %s's AVIF and WebP files differ at %dw", name, webp[i].width)
		}
	}
	list := func(fs []file) string {
		out := make([]string, len(fs))
		for i, f := range fs {
			out[i] = f.a.URL + " " + strconv.Itoa(f.width) + "w"
		}
		return strings.Join(out, ", ")
	}
	return &Shot{Src: webp[len(webp)/2].a.URL, Width: webp[0].a.Width, Height: webp[0].a.Height, Srcset: list(webp), Avif: list(avif), Sizes: sizes}, nil
}
