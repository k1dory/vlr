// Package qr is a tiny, dependency-free QR encoder — just enough to render a
// subscription URL as a scannable code on the self-service portal.
//
// Scope on purpose: byte mode, error-correction level L, versions 1–5 (single
// data block, so no block interleaving). That covers URLs up to ~106 bytes, which
// a subscription link always is. Anything longer returns an error and the caller
// simply omits the QR (the link text is always shown too). The math (Galois-field
// arithmetic, Reed–Solomon, format BCH) is unit-tested against published vectors.
//
// Algorithms follow the ISO/IEC 18004 reference; the structure mirrors Project
// Nayuki's public-domain QR encoder.
package qr

import (
	"fmt"
	"strings"
)

// level-L, single-block capacities for versions 1..5.
var (
	dataCodewords = [...]int{19, 34, 55, 80, 108} // usable data bytes per version
	ecCodewords   = [...]int{7, 10, 15, 20, 26}   // EC bytes per version
	alignCenter   = [...]int{0, 18, 22, 26, 30}   // single alignment-pattern center (0 = none, v1)
	ecLevelBits   = 0b01                          // format-info bits for EC level L
)

// Code is a rendered QR symbol: a square grid of dark/light modules.
type Code struct {
	Size    int
	modules [][]bool
	isFunc  [][]bool
}

// Dark reports whether the module at column x, row y is dark.
func (c *Code) Dark(x, y int) bool { return c.modules[y][x] }

// Encode renders text as a QR Code (level L, version 1–5). It errors if text is
// too long to fit version 5.
func Encode(text string) (*Code, error) {
	data := []byte(text)

	// Pick the smallest version whose data capacity holds mode(4) + count(8) +
	// 8*len bits. Byte-mode count is 8 bits for versions 1–9.
	needBits := 4 + 8 + 8*len(data)
	version := 0
	for v := 1; v <= 5; v++ {
		if dataCodewords[v-1]*8 >= needBits {
			version = v
			break
		}
	}
	if version == 0 {
		return nil, fmt.Errorf("qr: %d bytes too long for version 5", len(data))
	}

	bits := newBitBuffer()
	bits.append(0b0100, 4)    // byte mode
	bits.append(len(data), 8) // character count
	for _, b := range data {
		bits.append(int(b), 8)
	}
	// Terminator: up to 4 zero bits, not exceeding capacity.
	capBits := dataCodewords[version-1] * 8
	term := 4
	if rem := capBits - bits.len(); rem < term {
		term = rem
	}
	bits.append(0, term)
	// Pad to a byte boundary.
	if pad := (8 - bits.len()%8) % 8; pad > 0 {
		bits.append(0, pad)
	}
	// Pad bytes: alternate 0xEC / 0x11 until the capacity is full.
	for pad := 0xEC; bits.len() < capBits; pad ^= 0xEC ^ 0x11 {
		bits.append(pad, 8)
	}

	dataBytes := bits.bytes()
	ec := reedSolomon(dataBytes, ecCodewords[version-1])
	all := append(append([]byte{}, dataBytes...), ec...) // single block: data then EC

	c := newCode(version)
	c.drawFunctionPatterns(version)
	c.placeData(all)
	c.applyBestMask()
	return c, nil
}

func newCode(version int) *Code {
	size := 17 + 4*version
	m := make([][]bool, size)
	f := make([][]bool, size)
	for i := range m {
		m[i] = make([]bool, size)
		f[i] = make([]bool, size)
	}
	return &Code{Size: size, modules: m, isFunc: f}
}

func (c *Code) set(x, y int, dark, fn bool) {
	c.modules[y][x] = dark
	if fn {
		c.isFunc[y][x] = true
	}
}

func (c *Code) drawFunctionPatterns(version int) {
	size := c.Size
	// Timing patterns (row 6 and column 6).
	for i := 0; i < size; i++ {
		c.set(6, i, i%2 == 0, true)
		c.set(i, 6, i%2 == 0, true)
	}
	// Three finder patterns (with separators).
	c.drawFinder(3, 3)
	c.drawFinder(size-4, 3)
	c.drawFinder(3, size-4)
	// Alignment pattern (versions 2–5: a single one).
	if a := alignCenter[version-1]; a != 0 {
		c.drawAlignment(a, a)
	}
	// Dark module.
	c.set(8, size-8, true, true)
	// Reserve the format-info areas (filled after masking).
	c.reserveFormat()
}

