package webmap

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

var running = Check{Installed: true, Running: true}

// writeTile puts a tile in squaremap's folder sq, written at at, and
// returns its size.
func writeTile(t *testing.T, sq, world string, zoom, x, z int, at time.Time) int64 {
	t.Helper()
	name := filepath.Join(sq, "web", "tiles", world, strconv.Itoa(zoom), strconv.Itoa(x)+"_"+strconv.Itoa(z)+".png")
	content := strings.Repeat("t", 100+x*x+z*z+zoom)
	mustWrite(t, name, content)
	if err := os.Chtimes(name, at, at); err != nil {
		t.Fatal(err)
	}
	return int64(len(content))
}

func TestStatusStates(t *testing.T) {
	down := closedAddr(t)
	wait := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}
	webPage := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<!doctype html><title>squaremap</title>"))
	}
	for _, tc := range []struct {
		name, typ string
		check     Check
		tiles     int
		progress  bool
		setup     func(f *fakeSquaremap, m *Map)
		state     State
		params    map[string]string
		msg       string
		asks      bool
	}{
		{name: "vanilla", typ: "vanilla", check: running, state: StateUnsupported, params: kv("type", "vanilla"),
			msg: "Vanilla servers cannot show a live map."},
		{name: "unknown type", typ: "spigot", check: running, state: StateUnsupported, params: kv("type", "spigot"),
			msg: `Playkeeper does not know the server type "spigot", so it cannot set up a map for it.`},
		{name: "not installed", typ: "paper", check: Check{Running: true}, state: StateNotInstalled, params: kv("minutes", "10", "megabytes", "200"),
			msg: "The map is not set up yet."},
		{name: "server stopped", typ: "fabric", check: Check{Installed: true}, tiles: 3, state: StateStopped, params: kv("areas", "3"),
			msg: "The map shows while the server is running."},
		{name: "restart pending", typ: "paper", check: Check{Installed: true, Running: true, PendingRestart: true}, state: StateNeedsRestart,
			msg: "The map starts when the server restarts."},
		{name: "squaremap down", typ: "paper", check: running, tiles: 3, setup: func(f *fakeSquaremap, m *Map) { m.Addr = down },
			state: StateNotAnswering, params: kv("reason", "not_answering"), msg: "The map is not answering."},
		{name: "squaremap slow", typ: "neoforge", check: running, tiles: 3, setup: func(f *fakeSquaremap, m *Map) {
			m.Timeout = 50 * time.Millisecond
			f.setHandler(wait)
		}, state: StateNotAnswering, params: kv("reason", "too_slow"), asks: true},
		{name: "squaremap answers a web page", typ: "paper", check: running, setup: func(f *fakeSquaremap, m *Map) { f.setHandler(webPage) },
			state: StateNotAnswering, params: kv("reason", "bad_answer"), asks: true},
		{name: "first start", typ: "paper", check: running, setup: func(f *fakeSquaremap, m *Map) { f.deleteFile("/tiles/settings.json") },
			state: StateDrawing, msg: "Drawing the map for the first time.", asks: true},
		{name: "nothing drawn yet", typ: "quilt", check: running, state: StateDrawing, msg: "Drawing the map for the first time.", asks: true},
		{name: "full render", typ: "paper", check: running, tiles: 5, progress: true, state: StateDrawing,
			params: kv("done", "5", "total", "12", "percent", "41"), msg: "Drawing the map: 5 of 12 areas (41%).", asks: true},
		{name: "ready", typ: "purpur", check: running, tiles: 3, state: StateReady, params: kv("areas", "3"),
			msg: "The map is up to date: 3 areas drawn.", asks: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, m := startFakeSquaremap(t)
			m.Dir, m.Type = t.TempDir(), tc.typ
			if l, err := LayoutFor(tc.typ); err == nil {
				sq := filepath.Join(m.Dir, filepath.FromSlash(l.Dir))
				for i := range tc.tiles {
					writeTile(t, sq, "minecraft_overworld", ZoomMax, i, 0, testNow.Add(-time.Hour))
				}
				if tc.progress {
					mustWrite(t, filepath.Join(sq, "data", "minecraft_overworld", "resume_render.json"), string(readTestdata(t, "squaremap/resume_render.json")))
				}
			}
			if tc.setup != nil {
				tc.setup(f, &m)
			}
			s := m.Status(context.Background(), tc.check)
			if s.State != tc.state || !reflect.DeepEqual(s.Params, tc.params) || tc.msg != "" && s.Msg != tc.msg {
				t.Errorf("got %s %v %q, want %s %v %q", s.State, s.Params, s.Msg, tc.state, tc.params, tc.msg)
			}
			if s.Msg == "" || s.Hint == "" || s.Areas != tc.tiles || !s.CheckedAt.Equal(testNow) {
				t.Errorf("got %+v", s)
			}
			if asked := len(f.seen()) > 0; asked != tc.asks {
				t.Errorf("asked squaremap: %v, want %v", asked, tc.asks)
			}
		})
	}
}

