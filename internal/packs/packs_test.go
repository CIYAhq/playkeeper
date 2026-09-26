package packs

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// wantCode fails unless err is an *Error with code and a message written
// as a sentence.
func wantCode(t *testing.T, err error, code string) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error = %v (%#v), want code %s", err, err, code)
	}
	if e.Msg == "" || sentence(e.Msg) != e.Msg {
		t.Errorf("error %s has message %q, want a sentence", code, e.Msg)
	}
	if e.Hint != "" && sentence(e.Hint) != e.Hint {
		t.Errorf("error %s has hint %q, want a sentence", code, e.Hint)
	}
	return e
}

// entry is an entry of a test zip; edit adjusts its header. An entry with
// raw is written as it is, compressed already, with the sizes and checksum
// edit sets.
type entry struct {
	name string
	body string
	raw  []byte
	edit func(*zip.FileHeader)
}

func file(name, body string) entry { return entry{name: name, body: body} }

func dir(name string) entry { return entry{name: name} }

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// buildZip writes a zip of entries, deflating files unless edit says
// otherwise. Method 12 is written uncompressed, standing for a method
// Minecraft can't read.
func buildZip(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	w.RegisterCompressor(12, func(out io.Writer) (io.WriteCloser, error) { return nopWriteCloser{out}, nil })
	for _, e := range entries {
		fh := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.edit != nil {
			e.edit(fh)
		}
		create, body := w.CreateHeader, e.body
		if e.raw != nil {
			create, body = w.CreateRaw, string(e.raw)
		}
		fw, err := create(fh)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(fw, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const (
	dataMcmeta     = `{"pack": {"description": "A test data pack", "pack_format": 48}}`
	resourceMcmeta = `{"pack": {"description": "A test resource pack", "pack_format": 34}}`
)

func dataPack(t *testing.T, extra ...entry) []byte {
	return buildZip(t, append([]entry{
		file("pack.mcmeta", dataMcmeta),
		file("data/test/function/hello.mcfunction", "say hello\n"),
	}, extra...)...)
}

func resourcePack(t *testing.T, extra ...entry) []byte {
	return buildZip(t, append([]entry{
		file("pack.mcmeta", resourceMcmeta),
		file("assets/minecraft/textures/block/stone.png", "not really a png"),
	}, extra...)...)
}

func inspect(t *testing.T, b []byte, use Kind, lim Limits) (Info, error) {
	t.Helper()
	return Inspect(context.Background(), bytes.NewReader(b), int64(len(b)), use, lim)
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestFormatText(t *testing.T) {
	for _, tc := range []struct {
		f    Format
		text string
	}{
		{Format{81, 0}, "81.0"},
		{Format{97, 1}, "97.1"},
		{Format{94, AnyMinor}, "94.*"},
		{Format{-3, 0}, "-3.0"},
	} {
		if got := tc.f.String(); got != tc.text {
			t.Errorf("%#v.String() = %q, want %q", tc.f, got, tc.text)
		}
		var back Format
		if err := back.UnmarshalText([]byte(tc.text)); err != nil || back != tc.f {
			t.Errorf("UnmarshalText(%q) = %#v, %v, want %#v", tc.text, back, err, tc.f)
		}
	}
	for _, bad := range []string{"", "81", "a.0", "81.x", "81.-1", "81.", ".1", "99999999999.0"} {
		var f Format
		if err := f.UnmarshalText([]byte(bad)); err == nil {
			t.Errorf("UnmarshalText(%q) = %#v, want an error", bad, f)
		}
	}
	b, err := json.Marshal(FormatRange{Format{88, 0}, Format{94, AnyMinor}})
	if err != nil || string(b) != `{"min":"88.0","max":"94.*"}` {
		t.Errorf("FormatRange JSON = %s, %v", b, err)
	}
	var r FormatRange
	if err := json.Unmarshal(b, &r); err != nil || r != (FormatRange{Format{88, 0}, Format{94, AnyMinor}}) {
		t.Errorf("FormatRange from JSON = %#v, %v", r, err)
	}
}

func TestFormatRangeContains(t *testing.T) {
	r := FormatRange{Format{88, 0}, Format{94, AnyMinor}}
	for _, tc := range []struct {
		f    Format
		want bool
	}{
		{Format{87, 5}, false},
		{Format{88, 0}, true},
		{Format{94, 0}, true},
		{Format{94, 12}, true},
		{Format{95, 0}, false},
	} {
		if got := r.Contains(tc.f); got != tc.want {
			t.Errorf("Contains(%s) = %v, want %v", tc.f, got, tc.want)
		}
	}
	single := FormatRange{Format{97, 1}, Format{97, 1}}
	if !single.Contains(Format{97, 1}) || single.Contains(Format{97, 0}) {
		t.Error("a single-format range contains the wrong formats")
	}
}

func TestLimits(t *testing.T) {
	if got := (Limits{}).orDefaults(); got != DefaultLimits() {
		t.Errorf("zero Limits = %+v, want %+v", got, DefaultLimits())
	}
	custom := Limits{MaxBytes: 1, MaxFiles: 2, MaxUnpackedBytes: 3}
	if got := custom.orDefaults(); got != custom {
		t.Errorf("custom Limits became %+v", got)
	}
	big := Limits{MaxBytes: 1 << 40}.orDefaults()
	if big.maxBytes(Resource) != ResourcePackMaxBytes || big.maxBytes(Data) != 1<<40 {
		t.Errorf("maxBytes = %d for resource packs, %d for data packs", big.maxBytes(Resource), big.maxBytes(Data))
	}
}

func TestErrorIs(t *testing.T) {
	err := error(alreadyInstalled("a.zip"))
	if !errors.Is(err, ErrAlreadyInstalled) || errors.Is(err, ErrNotFound) {
		t.Error("errors.Is doesn't match by code")
	}
	inner := errors.New("disk full")
	ff := fileFailed("write the pack", inner)
	if !errors.Is(ff, inner) || ff.Params["detail"] != "disk full" {
		t.Errorf("fileFailed = %#v", ff)
	}
	wantCode(t, ff, CodeFileFailed)
}

func TestTooLarge(t *testing.T) {
	for _, tc := range []struct {
		size, max int64
		use       Kind
		msg       string
	}{
		{300 << 20, ResourcePackMaxBytes, Resource, "The resource pack is 300 MB, but players' games only download resource packs of up to 250 MB."},
		{ResourcePackMaxBytes + 1, ResourcePackMaxBytes, Resource, "The resource pack is larger than 250 MB, the most players' games download."},
		{-1, ResourcePackMaxBytes, Resource, "The resource pack is larger than 250 MB, the most players' games download."},
		{3 << 20, 1 << 20, Resource, "The resource pack is 3.0 MB, more than the 1.0 MB Playkeeper accepts."},
		{3 << 30, 2 << 30, Data, "The data pack is 3.0 GB, more than the 2.0 GB Playkeeper accepts."},
		{-1, 100 << 20, Data, "The data pack is larger than 100 MB, the most Playkeeper accepts."},
	} {
		e := wantCode(t, tooLarge(tc.size, tc.max, tc.use), CodeTooLarge)
		if e.Msg != tc.msg {
			t.Errorf("tooLarge(%d, %d, %s) = %q, want %q", tc.size, tc.max, tc.use, e.Msg, tc.msg)
		}
		if _, ok := e.Params["size"]; ok != (tc.size >= 0) || e.Params["max"] != tc.max {
			t.Errorf("tooLarge params = %v", e.Params)
		}
	}
}

func TestMessageHelpers(t *testing.T) {
	for n, want := range map[int64]string{0: "0 bytes", 1023: "1023 bytes", 1024: "1.0 KB", 1536: "1.5 KB", 10 << 20: "10 MB", ResourcePackMaxBytes: "250 MB", 5 << 40: "5.0 TB"} {
		if got := formatSize(n); got != want {
			t.Errorf("formatSize(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 100000: "100,000", 1234567: "1,234,567", -1234: "-1,234"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q, want %q", n, got, want)
		}
	}
	long := strings.Repeat("a", 50) + strings.Repeat("b", 50)
	if got := shortQuote(long); got != `"`+strings.Repeat("a", 40)+"…"+strings.Repeat("b", 30)+`"` {
		t.Errorf("shortQuote elided to %s", got)
	}
	if got := shortQuote("a\nb"); got != `"a\nb"` {
		t.Errorf("shortQuote(a newline b) = %s", got)
	}
	if got := shortName("bad\xffname"); got != "bad\uFFFDname" {
		t.Errorf("shortName kept invalid UTF-8: %q", got)
	}
	for in, want := range map[string]string{"Done": "Done.", "Done.": "Done.", "Really?": "Really?", "Stop!": "Stop!"} {
		if got := sentence(in); got != want {
			t.Errorf("sentence(%q) = %q", in, got)
		}
	}
	if kindName(Both) == kindName(Data) || kindName("") != "a pack" {
		t.Error("kindName doesn't tell kinds apart")
	}
}
