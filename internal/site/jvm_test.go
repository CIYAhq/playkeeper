package site

import (
	"fmt"
	"html"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// The JVM arguments tool sizes a heap and picks a Java as Playkeeper does
// when it starts a server, its script writes the flags and launcher
// arguments jvm.go lists, and its page starts as the script would.
func TestJVMToolMatchesHowPlaykeeperStartsServers(t *testing.T) {
	b, err := os.ReadFile("../../site/static/js/tools/jvm-flags.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	has := func(want string) {
		t.Helper()
		if !strings.Contains(js, want) {
			t.Errorf("jvm-flags.js doesn't have %s", want)
		}
	}

	// The script's heapFor, with its numbers, gives minecraft.HeapFor's heap
	// for every memory and number of mods the tool offers. Quilt counts as
	// Fabric, Forge as NeoForge, and Purpur and Vanilla as Paper.
	has(fmt.Sprintf("var HEAP = { share: %d, minMB: %d, perModMB: %d, fabric: %d, neoforge: %d };", jvmHeap.Share, jvmHeap.MinMB, jvmHeap.PerModMB, jvmHeap.Fabric, jvmHeap.NeoForge))
	loaders := map[string]int{"fabric": jvmHeap.Fabric, "neoforge": jvmHeap.NeoForge}
	heapFor := func(budgetMB int, typ string, mods int) int {
		outside := max(budgetMB/jvmHeap.Share, jvmHeap.MinMB)
		if base, ok := loaders[typ]; ok {
			outside = max(outside, min(base+jvmHeap.PerModMB*mods, budgetMB/2))
		}
		return budgetMB - outside
	}
	as := map[string][]string{"paper": {"paper", "purpur", "vanilla"}, "fabric": {"fabric", "quilt"}, "neoforge": {"neoforge", "forge"}}
	for budget := 1 << 10; budget <= 32<<10; budget += 512 {
		for mods := 0; mods <= 500; mods += 5 {
			for typ, ids := range as {
				for _, id := range ids {
					if got, want := heapFor(budget, typ, mods), minecraft.HeapFor(budget, id, mods); got != want {
						t.Fatalf("%d MB, %s, %d mods: the tool's heap is %d MB, Playkeeper's %d", budget, id, mods, got, want)
					}
				}
			}
		}
	}

	// The script's javaFor and minecraft.JavaFor agree at each boundary; the
	// browser checks try the same versions in the script.
	has("var NEWEST_JAVA = " + strconv.Itoa(minecraft.NewestJava) + ";")
	for v, want := range map[string]int{"1.8.9": 8, "1.16.5": 8, "1.17": 16, "1.17.1": 16, "1.18": 17, "1.20.4": 17, "1.20.5": 21, "1.21.11": 21, "26.1": 25, jvmVersion: 25} {
		if got := minecraft.JavaFor(v); got != want {
			t.Errorf("Minecraft %s runs on Java %d, not %d: change javaFor in jvm-flags.js and this list", v, got, want)
		}
	}

	// Aikar's flags, in the same order, with the same values from 12 GB up.
	list := regexp.MustCompile(`(?s)var AIKAR = \[(.*?)\];`).FindStringSubmatch(js)
	if list == nil {
		t.Fatal("jvm-flags.js has no AIKAR list")
	}
	var flags []string
	for _, m := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(list[1], -1) {
		flags = append(flags, m[1])
	}
	large := map[string]string{}
	if m := regexp.MustCompile(`var AIKAR_LARGE = \{(.*?)\};`).FindStringSubmatch(js); m != nil {
		for _, kv := range regexp.MustCompile(`'([^']*)': '([^']*)'`).FindAllStringSubmatch(m[1], -1) {
			large[kv[1]+"="+kv[2]] = kv[1]
		}
	}
	var want []string
	wantLarge := 0
	for _, f := range aikarFlags {
		want = append(want, f.Flag)
		if f.Large != "" {
			wantLarge++
			if _, ok := large[f.Large]; !ok {
				t.Errorf("AIKAR_LARGE in jvm-flags.js doesn't have %s", f.Large)
			}
		}
	}
	if strings.Join(flags, " ") != strings.Join(want, " ") {
		t.Errorf("jvm-flags.js has Aikar's flags as\n%s\nwant\n%s", strings.Join(flags, " "), strings.Join(want, " "))
	}
	if len(large) != wantLarge {
		t.Errorf("AIKAR_LARGE has %d values, want %d", len(large), wantLarge)
	}
	has("var LARGE_HEAP_MB = " + strconv.Itoa(jvmLargeHeapMB) + ";")
	has("var LAUNCHER = '" + launcherArgs + "';")
	has("var LAUNCHER_OLD = '" + launcherArgsOld + "';")

	page := html.UnescapeString(pages(build(t, Default))["/tools/jvm-flags"])
	heap := minecraft.HeapFor(jvmBudgetMB, "paper", 0)
	start := fmt.Sprintf("java -Xms%dM -Xmx%dM %s -jar paper.jar --nogui", heap, heap, strings.Join(want, " "))
	if !strings.Contains(page, start) {
		t.Errorf("/tools/jvm-flags doesn't start with %s", start)
	}
	for _, f := range aikarFlags {
		for _, s := range []string{"<code>" + f.Flag + "</code>", f.What} {
			if !strings.Contains(page, s) {
				t.Errorf("/tools/jvm-flags doesn't explain %s with %q", f.Flag, s)
			}
		}
	}
	for _, s := range []string{"-Xms4G -Xmx4G " + launcherArgs, "-Xmx2G " + launcherArgsOld} {
		if !strings.Contains(page, s) {
			t.Errorf("/tools/jvm-flags doesn't give the launcher's arguments %s", s)
		}
	}
}
