package site

import (
	"math"
	"strconv"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// The JVM arguments tool (/tools/jvm-flags) makes a server's start script, or
// the arguments for the game's launcher. js/tools/jvm-flags.js writes them
// with the same flags, heap rule and Java versions (jvm_test.go keeps them in
// step with this file and with how Playkeeper starts servers), and the page
// explains each of Aikar's flags from aikarFlags.

// A JVMFlag is one of Aikar's flags: Flag as it's written for a heap under
// 12 GB, Large as it's written from 12 GB up ("" when it's the same), and
// What it does, in a line.
type JVMFlag struct {
	Flag, Large, What string
}

// LargeValue is the value it has from 12 GB up, like 40, or "".
func (f JVMFlag) LargeValue() string {
	_, v, _ := strings.Cut(f.Large, "=")
	return v
}

// aikarFlags are Aikar's flags in PaperMC's order
// (https://docs.papermc.io/paper/aikars-flags, 28 Sep 2026), with the values
// Aikar gives a heap of 12 GB or more, which Playkeeper's runtime image uses
// too. Each starts on Java 8, 16, 17, 21 and 25 without a warning.
var aikarFlags = []JVMFlag{
	{"-XX:+UseG1GC", "", "Uses G1, the garbage collector the rest tune. Java only picks it itself on a machine with at least 2 cores and about 2 GB of memory."},
	{"-XX:+ParallelRefProcEnabled", "", "Clears weak and soft references with several threads, so pauses are shorter."},
	{"-XX:MaxGCPauseMillis=200", "", "Aims for pauses of at most 200 ms, 4 ticks, which the server makes up for at once."},
	{"-XX:+UnlockExperimentalVMOptions", "", "Allows G1NewSizePercent and G1MaxNewSizePercent, which Java counts as experimental."},
	{"-XX:+DisableExplicitGC", "", "Ignores plugins that ask for a full collection, which would freeze the server."},
	{"-XX:+AlwaysPreTouch", "", "Takes all of the heap from the system at start, so none is handed out slowly during play."},
	{"-XX:G1NewSizePercent=30", "-XX:G1NewSizePercent=40", "Keeps at least this share of the heap for new objects: Minecraft makes hundreds of megabytes of short-lived ones a second."},
	{"-XX:G1MaxNewSizePercent=40", "-XX:G1MaxNewSizePercent=50", "Keeps at most this share of the heap for new objects."},
	{"-XX:G1HeapRegionSize=8M", "-XX:G1HeapRegionSize=16M", "Splits the heap into regions this big, so fewer objects count as humongous and go straight to the old generation."},
	{"-XX:G1ReservePercent=20", "-XX:G1ReservePercent=15", "Keeps this share free for moving objects during a collection, so it never runs out of room to copy them."},
	{"-XX:G1HeapWastePercent=5", "", "Stops cleaning the old generation once no more than 5% of the heap would be freed."},
	{"-XX:G1MixedGCCountTarget=4", "", "Cleans old regions within 4 mixed collections instead of 8, so they're free sooner."},
	{"-XX:InitiatingHeapOccupancyPercent=15", "-XX:InitiatingHeapOccupancyPercent=20", "Starts marking the old generation when the heap is this full, instead of 45%."},
	{"-XX:G1MixedGCLiveThresholdPercent=90", "", "Lets mixed collections clean old regions up to 90% full, where Java's default stops at 85% or lower."},
	{"-XX:G1RSetUpdatingPauseTimePercent=5", "", "Spends 5% of each pause on bookkeeping instead of 10%, and does more of it while the game runs."},
	{"-XX:SurvivorRatio=32", "", "Makes the survivor spaces small, as MaxTenuringThreshold=1 leaves little in them."},
	{"-XX:+PerfDisableSharedMem", "", "Stops Java writing statistics to a file, which can stall the server when the disk is busy."},
	{"-XX:MaxTenuringThreshold=1", "", "Moves objects that survive two collections to the old generation, instead of copying them up to 15 times."},
	{"-Dusing.aikars.flags=https://mcflags.emc.gs", "", "A label: it changes nothing, and tells anyone reading the arguments, in a spark report say, that these are Aikar's flags."},
	{"-Daikars.new.flags=true", "", "Another label, for this version of the flags."},
}

// jvmLargeHeapMB is the heap from which Aikar's larger values apply.
const jvmLargeHeapMB = 12 << 10

// jvmHeap is how the tool sizes the heap from the memory a server has, the
// way Playkeeper does (minecraft.HeapFor): a share of the memory, at least
// MinMB, stays outside the heap, and a mod loader keeps its own amount plus
// PerModMB a mod there, never more than half. Quilt counts as Fabric and
// Forge as NeoForge.
var jvmHeap = struct {
	Share, MinMB, PerModMB, Fabric, NeoForge int
}{Share: 4, MinMB: 512, PerModMB: 6, Fabric: 768, NeoForge: 1024}

// The Minecraft Launcher's own JVM arguments after the memory: since 26.1,
// which runs on Java 25, 4 GB with ZGC (-Xms4G -Xmx4G first), and before it
// 2 GB with G1 (-Xmx2G first).
const (
	launcherArgs    = "-XX:+UseCompactObjectHeaders -XX:+AlwaysPreTouch -XX:+UseStringDeduplication -XX:+UseZGC"
	launcherArgsOld = "-XX:+UnlockExperimentalVMOptions -XX:+UseG1GC -XX:G1NewSizePercent=20 -XX:G1ReservePercent=20 -XX:MaxGCPauseMillis=50 -XX:G1HeapRegionSize=32M"
)

// The tool starts with a Paper server on the newest release with 8 GB.
const (
	jvmVersion  = "26.3"
	jvmBudgetMB = 8 << 10
)

// JVMData is what the page shows from Go: the flags and launcher arguments,
// the version it starts with and its Java, and the heap for its 8 GB.
type JVMData struct {
	Aikar                     []JVMFlag
	LauncherArgs, OldLauncher string
	Version                   string
	Java, HeapMB, LargeGB     int
	HeapGB, OutsideGB         string
}

// gb writes megabytes as gigabytes to one decimal, as the tool's script does:
// 6144 is 6, 5968 is 5.8.
func gb(mb int) string {
	return strconv.FormatFloat(math.Round(float64(mb)/1024*10)/10, 'f', -1, 64)
}

func init() {
	toolData["jvm"] = func() any {
		heap := minecraft.HeapFor(jvmBudgetMB, "paper", 0)
		return JVMData{
			Aikar: aikarFlags, LauncherArgs: launcherArgs, OldLauncher: launcherArgsOld,
			Version: jvmVersion, Java: minecraft.JavaFor(jvmVersion), HeapMB: heap, LargeGB: jvmLargeHeapMB >> 10,
			HeapGB: gb(heap), OutsideGB: gb(jvmBudgetMB - heap),
		}
	}
}
