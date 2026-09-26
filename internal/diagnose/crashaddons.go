package diagnose

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	// Fabric Loader's resolution messages (Messages.properties, resolution.*).
	reFabricDep        = regexp.MustCompile(`^\s*- Mod '([^']{1,100})' \(([a-z][a-z0-9_-]{0,63})\) \S{1,100} (requires|is incompatible with) (.{1,120}?) of (?:mod )?(?:'([^']{1,100})' \(([a-z][a-z0-9_-]{0,63})\)|([a-z][a-z0-9_-]{0,63})), (which is missing|which is disabled for this environment[^!]{0,40}|which can't be loaded due to other constraints|but only the wrong versions? (?:is|are) present: ([^!]{1,100})|yet (?:a )?conflicting versions? (?:is|are) present: ([^!]{1,100}))!$`)
	reFabricRemove     = regexp.MustCompile(`^\s*- Remove mod '[^']{1,100}' \(([a-z][a-z0-9_-]{0,63})\) \S{1,100} \((.{1,300})\)\.$`)
	reFabricLoadedFrom = regexp.MustCompile(`^\s*- Mod '[^']{1,100}' \(([a-z][a-z0-9_-]{0,63})\) \S{1,100} is being loaded from (.{1,300})$`)
	reFabricEntrypoint = regexp.MustCompile(`entrypoint (?:stage )?'[a-z_-]{1,32}'(?: due to errors,)? provided by '([a-z][a-z0-9_-]{0,63})'`)

	reFabricMixin  = regexp.MustCompile(`Mixin apply for mod ([a-z][a-z0-9_-]{0,63}) failed`)
	reMixinConfig  = regexp.MustCompile(`Mixin \[\S{1,200}?(?: from mod ([a-z][a-z0-9_-]{0,63}))?\] from phase \[\w{1,20}\] in config \[([^\]\s]{1,200})\] FAILED during \w{1,20}`)
	reMixinGeneric = regexp.MustCompile(`org\.spongepowered\.asm\.mixin\.\S{1,200}(?:Error|Exception)\b`)

	// NeoForge: FancyModLoader's issue messages and ModSorter's log.
	reNeoRequires   = regexp.MustCompile(`^\s*(?:- )?Mod ([a-z][a-z0-9_]{1,63}) (requires|only supports|is incompatible with) ([a-z][a-z0-9_]{1,63}) (.{1,100})$`)
	reNeoCurrently  = regexp.MustCompile(`^\s*Currently, ([a-z][a-z0-9_]{1,63}) is (.{1,100})$`)
	reNeoSorter     = regexp.MustCompile(`^\s*Mod ID: '([a-z][a-z0-9_]{1,63})', Requested by: '([a-z][a-z0-9_]{1,63})', Expected range: '([^']{1,100})', Actual version: '([^']{1,100})'`)
	reNeoBrokenFile = regexp.MustCompile(`^\s*(?:- |Skipping jar\. )?File (.{1,300}?) (is not a valid mod file|is not a jar file|is for .{1,80} and cannot be loaded|is an? .{1,100} and cannot be loaded|is an incompatible version of OptiFine|has an unrecognized FML mod-type.{0,80})$`)
	reNeoSection    = regexp.MustCompile(`Loading (errors|warnings) encountered:$`)
	reNeoMixin      = regexp.MustCompile(`^\s*(?:- )?Mixin application of (\S{1,200}) from (.{1,100}?) \(([a-z][a-z0-9_]{1,63})\) has failed$`)
	// Not anchored, so only lines players can't write are searched: the line
	// starts with the mod's name, which chat could imitate.
	reNeoModFailed = regexp.MustCompile(`\s*(?:- |Issue: )?(.{1,100}?) \(([a-z][a-z0-9_]{1,63})\) (has failed to load correctly|has class loading errors|encountered an error while dispatching the .{1,100} event|encountered an error processing deferred work)$`)

	// Paper and Spigot plugin loading.
	rePluginCouldNotLoad = regexp.MustCompile(`Could not load (?:plugin )?'([^']{1,300})' in (?:folder )?'[^']{1,300}'`)
	rePaperMissingDeps   = regexp.MustCompile(`Unknown/missing dependency plugins: \[([^\]]{1,300})\]\. Please download and install these plugins to run '([^']{1,100})'`)
	reSpigotMissingDep   = regexp.MustCompile(`Unknown dependency ([A-Za-z0-9_.-]{1,64})\. Please download and install`)
	rePluginEnable       = regexp.MustCompile(`^Error occurred (?:\(in the plugin loader\) )?while enabling (.{1,100}?) \(Is it up to date\?\)`)
	rePluginName         = regexp.MustCompile(`^[A-Za-z0-9 _.-]{1,64}$`)

	reJarPath  = regexp.MustCompile(`(?:mods|plugins)/([^/\\\s'"():]{1,200}\.jar)`)
	reCausedBy = regexp.MustCompile(`^\s*Caused by: `)
	reInt      = regexp.MustCompile(`\d{1,4}`)
)

