// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package questionnaire

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const doc = `apiVersion: takecare.agaetis.fr/v1alpha1
kind: TakeCareDoc
metadata:
  name: doc
  namespace: takecare-system
spec:
  level: Apprentice
  scope:
    namespaces: [shop]
`

func faultsOf(t *testing.T, err error) []string {
	t.Helper()
	if err == nil {
		return nil
	}
	var faults Faults
	if !errors.As(err, &faults) {
		t.Fatalf("got %v, want faults", err)
	}
	out := make([]string, len(faults))
	for i, f := range faults {
		out[i] = f.String()
	}
	return out
}

// Spec 13 §4.3: every output renders first, a YAML one parses, one under
// the Map passes the Map's checks, one of our kinds passes its schema.
func TestCheck(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "map", "team"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "map", "team", "payments.yaml"),
		[]byte("key: team/payments\nprovenance: declared\nstatus: validated\ncreated: \"2026-11-01\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := "key: environment/shop\nprovenance: declared\nstatus: proposed\ncreated: \"2026-11-25\"\nowner: team/payments\n"
	good := []Rendered{
		{Path: "takecare/roles/doc.yaml", Data: []byte(doc)},
		{Path: pathValues, Data: []byte("platform:\n  create: true\n---\n# nothing\n")},
		{Path: "map/environment/shop.yaml", Data: []byte(env)},
		{Path: "map/README.md", Data: []byte("# The Map\n")},
		{Path: "takecare/notes.md", Data: []byte("not: [yaml\n")},
	}
	if err := Check(repo, "map", good); err != nil {
		t.Fatalf("a good tree: %v", err)
	}

	bad := []Rendered{
		{Path: "takecare/broken.yaml", Data: []byte("a: [1\n")},
		{Path: "takecare/roles/doc.yaml", Data: []byte(strings.NewReplacer("Apprentice", "Master", "  scope:", "  scop:").Replace(doc))},
		{Path: "takecare/roles/unknown.yaml", Data: []byte("apiVersion: takecare.agaetis.fr/v1alpha1\nkind: TakeCareChef\nmetadata: { name: x }\n")},
		{Path: "takecare/roles/old.yml", Data: []byte("---\nkind: Note\n---\n" + strings.Replace(doc, "v1alpha1", "v1", 1))},
		{Path: "takecare/roles/anonymous.yaml", Data: []byte(strings.Replace(doc, "  name: doc\n", "", 1))},
		{Path: "takecare/other.yaml", Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata: { name: x }\nanything: goes\n")},
		{Path: "map/environment/shop.yaml", Data: []byte(strings.Replace(env, "environment/shop", "environment/other", 1))},
		{Path: "map/team/ops.md", Data: []byte("# ops\n")},
	}
	want := []string{
		"takecare/broken.yaml: not YAML: yaml: line 1: did not find expected ',' or ']'",
		`takecare/roles/doc.yaml: document 1: TakeCareDoc: spec.level: Unsupported value: "Master": supported values: "Apprentice", "Practitioner", "Expert"`,
		`takecare/roles/doc.yaml: document 1: TakeCareDoc: spec.scope: Required value`,
		`takecare/roles/doc.yaml: document 1: TakeCareDoc: unknown field "spec.scop"`,
		"takecare/roles/unknown.yaml: document 1: TakeCareChef v1alpha1 is not a served kind of takecare.agaetis.fr",
		"takecare/roles/old.yml: document 2: TakeCareDoc v1 is not a served kind of takecare.agaetis.fr",
		"takecare/roles/anonymous.yaml: document 1: TakeCareDoc has no metadata.name",
		"map: map/environment/shop.yaml: key environment/other does not match the file's place, environment/shop",
		"map: map/team/ops.md: not a map/<type>/<name>.yaml file",
	}
	got := faultsOf(t, Check(repo, "map", bad))
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("faults (-want +got):\n%s", diff)
	}

	// A Map elsewhere in the repository, and one not there yet.
	if err := Check(repo, "ops/map", []Rendered{{Path: "ops/map/environment/shop.yaml", Data: []byte(strings.Replace(env, "owner: team/payments\n", "", 1))}}); err != nil {
		t.Errorf("a new Map under ops/map: %v", err)
	}
	if err := Check(repo, "../map", nil); err == nil {
		t.Error("a Map directory outside the repository accepted")
	}
}

// Every takecare.agaetis.fr object the demo applies passes its kind's
// schema: the check refuses what the API server refuses, not more.
func TestCheckDemoManifests(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "demo", "manifests", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no demo manifest: %v", err)
	}
	checked := 0
	for _, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // the repository's demo manifests
		if err != nil {
			t.Fatal(err)
		}
		docs, err := documents(raw)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for i, d := range docs {
			if obj, ok := d.(map[string]any); ok && strings.HasPrefix(obj["apiVersion"].(string), group+"/") {
				checked++
				if msgs := checkKind(d); len(msgs) > 0 {
					t.Errorf("%s document %d: %v", f, i+1, msgs)
				}
			}
		}
	}
	if checked < 3 {
		t.Errorf("checked %d objects of %s, want the demo's roles and connectors", checked, group)
	}
}
