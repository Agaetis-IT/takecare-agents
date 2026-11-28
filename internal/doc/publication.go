// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package doc

import (
	"fmt"
	"strings"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/report"
)

// ApprenticeBanner opens every message at the Apprentice level (spec 02 §4).
const ApprenticeBanner = "Apprentice: shadow diagnosis, no one was paged by TakeCare."

// keyText and keyType are Block Kit's text and type fields.
const (
	keyText = "text"
	keyType = "type"
)

// Destination is where a completed investigation is published (spec 02
// §4), from the level it was created at: Apprentice goes to the Apprentice
// destination or nowhere, never to the Practitioner's; Practitioner and
// Expert go to the Practitioner destination; a silent investigation goes
// nowhere.
func Destination(d *v1alpha1.TakeCareDoc, inv *v1alpha1.TakeCareInvestigation) (string, bool) {
	if inv.Spec.Silent {
		return "", false
	}
	if inv.Spec.Level == v1alpha1.LevelApprentice || inv.Spec.Level == "" {
		return d.Spec.Report.Apprentice, d.Spec.Report.Apprentice != ""
	}
	return d.Spec.Report.Practitioner, d.Spec.Report.Practitioner != ""
}

// Publication is the message of one completed investigation.
type Publication struct {
	Alert, Service string
	Apprentice     bool
	Diagnosis      string
	// Since is the time from the alert's acceptance to publication.
	Since time.Duration
	// Usage is nil when it could not be measured; UsageReason says why.
	Usage       *v1alpha1.Usage
	UsageReason string
	TraceID     string
	// Later names the alerts of the incident folded in after the run
	// started, which the agent was not told about.
	Later    []string
	Degraded *v1alpha1.Degradation
	// Confidence is the recorded one, empty when none was recorded;
	// Capped when the operator lowered a stated High.
	Confidence v1alpha1.Confidence
	Capped     bool
}

// NewPublication builds the message. sent names the grouped alerts the
// run's message carried; nil when unknown (a resumed run), in which case
// the alerts that fired after the run started are named.
func NewPublication(inv *v1alpha1.TakeCareInvestigation, now time.Time, usageReason string, sent map[string]bool) Publication {
	a := inv.Spec.Alert
	service := a.Service
	if service == "" {
		service = a.Workload
	}
	if service == "" {
		service = "an unnamed service"
	}
	var later []string
	for _, g := range inv.Status.Alerts {
		switch {
		case sent != nil && sent[g.Fingerprint]:
		case sent == nil && inv.Status.StartedAt != nil && !g.FiredAt.After(inv.Status.StartedAt.Time):
		default:
			later = append(later, g.Name)
		}
	}
	return Publication{
		Alert: a.Name, Service: service, Apprentice: inv.Spec.Level == v1alpha1.LevelApprentice || inv.Spec.Level == "",
		Diagnosis: inv.Status.Diagnosis, Since: now.Sub(inv.CreationTimestamp.Time).Round(time.Second),
		Usage: inv.Status.Usage, UsageReason: usageReason, TraceID: inv.Status.TraceID,
		Later: later, Degraded: inv.Status.Degraded,
		Confidence: inv.Status.Confidence, Capped: inv.Status.ConfidenceCapped,
	}
}

// Sent names the grouped alerts a run's message carries (Message renders
// the record's status.alerts as they are when the run starts).
func Sent(inv *v1alpha1.TakeCareInvestigation) map[string]bool {
	sent := map[string]bool{}
	for _, g := range inv.Status.Alerts {
		sent[g.Fingerprint] = true
	}
	return sent
}

func (p Publication) header() string {
	return fmt.Sprintf("Doc · %s on %s", p.Alert, p.Service)
}

// context is the line under the diagnosis: time, confidence, usage,
// trace.
func (p Publication) context() string {
	parts := []string{p.Since.String() + " after the alert"}
	if p.Confidence != "" {
		c := "confidence " + strings.ToLower(string(p.Confidence))
		if p.Capped {
			c += " (capped from high: the alert is in a blind spot)"
		}
		parts = append(parts, c)
	}
	if p.Usage != nil {
		parts = append(parts,
			fmt.Sprintf("%d model calls", p.Usage.ModelCalls),
			report.Thousands(p.Usage.InputTokens+p.Usage.OutputTokens)+" tokens")
		if p.Usage.EstimatedCost != "" {
			cost := p.Usage.EstimatedCost
			if p.Usage.Approximate {
				cost += " (approximate)"
			}
			parts = append(parts, cost)
		}
	} else {
		parts = append(parts, "usage unknown: "+p.UsageReason)
	}
	parts = append(parts, "trace "+p.TraceID)
	return strings.Join(parts, " · ")
}

// notes are the lines about what the agent did not see.
func (p Publication) notes() []string {
	var notes []string
	if p.Degraded != nil {
		notes = append(notes, "Degraded: investigated without "+strings.Join(p.Degraded.Connectors, ", ")+".")
	}
	if n := len(p.Later); n > 0 {
		word := "alerts"
		if n == 1 {
			word = "alert"
		}
		notes = append(notes, fmt.Sprintf("%d other %s of the same incident, not passed to the investigation: %s.", n, word, strings.Join(p.Later, ", ")))
	}
	return notes
}

// Blocks renders the message as Slack Block Kit.
func (p Publication) Blocks() []any {
	text := func(kind, s string) map[string]any { return map[string]any{keyType: kind, keyText: s} }
	// verbatim: Slack links no URL, channel name or mention it did not
	// get as markup, written by the model or carried by a name
	// (docs.slack.dev, text object); markup characters are escaped.
	mrkdwnText := func(s string) map[string]any { return map[string]any{keyType: "mrkdwn", keyText: s, "verbatim": true} }
	var blocks []any
	blocks = append(blocks, map[string]any{keyType: "header", keyText: text("plain_text", p.header())})
	if p.Apprentice {
		blocks = append(blocks, map[string]any{keyType: "context", "elements": []any{mrkdwnText(":warning: *" + ApprenticeBanner + "*")}})
	}
	body := report.Section(report.Mrkdwn(p.Diagnosis))
	blocks = append(blocks, map[string]any{keyType: "section", keyText: mrkdwnText(body)})
	notes := p.notes()
	elements := make([]any, 0, 1+len(notes))
	elements = append(elements, mrkdwnText(report.Escape(p.context())))
	for _, n := range notes {
		// An alert's name comes from Alertmanager, not from TakeCare.
		elements = append(elements, mrkdwnText(report.Escape(n)))
	}
	blocks = append(blocks, map[string]any{keyType: "context", "elements": elements})
	return blocks
}

// Text renders the same content as plain text: the log adapter's message
// and Slack's notification fallback.
func (p Publication) Text() string {
	var b strings.Builder
	b.WriteString(p.header() + "\n")
	if p.Apprentice {
		b.WriteString(ApprenticeBanner + "\n")
	}
	b.WriteString(report.Plain(p.Diagnosis) + "\n")
	b.WriteString(p.context() + "\n")
	for _, n := range p.notes() {
		b.WriteString(n + "\n")
	}
	return b.String()
}
