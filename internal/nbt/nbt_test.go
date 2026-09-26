package nbt

import (
	"bytes"
	"compress/gzip"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func sample() Compound {
	return Compound{
		"Data": Compound{
			"DataVersion": int32(4189),
			"LevelName":   "Mundo de María \u0000 🌍",
			"GameType":    int32(0),
			"hardcore":    int8(1),
			"LastPlayed":  int64(1_760_000_000_000),
			"SpawnAngle":  float32(90.5),
			"BorderSize":  float64(59999968),
			"Version": Compound{
				"Id": int32(4189), "Name": "1.21.4", "Series": "main", "Snapshot": int8(0),
			},
			"DataPacks": Compound{
				"Enabled":  List{Type: TagString, Items: []any{"vanilla", "file/terralith.zip"}},
				"Disabled": List{Type: TagEnd},
			},
			"Player": Compound{
				"UUID": []int32{1, -2, 3, -4},
				"Pos":  List{Type: TagDouble, Items: []any{float64(1), float64(64), float64(-3)}},
			},
			"Heights":  []int64{1 << 40, -1},
			"Icon":     []byte{0, 1, 2, 255},
			"Short":    int16(-300),
			"Nested":   List{Type: TagList, Items: []any{List{Type: TagInt, Items: []any{int32(7)}}}},
			"Children": List{Type: TagCompound, Items: []any{Compound{"a": int8(1)}, Compound{}}},
		},
	}
}

func encode(t testing.TB, root Compound) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := Write(&b, "", root); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestRoundTrip(t *testing.T) {
	want := sample()
	name, got, err := Read(bytes.NewReader(encode(t, want)), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if name != "" || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed the tree:\n got %#v\nwant %#v", got, want)
	}
	data, _ := got.Compound("Data")
	if n, ok := data.Int("DataVersion"); !ok || n != 4189 {
		t.Errorf("Int(DataVersion) = %d, %v", n, ok)
	}
	if hc, ok := data.Bool("hardcore"); !ok || !hc {
		t.Errorf("Bool(hardcore) = %v, %v", hc, ok)
	}
	packs, _ := data.Compound("DataPacks")
	if got := packs.Strings("Enabled"); !reflect.DeepEqual(got, []string{"vanilla", "file/terralith.zip"}) {
		t.Errorf("Strings(Enabled) = %q", got)
	}
	if got := packs.Strings("Disabled"); len(got) != 0 {
		t.Errorf("empty list gave %q", got)
	}
	if got := data.Strings("LevelName"); got != nil {
		t.Errorf("Strings on a string = %q", got)
	}
}

func TestModifiedUTF8(t *testing.T) {
	for s, want := range map[string][]byte{
		"a\x00b": {'a', 0xC0, 0x80, 'b'},
		"é":      {0xC3, 0xA9},
		"€":      {0xE2, 0x82, 0xAC},
		"🌍":      {0xED, 0xA0, 0xBC, 0xED, 0xBC, 0x8D},
	} {
		if got := encodeMUTF8(s); !bytes.Equal(got, want) {
			t.Errorf("encode %q = % x, want % x", s, got, want)
		}
		if got := decodeMUTF8(want); got != s {
			t.Errorf("decode % x = %q, want %q", want, got, s)
		}
	}
	for raw, want := range map[string]string{
		"\xED\xA0\xBC":     "\uFFFD",  // high surrogate alone
		"\xED\xBC\x8Dx":    "\uFFFDx", // low surrogate alone
		"\xC3":             "\uFFFD",  // truncated sequence
		"ok\xFFok":         "ok\uFFFDok",
		"\xE2\x82":         "\uFFFD\uFFFD",
		"plain \x00 zero?": "plain \x00 zero?",
	} {
		if got := decodeMUTF8([]byte(raw)); got != want {
			t.Errorf("decode %q = %q, want %q", raw, got, want)
		}
	}
}

// root wraps a compound payload (without its end tag) in an unnamed root.
func root(payload ...byte) []byte {
	return append(append([]byte{TagCompound, 0, 0}, payload...), TagEnd)
}

func TestReadRefusesMalformed(t *testing.T) {
	for name, in := range map[string][]byte{
		"empty":                   {},
		"root is not a compound":  {TagInt, 0, 0, 0, 0, 0, 1},
		"unknown tag type":        root(13, 0, 1, 'x'),
		"negative array length":   root(TagByteArray, 0, 1, 'a', 0xFF, 0xFF, 0xFF, 0xFF),
		"negative list length":    root(TagList, 0, 1, 'l', TagInt, 0x80, 0, 0, 0),
		"untyped list with items": root(TagList, 0, 1, 'l', TagEnd, 0, 0, 0, 1),
		"list of unknown type":    root(TagList, 0, 1, 'l', 42, 0, 0, 0, 0),
		"missing end tag":         {TagCompound, 0, 0, TagByte, 0, 1, 'b', 1},
		"string cut short":        root(TagString, 0, 1, 's', 0, 10, 'a', 'b'),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := Read(bytes.NewReader(in), DefaultLimits())
			if !errors.Is(err, ErrFormat) {
				t.Fatalf("err = %v, want ErrFormat", err)
			}
		})
	}
}

