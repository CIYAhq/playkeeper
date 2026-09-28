package site

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"io/fs"
	"math"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Social previews for the directory: each category's and template's own,
// drawn while the site builds, so a link shared on X or Discord shows what
// it is. They're drawn on the frame the site's other previews share
// (site/og/frame.png, from test/e2e/ui/site-og.mjs): what it is, its name,
// what it runs on and its picture, the same as its card's. The words are in
// Inter, as the other previews' are (site/og, under the SIL Open Font
// License in site/og/Inter-LICENSE.txt), cut down to Latin letters.

const previewW, previewH = 1200, 630

// Where things go: the words on the left, the picture in a card on the
// right, both above the ground.
const (
	textX, textW          = 72, 520
	eyebrowBase, titleTop = 214, 240
	picX, picY            = 648, 160
	picW, picH            = 480, 300
	picRadius             = 22
)

var (
	ogInk   = color.NRGBA{0x1d, 0x21, 0x1c, 0xff}
	ogSage  = color.NRGBA{0x5c, 0x61, 0x57, 0xff}
	ogGreen = color.NRGBA{0x16, 0x65, 0x34, 0xff}
	ogTwine = color.NRGBA{0xe6, 0xe6, 0xdf, 0xff}
)

// preview is what one preview shows.
type preview struct {
	Eyebrow, Title, Meta string
	// Picture is picW × picH.
	Picture image.Image
}

// previewer draws previews: the frame and the fonts, read once.
type previewer struct {
	frame      image.Image
	bold, semi *opentype.Font
}

func newPreviewer(src fs.FS) (*previewer, error) {
	b, err := fs.ReadFile(src, "site/og/frame.png")
	if err != nil {
		return nil, err
	}
	frame, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("site/og/frame.png: %w", err)
	}
	if frame.Bounds() != image.Rect(0, 0, previewW, previewH) {
		return nil, fmt.Errorf("site/og/frame.png is %v, not %d × %d", frame.Bounds().Size(), previewW, previewH)
	}
	p := &previewer{frame: frame}
	for name, f := range map[string]**opentype.Font{"Inter-ExtraBold.ttf": &p.bold, "Inter-SemiBold.ttf": &p.semi} {
		b, err := fs.ReadFile(src, "site/og/"+name)
		if err != nil {
			return nil, err
		}
		if *f, err = opentype.Parse(b); err != nil {
			return nil, fmt.Errorf("site/og/%s: %w", name, err)
		}
	}
	return p, nil
}

func (p *previewer) face(f *opentype.Font, size float64) (font.Face, error) {
	return opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
}

