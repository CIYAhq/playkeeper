package share

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The friends' share and pack page reach the dashboard as these types' JSON,
// which the api package's contract test can't check: that package would
// import itself through this one. A field the dashboard declares that the
// share never sends is always undefined in the browser.
func TestTheDashboardDeclaresOnlyFieldsTheShareSends(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "src", "api", "types.ts"))
	if err != nil {
		t.Fatal(err)
	}
	sent := map[string]any{
		"FriendsShare": Share{}, "SharePack": Pack{}, "ShareMod": Mod{}, "ShareYourself": Yourself{}, "ShareText": Text{},
		"PackPage": Page{}, "PackPageMod": PageMod{}, "PackLauncher": Launcher{},
	}
	addedByPanel := map[string]bool{"PackPage.hasIcon": true}
	field := regexp.MustCompile(`(?m)^  (\w+)\??:`)
	found := 0
	for _, m := range regexp.MustCompile(`(?ms)^export interface (\w+) \{\n(.*?)^\}`).FindAllStringSubmatch(string(src), -1) {
		v, ok := sent[m[1]]
		if !ok {
			continue
		}
		found++
		names := jsonNames(reflect.TypeOf(v))
		for _, f := range field.FindAllStringSubmatch(m[2], -1) {
			if !names[f[1]] && !addedByPanel[m[1]+"."+f[1]] {
				t.Errorf("web/src/api/types.ts: %s.%s is not a JSON field of share.%s", m[1], f[1], reflect.TypeOf(v).Name())
			}
		}
	}
	if found != len(sent) {
		t.Fatalf("found %d of the %d interfaces in web/src/api/types.ts", found, len(sent))
	}
}

func jsonNames(t reflect.Type) map[string]bool {
	names := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case !f.IsExported() || name == "-":
		case name == "":
			names[f.Name] = true
		default:
			names[name] = true
		}
	}
	return names
}
