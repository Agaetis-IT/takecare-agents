// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp/mcptest"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

func metricsSpec() v1alpha1.TakeCareConnectorSpec {
	return v1alpha1.TakeCareConnectorSpec{
		Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics},
		MCP:   v1alpha1.MCPSpec{URL: "https://metrics.example.com/mcp"},
	}
}

func reportSpec() v1alpha1.TakeCareConnectorSpec {
	return v1alpha1.TakeCareConnectorSpec{
		Types:  []v1alpha1.ConnectorType{v1alpha1.ConnectorReport},
		MCP:    v1alpha1.MCPSpec{URL: "https://report.example.com/mcp"},
		Report: &v1alpha1.ReportSpec{Adapter: "log", Destinations: []v1alpha1.Destination{{Name: "on-call", Target: "log"}}},
	}
}

// noBackend says the connector has no gateway backend.
func (h *harness) noBackend(name string) bool {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(backendGVK)
	return apierrors.IsNotFound(h.c.Get(h.ctx, client.ObjectKey{Namespace: operatorNS, Name: "mcp-" + name}, obj))
}

// A connector of another type than report, stored with a name past the
// bound before the rule: BackendAccepted False with the rule's sentence,
// no backend, no Create refused at every reconcile; it is deleted as any
// connector (it holds no finalizer).
func TestStoredConnectorNameTooLong(t *testing.T) {
	t.Parallel()
	h := start(t, mcptest.Start(t, mcptest.ToolSpec{Name: "query", ReadOnly: true}))
	name := strings.Repeat("m", v1alpha1.MaxConnectorName+1)
	stored := &v1alpha1.TakeCareConnector{Name: name, Namespace: operatorNS, Spec: metricsSpec()}
	probe := stored.DeepCopy()
	probe.Name = strings.Repeat("p", v1alpha1.MaxConnectorName+1)
	testenv.StoreUnvalidated(t, h.cfg, "takecareconnectors.takecare.agaetis.fr",
		func() error { return h.c.Create(h.ctx, stored.DeepCopy()) },
		func() error { return h.c.Create(h.ctx, probe.DeepCopy(), client.DryRunAll) })
	c := h.waitCondition(name, ConditionBackendAccepted, metav1.ConditionFalse, v1alpha1.ReasonNameTooLong)
	if c.Message != v1alpha1.MessageConnectorName || !h.noBackend(name) {
		t.Errorf("BackendAccepted %+v, no backend %v", c, h.noBackend(name))
	}
	h.waitCondition(name, v1alpha1.ConditionReady, metav1.ConditionFalse, "")
	h.must(h.c.Delete(h.ctx, h.get(name)))
	h.eventually("the stored connector deleted", func() bool { return h.get(name) == nil })
}

// A report-only connector keeps a longer name and works. A report
// connector has no other type (an API rule), but its type can be replaced
// later, which the create-only name rule lets through: it then says why it
// gets no backend.
func TestLongReportConnectorRetyped(t *testing.T) {
	t.Parallel()
	h := start(t, mcptest.Start(t, mcptest.ToolSpec{Name: "query", ReadOnly: true}))
	name := strings.Repeat("r", v1alpha1.MaxConnectorName+1)
	h.connector(name, reportSpec())
	h.waitCondition(name, v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
	// On a fresh read, again on a conflict: the controller writes the
	// connector's status meanwhile.
	h.must(retry.RetryOnConflict(retry.DefaultRetry, func() error {
		c := h.get(name)
		c.Spec.Types, c.Spec.Report = []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}, nil
		return h.c.Update(h.ctx, c)
	}))
	if got := h.waitCondition(name, ConditionBackendAccepted, metav1.ConditionFalse, v1alpha1.ReasonNameTooLong); got.Message != v1alpha1.MessageConnectorName || !h.noBackend(name) {
		t.Errorf("BackendAccepted %+v, no backend %v", got, h.noBackend(name))
	}
}
