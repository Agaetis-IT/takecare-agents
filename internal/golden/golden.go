// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package golden compares a test's output with a file under testdata/,
// reviewed like code. Regenerate with `go test <package> -update` (the flag
// exists only in packages that import this one).
package golden

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata/")

// Assert fails the test when got differs from testdata/<name>, or rewrites
// the file with -update.
func Assert(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // a test's own testdata
	if err != nil {
		t.Fatalf("read golden file (run with -update to create it): %v", err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("%s differs (-want +got), run with -update if intended:\n%s", path, diff)
	}
}
