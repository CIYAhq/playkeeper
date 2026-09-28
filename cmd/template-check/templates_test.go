package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/templates"
)

func files(t *testing.T, memory map[string]int, modpacks ...string) map[string]*templateFile {
	t.Helper()
	out := map[string]*templateFile{}
	for id, mb := range memory {
		tpl := &templates.Template{Settings: templates.Settings{MemoryMB: mb}}
		if slices.Contains(modpacks, id) {
			tpl.Modpack = &templates.Modpack{}
		}
		out[id] = &templateFile{t: tpl}
	}
	return out
}

// Shards share out every template once, and each carries about the same
// weight, a modpack counting twice its memory.
func TestShardsSplitTemplatesByWeight(t *testing.T) {
	all := files(t, map[string]int{"a": 4096, "b": 4096, "c": 3072, "d": 4096, "e": 6144, "f": 8192, "g": 2048, "h": 4096}, "e", "f")
	var seen []string
	var weights []int
	for k := 1; k <= 3; k++ {
		ids, err := pick(all, nil, strings.Join([]string{string(rune('0' + k)), "3"}, "/"))
		if err != nil {
			t.Fatal(err)
		}
		w := 0
		for _, id := range ids {
			w += all[id].weight()
		}
		seen = append(seen, ids...)
		weights = append(weights, w)
	}
	slices.Sort(seen)
	if !slices.Equal(seen, []string{"a", "b", "c", "d", "e", "f", "g", "h"}) {
		t.Fatalf("the shards hold %v", seen)
	}
	if slices.Max(weights)-slices.Min(weights) > 8192 {
		t.Errorf("uneven shards: %v", weights)
	}
	if ids, err := pick(all, nil, "4/3"); err == nil {
		t.Errorf("shard 4 of 3 gave %v", ids)
	}
	if ids, err := pick(all, []string{"zz"}, ""); err == nil {
		t.Errorf("an unknown template gave %v", ids)
	}
	if ids, _ := pick(all, []string{"b", "a", "a"}, ""); !slices.Equal(ids, []string{"a", "b"}) {
		t.Errorf("-only a,b gave %v", ids)
	}
}
