// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package takecaredoc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// ADR 0014 §1-2, spec 02 AC 6: readiness by coverage of the scope.
func TestCoverage(t *testing.T) {
	f := start(t)
	f.platform(t)
	cluster := []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}
	f.connector(t, k8sConnector, cluster, read("pods_list"), true)
	f.billingConnector(t)
	f.connector(t, "report", []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}, nil, true)

	// shop is read; billing's connector was never discovered; nothing
	// declares payments; no metrics connector.
	f.doc(t, "doc", func(d *v1alpha1.TakeCareDoc) {
		d.Spec.Scope.Namespaces = []v1alpha1.Namespace{scope, "payments", billingNS}
		d.Spec.Connectors.Cluster = []string{k8sConnector, billingConnector}
		d.Spec.Connectors.Metrics = nil
	})
	d := f.waitCondition(t, "doc", ConditionDegraded, metav1.ConditionTrue, v1alpha1.ReasonBlindSpots)
	got := make([]string, 0, len(d.Status.BlindSpots))
	for _, s := range d.Status.BlindSpots {
		got = append(got, fmt.Sprintf("%s=%s", s.Entity, s.Reason))
	}
	if strings.Join(got, " ") != "environment/billing=ConnectorNotReady environment/payments=NoAccess" {
		t.Errorf("blind spots = %v", got)
	}
	degraded := meta.FindStatusCondition(d.Status.Conditions, ConditionDegraded)
	if !strings.Contains(degraded.Message, "no metrics connector (recommended)") || !strings.Contains(degraded.Message, "`payments` (environment/payments): no connector reads it") {
		t.Errorf("Degraded = %s", degraded.Message)
	}
	if c := meta.FindStatusCondition(d.Status.Conditions, v1alpha1.ConditionConnectorsReady); c.Reason != ReasonConnectorNotReady {
		t.Errorf("ConnectorsReady = %s %s: a never-discovered connector does not block a Doc that covers something", c.Reason, c.Message)
	}
	eventually(t, "the agent runs on what is covered and its instructions name the blind spots", func() (bool, string) {
		cm, err := f.get(schema.GroupVersionKind{Version: "v1", Kind: kindConfigMap}, engineNS, agent+"-instructions")
		if err != nil {
			return false, err.Error()
		}
		text, _, _ := unstructured.NestedString(cm.Object, "data", "instructions.md")
		return strings.Contains(text, "# What you cannot check") && strings.Contains(text, "environment/payments") &&
			strings.Contains(text, "limited to the namespaces `shop`."), text
	})
	eventually(t, "the blind spots are said once", func() (bool, string) {
		e := f.events(t, ReasonBlindSpotsChanged)
		return len(e) == 1 && strings.Contains(e[0], "environment/billing"), strings.Join(e, " | ")
	})

	// Nothing covered: blocking, and the agent goes (nothing may come).
	d = f.getDoc(t, "doc")
	d.Spec.Scope.Namespaces = []v1alpha1.Namespace{"payments"}
	if err := f.c.Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	d = f.waitCondition(t, "doc", v1alpha1.ConditionConnectorsReady, metav1.ConditionFalse, v1alpha1.ReasonBlindSpots)
	if !meta.IsStatusConditionFalse(d.Status.Conditions, v1alpha1.ConditionReady) {
		t.Error("a Doc that covers nothing is Ready")
	}
	eventually(t, "the agent is removed", func() (bool, string) {
		_, err := f.get(agentGVK, engineNS, agent)
		return err != nil, "agent still there"
	})
	eventually(t, "the new set is said", func() (bool, string) {
		e := f.events(t, ReasonBlindSpotsChanged)
		return len(e) == 2, strings.Join(e, " | ")
	})

	// Covered again: the blind spots go, and their end is said.
	d = f.getDoc(t, "doc")
	d.Spec.Scope.Namespaces = []v1alpha1.Namespace{scope}
	if err := f.c.Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	eventually(t, "no blind spot left", func() (bool, string) {
		d := f.getDoc(t, "doc")
		e := f.events(t, ReasonBlindSpotsChanged)
		return len(d.Status.BlindSpots) == 0 && len(e) == 3 && strings.Contains(strings.Join(e, " "), "every namespace of the scope is covered"), strings.Join(e, " | ") + "; " + describe(d)
	})
}

// billingConnector is a cluster connector declaring the billing namespace,
// never discovered: it is not given to the agent.
func (f *fixture) billingConnector(t *testing.T) {
	t.Helper()
	f.connector(t, billingConnector, []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}, nil, false)
	var c v1alpha1.TakeCareConnector
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: billingConnector}, &c); err != nil {
		t.Fatal(err)
	}
	c.Spec.Kubernetes.Namespaces = []v1alpha1.Namespace{billingNS}
	if err := f.c.Update(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
}

