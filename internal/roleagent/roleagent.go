// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package roleagent holds what every agent role decides the same way about
// its engine agent: which platform model it uses, the engine's model
// configuration that reaches it through the role's model route (spec 01,
// ADR 0004), and which tools of a connector the agent is given (spec 05).
// The roles keep their own conditions, messages and naming.
package roleagent

import (
	"fmt"
	"slices"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/controller/platform"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/routes"
)

// Why a role's model cannot be resolved.
const (
	ReasonPlatformMissing = "PlatformMissing"
	ReasonModelUnknown    = "ModelUnknown"
)

// Model returns the platform model a role uses: the one it names, else the
// platform's first. When it cannot be resolved, name is empty and reason
// and message say why.
func Model(p *v1alpha1.TakeCarePlatform, want *v1alpha1.ModelRef) (name, reason, message string) {
	if p == nil {
		return "", ReasonPlatformMissing, "no TakeCarePlatform"
	}
	if len(p.Spec.Models) == 0 {
		return "", ReasonModelUnknown, "the platform declares no model"
	}
	name = p.Spec.Models[0].Name
	if want != nil {
		name = want.Name
	}
	if !slices.ContainsFunc(p.Spec.Models, func(m v1alpha1.ModelSpec) bool { return m.Name == name }) {
		return "", ReasonModelUnknown, fmt.Sprintf("spec.model names %s, which the platform does not declare", name)
	}
	return name, "", ""
}

// ModelConfig is the engine's model configuration of a role's agent: the
// platform model's identifier, reached through the role's own model route,
// with the engine's dummy key. model must be one the platform declares
// (Model resolved it).
func ModelConfig(p *v1alpha1.TakeCarePlatform, role routes.Role, model, name, namespace string, labels map[string]string) engine.Model {
	i := slices.IndexFunc(p.Spec.Models, func(m v1alpha1.ModelSpec) bool { return m.Name == model })
	return engine.Model{
		Name: name, Namespace: namespace, Model: p.Spec.Models[i].Model,
		GatewayURL:     p.Status.GatewayURL + routes.ModelPath(role, model) + "/v1",
		DummyKeySecret: platform.DummyKeySecretName, DummyKeyField: platform.DummyKeyField,
		Labels: labels,
	}
}

// Tools are a connector's tools an agent is given: its read tools, a
// content tool only when the role lists it in contentTools. withheld are
// the content tools left out. Both are sorted.
func Tools(c *v1alpha1.TakeCareConnector, contentTools []string) (read, withheld []string) {
	for _, tool := range c.Status.Tools {
		switch {
		case tool.Class != v1alpha1.ToolRead:
		case tool.Content && !slices.Contains(contentTools, tool.Name):
			withheld = append(withheld, tool.Name)
		default:
			read = append(read, tool.Name)
		}
	}
	slices.Sort(read)
	slices.Sort(withheld)
	return read, withheld
}