func TestStatusSetupCardNamesTheCost(t *testing.T) {
	s := Map{Type: "fabric", Dir: t.TempDir()}.Status(context.Background(), Check{})
	if !strings.Contains(s.Hint, "about 10 minutes") || !strings.Contains(s.Hint, "about 200 MB") {
		t.Errorf("hint %q", s.Hint)
	}
}

func TestStatusCountsWhatIsDrawn(t *testing.T) {
	_, m := startFakeSquaremap(t)
	m.Dir = t.TempDir()
	sq := filepath.Join(m.Dir, "plugins", "squaremap")
	early, middle, latest := testNow.Add(-3*time.Hour), testNow.Add(-time.Hour), testNow.Add(-2*time.Minute)
	var size int64
	size += writeTile(t, sq, "minecraft_overworld", 3, 0, 0, early)
	size += writeTile(t, sq, "minecraft_overworld", 3, -1, 0, latest)
	size += writeTile(t, sq, "minecraft_overworld", 3, 0, -1, middle)
	size += writeTile(t, sq, "minecraft_overworld", 2, 0, 0, testNow)
	size += writeTile(t, sq, "minecraft_overworld", 0, 0, 0, testNow)
	size += writeTile(t, sq, "minecraft_the_nether", 3, 0, 0, early)
	size += writeTile(t, sq, "minecraft_the_nether", 3, 5, 5, early)

	tiles := filepath.Join(sq, "web", "tiles")
	zoom3 := filepath.Join(tiles, "minecraft_overworld", "3")
	mustWrite(t, filepath.Join(tiles, "settings.json"), "{}")
	mustWrite(t, filepath.Join(tiles, "minecraft_overworld", "settings.json"), "{}")
	mustWrite(t, filepath.Join(zoom3, "notes.txt"), "not a tile")
	mustWrite(t, filepath.Join(zoom3, ".0_1.png.tmp"), "half a tile")
	mustWrite(t, filepath.Join(zoom3, "7_7.png", "inside"), "a folder")
	mustSymlink(t, filepath.Join(zoom3, "0_0.png"), filepath.Join(zoom3, "9_9.png"))
	writeTile(t, sq, "minecraft_overworld", 4, 0, 0, testNow)
	writeTile(t, sq, "Bad World", 3, 0, 0, testNow)
	outside := t.TempDir()
	writeTile(t, outside, "minecraft_overworld", 3, 1, 1, testNow)
	mustSymlink(t, filepath.Join(outside, "web", "tiles", "minecraft_overworld"), filepath.Join(tiles, "linked_world"))

	s := m.Status(context.Background(), running)
	if s.State != StateReady || s.Areas != 5 || s.Bytes != size || s.LastDrawn == nil || !s.LastDrawn.Equal(latest) || s.Progress != nil {
		t.Fatalf("got %+v, want 5 areas, %d bytes, last drawn %v", s, size, latest)
	}
	if len(s.Worlds) != 2 {
		t.Fatalf("worlds %+v", s.Worlds)
	}
	for i, want := range []struct {
		name  string
		dim   Dimension
		areas int
		last  time.Time
	}{
		{"minecraft_overworld", Overworld, 3, latest},
		{"minecraft_the_nether", Nether, 2, early},
	} {
		got := s.Worlds[i]
		if got.Name != want.name || got.Dimension != want.dim || got.Areas != want.areas || got.LastDrawn == nil || !got.LastDrawn.Equal(want.last) || got.Progress != nil {
			t.Errorf("world %d is %+v, want %+v", i, got, want)
		}
	}
}

func TestStatusReadsTheProgressOfAFullRender(t *testing.T) {
	_, m := startFakeSquaremap(t)
	m.Dir = t.TempDir()
	sq := filepath.Join(m.Dir, "squaremap")
	m.Type = "fabric"
	for _, w := range []string{"minecraft_overworld", "minecraft_the_nether", "minecraft_the_end"} {
		writeTile(t, sq, w, 3, 0, 0, testNow)
	}
	data := filepath.Join(sq, "data")
	mustWrite(t, filepath.Join(data, "minecraft_overworld", "resume_render.json"), string(readTestdata(t, "squaremap/resume_render.json")))
	mustWrite(t, filepath.Join(data, "minecraft_the_nether", "resume_render.json"), `[[{"x":0,"z":0},true],[{"x":1,"z":0},false]]`)
	mustWrite(t, filepath.Join(data, "minecraft_the_end", "resume_render.json"), "{}")
	s := m.Status(context.Background(), running)
	if s.State != StateDrawing || !reflect.DeepEqual(s.Progress, &Progress{Done: 6, Total: 14, Percent: 42}) {
		t.Fatalf("got %s %+v", s.State, s.Progress)
	}
	if s.Msg != "Drawing the map: 6 of 14 areas (42%)." {
		t.Errorf("message %q", s.Msg)
	}
	for i, want := range []*Progress{{Done: 5, Total: 12, Percent: 41}, {Done: 1, Total: 2, Percent: 50}, nil} {
		if got := s.Worlds[i].Progress; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: progress %+v, want %+v", s.Worlds[i].Name, got, want)
		}
	}
}

