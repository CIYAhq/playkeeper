package diagnose

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const resourceID = `[a-z0-9_.-]{1,64}:[a-z0-9_./-]{1,200}`

var (
	// Minecraft stops the whole server when one entity, or a block's own
	// data and code (a block entity, like a hopper's), throws while it's
	// ticked, and its crash report names it and where it is.
	reTickingReport  = regexp.MustCompile(`^Description: Ticking (block )?entity$`)
	reTickingConsole = regexp.MustCompile(`^net\.minecraft\.ReportedException: Ticking (block )?entity$`)
	reEntityType     = regexp.MustCompile(`^Entity Type: (` + resourceID + `)(?: \(\S{1,300}\))?$`)
	reEntityName     = regexp.MustCompile(`^Entity Name: (.{1,100})$`)
	reEntityExact    = regexp.MustCompile(`^Entity's Exact location: (-?\d{1,9}\.\d{1,3}), (-?\d{1,9}\.\d{1,3}), (-?\d{1,9}\.\d{1,3})$`)
	reEntityBlock    = regexp.MustCompile(`^Entity's Block location: World: \((-?\d{1,9}),(-?\d{1,9}),(-?\d{1,9})\)`)
	reBlockEntityID  = regexp.MustCompile(`^Name: (` + resourceID + `)(?: // \S{1,300})?$`)
	reBlockLocation  = regexp.MustCompile(`^Block location: World: \((-?\d{1,9}),(-?\d{1,9}),(-?\d{1,9})\)`)
	reLevelDimension = regexp.MustCompile(`^Level dimension: (` + resourceID + `)$`)
	reLevelName      = regexp.MustCompile(`^Level name: ([\w.+ -]{1,64})$`)
	reReportError    = regexp.MustCompile(`^(?:[a-z][\w$]*\.)+[A-Z][\w$]*(?:Exception|Error)\b`)
)

// tickingEntity explains an entity, or a block entity, that throws every time
// the server ticks it. The world is saved with it in place, so starting again
// crashes again; removing just that one is the fix, whatever the loader.
func (c *crashCtx) tickingEntity() (CrashDiagnosis, bool) {
	rep, inReport := c.crashReport(reTickingReport)
	con, inConsole := c.console(reTickingConsole)
	if !inReport && !inConsole {
		return CrashDiagnosis{}, false
	}
	block := inReport && rep.groups[1] != "" || !inReport && con.groups[1] != ""
	cause := found{}
	if inConsole {
		cause = c.rootCause(con.idx, c.stackEnd(con.idx))
	}
	if cause.line == "" {
		cause, _ = c.reportError()
	}
	d := CrashDiagnosis{Kind: CrashTickingEntity, Params: map[string]any{"what": "entity"}, Repeats: true, Evidence: c.evidenceOf(rep, con, cause)}
	if block {
		d.Params["what"] = "block_entity"
	}
	t, ok := c.tickedTarget(block)
	if !ok {
		// Without the report, a chunk that couldn't be read just before
		// explains the error better: it's what the entity tripped over.
		if _, damaged := c.console(reChunkError); damaged {
			return CrashDiagnosis{}, false
		}
		d.Title = upperFirst(c.server()) + " crashes on something in its world"
		d.Explanation = c.typeName() + " stopped because something in the world hit an error each time the game ran it, and without its crash report Playkeeper can't tell what or where. " +
			"The world was saved with it, so starting again crashes again."
		c.backupFix(&d, true)
		return d, true
	}
	d.Params["type"], d.Params["dimension"] = t.id, t.dimension
	d.Params["x"], d.Params["y"], d.Params["z"] = t.x, t.y, t.z
	fix := Action{Kind: ActionRemoveEntity, Params: map[string]any{"what": d.Params["what"], "type": t.id, "x": t.x, "y": t.y, "z": t.z, "dimension": t.dimension}, Recommended: true}
	if t.level != "" {
		fix.Params["level"] = t.level
	}
	where := fmt.Sprintf("at x %d, y %d, z %d in %s", t.x, t.y, t.z, dimensionName(t.dimension))
	d.Title = fmt.Sprintf("The %s at %d, %d, %d crashes the server", t.what, t.x, t.y, t.z)
	d.Explanation = fmt.Sprintf("%s stopped because the %s %s hit an error each time the game ran it. "+
		"The world was saved with it in place, so starting again crashes again: at once near spawn, elsewhere as soon as a player comes near. ", c.typeName(), t.what, where)
	if block {
		fix.Title = fmt.Sprintf("Reset the %s at %d, %d, %d", t.what, t.x, t.y, t.z)
		d.Explanation += "Resetting that block's data fixes it: the block stays, and what it held goes."
	} else {
		if t.name != "" {
			d.Params["name"] = t.name
		}
		if t.pos != nil {
			fix.Params["pos"] = t.pos
		}
		fix.Title = fmt.Sprintf("Remove the %s at %d, %d, %d", t.what, t.x, t.y, t.z)
		d.Explanation += fmt.Sprintf("Removing that %s fixes it.", t.what)
	}
	d.Fixes = []Action{fix}
	c.backupFix(&d, false)
	if mod, jar := c.tickingMod(t.id); jar != "" {
		d.Params["addon"], d.Params["jar"] = mod, jar
		d.Explanation += fmt.Sprintf(" It comes from %s, so if more of them fail, update or remove that mod.", mod)
		d.Fixes = append(d.Fixes, updateFix(jar, false), removeFix(jar, false))
	}
	return d, true
}

