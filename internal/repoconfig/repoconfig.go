// Package repoconfig reads Helm's repositories.yaml (`helm repo add`): the
// names, credentials and TLS settings of classic chart repositories.
package repoconfig

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"helm.sh/helm/v4/pkg/helmpath"
	"sigs.k8s.io/yaml"
)

// Entry is one configured repository, as Helm's repo.Entry stores it.
type Entry struct {
	Name                  string `json:"name"`
	URL                   string `json:"url"`
	Username              string `json:"username"`
	Password              string `json:"password"`
	CertFile              string `json:"certFile"`
	KeyFile               string `json:"keyFile"`
	CAFile                string `json:"caFile"`
	InsecureSkipTLSVerify bool   `json:"insecure_skip_tls_verify"`
	PassCredentialsAll    bool   `json:"pass_credentials_all"`
}

type file struct {
	Repositories []*Entry `json:"repositories"`
}

// Config is the set of repositories Helm knows about. The zero value has none.
type Config struct{ entries []*Entry }

// DefaultFile is where Helm keeps repositories.yaml.
func DefaultFile() string {
	if v := os.Getenv("HELM_REPOSITORY_CONFIG"); v != "" {
		return v
	}
	return helmpath.ConfigPath("repositories.yaml")
}

// Load reads path. A missing file means no repositories, as in Helm.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load repositories file %s: %w", path, err)
	}
	var f file
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("load repositories file %s: %w", path, err)
	}
	return &Config{entries: f.Repositories}, nil
}

// New builds a Config from entries, for tests.
func New(entries ...*Entry) *Config { return &Config{entries: entries} }

// NotDefinedError means a dependency names a repository Helm has no entry for.
type NotDefinedError struct{ Repositories []string }

func (e *NotDefinedError) Error() string {
	msg := fmt.Sprintf("no repository definition for %s. Please add them via 'helm repo add'", strings.Join(e.Repositories, ", "))
	for _, r := range e.Repositories {
		if !strings.Contains(r, "//") && !strings.HasPrefix(r, "@") && !strings.HasPrefix(r, "alias:") {
			return msg + `
Note that repositories must be URLs or aliases. For example, to refer to the "example"
repository, use "https://charts.example.com/" or "@example" instead of
"example". Don't forget to add the repo, too ('helm repo add').`
		}
	}
	return msg
}

// IsAlias reports whether repository names a configured repository
// ("@name" or "alias:name") instead of giving its URL.
func IsAlias(repository string) bool {
	return strings.HasPrefix(repository, "@") || strings.HasPrefix(repository, "alias:")
}

// Resolve maps a Chart.yaml repository to its URL and, when Helm knows the
// repository, its entry. Aliases must be configured; URLs need not be.
func (c *Config) Resolve(repository string) (string, *Entry, bool) {
	if IsAlias(repository) {
		name := strings.TrimPrefix(strings.TrimPrefix(repository, "@"), "alias:")
		for _, e := range c.entries {
			if e.Name == name {
				return e.URL, e, true
			}
		}
		return "", nil, false
	}
	return repository, c.Lookup(repository), true
}

// Lookup returns the entry whose URL equals repoURL, or nil.
func (c *Config) Lookup(repoURL string) *Entry {
	for _, e := range c.entries {
		if urlEqual(e.URL, repoURL) {
			return e
		}
	}
	return nil
}

// urlEqual is Helm's internal urlutil.Equal: URLs match after cleaning
// their paths, so a trailing slash does not matter.
func urlEqual(a, b string) bool {
	au, err := url.Parse(a)
	if err != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	bu, err := url.Parse(b)
	if err != nil {
		return false
	}
	for _, u := range []*url.URL{au, bu} {
		if u.Path == "" {
			u.Path = "/"
		}
		u.Path = filepath.Clean(u.Path)
	}
	return au.String() == bu.String()
}

// TLSConfig builds e's TLS settings the way Helm does: a CA file replaces
// the system roots. It returns nil when e needs nothing special.
func TLSConfig(e *Entry) (*tls.Config, error) {
	if e == nil || (e.CAFile == "" && e.CertFile == "" && e.KeyFile == "" && !e.InsecureSkipTLSVerify) {
		return nil, nil
	}
	cfg := &tls.Config{InsecureSkipVerify: e.InsecureSkipTLSVerify} //nolint:gosec // user opted in via repositories.yaml
	if e.CertFile != "" && e.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(e.CertFile, e.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("repository %s: load client certificate: %w", e.Name, err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	if e.CAFile != "" {
		pem, err := os.ReadFile(e.CAFile)
		if err != nil {
			return nil, fmt.Errorf("repository %s: read CA file: %w", e.Name, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("repository %s: no certificates in CA file %s", e.Name, e.CAFile)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}
