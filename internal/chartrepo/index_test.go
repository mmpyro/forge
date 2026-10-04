package chartrepo

import (
	"slices"
	"strings"
	"testing"
)

func TestParseIndexSortsAndDropsInvalidEntries(t *testing.T) {
	idx, err := parseIndex([]byte(`apiVersion: v1
entries:
  dep-a:
  - {apiVersion: v2, name: dep-a, version: 1.0.0, urls: [a-1.0.0.tgz]}
  - {apiVersion: v2, name: dep-a, version: 2.0.0-rc.1, urls: [a-2.0.0-rc.1.tgz]}
  - {apiVersion: v2, name: dep-a, version: 1.10.0, urls: [a-1.10.0.tgz]}
  - {apiVersion: v2, name: dep-a, version: not-semver, urls: [x.tgz]}
  - {apiVersion: v2, name: "", version: 9.0.0, urls: [y.tgz]}
  -
generated: "2026-01-01T00:00:00Z"
`))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range idx.Entries["dep-a"] {
		got = append(got, v.Version)
	}
	if want := []string{"2.0.0-rc.1", "1.10.0", "1.0.0"}; !slices.Equal(got, want) {
		t.Fatalf("versions = %v, want %v", got, want)
	}
}

func TestParseIndexErrors(t *testing.T) {
	for in, want := range map[string]string{
		"":                              "empty index.yaml",
		"entries: {}\n":                 "no API version",
		"apiVersion: v1\nbogus: true\n": "unknown field",
	} {
		if _, err := parseIndex([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", in, err, want)
		}
	}
}

func TestParseIndexAcceptsJSON(t *testing.T) {
	idx, err := parseIndex([]byte(`{"apiVersion":"v1","entries":{"a":[{"apiVersion":"v2","name":"a","version":"1.0.0","urls":["a.tgz"]}]}}`))
	if err != nil || len(idx.Entries["a"]) != 1 {
		t.Fatalf("idx = %+v, err = %v", idx, err)
	}
}

func TestResolveReferenceURL(t *testing.T) {
	for _, c := range []struct{ base, ref, want string }{
		{"https://h/charts", "a-1.0.0.tgz", "https://h/charts/a-1.0.0.tgz"},
		{"https://h/charts/", "a-1.0.0.tgz", "https://h/charts/a-1.0.0.tgz"},
		{"https://h/charts", "https://cdn/a.tgz", "https://cdn/a.tgz"},
		{"https://h/charts?token=x", "a.tgz", "https://h/charts/a.tgz?token=x"},
		{"https://h", "index.yaml", "https://h/index.yaml"},
	} {
		if got, err := resolveReferenceURL(c.base, c.ref); err != nil || got != c.want {
			t.Errorf("(%s, %s) = %s, %v; want %s", c.base, c.ref, got, err, c.want)
		}
	}
}

func TestCacheKeyAndIsRepoURL(t *testing.T) {
	if got := CacheKey("https://charts.example.com:8443/stable/", "redis"); got != "https/charts.example.com:8443/stable/redis" {
		t.Fatalf("CacheKey = %s", got)
	}
	for in, want := range map[string]bool{
		"https://charts.example.com": true,
		"http://localhost:5002":      true,
		"oci://ghcr.io/x":            false,
		"file://x":                   false,
		"stable":                     false,
		"@stable":                    false,
	} {
		if IsRepoURL(in) != want {
			t.Errorf("IsRepoURL(%q) = %v", in, !want)
		}
	}
}
