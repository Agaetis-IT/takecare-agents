// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mecano/eos"
)

//go:embed message.tmpl
var messageSource string

var message = template.Must(template.New("message.tmpl").Parse(messageSource))

// BranchPrefix starts every branch a maintenance proposes on (spec 12
// §6.2), followed by the maintenance's name.
const BranchPrefix = "takecare/mecano/"

// Branch is the one branch a maintenance's pull requests use.
func Branch(maintenance string) string { return BranchPrefix + maintenance }

// Request is what one maintenance asks the agent.
type Request struct {
	Maintenance *v1alpha1.TakeCareMaintenance
	Scope       v1alpha1.Scope
	// Level is the level the maintenance runs at.
	Level v1alpha1.Level
	// Inventory, ClusterVersion and BlindSpots are the collector's.
	Inventory      v1alpha1.MaintenanceInventory
	ClusterVersion string
	BlindSpots     []v1alpha1.NotSeen
	// DatasetDate is the end-of-support dataset's date.
	DatasetDate string
	// Repositories are the ones the plan may be opened on, empty when no
	// pull request may be opened in this maintenance (Apprentice, silent,
	// a propose route not proven).
	Repositories []string
	// Copy says the team's copy was read.
	Copy bool
}

// Message renders the maintenance's request to the agent (spec 12 §9):
// the facts and the task, never a finding.
func Message(r Request) (string, error) {
	scope := make([]string, 0, len(r.Scope.Namespaces)+len(r.Scope.Entities))
	for _, ns := range r.Scope.Namespaces {
		scope = append(scope, "the namespace "+string(ns))
	}
	for _, e := range r.Scope.Entities {
		scope = append(scope, "the entity "+string(e))
	}
	if len(scope) == 0 {
		scope = []string{"none"}
	}
	inv, err := json.MarshalIndent(r.Inventory, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render the maintenance's inventory: %w", err)
	}
	var out bytes.Buffer
	if err := message.Execute(&out, map[string]any{
		"Name": r.Maintenance.Name, "Trigger": string(r.Maintenance.Spec.Trigger), "Scope": strings.Join(scope, ", "),
		"Components": r.Maintenance.Spec.Components, "DatasetDate": r.DatasetDate, "ClusterVersion": r.ClusterVersion,
		"Inventory": string(inv), "BlindSpots": r.BlindSpots,
		"Practitioner": r.Level != v1alpha1.LevelApprentice, "Repositories": r.Repositories,
		"Branch": Branch(r.Maintenance.Name), "Copy": r.Copy,
	}); err != nil {
		return "", fmt.Errorf("render the maintenance's message: %w", err)
	}
	return out.String(), nil
}

// DatasetVersions are the versions the dataset names, which a finding may
// name besides the inventory's: each Kubernetes minor and its latest
// patch, each component line and its latest version.
func DatasetVersions(ds *eos.Dataset) []string {
	var out []string
	for _, m := range ds.Kubernetes.Minors {
		out = append(out, m.Minor, m.Latest)
	}
	for _, c := range ds.Components {
		out = append(out, c.Latest)
		for _, l := range c.Lines {
			out = append(out, l.Line, l.Latest)
		}
	}
	return out
}
