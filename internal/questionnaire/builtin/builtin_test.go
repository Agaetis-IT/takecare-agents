// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package builtin

import (
	"slices"
	"strings"
	"testing"

	"github.com/agaetis-it/takecare-agents/internal/questionnaire"
)

const (
	operatorNS    = "takecare-system"
	docFile       = "takecare/roles/doc.yaml"
	intendantFile = "takecare/roles/intendant.yaml"
	valuesFile    = "takecare/values.yaml"
	promFile      = "map/observability/prometheus.yaml"
)

func load(t *testing.T) *questionnaire.Set {
	t.Helper()
	set, err := questionnaire.Load(Sources(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// Spec 13 §4.1: the fourteen factual questions, each with its why, none
// asking for a secret value.
func TestQuestions(t *testing.T) {
	set := load(t)
	want := []string{"cluster.name", "operator.namespace", "environments", "teams", "alerts.source", "metrics.service",
		"report.kind", "report.destinations", "report.secret", "model.provider", "model.secret", "map.repository",
		"map.secret", "knowledge"}
	got := make([]string, 0, len(set.Questions))
	for _, q := range set.Questions {
		got = append(got, q.ID)
		if q.Why == "" {
			t.Errorf("%s has no why", q.ID)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("questions %v, want %v", got, want)
	}
	if len(set.Outputs) != 0 {
		t.Errorf("the built-in file has outputs: the skeleton renders them")
	}
}

func answers(t *testing.T, set *questionnaire.Set, unknown ...string) map[string]questionnaire.Answer {
	t.Helper()
	all := map[string]questionnaire.Answer{
		"cluster.name":        {Value: "east"},
		"operator.namespace":  {Value: operatorNS},
		"environments":        {Value: []string{"shop", "pay"}},
		"teams":               {Value: []string{"web=shop", "money=pay shop"}},
		"alerts.source":       {Value: "None yet"},
		"metrics.service":     {Value: "monitoring/prometheus"},
		"report.kind":         {Value: "Slack"},
		"report.destinations": {Value: []string{"practitioner=C0123456789", "review=#map-review"}},
		"report.secret":       {Value: questionnaire.SecretRef{Namespace: operatorNS, Name: "slack", Key: "token"}},
		"model.provider":      {Value: "https://llm.example.com/v1 some-model"},
		"model.secret":        {Value: questionnaire.SecretRef{Namespace: "other", Name: "model"}},
		"map.repository":      {Value: "git@git.example.com:sre/map.git main ."},
		"map.secret":          {Value: questionnaire.SecretRef{Namespace: operatorNS, Name: "map-key", Key: "id_ed25519"}},
		"knowledge":           {Value: ""},
	}
	for _, id := range unknown {
		all[id] = questionnaire.Answer{Unknown: true}
	}
	for id, a := range all {
		if err := questionnaire.CheckAnswer(set.Question(id), a); err != nil {
			t.Fatal(err)
		}
	}
	return all
}

func render(t *testing.T, set *questionnaire.Set, a map[string]questionnaire.Answer) map[string]string {
	t.Helper()
	out, err := Skeleton(set, a, Dirs{Map: "map", Resources: "takecare"}, "2026-11-25")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, r := range out {
		files[r.Path] = string(r.Data)
	}
	// Every file passes the checks before writing.
	if err := questionnaire.Check(t.TempDir(), "map", out); err != nil {
		for p, d := range files {
			t.Logf("%s:\n%s", p, d)
		}
		t.Fatal(err)
	}
	return files
}

// A named cluster qualifies the environment keys and sources (ADR 0016
// §5), owners come from the teams, SSH needs known hosts, and a Secret
// outside the operator's namespace is flagged.
func TestSkeletonNamedCluster(t *testing.T) {
	set := load(t)
	files := render(t, set, answers(t, set))
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	want := []string{"map/README.md", "map/environment/east.pay.yaml", "map/environment/east.shop.yaml", "map/environment/east.yaml",
		promFile, "map/team/money.yaml", "map/team/web.yaml",
		docFile, intendantFile, valuesFile}
	if !slices.Equal(paths, want) {
		t.Fatalf("paths %v, want %v", paths, want)
	}
	for p, holds := range map[string][]string{
		"map/environment/east.shop.yaml": {"owner: team/web", `kubernetes: "east/shop"`, "observedBy: [observability/prometheus]"},
		promFile:                         {`kubernetes: "east/monitoring/Service/prometheus"`},
		valuesFile: {"not in other", "sshKeySecret:", "knownHostsConfigMap", `create: "false"`,
			`{ name: practitioner, target: "C0123456789" }`, "# TODO: the key of the Secret"},
		docFile:       {"level: Apprentice", "practitioner: practitioner", "metrics: [prometheus]"},
		intendantFile: {"apprentice: review"},
	} {
		for _, h := range holds {
			if !strings.Contains(files[p], h) {
				t.Errorf("%s does not hold %q:\n%s", p, h, files[p])
			}
		}
	}
	if strings.Contains(files[docFile], "apprentice:") {
		t.Errorf("the Doc has an apprentice destination none was given for:\n%s", files[docFile])
	}
}

// Spec 13 §4.1: an unknown answer leaves a TODO where its value goes,
// never a guessed value.
func TestSkeletonUnknown(t *testing.T) {
	set := load(t)
	files := render(t, set, answers(t, set, "cluster.name", "operator.namespace", "environments", "teams", "metrics.service",
		"report.kind", "model.provider", "model.secret", "map.repository", "knowledge"))
	for p, holds := range map[string][]string{
		"map/README.md": {"TODO: init did not know the cluster's name", "qualify each\nenvironment key",
			"TODO: init did not know the application namespaces (environment/*), the teams (team/*), the Prometheus service"},
		valuesFile:    {"# TODO: unknown: the operator's namespace", "# TODO: unknown: the provider's", "# TODO: unknown: the Secret's name", "# TODO: unknown: where results are posted", "# TODO: unknown: the Map's Git repository"},
		docFile:       {`namespace: ""`, "# TODO: unknown: the namespaces", "# TODO: unknown: a runbook source"},
		intendantFile: {"# TODO: unknown: the operator's namespace", "# TODO: unknown: a runbook source"},
	} {
		for _, h := range holds {
			if !strings.Contains(files[p], h) {
				t.Errorf("%s does not hold %q:\n%s", p, h, files[p])
			}
		}
	}
	if _, ok := files["map/team/web.yaml"]; ok {
		t.Error("a team entry was written for unknown teams")
	}
	if strings.Contains(files[valuesFile], "some-model") {
		t.Error("an unknown answer's value reached the values")
	}
}

// An unknown cluster name leaves the entries unqualified, each with a TODO
// saying a named cluster qualifies its key and source; an empty name, a
// single-cluster install, leaves none.
func TestSkeletonUnknownCluster(t *testing.T) {
	set := load(t)
	unknown := render(t, set, answers(t, set, "cluster.name"))
	for _, p := range []string{"map/environment/shop.yaml", promFile} {
		if !strings.HasPrefix(unknown[p], "# TODO: unknown: the cluster's name") {
			t.Errorf("%s has no TODO for the cluster's name:\n%s", p, unknown[p])
		}
	}
	if strings.Contains(unknown["map/team/web.yaml"], "TODO") {
		t.Errorf("a team entry, which no cluster qualifies, has a TODO:\n%s", unknown["map/team/web.yaml"])
	}
	a := answers(t, set)
	a["cluster.name"] = questionnaire.Answer{Value: ""}
	single := render(t, set, a)
	for p, d := range single {
		if strings.Contains(d, "cluster's name") {
			t.Errorf("%s has a TODO for the cluster's name on a single-cluster install:\n%s", p, d)
		}
	}
	if strings.Contains(single[docFile], "runbook source") {
		t.Errorf("an empty knowledge answer left a TODO:\n%s", single[docFile])
	}
}
