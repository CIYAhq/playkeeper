// Package apitest holds what the tests of the packages that serve the API
// share.
package apitest

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

var (
	iface = regexp.MustCompile(`(?ms)^export interface (\w+)(?: extends [\w, ]+)? \{\n(.*?)^\}`)
	field = regexp.MustCompile(`(?m)^  (\w+)\??:`)
)

// Undeclared compares the interfaces in src, the source of
// web/src/api/types.ts, with the Go values sent says the API sends for them.
// It returns a problem for each field an interface declares that isn't a JSON
// field of its Go value, unless addedByPanel has it as "Interface.field", and
// for each interface of sent that src doesn't have. An interface that extends
// another is checked for the fields it adds.
func Undeclared(src string, sent map[string]any, addedByPanel map[string]bool) []string {
	var out []string
	found := map[string]bool{}
	for _, m := range iface.FindAllStringSubmatch(src, -1) {
		v, ok := sent[m[1]]
		if !ok {
			continue
		}
		found[m[1]] = true
		t := reflect.TypeOf(v)
		names := JSONNames(t)
		for _, f := range field.FindAllStringSubmatch(m[2], -1) {
			if !names[f[1]] && !addedByPanel[m[1]+"."+f[1]] {
				out = append(out, fmt.Sprintf("web/src/api/types.ts: %s.%s is not a JSON field of %s", m[1], f[1], t))
			}
		}
	}
	for name := range sent {
		if !found[name] {
			out = append(out, fmt.Sprintf("web/src/api/types.ts has no interface %s", name))
		}
	}
	sort.Strings(out)
	return out
}

// JSONNames returns the names encoding/json gives the fields of a struct
// type, counting the fields of an embedded struct as its own.
func JSONNames(t reflect.Type) map[string]bool {
	names := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case name == "-":
		case f.Anonymous && name == "" && ft.Kind() == reflect.Struct:
			for n := range JSONNames(ft) {
				names[n] = true
			}
		case !f.IsExported():
		case name == "":
			names[f.Name] = true
		default:
			names[name] = true
		}
	}
	return names
}
