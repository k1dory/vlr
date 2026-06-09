package util

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// pubIPServices are plain-text "what is my IP" endpoints, tried in order. They
// return the bare IP (optionally with a trailing newline), nothing else.
var pubIPServices = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
	"https://icanhazip.com",
	"https://ipinfo.io/ip",
	"https://2ip.ru/api/", // returns the bare IP as plain text
}

// DetectPublicIP returns this host's public IP by querying external services.
// It tries each in turn and returns the first valid IP, so a single service
// being down does not break `vlr init`.
func DetectPublicIP(ctx context.Context) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	var lastErr error
	for _, url := range pubIPServices {
		ip, err := fetchIP(ctx, client, url)
		if err != nil {
			lastErr = err
			continue
		}
		return ip, nil
	}
	return "", fmt.Errorf("could not detect public IP from any service: %w", lastErr)
}

func fetchIP(ctx context.Context, c *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "curl/8.0") // some services serve HTML to browsers
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(body))
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("%s: %q is not an IP", url, ip)
	}
	return ip, nil
}
