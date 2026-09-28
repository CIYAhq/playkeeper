package site

import (
	"fmt"
	"strings"
)

// The share page (/t) shows a template's picture above what it holds. A
// template from the directory gets the same picture as its card and page, its
// thumbnail or its scene; any other template gets the scene of how its
// server is played. js/t.js only ever shows or hides pictures the page
// already has, so the page carries each one, marked with the templates it's
// for by the start of their links (shareKey).

// ShareArt is a picture the share page can show.
type ShareArt struct {
	// For are the keys of the directory templates it's the picture of,
	// separated by spaces.
	For string
	// Style is the play style (templates.Settings.PlayStyle) whose other
	// templates show it: friends, creative, hardcore, solo, or world for the
	// rest.
	Style string
	// Img is a scene, or Thumb a template's thumbnail.
	Img   *asset
	Thumb *Shot
}

// playArts are the scenes the share page shows by play style.
var playArts = [][2]string{
	{"friends", "app/pixel-art/play-with-friends.svg"},
	{"creative", "app/pixel-art/play-creative.svg"},
	{"hardcore", "app/pixel-art/play-hardcore.svg"},
	{"solo", "app/pixel-art/play-just-me.svg"},
	{"world", "app/pixel-art/world-normal.svg"},
}

// shareKey is what tells a template link's template apart on the share page:
// the first ten characters of its data, which carry its format, its length
// and the start of its checksum.
func shareKey(link string) string {
	_, data, _ := strings.Cut(link, "#")
	return data[:min(10, len(data))]
}

// shareArts are the pictures the share page carries: the play styles'
// scenes, then each other scene a directory template's card shows, and each
// thumbnail, with the templates they're for.
func (s *Site) shareArts() ([]ShareArt, error) {
	var out []ShareArt
	byArt := map[string]int{}
	for _, p := range playArts {
		a := s.assets[p[1]]
		if a == nil {
			return nil, fmt.Errorf("the share page's picture for %s, %s, isn't an asset", p[0], p[1])
		}
		byArt[p[1]] = len(out)
		out = append(out, ShareArt{Style: p[0], Img: a})
	}
	keys := map[string]string{}
	for _, c := range s.dir.Templates {
		key := shareKey(c.Link)
		if other, ok := keys[key]; ok || len(key) < 10 {
			return nil, fmt.Errorf("the share page can't tell %s's link from %s's", c.ID, other)
		}
		keys[key] = c.ID
		if c.thumb != "" {
			sh, err := s.shot(c.thumb, shareSizes)
			if err != nil {
				return nil, err
			}
			out = append(out, ShareArt{For: key, Thumb: sh})
			continue
		}
		i, ok := byArt[c.Art]
		if !ok {
			a := s.assets[c.Art]
			if a == nil {
				return nil, fmt.Errorf("cards.json: %s's art %q isn't an asset", c.ID, c.Art)
			}
			i = len(out)
			byArt[c.Art] = i
			out = append(out, ShareArt{Img: a})
		}
		out[i].For = strings.TrimSpace(out[i].For + " " + key)
	}
	return out, nil
}

// shareSizes is how wide the share page shows its picture.
const shareSizes = "(max-width: 551.98px) calc(100vw - 40px), 512px"
