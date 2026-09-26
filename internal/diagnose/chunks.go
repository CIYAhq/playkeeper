package diagnose

import "encoding/binary"

// CountChunks counts the chunks a region file (.mca) holds, from its first
// 4 KiB: the location table, one 4-byte entry per chunk. Entries the game
// itself ignores, pointing into the header or at no sectors, are left out; a
// truncated table counts as far as it goes.
//
// NewChunks is the sum over a world's region folders at the end of the window
// minus the sum at its start. Only region folders hold terrain: entities and
// poi folders use the same format for other data. Chunks reach the disk when
// the server saves them, so the count trails the game by a few minutes.
func CountChunks(header []byte) int {
	n := 0
	for i := 0; i+4 <= min(len(header), 4096); i += 4 {
		loc := binary.BigEndian.Uint32(header[i:])
		if loc>>8 >= 2 && loc&0xff > 0 {
			n++
		}
	}
	return n
}