// modLoader explains a mod loader refusing to start: a missing or wrong
// dependency, a file for another loader, or a mod built for a newer Java.
func (c *crashCtx) modLoader() (CrashDiagnosis, bool) {
	if d, ok := c.fabricDependency(); ok {
		return d, true
	}
	if d, ok := c.neoDependency(); ok {
		return d, true
	}
	if d, ok := c.neoBrokenFile(true); ok {
		return d, true
	}
	if !c.pluginServer() {
		return c.addonJava()
	}
	return CrashDiagnosis{}, false
}

func (c *crashCtx) fabricDependency() (CrashDiagnosis, bool) {
	deps := c.all(reFabricDep)
	if len(deps) == 0 {
		return CrashDiagnosis{}, false
	}
	f := deps[0]
	g := f.groups
	name, id, verb, want, outcome := g[1], g[2], g[3], g[4], g[8]
	targetID, targetName := firstNonEmpty(g[6], g[7]), firstNonEmpty(g[5], g[7])
	present := strings.TrimSpace(firstNonEmpty(g[9], g[10]))
	jar := c.modJar(id, name)
	d := CrashDiagnosis{Params: map[string]any{"addon": name, "addon_id": id}, Evidence: c.evidenceOf(f)}
	if jar != "" {
		d.Params["jar"] = jar
	}
	switch {
	case targetID == "java" && verb == "requires":
		have, _ := strconv.Atoi(present)
		if have == 0 {
			have = c.in.JavaVersion
		}
		return c.newerJavaDiagnosis(f, name, jar, firstInt(want), have, true), true
	case verb == "is incompatible with":
		d.Kind = CrashIncompatibleAddon
		d.Params["other"], d.Params["other_id"] = targetName, targetID
		d.Title = fmt.Sprintf("%s doesn't work together with %s", name, targetName)
		d.Explanation = fmt.Sprintf("Fabric refused to start because %s is marked as incompatible with %s, and both are installed. Remove one of them.", name, targetName)
		if other := c.modJar(targetID, targetName); other != "" {
			d.Fixes = append(d.Fixes, removeFix(other, true))
		}
		if jar != "" {
			d.Fixes = append(d.Fixes, removeFix(jar, false))
		}
	case outcome == "which is missing":
		d.Kind = CrashMissingDependency
		d.Params["dependency"] = targetID
		d.Title = fmt.Sprintf("%s needs %s, which isn't installed", name, targetID)
		d.Explanation = fmt.Sprintf("Fabric refused to start because %s requires %s of %s, and it isn't installed.", name, want, targetID)
		d.Fixes = append(d.Fixes, installFix(targetID))
		if jar != "" {
			d.Fixes = append(d.Fixes, removeFix(jar, false))
		}
	case strings.HasPrefix(outcome, "which is disabled"):
		d.Kind = CrashIncompatibleAddon
		d.Params["dependency"] = targetID
		d.Title = fmt.Sprintf("%s needs %s, which doesn't run on servers", name, targetName)
		d.Explanation = fmt.Sprintf("Fabric refused to start because %s requires %s, which only runs in the game itself, not on a server.", name, targetName)
		if jar != "" {
			d.Fixes = append(d.Fixes, removeFix(jar, true))
		}
	case targetID == "minecraft":
		d.Kind = CrashIncompatibleAddon
		d.Params["requires"] = want
		d.Title = fmt.Sprintf("%s is made for a different Minecraft version", name)
		d.Explanation = fmt.Sprintf("Fabric refused to start because %s requires %s of Minecraft, but this server runs %s.", name, want, firstNonEmpty(present, c.in.MCVersion))
		if jar != "" {
			d.Fixes = addonFixes(jar)
		}
	default:
		d.Kind = CrashIncompatibleAddon
		d.Params["dependency"] = targetID
		d.Title = fmt.Sprintf("%s needs a different version of %s", name, targetName)
		d.Explanation = fmt.Sprintf("Fabric refused to start because %s requires %s of %s, which can't be loaded alongside the other mods.", name, want, targetName)
		if present != "" {
			d.Explanation = fmt.Sprintf("Fabric refused to start because %s requires %s of %s, but %s is installed.", name, want, targetName, present)
		}
		if dep := c.modJar(targetID, targetName); dep != "" {
			d.Fixes = append(d.Fixes, updateFix(dep, true))
		}
		if jar != "" {
			d.Fixes = append(d.Fixes, removeFix(jar, false))
		}
	}
	if n := len(deps) - 1; n > 0 {
		d.Params["more"] = n
		d.Explanation += fmt.Sprintf(" Fabric reported %d more %s like this.", n, plural(n, "problem", "problems"))
	}
	return d, true
}

