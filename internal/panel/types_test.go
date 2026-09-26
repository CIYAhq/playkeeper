package panel

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// jsonFields adds the JSON names of t's fields to names, with those of the
// structs it embeds, as encoding/json sends them.
func jsonFields(t reflect.Type, names map[string]bool) map[string]bool {
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case name == "-":
		case f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct:
			jsonFields(f.Type, names)
		case !f.IsExported():
		case name == "":
			names[f.Name] = true
		default:
			names[name] = true
		}
	}
	return names
}

// A field the dashboard declares that the panel never sends is always
// undefined in the browser. These are the answers the panel builds from its
// own structs; internal/api checks the agent's types.
func TestTheDashboardDeclaresOnlyFieldsThePanelSends(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "api", "types.ts"))
	if err != nil {
		t.Fatal(err)
	}
	sent := map[string]any{
		"AgentActivity": agentActivityView{}, "ApiToken": tokenView{}, "DialAddress": dialAddress{}, "JoinCode": joinCodeView{},
		"JoinCommand": joinCommandView{}, "MachineEvent": machineEventView{}, "MachineView": machineView{},
	}
	field := regexp.MustCompile(`(?m)^  (\w+)\??:`)
	found := 0
	for _, m := range regexp.MustCompile(`(?ms)^export interface (\w+)(?: extends \w+)? \{\n(.*?)^\}`).FindAllStringSubmatch(string(src), -1) {
		v, ok := sent[m[1]]
		if !ok {
			continue
		}
		found++
		names := jsonFields(reflect.TypeOf(v), map[string]bool{})
		for _, f := range field.FindAllStringSubmatch(m[2], -1) {
			if !names[f[1]] {
				t.Errorf("web/src/api/types.ts: %s.%s is not a JSON field of %s", m[1], f[1], reflect.TypeOf(v))
			}
		}
	}
	if found != len(sent) {
		t.Fatalf("found %d of the %d interfaces in web/src/api/types.ts", found, len(sent))
	}
}