// draw makes v's PNG.
func (p *previewer) draw(v preview) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, previewW, previewH))
	draw.Draw(img, img.Bounds(), p.frame, image.Point{}, draw.Src)

	// The picture, in a card with a line around it and a soft shadow.
	card := image.Rect(picX, picY, picX+picW, picY+picH)
	for i := 12; i >= 1; i-- {
		fillRounded(img, card.Add(image.Pt(0, 10)).Inset(-i), picRadius+i, color.NRGBA{0x1d, 0x21, 0x1c, 3})
	}
	fillRounded(img, card.Inset(-1), picRadius+1, ogTwine)
	drawRounded(img, card, picRadius, v.Picture)

	eyebrow, err := p.face(p.semi, 22)
	if err != nil {
		return nil, err
	}
	drawText(img, eyebrow, textX, eyebrowBase, strings.ToUpper(v.Eyebrow), 22*0.08, ogGreen)

	// The title as big as fits in two lines, or three at the smallest sizes.
	var title font.Face
	var lines []string
	var size float64
	sizes := []float64{76, 68, 60, 54, 48, 42}
	for i, sz := range sizes {
		size = sz
		if title, err = p.face(p.bold, size); err != nil {
			return nil, err
		}
		if lines = wrapText(title, v.Title, -size*0.045, textW); len(lines) <= 2 || (i >= len(sizes)-2 && len(lines) <= 3) {
			break
		}
	}
	if len(lines) > 4 {
		return nil, fmt.Errorf("the title %q doesn't fit a preview", v.Title)
	}
	base := titleTop + title.Metrics().Ascent.Ceil()
	for i, l := range lines {
		drawText(img, title, textX, base+i*int(size*1.02), l, -size*0.045, ogInk)
	}
	// What it runs on, below: in two lines at most, smaller if it takes more.
	if v.Meta != "" {
		for _, sz := range []float64{30, 26, 22} {
			meta, err := p.face(p.semi, sz)
			if err != nil {
				return nil, err
			}
			ml := wrapText(meta, v.Meta, 0, textW)
			if len(ml) > 2 && sz > 22 {
				continue
			}
			for i, l := range ml[:min(len(ml), 2)] {
				drawText(img, meta, textX, base+(len(lines)-1)*int(size*1.02)+62+i*int(sz*1.3), l, 0, ogSage)
			}
			break
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// drawText draws s from x on the baseline y, tracking each letter apart by
// tracking pixels (negative draws them closer).
func drawText(dst draw.Image, f font.Face, x, y int, s string, tracking float64, c color.Color) {
	d := font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: f, Dot: fixed.P(x, y)}
	prev := rune(-1)
	for _, r := range s {
		if prev >= 0 {
			d.Dot.X += f.Kern(prev, r)
		}
		d.DrawString(string(r))
		d.Dot.X += fixed.Int26_6(tracking * 64)
		prev = r
	}
}

// textWidth is how wide drawText draws s.
func textWidth(f font.Face, s string, tracking float64) int {
	var w fixed.Int26_6
	prev := rune(-1)
	for _, r := range s {
		if prev >= 0 {
			w += f.Kern(prev, r)
		}
		adv, _ := f.GlyphAdvance(r)
		w += adv + fixed.Int26_6(tracking*64)
		prev = r
	}
	return (w - fixed.Int26_6(tracking*64)).Ceil()
}

// wrapText breaks s into lines at most width wide, at spaces, and a word
// too wide for a line of its own between letters.
func wrapText(f font.Face, s string, tracking float64, width int) []string {
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		for textWidth(f, w, tracking) > width {
			cut := len(w)
			for cut > 1 && textWidth(f, w[:cut], tracking) > width {
				_, n := utf8.DecodeLastRuneInString(w[:cut])
				cut -= n
			}
			if cur != "" {
				lines, cur = append(lines, cur), ""
			}
			lines, w = append(lines, w[:cut]), w[cut:]
		}
		next := strings.TrimSpace(cur + " " + w)
		if cur != "" && textWidth(f, next, tracking) > width {
			lines = append(lines, cur)
			next = w
		}
		cur = next
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// roundedCover is how much of the pixel whose centre is (x, y) a rectangle
// r with corners of radius rad covers, from 0 to 1.
func roundedCover(x, y float64, r image.Rectangle, rad float64) float64 {
	cx, cy := float64(r.Min.X+r.Max.X)/2, float64(r.Min.Y+r.Max.Y)/2
	qx := math.Abs(x-cx) - (float64(r.Dx())/2 - rad)
	qy := math.Abs(y-cy) - (float64(r.Dy())/2 - rad)
	dist := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - rad
	return math.Max(0, math.Min(1, 0.5-dist))
}

// fillRounded blends c over dst in r, with rounded corners.
func fillRounded(dst *image.RGBA, r image.Rectangle, rad int, c color.NRGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if a := roundedCover(float64(x)+0.5, float64(y)+0.5, r, float64(rad)); a > 0 {
				blend(dst, x, y, c, a)
			}
		}
	}
}