// remoteCluster is a cluster connector by URL on a named cluster (ADR
// 0016), declaring namespaces, its tools known.
func (f *fixture) remoteCluster(t *testing.T, name string, namespaces ...v1alpha1.Namespace) {
	t.Helper()
	c := &v1alpha1.TakeCareConnector{Name: name, Namespace: ns}
	c.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}
	c.Spec.MCP.URL = "https://" + name + ".example.internal:8443/mcp"
	c.Spec.Kubernetes = &v1alpha1.KubernetesSpec{Namespaces: namespaces}
	c.Spec.Cluster = &v1alpha1.ClusterSpec{Name: name, TLS: v1alpha1.ClusterTLS{
		CASecretRef: v1alpha1.SecretRef{Name: name + "-ca"}, ClientCertSecretRef: v1alpha1.SecretRef{Name: name + "-client"}}}
	if err := f.c.Create(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	f.setConnector(t, name, read("pods_list"), true)
}

// ADR 0016 §5: a namespace of the scope is wanted on every cluster the
// Doc reads; a named cluster's blind spot is keyed environment/<cluster>.<ns>
// and the instructions name cluster/namespace.
func TestCoverageOnNamedClusters(t *testing.T) {
	f := start(t)
	f.platform(t)
	f.connector(t, "report", []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}, nil, true)
	f.remoteCluster(t, "east", scope)
	f.remoteCluster(t, "west", billingNS)
	f.doc(t, "doc", func(d *v1alpha1.TakeCareDoc) {
		d.Spec.Scope.Namespaces = []v1alpha1.Namespace{scope}
		d.Spec.Connectors.Cluster = []string{"east", "west"}
		d.Spec.Connectors.Metrics = nil
	})
	d := f.waitCondition(t, "doc", ConditionDegraded, metav1.ConditionTrue, v1alpha1.ReasonBlindSpots)
	if got := blindSpots(d); got != "environment/west.shop=NoAccess" {
		t.Errorf("blind spots = %s", got)
	}
	if s := d.Status.BlindSpots[0].Message; s != "The namespace `west/shop` (environment/west.shop): no connector reads it." {
		t.Errorf("message %q", s)
	}
	eventually(t, "the instructions name cluster/namespace", func() (bool, string) {
		cm, err := f.get(schema.GroupVersionKind{Version: "v1", Kind: kindConfigMap}, engineNS, agent+"-instructions")
		if err != nil {
			return false, err.Error() + "; " + describe(f.getDoc(t, "doc"))
		}
		text, _, _ := unstructured.NestedString(cm.Object, "data", "instructions.md")
		return strings.Contains(text, "limited to the namespaces `east/shop`, `west/billing`."), text
	})
}

const billingAPI = "service/billing-api"

// ADR 0016 §5: an entity whose Map source names a cluster is covered when
// the Doc's connector of that cluster reads its namespace; otherwise the
// pair's own reason.
func TestEntitiesOnNamedClusters(t *testing.T) {
	f := start(t)
	f.maps.url.Store(startMap(t, map[string]string{
		"environment/east":     "provenance: declared\nstatus: validated\n",
		string(shopAPI):        "provenance: declared\nstatus: validated\nsources:\n  - kubernetes: east/shop/Deployment/shop-api\n",
		billingAPI:             "provenance: declared\nstatus: validated\nsources:\n  - kubernetes: east/billing/Deployment/billing-api\n",
		"service/unnamed-shop": "provenance: declared\nstatus: validated\nsources:\n  - kubernetes: shop/Deployment/unnamed-shop\n",
	}))
	f.platform(t)
	f.connector(t, "report", []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}, nil, true)
	f.remoteCluster(t, "east", scope)
	f.connector(t, mapConnector, []v1alpha1.ConnectorType{v1alpha1.ConnectorMap}, read("blast_radius", "entry", "map_at", "neighbours", "path"), true)
	f.setMapRevision(t, "rev1")
	f.doc(t, "doc", func(d *v1alpha1.TakeCareDoc) {
		d.Spec.Scope = v1alpha1.Scope{Entities: []v1alpha1.EntityKey{shopAPI, billingAPI, "service/unnamed-shop"}}
		d.Spec.Connectors.Cluster = []string{"east"}
		d.Spec.Connectors.Metrics = nil
		d.Spec.Connectors.Map = []string{mapConnector}
	})
	want := "service/billing-api=NoAccess service/unnamed-shop=NoAccess"
	eventually(t, "the entity on east/shop covered, the others the pair's reason", func() (bool, string) {
		got := blindSpots(f.getDoc(t, "doc"))
		return got == want, got
	})
	for _, s := range f.getDoc(t, "doc").Status.BlindSpots {
		if s.Entity == billingAPI && s.Message != "The entity `service/billing-api`: its namespace `east/billing`: no connector reads it." {
			t.Errorf("message %q", s.Message)
		}
	}
}
