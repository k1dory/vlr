module github.com/k1dory/vlr

go 1.25

// Zero external dependencies by design.
// A global VPN-node utility must `go build` on any RU/EU box without a module
// proxy. Reality x25519 keys come from the stdlib (crypto/ecdh), config is JSON,
// the CLI is stdlib flag. Keep it that way.
