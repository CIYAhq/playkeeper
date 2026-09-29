// Package region reads and rewrites single chunks of Minecraft's region
// files (.mca), for the crash helper's fix that takes one broken entity, or
// one block entity's data, out of a world.
//
// A region file holds 32 by 32 chunks. Its first 4 KiB say where each chunk's
// data starts, in 4 KiB sectors, and how many sectors it takes; the next 4
// KiB when each was last saved. A chunk's data is a length, a compression
// type and then NBT. Files are small enough to read whole, and a changed one
// is written whole, so a chunk that grows can simply move to the end.
package region

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/CIYAhq/playkeeper/internal/nbt"
)

const (
	sector     = 4096
	headerSize = 2 * sector
	maxSectors = 255
)

// The compression types Minecraft writes; LZ4 (4), the one it may use
// since 1.20.5, and chunks kept in a file of their own aren't read here.
const (
	Gzip         byte = 1
	Zlib         byte = 2
	Uncompressed byte = 3
	lz4          byte = 4
	external     byte = 128
)

var (
	// ErrNoChunk is a chunk the file has never saved.
	ErrNoChunk = errors.New("region: the chunk isn't in the file")
	ErrFormat  = errors.New("region: malformed file")
)

// File is a region file's bytes.
type File struct{ b []byte }

// Parse takes a region file as read. An empty file is one with no chunks.
func Parse(b []byte) (*File, error) {
	if len(b) == 0 {
		return &File{b: make([]byte, headerSize)}, nil
	}
	if len(b) < headerSize {
		return nil, fmt.Errorf("%w: %d bytes, shorter than its header", ErrFormat, len(b))
	}
	return &File{b: b}, nil
}

// Bytes is the file as it is now.
func (f *File) Bytes() []byte { return f.b }

func index(cx, cz int) int { return (cx & 31) + (cz&31)*32 }

// Chunk returns the NBT of chunk cx, cz (world chunk coordinates; the file
// holds those whose region is this one's) and how it was compressed.
func (f *File) Chunk(cx, cz int) (compression byte, data []byte, err error) {
	loc := binary.BigEndian.Uint32(f.b[4*index(cx, cz):])
	off, n := int(loc>>8), int(loc&0xff)
	if off == 0 && n == 0 {
		return 0, nil, ErrNoChunk
	}
	if off < 2 || n == 0 || (off+n)*sector > len(f.b)+sector {
		return 0, nil, fmt.Errorf("%w: chunk %d,%d points outside the file", ErrFormat, cx, cz)
	}
	start := off * sector
	if start+5 > len(f.b) {
		return 0, nil, fmt.Errorf("%w: chunk %d,%d is cut short", ErrFormat, cx, cz)
	}
	length := int(binary.BigEndian.Uint32(f.b[start:]))
	if length < 1 || length > n*sector-4 || start+4+length > len(f.b) {
		return 0, nil, fmt.Errorf("%w: chunk %d,%d has a length of %d bytes in %d sectors", ErrFormat, cx, cz, length, n)
	}
	compression = f.b[start+4]
	payload := f.b[start+5 : start+4+length]
	var r io.Reader
	switch compression {
	case Gzip:
		if r, err = gzip.NewReader(bytes.NewReader(payload)); err != nil {
			return 0, nil, fmt.Errorf("%w: chunk %d,%d: %v", ErrFormat, cx, cz, err)
		}
	case Zlib:
		if r, err = zlib.NewReader(bytes.NewReader(payload)); err != nil {
			return 0, nil, fmt.Errorf("%w: chunk %d,%d: %v", ErrFormat, cx, cz, err)
		}
	case Uncompressed:
		return compression, bytes.Clone(payload), nil
	case lz4:
		return 0, nil, errors.New("region: the chunk is compressed with LZ4, which Playkeeper doesn't write; set region-file-compression=deflate in server.properties")
	default:
		if compression&external != 0 {
			return 0, nil, errors.New("region: the chunk is too big for the region file and is kept in a file of its own")
		}
		return 0, nil, fmt.Errorf("%w: chunk %d,%d has compression type %d", ErrFormat, cx, cz, compression)
	}
	if data, err = io.ReadAll(io.LimitReader(r, 256<<20+1)); err != nil {
		return 0, nil, fmt.Errorf("%w: chunk %d,%d: %v", ErrFormat, cx, cz, err)
	}
	if len(data) > 256<<20 {
		return 0, nil, fmt.Errorf("region: chunk %d,%d is larger than 256 MB", cx, cz)
	}
	return compression, data, nil
}

