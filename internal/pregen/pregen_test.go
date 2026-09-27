package pregen

import (
	"errors"
	"strings"
	"testing"
)

func TestPlatformFor(t *testing.T) {
	for typ, want := range map[string]Platform{"paper": Bukkit, "purpur": Bukkit, "fabric": Fabric, "quilt": Fabric, "neoforge": NeoForge, "forge": Forge} {
		got, err := PlatformFor(typ)
		if err != nil || got != want {
			t.Errorf("PlatformFor(%q) = %q, %v; want %q", typ, got, err, want)
		}
	}
	for _, typ := range []string{"vanilla", "", "Paper"} {
		_, err := PlatformFor(typ)
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeUnsupportedServer || e.Params["type"] != typ || e.Hint == "" {
			t.Errorf("PlatformFor(%q) error = %#v, want %s", typ, err, CodeUnsupportedServer)
		}
	}
	if _, err := PlatformFor(""); !strings.Contains(err.Error(), "that this server can't load.") {
		t.Errorf("empty type message = %q", err)
	}
	if _, err := PlatformFor("vanilla"); !strings.Contains(err.Error(), `that "vanilla" servers can't load.`) {
		t.Errorf("vanilla message = %q", err)
	}
	for p, want := range map[Platform]string{Bukkit: "paper", Fabric: "fabric", NeoForge: "neoforge", Forge: "forge", "folia": ""} {
		if got := p.ModrinthLoader(); got != want {
			t.Errorf("%q.ModrinthLoader() = %q, want %q", p, got, want)
		}
	}
}

func TestWorlds(t *testing.T) {
	got := Worlds(Bukkit, "survival")
	want := []World{{"survival", Overworld}, {"survival_nether", Nether}, {"survival_the_end", End}}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("Worlds(Bukkit) = %v, want %v", got, want)
	}
	for _, p := range []Platform{Fabric, NeoForge, Forge} {
		got := Worlds(p, "survival")
		if got[0].Name != "minecraft:overworld" || got[1].Name != "minecraft:the_nether" || got[2].Name != "minecraft:the_end" {
			t.Errorf("Worlds(%s) = %v", p, got)
		}
		for _, w := range got {
			if err := p.CheckWorld(w.Name); err != nil {
				t.Errorf("%s: standard world %q rejected: %v", p, w.Name, err)
			}
		}
	}
	for _, w := range want {
		if err := Bukkit.CheckWorld(w.Name); err != nil {
			t.Errorf("standard world %q rejected: %v", w.Name, err)
		}
	}
	cases := []struct {
		p            Platform
		level, world string
		want         Dimension
	}{
		{Bukkit, "world", "world_nether", Nether},
		{Bukkit, "world", "world_the_end", End},
		{Bukkit, "world", "world", Overworld},
		{Bukkit, "world", "resource_world", Overworld},
		{Fabric, "world", "minecraft:the_nether", Nether},
		{NeoForge, "world", "minecraft:the_end", End},
		{Fabric, "world", "twilightforest:twilight_forest", Overworld},
	}
	for _, c := range cases {
		if got := DimensionOf(c.p, c.level, c.world); got != c.want {
			t.Errorf("DimensionOf(%s, %q, %q) = %s, want %s", c.p, c.level, c.world, got, c.want)
		}
	}
}

func TestCheckWorld(t *testing.T) {
	valid := map[Platform][]string{
		Bukkit:   {"world", "world_nether", "World-2", "my.world", "_x", "0", strings.Repeat("a", 64)},
		Fabric:   {"minecraft:overworld", "twilightforest:twilight_forest", "mod:dims/deep.dark-1", "a:b"},
		NeoForge: {"minecraft:the_end"},
	}
	invalid := map[Platform][]string{
		Bukkit: {
			"", "world stop", "world;stop", "world\nstop", "world\rstop", "-world", ".world", "../world",
			"world/x", `world\x`, "wörld", strings.Repeat("a", 65), "minecraft:overworld", "\"world\"",
		},
		Fabric: {
			"", "overworld", "Minecraft:overworld", "minecraft:Overworld", "minecraft:the end", "minecraft:",
			":overworld", "minecraft:overworld\nstop", "minecraft:../x", "minecraft:a/../b", "minecraft:a//b",
			"minecraft:/a", "minecraft:a/", "minecraft:./a", "a:b:c", "world",
		},
	}
	for p, names := range valid {
		for _, w := range names {
			if err := p.CheckWorld(w); err != nil {
				t.Errorf("%s.CheckWorld(%q) = %v, want nil", p, w, err)
			}
		}
	}
	for p, names := range invalid {
		for _, w := range names {
			err := p.CheckWorld(w)
			var e *Error
			if !errors.As(err, &e) || e.Code != CodeInvalidWorld || e.Params["world"] != w {
				t.Errorf("%s.CheckWorld(%q) = %v, want %s", p, w, err, CodeInvalidWorld)
				continue
			}
			if strings.ContainsAny(e.Msg, "\n\r") {
				t.Errorf("%s.CheckWorld(%q) message holds a line break: %q", p, w, e.Msg)
			}
		}
	}
	if err := Platform("forge").CheckWorld("world"); err == nil {
		t.Error("unknown platform accepted a world")
	}
}