func (c *crashCtx) neoDependency() (CrashDiagnosis, bool) {
	if f, ok := c.firstIn(reNeoRequires, 0, len(c.split)); ok {
		current := ""
		cur, ok := c.firstIn(reNeoCurrently, f.idx+1, f.idx+3)
		if ok && cur.groups[1] == f.groups[3] {
			current = strings.TrimSpace(cur.groups[2])
		} else {
			cur = found{}
		}
		return c.neoDiagnosis(f.groups[1], f.groups[2], f.groups[3], f.groups[4], current, c.evidenceOf(f, cur)), true
	}
	if f, ok := c.firstIn(reNeoSorter, 0, len(c.split)); ok {
		current := f.groups[4]
		if current == "[MISSING]" {
			current = "not installed"
		}
		return c.neoDiagnosis(f.groups[2], "requires", f.groups[1], f.groups[3], current, c.evidenceOf(f)), true
	}
	return CrashDiagnosis{}, false
}

func (c *crashCtx) neoDiagnosis(mod, verb, dep, want, current string, ev []Evidence) CrashDiagnosis {
	jar := c.modJar(mod, "")
	d := CrashDiagnosis{Params: map[string]any{"addon": mod, "dependency": dep}, Evidence: ev}
	if jar != "" {
		d.Params["jar"] = jar
	}
	switch {
	case verb == "is incompatible with":
		d.Kind = CrashIncompatibleAddon
		d.Title = fmt.Sprintf("%s doesn't work together with %s", mod, dep)
		d.Explanation = fmt.Sprintf("NeoForge refused to start because %s is marked as incompatible with %s %s, and both are installed. Remove one of them.", mod, dep, want)
		if other := c.modJar(dep, ""); other != "" {
			d.Fixes = append(d.Fixes, removeFix(other, true))
		}
		if jar != "" {
			d.Fixes = append(d.Fixes, removeFix(jar, false))
		}
	case current == "" || strings.HasPrefix(current, "not installed"):
		d.Kind = CrashMissingDependency
		d.Title = fmt.Sprintf("%s needs %s, which isn't installed", mod, dep)
		d.Explanation = fmt.Sprintf("NeoForge refused to start because %s %s %s %s, and it isn't installed.", mod, verb, dep, want)
		d.Fixes = append(d.Fixes, installFix(dep))
		if jar != "" {
			d.Fixes = append(d.Fixes, removeFix(jar, false))
		}
	case dep == "minecraft" || dep == "neoforge":
		product := "Minecraft"
		if dep == "neoforge" {
			product = "NeoForge"
		}
		d.Kind = CrashIncompatibleAddon
		d.Title = fmt.Sprintf("%s is made for a different %s version", mod, product)
		d.Explanation = fmt.Sprintf("NeoForge refused to start because %s %s %s %s, but this server has %s.", mod, verb, product, want, current)
		if jar != "" {
			d.Fixes = addonFixes(jar)
		}
	default:
		d.Kind = CrashIncompatibleAddon
		d.Title = fmt.Sprintf("%s needs a different version of %s", mod, dep)
		d.Explanation = fmt.Sprintf("NeoForge refused to start because %s %s %s %s, but %s %s is installed.", mod, verb, dep, want, dep, current)
		if other := c.modJar(dep, ""); other != "" {
			d.Fixes = append(d.Fixes, updateFix(other, true))
		}
		if jar != "" {
			d.Fixes = append(d.Fixes, removeFix(jar, false))
		}
	}
	return d
}

