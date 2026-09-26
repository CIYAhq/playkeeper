package panel

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// jsonFields adds the names of t's fields in JSON to into, with the fields
// of embedded structs as encoding/json flattens them.
func jsonFields(t reflect.Type, into map[string]bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct:
			jsonFields(f.Type, into)
		case !f.IsExported() || name == "-":
		case name == "":
			into[f.Name] = true
		default:
			into[name] = true
		}
	}
}

// The dashboard's types for the machine, join code and AI agent token
// answers the panel builds itself, checked like internal/api's contract test
// checks the agent's: a field the panel never sends is always undefined in
// the browser.
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
	found := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?ms)^export interface (\w+)(?: extends [\w, ]+)? \{\n(.*?)^\}`).FindAllStringSubmatch(string(src), -1) {
		v, ok := sent[m[1]]
		if !ok {
			continue
		}
		found[m[1]] = true
		names := map[string]bool{}
		jsonFields(reflect.TypeOf(v), names)
		for _, f := range field.FindAllStringSubmatch(m[2], -1) {
			if !names[f[1]] {
				t.Errorf("web/src/api/types.ts: %s.%s is not a JSON field of %T", m[1], f[1], v)
			}
		}
	}
	if len(found) != len(sent) {
		t.Fatalf("found %d of the %d interfaces in web/src/api/types.ts", len(found), len(sent))
	}
}
