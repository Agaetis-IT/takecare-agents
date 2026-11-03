// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package semconv

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// No Go file outside this package spells a gen_ai.* or takecare.* attribute
// name (ADR 0006): a rename upstream must be one change, here.
func TestAttributeNamesLiveHereOnly(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	literal := regexp.MustCompile(`"(gen_ai\.[a-z_.]+|takecare\.[a-z_.]+)"`)
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "spikes", "charts", "Claude outputs", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.Contains(path, "internal/telemetry/semconv") {
			return nil
		}
		content, err := os.ReadFile(path) //nolint:gosec // the test walks the repository's own Go files
		if err != nil {
			return err
		}
		for _, m := range literal.FindAllStringSubmatch(string(content), -1) {
			// takecare.agaetis.fr/... are label keys, not attributes.
			if strings.HasPrefix(m[1], "takecare.agaetis.fr") {
				continue
			}
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel+": "+m[1])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("attribute names spelled outside internal/telemetry/semconv:\n  %s", strings.Join(offenders, "\n  "))
	}
}