func TestReadAcceptsEmptyUntypedList(t *testing.T) {
	_, c, err := Read(bytes.NewReader(root(TagList, 0, 1, 'l', TagEnd, 0, 0, 0, 0)), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if l, ok := c["l"].(List); !ok || l.Type != TagEnd || len(l.Items) != 0 {
		t.Fatalf("got %#v", c["l"])
	}
}

func TestTruncatedEverywhere(t *testing.T) {
	full := encode(t, sample())
	for n := 0; n < len(full); n++ {
		if _, _, err := Read(bytes.NewReader(full[:n]), DefaultLimits()); !errors.Is(err, ErrFormat) {
			t.Fatalf("cut at %d of %d: err = %v, want ErrFormat", n, len(full), err)
		}
	}
}

func nestedCompounds(depth int) Compound {
	c := Compound{}
	for i := 1; i < depth; i++ {
		c = Compound{"c": c}
	}
	return c
}

func nestedLists(depth int) Compound {
	l := List{Type: TagEnd}
	for i := 2; i < depth; i++ {
		l = List{Type: TagList, Items: []any{l}}
	}
	return Compound{"l": l}
}

func TestDepthLimit(t *testing.T) {
	lim := Limits{MaxDepth: 8, MaxBytes: 1 << 20}
	for name, build := range map[string]func(int) Compound{"compounds": nestedCompounds, "lists": nestedLists} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Read(bytes.NewReader(encode(t, build(8))), lim); err != nil {
				t.Fatalf("depth 8 with limit 8: %v", err)
			}
			if _, _, err := Read(bytes.NewReader(encode(t, build(9))), lim); !errors.Is(err, ErrTooDeep) {
				t.Fatalf("depth 9 with limit 8: err = %v, want ErrTooDeep", err)
			}
		})
	}
	if _, _, err := Read(bytes.NewReader(encode(t, nestedCompounds(600))), DefaultLimits()); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("depth 600 with the default limit: err = %v, want ErrTooDeep", err)
	}
}

