// Package nbt reads and writes NBT (Named Binary Tag), the big-endian binary
// format of level.dat and the other Java Edition save files.
//
// Read is meant for untrusted files: it bounds the nesting depth, checks every
// length before allocating, and charges each value against a budget that
// estimates the memory of the decoded tree, as Minecraft's own NbtAccounter
// does. Write exists so tests can build real-format files.
package nbt

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// Tag types, in the order the format numbers them.
const (
	TagEnd byte = iota
	TagByte
	TagShort
	TagInt
	TagLong
	TagFloat
	TagDouble
	TagByteArray
	TagString
	TagList
	TagCompound
	TagIntArray
	TagLongArray
)

// Compound is a TAG_Compound. Values are int8, int16, int32, int64, float32,
// float64, []byte, string, List, Compound, []int32 or []int64.
type Compound map[string]any

// List is a TAG_List; every item has the tag type Type. An empty list may
// have the type TagEnd.
type List struct {
	Type  byte
	Items []any
}

// Limits bound what Read accepts.
type Limits struct {
	// MaxDepth is how deeply compounds and lists may nest; Minecraft stops
	// at 512.
	MaxDepth int
	// MaxBytes is the budget for the estimated memory of the decoded tree.
	// Every value costs at least a few bytes, so it also bounds how much
	// input is read.
	MaxBytes int64
}

func DefaultLimits() Limits { return Limits{MaxDepth: 512, MaxBytes: 16 << 20} }

var (
	ErrTooDeep  = errors.New("nbt: nested too deeply")
	ErrTooLarge = errors.New("nbt: larger than allowed")
	ErrFormat   = errors.New("nbt: malformed data")
)

// Estimated memory per value, close to what NbtAccounter charges.
const (
	costNumber   = 16
	costArray    = 24
	costString   = 36
	costList     = 40
	costCompound = 48
	costEntry    = 36
)

// maxTrailing is how much data after the root compound ReadGzip reads to
// reach the gzip checksum. Minecraft ignores trailing data too.
const maxTrailing = 1 << 20

// Read decodes one named root compound from uncompressed NBT.
func Read(r io.Reader, lim Limits) (name string, root Compound, err error) {
	d := &decoder{lim: lim}
	if br, ok := r.(io.ByteReader); ok {
		d.r, d.br = r, br
	} else {
		b := bufio.NewReader(r)
		d.r, d.br = b, b
	}
	t, err := d.byte()
	if err != nil {
		return "", nil, err
	}
	if t != TagCompound {
		return "", nil, fmt.Errorf("%w: the root tag is type %d, not a compound", ErrFormat, t)
	}
	if name, err = d.string(); err != nil {
		return "", nil, err
	}
	v, err := d.payload(TagCompound, 1)
	if err != nil {
		return "", nil, err
	}
	return name, v.(Compound), nil
}

// ReadGzip decodes a gzip-compressed NBT file such as level.dat and reads to
// the end of the gzip stream, so a damaged file fails its checksum.
func ReadGzip(r io.Reader, lim Limits) (name string, root Compound, err error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return "", nil, fmt.Errorf("%w: not gzip-compressed (%v)", ErrFormat, err)
	}
	defer zr.Close()
	br := bufio.NewReader(zr)
	if name, root, err = Read(br, lim); err != nil {
		if !errors.Is(err, ErrFormat) && !errors.Is(err, ErrTooDeep) && !errors.Is(err, ErrTooLarge) {
			err = fmt.Errorf("%w: %v", ErrFormat, err)
		}
		return "", nil, err
	}
	n, err := io.Copy(io.Discard, io.LimitReader(br, maxTrailing+1))
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrFormat, err)
	}
	if n > maxTrailing {
		return "", nil, fmt.Errorf("%w: more than %d bytes after the root compound", ErrFormat, maxTrailing)
	}
	return name, root, nil
}

type decoder struct {
	r    io.Reader
	br   io.ByteReader
	lim  Limits
	used int64
	buf  [8]byte
}

func (d *decoder) charge(n int64) error {
	if n < 0 || n > d.lim.MaxBytes-d.used {
		return ErrTooLarge
	}
	d.used += n
	return nil
}

