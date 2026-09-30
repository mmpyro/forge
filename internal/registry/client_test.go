package registry_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mmarszalek/helm-forge/internal/registry"
	"github.com/mmarszalek/helm-forge/internal/testutil/fakeregistry"
)

var ctx = context.Background()

func newClientWith(t *testing.T, credentialsFile string) *registry.Client {
	t.Helper()
	c, err := registry.New(registry.Options{
		CredentialsFile: credentialsFile,
		PlainHTTP:       true,
		Limits:          registry.Limits{Global: 16, PerHost: 8},
		RequestTimeout:  10 * time.Second,
		UserAgent:       "forge-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// newClient uses a credentials file that does not exist: anonymous access.
func newClient(t *testing.T) *registry.Client {
	return newClientWith(t, filepath.Join(t.TempDir(), "missing", "config.json"))
}

func TestTagsFollowHelmOrderingAndFiltering(t *testing.T) {
	fr := fakeregistry.New(t)
	for _, v := range []string{"1.0.0", "1.10.0", "2.0.0", "0.9.0+build.1", "latest", "v3.0.0"} {
		fr.AddChart("charts/dep-a", v, []byte(v))
	}
	got, err := newClient(t).Tags(ctx, fr.Host()+"/charts/dep-a")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2.0.0", "1.10.0", "1.0.0", "0.9.0+build.1"}
	if !slices.Equal(got, want) {
		t.Fatalf("tags = %v, want %v", got, want)
	}
}

func TestChartManifestFindsChartLayer(t *testing.T) {
	fr := fakeregistry.New(t)
	layer := fr.AddChart("charts/dep-a", "1.0.0", []byte("tgz"))
	cm, err := newClient(t).ChartManifest(ctx, fr.Host()+"/charts/dep-a", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if cm.Layer.Digest != layer || cm.Layer.Size != 3 || cm.Manifest == "" {
		t.Fatalf("manifest = %+v", cm)
	}
}

func TestChartManifestUsesUnderscoreTagForBuildMetadata(t *testing.T) {
	fr := fakeregistry.New(t)
	fr.AddChart("charts/dep-c", "1.0.0+build.1", []byte("tgz"))
	if _, err := newClient(t).ChartManifest(ctx, fr.Host()+"/charts/dep-c", "1.0.0+build.1"); err != nil {
		t.Fatal(err)
	}
	if fr.Count("/manifests/1.0.0_build.1") != 1 {
		t.Fatalf("requests = %v", fr.Requests())
	}
}

const emptyJSON = "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"

func manifestWithLayer(mediaType string) []byte {
	return []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json",`+
		`"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":2},`+
		`"layers":[{"mediaType":%q,"digest":%q,"size":2}]}`, emptyJSON, mediaType, emptyJSON))
}

func TestChartManifestAcceptsLegacyMediaType(t *testing.T) {
	fr := fakeregistry.New(t)
	fr.AddManifest("charts/old", "1.0.0", manifestWithLayer("application/tar+gzip"))
	if _, err := newClient(t).ChartManifest(ctx, fr.Host()+"/charts/old", "1.0.0"); err != nil {
		t.Fatal(err)
	}
}

func TestChartManifestRejectsNonChart(t *testing.T) {
	fr := fakeregistry.New(t)
	fr.AddManifest("charts/image", "1.0.0", manifestWithLayer("application/vnd.oci.image.layer.v1.tar+gzip"))
	_, err := newClient(t).ChartManifest(ctx, fr.Host()+"/charts/image", "1.0.0")
	var nc *registry.NotChartError
	if !errors.As(err, &nc) || !strings.Contains(err.Error(), "application/vnd.oci.image.layer.v1.tar+gzip") {
		t.Fatalf("err = %v, want NotChartError naming the media type", err)
	}
}

func TestOpenBlobStreamsContent(t *testing.T) {
	fr := fakeregistry.New(t)
	fr.AddChart("charts/dep-a", "1.0.0", []byte("chart-bytes"))
	c := newClient(t)
	repo := fr.Host() + "/charts/dep-a"
	cm, err := c.ChartManifest(ctx, repo, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	rc, err := c.OpenBlob(ctx, repo, cm.Layer)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	if string(b) != "chart-bytes" {
		t.Fatalf("blob = %q", b)
	}
}

func TestMissingCredentialsFileMeansAnonymous(t *testing.T) {
	fr := fakeregistry.New(t, fakeregistry.WithBearerAuth())
	fr.AddChart("charts/dep-a", "1.0.0", []byte("x"))
	if _, err := newClient(t).ChartManifest(ctx, fr.Host()+"/charts/dep-a", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	toks := fr.TokenRequests()
	if len(toks) != 1 || toks[0].Authorization != "" {
		t.Fatalf("token requests = %+v, want one anonymous", toks)
	}
}

func TestCredentialsComeFromHelmRegistryConfig(t *testing.T) {
	fr := fakeregistry.New(t, fakeregistry.WithBearerAuth())
	fr.AddChart("charts/dep-a", "1.0.0", []byte("x"))
	basic := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	cfg := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, fr.Host(), basic)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newClientWith(t, cfg).ChartManifest(ctx, fr.Host()+"/charts/dep-a", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	toks := fr.TokenRequests()
	if len(toks) != 1 || toks[0].Authorization != "Basic "+basic {
		t.Fatalf("token requests = %+v, want Basic credentials", toks)
	}
}

func TestWithPullScopesFetchesOneTokenForAllRepositories(t *testing.T) {
	fr := fakeregistry.New(t, fakeregistry.WithBearerAuth())
	var repos []string
	for i := range 5 {
		name := fmt.Sprintf("charts/dep-%d", i)
		fr.AddChart(name, "1.0.0", []byte(name))
		repos = append(repos, fr.Host()+"/"+name)
	}
	c := newClient(t)
	sctx := registry.WithPullScopes(ctx, repos)
	if _, err := c.ChartManifest(sctx, repos[0], "1.0.0"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, r := range repos[1:] {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.ChartManifest(sctx, r, "1.0.0"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	toks := fr.TokenRequests()
	if len(toks) != 1 {
		t.Fatalf("token requests = %d, want 1", len(toks))
	}
	scopes := strings.Join(toks[0].Query["scope"], " ")
	for i := range 5 {
		if want := fmt.Sprintf("repository:charts/dep-%d:pull", i); !strings.Contains(scopes, want) {
			t.Errorf("token scopes %q missing %q", scopes, want)
		}
	}
}

func TestExplain(t *testing.T) {
	fr := fakeregistry.New(t)
	fr.AddChart("charts/dep-a", "1.0.0", []byte("x"))
	c := newClient(t)
	repo := fr.Host() + "/charts/dep-a"
	cases := []struct {
		name    string
		fault   *fakeregistry.Fault
		version string
		want    string
	}{
		{"unauthorized", &fakeregistry.Fault{Status: 401}, "1.0.0", "run 'helm registry login " + fr.Host() + "'"},
		{"forbidden", &fakeregistry.Fault{Status: 403}, "1.0.0", "403 forbidden"},
		{"missing", nil, "9.9.9", "404 not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.fault != nil {
				fr.Fail(fr.ManifestPath("charts/dep-a", tc.version), *tc.fault)
			}
			_, err := c.ChartManifest(ctx, repo, tc.version)
			if err == nil {
				t.Fatal("want error")
			}
			if got := registry.Explain(err, fr.Host()); !strings.Contains(got, tc.want) {
				t.Fatalf("Explain = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestRepoRef(t *testing.T) {
	cases := []struct {
		repository, name, want string
		wantErr                bool
	}{
		{"oci://localhost:5001/charts", "dep-a", "localhost:5001/charts/dep-a", false},
		{"oci://ghcr.io/acme/charts/", "redis", "ghcr.io/acme/charts/redis", false},
		{"https://charts.example.com", "redis", "", true},
		{"oci://", "redis", "", true},
	}
	for _, tc := range cases {
		got, err := registry.RepoRef(tc.repository, tc.name)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("RepoRef(%q, %q) = %q, %v", tc.repository, tc.name, got, err)
		}
	}
}

func TestDefaultCredentialsFileHonoursEnv(t *testing.T) {
	t.Setenv("HELM_REGISTRY_CONFIG", "/tmp/helm-registry.json")
	if got := registry.DefaultCredentialsFile(); got != "/tmp/helm-registry.json" {
		t.Fatalf("got %s", got)
	}
	t.Setenv("HELM_REGISTRY_CONFIG", "")
	if got := registry.DefaultCredentialsFile(); !strings.HasSuffix(got, filepath.Join("helm", "registry", "config.json")) {
		t.Fatalf("got %s", got)
	}
}