// neoBrokenFile explains a file NeoForge can't load. It skips such files in
// the mods folder with a warning and keeps going; only a file listed under
// "Loading errors encountered" stopped the server.
func (c *crashCtx) neoBrokenFile(fatal bool) (CrashDiagnosis, bool) {
	for _, f := range c.all(reNeoBrokenFile) {
		h, ok := c.consoleIn(reNeoSection, c.entryStart(f.idx), f.idx)
		if (ok && h.groups[1] == "errors") != fatal {
			continue
		}
		file := jarName(f.groups[1])
		if file == "" {
			file = truncate(strings.TrimSpace(f.groups[1]), 100)
		}
		reason, why := neoFileReason(f.groups[2])
		d := CrashDiagnosis{
			Kind: CrashIncompatibleAddon, Params: map[string]any{"jar": file, "reason": reason},
			Title:       "NeoForge can't load " + file,
			Explanation: fmt.Sprintf("NeoForge refused to start because it can't load %s: %s.", file, why),
			Evidence:    c.evidenceOf(f),
		}
		if !fatal {
			d.Explanation = fmt.Sprintf("NeoForge can't load %s: %s. NeoForge skips files like this and keeps running, "+
				"so it may not be what stopped the server, but it is the only clear problem in the console.", file, why)
		}
		if jar, ok := c.installed(file); ok {
			d.Params["jar"] = jar
			d.Fixes = []Action{removeFix(jar, true)}
			if reason == "invalid" {
				d.Fixes = addonFixes(jar)
			}
		}
		return d, true
	}
	return CrashDiagnosis{}, false
}

func (c *crashCtx) skippedMod() (CrashDiagnosis, bool) { return c.neoBrokenFile(false) }

// neoFileReason turns FancyModLoader's reason for not loading a file into a
// stable key and plain words.
func neoFileReason(s string) (key, why string) {
	switch {
	case strings.Contains(s, "Fabric"):
		return "fabric", "it is a Fabric mod, not a NeoForge mod"
	case strings.Contains(s, "Quilt"):
		return "quilt", "it is a Quilt mod, not a NeoForge mod"
	case strings.Contains(s, "Bukkit"):
		return "plugin", "it is a plugin for Paper or Spigot servers, not a mod"
	case strings.Contains(s, "Forge"):
		return "forge", "it is made for Forge or an older NeoForge"
	case strings.Contains(s, "LiteLoader"):
		return "liteloader", "it is a LiteLoader mod, not a NeoForge mod"
	case strings.Contains(s, "OptiFine"):
		return "optifine", "it is a version of OptiFine that doesn't work with NeoForge"
	}
	return "invalid", "it isn't a valid mod file, so it may be damaged or only partly downloaded"
}

// addonJava explains a plugin or mod built for a newer Java than the server
// runs, naming its jar from the lines around the error when they do.
func (c *crashCtx) addonJava() (CrashDiagnosis, bool) {
	f, ok := c.find(reClassVersion)
	if !ok {
		return CrashDiagnosis{}, false
	}
	need, have := c.javaVersions(f)
	name, jar := "", ""
	if !f.inReport() {
		start, end := c.entryStart(f.idx), c.stackEnd(f.idx)
		if l, ok := c.consoleIn(rePluginCouldNotLoad, start, f.idx+1); ok {
			jar = l.groups[1]
		} else if l, ok := c.consoleIn(reNeoModFailed, start, f.idx+1); ok {
			name, jar = l.groups[1], c.modJar(l.groups[2], l.groups[1])
		} else if l, ok := c.firstIn(reJarPath, start, end); ok {
			jar = l.groups[1]
		} else if l, ok := c.firstIn(reJarFrame, start, end); ok {
			jar = l.groups[1]
		}
	}
	if jar == "" {
		jar = c.javaAddon(have)
	}
	return c.newerJavaDiagnosis(f, name, jarName(jar), need, have, !c.pluginServer()), true
}

