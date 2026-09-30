// Package testutil holds helpers shared by forge's tests.
package testutil

import (
	"os"
	"testing"

	chart "helm.sh/helm/v4/pkg/chart/v2"
	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"
)

// WriteChartTgz packages a minimal valid chart into dir and returns its path
// (dir/<name>-<version>.tgz).
func WriteChartTgz(t testing.TB, dir, name, version string) string {
	t.Helper()
	c := &chart.Chart{Metadata: &chart.Metadata{APIVersion: chart.APIVersionV2, Name: name, Version: version}}
	p, err := chartutil.Save(c, dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// ChartTgz returns the bytes of a minimal valid chart archive.
func ChartTgz(t testing.TB, name, version string) []byte {
	t.Helper()
	b, err := os.ReadFile(WriteChartTgz(t, t.TempDir(), name, version))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
