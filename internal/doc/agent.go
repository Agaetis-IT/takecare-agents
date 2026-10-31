// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Package doc holds what is specific to the Doc (spec 02): its instructions,
// and how a TakeCareDoc becomes an agent for the engine. The engine never
// sees this package; this package never sees an engine type beyond the
// boundary's.
package doc

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine"
)

//go:embed instructions.md
var instructionsSource string

var instructions = template.Must(template.New("instructions.md").Parse(instructionsSource))

// Kind is the Doc's short name in routes and labels.
const Kind = "doc"

// Skill is the Doc's A2A skill (ADR 0009).
var Skill = engine.Skill{
	ID:          "diagnose-alert",
	Description: "Investigate one firing alert, read-only, and return a sourced diagnosis: observations and where, root cause, recommended action and who should act, leads ruled out.",
	Tags:        []string{"sre", "diagnosis", "read-only"},
}

// AgentName names the engine's agent of a Doc. The engine namespace is
// shared, the Doc's name is unique in the operator namespace.
func AgentName(d *v1alpha1.TakeCareDoc) string { return "takecare-doc-" + d.Name }

// sources describes, per connector type, what the Doc can read. Order is
// the order of the instructions.
var sources = []struct {
	t    v1alpha1.ConnectorType
	text func(namespaces []string) string
}{
	{v1alpha1.ConnectorCluster, func(ns []string) string {
		return fmt.Sprintf("The Kubernetes cluster, limited to the namespaces %s. Cluster-wide queries and other namespaces are denied; do not retry them.", strings.Join(ns, ", "))
	}},
	{v1alpha1.ConnectorMetrics, func([]string) string { return "Metrics and alerting rules." }},
	{v1alpha1.ConnectorLogs, func([]string) string { return "Application and platform logs." }},
	{v1alpha1.ConnectorKnowledge, func([]string) string {
		return "The team's knowledge: incident post-mortems, runbooks and vendor status bulletins."
	}},
	{v1alpha1.ConnectorChanges, func([]string) string { return "The change log: recent deployments and configuration changes." }},
	{v1alpha1.ConnectorTickets, func([]string) string { return "Customer support tickets." }},
}

// Instructions renders the Doc's system message for the namespaces it can
// read and the types of the connectors it is given.
func Instructions(readable []v1alpha1.Namespace, types map[v1alpha1.ConnectorType]bool) (string, error) {
	namespaces := make([]string, len(readable))
	for i, n := range readable {
		namespaces[i] = "`" + string(n) + "`"
	}
	var lines []string
	for _, s := range sources {
		if types[s.t] {
			lines = append(lines, s.text(namespaces))
		}
	}
	var out bytes.Buffer
	if err := instructions.Execute(&out, map[string]any{"Sources": lines}); err != nil {
		return "", fmt.Errorf("render the Doc's instructions: %w", err)
	}
	return out.String(), nil
}

// Agent is what the engine runs for a Doc.
type Agent struct {
	Doc *v1alpha1.TakeCareDoc
	// EngineNamespace receives the engine's objects.
	EngineNamespace string
	// Model names the engine's model configuration.
	Model string
	// Tools are the connectors given to the agent, with their allow-lists.
	Tools []engine.ToolServer
	// Types are the types of those connectors, for the instructions.
	Types map[v1alpha1.ConnectorType]bool
	// Readable are the namespaces the cluster connectors read, a superset of
	// the Doc's scope (spec 02): a dependency of an alerting workload may
	// live in another of them. Empty means the scope.
	Readable []v1alpha1.Namespace
	// CaptureContent follows the platform (ADR 0006).
	CaptureContent bool
}

// Render returns the engine's view of the Doc.
func (a Agent) Render() (engine.Agent, error) {
	readable := a.Readable
	if len(readable) == 0 {
		readable = a.Doc.Spec.Scope.Namespaces
	}
	text, err := Instructions(readable, a.Types)
	if err != nil {
		return engine.Agent{}, err
	}
	return engine.Agent{
		Name:           AgentName(a.Doc),
		Namespace:      a.EngineNamespace,
		Description:    "TakeCare Doc: diagnoses one firing alert, read-only, and returns a sourced diagnosis.",
		Instructions:   text,
		Model:          a.Model,
		Tools:          a.Tools,
		Skill:          Skill,
		CaptureContent: a.CaptureContent,
		Labels: map[string]string{
			"app.kubernetes.io/part-of":    "takecare",
			"takecare.agaetis.fr/role":     Kind,
			"takecare.agaetis.fr/owner-ns": a.Doc.Namespace,
			"takecare.agaetis.fr/owner":    a.Doc.Name,
		},
	}, nil
}
