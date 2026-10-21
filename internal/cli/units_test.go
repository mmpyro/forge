package cli

import (
	"testing"
	"time"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{
		"1":       1,
		"1024":    1024,
		"10B":     10,
		"500MB":   500_000_000,
		"500mb":   500_000_000,
		"2GiB":    2 << 30,
		"1.5 KiB": 1536,
		"1TB":     1_000_000_000_000,
	} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "-1", "0.5B", "GiB", "2XB", "1e400", "NaN"} {
		if got, err := parseSize(in); err == nil {
			t.Errorf("parseSize(%q) = %d, want error", in, got)
		}
	}
}

func TestParseAge(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"30d":   30 * 24 * time.Hour,
		"2w":    14 * 24 * time.Hour,
		"1.5d":  36 * time.Hour,
		"12h":   12 * time.Hour,
		"90m":   90 * time.Minute,
		"1h30m": 90 * time.Minute,
		"0s":    0,
	} {
		if got, err := parseAge(in); err != nil || got != want {
			t.Errorf("parseAge(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"", "d", "30", "xd", "1y", "1e300d"} {
		if got, err := parseAge(in); err == nil {
			t.Errorf("parseAge(%q) = %s, want error", in, got)
		}
	}
}

func TestFormatSize(t *testing.T) {
	for n, want := range map[int64]string{
		0:                "0 B",
		1023:             "1023 B",
		1024:             "1.0 KiB",
		104 * 1024:       "104 KiB",
		2202010:          "2.1 MiB",
		3 << 30:          "3.0 GiB",
		5000 * (1 << 40): "5000 TiB",
	} {
		if got := formatSize(n); got != want {
			t.Errorf("formatSize(%d) = %q, want %q", n, got, want)
		}
	}
}
