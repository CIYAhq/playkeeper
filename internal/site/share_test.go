package site

import (
	"regexp"
	"strings"
	"testing"
)

var (
	reShareArt = regexp.MustCompile(`<img (?:class="share-thumb" )?src="([^"]+)"(?: srcset="([^"]+)")?[^>]*?(?: data-for="([^"]*)")?(?: data-art="([a-z]+)")? loading="lazy" hidden>`)
	reCardArt  = regexp.MustCompile(`<div class="dcard-art[^"]*">(?:<picture><source type="image/avif" srcset="[^"]+" sizes="[^"]+">)?<img (?:class="[^"]*" )?src="([^"]+)"(?: srcset="([^"]+)")?`)
)

// shareArtFor is the picture the share page shows for a link with key, and
// the one it shows for each play style.
func shareArtFor(t *testing.T, page, key string) (own string, styles map[string]string) {
	t.Helper()
	styles = map[string]string{}
	for _, m := range reShareArt.FindAllStringSubmatch(page, -1) {
		if m[4] != "" {
			styles[m[4]] = m[1]
		}
		if strings.Contains(" "+m[3]+" ", " "+key+" ") {
			if own != "" {
				t.Errorf("the share page has two pictures for %s", key)
			}
			own = firstOf(m[2], m[1])
		}
	}
	return own, styles
}

// The share page shows a directory template with its card's picture, its
// thumbnail once it has one, and any other template with the scene of its
// play style.
func TestTheSharePageShowsATemplatesOwnPicture(t *testing.T) {
	for _, thumbs := range []bool{false, true} {
		o := build(t, Default)
		var err error
		if thumbs {
			if o, err = buildWith(thumbFiles("towny")); err != nil {
				t.Fatal(err)
			}
		}
		built := pages(o)
		share, hub := built["/t"], built["/templates"]
		idx := indexOf(t, o)
		for i, e := range idx.Templates {
			if i >= PerPage {
				break
			}
			m := reCardArt.FindStringSubmatch(cardOf(hub, e.Page))
			if m == nil {
				t.Fatalf("/templates has no picture on %s's card", e.ID)
			}
			card := firstOf(m[2], m[1])
			if thumbs && e.ID == "towny" {
				card = strings.Join(e.Thumb, " ")
			}
			link := regexp.MustCompile(`href="(/t#[^"]+)" data-template-open="` + e.ID + `"`).FindStringSubmatch(hub)
			if link == nil {
				t.Fatalf("/templates doesn't open %s", e.ID)
			}
			own, _ := shareArtFor(t, share, shareKey(link[1]))
			switch {
			case thumbs && e.ID == "towny":
				if !strings.Contains(own, e.Thumb[0]+" 480w") || !strings.Contains(own, e.Thumb[1]+" 960w") {
					t.Errorf("the share page shows Towny with %q, not its thumbnail %v", own, e.Thumb)
				}
			case own != card:
				t.Errorf("the share page shows %s with %q, and its card with %q", e.ID, own, card)
			}
		}
		_, styles := shareArtFor(t, share, "")
		for _, p := range playArts {
			if styles[p[0]] == "" {
				t.Errorf("the share page has no picture for the play style %s", p[0])
			}
		}
	}
}
