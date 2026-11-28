// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const (
	demoApp = "ghcr.io/agaetis-it/takecare-agents-demo-app"
	repo    = "team/deploy"
	prURLOK = "https://github.com/team/deploy/pull/7"
	// canary is model text that must never reach an error.
	canary = "canary-text-of-the-model"
)

var branch = Branch(maintenanceName)

func fenced(body string) string { return "My findings.\n\n```json\n" + body + "\n```\n" }

const validAnswer = `{"findings": [
  {"id": "Drift/x", "kind": "Drift", "component": "` + demoApp + `", "text": "Git declares 0.3.0, 0.2.0 runs.", "severity": "Soon",
   "evidence": [{"tool": "manifests_index", "record": "apps/kustomization.yaml"}]},
  {"kind": "RemovedAPI", "id": "RemovedAPI/batch/v1beta1/CronJob", "component": "nightly-export", "text": "Removed in 1.25.", "severity": "Now"},
  {"kind": "Behind", "component": "kubernetes", "text": "1.32.8 runs; upgrade to 1.33, then on to 1.37.2.", "severity": "Soon"}
 ],
 "plan": {"steps": [
  {"id": "1", "text": "Move the CronJob to batch/v1.", "findings": ["RemovedAPI/batch/v1beta1/CronJob", "Behind/unknown"], "class": "reversible"},
  {"id": "2", "text": "Roll out 0.3.0.", "findings": ["Drift/` + demoApp + `"], "class": "reversible"}]},
 "pullRequests": [{"repository": "` + repo + `", "url": "` + prURLOK + `", "branch": "takecare/mecano/maintenance-0a1b2c3d4e"}]}`

func facts() Facts {
	return Facts{Inventory: demoInventory, Repositories: []string{repo}, Branch: branch, ClusterVersion: "1.32.8", Known: []string{"1.33", "1.33.6"}}
}

func TestParse(t *testing.T) {
	a, err := Parse(fenced(validAnswer))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Findings) != 3 || a.Plan == nil || len(a.Plan.Steps) != 2 || len(a.PullRequests) != 1 {
		t.Errorf("parsed %+v", a)
	}
}

