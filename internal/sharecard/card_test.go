package sharecard

import (
	"bytes"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"
)

func TestEveryGlyphIsFiveByEight(t *testing.T) {
	for r, rows := range glyphRows {
		f := strings.Fields(rows)
		if len(f) != glyphHeight {
			t.Errorf("%q has %d rows", r, len(f))
		}
		for _, row := range f {
			if len(row) != glyphWidth || strings.Trim(row, "#.") != "" {
				t.Errorf("%q has the row %q", r, row)
			}
		}
	}
	for c := rune(' '); c <= '~'; c++ {
		if _, ok := glyphs[c]; !ok {
			t.Errorf("no glyph for %q", c)
		}
	}
}

func TestTextIsFoldedToWhatTheFontDraws(t *testing.T) {
	for in, want := range map[string]string{
		"Siya’s Café — “Noël”":     `Siya's Cafe - "Noel"`,
		"Straße 🎮  world…":         "Strasse world...",
		"Day 3 · Nether reached":   "Day 3 · Nether reached",
		"🎮🎮":                       "",
		"Łódź\tœuvre\u00a0Øresund": "Lodz oeuvre Oresund",
	} {
		if got := drawable(in); got != want {
			t.Errorf("drawable(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNamesWrapAndShrinkToFit(t *testing.T) {
	lines, sc := fit("Claude tries to beat Minecraft", 1072, []int{8, 7, 6, 5}, 2)
	if sc != 8 || len(lines) != 2 || lines[0] != "Claude tries to beat" || lines[1] != "Minecraft" {
		t.Fatalf("got %q at %d", lines, sc)
	}
	lines, sc = fit("Survival", 1072, []int{8, 7}, 2)
	if sc != 8 || len(lines) != 1 {
		t.Fatalf("a short name: %q at %d", lines, sc)
	}
	long := strings.Repeat("Supercalifragilistic ", 6)
	lines, sc = fit(long, 1072, []int{8, 7, 6, 5}, 2)
	if sc != 5 || len(lines) != 2 || !strings.HasSuffix(lines[1], "...") {
		t.Fatalf("a name too long for two lines: %q at %d", lines, sc)
	}
	for _, l := range lines {
		if width(l, sc) > 1072 {
			t.Errorf("%q is %d wide", l, width(l, sc))
		}
	}
	if lines, _ := fit(strings.Repeat("W", 400), 500, []int{4}, 1); len(lines) != 1 || width(lines[0], 4) > 500 {
		t.Fatalf("one long word: %q", lines)
	}
}

// A cut line keeps exactly what cutting a character at a time would, and a
// card with lines far too long costs about what one with short lines does.
func TestALongLineIsCutOffAtOnce(t *testing.T) {
	slow := func(s string, w, scale int) string {
		if width(s, scale) <= w {
			return s
		}
		r := []rune(s)
		for len(r) > 0 && width(string(r)+"...", scale) > w {
			r = r[:len(r)-1]
		}
		return strings.TrimRight(string(r), " ") + "..."
	}
	for _, s := range []string{"Claude tries to beat Minecraft", strings.Repeat("ab ", 300), strings.Repeat("W", 400), "a", ""} {
		for _, w := range []int{0, 5, 40, 500, 840, 1072} {
			for _, scale := range []int{1, 4, 5, 8} {
				if got, want := truncate(s, w, scale), slow(s, w, scale); got != want {
					t.Errorf("truncate(%d characters, %d, %d) = %q, want %q", len([]rune(s)), w, scale, got, want)
				}
			}
		}
	}
	long := strings.Repeat("a", 200000)
	done := make(chan error, 1)
	go func() {
		_, err := PNG(Card{Name: long, Status: long, Headline: long, Address: long})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a card with lines of 200,000 characters took over 10 seconds")
	}
}

func TestTheCardIsDrawn(t *testing.T) {
	for _, c := range []Card{
		{Name: "Claude tries to beat Minecraft", Status: "Online · 64 of 80 playing", Online: true, Headline: "Day 3 · Nether reached", Address: "ai.playkeeper.me"},
		{},
		{Name: "🎮", Status: "🎮", Headline: strings.Repeat("x", 500), Address: strings.Repeat("a", 300)},
	} {
		b, err := PNG(c)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dx() != Width || img.Bounds().Dy() != Height {
			t.Fatalf("the card is %v", img.Bounds())
		}
		if got := color.RGBAModel.Convert(img.At(8, 8)); got != chalk {
			t.Errorf("the corner is %v, want chalk", got)
		}
		if got := color.RGBAModel.Convert(img.At(10, Height-4)); got == chalk {
			t.Error("no ground along the bottom")
		}
	}
	dots := func(c Card) (on, off int) {
		b, _ := PNG(c)
		img, _ := png.Decode(bytes.NewReader(b))
		for x := margin; x < margin+16; x++ {
			for y := 0; y < Height; y++ {
				switch color.RGBAModel.Convert(img.At(x, y)) {
				case lit:
					on++
				case dim:
					off++
				}
			}
		}
		return on, off
	}
	if on, off := dots(Card{Name: "Survival", Status: "Online", Online: true}); on == 0 || off != 0 {
		t.Errorf("an online server's dot: %d lit and %d dim pixels", on, off)
	}
	if on, off := dots(Card{Name: "Survival", Status: "Offline"}); on != 0 || off == 0 {
		t.Errorf("an offline server's dot: %d lit and %d dim pixels", on, off)
	}
}
