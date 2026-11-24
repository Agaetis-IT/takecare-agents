// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/doc"
	"github.com/agaetis-it/takecare-agents/internal/surveyagent"
)

// crdSays returns every CEL rule and message of a generated CRD.
func crdSays(t *testing.T, plural string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "takecare.agaetis.fr_"+plural+".yaml")) //nolint:gosec // the repository's own generated CRD
	if err != nil {
		t.Fatal(err)
	}
	var tree any
	if err := yaml.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	said := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, x := range v {
				if s, ok := x.(string); ok && (k == "rule" || k == "message") {
					said[s] = true
				}
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(tree)
	return said
}

// TestNameBoundsAgree: each kind's rule states its constant, the API
// server and the status say the same message, and a role at its bound
// gives an engine agent name of exactly 63 characters (kagent's Service
// name and label value).
func TestNameBoundsAgree(t *testing.T) {
	for _, tc := range []struct {
		plural, rule, message string
		max                   int
	}{
		{"takecareintendants", fmt.Sprintf("oldSelf.hasValue() || size(self.metadata.name) <= %d", v1alpha1.MaxIntendantName), v1alpha1.MessageIntendantName, v1alpha1.MaxIntendantName},
		{"takecaredocs", fmt.Sprintf("oldSelf.hasValue() || size(self.metadata.name) <= %d", v1alpha1.MaxDocName), v1alpha1.MessageDocName, v1alpha1.MaxDocName},
		{"takecareconnectors", fmt.Sprintf("oldSelf.hasValue() || size(self.metadata.name) <= %d || self.spec.types.all(t, t == 'report')", v1alpha1.MaxConnectorName), v1alpha1.MessageConnectorName, v1alpha1.MaxConnectorName},
		{"takecaredemos", fmt.Sprintf("oldSelf.hasValue() || size(self.metadata.name) <= %d", v1alpha1.MaxDemoName), v1alpha1.MessageDemoName, v1alpha1.MaxDemoName},
		{"takecarekines", fmt.Sprintf("oldSelf.hasValue() || size(self.metadata.name) <= %d", v1alpha1.MaxKineName), v1alpha1.MessageKineName, v1alpha1.MaxKineName},
		{"takecarereviews", fmt.Sprintf("oldSelf.hasValue() || size(self.metadata.name) <= %d", v1alpha1.MaxReviewName), v1alpha1.MessageReviewName, v1alpha1.MaxReviewName},
	} {
		t.Run(tc.plural, func(t *testing.T) {
			said := crdSays(t, tc.plural)
			for _, want := range []string{tc.rule, tc.message} {
				if !said[want] {
					t.Errorf("no CEL rule or message of the CRD is %q", want)
				}
			}
			if !strings.Contains(tc.message, fmt.Sprintf("at most %d characters", tc.max)) {
				t.Errorf("the message %q does not name the bound %d", tc.message, tc.max)
			}
		})
	}
	for _, plural := range []string{"takecareintendants", "takecarekines"} {
		if !crdSays(t, plural)[v1alpha1.MessageScheduleUTC] {
			t.Errorf("the %s CRD does not say %q", plural, v1alpha1.MessageScheduleUTC)
		}
	}
	// The Kiné's engine agent, takecare-kine-<name>, at its bound: 63
	// characters (the agent's renderer comes with lot K3).
	if n := len(v1alpha1.KineAgentPrefix) + v1alpha1.MaxKineName; n != 63 || !strings.Contains(v1alpha1.MessageKineName, v1alpha1.KineAgentPrefix) {
		t.Errorf("Kiné at its bound: agent name of %d characters, want 63; message names the prefix: %v", n, strings.Contains(v1alpha1.MessageKineName, v1alpha1.KineAgentPrefix))
	}
	// A new action's id, <review name>-<n>, fits its bound for a review
	// at its own bound and its 20th action.
	if n := v1alpha1.MaxReviewName + len("-20"); n > v1alpha1.MaxActionID {
		t.Errorf("a new action's id may be %d characters, past MaxActionID %d", n, v1alpha1.MaxActionID)
	}
	for _, tc := range []struct {
		name, prefix, message string
		agent                 int
	}{
		{"Intendant", v1alpha1.IntendantAgentPrefix, v1alpha1.MessageIntendantName, len(surveyagent.AgentName(&v1alpha1.TakeCareIntendant{Name: strings.Repeat("a", v1alpha1.MaxIntendantName)}))},
		{"Doc", v1alpha1.DocAgentPrefix, v1alpha1.MessageDocName, len(doc.AgentName(&v1alpha1.TakeCareDoc{Name: strings.Repeat("a", v1alpha1.MaxDocName)}))},
	} {
		if tc.agent != 63 || !strings.Contains(tc.message, tc.prefix) {
			t.Errorf("%s at its bound: agent name of %d characters, want 63; message names the prefix: %v", tc.name, tc.agent, strings.Contains(tc.message, tc.prefix))
		}
	}
}

