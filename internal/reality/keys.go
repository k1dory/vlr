// Package reality generates the cryptographic material a VLESS+Reality inbound
// needs: an x25519 key pair (private kept on the node, public handed to clients)
// and short IDs. The output is byte-for-byte compatible with `xray x25519`, so
// configs generated here are accepted by an unmodified Xray-core.
package reality

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// KeyPair is a Reality x25519 key pair, base64 RawURL-encoded exactly the way
// Xray prints them.
type KeyPair struct {
	// PrivateKey goes into the server inbound (realitySettings.privateKey).
	PrivateKey string `json:"private_key"`
	// PublicKey goes to the client (pbk= in the VLESS URL).
	PublicKey string `json:"public_key"`
}

// GenerateKeyPair produces a fresh Reality key pair.
//
// crypto/ecdh.X25519 clamps the scalar and derives the public point the same
// way Xray's vendored utls/curve25519 does, so PrivateKey/PublicKey match
// `xray x25519` output and are interoperable.
func GenerateKeyPair() (KeyPair, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, fmt.Errorf("generate x25519 key: %w", err)
	}
	enc := base64.RawURLEncoding
	return KeyPair{
		PrivateKey: enc.EncodeToString(priv.Bytes()),
		PublicKey:  enc.EncodeToString(priv.PublicKey().Bytes()),
	}, nil
}

// PublicFromPrivate re-derives the public key from a stored private key. Used
// when a node already has a private key persisted and needs to hand the public
// half to a client without regenerating (which would invalidate every config).
func PublicFromPrivate(privB64 string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(privB64)
	if err != nil {
		return "", fmt.Errorf("decode private key: %w", err)
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", fmt.Errorf("parse private key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}

// NewShortID returns a random Reality short ID: an even-length hex string of
// 1..16 bytes. Xray accepts 0..8 bytes (0..16 hex chars). We default callers to
// 8 bytes for good entropy without bloating the URL.
func NewShortID(nBytes int) (string, error) {
	if nBytes < 1 || nBytes > 8 {
		return "", fmt.Errorf("short id length must be 1..8 bytes, got %d", nBytes)
	}
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ShortIDSet returns n distinct short IDs. A node hands a different short ID to
// each client tier so traffic can be attributed and individually revoked
// without rotating the whole inbound.
func ShortIDSet(n, nBytes int) ([]string, error) {
	seen := make(map[string]struct{}, n)
	out := make([]string, 0, n)
	for len(out) < n {
		id, err := NewShortID(nBytes)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}
