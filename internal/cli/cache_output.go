package cli

import (
	"io"
	"time"

	"github.com/mmarszalek/helm-forge/internal/store"
)

// States of a cache entry in JSON output.
const (
	cacheStateOK      = "ok"      // ref and blob present
	cacheStateMissing = "missing" // ref points at a blob that is gone
	cacheStateInvalid = "invalid" // ref file unreadable
)

type jsonCacheEntry struct {
	Name       string     `json:"name"`
	Version    string     `json:"version"`
	Repository string     `json:"repository"`
	Source     string     `json:"source"`
	State      string     `json:"state"`
	Digest     string     `json:"digest,omitempty"`
	SizeBytes  int64      `json:"sizeBytes,omitempty"`
	LastUsed   time.Time  `json:"lastUsed"`
	FetchedAt  *time.Time `json:"fetchedAt,omitempty"`
}

type jsonCacheLs struct {
	SchemaVersion int              `json:"schemaVersion"`
	Command       string           `json:"command"`
	Cache         string           `json:"cache"`
	Success       bool             `json:"success"`
	Summary       jsonCacheLsSum   `json:"summary"`
	Entries       []jsonCacheEntry `json:"entries"`
}

type jsonCacheLsSum struct {
	Charts    int   `json:"charts"`
	SizeBytes int64 `json:"sizeBytes"` // blobs shared by several charts count once
}

type jsonCacheGC struct {
	SchemaVersion int              `json:"schemaVersion"`
	Command       string           `json:"command"`
	Cache         string           `json:"cache"`
	Success       bool             `json:"success"`
	DryRun        bool             `json:"dryRun"`
	Summary       jsonCacheGCSum   `json:"summary"`
	Removed       []jsonCacheEntry `json:"removed"`
}

type jsonCacheGCSum struct {
	ChartsRemoved int   `json:"chartsRemoved"`
	BlobsRemoved  int   `json:"blobsRemoved"`
	BytesFreed    int64 `json:"bytesFreed"`
	BlobsKept     int   `json:"blobsKept"`
	BytesKept     int64 `json:"bytesKept"`
}

// jsonFailure is printed when a command other than dep build/update fails
// with -o json.
type jsonFailure struct {
	SchemaVersion int        `json:"schemaVersion"`
	Command       string     `json:"command"`
	Success       bool       `json:"success"`
	Error         *jsonError `json:"error"`
}

func cacheEntriesJSON(es []store.Entry) []jsonCacheEntry {
	out := make([]jsonCacheEntry, 0, len(es))
	for _, e := range es {
		j := jsonCacheEntry{
			Name: e.Name(), Version: e.Version, Repository: e.Repository(), Source: e.Source,
			LastUsed: e.LastUsed.UTC().Truncate(time.Second),
		}
		switch {
		case !e.Valid:
			j.State = cacheStateInvalid
		case !e.BlobPresent:
			j.State, j.Digest = cacheStateMissing, e.Ref.Layer.String()
		default:
			j.State, j.Digest, j.SizeBytes = cacheStateOK, e.Ref.Layer.String(), e.BlobSize
		}
		if e.Valid && !e.Ref.FetchedAt.IsZero() {
			t := e.Ref.FetchedAt.UTC().Truncate(time.Second)
			j.FetchedAt = &t
		}
		out = append(out, j)
	}
	return out
}

func newCacheLsJSON(root string, es []store.Entry) jsonCacheLs {
	return jsonCacheLs{
		SchemaVersion: schemaVersion, Command: "cache ls", Cache: root, Success: true,
		Summary: jsonCacheLsSum{Charts: len(es), SizeBytes: uniqueBlobBytes(es)},
		Entries: cacheEntriesJSON(es),
	}
}

func newCacheGCJSON(root string, dryRun bool, r store.GCResult) jsonCacheGC {
	return jsonCacheGC{
		SchemaVersion: schemaVersion, Command: "cache gc", Cache: root, Success: true, DryRun: dryRun,
		Summary: jsonCacheGCSum{
			ChartsRemoved: len(r.Removed), BlobsRemoved: r.BlobsRemoved, BytesFreed: r.BytesFreed,
			BlobsKept: r.BlobsKept, BytesKept: r.BytesKept,
		},
		Removed: cacheEntriesJSON(r.Removed),
	}
}

// failJSON prints a failure document when output is JSON, and returns err
// either way.
func failJSON(w io.Writer, output, command string, err error) error {
	if output == outputJSON {
		_ = writeJSON(w, jsonFailure{SchemaVersion: schemaVersion, Command: command, Error: runError(err)})
	}
	return err
}