func TestParseRefuses(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"no block", "nothing " + canary},
		{"two blocks", fenced(validAnswer) + fenced(validAnswer)},
		{"not JSON", fenced("{" + canary)},
		{"two values", fenced(`{"findings": []} {}`)},
		{"an unknown field", fenced(`{"findings": [], "` + canary + `": 1}`)},
		{"an unknown kind", fenced(`{"findings": [{"kind": "` + canary + `", "text": "t", "severity": "Now"}]}`)},
		{"an unknown class", fenced(`{"findings": [], "plan": {"steps": [{"id": "1", "text": "t", "class": "` + canary + `"}]}}`)},
		{"no findings", fenced(`{"plan": {"steps": []}}`)},
		{"oversized", fenced(`{"findings": [], "x": "` + strings.Repeat("a", MaxAnswerBytes) + `"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.text)
			if !errors.Is(err, ErrUnreadable) {
				t.Fatalf("err = %v, want ErrUnreadable", err)
			}
			if strings.Contains(err.Error(), canary) {
				t.Errorf("the error quotes the model: %v", err)
			}
		})
	}
}

func record(t *testing.T, level v1alpha1.Level, f Facts) Recorded {
	t.Helper()
	a, err := Parse(fenced(validAnswer))
	if err != nil {
		t.Fatal(err)
	}
	return Record(a, level, f)
}

func TestRecordPractitioner(t *testing.T) {
	got := record(t, v1alpha1.LevelPractitioner, facts())
	ids := FindingIDs(got.Findings)
	want := []string{"Behind/kubernetes", "Drift/" + demoApp, "RemovedAPI/batch/v1beta1/CronJob"}
	if diff := cmp.Diff(want, ids); diff != "" {
		t.Errorf("finding ids (-want +got):\n%s", diff)
	}
	if got.Counts != (v1alpha1.MaintenanceCounts{Findings: 3, Now: 1, PullRequests: 1}) {
		t.Errorf("counts %+v", got.Counts)
	}
	if got.Plan == nil || got.Plan.Tested != v1alpha1.PlanNotTested || len(got.Plan.PullRequests) != 1 {
		t.Fatalf("plan %+v", got.Plan)
	}
	if s := got.Plan.Steps[0].Findings; !slices.Equal(s, []string{"RemovedAPI/batch/v1beta1/CronJob"}) {
		t.Errorf("a step keeps a finding that was not kept: %v", s)
	}
	if len(got.BlindSpots) != 0 {
		t.Errorf("blind spots %+v", got.BlindSpots)
	}
}

func TestRecordApprentice(t *testing.T) {
	got := record(t, v1alpha1.LevelApprentice, facts())
	if got.Plan != nil || got.Counts.PullRequests != 0 || got.Counts.Findings != 3 {
		t.Errorf("an Apprentice keeps a plan or a pull request: %+v", got)
	}
}

// TestRecordDropsUnknownFacts: a finding on a component, an apiVersion or
// a version the inventory does not hold is dropped with one blind spot
// (spec 12 §9).
func TestRecordDropsUnknownFacts(t *testing.T) {
	for _, tc := range []struct{ name, finding string }{
		{"an unknown component", `{"kind": "Behind", "component": "ghcr.io/other", "text": "behind", "severity": "Now"}`},
		{"an unknown version", `{"kind": "Behind", "component": "kubernetes", "text": "upgrade to 1.40.1", "severity": "Now"}`},
		{"an unknown apiVersion", `{"kind": "RemovedAPI", "id": "RemovedAPI/extensions/v1beta1/Ingress", "text": "removed", "severity": "Now"}`},
		{"a removed API without its id", `{"kind": "RemovedAPI", "component": "nightly-export", "text": "removed", "severity": "Now"}`},
		{"an unknown certificate", `{"kind": "Certificate", "component": "demo-apps/other-tls", "text": "expires", "severity": "Now"}`},
		{"a component too long", `{"kind": "Behind", "component": "` + strings.Repeat("c", 400) + `", "text": "behind", "severity": "Now"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := Parse(fenced(`{"findings": [` + tc.finding + `]}`))
			if err != nil {
				t.Fatal(err)
			}
			inv := demoInventory
			if strings.Contains(tc.name, "too long") {
				inv.Components = append(slices.Clone(inv.Components), v1alpha1.InventoryComponent{Key: strings.Repeat("c", 400)})
			}
			f := facts()
			f.Inventory = inv
			got := Record(a, v1alpha1.LevelApprentice, f)
			if len(got.Findings) != 0 || len(got.BlindSpots) != 1 || got.BlindSpots[0].Reason != UnknownFact {
				t.Errorf("got %+v", got)
			}
		})
	}
	// A certificate by namespace/name, a drift on a GitOps tool, a version
	// with a v.
	a, err := Parse(fenced(`{"findings": [
	  {"kind": "Certificate", "component": "demo-apps/billing-tls", "text": "expires soon", "severity": "Soon"},
	  {"kind": "Behind", "component": "kubernetes", "text": "v1.32.8 runs", "severity": "Later"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := Record(a, v1alpha1.LevelApprentice, facts()); len(got.Findings) != 2 || len(got.BlindSpots) != 0 {
		t.Errorf("known facts dropped: %+v", got)
	}
}

// TestRecordPullRequests keeps a pull request only on a repository of the
// maintenance and on its branch, never in a silent maintenance, and keeps
// a valid one opened while the rule was unproven with a blind spot.
func TestRecordPullRequests(t *testing.T) {
	pr := func(repository, url, branch string) string {
		return fmt.Sprintf(`{"findings": [], "plan": {"steps": []}, "pullRequests": [{"repository": %q, "url": %q, "branch": %q}]}`, repository, url, branch)
	}
	for _, tc := range []struct {
		name   string
		answer string
		facts  func(*Facts)
		kept   bool
		reason string
	}{
		{"valid", pr(repo, prURLOK, branch), nil, true, ""},
		{"a foreign repository", pr("evil/repo", prURLOK, branch), nil, false, ForeignPR},
		{"a wrong branch", pr(repo, prURLOK, "takecare/mecano/other"), nil, false, ForeignPR},
		{"an http URL", pr(repo, "http://github.com/team/deploy/pull/7", branch), nil, false, ForeignPR},
		{"a URL with user information", pr(repo, "https://user:pw@github.com/x", branch), nil, false, ForeignPR},
		{"no repositories allowed", pr(repo, prURLOK, branch), func(f *Facts) { f.Repositories = nil }, false, ForeignPR},
		{"silent", pr(repo, prURLOK, branch), func(f *Facts) { f.Silent = true }, false, SilentPR},
		{"unproven", pr(repo, prURLOK, branch), func(f *Facts) { f.Unproven = true }, true, UnprovenPR},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := Parse(fenced(tc.answer))
			if err != nil {
				t.Fatal(err)
			}
			f := facts()
			if tc.facts != nil {
				tc.facts(&f)
			}
			got := Record(a, v1alpha1.LevelPractitioner, f)
			if kept := len(got.Plan.PullRequests) == 1; kept != tc.kept || got.Counts.PullRequests != int32(len(got.Plan.PullRequests)) {
				t.Errorf("kept %v, want %v: %+v", kept, tc.kept, got.Plan)
			}
			if tc.reason == "" && len(got.BlindSpots) != 0 || tc.reason != "" && (len(got.BlindSpots) != 1 || got.BlindSpots[0].Reason != tc.reason) {
				t.Errorf("blind spots %+v, want %q", got.BlindSpots, tc.reason)
			}
		})
	}
}

// TestRecordTestedOnCopy says OnCopy only when the copy runs the plan's
// target, ready, with its evidence (spec 12 §8).
func TestRecordTestedOnCopy(t *testing.T) {
	ev := []v1alpha1.Evidence{{Tool: "resources_list", Record: "staging:demo-apps/Deployment/billing-api"}}
	for _, tc := range []struct {
		name string
		copy map[string]CopyFact
		want v1alpha1.PlanTested
	}{
		{"no copy", nil, v1alpha1.PlanNotTested},
		{"the target, ready", map[string]CopyFact{demoApp: {Version: "0.3.0", Ready: true, Evidence: ev}}, v1alpha1.PlanOnCopy},
		{"the target, not ready", map[string]CopyFact{demoApp: {Version: "0.3.0", Evidence: ev}}, v1alpha1.PlanNotTested},
		{"the live version", map[string]CopyFact{demoApp: {Version: "0.2.0", Ready: true, Evidence: ev}}, v1alpha1.PlanNotTested},
		{"another version", map[string]CopyFact{demoApp: {Version: "0.4.0", Ready: true, Evidence: ev}}, v1alpha1.PlanNotTested},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := facts()
			f.Copy = tc.copy
			got := record(t, v1alpha1.LevelPractitioner, f)
			if got.Plan.Tested != tc.want || (tc.want == v1alpha1.PlanOnCopy) != (len(got.Plan.Evidence) > 0) {
				t.Errorf("tested %s with %d evidence, want %s", got.Plan.Tested, len(got.Plan.Evidence), tc.want)
			}
		})
	}
}

func TestRecordBounds(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"findings": [`)
	for i := range maxFindings + 3 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"kind": "Behind", "component": "c%d", "text": %q, "severity": "Later"}`, i, strings.Repeat("é", 400))
	}
	b.WriteString(`]}`)
	a, err := Parse(fenced(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	f := facts()
	for i := range maxFindings + 3 {
		f.Inventory.Components = append(f.Inventory.Components, v1alpha1.InventoryComponent{Key: fmt.Sprintf("c%d", i)})
	}
	got := Record(a, v1alpha1.LevelApprentice, f)
	if len(got.Findings) != maxFindings || got.FindingsOmitted != 3 || got.Counts.Findings != maxFindings {
		t.Errorf("kept %d, omitted %d", len(got.Findings), got.FindingsOmitted)
	}
	if l := len(got.Findings[0].Text); l > v1alpha1.MaxFindingText {
		t.Errorf("a text of %d bytes kept", l)
	}
}

func TestChanged(t *testing.T) {
	f := func(ids ...string) []v1alpha1.MaintenanceFinding {
		var out []v1alpha1.MaintenanceFinding
		for _, id := range ids {
			out = append(out, v1alpha1.MaintenanceFinding{ID: id})
		}
		return out
	}
	for _, tc := range []struct {
		name          string
		current, prev []v1alpha1.MaintenanceFinding
		changed       bool
		added         []string
	}{
		{"same set, other order", f("b", "a"), f("a", "b"), false, nil},
		{"one new", f("a", "c"), f("a"), true, []string{"c"}},
		{"one gone", f("a"), f("a", "b"), true, nil},
		{"none before", f("a"), nil, true, []string{"a"}},
		{"none at all", nil, nil, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed, added := Changed(tc.current, tc.prev)
			if changed != tc.changed || !slices.Equal(added, tc.added) {
				t.Errorf("Changed = %v %v, want %v %v", changed, added, tc.changed, tc.added)
			}
		})
	}
}
