package diagnose

import (
	"fmt"
	"path"
	"regexp"
	"regexp/syntax"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// CrashKind identifies what stopped a server.
type CrashKind string

const (
	CrashContainerMemory   CrashKind = "container_memory_limit"
	CrashHeapMemory        CrashKind = "heap_out_of_memory"
	CrashMetaspace         CrashKind = "metaspace_out_of_memory"
	CrashThreads           CrashKind = "thread_limit"
	CrashWatchdog          CrashKind = "watchdog"
	CrashPortInUse         CrashKind = "port_in_use"
	CrashNewerJava         CrashKind = "newer_java"
	CrashMissingDependency CrashKind = "missing_dependency"
	CrashIncompatibleAddon CrashKind = "incompatible_addon"
	CrashAddonFailed       CrashKind = "addon_failed"
	CrashMixinFailed       CrashKind = "mixin_failed"
	CrashDatapack          CrashKind = "datapack_failed"
	CrashCorruptWorld      CrashKind = "corrupt_world"
	CrashWorldLocked       CrashKind = "world_locked"
	CrashDiskFull          CrashKind = "disk_full"
	CrashEULA              CrashKind = "eula"
	CrashPermissionDenied  CrashKind = "permission_denied"
	CrashKilled            CrashKind = "killed"
	CrashUnknown           CrashKind = "unknown"
)

// CrashInput is what the agent knows about a run that ended unexpectedly: a
// crash, or a start that failed.
type CrashInput struct {
	ServerType  string // registry id: paper, purpur, vanilla, fabric, neoforge…
	MCVersion   string
	JavaVersion int // Java major version of the image, e.g. 25

	ExitCode    int
	OOMKilled   bool   // Docker's State.OOMKilled
	DockerError string // State.Error or the error starting the container

	BudgetMB     int // the memory budget (container limit)
	HeapMB       int
	HostMB       int // the machine's memory, for the budgets minecraft.MemoryOptions offers
	RoomMB       int // memory the machine could still give this server on top of BudgetMB
	ViewDistance int // from ParseDistances, for advice when there is no room

	// Console is the run's last lines as Docker returns them, oldest first;
	// the last 2,000 are read. Addresses are redacted here, in what is shown.
	// Lines that already went through minecraft.CleanLine work too, but a jar
	// with a four-part version (Jobs-5.2.6.0.jar) no longer matches its file.
	Console         []string
	CrashReport     string // newest crash report written during the run; "" if none
	CrashReportName string

	Addons     []Addon // installed plugin or mod jars
	FreeDiskMB *int64  // free space on the data disk
	HasBackup  bool    // a backup exists that can be restored
	Port       int     // the server's public port
}

// Addon is an installed plugin or mod jar.
type Addon struct {
	File        string `json:"file"`                   // e.g. "EssentialsX-2.21.0.jar"
	JavaVersion int    `json:"java_version,omitempty"` // Java its classes need: the highest class file major version minus 44; 0 when unknown
}

// CrashDiagnosis says what stopped the server, the evidence for it and what
// can be done. Certain is false when the evidence only makes it the most
// likely explanation, which Explanation then says. Exactly one fix is
// Recommended when there are any.
type CrashDiagnosis struct {
	Kind        CrashKind      `json:"kind"`
	Params      map[string]any `json:"params,omitempty"`
	Certain     bool           `json:"certain"`
	Title       string         `json:"title"`
	Explanation string         `json:"explanation"`
	Evidence    []Evidence     `json:"evidence"`
	Fixes       []Action       `json:"fixes,omitempty"`
}

// Paper's watchdog prints the server thread before a dump of every thread,
// which alone can run to hundreds of lines, so the window is generous.
const (
	maxConsoleLines = 2000
	maxReportLines  = 3000
	maxLineLen      = 2000
	maxEvidenceLen  = 300
	lowDiskMB       = 100
)

// ExplainCrash works out why a server stopped from its exit, console and
// crash report. Rules run from the most to the least specific; the first
// that matches wins, and "unknown" shows the last errors instead.
func ExplainCrash(in CrashInput) CrashDiagnosis {
	c := newCrashCtx(in)
	for _, r := range crashRules {
		if d, ok := r.explain(c); ok {
			d.Certain = d.Certain || r.certain
			return c.finish(d)
		}
	}
	return c.finish(c.unknown())
}

// crashCtx holds the input prepared for the rules.
type crashCtx struct {
	in     CrashInput
	lines  []string
	split  []logLine
	report []string
}

func newCrashCtx(in CrashInput) *crashCtx {
	c := &crashCtx{in: in}
	console := in.Console
	if len(console) > maxConsoleLines {
		console = console[len(console)-maxConsoleLines:]
	}
	for _, l := range console {
		l = minecraft.StripANSI(truncate(l, maxLineLen))
		c.lines = append(c.lines, l)
		c.split = append(c.split, splitLog(l))
	}
	if in.CrashReport != "" {
		for i, l := range strings.Split(in.CrashReport, "\n") {
			if i == maxReportLines {
				break
			}
			c.report = append(c.report, strings.TrimRight(truncate(l, maxLineLen), "\r"))
		}
	}
	return c
}

var reJarToken = regexp.MustCompile(`[A-Za-z0-9_.+-]{1,200}\.jar\b`)

// redact removes addresses from text that is shown but keeps jar names
// whole, because versions such as Jobs-5.2.6.0.jar look like addresses.
func redact(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range reJarToken.FindAllStringIndex(s, -1) {
		b.WriteString(minecraft.RedactIPs(s[last:m[0]]))
		b.WriteString(s[m[0]:m[1]])
		last = m[1]
	}
	b.WriteString(minecraft.RedactIPs(s[last:]))
	return b.String()
}

// found is a line a rule matched, in the console (idx ≥ 0) or the crash
// report.
type found struct {
	idx    int
	line   string
	groups []string
}

func (f found) inReport() bool { return f.idx < 0 }

// console finds the newest console line whose message matches re. Lines
// players can write (chat, /say, /me) start with a name or a tag, so a
// pattern anchored with ^ that begins with fixed text is safe on any line;
// unanchored patterns only look at lines players cannot write.
func (c *crashCtx) console(re *regexp.Regexp) (found, bool) {
	return c.consoleIn(re, 0, len(c.split))
}

// consoleIn is console limited to lines [from, to).
func (c *crashCtx) consoleIn(re *regexp.Regexp, from, to int) (found, bool) {
	a := anchored(re)
	for i := min(to, len(c.split)) - 1; i >= max(from, 0); i-- {
		if f, ok := c.match(re, a, i); ok {
			return f, true
		}
	}
	return found{}, false
}

// firstIn finds the oldest matching line in [from, to).
func (c *crashCtx) firstIn(re *regexp.Regexp, from, to int) (found, bool) {
	a := anchored(re)
	for i := max(from, 0); i < min(to, len(c.split)); i++ {
		if f, ok := c.match(re, a, i); ok {
			return f, true
		}
	}
	return found{}, false
}

// all returns every matching line, oldest first.
func (c *crashCtx) all(re *regexp.Regexp) []found {
	a := anchored(re)
	var out []found
	for i := range c.split {
		if f, ok := c.match(re, a, i); ok {
			out = append(out, f)
		}
	}
	return out
}

func (c *crashCtx) match(re *regexp.Regexp, anchored bool, i int) (found, bool) {
	l := c.split[i]
	if !anchored && !l.trusted() {
		return found{}, false
	}
	if m := re.FindStringSubmatch(l.msg); m != nil {
		return found{idx: i, line: c.lines[i], groups: m}, true
	}
	return found{}, false
}

// anchored reports whether every match of re has to start at the beginning
// of the message, including every branch of an alternation.
func anchored(re *regexp.Regexp) bool {
	s, err := syntax.Parse(re.String(), syntax.Perl)
	if err != nil {
		return false
	}
	return startsAnchored(s.Simplify())
}

func startsAnchored(s *syntax.Regexp) bool {
	switch s.Op {
	case syntax.OpBeginText:
		return true
	case syntax.OpConcat, syntax.OpCapture:
		return len(s.Sub) > 0 && startsAnchored(s.Sub[0])
	case syntax.OpAlternate:
		for _, sub := range s.Sub {
			if !startsAnchored(sub) {
				return false
			}
		}
		return len(s.Sub) > 0
	}
	return false
}

// crashReport finds the first crash report line matching re.
func (c *crashCtx) crashReport(re *regexp.Regexp) (found, bool) {
	for _, l := range c.report {
		if m := re.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			return found{idx: -1, line: strings.TrimSpace(l), groups: m}, true
		}
	}
	return found{}, false
}

