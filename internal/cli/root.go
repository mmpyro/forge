// Package cli is forge's command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// Version is set by cmd/forge from main.Version (injected with -ldflags).
var Version = "dev"

// usageError marks mistakes in how forge was called (exit code 2).
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// Run executes forge with args and returns the process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	root := newRootCmd(stdout)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	fmt.Fprintln(stderr, "Error:", err)
	var ue usageError
	if errors.As(err, &ue) || strings.HasPrefix(err.Error(), "unknown command") {
		if cmd, ok := jsonUsageCommand(args); ok {
			usageJSON(stdout, cmd, err)
		}
		return 2
	}
	return 1
}

func newRootCmd(out io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "forge",
		Short:         "Fast dependency fetching for Helm charts",
		Version:       versionText(),
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetVersionTemplate("{{.Version}}")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.AddCommand(newDepCmd(out), newCacheCmd(out))
	return root
}

func maxArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.MaximumNArgs(n)(cmd, args); err != nil {
			return usageError{err}
		}
		return nil
	}
}
