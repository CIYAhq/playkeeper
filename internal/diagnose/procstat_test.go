package diagnose

import "testing"

func TestCPUUsageMeasuresStealBetweenSnapshots(t *testing.T) {
	before, err := ParseProcStat([]byte(fixture(t, "procstat/before.txt")))
	if err != nil {
		t.Fatal(err)
	}
	after, err := ParseProcStat([]byte(fixture(t, "procstat/after.txt")))
	if err != nil {
		t.Fatal(err)
	}
	if after.Steal != 761813 || after.IOWait != 90261 {
		t.Fatalf("parsed %+v", after)
	}
	u, ok := after.Since(before)
	if !ok || !near(u.BusyPercent, 53.5) || !near(u.IOWaitPercent, 2.5) || !near(u.StealPercent, 29) {
		t.Errorf("got %+v, %v", u, ok)
	}
	if _, ok := before.Since(after); ok {
		t.Error("counters going backwards (a reboot) must not produce a usage")
	}
	if _, ok := after.Since(after); ok {
		t.Error("no time passing must not produce a usage")
	}
	dipped := after
	dipped.IOWait = before.IOWait - 10
	if u, ok := dipped.Since(before); !ok || u.IOWaitPercent != 0 || u.StealPercent <= 29 {
		t.Errorf("iowait may decrease without a reboot: got %+v, %v", u, ok)
	}
}

func TestParseProcStatAcceptsOldKernelsAndRefusesGarbage(t *testing.T) {
	old, err := ParseProcStat([]byte("cpu  100 2 30 4000\ncpu0 100 2 30 4000\n"))
	if err != nil || old != (CPUTimes{User: 100, Nice: 2, System: 30, Idle: 4000}) {
		t.Errorf("four counters: got %+v, %v", old, err)
	}
	for _, in := range []string{"", "cpu0 1 2 3 4 5 6 7 8\n", "cpu  1 2 3\n", "cpu  1 2 x 4 5 6 7 8\n", "cpu  1 2 -3 4\n"} {
		if got, err := ParseProcStat([]byte(in)); err == nil {
			t.Errorf("%q parsed as %+v", in, got)
		}
	}
}
