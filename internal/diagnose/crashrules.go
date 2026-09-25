package diagnose

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// crashRules run in order and the first match explains the stop. Causes that
// stop a server for certain come first; weaker evidence, and problems a
// server keeps running with (a plugin that failed to load), come last.
var crashRules = []struct {
	explain func(*crashCtx) (CrashDiagnosis, bool)
	certain bool
}{
	{(*crashCtx).dockerPort, true},
	{(*crashCtx).containerMemory, true},
	{(*crashCtx).diskFull, true},
	{(*crashCtx).eula, true},
	{(*crashCtx).javaMemory, true},
	{(*crashCtx).serverJava, true},
	{(*crashCtx).worldLocked, true},
	{(*crashCtx).levelData, true},
	{(*crashCtx).portBind, true},
	{(*crashCtx).modLoader, true},
	{(*crashCtx).mixin, true},
	{(*crashCtx).modFailed, true},
	{(*crashCtx).watchdog, true},
	{(*crashCtx).datapack, true},
	{(*crashCtx).killed, true},
	{(*crashCtx).permission, false},
	{(*crashCtx).lowDisk, false},
	{(*crashCtx).chunks, false},
	{(*crashCtx).plugins, false},
	{(*crashCtx).skippedMod, false},
}

var (
	reDockerPort = regexp.MustCompile(`Bind for \S*:(\d{1,5}) failed: port is already allocated|listen (?:tcp|udp)[46]? \S*:(\d{1,5}): bind: address already in use`)

	reNoSpace = regexp.MustCompile(`No space left on device`)
	reEULA    = regexp.MustCompile(`^You need to agree to the EULA in order to run the server`)

	reHeapOOM      = regexp.MustCompile(`java\.lang\.OutOfMemoryError: (?:Java heap space|GC overhead limit exceeded)`)
	reMetaspaceOOM = regexp.MustCompile(`java\.lang\.OutOfMemoryError: (?:Metaspace|Compressed class space)`)
	reThreadOOM    = regexp.MustCompile(`java\.lang\.OutOfMemoryError: unable to create (?:new )?native thread`)

	reClassVersion = regexp.MustCompile(`(\S{1,300}) has been compiled by a more recent version of the Java Runtime \(class file version (\d{2,3})(?:\.\d{1,5})?\), this version of the Java Runtime only recognizes class file versions up to (\d{2,3})`)
	reMainClass    = regexp.MustCompile(`^Error: LinkageError occurred while loading main class `)
	reServerClass  = regexp.MustCompile(`^(?:net/minecraft|com/mojang|io/papermc|org/bukkit|org/spigotmc|net/fabricmc|net/neoforged|cpw/mods)/`)

	reSessionLock = regexp.MustCompile(`session\.lock: already locked|The save is being accessed from another location`)
	reLevelDat    = regexp.MustCompile(`Failed to load world data from \S{1,300} and \S{1,300}\. World files may be corrupted`)

	reBindFailed = regexp.MustCompile(`FAILED TO BIND TO PORT`)
	reBindReason = regexp.MustCompile(`^The exception was: (.{1,300})$`)

	rePaperWatchdog   = regexp.MustCompile(`^The server has stopped responding! This is \(probably\) not a \w{1,20} bug\.`)
	reServerDump      = regexp.MustCompile(`^Server thread dump \(Look for plugins here`)
	reEntireDump      = regexp.MustCompile(`^Entire Thread Dump:`)
	reJarFrame        = regexp.MustCompile(`(?:^|[\s(])([A-Za-z0-9][A-Za-z0-9_.+-]{0,120}\.jar)//`)
	reVanillaWatchdog = regexp.MustCompile(`^A single server tick took (\d{1,6}(?:\.\d{1,2})?) seconds \(should be max`)
	reReportWatchdog  = regexp.MustCompile(`ServerHangWatchdog detected that a single server tick took (\d{1,6}(?:\.\d{1,2})?) seconds`)

	reDatapackFailed = regexp.MustCompile(`Failed to load datapacks, can't proceed with server load|Failed to load registries due to above errors`)
	reDatapackFile   = regexp.MustCompile(`from pack file/(.{1,200})$`)

	rePermission = regexp.MustCompile(`AccessDeniedException|\(Permission denied\)|: Permission denied$`)
	reDeniedPath = regexp.MustCompile(`AccessDeniedException: (\S{1,300})|(\S{1,300}) \(Permission denied\)|'([^']{1,300})': Permission denied$`)

	reChunkError = regexp.MustCompile(`Couldn't load chunk|Failed to read chunk|Chunk \[-?\d{1,7}, ?-?\d{1,7}\] (?:header|stream) is truncated|Region file \S{1,300} has truncated header`)
	reChunkPos   = regexp.MustCompile(`\[(-?\d{1,7}), ?(-?\d{1,7})\]`)
)

