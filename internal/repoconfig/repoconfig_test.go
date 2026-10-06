package repoconfig_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mmarszalek/helm-forge/internal/repoconfig"
)

func TestLoadMissingFileIsEmpty(t *testing.T) {
	c, err := repoconfig.Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.Resolve("@x"); ok {
		t.Fatal("resolved an alias from an empty config")
	}
}

func TestLoadAndResolve(t *testing.T) {
	p := filepath.Join(t.TempDir(), "repositories.yaml")
	y := `apiVersion: ""
generated: "2026-01-01T00:00:00Z"
repositories:
- name: stable
  url: https://charts.example.com/stable
  username: u
  password: p
  pass_credentials_all: true
`
	if err := os.WriteFile(p, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := repoconfig.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"@stable", "alias:stable"} {
		u, e, ok := c.Resolve(ref)
		if !ok || u != "https://charts.example.com/stable" || e.Username != "u" || !e.PassCredentialsAll {
			t.Fatalf("%s: %s %+v %v", ref, u, e, ok)
		}
	}
	// URLs match Helm's urlutil.Equal: a trailing slash does not matter.
	if e := c.Lookup("https://charts.example.com/stable/"); e == nil || e.Name != "stable" {
		t.Fatalf("lookup = %+v", e)
	}
	if e := c.Lookup("https://charts.example.com/other"); e != nil {
		t.Fatalf("lookup = %+v", e)
	}
	if u, e, ok := c.Resolve("https://elsewhere"); !ok || e != nil || u != "https://elsewhere" {
		t.Fatalf("unregistered URL: %s %+v %v", u, e, ok)
	}
}

func TestTLSConfig(t *testing.T) {
	if cfg, err := repoconfig.TLSConfig(&repoconfig.Entry{}); cfg != nil || err != nil {
		t.Fatalf("plain entry: %v %v", cfg, err)
	}
	cfg, err := repoconfig.TLSConfig(&repoconfig.Entry{InsecureSkipTLSVerify: true})
	if err != nil || cfg == nil || !cfg.InsecureSkipVerify {
		t.Fatalf("insecure: %v %v", cfg, err)
	}
	if _, err := repoconfig.TLSConfig(&repoconfig.Entry{Name: "x", CAFile: "/nonexistent"}); err == nil {
		t.Fatal("want error for missing CA file")
	}
}
