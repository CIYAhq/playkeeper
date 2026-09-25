package worldimport

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/nbt"
)

// Level is what a world's level.dat says about it.
type Level struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	DataVersion int    `json:"dataVersion,omitempty"`
	// Series is "main" for releases and snapshots; experimental builds
	// such as the 1.18 previews had their own.
	Series   string `json:"series,omitempty"`
	Snapshot bool   `json:"snapshot,omitempty"`
	GameMode string `json:"gameMode,omitempty"`
	Hardcore bool   `json:"hardcore"`
	// Difficulty is empty when level.dat doesn't store one.
	Difficulty    string    `json:"difficulty,omitempty"`
	LastPlayed    time.Time `json:"lastPlayed,omitzero"`
	DataPacks     []string  `json:"dataPacks,omitempty"`
	DisabledPacks []string  `json:"disabledPacks,omitempty"`
	// Features are the experimental features the world was created with.
	Features []string `json:"features,omitempty"`
	// Brands are the server brands that ran the world, such as "vanilla",
	// "fabric" or "Paper".
	Brands []string `json:"brands,omitempty"`
	Modded bool     `json:"modded,omitempty"`
	Seed   string   `json:"seed,omitempty"`
	// Owner is the UUID of the player of a singleplayer world.
	Owner string `json:"owner,omitempty"`
	// Spawn is the world spawn's block column, when level.dat has one.
	Spawn *Spawn `json:"spawn,omitempty"`
	// FromBackup is set when level.dat was unreadable and this comes from
	// level.dat_old, the copy Minecraft keeps from the previous save.
	FromBackup bool `json:"fromBackup,omitempty"`

	// ownerInLevel is set when level.dat itself holds the singleplayer
	// player's inventory and position, which a dedicated server ignores.
	ownerInLevel bool
}

// Spawn is a block column: x runs east, z south.
type Spawn struct {
	X int `json:"x"`
	Z int `json:"z"`
}

const (
	maxTextRunes = 100
	maxListItems = 256
	// maxLastPlayed is the end of year 9999, the last time encoding/json
	// can write.
	maxLastPlayed = 253402300799999
)

// ParseLevel reads a gzip-compressed level.dat. maxBytes bounds the memory
// of the decoded file; see nbt.Limits.
func ParseLevel(gz []byte, maxBytes int64) (*Level, error) {
	lim := nbt.DefaultLimits()
	lim.MaxBytes = maxBytes
	_, root, err := nbt.ReadGzip(bytes.NewReader(gz), lim)
	if err != nil {
		return nil, err
	}
	data, ok := root.Compound("Data")
	if !ok {
		return nil, fmt.Errorf("%w: level.dat has no Data compound", nbt.ErrFormat)
	}
	lv := &Level{Name: text(data, "LevelName", maxTextRunes)}
	if v, ok := data.Compound("Version"); ok {
		lv.Version = text(v, "Name", 32)
		lv.Series = text(v, "Series", 32)
		lv.Snapshot, _ = v.Bool("Snapshot")
		if id, ok := v.Int("Id"); ok {
			lv.DataVersion = clampInt(id)
		}
	}
	if dv, ok := data.Int("DataVersion"); ok {
		lv.DataVersion = clampInt(dv)
	}
	if gt, ok := data.Int("GameType"); ok {
		lv.GameMode = gameModeName(gt)
	}
	if ds, ok := data.Compound("difficulty_settings"); ok {
		d, _ := ds.String("difficulty")
		lv.Difficulty = difficultyByName(d)
		lv.Hardcore, _ = ds.Bool("hardcore")
	} else {
		if d, ok := data.Int("Difficulty"); ok {
			lv.Difficulty = difficultyName(d)
		}
		lv.Hardcore, _ = data.Bool("hardcore")
	}
	if ms, ok := data.Int("LastPlayed"); ok && ms > 0 && ms <= maxLastPlayed {
		lv.LastPlayed = time.UnixMilli(ms).UTC()
	}
	if dp, ok := data.Compound("DataPacks"); ok {
		lv.DataPacks = cleanList(dp.Strings("Enabled"))
		lv.DisabledPacks = cleanList(dp.Strings("Disabled"))
	}
	for _, f := range cleanList(data.Strings("enabled_features")) {
		if f != "minecraft:vanilla" {
			lv.Features = append(lv.Features, f)
		}
	}
	lv.Brands = cleanList(data.Strings("ServerBrands"))
	lv.Modded, _ = data.Bool("WasModded")
	if wg, ok := data.Compound("WorldGenSettings"); ok {
		lv.Seed = seedOf(wg, "seed")
	} else {
		lv.Seed = seedOf(data, "RandomSeed")
	}
	if u, ok := data.IntArray("singleplayer_uuid"); ok {
		lv.Owner = uuidString(u)
	}
	if p, ok := data.Compound("Player"); ok {
		lv.ownerInLevel = true
		if u, ok := p.IntArray("UUID"); ok {
			lv.Owner = uuidString(u)
		}
	}
	lv.Spawn = spawnOf(data)
	return lv, nil
}

