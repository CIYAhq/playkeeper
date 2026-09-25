package qrcode

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"image"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/common/reedsolomon"
	zxqrcode "github.com/makiuchi-d/gozxing/qrcode"
	"github.com/makiuchi-d/gozxing/qrcode/decoder"
	"github.com/makiuchi-d/gozxing/qrcode/encoder"
)

// These tests hold the encoder to two independent implementations: ZXing's Go
// port (github.com/makiuchi-d/gozxing, MIT licence, used only in tests) and
// segno (Python, BSD licence), whose matrices are in testdata. The format and
// version information are also checked against the standard's own tables,
// because decoders silently correct a few wrong bits there.

const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

// uris are otpauth:// links from the shortest username the panel allows to the
// longest: 32 characters that take 9 bytes each once escaped.
func uris() []string {
	var out []string
	for _, account := range []string{"sam", "alice", "minecraft-admin_01", "alice%40mc.example.com", strings.Repeat("x", 32), strings.Repeat("%E7%AE%A1", 32)} {
		out = append(out, "otpauth://totp/Playkeeper:"+account+"?secret="+secret+"&issuer=Playkeeper")
	}
	return out
}

// text is n bytes of link-like ASCII, lower case so ZXing uses byte mode too.
func text(n int) string {
	const filler = "otpauth://totp/Playkeeper:abcdefghijklmnopqrstuvwxyz0123456789-._~"
	return strings.Repeat(filler, n/len(filler)+1)[:n]
}

func TestVersionTablesMatchZXing(t *testing.T) {
	for v := 1; v <= 40; v++ {
		zv, err := decoder.Version_GetVersionForNumber(v)
		if err != nil {
			t.Fatal(err)
		}
		ecb := zv.GetECBlocksForLevel(decoder.ErrorCorrectionLevel_M)
		var lens []int
		for _, g := range ecb.GetECBlocks() {
			for range g.GetCount() {
				lens = append(lens, g.GetDataCodewords())
			}
		}
		if got, want := totalCodewords(v), zv.GetTotalCodewords(); got != want {
			t.Errorf("version %d: %d codewords, want %d", v, got, want)
		}
		if got, want := eccPerBlock[v], ecb.GetECCodewordsPerBlock(); got != want {
			t.Errorf("version %d: %d error correction codewords per block, want %d", v, got, want)
		}
		if got := blockLens(v); !slices.Equal(got, lens) {
			t.Errorf("version %d: blocks of %v data codewords, want %v", v, got, lens)
		}
		if got, want := alignment(v), zv.GetAlignmentPatternCenters(); !slices.Equal(got, want) {
			t.Errorf("version %d: alignment patterns at %v, want %v", v, got, want)
		}
		if got, want := newMatrix(v).size, zv.GetDimensionForVersion(); got != want {
			t.Errorf("version %d: %d modules wide, want %d", v, got, want)
		}
	}
}

func TestFormatAndVersionInformationMatchTheStandard(t *testing.T) {
	// ISO/IEC 18004 annex C, level M, masks 0 to 7.
	for mask, want := range []int{0x5412, 0x5125, 0x5e7c, 0x5b4b, 0x45f9, 0x40ce, 0x4f97, 0x4aa0} {
		if got := formatBits(mask); got != want {
			t.Errorf("format information for mask %d = %015b, want %015b", mask, got, want)
		}
	}
	// Annex D, versions 7 to 40.
	for i, want := range []int{
		0x07c94, 0x085bc, 0x09a99, 0x0a4d3, 0x0bbf6, 0x0c762, 0x0d847, 0x0e60d, 0x0f928, 0x10b78,
		0x1145d, 0x12a17, 0x13532, 0x149a6, 0x15683, 0x168c9, 0x177ec, 0x18ec4, 0x191e1, 0x1afab,
		0x1b08e, 0x1cc1a, 0x1d33f, 0x1ed75, 0x1f250, 0x209d5, 0x216f0, 0x228ba, 0x2379f, 0x24b0b,
		0x2542e, 0x26a64, 0x27541, 0x28c69,
	} {
		if got := versionBits(i + 7); got != want {
			t.Errorf("version information for version %d = %018b, want %018b", i+7, got, want)
		}
	}
}

