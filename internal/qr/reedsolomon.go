package qr

// Galois-field GF(256) arithmetic with primitive polynomial 0x11D, and the
// Reed–Solomon error-correction codeword generator. Ported from Project Nayuki's
// public-domain QR encoder; validated in reedsolomon_test.go against the canonical
// "HELLO WORLD" vector.

var gfExp [512]byte
var gfLog [256]byte

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i] = byte(x)
		gfLog[byte(x)] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11D
		}
	}
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
}

// gfMul multiplies two GF(256) elements.
func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

// rsGenerator returns the divisor polynomial coefficients (length = degree) for
// the given number of EC codewords, excluding the implicit leading 1.
func rsGenerator(degree int) []byte {
	coeff := make([]byte, degree)
	coeff[degree-1] = 1
	root := byte(1)
	for i := 0; i < degree; i++ {
		for j := 0; j < degree; j++ {
			coeff[j] = gfMul(coeff[j], root)
			if j+1 < degree {
				coeff[j] ^= coeff[j+1]
			}
		}
		root = gfMul(root, 0x02)
	}
	return coeff
}

// reedSolomon computes the EC codewords for data using an ecLen-term generator.
func reedSolomon(data []byte, ecLen int) []byte {
	coeff := rsGenerator(ecLen)
	res := make([]byte, ecLen)
	for _, b := range data {
		factor := b ^ res[0]
		copy(res, res[1:])
		res[ecLen-1] = 0
		for i := 0; i < ecLen; i++ {
			res[i] ^= gfMul(coeff[i], factor)
		}
	}
	return res
}
