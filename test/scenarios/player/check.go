// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/agaetis-it/takecare-agents/internal/demo/corpus"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
)

// demoMap is the directory of the demo's Map (spec 08), from the
// repository's root.
const demoMap = "charts/takecare/files/demo-map"

// World is what a scenario's keys and records are checked against: the
// entries of its Map, and, on the demo, the corpus's records.
type World struct {
	Entries map[string]*mapserver.Entry
	// Records holds the ids of each source's records; nil when the
	// ground's records are not known to the player.
	Records map[string]map[string]bool
	// Served matches, by <connector>/<id>, the id a record is served
	// under when the ground serves it under another one: the demo numbers
	// its deployment log and its tickets by rank.
	Served map[string]*regexp.Regexp
}

// LoadWorld reads the ground a scenario names. Only the demo's Map is
// known today (spec 14 §3.2): mapDir overrides its directory.
func LoadWorld(s *Scenario, mapDir string) (*World, error) {
	if s.Ground.Map != "demo" {
		return nil, fmt.Errorf("ground.map %s: the player knows the demo's Map only", s.Ground.Map)
	}
	if mapDir == "" {
		mapDir = demoMap
	}
	m, err := mapserver.Load(os.DirFS(mapDir))
	if err != nil {
		return nil, fmt.Errorf("the %s Map (%s): %w", s.Ground.Map, mapDir, err)
	}
	w := &World{Entries: m.Entries}
	if s.Fault.Injection.Driver == driverDemo {
		// The records a live demo scenario can cite: the corpus's seeds
		// for that scenario (the generated noise has no stable id).
		c, err := corpus.Seeds(s.ID, time.Now())
		if err != nil {
			return nil, fmt.Errorf("the demo corpus: %w", err)
		}
		w.Records = map[string]map[string]bool{}
		for source, records := range c {
			w.Records[source] = map[string]bool{}
			for _, r := range records {
				w.Records[source][r["id"]] = true
			}
		}
		served, err := corpus.Served(s.ID)
		if err != nil {
			return nil, fmt.Errorf("the demo corpus: %w", err)
		}
		w.Served = map[string]*regexp.Regexp{}
		for source, ids := range served {
			for seed, id := range ids {
				w.Served[source+"/"+seed] = recordRE(id)
			}
		}
	}
	return w, nil
}

// Values of the format the rules below read.
const (
	classNone        = "none"
	variantFalse     = "false-alarm"
	variantTwoFaults = "two-faults"
	triggerAlert     = "alert"
	triggerOnDemand  = "onDemand"
	driverDemo       = "takecare-demo"
)

// classes are the action classes, from least to most harmful (spec 14
// §3.8).
var classes = []string{classNone, "reversible", "disruptive", "destructive"}

// rules are the format's rules the schema cannot say, each adding its
// faults.
var rules = []func(s *Scenario, fail func(string, ...any)){checkSignals, checkCauses, checkCutoff, checkIDs}

// Check applies the format's rules the schema cannot say, and checks the
// scenario's keys and records against its world. file is the scenario's
// path: its name must be the id. Every fault is returned at once.
func Check(s *Scenario, file string, w *World, allowReserved bool) []error {
	var errs []error
	fail := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if want := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)); s.ID != want {
		fail("id %s: the file must be named %s.yaml", s.ID, s.ID)
	}
	if s.Reserved && !allowReserved {
		fail("reserved: true: a reserved scenario is not kept in this repository (spec 14 §3.11)")
	}
	if s.Ground.Type != "kubernetes" {
		fail("ground.type %s: only kubernetes is playable today", s.Ground.Type)
	}
	for _, rule := range rules {
		rule(s, fail)
	}
	if w != nil {
		checkWorld(s, w, fail)
	}
	return errs
}

// checkSignals: the alerts go with their trigger, the window is in order
// (spec 14 §3.4).
func checkSignals(s *Scenario, fail func(string, ...any)) {
	sig := s.Fault.Signals
	switch {
	case sig.Trigger == triggerAlert && len(sig.Alerts) == 0:
		fail("fault.signals: trigger alert names no alert")
	case sig.Trigger == triggerOnDemand && len(sig.Alerts) > 0:
		fail("fault.signals: trigger onDemand takes alerts: []")
	}
	if s.window[0] > s.window[1] {
		fail("fault.signals.window: from %s is after to %s", sig.Window.From, sig.Window.To)
	}
	if sig.Trigger == triggerOnDemand && s.window[0] != s.window[1] {
		fail("fault.signals.window: without an alert, from and to are the wait before the record, equal")
	}
}