// find looks in the console first, then in the crash report.
func (c *crashCtx) find(re *regexp.Regexp) (found, bool) {
	if f, ok := c.console(re); ok {
		return f, true
	}
	return c.crashReport(re)
}

// near finds a match within a few console lines after (or else before) f.
func (c *crashCtx) near(f found, re *regexp.Regexp, span int) (found, bool) {
	if f.inReport() {
		return c.crashReport(re)
	}
	if n, ok := c.consoleIn(re, f.idx, f.idx+span+1); ok {
		return n, true
	}
	return c.consoleIn(re, f.idx-span, f.idx)
}

func (c *crashCtx) evidence(f found) Evidence {
	line := truncate(redact(f.line), maxEvidenceLen)
	if f.inReport() {
		return Evidence{Kind: EvidenceCrashReport, Params: map[string]any{"file": c.in.CrashReportName, "line": line}, Text: line}
	}
	return logEvidence(line)
}

func (c *crashCtx) evidenceOf(fs ...found) []Evidence {
	var out []Evidence
	for _, f := range fs {
		if f.line != "" {
			out = append(out, c.evidence(f))
		}
	}
	return out
}

var typeNames = map[string]string{
	"paper": "Paper", "purpur": "Purpur", "spigot": "Spigot", "folia": "Folia", "vanilla": "Minecraft",
	"fabric": "Fabric", "quilt": "Quilt", "neoforge": "NeoForge", "forge": "Forge",
}