// javaAddon is the one installed jar that needs a newer Java than have, if
// exactly one does.
func (c *crashCtx) javaAddon(have int) string {
	var hits []string
	for _, a := range c.in.Addons {
		if have > 0 && a.JavaVersion > have {
			hits = append(hits, a.File)
		}
	}
	if len(hits) == 1 {
		return hits[0]
	}
	return ""
}

// newerJavaDiagnosis explains an add-on built for a newer Java. fatal is
// false where the server keeps running without it.
func (c *crashCtx) newerJavaDiagnosis(f found, name, jar string, need, have int, fatal bool) CrashDiagnosis {
	file, installed := c.installed(jar)
	if installed {
		jar = file
	}
	d := CrashDiagnosis{
		Kind: CrashNewerJava, Params: map[string]any{"required": need, "available": have},
		Evidence: append(c.evidenceOf(f), javaEvidence(need, have, jar)),
	}
	if name != "" {
		d.Params["addon"] = name
	}
	if jar != "" {
		d.Params["jar"] = jar
	}
	who := firstNonEmpty(name, jar)
	if who == "" {
		d.Title = fmt.Sprintf("A %s needs a newer Java", c.addonWord())
		d.Explanation = fmt.Sprintf("A %s is built for Java %d, but the server runs Java %d, which can't load it. The log doesn't say which %s.", c.addonWord(), need, have, c.addonWord())
	} else {
		d.Title = who + " needs a newer Java"
		d.Explanation = fmt.Sprintf("%s is built for Java %d, but the server runs Java %d, which can't load it.", who, need, have)
	}
	if !fatal {
		d.Explanation += c.survivable()
	}
	if installed {
		d.Fixes = []Action{removeFix(jar, true), {Kind: ActionUpdateAddon, Params: map[string]any{"jar": jar},
			Title: fmt.Sprintf("Replace %s with a version that runs on Java %d", jar, have)}}
	}
	return d
}

func (c *crashCtx) mixin() (CrashDiagnosis, bool) {
	var id, name string
	f, ok := c.find(reNeoMixin)
	switch {
	case ok:
		name, id = f.groups[2], f.groups[3]
	default:
		if f, ok = c.find(reFabricMixin); ok {
			id = f.groups[1]
		} else if f, ok = c.find(reMixinConfig); ok {
			id = firstNonEmpty(f.groups[1], configMod(f.groups[2]))
		} else if f, ok = c.find(reMixinGeneric); !ok {
			return CrashDiagnosis{}, false
		}
	}
	d := CrashDiagnosis{Kind: CrashMixinFailed, Params: map[string]any{}, Evidence: c.evidenceOf(f)}
	if id == "" {
		d.Title = "A mod failed to patch the game"
		d.Explanation = "A mod's changes to Minecraft's code (a mixin) couldn't be applied, so the server stopped. " +
			"That usually means a mod is built for a different Minecraft version, or two mods change the same code. The log doesn't say which mod."
		return d, true
	}
	who := firstNonEmpty(name, id)
	d.Params["addon"] = who
	d.Title = who + " failed to patch the game"
	d.Explanation = fmt.Sprintf("%s's changes to Minecraft's code (a mixin) couldn't be applied, so the server stopped. "+
		"That usually means the mod is built for a different Minecraft version, or another mod changes the same code.", who)
	if jar := c.modJar(id, name); jar != "" {
		d.Params["jar"] = jar
		d.Fixes = addonFixes(jar)
	}
	return d, true
}

// configMod guesses the mod id from a mixin config name such as
// "sodium.mixins.json" or "mixins.examplemod.json".
func configMod(config string) string {
	for _, p := range strings.Split(config, ".") {
		if p != "" && p != "mixins" && p != "mixin" && p != "json" {
			return p
		}
	}
	return ""
}

