// Package lockdigest recomputes the digest Helm stores in Chart.lock.
//
// It mirrors HashReq in helm.sh/helm/v4/internal/resolver, which cannot be
// imported because it lives in an internal package. The golden Chart.lock
// files in testdata/golden are the authority on its output.
package lockdigest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	chart "helm.sh/helm/v4/pkg/chart/v2"
)

// Compute returns "sha256:<hex>" over the JSON encoding of [req, lock].
func Compute(req, lock []*chart.Dependency) (string, error) {
	data, err := json.Marshal([2][]*chart.Dependency{req, lock})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