// drawRounded draws src, as big as r, into r with rounded corners.
func drawRounded(dst *image.RGBA, r image.Rectangle, rad int, src image.Image) {
	sb := src.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			a := roundedCover(float64(x)+0.5, float64(y)+0.5, r, float64(rad))
			if a == 0 {
				continue
			}
			c := color.NRGBAModel.Convert(src.At(sb.Min.X+x-r.Min.X, sb.Min.Y+y-r.Min.Y)).(color.NRGBA)
			blend(dst, x, y, c, a)
		}
	}
}

// blend puts c over dst's pixel (x, y), with c's alpha times cover.
func blend(dst *image.RGBA, x, y int, c color.NRGBA, cover float64) {
	i := dst.PixOffset(x, y)
	a := float64(c.A) / 255 * cover
	for k, v := range []uint8{c.R, c.G, c.B} {
		dst.Pix[i+k] = uint8(float64(v)*a + float64(dst.Pix[i+k])*(1-a) + 0.5)
	}
	dst.Pix[i+3] = uint8(math.Min(255, float64(dst.Pix[i+3])+a*255*(1-float64(dst.Pix[i+3])/255)+0.5))
}

// pixelArt draws one of the site's pixel-art scenes, an SVG of <rect>s and
// <path>s of straight lines filled with colours, at one pixel a unit.
func pixelArt(b []byte) (*image.NRGBA, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	var img *image.NRGBA
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		attr := map[string]string{}
		for _, a := range el.Attr {
			attr[a.Name.Local] = a.Value
		}
		switch el.Name.Local {
		case "svg":
			var x, y, w, h int
			if _, err := fmt.Sscanf(attr["viewBox"], "%d %d %d %d", &x, &y, &w, &h); err != nil || x != 0 || y != 0 || w <= 0 || h <= 0 || w > 512 || h > 512 {
				return nil, fmt.Errorf("a pixel-art scene's viewBox %q isn't 0 0 <width> <height>", attr["viewBox"])
			}
			img = image.NewNRGBA(image.Rect(0, 0, w, h))
		case "rect", "path":
			if img == nil {
				return nil, errors.New("a shape before the <svg>")
			}
			for _, k := range []string{"opacity", "fill-opacity", "transform", "style"} {
				if _, ok := attr[k]; ok {
					return nil, fmt.Errorf("a pixel-art shape with %s, which previews don't draw", k)
				}
			}
			c, err := hexColor(attr["fill"])
			if err != nil {
				return nil, err
			}
			var shapes [][][2]float64
			if el.Name.Local == "rect" {
				v := make([]float64, 4)
				for i, k := range []string{"x", "y", "width", "height"} {
					if v[i], err = strconv.ParseFloat(firstOf(attr[k], "0"), 64); err != nil {
						return nil, fmt.Errorf("a <rect>'s %s: %w", k, err)
					}
				}
				shapes = [][][2]float64{{{v[0], v[1]}, {v[0] + v[2], v[1]}, {v[0] + v[2], v[1] + v[3]}, {v[0], v[1] + v[3]}}}
			} else if shapes, err = pathShapes(attr["d"]); err != nil {
				return nil, err
			}
			for _, s := range shapes {
				fillPolygon(img, s, c)
			}
		case "g", "circle", "ellipse", "polygon", "polyline", "line", "text", "image", "use":
			return nil, fmt.Errorf("a pixel-art scene with <%s>, which previews don't draw", el.Name.Local)
		}
	}
	if img == nil {
		return nil, errors.New("not an SVG")
	}
	return img, nil
}

func hexColor(s string) (color.NRGBA, error) {
	s = strings.TrimPrefix(s, "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if len(s) != 6 || err != nil {
		return color.NRGBA{}, fmt.Errorf("a fill %q that isn't a #rrggbb colour", s)
	}
	return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 0xff}, nil
}