func (c *crashCtx) dockerPort() (CrashDiagnosis, bool) {
	m := reDockerPort.FindStringSubmatch(c.in.DockerError)
	if m == nil {
		return CrashDiagnosis{}, false
	}
	port, _ := strconv.Atoi(m[1] + m[2])
	msg := truncate(redact(strings.Join(strings.Fields(c.in.DockerError), " ")), maxEvidenceLen)
	return CrashDiagnosis{
		Kind: CrashPortInUse, Params: map[string]any{"port": port},
		Title:       fmt.Sprintf("Port %d is already in use", port),
		Explanation: fmt.Sprintf("Docker couldn't start %s because another program on this machine already uses port %d.", c.server(), port),
		Evidence:    []Evidence{{Kind: EvidenceDockerError, Params: map[string]any{"message": msg}, Text: msg}},
		Fixes: []Action{
			{Kind: ActionChangePort, Params: map[string]any{"port": port}, Title: "Give this server a different port", Recommended: true},
			{Kind: ActionRestart, Title: "Start it again once the other program has stopped"},
		},
	}, true
}

func (c *crashCtx) containerMemory() (CrashDiagnosis, bool) {
	if !c.in.OOMKilled {
		return CrashDiagnosis{}, false
	}
	budget, heap := c.in.BudgetMB, c.in.HeapMB
	d := CrashDiagnosis{
		Kind: CrashContainerMemory, Params: map[string]any{"budget_mb": budget, "heap_mb": heap},
		Title:       upperFirst(c.server()) + " ran out of memory",
		Explanation: "Docker stopped the server because it used all the memory it is allowed.",
		Evidence: []Evidence{{Kind: EvidenceOOMKilled, Params: map[string]any{"limit_mb": budget},
			Text: "Docker reports that the server was killed for going over its memory limit."}},
	}
	if budget > 0 && heap > 0 && heap < budget {
		d.Explanation = fmt.Sprintf("Docker stopped the server because it reached its memory limit of %s. Java sets aside %s of that for the game (the heap) when it starts, "+
			"so the rest of Java's memory, for loaded code, threads and network buffers, needed more than the remaining %s. Some plugins and mods use a lot of that kind of memory.",
			sizeText(budget), sizeText(heap), sizeText(budget-heap))
	}
	d.Evidence = append(d.Evidence, c.roomEvidence()...)
	d.Fixes = c.memoryFixes(false)
	return d, true
}

// memoryFixes offers more memory when the machine has room for it, what to
// do instead when it has not, and starting again as it is. Fewer chunks only
// save heap, so a lower view distance is offered for heap problems alone.
func (c *crashCtx) memoryFixes(heap bool) []Action {
	if c.in.HostMB <= 0 || c.in.BudgetMB <= 0 {
		return []Action{restartFix()}
	}
	view := 0
	if heap {
		view = c.in.ViewDistance
	}
	return append(memoryFixes(c.in.BudgetMB, c.in.HostMB, c.in.RoomMB, view), restartFix())
}

func (c *crashCtx) roomEvidence() []Evidence {
	return roomEvidence(c.in.BudgetMB, c.in.HostMB, c.in.RoomMB)
}

// roomEvidence says how much more memory the machine could give the server;
// nothing when the machine's memory is unknown.
func roomEvidence(budgetMB, hostMB, roomMB int) []Evidence {
	if hostMB <= 0 {
		return nil
	}
	p := map[string]any{"budget_mb": budgetMB, "room_mb": max(roomMB, 0)}
	if roomMB <= 0 {
		return []Evidence{{Kind: EvidenceMemoryRoom, Params: p, Text: "This machine has no memory to spare for a bigger budget."}}
	}
	return []Evidence{{Kind: EvidenceMemoryRoom, Params: p, Text: fmt.Sprintf("This machine could give the server up to %s more.", sizeText(roomMB))}}
}

// diskFull needs the server to have said so; lowDisk covers a nearly full
// disk without that line.
func (c *crashCtx) diskFull() (CrashDiagnosis, bool) {
	f, ok := c.find(reNoSpace)
	if !ok {
		return CrashDiagnosis{}, false
	}
	return c.disk(f), true
}

