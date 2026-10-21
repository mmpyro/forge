package cli

import (
	"time"

	"github.com/mmarszalek/helm-forge/internal/engine"
	"github.com/mmarszalek/helm-forge/internal/registry"
)

// codeOutdated is the error code of dep outdated --fail-if-outdated.
const codeOutdated = "outdated"

type jsonInspect struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Command       string               `json:"command"`
	Chart         string               `json:"chart"`
	Success       bool                 `json:"success"`
	DurationMs    *int64               `json:"durationMs,omitempty"`
	ChartName     string               `json:"chartName"`
	ChartVersion  string               `json:"chartVersion"`
	HasLock       bool                 `json:"hasLock"`
	LockInSync    bool                 `json:"lockInSync"`
	Summary       *jsonOutdatedSummary `json:"summary,omitempty"`
	Dependencies  []jsonInspectDep     `json:"dependencies"`
	Registries    []jsonRegistry       `json:"registries,omitempty"`
	Error         *jsonError           `json:"error,omitempty"`
}

type jsonOutdatedSummary struct {
	Total    int `json:"total"`
	Outdated int `json:"outdated"`
}

type jsonInspectDep struct {
	Name       string     `json:"name"`
	Alias      string     `json:"alias,omitempty"`
	Constraint string     `json:"constraint"`
	Repository string     `json:"repository"`
	Condition  string     `json:"condition,omitempty"`
	Tags       []string   `json:"tags,omitempty"`
	Locked     string     `json:"locked,omitempty"`
	State      string     `json:"state"`
	Digest     string     `json:"digest,omitempty"`
	SizeBytes  int64      `json:"sizeBytes,omitempty"`
	FetchedAt  *time.Time `json:"fetchedAt,omitempty"`
	LastUsed   *time.Time `json:"lastUsed,omitempty"`
	BlobPath   string     `json:"blobPath,omitempty"`
	FileName   string     `json:"fileName,omitempty"`
	InCharts   bool       `json:"inCharts"`
	Wanted     string     `json:"wanted,omitempty"`
	Latest     string     `json:"latest,omitempty"`
	Freshness  string     `json:"freshness,omitempty"`
}

func inspectDepJSON(r engine.Row) jsonInspectDep {
	j := jsonInspectDep{
		Name: r.Name, Alias: r.Alias, Constraint: r.Constraint, Repository: r.Repository,
		Condition: r.Condition, Tags: r.Tags, Locked: r.Locked, State: string(r.State),
		Digest: r.Digest, SizeBytes: r.Size, BlobPath: r.BlobPath, FileName: r.FileName, InCharts: r.InCharts,
	}
	if !r.FetchedAt.IsZero() {
		t := r.FetchedAt.UTC().Truncate(time.Second)
		j.FetchedAt = &t
	}
	if !r.LastUsed.IsZero() {
		t := r.LastUsed.UTC().Truncate(time.Second)
		j.LastUsed = &t
	}
	return j
}

func newInspectJSON(command, chart string, rep engine.Report, rows []engine.Row) jsonInspect {
	j := jsonInspect{
		SchemaVersion: schemaVersion, Command: command, Chart: chart, Success: true,
		ChartName: rep.Chart, ChartVersion: rep.ChartVersion, HasLock: rep.HasLock, LockInSync: rep.LockInSync,
		Dependencies: make([]jsonInspectDep, 0, len(rows)),
	}
	for _, r := range rows {
		j.Dependencies = append(j.Dependencies, inspectDepJSON(r))
	}
	return j
}

func newOutdatedJSON(chart string, rep engine.OutdatedReport, hosts []registry.HostStats, elapsed time.Duration, err error) jsonInspect {
	ms := elapsed.Milliseconds()
	j := jsonInspect{
		SchemaVersion: schemaVersion, Command: "dep outdated", Chart: chart, Success: err == nil, DurationMs: &ms,
		ChartName: rep.Chart, ChartVersion: rep.ChartVersion, HasLock: rep.HasLock, LockInSync: rep.LockInSync,
		Summary:      &jsonOutdatedSummary{Total: len(rep.Rows), Outdated: countOutdated(rep.Rows)},
		Dependencies: make([]jsonInspectDep, 0, len(rep.Rows)),
		Registries:   make([]jsonRegistry, 0, len(hosts)),
	}
	for _, r := range rep.Rows {
		d := inspectDepJSON(r.Row)
		d.Wanted, d.Latest, d.Freshness = r.Wanted, r.Latest, string(r.Freshness)
		j.Dependencies = append(j.Dependencies, d)
	}
	for _, h := range hosts {
		j.Registries = append(j.Registries, jsonRegistry(h))
	}
	if err != nil {
		j.Error = runError(err)
	}
	return j
}
