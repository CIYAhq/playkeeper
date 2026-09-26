package webmap

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestWorldsFromSquaremap(t *testing.T) {
	f, m := startFakeSquaremap(t)
	w := serveMap(m, "GET", "/map/worlds", nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
	}
	var got Worlds
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	zoom := Zoom{Max: 3, Default: 3, Extra: 2}
	want := Worlds{TileSize: 512, Worlds: []World{
		{Name: "minecraft_overworld", Dimension: Overworld, Label: "Overworld", Spawn: Point{16, -48}, Zoom: zoom, RefreshSeconds: 15},
		{Name: "minecraft_the_nether", Dimension: Nether, Label: "Nether", Spawn: Point{64, 48}, Zoom: zoom, RefreshSeconds: 15},
		{Name: "minecraft_the_end", Dimension: End, Label: "The End", Spawn: Point{64, 48}, Zoom: zoom, RefreshSeconds: 15},
		{Name: "terralith_overworld", Dimension: Custom, Label: "terralith:overworld", Spawn: Point{0, 0}, Zoom: zoom, RefreshSeconds: 15},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	for _, s := range []string{"heads_url", "mc-heads", "<br/>", "\\u003c", "title", "player_tracker"} {
		if strings.Contains(w.Body.String(), s) {
			t.Errorf("the answer carries squaremap's %q: %s", s, w.Body)
		}
	}
	if paths := f.paths(); len(paths) != 5 || paths[0] != "/tiles/settings.json" {
		t.Errorf("squaremap was asked for %v", paths)
	}
}

func TestWorldsLeavesOutWorldsItCannotShow(t *testing.T) {
	f, m := startFakeSquaremap(t)
	f.setFile("/tiles/settings.json", `{"worlds":[
		{"name":"minecraft_overworld","display_name":"minecraft:overworld","type":"normal","order":0},
		{"name":"minecraft_overworld","display_name":"again","type":"normal","order":0},
		{"name":"../../etc","type":"custom","order":0},
		{"name":"Minecraft_The_Nether","type":"nether","order":0},
		{"name":"","type":"custom","order":0},
		{"name":"minecraft_the_end","display_name":"minecraft:the_end","type":"the_end","order":-1},
		{"name":"mystery","display_name":"<b onclick=x>Mystery</b>","type":"custom","order":0},
		{"name":"not_written_yet","type":"custom","order":0},
		{"name":"too_detailed","type":"custom","order":0},
		{"name":"beyond_the_border","type":"custom","order":0}
	]}`)
	f.setFile("/tiles/mystery/settings.json", `{"spawn":{"x":1,"z":2},"zoom":{"def":9,"max":3,"extra":2},"tiles_update_interval":0}`)
	f.setFile("/tiles/too_detailed/settings.json", `{"spawn":{"x":0,"z":0},"zoom":{"def":3,"max":7,"extra":2},"tiles_update_interval":15}`)
	f.setFile("/tiles/beyond_the_border/settings.json", `{"spawn":{"x":40000000,"z":0},"zoom":{"def":3,"max":3,"extra":2},"tiles_update_interval":15}`)
	w := serveMap(m, "GET", "/map/worlds", nil)
	var got Worlds
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body, err)
	}
	var names []string
	for _, w := range got.Worlds {
		names = append(names, w.Name)
	}
	if !slices.Equal(names, []string{"minecraft_the_end", "minecraft_overworld", "mystery"}) {
		t.Fatalf("worlds %v", names)
	}
	if mystery := got.Worlds[2]; mystery.Label != "mystery" || mystery.Zoom.Default != 5 || mystery.RefreshSeconds != 30 {
		t.Errorf("mystery is %+v", mystery)
	}
	asked := f.paths()
	slices.Sort(asked)
	want := []string{
		"/tiles/beyond_the_border/settings.json", "/tiles/minecraft_overworld/settings.json", "/tiles/minecraft_the_end/settings.json",
		"/tiles/mystery/settings.json", "/tiles/not_written_yet/settings.json", "/tiles/settings.json", "/tiles/too_detailed/settings.json",
	}
	if !slices.Equal(asked, want) {
		t.Errorf("squaremap was asked for %v", asked)
	}
}