func (c *Code) drawFinder(cx, cy int) {
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			x, y := cx+dx, cy+dy
			if x < 0 || x >= c.Size || y < 0 || y >= c.Size {
				continue
			}
			d := max2(abs(dx), abs(dy))
			c.set(x, y, d != 2 && d != 4, true)
		}
	}
}

func (c *Code) drawAlignment(cx, cy int) {
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			d := max2(abs(dx), abs(dy))
			c.set(cx+dx, cy+dy, d != 1, true)
		}
	}
}

// reserveFormat marks the 2×15 format-info module positions as function modules
// so data placement skips them; the real bits are written by setFormatBits.
func (c *Code) reserveFormat() {
	size := c.Size
	for i := 0; i <= 5; i++ {
		c.isFunc[i][8] = true
	}
	c.isFunc[7][8] = true
	c.isFunc[8][8] = true
	c.isFunc[8][7] = true
	for i := 0; i <= 5; i++ {
		c.isFunc[8][i] = true
	}
	for i := 0; i < 8; i++ {
		c.isFunc[8][size-1-i] = true
	}
	for i := 0; i < 7; i++ {
		c.isFunc[size-1-i][8] = true
	}
}

// placeData writes the codeword bit stream in the standard upward/downward zigzag,
// skipping function modules.
func (c *Code) placeData(codewords []byte) {
	size := c.Size
	bit := 0
	total := len(codewords) * 8
	for right := size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5 // skip the vertical timing column
		}
		for vert := 0; vert < size; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				upward := ((right + 1) & 2) == 0
				y := vert
				if upward {
					y = size - 1 - vert
				}
				if c.isFunc[y][x] || bit >= total {
					continue
				}
				b := codewords[bit>>3]
				dark := (b>>(7-uint(bit&7)))&1 == 1
				c.modules[y][x] = dark
				bit++
			}
		}
	}
}

func (c *Code) applyBestMask() {
	best := -1
	bestPenalty := 1 << 62
	for mask := 0; mask < 8; mask++ {
		c.applyMask(mask)
		c.setFormatBits(mask)
		p := c.penalty()
		if p < bestPenalty {
			bestPenalty = p
			best = mask
		}
		c.applyMask(mask) // XOR again to undo (mask is its own inverse)
	}
	c.applyMask(best)
	c.setFormatBits(best)
}

// applyMask XORs the mask pattern over data (non-function) modules.
func (c *Code) applyMask(mask int) {
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.isFunc[y][x] {
				continue
			}
			var invert bool
			switch mask {
			case 0:
				invert = (x+y)%2 == 0
			case 1:
				invert = y%2 == 0
			case 2:
				invert = x%3 == 0
			case 3:
				invert = (x+y)%3 == 0
			case 4:
				invert = (y/2+x/3)%2 == 0
			case 5:
				invert = (x*y)%2+(x*y)%3 == 0
			case 6:
				invert = ((x*y)%2+(x*y)%3)%2 == 0
			case 7:
				invert = ((x+y)%2+(x*y)%3)%2 == 0
			}
			if invert {
				c.modules[y][x] = !c.modules[y][x]
			}
		}
	}
}

// setFormatBits writes the 15-bit format information (EC level + mask) with its
// BCH error correction, in both standard locations.
func (c *Code) setFormatBits(mask int) {
	size := c.Size
	data := ecLevelBits<<3 | mask
	rem := data
	for i := 0; i < 10; i++ {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537)
	}
	bits := (data<<10 | rem) ^ 0x5412 // 15 bits

	get := func(i int) bool { return (bits>>uint(i))&1 == 1 }
	// First copy (around the top-left finder).
	for i := 0; i <= 5; i++ {
		c.modules[i][8] = get(i)
	}
	c.modules[7][8] = get(6)
	c.modules[8][8] = get(7)
	c.modules[8][7] = get(8)
	for i := 9; i <= 14; i++ {
		c.modules[8][14-i] = get(i)
	}
	// Second copy (bottom-left and top-right).
	for i := 0; i <= 7; i++ {
		c.modules[size-1-i][8] = get(i)
	}
	for i := 8; i <= 14; i++ {
		c.modules[8][size-15+i] = get(i)
	}
	c.modules[size-8][8] = true // dark module, always
}

