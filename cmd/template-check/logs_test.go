package main

import (
	"slices"
	"strings"
	"testing"
)

func TestReadLog(t *testing.T) {
	r := readLog([]string{
		"[init] Resolving type given PAPER",
		"[11:02:03 INFO]: Enabling CoreProtect v24.1",
		"[11:02:04 ERROR]: [Towny] Could not find an economy plugin, economy is off",
		"[11:02:05 INFO]: Done (12.005s)! For help, type \"help\"",
		"[11:02:09 INFO]: Done (99.0s)! A second one doesn't count",
	})
	if r.done != 12.005 || len(r.failures) != 0 || len(r.errors) != 1 {
		t.Errorf("a clean start with one error line: %+v", r)
	}

	for _, line := range []string{
		"[Server thread/ERROR]: Error occurred while enabling LifeStealZ v2.21.1 (Is it up to date?)",
		"[Server thread/ERROR]: Could not load 'plugins/Skyblock.jar' in folder 'plugins'",
		"org.bukkit.plugin.UnknownDependencyException: Unknown/missing dependency plugins: [Vault]",
		"java.lang.UnsupportedClassVersionError: com/example/Plugin has been compiled by a more recent version",
		"Incompatible mods found!",
		" - Mod 'Ledger' (ledger) 1.3.23 requires version 1.13.9+kotlin.2.3.10 or later of fabric-language-kotlin, which is missing!",
		"Mod resolution encountered an incompatible mod set!",
		"Missing or unsupported mandatory dependencies:",
		"#@!@# Game crashed! Crash report saved to: #@!@# This crash report has been saved to: crash-reports/x.txt",
	} {
		if r := readLog([]string{line}); len(r.failures) != 1 {
			t.Errorf("%q didn't fail the check", line)
		}
	}
	long := readLog([]string{"Error occurred while enabling " + strings.Repeat("x", 500)})
	if !slices.ContainsFunc(long.failures, func(s string) bool { return len(s) < 300 && strings.HasSuffix(s, "…") }) {
		t.Errorf("a long line isn't clipped: %v", long.failures)
	}
}
