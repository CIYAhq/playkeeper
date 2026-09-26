package diagnose

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

const (
	replyUnknown = "Unknown or incomplete command, see below for error"
	replyDenied  = "§cI'm sorry, but you do not have permission to perform this command. Please contact the server administrators if you believe that this is in error.\n"
)

func TestParsePaperTPSReadsColouredRepliesInAnyLocale(t *testing.T) {
	for _, tc := range []struct {
		file string
		want [3]float64
	}{
		{"ticks/paper_tps.txt", [3]float64{20, 20, 19.9}},
		{"ticks/paper_tps_lagging_comma.txt", [3]float64{12.4, 17.1, 19.2}},
		{"ticks/spigot_tps_star.txt", [3]float64{20, 20, 19.98}},
	} {
		got, ok := ParsePaperTPS(fixture(t, tc.file))
		if !ok || got != tc.want {
			t.Errorf("%s: got %v, %v; want %v", tc.file, got, ok, tc.want)
		}
	}
	for _, reply := range []string{"", replyUnknown, replyDenied, "§6TPS from last 1m, 5m, 15m: §a20.0§6, §a20.0\n"} {
		if got, ok := ParsePaperTPS(reply); ok {
			t.Errorf("%q parsed as %v", reply, got)
		}
	}
}

func TestParsePaperMSPTReadsAllThreeWindows(t *testing.T) {
	got, ok := ParsePaperMSPT(fixture(t, "ticks/paper_mspt.txt"))
	want := [3]TickTimes{{4.2, 2.1, 9.8}, {4.5, 2.0, 14.3}, {4.9, 1.9, 41.7}}
	if !ok || got != want {
		t.Errorf("got %v, %v; want %v", got, ok, want)
	}
	got, ok = ParsePaperMSPT(fixture(t, "ticks/paper_mspt_lagging_comma.txt"))
	want = [3]TickTimes{{81.3, 38, 240.6}, {79.9, 36.2, 240.6}, {77.5, 31.4, 512}}
	if !ok || got != want {
		t.Errorf("comma locale: got %v, %v; want %v", got, ok, want)
	}
	for _, reply := range []string{"", replyUnknown, "§6◴ §a4.2§7/§a2.1§7/§a9.8§e, §a4.5§7/§a2.0§7/§a14.3§e, §a4.9§7/§a1.9§7/§e41.7",
		"§6Server tick times §e(§7avg§e/§7min§e/§7max§e)§6 from last 5s§7,§6 10s§7,§6 1m§e:\n§6◴ §a4.2§7/§a2.1§7/§a9.8§e, §a4.5§7/§a2.0§7/§a14.3\n"} {
		if got, ok := ParsePaperMSPT(reply); ok {
			t.Errorf("%q parsed as %v", reply, got)
		}
	}
}

func TestParseTickQueryReadsEveryStateAndBothPercentileFormats(t *testing.T) {
	for _, tc := range []struct {
		file string
		want TickQuery
	}{
		{"ticks/tick_query_running.txt", TickQuery{"running", 20, 6.3, 50, 5.8, 9.1, 14.2, 100}},
		{"ticks/tick_query_lagging_1_20_4.txt", TickQuery{"lagging", 20, 74.5, 50, 70.1, 98.3, 131, 100}},
		{"ticks/tick_query_sprinting.txt", TickQuery{"sprinting", 20, 3.1, 0, 2.9, 4.4, 6, 100}},
		{"ticks/tick_query_frozen_lines.txt", TickQuery{"frozen", 20, 0.4, 50, 0.3, 0.6, 0.9, 100}},
	} {
		got, ok := ParseTickQuery(fixture(t, tc.file))
		if !ok || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.file, got, ok, tc.want)
		}
	}
	for _, reply := range []string{"", replyUnknown, "Target tick rate: 0.0 per second.\nAverage time per tick: 1.0ms (Target: 50.0ms)"} {
		if got, ok := ParseTickQuery(reply); ok {
			t.Errorf("%q parsed as %+v", reply, got)
		}
	}
}

