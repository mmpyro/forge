package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/mmarszalek/helm-forge/internal/engine"
	"github.com/mmarszalek/helm-forge/internal/repoconfig"
)

// OutdatedError is returned by dep outdated --fail-if-outdated when some
// dependency is not up to date.
type OutdatedError struct{ Outdated, Total int }

func (e *OutdatedError) Error() string {
	return fmt.Sprintf("%d of %d dependencies are not up to date", e.Outdated, e.Total)
}

func chartArg(args []string, n int) string {
	if len(args) > n {
		return args[n]
	}
	return "."
}

// inspectChart describes dir's dependencies from local files only.
func inspectChart(dir, repoConfig string) (engine.Report, error) {
	repos, err := repoconfig.Load(repoConfig)
	if err != nil {
		return engine.Report{}, err
	}
	st, _, err := openExistingStore()
	if err != nil {
		return engine.Report{}, err
	}
	return engine.Inspect(engine.InspectOptions{ChartDir: dir, RepoConfig: repos, Store: st})
}

func newDepTreeCmd(out io.Writer) *cobra.Command {
	var repoConfig, output string
	cmd := &cobra.Command{
		Use:   "tree [CHART]",
		Short: "Show each dependency's constraint, locked version, source and cache state (no network)",
		Args:  maxArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			dir := chartArg(args, 0)
			rep, err := inspectChart(dir, repoConfig)
			if err != nil {
				return failJSON(out, output, "dep tree", err)
			}
			if output == outputJSON {
				return writeJSON(out, newInspectJSON("dep tree", dir, rep, rep.Rows))
			}
			printTree(out, rep)
			return nil
		},
	}
	addRepoConfigFlag(cmd.Flags(), &repoConfig)
	addOutputFlag(cmd.Flags(), &output)
	return cmd
}

func newDepWhyCmd(out io.Writer) *cobra.Command {
	var repoConfig, output string
	cmd := &cobra.Command{
		Use:   "why NAME [CHART]",
		Short: "Explain where a dependency (chart name or alias) comes from and which version is used (no network)",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.RangeArgs(1, 2)(cmd, args); err != nil {
				return usageError{err}
			}
			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			name, dir := args[0], chartArg(args, 1)
			rep, err := inspectChart(dir, repoConfig)
			if err != nil {
				return failJSON(out, output, "dep why", err)
			}
			var matches []engine.Row
			for _, r := range rep.Rows {
				if r.Name == name || r.Alias == name {
					matches = append(matches, r)
				}
			}
			if len(matches) == 0 {
				return usageError{fmt.Errorf("%s has no dependency named %q; dependencies: %s", dir, name, rowLabels(rep.Rows))}
			}
			if output == outputJSON {
				return writeJSON(out, newInspectJSON("dep why", dir, rep, matches))
			}
			for i, r := range matches {
				if i > 0 {
					fmt.Fprintln(out)
				}
				printWhy(out, rep, r)
			}
			return nil
		},
	}
	addRepoConfigFlag(cmd.Flags(), &repoConfig)
	addOutputFlag(cmd.Flags(), &output)
	return cmd
}

func newDepOutdatedCmd(out io.Writer) *cobra.Command {
	var (
		f                                 netFlags
		output                            string
		includePrerelease, failIfOutdated bool
	)
	cmd := &cobra.Command{
		Use:   "outdated [CHART]",
		Short: "Compare Chart.lock with the versions registries and chart repositories offer now",
		Args:  maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			if err := f.validate(); err != nil {
				return err
			}
			dir := chartArg(args, 0)
			rt, err := newRuntime(f)
			if err != nil {
				return failJSON(out, output, "dep outdated", err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), f.timeout)
			defer cancel()
			start := time.Now()
			rep, err := engine.Outdated(ctx, rt.options(dir, false), includePrerelease)
			elapsed := time.Since(start)
			err = runErr(err, f.timeout)
			if err == nil && failIfOutdated {
				if n := countOutdated(rep.Rows); n > 0 {
					err = &OutdatedError{Outdated: n, Total: len(rep.Rows)}
				}
			}
			if output == outputJSON {
				if werr := writeJSON(out, newOutdatedJSON(dir, rep, rt.http.Stats(), elapsed, err)); werr != nil && err == nil {
					return werr
				}
			} else if rep.Rows != nil {
				printOutdated(out, rep)
			}
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				return fmt.Errorf("timed out after %s", f.timeout)
			case errors.Is(err, context.Canceled):
				return errors.New("interrupted")
			}
			return err
		},
	}
	fl := cmd.Flags()
	addNetFlags(fl, &f)
	fl.BoolVar(&includePrerelease, "include-prerelease", false, "count prerelease versions as the latest")
	fl.BoolVar(&failIfOutdated, "fail-if-outdated", false, "exit with code 1 if any dependency is not up to date")
	addOutputFlag(fl, &output)
	return cmd
}