func (c *crashCtx) lowDisk() (CrashDiagnosis, bool) {
	if c.in.FreeDiskMB == nil || *c.in.FreeDiskMB >= lowDiskMB {
		return CrashDiagnosis{}, false
	}
	return c.disk(found{}), true
}

func (c *crashCtx) disk(f found) CrashDiagnosis {
	d := CrashDiagnosis{Kind: CrashDiskFull, Params: map[string]any{}, Title: "The disk is full", Evidence: c.evidenceOf(f)}
	freeUp := Action{Kind: ActionFreeDisk, Title: "Free up disk space, for example by deleting old backups or worlds you no longer need", Recommended: true}
	free := c.in.FreeDiskMB
	if free == nil {
		d.Explanation = "The server couldn't write to the disk because it was full. Minecraft needs free space to save the world and write its logs."
		d.Fixes = []Action{freeUp}
		return d
	}
	mb := int(max(*free, 0))
	d.Params["free_mb"] = mb
	freeUp.Params = map[string]any{"free_mb": mb}
	d.Evidence = append(d.Evidence, Evidence{Kind: EvidenceFreeDisk, Params: map[string]any{"free_mb": mb},
		Text: fmt.Sprintf("%s free on the server's disk.", sizeText(mb))})
	switch {
	case f.line != "" && mb >= 1024:
		d.Explanation = fmt.Sprintf("The server couldn't write to the disk because it was full at the time. There is %s free now, so it should be able to start again.", sizeText(mb))
		freeUp.Recommended = false
		d.Fixes = []Action{{Kind: ActionRestart, Title: "Start the server again", Recommended: true}, freeUp}
	case f.line != "":
		d.Explanation = fmt.Sprintf("The server couldn't write to the disk because it is full: only %s is free. Minecraft needs free space to save the world and write its logs.", sizeText(mb))
		d.Fixes = []Action{freeUp}
	default:
		d.Explanation = fmt.Sprintf("Only %s is free on the server's disk. Minecraft needs free space to save the world and write its logs, so this is the most likely reason it stopped.", sizeText(mb))
		d.Fixes = []Action{freeUp}
	}
	return d
}

func (c *crashCtx) eula() (CrashDiagnosis, bool) {
	f, ok := c.console(reEULA)
	if !ok {
		return CrashDiagnosis{}, false
	}
	return CrashDiagnosis{
		Kind: CrashEULA, Title: "The Minecraft EULA hasn't been accepted",
		Explanation: "Minecraft only starts once its end-user licence agreement (EULA) is accepted, and the server found it wasn't.",
		Evidence:    c.evidenceOf(f),
		Fixes:       []Action{{Kind: ActionAcceptEULA, Title: "Read and accept the Minecraft EULA", Recommended: true}},
	}, true
}

func (c *crashCtx) javaMemory() (CrashDiagnosis, bool) {
	if f, ok := c.find(reHeapOOM); ok {
		d := CrashDiagnosis{
			Kind: CrashHeapMemory, Params: map[string]any{"budget_mb": c.in.BudgetMB, "heap_mb": c.in.HeapMB},
			Title:       upperFirst(c.server()) + " ran out of memory",
			Explanation: "Java ran out of the memory it has for the game (the heap) and couldn't continue.",
			Evidence:    c.evidenceOf(f),
		}
		if c.in.HeapMB > 0 {
			d.Explanation = fmt.Sprintf("Java ran out of the %s it has for the game (the heap) and couldn't continue.", sizeText(c.in.HeapMB))
		}
		if c.in.ServerType == "vanilla" {
			d.Explanation += " Players and loaded chunks take up most of it."
		} else {
			w := c.addonWord()
			d.Explanation += fmt.Sprintf(" Players, loaded chunks and %ss all take up heap. If it keeps happening with more memory, a %s may be holding on to memory it no longer needs.", w, w)
		}
		d.Evidence = append(d.Evidence, c.roomEvidence()...)
		d.Fixes = c.memoryFixes(true)
		return d, true
	}
	if f, ok := c.find(reMetaspaceOOM); ok {
		d := CrashDiagnosis{
			Kind: CrashMetaspace, Title: upperFirst(c.server()) + " ran out of memory for code",
			Explanation: "Java ran out of room for program code (metaspace), which is separate from the memory for the game.",
			Evidence:    c.evidenceOf(f), Fixes: []Action{restartFix()},
		}
		switch {
		case c.pluginServer():
			d.Explanation += " Reloading plugins without restarting makes it grow, because the old copies stay loaded; a fresh start clears it."
		case c.in.ServerType != "vanilla":
			d.Explanation += " Very large mod packs can fill it."
		}
		return d, true
	}
	if f, ok := c.find(reThreadOOM); ok {
		d := CrashDiagnosis{
			Kind: CrashThreads, Title: upperFirst(c.server()) + " hit its thread limit",
			Explanation: "Java couldn't start another thread because the server reached the number of threads its container allows.",
			Evidence:    c.evidenceOf(f), Fixes: []Action{restartFix()},
		}
		if c.in.ServerType != "vanilla" {
			d.Explanation += fmt.Sprintf(" A %s that keeps starting new threads is the usual reason.", c.addonWord())
		}
		return d, true
	}
	return CrashDiagnosis{}, false
}

