package packs

import (
	"cmp"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// rangeText is m's format range as "min to max", or else its problem.
func rangeText(m mcmeta) string {
	if m.formats == nil {
		return m.formatProblem
	}
	return fmt.Sprintf("%s to %s", m.formats.Min, m.formats.Max)
}

// packMcmeta is a pack.mcmeta whose pack section holds a description and
// fields.
func packMcmeta(fields string) string {
	if fields != "" {
		fields = ", " + fields
	}
	return `{"pack": {"description": "d"` + fields + `}}`
}

// The fixtures are the pack.mcmeta files of popular packs for Minecraft
// 26.3, as published.
func TestMcmetaFixtures(t *testing.T) {
	for _, tc := range []struct {
		file    string
		use     Kind
		desc    string
		formats string
	}{
		{"dungeons-and-taverns-6.0.1", Data, "Structure datapack adding unique new structures in the vanilla style", "121.0 to 121.0"},
		{"fresh-animations-1.10.5", Resource, "■ 1.10.5 BETA\n■ By FreshLX", "84.0 to 999.*"},
		{"low-on-fire-26.3", Resource, "Handle the heat!\nby Haikis", "15.0 to 200.*"},
		{"low-on-fire-26.3", Data, "Handle the heat!\nby Haikis", "15.0 to 200.*"},
		{"terralith-2.6.4", Data, "Terralith - Overworld Evolved\nCreated by Stardust Labs", "107.0 to 107.*"},
		{"veinminer-1.3.6", Data, "Veinminer | 26.3+\nBy Miraculixx", "121.0 to 121.*"},
	} {
		b, err := os.ReadFile("testdata/" + tc.file + ".mcmeta")
		if err != nil {
			t.Fatal(err)
		}
		m, err := parseMcmeta(b, tc.use)
		if err != nil {
			t.Errorf("%s as %s: %v", tc.file, tc.use, err)
			continue
		}
		if m.description != tc.desc || rangeText(m) != tc.formats || m.features != nil || m.overlays != nil {
			t.Errorf("%s as %s: description %q, formats %s, features %q, overlays %q; want %q, %s",
				tc.file, tc.use, m.description, rangeText(m), m.features, m.overlays, tc.desc, tc.formats)
		}
	}
}

func TestMcmetaFormats(t *testing.T) {
	list := func(first string, n int) string { return "[" + first + strings.Repeat(", 0", n-1) + "]" }
	const (
		badMin       = "Pack has an invalid min_format: it must be a number of at least 0, or a list of 1 to 256 of them"
		badMax       = "Pack has an invalid max_format: it must be a number of at least 0, or a list of 1 to 256 of them"
		badPF        = "Pack has an invalid pack_format: it must be a whole number"
		badSupported = "Pack has an invalid supported_formats: it must be a number, a list of two numbers, or an object with min_inclusive and max_inclusive"
		badOrder     = "Pack has an invalid supported_formats: min_inclusive must be less than or equal to max_inclusive"
		newerData    = "Pack declares support for version newer than 81, but is missing mandatory fields min_format and max_format"
		noFormat     = "Pack could not be parsed, missing format version information"
		halfDeclared = "Pack missing field, must declare both min_format and max_format"
		belowFifteen = "Multi-version packs cannot support minimum version of less than 15, since this will leave versions in range unable to load pack."
	)
	for _, tc := range []struct {
		fields string
		use    Kind // Data if empty
		want   string
	}{
		{`"pack_format": 48`, "", "48.0 to 48.0"},
		{`"pack_format": 48.9`, "", "48.0 to 48.0"},
		{`"pack_format": -3`, "", "-3.0 to -3.0"},
		{`"pack_format": 81`, "", "81.0 to 81.0"},
		{`"pack_format": 82`, "", newerData},
		{`"pack_format": 64`, Resource, "64.0 to 64.0"},
		{`"pack_format": 65`, Resource, "Pack declares support for version newer than 64, but is missing mandatory fields min_format and max_format"},
		{``, "", noFormat},
		{`"pack_format": null, "min_format": null, "max_format": null, "supported_formats": null`, "", noFormat},
		{`"min_format": 88`, "", halfDeclared},
		{`"max_format": 88, "pack_format": 48`, "", halfDeclared},

		{`"min_format": 88, "max_format": [94, 1]`, "", "88.0 to 94.1"},
		{`"min_format": [88, 2, 7], "max_format": [94]`, "", "88.2 to 94.*"},
		{`"min_format": 95, "max_format": 90`, "", "Pack min_format (95.0) is greater than max_format (90.*)"},
		{`"min_format": [94, 1], "max_format": [94, 0]`, "", "Pack min_format (94.1) is greater than max_format (94.0)"},
		{`"min_format": 82, "max_format": 90, "supported_formats": [82, 90]`, "", "Pack key supported_formats is deprecated starting from pack format 82. Remove supported_formats from your pack.mcmeta."},
		{`"min_format": 65, "max_format": 70, "supported_formats": [65, 70]`, Resource, "Pack key supported_formats is deprecated starting from pack format 65. Remove supported_formats from your pack.mcmeta."},
		{`"min_format": 82, "max_format": 90, "pack_format": 91`, "", "Pack declared support for versions 82 to 90 but declared main format is 91"},
		{`"min_format": 82, "max_format": 90, "pack_format": 85`, "", "82.0 to 90.*"},

		// Packs that also work in games older than format 82 declare
		// their range the old way too.
		{`"min_format": 71, "max_format": 90`, "", `Pack declares support for format 71, but game versions supporting formats 15 to 81 require a supported_formats field. Add "supported_formats": [71, 81] or require a version greater or equal to 82.0.`},
		{`"min_format": 71, "max_format": 90, "supported_formats": [70, 81], "pack_format": 71`, "", "Pack version declaration mismatch between supported_formats (from 70) and min_format (71.0)"},
		{`"min_format": 71, "max_format": 90, "supported_formats": [71, 85], "pack_format": 71`, "", "Pack version declaration mismatch between supported_formats (up to 85) and max_format (90.*)"},
		{`"min_format": 71, "max_format": 90, "supported_formats": [71, 81], "pack_format": 71`, "", "71.0 to 90.*"},
		{`"min_format": 71, "max_format": 90, "supported_formats": {"min_inclusive": 71, "max_inclusive": 90}, "pack_format": 80`, "", "71.0 to 90.*"},
		{`"min_format": 71, "max_format": 90, "supported_formats": [71, 81]`, "", `Pack declares support for formats up to 81, but game versions supporting formats 15 to 81 require a pack_format field. Add "pack_format": 71 or require a version greater or equal to 82.0.`},
		{`"min_format": 10, "max_format": 90, "supported_formats": [10, 81], "pack_format": 10`, "", belowFifteen},

		{`"supported_formats": [50, 70], "pack_format": 60`, "", "50.0 to 70.0"},
		{`"supported_formats": 50, "pack_format": 50`, "", "50.0 to 50.0"},
		{`"supported_formats": {"min_inclusive": 50, "max_inclusive": 70}, "pack_format": 70`, "", "50.0 to 70.0"},
		{`"supported_formats": [70, 90], "pack_format": 70`, "", newerData},
		{`"supported_formats": [50, 70]`, "", `Pack declares support for formats up to 81, but game versions supporting formats 15 to 81 require a pack_format field. Add "pack_format": 50 or require a version greater or equal to 82.0.`},
		{`"supported_formats": [50, 70], "pack_format": 71`, "", "Pack declared support for versions 50 to 70 but declared main format is 71"},
		{`"supported_formats": [10, 20], "pack_format": 10`, "", belowFifteen},

		{`"min_format": "88", "max_format": 90`, "", badMin},
		{`"min_format": -1, "max_format": 90`, "", badMin},
		{`"min_format": [], "max_format": 90`, "", badMin},
		{`"min_format": [88, -1], "max_format": 90`, "", badMin},
		{`"min_format": ` + list("88", 257) + `, "max_format": 90`, "", badMin},
		{`"min_format": ` + list("88", 256) + `, "max_format": 90`, "", "88.0 to 90.*"},
		{`"min_format": "x", "pack_format": "y"`, "", badMin},
		{`"min_format": 88, "max_format": {"major": 90}`, "", badMax},
		{`"pack_format": "48"`, "", badPF},
		{`"pack_format": 1e20`, "", badPF},
		{`"pack_format": 4294967344`, "", badPF},
		{`"supported_formats": [1], "pack_format": 1`, "", badSupported},
		{`"supported_formats": [1, 2, 3], "pack_format": 1`, "", badSupported},
		{`"supported_formats": {"min_inclusive": 50}, "pack_format": 50`, "", badSupported},
		{`"supported_formats": [true, 20], "pack_format": 20`, "", badSupported},
		{`"supported_formats": "50", "pack_format": 50`, "", badSupported},
		{`"supported_formats": [60, 50], "pack_format": 50`, "", badOrder},
		{`"supported_formats": {"min_inclusive": 60, "max_inclusive": 50}, "pack_format": 50`, "", badOrder},
	} {
		use := cmp.Or(tc.use, Data)
		m, err := parseMcmeta([]byte(packMcmeta(tc.fields)), use)
		if err != nil {
			t.Errorf("%s as %s: %v", tc.fields, use, err)
			continue
		}
		if (m.formats == nil) == (m.formatProblem == "") {
			t.Errorf("%s as %s: formats %v with problem %q", tc.fields, use, m.formats, m.formatProblem)
		}
		if got := rangeText(m); got != tc.want {
			t.Errorf("%s as %s:\n got %s\nwant %s", tc.fields, use, got, tc.want)
		}
	}
}

func TestMcmetaDescription(t *testing.T) {
	for _, tc := range []struct{ desc, want string }{
		{`"A pack"`, "A pack"},
		{`"§aGreen §lbold§r text"`, "Green bold text"},
		{`"ends with §"`, "ends with"},
		{`["a", {"text": "b"}, ["c", "d"]]`, "abcd"},
		{`{"text": "a", "bold": true, "color": "red", "extra": ["b", {"text": "c", "extra": ["d"]}]}`, "abcd"},
		{`{"translate": "pack.name"}`, "pack.name"},
		{`{"translate": "pack.name", "fallback": "My pack"}`, "My pack"},
		{`{"translate": "x", "with": [1, true, "s", {"text": "t"}, ["u"]], "fallback": "F"}`, "F"},
		{`{"keybind": "key.jump"}`, "key.jump"},
		{`{"type": "keybind", "keybind": "key.jump", "text": "ignored"}`, "key.jump"},
		{`{"text": "wins", "translate": "loses"}`, "wins"},
		{`{"text": null, "translate": "used"}`, "used"},
		{`["A", {"score": {"name": "@p", "objective": "o"}}, {"selector": "@a"}, {"nbt": "x", "block": "0 0 0"}, {"sprite": "item/apple"}, {"object": "atlas"}, {"player": {"name": "Steve"}}, "B"]`, "AB"},
		{`"  line one  \n   line two  "`, "line one\nline two"},
		{`"\n\nA\n\n"`, "A"},
		{`"a\tb\u0007c"`, "a b c"},
		{`"a\u2028b"`, "a b"},
		{`"a\uE000b\uDBA3\uDF26"`, "ab"},
		{`"` + strings.Repeat("x", 300) + `"`, strings.Repeat("x", 255) + "…"},
		{`{"text": ""}`, ""},
	} {
		m, err := parseMcmeta([]byte(`{"pack": {"pack_format": 48, "description": `+tc.desc+`}}`), Data)
		if err != nil || m.description != tc.want {
			t.Errorf("description %s = %q, %v; want %q", tc.desc, m.description, err, tc.want)
		}
	}
}

func TestMcmetaInvalid(t *testing.T) {
	desc := func(d string) string { return `{"pack": {"pack_format": 48, "description": ` + d + `}}` }
	section := func(name, v string) string {
		return `{"pack": {"pack_format": 48, "description": "d"}, "` + name + `": ` + v + `}`
	}
	overlays := func(entries string) string { return section("overlays", `{"entries": `+entries+`}`) }
	const noneOf = `a part has none of "text", "translate", "keybind", "score", "selector", "nbt" or "object" in a form the game reads`
	for _, tc := range []struct {
		mcmeta, problem, detail string
	}{
		{``, "json", "the file is empty"},
		{"  \n", "json", "the file is empty"},
		{`{"pack": {`, "json", "the file ends in the middle of the JSON"},
		{"{\"pack\":\n  {\"description\": x}}", "json", "invalid character 'x' looking for beginning of value (line 2, column 19)"},
		{"\uFEFF{\"pack\": x}", "json", "invalid character 'x' looking for beginning of value (line 1, column 10)"},
		{`[1]`, "not_object", ""},
		{`"pack"`, "not_object", ""},
		{`{}`, "no_pack", ""},
		{`{"pack": null}`, "pack_not_object", ""},
		{`{"pack": []}`, "pack_not_object", ""},

		{`{"pack": {"pack_format": 48}}`, "description", "it is missing"},
		{desc(`null`), "description", "it is missing"},
		{desc(`5`), "description", "5 is not text, a list or an object"},
		{desc(`true`), "description", "true is not text, a list or an object"},
		{desc(`12345678901234567890123456789012345678901234567890`), "description", "1234567890123456789012345678901234567… is not text, a list or an object"},
		{desc(`[]`), "description", "it is an empty list"},
		{desc(`["a", 5]`), "description", "5 is not text, a list or an object"},
		{desc(`{"color": "red"}`), "description", noneOf},
		{desc(`{"text": 5}`), "description", noneOf},
		{desc(`{"text": "a", "extra": []}`), "description", `its "extra" is not a list of parts`},
		{desc(`{"text": "a", "extra": "b"}`), "description", `its "extra" is not a list of parts`},
		{desc(`{"type": "text"}`), "description", `a part of type "text" lacks its content`},
		{desc(`{"type": "bogus", "text": "a"}`), "description", `"bogus" is not a type of text part`},
		{desc(`{"translate": "a", "with": "b"}`), "description", noneOf},
		{desc(`{"translate": "a", "with": [null]}`), "description", noneOf},
		{desc(`{"score": "x"}`), "description", noneOf},
		{desc(strings.Repeat("[", 40) + `"a"` + strings.Repeat("]", 40)), "description", "its parts are nested too deeply"},

		{section("features", `null`), "features", "it is not a JSON object"},
		{section("features", `{}`), "features", `it has no "enabled" list`},
		{section("features", `{"enabled": "trade_rebalance"}`), "features", `its "enabled" is not a list`},
		{section("features", `{"enabled": ["Trade Rebalance"]}`), "features", `"Trade Rebalance" is not a feature name`},
		{section("features", `{"enabled": [5]}`), "features", "5 is not a feature name"},

		{section("overlays", `null`), "overlays", "it is not a JSON object"},
		{section("overlays", `{}`), "overlays", `it has no "entries" list`},
		{overlays(`{}`), "overlays", `its "entries" is not a list`},
		{overlays(`[1]`), "overlays", "entry 1 is not a JSON object"},
		{overlays(`[{"directory": "a", "formats": 50}, {"formats": 50}]`), "overlays", "entry 2 has no directory"},
		{overlays(`[{"directory": 5}]`), "overlays", "entry 1 has no directory"},
		{overlays(`[{"directory": "a/b", "formats": 50}]`), "overlays", "a/b is not accepted directory name"},
		{overlays(`[{"directory": "o", "min_format": "x", "max_format": 90}]`), "overlays", `Overlay "o" has an invalid min_format: it must be a number of at least 0, or a list of 1 to 256 of them`},
		{overlays(`[{"directory": "o", "formats": [2, 1]}]`), "overlays", `Overlay "o" has an invalid formats: min_inclusive must be less than or equal to max_inclusive`},
		{overlays(`[{"directory": "o", "min_format": 90}]`), "overlays", `Overlay "o" missing field, must declare both min_format and max_format`},
		{overlays(`[{"directory": "old", "formats": [50, 70]}, {"directory": "new", "min_format": 90, "max_format": 95}]`), "overlays", `Overlay "new" missing required field formats, must be present in all overlays for any overlays to work across game versions`},
		{overlays(`[{"directory": "o", "formats": [70, 90]}]`), "overlays", `Overlay "o" declares support for version newer than 81, but is missing mandatory fields min_format and max_format`},
		{overlays(`[{"directory": "o", "formats": [90, 95]}]`), "overlays", `Overlay "o" declares support for version newer than 81, but is missing mandatory fields min_format and max_format`},
		{overlays(`[{"directory": "o", "min_format": 90, "max_format": 95, "formats": [90, 95]}]`), "overlays", `Overlay "o" key formats is deprecated starting from pack format 82. Remove formats from your pack.mcmeta.`},
	} {
		t.Run(tc.problem, func(t *testing.T) {
			_, err := parseMcmeta([]byte(tc.mcmeta), Data)
			e := wantCode(t, err, CodeInvalidMcmeta)
			detail, _ := e.Params["detail"].(string)
			if e.Params["problem"] != tc.problem || detail != tc.detail {
				t.Errorf("%s:\n got %v: %s\nwant %s: %s", tc.mcmeta, e.Params["problem"], detail, tc.problem, tc.detail)
			}
		})
	}
}

func TestMcmetaMessages(t *testing.T) {
	for _, tc := range []struct{ mcmeta, msg string }{
		{``, "The pack's pack.mcmeta file is not valid JSON: the file is empty."},
		{`{"pack": {"pack_format": 48}}`, "The pack's description in pack.mcmeta is invalid (it is missing), so Minecraft ignores the pack."},
		{`{"pack": {"pack_format": 48, "description": "d"}, "features": {}}`, `The "features" section of the pack's pack.mcmeta file is invalid (it has no "enabled" list), so Minecraft ignores the pack.`},
		{`{"pack": {"pack_format": 48, "description": "d"}, "overlays": {"entries": [1]}}`, `The "overlays" section of the pack's pack.mcmeta file is invalid, so Minecraft ignores the pack: entry 1 is not a JSON object.`},
	} {
		_, err := parseMcmeta([]byte(tc.mcmeta), Data)
		if e := wantCode(t, err, CodeInvalidMcmeta); e.Msg != tc.msg || e.Hint != hintRedownload {
			t.Errorf("%s: message %q, hint %q; want %q", tc.mcmeta, e.Msg, e.Hint, tc.msg)
		}
	}
}

func TestMcmetaSections(t *testing.T) {
	m, err := parseMcmeta([]byte("\uFEFF"+packMcmeta(`"pack_format": 48`)+" and then some text"), Data)
	if err != nil || m.description != "d" || rangeText(m) != "48.0 to 48.0" {
		t.Errorf("with a byte order mark and trailing text: %+v, %v", m, err)
	}

	m, err = parseMcmeta([]byte(`{"pack": {"description": "d", "pack_format": 48}, "features": {"enabled": ["trade_rebalance", ":bundle", "mod:a/b", "minecraft:minecart_improvements"]}}`), Data)
	want := []string{"minecraft:trade_rebalance", "minecraft:bundle", "mod:a/b", "minecraft:minecart_improvements"}
	if err != nil || !slices.Equal(m.features, want) {
		t.Errorf("features = %q, %v; want %q", m.features, err, want)
	}

	for _, tc := range []struct {
		entries string
		want    []string
	}{
		{`[]`, nil},
		{`[{"directory": "ov_new", "min_format": 90, "max_format": 95}, {"directory": "ov.2", "min_format": [85, 1], "max_format": 88}, {"directory": "skipped"}]`, []string{"ov_new", "ov.2"}},
		{`[{"directory": "a", "formats": [50, 70]}, {"directory": "b", "min_format": 71, "max_format": 90, "formats": [71, 81]}]`, []string{"a", "b"}},
		{`[{"directory": "a", "formats": {"min_inclusive": 50, "max_inclusive": 81}}, {"directory": "b", "formats": 60}]`, []string{"a", "b"}},
	} {
		m, err := parseMcmeta([]byte(`{"pack": {"description": "d", "pack_format": 48}, "overlays": {"entries": `+tc.entries+`}}`), Data)
		if err != nil || !slices.Equal(m.overlays, tc.want) {
			t.Errorf("overlays %s = %q, %v; want %q", tc.entries, m.overlays, err, tc.want)
		}
	}
}
