// Package pregen pre-generates a server's map with Chunky, the plugin and mod
// that generates chunks ahead of time so players exploring new land don't
// wait for it, and the server doesn't lag while it is generated.
//
// It plans a task (world, center, radius, shape), estimates what the task
// costs, drives Chunky through the server console, reads Chunky's console
// messages, writes Chunky's config and decides when to pause for players.
// It installs nothing: the add-on installer fetches Chunky using the
// identifiers below, and Detect finds the installed jar.
package pregen

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/gamefiles"
)

const (
	// ModrinthProjectID and ModrinthSlug identify Chunky on Modrinth, which
	// hosts its Paper, Fabric and NeoForge builds.
	ModrinthProjectID = "fALzjamp"
	ModrinthSlug      = "chunky"
	// HangarProject is Chunky on PaperMC's Hangar, which hosts its Paper
	// builds only.
	HangarProject = "pop4959/Chunky"
)

// Platform is the kind of server Chunky runs on. It decides where Chunky is
// installed, where it keeps its files and how it names worlds.
type Platform string

const (
	// Bukkit is Paper and servers built on it: Chunky is a plugin in
	// plugins/, and worlds are named like their folders ("world_nether").
	Bukkit Platform = "bukkit"
	// Fabric is Fabric and Quilt: Chunky is a mod in mods/, and worlds are
	// named by dimension ("minecraft:the_nether").
	Fabric Platform = "fabric"
	// NeoForge is like Fabric, with NeoForge's mod metadata.
	NeoForge Platform = "neoforge"
)

// PlatformFor maps a Playkeeper server type to the platform Chunky runs on.
// Vanilla servers can't load plugins or mods, so they have none.
func PlatformFor(serverType string) (Platform, error) {
	switch serverType {
	case "paper", "purpur":
		return Bukkit, nil
	case "fabric", "quilt":
		return Fabric, nil
	case "neoforge":
		return NeoForge, nil
	}
	what := "this server"
	if serverType != "" {
		what = shortQuote(serverType) + " servers"
	}
	return "", &Error{
		Code:   CodeUnsupportedServer,
		Params: map[string]any{"type": serverType},
		Msg:    fmt.Sprintf("Map pre-generation needs Chunky, a plugin or mod that %s can't load.", what),
		Hint:   "Switch the server to Paper, Fabric or NeoForge to pre-generate its map.",
	}
}

// ModrinthLoader is the Modrinth loader whose Chunky build runs on p.
func (p Platform) ModrinthLoader() string {
	switch p {
	case Bukkit:
		return "paper"
	case Fabric:
		return "fabric"
	case NeoForge:
		return "neoforge"
	}
	return ""
}

func (p Platform) valid() bool { return p == Bukkit || p == Fabric || p == NeoForge }

// Dimension is the kind of a world, which decides how much disk its chunks
// take.
type Dimension string

const (
	Overworld Dimension = "overworld"
	Nether    Dimension = "nether"
	End       Dimension = "end"
)

// World is a world Chunky can pre-generate.
type World struct {
	// Name is how Chunky's commands and messages refer to the world.
	Name      string    `json:"name"`
	Dimension Dimension `json:"dimension"`
}

// Worlds lists a server's three standard worlds; level is its level-name
// (the overworld's folder). A server may have the Nether or the End turned
// off, in which case Chunky reports that world as unknown.
func Worlds(p Platform, level string) []World {
	if p == Bukkit {
		return []World{{level, Overworld}, {level + "_nether", Nether}, {level + "_the_end", End}}
	}
	return []World{{"minecraft:overworld", Overworld}, {"minecraft:the_nether", Nether}, {"minecraft:the_end", End}}
}