// serverJava catches the server software itself needing a newer Java than
// the image has; add-ons built for a newer Java are addonJava.
func (c *crashCtx) serverJava() (CrashDiagnosis, bool) {
	f, ok := c.find(reClassVersion)
	if !ok {
		return CrashDiagnosis{}, false
	}
	if _, main := c.console(reMainClass); !main && !reServerClass.MatchString(f.groups[1]) {
		return CrashDiagnosis{}, false
	}
	need, have := c.javaVersions(f)
	return CrashDiagnosis{
		Kind: CrashNewerJava, Params: map[string]any{"required": need, "available": have},
		Title:       fmt.Sprintf("%s needs Java %d", upperFirst(c.server()), need),
		Explanation: fmt.Sprintf("The %s server software is built for Java %d, but it ran on Java %d, which can't load it.", c.typeName(), need, have),
		Evidence:    append(c.evidenceOf(f), javaEvidence(need, have, "")),
	}, true
}

// javaVersions reads the Java versions from a class file version error:
// class file version 65 is Java 21.
func (c *crashCtx) javaVersions(f found) (need, have int) {
	class, _ := strconv.Atoi(f.groups[2])
	upTo, _ := strconv.Atoi(f.groups[3])
	return class - 44, upTo - 44
}

func javaEvidence(need, have int, jar string) Evidence {
	p := map[string]any{"required": need, "available": have}
	text := fmt.Sprintf("Needs Java %d; the server runs Java %d.", need, have)
	if jar != "" {
		p["jar"] = jar
		text = fmt.Sprintf("%s needs Java %d; the server runs Java %d.", jar, need, have)
	}
	return Evidence{Kind: EvidenceJavaVersion, Params: p, Text: text}
}

func (c *crashCtx) worldLocked() (CrashDiagnosis, bool) {
	f, ok := c.find(reSessionLock)
	if !ok {
		return CrashDiagnosis{}, false
	}
	return CrashDiagnosis{
		Kind: CrashWorldLocked, Title: "The world is already open somewhere else",
		Explanation: "The server couldn't open the world because something else still had it open, such as another copy of the server or a program copying the world. " +
			"Minecraft locks the world folder (session.lock) so that two programs can't write to it at once.",
		Evidence: c.evidenceOf(f),
		Fixes:    []Action{{Kind: ActionRestart, Title: "Start it again once the other copy has stopped", Recommended: true}},
	}, true
}

func (c *crashCtx) levelData() (CrashDiagnosis, bool) {
	f, ok := c.find(reLevelDat)
	if !ok {
		return CrashDiagnosis{}, false
	}
	d := CrashDiagnosis{
		Kind: CrashCorruptWorld, Params: map[string]any{"file": "level.dat"},
		Title: "The world's level.dat file is damaged",
		Explanation: "Minecraft couldn't read level.dat or its spare copy level.dat_old. These files hold the world's settings, " +
			"so it stopped rather than risk the world.",
		Evidence: c.evidenceOf(f),
	}
	c.backupFix(&d, true)
	return d, true
}

// backupFix offers restoring a backup, or says there is none.
func (c *crashCtx) backupFix(d *CrashDiagnosis, recommended bool) {
	if !c.in.HasBackup {
		d.Explanation += " There is no backup to restore."
		return
	}
	d.Fixes = append(d.Fixes, Action{Kind: ActionRestoreBackup, Title: "Restore the latest backup of the world", Recommended: recommended})
}

