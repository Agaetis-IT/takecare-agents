// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package surveyagent holds what is specific to the Intendant's agent pass
// (spec 10 §5): its instructions, and how a TakeCareIntendant becomes an
// agent for the engine. The engine never sees this package; this package
// never sees an engine type beyond the boundary's.
package surveyagent

import (
	"bytes"
	_ "embed"
	"fmt"
	"text/template"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine"
)

//go:embed instructions.md
var instructionsSource string

var instructions = template.Must(template.New("instructions.md").Parse(instructionsSource))

// Kind is the Intendant's short name in routes and labels.
const Kind = "intendant"

// Skill is the Intendant's A2A skill (ADR 0009).
var Skill = engine.Skill{
	ID:          "survey-ground",
	Description: "Given a survey's import, read the ground read-only and propose what a listing cannot see: dependencies, owners, runbooks; each proposal with the tool calls that support it.",
	Tags:        []string{"inventory", "map", "read-only"},
}

// AgentName names the engine's agent of an Intendant. The engine namespace
// is shared, the Intendant's name is unique in the operator namespace.
func AgentName(in *v1alpha1.TakeCareIntendant) string { return "takecare-intendant-" + in.Name }

// sources describes, per connector type, what the agent can read. Order
// is the order of the instructions.
var sources = []struct {
	t    v1alpha1.ConnectorType
	text string
}{
	{v1alpha1.ConnectorMap, "The Map of the ground: its entries, their relations, what depends on what (`blast_radius`) and how two entries connect (`path`)."},
	{v1alpha1.ConnectorCluster, "The Kubernetes cluster, read-only: the workloads' specs, labels and annotations, never a Secret."},
	{v1alpha1.ConnectorKnowledge, "The team's knowledge: runbooks and the other records its tools return, each with its id."},
}

// Instructions renders the agent's system message for the Intendant's
// scope and the types of the connectors its agent is given.
func Instructions(scope v1alpha1.Scope, types map[v1alpha1.ConnectorType]bool) (string, error) {
	var lines []string
	for _, s := range sources {
		if types[s.t] {
			lines = append(lines, s.text)
		}
	}
	in := make([]string, 0, len(scope.Namespaces)+len(scope.Entities))
	for _, ns := range scope.Namespaces {
		in = append(in, fmt.Sprintf("The namespace `%s`.", ns))
	}
	for _, e := range scope.Entities {
		in = append(in, fmt.Sprintf("The entity `%s`.", e))
	}
	var out bytes.Buffer
	if err := instructions.Execute(&out, map[string]any{"Sources": lines, "Scope": in}); err != nil {
		return "", fmt.Errorf("render the Intendant's instructions: %w", err)
	}
	return out.String(), nil
}

// Agent is what the engine runs for an Intendant.
type Agent struct {
	Intendant *v1alpha1.TakeCareIntendant
	// EngineNamespace receives the engine's objects.
	EngineNamespace string
	// Model names the engine's model configuration.
	Model string
	// Tools are the connectors given to the agent, with its own allow-lists:
	// never the operator's get_rules.
	Tools []engine.ToolServer
	// Types are the types of those connectors, for the instructions.
	Types map[v1alpha1.ConnectorType]bool
	// CaptureContent follows the platform (ADR 0006).
	CaptureContent bool
}

// Render returns the engine's view of the Intendant's agent.
func (a Agent) Render() (engine.Agent, error) {
	text, err := Instructions(a.Intendant.Spec.Scope, a.Types)
	if err != nil {
		return engine.Agent{}, err
	}
	return engine.Agent{
		Name:           AgentName(a.Intendant),
		Namespace:      a.EngineNamespace,
		Description:    "TakeCare Intendant: reads the ground read-only and proposes what a survey's import cannot see.",
		Instructions:   text,
		Model:          a.Model,
		Tools:          a.Tools,
		Skill:          Skill,
		CaptureContent: a.CaptureContent,
		Labels:         OwnerLabels(a.Intendant),
	}, nil
}

// OwnerLabels mark the Intendant's objects in the engine namespace, which
// cannot carry an owner reference to it: its agent and its model
// configuration.
func OwnerLabels(in *v1alpha1.TakeCareIntendant) map[string]string {
	return map[string]string{
		"app.kubernetes.io/part-of":    "takecare",
		"takecare.agaetis.fr/role":     Kind,
		"takecare.agaetis.fr/owner-ns": in.Namespace,
		"takecare.agaetis.fr/owner":    in.Name,
	}
}
