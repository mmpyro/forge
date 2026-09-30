package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// versionText renders `forge --version`: the release name first, then build
// details from the VCS stamp Go embeds in every binary built from a checkout.
func versionText() string {
	info, _ := debug.ReadBuildInfo()
	return formatVersion(Version, info, runtime.GOOS+"/"+runtime.GOARCH)
}

func formatVersion(version string, info *debug.BuildInfo, platform string) string {
	var rev, modified string
	var committed time.Time
	goVersion := runtime.Version()
	if info != nil {
		goVersion = info.GoVersion
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				modified = s.Value
			case "vcs.time":
				committed, _ = time.Parse(time.RFC3339, s.Value)
			}
		}
	}
	short := rev
	if len(short) > 7 {
		short = short[:7]
	}

	// `git describe --always` falls back to a bare hash when there is no tag;
	// that is a development build, not a release name.
	name := strings.TrimSuffix(version, "-dirty")
	if name == "" || name == "dev" || (short != "" && strings.HasPrefix(rev, name)) || isHex(name) {
		name = "development build"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "forge %s\n", name)
	if short != "" {
		if modified == "true" || strings.HasSuffix(version, "-dirty") {
			short += " (uncommitted changes)"
		}
		fmt.Fprintf(&b, "  commit:     %s\n", short)
	}
	if !committed.IsZero() {
		fmt.Fprintf(&b, "  committed:  %s\n", committed.UTC().Format("2 Jan 2006, 15:04 MST"))
	}
	fmt.Fprintf(&b, "  go:         %s %s\n", goVersion, platform)
	return b.String()
}

func isHex(s string) bool {
	if len(s) < 7 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
