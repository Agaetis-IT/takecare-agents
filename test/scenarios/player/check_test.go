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

// mutated writes proxy-timeout.yaml, changed by fn, under name in a
// temporary directory, and returns its path.
func mutated(t *testing.T, name string, fn func(map[string]any)) string {
	t.Helper()
	raw, err := os.ReadFile(proxyFile)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	fn(doc)
	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func get(doc map[string]any, path ...string) map[string]any {
	for _, k := range path {
		doc = doc[k].(map[string]any)
	}
	return doc
}

func firstCause(doc map[string]any) map[string]any { return doc["causes"].([]any)[0].(map[string]any) }

func TestCheckRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		file string
		fn   func(map[string]any)
		want string
	}{
		"a missing field":        {fn: func(d map[string]any) { delete(d, "blindSpots") }, want: "blindSpots"},
		"a missing nested field": {fn: func(d map[string]any) { delete(firstCause(d), "with") }, want: "/properties/causes"},
		"an unknown field":       {fn: func(d map[string]any) { d["expect"] = map[string]any{"doc": true} }, want: "expect"},
		"an omitted cutoff":      {fn: func(d map[string]any) { delete(d, "cutoff") }, want: "cutoff"},
		"an unknown Map key": {
			fn:   func(d map[string]any) { firstCause(d)["entity"] = "network/orders-db-proxy" },
			want: "network/orders-db-proxy is not a key of the demo Map",
		},
		"an unknown Map key in a matcher": {
			fn: func(d map[string]any) {
				get(d, "evidence")["expected"] = []any{map[string]any{"id": "x", "match": map[string]any{"entity": "compute/nowhere"}}}
			},
			want: "compute/nowhere is not a key",
		},
		"a regular expression that does not compile": {
			fn:   func(d map[string]any) { get(firstCause(d), "match", "text")["all"] = []any{"proxy(", "idle"} },
			want: `regular expression "proxy("`,
		},
		"a bad expression deep in a combination": {
			fn: func(d map[string]any) {
				firstCause(d)["match"] = map[string]any{"anyOf": []any{
					map[string]any{"record": "runbooks/RB-014"},
					map[string]any{"allOf": []any{map[string]any{"text": map[string]any{"none": []any{"[a-"}}}}},
				}}
			},
			want: `regular expression "[a-"`,
		},
		"reserved: true": {fn: func(d map[string]any) { d["reserved"] = true }, want: "reserved: true"},
		"a record the corpus does not hold": {
			fn: func(d map[string]any) {
				get(d, "evidence")["expected"] = []any{map[string]any{"id": "x", "match": map[string]any{"record": "runbooks/RB-999"}}}
			},
			want: "record runbooks/RB-999 is not in the demo corpus's runbooks",
		},
		"a record of an unknown source": {
			fn: func(d map[string]any) {
				get(d, "evidence")["expected"] = []any{map[string]any{"id": "x", "match": map[string]any{"record": "wiki/RB-014"}}}
			},
			want: "wiki is not a source",
		},
		"two matcher kinds in one": {
			fn: func(d map[string]any) {
				firstCause(d)["match"] = map[string]any{"record": "runbooks/RB-014", "entity": "compute/billing-api"}
			},
			want: "causes",
		},
		"causes none on a simple scenario": {fn: func(d map[string]any) { d["causes"] = "none" }, want: "none is for a false-alarm only"},
		"a window ending before it starts": {
			fn: func(d map[string]any) {
				get(d, "fault", "signals")["window"] = map[string]any{"from": "5m", "to": "1m"}
			},
			want: "from 5m is after to 1m",
		},
		"an alert trigger without alerts": {fn: func(d map[string]any) { get(d, "fault", "signals")["alerts"] = []any{} }, want: "names no alert"},
		"a live scenario with a cutoff": {
			fn:   func(d map[string]any) { d["cutoff"] = map[string]any{"at": "alert", "excluded": []any{}} },
			want: "a live scenario has cutoff: none",
		},
		"a duplicated id": {
			fn: func(d map[string]any) {
				h := get(d, "evidence")["redHerrings"].([]any)
				get(d, "evidence")["redHerrings"] = append(h, h[0])
			},
			want: "id memory twice",
		},
		"version 0":            {fn: func(d map[string]any) { d["version"] = 0 }, want: "version"},
		"an unknown connector": {fn: func(d map[string]any) { get(d, "ground")["connectors"] = []any{"wiki"} }, want: "connectors"},
		"a file named after another id": {
			file: "memory-leak.yaml", fn: func(map[string]any) {}, want: "must be named proxy-timeout.yaml",
		},
	} {
		t.Run(name, func(t *testing.T) {
			file := tc.file
			if file == "" {
				file = "proxy-timeout.yaml"
			}
			_, err := CheckDir(filepath.Dir(mutated(t, file, tc.fn)), mapDir, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error with %q", err, tc.want)
			}
		})
	}
}