// spawnOf reads the world spawn: SpawnX and SpawnZ until Minecraft 1.21.9,
// then the spawn compound's pos (x, y, z).
func spawnOf(data nbt.Compound) *Spawn {
	if sp, ok := data.Compound("spawn"); ok {
		if pos, ok := sp.IntArray("pos"); ok && len(pos) == 3 {
			return &Spawn{X: int(pos[0]), Z: int(pos[2])}
		}
	}
	x, okX := data.Int("SpawnX")
	z, okZ := data.Int("SpawnZ")
	if okX && okZ {
		return &Spawn{X: clampInt(x), Z: clampInt(z)}
	}
	return nil
}

// readSeed reads the seed from world_gen_settings.dat, where it lives since
// Minecraft 26.1.
func readSeed(gz []byte, maxBytes int64) string {
	lim := nbt.DefaultLimits()
	lim.MaxBytes = maxBytes
	_, root, err := nbt.ReadGzip(bytes.NewReader(gz), lim)
	if err != nil {
		return ""
	}
	if data, ok := root.Compound("data"); ok {
		return seedOf(data, "seed")
	}
	return seedOf(root, "seed")
}

func seedOf(c nbt.Compound, key string) string {
	if v, ok := c[key].(int64); ok {
		return strconv.FormatInt(v, 10)
	}
	return ""
}

// levelProblem explains why a level.dat (or level.dat_old, given as name)
// couldn't be read.
func levelProblem(name string, err error) string {
	switch {
	case errors.Is(err, nbt.ErrTooDeep):
		return name + " is nested more deeply than Minecraft allows, so it is damaged or not a real level.dat."
	case errors.Is(err, nbt.ErrTooLarge):
		return name + " is larger than Playkeeper reads."
	}
	return name + " is damaged or incomplete."
}

func text(c nbt.Compound, key string, max int) string {
	s, _ := c.String(key)
	return cleanText(s, max)
}

// cleanText makes a string from a world safe to show: control and
// direction-changing characters are dropped and it is cut to max runes.
func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unsafeRune(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, string(utf8.RuneError)))
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}

func cleanList(items []string) []string {
	var out []string
	for _, it := range items {
		if len(out) == maxListItems {
			break
		}
		if s := cleanText(it, 200); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func clampInt(v int64) int {
	return int(max(min(v, 1<<31-1), -1<<31))
}

func gameModeName(v int64) string {
	switch v {
	case 0:
		return "survival"
	case 1:
		return "creative"
	case 2:
		return "adventure"
	case 3:
		return "spectator"
	}
	return ""
}

func difficultyName(v int64) string {
	switch v {
	case 0:
		return "peaceful"
	case 1:
		return "easy"
	case 2:
		return "normal"
	case 3:
		return "hard"
	}
	return ""
}

func difficultyByName(s string) string {
	switch s = strings.ToLower(s); s {
	case "peaceful", "easy", "normal", "hard":
		return s
	}
	return ""
}

// uuidString formats a UUID stored as four ints, most significant first.
func uuidString(u []int32) string {
	if len(u) != 4 {
		return ""
	}
	var b [16]byte
	for i, v := range u {
		b[4*i] = byte(v >> 24)
		b[4*i+1] = byte(v >> 16)
		b[4*i+2] = byte(v >> 8)
		b[4*i+3] = byte(v)
	}
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
