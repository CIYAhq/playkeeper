// Package zipdir checks a zip's central directory, its table of contents,
// before archive/zip reads it. archive/zip reads entries until one is
// malformed and compares their number with the declared one only modulo
// 65,536, so a crafted zip of a few megabytes could make it hold millions
// of entries in memory.
package zipdir

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Limits bounds a central directory. archive/zip holds a few times its
// size in memory.
type Limits struct {
	Bytes   int64 // the directory's size
	Entries int   // the entries it lists
}

// Metadata suits a zip read only for a few small files, such as the
// descriptor of a plugin or mod jar.
var Metadata = Limits{Bytes: 16 << 20, Entries: 100_000}

// Problem is what Check found wrong with a zip.
type Problem int

const (
	NotZip         Problem = iota + 1 // no end record: not a zip, or cut short
	Split                             // one part of a zip split into several files
	TooManyEntries                    // it declares more entries than the limit
	TooLarge                          // its directory is larger than the limit
	Misplaced                         // its directory isn't where its end records say, or doesn't end at them
	Malformed                         // an entry in its directory is malformed
	MoreEntries                       // its directory lists more entries than it declares
	FewerEntries                      // its directory lists fewer entries than it declares
	Zip64Outside                      // its zip64 end record is outside the file
	Zip64Malformed                    // its zip64 end record is malformed
)

// Error is a zip Check refused.
type Error struct {
	Problem Problem
	Entries uint64 // the entries the zip declares, for TooManyEntries
}

func (e *Error) Error() string {
	switch e.Problem {
	case NotZip:
		return "The file isn't a zip file."
	case Split:
		return "The zip is one part of a zip split into several files."
	case TooManyEntries:
		return fmt.Sprintf("The zip declares %d entries, more than Playkeeper reads.", e.Entries)
	case TooLarge:
		return "The zip's table of contents is larger than Playkeeper reads."
	case Misplaced:
		return "The zip's table of contents isn't where the zip says it is."
	case Malformed:
		return "The zip's table of contents is malformed."
	case MoreEntries:
		return "The zip's table of contents lists more entries than it declares."
	case FewerEntries:
		return "The zip's table of contents lists fewer entries than it declares."
	case Zip64Outside:
		return "The zip's zip64 end record is outside the file."
	case Zip64Malformed:
		return "The zip's zip64 end record is malformed."
	}
	return fmt.Sprintf("The zip's table of contents can't be read (problem %d).", e.Problem)
}

const (
	endLen             = 22
	end64LocLen        = 20
	end64Len           = 56
	headerLen          = 46
	headerSignature    = 0x02014b50
	end64LocSignature  = 0x07064b50
	end64EndSignature  = 0x06064b50
	maxEndSearchLength = 65 * 1024
)

// Check finds a zip's central directory as archive/zip does and checks it
// within lim before archive/zip reads it: the directory must end where the
// zip's end records start and hold exactly the declared number of entries,
// which Check returns. A zip it refuses gives an *Error, a failed read the
// read's error. It reads the directory once, keeping only one entry at a
// time in memory.
func Check(r io.ReaderAt, size int64, lim Limits) (int, error) {
	if size < endLen {
		return 0, &Error{Problem: NotZip}
	}
	tail := make([]byte, min(size, maxEndSearchLength))
	if err := readFull(r, tail, size-int64(len(tail))); err != nil {
		return 0, err
	}
	p := -1
	for i := len(tail) - endLen; i >= 0; i-- {
		if tail[i] == 'P' && tail[i+1] == 'K' && tail[i+2] == 5 && tail[i+3] == 6 {
			p = i
			break
		}
	}
	if p < 0 || p+endLen+int(le16(tail[p+20:])) > len(tail) {
		return 0, &Error{Problem: NotZip}
	}
	end := size - int64(len(tail)) + int64(p)
	eocd := tail[p:]
	disk, dirDisk := uint64(le16(eocd[4:])), uint64(le16(eocd[6:]))
	recordsHere, records := uint64(le16(eocd[8:])), uint64(le16(eocd[10:]))
	dirSize, dirOffset := uint64(le32(eocd[12:])), uint64(le32(eocd[16:]))

	if records == 0xffff || dirSize == 0xffff || dirOffset == 0xffffffff {
		if loc := end - end64LocLen; loc >= 0 {
			var lb [end64LocLen]byte
			if err := readFull(r, lb[:], loc); err != nil {
				return 0, err
			}
			at := int64(le64(lb[8:]))
			if le32(lb[:]) == end64LocSignature && le32(lb[4:]) == 0 && le32(lb[16:]) == 1 && at >= 0 {
				if at > size-end64Len {
					return 0, &Error{Problem: Zip64Outside}
				}
				var eb [end64Len]byte
				if err := readFull(r, eb[:], at); err != nil {
					return 0, err
				}
				if le32(eb[:]) != end64EndSignature {
					return 0, &Error{Problem: Zip64Malformed}
				}
				end = at
				disk, dirDisk = uint64(le32(eb[16:])), uint64(le32(eb[20:]))
				recordsHere, records = le64(eb[24:]), le64(eb[32:])
				dirSize, dirOffset = le64(eb[40:]), le64(eb[48:])
			}
		}
	}

	switch {
	case disk != 0 || dirDisk != 0 || recordsHere != records:
		return 0, &Error{Problem: Split}
	case records > uint64(max(lim.Entries, 0)):
		return 0, &Error{Problem: TooManyEntries, Entries: records}
	case dirSize > uint64(max(lim.Bytes, 0)):
		return 0, &Error{Problem: TooLarge}
	case dirOffset > uint64(end) || dirSize != uint64(end)-dirOffset:
		return 0, &Error{Problem: Misplaced}
	}

	br := bufio.NewReader(io.NewSectionReader(r, int64(dirOffset), int64(dirSize)))
	var hdr [headerLen]byte
	var n uint64
	for left := int64(dirSize); left > 0; n++ {
		if n == records {
			return 0, &Error{Problem: MoreEntries}
		}
		if left < headerLen {
			return 0, &Error{Problem: Malformed}
		}
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return 0, err
		}
		if le32(hdr[:]) != headerSignature {
			return 0, &Error{Problem: Malformed}
		}
		rest := int64(le16(hdr[28:])) + int64(le16(hdr[30:])) + int64(le16(hdr[32:]))
		if left -= headerLen + rest; left < 0 {
			return 0, &Error{Problem: Malformed}
		}
		if _, err := br.Discard(int(rest)); err != nil {
			return 0, err
		}
	}
	if n != records {
		return 0, &Error{Problem: FewerEntries}
	}
	return int(records), nil
}

// readFull reads len(b) bytes at off: io.ReaderAt lets a complete read end
// with io.EOF.
func readFull(r io.ReaderAt, b []byte, off int64) error {
	n, err := r.ReadAt(b, off)
	if n == len(b) {
		return nil
	}
	if err == nil || errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return err
}

func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
func le64(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }
