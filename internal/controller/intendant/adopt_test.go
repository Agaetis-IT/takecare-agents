// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// waitSurvey returns the survey once check passes on it.
func (f *fixture) waitSurvey(t *testing.T, name string, check func(*v1alpha1.TakeCareSurvey) bool) *v1alpha1.TakeCareSurvey {
	t.Helper()
	var s v1alpha1.TakeCareSurvey
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &s); err == nil && check(&s) {
			return &s
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("survey %s: phase %q, reason %q, owners %v", name, s.Status.Phase, s.Status.Reason, s.OwnerReferences)
	return nil
}

// handMade creates a survey as a person would: no owner, no label.
func (f *fixture) handMade(t *testing.T, name, intendant string) {
	t.Helper()
	s := &v1alpha1.TakeCareSurvey{Name: name, Namespace: ns}
	s.Spec.IntendantRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: intendant}
	s.Spec.Trigger = v1alpha1.TriggerSchedule
	if err := f.c.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

// A survey made by hand is given its Intendant as controller owner when
// it is admitted, set once and never moved, so that it goes with its
// Intendant; a survey whose Intendant does not exist or is being deleted
// ends Failed, naming it, never left Queued (spec 10 §4).
func TestHandMadeSurveyAdmission(t *testing.T) {
	t.Parallel()
	f := startRunner(t)

	t.Run("owned by its Intendant once admitted", func(t *testing.T) {
		f.intendant(t, "owner", nil)
		var in v1alpha1.TakeCareIntendant
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "owner"}, &in); err != nil {
			t.Fatal(err)
		}
		f.handMade(t, "by-hand", "owner")
		s := f.waitSurvey(t, "by-hand", func(s *v1alpha1.TakeCareSurvey) bool { return metav1.GetControllerOf(s) != nil })
		ref := metav1.GetControllerOf(s)
		if ref.Kind != v1alpha1.KindTakeCareIntendant || ref.Name != "owner" || ref.UID != in.UID || ref.BlockOwnerDeletion == nil || !*ref.BlockOwnerDeletion {
			t.Errorf("owner reference %+v, want the Intendant owner as controller", ref)
		}
	})

	t.Run("an existing controller reference is never moved", func(t *testing.T) {
		f.intendant(t, "first", nil)
		f.intendant(t, "second", nil)
		var first v1alpha1.TakeCareIntendant
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "first"}, &first); err != nil {
			t.Fatal(err)
		}
		s := &v1alpha1.TakeCareSurvey{Name: "kept-owner", Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&first, v1alpha1.GroupVersion.WithKind(v1alpha1.KindTakeCareIntendant))}}
		s.Spec.IntendantRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: "second"}
		s.Spec.Trigger = v1alpha1.TriggerSchedule
		if err := f.c.Create(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		got := f.waitSurvey(t, "kept-owner", func(s *v1alpha1.TakeCareSurvey) bool {
			return s.Status.Phase != "" && s.Status.Phase != v1alpha1.RunQueued
		})
		if owners := got.OwnerReferences; len(owners) != 1 || owners[0].UID != first.UID {
			t.Errorf("owners %v, want the first Intendant's only", owners)
		}
	})

	t.Run("an Intendant that does not exist", func(t *testing.T) {
		f.handMade(t, "orphan", "nobody")
		s := f.waitSurvey(t, "orphan", func(s *v1alpha1.TakeCareSurvey) bool { return s.Status.Phase == v1alpha1.RunFailed })
		if s.Status.Reason != "the Intendant nobody does not exist" || s.Status.CompletedAt == nil || metav1.GetControllerOf(s) != nil {
			t.Errorf("reason %q, completedAt %v, owner %v", s.Status.Reason, s.Status.CompletedAt, metav1.GetControllerOf(s))
		}
	})

	t.Run("an Intendant being deleted", func(t *testing.T) {
		f.intendant(t, "leaving", func(in *v1alpha1.TakeCareIntendant) { in.Finalizers = []string{"test.takecare/hold"} })
		if err := f.c.Delete(context.Background(), &v1alpha1.TakeCareIntendant{Name: "leaving", Namespace: ns}); err != nil {
			t.Fatal(err)
		}
		f.handMade(t, "too-late", "leaving")
		s := f.waitSurvey(t, "too-late", func(s *v1alpha1.TakeCareSurvey) bool { return s.Status.Phase == v1alpha1.RunFailed })
		if s.Status.Reason != "the Intendant leaving is being deleted" {
			t.Errorf("reason %q", s.Status.Reason)
		}
		f.update(t, "leaving", func(in *v1alpha1.TakeCareIntendant) { in.Finalizers = nil })
	})
}