// server names the server in sentences: "your Paper 1.21.4 server".
func (c *crashCtx) server() string {
	name := c.typeName()
	if c.in.MCVersion != "" {
		name += " " + c.in.MCVersion
	}
	return "your " + name + " server"
}

func (c *crashCtx) typeName() string {
	if name := typeNames[c.in.ServerType]; name != "" {
		return name
	}
	return "Minecraft"
}

// addonWord is what the server's add-ons are called.
func (c *crashCtx) addonWord() string {
	if c.pluginServer() {
		return "plugin"
	}
	return "mod"
}

// pluginServer reports whether the server runs Bukkit plugins.
func (c *crashCtx) pluginServer() bool {
	switch c.in.ServerType {
	case "paper", "purpur", "spigot", "folia":
		return true
	}
	return false
}

// installed returns the installed jar a log or path names, if any.
func (c *crashCtx) installed(name string) (string, bool) {
	name = jarName(name)
	if name == "" {
		return "", false
	}
	var fold []string
	for _, a := range c.in.Addons {
		if a.File == name {
			return a.File, true
		}
		if strings.EqualFold(a.File, name) {
			fold = append(fold, a.File)
		}
	}
	if len(fold) == 1 {
		return fold[0], true
	}
	return "", false
}

// jarForMod finds the one installed jar whose file name starts with a mod's
// id or name, ignoring case and punctuation ("sodium-extra" matches
// "Sodium-Extra-0.5.4+mc1.20.4.jar"). Loaders name mods, not files.
func (c *crashCtx) jarForMod(id, name string) (string, bool) {
	var keys []string
	for _, k := range []string{id, name} {
		if k = alnum(k); len(k) >= 3 {
			keys = append(keys, k)
		}
	}
	var hits []string
	for _, a := range c.in.Addons {
		file := alnum(strings.TrimSuffix(a.File, ".jar"))
		for _, k := range keys {
			if strings.HasPrefix(file, k) {
				hits = append(hits, a.File)
				break
			}
		}
	}
	if len(hits) == 1 {
		return hits[0], true
	}
	return "", false
}

