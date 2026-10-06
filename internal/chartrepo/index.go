package chartrepo

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"sigs.k8s.io/yaml"
)

// The index types and loader mirror helm.sh/helm/v4/pkg/repo/v1 (index.go),
// which forge does not import because it pulls in Helm's whole getter and
// plugin stack. Filtering, strictness and sort order must stay identical:
// the first matching version in an entry is the one Helm locks.

// indexFile is a repository's index.yaml.
type indexFile struct {
	ServerInfo  map[string]any           `json:"serverInfo,omitempty"`
	APIVersion  string                   `json:"apiVersion"`
	Generated   time.Time                `json:"generated"`
	Entries     map[string]chartVersions `json:"entries"`
	PublicKeys  []string                 `json:"publicKeys,omitempty"`
	Annotations map[string]string        `json:"annotations,omitempty"`
}

// chartVersion is one entry of index.yaml. The deprecated fields only exist
// so strict parsing accepts old indexes, as in Helm.
type chartVersion struct {
	*chart.Metadata
	URLs    []string  `json:"urls"`
	Created time.Time `json:"created,omitempty"`
	Removed bool      `json:"removed,omitempty"`
	Digest  string    `json:"digest,omitempty"`

	ChecksumDeprecated      string `json:"checksum,omitempty"`
	EngineDeprecated        string `json:"engine,omitempty"`
	TillerVersionDeprecated string `json:"tillerVersion,omitempty"`
	URLDeprecated           string `json:"url,omitempty"`
}

type chartVersions []*chartVersion

func (c chartVersions) Len() int      { return len(c) }
func (c chartVersions) Swap(i, j int) { c[i], c[j] = c[j], c[i] }
func (c chartVersions) Less(a, b int) bool {
	// Failed parse pushes to the back.
	i, err := semver.NewVersion(c[a].Version)
	if err != nil {
		return true
	}
	j, err := semver.NewVersion(c[b].Version)
	if err != nil {
		return false
	}
	return i.LessThan(j)
}

// parseIndex is Helm's loadIndex: drop empty and invalid entries, sort each
// chart's versions highest first, and require an apiVersion.
func parseIndex(data []byte) (*indexFile, error) {
	i := &indexFile{}
	if len(data) == 0 {
		return nil, errors.New("empty index.yaml file")
	}
	var err error
	if json.Valid(data) {
		err = json.Unmarshal(data, i)
	} else {
		err = yaml.UnmarshalStrict(data, i)
	}
	if err != nil {
		return nil, err
	}
	for name, cvs := range i.Entries {
		for idx := len(cvs) - 1; idx >= 0; idx-- {
			if cvs[idx] == nil {
				cvs = append(cvs[:idx], cvs[idx+1:]...)
				continue
			}
			if cvs[idx].Metadata == nil {
				cvs[idx].Metadata = &chart.Metadata{}
			}
			if cvs[idx].APIVersion == "" {
				cvs[idx].APIVersion = chart.APIVersionV1
			}
			if err := cvs[idx].Validate(); skippable(err) != nil {
				cvs = append(cvs[:idx], cvs[idx+1:]...)
			}
		}
		i.Entries[name] = cvs
	}
	for _, vs := range i.Entries {
		sort.Sort(sort.Reverse(vs))
	}
	if i.APIVersion == "" {
		return nil, errors.New("no API version specified")
	}
	return i, nil
}

// skippable ignores the validation error JFrog indexes trigger by
// stripping dependency aliases (helm/helm#12748).
func skippable(err error) error {
	var verr chart.ValidationError
	if errors.As(err, &verr) && strings.HasPrefix(verr.Error(), "validation: more than one dependency with name or alias") {
		return nil
	}
	return err
}

// resolveReferenceURL is Helm's repo.ResolveReferenceURL: refURL as is when
// absolute, else relative to baseURL treated as a directory, keeping
// baseURL's query.
func resolveReferenceURL(baseURL, refURL string) (string, error) {
	ref, err := url.Parse(refURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse %s as URL: %w", refURL, err)
	}
	if ref.IsAbs() {
		return refURL, nil
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse %s as URL: %w", baseURL, err)
	}
	base.RawPath = strings.TrimSuffix(base.RawPath, "/") + "/"
	base.Path = strings.TrimSuffix(base.Path, "/") + "/"
	resolved := base.ResolveReference(ref)
	resolved.RawQuery = base.RawQuery
	return resolved.String(), nil
}
