// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const (
	setDir    = ".."
	mapDir    = "../../../charts/takecare/files/demo-map"
	proxyFile = "../proxy-timeout.yaml"
	proxyName = "proxy-timeout.yaml"

	reservedTrue patch = "reserved: true"
)

// The repository's set passes the checker make lint runs.
func TestRepositorySet(t *testing.T) {
	n, err := CheckDir(setDir, mapDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no scenario checked")
	}
}

// A checker that read nothing did not check.
func TestEmptyDirFails(t *testing.T) {
	if _, err := CheckDir(t.TempDir(), mapDir, false); err == nil || !strings.Contains(err.Error(), "no scenario") {
		t.Fatalf("got %v, want no scenario", err)
	}
}

// patch is a YAML merge patch on a scenario document: maps merge, any
// other value replaces, null deletes.
type patch string

func (p patch) apply(d map[string]any) {
	var v map[string]any
	if err := yaml.Unmarshal([]byte(p), &v); err != nil {
		panic(err)
	}
	merge(d, v)
}

func merge(d, p map[string]any) {
	for k, v := range p {
		sub, isMap := v.(map[string]any)
		cur, curMap := d[k].(map[string]any)
		switch {
		case v == nil:
			delete(d, k)
		case isMap && curMap:
			merge(cur, sub)
		default:
			d[k] = v
		}
	}
}

// expect makes the matcher, in YAML, the scenario's only expected finding.
func expect(matcher string) patch {
	return patch("evidence: {expected: [{id: x, match: " + matcher + "}]}")
}

// mutated writes proxy-timeout.yaml, patched, under name in a temporary
// directory, and returns its path.
func mutated(t *testing.T, name string, p patch) string {
	t.Helper()
	raw, err := os.ReadFile(proxyFile)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	p.apply(doc)
	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Spec 14 §6, criterion 1: a missing field, an unknown Map key, a regular
// expression that does not compile and reserved: true are refused; so is
// what else the format forbids.
func TestCheckRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		file  string
		patch patch
		want  string
	}{
		"a missing field":        {patch: "blindSpots: null", want: "blindSpots"},
		"a missing nested field": {patch: "causes: [{entity: network/billing-db-proxy, statement: s, match: {text: {all: [proxy]}}}]", want: "/properties/causes"},
		"an unknown field":       {patch: "expect: {doc: true}", want: "expect"},
		"an omitted cutoff":      {patch: "cutoff: null", want: "cutoff"},
		"an unknown Map key": {
			patch: "causes: [{entity: network/orders-db-proxy, with: [], statement: s, match: {text: {all: [proxy]}}}]",
			want:  "network/orders-db-proxy is not a key of the demo Map",
		},
		"an unknown key in a match": {patch: expect("{entity: compute/nowhere}"), want: "compute/nowhere is not a key"},
		"a bad regular expression": {
			patch: "causes: [{entity: network/billing-db-proxy, with: [], statement: s, match: {text: {all: ['proxy(', idle]}}}]",
			want:  `regular expression "proxy("`,
		},
		"a bad expression, deep": {
			patch: expect("{anyOf: [{record: runbooks/RB-014}, {allOf: [{text: {none: ['[a-']}}]}]}"),
			want:  `regular expression "[a-"`,
		},
		"reserved true":                     {patch: reservedTrue, want: "a reserved scenario is not kept"},
		"a record the corpus does not hold": {patch: expect("{record: runbooks/RB-999}"), want: "record runbooks/RB-999 is not in the demo corpus's runbooks"},
		"a record of an unknown source":     {patch: expect("{record: wiki/RB-014}"), want: "wiki is not a source"},
		"two matcher kinds in one":          {patch: expect("{record: runbooks/RB-014, entity: compute/billing-api}"), want: "/properties/evidence"},
		"causes none on a simple scenario":  {patch: "causes: none", want: "none is for a false-alarm only"},
		"a window ending before it starts":  {patch: "fault: {signals: {window: {from: 5m, to: 1m}}}", want: "from 5m is after to 1m"},
		"a duration that is not one":        {patch: "fault: {signals: {window: {from: soon}}}", want: "window"},
		"an alert trigger without alerts":   {patch: "fault: {signals: {alerts: []}}", want: "names no alert"},
		"a live scenario with a cutoff":     {patch: "cutoff: {at: alert, excluded: []}", want: "a live scenario has cutoff: none"},
		"a duplicated id": {
			patch: "evidence: {redHerrings: [{id: lead, lure: a, match: {text: {any: [a]}}}, {id: lead, lure: b, match: {text: {any: [b]}}}]}",
			want:  "id lead twice",
		},
		"version 0":                     {patch: "version: 0", want: "version"},
		"an unknown connector":          {patch: "ground: {connectors: [wiki]}", want: "connectors"},
		"a file named after another id": {file: "memory-leak.yaml", patch: "{}", want: "must be named proxy-timeout.yaml"},
	} {
		t.Run(name, func(t *testing.T) {
			file := tc.file
			if file == "" {
				file = proxyName
			}
			_, err := CheckDir(filepath.Dir(mutated(t, file, tc.patch)), mapDir, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error with %q", err, tc.want)
			}
		})
	}
}

