// Package sharecard draws a public server page's share card: the picture
// link previews show (og:image), with the server's name, how it's doing,
// the headline its owner's tools posted and its address, in Playkeeper's
// look: chalk, ink and green, a pixel font, Pip on pixel ground. Its
// pictures are made from the dashboard's own SVGs by
// test/e2e/ui/sharecard-assets.mjs.
package sharecard

import (
	"bytes"
	"embed"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"unicode/utf8"
)

// The card's size: what X, Discord and the other previews show best.
const (
	Width  = 1200
	Height = 630
)

// Card is what a share card shows.
type Card struct {
	Name string
	// Status is how the server is doing, such as "Online · 12 of 80
	// playing"; Online colours it.
	Status string
	Online bool
	// Headline is the line the owner's tools posted, if any.
	Headline string
	Address  string
}

//go:embed assets/mark.png assets/pip.png assets/ground.png
var assets embed.FS

var (
	chalk = color.RGBA{0xf2, 0xf2, 0xec, 0xff}
	ink   = color.RGBA{0x1d, 0x21, 0x1c, 0xff}
	sage  = color.RGBA{0x5c, 0x61, 0x57, 0xff}
	green = color.RGBA{0x15, 0x80, 0x3d, 0xff}
	lit   = color.RGBA{0x16, 0xa3, 0x4a, 0xff}
	dim   = color.RGBA{0x9a, 0x9e, 0x94, 0xff}
)

const (
	margin = 64
	// groundTop is where the pixel ground starts; Pip stands on it.
	groundTop = Height - 72
	// narrow is how wide the lines under the name may be, clear of Pip.
	narrow = 840
)

// PNG draws the card.
func PNG(c Card) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, Width, Height))
	draw.Draw(img, img.Bounds(), &image.Uniform{chalk}, image.Point{}, draw.Src)
	ground, err := picture("ground.png")
	if err != nil {
		return nil, err
	}
	for x := 0; x < Width; x += ground.Bounds().Dx() {
		draw.Draw(img, ground.Bounds().Add(image.Pt(x, groundTop)), ground, image.Point{}, draw.Over)
	}
	pip, err := picture("pip.png")
	if err != nil {
		return nil, err
	}
	draw.Draw(img, pip.Bounds().Add(image.Pt(Width-margin-pip.Bounds().Dx(), groundTop-pip.Bounds().Dy()+30)), pip, image.Point{}, draw.Over)
	mark, err := picture("mark.png")
	if err != nil {
		return nil, err
	}
	draw.Draw(img, mark.Bounds().Add(image.Pt(margin, 52)), mark, image.Point{}, draw.Over)
	write(img, "Playkeeper", margin+mark.Bounds().Dx()+18, 52+(mark.Bounds().Dy()-7*4)/2, 4, ink)

	y := 160
	lines, scale := fit(drawable(c.Name), Width-2*margin, []int{8, 7, 6, 5}, 2)
	for _, l := range lines {
		write(img, l, margin, y, scale, ink)
		y += (glyphHeight + lineGap) * scale
	}
	y += 12
	if status := drawable(c.Status); status != "" {
		tone, dot := sage, dim
		if c.Online {
			tone, dot = green, lit
		}
		draw.Draw(img, image.Rect(margin, y+6, margin+16, y+22), &image.Uniform{dot}, image.Point{}, draw.Src)
		write(img, truncate(status, narrow-30, 4), margin+30, y, 4, tone)
		y += (glyphHeight + lineGap + 4) * 4
	}
	if headline, sc := fit(drawable(c.Headline), narrow, []int{5, 4}, 1); len(headline) > 0 {
		write(img, headline[0], margin, y, sc, ink)
	}
	if address := drawable(c.Address); address != "" {
		write(img, truncate("Join at "+address, narrow, 4), margin, groundTop-56, 4, green)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func picture(name string) (image.Image, error) {
	f, err := assets.Open("assets/" + name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

// width is how wide s is at scale, in pixels.
func width(s string, scale int) int {
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return 0
	}
	return (n*advance - 1) * scale
}

// fit lays s out in at most maxLines lines no wider than w, at the largest
// of scales that fits it whole, or at the smallest with the rest cut off.
func fit(s string, w int, scales []int, maxLines int) ([]string, int) {
	if s == "" {
		return nil, scales[0]
	}
	for _, sc := range scales {
		if lines, whole := wrap(s, w, sc, maxLines); whole {
			return lines, sc
		}
	}
	sc := scales[len(scales)-1]
	lines, _ := wrap(s, w, sc, maxLines)
	return lines, sc
}

// wrap breaks s into at most maxLines lines no wider than w at scale,
// between words, and reports whether all of it fit; the last line is cut
// when it didn't.
func wrap(s string, w, scale, maxLines int) ([]string, bool) {
	var lines []string
	line := ""
	words := strings.Fields(s)
	for i, word := range words {
		next := strings.TrimSpace(line + " " + word)
		if width(next, scale) <= w {
			line = next
			continue
		}
		if line == "" || len(lines) == maxLines-1 {
			rest := strings.Join(words[i:], " ")
			if line != "" {
				rest = line + " " + rest
			}
			return append(lines, truncate(rest, w, scale)), false
		}
		lines = append(lines, line)
		line = word
	}
	if width(line, scale) > w {
		return append(lines, truncate(line, w, scale)), false
	}
	return append(lines, line), true
}

// truncate cuts s to fit w at scale, ending in "..." when it had to. Every
// glyph advances as far, so it keeps as many characters as fit at once,
// however long s is.
func truncate(s string, w, scale int) string {
	if width(s, scale) <= w {
		return s
	}
	keep := max(0, (w/scale+1)/advance-len("..."))
	r := []rune(s)
	return strings.TrimRight(string(r[:min(keep, len(r))]), " ") + "..."
}

// write draws s with its top left at x, y, each font pixel scale pixels
// wide.
func write(img *image.RGBA, s string, x, y, scale int, c color.RGBA) {
	u := &image.Uniform{c}
	for _, r := range s {
		g := glyphs[r]
		for row := range glyphHeight {
			for col := range glyphWidth {
				if g[row]&(1<<(glyphWidth-1-col)) == 0 {
					continue
				}
				px, py := x+col*scale, y+row*scale
				draw.Draw(img, image.Rect(px, py, px+scale, py+scale), u, image.Point{}, draw.Src)
			}
		}
		x += advance * scale
	}
}
