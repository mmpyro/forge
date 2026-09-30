// Package materialize puts cached chart archives into a chart's charts/
// directory without leaving it half-updated.
package materialize

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"helm.sh/helm/v4/pkg/chart/v2/loader"
)

// stagingName sits at the chart root, not in charts/: Helm would load a
// leftover directory in charts/ as a broken subchart.
const stagingName = ".forge-staging"

// Stage collects archives before they replace the contents of charts/.
// A Stage is not safe for concurrent use; callers add archives sequentially.
type Stage struct {
	chartDir string
	dir      string
	files    map[string]bool
}

// NewStage starts a fresh staging directory, deleting any left by a killed run.
func NewStage(chartDir string) (*Stage, error) {
	dir := filepath.Join(chartDir, stagingName)
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Stage{chartDir: chartDir, dir: dir, files: map[string]bool{}}, nil
}

// Add stages the blob at blobPath as fileName (<name>-<version>.tgz).
func (s *Stage) Add(blobPath, fileName string) error {
	if fileName == "" || fileName == "." || fileName == ".." ||
		strings.Contains(fileName, "..") || strings.Contains(fileName, "/") || strings.Contains(fileName, "\\") {
		return fmt.Errorf("invalid archive name %q", fileName)
	}
	if s.files[fileName] {
		return nil
	}
	if err := place(blobPath, filepath.Join(s.dir, fileName)); err != nil {
		return err
	}
	s.files[fileName] = true
	return nil
}

// Commit moves staged archives into charts/ and removes chart archives that
// are no longer dependencies, exactly as `helm dependency build` does: other
// files and subchart directories are left alone. On error, the caller should
// call Abort.
func (s *Stage) Commit() error {
	charts := filepath.Join(s.chartDir, "charts")
	if fi, err := os.Stat(charts); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%q is not a directory", charts)
		}
	} else if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(charts, 0o755); err != nil {
			return err
		}
	} else {
		return err
	}
	existing, err := os.ReadDir(charts)
	if err != nil {
		return err
	}
	for name := range s.files {
		if err := os.Rename(filepath.Join(s.dir, name), filepath.Join(charts, name)); err != nil {
			return err
		}
	}
	for _, e := range existing {
		if e.IsDir() || s.files[e.Name()] {
			continue
		}
		p := filepath.Join(charts, e.Name())
		if _, err := loader.LoadFile(p); err != nil {
			continue // not a chart archive
		}
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	return os.RemoveAll(s.dir)
}

// Abort discards the staging directory; charts/ is not touched.
func (s *Stage) Abort() error { return os.RemoveAll(s.dir) }