// ticked is the entity or block entity a crash report names.
type ticked struct {
	id, what, name, dimension string
	level                     string // the world's name, which on Paper is its folder
	x, y, z                   int
	pos                       []float64 // an entity's exact position
}

// tickedTarget reads what was ticked from the crash report's section about
// it, and its dimension from the section about the level.
func (c *crashCtx) tickedTarget(block bool) (ticked, bool) {
	var t ticked
	if block {
		sec := c.reportSection("Block entity being ticked")
		id, ok1 := firstMatch(sec, reBlockEntityID)
		at, ok2 := firstMatch(sec, reBlockLocation)
		if !ok1 || !ok2 {
			return t, false
		}
		t.id = id[1]
		t.x, t.y, t.z = atoi(at[1]), atoi(at[2]), atoi(at[3])
	} else {
		sec := c.reportSection("Entity being ticked")
		id, ok1 := firstMatch(sec, reEntityType)
		at, ok2 := firstMatch(sec, reEntityBlock)
		if !ok1 || !ok2 {
			return t, false
		}
		t.id = id[1]
		t.x, t.y, t.z = atoi(at[1]), atoi(at[2]), atoi(at[3])
		if n, ok := firstMatch(sec, reEntityName); ok {
			t.name = strings.TrimSpace(n[1])
		}
		if p, ok := firstMatch(sec, reEntityExact); ok {
			for _, s := range p[1:4] {
				v, _ := strconv.ParseFloat(s, 64)
				t.pos = append(t.pos, v)
			}
		}
	}
	t.dimension = "minecraft:overworld"
	level := c.reportSection("Affected level")
	if dim, ok := firstMatch(level, reLevelDimension); ok {
		t.dimension = dim[1]
	}
	if n, ok := firstMatch(level, reLevelName); ok {
		t.level = n[1]
	}
	_, path, _ := strings.Cut(t.id, ":")
	t.what = strings.ReplaceAll(path[strings.LastIndex(path, "/")+1:], "_", " ")
	return t, true
}

// tickingMod is the installed mod behind what was ticked: the mod its id
// names, else the first frame of the error's stack from an installed mod.
func (c *crashCtx) tickingMod(id string) (mod, jar string) {
	if ns, _, _ := strings.Cut(id, ":"); ns != "minecraft" {
		if jar := c.modJar(ns, ""); jar != "" {
			return ns, jar
		}
	}
	e, ok := c.reportError()
	if !ok {
		return "", ""
	}
	start := -1
	for i, l := range c.report {
		if strings.TrimSpace(l) == e.line {
			start = i + 1
			break
		}
	}
	for i := max(start, 0); start >= 0 && i < len(c.report); i++ {
		l := strings.TrimSpace(c.report[i])
		if l == "" {
			break
		}
		if m := reModFrame.FindStringSubmatch(l); m != nil && !loaderModules[m[1]] {
			if jar := c.modJar(m[1], ""); jar != "" {
				return m[1], jar
			}
		}
		for _, m := range reForgeFrameJar.FindAllStringSubmatch(l, -1) {
			if jar, ok := c.installed(m[1]); ok {
				return jar, jar
			}
		}
	}
	return "", ""
}

// reportError is the error a crash report is about: the first line after
// its Description that names an exception.
func (c *crashCtx) reportError() (found, bool) {
	past := false
	for _, l := range c.report {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "Description: ") {
			past = true
			continue
		}
		if past && reReportError.MatchString(l) {
			return found{idx: -1, line: l}, true
		}
	}
	return found{}, false
}

// reportSection is the lines of a crash report's section, headed
// "-- title --", up to its stack trace or the next section.
func (c *crashCtx) reportSection(title string) []string {
	var out []string
	in := false
	for _, l := range c.report {
		l = strings.TrimSpace(l)
		switch {
		case l == "-- "+title+" --":
			in = true
		case !in:
		case strings.HasPrefix(l, "-- ") || l == "Stacktrace:":
			return out
		default:
			out = append(out, l)
		}
	}
	return out
}

func firstMatch(lines []string, re *regexp.Regexp) ([]string, bool) {
	for _, l := range lines {
		if m := re.FindStringSubmatch(l); m != nil {
			return m, true
		}
	}
	return nil, false
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// dimensionName names a dimension in a sentence.
func dimensionName(id string) string {
	switch id {
	case "minecraft:overworld":
		return "the Overworld"
	case "minecraft:the_nether":
		return "the Nether"
	case "minecraft:the_end":
		return "the End"
	}
	return "the dimension " + id
}
