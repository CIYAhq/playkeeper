package site

import (
	"os"
	"strings"
	"testing"
)

// The colour codes page's tables and the script that draws Minecraft text
// give every code the same name and colours, and the page lists each with
// its § and & codes and its hex.
func TestMinecraftColorsMatchTheScript(t *testing.T) {
	js, err := os.ReadFile("../../site/static/js/tools/mc-text.js")
	if err != nil {
		t.Fatal(err)
	}
	css, err := os.ReadFile("../../site/static/css/tools/mc-text.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range minecraftColors {
		want := c.Code + ": ['" + c.Name + "', '" + c.ID + "', '" + c.Java + "', '" + c.Bedrock + "'],"
		if !strings.Contains(string(js), want) {
			t.Errorf("mc-text.js doesn't have %s", want)
		}
		if !strings.Contains(string(css), ".mc-c-"+c.Code+" { background: "+c.Java+"; }") {
			t.Errorf("mc-text.css doesn't draw the %s button in %s", c.Name, c.Java)
		}
	}
	for _, c := range bedrockColors {
		want := c.Code + ": ['" + c.Name + "', '" + c.ID + "', '" + c.Bedrock + "'],"
		if !strings.Contains(string(js), want) {
			t.Errorf("mc-text.js doesn't have %s", want)
		}
		if !strings.Contains(string(css), ".mc-c-"+c.Code+" { background: "+c.Bedrock+"; }") {
			t.Errorf("mc-text.css doesn't draw the %s button in %s", c.Name, c.Bedrock)
		}
	}
	page := pages(build(t, Default))["/tools/color-codes"]
	for _, c := range append(append([]MinecraftColor{}, minecraftColors...), bedrockColors...) {
		for _, want := range []string{`data-copy-text="§` + c.Code + `"`, strings.ToUpper(firstOf(c.Java, c.Bedrock))} {
			if !strings.Contains(page, want) {
				t.Errorf("/tools/color-codes doesn't show %s for %s", want, c.Name)
			}
		}
	}
}