// A reserved set, played from its own directory, may hold reserved: true.
func TestReservedAllowedFromItsDirectory(t *testing.T) {
	p := mutated(t, proxyName, reservedTrue)
	if _, err := CheckDir(filepath.Dir(p), mapDir, true); err != nil {
		t.Fatal(err)
	}
}

// The other variants' rules, on top of the schema.
func TestCheckVariants(t *testing.T) {
	for name, tc := range map[string]struct {
		patch patch
		want  string // "" when the file is fine
	}{
		"a false alarm, done right":            {patch: "{variant: false-alarm, causes: none, safeAction: {class: none}}"},
		"a false alarm allowing an action":     {patch: "{variant: false-alarm, causes: none}", want: "a false-alarm allows none"},
		"a false alarm without its cause none": {patch: "{variant: false-alarm, safeAction: {class: none}}", want: "a false-alarm has causes: none"},
		"a false alarm showing nothing": {
			patch: "{variant: false-alarm, causes: none, safeAction: {class: none}, evidence: {expected: []}}",
			want:  "its expected evidence, none is given",
		},
		"two faults, one cause":              {patch: "variant: two-faults", want: "two causes, not 1"},
		"a mute source without a blind spot": {patch: "variant: mute-source", want: "at least one"},
		"a mute source with its blind spot": {
			patch: "{variant: mute-source, blindSpots: [{key: datastore/billing-db, reason: NoAccess, mustDeclare: true}]}",
		},
		"a blind spot off the Map": {
			patch: "blindSpots: [{key: datastore/orders-db, reason: NotInMap, mustDeclare: true}]",
			want:  "datastore/orders-db is not a key",
		},
		"a replay without a cutoff": {patch: "fault: {injection: {mode: replay}}", want: "a replay names its cutoff"},
		"a replay excluding a source with no blind spot": {
			patch: "{fault: {injection: {mode: replay}}, cutoff: {at: '2026-11-01T10:00:00Z', excluded: [knowledge]}}",
			want:  "reason Excluded",
		},
		"a replay cut at an odd time": {
			patch: "{fault: {injection: {mode: replay}}, cutoff: {at: '2026-11-01T10:00:00+99:00', excluded: []}}",
			want:  "not an RFC 3339 time",
		},
		"an on-demand scenario with alerts": {patch: "fault: {signals: {trigger: onDemand}}", want: "onDemand takes alerts: []"},
		"an on-demand scenario with a window": {
			patch: "fault: {signals: {trigger: onDemand, alerts: []}}",
			want:  "from and to are the wait before the record",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := CheckDir(filepath.Dir(mutated(t, proxyName, tc.patch)), mapDir, false)
			switch {
			case tc.want == "" && err != nil:
				t.Fatal(err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("got %v, want an error with %q", err, tc.want)
			}
		})
	}
}