func TestPlanCheck(t *testing.T) {
	ok := Plan{World: "world", Radius: 2500, Shape: Square}
	cases := []struct {
		name string
		p    Platform
		edit func(*Plan)
		code string
	}{
		{"valid square", Bukkit, func(*Plan) {}, ""},
		{"valid circle off center", Bukkit, func(pl *Plan) { pl.Shape, pl.CenterX, pl.CenterZ = Circle, -12000, 800 }, ""},
		{"valid fabric", Fabric, func(pl *Plan) { pl.World = "minecraft:the_nether" }, ""},
		{"no platform", "", func(*Plan) {}, CodeUnsupportedServer},
		{"bad world", Bukkit, func(pl *Plan) { pl.World = "world; op Steve" }, CodeInvalidWorld},
		{"dimension on bukkit", Bukkit, func(pl *Plan) { pl.World = "minecraft:overworld" }, CodeInvalidWorld},
		{"empty shape", Bukkit, func(pl *Plan) { pl.Shape = "" }, CodeInvalidShape},
		{"chunky-only shape", Bukkit, func(pl *Plan) { pl.Shape = "star" }, CodeInvalidShape},
		{"min radius", Bukkit, func(pl *Plan) { pl.Radius = MinRadius }, ""},
		{"radius too small", Bukkit, func(pl *Plan) { pl.Radius = MinRadius - 1 }, CodeRadiusTooSmall},
		{"negative radius", Bukkit, func(pl *Plan) { pl.Radius = -100 }, CodeRadiusTooSmall},
		{"max radius", Bukkit, func(pl *Plan) { pl.Radius = MaxRadius }, ""},
		{"radius too large", Bukkit, func(pl *Plan) { pl.Radius = MaxRadius + 1 }, CodeRadiusTooLarge},
		{"at world edge", Bukkit, func(pl *Plan) { pl.CenterX = WorldLimit - pl.Radius }, ""},
		{"past world edge x", Bukkit, func(pl *Plan) { pl.CenterX = WorldLimit - pl.Radius + 1 }, CodeOutsideWorld},
		{"past world edge -z", Bukkit, func(pl *Plan) { pl.CenterZ = -(WorldLimit - pl.Radius + 1) }, CodeOutsideWorld},
		{"far center on spawn", Bukkit, func(pl *Plan) { pl.CenterX, pl.CenterOnSpawn = WorldLimit, true }, ""},
	}
	for _, c := range cases {
		pl := ok
		c.edit(&pl)
		err := pl.Check(c.p)
		if c.code == "" {
			if err != nil {
				t.Errorf("%s: Check = %v, want nil", c.name, err)
			}
			continue
		}
		var e *Error
		if !errors.As(err, &e) || e.Code != c.code || e.Msg == "" || e.Hint == "" {
			t.Errorf("%s: Check = %#v, want %s with message and hint", c.name, err, c.code)
		}
	}
	err := Plan{World: "world", Radius: 5, Shape: Square}.Check(Bukkit)
	var e *Error
	if errors.As(err, &e) && (e.Params["radius"] != 5 || e.Params["min"] != MinRadius) {
		t.Errorf("radius params = %v", e.Params)
	}
}

func TestPresets(t *testing.T) {
	ps := Presets()
	ids := []string{"small", "medium", "large", "huge"}
	if len(ps) != len(ids) {
		t.Fatalf("Presets() = %v", ps)
	}
	for i, p := range ps {
		if p.ID != ids[i] || (i > 0 && p.Radius <= ps[i-1].Radius) {
			t.Errorf("preset %d = %+v, want %q and larger than the one before", i, p, ids[i])
		}
		pl, ok := PresetPlan(p.ID, "minecraft:overworld")
		if !ok || pl.Radius != p.Radius || pl.Shape != Square || !pl.CenterOnSpawn || pl.World != "minecraft:overworld" {
			t.Errorf("PresetPlan(%q) = %+v, %v", p.ID, pl, ok)
		}
		if err := pl.Check(Fabric); err != nil {
			t.Errorf("preset %q plan fails its own check: %v", p.ID, err)
		}
	}
	if _, ok := PresetPlan("gigantic", "world"); ok {
		t.Error("PresetPlan accepted an unknown preset")
	}
}

func TestErrorIs(t *testing.T) {
	cause := errors.New("connection refused")
	err := error(&Error{Code: CodeConsole, Msg: "m", Err: cause})
	if !errors.Is(err, cause) {
		t.Error("errors.Is does not reach the cause")
	}
	if errors.Is(err, ErrNotInstalled) {
		t.Error("errors.Is matched a different code")
	}
	if !errors.Is(unknownWorld("world_nether"), ErrUnknownWorld) || !errors.Is(alreadyRunning("world"), ErrAlreadyRunning) {
		t.Error("errors.Is does not match by code")
	}
	if got := shortQuote(strings.Repeat("x", 100)); len([]rune(got)) != 73 || !strings.Contains(got, "…") {
		t.Errorf("shortQuote of a long name = %q", got)
	}
}
