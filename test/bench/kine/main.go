// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Command kine scores one review of the Kiné bench (spec 11 §12) against
// the expected set and prints the run's score as one JSON line: ids,
// counts and fixed phrases, never a statement, an action's text or a
// post-mortem. hack/bench-kine.sh runs it once per review.
//
// Usage: go run ./test/bench/kine -expected FILE -review FILE, the review
// as kubectl get -o json prints it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

func main() {
	expected := flag.String("expected", "test/bench/kine/expected.yaml", "the expected set")
	review := flag.String("review", "", "the TakeCareReview, as JSON")
	flag.Parse()
	if err := run(*expected, *review); err != nil {
		fmt.Fprintln(os.Stderr, "score:", err)
		os.Exit(1)
	}
}

func run(expectedPath, reviewPath string) error {
	var exp Expected
	// Strict: a misspelled key of the expected set would score nothing.
	if err := readYAML(expectedPath, &exp, yaml.DisallowUnknownFields); err != nil {
		return err
	}
	var rv v1alpha1.TakeCareReview
	if err := readYAML(reviewPath, &rv); err != nil {
		return err
	}
	r, err := Score(exp, &rv)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(r)
}

func readYAML(path string, v any, opts ...yaml.JSONOpt) error {
	b, err := os.ReadFile(path) //nolint:gosec // a path the bench script gives
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(b, v, opts...); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
