// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package doc

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

// Namespaces the tests use.
const (
	appsNS     = "demo-apps"
	operatorNS = "takecare-system"
	engineNS   = "kagent"

	podNotReady     = "PodNotReady"
	podCrashLooping = "PodCrashLooping"
	onCall          = "on-call"
)

func investigation() *v1alpha1.TakeCareInvestigation {
	inv := &v1alpha1.TakeCareInvestigation{Name: "doc-3fa29c1b07-a1b2c3", Namespace: operatorNS}
	inv.Spec.Alert = v1alpha1.AlertRecord{
		Name: "HighErrorRate", Service: "billing-api", Namespace: appsNS,
		Annotations: map[string]string{"description": "More than 2 % of billing-api requests have answered 504 for 30 seconds."},
	}
	return inv
}

func TestMessage(t *testing.T) {
	grouped := investigation()
	grouped.Spec.Alert = v1alpha1.AlertRecord{Name: podCrashLooping, Namespace: appsNS, Workload: "notifications-worker",
		Annotations: map[string]string{"summary": "notifications-worker restarts."}}
	grouped.Status.Alerts = []v1alpha1.GroupedAlert{{Name: podNotReady, Fingerprint: "5m0ke00000000002", FiredAt: metav1.NewTime(time.Date(2026, 10, 16, 3, 0, 12, 0, time.UTC))}}
	bare := investigation()
	bare.Spec.Alert = v1alpha1.AlertRecord{Name: "TargetDown", Namespace: appsNS}
	// ADR 0015 §4, ADR 0016 §5: the Map entry (the demo's shape), and the
	// entry on a named cluster.
	entity := investigation()
	entity.Spec.Alert.Entity = "service/billing-api"
	onEast := investigation()
	onEast.Spec.Alert.Entity, onEast.Spec.Alert.Cluster = "service/billing-api", "east"
	for name, inv := range map[string]*v1alpha1.TakeCareInvestigation{
		"message-alert.txt":   investigation(),
		"message-grouped.txt": grouped,
		"message-bare.txt":    bare,
		"message-entity.txt":  entity,
		"message-cluster.txt": onEast,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Message(inv)
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, name, []byte(got))
		})
	}
}

func TestRunNamesTheAgentNotTheAlert(t *testing.T) {
	d := &v1alpha1.TakeCareDoc{Name: Kind, Namespace: operatorNS}
	r, err := Run(d, investigation(), engineNS, "http://agentgateway-proxy.takecare-system.svc")
	if err != nil {
		t.Fatal(err)
	}
	if r.Agent != "takecare-doc-doc" || r.Namespace != engineNS || r.ConversationID != "doc-3fa29c1b07-a1b2c3" || r.GatewayURL == "" {
		t.Errorf("run = %+v", r)
	}
}