func TestPlayersInPlaykeepersShape(t *testing.T) {
	f, m := startFakeSquaremap(t)
	w := serveMap(m, "GET", "/map/players", nil)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
	}
	var got Players
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.Equal(testNow) {
		t.Errorf("updated at %v", got.UpdatedAt)
	}
	place := func(k PlaceKind, text string, params ...string) *Place {
		p := Place{Kind: k, Text: text}
		if len(params) > 0 {
			p.Params = kv(params...)
		}
		return &p
	}
	want := []Player{
		{".BedrockSteve", "00000000-0000-0000-0009-011f2a3b4c5d", "terralith_overworld", Custom, 7, -2, place(PlaceOtherWorld, "In terralith:overworld", "world", "terralith:overworld")},
		{"Dinnerbone", "61699b2e-d327-4a01-9f1e-0ea8c3f06bc6", "minecraft_the_nether", Nether, 120, -40, place(PlaceNether, "In the Nether")},
		{"Ender_Kid", "5f3c2d1e-0a9b-4c8d-9e7f-6a5b4c3d2e1f", "minecraft_the_end", End, -1210, 880, place(PlaceEnd, "In the End")},
		{"jeb_", "853c80ef-3c37-49fd-aa49-938b674adae6", "minecraft_overworld", Overworld, 40, 12, place(PlaceNearSpawn, "Near spawn")},
		{"Notch", "069a79f4-44e9-4726-a5be-fca90e38aaf5", "minecraft_overworld", Overworld, 3200, -3300, place(PlaceExploring, "Exploring northeast", "direction", "northeast")},
		{"pewdiepie_epic", "7f184d63-9f9c-47a7-be03-8382145fb2c2", "minecraft_overworld", Overworld, -650, -200, place(PlaceExploring, "Exploring west", "direction", "west")},
	}
	if !reflect.DeepEqual(got.Players, want) {
		t.Errorf("got %s\nwant %s", show(got.Players), show(want))
	}
	for _, s := range []string{"armor", "health", "yaw", "display_name", "span", `"y":`, `"max":`} {
		if strings.Contains(w.Body.String(), s) {
			t.Errorf("the answer carries squaremap's %q: %s", s, w.Body)
		}
	}
	if paths := f.paths(); !slices.Equal(paths, []string{"/tiles/players.json", "/tiles/settings.json", "/tiles/minecraft_overworld/settings.json"}) {
		t.Errorf("squaremap was asked for %v", paths)
	}
}