func (c *crashCtx) portBind() (CrashDiagnosis, bool) {
	f, ok := c.console(reBindFailed)
	if !ok {
		return CrashDiagnosis{}, false
	}
	d := CrashDiagnosis{Kind: CrashPortInUse, Params: map[string]any{"port": c.in.Port}, Title: "The server couldn't open its port"}
	reason, _ := c.near(f, reBindReason, 3)
	d.Evidence = c.evidenceOf(f, reason)
	switch r := reason.line; {
	case strings.Contains(r, "Address already in use"):
		d.Params["reason"] = "in_use"
		d.Explanation = "Something inside the server's container was already using its port, so it couldn't accept players and stopped. " +
			"That happens when a plugin, a mod or RCON is set to use the same port as the game."
		d.Fixes = []Action{restartFix()}
	case strings.Contains(r, "Cannot assign requested address"):
		d.Params["reason"] = "address"
		d.Explanation = "The server is set to listen on an address this machine doesn't have (server-ip in server.properties), so it couldn't accept players and stopped."
	default:
		d.Explanation = "The server couldn't start listening for players on its port, so it stopped."
		d.Fixes = []Action{restartFix()}
	}
	return d, true
}

func (c *crashCtx) watchdog() (CrashDiagnosis, bool) {
	if f, ok := c.console(rePaperWatchdog); ok {
		return c.paperWatchdog(f), true
	}
	f, ok := c.console(reVanillaWatchdog)
	if !ok {
		if f, ok = c.crashReport(reReportWatchdog); !ok {
			return CrashDiagnosis{}, false
		}
	}
	secs, _ := strconv.ParseFloat(f.groups[1], 64)
	return CrashDiagnosis{
		Kind: CrashWatchdog, Params: map[string]any{"seconds": secs},
		Title: upperFirst(c.server()) + " froze",
		Explanation: fmt.Sprintf("Minecraft's watchdog stopped the server because a single tick took %s seconds. "+
			"It gives up once one tick takes longer than max-tick-time in server.properties, a minute by default. Playkeeper couldn't tell from the log what kept it busy.", trimZero(secs)),
		Evidence: c.evidenceOf(f),
		Fixes:    []Action{restartFix()},
	}, true
}

// paperWatchdog names the plugin whose code the server thread was running
// when Paper gave up on it: the first plugin frame of the server thread's
// stack, before the dump of every other thread.
func (c *crashCtx) paperWatchdog(f found) CrashDiagnosis {
	start, end := f.idx, len(c.split)
	if s, ok := c.firstIn(reServerDump, f.idx, end); ok {
		start = s.idx
	}
	if e, ok := c.firstIn(reEntireDump, start, end); ok {
		end = e.idx
	}
	d := CrashDiagnosis{
		Kind: CrashWatchdog, Params: map[string]any{},
		Title:       upperFirst(c.server()) + " froze",
		Explanation: c.typeName() + "'s watchdog stopped the server because the game froze: one tick didn't finish for a long time (a minute by default).",
		Evidence:    c.evidenceOf(f),
	}
	frame, ok := c.firstIn(reJarFrame, start, end)
	if !ok {
		d.Explanation += " Its report doesn't show a plugin at work at that moment, which points at the game itself, for example a big world edit or save, or too many entities or too much redstone in one place."
		d.Fixes = []Action{restartFix(), {Kind: ActionRunProfiler, Title: "Run a profiler to see what takes the time"}}
		return d
	}
	jar := frame.groups[1]
	d.Params["jar"] = jar
	d.Evidence = append(d.Evidence, c.evidence(frame))
	d.Explanation += fmt.Sprintf(" At that moment the server was running code from %s, so that plugin is the most likely cause.", jar)
	d.Fixes = []Action{restartFix()}
	if file, ok := c.installed(jar); ok {
		d.Params["jar"] = file
		d.Fixes = append(addonFixes(file), restartFix())
	}
	return d
}

func (c *crashCtx) datapack() (CrashDiagnosis, bool) {
	f, ok := c.find(reDatapackFailed)
	if !ok {
		return CrashDiagnosis{}, false
	}
	source := "a data pack"
	if !c.pluginServer() && c.in.ServerType != "vanilla" {
		source = "a data pack or mod"
	}
	d := CrashDiagnosis{
		Kind: CrashDatapack, Params: map[string]any{},
		Title:       "A data pack stopped the world from loading",
		Explanation: fmt.Sprintf("Minecraft refused to load the world because some of its data, from %s, has errors.", source),
		Evidence:    c.evidenceOf(f),
	}
	if p, ok := c.find(reDatapackFile); ok {
		if name := strings.TrimSpace(p.groups[1]); validPackName(name) {
			d.Params["pack"] = name
			d.Explanation += fmt.Sprintf(" The errors are in the data pack %s.", name)
			d.Evidence = append(d.Evidence, c.evidence(p))
			d.Fixes = []Action{{Kind: ActionRemoveDatapack, Params: map[string]any{"pack": name}, Title: "Remove the data pack " + name, Recommended: true}}
			return d, true
		}
	}
	d.Explanation += " Remove or update the data pack that was added or changed most recently."
	return d, true
}

