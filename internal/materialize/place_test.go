package materialize

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func withFns(t *testing.T, reflink, hardlink func(string, string) error) {
	t.Helper()
	oldR, oldH := reflinkFn, hardlinkFn
	reflinkFn, hardlinkFn = reflink, hardlink
	t.Cleanup(func() { reflinkFn, hardlinkFn = oldR, oldH })
}

func fail(string, string) error { return errors.New("unsupported") }

func source(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "blob")
	_ = os.WriteFile(p, []byte("archive"), 0o444)
	return p
}

func TestPlaceFallsBackToHardlink(t *testing.T) {
	withFns(t, fail, os.Link)
	src := source(t)
	dst := filepath.Join(t.TempDir(), "out.tgz")
	if m, err := place(src, dst); err != nil || m != Hardlink {
		t.Fatalf("method=%q err=%v, want hardlink", m, err)
	}
	a, _ := os.Stat(src)
	b, _ := os.Stat(dst)
	if !os.SameFile(a, b) {
		t.Fatal("want a hardlink")
	}
}

func TestPlaceFallsBackToCopy(t *testing.T) {
	withFns(t, fail, fail)
	src := source(t)
	dst := filepath.Join(t.TempDir(), "out.tgz")
	if m, err := place(src, dst); err != nil || m != Copy {
		t.Fatalf("method=%q err=%v, want copy", m, err)
	}
	a, _ := os.Stat(src)
	b, _ := os.Stat(dst)
	if os.SameFile(a, b) {
		t.Fatal("want an independent copy")
	}
	if b.Mode().Perm() != 0o644 {
		t.Fatalf("copy mode = %v, want 0644", b.Mode().Perm())
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, []byte("archive")) {
		t.Fatal("copy content differs")
	}
}

func TestPlaceReportsReflink(t *testing.T) {
	withFns(t, copyFile, fail)
	src := source(t)
	dst := filepath.Join(t.TempDir(), "out.tgz")
	if m, err := place(src, dst); err != nil || m != Reflink {
		t.Fatalf("method=%q err=%v, want reflink", m, err)
	}
}

func TestStageRemembersPlacement(t *testing.T) {
	withFns(t, fail, fail)
	st, err := NewStage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := source(t)
	for range 2 { // the duplicate keeps the first placement
		if err := st.Add(src, "a-1.0.0.tgz"); err != nil {
			t.Fatal(err)
		}
	}
	if m := st.Placement("a-1.0.0.tgz"); m != Copy {
		t.Fatalf("placement = %q, want copy", m)
	}
	if m := st.Placement("missing.tgz"); m != "" {
		t.Fatalf("placement of unstaged = %q", m)
	}
}

func TestPlaceWithPlatformDefaults(t *testing.T) {
	src := source(t)
	dst := filepath.Join(t.TempDir(), "out.tgz")
	if _, err := place(src, dst); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, []byte("archive")) {
		t.Fatal("content differs")
	}
}