// A reserved set, played from its own directory, may hold reserved: true.
func TestReservedAllowedFromItsDirectory(t *testing.T) {
	p := mutated(t, "proxy-timeout.yaml", func(d map[string]any) { d["reserved"] = true })
	if _, err := CheckDir(filepath.Dir(p), mapDir, true); err != nil {
		t.Fatal(err)
	}
}

// The other variants' rules, on top of the schema.
func TestCheckVariants(t *testing.T) {
	for name, tc := range map[string]struct {
		fn   func(map[string]any)
		want string // "" when the file is fine
	}{
		"a false alarm, done right": {fn: func(d map[string]any) {
			d["variant"], d["causes"] = "false-alarm", "none"
			get(d, "safeAction")["class"] = "none"
		}},
		"a false alarm allowing an action": {fn: func(d map[string]any) {
			d["variant"], d["causes"] = "false-alarm", "none"
		}, want: "a false-alarm allows none"},
		"a false alarm without its cause none": {fn: func(d map[string]any) {
			d["variant"] = "false-alarm"
			get(d, "safeAction")["class"] = "none"
		}, want: "a false-alarm has causes: none"},
		"two faults, one cause":              {fn: func(d map[string]any) { d["variant"] = "two-faults" }, want: "two causes, not 1"},
		"a mute source without a blind spot": {fn: func(d map[string]any) { d["variant"] = "mute-source" }, want: "at least one"},
		"a mute source with its blind spot": {fn: func(d map[string]any) {
			d["variant"] = "mute-source"
			d["blindSpots"] = []any{map[string]any{"key": "datastore/billing-db", "reason": "NoAccess", "mustDeclare": true}}
		}},
		"a blind spot off the Map": {fn: func(d map[string]any) {
			d["blindSpots"] = []any{map[string]any{"key": "datastore/orders-db", "reason": "NotInMap", "mustDeclare": true}}
		}, want: "datastore/orders-db is not a key"},
		"a replay without a cutoff": {fn: func(d map[string]any) { get(d, "fault", "injection")["mode"] = "replay" }, want: "a replay names its cutoff"},
		"a replay excluding a source with no blind spot": {fn: func(d map[string]any) {
			get(d, "fault", "injection")["mode"] = "replay"
			d["cutoff"] = map[string]any{"at": "2026-11-01T10:00:00Z", "excluded": []any{"knowledge"}}
		}, want: "reason Excluded"},
		"an on-demand scenario with alerts": {fn: func(d map[string]any) {
			get(d, "fault", "signals")["trigger"] = "onDemand"
		}, want: "onDemand takes alerts: []"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := CheckDir(filepath.Dir(mutated(t, "proxy-timeout.yaml", tc.fn)), mapDir, false)
			switch {
			case tc.want == "" && err != nil:
				t.Fatal(err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("got %v, want an error with %q", err, tc.want)
			}
		})
	}
}
