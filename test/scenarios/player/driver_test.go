// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agaetis-it/takecare-agents/internal/controller/demo"
	"github.com/agaetis-it/takecare-agents/internal/demo/corpus"
)

// demoScenarios are the scenario files the TakeCareDemo controller
// injects: one per value of its enum (spec 14 §3.1).
func demoScenarios(t *testing.T) []*Scenario {
	t.Helper()
	out := make([]*Scenario, 0, len(corpus.Scenarios))
	for _, id := range corpus.Scenarios {
		s, err := Load(filepath.Join("..", id+".yaml"))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if s.Fault.Injection.Driver != driverDemo {
			t.Errorf("%s: driver %s, want %s", id, s.Fault.Injection.Driver, driverDemo)
		}
		out = append(out, s)
	}
	return out
}

// A scenario's injection steps describe what the driver does; the driver's
// code is the truth (spec 14 §3.4). Every object the controller changes
// for the incident is named in a step, by kind and name, and every step
// names something the controller changes: a step that the driver no longer
// does, or a change no step tells, fails here.
func TestInjectionStepsAgreeWithTheDriver(t *testing.T) {
	for _, s := range demoScenarios(t) {
		touched, err := demo.Injection(s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(touched) < 2 {
			t.Fatalf("%s: the driver changes %v: nothing beyond the corpus", s.ID, touched)
		}
		named := make([]bool, len(s.Fault.Injection.Steps))
		for _, obj := range touched {
			kind, ref, _ := strings.Cut(obj, " ")
			ns, name, _ := strings.Cut(ref, "/")
			found := false
			for i, step := range s.Fault.Injection.Steps {
				var says bool
				if obj == "corpus" {
					says = strings.Contains(step, "corpus")
				} else {
					says = strings.Contains(step, kind+" "+name) && strings.Contains(step, ns)
				}
				if says {
					found, named[i] = true, true
				}
			}
			if !found {
				t.Errorf("%s: the driver changes %s, which no injection step names (as %q, with its namespace)", s.ID, obj, kind+" "+name)
			}
		}
		for i, ok := range named {
			if !ok {
				t.Errorf("%s: step %q names nothing the driver changes (it changes %s)", s.ID, s.Fault.Injection.Steps[i], strings.Join(touched, ", "))
			}
		}
	}
}

// The scenario's window allows at least the one the controller announces
// in expectedAlertWindow, from the same start (the fault in place), with
// the demo's ramp (demo/manifests/demo.yaml, 4 minutes).
func TestSignalWindowCoversTheDriver(t *testing.T) {
	for _, s := range demoScenarios(t) {
		from, to, err := demo.AlertWindow(s.ID, 4*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if s.window[0] > from || s.window[1] < to {
			t.Errorf("%s: window %s to %s does not cover the controller's %s to %s", s.ID, s.Fault.Signals.Window.From, s.Fault.Signals.Window.To, from, to)
		}
	}
	if _, err := demo.Injection("disk-full"); err == nil {
		t.Error("a scenario the driver does not inject has an injection")
	}
	if !slices.Equal(corpus.Scenarios, []string{"proxy-timeout", "bad-config-rollout", "missing-secret-key"}) {
		t.Errorf("the demo's scenarios are %v: update the enum's files and this test together", corpus.Scenarios)
	}
}