// TestNameBounds: each kind takes a name at its bound and refuses one more
// character; a report-only connector takes a longer name, since it has no
// gateway backend.
func TestNameBounds(t *testing.T) {
	cluster := func(n int) client.Object {
		c := clusterConnector()
		c.Name = padded(c.Name, n)
		return c
	}
	report := func(n int) client.Object {
		c := reportConnector()
		c.Name = padded(c.Name, n)
		return c
	}
	docNamed := func(n int) client.Object {
		d := docFixture()
		d.Name = padded(d.Name, n)
		return d
	}
	demoNamed := func(n int) client.Object {
		d := demo()
		d.Name = padded(d.Name, n)
		return d
	}
	for _, tc := range []struct {
		name string
		obj  client.Object
		want string
	}{
		{"Doc at the bound", docNamed(v1alpha1.MaxDocName), ""},
		{"Doc past the bound", docNamed(v1alpha1.MaxDocName + 1), v1alpha1.MessageDocName},
		{"cluster connector at the bound", cluster(v1alpha1.MaxConnectorName), ""},
		{"cluster connector past the bound", cluster(v1alpha1.MaxConnectorName + 1), v1alpha1.MessageConnectorName},
		{"report connector past the bound", report(v1alpha1.MaxConnectorName + 1), ""},
		{"demo at the bound", demoNamed(v1alpha1.MaxDemoName), ""},
		{"demo past the bound", demoNamed(v1alpha1.MaxDemoName + 1), v1alpha1.MessageDemoName},
	} {
		t.Run(tc.name, func(t *testing.T) { checkError(t, create(t, tc.obj), tc.want) })
	}
}

// TestDocSlotCaps: the Doc's connector slots take 16 names of at most 253
// characters, as the Intendant's.
func TestDocSlotCaps(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*v1alpha1.DocConnectors)
		want   string
	}{
		{case16Knowledge, func(c *v1alpha1.DocConnectors) { c.Knowledge = names(16) }, ""},
		{case17Knowledge, func(c *v1alpha1.DocConnectors) { c.Knowledge = names(17) }, wantKnowledge},
		{"17 cluster connectors", func(c *v1alpha1.DocConnectors) { c.Cluster = names(17) }, "cluster"},
		{"17 metrics connectors", func(c *v1alpha1.DocConnectors) { c.Metrics = names(17) }, "metrics"},
		{"17 changes connectors", func(c *v1alpha1.DocConnectors) { c.Changes = names(17) }, "changes"},
		{"17 tickets connectors", func(c *v1alpha1.DocConnectors) { c.Tickets = names(17) }, connTickets},
		{"map connector name past 253", func(c *v1alpha1.DocConnectors) { c.Map = []string{strings.Repeat("m", 254)} }, connMap},
		{"report connector name past 253", func(c *v1alpha1.DocConnectors) { c.Report = strings.Repeat("r", 254) }, connReport},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := docFixture()
			tc.mutate(&d.Spec.Connectors)
			checkError(t, create(t, d), tc.want)
		})
	}
}
