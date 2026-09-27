package site

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"image"
	_ "image/png" // PNG sizes for <img width height>
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// asset is a file the pages link to, served from /assets/ under a name that
// carries a hash of its content, so it can be cached for a year.
type asset struct {
	URL  string
	data []byte
	// Width and Height are an image's size in CSS pixels, for <img>.
	Width, Height int
}

// assets maps a file's name, like css/site.css or app/pip/pip-wave.svg, to
// the asset it is published as.
type assets map[string]*asset

// cssRef is how the stylesheet names another asset: url("@/img/x.svg").
var cssRef = regexp.MustCompile(`url\("@/([^"]+)"\)`)

// loadAssets reads every file of each root (prefix → folder in src) and gives
// it its hashed address. Stylesheets come last, so the assets they name have
// their addresses when the names are replaced.
func loadAssets(src fs.FS, roots map[string]string) (assets, error) {
	a := assets{}
	var css []string
	prefixes := make([]string, 0, len(roots))
	for p := range roots {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	for _, prefix := range prefixes {
		dir := roots[prefix]
		err := fs.WalkDir(src, dir, func(name string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			base := path.Base(name)
			if strings.HasPrefix(base, ".") || strings.HasSuffix(base, ".md") || strings.HasSuffix(base, ".txt") {
				return nil
			}
			b, err := fs.ReadFile(src, name)
			if err != nil {
				return err
			}
			key := path.Join(prefix, strings.TrimPrefix(name, dir+"/"))
			if path.Ext(name) == ".css" {
				css = append(css, key)
				a[key] = &asset{data: b}
				return nil
			}
			x, err := newAsset(key, b)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			a[key] = x
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, key := range css {
		var missing []string
		b := cssRef.ReplaceAllFunc(a[key].data, func(m []byte) []byte {
			ref := string(cssRef.FindSubmatch(m)[1])
			x, ok := a[ref]
			if !ok {
				missing = append(missing, ref)
				return m
			}
			return []byte(`url("` + x.URL + `")`)
		})
		if len(missing) > 0 {
			return nil, fmt.Errorf("%s names assets that don't exist: %s", key, strings.Join(missing, ", "))
		}
		x, err := newAsset(key, b)
		if err != nil {
			return nil, err
		}
		a[key] = x
	}
	return a, nil
}

func newAsset(key string, b []byte) (*asset, error) {
	sum := sha256.Sum256(b)
	ext := path.Ext(key)
	x := &asset{URL: "/assets/" + strings.TrimSuffix(key, ext) + "." + hex.EncodeToString(sum[:4]) + ext, data: b}
	var err error
	switch ext {
	case ".svg":
		x.Width, x.Height, err = svgSize(b)
	case ".png":
		var c image.Config
		c, _, err = image.DecodeConfig(bytes.NewReader(b))
		x.Width, x.Height = c.Width, c.Height
	case ".webp":
		x.Width, x.Height, err = webpSize(b)
	}
	return x, err
}

// svgSize reads an SVG's size from its width and height, or its viewBox.
func svgSize(b []byte) (int, int, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := d.Token()
		if err != nil {
			return 0, 0, fmt.Errorf("no <svg> element: %w", err)
		}
		el, ok := tok.(xml.StartElement)
		if !ok || el.Name.Local != "svg" {
			continue
		}
		var w, h float64
		var box []float64
		for _, at := range el.Attr {
			switch at.Name.Local {
			case "width":
				w, _ = strconv.ParseFloat(strings.TrimSuffix(at.Value, "px"), 64)
			case "height":
				h, _ = strconv.ParseFloat(strings.TrimSuffix(at.Value, "px"), 64)
			case "viewBox":
				for _, f := range strings.Fields(strings.ReplaceAll(at.Value, ",", " ")) {
					v, _ := strconv.ParseFloat(f, 64)
					box = append(box, v)
				}
			}
		}
		if (w == 0 || h == 0) && len(box) == 4 {
			w, h = box[2], box[3]
		}
		if w == 0 || h == 0 {
			return 0, 0, fmt.Errorf("an <svg> without a size or viewBox")
		}
		return int(w + 0.5), int(h + 0.5), nil
	}
}

// webpSize reads a WebP image's size from its header: lossy (VP8), lossless
// (VP8L) or extended (VP8X).
func webpSize(b []byte) (int, int, error) {
	if len(b) < 30 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0, fmt.Errorf("not a WebP image")
	}
	switch string(b[12:16]) {
	case "VP8 ":
		if b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return 0, 0, fmt.Errorf("a VP8 frame without its start code")
		}
		return int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff), nil
	case "VP8L":
		if b[20] != 0x2f {
			return 0, 0, fmt.Errorf("a VP8L image without its signature")
		}
		bits := binary.LittleEndian.Uint32(b[21:25])
		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, nil
	case "VP8X":
		w := int(b[24]) | int(b[25])<<8 | int(b[26])<<16
		h := int(b[27]) | int(b[28])<<8 | int(b[29])<<16
		return w + 1, h + 1, nil
	}
	return 0, 0, fmt.Errorf("an unknown WebP chunk %q", b[12:16])
}
