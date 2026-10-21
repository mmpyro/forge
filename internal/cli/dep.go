package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/mmarszalek/helm-forge/internal/chartrepo"
	"github.com/mmarszalek/helm-forge/internal/engine"
	"github.com/mmarszalek/helm-forge/internal/registry"
	"github.com/mmarszalek/helm-forge/internal/repoconfig"
	"github.com/mmarszalek/helm-forge/internal/store"
)

type runFunc func(context.Context, engine.Options) (engine.Summary, error)

// netFlags are the flags of every command that talks to registries and
// chart repositories.
type netFlags struct {
	concurrency    int
	perHost        int
	plainHTTP      bool
	timeout        time.Duration
	registryConfig string
	repoConfig     string
}

type depFlags struct {
	netFlags
	refresh bool
	verbose bool
	output  string
}

func addRepoConfigFlag(fl *pflag.FlagSet, p *string) {
	fl.StringVar(p, "repository-config", repoconfig.DefaultFile(), "path to Helm's repositories.yaml (chart repository names and credentials)")
}

func addOutputFlag(fl *pflag.FlagSet, p *string) {
	fl.StringVarP(p, "output", "o", outputText, "output format: text or json (json goes to stdout)")
}

func addNetFlags(fl *pflag.FlagSet, f *netFlags) {
	fl.IntVar(&f.concurrency, "concurrency", 16, "maximum requests in flight in total")
	fl.IntVar(&f.perHost, "per-host", 8, "maximum requests in flight per host")
	fl.BoolVar(&f.plainHTTP, "plain-http", false, "use insecure HTTP connections to OCI registries")
	fl.DurationVar(&f.timeout, "timeout", 5*time.Minute, "time limit for the whole run")
	fl.StringVar(&f.registryConfig, "registry-config", registry.DefaultCredentialsFile(), "path to Helm's registry config file")
	addRepoConfigFlag(fl, &f.repoConfig)
}

func (f netFlags) validate() error {
	if f.concurrency < 1 || f.perHost < 1 {
		return usageError{errors.New("--concurrency and --per-host must be at least 1")}
	}
	if f.timeout <= 0 {
		return usageError{errors.New("--timeout must be positive")}
	}
	return nil
}

// netRuntime is what one networked run shares: the cache, Helm's repository
// config and one set of HTTP clients for registries and chart repositories.
type netRuntime struct {
	http    *registry.HTTP
	reg     *registry.Client
	repos   *chartrepo.Client
	repoCfg *repoconfig.Config
	store   *store.Store
}

func newRuntime(f netFlags) (*netRuntime, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(root)
	if err != nil {
		return nil, err
	}
	repos, err := repoconfig.Load(f.repoConfig)
	if err != nil {
		return nil, err
	}
	userAgent := "helm-forge/" + Version
	h := registry.NewHTTP(registry.Limits{Global: f.concurrency, PerHost: f.perHost}, 60*time.Second)
	reg, err := registry.New(registry.Options{
		CredentialsFile: f.registryConfig,
		PlainHTTP:       f.plainHTTP,
		UserAgent:       userAgent,
		HTTP:            h,
	})
	if err != nil {
		return nil, err
	}
	return &netRuntime{http: h, reg: reg, repos: chartrepo.New(h, repos, userAgent), repoCfg: repos, store: st}, nil
}

func (rt *netRuntime) options(dir string, refresh bool) engine.Options {
	return engine.Options{ChartDir: dir, Registry: rt.reg, Repos: rt.repos, RepoConfig: rt.repoCfg, Store: rt.store, Refresh: refresh}
}

// runErr turns a context error into forge's wording, keeping it matchable
// with errors.Is.
func runErr(err error, timeout time.Duration) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("timed out after %s: %w", timeout, context.DeadlineExceeded)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("interrupted: %w", context.Canceled)
	}
	return err
}

func newDepCmd(out io.Writer) *cobra.Command {
	dep := &cobra.Command{Use: "dep", Aliases: []string{"dependency"}, Short: "Manage a chart's dependencies"}
	dep.AddCommand(
		depSubcommand(out, "build", "Download the dependencies pinned in Chart.lock into charts/", engine.Build),
		depSubcommand(out, "update", "Resolve Chart.yaml dependencies, download them and update Chart.lock", engine.Update),
		newDepTreeCmd(out),
		newDepWhyCmd(out),
		newDepOutdatedCmd(out),
	)
	return dep
}

func depSubcommand(out io.Writer, name, short string, run runFunc) *cobra.Command {
	var f depFlags
	cmd := &cobra.Command{
		Use:   name + " [CHART]",
		Short: short,
		Args:  maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runDep(cmd.Context(), out, "dep "+name, dir, f, run)
		},
	}
	fl := cmd.Flags()
	addNetFlags(fl, &f.netFlags)
	fl.BoolVar(&f.refresh, "refresh", false, "re-check versions with registries and repositories instead of trusting the cache")
	fl.BoolVarP(&f.verbose, "verbose", "v", false, "print per-dependency status (cached, downloaded, local, failed)")
	addOutputFlag(fl, &f.output)
	return cmd
}

func runDep(ctx context.Context, out io.Writer, command, dir string, f depFlags, run runFunc) error {
	if err := checkOutput(f.output); err != nil {
		return err
	}
	if err := f.validate(); err != nil {
		return err
	}
	rt, err := newRuntime(f.netFlags)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	start := time.Now()
	sum, err := run(ctx, rt.options(dir, f.refresh))
	elapsed := time.Since(start)
	err = runErr(err, f.timeout)
	if f.output == outputJSON {
		if werr := writeJSON(out, newJSONResult(command, dir, sum, rt.http.Stats(), elapsed, err)); werr != nil && err == nil {
			return werr
		}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("timed out after %s", f.timeout)
	case errors.Is(err, context.Canceled):
		return errors.New("interrupted")
	case err != nil:
		return err
	case f.output == outputJSON:
		return nil
	}
	if f.verbose {
		for _, d := range sum.Deps {
			label := depLabel(d)
			icon := statusIcon(d.Status)
			fmt.Fprintf(out, "  %s %s %s@%s (%s) [%s]\n",
				icon, string(d.Status), label, d.Version, d.Repository, d.Duration.Round(time.Millisecond))
		}
	}
	local := ""
	if sum.Local > 0 {
		local = fmt.Sprintf(", %d local", sum.Local)
	}
	fmt.Fprintf(out, "Saved %d charts (%d cached, %d downloaded%s) in %s\n",
		sum.Charts, sum.Cached, sum.Downloaded, local, elapsed.Round(time.Millisecond))
	return nil
}

// depLabel returns a display name for a dependency, showing alias→name when
// an alias is set.
func depLabel(d engine.Dep) string {
	if d.Alias != "" && d.Alias != d.Name {
		return d.Alias + "→" + d.Name
	}
	return d.Name
}

// statusIcon returns a short emoji prefix for each status.
func statusIcon(s engine.Status) string {
	switch s {
	case engine.StatusCached:
		return "📦"
	case engine.StatusDownloaded:
		return "⬇️"
	case engine.StatusLocal:
		return "📁"
	case engine.StatusFailed:
		return "❌"
	case engine.StatusSkipped:
		return "⏭️"
	default:
		return "•"
	}
}