// pathShapes are the closed shapes of a path made of M, H, V, L and Z, in
// either case.
func pathShapes(d string) ([][][2]float64, error) {
	var shapes [][][2]float64
	var cur [][2]float64
	var x, y float64
	cmd := byte(0)
	rest := strings.TrimSpace(d)
	num := func() (float64, error) {
		rest = strings.TrimLeft(rest, " ,\n\t")
		end := strings.IndexFunc(rest, func(r rune) bool {
			return !(unicode.IsDigit(r) || r == '.' || r == '-' || r == '+' || r == 'e')
		})
		if end < 0 {
			end = len(rest)
		}
		if end == 0 {
			return 0, fmt.Errorf("a path %q with a number missing", d)
		}
		v, err := strconv.ParseFloat(rest[:end], 64)
		rest = rest[end:]
		return v, err
	}
	for rest = strings.TrimLeft(rest, " ,\n\t"); rest != ""; rest = strings.TrimLeft(rest, " ,\n\t") {
		if c := rest[0]; strings.IndexByte("MmHhVvLlZz", c) >= 0 {
			cmd, rest = c, rest[1:]
		} else if cmd == 0 {
			return nil, fmt.Errorf("a path %q that doesn't start with M", d)
		}
		rel := cmd >= 'a'
		var err error
		switch cmd | 0x20 {
		case 'm', 'l':
			var nx, ny float64
			if nx, err = num(); err == nil {
				ny, err = num()
			}
			if rel {
				nx, ny = x+nx, y+ny
			}
			if cmd|0x20 == 'm' {
				if len(cur) > 2 {
					shapes = append(shapes, cur)
				}
				cur = nil
				cmd = map[bool]byte{true: 'l', false: 'L'}[rel]
			}
			x, y = nx, ny
		case 'h':
			var v float64
			if v, err = num(); err == nil {
				x = map[bool]float64{true: x + v, false: v}[rel]
			}
		case 'v':
			var v float64
			if v, err = num(); err == nil {
				y = map[bool]float64{true: y + v, false: v}[rel]
			}
		case 'z':
			if len(cur) > 2 {
				shapes = append(shapes, cur)
			}
			if len(cur) > 0 {
				x, y = cur[0][0], cur[0][1]
			}
			cur = nil
			continue
		}
		if err != nil {
			return nil, err
		}
		cur = append(cur, [2]float64{x, y})
	}
	if len(cur) > 2 {
		shapes = append(shapes, cur)
	}
	return shapes, nil
}

// fillPolygon fills the pixels whose centres are inside poly, by the
// even-odd rule.
func fillPolygon(img *image.NRGBA, poly [][2]float64, c color.NRGBA) {
	b := img.Bounds()
	for py := b.Min.Y; py < b.Max.Y; py++ {
		for px := b.Min.X; px < b.Max.X; px++ {
			x, y, in := float64(px)+0.5, float64(py)+0.5, false
			for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
				a, e := poly[i], poly[j]
				if (a[1] > y) != (e[1] > y) && x < (e[0]-a[0])*(y-a[1])/(e[1]-a[1])+a[0] {
					in = !in
				}
			}
			if in {
				img.SetNRGBA(px, py, c)
			}
		}
	}
}

// scaled is src drawn picW × picH: pixel art by whole pixels, a picture
// smoothly.
func scaled(src image.Image, pixel bool) image.Image {
	dst := image.NewNRGBA(image.Rect(0, 0, picW, picH))
	var s xdraw.Scaler = xdraw.CatmullRom
	if pixel {
		s = xdraw.NearestNeighbor
	}
	s.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	return dst
}

// drawAll draws each preview, a few at a time.
func (p *previewer) drawAll(want map[string]preview) (map[string][]byte, error) {
	out := map[string][]byte{}
	var mu sync.Mutex
	var first error
	var wg sync.WaitGroup
	slots := make(chan struct{}, runtime.NumCPU())
	for key, v := range want {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			b, err := p.draw(v)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && first == nil {
				first = fmt.Errorf("the preview %s: %w", key, err)
			}
			out[key] = b
		}()
	}
	wg.Wait()
	return out, first
}

