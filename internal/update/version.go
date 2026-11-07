package update

import (
	"strconv"
	"strings"
)

// newerVersion reports whether latest is strictly newer than current.
// Versions look like "0.1.0" (optionally "v"-prefixed); pre-release/build
// suffixes are ignored. When either side cannot be parsed as semver-ish, the
// versions compare by difference.
func newerVersion(latest, current string) bool {
	l, lok := parseVersion(latest)
	c, cok := parseVersion(current)
	if !lok || !cok {
		return normalize(latest) != normalize(current)
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func normalize(v string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = normalize(v)
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return out, false
	}
	parts := strings.Split(v, ".")
	if len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