func (c *crashCtx) modFailed() (CrashDiagnosis, bool) {
	var name, id, loader string
	f, ok := c.firstIn(reNeoModFailed, 0, len(c.split))
	switch {
	case ok:
		name, id, loader = f.groups[1], f.groups[2], "NeoForge"
	default:
		if f, ok = c.find(reFabricEntrypoint); !ok {
			return CrashDiagnosis{}, false
		}
		id, loader = f.groups[1], "Fabric"
	}
	who := firstNonEmpty(name, id)
	d := CrashDiagnosis{
		Kind: CrashAddonFailed, Params: map[string]any{"addon": who},
		Title:       who + " failed to start",
		Explanation: fmt.Sprintf("%s stopped because the mod %s hit an error while starting.", loader, who),
		Evidence:    c.evidenceOf(f),
	}
	if !f.inReport() {
		d.Evidence = append(d.Evidence, c.evidenceOf(c.rootCause(f.idx, c.stackEnd(f.idx)))...)
	}
	if jar := c.modJar(id, name); jar != "" {
		d.Params["jar"] = jar
		d.Fixes = addonFixes(jar)
	}
	return d, true
}

// modJar finds a mod's installed jar from the paths Fabric printed, or else
// by its id or name.
func (c *crashCtx) modJar(id, name string) string {
	for _, re := range []*regexp.Regexp{reFabricRemove, reFabricLoadedFrom} {
		for _, f := range c.all(re) {
			if f.groups[1] == id {
				if jar, ok := c.installed(f.groups[2]); ok {
					return jar
				}
			}
		}
	}
	jar, _ := c.jarForMod(id, name)
	return jar
}

// plugins explains plugins that failed to load or start. Paper keeps running
// without them, so these come last and say so.
func (c *crashCtx) plugins() (CrashDiagnosis, bool) {
	if c.pluginServer() {
		if d, ok := c.addonJava(); ok {
			return d, true
		}
	}
	if d, ok := c.pluginDependency(); ok {
		return d, true
	}
	if d, ok := c.pluginEnable(); ok {
		return d, true
	}
	return c.pluginLoad()
}

// survivable is said about problems the server keeps running with.
func (c *crashCtx) survivable() string {
	return fmt.Sprintf(" %s keeps running without a plugin that fails like this, so it may not be what stopped the server, but it is the only clear problem in the console.", c.typeName())
}

func (c *crashCtx) pluginDependency() (CrashDiagnosis, bool) {
	var deps []string
	plugin := ""
	f, ok := c.console(rePaperMissingDeps)
	if ok {
		for _, d := range strings.Split(f.groups[1], ",") {
			if d = strings.TrimSpace(d); rePluginName.MatchString(d) {
				deps = append(deps, d)
			}
		}
		plugin = f.groups[2]
	} else if f, ok = c.console(reSpigotMissingDep); ok {
		deps = []string{f.groups[1]}
	}
	if !ok || len(deps) == 0 {
		return CrashDiagnosis{}, false
	}
	load, _ := c.consoleIn(rePluginCouldNotLoad, c.entryStart(f.idx), f.idx+1)
	jar, file := "", ""
	if load.groups != nil {
		file = jarName(load.groups[1])
		jar, _ = c.installed(file)
	}
	name := firstNonEmpty(plugin, file, "a plugin")
	d := CrashDiagnosis{
		Kind: CrashMissingDependency, Params: map[string]any{"addon": name, "dependency": deps[0], "dependencies": deps},
		Title:       fmt.Sprintf("%s needs %s, which isn't installed", upperFirst(name), joinAnd(deps)),
		Explanation: fmt.Sprintf("%s couldn't load %s because it needs %s, which isn't installed.", c.typeName(), name, joinAnd(deps)) + c.survivable(),
		Evidence:    c.evidenceOf(load, f),
	}
	for i, dep := range deps {
		fix := installFix(dep)
		fix.Recommended = i == 0
		d.Fixes = append(d.Fixes, fix)
	}
	if jar != "" {
		d.Params["jar"] = jar
		d.Fixes = append(d.Fixes, removeFix(jar, false))
	}
	return d, true
}

