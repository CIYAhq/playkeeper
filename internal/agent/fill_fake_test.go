package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fillBuildSpec struct {
	id      int
	channel string
}

type fillVersionSpec struct {
	id      string
	support string
	builds  []fillBuildSpec
}

// fakeFill serves the parts of PaperMC's Fill v3 API the agent reads. Every
// build's jar checksum is sum: the fake Docker writes the same jar for any
// version.
type fakeFill struct {
	srv      *httptest.Server
	mu       sync.Mutex
	versions []fillVersionSpec
	sum      string
}

// defaultFill mirrors PaperMC's list closely enough for the tests: 26.3 has
// only alpha builds, 26.2 is the latest stable version, and 26.1.2 build 74
// is what Playkeeper 0.1.0 pinned.
func defaultFill() []fillVersionSpec {
	return []fillVersionSpec{
		{"26.3", "SUPPORTED", []fillBuildSpec{{41, "ALPHA"}}},
		{"26.2", "SUPPORTED", []fillBuildSpec{{129, "STABLE"}}},
		{"26.1.2", "UNSUPPORTED", []fillBuildSpec{{74, "STABLE"}}},
	}
}

func startFakeFill(t *testing.T, sum string) *fakeFill {
	ff := &fakeFill{versions: defaultFill(), sum: sum}
	ff.srv = httptest.NewServer(http.HandlerFunc(ff.serve))
	t.Cleanup(ff.srv.Close)
	return ff
}

func (ff *fakeFill) set(sum string, versions []fillVersionSpec) {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	if sum != "" {
		ff.sum = sum
	}
	if versions != nil {
		ff.versions = versions
	}
}

func (ff *fakeFill) serve(w http.ResponseWriter, r *http.Request) {
	ff.mu.Lock()
	versions, sum := ff.versions, ff.sum
	ff.mu.Unlock()
	version := func(v fillVersionSpec) map[string]any {
		return map[string]any{"id": v.id, "support": map[string]any{"status": v.support}, "java": map[string]any{"version": map[string]any{"minimum": 25}}}
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v3/projects/paper/versions")
	if rest == "" {
		var list []any
		for _, v := range versions {
			list = append(list, map[string]any{"version": version(v)})
		}
		json.NewEncoder(w).Encode(map[string]any{"versions": list})
		return
	}
	id, builds := strings.CutSuffix(strings.TrimPrefix(rest, "/"), "/builds")
	for _, v := range versions {
		if v.id != id {
			continue
		}
		if !builds {
			json.NewEncoder(w).Encode(map[string]any{"version": version(v)})
			return
		}
		var out []any
		for _, b := range v.builds {
			out = append(out, map[string]any{"id": b.id, "channel": b.channel, "downloads": map[string]any{"server:default": map[string]any{
				"name": fmt.Sprintf("paper-%s-%d.jar", v.id, b.id), "checksums": map[string]any{"sha256": sum}}}})
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}
