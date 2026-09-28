package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/templates"
)

// templateFile is one template in site/data/templates.
type templateFile struct {
	path string
	raw  []byte
	t    *templates.Template
	// opensFrom is the release that first opens it, from cards.json.
	opensFrom string
}

// weight is roughly how long its check takes: its memory, twice over for a
// modpack, whose files take longest to fetch.
func (f *templateFile) weight() int {
	w := cmp.Or(f.t.Settings.MemoryMB, 2048)
	if f.t.Modpack != nil {
		w *= 2
	}
	return w
}

func loadTemplates(root string) (map[string]*templateFile, error) {
	dir := filepath.Join(root, "site", "data", "templates")
	var cards map[string]struct {
		OpensFrom string `json:"opensFrom"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "cards.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &cards); err != nil {
		return nil, fmt.Errorf("cards.json: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]*templateFile{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok || id == "cards" {
			continue
		}
		p := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		t, err := templates.Decode(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out[id] = &templateFile{path: p, raw: raw, t: t, opensFrom: cards[id].OpensFrom}
	}
	return out, nil
}

// pick is the ids to check: those named, or all of them, and of those the
// shard asked for ("k/n"), split so each shard's weight is about the same.
func pick(all map[string]*templateFile, only []string, shard string) ([]string, error) {
	ids := slices.Sorted(func(yield func(string) bool) {
		for id := range all {
			if !yield(id) {
				return
			}
		}
	})
	if len(only) > 0 {
		for _, id := range only {
			if all[id] == nil {
				return nil, fmt.Errorf("%w: no template %q in site/data/templates", errNoTemplates, id)
			}
		}
		ids = slices.Clone(only)
		slices.Sort(ids)
		ids = slices.Compact(ids)
	}
	if shard == "" {
		return ids, nil
	}
	ks, ns, ok := strings.Cut(shard, "/")
	k, err1 := strconv.Atoi(ks)
	n, err2 := strconv.Atoi(ns)
	if !ok || err1 != nil || err2 != nil || n < 1 || k < 1 || k > n {
		return nil, fmt.Errorf("-shard %q is k/n, like 2/8", shard)
	}
	// Heaviest first, each to the lightest shard so far.
	byWeight := slices.Clone(ids)
	slices.SortStableFunc(byWeight, func(a, b string) int { return all[b].weight() - all[a].weight() })
	loads := make([]int, n)
	var mine []string
	for _, id := range byWeight {
		s := slices.Index(loads, slices.Min(loads))
		loads[s] += all[id].weight()
		if s == k-1 {
			mine = append(mine, id)
		}
	}
	slices.Sort(mine)
	return mine, nil
}
