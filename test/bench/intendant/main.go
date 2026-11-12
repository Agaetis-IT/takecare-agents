// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Command intendant scores one survey of the Intendant bench (spec 10 §11
// criterion 9) against the expected set and prints the run's score as one
// JSON line: keys, counts and fixed phrases, never an entry or evidence.
// hack/bench-intendant.sh runs it once per survey.
//
// Usage: go run ./test/bench/intendant -expected FILE -survey FILE
// -proposals FILE -map FILE, the last three as kubectl get -o json prints
// the survey, its proposals' ConfigMap and the Map's ConfigMap.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
)

func main() {
	expected := flag.String("expected", "test/bench/intendant/expected.yaml", "the expected set")
	survey := flag.String("survey", "", "the TakeCareSurvey, as JSON")
	proposals := flag.String("proposals", "", "the survey's proposals ConfigMap, as JSON")
	mapCM := flag.String("map", "", "the Map's ConfigMap the survey ran against, as JSON")
	flag.Parse()
	if err := run(*expected, *survey, *proposals, *mapCM); err != nil {
		fmt.Fprintln(os.Stderr, "score:", err)
		os.Exit(1)
	}
}

func run(expectedPath, surveyPath, proposalsPath, mapPath string) error {
	var exp Expected
	if err := readYAML(expectedPath, &exp); err != nil {
		return err
	}
	var s v1alpha1.TakeCareSurvey
	if err := readYAML(surveyPath, &s); err != nil {
		return err
	}
	var pcm, mcm corev1.ConfigMap
	if err := readYAML(proposalsPath, &pcm); err != nil {
		return err
	}
	if err := readYAML(mapPath, &mcm); err != nil {
		return err
	}
	ps, err := Proposals(&pcm)
	if err != nil {
		return err
	}
	base, err := Entries(&mcm)
	if err != nil {
		return err
	}
	r, err := Score(exp, &s, ps, base)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(r)
}

func readYAML(path string, v any) error {
	b, err := os.ReadFile(path) //nolint:gosec // a path the bench script gives
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// Proposals reads a survey's proposals ConfigMap: one key per proposal,
// the proposal as YAML in the status's shape (spec 10 §7).
func Proposals(cm *corev1.ConfigMap) ([]v1alpha1.Proposal, error) {
	out := make([]v1alpha1.Proposal, 0, len(cm.Data))
	for k, v := range cm.Data {
		var p v1alpha1.Proposal
		if err := yaml.Unmarshal([]byte(v), &p); err != nil {
			return nil, fmt.Errorf("proposal %s: %w", k, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// Entries reads the demo Map's ConfigMap: one key per file, each an entry.
func Entries(cm *corev1.ConfigMap) (map[string]mapserver.Entry, error) {
	out := map[string]mapserver.Entry{}
	for k, v := range cm.Data {
		var e mapserver.Entry
		if err := yaml.Unmarshal([]byte(v), &e); err != nil {
			return nil, fmt.Errorf("map file %s: %w", k, err)
		}
		out[e.Key] = e
	}
	return out, nil
}
