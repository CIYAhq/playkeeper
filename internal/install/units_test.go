package install

import (
	"strconv"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

func TestAgentUnitCapsItsMemory(t *testing.T) {
	mib := map[string]int{}
	for line := range strings.Lines(agentUnit()) {
		line = strings.TrimSpace(line)
		for key, unit := range map[string]string{"MemoryHigh=": "M", "MemoryMax=": "M", "Environment=GOMEMLIMIT=": "MiB"} {
			v, ok := strings.CutPrefix(line, key)
			if !ok {
				continue
			}
			n, err := strconv.Atoi(strings.TrimSuffix(v, unit))
			if err != nil || !strings.HasSuffix(v, unit) {
				t.Fatalf("%s%s is not a size in %s", key, v, unit)
			}
			mib[key] = n
		}
	}
	soft, high, hard := mib["Environment=GOMEMLIMIT="], mib["MemoryHigh="], mib["MemoryMax="]
	if soft <= 0 || soft >= high || high >= hard || hard > minecraft.HostReserveMB/2 {
		t.Errorf("GOMEMLIMIT %d MiB, MemoryHigh %d MiB, MemoryMax %d MiB: each must be below the next, and MemoryMax at most half the %d MB kept free for the host",
			soft, high, hard, minecraft.HostReserveMB)
	}
}

// The updater may write only the binary, the units, the config and
// Playkeeper's state: the rest of the host is read-only to it.
func TestTheUpdaterWritesOnlyItsOwnPaths(t *testing.T) {
	unit := updateServiceUnit()
	for _, want := range []string{"ProtectSystem=strict\n", "ReadWritePaths=/usr/local/bin /etc/systemd/system /etc/playkeeper /var/lib/playkeeper\n", "ProtectHome=yes\n", "NoNewPrivileges=yes\n"} {
		if !strings.Contains(unit, want) {
			t.Errorf("the updater's unit lacks %q:\n%s", want, unit)
		}
	}
}