func truncated(err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return fmt.Errorf("%w: the data ends early", ErrFormat)
	}
	return err
}

func (d *decoder) byte() (byte, error) {
	b, err := d.br.ReadByte()
	if err != nil {
		return 0, truncated(err)
	}
	return b, nil
}

func (d *decoder) read(n int) ([]byte, error) {
	if _, err := io.ReadFull(d.r, d.buf[:n]); err != nil {
		return nil, truncated(err)
	}
	return d.buf[:n], nil
}

func (d *decoder) length() (int64, error) {
	b, err := d.read(4)
	if err != nil {
		return 0, err
	}
	n := int64(int32(binary.BigEndian.Uint32(b)))
	if n < 0 {
		return 0, fmt.Errorf("%w: negative length %d", ErrFormat, n)
	}
	return n, nil
}

func (d *decoder) string() (string, error) {
	b, err := d.read(2)
	if err != nil {
		return "", err
	}
	n := int64(binary.BigEndian.Uint16(b))
	if err := d.charge(costString + 2*n); err != nil {
		return "", err
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(d.r, raw); err != nil {
		return "", truncated(err)
	}
	return decodeMUTF8(raw), nil
}

func (d *decoder) payload(t byte, depth int) (any, error) {
	switch t {
	case TagByte:
		if err := d.charge(costNumber); err != nil {
			return nil, err
		}
		b, err := d.byte()
		return int8(b), err
	case TagShort:
		if err := d.charge(costNumber); err != nil {
			return nil, err
		}
		b, err := d.read(2)
		if err != nil {
			return nil, err
		}
		return int16(binary.BigEndian.Uint16(b)), nil
	case TagInt, TagFloat:
		if err := d.charge(costNumber); err != nil {
			return nil, err
		}
		b, err := d.read(4)
		if err != nil {
			return nil, err
		}
		u := binary.BigEndian.Uint32(b)
		if t == TagFloat {
			return math.Float32frombits(u), nil
		}
		return int32(u), nil
	case TagLong, TagDouble:
		if err := d.charge(costNumber); err != nil {
			return nil, err
		}
		b, err := d.read(8)
		if err != nil {
			return nil, err
		}
		u := binary.BigEndian.Uint64(b)
		if t == TagDouble {
			return math.Float64frombits(u), nil
		}
		return int64(u), nil
	case TagString:
		return d.string()
	case TagByteArray, TagIntArray, TagLongArray:
		return d.array(t)
	case TagList:
		return d.list(depth)
	case TagCompound:
		return d.compound(depth)
	default:
		return nil, fmt.Errorf("%w: unknown tag type %d", ErrFormat, t)
	}
}

func (d *decoder) array(t byte) (any, error) {
	n, err := d.length()
	if err != nil {
		return nil, err
	}
	size := int64(1)
	switch t {
	case TagIntArray:
		size = 4
	case TagLongArray:
		size = 8
	}
	if n > (math.MaxInt64-costArray)/size {
		return nil, ErrTooLarge
	}
	if err := d.charge(costArray + n*size); err != nil {
		return nil, err
	}
	raw := make([]byte, n*size)
	if _, err := io.ReadFull(d.r, raw); err != nil {
		return nil, truncated(err)
	}
	switch t {
	case TagIntArray:
		out := make([]int32, n)
		for i := range out {
			out[i] = int32(binary.BigEndian.Uint32(raw[4*i:]))
		}
		return out, nil
	case TagLongArray:
		out := make([]int64, n)
		for i := range out {
			out[i] = int64(binary.BigEndian.Uint64(raw[8*i:]))
		}
		return out, nil
	}
	return raw, nil
}

func (d *decoder) list(depth int) (any, error) {
	if depth > d.lim.MaxDepth {
		return nil, ErrTooDeep
	}
	et, err := d.byte()
	if err != nil {
		return nil, err
	}
	n, err := d.length()
	if err != nil {
		return nil, err
	}
	if et > TagLongArray {
		return nil, fmt.Errorf("%w: list of unknown tag type %d", ErrFormat, et)
	}
	if et == TagEnd && n > 0 {
		return nil, fmt.Errorf("%w: list of %d items without a type", ErrFormat, n)
	}
	// Charging every item up front keeps a huge claimed length from looping.
	if err := d.charge(costList + 4*n); err != nil {
		return nil, err
	}
	l := List{Type: et}
	for i := int64(0); i < n; i++ {
		v, err := d.payload(et, depth+1)
		if err != nil {
			return nil, err
		}
		l.Items = append(l.Items, v)
	}
	return l, nil
}

func (d *decoder) compound(depth int) (any, error) {
	if depth > d.lim.MaxDepth {
		return nil, ErrTooDeep
	}
	if err := d.charge(costCompound); err != nil {
		return nil, err
	}
	c := Compound{}
	for {
		t, err := d.byte()
		if err != nil {
			return nil, err
		}
		if t == TagEnd {
			return c, nil
		}
		name, err := d.string()
		if err != nil {
			return nil, err
		}
		if err := d.charge(costEntry); err != nil {
			return nil, err
		}
		v, err := d.payload(t, depth+1)
		if err != nil {
			return nil, err
		}
		c[name] = v
	}
}

// decodeMUTF8 decodes Java's modified UTF-8: NUL is two bytes and characters
// outside the Basic Multilingual Plane are surrogate pairs of three bytes
// each. Malformed bytes become U+FFFD rather than an error, because a bad
// character in a name should not make a world unreadable.
func decodeMUTF8(b []byte) string {
	ascii := true
	for _, c := range b {
		if c == 0 || c >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return string(b)
	}
	var sb strings.Builder
	three := func(i int) (rune, bool) {
		if i+2 < len(b) && b[i]&0xF0 == 0xE0 && b[i+1]&0xC0 == 0x80 && b[i+2]&0xC0 == 0x80 {
			return rune(b[i]&0x0F)<<12 | rune(b[i+1]&0x3F)<<6 | rune(b[i+2]&0x3F), true
		}
		return 0, false
	}
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c < 0x80:
			sb.WriteByte(c)
			i++
		case c&0xE0 == 0xC0 && i+1 < len(b) && b[i+1]&0xC0 == 0x80:
			sb.WriteRune(rune(c&0x1F)<<6 | rune(b[i+1]&0x3F))
			i += 2
		default:
			r, ok := three(i)
			if !ok {
				sb.WriteRune(utf8.RuneError)
				i++
				continue
			}
			i += 3
			if r >= 0xD800 && r < 0xDC00 {
				if lo, ok := three(i); ok && lo >= 0xDC00 && lo < 0xE000 {
					sb.WriteRune(0x10000 + (r-0xD800)<<10 + (lo - 0xDC00))
					i += 3
					continue
				}
			}
			if r >= 0xD800 && r < 0xE000 {
				r = utf8.RuneError
			}
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func encodeMUTF8(s string) []byte {
	out := make([]byte, 0, len(s))
	put3 := func(r rune) {
		out = append(out, byte(0xE0|r>>12), byte(0x80|(r>>6)&0x3F), byte(0x80|r&0x3F))
	}
	for _, r := range s {
		switch {
		case r == 0:
			out = append(out, 0xC0, 0x80)
		case r < 0x80:
			out = append(out, byte(r))
		case r < 0x800:
			out = append(out, byte(0xC0|r>>6), byte(0x80|r&0x3F))
		case r < 0x10000:
			put3(r)
		default:
			r -= 0x10000
			put3(0xD800 + r>>10)
			put3(0xDC00 + r&0x3FF)
		}
	}
	return out
}

// Write encodes root as a named root compound. Keys are written in sorted
// order, so the same tree always gives the same bytes.
func Write(w io.Writer, name string, root Compound) error {
	e := &encoder{w: bufio.NewWriter(w)}
	e.byte(TagCompound)
	e.string(name)
	e.value(root)
	if e.err != nil {
		return e.err
	}
	return e.w.Flush()
}

type encoder struct {
	w   *bufio.Writer
	err error
}

func (e *encoder) byte(b byte) {
	if e.err == nil {
		e.err = e.w.WriteByte(b)
	}
}

func (e *encoder) bytes(b []byte) {
	if e.err == nil {
		_, e.err = e.w.Write(b)
	}
}

func (e *encoder) uint(v uint64, n int) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	e.bytes(b[8-n:])
}

func (e *encoder) string(s string) {
	b := encodeMUTF8(s)
	if len(b) > math.MaxUint16 {
		e.fail(fmt.Errorf("nbt: string of %d bytes is longer than 65535", len(b)))
		return
	}
	e.uint(uint64(len(b)), 2)
	e.bytes(b)
}

func (e *encoder) fail(err error) {
	if e.err == nil {
		e.err = err
	}
}

func (e *encoder) length(n int) {
	if n > math.MaxInt32 {
		e.fail(fmt.Errorf("nbt: %d items is more than a length can hold", n))
		return
	}
	e.uint(uint64(n), 4)
}

// TypeOf returns the tag type Write uses for v, or false for a Go type NBT
// has no tag for.
func TypeOf(v any) (byte, bool) {
	switch v.(type) {
	case int8:
		return TagByte, true
	case int16:
		return TagShort, true
	case int32:
		return TagInt, true
	case int64:
		return TagLong, true
	case float32:
		return TagFloat, true
	case float64:
		return TagDouble, true
	case []byte:
		return TagByteArray, true
	case string:
		return TagString, true
	case List:
		return TagList, true
	case Compound:
		return TagCompound, true
	case []int32:
		return TagIntArray, true
	case []int64:
		return TagLongArray, true
	}
	return 0, false
}

func (e *encoder) value(v any) {
	switch x := v.(type) {
	case int8:
		e.byte(byte(x))
	case int16:
		e.uint(uint64(uint16(x)), 2)
	case int32:
		e.uint(uint64(uint32(x)), 4)
	case int64:
		e.uint(uint64(x), 8)
	case float32:
		e.uint(uint64(math.Float32bits(x)), 4)
	case float64:
		e.uint(math.Float64bits(x), 8)
	case []byte:
		e.length(len(x))
		e.bytes(x)
	case string:
		e.string(x)
	case List:
		if x.Type == TagEnd && len(x.Items) > 0 {
			e.fail(errors.New("nbt: a list with items needs a type"))
			return
		}
		e.byte(x.Type)
		e.length(len(x.Items))
		for _, it := range x.Items {
			if t, ok := TypeOf(it); !ok || t != x.Type {
				e.fail(fmt.Errorf("nbt: list of type %d holds a %T", x.Type, it))
				return
			}
			e.value(it)
		}
	case Compound:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t, ok := TypeOf(x[k])
			if !ok {
				e.fail(fmt.Errorf("nbt: %q holds a %T, which has no tag type", k, x[k]))
				return
			}
			e.byte(t)
			e.string(k)
			e.value(x[k])
		}
		e.byte(TagEnd)
	case []int32:
		e.length(len(x))
		for _, n := range x {
			e.uint(uint64(uint32(n)), 4)
		}
	case []int64:
		e.length(len(x))
		for _, n := range x {
			e.uint(uint64(n), 8)
		}
	default:
		e.fail(fmt.Errorf("nbt: %T has no tag type", v))
	}
}

// Compound returns the compound stored under key.
func (c Compound) Compound(key string) (Compound, bool) {
	v, ok := c[key].(Compound)
	return v, ok
}

// String returns the string stored under key.
func (c Compound) String(key string) (string, bool) {
	v, ok := c[key].(string)
	return v, ok
}

// Int returns the integer stored under key, whatever its width.
func (c Compound) Int(key string) (int64, bool) {
	switch v := c[key].(type) {
	case int8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	}
	return 0, false
}

// Bool returns a flag Minecraft stores as a byte: anything but 0 is true.
func (c Compound) Bool(key string) (value, ok bool) {
	v, ok := c.Int(key)
	return v != 0, ok
}

// Strings returns the strings of the list stored under key, or nil when
// key does not hold a list of strings.
func (c Compound) Strings(key string) []string {
	l, ok := c[key].(List)
	if !ok || l.Type != TagString {
		return nil
	}
	out := make([]string, 0, len(l.Items))
	for _, it := range l.Items {
		out = append(out, it.(string))
	}
	return out
}

// IntArray returns the TAG_Int_Array stored under key.
func (c Compound) IntArray(key string) ([]int32, bool) {
	v, ok := c[key].([]int32)
	return v, ok
}
