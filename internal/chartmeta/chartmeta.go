// Package chartmeta reads and writes the Helm files forge works from:
// Chart.yaml and Chart.lock.
package chartmeta

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	"sigs.k8s.io/yaml"

	"github.com/mmarszalek/helm-forge/internal/lockdigest"
)

// ErrLockOutOfSync is Helm's own message for a Chart.lock that no longer
// matches Chart.yaml.
var ErrLockOutOfSync = errors.New("the lock file (Chart.lock) is out of sync with the dependencies file (Chart.yaml). Please update the dependencies")

// Chart is the part of a chart directory that dependency management needs.
type Chart struct {
	Dir          string
	Dependencies []*chart.Dependency // as declared in Chart.yaml
	Lock         *chart.Lock         // nil when Chart.lock is absent
}

// Load reads the chart in dir with Helm's own loader, so dependency fields
// are sanitised exactly as Helm sanitises them before hashing.
func Load(dir string) (*Chart, error) {
	c, err := loader.LoadDir(dir)
	if err != nil {
		return nil, err
	}
	if c.Metadata.APIVersion != chart.APIVersionV2 {
		return nil, fmt.Errorf("chart %s uses apiVersion %q; forge supports apiVersion v2 only", dir, c.Metadata.APIVersion)
	}
	return &Chart{Dir: dir, Dependencies: c.Metadata.Dependencies, Lock: c.Lock}, nil
}

// CheckLock reports ErrLockOutOfSync when Chart.lock's digest does not match
// Chart.yaml.
func (c *Chart) CheckLock() error {
	if c.Lock == nil {
		return fmt.Errorf("%s has no Chart.lock", c.Dir)
	}
	sum, err := lockdigest.Compute(c.Dependencies, c.Lock.Dependencies)
	if err != nil || sum != c.Lock.Digest {
		return ErrLockOutOfSync
	}
	return nil
}

// WriteLock writes dir/Chart.lock byte-for-byte as Helm would.
func WriteLock(dir string, lock *chart.Lock) error {
	data, err := yaml.Marshal(lock)
	if err != nil {
		return err
	}
	dest := filepath.Join(dir, "Chart.lock")
	info, err := os.Lstat(dest)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		link, _ := os.Readlink(dest)
		return fmt.Errorf("the Chart.lock file is a symlink to %q", link)
	case err != nil && !os.IsNotExist(err):
		return fmt.Errorf("error getting info for %q: %w", dest, err)
	}
	return os.WriteFile(dest, data, 0o644)
}