func TestReedSolomonMatchesZXing(t *testing.T) {
	// ISO/IEC 18004 annex I: "01234567" as version 1-M.
	data := []byte{0x10, 0x20, 0x0c, 0x56, 0x61, 0x80, 0xec, 0x11, 0xec, 0x11, 0xec, 0x11, 0xec, 0x11, 0xec, 0x11}
	want := []byte{0xa5, 0x24, 0xd4, 0xc1, 0xed, 0x36, 0xc7, 0x87, 0x2c, 0x55}
	if got := rsRemainder(data, rsGenerator(10)); !bytes.Equal(got, want) {
		t.Errorf("annex I error correction = % x, want % x", got, want)
	}
	enc := reedsolomon.NewReedSolomonEncoder(reedsolomon.GenericGF_QR_CODE_FIELD_256)
	seed := uint32(1)
	for _, degree := range []int{10, 16, 18, 22, 24, 26, 28, 30} {
		for n := 1; n <= 125; n += 31 {
			data := make([]byte, n)
			ints := make([]int, n+degree)
			for i := range data {
				seed = seed*1664525 + 1013904223
				data[i] = byte(seed >> 24)
				ints[i] = int(data[i])
			}
			if err := enc.Encode(ints, degree); err != nil {
				t.Fatal(err)
			}
			for i, c := range rsRemainder(data, rsGenerator(degree)) {
				if int(c) != ints[n+i] {
					t.Errorf("degree %d, %d data codewords: error correction differs from ZXing at %d", degree, n, i)
					break
				}
			}
		}
	}
}

// differ describes the first module where c and a ZXing matrix differ.
func differ(c *Code, m *encoder.ByteMatrix) string {
	if m.GetWidth() != c.size || m.GetHeight() != c.size {
		return fmt.Sprintf("%d modules wide, ZXing %d", c.size, m.GetWidth())
	}
	for y := range c.size {
		for x := range c.size {
			if c.Dark(x, y) != (m.Get(x, y) == 1) {
				return fmt.Sprintf("module (%d, %d) differs from ZXing", x, y)
			}
		}
	}
	return ""
}

func TestMatchesZXingForEveryVersionAndMask(t *testing.T) {
	for v := 1; v <= 40; v++ {
		for _, n := range []int{capacity(v), capacity(v) / 2} {
			s := text(n)
			for mask := range 8 {
				zc, err := encoder.Encoder_encode(s, decoder.ErrorCorrectionLevel_M, map[gozxing.EncodeHintType]interface{}{
					gozxing.EncodeHintType_QR_VERSION:      v,
					gozxing.EncodeHintType_QR_MASK_PATTERN: mask,
				})
				if err != nil {
					t.Fatalf("ZXing, version %d, %d bytes: %v", v, n, err)
				}
				if d := differ(build([]byte(s), v, mask), zc.GetMatrix()); d != "" {
					t.Errorf("version %d, %d bytes, mask %d: %s", v, n, mask, d)
				}
			}
		}
	}
}

func TestPicksTheSameVersionAndMaskAsZXing(t *testing.T) {
	inputs := append(uris(), "", "a", text(capacity(9)), text(capacity(9)+1), text(capacity(27)), text(MaxLen))
	for _, s := range inputs {
		c, err := Encode(s)
		if err != nil {
			t.Fatalf("Encode(%d bytes): %v", len(s), err)
		}
		zc, err := encoder.Encoder_encode(s, decoder.ErrorCorrectionLevel_M, nil)
		if err != nil {
			t.Fatalf("ZXing, %d bytes: %v", len(s), err)
		}
		if got, want := c.version, zc.GetVersion().GetVersionNumber(); got != want {
			t.Errorf("%d bytes: version %d, ZXing %d", len(s), got, want)
		}
		if got, want := c.mask, zc.GetMaskPattern(); got != want {
			t.Errorf("%d bytes: mask %d, ZXing %d", len(s), got, want)
		}
		if d := differ(c, zc.GetMatrix()); d != "" {
			t.Errorf("%d bytes: %s", len(s), d)
		}
	}
}

func TestPicksTheSmallestVersion(t *testing.T) {
	if got := capacity(40); got != MaxLen {
		t.Fatalf("version 40 holds %d bytes, MaxLen says %d", got, MaxLen)
	}
	quick := map[gozxing.EncodeHintType]interface{}{gozxing.EncodeHintType_QR_MASK_PATTERN: 0}
	for v := 1; v <= 40; v++ {
		for _, tc := range []struct{ n, want int }{{capacity(v), v}, {capacity(v) + 1, v + 1}} {
			if tc.want > 40 {
				continue
			}
			s := text(tc.n)
			c, err := Encode(s)
			if err != nil || c.version != tc.want || c.Size() != 17+4*tc.want {
				t.Errorf("%d bytes: %v, want version %d", tc.n, err, tc.want)
				continue
			}
			zc, err := encoder.Encoder_encode(s, decoder.ErrorCorrectionLevel_M, quick)
			if err != nil || zc.GetVersion().GetVersionNumber() != tc.want {
				t.Errorf("%d bytes: ZXing disagrees (%v), want version %d", tc.n, err, tc.want)
			}
		}
	}
	_, err := Encode(text(MaxLen + 1))
	if want := "The text is 2332 bytes long, but a QR code holds at most 2331."; err == nil || err.Error() != want {
		t.Errorf("too long: %v, want %q", err, want)
	}
	c, err := Encode("")
	if err != nil || c.version != 1 {
		t.Fatalf("empty text: %v", err)
	}
	if !c.Dark(0, 0) || c.Dark(-1, 0) || c.Dark(0, -1) || c.Dark(21, 0) || c.Dark(0, 21) {
		t.Error("the finder corner is dark and everything outside the code is light")
	}
}

