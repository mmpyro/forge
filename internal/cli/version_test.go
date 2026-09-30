package cli

import (
	"runtime/debug"
	"strings"
	"testing"
)

func buildInfo(settings ...string) *debug.BuildInfo {
	info := &debug.BuildInfo{GoVersion: "go1.27.1"}
	for i := 0; i+1 < len(settings); i += 2 {
		info.Settings = append(info.Settings, debug.BuildSetting{Key: settings[i], Value: settings[i+1]})
	}
	return info
}

func TestFormatVersionRelease(t *testing.T) {
	info := buildInfo("vcs.revision", "db041d2c0ffee", "vcs.time", "2026-09-30T14:02:00Z", "vcs.modified", "false")
	got := formatVersion("v0.3.1", info, "darwin/arm64")
	want := "forge v0.3.1\n" +
		"  commit:     db041d2\n" +
		"  committed:  30 Sep 2026, 14:02 UTC\n" +
		"  go:         go1.27.1 darwin/arm64\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFormatVersionBareHashIsDevelopmentBuild(t *testing.T) {
	info := buildInfo("vcs.revision", "db041d2c0ffee", "vcs.modified", "true")
	for _, v := range []string{"db041d2", "db041d2-dirty", "dev", ""} {
		got := formatVersion(v, info, "linux/amd64")
		if !strings.HasPrefix(got, "forge development build\n") {
			t.Errorf("version %q: got %q", v, got)
		}
		if !strings.Contains(got, "db041d2 (uncommitted changes)") {
			t.Errorf("version %q: missing dirty marker in %q", v, got)
		}
	}
}

func TestFormatVersionWithoutVCS(t *testing.T) {
	got := formatVersion("v1.0.0", nil, "linux/amd64")
	if !strings.HasPrefix(got, "forge v1.0.0\n  go:") {
		t.Fatalf("got %q", got)
	}
}
