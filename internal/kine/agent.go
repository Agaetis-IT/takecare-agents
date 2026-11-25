// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package kine holds what is specific to the Kiné's agent (spec 11 §6,
// §7): its instructions, and how a TakeCareKine becomes an agent for the
// engine. The engine never sees this package; this package never sees an
// engine type beyond the boundary's.
package kine

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

// Kind is the Kiné's short name in routes and labels.
const Kind = "kine"

// DiagnosisTool is the history tool that returns an investigation's
// diagnosis, a content tool (spec 11 §4).
const DiagnosisTool = "investigation_get"

// Skill is the Kiné's A2A skill (ADR 0009).
var Skill = engine.Skill{
	ID:          "review-incident",
	Description: "Review ended incidents read-only: the problem and its root cause from the diagnosis, its recurrences, and the corrective actions from earlier incidents with their state, each backed by the tool calls that support it.",
	Tags:        []string{"sre", "problem-management", "read-only"},
}

// AgentName names the engine's agent of a Kiné. The engine namespace is
// shared, the Kiné's name is unique in the operator namespace.
func AgentName(k *v1alpha1.TakeCareKine) string { return v1alpha1.KineAgentPrefix + k.Name }

// sources describes, per connector type, what the agent can read. Order is
// the order of the instructions.
var sources = []struct {
	t    v1alpha1.ConnectorType
	text string
}{
	{v1alpha1.ConnectorHistory, "TakeCare's history: the investigations and the earlier reviews of the Kiné, listed by period, entity, alert and incident key."},
	{v1alpha1.ConnectorKnowledge, "The team's knowledge: post-mortems and runbooks, each record with its id and, for a post-mortem, its follow-up actions."},
	{v1alpha1.ConnectorTickets, "The team's tickets: whether a corrective action was taken up, and whether it is done."},
	{v1alpha1.ConnectorChanges, "The change log: what was deployed or reconfigured, and when."},
	{v1alpha1.ConnectorMap, "The Map of the ground: the entries, their relations and what depends on what."},
}

// Instructions renders the Kiné's system message for its scope, its level,
// the types of the connectors its agent is given and whether it can read
// a diagnosis.
func Instructions(scope v1alpha1.Scope, level v1alpha1.Level, types map[v1alpha1.ConnectorType]bool, diagnosis bool) (string, error) {
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
	if err := instructions.Execute(&out, map[string]any{
		"Sources": lines, "Scope": in, "Diagnosis": diagnosis,
		"Practitioner": level != v1alpha1.LevelApprentice,
	}); err != nil {
		return "", fmt.Errorf("render the Kiné's instructions: %w", err)
	}
	return out.String(), nil
}

// Agent is what the engine runs for a Kiné.
type Agent struct {
	Kine *v1alpha1.TakeCareKine
	// Level is the level the Kiné works at (Expert works as Practitioner).
	Level v1alpha1.Level
	// EngineNamespace receives the engine's objects.
	EngineNamespace string
	// Model names the engine's model configuration.
	Model string
	// Tools are the connectors given to the agent, with their allow-lists.
	Tools []engine.ToolServer
	// Types are the types of those connectors, for the instructions.
	Types map[v1alpha1.ConnectorType]bool
	// CaptureContent follows the platform (ADR 0006).
	CaptureContent bool
}

// Render returns the engine's view of the Kiné.
func (a Agent) Render() (engine.Agent, error) {
	diagnosis := false
	for _, ts := range a.Tools {
		for _, tool := range ts.Tools {
			diagnosis = diagnosis || tool == DiagnosisTool
		}
	}
	text, err := Instructions(a.Kine.Spec.Scope, a.Level, a.Types, diagnosis)
	if err != nil {
		return engine.Agent{}, err
	}
	return engine.Agent{
		Name:           AgentName(a.Kine),
		Namespace:      a.EngineNamespace,
		Description:    "TakeCare Kiné: reviews ended incidents read-only and follows their corrective actions.",
		Instructions:   text,
		Model:          a.Model,
		Tools:          a.Tools,
		Skill:          Skill,
		CaptureContent: a.CaptureContent,
		Labels:         OwnerLabels(a.Kine),
	}, nil
}

// OwnerLabels mark the Kiné's objects in the engine namespace, which cannot
// carry an owner reference to it: its agent and its model configuration.
func OwnerLabels(k *v1alpha1.TakeCareKine) map[string]string {
	return map[string]string{
		"app.kubernetes.io/part-of":    "takecare",
		v1alpha1.LabelRole:             Kind,
		"takecare.agaetis.fr/owner-ns": k.Namespace,
		"takecare.agaetis.fr/owner":    k.Name,
	}
}
