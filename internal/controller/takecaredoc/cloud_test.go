// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package takecaredoc

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const (
	cloudShop    = "azure"
	cloudPay     = "azure-pay"
	tenant       = "00000000-0000-0000-0000-000000000000"
	subShop      = "11111111-1111-1111-1111-111111111111"
	subPay       = "22222222-2222-2222-2222-222222222222"
	vmBillingKey = v1alpha1.EntityKey("compute/vm-billing")
	vmBilling    = "/subscriptions/" + subShop + "/resourceGroups/rg-billing/providers/Microsoft.Compute/virtualMachines/vm-billing"
)

// cloudConnector is a cloud connector whose reach is one subscription,
// narrowed to groups when some are named.
func (f *fixture) cloudConnector(t *testing.T, name, sub string, groups []v1alpha1.ResourceGroupName, tools []v1alpha1.ToolStatus, ready bool) {
	t.Helper()
	c := &v1alpha1.TakeCareConnector{Name: name, Namespace: ns}
	c.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorCloud}
	c.Spec.MCP.Addon = name
	c.Spec.Reach = &v1alpha1.Reach{Azure: &v1alpha1.AzureReach{TenantID: tenant,
		Subscriptions: []v1alpha1.AzureSubscription{{ID: sub, ResourceGroups: groups}}}}
	if err := f.c.Create(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	f.setConnector(t, name, tools, ready)
}

// ADR 0021 §6, spec 08 §7: an entity whose Map source is an ARM ID is
// covered when the reach of a cloud connector given to the agent holds it;
// otherwise the reason names the resource. A connector bound as cloud
// that does not declare the type is a misconfiguration.
func TestCloudCoverage(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.maps.url.Store(startMap(t, map[string]string{
		"compute/vm-web":     "provenance: declared\nstatus: validated\nsources:\n  - azure: /subscriptions/" + strings.ToUpper(subShop) + "/resourceGroups/RG-Shop/providers/Microsoft.Compute/virtualMachines/vm-web\n",
		string(vmBillingKey): "provenance: declared\nstatus: validated\nsources:\n  - azure: " + vmBilling + "\n",
		"datastore/sql-pay":  "provenance: declared\nstatus: validated\nsources:\n  - azure: /subscriptions/" + subPay + "/resourceGroups/rg-pay/providers/Microsoft.Sql/servers/sql-pay\n",
	}))
	f.platform(t)
	f.cloudConnector(t, cloudShop, subShop, []v1alpha1.ResourceGroupName{"rg-shop"}, read("resources_list"), true)
	f.cloudConnector(t, cloudPay, subPay, nil, nil, false)
	f.connector(t, mapConnector, []v1alpha1.ConnectorType{v1alpha1.ConnectorMap}, read("blast_radius", "entry", "map_at", "neighbours", "path"), true)
	f.setMapRevision(t, "rev1")
	f.doc(t, "doc", func(d *v1alpha1.TakeCareDoc) {
		d.Spec.Level = v1alpha1.LevelApprentice
		d.Spec.Scope = v1alpha1.Scope{Entities: []v1alpha1.EntityKey{"compute/vm-web", vmBillingKey, "datastore/sql-pay"}}
		d.Spec.Connectors = v1alpha1.DocConnectors{Cloud: []string{cloudShop, cloudPay}, Map: []string{mapConnector}}
	})

	want := "compute/vm-billing=NoAccess datastore/sql-pay=ConnectorNotReady"
	eventually(t, "the entity in the reach covered, the others their reason", func() (bool, string) {
		got := blindSpots(f.getDoc(t, "doc"))
		return got == want, got + "; " + describe(f.getDoc(t, "doc"))
	})
	for _, s := range f.getDoc(t, "doc").Status.BlindSpots {
		if s.Entity == vmBillingKey && s.Message != "The entity `compute/vm-billing`: its azure resource `"+vmBilling+"`: no connector reads it." {
			t.Errorf("message %q", s.Message)
		}
	}
	eventually(t, "the cloud connector is given, the instructions say what it reads", func() (bool, string) {
		if tools := f.agentTools(t)[agent+"-"+cloudShop]; strings.Join(tools, ",") != "resources_list" {
			return false, strings.Join(mapKeys(f.agentTools(t)), ",")
		}
		cm, err := f.get(schema.GroupVersionKind{Version: "v1", Kind: kindConfigMap}, engineNS, agent+"-instructions")
		if err != nil {
			return false, err.Error()
		}
		text, _, _ := unstructured.NestedString(cm.Object, "data", "instructions.md")
		return strings.Contains(text, "The cloud provider's control plane") && strings.Contains(text, "The entity `compute/vm-billing`: its azure resource"), text
	})

	// A cluster connector bound in the cloud slot.
	f.connector(t, k8sConnector, []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}, read("pods_list"), true)
	f.updateDoc(t, "doc", func(d *v1alpha1.TakeCareDoc) { d.Spec.Connectors.Cloud = append(d.Spec.Connectors.Cloud, k8sConnector) })
	d := f.waitCondition(t, "doc", v1alpha1.ConditionConnectorsReady, metav1.ConditionFalse, ReasonConnectorMissing)
	if msg := describe(d); !strings.Contains(msg, "connector kubernetes is bound as cloud but does not declare that type") {
		t.Errorf("conditions %s", msg)
	}
}
