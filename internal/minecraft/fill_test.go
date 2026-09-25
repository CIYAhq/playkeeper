package minecraft

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeBuild struct {
	id      int
	channel string
	sha     string
}

type fakeVersion struct {
	id      string
	support string
	java    int
	builds  []fakeBuild
}

func sha(n int) string { return fmt.Sprintf("%064x", n) }

// fillToday mirrors PaperMC on 2026-09-25: 26.3 has only alpha builds, 26.2
// is the newest version with stable builds.
var fillToday = []fakeVersion{
	{"26.3", "SUPPORTED", 25, []fakeBuild{{41, "ALPHA", sha(341)}, {40, "ALPHA", sha(340)}}},
	{"26.3-rc-3", "UNSUPPORTED", 25, []fakeBuild{{1, "ALPHA", sha(31)}}},
	{"26.2", "SUPPORTED", 25, []fakeBuild{{130, "BETA", sha(2130)}, {129, "STABLE", sha(2129)}, {128, "STABLE", sha(2128)}}},
	{"26.2-rc-2", "UNSUPPORTED", 25, []fakeBuild{{6, "BETA", sha(26)}}},
	{"26.1.2", "UNSUPPORTED", 25, []fakeBuild{{74, "STABLE", "1d70b1dab9cf4a6de615209a536f3a45a2186240253c428213ce2188ab95e5f7"}}},
	{"26.1.1", "UNSUPPORTED", 25, []fakeBuild{{20, "STABLE", sha(1120)}}},
	{"1.21.11", "UNSUPPORTED", 21, []fakeBuild{{132, "STABLE", sha(21132)}}},
	{"1.21.10", "UNSUPPORTED", 21, []fakeBuild{{50, "STABLE", sha(2110)}}},
	{"1.20.6", "UNSUPPORTED", 21, []fakeBuild{{151, "STABLE", sha(206)}}},
}

type fakeFill struct {
	mu       sync.Mutex
	versions []fakeVersion
	requests []string
}

func startFakeFill(t *testing.T, versions []fakeVersion) (*fakeFill, Fill) {
	f := &fakeFill{versions: versions}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, Fill{BaseURL: srv.URL, Client: srv.Client()}
}

func (f *fakeFill) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.URL.Path)
	f.mu.Unlock()
	version := func(v fakeVersion) map[string]any {
		return map[string]any{"id": v.id, "support": map[string]any{"status": v.support}, "java": map[string]any{"version": map[string]any{"minimum": v.java}}}
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v3/projects/paper/versions")
	if rest == "" {
		var list []any
		for _, v := range f.versions {
			list = append(list, map[string]any{"version": version(v)})
		}
		json.NewEncoder(w).Encode(map[string]any{"versions": list})
		return
	}
	id, builds := strings.CutSuffix(strings.TrimPrefix(rest, "/"), "/builds")
	for _, v := range f.versions {
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
				"name": fmt.Sprintf("paper-%s-%d.jar", v.id, b.id), "checksums": map[string]any{"sha256": b.sha}}}})
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(`{"ok":false,"error":"version_not_found"}`))
}

func TestCatalogOffersTheLatestStableFirstAndExperimentalWithAWarning(t *testing.T) {
	f, fill := startFakeFill(t, fillToday)
	got, err := fill.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range got {
		ids = append(ids, fmt.Sprintf("%s#%d", e.MinecraftVersion, e.PaperBuild))
	}
	if want := "26.3#41 26.2#129 26.1.2#74 1.21.11#132"; strings.Join(ids, " ") != want {
		t.Fatalf("offered %v, want %s", ids, want)
	}
	exp, rec := got[0], got[1]
	if !exp.Experimental || exp.Recommended || exp.Channel != "ALPHA" || !strings.Contains(exp.Notes, "Experimental") || !strings.Contains(exp.Notes, "cannot go back") {
		t.Errorf("26.3 must be offered as experimental with a warning: %+v", exp)
	}
	if !rec.Recommended || rec.Experimental || rec.Channel != "STABLE" || rec.JarSHA256 != sha(2129) || rec.ID != "paper-26.2" {
		t.Errorf("26.2's latest stable build (not its newer beta) must be recommended with its checksum: %+v", rec)
	}
	for _, e := range got[2:] {
		if e.Recommended || e.Experimental || e.Supported || !strings.Contains(e.Notes, "no longer updates") {
			t.Errorf("%s is an older, unsupported version: %+v", e.MinecraftVersion, e)
		}
	}
	for _, p := range f.requests {
		for _, skipped := range []string{"26.1.1", "1.21.10", "1.20.6", "rc"} {
			if strings.Contains(p, skipped) {
				t.Errorf("%s was fetched although it is not offered", p)
			}
		}
	}
}

func TestCatalogSkipsBuildsWithoutAChecksumAndVersionsTheImageCannotRun(t *testing.T) {
	_, fill := startFakeFill(t, []fakeVersion{
		{"27.1", "SUPPORTED", 27, []fakeBuild{{5, "STABLE", sha(5)}}},
		{"26.2", "SUPPORTED", 25, []fakeBuild{{131, "STABLE", "not-a-checksum"}, {129, "STABLE", sha(2129)}}},
	})
	got, err := fill.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MinecraftVersion != "26.2" || got[0].PaperBuild != 129 {
		t.Fatalf("want only 26.2 build 129 (27.1 needs Java 27, build 131 has no checksum): %+v", got)
	}
}

func TestRestoreBuildKeepsTheBackupsBuildWhenNoStableOneIsNewer(t *testing.T) {
	_, fill := startFakeFill(t, fillToday)
	ctx := context.Background()
	for _, tc := range []struct {
		mc    string
		build int
		want  int
		exp   bool
	}{
		{"26.2", 100, 129, false},
		{"26.3", 40, 40, true},
		{"26.1.2", 74, 74, false},
	} {
		e, err := fill.RestoreBuild(ctx, tc.mc, tc.build)
		if err != nil || e.PaperBuild != tc.want || e.Experimental != tc.exp {
			t.Errorf("restore %s build %d: got %+v %v, want build %d experimental=%v", tc.mc, tc.build, e, err, tc.want, tc.exp)
		}
	}
	for _, tc := range []struct{ mc, why string }{{"9.9.9", "no Paper build"}, {"1.20.6", "1.21 and newer"}, {"26.3", "no usable build"}} {
		build := 1
		if tc.mc == "26.3" {
			build = 99
		}
		if _, err := fill.RestoreBuild(ctx, tc.mc, build); err == nil || !strings.Contains(err.Error(), tc.why) {
			t.Errorf("restore %s: want an error about %q, got %v", tc.mc, tc.why, err)
		}
	}
}

func TestCompareMinecraft(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{{"26.2", "26.1.2", 1}, {"1.21.11", "26.2", -1}, {"26.2", "26.2.0", 0}, {"1.21.10", "1.21.9", 1}, {"26.3", "26.3", 0}} {
		if got := CompareMinecraft(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareMinecraft(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
