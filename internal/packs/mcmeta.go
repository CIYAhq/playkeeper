package packs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	// maxMcmetaBytes bounds the pack.mcmeta Inspect reads.
	maxMcmetaBytes = 1 << 20
	// maxDescriptionRunes bounds Info.Description.
	maxDescriptionRunes = 256
	// maxComponentDepth bounds how deeply a description's parts may nest.
	maxComponentDepth = 32
)

// lastPreMinor is the last pack format of each kind before formats gained
// a minor version in Minecraft 1.21.9. Packs that also work in older games
// must declare their range the old way as well.
func lastPreMinor(use Kind) int {
	if use == Resource {
		return 64
	}
	return 81
}

// mcmeta is what Inspect reads from a pack.mcmeta.
type mcmeta struct {
	description   string
	formats       *FormatRange
	formatProblem string
	features      []string
	// overlays are the directories of the pack's overlays: folders beside
	// data/ and assets/ with their own data/ and assets/, which the game
	// layers on top for some versions.
	overlays []string
}

// parseMcmeta reads a pack.mcmeta the way Minecraft 26.3 does for a pack
// used as use. It fails where the game skips the pack. A format
// declaration the game rejects is only reported: the game then falls back
// to reading the description alone and loads the pack anyway.
func parseMcmeta(b []byte, use Kind) (mcmeta, error) {
	var m mcmeta
	b = bytes.TrimPrefix(b, []byte("\uFEFF"))
	// The game reads the first JSON value and ignores anything after it.
	dec := json.NewDecoder(bytes.NewReader(b))
	var root json.RawMessage
	if err := dec.Decode(&root); err != nil {
		return m, badMcmeta("json", jsonDetail(b, err))
	}
	top, ok := members(root)
	if !ok {
		return m, badMcmeta("not_object", "")
	}
	// Top-level sections are looked up directly, so a null section is an
	// error, while null fields inside a section count as missing.
	packRaw, ok := top["pack"]
	if !ok {
		return m, badMcmeta("no_pack", "")
	}
	pack, ok := members(packRaw)
	if !ok {
		return m, badMcmeta("pack_not_object", "")
	}
	descRaw, ok := field(pack, "description")
	if !ok {
		return m, badMcmeta("description", "it is missing")
	}
	var desc strings.Builder
	if err := componentText(descRaw, &desc, 0); err != nil {
		return m, badMcmeta("description", err.Error())
	}
	m.description = plainText(desc.String())

	last := lastPreMinor(use)
	decl, problem := decodeFormats(pack, true, "supported_formats", "Pack")
	if problem == "" {
		var r FormatRange
		if r, problem = decl.validate(last, true, false, "Pack", "supported_formats"); problem == "" {
			m.formats = &r
		}
	}
	m.formatProblem = problem

	if raw, ok := top["features"]; ok {
		features, err := parseFeatures(raw)
		if err != nil {
			return m, badMcmeta("features", err.Error())
		}
		m.features = features
	}
	if raw, ok := top["overlays"]; ok {
		dirs, err := parseOverlays(raw, last)
		if err != nil {
			return m, badMcmeta("overlays", err.Error())
		}
		m.overlays = dirs
	}
	return m, nil
}

