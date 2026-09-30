package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/mmarszalek/helm-forge/internal/store"
)

func newCacheCmd(out io.Writer) *cobra.Command {
	c := &cobra.Command{Use: "cache", Short: "Inspect or clear forge's local cache"}
	c.AddCommand(
		&cobra.Command{
			Use:   "path",
			Short: "Print the cache directory (for CI cache steps)",
			Args:  maxArgs(0),
			RunE: func(*cobra.Command, []string) error {
				root, err := store.DefaultRoot()
				if err != nil {
					return err
				}
				fmt.Fprintln(out, root)
				return nil
			},
		},
		&cobra.Command{
			Use:   "clean",
			Short: "Delete the cache directory",
			Args:  maxArgs(0),
			RunE: func(*cobra.Command, []string) error {
				root, err := store.DefaultRoot()
				if err != nil {
					return err
				}
				if err := store.Clean(root); err != nil {
					return err
				}
				fmt.Fprintf(out, "Removed %s\n", root)
				return nil
			},
		},
	)
	return c
}
