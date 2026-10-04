package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mmarszalek/helm-forge/internal/chartmeta"
	"github.com/mmarszalek/helm-forge/internal/engine"
	"github.com/mmarszalek/helm-forge/internal/registry"
	"github.com/mmarszalek/helm-forge/internal/resolve"
)

// schemaVersion of the --output json document. Bump it on any change that
// could break a consumer; adding fields is not one.
const schemaVersion = 1

// Codes for failures that registry.Classify does not produce.
const (
	codeLockOutOfSync = "lock_out_of_sync"
	codeInterrupted   = "interrupted"
	codeUsage         = "usage"
)

const (
	outputText = "text"
	outputJSON = "json"
)

type jsonResult struct {
	SchemaVersion int            `json:"schemaVersion"`
	Command       string         `json:"command"`
	Chart         string         `json:"chart"`
	Success       bool           `json:"success"`
	DurationMs    int64          `json:"durationMs"`
	Summary       jsonSummary    `json:"summary"`
	LockWritten   bool           `json:"lockWritten"`
	Dependencies  []jsonDep      `json:"dependencies"`
	Registries    []jsonRegistry `json:"registries"`
	Error         *jsonError     `json:"error,omitempty"`
}

type jsonSummary struct {
	Saved      int `json:"saved"`
	Cached     int `json:"cached"`
	Downloaded int `json:"downloaded"`
	Failed     int `json:"failed"`
	Skipped    int `json:"skipped"`
}

type jsonDep struct {
	Name       string     `json:"name"`
	Alias      string     `json:"alias,omitempty"`
	Version    string     `json:"version,omitempty"`
	Constraint string     `json:"constraint,omitempty"`
	Repository string     `json:"repository"`
	Status     string     `json:"status"`
	Digest     string     `json:"digest,omitempty"`
	SizeBytes  int64      `json:"sizeBytes,omitempty"`
	Placement  string     `json:"placement,omitempty"`
	DurationMs *int64     `json:"durationMs,omitempty"`
	Error      *jsonError `json:"error,omitempty"`
}

type jsonRegistry struct {
	Host       string `json:"host"`
	Requests   int    `json:"requests"`
	Retries    int    `json:"retries"`
	AuthRounds int    `json:"authRounds"`
}

type jsonError struct {
	Code       string `json:"code"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	Message    string `json:"message"`
	Hint       string `json:"hint,omitempty"`
}

// jsonUsage is printed instead of jsonResult when forge was called wrongly.
type jsonUsage struct {
	SchemaVersion int       `json:"schemaVersion"`
	Command       string    `json:"command"`
	Success       bool      `json:"success"`
	Error         jsonError `json:"error"`
}

func newJSONResult(command, chart string, sum engine.Summary, hosts []registry.HostStats, elapsed time.Duration, err error) jsonResult {
	r := jsonResult{
		SchemaVersion: schemaVersion,
		Command:       command,
		Chart:         chart,
		Success:       err == nil,
		DurationMs:    elapsed.Milliseconds(),
		LockWritten:   sum.LockWritten,
		Dependencies:  make([]jsonDep, 0, len(sum.Deps)),
		Registries:    make([]jsonRegistry, 0, len(hosts)),
	}
	for _, d := range sum.Deps {
		jd := jsonDep{
			Name: d.Name, Alias: d.Alias, Version: d.Version, Constraint: d.Constraint, Repository: d.Repository,
			Status: string(d.Status), Digest: d.Digest, SizeBytes: d.Size, Placement: string(d.Placement),
		}
		if d.Version != "" { // locked, so a fetch was attempted

			ms := d.Duration.Milliseconds()
			jd.DurationMs = &ms
		}
		if d.Problem != nil {
			jd.Error = problemJSON(*d.Problem)
		}
		switch d.Status {
		case engine.StatusCached:
			r.Summary.Cached++
		case engine.StatusDownloaded:
			r.Summary.Downloaded++
		case engine.StatusFailed:
			r.Summary.Failed++
		case engine.StatusSkipped:
			r.Summary.Skipped++
		}
		r.Dependencies = append(r.Dependencies, jd)
	}
	r.Summary.Saved = r.Summary.Cached + r.Summary.Downloaded
	for _, h := range hosts {
		r.Registries = append(r.Registries, jsonRegistry(h))
	}
	if err != nil && !perDependency(err) {
		r.Error = runError(err)
	}
	return r
}

// perDependency reports whether err is fully described by the failed
// entries in "dependencies".
func perDependency(err error) bool {
	var (
		de *engine.DependencyError
		ue *engine.UnsupportedError
		nm *resolve.NoMatchError
	)
	return errors.As(err, &de) || errors.As(err, &ue) || errors.As(err, &nm)
}

func runError(err error) *jsonError {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &jsonError{Code: registry.CodeTimeout, Message: strings.TrimSuffix(err.Error(), ": "+context.DeadlineExceeded.Error()), Hint: "raise --timeout"}
	case errors.Is(err, context.Canceled):
		return &jsonError{Code: codeInterrupted, Message: "interrupted"}
	case errors.Is(err, chartmeta.ErrLockOutOfSync):
		return &jsonError{Code: codeLockOutOfSync, Message: err.Error(), Hint: "run 'forge dep update'"}
	}
	host := ""
	var te *resolve.TagsError
	if errors.As(err, &te) {
		host = registry.Host(te.Repo)
	}
	p := registry.Classify(err, host)
	p.Message = err.Error() // keeps the context (which repository) around the short message
	return problemJSON(p)
}

func problemJSON(p registry.Problem) *jsonError {
	return &jsonError{Code: p.Code, HTTPStatus: p.HTTPStatus, Message: p.Message, Hint: p.Hint}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// jsonUsageCommand reports whether args ask a dep subcommand for JSON
// output, and which one ("dep build"). Flags were not parsed — that is
// what failed — so args are scanned by hand.
func jsonUsageCommand(args []string) (string, bool) {
	var words []string
	wantJSON := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-o" || a == "--output":
			if i+1 < len(args) {
				i++
				wantJSON = wantJSON || args[i] == outputJSON
			}
		case a == "-o"+outputJSON || a == "--output="+outputJSON || a == "-o="+outputJSON:
			wantJSON = true
		case strings.HasPrefix(a, "-"):
		default:
			words = append(words, a)
		}
	}
	if !wantJSON || len(words) < 2 || (words[0] != "dep" && words[0] != "dependency") ||
		(words[1] != "build" && words[1] != "update") {
		return "", false
	}
	return "dep " + words[1], true
}

func usageJSON(w io.Writer, command string, err error) {
	_ = writeJSON(w, jsonUsage{
		SchemaVersion: schemaVersion,
		Command:       command,
		Error:         jsonError{Code: codeUsage, Message: err.Error(), Hint: fmt.Sprintf("run 'forge %s --help'", command)},
	})
}
