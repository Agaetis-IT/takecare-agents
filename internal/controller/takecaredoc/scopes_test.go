// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package takecaredoc

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const shopAPI = v1alpha1.EntityKey("service/shop-api")

// The three kinds of overlap of ADR 0015 §5.
func TestSharedScope(t *testing.T) {
	docOf := func(namespaces []v1alpha1.Namespace, entities ...v1alpha1.EntityKey) *v1alpha1.TakeCareDoc {
		d := &v1alpha1.TakeCareDoc{}
		d.Spec.Scope = v1alpha1.Scope{Namespaces: namespaces, Entities: entities}
		return d
	}
	inShop := map[v1alpha1.EntityKey][]v1alpha1.Namespace{shopAPI: {scope}}
	for _, tc := range []struct {
		name string
		a, b *v1alpha1.TakeCareDoc
		aNS  map[v1alpha1.EntityKey][]v1alpha1.Namespace
		want []string
	}{
		{"a shared namespace", docOf([]v1alpha1.Namespace{scope, billingNS}), docOf([]v1alpha1.Namespace{billingNS}), nil, []string{"namespace billing"}},
		{"a shared entity", docOf(nil, shopAPI), docOf(nil, shopAPI, "service/other"), nil, []string{"entity service/shop-api"}},
		{"an entity in the other's namespace", docOf(nil, shopAPI), docOf([]v1alpha1.Namespace{scope}), inShop, []string{"entity service/shop-api in namespace shop"}},
		{"disjoint", docOf([]v1alpha1.Namespace{billingNS}, shopAPI), docOf([]v1alpha1.Namespace{"ops"}), inShop, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sharedScope(tc.a, tc.b, tc.aNS, nil)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("shared (-want +got):\n%s", diff)
			}
			if back := sharedScope(tc.b, tc.a, nil, tc.aNS); !cmp.Equal(back, got) {
				t.Errorf("not symmetric: %v", back)
			}
		})
	}
}

// ADR 0015 §5: a later Doc whose entity lies, in the Map, in an earlier
// Doc's namespace is ScopeOverlap and runs nothing; the earlier one runs;
// a disjoint scope lifts it.
func TestScopeOverlap(t *testing.T) {
	f := start(t)
	f.maps.url.Store(startMap(t, map[string]string{
		string(shopAPI): "provenance: declared\nstatus: validated\nsources:\n  - kubernetes: shop/Deployment/shop-api\n",
	}))
	f.platform(t)
	f.connector(t, k8sConnector, []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}, read("pods_list"), true)
	f.connector(t, "prometheus", []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}, read("execute_query"), true)
	f.connector(t, "report", []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}, nil, true)
	f.connector(t, mapConnector, []v1alpha1.ConnectorType{v1alpha1.ConnectorMap}, read("blast_radius", "entry", "map_at", "neighbours", "path"), true)
	f.setMapRevision(t, "rev1")
	f.doc(t, "doc", nil)
	f.waitAgent(t, "doc")
	f.doc(t, "second", func(d *v1alpha1.TakeCareDoc) {
		d.Spec.Scope = v1alpha1.Scope{Entities: []v1alpha1.EntityKey{shopAPI}}
		d.Spec.Connectors.Map = []string{mapConnector}
	})
	second := f.waitCondition(t, "second", v1alpha1.ConditionReady, metav1.ConditionFalse, v1alpha1.ReasonScopeOverlap)
	if msg := meta.FindStatusCondition(second.Status.Conditions, v1alpha1.ConditionReady).Message; !strings.Contains(msg, "TakeCareDoc doc, which came first (entity service/shop-api in namespace shop)") {
		t.Errorf("message = %q", msg)
	}
	if _, err := f.get(agentGVK, engineNS, "takecare-doc-second"); err == nil {
		t.Error("the later Doc gets an agent")
	}
	eventually(t, "one ScopeOverlap event", func() (bool, string) {
		e := f.events(t, v1alpha1.ReasonScopeOverlap)
		return len(e) == 1, strings.Join(e, " | ")
	})
	if d := f.getDoc(t, "doc"); meta.FindStatusCondition(d.Status.Conditions, v1alpha1.ConditionReady).Reason == v1alpha1.ReasonScopeOverlap {
		t.Errorf("the earlier Doc is flagged: %s", describe(d))
	}

	// A disjoint scope: the billing namespace, read by its own connector.
	f.connector(t, "billing-k8s", []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}, read("pods_list"), true)
	var c v1alpha1.TakeCareConnector
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "billing-k8s"}, &c); err != nil {
		t.Fatal(err)
	}
	c.Spec.Kubernetes.Namespaces = []v1alpha1.Namespace{billingNS}
	if err := f.c.Update(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	f.updateDoc(t, "second", func(d *v1alpha1.TakeCareDoc) {
		d.Spec.Scope = v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{billingNS}}
		d.Spec.Connectors.Cluster = []string{"billing-k8s"}
	})
	f.waitAgent(t, "second")
	if d := f.getDoc(t, "second"); meta.FindStatusCondition(d.Status.Conditions, v1alpha1.ConditionReady).Reason == v1alpha1.ReasonScopeOverlap {
		t.Errorf("a disjoint scope is still flagged: %s", describe(d))
	}
}

// waitAgent waits for the Doc's agent: the Doc runs (the engine's
// acceptance is not simulated here).
func (f *fixture) waitAgent(t *testing.T, doc string) {
	t.Helper()
	eventually(t, "the agent of "+doc, func() (bool, string) {
		_, err := f.get(agentGVK, engineNS, "takecare-doc-"+doc)
		return err == nil, describe(f.getDoc(t, doc))
	})
}
