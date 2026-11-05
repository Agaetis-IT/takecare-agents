// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package doc

import (
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

func completed(level v1alpha1.Level) *v1alpha1.TakeCareInvestigation {
	created := time.Date(2026, 10, 16, 3, 0, 12, 0, time.UTC)
	started := metav1.NewTime(created.Add(time.Second))
	inv := investigation()
	inv.CreationTimestamp = metav1.NewTime(created)
	inv.Spec.Level = level
	inv.Status.StartedAt = &started
	inv.Status.TraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	inv.Status.Diagnosis = "Observed: billing-api pod log, 504 from db-proxy at 03:00:05.\nRoot cause: the proxy's idle timeout is shorter than the pool's.\nAction: the platform team aligns the proxy's idle timeout with the pool's."
	inv.Status.Usage = &v1alpha1.Usage{ModelCalls: 7, InputTokens: 41250, CachedInputTokens: 30200, OutputTokens: 1310, EstimatedCost: "0.0075 USD"}
	return inv
}

func TestPublication(t *testing.T) {
	now := time.Date(2026, 10, 16, 3, 0, 51, 0, time.UTC)
	apprentice := completed(v1alpha1.LevelApprentice)
	grouped := completed(v1alpha1.LevelPractitioner)
	grouped.Status.Alerts = []v1alpha1.GroupedAlert{
		{Name: podNotReady, Fingerprint: "before", FiredAt: metav1.NewTime(now.Add(-50 * time.Second))},
		{Name: podCrashLooping, Fingerprint: "after", FiredAt: metav1.NewTime(now.Add(-10 * time.Second))},
	}
	grouped.Status.Usage.Approximate = true
	grouped.Status.Degraded = &v1alpha1.Degradation{Connectors: []string{"prometheus"}}
	noUsage := completed(v1alpha1.LevelPractitioner)
	noUsage.Status.Usage = nil
	for name, tc := range map[string]struct {
		inv    *v1alpha1.TakeCareInvestigation
		reason string
		sent   map[string]bool
	}{
		"publication-practitioner": {inv: completed(v1alpha1.LevelPractitioner)},
		"publication-apprentice":   {inv: apprentice},
		"publication-grouped":      {inv: grouped, sent: map[string]bool{"before": true}},
		"publication-no-usage":     {inv: noUsage, reason: "Prometheus at http://prometheus is unreachable"},
	} {
		t.Run(name, func(t *testing.T) {
			p := NewPublication(tc.inv, now, tc.reason, tc.sent)
			blocks, err := json.MarshalIndent(p.Blocks(), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, name+".json", append(blocks, '\n'))
			golden.Assert(t, name+".txt", []byte(p.Text()))
		})
	}
}

// A resumed run does not know which alerts its message carried: those that
// fired after the run started are named.
func TestPublicationLaterAlertsWithoutTheSentSet(t *testing.T) {
	inv := completed(v1alpha1.LevelPractitioner)
	inv.Status.Alerts = []v1alpha1.GroupedAlert{
		{Name: podNotReady, FiredAt: metav1.NewTime(inv.Status.StartedAt.Add(-time.Second))},
		{Name: podCrashLooping, FiredAt: metav1.NewTime(inv.Status.StartedAt.Add(time.Second))},
	}
	if got := NewPublication(inv, time.Now(), "", nil).Later; len(got) != 1 || got[0] != podCrashLooping {
		t.Errorf("later = %v", got)
	}
}

func TestDestination(t *testing.T) {
	d := &v1alpha1.TakeCareDoc{}
	d.Spec.Report = v1alpha1.DocReport{Apprentice: "review", Practitioner: onCall}
	noApprentice := &v1alpha1.TakeCareDoc{}
	noApprentice.Spec.Report = v1alpha1.DocReport{Practitioner: onCall}
	for _, tc := range []struct {
		name   string
		doc    *v1alpha1.TakeCareDoc
		level  v1alpha1.Level
		silent bool
		want   string
		ok     bool
	}{
		{"Apprentice to review", d, v1alpha1.LevelApprentice, false, "review", true},
		{"Apprentice without an Apprentice destination: status only, never on-call", noApprentice, v1alpha1.LevelApprentice, false, "", false},
		{"Practitioner to on-call", d, v1alpha1.LevelPractitioner, false, onCall, true},
		{"Expert as Practitioner", d, v1alpha1.LevelExpert, false, onCall, true},
		{"silent: nowhere", d, v1alpha1.LevelPractitioner, true, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv := investigation()
			inv.Spec.Level, inv.Spec.Silent = tc.level, tc.silent
			got, ok := Destination(tc.doc, inv)
			if got != tc.want || ok != tc.ok {
				t.Errorf("Destination = %q, %t", got, ok)
			}
		})
	}
}
