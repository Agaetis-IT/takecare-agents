// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package engine_test

import (
	"go/build"
	"strings"
	"testing"
)

const module = "github.com/agaetis-it/takecare-agents/"

// The engine sees a run, never an alert (spec 06 §2): no package of the
// boundary depends, even indirectly, on the alert intake or on a role.
func TestBoundaryImportsNoRole(t *testing.T) {
	forbidden := []string{module + "internal/alerts", module + "internal/doc", module + "internal/controller"}
	seen := map[string]bool{}
	var walk func(path, from string)
	walk = func(path, from string) {
		if seen[path] || !strings.HasPrefix(path, module) {
			return
		}
		seen[path] = true
		for _, f := range forbidden {
			if path == f || strings.HasPrefix(path, f+"/") {
				t.Errorf("%s imports %s", from, path)
			}
		}
		pkg, err := build.Import(path, ".", 0)
		if err != nil {
			t.Fatalf("import %s: %v", path, err)
		}
		for _, imp := range pkg.Imports {
			walk(imp, path)
		}
	}
	for _, root := range []string{module + "internal/engine", module + "internal/engine/kagent0103"} {
		walk(root, "(root)")
	}
	if !seen[module+"internal/engine/kagent0103"] {
		t.Fatal("the walk did not reach the implementation: the check would not bite")
	}
}
