// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package doc

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine"
)

//go:embed message.tmpl
var messageSource string

var message = template.Must(template.New("message.tmpl").Funcs(template.FuncMap{"join": strings.Join}).Parse(messageSource))

// Run turns an investigation into a run of the Doc's agent: the second of
// the three steps of spec 06 §2 (the alert made the record; the record makes
// a run; the engine runs it). The run carries a rendered message and
// nothing that names an alert.
//
// The alerts of the same incident folded into the record before the run
// starts are named in the message; those folded later are not, a running
// investigation is never changed (the publication names them, PR 4c).
func Run(d *v1alpha1.TakeCareDoc, inv *v1alpha1.TakeCareInvestigation, engineNamespace, gatewayURL string) (engine.Run, error) {
	text, err := Message(inv)
	if err != nil {
		return engine.Run{}, err
	}
	return engine.Run{
		Namespace:      engineNamespace,
		Agent:          AgentName(d),
		GatewayURL:     gatewayURL,
		Message:        text,
		ConversationID: inv.Name,
	}, nil
}

// Message renders the investigation's message.
func Message(inv *v1alpha1.TakeCareInvestigation) (string, error) {
	a := inv.Spec.Alert
	service := a.Service
	if service == "" {
		service = a.Workload
	}
	if service == "" {
		service = "an unnamed service"
	}
	description := a.Annotations["description"]
	if description == "" {
		description = a.Annotations["summary"]
	}
	others := make([]string, 0, len(inv.Status.Alerts))
	for _, g := range inv.Status.Alerts {
		others = append(others, fmt.Sprintf("%s (fired %s)", g.Name, g.FiredAt.UTC().Format(time.RFC3339)))
	}
	var out bytes.Buffer
	if err := message.Execute(&out, map[string]any{
		"Alert": a.Name, "Service": service, "Namespace": a.Namespace,
		"Description": strings.TrimSpace(description), "Others": others,
	}); err != nil {
		return "", fmt.Errorf("render the message of %s: %w", inv.Name, err)
	}
	return out.String(), nil
}
