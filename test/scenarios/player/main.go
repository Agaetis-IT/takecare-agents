// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Command player reads the scenario files of spec 14 for the demo and the
// benches: it checks them, says what a scenario needs, decides whether a
// pass can be played, and scores a pass. It prints ids, states and fixed
// phrases, never a diagnosis. A test tool, run from the repository's root,
// never released.
//
// Usage:
//
//	go run ./test/scenarios/player check [-reserved] [-map-dir DIR] [DIR]
//	go run ./test/scenarios/player info -scenario FILE
//	go run ./test/scenarios/player signal -scenario FILE -injected TIME -active FILE [-now TIME] [-grace 1m]
//	go run ./test/scenarios/player score -scenario FILE -investigation FILE [-tools FILE] [-results FILE] [-format json|text]
//
// The investigation is a TakeCareInvestigation as kubectl get -o json
// prints it; the tools file a JSON list of {name, arguments, class}; the
// results file, with content capture only, a JSON list of strings; the
// active file the alerts firing, as Alertmanager's /api/v2/alerts lists them.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "player:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("a command: check, info, signal or score")
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	switch cmd {
	case "check":
		mapDir := fs.String("map-dir", "", "the demo Map's directory (default "+demoMap+")")
		reserved := fs.Bool("reserved", false, "a reserved set, played from SCENARIOS_DIR: reserved: true allowed")
		if err := fs.Parse(args); err != nil {
			return err
		}
		dir := fs.Arg(0)
		if dir == "" {
			dir = "test/scenarios"
		}
		n, err := CheckDir(dir, *mapDir, *reserved)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "%d scenario(s) checked in %s\n", n, dir)
		return err
	case "info":
		file := fs.String("scenario", "", "the scenario file")
		if err := fs.Parse(args); err != nil {
			return err
		}
		s, err := Load(*file)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(info(s))
	case "signal":
		file := fs.String("scenario", "", "the scenario file")
		injected := fs.String("injected", "", "when the fault was injected (RFC 3339)")
		now := fs.String("now", "", "now (RFC 3339; default the clock)")
		active := fs.String("active", "", "the alerts firing on the ground, Alertmanager's API v2 JSON list")
		grace := fs.Duration("grace", time.Minute, "the alert's way from the rule to Alertmanager")
		if err := fs.Parse(args); err != nil {
			return err
		}
		s, err := Load(*file)
		if err != nil {
			return err
		}
		at, err := time.Parse(time.RFC3339, *injected)
		if err != nil {
			return fmt.Errorf("-injected: %w", err)
		}
		t := time.Now()
		if *now != "" {
			if t, err = time.Parse(time.RFC3339, *now); err != nil {
				return fmt.Errorf("-now: %w", err)
			}
		}
		var firing []Firing
		if err := readJSON(*active, &firing); err != nil {
			return err
		}
		st, err := Signal(s, at, t, firing, *grace)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(st)
	case "score":
		file := fs.String("scenario", "", "the scenario file")
		inv := fs.String("investigation", "", "the TakeCareInvestigation, as JSON")
		tools := fs.String("tools", "", "the trace's tool calls, a JSON list")
		results := fs.String("results", "", "the tool results, a JSON list of strings (content capture only)")
		mapDir := fs.String("map-dir", "", "the demo Map's directory (default "+demoMap+")")
		format := fs.String("format", "json", "json (one line) or text (the verdict lines)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *format != "json" && *format != "text" {
			return fmt.Errorf("-format %s: json or text", *format)
		}
		r, err := score(*file, *inv, *tools, *results, *mapDir)
		if err != nil {
			return err
		}
		if *format == "text" {
			_, err = io.WriteString(out, Text(r))
			return err
		}
		return json.NewEncoder(out).Encode(r)
	}
	return fmt.Errorf("unknown command %s: check, info, signal or score", cmd)
}