func (c *crashCtx) pluginEnable() (CrashDiagnosis, bool) {
	f, ok := c.console(rePluginEnable)
	if !ok {
		return CrashDiagnosis{}, false
	}
	name := f.groups[1]
	if i := strings.LastIndex(name, " v"); i > 0 {
		name = name[:i]
	}
	end := c.stackEnd(f.idx)
	jar, frame := c.pluginJar(name, f.idx+1, end)
	d := CrashDiagnosis{
		Kind: CrashAddonFailed, Params: map[string]any{"addon": name},
		Title:       name + " failed to start",
		Explanation: fmt.Sprintf("%s hit an error while starting, so %s turned it off.", name, c.typeName()) + c.survivable(),
		Evidence:    c.evidenceOf(f, c.rootCause(f.idx, end), frame),
	}
	if jar != "" {
		d.Params["jar"] = jar
		d.Fixes = addonFixes(jar)
	}
	return d, true
}

// pluginJar finds the installed jar of a plugin from its stack trace in
// lines [from, to): a frame from a jar named like the plugin, else the only
// jar in the stack, else an installed jar named like it.
func (c *crashCtx) pluginJar(name string, from, to int) (string, found) {
	var frames []found
	jars := map[string]bool{}
	for i := max(from, 0); i < min(to, len(c.split)); i++ {
		if fr, ok := c.match(reJarFrame, false, i); ok {
			frames = append(frames, fr)
			jars[fr.groups[1]] = true
		}
	}
	if key := alnum(name); len(key) >= 3 {
		for _, fr := range frames {
			if strings.HasPrefix(alnum(fr.groups[1]), key) {
				if jar, ok := c.installed(fr.groups[1]); ok {
					return jar, fr
				}
			}
		}
	}
	if len(jars) == 1 {
		if jar, ok := c.installed(frames[0].groups[1]); ok {
			return jar, frames[0]
		}
	}
	jar, _ := c.jarForMod(name, "")
	return jar, found{}
}

func (c *crashCtx) pluginLoad() (CrashDiagnosis, bool) {
	f, ok := c.console(rePluginCouldNotLoad)
	if !ok {
		return CrashDiagnosis{}, false
	}
	file := jarName(f.groups[1])
	if file == "" {
		file = truncate(f.groups[1], 100)
	}
	d := CrashDiagnosis{
		Kind: CrashAddonFailed, Params: map[string]any{"jar": file},
		Title:       fmt.Sprintf("%s couldn't load %s", c.typeName(), file),
		Explanation: fmt.Sprintf("%s couldn't load the plugin %s.", c.typeName(), file) + c.survivable(),
		Evidence:    c.evidenceOf(f, c.rootCause(f.idx, c.stackEnd(f.idx))),
	}
	if jar, ok := c.installed(file); ok {
		d.Params["jar"] = jar
		d.Fixes = addonFixes(jar)
	}
	return d, true
}

// entryStart is the first line of the log entry line i belongs to: the
// closest line with a log prefix at or before it.
func (c *crashCtx) entryStart(i int) int {
	for j := i; j > 0; j-- {
		if c.split[j].prefixed {
			return j
		}
	}
	return 0
}

// stackEnd is the end of the multi-line message or stack trace that starts
// at line i: the next line with a log prefix.
func (c *crashCtx) stackEnd(i int) int {
	for j := i + 1; j < len(c.split); j++ {
		if c.split[j].prefixed {
			return j
		}
	}
	return len(c.split)
}

// rootCause is the last "Caused by" in the stack trace after line i, or else
// the exception on the line after it.
func (c *crashCtx) rootCause(i, end int) found {
	if f, ok := c.consoleIn(reCausedBy, i+1, end); ok {
		return f
	}
	j := i + 1
	if j >= len(c.split) || c.split[j].prefixed {
		return found{}
	}
	if msg := strings.TrimSpace(c.split[j].msg); msg == "" || strings.HasPrefix(msg, "at ") {
		return found{}
	}
	return found{idx: j, line: c.lines[j]}
}

func installFix(name string) Action {
	return Action{Kind: ActionInstallAddon, Params: map[string]any{"name": name}, Title: "Install " + name, Recommended: true}
}

func firstInt(s string) int {
	n, _ := strconv.Atoi(reInt.FindString(s))
	return n
}

func joinAnd(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
