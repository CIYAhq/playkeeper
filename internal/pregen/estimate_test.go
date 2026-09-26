package pregen

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// chunkyCircle counts the chunks Chunky 1.5.3 generates for a circle: a
// chunk counts when its center block lies inside the chunk-aligned circle
// (Circle.isBounding with the shape built by ShapeFactory.getShape).
func chunkyCircle(centerX, centerZ, radius int) int64 {
	rc := int(math.Ceil(float64(radius) / 16))
	ccx, ccz := centerX>>4, centerZ>>4
	sx, sz := float64(ccx<<4+8), float64(ccz<<4+8)
	r := float64((2*rc+1)<<4) / 2
	var n int64
	for x := ccx - rc; x <= ccx+rc; x++ {
		for z := ccz - rc; z <= ccz+rc; z++ {
			if math.Hypot(sx-float64(x<<4+8), sz-float64(z<<4+8)) <= r {
				n++
			}
		}
	}
	return n
}

func TestCircleMatchesChunky(t *testing.T) {
	for _, radius := range []int{16, 17, 100, 640, 1000, 2500, 10_000} {
		for _, c := range [][2]int{{0, 0}, {-37, 1234}, {8, -8}} {
			pl := Plan{World: "world", CenterX: c[0], CenterZ: c[1], Radius: radius, Shape: Circle}
			if got, want := pl.Estimate(Overworld, 4).Chunks, chunkyCircle(c[0], c[1], radius); got != want {
				t.Errorf("circle radius %d at %v: %d chunks, Chunky generates %d", radius, c, got, want)
			}
		}
	}
	for rc := 0; rc <= 200; rc++ {
		var want int64
		for dx := -rc; dx <= rc; dx++ {
			for dz := -rc; dz <= rc; dz++ {
				if dx*dx+dz*dz <= rc*rc+rc {
					want++
				}
			}
		}
		if got := circleChunks(rc); got != want {
			t.Fatalf("circleChunks(%d) = %d, want %d", rc, got, want)
		}
	}
}

func TestEstimate(t *testing.T) {
	for blocks, want := range map[int]int{0: 0, 1: 1, 16: 1, 17: 2, 640: 40, 1000: 63, 2500: 157, 10_000: 625} {
		if got := chunkRadius(blocks); got != want {
			t.Errorf("chunkRadius(%d) = %d, want %d", blocks, got, want)
		}
	}

	// The real Paper log in testdata finished a square of radius 640 at
	// "Processed: 6561 chunks (100.00%)".
	sq := Plan{World: "world", Radius: 640, Shape: Square}.Estimate(Overworld, 2)
	if sq.Total != 6561 || sq.Chunks != 6561 {
		t.Errorf("radius 640 square = %d of %d chunks, want 6561 of 6561", sq.Chunks, sq.Total)
	}
	e := Plan{World: "world", Radius: 2500, Shape: Square}.Estimate(Overworld, 4)
	if e.Total != 99_225 || e.Chunks != 99_225 {
		t.Errorf("radius 2500 square = %d of %d chunks, want 99225", e.Chunks, e.Total)
	}
	c := Plan{World: "world", Radius: 2500, Shape: Circle}.Estimate(Overworld, 4)
	if c.Total != 99_225 || c.Chunks >= c.Total || c.Chunks < c.Total*3/4 {
		t.Errorf("radius 2500 circle = %d of %d chunks, want about π/4 of 99225", c.Chunks, c.Total)
	}

	bytes := 99_225 * 9.82 * 1024
	if math.Abs(float64(e.DiskLow)-bytes*0.7) > 1 || math.Abs(float64(e.DiskHigh)-bytes*1.4) > 1 {
		t.Errorf("disk range = %d..%d, want about %.0f..%.0f", e.DiskLow, e.DiskHigh, bytes*0.7, bytes*1.4)
	}
	if e.SecondsLow != 99_225/(12*4) || e.SecondsHigh != int64(math.Ceil(99_225/(4.0*4))) {
		t.Errorf("time range = %d..%d s", e.SecondsLow, e.SecondsHigh)
	}
	nether := Plan{World: "world_nether", Radius: 2500, Shape: Square}.Estimate(Nether, 4)
	end := Plan{World: "world_the_end", Radius: 2500, Shape: Square}.Estimate(End, 4)
	if !(end.DiskHigh < nether.DiskHigh && nether.DiskHigh < e.DiskHigh) || nether.Chunks != e.Chunks {
		t.Errorf("disk by dimension: overworld %d, nether %d, end %d", e.DiskHigh, nether.DiskHigh, end.DiskHigh)
	}

	one := Plan{World: "world", Radius: 2500, Shape: Square}.Estimate(Overworld, 0)
	many := Plan{World: "world", Radius: 2500, Shape: Square}.Estimate(Overworld, 64)
	eight := Plan{World: "world", Radius: 2500, Shape: Square}.Estimate(Overworld, 8)
	if one.SecondsHigh != int64(math.Ceil(99_225/4.0)) || many != eight {
		t.Errorf("cores: 0 cpus → %d s, 64 cpus %+v vs 8 cpus %+v", one.SecondsHigh, many, eight)
	}

	huge, _ := PresetPlan("huge", "world")
	h := huge.Estimate(Overworld, 2)
	if h.Chunks != 1251*1251 || h.DiskHigh < 20<<30 || h.DiskHigh > 25<<30 || h.SecondsLow <= 0 || h.SecondsHigh <= h.SecondsLow {
		t.Errorf("huge preset estimate = %+v", h)
	}
	largest := Plan{World: "world", Radius: MaxRadius, Shape: Square}.Estimate(Overworld, 1)
	if largest.Chunks != 6251*6251 || largest.DiskHigh <= 0 || largest.SecondsHigh <= 0 {
		t.Errorf("largest plan overflows: %+v", largest)
	}
}

func TestSecondsAt(t *testing.T) {
	e := Estimate{Chunks: 6561}
	if s, ok := e.SecondsAt(41.5); !ok || s != 159 {
		t.Errorf("SecondsAt(41.5) = %d, %v; want 159", s, ok)
	}
	for _, r := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, ok := e.SecondsAt(r); ok {
			t.Errorf("SecondsAt(%v) reported a time", r)
		}
	}
}

func TestCheckDisk(t *testing.T) {
	e := Plan{World: "world", Radius: 5000, Shape: Square}.Estimate(Overworld, 4)
	need := e.DiskHigh + diskReserve
	if err := e.CheckDisk(need); err != nil {
		t.Errorf("CheckDisk(exactly enough) = %v", err)
	}
	err := e.CheckDisk(need - 1)
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != CodeNotEnoughDisk || pe.Params["need"] != need || pe.Params["free"] != need-1 {
		t.Fatalf("CheckDisk(too little) = %#v", err)
	}
	if !strings.Contains(pe.Msg, "GiB") || pe.Hint == "" {
		t.Errorf("message = %q, hint = %q", pe.Msg, pe.Hint)
	}
	if err := e.CheckDisk(-5); err == nil || !strings.Contains(err.Error(), "only 0 B is free") {
		t.Errorf("CheckDisk(negative) = %v", err)
	}
}
