package diagnose

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func crashConsole(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "crash", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func addonFiles(files ...string) []Addon {
	var out []Addon
	for _, f := range files {
		out = append(out, Addon{File: f})
	}
	return out
}

func paperCrash(console []string) CrashInput {
	return CrashInput{
		ServerType: "paper", MCVersion: "1.21.4", JavaVersion: 25, ExitCode: 1,
		BudgetMB: 4096, HeapMB: 3072, HostMB: 8192, RoomMB: 4096, ViewDistance: 12,
		Console: console, HasBackup: true, Port: 25565,
	}
}

func moddedCrash(serverType, version string, console []string, jars ...string) CrashInput {
	in := paperCrash(console)
	in.ServerType, in.MCVersion, in.Addons = serverType, version, addonFiles(jars...)
	return in
}

func evidenceText(d CrashDiagnosis) string {
	var lines []string
	for _, e := range d.Evidence {
		lines = append(lines, e.Text)
	}
	return strings.Join(lines, "\n")
}

func TestExplainCrashRecognisesEachCause(t *testing.T) {
	report, err := os.ReadFile(filepath.Join("testdata", "crash", "crash-2026-09-25_03.22.17-server.txt"))
	if err != nil {
		t.Fatal(err)
	}
	with := func(in CrashInput, change func(*CrashInput)) CrashInput {
		change(&in)
		return in
	}
	quiet := []string{"[21:03:12 INFO]: Alex joined the game", "[21:30:40 INFO]: Alex left the game"}

	tests := []struct {
		name        string
		in          CrashInput
		kind        CrashKind
		certain     bool
		params      map[string]any
		fixes       string
		explanation []string
		evidence    []string
	}{
		{
			name: "heap out of memory with room for a bigger budget", in: paperCrash(crashConsole(t, "paper_heap_oom.txt")),
			kind: CrashHeapMemory, certain: true, params: map[string]any{"budget_mb": 4096, "heap_mb": 3072},
			fixes:       "raise_memory* from_mb=4096 to_mb=6144; restart",
			explanation: []string{"ran out of the 3 GB it has for the game", "a plugin may be holding on to memory"},
			evidence:    []string{"java.lang.OutOfMemoryError: Java heap space", "could give the server up to 4 GB more", "exited with code 1"},
		},
		{
			name: "heap too full to say so, then killed",
			in: with(paperCrash(crashConsole(t, "paper_heap_oom_handler.txt")), func(in *CrashInput) {
				in.ExitCode, in.BudgetMB, in.HeapMB = 137, 768, 256
			}),
			kind: CrashHeapMemory, certain: true, params: map[string]any{"budget_mb": 768, "heap_mb": 256},
			fixes:    "raise_memory* from_mb=768 to_mb=1536; restart",
			evidence: []string{"OutOfMemoryError thrown from the UncaughtExceptionHandler"},
		},
		{
			name: "heap out of memory on a full machine offers what to do instead",
			in:   with(paperCrash(crashConsole(t, "paper_heap_oom.txt")), func(in *CrashInput) { in.RoomMB = 0 }),
			kind: CrashHeapMemory, certain: true,
			fixes:    "lower_view_distance* from=12 to=8; upgrade_host resource=memory; restart",
			evidence: []string{"no memory to spare"},
		},
		{
			name: "container memory limit",
			in:   with(paperCrash(quiet), func(in *CrashInput) { in.OOMKilled, in.ExitCode = true, 137 }),
			kind: CrashContainerMemory, certain: true, params: map[string]any{"budget_mb": 4096, "heap_mb": 3072},
			fixes:       "raise_memory* from_mb=4096 to_mb=6144; restart",
			explanation: []string{"memory limit of 4 GB", "sets aside 3 GB of that for the game", "the remaining 1 GB"},
			evidence:    []string{"killed for going over its memory limit", "exited with code 137"},
		},
		{
			name: "container memory limit on a full machine never offers a lower view distance",
			in:   with(paperCrash(quiet), func(in *CrashInput) { in.OOMKilled, in.ExitCode, in.RoomMB = true, 137, 0 }),
			kind: CrashContainerMemory, certain: true,
			fixes: "upgrade_host* resource=memory; restart",
		},
		{
			name: "metaspace after plugin reloads", in: paperCrash(crashConsole(t, "paper_metaspace.txt")),
			kind: CrashMetaspace, certain: true, fixes: "restart*",
			explanation: []string{"Reloading plugins without restarting makes it grow"},
			evidence:    []string{"java.lang.OutOfMemoryError: Metaspace"},
		},
		{
			name: "thread limit", in: paperCrash(crashConsole(t, "paper_threads.txt")),
			kind: CrashThreads, certain: true, fixes: "restart*",
			explanation: []string{"A plugin that keeps starting new threads"},
			evidence:    []string{"unable to create native thread"},
		},
		{
			name: "Paper watchdog names the plugin the server thread was running",
			in:   with(paperCrash(crashConsole(t, "paper_watchdog.txt")), func(in *CrashInput) { in.Addons = addonFiles("EssentialsX-2.21.0.jar", "SlowShop-3.2.1.jar") }),
			kind: CrashWatchdog, certain: true, params: map[string]any{"jar": "SlowShop-3.2.1.jar"},
			fixes:       "update_addon* jar=SlowShop-3.2.1.jar; remove_addon jar=SlowShop-3.2.1.jar; restart",
			explanation: []string{"running code from SlowShop-3.2.1.jar, so that plugin is the most likely cause"},
			evidence:    []string{"The server has stopped responding!", "SlowShop-3.2.1.jar//com.example.slowshop.PriceFeed.fetch"},
		},
		{
			name: "Paper watchdog names a plugin that is no longer installed without offering to change it",
			in:   paperCrash(crashConsole(t, "paper_watchdog.txt")),
			kind: CrashWatchdog, certain: true, params: map[string]any{"jar": "SlowShop-3.2.1.jar"}, fixes: "restart*",
		},
		{
			name: "Paper watchdog without a plugin at work",
			in:   with(paperCrash(crashConsole(t, "paper_watchdog_no_plugin.txt")), func(in *CrashInput) { in.Addons = addonFiles("LuckPerms-Bukkit-5.4.145.jar") }),
			kind: CrashWatchdog, certain: true, fixes: "restart*; run_profiler",
			explanation: []string{"doesn't show a plugin at work"},
		},
		{
			name: "vanilla watchdog",
			in: with(moddedCrash("vanilla", "1.21.4", crashConsole(t, "vanilla_watchdog.txt")), func(in *CrashInput) {
				in.CrashReport, in.CrashReportName = string(report), "crash-2026-09-25_03.22.17-server.txt"
			}),
			kind: CrashWatchdog, certain: true, params: map[string]any{"seconds": 60}, fixes: "restart*",
			explanation: []string{"a single tick took 60 seconds"},
			evidence:    []string{"A single server tick took 60.00 seconds"},
		},
		{
			name: "vanilla watchdog from the crash report alone",
			in: with(moddedCrash("vanilla", "1.21.4", nil), func(in *CrashInput) {
				in.CrashReport, in.CrashReportName = string(report), "crash-2026-09-25_03.22.17-server.txt"
			}),
			kind: CrashWatchdog, certain: true, params: map[string]any{"seconds": 60}, fixes: "restart*",
			evidence: []string{"java.lang.Error: ServerHangWatchdog detected that a single server tick took 60.00 seconds"},
		},
		{
			name: "port taken inside the container", in: moddedCrash("vanilla", "1.21.4", crashConsole(t, "vanilla_bind.txt")),
			kind: CrashPortInUse, certain: true, params: map[string]any{"reason": "in_use", "port": 25565}, fixes: "restart*",
			explanation: []string{"set to use the same port as the game"},
			evidence:    []string{"**** FAILED TO BIND TO PORT!", "bind(..) failed: Address already in use"},
		},
		{
			name: "Docker port already allocated",
			in: with(paperCrash(nil), func(in *CrashInput) {
				in.ExitCode = 0
				in.DockerError = "driver failed programming external connectivity on endpoint pk-survival (3f2a91c0): Bind for 0.0.0.0:25565 failed: port is already allocated"
			}),
			kind: CrashPortInUse, certain: true, params: map[string]any{"port": 25565},
			fixes:       "change_port* port=25565; restart",
			explanation: []string{"already uses port 25565"},
			evidence:    []string{"Bind for [ip redacted] failed: port is already allocated"},
		},
		{
			name: "Docker port bound by another program",
			in: with(paperCrash(nil), func(in *CrashInput) {
				in.ExitCode = 0
				in.DockerError = "Error response from daemon: failed to set up container networking: Error starting userland proxy: listen tcp4 0.0.0.0:25566: bind: address already in use"
			}),
			kind: CrashPortInUse, certain: true, params: map[string]any{"port": 25566}, fixes: "change_port* port=25566; restart",
		},
		{
			name: "Docker 29 host port bound by another program",
			in: with(paperCrash(nil), func(in *CrashInput) {
				in.ExitCode = 0
				in.DockerError = "failed to set up container networking: driver failed programming external connectivity on endpoint pk-survival (e8c1b689): failed to bind host port 0.0.0.0:25800/tcp: address already in use"
			}),
			kind: CrashPortInUse, certain: true, params: map[string]any{"port": 25800}, fixes: "change_port* port=25800; restart",
		},
		{
			name: "Docker 28 host port bound by another program",
			in: with(paperCrash(nil), func(in *CrashInput) {
				in.ExitCode = 0
				in.DockerError = "driver failed programming external connectivity on endpoint pk-survival (e8c1b689): failed to bind host port for [::]:25801:172.18.0.2:25565/tcp: address already in use"
			}),
			kind: CrashPortInUse, certain: true, params: map[string]any{"port": 25801}, fixes: "change_port* port=25801; restart",
		},
		{
			name: "plugin built for a newer Java is only a possible cause",
			in:   with(paperCrash(crashConsole(t, "paper_plugin_java.txt")), func(in *CrashInput) { in.Addons = addonFiles("FancyNpcs-2.8.0.jar") }),
			kind: CrashNewerJava, params: map[string]any{"required": 26, "available": 25, "jar": "FancyNpcs-2.8.0.jar"},
			fixes:       "remove_addon* jar=FancyNpcs-2.8.0.jar; update_addon jar=FancyNpcs-2.8.0.jar",
			explanation: []string{"FancyNpcs-2.8.0.jar is built for Java 26, but the server runs Java 25", "Paper keeps running without a plugin that fails like this"},
			evidence:    []string{"FancyNpcs-2.8.0.jar needs Java 26; the server runs Java 25."},
		},
		{
			name: "server software built for a newer Java", in: paperCrash(crashConsole(t, "paper_server_java.txt")),
			kind: CrashNewerJava, certain: true, params: map[string]any{"required": 26, "available": 25},
			explanation: []string{"The Paper server software is built for Java 26, but it ran on Java 25"},
			evidence:    []string{"io/papermc/paperclip/Main has been compiled by a more recent version"},
		},
		{
			name: "Fabric mod built for a newer Java",
			in:   moddedCrash("fabric", "1.21.9", crashConsole(t, "fabric_java.txt"), "sodium-fabric-0.7.1+mc1.21.9.jar", "lithium-fabric-0.16.0+mc1.21.9.jar"),
			kind: CrashNewerJava, certain: true, params: map[string]any{"required": 26, "available": 25, "addon": "Sodium", "jar": "sodium-fabric-0.7.1+mc1.21.9.jar"},
			fixes:       "remove_addon* jar=sodium-fabric-0.7.1+mc1.21.9.jar; update_addon jar=sodium-fabric-0.7.1+mc1.21.9.jar",
			explanation: []string{"Sodium is built for Java 26, but the server runs Java 25, which can't load it."},
		},
		{
			name: "Fabric mod missing a dependency",
			in:   moddedCrash("fabric", "1.21.4", crashConsole(t, "fabric_missing.txt"), "Chunky-Fabric-1.4.23.jar", "spark-1.10.121-fabric.jar"),
			kind: CrashMissingDependency, certain: true,
			params:      map[string]any{"addon": "Chunky", "addon_id": "chunky", "dependency": "fabric-api", "more": 1, "jar": "Chunky-Fabric-1.4.23.jar"},
			fixes:       "install_addon* name=fabric-api; remove_addon jar=Chunky-Fabric-1.4.23.jar",
			explanation: []string{"Chunky requires any version of fabric-api, and it isn't installed", "Fabric reported 1 more problem like this"},
			evidence:    []string{"- Mod 'Chunky' (chunky) 1.4.23 requires any version of fabric-api, which is missing!"},
		},
		{
			name: "Fabric mod made for another Minecraft version",
			in:   moddedCrash("fabric", "1.21.4", crashConsole(t, "fabric_mismatch.txt"), "fabric-api-0.128.2+1.21.5.jar", "fabric-language-kotlin-1.13.4+kotlin.2.2.0.jar"),
			kind: CrashIncompatibleAddon, certain: true,
			params:      map[string]any{"addon": "Fabric API", "requires": "version 1.21.5", "jar": "fabric-api-0.128.2+1.21.5.jar"},
			fixes:       "update_addon* jar=fabric-api-0.128.2+1.21.5.jar; remove_addon jar=fabric-api-0.128.2+1.21.5.jar",
			explanation: []string{"Fabric API requires version 1.21.5 of Minecraft, but this server runs 1.21.4"},
		},
		{
			name: "Fabric mods that break each other",
			in:   moddedCrash("fabric", "1.21.1", crashConsole(t, "fabric_breaks.txt"), "lithium-fabric-0.14.3+mc1.21.1.jar", "canary-mc1.21.1-0.3.3.jar"),
			kind: CrashIncompatibleAddon, certain: true, params: map[string]any{"addon": "Lithium", "other": "Canary"},
			fixes:       "remove_addon* jar=canary-mc1.21.1-0.3.3.jar; remove_addon jar=lithium-fabric-0.14.3+mc1.21.1.jar",
			explanation: []string{"Lithium is marked as incompatible with Canary"},
		},
		{
			name: "Fabric mixin failure",
			in:   moddedCrash("fabric", "1.21.4", crashConsole(t, "fabric_mixin.txt"), "better-end-4.0.11.jar", "bclib-4.0.13.jar"),
			kind: CrashMixinFailed, certain: true, params: map[string]any{"addon": "betterend", "jar": "better-end-4.0.11.jar"},
			fixes:    "update_addon* jar=better-end-4.0.11.jar; remove_addon jar=better-end-4.0.11.jar",
			evidence: []string{"Mixin apply for mod betterend failed"},
		},
		{
			name: "Fabric mod failing in its entrypoint",
			in:   moddedCrash("fabric", "1.21.4", crashConsole(t, "fabric_entrypoint.txt"), "Chunky-Fabric-1.4.23.jar"),
			kind: CrashAddonFailed, certain: true, params: map[string]any{"addon": "chunky", "jar": "Chunky-Fabric-1.4.23.jar"},
			fixes:    "update_addon* jar=Chunky-Fabric-1.4.23.jar; remove_addon jar=Chunky-Fabric-1.4.23.jar",
			evidence: []string{"provided by 'chunky'", "Caused by: java.lang.NoClassDefFoundError"},
		},
		{
			name: "NeoForge mod missing a dependency",
			in:   moddedCrash("neoforge", "1.21.1", crashConsole(t, "neoforge_missing.txt"), "mowziesmobs-1.7.2-1.21.1.jar"),
			kind: CrashMissingDependency, certain: true, params: map[string]any{"addon": "mowziesmobs", "dependency": "geckolib", "jar": "mowziesmobs-1.7.2-1.21.1.jar"},
			fixes:       "install_addon* name=geckolib; remove_addon jar=mowziesmobs-1.7.2-1.21.1.jar",
			explanation: []string{"mowziesmobs requires geckolib 4.7 or above, and it isn't installed"},
			evidence:    []string{"- Mod mowziesmobs requires geckolib 4.7 or above", "Currently, geckolib is not installed"},
		},
		{
			name: "NeoForge file that stops it loading",
			in:   moddedCrash("neoforge", "1.21.1", crashConsole(t, "neoforge_broken_file.txt"), "Jade-1.21.1-NeoForge-15.10.0.jar", "sodium-fabric-0.6.13+mc1.21.1.jar"),
			kind: CrashIncompatibleAddon, certain: true, params: map[string]any{"jar": "Jade-1.21.1-NeoForge-15.10.0.jar", "reason": "invalid"},
			fixes:       "update_addon* jar=Jade-1.21.1-NeoForge-15.10.0.jar; remove_addon jar=Jade-1.21.1-NeoForge-15.10.0.jar",
			explanation: []string{"refused to start because it can't load Jade-1.21.1-NeoForge-15.10.0.jar"},
		},
		{
			name: "NeoForge file it skipped is only a possible cause",
			in:   moddedCrash("neoforge", "1.21.1", crashConsole(t, "neoforge_skipped_file.txt"), "sodium-fabric-0.6.13+mc1.21.1.jar"),
			kind: CrashIncompatibleAddon, params: map[string]any{"jar": "sodium-fabric-0.6.13+mc1.21.1.jar", "reason": "fabric"},
			fixes:       "remove_addon* jar=sodium-fabric-0.6.13+mc1.21.1.jar",
			explanation: []string{"it is a Fabric mod, not a NeoForge mod", "skips files like this and keeps running"},
		},
		{
			name: "NeoForge mod failing while starting",
			in:   moddedCrash("neoforge", "1.21.1", crashConsole(t, "neoforge_mod_failed.txt"), "FarmersDelight-1.21.1-1.2.7.jar"),
			kind: CrashAddonFailed, certain: true, params: map[string]any{"addon": "Farmer's Delight", "jar": "FarmersDelight-1.21.1-1.2.7.jar"},
			fixes:    "update_addon* jar=FarmersDelight-1.21.1-1.2.7.jar; remove_addon jar=FarmersDelight-1.21.1-1.2.7.jar",
			evidence: []string{"java.lang.NullPointerException: Cannot invoke"},
		},
		{
			name: "Forge mod missing a dependency",
			in:   moddedCrash("forge", "26.2", crashConsole(t, "forge_missing.txt"), "BiomesOPlenty-forge-26.2-26.2.0.0.28.jar"),
			kind: CrashMissingDependency, certain: true, params: map[string]any{"addon": "biomesoplenty", "dependency": "terrablender", "jar": "BiomesOPlenty-forge-26.2-26.2.0.0.28.jar"},
			fixes:       "install_addon* name=terrablender; remove_addon jar=BiomesOPlenty-forge-26.2-26.2.0.0.28.jar",
			explanation: []string{"Forge refused to start because biomesoplenty requires terrablender 26.2.0.0.1 or above, and it isn't installed."},
			evidence:    []string{"Mod biomesoplenty requires terrablender 26.2.0.0.1 or above", "Currently, terrablender is not installed"},
		},
		{
			name: "Forge mod missing a dependency, from the crash report alone",
			in: with(moddedCrash("forge", "26.2", []string{"[13:10:24] [main/FATAL] [ne.mi.se.lo.ServerModLoader/]: Crash report saved to ./crash-reports/crash-2026-09-26_13.10.24-fml.txt"},
				"BiomesOPlenty-forge-26.2-26.2.0.0.28.jar"), func(in *CrashInput) {
				in.CrashReport, in.CrashReportName = strings.Join(crashConsole(t, "crash-2026-09-26_13.10.24-fml.txt"), "\n"), "crash-2026-09-26_13.10.24-fml.txt"
			}),
			kind: CrashMissingDependency, certain: true, params: map[string]any{"addon": "biomesoplenty", "dependency": "terrablender"},
			fixes:       "install_addon* name=terrablender; remove_addon jar=BiomesOPlenty-forge-26.2-26.2.0.0.28.jar",
			explanation: []string{"Forge refused to start because biomesoplenty requires terrablender 26.2.0.0.1 or above"},
		},
		{
			name: "Forge mod made for another Minecraft",
			in:   moddedCrash("forge", "26.2", crashConsole(t, "forge_wrong_minecraft.txt"), "Chunky-Forge-1.4.55.jar"),
			kind: CrashIncompatibleAddon, certain: true, params: map[string]any{"addon": "chunky", "dependency": "minecraft", "jar": "Chunky-Forge-1.4.55.jar"},
			fixes:       "update_addon* jar=Chunky-Forge-1.4.55.jar; remove_addon jar=Chunky-Forge-1.4.55.jar",
			explanation: []string{"Forge refused to start because chunky requires Minecraft 1.21.11 or above, and below 1.22, but this server has 26.2."},
		},
		{
			name: "Forge jar it can't open",
			in:   moddedCrash("forge", "26.2", crashConsole(t, "forge_broken_jar.txt"), "Jade-26.2-Forge-21.0.1.jar", "Chunky-Forge-1.5.4.jar"),
			kind: CrashIncompatibleAddon, certain: true, params: map[string]any{"jar": "Jade-26.2-Forge-21.0.1.jar", "reason": "invalid"},
			fixes:       "update_addon* jar=Jade-26.2-Forge-21.0.1.jar; remove_addon jar=Jade-26.2-Forge-21.0.1.jar",
			explanation: []string{"Forge refused to start because it can't open Jade-26.2-Forge-21.0.1.jar"},
			evidence:    []string{`Failed to create secure jar for "/data/mods/Jade-26.2-Forge-21.0.1.jar" - zip END header not found`},
		},
		{
			name: "Forge mod failing in its setup",
			in:   moddedCrash("forge", "26.2", crashConsole(t, "forge_mod_failed.txt"), "wthit-26.1-forge-19.0.1.jar", "badpackets-forge-0.12.2.jar"),
			kind: CrashAddonFailed, certain: true, params: map[string]any{"addon": "waila", "jar": "wthit-26.1-forge-19.0.1.jar"},
			fixes:       "update_addon* jar=wthit-26.1-forge-19.0.1.jar; remove_addon jar=wthit-26.1-forge-19.0.1.jar",
			explanation: []string{"Forge stopped because the mod waila hit an error while starting."},
			evidence:    []string{"Mod 'waila' encountered an error in a deferred task:", "java.lang.NoSuchFieldError: Class net.minecraft.world.entity.EntityType"},
		},
		{
			name: "data pack with errors", in: moddedCrash("vanilla", "1.21.4", crashConsole(t, "vanilla_datapack.txt")),
			kind: CrashDatapack, certain: true, params: map[string]any{"pack": "Terralith_1.21.x_v2.5.8.zip"},
			fixes:       "remove_datapack* pack=Terralith_1.21.x_v2.5.8.zip",
			explanation: []string{"from a data pack, has errors", "The errors are in the data pack Terralith_1.21.x_v2.5.8.zip"},
		},
		{
			name: "world open somewhere else", in: paperCrash(crashConsole(t, "paper_world_locked.txt")),
			kind: CrashWorldLocked, certain: true, fixes: "restart*",
			evidence: []string{"session.lock: already locked"},
		},
		{
			name: "damaged level.dat with a backup", in: moddedCrash("vanilla", "1.21.4", crashConsole(t, "vanilla_level_dat.txt")),
			kind: CrashCorruptWorld, certain: true, params: map[string]any{"file": "level.dat"}, fixes: "restore_backup*",
		},
		{
			name: "damaged level.dat without a backup",
			in:   with(moddedCrash("vanilla", "1.21.4", crashConsole(t, "vanilla_level_dat.txt")), func(in *CrashInput) { in.HasBackup = false }),
			kind: CrashCorruptWorld, certain: true, fixes: "",
			explanation: []string{"There is no backup to restore."},
		},
		{
			name: "unreadable chunk is only the likely cause", in: paperCrash(crashConsole(t, "paper_chunk.txt")),
			kind: CrashCorruptWorld, params: map[string]any{"chunk_x": 12, "chunk_z": -5}, fixes: "restart*; restore_backup",
			explanation: []string{"the chunk around x 192, z -80", "most likely reason", "Restoring a backup keeps the builds"},
			evidence:    []string{"Couldn't load chunk [12, -5]"},
		},
		{
			name: "disk that was full when it stopped",
			in:   with(paperCrash(crashConsole(t, "disk_full.txt")), func(in *CrashInput) { in.FreeDiskMB = ptr(int64(20480)) }),
			kind: CrashDiskFull, certain: true, params: map[string]any{"free_mb": 20480}, fixes: "restart*; free_disk free_mb=20480",
			explanation: []string{"full at the time. There is 20 GB free now"},
			evidence:    []string{"No space left on device", "20 GB free on the server's disk."},
		},
		{
			name: "disk that is still full",
			in:   with(paperCrash(crashConsole(t, "disk_full.txt")), func(in *CrashInput) { in.FreeDiskMB = ptr(int64(40)) }),
			kind: CrashDiskFull, certain: true, fixes: "free_disk* free_mb=40",
			explanation: []string{"only 40 MB is free"},
		},
		{
			name: "nearly full disk without a line about it is only the likely cause",
			in:   with(paperCrash(crashConsole(t, "unknown.txt")), func(in *CrashInput) { in.FreeDiskMB = ptr(int64(50)) }),
			kind: CrashDiskFull, fixes: "free_disk* free_mb=50",
			explanation: []string{"Only 50 MB is free", "most likely reason"},
		},
		{
			name: "EULA not accepted", in: moddedCrash("vanilla", "1.21.4", crashConsole(t, "eula.txt")),
			kind: CrashEULA, certain: true, fixes: "accept_eula*",
		},
		{
			name: "permission denied in the error it stopped with", in: paperCrash(crashConsole(t, "permission.txt")),
			kind: CrashPermissionDenied, certain: true, params: map[string]any{"path": "./world/session.lock"}, fixes: "fix_permissions*",
			explanation: []string{"stopped because it wasn't allowed to read or write ./world/session.lock"},
		},
		{
			name: "plugin missing a dependency is only a possible cause",
			in:   with(paperCrash(crashConsole(t, "paper_missing_dependency.txt")), func(in *CrashInput) { in.Addons = addonFiles("EssentialsXChat-2.21.0.jar") }),
			kind: CrashMissingDependency, params: map[string]any{"addon": "EssentialsChat", "dependency": "Essentials", "jar": "EssentialsXChat-2.21.0.jar"},
			fixes:       "install_addon* name=Essentials; remove_addon jar=EssentialsXChat-2.21.0.jar",
			explanation: []string{"couldn't load EssentialsChat because it needs Essentials", "Paper keeps running without a plugin that fails like this"},
			evidence:    []string{"Could not load 'plugins/EssentialsXChat-2.21.0.jar' in 'plugins'", "Unknown/missing dependency plugins: [Essentials]"},
		},
		{
			name: "plugin missing two dependencies",
			in: with(paperCrash([]string{
				"[09:14:02 ERROR]: [ModernPluginLoadingStrategy] Could not load 'plugins/Jobs-5.2.6.0.jar' in 'plugins'",
				"org.bukkit.plugin.UnknownDependencyException: Unknown/missing dependency plugins: [Vault, CMILib]. Please download and install these plugins to run 'Jobs'.",
			}), func(in *CrashInput) { in.Addons = addonFiles("Jobs-5.2.6.0.jar") }),
			kind: CrashMissingDependency, params: map[string]any{"addon": "Jobs", "dependency": "Vault", "dependencies": []string{"Vault", "CMILib"}},
			fixes:       "install_addon* name=Vault; install_addon name=CMILib; remove_addon jar=Jobs-5.2.6.0.jar",
			explanation: []string{"it needs Vault and CMILib"},
			evidence:    []string{"Could not load 'plugins/Jobs-5.2.6.0.jar' in 'plugins'"},
		},
		{
			name: "plugin failing to enable is only a possible cause",
			in:   with(paperCrash(crashConsole(t, "paper_enable_failure.txt")), func(in *CrashInput) { in.Addons = addonFiles("Shopkeepers-2.23.3.jar", "Vault-1.7.3.jar") }),
			kind: CrashAddonFailed, params: map[string]any{"addon": "Shopkeepers", "jar": "Shopkeepers-2.23.3.jar"},
			fixes:    "update_addon* jar=Shopkeepers-2.23.3.jar; remove_addon jar=Shopkeepers-2.23.3.jar",
			evidence: []string{"Error occurred while enabling Shopkeepers v2.23.3", "java.lang.NoSuchMethodError", "Shopkeepers-2.23.3.jar//com.nisovin.shopkeepers.compat"},
		},
		{
			name: "killed from outside", in: with(paperCrash(quiet), func(in *CrashInput) { in.ExitCode = 137 }),
			kind: CrashKilled, certain: true, fixes: "restart*",
			explanation: []string{"exit code 137 means it received SIGKILL"},
		},
		{
			name: "unknown shows the last errors", in: paperCrash(crashConsole(t, "unknown.txt")),
			kind: CrashUnknown, fixes: "restart*",
			explanation: []string{"These are the last errors it found."},
			evidence:    []string{"Encountered an unexpected exception", "net.minecraft.ReportedException: Exception ticking world", "Caused by: java.lang.IllegalStateException: Recursive call"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := ExplainCrash(tt.in)
			if d.Kind != tt.kind || d.Certain != tt.certain {
				t.Fatalf("got %s certain=%v, want %s certain=%v: %s %s\n%s", d.Kind, d.Certain, tt.kind, tt.certain, d.Title, d.Explanation, evidenceText(d))
			}
			for k, v := range tt.params {
				if fmt.Sprint(d.Params[k]) != fmt.Sprint(v) {
					t.Errorf("params[%s] = %v, want %v (all: %v)", k, d.Params[k], v, d.Params)
				}
			}
			if got := actionSummary(d.Fixes); got != tt.fixes {
				t.Errorf("fixes = %q, want %q", got, tt.fixes)
			}
			for _, s := range tt.explanation {
				if !strings.Contains(d.Explanation, s) {
					t.Errorf("explanation %q lacks %q", d.Explanation, s)
				}
			}
			if tt.in.ServerType == "forge" && strings.Contains(d.Title+" "+d.Explanation, "NeoForge") {
				t.Errorf("crash help on a Forge server names NeoForge: %s %s", d.Title, d.Explanation)
			}
			ev := evidenceText(d)
			for _, s := range tt.evidence {
				if !strings.Contains(ev, s) {
					t.Errorf("evidence lacks %q:\n%s", s, ev)
				}
			}
			if d.Title == "" || d.Explanation == "" || d.Params == nil {
				t.Errorf("incomplete diagnosis %+v", d)
			}
			if _, err := json.Marshal(d); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestExplainCrashIgnoresCausesPlayersTypeInChat(t *testing.T) {
	console := crashConsole(t, "paper_chat_spoof.txt")
	jars := []string{"EssentialsX-2.21.0.jar", "FarmersDelight-1.21.1-1.2.7.jar", "better-end-4.0.11.jar", "Shopkeepers-2.23.3.jar"}
	for _, serverType := range []string{"paper", "fabric", "neoforge", "forge", "vanilla"} {
		d := ExplainCrash(moddedCrash(serverType, "1.21.4", console, jars...))
		if d.Kind != CrashUnknown || len(d.Evidence) != 1 || d.Evidence[0].Kind != EvidenceExitCode {
			t.Errorf("%s: chat lines were taken as evidence: %s %s\n%s", serverType, d.Kind, d.Explanation, evidenceText(d))
		}
	}
}

func TestAnchoredChecksEveryBranch(t *testing.T) {
	for pattern, want := range map[string]bool{
		`^Error occurred`:      true,
		`^(?:Mod ID|File) `:    true,
		`^A|^B`:                true,
		`^A|B`:                 false,
		`(?:^|\s)x\.jar//`:     false,
		`No space left`:        false,
		`^\s*- Mod '[^']+'`:    true,
		`\s*(?:- )?\w+ failed`: false,
	} {
		if got := anchored(regexp.MustCompile(pattern)); got != want {
			t.Errorf("anchored(%q) = %v, want %v", pattern, got, want)
		}
	}
}

func TestExplainCrashReadsOnlyTheLastConsoleLines(t *testing.T) {
	console := func(eulaAt int) []string {
		lines := make([]string, 2100)
		for i := range lines {
			lines[i] = fmt.Sprintf("[12:00:00] [Server thread/INFO]: Line %d", i)
		}
		lines[eulaAt] = "[12:00:00] [ServerMain/INFO]: You need to agree to the EULA in order to run the server. Go to eula.txt for more info."
		return lines
	}
	if d := ExplainCrash(moddedCrash("vanilla", "1.21.4", console(150))); d.Kind != CrashEULA {
		t.Errorf("line 150 of 2,100 is within the last 2,000, got %s", d.Kind)
	}
	if d := ExplainCrash(moddedCrash("vanilla", "1.21.4", console(50))); d.Kind != CrashUnknown {
		t.Errorf("line 50 of 2,100 is outside the last 2,000, got %s", d.Kind)
	}
}

func TestExplainCrashShortensAndRedactsEvidence(t *testing.T) {
	d := ExplainCrash(paperCrash([]string{
		"[21:05:01 ERROR]: Couldn't handle packet from 198.51.100.4:40000 " + strings.Repeat("x", 5000),
		"[21:05:02 ERROR]: Encountered an unexpected exception",
	}))
	if d.Kind != CrashUnknown || len(d.Evidence) != 3 {
		t.Fatalf("got %s with %d evidence items", d.Kind, len(d.Evidence))
	}
	first := d.Evidence[0].Text
	if strings.Contains(first, "198.51.100.4") || !strings.Contains(first, "[ip redacted]") {
		t.Errorf("address not redacted: %.80s", first)
	}
	if len(first) > maxEvidenceLen+len("…") || !strings.HasSuffix(first, "…") {
		t.Errorf("evidence is %d bytes, want at most %d and an ellipsis", len(first), maxEvidenceLen)
	}

	report := "---- Minecraft Crash Report ----\nDescription: Exception in server tick loop\n\njava.lang.IllegalStateException: Lost connection to 203.0.113.9:3306\n"
	in := paperCrash(nil)
	in.CrashReport, in.CrashReportName = report, "crash-2026-09-25_21.05.03-server.txt"
	d = ExplainCrash(in)
	if ev := evidenceText(d); !strings.Contains(ev, "Description: Exception in server tick loop") {
		t.Errorf("crash report description missing:\n%s", ev)
	}
	for _, e := range d.Evidence {
		if e.Kind == EvidenceCrashReport && e.Params["file"] != "crash-2026-09-25_21.05.03-server.txt" {
			t.Errorf("crash report evidence without its file: %+v", e)
		}
	}
	d = ExplainCrash(moddedCrash("vanilla", "1.21.4", nil))
	if d.Kind != CrashUnknown || !strings.Contains(d.Explanation, "couldn't tell why") || strings.Contains(d.Explanation, "last errors") {
		t.Errorf("empty input: %s %q", d.Kind, d.Explanation)
	}
}

func TestRedactKeepsJarNamesThatLookLikeAddresses(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Could not load 'plugins/Jobs-5.2.6.0.jar' in 'plugins'", "Could not load 'plugins/Jobs-5.2.6.0.jar' in 'plugins'"},
		{"Alex[/198.51.100.4:50122] logged in", "Alex[/[ip redacted]] logged in"},
		{"Jobs-5.2.6.0.jar lost 203.0.113.9:3306 and 10.0.0.1", "Jobs-5.2.6.0.jar lost [ip redacted] and [ip redacted]"},
		{"198.51.100.4.jar", "198.51.100.4.jar"},
		{"no addresses here", "no addresses here"},
	}
	for _, tt := range tests {
		if got := redact(tt.in); got != tt.want {
			t.Errorf("redact(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestExplainCrashOnlyOffersAddonFixesForOneInstalledJar(t *testing.T) {
	console := crashConsole(t, "fabric_missing.txt")
	d := ExplainCrash(moddedCrash("fabric", "1.21.4", console, "Chunky-Fabric-1.4.23.jar", "ChunkyBorder-1.2.23.jar"))
	if got := actionSummary(d.Fixes); got != "install_addon* name=fabric-api" {
		t.Errorf("two jars could be Chunky, so neither may be removed; got %q", got)
	}
	if _, ok := d.Params["jar"]; ok {
		t.Errorf("ambiguous jar named: %v", d.Params)
	}
	d = ExplainCrash(moddedCrash("fabric", "1.21.4", console))
	if got := actionSummary(d.Fixes); got != "install_addon* name=fabric-api" {
		t.Errorf("nothing installed to remove; got %q", got)
	}
}

func TestFinishRecommendsExactlyOneFixAndAddsTheExitCodeOnce(t *testing.T) {
	c := newCrashCtx(CrashInput{ExitCode: 2})
	d := c.finish(CrashDiagnosis{Fixes: []Action{installFix("Vault"), installFix("CMILib")}})
	if got := actionSummary(d.Fixes); got != "install_addon* name=Vault; install_addon name=CMILib" {
		t.Errorf("two recommended: got %q", got)
	}
	d = c.finish(CrashDiagnosis{Fixes: []Action{restartFix(), removeFix("a.jar", false)}})
	if got := actionSummary(d.Fixes); got != "restart*; remove_addon jar=a.jar" {
		t.Errorf("none recommended: got %q", got)
	}
	d = c.finish(CrashDiagnosis{Evidence: []Evidence{exitEvidence(2)}})
	if len(d.Evidence) != 1 || d.Params == nil || d.Fixes != nil {
		t.Errorf("got %+v", d)
	}
	if d := newCrashCtx(CrashInput{}).finish(CrashDiagnosis{}); len(d.Evidence) != 0 {
		t.Errorf("exit code 0 is not evidence: %+v", d.Evidence)
	}
}

func shownText(lines []ShownLine) string {
	var out []string
	for _, l := range lines {
		out = append(out, strings.Join(slices.DeleteFunc([]string{l.Time, l.Level, l.Text}, func(s string) bool { return s == "" }), " "))
	}
	return strings.Join(out, "\n")
}

func TestExplainCrashShowsTheQuotedLinesThenStoppingServer(t *testing.T) {
	portIn := paperCrash(nil)
	portIn.DockerError = "driver failed programming external connectivity on endpoint pk: Bind for 0.0.0.0:25565 failed: port is already allocated"
	many := []string{"[09:00:00 INFO]: Loading 3 plugins"}
	for i := range 5 {
		many = append(many, fmt.Sprintf("[09:00:0%d ERROR]: Could not load 'plugins/Broken%d.jar' in folder 'plugins'", i+1, i), "org.bukkit.plugin.InvalidPluginException: broken")
	}
	many = append(many, "[09:00:07 INFO]: Done (3.1s)! For help, type \"help\"", "[09:10:00 ERROR]: Encountered an unexpected exception", "java.lang.IllegalStateException: boom", "[09:10:01 INFO]: Stopping server", "[09:10:01 INFO]: Saving players")
	tests := []struct {
		name string
		in   CrashInput
		want string
	}{
		{"quoted lines, with a stack trace's line under its entry, then the stop", paperCrash(crashConsole(t, "paper_heap_oom.txt")),
			"03:11:30 ERROR java.lang.OutOfMemoryError: Java heap space\n03:11:31 Stopping server"},
		{"no quoted lines: the last lines with a log prefix, addresses redacted", paperCrash([]string{
			"[21:03:12 INFO]: Alex[/198.51.100.4:50122] logged in with entity id 7",
			"\tat some.Frame(Frame.java:1)",
			"[21:30:40 WARN]: Can't keep up! Is the server overloaded? Running 2400ms or 48 ticks behind",
		}), "21:03:12 Alex[/[ip redacted]] logged in with entity id 7\n21:30:40 WARN Can't keep up! Is the server overloaded? Running 2400ms or 48 ticks behind"},
		{"a Docker error stands in for a server that printed nothing", portIn,
			"ERROR driver failed programming external connectivity on endpoint pk: Bind for [ip redacted] failed: port is already allocated"},
		{"at most three quoted lines", paperCrash(many), ""},
	}
	for _, tt := range tests {
		d := ExplainCrash(tt.in)
		got := shownText(d.Lines)
		if tt.want == "" {
			if len(d.Lines) > maxShownLines || len(d.Lines) == 0 || d.Lines[len(d.Lines)-1].Text != "Stopping server" {
				t.Errorf("%s: %s\n%s", tt.name, d.Kind, got)
			}
			continue
		}
		if got != tt.want {
			t.Errorf("%s (%s):\n got %s\nwant %s", tt.name, d.Kind, got, tt.want)
		}
	}
}