var (
	// Bukkit world names are folder names. Chunky splits its arguments on
	// spaces, so names with spaces or symbols can't be selected at all.
	reBukkitWorld = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.\-]{0,63}$`)
	// Fabric and NeoForge take a dimension ID, parsed by the game's command
	// parser as a resource location.
	reDimension = regexp.MustCompile(`^[a-z0-9_.\-]{1,64}:[a-z0-9_.\-/]{1,128}$`)
)

// CheckWorld reports whether world is a name Chunky on p can be given in a
// console command. It does not check that the world exists; Chunky does.
func (p Platform) CheckWorld(world string) error {
	ok := false
	switch p {
	case Bukkit:
		ok = reBukkitWorld.MatchString(world)
	case Fabric, NeoForge:
		ok = reDimension.MatchString(world)
		if ok {
			_, path, _ := strings.Cut(world, ":")
			for _, seg := range strings.Split(path, "/") {
				if seg == "" || seg == "." || seg == ".." {
					ok = false
				}
			}
		}
	}
	if ok {
		return nil
	}
	hint := "Pick one of the server's worlds from the list."
	if p == Bukkit {
		hint = "Chunky can only select worlds whose names use letters, digits, dots, dashes and underscores. Rename the world folder (and level-name) to pre-generate it."
	}
	return &Error{
		Code:   CodeInvalidWorld,
		Params: map[string]any{"world": world},
		Msg:    fmt.Sprintf("%s is not a world name Chunky can select.", shortQuote(world)),
		Hint:   hint,
	}
}

// DimensionOf is the kind of the world named world on a server whose
// level-name is level. Worlds added by plugins or mods count as overworlds.
func DimensionOf(p Platform, level, world string) Dimension {
	for _, w := range Worlds(p, level) {
		if w.Name == world {
			return w.Dimension
		}
	}
	return Overworld
}

// Shape is the outline of the area to generate.
type Shape string

const (
	Square Shape = "square"
	Circle Shape = "circle"
)

const (
	// MinRadius and MaxRadius bound a plan's radius in blocks. The largest
	// plan covers about 39 million chunks (hundreds of GB of disk).
	MinRadius = 16
	MaxRadius = 50_000
	// WorldLimit is how far from 0, 0 the default world border reaches, in
	// blocks; Chunky refuses anything past 30 million.
	WorldLimit = 29_999_984
)

// Plan is a pre-generation task to start.
type Plan struct {
	// World is the world's name as Chunky knows it (see Worlds).
	World string `json:"world"`
	// CenterX and CenterZ are the block the area is centered on. They are
	// ignored when CenterOnSpawn is set.
	CenterX int `json:"centerX"`
	CenterZ int `json:"centerZ"`
	// CenterOnSpawn centers the area on the world's spawn point, where new
	// players appear; the controller asks Chunky where that is.
	CenterOnSpawn bool `json:"centerOnSpawn"`
	// Radius is the distance in blocks from the center to the edge: half
	// the width of a square, or a circle's radius.
	Radius int   `json:"radius"`
	Shape  Shape `json:"shape"`
}

// Check reports the first thing wrong with the plan for a server on p.
func (pl Plan) Check(p Platform) error {
	if !p.valid() {
		return &Error{Code: CodeUnsupportedServer, Params: map[string]any{"type": string(p)}, Msg: "This server type can't run Chunky.", Hint: "Switch the server to Paper, Fabric or NeoForge to pre-generate its map."}
	}
	if err := p.CheckWorld(pl.World); err != nil {
		return err
	}
	if pl.Shape != Square && pl.Shape != Circle {
		return &Error{Code: CodeInvalidShape, Params: map[string]any{"shape": string(pl.Shape)}, Msg: fmt.Sprintf("%s is not a shape Playkeeper can pre-generate.", shortQuote(string(pl.Shape))), Hint: `Choose "square" or "circle".`}
	}
	if pl.Radius < MinRadius {
		return &Error{Code: CodeRadiusTooSmall, Params: map[string]any{"radius": pl.Radius, "min": MinRadius}, Msg: fmt.Sprintf("A radius of %d blocks is too small to pre-generate; the smallest is %d.", pl.Radius, MinRadius), Hint: "Pick a preset or a radius of at least a few hundred blocks."}
	}
	if pl.Radius > MaxRadius {
		return &Error{Code: CodeRadiusTooLarge, Params: map[string]any{"radius": pl.Radius, "max": MaxRadius}, Msg: fmt.Sprintf("A radius of %d blocks is larger than Playkeeper pre-generates (at most %d).", pl.Radius, MaxRadius), Hint: "Players rarely travel that far; a radius of 5,000 to 10,000 blocks covers most worlds."}
	}
	if !pl.CenterOnSpawn && (abs(pl.CenterX)+pl.Radius > WorldLimit || abs(pl.CenterZ)+pl.Radius > WorldLimit) {
		return &Error{Code: CodeOutsideWorld, Params: map[string]any{"centerX": pl.CenterX, "centerZ": pl.CenterZ, "radius": pl.Radius, "limit": WorldLimit}, Msg: "This area reaches past the edge of the Minecraft world.", Hint: "Move the center closer to 0, 0 or use a smaller radius."}
	}
	return nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Preset is a ready-made plan size for the UI to offer.
type Preset struct {
	// ID is a stable name for translation: "small", "medium", "large" or
	// "huge".
	ID string `json:"id"`
	// Radius is in blocks, from the center to the edge.
	Radius int `json:"radius"`
}

// Presets are square areas around spawn, smallest first.
func Presets() []Preset {
	return []Preset{{"small", 1_000}, {"medium", 2_500}, {"large", 5_000}, {"huge", 10_000}}
}

// PresetPlan is preset id as a square plan centered on world's spawn.
func PresetPlan(id, world string) (Plan, bool) {
	for _, p := range Presets() {
		if p.ID == id {
			return Plan{World: world, CenterOnSpawn: true, Radius: p.Radius, Shape: Square}, true
		}
	}
	return Plan{}, false
}

// Error codes: stable identifiers the UI translates, with Error.Params as
// the values.
const (
	CodeUnsupportedServer = "unsupported_server"
	CodeInvalidWorld      = "invalid_world"
	CodeInvalidShape      = "invalid_shape"
	CodeRadiusTooSmall    = "radius_too_small"
	CodeRadiusTooLarge    = "radius_too_large"
	CodeOutsideWorld      = "outside_world"
	CodeNotEnoughDisk     = "not_enough_disk"
	CodeNotInstalled      = "not_installed"
	CodeUnknownWorld      = "unknown_world"
	CodeAlreadyRunning    = "already_running"
	CodeSavedTask         = "saved_task"
	CodeNotRunning        = "not_running"
	CodeNothingToContinue = "nothing_to_continue"
	CodeRadiusLimit       = "radius_limit"
	CodeUnexpectedReply   = "unexpected_reply"
	CodeConsole           = "console_failed"
	CodeConfig            = "config_failed"
	// CodeFileRefused is a file in the server's folder that Playkeeper
	// refused, such as a link; see internal/gamefiles.
	CodeFileRefused = "file_refused"
)

// Error is an error the UI can show: Code is a stable identifier to
// translate, Params its values, Msg the English sentence and Hint what to
// do next. errors.Is matches Errors with the same Code, so callers can test
// against the Err* values.
type Error struct {
	Code   string
	Params map[string]any
	Msg    string
	Hint   string
	Err    error
}

func (e *Error) Error() string { return e.Msg }

func (e *Error) Unwrap() error { return e.Err }

func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// Targets for errors.Is.
var (
	ErrNotInstalled      = &Error{Code: CodeNotInstalled}
	ErrUnknownWorld      = &Error{Code: CodeUnknownWorld}
	ErrAlreadyRunning    = &Error{Code: CodeAlreadyRunning}
	ErrSavedTask         = &Error{Code: CodeSavedTask}
	ErrNotRunning        = &Error{Code: CodeNotRunning}
	ErrNothingToContinue = &Error{Code: CodeNothingToContinue}
	ErrRadiusLimit       = &Error{Code: CodeRadiusLimit}
	ErrUnexpectedReply   = &Error{Code: CodeUnexpectedReply}
)

// refusal explains a file in the server's folder that Playkeeper refused,
// after the sentence saying what it could not do. Other errors are returned
// as they are.
func refusal(err error, couldNot string) error {
	var ge *gamefiles.Error
	if !errors.As(err, &ge) {
		return err
	}
	params := map[string]any{"kind": string(ge.Kind)}
	for k, v := range ge.Params {
		params[k] = v
	}
	return &Error{Code: CodeFileRefused, Params: params, Msg: couldNot + " " + ge.Msg, Hint: ge.Hint, Err: ge}
}

// shortQuote quotes a name for a message, eliding the middle of a long one.
func shortQuote(s string) string {
	if r := []rune(s); len(r) > 80 {
		s = string(r[:40]) + "…" + string(r[len(r)-30:])
	}
	return strconv.Quote(s)
}
