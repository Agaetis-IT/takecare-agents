// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package takecaredoc

import (
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/routes"
)

// A connector of two types (spec 05) in two slots of the Doc has one
// route and one tool server, the union of what the slots give, and edits
// of the Doc that change nothing the route depends on leave its policy as
// it is. Regression test: the Doc already behaved so.
const reportName = "report"

func TestConnectorInTwoSlots(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	f.connector(t, "both", []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster, v1alpha1.ConnectorMetrics}, read("pods_list", "execute_query", "get_alerts"), true)
	f.connector(t, reportName, []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}, nil, true)
	f.doc(t, "doc", func(d *v1alpha1.TakeCareDoc) {
		d.Spec.Connectors = v1alpha1.DocConnectors{Cluster: []string{"both"}, Metrics: []string{"both"}, Report: reportName}
	})
	f.waitCondition(t, "doc", v1alpha1.ConditionConnectorsReady, metav1.ConditionTrue, "")
	// The condition alone may be the previous generation's: wait for the
	// reconcile of the current one before reading the policy.
	reconciled := func() {
		t.Helper()
		eventually(t, "the new generation is reconciled", func() (bool, string) {
			d := f.getDoc(t, "doc")
			return d.Status.ObservedGeneration == d.Generation, describe(d)
		})
	}
	reconciled()
	version := func() string {
		obj, err := f.get(routes.PolicyGVK, ns, "role-doc-doc-mcp-both")
		if err != nil {
			t.Fatal(err)
		}
		return obj.GetResourceVersion()
	}
	before := version()
	for i := range 3 {
		f.updateDoc(t, "doc", func(d *v1alpha1.TakeCareDoc) { d.Spec.Report.Practitioner = fmt.Sprintf("d%d", i) })
		reconciled()
		f.waitCondition(t, "doc", v1alpha1.ConditionConnectorsReady, metav1.ConditionTrue, "")
	}
	if after := version(); after != before {
		t.Errorf("the policy's version went from %s to %s over three edits that change no route", before, after)
	}
	if got, want := f.policyOf(t, "both"), `mcp.tool.name in ["execute_query", "get_alerts", "pods_list"]`; got != want {
		t.Errorf("allow-list %s, want %s", got, want)
	}
	if servers := f.agentTools(t); len(servers) != 1 || len(servers["takecare-doc-doc-both"]) != 3 {
		t.Errorf("tool servers %v", servers)
	}
}
