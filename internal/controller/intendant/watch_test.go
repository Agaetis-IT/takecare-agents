// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// agentObj is a kagent Agent as the watch sees it: its generation, its
// status' observed generation and its conditions.
func agentObj(gen, observed int64, conditions ...map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"observedGeneration": observed}}}
	u.SetAPIVersion("kagent.dev/v1alpha2")
	u.SetKind("Agent")
	u.SetName("takecare-intendant-intendant")
	u.SetGeneration(gen)
	cs := make([]any, 0, len(conditions))
	for _, c := range conditions {
		cs = append(cs, c)
	}
	_ = unstructured.SetNestedSlice(u.Object, cs, "status", "conditions")
	return u
}

func readyCond(typ, status string) map[string]any {
	return map[string]any{"type": typ, "status": status}
}

// N3: the Intendant's EngineReady follows its agent. kagent flips the
// agent's Ready condition without a new generation, which the former
// generation-only predicate dropped; the watch passes it now, and passes
// what the settled digest needs (an observed generation catching up).
func TestEngineWatchPassesReadyFlips(t *testing.T) {
	for _, tc := range []struct {
		name      string
		old, new  *unstructured.Unstructured
		want      bool
		wantGenOn bool // what the former generation-only predicate said
	}{
		{"Ready flips False to True, same generation", agentObj(2, 2, readyCond("Ready", "False")), agentObj(2, 2, readyCond("Ready", "True")), true, false},
		{"Ready flips True to False, same generation", agentObj(2, 2, readyCond("Ready", "True")), agentObj(2, 2, readyCond("Ready", "False")), true, false},
		{"the status observes the generation", agentObj(3, 2, readyCond("Ready", "True")), agentObj(3, 3, readyCond("Ready", "True")), true, false},
		{"a new generation", agentObj(2, 2, readyCond("Ready", "True")), agentObj(3, 2, readyCond("Ready", "True")), true, true},
		{"another condition changes", agentObj(2, 2, readyCond("Ready", "True"), readyCond("Accepted", "False")), agentObj(2, 2, readyCond("Ready", "True"), readyCond("Accepted", "True")), false, false},
		{"nothing the Intendant reads changes", agentObj(2, 2, readyCond("Ready", "True")), agentObj(2, 2, readyCond("Ready", "True")), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := event.UpdateEvent{ObjectOld: tc.old, ObjectNew: tc.new}
			if got := engineWatch.Update(e); got != tc.want {
				t.Errorf("engineWatch passes %t, want %t", got, tc.want)
			}
			if got := (predicate.GenerationChangedPredicate{}).Update(e); got != tc.wantGenOn {
				t.Errorf("the generation-only predicate passes %t, want %t (the fault N3 fixes)", got, tc.wantGenOn)
			}
		})
	}
}

// When the quiet period and the owed pass's wait both hold a trigger, the
// reconcile comes back at the earlier of the two.
func TestEarliestWakeUp(t *testing.T) {
	t0 := time.Date(2026, 11, 19, 19, 40, 0, 0, time.UTC)
	quiet, ready := t0.Add(60*time.Second), t0.Add(20*time.Second)
	for _, tc := range []struct {
		name string
		a, b time.Time
		want time.Time
	}{
		{"none", time.Time{}, time.Time{}, time.Time{}},
		{"the quiet period only", quiet, time.Time{}, quiet},
		{"the owed pass only", time.Time{}, ready, ready},
		{"both wait: the earlier", quiet, ready, ready},
		{"both wait, the other order", ready, quiet, ready},
	} {
		if got := earliest(tc.a, tc.b); !got.Equal(tc.want) {
			t.Errorf("%s: earliest = %v, want %v", tc.name, got, tc.want)
		}
	}
}
