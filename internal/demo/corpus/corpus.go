// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Package corpus composes the content served by the demo's five corpus MCP
// servers (spec 03): incident history, deployment log, runbooks, vendor
// status and support tickets. Demo only.
//
// Ported from spike 001's generate-corpus.py (itself from the challenge POC).
// Each source is CURATED records (seed/, hand-written, they carry the
// scenario's signals and their competitors) plus generated noise around them.
// Only the proxy-timeout scenario is carried: the other scenarios' live
// changes were left out of the seeds, their unscoped records stay as noise.
//
// Dates are relative to the incident time. Seeds write them as "@TS<minutes>@"
// and "@DAY<days>@", resolved here into explicit UTC ISO 8601: the agent
// compares them with what it reads in the cluster (pod logs, object ages,
// alert time), all in UTC. Static historical dates (2024-03-12) stay as
// written.
//
// A seed record carrying a `scenario` field is a live change of that scenario
// (the change that breaks the cluster now): it is kept only when that
// scenario is composed, so that the deployment log never claims a change the
// cluster does not show. The field is stripped.
package corpus

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// Scenarios.
const (
	// ScenarioNone composes the corpus without any live change.
	ScenarioNone = ""
	// ScenarioProxyTimeout is the demo's scenario (spec 03).
	ScenarioProxyTimeout = "proxy-timeout"
)

// Sources, one corpus MCP server each.
const (
	SourceDeployments = "deployments"
	SourceIncidents   = "incidents"
	SourceRunbooks    = "runbooks"
	SourceSupport     = "support"
	SourceVendor      = "vendor"
)

// Sources are every source, in a stable order.
var Sources = []string{SourceDeployments, SourceIncidents, SourceRunbooks, SourceSupport, SourceVendor}

// Record keys read by the code.
const (
	keyDate    = "date"
	keyService = "service"
	keyStatus  = "status"
	keySummary = "summary"
	keyTags    = "tags"
	keyTitle   = "title"
)

// isoUTC is the layout of every generated date (2026-10-17T18:30:00Z).
const isoUTC = "2006-01-02T15:04:05Z"

// Record is one entry of a source; every field is text.
type Record map[string]string

// Corpus is the records of each source.
type Corpus map[string][]Record

//go:embed seed/*.json
var seeds embed.FS

var marker = regexp.MustCompile(`^@(TS|DAY)([0-9]+)@$`)

// Seeds loads the curated records of every source for a scenario, dates
// resolved relative to the incident time.
func Seeds(scenario string, incident time.Time) (Corpus, error) {
	if scenario != ScenarioNone && scenario != ScenarioProxyTimeout {
		return nil, fmt.Errorf("scenario %q is not available", scenario)
	}
	c := Corpus{}
	for _, source := range Sources {
		raw, err := seeds.ReadFile("seed/" + source + ".json")
		if err != nil {
			return nil, fmt.Errorf("read seed %s: %w", source, err)
		}
		var records []Record
		if err := json.Unmarshal(raw, &records); err != nil {
			return nil, fmt.Errorf("parse seed %s: %w", source, err)
		}
		kept := records[:0]
		for _, r := range records {
			owner, live := r["scenario"]
			if live && owner != scenario {
				continue
			}
			delete(r, "scenario")
			for k, v := range r {
				r[k] = resolve(v, incident)
			}
			kept = append(kept, r)
		}
		c[source] = kept
	}
	return c, nil
}

// resolve turns a relative date marker into a UTC date; other values are
// returned unchanged.
func resolve(v string, incident time.Time) string {
	m := marker.FindStringSubmatch(v)
	if m == nil {
		return v
	}
	n, _ := strconv.Atoi(m[2]) // digits only, by the pattern
	unit := time.Minute
	if m[1] == "DAY" {
		unit = 24 * time.Hour
	}
	return incident.Add(-time.Duration(n) * unit).UTC().Format(isoUTC)
}

// parseDate reads a record's date, generated or static.
func parseDate(s string) (time.Time, error) {
	for _, layout := range []string{isoUTC, time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unreadable date %q", s)
}
