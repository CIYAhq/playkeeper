package sizing

import (
	"slices"
	"testing"
)

func TestMachinesAreTheSmallestThatFit(t *testing.T) {
	for _, r := range Table() {
		name := r.Band.Label() + " " + string(r.Workload)
		i := slices.Index(memorySizesGB, r.MemoryGB)
		switch {
		case i < 0:
			t.Errorf("%s: %d GB is not a common VPS size", name, r.MemoryGB)
		case !fits(r.MemoryGB, r.BudgetMB):
			t.Errorf("%s: a %d MB budget doesn't fit %d GB", name, r.BudgetMB, r.MemoryGB)
		case i > 0 && fits(memorySizesGB[i-1], r.BudgetMB):
			t.Errorf("%s: %d GB would already fit a %d MB budget, not only %d GB", name, memorySizesGB[i-1], r.BudgetMB, r.MemoryGB)
		}
		p, err := profileFor(r.Workload)
		if err != nil {
			t.Fatal(err)
		}
		need := systemDiskGB + p.serverDiskGB(r.Band)
		j := slices.Index(diskSizesGB, r.DiskGB)
		switch {
		case j < 0:
			t.Errorf("%s: %d GB is not a common disk size", name, r.DiskGB)
		case float64(r.DiskGB) < need:
			t.Errorf("%s: %d GB of disk is less than the %.1f GB needed", name, r.DiskGB, need)
		case j > 0 && float64(diskSizesGB[j-1]) >= need:
			t.Errorf("%s: %d GB of disk would do, not only %d GB", name, diskSizesGB[j-1], r.DiskGB)
		}
		if !slices.Contains(coreSizes, r.Cores) {
			t.Errorf("%s: %d cores is not a common VPS size", name, r.Cores)
		}
	}
}

func TestSizesGoPastTheLastCommonOne(t *testing.T) {
	for _, c := range []struct {
		need float64
		want int
	}{{0, 40}, {40, 40}, {40.5, 80}, {640, 640}, {641, 800}, {960, 960}, {961, 1120}} {
		if got := roundUp(c.need, diskSizesGB, 160); got != c.want {
			t.Errorf("roundUp(%v) = %d, want %d", c.need, got, c.want)
		}
	}
	const budgetMB = 100_000
	gb := memoryGBFor(budgetMB)
	if !fits(gb, budgetMB) || fits(gb-32, budgetMB) || gb%32 != 0 {
		t.Errorf("memoryGBFor(%d) = %d GB, want the smallest multiple of 32 GB past 64 GB that fits", budgetMB, gb)
	}
}
