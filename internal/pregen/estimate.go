package pregen

import (
	"fmt"
	"math"
)

// Estimate is what a plan is expected to cost. Disk and time are ranges
// because terrain and hardware move them by tens of percent.
type Estimate struct {
	// Chunks is how many chunks the area covers. Chunks that already exist
	// are skipped quickly, so an explored world needs less time and disk.
	Chunks int64 `json:"chunks"`
	// Total is the count Chunky's progress percentage is out of: the area's
	// square bounding box (for a circle it passes over the corners without
	// generating them).
	Total int64 `json:"total"`
	// DiskLow and DiskHigh bound the disk the new chunks take, in bytes.
	DiskLow  int64 `json:"diskLow"`
	DiskHigh int64 `json:"diskHigh"`
	// SecondsLow and SecondsHigh bound how long generating the area takes.
	SecondsLow  int64 `json:"secondsLow"`
	SecondsHigh int64 `json:"secondsHigh"`
}

const (
	// Generation speed per CPU core in chunks a second, from a busy server
	// in dense terrain to an idle one; Chunky reports the real rate once a
	// task runs. Generation stops getting faster beyond maxCores.
	slowPerCore = 4.0
	fastPerCore = 12.0
	maxCores    = 8
	// A generated chunk's size varies with terrain around kibPerChunk.
	diskLowFactor  = 0.7
	diskHighFactor = 1.4
	// diskReserve stays free after a task, for backups, logs and players
	// exploring past the pre-generated area.
	diskReserve = 1 << 30
)

// kibPerChunk is the average size of a generated chunk in the region files
// of Minecraft 26.1, in KiB.
func kibPerChunk(d Dimension) float64 {
	switch d {
	case Nether:
		return 6.42
	case End:
		return 4.31
	}
	return 9.82
}

// Estimate works out what generating the plan's area costs in a world of
// kind dim on a machine with cpus cores. Where the area is doesn't matter,
// only its size and shape.
func (pl Plan) Estimate(dim Dimension, cpus int) Estimate {
	rc := chunkRadius(pl.Radius)
	side := int64(2*rc + 1)
	e := Estimate{Total: side * side, Chunks: side * side}
	if pl.Shape == Circle {
		e.Chunks = circleChunks(rc)
	}
	bytes := float64(e.Chunks) * kibPerChunk(dim) * 1024
	e.DiskLow = int64(bytes * diskLowFactor)
	e.DiskHigh = int64(math.Ceil(bytes * diskHighFactor))
	cores := float64(min(max(cpus, 1), maxCores))
	e.SecondsLow = int64(float64(e.Chunks) / (fastPerCore * cores))
	e.SecondsHigh = int64(math.Ceil(float64(e.Chunks) / (slowPerCore * cores)))
	return e
}

// SecondsAt is how long the area takes at rate chunks a second, such as the
// rate Chunky reported for an earlier task on the same server.
func (e Estimate) SecondsAt(rate float64) (int64, bool) {
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return 0, false
	}
	return int64(math.Ceil(float64(e.Chunks) / rate)), true
}

// CheckDisk reports whether free bytes of disk leave room for the upper
// estimate plus a reserve.
func (e Estimate) CheckDisk(free int64) error {
	need := e.DiskHigh + diskReserve
	if free >= need {
		return nil
	}
	return &Error{
		Code:   CodeNotEnoughDisk,
		Params: map[string]any{"need": need, "free": free},
		Msg:    fmt.Sprintf("This area could take up to %s of disk, and the server should keep %s spare, but only %s is free.", humanBytes(e.DiskHigh), humanBytes(diskReserve), humanBytes(max(free, 0))),
		Hint:   "Pick a smaller area, or free up disk space (old backups are often the largest files).",
	}
}

// chunkRadius is Chunky's radius in chunks for a radius in blocks.
func chunkRadius(blocks int) int { return (max(blocks, 0) + 15) / 16 }

// circleChunks counts the chunks Chunky generates for a circle of rc
// chunks: those whose center lies within rc+½ chunks of the center chunk's
// center, i.e. dx²+dz² ≤ rc²+rc.
func circleChunks(rc int) int64 {
	r := int64(rc)
	limit := r*r + r
	var n int64
	for dx := -r; dx <= r; dx++ {
		rem := limit - dx*dx
		dz := int64(math.Sqrt(float64(rem)))
		for dz*dz > rem {
			dz--
		}
		for (dz+1)*(dz+1) <= rem {
			dz++
		}
		n += 2*dz + 1
	}
	return n
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
