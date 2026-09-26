package diagnose

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// CPUTimes are the machine-wide CPU counters from the "cpu" line of
// /proc/stat, in clock ticks since boot. Guest time is already counted in
// User and Nice, so it is left out.
type CPUTimes struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal uint64
}

// ParseProcStat reads the aggregate "cpu" line of /proc/stat.
func ParseProcStat(data []byte) (CPUTimes, error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || fields[0] != "cpu" {
			continue
		}
		if len(fields) < 5 {
			return CPUTimes{}, fmt.Errorf("cpu line has %d counters, want at least 4", len(fields)-1)
		}
		var v [8]uint64
		for i := 0; i < len(v) && i+1 < len(fields); i++ {
			n, err := strconv.ParseUint(fields[i+1], 10, 64)
			if err != nil {
				return CPUTimes{}, fmt.Errorf("cpu counter %d is not a number: %q", i+1, fields[i+1])
			}
			v[i] = n
		}
		return CPUTimes{User: v[0], Nice: v[1], System: v[2], Idle: v[3], IOWait: v[4], IRQ: v[5], SoftIRQ: v[6], Steal: v[7]}, nil
	}
	return CPUTimes{}, errors.New("no aggregate cpu line in /proc/stat")
}

// CPUUsage is how the machine's processor time was spent between two
// snapshots, in percent of all CPU time (every core counts).
type CPUUsage struct {
	BusyPercent   float64 `json:"busy_percent"`
	IOWaitPercent float64 `json:"iowait_percent"`
	// StealPercent is time the hypervisor gave to other virtual machines
	// while this one wanted to run: an overbooked host.
	StealPercent float64 `json:"steal_percent"`
}

// Since returns the usage between an earlier snapshot and t. ok is false when
// a counter went backwards (the machine rebooted) or no time passed.
func (t CPUTimes) Since(prev CPUTimes) (u CPUUsage, ok bool) {
	var busy, idle, steal float64
	for _, c := range []struct {
		cur, old uint64
		sum      *float64
	}{
		{t.User, prev.User, &busy}, {t.Nice, prev.Nice, &busy}, {t.System, prev.System, &busy},
		{t.IRQ, prev.IRQ, &busy}, {t.SoftIRQ, prev.SoftIRQ, &busy}, {t.Idle, prev.Idle, &idle}, {t.Steal, prev.Steal, &steal},
	} {
		if c.cur < c.old {
			return CPUUsage{}, false
		}
		*c.sum += float64(c.cur - c.old)
	}
	// The kernel documents that iowait can decrease; that is not a reboot.
	var iowait float64
	if t.IOWait > prev.IOWait {
		iowait = float64(t.IOWait - prev.IOWait)
	}
	total := busy + idle + steal + iowait
	if total == 0 {
		return CPUUsage{}, false
	}
	return CPUUsage{
		BusyPercent:   busy / total * 100,
		IOWaitPercent: iowait / total * 100,
		StealPercent:  steal / total * 100,
	}, true
}
