// Package qrcode draws QR codes (ISO/IEC 18004) as SVG, so the dashboard can
// show an otpauth:// link to an authenticator app without loading anything
// from another site. It stores text as bytes at error correction level M,
// which survives about 15% of the code being damaged or covered, in the
// smallest of the 40 sizes (versions) that holds it.
package qrcode

import (
	"fmt"
	"strings"
)

// MaxLen is the most bytes one code holds: version 40 at level M.
const MaxLen = 2331

// quietZone is the light border scanners need around a code, in modules.
const quietZone = 4

// Code is a QR code: a square of dark and light modules, without the quiet
// zone that must surround it.
type Code struct {
	version, mask, size int
	dark                []bool
}

// Encode makes the QR code for text, in the smallest version that holds it and
// with the mask that is easiest to scan. The bytes are stored as they are,
// without a character set marker: ASCII, which covers otpauth:// URIs, reads
// back exactly.
func Encode(text string) (*Code, error) {
	for v := 1; v <= 40; v++ {
		if len(text) <= capacity(v) {
			return build([]byte(text), v, -1), nil
		}
	}
	return nil, fmt.Errorf("The text is %d bytes long, but a QR code holds at most %d.", len(text), MaxLen)
}

// Size is the number of modules on each side.
func (c *Code) Size() int { return c.size }

// Dark reports whether the module in column x and row y is dark. Modules
// outside the code are light, like the quiet zone.
func (c *Code) Dark(x, y int) bool {
	return x >= 0 && y >= 0 && x < c.size && y < c.size && c.dark[y*c.size+x]
}

// SVG draws the code as a standalone SVG image: black on white with the quiet
// zone, 4 pixels per module unless CSS sizes it. It has no style attributes,
// scripts or links, so it works under a strict Content-Security-Policy as an
// <img>, also from a data: URL, or inline.
func (c *Code) SVG() string {
	n := c.size + 2*quietZone
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" shape-rendering="crispEdges">`, n, n, 4*n, 4*n)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n)
	for y := range c.size {
		for x := 0; x < c.size; x++ {
			if !c.Dark(x, y) {
				continue
			}
			start := x
			for c.Dark(x+1, y) {
				x++
			}
			w := x - start + 1
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", start+quietZone, y+quietZone, w, w)
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}

// build lays out data in version v with the given mask, or with the mask
// that scores the lowest penalty if mask is negative.
func build(data []byte, v, mask int) *Code {
	m := newMatrix(v)
	m.drawCodewords(interleave(stream(data, v), v))
	if mask < 0 {
		best := -1
		for k := range 8 {
			m.applyMask(k)
			m.drawFormat(k)
			if p := m.penalty(); best < 0 || p < best {
				mask, best = k, p
			}
			m.applyMask(k)
		}
	}
	m.applyMask(mask)
	m.drawFormat(mask)
	return &Code{version: v, mask: mask, size: m.size, dark: m.dark}
}

// eccPerBlock and numBlocks are the error correction codewords per block and
// the number of blocks at level M, by version (ISO/IEC 18004 table 9).
var (
	eccPerBlock = [41]int{0, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28}
	numBlocks   = [41]int{0, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49}
)

// rawModules is how many modules of version v hold codewords: all of them but
// the finder, timing and alignment patterns and the format and version
// information.
func rawModules(v int) int {
	n := (16*v+128)*v + 64
	if v >= 2 {
		a := v/7 + 2
		n -= (25*a-10)*a - 55
		if v >= 7 {
			n -= 36
		}
	}
	return n
}

func totalCodewords(v int) int { return rawModules(v) / 8 }

func dataCodewords(v int) int { return totalCodewords(v) - eccPerBlock[v]*numBlocks[v] }

// countBits is the width of the byte-mode length field.
func countBits(v int) int {
	if v < 10 {
		return 8
	}
	return 16
}

// capacity is how many bytes fit in version v at level M.
func capacity(v int) int { return (dataCodewords(v)*8 - 4 - countBits(v)) / 8 }

// stream is the data codewords of version v: the byte mode indicator, the
// length, the bytes, a terminator and the standard padding.
func stream(data []byte, v int) []byte {
	var w bitWriter
	w.put(0b0100, 4)
	w.put(len(data), countBits(v))
	for _, b := range data {
		w.put(int(b), 8)
	}
	n := dataCodewords(v)
	w.put(0, min(4, n*8-w.n))
	w.put(0, (8-w.n%8)%8)
	for pad := 0; len(w.buf) < n; pad++ {
		w.buf = append(w.buf, [2]byte{0xec, 0x11}[pad%2])
	}
	return w.buf
}

type bitWriter struct {
	buf []byte
	n   int
}

func (w *bitWriter) put(v, bits int) {
	for i := bits - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.buf = append(w.buf, 0)
		}
		w.buf[len(w.buf)-1] |= byte(v>>i&1) << (7 - w.n%8)
		w.n++
	}
}

