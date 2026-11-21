// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
)

// The route's state follows the gateway's status and the last completed
// survey's canary line; surveys completed in the same second all count, a
// problem in any of them wins, then a proof.
func TestRulesRouteState(t *testing.T) {
	at := func(sec int) *metav1.Time { m := metav1.NewTime(time.Unix(int64(1_800_000_000+sec), 0)); return &m }
	survey := func(name, intendant string, phase v1alpha1.RunPhase, done *metav1.Time, reason string) *v1alpha1.TakeCareSurvey {
		s := &v1alpha1.TakeCareSurvey{Name: name, Namespace: ns}
		s.Spec.IntendantRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: intendant}
		s.Status.Phase, s.Status.CompletedAt = phase, done
		l := v1alpha1.SurveyLayer{Name: discovery.LayerAlerting}
		if reason != "" {
			l.NotSeen = []v1alpha1.NotSeen{{Ref: connMetrics, Reason: reason}}
		}
		s.Status.Layers = []v1alpha1.SurveyLayer{l}
		return s
	}
	const statusProblem = "the argument rule on prometheus's route is not enforced yet"
	closed := func(survey, reason string) rulesRoute {
		return rulesRoute{problem: "survey " + survey + ": " + reason, canary: true}
	}
	for _, tc := range []struct {
		name     string
		statusOK bool
		surveys  []*v1alpha1.TakeCareSurvey
		want     rulesRoute
	}{
		{"the status refuses: closed", false, nil, rulesRoute{problem: statusProblem}},
		{"a new Intendant, no survey completed: open", true, []*v1alpha1.TakeCareSurvey{survey("a", "i", v1alpha1.RunRunning, nil, "")}, rulesRoute{open: true}},
		{"let through, then refused on the open route: open", true, []*v1alpha1.TakeCareSurvey{
			survey("b", "i", v1alpha1.RunCompleted, at(1), letThrough), survey("a", "i", v1alpha1.RunCompleted, at(2), ""),
		}, rulesRoute{open: true}},
		{"let through: closed", true, []*v1alpha1.TakeCareSurvey{survey("a", "i", v1alpha1.RunCompleted, at(1), letThrough)}, closed("a", letThrough)},
		{"refused, then unproven: closed", true, []*v1alpha1.TakeCareSurvey{
			survey("a", "i", v1alpha1.RunCompleted, at(1), ""), survey("b", "i", v1alpha1.RunCompleted, at(2), notChecked),
		}, closed("b", notChecked)},
		{"refused by the tool list: closed", true, []*v1alpha1.TakeCareSurvey{survey("a", "i", v1alpha1.RunCompleted, at(1), canaryClosed)}, closed("a", canaryClosed)},
		{"closed, then proven: open, no problem", true, []*v1alpha1.TakeCareSurvey{
			survey("a", "i", v1alpha1.RunCompleted, at(1), canaryClosed), survey("b", "i", v1alpha1.RunCompleted, at(2), proven),
		}, rulesRoute{open: true, canary: true}},
		{"the same second: the problem wins over the proof", true, []*v1alpha1.TakeCareSurvey{
			survey("a", "i", v1alpha1.RunCompleted, at(3), proven), survey("z", "i", v1alpha1.RunCompleted, at(3), letThrough),
		}, closed("z", letThrough)},
		{"blocked by the status: the status read reports it", true, []*v1alpha1.TakeCareSurvey{
			survey("a", "i", v1alpha1.RunCompleted, at(1), notEnforced),
		}, rulesRoute{open: true}},
		{"another Intendant's, or a failed survey", true, []*v1alpha1.TakeCareSurvey{
			survey("a", "other", v1alpha1.RunCompleted, at(5), letThrough), survey("b", "i", v1alpha1.RunFailed, at(6), letThrough),
			survey("c", "i", v1alpha1.RunCompleted, at(1), ""),
		}, rulesRoute{open: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := v1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			objs := make([]client.Object, 0, len(tc.surveys))
			for _, s := range tc.surveys {
				objs = append(objs, s)
			}
			// The reconcile reads through the cache's index, the runner
			// lists the namespace live: both give the same record.
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&v1alpha1.TakeCareSurvey{}).
				WithIndex(&v1alpha1.TakeCareSurvey{}, indexIntendant, intendantOf).Build()
			for _, indexed := range []bool{true, false} {
				last, err := lastCanary(t.Context(), c, &v1alpha1.TakeCareIntendant{Name: "i", Namespace: ns}, connMetrics, indexed)
				if err != nil {
					t.Fatal(err)
				}
				if got := rulesRouteState(tc.statusOK, statusProblem, last); got != tc.want {
					t.Errorf("indexed %v: rulesRouteState = %+v, want %+v", indexed, got, tc.want)
				}
			}
		})
	}
}
