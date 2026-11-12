// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
	"github.com/agaetis-it/takecare-agents/internal/surveyagent"
)

// The bench's Map, as the script leaves it: service/billing-api without
// its documentedBy, the runbook without a knowledge source.
const (
	serviceBase = `key: service/billing-api
provenance: declared
status: validated
created: 2026-11-05
sources:
  - kubernetes: demo-apps/Deployment/billing-api
relations:
  runsAs: [compute/billing-api]
  dependsOn: [datastore/billing-db, network/billing-db-proxy]
  belongsTo: [team/platform]
`
	runbookBase = `key: runbook/proxy-timeout
provenance: declared
status: validated
created: 2026-11-05
description: RB-014, connections reset by the database proxy.
`
)

func loadExpected(t *testing.T) Expected {
	t.Helper()
	var exp Expected
	if err := readYAML("expected.yaml", &exp); err != nil {
		t.Fatal(err)
	}
	return exp
}

func baseMap(t *testing.T) map[string]mapserver.Entry {
	t.Helper()
	m, err := Entries(&corev1.ConfigMap{Data: map[string]string{
		"map__service__billing-api.yaml":   serviceBase,
		"map__runbook__proxy-timeout.yaml": runbookBase,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// proposal is one of the agent's proposals.
func proposal(action v1alpha1.ProposalAction, key, entry string, evidence ...string) v1alpha1.Proposal {
	return v1alpha1.Proposal{ID: key, Action: action, Key: v1alpha1.EntityKey(key), Entry: entry, Evidence: evidence, Source: v1alpha1.ProposalFromAgent}
}

const (
	updateRunbook = "Update runbook/proxy-timeout"
	updateService = "Update service/billing-api"
)

var (
	runbookWithSource = runbookBase + "sources:\n  - knowledge: runbooks/RB-014\n"
	serviceDocumented = serviceBase + "  documentedBy: [runbook/proxy-timeout]\n"
	serviceGuessed    = serviceBase[:len(serviceBase)-len("  belongsTo: [team/platform]\n")] +
		"  belongsTo: [team/platform, team/billing]\n  documentedBy: [runbook/proxy-timeout]\n"
	rightRunbook = proposal(v1alpha1.ProposalUpdate, "runbook/proxy-timeout", runbookWithSource, "runbooks_search", "runbooks_get")
	rightService = proposal(v1alpha1.ProposalUpdate, "service/billing-api", serviceDocumented, "runbooks_search")
)

func survey(layers ...v1alpha1.SurveyLayer) *v1alpha1.TakeCareSurvey {
	start := metav1.Unix(1793433600, 0)
	end := metav1.Unix(1793433690, 0)
	s := &v1alpha1.TakeCareSurvey{Name: "s"}
	s.Status.Phase, s.Status.StartedAt, s.Status.CompletedAt = v1alpha1.RunCompleted, &start, &end
	s.Status.Usage = &v1alpha1.Usage{ModelCalls: 7, InputTokens: 41174, OutputTokens: 590}
	s.Status.Layers = append([]v1alpha1.SurveyLayer{{Name: "compute", Seen: 1}}, layers...)
	return s
}

// The four synthetic sets of the bench's --score-only check.
func TestScore(t *testing.T) {
	exp := loadExpected(t)
	if len(exp.Required) != 2 {
		t.Fatalf("expected.yaml: %d required items, want 2", len(exp.Required))
	}
	importOnly := v1alpha1.Proposal{ID: "i", Action: v1alpha1.ProposalAdd, Key: "network/billing-api", Entry: "key: network/billing-api\n", Source: v1alpha1.ProposalFromImport}
	extraRunbook := proposal(v1alpha1.ProposalAdd, "runbook/memory-growth",
		"key: runbook/memory-growth\nprovenance: proposed\nstatus: proposed\ncreated: 2026-11-12\nsources:\n  - knowledge: runbooks/RB-007\n", "runbooks_get")
	agent := v1alpha1.SurveyLayer{Name: surveyagent.Layer, Seen: 4, Proposed: 2, NotSeen: []v1alpha1.NotSeen{
		{Ref: surveyagent.Layer, Reason: "1 agent proposals dropped: evidence"},
		{Ref: surveyagent.Layer, Reason: "1 agent proposals dropped: scope"},
	}}
	for _, tc := range []struct {
		name      string
		proposals []v1alpha1.Proposal
		matched   []string
		allowed   []string
		forbidden []string
		other     []string
		precision float64
		ok        bool
	}{
		{
			name:      "perfect: both required, an import proposal ignored, an allowed runbook",
			proposals: []v1alpha1.Proposal{importOnly, rightService, rightRunbook, extraRunbook},
			matched:   []string{updateRunbook, updateService},
			allowed:   []string{"Add runbook/memory-growth"},
			precision: 1, ok: true,
		},
		{
			name:      "one missing: the runbook's source without the documentedBy",
			proposals: []v1alpha1.Proposal{rightRunbook},
			matched:   []string{updateRunbook},
			precision: 1,
		},
		{
			name: "one forbidden: a Remove, and the runbook without the evidence that shows it",
			proposals: []v1alpha1.Proposal{
				proposal(v1alpha1.ProposalRemove, "service/notifications", "key: service/notifications\n", "resources_list"),
				proposal(v1alpha1.ProposalUpdate, "runbook/proxy-timeout", runbookWithSource, "map_at"),
			},
			forbidden: []string{"Remove service/notifications"},
			// Right content, but its evidence does not show it: allowed,
			// never matched.
			allowed:   []string{updateRunbook},
			precision: 0.5,
		},
		{
			name: "both required matched, but a team guessed on another proposal",
			proposals: []v1alpha1.Proposal{rightRunbook, rightService,
				proposal(v1alpha1.ProposalUpdate, "compute/billing-api", "key: compute/billing-api\nrelations:\n  belongsTo: [team/billing]\n", "resources_get")},
			matched:   []string{updateRunbook, updateService},
			forbidden: []string{"Update compute/billing-api"},
			precision: 2.0 / 3,
		},
		{
			name:      "the required key, but with a guessed team: forbidden, not matched",
			proposals: []v1alpha1.Proposal{rightRunbook, proposal(v1alpha1.ProposalUpdate, "service/billing-api", serviceGuessed, "runbooks_get")},
			matched:   []string{updateRunbook},
			forbidden: []string{updateService},
			precision: 0.5,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Score(exp, survey(agent), tc.proposals, baseMap(t))
			if err != nil {
				t.Fatal(err)
			}
			want := Run{
				Survey: "s", Phase: "Completed", Seen: 4, Dropped: map[string]int32{"evidence": 1, "scope": 1}, Required: 2,
				Matched: nonNil(tc.matched), Allowed: nonNil(tc.allowed), Forbidden: nonNil(tc.forbidden), Other: nonNil(tc.other),
				Proposals: countAgent(tc.proposals), Recall: float64(len(tc.matched)) / 2, Precision: tc.precision, OK: tc.ok,
				DurationSeconds: r.DurationSeconds, ModelCalls: r.ModelCalls, InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
			}
			if diff := cmp.Diff(want, r); diff != "" {
				t.Errorf("score (-want +got):\n%s", diff)
			}
			if r.DurationSeconds == nil || *r.DurationSeconds != 90 || *r.InputTokens != 41174 {
				t.Errorf("metrics: duration %v, input tokens %v", r.DurationSeconds, r.InputTokens)
			}
		})
	}
}

// A pass that did not answer: its fixed phrase is kept, nothing matched.
func TestScorePassNotFinished(t *testing.T) {
	s := survey(v1alpha1.SurveyLayer{Name: surveyagent.Layer, NotSeen: []v1alpha1.NotSeen{{Ref: surveyagent.Layer, Reason: "the agent pass did not finish"}}})
	r, err := Score(loadExpected(t), s, nil, baseMap(t))
	if err != nil {
		t.Fatal(err)
	}
	if r.OK || r.Recall != 0 || !cmp.Equal(r.Agent, []string{"the agent pass did not finish"}) || len(r.Dropped) != 0 {
		t.Errorf("score: ok %v, recall %v, agent %q, dropped %v", r.OK, r.Recall, r.Agent, r.Dropped)
	}
}

// The ConfigMap's proposals, as the survey writes them, are read back.
func TestProposals(t *testing.T) {
	raw, err := yaml.Marshal(rightRunbook)
	if err != nil {
		t.Fatal(err)
	}
	ps, err := Proposals(&corev1.ConfigMap{Data: map[string]string{"abc": string(raw)}})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]v1alpha1.Proposal{rightRunbook}, ps); diff != "" {
		t.Errorf("proposals (-want +got):\n%s", diff)
	}
	if _, err := Proposals(&corev1.ConfigMap{Data: map[string]string{"bad": "action: [\n"}}); err == nil {
		t.Error("a proposal that is not YAML was read")
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func countAgent(ps []v1alpha1.Proposal) int {
	n := 0
	for _, p := range ps {
		if p.Source == v1alpha1.ProposalFromAgent {
			n++
		}
	}
	return n
}

// The bench's Map is the demo Map less one line of one file
// (hack/bench-intendant.sh): both must still be there, and every required
// key must be an entry of the demo Map.
func TestBenchMapMatchesDemoMap(t *testing.T) {
	const dir = "../../../charts/takecare/files/demo-map/"
	script, err := os.ReadFile("../../../hack/bench-intendant.sh")
	if err != nil {
		t.Fatal(err)
	}
	file := regexp.MustCompile(`(?m)^MAP_FILE=(\S+)$`).FindSubmatch(script)
	line := regexp.MustCompile(`(?m)^MAP_LINE='([^']+)'$`).FindSubmatch(script)
	if file == nil || line == nil {
		t.Fatal("hack/bench-intendant.sh: no MAP_FILE or MAP_LINE")
	}
	b, err := os.ReadFile(dir + strings.ReplaceAll(string(file[1]), "__", "/")) //nolint:gosec // a file of this repository
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(strings.Split(string(b), "\n"), string(line[1])) {
		t.Errorf("the demo Map's %s has no line %q", file[1], line[1])
	}
	for _, q := range loadExpected(t).Required {
		if _, err := os.Stat(dir + "map/" + q.Key + ".yaml"); err != nil {
			t.Errorf("required %s: not an entry of the demo Map: %v", q.Key, err)
		}
	}
}
