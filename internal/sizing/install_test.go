package sizing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/install"
	"github.com/CIYAhq/playkeeper/internal/sizing"
)

// preflight is what the installer says about memory and disk on a host
// that reports memMB of memory and freeBytes of free disk.
func preflight(t *testing.T, memMB int, freeBytes int64) (memory, disk string) {
	t.Helper()
	sys := install.System{
		Root:       t.TempDir(),
		Run:        func(string, ...string) (string, error) { return "", errors.New("no commands in tests") },
		IsRoot:     func() bool { return true },
		Arch:       func() string { return "amd64" },
		MemTotalMB: func() int { return memMB },
		DiskFree:   func(string) int64 { return freeBytes },
		Listening:  func(int) bool { return false },
		Processes:  func() []string { return nil },
		Docker: func(context.Context) (install.DockerInfo, error) {
			return install.DockerInfo{}, errors.New("no Docker in tests")
		},
	}
	for _, c := range install.Preflight(context.Background(), sys, install.Options{PanelPort: 8443, GamePort: 25565}).Checks {
		switch c.ID {
		case "memory":
			memory = c.Status
		case "disk":
			disk = c.Status
		}
	}
	return memory, disk
}

func TestMinimumIsWhatTheInstallerPasses(t *testing.T) {
	const gb = 1 << 30
	if memory, disk := preflight(t, sizing.ReportedMemoryMB(sizing.MinMemoryGB), sizing.MinFreeDiskGB*gb); memory != "pass" || disk != "pass" {
		t.Errorf("the installer says memory %s and disk %s on the guide's minimum, want pass for both", memory, disk)
	}
	if memory, _ := preflight(t, sizing.ReportedMemoryMB(sizing.MinMemoryGB-1), 0); memory != "fail" {
		t.Errorf("the installer says memory %s on a VPS 1 GB below the guide's minimum, want fail", memory)
	}
	if _, disk := preflight(t, 0, sizing.MinFreeDiskGB*gb-1); disk != "warn" {
		t.Errorf("the installer says disk %s just below the guide's minimum, want warn", disk)
	}
	if install.RecommendedDisk != sizing.MinFreeDiskGB*gb || install.MinMemoryMB > sizing.ReportedMemoryMB(sizing.MinMemoryGB) {
		t.Errorf("installer minimums %d MB and %d bytes, guide %d GB and %d GB", install.MinMemoryMB, install.RecommendedDisk, sizing.MinMemoryGB, sizing.MinFreeDiskGB)
	}
	for _, r := range sizing.Table() {
		if r.MemoryGB < sizing.MinMemoryGB || r.Cores < sizing.MinCores || r.DiskGB < sizing.MinFreeDiskGB {
			t.Errorf("%s: %+v is below the minimum", name(r), r.Machine)
		}
		if memory, _ := preflight(t, sizing.ReportedMemoryMB(r.MemoryGB), 0); memory != "pass" {
			t.Errorf("%s: the installer says memory %s on %d GB, want pass", name(r), memory, r.MemoryGB)
		}
	}
}
