package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"text/tabwriter"
	"time"

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
		newCacheLsCmd(out),
		newCacheGCCmd(out),
	)
	return c
}

// openExistingStore opens the cache, or returns nil when it does not exist
// yet (so listing an empty cache does not create one).
func openExistingStore() (*store.Store, string, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, "", err
	}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, root, nil
	} else if err != nil {
		return nil, root, err
	}
	st, err := store.Open(root)
	return st, root, err
}

const (
	sortName = "name"
	sortSize = "size"
	sortAge  = "age"
)

func newCacheLsCmd(out io.Writer) *cobra.Command {
	var sortBy, output string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List cached charts with their size and when they were last used",
		Args:  maxArgs(0),
		RunE: func(*cobra.Command, []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			if sortBy != sortName && sortBy != sortSize && sortBy != sortAge {
				return usageError{fmt.Errorf("--sort must be %q, %q or %q, not %q", sortName, sortSize, sortAge, sortBy)}
			}
			st, root, err := openExistingStore()
			if err != nil {
				return failJSON(out, output, "cache ls", err)
			}
			var entries []store.Entry
			if st != nil {
				if entries, err = st.Entries(); err != nil {
					return failJSON(out, output, "cache ls", err)
				}
			}
			sortEntries(entries, sortBy)
			if output == outputJSON {
				return writeJSON(out, newCacheLsJSON(root, entries))
			}
			printEntries(out, entries, time.Now())
			fmt.Fprintf(out, "%d charts, %s in %s\n", len(entries), formatSize(uniqueBlobBytes(entries)), root)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&sortBy, "sort", sortName, "sort by name, size (largest first) or age (least recently used first)")
	fl.StringVarP(&output, "output", "o", outputText, "output format: text or json (json goes to stdout)")
	return cmd
}

func newCacheGCCmd(out io.Writer) *cobra.Command {
	var (
		maxSize, olderThan, grace, output string
		dryRun, verbose                   bool
	)
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Remove least recently used charts from the cache",
		Long: "Remove cached charts not used within --older-than, then the least recently used ones until the\n" +
			"cache holds at most --max-size. Anything used within --grace is kept, so concurrent runs are safe.",
		Args: maxArgs(0),
		RunE: func(*cobra.Command, []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			o := store.GCOptions{DryRun: dryRun}
			var err error
			if maxSize == "" && olderThan == "" {
				return usageError{errors.New("specify --max-size and/or --older-than (use 'forge cache clean' to remove everything)")}
			}
			if maxSize != "" {
				if o.MaxSize, err = parseSize(maxSize); err != nil {
					return usageError{fmt.Errorf("--max-size: %w", err)}
				}
			}
			if olderThan != "" {
				if o.OlderThan, err = parseAge(olderThan); err != nil {
					return usageError{fmt.Errorf("--older-than: %w", err)}
				}
				if o.OlderThan <= 0 {
					return usageError{errors.New("--older-than must be positive")}
				}
			}
			if o.Grace, err = parseAge(grace); err != nil {
				return usageError{fmt.Errorf("--grace: %w", err)}
			}
			if o.Grace < 0 {
				return usageError{errors.New("--grace must not be negative")}
			}

			st, root, err := openExistingStore()
			if err != nil {
				return failJSON(out, output, "cache gc", err)
			}
			var res store.GCResult
			if st != nil {
				if res, err = st.GC(o); err != nil {
					return failJSON(out, output, "cache gc", err)
				}
			}
			if output == outputJSON {
				return writeJSON(out, newCacheGCJSON(root, dryRun, res))
			}
			verb, freed, kept := "Removed", "freed", "kept"
			if dryRun {
				verb, freed, kept = "Would remove", "freeing", "would keep"
			}
			if dryRun || verbose {
				printEntries(out, res.Removed, time.Now())
			}
			fmt.Fprintf(out, "%s %d charts (%d blobs), %s %s; %s %d blobs, %s\n",
				verb, len(res.Removed), res.BlobsRemoved, freed, formatSize(res.BytesFreed),
				kept, res.BlobsKept, formatSize(res.BytesKept))
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&maxSize, "max-size", "", "keep at most this much in the cache, e.g. 500MB or 2GiB")
	fl.StringVar(&olderThan, "older-than", "", "remove charts not used for this long, e.g. 30d, 2w or 12h")
	fl.StringVar(&grace, "grace", store.DefaultGCGrace.String(), "never remove charts used or written this recently")
	fl.BoolVar(&dryRun, "dry-run", false, "print what would be removed without removing anything")
	fl.BoolVarP(&verbose, "verbose", "v", false, "list removed charts")
	fl.StringVarP(&output, "output", "o", outputText, "output format: text or json (json goes to stdout)")
	return cmd
}

func checkOutput(output string) error {
	if output != outputText && output != outputJSON {
		return usageError{fmt.Errorf("--output must be %q or %q, not %q", outputText, outputJSON, output)}
	}
	return nil
}

func sortEntries(es []store.Entry, by string) {
	switch by {
	case sortSize:
		sort.SliceStable(es, func(i, j int) bool { return es[i].BlobSize > es[j].BlobSize })
	case sortAge:
		sort.SliceStable(es, func(i, j int) bool { return es[i].LastUsed.Before(es[j].LastUsed) })
	default:
		sort.SliceStable(es, func(i, j int) bool {
			if es[i].Name() != es[j].Name() {
				return es[i].Name() < es[j].Name()
			}
			return es[i].Repository() < es[j].Repository()
		})
	}
}

func printEntries(out io.Writer, es []store.Entry, now time.Time) {
	if len(es) == 0 {
		return
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "CHART\tVERSION\tSIZE\tLAST-USED\tSOURCE")
	for _, e := range es {
		size := "missing"
		if e.BlobPresent {
			size = formatSize(e.BlobSize)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Name(), e.Version, size, formatAge(now, e.LastUsed), e.Repository())
	}
	_ = tw.Flush()
}

// uniqueBlobBytes is the disk space es take: blobs shared by several refs
// count once.
func uniqueBlobBytes(es []store.Entry) int64 {
	seen := map[string]bool{}
	var n int64
	for _, e := range es {
		if e.BlobPresent && !seen[e.Ref.Layer.String()] {
			seen[e.Ref.Layer.String()] = true
			n += e.BlobSize
		}
	}
	return n
}