// badMcmeta is the error for a pack.mcmeta the game can't use. problem is
// "json", "not_object", "no_pack", "pack_not_object", "description",
// "features", "overlays" or "too_large".
func badMcmeta(problem, detail string) *Error {
	var msg string
	switch problem {
	case "json":
		msg = fmt.Sprintf("The pack's pack.mcmeta file is not valid JSON: %s.", detail)
	case "not_object":
		msg = "The pack's pack.mcmeta file doesn't hold a JSON object, so Minecraft can't read it."
	case "no_pack":
		msg = `The pack's pack.mcmeta file has no "pack" section, so Minecraft ignores the pack.`
	case "pack_not_object":
		msg = `The "pack" section of the pack's pack.mcmeta file is not a JSON object, so Minecraft ignores the pack.`
	case "description":
		msg = fmt.Sprintf("The pack's description in pack.mcmeta is invalid (%s), so Minecraft ignores the pack.", detail)
	case "features":
		msg = fmt.Sprintf(`The "features" section of the pack's pack.mcmeta file is invalid (%s), so Minecraft ignores the pack.`, detail)
	case "overlays":
		msg = sentence(fmt.Sprintf(`The "overlays" section of the pack's pack.mcmeta file is invalid, so Minecraft ignores the pack: %s`, detail))
	case "too_large":
		msg = fmt.Sprintf("The pack's pack.mcmeta file is larger than %s, which no real pack needs.", formatSize(maxMcmetaBytes))
	}
	params := map[string]any{"problem": problem}
	if detail != "" {
		params["detail"] = detail
	}
	return &Error{
		Code:   CodeInvalidMcmeta,
		Params: params,
		Msg:    msg,
		Hint:   "Download the pack again from where you got it, or ask its author for a fixed version.",
	}
}

// jsonDetail describes a JSON syntax error with its position in b.
func jsonDetail(b []byte, err error) string {
	var syn *json.SyntaxError
	switch {
	case errors.Is(err, io.EOF):
		return "the file is empty"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "the file ends in the middle of the JSON"
	case errors.As(err, &syn):
		upto := b[:min(max(syn.Offset, 0), int64(len(b)))]
		line := bytes.Count(upto, []byte("\n")) + 1
		col := len([]rune(string(upto[bytes.LastIndexByte(upto, '\n')+1:])))
		return fmt.Sprintf("%s (line %d, column %d)", syn.Error(), line, col)
	}
	return err.Error()
}

// members decodes a JSON object's members. Later duplicates win, as in the
// game.
func members(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false
	}
	return m, true
}

// field is a member of a section, which the game treats as missing when it
// is null.
func field(m map[string]json.RawMessage, name string) (json.RawMessage, bool) {
	v, ok := m[name]
	if !ok || string(v) == "null" {
		return nil, false
	}
	return v, true
}

func jsonString(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func jsonList(raw json.RawMessage) ([]json.RawMessage, bool) {
	var l []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &l) != nil {
		return nil, false
	}
	return l, true
}

