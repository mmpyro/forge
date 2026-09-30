package chartmeta_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmarszalek/helm-forge/internal/chartmeta"
)

const golden = "../../testdata/golden/alias"

func TestLoadReadsDependenciesAndLock(t *testing.T) {
	c, err := chartmeta.Load(golden)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Dependencies) != 3 || c.Dependencies[0].Alias != "first" {
		t.Fatalf("dependencies = %+v", c.Dependencies)
	}
	if c.Lock == nil || len(c.Lock.Dependencies) != 3 {
		t.Fatalf("lock = %+v", c.Lock)
	}
}

func TestLoadWithoutLock(t *testing.T) {
	c, err := chartmeta.Load("../../testdata/charts/exact")
	if err != nil {
		t.Fatal(err)
	}
	if c.Lock != nil {
		t.Fatal("want nil lock")
	}
}

func TestLoadRejectsAPIVersionV1(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Chart.yaml"), "apiVersion: v1\nname: old\nversion: 0.1.0\n")
	_, err := chartmeta.Load(dir)
	if err == nil || !strings.Contains(err.Error(), "apiVersion v2 only") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckLock(t *testing.T) {
	c, err := chartmeta.Load(golden)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CheckLock(); err != nil {
		t.Fatalf("golden lock should be in sync: %v", err)
	}
	c.Dependencies[0].Version = "9.9.9"
	if err := c.CheckLock(); !errors.Is(err, chartmeta.ErrLockOutOfSync) {
		t.Fatalf("err = %v, want ErrLockOutOfSync", err)
	}
}

func TestWriteLockReproducesHelmBytes(t *testing.T) {
	c, err := chartmeta.Load(golden)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := chartmeta.WriteLock(dir, c.Lock); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "Chart.lock"))
	want, _ := os.ReadFile(filepath.Join(golden, "Chart.lock"))
	if !bytes.Equal(got, want) {
		t.Fatalf("Chart.lock differs from helm's:\n--- got\n%s\n--- want\n%s", got, want)
	}
}

func TestWriteLockRefusesSymlink(t *testing.T) {
	c, _ := chartmeta.Load(golden)
	dir := t.TempDir()
	if err := os.Symlink("/etc/hosts", filepath.Join(dir, "Chart.lock")); err != nil {
		t.Fatal(err)
	}
	if err := chartmeta.WriteLock(dir, c.Lock); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v, want symlink refusal", err)
	}
}

func TestCheckLockWithoutLockReturnsError(t *testing.T) {
	c, err := chartmeta.Load("../../testdata/charts/exact")
	if err != nil {
		t.Fatal(err)
	}
	if c.Lock != nil {
		t.Fatal("want nil lock")
	}
	err = c.CheckLock()
	if err == nil {
		t.Fatal("want error when checking lock without Chart.lock")
	}
	if errors.Is(err, chartmeta.ErrLockOutOfSync) {
		t.Fatalf("err should not be ErrLockOutOfSync, got %v", err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
