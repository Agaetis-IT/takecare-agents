// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

//go:embed message.tmpl
var messageSource string

var message = template.Must(template.New("message.tmpl").Parse(messageSource))

// Request is what one review asks the agent.
type Request struct {
	Review *v1alpha1.TakeCareReview
	Scope  v1alpha1.Scope
	// Level is the level the review runs at.
	Level v1alpha1.Level
	// Period is the window the agent looks back over.
	Period v1alpha1.ReviewPeriod
}

// Message renders the review's request to the agent (spec 11 §6): names,
// never a diagnosis.
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
	var out bytes.Buffer
	if err := message.Execute(&out, map[string]any{
		"Name": r.Review.Name, "Trigger": string(r.Review.Spec.Trigger), "Scope": strings.Join(scope, ", "),
		"Investigations": r.Review.Spec.Investigations, "Incident": r.Review.Spec.Incident,
		"From": r.Period.From.UTC().Format(time.RFC3339), "To": r.Period.To.UTC().Format(time.RFC3339),
		"Practitioner": r.Level != v1alpha1.LevelApprentice,
	}); err != nil {
		return "", fmt.Errorf("render the review's message: %w", err)
	}
	return out.String(), nil
}
