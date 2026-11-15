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

// The condition follows the last completed survey's canary; surveys
// completed in the same second all count, a problem in any of them wins.
func TestLastCanary(t *testing.T) {
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
	for _, tc := range []struct {
		name    string
		surveys []*v1alpha1.TakeCareSurvey
		want    string
	}{
		{"no survey completed", []*v1alpha1.TakeCareSurvey{survey("a", "i", v1alpha1.RunRunning, nil, "")}, ""},
		{"let through, then refused", []*v1alpha1.TakeCareSurvey{
			survey("b", "i", v1alpha1.RunCompleted, at(1), letThrough), survey("a", "i", v1alpha1.RunCompleted, at(2), ""),
		}, ""},
		{"refused, then unproven", []*v1alpha1.TakeCareSurvey{
			survey("a", "i", v1alpha1.RunCompleted, at(1), ""), survey("b", "i", v1alpha1.RunCompleted, at(2), notChecked),
		}, "survey b: " + notChecked},
		{"the same second: the problem wins", []*v1alpha1.TakeCareSurvey{
			survey("a", "i", v1alpha1.RunCompleted, at(3), letThrough), survey("z", "i", v1alpha1.RunCompleted, at(3), ""),
		}, "survey a: " + letThrough},
		{"blocked by the status: the status read reports it", []*v1alpha1.TakeCareSurvey{
			survey("a", "i", v1alpha1.RunCompleted, at(1), notEnforced),
		}, ""},
		{"another Intendant's, or a failed survey", []*v1alpha1.TakeCareSurvey{
			survey("a", "other", v1alpha1.RunCompleted, at(5), letThrough), survey("b", "i", v1alpha1.RunFailed, at(6), letThrough),
			survey("c", "i", v1alpha1.RunCompleted, at(1), ""),
		}, ""},
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
			r := &Reconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&v1alpha1.TakeCareSurvey{}).Build()}
			got, err := r.lastCanary(t.Context(), &v1alpha1.TakeCareIntendant{Name: "i", Namespace: ns}, connMetrics)
			if err != nil || got != tc.want {
				t.Errorf("lastCanary = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