func bitMatrix(c *Code) *gozxing.BitMatrix {
	m, _ := gozxing.NewSquareBitMatrix(c.size)
	for y := range c.size {
		for x := range c.size {
			if c.Dark(x, y) {
				m.Set(x, y)
			}
		}
	}
	return m
}

// decodeStrict reads c with ZXing's decoder and fails unless the version,
// level and mask read back as encoded, every block is a valid Reed–Solomon
// codeword as read (so error correction cannot hide a mistake) and the code
// reads the right way round rather than mirrored.
func decodeStrict(t *testing.T, c *Code) string {
	t.Helper()
	p, err := decoder.NewBitMatrixParser(bitMatrix(c))
	if err != nil {
		t.Fatal(err)
	}
	ver, err := p.ReadVersion()
	if err != nil {
		t.Fatal(err)
	}
	format, err := p.ReadFormatInformation()
	if err != nil {
		t.Fatal(err)
	}
	if ver.GetVersionNumber() != c.version || format.GetErrorCorrectionLevel() != decoder.ErrorCorrectionLevel_M || int(format.GetDataMask()) != c.mask {
		t.Fatalf("read version %d, level %v, mask %d; want %d, M, %d", ver.GetVersionNumber(), format.GetErrorCorrectionLevel(), format.GetDataMask(), c.version, c.mask)
	}
	codewords, err := p.ReadCodewords()
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := decoder.DataBlock_GetDataBlocks(codewords, ver, decoder.ErrorCorrectionLevel_M)
	if err != nil {
		t.Fatal(err)
	}
	rs := reedsolomon.NewReedSolomonDecoder(reedsolomon.GenericGF_QR_CODE_FIELD_256)
	for i, b := range blocks {
		read := b.GetCodewords()
		ints := make([]int, len(read))
		for j, x := range read {
			ints[j] = int(x)
		}
		if err := rs.Decode(ints, len(read)-b.GetNumDataCodewords()); err != nil {
			t.Fatalf("version %d, block %d: %v", c.version, i, err)
		}
		for j, x := range read {
			if ints[j] != int(x) {
				t.Fatalf("version %d, block %d needed correcting at codeword %d", c.version, i, j)
			}
		}
	}
	res, err := decoder.NewDecoder().Decode(bitMatrix(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.GetOther() != nil {
		t.Fatalf("version %d only reads mirrored", c.version)
	}
	return res.GetText()
}

func TestDecodesExactlyAtEveryVersion(t *testing.T) {
	inputs := uris()
	for v := 1; v <= 40; v++ {
		inputs = append(inputs, text(capacity(v)))
	}
	for _, s := range inputs {
		c, err := Encode(s)
		if err != nil {
			t.Fatalf("Encode(%d bytes): %v", len(s), err)
		}
		if got := decodeStrict(t, c); got != s {
			t.Errorf("version %d decodes to %q, want %q", c.version, got, s)
		}
	}
}

// testdata/segno.json was made once with segno 1.6.6: segno.make(data,
// error="m", mode="byte", mask=mask, boost_error=False, micro=False), rows
// from matrix_iter(border=0). Each input fills its version exactly: when there
// is room left, segno adds a zero byte before the pad codewords, which ISO/IEC
// 18004 section 7.4.10 does not (ZXing follows the standard), so its padding
// differs although the content reads the same.
func TestMatchesSegno(t *testing.T) {
	raw, err := os.ReadFile("testdata/segno.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden []struct {
		Data          string
		Version, Mask int
		Rows          []string
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	masks := map[int]bool{}
	for _, g := range golden {
		masks[g.Mask] = true
		if len(g.Data) != capacity(g.Version) {
			t.Errorf("a segno input is %d bytes; it must fill version %d (%d bytes)", len(g.Data), g.Version, capacity(g.Version))
		}
		auto, err := Encode(g.Data)
		if err != nil || auto.version != g.Version {
			t.Errorf("%d bytes: %v, want version %d like segno", len(g.Data), err, g.Version)
			continue
		}
		c := build([]byte(g.Data), g.Version, g.Mask)
		if len(g.Rows) != c.size {
			t.Errorf("version %d: segno has %d rows, want %d", g.Version, len(g.Rows), c.size)
			continue
		}
	rows:
		for y, row := range g.Rows {
			for x, m := range row {
				if (m == '1') != c.Dark(x, y) {
					t.Errorf("version %d, mask %d: module (%d, %d) differs from segno", g.Version, g.Mask, x, y)
					break rows
				}
			}
		}
	}
	if len(masks) != 8 {
		t.Errorf("the segno matrices cover %d masks, want all 8", len(masks))
	}
}

// checkSVG fails on anything in svg but the white background and the black
// path a QR code needs: no style attributes or elements, scripts, links or
// foreign content, which a Content-Security-Policy would block or which could
// run. It returns the path data.
func checkSVG(t *testing.T, svg string, n int) string {
	t.Helper()
	if !strings.HasPrefix(svg, "<svg ") {
		t.Fatalf("the image starts with %.20q, want <svg", svg)
	}
	allowed := map[string][]string{
		"svg":  {"xmlns", "viewBox", "width", "height", "shape-rendering"},
		"rect": {"width", "height", "fill"},
		"path": {"fill", "d"},
	}
	var elements []string
	attrs := map[string]string{}
	dec := xml.NewDecoder(strings.NewReader(svg))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("the SVG is not well-formed XML: %v", err)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			el := tok.Name.Local
			elements = append(elements, el)
			if tok.Name.Space != "http://www.w3.org/2000/svg" {
				t.Fatalf("<%s> is outside the SVG namespace", el)
			}
			for _, a := range tok.Attr {
				if a.Name.Space != "" || !slices.Contains(allowed[el], a.Name.Local) {
					t.Fatalf("<%s> has the attribute %s", el, a.Name.Local)
				}
				attrs[el+"."+a.Name.Local] = a.Value
			}
		case xml.CharData:
			if strings.TrimSpace(string(tok)) != "" {
				t.Fatalf("the SVG contains the text %q", tok)
			}
		case xml.EndElement:
		default:
			t.Fatalf("the SVG contains a %T", tok)
		}
	}
	if !slices.Equal(elements, []string{"svg", "rect", "path"}) {
		t.Fatalf("elements %v, want svg, rect, path", elements)
	}
	for k, want := range map[string]string{
		"svg.xmlns": "http://www.w3.org/2000/svg", "svg.viewBox": fmt.Sprintf("0 0 %d %d", n, n),
		"svg.width": strconv.Itoa(4 * n), "svg.height": strconv.Itoa(4 * n), "svg.shape-rendering": "crispEdges",
		"rect.width": strconv.Itoa(n), "rect.height": strconv.Itoa(n), "rect.fill": "#fff", "path.fill": "#000",
	} {
		if attrs[k] != want {
			t.Errorf("%s = %q, want %q", k, attrs[k], want)
		}
	}
	return attrs["path.d"]
}

func TestSVGDrawsTheCodeAndScans(t *testing.T) {
	run := regexp.MustCompile(`M(\d+) (\d+)h(\d+)v1h-(\d+)z`)
	for _, s := range uris() {
		c, err := Encode(s)
		if err != nil {
			t.Fatal(err)
		}
		n := c.size + 2*quietZone
		d := checkSVG(t, c.SVG(), n)
		grid := make([]bool, n*n)
		parsed := 0
		for _, m := range run.FindAllStringSubmatch(d, -1) {
			x, _ := strconv.Atoi(m[1])
			y, _ := strconv.Atoi(m[2])
			w, _ := strconv.Atoi(m[3])
			back, _ := strconv.Atoi(m[4])
			if w != back || w < 1 || x+w > n || y >= n {
				t.Fatalf("version %d: bad run %q", c.version, m[0])
			}
			for i := range w {
				if grid[y*n+x+i] {
					t.Fatalf("version %d: runs overlap at (%d, %d)", c.version, x+i, y)
				}
				grid[y*n+x+i] = true
			}
			parsed += len(m[0])
		}
		if parsed != len(d) {
			t.Fatalf("version %d: the path has more than rectangles: %q", c.version, d)
		}
		for y := range n {
			for x := range n {
				if grid[y*n+x] != c.Dark(x-quietZone, y-quietZone) {
					t.Fatalf("version %d: the SVG differs from the code at (%d, %d), counting the quiet zone", c.version, x, y)
				}
			}
		}

		const px = 4
		img := image.NewGray(image.Rect(0, 0, n*px, n*px))
		for i := range img.Pix {
			if !grid[(i/(n*px)/px)*n+i%(n*px)/px] {
				img.Pix[i] = 0xff
			}
		}
		bmp, err := gozxing.NewBinaryBitmapFromImage(img)
		if err != nil {
			t.Fatal(err)
		}
		res, err := zxqrcode.NewQRCodeReader().Decode(bmp, nil)
		if err != nil {
			t.Fatalf("version %d: ZXing cannot find or read the drawn code: %v", c.version, err)
		}
		if res.GetText() != s {
			t.Errorf("version %d: the drawn code reads %q, want %q", c.version, res.GetText(), s)
		}
	}
}
