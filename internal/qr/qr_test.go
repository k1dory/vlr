package qr

import (
	"strings"
	"testing"
)

// TestReedSolomonHelloWorld validates the GF(256) + RS engine against the
// canonical worked example (Thonky / ISO 18004): the 16 data codewords of
// "HELLO WORLD" at version 1, EC level M produce these 10 EC codewords.
func TestReedSolomonHelloWorld(t *testing.T) {
	data := []byte{32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17}
	want := []byte{196, 35, 39, 119, 235, 215, 231, 226, 93, 23}
	got := reedSolomon(data, 10)
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("EC codeword %d = %d, want %d (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestFormatBits validates the BCH(15,5) format-info encoding against the
// published level-L strings: mask 0 = 111011111000100, mask 5 = 110001100011000.
func TestFormatBits(t *testing.T) {
	cases := []struct {
		mask int
		want int
	}{
		{0, 0b111011111000100},
		{5, 0b110001100011000},
	}
	for _, tc := range cases {
		data := ecLevelBits<<3 | tc.mask
		rem := data
		for i := 0; i < 10; i++ {
			rem = (rem << 1) ^ ((rem >> 9) * 0x537)
		}
		got := (data<<10 | rem) ^ 0x5412
		if got != tc.want {
			t.Fatalf("format bits mask %d = %015b, want %015b", tc.mask, got, tc.want)
		}
	}
}

func TestEncodeVersionSelection(t *testing.T) {
	cases := []struct {
		text     string
		wantSize int // 17 + 4*version
	}{
		{"HI", 21},                    // v1 (21x21)
		{strings.Repeat("x", 30), 25}, // >19 data bytes -> v2 (25x25)
		{"https://link.infrashark.tech/base64/9f3c1a7be04d82a6f1c05e7b2d4a8091", 33}, // 68 bytes -> v4 (33x33)
	}
	for _, tc := range cases {
		c, err := Encode(tc.text)
		if err != nil {
			t.Fatalf("Encode(%q): %v", tc.text, err)
		}
		if c.Size != tc.wantSize {
			t.Fatalf("Encode(%q) size = %d, want %d", tc.text, c.Size, tc.wantSize)
		}
	}
}

func TestEncodeTooLong(t *testing.T) {
	if _, err := Encode(strings.Repeat("x", 200)); err == nil {
		t.Fatal("expected error for oversized input")
	}
}

// TestFinderPatterns checks the three finder patterns are placed correctly: a
// dark 3x3 center at each corner, with the light separator ring.
func TestFinderPatterns(t *testing.T) {
	c, err := Encode("test")
	if err != nil {
		t.Fatal(err)
	}
	n := c.Size
	corners := [][2]int{{3, 3}, {n - 4, 3}, {3, n - 4}} // finder centers
	for _, ctr := range corners {
		cx, cy := ctr[0], ctr[1]
		if !c.Dark(cx, cy) {
			t.Fatalf("finder center (%d,%d) should be dark", cx, cy)
		}
		// The ring at Chebyshev distance 2 must be light.
		if c.Dark(cx+2, cy) || c.Dark(cx, cy+2) {
			t.Fatalf("finder separator ring around (%d,%d) should be light", cx, cy)
		}
	}
}

func TestSVGWellFormed(t *testing.T) {
	c, err := Encode("https://link.infrashark.tech/base64/deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	svg := c.SVG(4, 4)
	if !strings.HasPrefix(svg, "<svg") || !strings.HasSuffix(svg, "</svg>") {
		t.Fatal("SVG not well-formed")
	}
	if !strings.Contains(svg, "<path") {
		t.Fatal("SVG missing module path")
	}
}