// validPackName accepts a data pack's file or folder name, never a path.
func validPackName(s string) bool {
	return s != "" && len(s) <= 200 && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00\r\n\t")
}

func (c *crashCtx) killed() (CrashDiagnosis, bool) {
	if c.in.ExitCode != 137 || c.in.OOMKilled {
		return CrashDiagnosis{}, false
	}
	return CrashDiagnosis{
		Kind: CrashKilled, Title: upperFirst(c.server()) + " was stopped abruptly",
		Explanation: "Something outside the server killed it: exit code 137 means it received SIGKILL, and Docker doesn't report it going over its own memory limit. " +
			"That happens when the whole machine runs out of memory, or when a person or program force-stops the container.",
		Fixes: []Action{restartFix()},
	}, true
}

func (c *crashCtx) permission() (CrashDiagnosis, bool) {
	f, ok := c.find(rePermission)
	if !ok {
		return CrashDiagnosis{}, false
	}
	d := CrashDiagnosis{
		Kind: CrashPermissionDenied, Params: map[string]any{},
		Title: "The server isn't allowed to use its own files", Evidence: c.evidenceOf(f),
		Fixes: []Action{{Kind: ActionFixPermissions, Title: "Give the server back ownership of its files", Recommended: true}},
	}
	what := "some of its files"
	if m := reDeniedPath.FindStringSubmatch(f.line); m != nil {
		if p := firstNonEmpty(m[1:]...); p != "" {
			p = truncate(redact(p), 120)
			d.Params["path"] = p
			what = p
		}
	}
	d.Certain = c.fatal(f)
	if d.Certain {
		d.Explanation = fmt.Sprintf("The server stopped because it wasn't allowed to read or write %s, which happens when files were copied in or edited as a different user.", what)
		return d, true
	}
	d.Explanation = fmt.Sprintf("The server wasn't allowed to read or write %s, which happens when files were copied in or edited as a different user. "+
		"That is the clearest problem in the console, so it is the most likely reason it stopped.", what)
	return d, true
}

var reFatal = regexp.MustCompile(`^(?:Failed to start the minecraft server|Encountered an unexpected exception)$`)

// fatal reports whether a console line belongs to the error the server
// stopped with: the entry Minecraft logs when starting or running fails.
func (c *crashCtx) fatal(f found) bool {
	if f.inReport() {
		return false
	}
	s := c.entryStart(f.idx)
	return c.split[s].prefixed && reFatal.MatchString(c.split[s].msg)
}

func (c *crashCtx) chunks() (CrashDiagnosis, bool) {
	f, ok := c.find(reChunkError)
	if !ok {
		return CrashDiagnosis{}, false
	}
	d := CrashDiagnosis{Kind: CrashCorruptWorld, Params: map[string]any{}, Title: "Part of the world may be damaged", Evidence: c.evidenceOf(f)}
	where := "part of the world"
	if m := reChunkPos.FindStringSubmatch(f.line); m != nil {
		x, _ := strconv.Atoi(m[1])
		z, _ := strconv.Atoi(m[2])
		d.Params["chunk_x"], d.Params["chunk_z"] = x, z
		where = fmt.Sprintf("the chunk around x %d, z %d", x*16, z*16)
	}
	d.Explanation = fmt.Sprintf("Before it stopped, the server couldn't read %s, so that part of the world file is probably damaged. "+
		"That is the clearest problem in the console, so it is the most likely reason it stopped. "+
		"Starting again usually works: Minecraft replaces a chunk it can't read with fresh terrain, which loses anything built there.", where)
	d.Fixes = []Action{{Kind: ActionRestart, Title: "Start the server again", Recommended: true}}
	if c.in.HasBackup {
		d.Explanation += " Restoring a backup keeps the builds as they were when it was made."
	}
	c.backupFix(&d, false)
	return d, true
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
