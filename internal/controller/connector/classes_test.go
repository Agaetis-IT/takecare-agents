// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp/mcptest"
)

// TestCustomerToolsAreActUntilListed: a customer's server by url claims a
// read tool; its annotations are not trusted, so the tool is act until
// spec.tools lists it as read (spec 05). An operator upgraded from the
// annotation rule says the change once.
func TestCustomerToolsAreActUntilListed(t *testing.T) {
	const tool = "lookup"
	h := start(t, mcptest.Start(t, mcptest.ToolSpec{Name: tool, ReadOnly: true}))
	h.connector("customer", v1alpha1.TakeCareConnectorSpec{
		Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorKnowledge},
		MCP:   v1alpha1.MCPSpec{URL: "https://customer.example.com/mcp"},
	})
	h.acceptBackend("customer")
	h.waitCondition("customer", v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)
	toolIs := func(class v1alpha1.ToolClass, source string) func() bool {
		return func() bool {
			c := h.get("customer")
			return c != nil && len(c.Status.Tools) == 1 && c.Status.Tools[0].Class == class && c.Status.Tools[0].Source == source
		}
	}
	h.eventually("the claimed read tool is act, untrusted", toolIs(v1alpha1.ToolAct, v1alpha1.ToolSourceUntrusted))

	// The status an operator under the annotation rule left: read.
	c := h.get("customer")
	c.Status.Tools = []v1alpha1.ToolStatus{{Name: tool, Class: v1alpha1.ToolRead, Source: v1alpha1.ToolSourceAnnotation}}
	h.must(h.c.Status().Update(h.ctx, c))
	for _, n := range []string{"1", "2"} {
		h.must(h.c.Patch(h.ctx, h.get("customer"), client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"annotations":{"takecare.agaetis.fr/discover":"`+n+`"}}}`))))
		h.eventually("rediscovered after the upgrade", toolIs(v1alpha1.ToolAct, v1alpha1.ToolSourceUntrusted))
	}
	h.eventually("one ToolClassChanged Warning naming the way back", func() bool {
		var events eventsv1.EventList
		h.must(h.c.List(h.ctx, &events, client.InNamespace(operatorNS)))
		n := 0
		for _, e := range events.Items {
			if e.Reason != ReasonToolClassChanged || e.Type != corev1.EventTypeWarning || e.Regarding.Name != "customer" {
				continue
			}
			if !strings.Contains(e.Note, "list it in spec.tools with class read") {
				t.Errorf("event note = %q", e.Note)
			}
			n++
			if e.Series != nil {
				n += int(e.Series.Count) - 1
			}
		}
		return n == 1
	})

	// Listed as read: the override, recorded and warned as any relaxing.
	c = h.get("customer")
	c.Spec.Tools = []v1alpha1.ToolOverride{{Name: tool, Class: v1alpha1.ToolRead}}
	h.must(h.c.Update(h.ctx, c))
	h.eventually("the listed tool is read, by override", toolIs(v1alpha1.ToolRead, v1alpha1.ToolSourceOverride))
	if o := h.get("customer").Status.Overrides; len(o) != 1 || o[0] != (v1alpha1.ToolClassChange{Name: tool, From: v1alpha1.ToolAct, To: v1alpha1.ToolRead}) {
		t.Errorf("status.overrides = %+v", o)
	}
}
