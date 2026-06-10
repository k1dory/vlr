package reality

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// uTLS fingerprint selection.
//
// Context (RU DPI, mid-2026): the default `chrome` fingerprint paired with a
// Google SNI (www.google.com etc.) is reset on sight — the handshake signature
// is in the DPI's blocklist, and uTLS <1.8.2 ClientHello variants
// (HelloChrome_120..133) are individually fingerprintable. JA4+ is now used by
// the large CDNs and increasingly by state DPI, so a *static* fingerprint is a
// pattern waiting to be matched.
//
// Policy enforced here:
//   - Default fingerprint is "randomized", NOT "chrome". Every dial draws a
//     fresh ClientHello, so there is no stable JA4 to block.
//   - SNI must be a real, non-Google TLS1.3 host from a reputable network. For a
//     Yandex Cloud entry node, an own/Yandex domain is ideal (no SNI<->IP
//     mismatch). Google domains are rejected by ValidateSNI.
//   - Fallback fingerprints (ios, qq, safari) are offered for the rare client
//     that the randomized hello upsets; they survive RU DPI better than chrome.
const (
	FPRandomized = "randomized" // default — no stable signature
	FPRandom     = "random"     // pick one real fp at dial time
	FPChrome     = "chrome"     // AVOID in RU: known DPI signature
	FPFirefox    = "firefox"
	FPSafari     = "safari"
	FPIOS        = "ios" // good RU survivability
	FPAndroid    = "android"
	FPEdge       = "edge"
	FPQQ         = "qq" // good RU survivability
	FP360        = "360"
)

// DefaultFingerprint is what every generated config gets unless the operator
// overrides it. Do not change to "chrome".
const DefaultFingerprint = FPRandomized

// safeFingerprints are accepted without a warning.
var safeFingerprints = map[string]bool{
	FPRandomized: true, FPRandom: true, FPFirefox: true, FPSafari: true,
	FPIOS: true, FPAndroid: true, FPEdge: true, FPQQ: true, FP360: true,
}

// googleSNIs are domains we refuse to use as a Reality target: they carry the
// exact chrome+google DPI signature that is being reset in RU.
var googleSNIs = map[string]bool{
	"www.google.com": true, "google.com": true, "www.youtube.com": true,
	"youtube.com": true, "www.gstatic.com": true, "dl.google.com": true,
}

// ValidateFingerprint reports whether fp is a recognised uTLS fingerprint and,
// if recognised, whether using it is advisable. ok=false means Xray will reject
// it; warn != "" means it will work but is risky in RU.
func ValidateFingerprint(fp string) (ok bool, warn string) {
	if fp == FPChrome {
		return true, "chrome fingerprint has a known RU DPI signature; prefer randomized/ios/qq"
	}
	if safeFingerprints[fp] {
		return true, ""
	}
	return false, ""
}

// ValidateSNI rejects SNIs that pair badly with Reality in RU. An empty return
// means the SNI is acceptable.
func ValidateSNI(sni string) error {
	if sni == "" {
		return fmt.Errorf("SNI is empty")
	}
	if googleSNIs[sni] {
		return fmt.Errorf("%q is a Google SNI: chrome+google is reset by RU DPI, use an own/Yandex domain", sni)
	}
	return nil
}

// RecommendedSNIs are real TLS1.3 hosts that pair well with a Russian-cloud
// entry node. An own domain on the entry IP (no SNI<->IP mismatch) beats all of
// these; these are sane defaults when no own domain exists yet.
var RecommendedSNIs = []string{
	"storage.yandexcloud.net", // matches a Yandex Cloud entry IP range
	"www.tinkoff.ru",
	"www.wildberries.ru",
	"cdn-static.ozone.ru",
}

// PickSNI returns a deterministic-ish but unpredictable SNI from the
// recommended list. Used by `vlr init` when the operator did not supply one.
func PickSNI() (string, error) {
	return RecommendedSNIs[RandIndex(len(RecommendedSNIs))], nil
}

// RandIndex returns a cryptographically random index in [0, n). For n <= 1 it
// returns 0. Used to spread SNI/short-id choices unpredictably across nodes.
func RandIndex(n int) int {
	if n <= 1 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}
