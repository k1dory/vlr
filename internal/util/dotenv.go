package util

import (
	"bufio"
	"os"
	"strings"
)

// LoadDotEnv reads the first existing file from paths and returns its KEY=VALUE
// pairs. It is a tiny, dependency-free parser: blank lines and `#` comments are
// skipped, surrounding quotes are stripped, and a leading `export ` is ignored.
// Missing files are not an error (returns whatever it found, or an empty map).
func LoadDotEnv(paths ...string) map[string]string {
	out := map[string]string{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "export ")
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			v = strings.Trim(v, `"'`)
			if k != "" {
				out[k] = v
			}
		}
		f.Close()
		return out // first existing file wins
	}
	return out
}

// DotEnvCandidates returns the standard places vlr looks for a .env, in order:
// $VLR_ENV, ./.env (cwd, e.g. the cloned /opt/vlr), then /etc/vlr/.env.
func DotEnvCandidates() []string {
	return []string{os.Getenv("VLR_ENV"), ".env", "/etc/vlr/.env"}
}

// SplitList splits a comma-separated env value into trimmed, non-empty items.
func SplitList(v string) []string {
	var out []string
	for s := range strings.SplitSeq(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
