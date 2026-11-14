package cascade

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// EgressCheckURL returns the caller's public IPv4 as plain text.
var EgressCheckURL = "https://api.ipify.org"

// EgressResult compares where Xray's (marked) traffic leaves from with where the
// node's own unmarked traffic leaves from. If they match, the cascade is up but
// bypassed — the failure a handshake or `curl --interface` probe cannot see.
type EgressResult struct {
	Marked   string // public IP seen for traffic carrying the cascade fwmark
	Direct   string // public IP seen for the node's own traffic
	Err      error
	Bypassed bool
}

// OK reports whether marked traffic provably leaves through the exit.
func (r EgressResult) OK() bool { return r.Err == nil && r.Marked != "" && !r.Bypassed }

// Egress fetches the public IP twice — with the cascade fwmark (as Xray does)
// and without — and flags the cascade as bypassed when both are the same.
func Egress(ctx context.Context, mark int, timeout time.Duration) EgressResult {
	var r EgressResult
	r.Marked, r.Err = publicIP(ctx, mark, timeout)
	if r.Err != nil {
		r.Err = fmt.Errorf("marked request: %w", r.Err)
		return r
	}
	direct, err := publicIP(ctx, 0, timeout)
	if err == nil {
		r.Direct = direct
		r.Bypassed = direct == r.Marked
	}
	return r
}

func publicIP(ctx context.Context, mark int, timeout time.Duration) (string, error) {
	d := &net.Dialer{Timeout: timeout}
	if mark != 0 {
		ctl, err := markControl(mark)
		if err != nil {
			return "", err
		}
		d.Control = ctl
	}
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp4", addr)
		},
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, EgressCheckURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: HTTP %d", EgressCheckURL, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(b))
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("%s: unexpected answer %q", EgressCheckURL, ip)
	}
	return ip, nil
}

// FormatEgress renders the policy/egress lines shown by `vlr cascade check`.
func FormatEgress(st RuleState, stErr error, eg EgressResult) string {
	var b strings.Builder
	mark := func(ok bool) string {
		if ok {
			return "[OK]"
		}
		return "[FAIL]"
	}
	if stErr != nil {
		fmt.Fprintf(&b, "маршрутизация каскада [FAIL]\n  logs: %v\n", stErr)
	} else {
		fmt.Fprintf(&b, "правило fwmark → туннель %s\n", mark(st.Fwmark))
		fmt.Fprintf(&b, "правило oif → туннель    %s\n", mark(st.Oif))
		fmt.Fprintf(&b, "маршрут в таблице туннеля %s\n", mark(st.Route))
	}
	switch {
	case eg.Err != nil:
		fmt.Fprintf(&b, "выход трафика Xray [FAIL]\n  logs: %v\n", eg.Err)
	case eg.Bypassed:
		fmt.Fprintf(&b, "выход трафика Xray %s [FAIL] — совпадает с адресом узла, каскад обходится\n", eg.Marked)
	case eg.Direct == "":
		fmt.Fprintf(&b, "выход трафика Xray %s [OK] (адрес узла узнать не удалось)\n", eg.Marked)
	default:
		fmt.Fprintf(&b, "выход трафика Xray %s [OK] (узел напрямую: %s)\n", eg.Marked, eg.Direct)
	}
	return b.String()
}