func TestBudget(t *testing.T) {
	lim := Limits{MaxDepth: 16, MaxBytes: 64 << 10}
	huge := []byte{0x40, 0, 0, 0} // 1 GiB of claimed elements
	for name, in := range map[string][]byte{
		"byte array claims 1 GiB": root(append([]byte{TagByteArray, 0, 1, 'a'}, huge...)...),
		"int array claims 4 GiB":  root(append([]byte{TagIntArray, 0, 1, 'a'}, huge...)...),
		"long array claims 8 GiB": root(append([]byte{TagLongArray, 0, 1, 'a'}, huge...)...),
		"list claims 2^31 items":  root(TagList, 0, 1, 'l', TagInt, 0x7F, 0xFF, 0xFF, 0xFF),
		"list of empty compounds": root(append([]byte{TagList, 0, 1, 'l', TagCompound, 0, 0, 0x10, 0}, bytes.Repeat([]byte{TagEnd}, 0x1000)...)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Read(bytes.NewReader(in), lim); !errors.Is(err, ErrTooLarge) {
				t.Fatalf("err = %v, want ErrTooLarge", err)
			}
		})
	}
	many := Compound{}
	for i := 0; i < 2000; i++ {
		many[strings.Repeat("k", 10)+string(rune('a'+i%26))+strings.Repeat("x", i%7)+string(rune(0x100+i))] = strings.Repeat("v", 20)
	}
	if _, _, err := Read(bytes.NewReader(encode(t, many)), lim); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("many strings: err = %v, want ErrTooLarge", err)
	}
	if _, _, err := Read(bytes.NewReader(encode(t, many)), DefaultLimits()); err != nil {
		t.Fatalf("the same tree within the default budget: %v", err)
	}
}

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	zw.Write(b)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestReadGzip(t *testing.T) {
	raw := encode(t, sample())
	good := gz(t, raw)
	if _, c, err := ReadGzip(bytes.NewReader(good), DefaultLimits()); err != nil || c["Data"] == nil {
		t.Fatalf("valid file: %v", err)
	}
	bad := bytes.Clone(good)
	bad[len(bad)-8] ^= 0xFF // the CRC-32 in the gzip trailer
	if _, _, err := ReadGzip(bytes.NewReader(bad), DefaultLimits()); !errors.Is(err, ErrFormat) {
		t.Errorf("wrong checksum: err = %v, want ErrFormat", err)
	}
	if _, _, err := ReadGzip(bytes.NewReader(raw), DefaultLimits()); !errors.Is(err, ErrFormat) {
		t.Errorf("uncompressed input: err = %v, want ErrFormat", err)
	}
	if _, _, err := ReadGzip(bytes.NewReader(good[:len(good)/2]), DefaultLimits()); !errors.Is(err, ErrFormat) {
		t.Errorf("cut in half: err = %v, want ErrFormat", err)
	}
	trailing := gz(t, append(bytes.Clone(raw), make([]byte, maxTrailing+1)...))
	if _, _, err := ReadGzip(bytes.NewReader(trailing), DefaultLimits()); !errors.Is(err, ErrFormat) {
		t.Errorf("over 1 MiB of trailing data: err = %v, want ErrFormat", err)
	}
	some := gz(t, append(bytes.Clone(raw), 0, 0, 0))
	if _, _, err := ReadGzip(bytes.NewReader(some), DefaultLimits()); err != nil {
		t.Errorf("a few trailing bytes: %v", err)
	}
}

func TestWriteRefuses(t *testing.T) {
	for name, c := range map[string]Compound{
		"untyped list with items": {"l": List{Type: TagEnd, Items: []any{int8(1)}}},
		"mixed list":              {"l": List{Type: TagInt, Items: []any{int32(1), int64(2)}}},
		"Go int":                  {"n": 1},
		"string too long":         {"s": strings.Repeat("é", 40000)},
	} {
		t.Run(name, func(t *testing.T) {
			var b bytes.Buffer
			if err := Write(&b, "", c); err == nil {
				t.Fatal("Write accepted it")
			}
		})
	}
}

func FuzzRead(f *testing.F) {
	f.Add(encode(f, sample()))
	f.Add(root(TagList, 0, 1, 'l', TagEnd, 0, 0, 0, 0))
	f.Add(root(TagString, 0, 2, 0xC0, 0x80, 0, 3, 0xED, 0xA0, 0xBC))
	f.Fuzz(func(t *testing.T, in []byte) {
		lim := Limits{MaxDepth: 64, MaxBytes: 1 << 20}
		name, c, err := Read(bytes.NewReader(in), lim)
		if err != nil {
			return
		}
		var once bytes.Buffer
		if err := Write(&once, name, c); err != nil {
			t.Fatalf("decoded tree does not encode: %v", err)
		}
		name2, c2, err := Read(bytes.NewReader(once.Bytes()), lim)
		if err != nil {
			t.Fatalf("re-encoded tree does not decode: %v", err)
		}
		var twice bytes.Buffer
		if err := Write(&twice, name2, c2); err != nil || !bytes.Equal(once.Bytes(), twice.Bytes()) {
			t.Fatalf("encoding is not stable: %v", err)
		}
	})
}