// jarName returns the file name of a jar path from a log, or "" when it is
// not a plausible jar file name.
func jarName(p string) string {
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"))
	if len(name) > 255 || !strings.HasSuffix(name, ".jar") || name == ".jar" || strings.ContainsAny(name, "\x00\n\r\t") {
		return ""
	}
	return name
}

func alnum(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

func restartFix() Action { return Action{Kind: ActionRestart, Title: "Start the server again"} }

// addonFixes offers updating an installed jar, or else removing it.
func addonFixes(jar string) []Action {
	return []Action{updateFix(jar, true), removeFix(jar, false)}
}

func updateFix(jar string, recommended bool) Action {
	return Action{Kind: ActionUpdateAddon, Params: map[string]any{"jar": jar}, Title: "Update " + jar, Recommended: recommended}
}

func removeFix(jar string, recommended bool) Action {
	return Action{Kind: ActionRemoveAddon, Params: map[string]any{"jar": jar}, Title: "Remove " + jar, Recommended: recommended}
}

// finish adds the exit code and makes sure exactly one fix is recommended.
func (c *crashCtx) finish(d CrashDiagnosis) CrashDiagnosis {
	if c.in.ExitCode != 0 {
		has := false
		for _, e := range d.Evidence {
			has = has || e.Kind == EvidenceExitCode
		}
		if !has {
			d.Evidence = append(d.Evidence, exitEvidence(c.in.ExitCode))
		}
	}
	if d.Params == nil {
		d.Params = map[string]any{}
	}
	seen := false
	for i := range d.Fixes {
		d.Fixes[i].Recommended = d.Fixes[i].Recommended && !seen
		seen = seen || d.Fixes[i].Recommended
	}
	if !seen && len(d.Fixes) > 0 {
		d.Fixes[0].Recommended = true
	}
	return d
}

func exitEvidence(code int) Evidence {
	return Evidence{Kind: EvidenceExitCode, Params: map[string]any{"code": code}, Text: fmt.Sprintf("The server exited with code %d.", code)}
}

var (
	reErrorLine   = regexp.MustCompile(`^(?:Exception|Caused by: |java\.|Error: |\S+(?:Exception|Error): )`)
	reDescription = regexp.MustCompile(`^Description: `)
)

// unknown shows the last error lines when no rule matched.
func (c *crashCtx) unknown() CrashDiagnosis {
	var ev []Evidence
	for i := len(c.split) - 1; i >= 0 && len(ev) < 5; i-- {
		l := c.split[i]
		if l.prefixed && (l.level == "ERROR" || l.level == "FATAL") || !l.prefixed && reErrorLine.MatchString(strings.TrimSpace(l.msg)) {
			ev = append([]Evidence{c.evidence(found{idx: i, line: c.lines[i]})}, ev...)
		}
	}
	if f, ok := c.crashReport(reDescription); ok {
		ev = append(ev, c.evidence(f))
	}
	explanation := "Playkeeper couldn't tell why from the console or a crash report."
	if len(ev) > 0 {
		explanation += " These are the last errors it found."
	}
	return CrashDiagnosis{
		Kind: CrashUnknown, Title: upperFirst(c.server()) + " stopped unexpectedly", Explanation: explanation,
		Evidence: ev, Fixes: []Action{restartFix()},
	}
}
