package worldimport

import (
	"strings"
	"testing"
)

func TestCheckVersion(t *testing.T) {
	cases := []struct {
		name     string
		lv       Level
		target   string
		compat   Compat
		problem  string
		warnings []string
	}{
		{"same release", Level{Version: "1.21.4", DataVersion: 4189, Series: "main"}, "1.21.4", CompatSame, "", nil},
		{"older world", Level{Version: "1.20.1", DataVersion: 3465, Series: "main"}, "1.21.4", CompatUpgrade, "", []string{KindUpgrade}},
		{"newer world", Level{Version: "1.21.11", DataVersion: 4671, Series: "main"}, "1.21.4", CompatNewer, KindWorldNewer, nil},
		{"26.1 world on a 1.21 server", Level{Version: "26.1", DataVersion: 4786}, "1.21.11", CompatNewer, KindWorldNewer, nil},
		{"1.21 world on a 26.2 server", Level{Version: "1.21.11", DataVersion: 4671}, "26.2", CompatUpgrade, "", []string{KindUpgrade}},
		{"snapshot world", Level{Version: "26.2-snapshot-3", DataVersion: 4850, Snapshot: true}, "26.2", CompatUpgrade, "", []string{KindUpgrade, KindSnapshot}},
		{"release after the table, known world", Level{Version: "26.3", DataVersion: 5023}, "26.4", CompatUpgrade, "", []string{KindUpgrade}},
		{"release after the table, newer world", Level{Version: "26.5", DataVersion: 5300}, "26.4", CompatNewer, KindWorldNewer, nil},
		{"release after the table, snapshot world", Level{Version: "26.4-snapshot-2", DataVersion: 5100, Snapshot: true}, "26.4", CompatUnknown, "", []string{KindUnknownVersion, KindSnapshot}},
		{"release before the table", Level{Version: "1.12.2", DataVersion: 1343}, "1.12.2", CompatSame, "", nil},
		{"release before the table, newer world", Level{Version: "1.16.5", DataVersion: 2586}, "1.12.2", CompatNewer, KindWorldNewer, nil},
		{"release before the table, unnamed newer world", Level{DataVersion: 2586}, "1.14.4", CompatNewer, KindWorldNewer, nil},
		{"release before the table, unnamed old world", Level{DataVersion: 1343}, "1.14.4", CompatUnknown, "", []string{KindUnknownVersion}},
		{"server on a release candidate", Level{Version: "1.21.4", DataVersion: 4189}, "26.3-rc-1", CompatUnknown, "", []string{KindUnknownVersion}},
		{"world without a version", Level{}, "1.21.4", CompatUpgrade, "", []string{KindNoVersion}},
		{"experimental build", Level{Version: "1.18 experimental snapshot 1", DataVersion: 2825, Series: "ccpreview"}, "1.21.4", CompatUnknown, KindWorldSeries, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CheckVersion(&c.lv, c.target)
			if got.Compat != c.compat {
				t.Errorf("compat %s, want %s", got.Compat, c.compat)
			}
			problem := ""
			if got.Problem != nil {
				problem = got.Problem.Kind
			}
			if problem != c.problem {
				t.Errorf("problem %q, want %q", problem, c.problem)
			}
			if k := kinds(got.Warnings); !equalLists(k, c.warnings) {
				t.Errorf("warnings %v, want %v", k, c.warnings)
			}
			all := got.Warnings
			if got.Problem != nil {
				all = append(all, *got.Problem)
			}
			for _, m := range all {
				if !strings.HasSuffix(m.Text, ".") || m.Hint != "" && !strings.HasSuffix(m.Hint, ".") {
					t.Errorf("%s: text %q and hint %q should be sentences", m.Kind, m.Text, m.Hint)
				}
			}
		})
	}
}

func TestCheckVersionExplains(t *testing.T) {
	c := CheckVersion(&Level{Version: "1.21.11", DataVersion: 4671}, "1.21.4")
	if c.World != "1.21.11" || c.DataVersion != 4671 || c.Target != "1.21.4" || c.TargetData != 4189 {
		t.Errorf("check %+v", c)
	}
	p := c.Problem
	if p == nil || p.Params["world"] != "1.21.11" || p.Params["target"] != "1.21.4" ||
		!strings.Contains(p.Text, "newer than this server's Minecraft 1.21.4") || !strings.Contains(p.Hint, "Minecraft 1.21.11 or newer") {
		t.Errorf("problem %+v", p)
	}

	c = CheckVersion(&Level{DataVersion: 1343}, "1.14.4")
	if w := c.Warnings[0]; w.Params["dataVersion"] != 1343 || !strings.Contains(w.Text, "data version 1343") {
		t.Errorf("warning %+v", w)
	}
}

func TestReleaseDataIncreases(t *testing.T) {
	versions := []string{"1.16", "1.16.1", "1.16.2", "1.16.3", "1.16.4", "1.16.5", "1.17", "1.17.1", "1.18", "1.18.1",
		"1.18.2", "1.19", "1.19.1", "1.19.2", "1.19.3", "1.19.4", "1.20", "1.20.1", "1.20.2", "1.20.3", "1.20.4", "1.20.5",
		"1.20.6", "1.21", "1.21.1", "1.21.2", "1.21.3", "1.21.4", "1.21.5", "1.21.6", "1.21.7", "1.21.8", "1.21.9",
		"1.21.10", "1.21.11", "26.1", "26.1.1", "26.1.2", "26.2", "26.3"}
	prev, prevVersion := 0, ""
	for _, v := range versions {
		dv, ok := releaseData(v)
		switch {
		case !ok:
			t.Errorf("no data version for %s", v)
		case dv <= prev:
			t.Errorf("%s has data version %d, not more than %s's %d", v, dv, prevVersion, prev)
		case !isRelease(v):
			t.Errorf("%s is not recognised as a release", v)
		case prevVersion != "" && compareVersions(prevVersion, v) >= 0:
			t.Errorf("compareVersions(%s, %s) >= 0", prevVersion, v)
		}
		prev, prevVersion = dv, v
	}
	if newestKnown != versions[len(versions)-1] {
		t.Errorf("newestKnown is %s, want %s", newestKnown, versions[len(versions)-1])
	}
	for _, v := range []string{"26.3.1", "26.4", "27.1"} {
		if _, ok := releaseData(v); ok {
			t.Errorf("releaseData knows %s, which is newer than newestKnown", v)
		}
	}
	last, _ := releaseData("1.21.11")
	first, _ := releaseData("26.1")
	if !(last < modernLayoutData && modernLayoutData <= first) {
		t.Errorf("modernLayoutData %d should be after 1.21.11 (%d) and not after 26.1 (%d)", modernLayoutData, last, first)
	}
}

func TestModernTarget(t *testing.T) {
	for v, want := range map[string]bool{"1.12.2": false, "1.21.4": false, "1.21.11": false, "26.1": true, "26.2": true, "26.4": true, "27.1": true} {
		if got := modernTarget(v); got != want {
			t.Errorf("modernTarget(%s) = %v, want %v", v, got, want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.21.10", "1.21.9", 1},
		{"1.9", "1.10", -1},
		{"1.21", "1.21.0", 0},
		{"26.1", "1.21.11", 1},
		{"1.21.4", "1.21.4", 0},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
