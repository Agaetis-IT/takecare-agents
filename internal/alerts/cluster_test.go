// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package alerts

import (
	"context"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const (
	eastToken = "east-alert-token"
	east      = "east"
)

// clusterConnector registers a workload cluster whose Alertmanager
// presents the token kept in the Secret named secret.
func (f *fixture) clusterConnector(name, secret, token string) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.c.Create(ctx, &corev1.Secret{Name: secret, Namespace: ns, StringData: map[string]string{TokenSecretKey: token}}); err != nil {
		f.t.Fatal(err)
	}
	c := &v1alpha1.TakeCareConnector{Name: name, Namespace: ns}
	c.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}
	c.Spec.MCP.URL = "https://" + name + ".example.internal:8443/mcp"
	c.Spec.Kubernetes = &v1alpha1.KubernetesSpec{Namespaces: []v1alpha1.Namespace{appNS}}
	c.Spec.Cluster = &v1alpha1.ClusterSpec{Name: name, AlertTokenSecretRef: &v1alpha1.SecretRef{Name: secret}, TLS: v1alpha1.ClusterTLS{
		CASecretRef: v1alpha1.SecretRef{Name: name + "-ca"}, ClientCertSecretRef: v1alpha1.SecretRef{Name: name + "-client"}}}
	if err := f.c.Create(ctx, c); err != nil {
		f.t.Fatal(err)
	}
}

// ADR 0016 §5: the token says the alert's cluster; routing, the incident
// key and the blind spots are by (cluster, namespace).
func TestAlertsPerCluster(t *testing.T) {
	f := start(t)
	ctx := context.Background()
	f.clusterConnector(east, "east-alerts", eastToken)
	d := &v1alpha1.TakeCareDoc{Name: east, Namespace: ns}
	d.Spec.Scope.Namespaces = []v1alpha1.Namespace{appNS}
	d.Spec.Connectors.Cluster = []string{east}
	if err := f.c.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-time.Minute).Truncate(time.Second)
	if code := f.post(token, alert(alertNotReady, "c1c2c3c4c5c6c7c8", "billing-api-1", t0, nil)); code != http.StatusAccepted {
		t.Fatalf("operator token: %d", code)
	}
	if code := f.post(eastToken, alert(alertNotReady, "d1d2d3d4d5d6d7d8", "billing-api-1", t0, nil)); code != http.StatusAccepted {
		t.Fatalf("east token: %d", code)
	}
	byDoc := map[string]v1alpha1.TakeCareInvestigation{}
	for _, inv := range f.investigations() {
		byDoc[inv.Spec.RoleRef.Name] = inv
	}
	unnamed, onEast := byDoc[roleDoc], byDoc[east]
	if unnamed.Spec.Alert.Cluster != "" || onEast.Spec.Alert.Cluster != east || onEast.Labels[v1alpha1.LabelAlertCluster] != east {
		t.Fatalf("routed by place: doc %+v, east %+v", unnamed.Spec.Alert, onEast.Spec.Alert)
	}
	if unnamed.Labels[LabelIncident] == onEast.Labels[LabelIncident] {
		t.Error("one workload on two clusters must be two incidents")
	}

	before := len(f.investigations())
	f.post(eastToken, alert(alertNotReady, "e1e2e3e4e5e6e7e8", "billing-api-2", t0, map[string]string{labelCluster: "west"}))
	if n := len(f.investigations()); n != before || f.ignored(ReasonClusterMismatch) != 1 {
		t.Errorf("a contradicting cluster label: %d new, counted %v", n-before, f.ignored(ReasonClusterMismatch))
	}

	d.Status.BlindSpots = []v1alpha1.BlindSpot{{Entity: "environment/east.demo-apps", Reason: v1alpha1.BlindSpotNoAccess}}
	if err := f.c.Status().Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	f.post(eastToken, alert(alertNotReady, "f1f2f3f4f5f6f7f8", "orders-1", t0.Add(time.Second), nil))
	if f.ignored(ReasonOutsideGround) != 1 {
		t.Errorf("outside_ground on environment/east.demo-apps: %v", f.ignored(ReasonOutsideGround))
	}
}

// Two connectors sharing one token value are no identity: 401.
func TestSharedAlertTokenIsRefused(t *testing.T) {
	f := start(t)
	f.clusterConnector(east, "east-alerts", eastToken)
	f.clusterConnector("west", "west-alerts", eastToken)
	if code := f.post(eastToken, alert(alertNotReady, "a9a9a9a9a9a9a9a9", "billing-api-1", time.Now(), nil)); code != http.StatusUnauthorized {
		t.Errorf("shared token: %d, want 401", code)
	}
	if code := f.post("unknown-token", alert(alertNotReady, "b9b9b9b9b9b9b9b9", "billing-api-1", time.Now(), nil)); code != http.StatusUnauthorized {
		t.Errorf("unknown token: %d, want 401", code)
	}
}
