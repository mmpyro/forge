package cli

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

var sizeUnits = []struct {
	suffix string
	mult   float64
}{
	// Longest suffixes first, so "KiB" is not read as "B".
	{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30}, {"tib", 1 << 40},
	{"kb", 1e3}, {"mb", 1e6}, {"gb", 1e9}, {"tb", 1e12},
	{"b", 1},
}

// parseSize reads a byte count: a bare number is bytes, KB/MB/GB/TB are
// powers of 1000 and KiB/MiB/GiB/TiB powers of 1024 (case-insensitive).
func parseSize(s string) (int64, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	mult := 1.0
	for _, u := range sizeUnits {
		if strings.HasSuffix(t, u.suffix) {
			t, mult = strings.TrimSpace(strings.TrimSuffix(t, u.suffix)), u.mult
			break
		}
	}
	n, err := strconv.ParseFloat(t, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, fmt.Errorf("invalid size %q: use bytes or a unit like 500MB or 2GiB", s)
	}
	v := n * mult
	if v < 1 {
		return 0, fmt.Errorf("size %q must be at least 1 byte", s)
	}
	if v > math.MaxInt64 {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return int64(v), nil
}

// parseAge reads a duration in Go syntax (90m, 12h) or in days or weeks
// (30d, 2w).
func parseAge(s string) (time.Duration, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	bad := fmt.Errorf("invalid duration %q: use e.g. 12h, 30d or 2w", s)
	if n := len(t); n > 1 && (t[n-1] == 'd' || t[n-1] == 'w') {
		per := 24 * time.Hour
		if t[n-1] == 'w' {
			per *= 7
		}
		v, err := strconv.ParseFloat(t[:n-1], 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v*float64(per)) > math.MaxInt64 {
			return 0, bad
		}
		return time.Duration(v * float64(per)), nil
	}
	d, err := time.ParseDuration(t)
	if err != nil {
		return 0, bad
	}
	return d, nil
}

// formatSize prints n bytes with binary units: "512 B", "104 KiB", "2.1 MiB".
func formatSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	for _, u := range []string{"KiB", "MiB", "GiB", "TiB"} {
		v /= 1024
		if v < 1024 || u == "TiB" {
			if v < 10 {
				return fmt.Sprintf("%.1f %s", v, u)
			}
			return fmt.Sprintf("%.0f %s", v, u)
		}
	}
	return fmt.Sprintf("%d B", n)
}

// formatAge prints how long ago t was, coarsely: "now", "5m ago", "3h ago", "12d ago".
func formatAge(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}