// addPreviews draws each listed template's preview, og/templates/<id>.png,
// and each category's without a guide, og/categories/<id>.png, and gives
// their pages them: a template's page and its /t/<id>, which is what a
// copied link shares, and a category's pages. A page with a preview of its
// own keeps it. Without Options.Previews, they share og/templates.png.
func (s *Site) addPreviews() error {
	if !s.opts.Previews {
		return nil
	}
	p, err := newPreviewer(s.opts.Root)
	if err != nil {
		return err
	}
	pictures := map[string]image.Image{}
	picture := func(art, thumb string) (image.Image, error) {
		key, file, pixel := art, art, true
		if thumb != "" {
			key, file, pixel = thumb, "shots/"+thumb+"-960w.webp", false
		}
		if img, ok := pictures[key]; ok {
			return img, nil
		}
		a := s.assets[file]
		if a == nil {
			return nil, fmt.Errorf("no picture %s", file)
		}
		var img image.Image
		var err error
		if pixel {
			img, err = pixelArt(a.data)
		} else {
			img, _, err = image.Decode(bytes.NewReader(a.data))
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		pictures[key] = scaled(img, pixel)
		return pictures[key], nil
	}
	want := map[string]preview{}
	for _, t := range s.dir.Templates {
		pic, err := picture(t.Art, t.thumb)
		if err != nil {
			return fmt.Errorf("%s's preview: %w", t.ID, err)
		}
		want["og/templates/"+t.ID+".png"] = preview{Eyebrow: "Server template", Title: t.Name, Meta: t.runsOn(), Picture: pic}
	}
	for _, c := range s.dir.Categories {
		if c.Guide() != nil {
			continue
		}
		pic, err := picture(c.Scene(), c.Thumb())
		if err != nil {
			return fmt.Errorf("the %s category's preview: %w", c.ID, err)
		}
		want["og/categories/"+c.ID+".png"] = preview{Eyebrow: "Server templates", Title: c.Name + " server templates", Meta: c.runsOn(), Picture: pic}
	}
	files, err := p.drawAll(want)
	if err != nil {
		return err
	}
	for key, b := range files {
		if s.assets[key], err = newAsset(key, b); err != nil {
			return err
		}
	}
	for _, pg := range s.pages {
		v := pg.dir
		if v == nil || (pg.OG != "" && pg.OG != sharedPreview && pg.OG != "t") {
			continue
		}
		switch {
		case (v.Kind == "template" || v.Kind == "open") && v.Template != nil:
			t := v.Template
			pg.OG, pg.OGAlt = "templates/"+t.ID, t.Name+", a Minecraft server template on Playkeeper: "+t.runsOn()
		case v.Kind == "category" && v.Category.Guide() == nil:
			c := v.Category
			pg.OG, pg.OGAlt = "categories/"+c.ID, c.Name+" Minecraft server templates on Playkeeper: "+c.runsOn()
		}
	}
	return nil
}

// runsOn is what a template's preview says it runs on: its server type
// and version, its memory, and crossplay when its check turned it on.
func (c *TemplateCard) runsOn() string {
	s := c.Loader().Name + " " + c.Version() + " · " + c.Memory()
	if c.Crossplay {
		s += " · Crossplay"
	}
	return s
}

// runsOn is what a category's preview says of it: how many templates, and
// on which server types.
func (c *Category) runsOn() string {
	var types []string
	for _, t := range serverTypes(rowOrder) {
		for _, x := range c.Templates {
			if x.Template.Server.Type == t.ID {
				types = append(types, t.Name)
				break
			}
		}
	}
	word := "templates"
	if len(c.Templates) == 1 {
		word = "template"
	}
	return count(len(c.Templates)) + " " + word + " · " + andList(types)
}