func TestPlayersLeavesOutWhatItCannotTrust(t *testing.T) {
	f, m := startFakeSquaremap(t)
	players := `{"max":20,"players":[
		{"name":"Notch","uuid":"069a79f444e94726a5befca90e38aaf5","world":"minecraft_overworld","x":-12.7,"z":0.2},
		{"name":"NotchAgain","uuid":"069A79F4-44E9-4726-A5BE-FCA90E38AAF5","world":"minecraft_overworld","x":1,"z":1},
		{"name":"<b>x</b>","uuid":"11111111111141118111111111111111","world":"minecraft_overworld","x":1,"z":1},
		{"name":"ThisNameIsTooLong","uuid":"22222222222242228222222222222222","world":"minecraft_overworld","x":1,"z":1},
		{"name":"","uuid":"33333333333343338333333333333333","world":"minecraft_overworld","x":1,"z":1},
		{"name":"Untracked","uuid":"44444444444444448444444444444444","world":"minecraft_overworld"},
		{"name":"Faraway","uuid":"55555555555545558555555555555555","world":"minecraft_overworld","x":30000001,"z":0},
		{"name":"Sneaky","uuid":"66666666666646668666666666666666","world":"../../etc","x":1,"z":1},
		{"name":"Loud","uuid":"not-a-uuid","world":"minecraft_overworld","x":1,"z":1},
		{"name":"Stringy","uuid":"77777777777747778777777777777777","world":"minecraft_overworld","x":"1","z":1},
		{"name":"Elsewhere","uuid":"88888888888848888888888888888888","world":"plugin_world","x":5,"z":5}
	]}`
	f.setPlayers([]byte(players))
	w := serveMap(m, "GET", "/map/players", nil)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("a player with a text position should make the answer unreadable: %d %s", w.Code, w.Body)
	}
	f.setPlayers([]byte(strings.Replace(players, `"x":"1"`, `"x":1.5e300`, 1)))
	w = serveMap(m, "GET", "/map/players", nil)
	var got Players
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body, err)
	}
	want := []Player{
		{Name: "Elsewhere", UUID: "88888888-8888-4888-8888-888888888888", World: "plugin_world", Dimension: Custom, X: 5, Z: 5},
		{Name: "Notch", UUID: "069a79f4-44e9-4726-a5be-fca90e38aaf5", World: "minecraft_overworld", Dimension: Overworld, X: -13, Z: 0,
			Place: &Place{Kind: PlaceNearSpawn, Text: "Near spawn"}},
	}
	if !reflect.DeepEqual(got.Players, want) {
		t.Errorf("got %s\nwant %s", show(got.Players), show(want))
	}
}

func TestPlayersStopsAtAFewHundred(t *testing.T) {
	f, m := startFakeSquaremap(t)
	var list []string
	for i := range maxPlayers + 20 {
		list = append(list, fmt.Sprintf(`{"name":"p%03d","uuid":"%032x","world":"minecraft_the_nether","x":%d,"z":0}`, i, i+1, i))
	}
	f.setPlayers([]byte(`{"max":1000,"players":[` + strings.Join(list, ",") + `]}`))
	var got Players
	w := serveMap(m, "GET", "/map/players", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Players) != maxPlayers {
		t.Errorf("%d players, %v", len(got.Players), err)
	}
}

func TestPlayersWhenNobodyIsOnline(t *testing.T) {
	for _, body := range [][]byte{nil, []byte(`{"max":20,"players":[]}`)} {
		f, m := startFakeSquaremap(t)
		f.setPlayers(body)
		w := serveMap(m, "GET", "/map/players", nil)
		if want := `{"players":[],"updatedAt":"2026-09-25T16:00:00Z"}`; w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("%q: %d %s", body, w.Code, w.Body)
		}
		if paths := f.paths(); len(paths) != 1 {
			t.Errorf("%q: squaremap was asked for %v", body, paths)
		}
	}
}

