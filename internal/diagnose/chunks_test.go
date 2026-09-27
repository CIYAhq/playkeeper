package diagnose

import (
	"encoding/binary"
	"testing"
)

// regionHeader lays out a region file's location table: entry i at byte 4i,
// its first sector in the high three bytes and its sector count in the low one.
func regionHeader(size int, entries map[int][2]uint32) []byte {
	b := make([]byte, size)
	for i, e := range entries {
		binary.BigEndian.PutUint32(b[4*i:], e[0]<<8|e[1])
	}
	return b
}

func TestCountChunksReadsOnlyTheLocationTable(t *testing.T) {
	full := regionHeader(8192+3*4096, map[int][2]uint32{0: {2, 1}, 33: {3, 1}, 1023: {4, 1}})
	for i := 4096; i < 8192; i += 4 {
		// Save times, which would all count if read as locations.
		binary.BigEndian.PutUint32(full[i:], uint32(memNow.Unix()))
	}
	for _, tc := range []struct {
		name   string
		header []byte
		want   int
	}{
		{"a whole file with three chunks", full, 3},
		{"just its location table", full[:4096], 3},
		{"entries the game ignores: in the header or with no sectors",
			regionHeader(4096, map[int][2]uint32{1: {0, 1}, 2: {1, 1}, 3: {7, 0}, 4: {9, 2}}), 1},
		{"a truncated table, ending inside an entry",
			regionHeader(4096, map[int][2]uint32{0: {2, 1}, 25: {3, 1}, 1023: {4, 1}})[:102], 1},
		{"an empty file", nil, 0},
	} {
		if got := CountChunks(tc.header); got != tc.want {
			t.Errorf("%s: got %d chunks, want %d", tc.name, got, tc.want)
		}
	}
}
