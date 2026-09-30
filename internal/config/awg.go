package config

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// AWGConfig uses the basic obfuscation parameters supported by AmneziaWG.
// Both peers must share the padding sizes and header values.
type AWGConfig struct {
	Jc   int    `json:"jc"`
	Jmin int    `json:"jmin"`
	Jmax int    `json:"jmax"`
	S1   int    `json:"s1"`
	S2   int    `json:"s2"`
	H1   uint32 `json:"h1"`
	H2   uint32 `json:"h2"`
	H3   uint32 `json:"h3"`
	H4   uint32 `json:"h4"`
}

func NewAWGConfig() (*AWGConfig, error) {
	a := &AWGConfig{Jc: 4, Jmin: 8, Jmax: 80, S1: 64, S2: 32}
	seen := map[uint32]bool{}
	for _, h := range []*uint32{&a.H1, &a.H2, &a.H3, &a.H4} {
		for {
			n, err := rand.Int(rand.Reader, big.NewInt(2147483643))
			if err != nil {
				return nil, err
			}
			v := uint32(n.Int64()) + 5
			if !seen[v] {
				*h = v
				seen[v] = true
				break
			}
		}
	}
	return a, nil
}

func (a *AWGConfig) Validate() error {
	if a == nil {
		return fmt.Errorf("AWG parameters are required")
	}
	if a.Jc < 1 || a.Jc > 128 || a.Jmin < 0 || a.Jmax <= a.Jmin || a.Jmax > 1280 || a.S1 < 0 || a.S1 > 1132 || a.S2 < 0 || a.S2 > 1188 || a.S1+56 == a.S2 {
		return fmt.Errorf("invalid AWG junk/padding parameters")
	}
	seen := map[uint32]bool{}
	for _, h := range []uint32{a.H1, a.H2, a.H3, a.H4} {
		if h < 5 || seen[h] {
			return fmt.Errorf("AWG headers must be distinct and >= 5")
		}
		seen[h] = true
	}
	return nil
}

func (a *AWGConfig) Render() string {
	return fmt.Sprintf("Jc = %d\nJmin = %d\nJmax = %d\nS1 = %d\nS2 = %d\nH1 = %d\nH2 = %d\nH3 = %d\nH4 = %d\n", a.Jc, a.Jmin, a.Jmax, a.S1, a.S2, a.H1, a.H2, a.H3, a.H4)
}

func (c CascadeConfig) Tool() string {
	if c.Transport == "awg" {
		return "awg"
	}
	return "wg"
}

func (c CascadeConfig) ConfigDir() string {
	if c.Transport == "awg" {
		return "/etc/amnezia/amneziawg"
	}
	return "/etc/wireguard"
}