func countOutdated(rows []engine.OutdatedRow) int {
	n := 0
	for _, r := range rows {
		if r.Outdated() {
			n++
		}
	}
	return n
}

// rowLabel names a dependency as "name (alias)", or "name" without an alias.
func rowLabel(r engine.Row) string {
	if r.Alias != "" && r.Alias != r.Name {
		return r.Name + " (" + r.Alias + ")"
	}
	return r.Name
}

func rowLabels(rows []engine.Row) string {
	if len(rows) == 0 {
		return "none"
	}
	s := make([]string, len(rows))
	for i, r := range rows {
		s[i] = rowLabel(r)
	}
	return strings.Join(s, ", ")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func stateText(s engine.State) string { return strings.ReplaceAll(string(s), "_", " ") }

func lockNote(rep engine.Report) string {
	switch {
	case !rep.HasLock:
		return "No Chart.lock: run 'forge dep update'"
	case !rep.LockInSync:
		return "Chart.lock is out of sync with Chart.yaml: run 'forge dep update'"
	}
	return ""
}

func printTree(out io.Writer, rep engine.Report) {
	fmt.Fprintf(out, "%s %s\n", rep.Chart, rep.ChartVersion)
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for i, r := range rep.Rows {
		branch := "├── "
		if i == len(rep.Rows)-1 {
			branch = "└── "
		}
		size := "-"
		if r.State == engine.StateCached {
			size = formatSize(r.Size)
		}
		fmt.Fprintf(tw, "%s%s\t%s\t->\t%s\t%s\t%s\t%s\n",
			branch, rowLabel(r), r.Constraint, dash(r.Locked), r.Repository, size, stateText(r.State))
	}
	_ = tw.Flush()
	if note := lockNote(rep); note != "" {
		fmt.Fprintln(out, note)
	}
}

func printWhy(out io.Writer, rep engine.Report, r engine.Row) {
	fmt.Fprintln(out, rowLabel(r))
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "  Chart.yaml:\t%s from %s\n", r.Constraint, r.Repository)
	if r.Condition != "" {
		fmt.Fprintf(tw, "  condition:\t%s\n", r.Condition)
	}
	if len(r.Tags) > 0 {
		fmt.Fprintf(tw, "  tags:\t%s\n", strings.Join(r.Tags, ", "))
	}
	lock := "no entry"
	switch {
	case r.Locked != "" && rep.LockInSync:
		lock = r.Locked
	case r.Locked != "":
		lock = r.Locked + " (Chart.lock is out of sync with Chart.yaml)"
	case !rep.HasLock:
		lock = "no Chart.lock"
	}
	fmt.Fprintf(tw, "  Chart.lock:\t%s\n", lock)
	switch r.State {
	case engine.StateCached:
		fmt.Fprintf(tw, "  cache:\tcached, %s, %s, fetched %s, last used %s\n", r.Digest, formatSize(r.Size),
			r.FetchedAt.UTC().Format(time.RFC3339), formatAge(time.Now(), r.LastUsed))
		fmt.Fprintf(tw, "  \t%s\n", r.BlobPath)
	case engine.StateNotCached:
		fmt.Fprintf(tw, "  cache:\tnot cached: 'forge dep build' downloads it\n")
	case engine.StateLocal:
		fmt.Fprintf(tw, "  cache:\tlocal: packaged from the directory on every run\n")
	}
	if r.FileName != "" {
		present := "missing: run 'forge dep build'"
		if r.InCharts {
			present = "present"
		}
		fmt.Fprintf(tw, "  charts/:\t%s (%s)\n", r.FileName, present)
	}
	_ = tw.Flush()
	if note := lockNote(rep); note != "" && r.Locked == "" {
		fmt.Fprintln(out, note)
	}
}

func printOutdated(out io.Writer, rep engine.OutdatedReport) {
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tCONSTRAINT\tLOCKED\tWANTED\tLATEST\tSTATE")
	for _, r := range rep.Rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", rowLabel(r.Row), r.Constraint, dash(r.Locked), dash(r.Wanted),
			dash(r.Latest), strings.ReplaceAll(string(r.Freshness), "_", " "))
	}
	_ = tw.Flush()
	if n := countOutdated(rep.Rows); n == 0 {
		fmt.Fprintf(out, "All %d dependencies are up to date\n", len(rep.Rows))
	} else {
		fmt.Fprintf(out, "%d of %d dependencies are not up to date\n", n, len(rep.Rows))
	}
}
