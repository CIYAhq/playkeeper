package webmap

import (
	"cmp"
	"context"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Dimension is the kind of world a map shows.
type Dimension string

const (
	Overworld Dimension = "overworld"
	Nether    Dimension = "nether"
	End       Dimension = "end"
	Custom    Dimension = "custom"
)

// Point is a block position seen from above.
type Point struct {
	X int `json:"x"`
	Z int `json:"z"`
}

// Zoom are a world's zoom levels. Tiles exist for 0 to Max; at Max one
// pixel is one block. Extra more levels enlarge Max's tiles.
type Zoom struct {
	Max     int `json:"max"`
	Default int `json:"default"`
	Extra   int `json:"extra"`
}

// World is one world squaremap draws.
type World struct {
	// Name is squaremap's name for the world, used in tile paths.
	Name      string    `json:"name"`
	Dimension Dimension `json:"dimension"`
	Label     string    `json:"label"`
	Spawn     Point     `json:"spawn"`
	Zoom      Zoom      `json:"zoom"`
	// RefreshSeconds is how often squaremap redraws changed land, and so
	// how often a viewer reloads the tiles on screen.
	RefreshSeconds int `json:"refreshSeconds"`
}

// Worlds is the answer to /worlds.
type Worlds struct {
	Worlds   []World `json:"worlds"`
	TileSize int     `json:"tileSize"`
}

// Player is one player on the map. UUID is dashed and lowercase, for the
// panel's face cache.
type Player struct {
	Name      string    `json:"name"`
	UUID      string    `json:"uuid"`
	World     string    `json:"world"`
	Dimension Dimension `json:"dimension"`
	X         int       `json:"x"`
	Z         int       `json:"z"`
	Place     *Place    `json:"place,omitempty"`
}

// Players is the answer to /players: who is on the map as of UpdatedAt.
// squaremap leaves out spectators, invisible players and players it was
// told to hide.
type Players struct {
	Players   []Player  `json:"players"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// PlaceKind names where a player is in words, for the side column.
type PlaceKind string

const (
	PlaceNearSpawn  PlaceKind = "near_spawn"
	PlaceExploring  PlaceKind = "exploring"
	PlaceNether     PlaceKind = "nether"
	PlaceEnd        PlaceKind = "end"
	PlaceOtherWorld PlaceKind = "other_world"
)

// Place is where a player is in words: Text in English, Kind and Params
// (direction, world) for translation.
type Place struct {
	Kind   PlaceKind         `json:"kind"`
	Params map[string]string `json:"params,omitempty"`
	Text   string            `json:"text"`
}

// NearSpawn is how far from spawn, in blocks, a player counts as near it.
const NearSpawn = 128

// maxCoordinate is just outside the largest world border.
const maxCoordinate = 30_000_000

// PlaceOf describes a position in a world. In the overworld it needs the
// world's spawn: near it, or exploring in a compass direction from it.
func PlaceOf(w World, x, z int) Place {
	switch w.Dimension {
	case Nether:
		return Place{Kind: PlaceNether, Text: "In the Nether"}
	case End:
		return Place{Kind: PlaceEnd, Text: "In the End"}
	case Overworld:
		dx, dz := int64(x)-int64(w.Spawn.X), int64(z)-int64(w.Spawn.Z)
		if dx*dx+dz*dz <= NearSpawn*NearSpawn {
			return Place{Kind: PlaceNearSpawn, Text: "Near spawn"}
		}
		d := direction(dx, dz)
		return Place{Kind: PlaceExploring, Params: kv("direction", d), Text: "Exploring " + d}
	}
	label := cmp.Or(w.Label, w.Name)
	return Place{Kind: PlaceOtherWorld, Params: kv("world", label), Text: "In " + label}
}

// direction is the compass direction of (dx, dz); in Minecraft north is -z
// and east is +x.
func direction(dx, dz int64) string {
	a := math.Atan2(float64(dx), float64(-dz)) * 180 / math.Pi
	if a < 0 {
		a += 360
	}
	names := [...]string{"north", "northeast", "east", "southeast", "south", "southwest", "west", "northwest"}
	return names[int(math.Round(a/45))%len(names)]
}

// squaremap's tiles/settings.json, as UpdateWorldData writes it. Its "ui"
// part holds HTML and is never read.
type sqWorldList struct {
	Worlds []sqWorldEntry `json:"worlds"`
}

type sqWorldEntry struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	Order       int    `json:"order"`
}

// squaremap's tiles/<world>/settings.json. Its player tracker part names a
// third-party head service and is never read.
type sqWorldSettings struct {
	Spawn struct {
		X int `json:"x"`
		Z int `json:"z"`
	} `json:"spawn"`
	Zoom struct {
		Def   int `json:"def"`
		Max   int `json:"max"`
		Extra int `json:"extra"`
	} `json:"zoom"`
	TilesUpdateInterval int `json:"tiles_update_interval"`
}

// squaremap's tiles/players.json, as UpdatePlayers publishes it. Its
// display names are HTML, and health and armour are left out.
type sqPlayers struct {
	Players []struct {
		Name  string   `json:"name"`
		UUID  string   `json:"uuid"`
		World string   `json:"world"`
		X     *float64 `json:"x"`
		Z     *float64 `json:"z"`
	} `json:"players"`
}

var (
	reDimensionID = regexp.MustCompile(`^[a-z0-9_.-]{1,64}:[a-z0-9_./-]{1,128}$`)
	// Java names, and Bedrock names behind Geyser, which Floodgate prefixes
	// with a dot.
	rePlayerName = regexp.MustCompile(`^\.?[A-Za-z0-9_]{1,16}$`)
	reUUID       = regexp.MustCompile(`^[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12}$`)
)

func dimensionOf(squaremapType string) Dimension {
	switch squaremapType {
	case "normal":
		return Overworld
	case "nether":
		return Nether
	case "the_end":
		return End
	}
	return Custom
}

func labelOf(e sqWorldEntry, d Dimension) string {
	switch d {
	case Overworld:
		return "Overworld"
	case Nether:
		return "Nether"
	case End:
		return "The End"
	}
	if reDimensionID.MatchString(e.DisplayName) {
		return e.DisplayName
	}
	return e.Name
}

func worldFrom(e sqWorldEntry, s sqWorldSettings) (World, bool) {
	z := s.Zoom
	if z.Max < 0 || z.Max > ZoomMax || z.Extra < 0 || z.Extra > 4 || !inWorld(s.Spawn.X) || !inWorld(s.Spawn.Z) {
		return World{}, false
	}
	refresh := s.TilesUpdateInterval
	if refresh < 1 || refresh > 3600 {
		refresh = 30
	}
	d := dimensionOf(e.Type)
	return World{
		Name: e.Name, Dimension: d, Label: labelOf(e, d),
		Spawn:          Point{s.Spawn.X, s.Spawn.Z},
		Zoom:           Zoom{Max: z.Max, Default: min(max(z.Def, 0), z.Max+z.Extra), Extra: z.Extra},
		RefreshSeconds: refresh,
	}, true
}

func inWorld(v int) bool { return v >= -maxCoordinate && v <= maxCoordinate }

func dimensionRank(d Dimension) int {
	return slices.Index([]Dimension{Overworld, Nether, End, Custom}, d)
}

// worldList reads squaremap's list of worlds, keeping the ones with valid
// names. squaremap writes it within seconds of its first start.
func (m Map) worldList(ctx context.Context) ([]sqWorldEntry, error) {
	var list sqWorldList
	found, err := m.getJSON(ctx, "/tiles/settings.json", &list)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fail(KindStarting, nil, "The map is starting.", "It appears in a few seconds.")
	}
	var out []sqWorldEntry
	seen := map[string]bool{}
	for _, e := range list.Worlds {
		if len(out) == maxWorlds {
			break
		}
		if validWorld(e.Name) && !seen[e.Name] {
			seen[e.Name] = true
			out = append(out, e)
		}
	}
	return out, nil
}

func (m Map) world(ctx context.Context, e sqWorldEntry) (World, bool, error) {
	var s sqWorldSettings
	found, err := m.getJSON(ctx, "/tiles/"+e.Name+"/settings.json", &s)
	if err != nil || !found {
		return World{}, false, err
	}
	w, ok := worldFrom(e, s)
	return w, ok, nil
}

// worlds lists the worlds squaremap draws, overworld first. A world whose
// settings squaremap has not written yet is left out.
func (m Map) worlds(ctx context.Context) ([]World, error) {
	list, err := m.worldList(ctx)
	if err != nil {
		return nil, err
	}
	out := []World{}
	for _, e := range list {
		w, ok, err := m.world(ctx, e)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, w)
		}
	}
	order := map[string]int{}
	for _, e := range list {
		order[e.Name] = e.Order
	}
	slices.SortStableFunc(out, func(a, b World) int {
		return cmp.Or(cmp.Compare(order[a.Name], order[b.Name]), cmp.Compare(dimensionRank(a.Dimension), dimensionRank(b.Dimension)), strings.Compare(a.Name, b.Name))
	})
	return out, nil
}

// players converts squaremap's player positions. Entries with a bad name,
// UUID, world or position are left out rather than passed on.
func (m Map) players(ctx context.Context) (Players, error) {
	out := Players{Players: []Player{}, UpdatedAt: m.now()}
	var raw sqPlayers
	found, err := m.getJSON(ctx, "/tiles/players.json", &raw)
	if err != nil {
		return Players{}, err
	}
	// squaremap publishes players a second after it starts.
	if !found || len(raw.Players) == 0 {
		return out, nil
	}
	list, err := m.worldList(ctx)
	if err != nil {
		return Players{}, err
	}
	entries := map[string]sqWorldEntry{}
	for _, e := range list {
		entries[e.Name] = e
	}
	worlds := map[string]*World{}
	seen := map[string]bool{}
	for _, p := range raw.Players[:min(len(raw.Players), maxPlayers)] {
		uuid, ok := dashedUUID(p.UUID)
		if !ok || seen[uuid] || !rePlayerName.MatchString(p.Name) || !validWorld(p.World) {
			continue
		}
		x, okX := coordinate(p.X)
		z, okZ := coordinate(p.Z)
		if !okX || !okZ {
			continue
		}
		seen[uuid] = true
		w, known := worlds[p.World]
		if !known {
			w = m.playerWorld(ctx, p.World, entries)
			worlds[p.World] = w
		}
		pl := Player{Name: p.Name, UUID: uuid, World: p.World, Dimension: Custom, X: x, Z: z}
		if w != nil {
			pl.Dimension = w.Dimension
			place := PlaceOf(*w, x, z)
			pl.Place = &place
		}
		out.Players = append(out.Players, pl)
	}
	slices.SortFunc(out.Players, func(a, b Player) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.UUID, b.UUID))
	})
	return out, nil
}

// playerWorld is the world a player is in, as far as squaremap tells. Only
// the overworld needs its settings (for the spawn); without them, or for a
// world squaremap does not list, the player gets no place.
func (m Map) playerWorld(ctx context.Context, name string, entries map[string]sqWorldEntry) *World {
	e, ok := entries[name]
	if !ok {
		return nil
	}
	d := dimensionOf(e.Type)
	if d != Overworld {
		return &World{Name: name, Dimension: d, Label: labelOf(e, d)}
	}
	w, ok, err := m.world(ctx, e)
	if err != nil || !ok {
		return nil
	}
	return &w
}

func dashedUUID(s string) (string, bool) {
	s = strings.ToLower(s)
	if !reUUID.MatchString(s) {
		return "", false
	}
	h := strings.ReplaceAll(s, "-", "")
	if len(h) != 32 {
		return "", false
	}
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], true
}

func coordinate(v *float64) (int, bool) {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || math.Abs(*v) > maxCoordinate {
		return 0, false
	}
	return int(math.Floor(*v)), true
}
