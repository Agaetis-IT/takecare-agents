// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package mecano holds what is specific to the Mécano's agent (spec 12
// §9, §10): its instructions, and how a TakeCareMecano becomes an agent for
// the engine. The engine never sees this package; this package never sees
// an engine type beyond the boundary's.
package mecano

import (
	"bytes"
	_ "embed"
	"fmt"
	"slices"
	"text/template"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine"
)

//go:embed instructions.md
var instructionsSource string

var instructions = template.Must(template.New("instructions.md").Parse(instructionsSource))

// Kind is the Mécano's short name in routes and labels.
const Kind = "mecano"

// ProposeTool is the propose server's one tool (spec 12 §6.2).
const ProposeTool = "propose_change"

// FileTool is the code connector's content tool: a file's content (spec 12
// §5).
const FileTool = "file_get"

// Skill is the Mécano's A2A skill (ADR 0009).
var Skill = engine.Skill{
	ID:          "maintain-fleet",
	Description: "Take stock of the fleet's versions, end of support, removed APIs, certificates and drift between what runs and what Git declares, read-only on clusters; at Practitioner, plan the upgrades and open the plan as a draft pull request.",
	Tags:        []string{"sre", "lifecycle", "read-only-clusters"},
}

// AgentName names the engine's agent of a Mécano. The engine namespace is
// shared, the Mécano's name is unique in the operator namespace.
func AgentName(m *v1alpha1.TakeCareMecano) string { return v1alpha1.MecanoAgentPrefix + m.Name }

// sources describes, per connector type given to the agent, what it can
// read. Order is the order of the instructions. The releases and copy
// slots are the collector's: their facts come in the message.
var sources = []struct {
	t    v1alpha1.ConnectorType
	text string
}{
	{v1alpha1.ConnectorCluster, "What runs live on the clusters: workloads, their images and labels, and the custom resources the connector lists."},
	{v1alpha1.ConnectorCode, "What Git declares: the repositories' manifests, charts and GitOps definitions, indexed by `manifests_index`."},
	{v1alpha1.ConnectorKnowledge, "The team's knowledge: release notes, runbooks and the team's own end-of-support dates."},
	{v1alpha1.ConnectorChanges, "The change log: what was deployed or reconfigured, and when."},
	{v1alpha1.ConnectorHistory, "TakeCare's history: the Kiné's reviews, whose actions may ask for an upgrade, and the earlier maintenances."},
	{v1alpha1.ConnectorMap, "The Map of the ground: the entries, their relations and what depends on what."},
}

// Instructions renders the Mécano's system message for its scope, its
// level, the types of the connectors its agent is given, whether it can
// read a file's content, and whether it can open a pull request.
func Instructions(scope v1alpha1.Scope, level v1alpha1.Level, types map[v1alpha1.ConnectorType]bool, files, propose bool) (string, error) {
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
		"Sources": lines, "Scope": in, "Files": files, "Propose": propose,
		"Practitioner": level != v1alpha1.LevelApprentice,
	}); err != nil {
		return "", fmt.Errorf("render the Mécano's instructions: %w", err)
	}
	return out.String(), nil
}

// Agent is what the engine runs for a Mécano.
type Agent struct {
	Mecano *v1alpha1.TakeCareMecano
	// Level is the level the Mécano works at (Expert works as
	// Practitioner).
	Level v1alpha1.Level
	// EngineNamespace receives the engine's objects.
	EngineNamespace string
	// Model names the engine's model configuration.
	Model string
	// Tools are the connectors given to the agent, with their allow-lists:
	// never the releases or copy connectors, which the collector reads.
	Tools []engine.ToolServer
	// Types are the types of those connectors, for the instructions.
	Types map[v1alpha1.ConnectorType]bool
	// CaptureContent follows the platform (ADR 0006).
	CaptureContent bool
}

// Render returns the engine's view of the Mécano.
func (a Agent) Render() (engine.Agent, error) {
	given := func(name string) bool {
		return slices.ContainsFunc(a.Tools, func(ts engine.ToolServer) bool { return slices.Contains(ts.Tools, name) })
	}
	text, err := Instructions(a.Mecano.Spec.Scope, a.Level, a.Types, given(FileTool), given(ProposeTool))
	if err != nil {
		return engine.Agent{}, err
	}
	return engine.Agent{
		Name:           AgentName(a.Mecano),
		Namespace:      a.EngineNamespace,
		Description:    "TakeCare Mécano: takes stock of versions, end of support and drift, read-only on clusters, and proposes upgrade plans as draft pull requests.",
		Instructions:   text,
		Model:          a.Model,
		Tools:          a.Tools,
		Skill:          Skill,
		CaptureContent: a.CaptureContent,
		Labels:         OwnerLabels(a.Mecano),
	}, nil
}

// OwnerLabels mark the Mécano's objects in the engine namespace, which
// cannot carry an owner reference to it: its agent and its model
// configuration.
func OwnerLabels(m *v1alpha1.TakeCareMecano) map[string]string {
	return map[string]string{
		"app.kubernetes.io/part-of":    "takecare",
		v1alpha1.LabelRole:             Kind,
		"takecare.agaetis.fr/owner-ns": m.Namespace,
		"takecare.agaetis.fr/owner":    m.Name,
	}
}