func TestReadTicksCombinesTheRepliesOfTickCommands(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replies map[string]string
		want    TickStats
		ok      bool
	}{
		{"paper", map[string]string{"tps": fixture(t, "ticks/paper_tps.txt"), "mspt": fixture(t, "ticks/paper_mspt.txt")},
			TickStats{TPS: 20, MSPT: 4.9, MaxMSPT: 41.7, TargetTPS: 20}, true},
		{"paper without tps", map[string]string{"tps": replyDenied, "mspt": fixture(t, "ticks/paper_mspt_lagging_comma.txt")},
			TickStats{TPS: 1000 / 77.5, MSPT: 77.5, MaxMSPT: 512, TargetTPS: 20}, true},
		{"vanilla lagging", map[string]string{"tick query": fixture(t, "ticks/tick_query_lagging_1_20_4.txt")},
			TickStats{TPS: 1000 / 74.5, MSPT: 74.5, MaxMSPT: 131, TargetTPS: 20}, true},
		{"vanilla frozen", map[string]string{"tick query": fixture(t, "ticks/tick_query_frozen_lines.txt")},
			TickStats{TPS: 20, MSPT: 0.4, MaxMSPT: 0.9, TargetTPS: 20, Frozen: true}, true},
		{"nothing readable", map[string]string{"tick query": replyUnknown}, TickStats{}, false},
		{"no replies", nil, TickStats{}, false},
	} {
		got, ok := ReadTicks(tc.replies)
		if ok != tc.ok || !near(got.TPS, tc.want.TPS) || got.MSPT != tc.want.MSPT || got.MaxMSPT != tc.want.MaxMSPT ||
			got.TargetTPS != tc.want.TargetTPS || got.Frozen != tc.want.Frozen || got.Sprinting != tc.want.Sprinting {
			t.Errorf("%s: got %+v, %v; want %+v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestTickCommandsDependOnTypeAndVersion(t *testing.T) {
	for _, tc := range []struct {
		typ, mc string
		want    []string
	}{
		{"paper", "1.21.11", []string{"tps", "mspt"}},
		{"purpur", "1.16.5", []string{"tps", "mspt"}},
		{"vanilla", "26.1.2", []string{"tick query"}},
		{"neoforge", "1.20.4", []string{"tick query"}},
		{"fabric", "1.20.2", nil},
		{"vanilla", "", nil},
	} {
		if got := TickCommands(tc.typ, tc.mc); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s %s: got %v, want %v", tc.typ, tc.mc, got, tc.want)
		}
	}
}

func TestParseOverloadReadsWarningsButNotChat(t *testing.T) {
	at := time.Date(2026, 9, 25, 14, 3, 11, 0, time.UTC)
	for _, tc := range []struct {
		line string
		ok   bool
	}{
		{"[14:03:11] [Server thread/WARN]: Can't keep up! Is the server overloaded? Running 2034ms or 40 ticks behind", true},
		{"[14:03:11 WARN]: Can't keep up! Is the server overloaded? Running 2034ms or 40 ticks behind", true},
		{"[14:03:11] [Server thread/INFO]: <Steve> Can't keep up! Is the server overloaded? Running 2034ms or 40 ticks behind", false},
		{"[14:03:11] [Server thread/INFO]: [Steve] Can't keep up! Is the server overloaded? Running 2034ms or 40 ticks behind", false},
		{"[14:03:11 INFO]: Can't keep up! Is the server overloaded? Running 2034ms or 40 ticks behind", false},
		{"Can't keep up! Is the server overloaded? Running 2034ms or 40 ticks behind", false},
	} {
		o, ok := ParseOverload(ConsoleLine{At: at, Text: tc.line})
		if ok != tc.ok {
			t.Errorf("%q: ok = %v", tc.line, ok)
		}
		if ok && (o.Behind != 2034*time.Millisecond || o.Ticks != 40 || !o.At.Equal(at)) {
			t.Errorf("%q: got %+v", tc.line, o)
		}
	}
}