// blockLens is the number of data codewords in each block of version v. When
// the codewords do not divide evenly, the last blocks hold one more.
func blockLens(v int) []int {
	nb, total := numBlocks[v], totalCodewords(v)
	lens := make([]int, nb)
	for i := range lens {
		lens[i] = total/nb - eccPerBlock[v]
		if i >= nb-total%nb {
			lens[i]++
		}
	}
	return lens
}

// interleave splits the data codewords into blocks, adds each block's error
// correction and interleaves them in the order they are placed.
func interleave(data []byte, v int) []byte {
	ecc := eccPerBlock[v]
	gen := rsGenerator(ecc)
	var blocks, checks [][]byte
	longest := 0
	for _, n := range blockLens(v) {
		blocks = append(blocks, data[:n])
		checks = append(checks, rsRemainder(data[:n], gen))
		data = data[n:]
		longest = max(longest, n)
	}
	out := make([]byte, 0, totalCodewords(v))
	for i := range longest {
		for _, b := range blocks {
			if i < len(b) {
				out = append(out, b[i])
			}
		}
	}
	for i := range ecc {
		for _, c := range checks {
			out = append(out, c[i])
		}
	}
	return out
}

// gfMul multiplies in GF(2⁸) with the QR polynomial x⁸+x⁴+x³+x²+1.
func gfMul(x, y byte) byte {
	var z byte
	for i := 7; i >= 0; i-- {
		z = z<<1 ^ (z>>7)*0x1d
		z ^= (y >> i & 1) * x
	}
	return z
}

// rsGenerator is the Reed–Solomon generator polynomial of the given degree,
// (x-1)(x-α)…(x-α^(degree-1)) with α = 2, highest coefficient first and the
// leading 1 left out.
func rsGenerator(degree int) []byte {
	g := make([]byte, degree)
	g[degree-1] = 1
	root := byte(1)
	for range degree {
		for j := range g {
			g[j] = gfMul(g[j], root)
			if j+1 < len(g) {
				g[j] ^= g[j+1]
			}
		}
		root = gfMul(root, 2)
	}
	return g
}

// rsRemainder is the error correction for data: the remainder of dividing
// data·x^len(gen) by the generator.
func rsRemainder(data, gen []byte) []byte {
	rem := make([]byte, len(gen))
	for _, b := range data {
		f := b ^ rem[0]
		copy(rem, rem[1:])
		rem[len(rem)-1] = 0
		for i, c := range gen {
			rem[i] ^= gfMul(c, f)
		}
	}
	return rem
}

// matrix is a code being built. fn marks function modules, which hold no
// codewords and are never masked.
type matrix struct {
	size     int
	dark, fn []bool
}

func newMatrix(v int) *matrix {
	n := 17 + 4*v
	m := &matrix{size: n, dark: make([]bool, n*n), fn: make([]bool, n*n)}
	for i := range n {
		m.set(6, i, i%2 == 0)
		m.set(i, 6, i%2 == 0)
	}
	m.finder(3, 3)
	m.finder(n-4, 3)
	m.finder(3, n-4)
	pos := alignment(v)
	last := len(pos) - 1
	for i, x := range pos {
		for j, y := range pos {
			if i == 0 && j == 0 || i == 0 && j == last || i == last && j == 0 {
				continue
			}
			m.alignment(x, y)
		}
	}
	m.drawFormat(0)
	m.drawVersion(v)
	return m
}

func (m *matrix) set(x, y int, dark bool) {
	m.dark[y*m.size+x] = dark
	m.fn[y*m.size+x] = true
}

// finder draws a finder pattern centred on (x, y) with its light separator.
func (m *matrix) finder(x, y int) {
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			xx, yy := x+dx, y+dy
			if xx < 0 || yy < 0 || xx >= m.size || yy >= m.size {
				continue
			}
			d := max(abs(dx), abs(dy))
			m.set(xx, yy, d != 2 && d != 4)
		}
	}
}

func (m *matrix) alignment(x, y int) {
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			m.set(x+dx, y+dy, max(abs(dx), abs(dy)) != 1)
		}
	}
}

// alignment is the centres of the alignment patterns, the same for rows and
// columns: 6, then evenly spaced up to size-7.
func alignment(v int) []int {
	if v == 1 {
		return nil
	}
	n := v/7 + 2
	step := (v*8 + n*3 + 5) / (n*4 - 4) * 2
	pos := make([]int, n)
	pos[0] = 6
	for i, p := n-1, 4*v+10; i >= 1; i, p = i-1, p-step {
		pos[i] = p
	}
	return pos
}

// formatBits is the format information for level M (bits 00) and mask: a
// BCH(15,5) code, XORed with 101010000010010 so it is never all light.
func formatBits(mask int) int {
	rem := mask
	for range 10 {
		rem = rem<<1 ^ (rem>>9)*0x537
	}
	return (mask<<10 | rem) ^ 0x5412
}

