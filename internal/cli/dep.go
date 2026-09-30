package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/mmarszalek/helm-forge/internal/engine"
	"github.com/mmarszalek/helm-forge/internal/registry"
	"github.com/mmarszalek/helm-forge/internal/store"
)

type runFunc func(context.Context, engine.Options) (engine.Summary, error)

type depFlags struct {
	concurrency    int
	perHost        int
	refresh        bool
	plainHTTP      bool
	timeout        time.Duration
	registryConfig string
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
			return runDep(cmd.Context(), out, dir, f, run)
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&f.concurrency, "concurrency", 16, "maximum requests in flight in total")
	fl.IntVar(&f.perHost, "per-host", 8, "maximum requests in flight per host")
	fl.BoolVar(&f.refresh, "refresh", false, "re-check tags with the registry instead of trusting the cache")
	fl.BoolVar(&f.plainHTTP, "plain-http", false, "use insecure HTTP connections to registries")
	fl.DurationVar(&f.timeout, "timeout", 5*time.Minute, "time limit for the whole run")
	fl.StringVar(&f.registryConfig, "registry-config", registry.DefaultCredentialsFile(), "path to Helm's registry config file")
	return cmd
}

func runDep(ctx context.Context, out io.Writer, dir string, f depFlags, run runFunc) error {
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
	reg, err := registry.New(registry.Options{
		CredentialsFile: f.registryConfig,
		PlainHTTP:       f.plainHTTP,
		Limits:          registry.Limits{Global: f.concurrency, PerHost: f.perHost},
		RequestTimeout:  60 * time.Second,
		UserAgent:       "helm-forge/" + Version,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	start := time.Now()
	sum, err := run(ctx, engine.Options{ChartDir: dir, Registry: reg, Store: st, Refresh: f.refresh})
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("timed out after %s", f.timeout)
	case errors.Is(err, context.Canceled):
		return errors.New("interrupted")
	case err != nil:
		return err
	}
	fmt.Fprintf(out, "Saved %d charts (%d cached, %d downloaded) in %s\n",
		sum.Charts, sum.Cached, sum.Downloaded, time.Since(start).Round(time.Millisecond))
	return nil
}
