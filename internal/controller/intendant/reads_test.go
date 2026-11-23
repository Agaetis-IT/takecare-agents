// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"testing"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// Each kagent kind has its own opt-in: the agents rule does not list
// SandboxAgents, nor the sandboxagents rule Agents (spec 10 §8).
func TestReadsKagentKinds(t *testing.T) {
	t.Parallel()
	agents := v1alpha1.ResourceRef{Group: agentGVK.Group, Resource: "agents"}
	sandboxes := v1alpha1.ResourceRef{Group: agentGVK.Group, Resource: "sandboxagents"}
	for name, tc := range map[string]struct {
		k8s                     *v1alpha1.KubernetesSpec
		wantAgents, wantSandbox bool
	}{
		"no kubernetes spec": {nil, false, false},
		"no opt-in":          {&v1alpha1.KubernetesSpec{}, false, false},
		"agents only":        {&v1alpha1.KubernetesSpec{ExtraResources: []v1alpha1.ResourceRef{agents}}, true, false},
		"sandboxagents only": {&v1alpha1.KubernetesSpec{ExtraResources: []v1alpha1.ResourceRef{sandboxes}}, false, true},
		"both":               {&v1alpha1.KubernetesSpec{ExtraResources: []v1alpha1.ResourceRef{agents, sandboxes}}, true, true},
		"another group":      {&v1alpha1.KubernetesSpec{ExtraResources: []v1alpha1.ResourceRef{{Group: "other.dev", Resource: "sandboxagents"}}}, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := &v1alpha1.TakeCareConnector{Spec: v1alpha1.TakeCareConnectorSpec{Kubernetes: tc.k8s}}
			if got := reads(c, "agents"); got != tc.wantAgents {
				t.Errorf("reads agents = %v, want %v", got, tc.wantAgents)
			}
			if got := reads(c, "sandboxagents"); got != tc.wantSandbox {
				t.Errorf("reads sandboxagents = %v, want %v", got, tc.wantSandbox)
			}
		})
	}
}