// Info is what a player needs to know before it plays a scenario.
type Info struct {
	Ref             string   `json:"ref"`
	ID              string   `json:"id"`
	Version         int      `json:"version"`
	Driver          string   `json:"driver"`
	Trigger         string   `json:"trigger"`
	Alerts          []string `json:"alerts"`
	Connectors      []string `json:"connectors"`
	Passes          int      `json:"passes"`
	Reserved        bool     `json:"reserved"`
	NeedsTrace      bool     `json:"needsTrace"`
	RequiresContent bool     `json:"requiresContent"`
}

func info(s *Scenario) Info {
	trace, content := s.Needs()
	return Info{Ref: s.Ref(), ID: s.ID, Version: s.Version, Driver: s.Fault.Injection.Driver, Trigger: s.Fault.Signals.Trigger, Alerts: s.Fault.Signals.Alerts,
		Connectors: s.Ground.Connectors, Passes: s.Passes, Reserved: s.Reserved, NeedsTrace: trace, RequiresContent: content}
}

func score(file, invFile, toolsFile, resultsFile, mapDir string) (*Result, error) {
	s, err := Load(file)
	if err != nil {
		return nil, err
	}
	w, err := LoadWorld(s, mapDir)
	if err != nil {
		return nil, err
	}
	// A scenario is checked before it is played; reserved ones come from
	// a directory of their own, so they are allowed here.
	if errs := Check(s, file, w, true); len(errs) > 0 {
		return nil, fmt.Errorf("%s: %w", file, errors.Join(errs...))
	}
	var inv v1alpha1.TakeCareInvestigation
	if err := readJSON(invFile, &inv); err != nil {
		return nil, err
	}
	p := Pass{Diagnosis: inv.Status.Diagnosis}
	if toolsFile != "" {
		if err := readJSON(toolsFile, &p.Tools); err != nil {
			return nil, err
		}
		p.ToolsRead = true
	}
	if resultsFile != "" {
		if err := readJSON(resultsFile, &p.Results); err != nil {
			return nil, err
		}
		p.Content = true
	}
	return Score(s, w, p)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path) //nolint:gosec // a path the bench script gives
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// Text prints a result as the verdict lines of make demo-verdict.
func Text(r *Result) string {
	var b strings.Builder
	verdict := "pass"
	if !r.Pass {
		verdict = "fail"
	}
	fmt.Fprintf(&b, "verdict %s: %s\n", r.Scenario, verdict)
	for _, c := range r.Causes {
		fmt.Fprintf(&b, "  cause %s: %s\n", c.Entity, yesNo(c.Named, "named", "not named"))
	}
	items := make([]string, 0, len(r.Evidence))
	for _, e := range r.Evidence {
		items = append(items, e.ID+" "+yesNo(e.Found, "found", "not found"))
	}
	fmt.Fprintf(&b, "  evidence: %s\n", orNone(items))
	items, pattern := make([]string, 0, len(r.RedHerrings)), false
	for _, h := range r.RedHerrings {
		items = append(items, h.ID+" "+h.State)
		pattern = pattern || h.PatternBased
	}
	note := ""
	if pattern {
		note = " (mentioned or concluded: pattern-based)"
	}
	fmt.Fprintf(&b, "  red herrings%s: %s\n", note, orNone(items))
	items = items[:0]
	for _, s := range r.BlindSpots {
		state := yesNo(s.Declared, "declared", "not declared")
		if s.MustDeclare && !s.Declared {
			state = "not declared, though it must be"
		}
		items = append(items, s.Key+" "+state)
	}
	fmt.Fprintf(&b, "  blind spots: %s\n", orNone(items))
	fmt.Fprintf(&b, "  dangerous actions: %s\n", orNone(r.Dangerous))
	fmt.Fprintf(&b, "  actions above the scenario's class: %s\n", orNone(r.ActionsAboveClass))
	return b.String()
}

func yesNo(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

func orNone(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}
