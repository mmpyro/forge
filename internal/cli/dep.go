package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/mmarszalek/helm-forge/internal/chartrepo"
	"github.com/mmarszalek/helm-forge/internal/engine"
	"github.com/mmarszalek/helm-forge/internal/registry"
	"github.com/mmarszalek/helm-forge/internal/repoconfig"
	"github.com/mmarszalek/helm-forge/internal/store"
)

type runFunc func(context.Context, engine.Options) (engine.Summary, error)

type depFlags struct {
	concurrency    int
	perHost        int
	refresh        bool
	plainHTTP      bool
	verbose        bool
	timeout        time.Duration
	registryConfig string
	repoConfig     string
	output         string
}

func newDepCmd(out io.Writer) *cobra.Command {
	dep := &cobra.Command{Use: "dep", Aliases: []string{"dependency"}, Short: "Manage a chart's dependencies"}
	dep.AddCommand(
		depSubcommand(out, "build", "Download the dependencies pinned in Chart.lock into charts/", engine.Build),
		depSubcommand(out, "update", "Resolve Chart.yaml dependencies, download them and update Chart.lock", engine.Update),
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
	fl.IntVar(&f.concurrency, "concurrency", 16, "maximum requests in flight in total")
	fl.IntVar(&f.perHost, "per-host", 8, "maximum requests in flight per host")
	fl.BoolVar(&f.refresh, "refresh", false, "re-check versions with registries and repositories instead of trusting the cache")
	fl.BoolVar(&f.plainHTTP, "plain-http", false, "use insecure HTTP connections to OCI registries")
	fl.BoolVarP(&f.verbose, "verbose", "v", false, "print per-dependency status (cached, downloaded, local, failed)")
	fl.DurationVar(&f.timeout, "timeout", 5*time.Minute, "time limit for the whole run")
	fl.StringVar(&f.registryConfig, "registry-config", registry.DefaultCredentialsFile(), "path to Helm's registry config file")
	fl.StringVar(&f.repoConfig, "repository-config", repoconfig.DefaultFile(), "path to Helm's repositories.yaml (chart repository names and credentials)")
	fl.StringVarP(&f.output, "output", "o", outputText, "output format: text or json (json goes to stdout)")
	return cmd
}

func runDep(ctx context.Context, out io.Writer, command, dir string, f depFlags, run runFunc) error {
	if f.output != outputText && f.output != outputJSON {
		return usageError{fmt.Errorf("--output must be %q or %q, not %q", outputText, outputJSON, f.output)}
	}
	if f.concurrency < 1 || f.perHost < 1 {
		return usageError{errors.New("--concurrency and --per-host must be at least 1")}
	}
	if f.timeout <= 0 {
		return usageError{errors.New("--timeout must be positive")}
	}
	root, err := store.DefaultRoot()
	if err != nil {
		return err
	}
	st, err := store.Open(root)
	if err != nil {
		return err
	}
	repos, err := repoconfig.Load(f.repoConfig)
	if err != nil {
		return err
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
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	start := time.Now()
	sum, err := run(ctx, engine.Options{
		ChartDir:   dir,
		Registry:   reg,
		Repos:      chartrepo.New(h, repos, userAgent),
		RepoConfig: repos,
		Store:      st,
		Refresh:    f.refresh,
	})
	elapsed := time.Since(start)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		err = fmt.Errorf("timed out after %s: %w", f.timeout, context.DeadlineExceeded)
	case errors.Is(err, context.Canceled):
		err = fmt.Errorf("interrupted: %w", context.Canceled)
	}
	if f.output == outputJSON {
		if werr := writeJSON(out, newJSONResult(command, dir, sum, h.Stats(), elapsed, err)); werr != nil && err == nil {
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
