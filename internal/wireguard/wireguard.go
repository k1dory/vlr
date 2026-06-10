// Package wireguard renders wg-quick configs for the RU(entry) <-> EU(exit)
// cascade hop and generates WireGuard key pairs.
//
// Why WireGuard for the inner hop (and not SOCKS5-over-SSH or an Xray
// dialerProxy): the cascade carries *all* client traffic, including UDP and
// HTTP/3 (QUIC). A TCP CONNECT SOCKS5 proxy silently drops UDP, breaking QUIC;
// SSH adds a second encryption layer on top of Reality. WireGuard is kernel
// space, UDP-native and the lowest-overhead option for datacenter-to-datacenter,
// where DPI camouflage on the inner hop buys nothing.
//
// Roles:
//   - entry (RU, Yandex Cloud): default route for client egress goes into the
//     tunnel; the EU peer is the only allowed peer (AllowedIPs 0.0.0.0/0).
//   - exit  (EU, Aeza): NATs tunnel traffic out to the internet; AllowedIPs is
//     just the entry tunnel IP.
package wireguard

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/k1dory/vlr/internal/config"
)

// KeyPair is a WireGuard key pair, base64 std-encoded as wg expects.
type KeyPair struct {
	PrivateKey string
	PublicKey  string
}

// GenerateKeyPair makes a WireGuard (curve25519) key pair compatible with
// `wg genkey`/`wg pubkey`.
func GenerateKeyPair() (KeyPair, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, fmt.Errorf("generate wg key: %w", err)
	}
	enc := base64.StdEncoding
	return KeyPair{
		PrivateKey: enc.EncodeToString(priv.Bytes()),
		PublicKey:  enc.EncodeToString(priv.PublicKey().Bytes()),
	}, nil
}

// PublicFromPrivate re-derives the WireGuard public key from a stored private
// key, so `cascade up` can reuse an existing RU key instead of rotating it.
func PublicFromPrivate(privB64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(privB64)
	if err != nil {
		return "", fmt.Errorf("decode wg private key: %w", err)
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", fmt.Errorf("parse wg private key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}

// RenderEntry renders the wg-quick config for the RU entry node. PostUp/PostDown
// add a policy route so only client-egress traffic uses the tunnel, leaving the
// node's own management traffic (SSH, heartbeat to main) on the default route.
func RenderEntry(c *config.Config) (string, error) {
	w := c.Cascade
	if !w.Enabled {
		return "", fmt.Errorf("cascade is not enabled in config")
	}
	if w.PrivateKey == "" || w.ExitPublicKey == "" || w.ExitEndpoint == "" {
		return "", fmt.Errorf("cascade keys/endpoint not set (run `vlr cascade init`)")
	}
	allowed := w.ExitAllowedIP
	if allowed == "" {
		allowed = "0.0.0.0/0, ::/0"
	}
	keepalive := w.Keepalive
	if keepalive == 0 {
		keepalive = 25
	}
	mtu := w.MTU
	if mtu == 0 {
		mtu = 1420
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# vlr cascade — RU entry side (%s)\n", c.NodeID)
	fmt.Fprintf(&b, "[Interface]\n")
	fmt.Fprintf(&b, "Address = %s\n", w.Address)
	fmt.Fprintf(&b, "PrivateKey = %s\n", w.PrivateKey)
	fmt.Fprintf(&b, "MTU = %d\n", mtu)
	if w.ListenPort != 0 {
		fmt.Fprintf(&b, "ListenPort = %d\n", w.ListenPort)
	}
	// fwmark + ip rule keeps node management traffic off the tunnel.
	fmt.Fprintf(&b, "Table = off\n")
	fmt.Fprintf(&b, "PostUp = ip route add default dev %%i table 51820; ip rule add not fwmark 51820 table 51820; wg set %%i fwmark 51820\n")
	fmt.Fprintf(&b, "PostDown = ip rule del not fwmark 51820 table 51820\n\n")
	fmt.Fprintf(&b, "[Peer]\n")
	fmt.Fprintf(&b, "# EU exit (Aeza)\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", w.ExitPublicKey)
	fmt.Fprintf(&b, "Endpoint = %s\n", w.ExitEndpoint)
	fmt.Fprintf(&b, "AllowedIPs = %s\n", allowed)
	fmt.Fprintf(&b, "PersistentKeepalive = %d\n", keepalive)
	return b.String(), nil
}

// ExitParams is what the EU side needs to render its peer entry for the RU node.
type ExitParams struct {
	NodeID         string
	Interface      string // e.g. wg-cascade
	ListenPort     int    // EU listen port (the RU Endpoint points here)
	Address        string // EU tunnel IP with mask, e.g. 10.66.0.1/24
	PrivateKey     string // EU WG private key
	EntryPublicKey string // RU node WG public key
	EntryTunnelIP  string // RU tunnel IP /32, e.g. 10.66.0.2/32
	WANInterface   string // EU NIC for the NAT masquerade, e.g. eth0
}

// RenderExit renders the wg-quick config for the EU exit node, including the NAT
// masquerade that lets tunnelled client traffic reach the internet.
func RenderExit(p ExitParams) (string, error) {
	if p.PrivateKey == "" || p.EntryPublicKey == "" {
		return "", fmt.Errorf("exit keys not set")
	}
	wan := p.WANInterface
	if wan == "" {
		wan = "eth0"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# vlr cascade — EU exit side for entry %s\n", p.NodeID)
	fmt.Fprintf(&b, "[Interface]\n")
	fmt.Fprintf(&b, "Address = %s\n", p.Address)
	fmt.Fprintf(&b, "PrivateKey = %s\n", p.PrivateKey)
	fmt.Fprintf(&b, "ListenPort = %d\n", p.ListenPort)
	fmt.Fprintf(&b, "PostUp = iptables -A FORWARD -i %%i -j ACCEPT; iptables -A FORWARD -o %%i -j ACCEPT; iptables -t nat -A POSTROUTING -o %s -j MASQUERADE\n", wan)
	fmt.Fprintf(&b, "PostDown = iptables -D FORWARD -i %%i -j ACCEPT; iptables -D FORWARD -o %%i -j ACCEPT; iptables -t nat -D POSTROUTING -o %s -j MASQUERADE\n\n", wan)
	fmt.Fprintf(&b, "[Peer]\n")
	fmt.Fprintf(&b, "# RU entry (Yandex Cloud)\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", p.EntryPublicKey)
	fmt.Fprintf(&b, "AllowedIPs = %s\n", p.EntryTunnelIP)
	return b.String(), nil
}