func TestPlaceOf(t *testing.T) {
	overworld := World{Name: "minecraft_overworld", Dimension: Overworld, Label: "Overworld", Spawn: Point{16, -48}}
	for _, tc := range []struct {
		w      World
		x, z   int
		kind   PlaceKind
		text   string
		params map[string]string
	}{
		{overworld, 16, -48, PlaceNearSpawn, "Near spawn", nil},
		{overworld, 16 + NearSpawn, -48, PlaceNearSpawn, "Near spawn", nil},
		{overworld, 16 + 90, -48 + 90, PlaceNearSpawn, "Near spawn", nil},
		{overworld, 16 + NearSpawn + 1, -48, PlaceExploring, "Exploring east", kv("direction", "east")},
		{overworld, 16, -48 - 1000, PlaceExploring, "Exploring north", kv("direction", "north")},
		{overworld, 16 + 300, -48 - 1000, PlaceExploring, "Exploring north", kv("direction", "north")},
		{overworld, 16 + 1000, -48 - 1000, PlaceExploring, "Exploring northeast", kv("direction", "northeast")},
		{overworld, 16 + 1000, -48 + 1000, PlaceExploring, "Exploring southeast", kv("direction", "southeast")},
		{overworld, 16, -48 + 1000, PlaceExploring, "Exploring south", kv("direction", "south")},
		{overworld, 16 - 1000, -48 + 1000, PlaceExploring, "Exploring southwest", kv("direction", "southwest")},
		{overworld, 16 - 1000, -48, PlaceExploring, "Exploring west", kv("direction", "west")},
		{overworld, 16 - 1000, -48 - 1000, PlaceExploring, "Exploring northwest", kv("direction", "northwest")},
		{overworld, maxCoordinate, maxCoordinate, PlaceExploring, "Exploring southeast", kv("direction", "southeast")},
		{overworld, -maxCoordinate, -maxCoordinate, PlaceExploring, "Exploring northwest", kv("direction", "northwest")},
		{World{Dimension: Nether}, 0, 0, PlaceNether, "In the Nether", nil},
		{World{Dimension: End}, 0, 0, PlaceEnd, "In the End", nil},
		{World{Name: "terralith_overworld", Dimension: Custom, Label: "terralith:overworld"}, 0, 0, PlaceOtherWorld, "In terralith:overworld", kv("world", "terralith:overworld")},
		{World{Name: "minecraft_creative", Dimension: Custom}, 0, 0, PlaceOtherWorld, "In minecraft_creative", kv("world", "minecraft_creative")},
		{World{Name: "odd", Dimension: "sideways"}, 0, 0, PlaceOtherWorld, "In odd", kv("world", "odd")},
	} {
		got := PlaceOf(tc.w, tc.x, tc.z)
		if want := (Place{Kind: tc.kind, Params: tc.params, Text: tc.text}); !reflect.DeepEqual(got, want) {
			t.Errorf("%s at %d, %d: got %+v, want %+v", tc.w.Name, tc.x, tc.z, got, want)
		}
	}
}

func TestDashedUUID(t *testing.T) {
	for in, want := range map[string]string{
		"069a79f444e94726a5befca90e38aaf5":      "069a79f4-44e9-4726-a5be-fca90e38aaf5",
		"069a79f4-44e9-4726-a5be-fca90e38aaf5":  "069a79f4-44e9-4726-a5be-fca90e38aaf5",
		"069A79F444E94726A5BEFCA90E38AAF5":      "069a79f4-44e9-4726-a5be-fca90e38aaf5",
		"":                                      "",
		"069a79f444e94726a5befca90e38aaf":       "",
		"069a79f444e94726a5befca90e38aaf5a":     "",
		"069a79f444e94726a5befca90e38aazz":      "",
		"069a79f4-44e9-4726-a5be-fca90e38aaf5-": "",
		"../069a79f444e94726a5befca90e38aaf5":   "",
	} {
		got, ok := dashedUUID(in)
		if got != want || ok != (want != "") {
			t.Errorf("%q: got %q, %v", in, got, ok)
		}
	}
}

func TestCoordinate(t *testing.T) {
	for _, tc := range []struct {
		v    *float64
		want int
		ok   bool
	}{
		{nil, 0, false},
		{ptr(math.NaN()), 0, false},
		{ptr(math.Inf(1)), 0, false},
		{ptr(math.Inf(-1)), 0, false},
		{ptr(maxCoordinate + 1), 0, false},
		{ptr(-maxCoordinate - 1), 0, false},
		{ptr(maxCoordinate), maxCoordinate, true},
		{ptr(-0.5), -1, true},
		{ptr(12.9), 12, true},
		{ptr(0), 0, true},
	} {
		got, ok := coordinate(tc.v)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%v: got %d, %v", tc.v, got, ok)
		}
	}
}

func ptr(v float64) *float64 { return &v }

func show(ps []Player) string {
	b, _ := json.MarshalIndent(ps, "", "  ")
	return string(b)
}