// versionBits is the version information of versions 7 and up: a BCH(18,6)
// code.
func versionBits(v int) int {
	rem := v
	for range 12 {
		rem = rem<<1 ^ (rem>>11)*0x1f25
	}
	return v<<12 | rem
}

// drawFormat draws both copies of the format information and the dark module
// next to the lower left finder.
func (m *matrix) drawFormat(mask int) {
	bits := formatBits(mask)
	bit := func(i int) bool { return bits>>i&1 == 1 }
	n := m.size
	for i := range 6 {
		m.set(8, i, bit(i))
	}
	m.set(8, 7, bit(6))
	m.set(8, 8, bit(7))
	m.set(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		m.set(14-i, 8, bit(i))
	}
	for i := range 8 {
		m.set(n-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		m.set(8, n-15+i, bit(i))
	}
	m.set(8, n-8, true)
}

// drawVersion draws both copies of the version information, which only
// versions 7 and up have.
func (m *matrix) drawVersion(v int) {
	if v < 7 {
		return
	}
	bits := versionBits(v)
	for i := range 18 {
		dark := bits>>i&1 == 1
		a, b := m.size-11+i%3, i/3
		m.set(a, b, dark)
		m.set(b, a, dark)
	}
}

// drawCodewords places the codewords two columns at a time, up and down from
// the right edge, skipping function modules and the vertical timing pattern.
// Remainder modules at the end stay light.
func (m *matrix) drawCodewords(cw []byte) {
	n, i := m.size, 0
	for right := n - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		upward := (right+1)&2 == 0
		for vert := range n {
			y := vert
			if upward {
				y = n - 1 - vert
			}
			for x := right; x >= right-1; x-- {
				k := y*n + x
				if m.fn[k] || i >= len(cw)*8 {
					continue
				}
				m.dark[k] = cw[i/8]>>(7-i%8)&1 == 1
				i++
			}
		}
	}
}

// applyMask flips the data modules that the mask pattern selects; applying the
// same mask again undoes it.
func (m *matrix) applyMask(mask int) {
	n := m.size
	for y := range n {
		for x := range n {
			if m.fn[y*n+x] {
				continue
			}
			var flip bool
			switch mask {
			case 0:
				flip = (x+y)%2 == 0
			case 1:
				flip = y%2 == 0
			case 2:
				flip = x%3 == 0
			case 3:
				flip = (x+y)%3 == 0
			case 4:
				flip = (x/3+y/2)%2 == 0
			case 5:
				flip = x*y%2+x*y%3 == 0
			case 6:
				flip = (x*y%2+x*y%3)%2 == 0
			case 7:
				flip = ((x+y)%2+x*y%3)%2 == 0
			default:
				panic(fmt.Sprintf("qrcode: mask %d does not exist", mask))
			}
			if flip {
				m.dark[y*n+x] = !m.dark[y*n+x]
			}
		}
	}
}

// penalty scores how hard the code is to scan with the four rules of ISO/IEC
// 18004 section 7.8.3: long runs of one colour, 2×2 blocks of one colour,
// patterns that look like a finder, and an unbalanced share of dark modules.
// Like ZXing, it counts a finder-like pattern once if either side of it has
// four light modules, and treats the quiet zone as light.
func (m *matrix) penalty() int {
	n := m.size
	score, darkCount := 0, 0
	row, col := make([]bool, n), make([]bool, n)
	for i := range n {
		for j := range n {
			row[j], col[j] = m.dark[i*n+j], m.dark[j*n+i]
			if row[j] {
				darkCount++
			}
		}
		score += runPenalty(row) + runPenalty(col) + finderPenalty(row) + finderPenalty(col)
	}
	for y := range n - 1 {
		for x := range n - 1 {
			c := m.dark[y*n+x]
			if c == m.dark[y*n+x+1] && c == m.dark[(y+1)*n+x] && c == m.dark[(y+1)*n+x+1] {
				score += 3
			}
		}
	}
	return score + abs(2*darkCount-n*n)*10/(n*n)*10
}

func runPenalty(line []bool) int {
	p, run := 0, 1
	for i := 1; i <= len(line); i++ {
		if i < len(line) && line[i] == line[i-1] {
			run++
			continue
		}
		if run >= 5 {
			p += 3 + run - 5
		}
		run = 1
	}
	return p
}

func finderPenalty(line []bool) int {
	pattern := [7]bool{true, false, true, true, true, false, true}
	p := 0
	for i := 0; i+7 <= len(line); i++ {
		if [7]bool(line[i:i+7]) == pattern && (light(line, i-4, i) || light(line, i+7, i+11)) {
			p += 40
		}
	}
	return p
}

func light(line []bool, from, to int) bool {
	for i := max(from, 0); i < min(to, len(line)); i++ {
		if line[i] {
			return false
		}
	}
	return true
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