func TestStatusIgnoresProgressItCannotTrust(t *testing.T) {
	fixture := string(readTestdata(t, "squaremap/resume_render.json"))
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, file string)
	}{
		{"a link", func(t *testing.T, file string) {
			outside := filepath.Join(t.TempDir(), "resume_render.json")
			mustWrite(t, outside, fixture)
			mustSymlink(t, outside, file)
		}},
		{"a folder", func(t *testing.T, file string) { mustWrite(t, filepath.Join(file, "inside"), fixture) }},
		{"too big", func(t *testing.T, file string) { mustWrite(t, file, fixture+strings.Repeat(" ", maxProgressBytes)) }},
		{"another format", func(t *testing.T, file string) { mustWrite(t, file, `{"regions":12,"done":5}`) }},
		{"garbage", func(t *testing.T, file string) { mustWrite(t, file, "\x00\x01\x02") }},
	} {
		_, m := startFakeSquaremap(t)
		m.Dir = t.TempDir()
		sq := filepath.Join(m.Dir, "plugins", "squaremap")
		writeTile(t, sq, "minecraft_overworld", 3, 0, 0, testNow)
		tc.setup(t, filepath.Join(sq, "data", "minecraft_overworld", "resume_render.json"))
		if s := m.Status(context.Background(), running); s.State != StateReady || s.Progress != nil || s.Worlds[0].Progress != nil {
			t.Errorf("%s: got %s %+v", tc.name, s.State, s.Progress)
		}
	}
}

func TestStatusDoesNotFollowLinkedFolders(t *testing.T) {
	outside := t.TempDir()
	writeTile(t, outside, "minecraft_overworld", 3, 0, 0, testNow)
	for _, link := range []struct{ target, name string }{
		{outside, "plugins/squaremap"},
		{filepath.Join(outside, "web"), "plugins/squaremap/web"},
		{filepath.Join(outside, "web", "tiles"), "plugins/squaremap/web/tiles"},
		{filepath.Join(outside, "web", "tiles", "minecraft_overworld", "3"), "plugins/squaremap/web/tiles/minecraft_overworld/3"},
	} {
		_, m := startFakeSquaremap(t)
		m.Dir = t.TempDir()
		mustSymlink(t, link.target, filepath.Join(m.Dir, filepath.FromSlash(link.name)))
		if s := m.Status(context.Background(), running); s.Areas != 0 || s.Bytes != 0 || s.State != StateDrawing {
			t.Errorf("%s linked: got %+v", link.name, s)
		}
	}
}

func TestParseProgress(t *testing.T) {
	for in, want := range map[string]*Progress{
		string(readTestdata(t, "squaremap/resume_render.json")):               {Done: 5, Total: 12, Percent: 41},
		`[[{"x":0,"z":0},true]]`:                                              {Done: 1, Total: 1, Percent: 100},
		`[[{"x":0,"z":0},false],[{"x":1,"z":0},false],[{"x":2,"z":0},false]]`: {Done: 0, Total: 3, Percent: 0},
		"{}":                                  nil,
		"[]":                                  nil,
		"":                                    nil,
		"null":                                nil,
		`[[{"x":0,"z":0},"yes"]]`:             nil,
		`[[{"x":0,"z":0}]]`:                   nil,
		`[[{"x":0,"z":0},true,false]]`:        nil,
		`{"RegionCoordinate[x=0, z=0]":true}`: nil,
	} {
		if got := parseProgress([]byte(in)); !reflect.DeepEqual(got, want) {
			t.Errorf("%.40q: got %+v, want %+v", in, got, want)
		}
	}
}

func TestThousands(t *testing.T) {
	for n, want := range map[int]string{0: "0", 7: "7", 999: "999", 1000: "1,000", 3610: "3,610", 1234567: "1,234,567", -1234: "-1,234"} {
		if got := thousands(n); got != want {
			t.Errorf("%d: got %q, want %q", n, got, want)
		}
	}
}
