package lockdigest_test

import (
	"path/filepath"
	"strings"
	"testing"

	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/chart/v2/loader"

	"github.com/mmarszalek/helm-forge/internal/lockdigest"
)

func TestComputeMatchesHelmGoldenLocks(t *testing.T) {
	dirs, err := filepath.Glob("../../testdata/golden/*")
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) < 6 {
		t.Fatalf("want >= 6 golden fixtures, got %d (run `make golden`)", len(dirs))
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			c, err := loader.LoadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if c.Lock == nil {
				t.Fatal("golden fixture has no Chart.lock")
			}
			got, err := lockdigest.Compute(c.Metadata.Dependencies, c.Lock.Dependencies)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.Lock.Digest {
				t.Errorf("digest = %s, helm wrote %s", got, c.Lock.Digest)
			}
		})
	}
}

func TestComputeChangesWhenRequirementChanges(t *testing.T) {
	req := []*chart.Dependency{{Name: "a", Version: "^1.0.0", Repository: "oci://r/c"}}
	lock := []*chart.Dependency{{Name: "a", Version: "1.2.0", Repository: "oci://r/c"}}
	d1, err := lockdigest.Compute(req, lock)
	if err != nil {
		t.Fatal(err)
	}
	req[0].Version = "^1.1.0"
	d2, _ := lockdigest.Compute(req, lock)
	if d1 == d2 {
		t.Fatal("digest must change when a requirement changes")
	}
	if !strings.HasPrefix(d1, "sha256:") || len(d1) != len("sha256:")+64 {
		t.Fatalf("bad digest format %q", d1)
	}
}