// penalty scores the four ISO/IEC 18004 rules; lower is better.
func (c *Code) penalty() int {
	size := c.Size
	p := 0
	// Rule 1: runs of 5+ same-color modules in each row and column.
	for y := 0; y < size; y++ {
		runColor := c.modules[y][0]
		run := 1
		for x := 1; x < size; x++ {
			if c.modules[y][x] == runColor {
				run++
			} else {
				p += runPenalty(run)
				runColor = c.modules[y][x]
				run = 1
			}
		}
		p += runPenalty(run)
	}
	for x := 0; x < size; x++ {
		runColor := c.modules[0][x]
		run := 1
		for y := 1; y < size; y++ {
			if c.modules[y][x] == runColor {
				run++
			} else {
				p += runPenalty(run)
				runColor = c.modules[y][x]
				run = 1
			}
		}
		p += runPenalty(run)
	}
	// Rule 2: 2×2 blocks of the same color.
	for y := 0; y < size-1; y++ {
		for x := 0; x < size-1; x++ {
			col := c.modules[y][x]
			if col == c.modules[y][x+1] && col == c.modules[y+1][x] && col == c.modules[y+1][x+1] {
				p += 3
			}
		}
	}
	// Rule 3: finder-like 1:1:3:1:1 patterns with 4 light modules on a side.
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if c.hasFinderLike(x, y, 1, 0) {
				p += 40
			}
			if c.hasFinderLike(x, y, 0, 1) {
				p += 40
			}
		}
	}
	// Rule 4: deviation of dark-module proportion from 50%.
	dark := 0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if c.modules[y][x] {
				dark++
			}
		}
	}
	total := size * size
	k := (abs(dark*20-total*10) + total - 1) / total // ceil(|prop-50|/5)*... simplified
	p += k * 10
	return p
}

// hasFinderLike matches the 11-module sequence 4×light + 1:1:3:1:1 dark/light
// starting at (x,y) in direction (dx,dy).
func (c *Code) hasFinderLike(x, y, dx, dy int) bool {
	pattern := []bool{true, false, true, true, true, false, true, false, false, false, false}
	for i, want := range pattern {
		xx, yy := x+dx*i, y+dy*i
		if xx < 0 || xx >= c.Size || yy < 0 || yy >= c.Size {
			return false
		}
		if c.modules[yy][xx] != want {
			return false
		}
	}
	return true
}

func runPenalty(run int) int {
	if run >= 5 {
		return 3 + (run - 5)
	}
	return 0
}

// --- bit buffer ---

type bitBuffer struct {
	bits []bool
}

func newBitBuffer() *bitBuffer { return &bitBuffer{} }
func (b *bitBuffer) len() int  { return len(b.bits) }
func (b *bitBuffer) append(val, n int) {
	for i := n - 1; i >= 0; i-- {
		b.bits = append(b.bits, (val>>uint(i))&1 == 1)
	}
}
func (b *bitBuffer) bytes() []byte {
	out := make([]byte, (len(b.bits)+7)/8)
	for i, bit := range b.bits {
		if bit {
			out[i>>3] |= 1 << uint(7-i&7)
		}
	}
	return out
}

// --- helpers ---

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// SVG renders the code as a standalone <svg> string: quiet-zone modules of white
// border, dark modules as one black path, each module `scale` px. Safe to inline.
func (c *Code) SVG(quiet, scale int) string {
	dim := (c.Size + 2*quiet) * scale
	var path strings.Builder
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.modules[y][x] {
				fmt.Fprintf(&path, "M%d %dh%dv%dh%dz", (x+quiet)*scale, (y+quiet)*scale, scale, scale, -scale)
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, dim, dim, dim, dim)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/>`, dim, dim)
	fmt.Fprintf(&b, `<path d="%s" fill="#000"/></svg>`, path.String())
	return b.String()
}