// javaInt reads a JSON number the way the game's integer fields do,
// keeping its integer part. Numbers past 32 bits, which the game wraps
// around, are refused.
func javaInt(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 || raw[0] != '-' && (raw[0] < '0' || raw[0] > '9') {
		return 0, false
	}
	if n, err := strconv.ParseInt(string(raw), 10, 32); err == nil {
		return int(n), true
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil {
		return 0, false
	}
	f = math.Trunc(f)
	if f < math.MinInt32 || f > math.MaxInt32 {
		return 0, false
	}
	return int(f), true
}

// componentText appends the text of a text component to b, and fails
// where the game's text codec would for the parts descriptions use: plain
// text, translations, lists and extra parts. Styling, and the fields of
// scores, selectors, NBT paths and sprites, aren't checked; those parts
// contribute no text.
func componentText(raw json.RawMessage, b *strings.Builder, depth int) error {
	if depth > maxComponentDepth {
		return errors.New("its parts are nested too deeply")
	}
	if s, ok := jsonString(raw); ok {
		b.WriteString(s)
		return nil
	}
	if parts, ok := jsonList(raw); ok {
		if len(parts) == 0 {
			return errors.New("it is an empty list")
		}
		for _, p := range parts {
			if err := componentText(p, b, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	m, ok := members(raw)
	if !ok {
		return fmt.Errorf("%s is not text, a list or an object", snippet(raw))
	}
	if err := contentText(m, b, depth); err != nil {
		return err
	}
	if extra, ok := field(m, "extra"); ok {
		parts, ok := jsonList(extra)
		if !ok || len(parts) == 0 {
			return errors.New(`its "extra" is not a list of parts`)
		}
		for _, p := range parts {
			if err := componentText(p, b, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// contentText appends the text of a text component object's own content.
// Without a "type", the game takes the first kind of content whose field
// the object has.
func contentText(m map[string]json.RawMessage, b *strings.Builder, depth int) error {
	kinds := []string{"text", "translatable", "keybind", "score", "selector", "nbt", "object"}
	if raw, ok := field(m, "type"); ok {
		t, _ := jsonString(raw)
		for _, k := range kinds {
			if k == t {
				if contentOf(k, m, b, depth) {
					return nil
				}
				return fmt.Errorf("a part of type %q lacks its content", t)
			}
		}
		return fmt.Errorf("%s is not a type of text part", snippet(raw))
	}
	for _, k := range kinds {
		if contentOf(k, m, b, depth) {
			return nil
		}
	}
	return errors.New(`a part has none of "text", "translate", "keybind", "score", "selector", "nbt" or "object" in a form the game reads`)
}

// contentOf appends the content of kind k when m has it in a form the game
// reads.
func contentOf(k string, m map[string]json.RawMessage, b *strings.Builder, depth int) bool {
	str := func(name string) (string, bool) {
		raw, ok := field(m, name)
		if !ok {
			return "", false
		}
		return jsonString(raw)
	}
	switch k {
	case "text", "keybind":
		s, ok := str(k)
		b.WriteString(s)
		return ok
	case "translatable":
		key, ok := str("translate")
		if !ok {
			return false
		}
		if raw, has := field(m, "with"); has && !validArgs(raw, depth) {
			return false
		}
		if fallback, has := str("fallback"); has {
			key = fallback
		}
		b.WriteString(key)
		return true
	case "score":
		raw, ok := field(m, "score")
		if ok {
			_, ok = members(raw)
		}
		return ok
	case "selector", "nbt":
		_, ok := str(k)
		return ok
	case "object":
		for _, name := range []string{"object", "sprite", "player"} {
			if _, ok := field(m, name); ok {
				return true
			}
		}
	}
	return false
}

// validArgs reports whether a translation's "with" is a list of numbers,
// booleans, strings and text components, as the game requires.
func validArgs(raw json.RawMessage, depth int) bool {
	args, ok := jsonList(raw)
	if !ok {
		return false
	}
	for _, a := range args {
		if isNumber(a) || string(a) == "true" || string(a) == "false" {
			continue
		}
		if _, isStr := jsonString(a); isStr {
			continue
		}
		if componentText(a, &strings.Builder{}, depth+1) != nil {
			return false
		}
	}
	return true
}

// isNumber reports whether raw is a JSON number.
func isNumber(raw json.RawMessage) bool {
	if len(raw) == 0 || raw[0] != '-' && (raw[0] < '0' || raw[0] > '9') {
		return false
	}
	_, err := strconv.ParseFloat(string(raw), 64)
	return err == nil || errors.Is(err, strconv.ErrRange)
}

// snippet is a short excerpt of a JSON value for a message.
func snippet(raw json.RawMessage) string {
	s := string(raw)
	if r := []rune(s); len(r) > 40 {
		s = string(r[:37]) + "…"
	}
	return s
}

// plainText tidies a description for display: it drops formatting codes
// (§ and the character after it) and private-use characters, which packs
// use for custom glyphs, turns other control characters into spaces, and
// trims each line.
func plainText(s string) string {
	var b strings.Builder
	skip := false
	for _, r := range s {
		switch {
		case skip:
			skip = false
		case r == '§':
			skip = true
		case r == '\n':
			b.WriteByte('\n')
		case unicode.Is(unicode.Co, r):
		case unicode.IsControl(r) || r == '\u2028' || r == '\u2029':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	lines := strings.Split(b.String(), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	out := strings.TrimSpace(strings.Join(lines, "\n"))
	if r := []rune(out); len(r) > maxDescriptionRunes {
		out = strings.TrimSpace(string(r[:maxDescriptionRunes-1])) + "…"
	}
	return out
}

// formatDecl is the format fields of a pack section or an overlay entry.
type formatDecl struct {
	min, max   *Format
	packFormat *int
	// supported is supported_formats in a pack section and formats in an
	// overlay entry: the range older games read.
	supported *[2]int
}

// decodeFormats reads the format fields from m, reporting the first field
// the game can't decode. Overlay entries have no pack_format.
func decodeFormats(m map[string]json.RawMessage, withPackFormat bool, oldName, context string) (formatDecl, string) {
	var d formatDecl
	for _, f := range []struct {
		name     string
		minor    int
		dst      **Format
		expected string
	}{
		{"min_format", 0, &d.min, "a number of at least 0, or a list of 1 to 256 of them"},
		{"max_format", AnyMinor, &d.max, "a number of at least 0, or a list of 1 to 256 of them"},
	} {
		raw, ok := field(m, f.name)
		if !ok {
			continue
		}
		v, ok := decodeFormat(raw, f.minor)
		if !ok {
			return d, fmt.Sprintf("%s has an invalid %s: it must be %s", context, f.name, f.expected)
		}
		*f.dst = &v
	}
	if raw, ok := field(m, "pack_format"); ok && withPackFormat {
		n, ok := javaInt(raw)
		if !ok {
			return d, context + " has an invalid pack_format: it must be a whole number"
		}
		d.packFormat = &n
	}
	if raw, ok := field(m, oldName); ok {
		r, problem := decodeRange(raw)
		if problem != "" {
			return d, fmt.Sprintf("%s has an invalid %s: %s", context, oldName, problem)
		}
		d.supported = &r
	}
	return d, ""
}

// decodeFormat reads min_format or max_format: a format number, or a list
// whose second element is the minor version. minor is the default minor.
func decodeFormat(raw json.RawMessage, minor int) (Format, bool) {
	if n, ok := javaInt(raw); ok {
		return Format{n, minor}, n >= 0
	}
	l, ok := jsonList(raw)
	if !ok || len(l) == 0 || len(l) > 256 {
		return Format{}, false
	}
	nums := make([]int, len(l))
	for i, e := range l {
		if nums[i], ok = javaInt(e); !ok || nums[i] < 0 {
			return Format{}, false
		}
	}
	if len(nums) > 1 {
		minor = nums[1]
	}
	return Format{nums[0], minor}, true
}

// decodeRange reads supported_formats or formats: a number, a list of two
// numbers, or an object with min_inclusive and max_inclusive.
func decodeRange(raw json.RawMessage) ([2]int, string) {
	const expected = "it must be a number, a list of two numbers, or an object with min_inclusive and max_inclusive"
	var lo, hi int
	var ok bool
	if lo, ok = javaInt(raw); ok {
		return [2]int{lo, lo}, ""
	}
	if l, isList := jsonList(raw); isList {
		if len(l) != 2 {
			return [2]int{}, expected
		}
		lo, ok = javaInt(l[0])
		if hi, ok2 := javaInt(l[1]); ok && ok2 {
			return ordered(lo, hi)
		}
		return [2]int{}, expected
	}
	m, isObject := members(raw)
	if !isObject {
		return [2]int{}, expected
	}
	loRaw, ok1 := field(m, "min_inclusive")
	hiRaw, ok2 := field(m, "max_inclusive")
	if !ok1 || !ok2 {
		return [2]int{}, expected
	}
	lo, ok1 = javaInt(loRaw)
	hi, ok2 = javaInt(hiRaw)
	if !ok1 || !ok2 {
		return [2]int{}, expected
	}
	return ordered(lo, hi)
}

func ordered(lo, hi int) ([2]int, string) {
	if lo > hi {
		return [2]int{}, "min_inclusive must be less than or equal to max_inclusive"
	}
	return [2]int{lo, hi}, ""
}

// effectiveMin is the oldest format major the declaration reaches.
func (d formatDecl) effectiveMin() int {
	switch {
	case d.min != nil && d.supported != nil:
		return min(d.min.Major, d.supported[0])
	case d.min != nil:
		return d.min.Major
	case d.supported != nil:
		return d.supported[0]
	}
	return math.MaxInt32
}

// validate checks a declaration as Minecraft 26.3 does and returns its
// range, or the game's message about what is wrong. last is lastPreMinor
// for the pack's use; requireOld makes the old-style range mandatory.
func (d formatDecl) validate(last int, hasPackFormat, requireOld bool, context, oldName string) (FormatRange, string) {
	if (d.min == nil) != (d.max == nil) {
		return FormatRange{}, context + " missing field, must declare both min_format and max_format"
	}
	if requireOld && d.supported == nil {
		return FormatRange{}, fmt.Sprintf("%s missing required field %s, must be present in all overlays for any overlays to work across game versions", context, oldName)
	}
	switch {
	case d.min != nil:
		return d.validateNew(last, hasPackFormat, requireOld, context, oldName)
	case d.supported != nil:
		return d.validateOld(last, hasPackFormat, context)
	case hasPackFormat && d.packFormat != nil:
		pf := *d.packFormat
		if pf > last {
			return FormatRange{}, fmt.Sprintf("%s declares support for version newer than %d, but is missing mandatory fields min_format and max_format", context, last)
		}
		return FormatRange{Format{pf, 0}, Format{pf, 0}}, ""
	}
	return FormatRange{}, context + " could not be parsed, missing format version information"
}

func (d formatDecl) validateNew(last int, hasPackFormat, requireOld bool, context, oldName string) (FormatRange, string) {
	lo, hi := *d.min, *d.max
	if lo.compare(hi) > 0 {
		return FormatRange{}, fmt.Sprintf("%s min_format (%s) is greater than max_format (%s)", context, lo, hi)
	}
	if lo.Major > last && !requireOld {
		if d.supported != nil {
			return FormatRange{}, fmt.Sprintf("%s key %s is deprecated starting from pack format %d. Remove %s from your pack.mcmeta.", context, oldName, last+1, oldName)
		}
		if hasPackFormat && d.packFormat != nil {
			if p := d.checkPackFormat(lo.Major, hi.Major); p != "" {
				return FormatRange{}, p
			}
		}
		return FormatRange{lo, hi}, ""
	}
	if d.supported == nil {
		return FormatRange{}, fmt.Sprintf("%s declares support for format %d, but game versions supporting formats 15 to %d require a %s field. Add \"%s\": [%d, %d] or require a version greater or equal to %d.0.", context, lo.Major, last, oldName, oldName, lo.Major, last, last+1)
	}
	s := *d.supported
	if s[0] != lo.Major {
		return FormatRange{}, fmt.Sprintf("%s version declaration mismatch between %s (from %d) and min_format (%s)", context, oldName, s[0], lo)
	}
	if s[1] != hi.Major && s[1] != last {
		return FormatRange{}, fmt.Sprintf("%s version declaration mismatch between %s (up to %d) and max_format (%s)", context, oldName, s[1], hi)
	}
	if hasPackFormat {
		if d.packFormat == nil {
			return FormatRange{}, fmt.Sprintf("%s declares support for formats up to %d, but game versions supporting formats 15 to %d require a pack_format field. Add \"pack_format\": %d or require a version greater or equal to %d.0.", context, last, last, lo.Major, last+1)
		}
		if p := d.checkPackFormat(lo.Major, hi.Major); p != "" {
			return FormatRange{}, p
		}
	}
	return FormatRange{lo, hi}, ""
}

func (d formatDecl) validateOld(last int, hasPackFormat bool, context string) (FormatRange, string) {
	s := *d.supported
	if s[1] > last {
		return FormatRange{}, fmt.Sprintf("%s declares support for version newer than %d, but is missing mandatory fields min_format and max_format", context, last)
	}
	if hasPackFormat {
		if d.packFormat == nil {
			return FormatRange{}, fmt.Sprintf("%s declares support for formats up to %d, but game versions supporting formats 15 to %d require a pack_format field. Add \"pack_format\": %d or require a version greater or equal to %d.0.", context, last, last, s[0], last+1)
		}
		if p := d.checkPackFormat(s[0], s[1]); p != "" {
			return FormatRange{}, p
		}
	}
	return FormatRange{Format{s[0], 0}, Format{s[1], 0}}, ""
}

func (d formatDecl) checkPackFormat(lo, hi int) string {
	pf := *d.packFormat
	if pf < lo || pf > hi {
		return fmt.Sprintf("Pack declared support for versions %d to %d but declared main format is %d", lo, hi, pf)
	}
	if pf < 15 {
		return "Multi-version packs cannot support minimum version of less than 15, since this will leave versions in range unable to load pack."
	}
	return ""
}

var (
	reOverlayDir = regexp.MustCompile(`^[-_a-zA-Z0-9.]+$`)
	// reFeatureID is a resource location; without a namespace it is in
	// "minecraft".
	reFeatureID = regexp.MustCompile(`^(?:[a-z0-9_.\-]*:)?[a-z0-9_.\-/]+$`)
)

// parseOverlays reads the overlays section and returns the directories of
// the overlays the game uses, or why it refuses the section, and with it
// the pack.
func parseOverlays(raw json.RawMessage, last int) ([]string, error) {
	sec, ok := members(raw)
	if !ok {
		return nil, errors.New("it is not a JSON object")
	}
	entriesRaw, ok := field(sec, "entries")
	if !ok {
		return nil, errors.New(`it has no "entries" list`)
	}
	list, ok := jsonList(entriesRaw)
	if !ok {
		return nil, errors.New(`its "entries" is not a list`)
	}
	type entry struct {
		dir  string
		decl formatDecl
	}
	entries := make([]entry, 0, len(list))
	for i, raw := range list {
		m, ok := members(raw)
		if !ok {
			return nil, fmt.Errorf("entry %d is not a JSON object", i+1)
		}
		dirRaw, ok := field(m, "directory")
		dir, isString := jsonString(dirRaw)
		if !ok || !isString {
			return nil, fmt.Errorf("entry %d has no directory", i+1)
		}
		if !reOverlayDir.MatchString(dir) {
			return nil, fmt.Errorf("%s is not accepted directory name", dir)
		}
		decl, problem := decodeFormats(m, false, "formats", fmt.Sprintf("Overlay %q", dir))
		if problem != "" {
			return nil, errors.New(problem)
		}
		entries = append(entries, entry{dir, decl})
	}
	oldest := math.MaxInt32
	for _, e := range entries {
		oldest = min(oldest, e.decl.effectiveMin())
	}
	var dirs []string
	for _, e := range entries {
		if e.decl.min == nil && e.decl.max == nil && e.decl.supported == nil {
			// The game skips entries without formats.
			continue
		}
		if _, problem := e.decl.validate(last, false, oldest <= last, fmt.Sprintf("Overlay %q", e.dir), "formats"); problem != "" {
			return nil, errors.New(problem)
		}
		dirs = append(dirs, e.dir)
	}
	return dirs, nil
}

// parseFeatures reads the features section: the experimental features the
// pack needs. Which features exist depends on the game version, so unknown
// names, which make the game skip the pack, aren't caught here.
func parseFeatures(raw json.RawMessage) ([]string, error) {
	sec, ok := members(raw)
	if !ok {
		return nil, errors.New("it is not a JSON object")
	}
	enabledRaw, ok := field(sec, "enabled")
	if !ok {
		return nil, errors.New(`it has no "enabled" list`)
	}
	list, ok := jsonList(enabledRaw)
	if !ok {
		return nil, errors.New(`its "enabled" is not a list`)
	}
	out := make([]string, 0, len(list))
	for _, raw := range list {
		id, ok := jsonString(raw)
		if !ok || !reFeatureID.MatchString(id) {
			return nil, fmt.Errorf("%s is not a feature name", snippet(raw))
		}
		ns, path, found := strings.Cut(id, ":")
		if !found {
			ns, path = "minecraft", id
		} else if ns == "" {
			ns = "minecraft"
		}
		out = append(out, ns+":"+path)
	}
	return out, nil
}
