// Package zipdirtest builds zips crafted against archive/zip, for tests of
// code that reads zips someone else made.
package zipdirtest

import "encoding/binary"

// Bomb is a zip that is only n empty 46-byte entries in its table of
// contents, which archive/zip holds in memory at about 200 bytes each.
// Without zip64 its end record declares n modulo 65,536 entries, which is
// all archive/zip checks; with zip64 its zip64 end record declares n. n
// must not be 65,535 modulo 65,536, which would mean zip64.
func Bomb(n int, zip64 bool) []byte {
	le := binary.LittleEndian
	dirSize := uint64(n) * 46
	b := make([]byte, 0, dirSize+98)
	var rec [46]byte
	le.PutUint32(rec[:], 0x02014b50)
	for range n {
		b = append(b, rec[:]...)
	}
	var end [22]byte
	le.PutUint32(end[:], 0x06054b50)
	if !zip64 {
		le.PutUint16(end[8:], uint16(n))
		le.PutUint16(end[10:], uint16(n))
		le.PutUint32(end[12:], uint32(dirSize))
		return append(b, end[:]...)
	}
	var rec64 [56]byte
	le.PutUint32(rec64[:], 0x06064b50)
	le.PutUint64(rec64[4:], 44)
	le.PutUint16(rec64[12:], 45)
	le.PutUint16(rec64[14:], 45)
	le.PutUint64(rec64[24:], uint64(n))
	le.PutUint64(rec64[32:], uint64(n))
	le.PutUint64(rec64[40:], dirSize)
	var loc [20]byte
	le.PutUint32(loc[:], 0x07064b50)
	le.PutUint64(loc[8:], dirSize)
	le.PutUint32(loc[16:], 1)
	le.PutUint16(end[8:], 0xffff)
	le.PutUint16(end[10:], 0xffff)
	le.PutUint32(end[12:], 0xffffffff)
	le.PutUint32(end[16:], 0xffffffff)
	return append(append(append(b, rec64[:]...), loc[:]...), end[:]...)
}
