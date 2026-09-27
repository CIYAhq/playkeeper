package worldimport

import (
	"encoding/json"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func guideTexts(g Guide) []Text {
	out := append([]Text{g.Name, g.Download}, g.Steps...)
	for _, n := range g.Notes {
		out = append(out, n.Text)
	}
	return out
}

// Help pages the guides follow; a new guide needs its host here.
var guideHosts = map[string]string{
	"singleplayer":  "help.minecraft.net",
	"realms":        "help.minecraft.net",
	"aternos":       "support.aternos.org",
	"minehut":       "support.minehut.com",
	"apexhosting":   "apexminecrafthosting.com",
	"bisecthosting": "help.bisecthosting.com",
	"shockbyte":     "shockbyte.com",
	"other":         "",
}

func TestGuides(t *testing.T) {
	guides := Guides()
	idPattern := regexp.MustCompile(`^[a-z]+$`)
	partPattern := regexp.MustCompile(`^(name|download|(step|note)\.[a-z][a-z_]*)$`)
	ids := map[string]bool{}
	keys := map[string]string{}
	for _, g := range guides {
		t.Run(g.ID, func(t *testing.T) {
			if !idPattern.MatchString(g.ID) || ids[g.ID] {
				t.Fatalf("ID %q is not lowercase letters, or is used twice", g.ID)
			}
			ids[g.ID] = true
			prefix := "worldimport.guide." + g.ID + "."
			if g.Name.Key != prefix+"name" || g.Download.Key != prefix+"download" {
				t.Errorf("name key %q, download key %q", g.Name.Key, g.Download.Key)
			}
			if len(g.Steps) < 3 {
				t.Errorf("%d steps, want at least 3", len(g.Steps))
			} else if last := g.Steps[len(g.Steps)-1].Key; last != prefix+"step.upload" {
				t.Errorf("the last step is %q, want the upload", last)
			}
			for _, s := range g.Steps {
				if !strings.HasPrefix(s.Key, prefix+"step.") {
					t.Errorf("step key %q", s.Key)
				}
			}
			for _, n := range g.Notes {
				if !strings.HasPrefix(n.Key, prefix+"note.") {
					t.Errorf("note key %q", n.Key)
				}
			}
			for i, x := range guideTexts(g) {
				if part, ok := strings.CutPrefix(x.Key, prefix); !ok || !partPattern.MatchString(part) {
					t.Errorf("key %q doesn't follow %s<part>", x.Key, prefix)
				}
				if other, ok := keys[x.Key]; ok {
					t.Errorf("key %q is used by %s too", x.Key, other)
				}
				keys[x.Key] = g.ID
				if x.Text == "" || strings.ContainsAny(x.Text, "{}") {
					t.Errorf("%s: text %q is empty or has a placeholder left", x.Key, x.Text)
				}
				for name, v := range x.Params {
					if v == "" || !strings.Contains(x.Text, v) {
						t.Errorf("%s: parameter %s=%q isn't in the text %q", x.Key, name, v, x.Text)
					}
				}
				if i > 0 {
					checkGuideSentence(t, x)
				}
			}
			host, ok := guideHosts[g.ID]
			switch {
			case !ok:
				t.Errorf("add the help page's host of %s to guideHosts", g.ID)
			case host == "":
				if g.HelpURL != "" {
					t.Errorf("help URL %q, want none", g.HelpURL)
				}
			default:
				u, err := url.Parse(g.HelpURL)
				if err != nil || u.Scheme != "https" || u.Host != host || len(u.Path) < 2 {
					t.Errorf("help URL %q, want an https page on %s", g.HelpURL, host)
				}
			}
		})
	}
	for _, id := range []string{"singleplayer", "realms", "aternos", "minehut", "other"} {
		if !ids[id] {
			t.Errorf("no guide for %s", id)
		}
	}
	if len(guides) < 7 {
		t.Errorf("%d guides, want Realms, Aternos, Minehut and at least two other hosts besides singleplayer and other", len(guides))
	}
	if guides[0].ID != "singleplayer" || guides[len(guides)-1].ID != "other" {
		t.Errorf("the picker starts with %s and ends with %s", guides[0].ID, guides[len(guides)-1].ID)
	}
}

// checkGuideSentence checks that a text is a full sentence, or ends with the
// link its url parameter gives.
func checkGuideSentence(t *testing.T, x Text) {
	t.Helper()
	if r, _ := utf8.DecodeRuneInString(x.Text); !unicode.IsUpper(r) {
		t.Errorf("%s: %q doesn't start with a capital letter", x.Key, x.Text)
	}
	link, ok := x.Params["url"]
	if !ok {
		if !strings.HasSuffix(x.Text, ".") {
			t.Errorf("%s: %q doesn't end with a full stop", x.Key, x.Text)
		}
		return
	}
	if u, err := url.Parse(link); err != nil || u.Scheme != "https" || u.Host == "" {
		t.Errorf("%s: link %q is not an https URL", x.Key, link)
	}
	if !strings.HasSuffix(x.Text, " "+link) {
		t.Errorf("%s: %q doesn't end with its link, which a full stop would break", x.Key, x.Text)
	}
}

func TestGuideByID(t *testing.T) {
	for _, g := range Guides() {
		got, ok := GuideByID(g.ID)
		if !ok || !reflect.DeepEqual(got, g) {
			t.Errorf("GuideByID(%q) = %+v, %v", g.ID, got, ok)
		}
	}
	for _, id := range []string{"", "Aternos", "nope", "worldimport.guide.aternos"} {
		if _, ok := GuideByID(id); ok {
			t.Errorf("GuideByID(%q) found a guide", id)
		}
	}
}

func TestGuidesReturnsFreshCopies(t *testing.T) {
	first := Guides()
	for i := range first {
		first[i].Name.Text = "changed"
		for j := range first[i].Steps {
			first[i].Steps[j].Text = "changed"
			for k := range first[i].Steps[j].Params {
				first[i].Steps[j].Params[k] = "changed"
			}
		}
		for j := range first[i].Notes {
			for k := range first[i].Notes[j].Params {
				first[i].Notes[j].Params[k] = "changed"
			}
		}
	}
	for _, g := range Guides() {
		for _, x := range guideTexts(g) {
			if x.Text == "changed" {
				t.Fatalf("%s: a caller's change shows up in later guides", x.Key)
			}
			for _, v := range x.Params {
				if v == "changed" {
					t.Fatalf("%s: a caller's change to the parameters shows up in later guides", x.Key)
				}
			}
		}
	}
}

func TestGuideJSON(t *testing.T) {
	var got struct {
		ID       string          `json:"id"`
		HelpURL  *string         `json:"helpUrl"`
		Name     json.RawMessage `json:"name"`
		Download json.RawMessage `json:"download"`
		Steps    []map[string]any
		Notes    []map[string]any
	}
	aternos, _ := GuideByID("aternos")
	b, err := json.Marshal(aternos)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "aternos" || got.HelpURL == nil || len(got.Name) == 0 || len(got.Download) == 0 {
		t.Errorf("aternos JSON: %s", b)
	}
	if params, _ := got.Steps[0]["params"].(map[string]any); params["url"] != "https://aternos.org/worlds/" {
		t.Errorf("first step: %v", got.Steps[0])
	}
	for _, n := range got.Notes {
		if n["key"] == "worldimport.guide.aternos.note.browser" && n["warning"] != true {
			t.Errorf("the download manager note is not a warning: %v", n)
		}
		if _, ok := n["text"]; !ok {
			t.Errorf("note without text: %v", n)
		}
	}
	other, _ := GuideByID("other")
	if b, err = json.Marshal(other); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "helpUrl") || strings.Contains(string(b), `"params"`) {
		t.Errorf("other JSON has a help URL or empty parameters: %s", b)
	}
}
