// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package takecaredoc

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// The accuracy bench sets the Doc's cooldown to 1 s for its runs (spec 04):
// the cooldown is the alert intake's, never part of the agent's rendering.
// The patch reconciles the Doc once, with the same rendered agent, and its
// EngineReady never leaves True, so the next investigation is never held.
func TestCooldownChangeKeepsTheAgent(t *testing.T) {
	f := start(t)
	f.platform(t)
	f.connector(t, k8sConnector, []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}, read("pods_list", podsLog), true)
	f.connector(t, "prometheus", []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}, read("execute_query"), true)
	f.connector(t, "report", []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}, nil, true)
	f.doc(t, "doc", nil)
	f.waitCondition(t, "doc", v1alpha1.ConditionEngineReady, metav1.ConditionFalse, "AgentAcceptedPending")
	f.agentReady(t)
	f.answer.Store(true)
	f.waitCondition(t, "doc", v1alpha1.ConditionEngineReady, metav1.ConditionTrue, "")
	before, err := f.get(agentGVK, engineNS, agent)
	if err != nil {
		t.Fatal(err)
	}

	d := f.getDoc(t, "doc")
	patch := client.MergeFrom(d.DeepCopy())
	d.Spec.Investigation.Cooldown = &metav1.Duration{Duration: time.Second}
	if err := f.c.Patch(context.Background(), d, patch); err != nil {
		t.Fatal(err)
	}
	generation := d.Generation
	// Until the reconcile of the new generation, and a while after it.
	deadline := time.Now().Add(20 * time.Second)
	seen := false
	var after time.Time
	for time.Now().Before(deadline) && (!seen || time.Now().Before(after)) {
		cur := f.getDoc(t, "doc")
		if c := meta.FindStatusCondition(cur.Status.Conditions, v1alpha1.ConditionEngineReady); c == nil || c.Status != metav1.ConditionTrue {
			t.Fatalf("EngineReady left True after a cooldown change: %+v", c)
		}
		if !seen && cur.Status.ObservedGeneration == generation {
			seen, after = true, time.Now().Add(2*time.Second)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !seen {
		t.Fatalf("the Doc's generation %d was never reconciled", generation)
	}
	got, err := f.get(agentGVK, engineNS, agent)
	if err != nil {
		t.Fatal(err)
	}
	if got.GetResourceVersion() != before.GetResourceVersion() || got.GetAnnotations()["takecare.agaetis.fr/render-hash"] != before.GetAnnotations()["takecare.agaetis.fr/render-hash"] {
		t.Errorf("the agent changed: resourceVersion %s → %s", before.GetResourceVersion(), got.GetResourceVersion())
	}
}