// checkCauses: the causes, the class and the blind spots per variant
// (spec 14 §3.3, §3.5, §3.7).
func checkCauses(s *Scenario, fail func(string, ...any)) {
	n := len(s.Causes.Items)
	switch {
	case s.Variant == variantFalse && !s.Causes.None:
		fail("causes: a false-alarm has causes: none")
	case s.Variant != variantFalse && s.Causes.None:
		fail("causes: none is for a false-alarm only")
	case s.Variant == variantTwoFaults && n != 2:
		fail("causes: a two-faults scenario has two causes, not %d", n)
	case !s.Causes.None && s.Variant != variantTwoFaults && n != 1:
		fail("causes: a %s scenario has one cause, not %d", s.Variant, n)
	}
	if s.Variant == variantFalse {
		if s.SafeAction.Class != classNone {
			fail("safeAction.class: a false-alarm allows none, not %s", s.SafeAction.Class)
		}
		if len(s.Evidence.Expected) == 0 {
			fail("evidence.expected: a false-alarm's correctness is its expected evidence, none is given")
		}
	}
	if s.Variant == "mute-source" && len(s.BlindSpots) == 0 {
		fail("blindSpots: a mute-source scenario has at least one")
	}
}

// checkCutoff: none for a live scenario, a time for a replay, and the
// excluded sources among the blind spots (spec 14 §3.10).
func checkCutoff(s *Scenario, fail func(string, ...any)) {
	switch mode := s.Fault.Injection.Mode; {
	case mode == "live" && !s.Cutoff.None:
		fail("cutoff: a live scenario has cutoff: none")
	case mode == "replay" && s.Cutoff.None:
		fail("cutoff: a replay names its cutoff")
	}
	if !s.Cutoff.None && s.Cutoff.At != triggerAlert {
		if _, err := time.Parse(time.RFC3339, s.Cutoff.At); err != nil {
			fail("cutoff.at %s: not an RFC 3339 time", s.Cutoff.At)
		}
	}
	if len(s.Cutoff.Excluded) > 0 && !slices.ContainsFunc(s.BlindSpots, func(b BlindSpot) bool { return b.Reason == "Excluded" }) {
		fail("cutoff.excluded: each excluded source appears in blindSpots with the reason Excluded")
	}
}

// checkIDs: ids unique within their list, so that reports compare by id.
func checkIDs(s *Scenario, fail func(string, ...any)) {
	uniq := func(list string, ids []string) {
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				fail("%s: id %s twice", list, id)
			}
			seen[id] = true
		}
	}
	uniq("evidence.expected", ids(s.Evidence.Expected, func(e Expected) string { return e.ID }))
	uniq("evidence.redHerrings", ids(s.Evidence.RedHerrings, func(e RedHerring) string { return e.ID }))
	uniq("safeAction.dangerous", ids(s.SafeAction.Dangerous, func(e Dangerous) string { return e.ID }))
}

// checkWorld: the Map keys and the records exist in the scenario's world.
func checkWorld(s *Scenario, w *World, fail func(string, ...any)) {
	key := func(where, k string) {
		if _, ok := w.Entries[k]; !ok {
			fail("%s: %s is not a key of the %s Map", where, k, s.Ground.Map)
		}
	}
	for i, c := range s.Causes.Items {
		key(fmt.Sprintf("causes[%d].entity", i), c.Entity)
		for _, k := range c.With {
			key(fmt.Sprintf("causes[%d].with", i), k)
		}
	}
	for _, b := range s.BlindSpots {
		key("blindSpots", b.Key)
	}
	s.eachMatcher(func(where string, m *Matcher) {
		m.walk(func(m *Matcher) {
			if m.Entity != "" {
				key(where, m.Entity)
			}
			if m.Record != "" && w.Records != nil {
				source, id := recordSource(m.Record), recordID(m.Record)
				switch records, ok := w.Records[source]; {
				case !ok:
					fail("%s: record %s: %s is not a source of the demo corpus", where, m.Record, source)
				case !records[id]:
					fail("%s: record %s is not in the demo corpus's %s", where, m.Record, source)
				}
			}
		})
	})
}

func ids[T any](items []T, id func(T) string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, id(it))
	}
	return out
}

// CheckDir loads and checks every <id>.yaml of dir (not its
// subdirectories). An empty set is a failure: a checker that read nothing
// did not check.
func CheckDir(dir, mapDir string, allowReserved bool) (int, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return 0, fmt.Errorf("list %s: %w", dir, err)
	}
	if len(files) == 0 {
		return 0, fmt.Errorf("no scenario in %s", dir)
	}
	var errs []error
	for _, f := range files {
		s, err := Load(f)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f, err))
			continue
		}
		w, err := LoadWorld(s, mapDir)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f, err))
		}
		for _, e := range Check(s, f, w, allowReserved) {
			errs = append(errs, fmt.Errorf("%s: %w", f, e))
		}
	}
	return len(files), errors.Join(errs...)
}