// SetChunk replaces chunk cx, cz with data, compressed as compression, and
// marks it saved at now. It keeps the chunk's sectors when the data still
// fits in them and moves it to the end of the file when it doesn't.
func (f *File) SetChunk(cx, cz int, compression byte, data []byte, now time.Time) error {
	var payload bytes.Buffer
	switch compression {
	case Gzip:
		w := gzip.NewWriter(&payload)
		if _, err := w.Write(data); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
	case Zlib:
		w := zlib.NewWriter(&payload)
		if _, err := w.Write(data); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
	case Uncompressed:
		payload.Write(data)
	default:
		return fmt.Errorf("region: can't write compression type %d", compression)
	}
	total := 5 + payload.Len()
	need := (total + sector - 1) / sector
	if need > maxSectors {
		return fmt.Errorf("region: chunk %d,%d would take %d sectors, more than a region file holds for one", cx, cz, need)
	}
	i := index(cx, cz)
	loc := binary.BigEndian.Uint32(f.b[4*i:])
	off, n := int(loc>>8), int(loc&0xff)
	if off < 2 || need > n || (off+n)*sector > len(f.b) {
		off, n = (len(f.b)+sector-1)/sector, need
		f.b = append(f.b, make([]byte, off*sector+n*sector-len(f.b))...)
	}
	area := f.b[off*sector : (off+n)*sector]
	clear(area)
	binary.BigEndian.PutUint32(area, uint32(1+payload.Len()))
	area[4] = compression
	copy(area[5:], payload.Bytes())
	binary.BigEndian.PutUint32(f.b[4*i:], uint32(off)<<8|uint32(need))
	binary.BigEndian.PutUint32(f.b[sector+4*i:], uint32(now.Unix()))
	return nil
}

// Decode reads a chunk's NBT.
func Decode(data []byte) (nbt.Compound, error) {
	_, root, err := nbt.Read(bytes.NewReader(data), nbt.Limits{MaxDepth: 512, MaxBytes: 512 << 20})
	return root, err
}

// Encode writes a chunk's NBT.
func Encode(root nbt.Compound) ([]byte, error) {
	var b bytes.Buffer
	err := nbt.Write(&b, "", root)
	return b.Bytes(), err
}

// RemoveEntity takes the entities of type id whose block is x, y, z out of
// an entities chunk, riders included, and returns how many it took. With pos,
// an entity must also be within a hundredth of a block of it, as a crash
// report gives it. Entities riding one that goes stay, in the chunk's own
// list.
func RemoveEntity(chunk nbt.Compound, id string, x, y, z int, pos []float64) int {
	list, ok := chunk["Entities"].(nbt.List)
	if !ok {
		return 0
	}
	match := func(e nbt.Compound) bool {
		got, _ := e.String("id")
		p, ok := e["Pos"].(nbt.List)
		if got != id || !ok || len(p.Items) != 3 {
			return false
		}
		for i, want := range []int{x, y, z} {
			v, ok := p.Items[i].(float64)
			if !ok || int(math.Floor(v)) != want || len(pos) == 3 && math.Abs(v-pos[i]) > 0.0051 {
				return false
			}
		}
		return true
	}
	var freed []any
	var take func(l nbt.List) (nbt.List, int)
	take = func(l nbt.List) (nbt.List, int) {
		out := nbt.List{Type: l.Type}
		n := 0
		for _, it := range l.Items {
			e, ok := it.(nbt.Compound)
			if !ok {
				out.Items = append(out.Items, it)
				continue
			}
			riders, hasRiders := e["Passengers"].(nbt.List)
			if match(e) {
				n++
				if hasRiders {
					freed = append(freed, riders.Items...)
				}
				continue
			}
			if hasRiders {
				kept, m := take(riders)
				n += m
				if m > 0 {
					e["Passengers"] = kept
				}
			}
			out.Items = append(out.Items, e)
		}
		return out, n
	}
	kept, n := take(list)
	if n == 0 {
		return 0
	}
	kept.Items = append(kept.Items, freed...)
	if len(kept.Items) > 0 {
		kept.Type = nbt.TagCompound
	}
	chunk["Entities"] = kept
	return n
}

// RemoveBlockEntity takes the data of the block entity at x, y, z, of type
// id, out of a chunk and returns whether it was there. The block stays; the
// game gives it fresh data when it next needs some.
func RemoveBlockEntity(chunk nbt.Compound, id string, x, y, z int) bool {
	list, ok := chunk["block_entities"].(nbt.List)
	if !ok {
		return false
	}
	out := nbt.List{Type: list.Type}
	found := false
	for _, it := range list.Items {
		e, ok := it.(nbt.Compound)
		if ok {
			got, _ := e.String("id")
			bx, _ := e.Int("x")
			by, _ := e.Int("y")
			bz, _ := e.Int("z")
			if got == id && bx == int64(x) && by == int64(y) && bz == int64(z) {
				found = true
				continue
			}
		}
		out.Items = append(out.Items, it)
	}
	if found {
		chunk["block_entities"] = out
	}
	return found
}
