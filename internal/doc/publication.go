// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package doc

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// TrialBanner opens every message at the trial level (spec 02 §4).
const TrialBanner = "Trial: shadow diagnosis, no one was paged by TakeCare."

// sectionLimit is Slack's limit on a section's text; keyText is Block
// Kit's text field.
const (
	sectionLimit = 3000
	keyText      = "text"
	keyType      = "type"
)

// Destination is where a completed investigation is published (spec 02
// §4), from the level it was created at: trial goes to the trial
// destination or nowhere, never to service's; service and seasoned go to
// the service destination; a silent investigation goes nowhere.
func Destination(d *v1alpha1.TakeCareDoc, inv *v1alpha1.TakeCareInvestigation) (string, bool) {
	if inv.Spec.Silent {
		return "", false
	}
	if inv.Spec.Level == v1alpha1.LevelTrial || inv.Spec.Level == "" {
		return d.Spec.Report.Trial, d.Spec.Report.Trial != ""
	}
	return d.Spec.Report.Service, d.Spec.Report.Service != ""
}

// Publication is the message of one completed investigation.
type Publication struct {
	Alert, Service string
	Trial          bool
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
		Alert: a.Name, Service: service, Trial: inv.Spec.Level == v1alpha1.LevelTrial || inv.Spec.Level == "",
		Diagnosis: inv.Status.Diagnosis, Since: now.Sub(inv.CreationTimestamp.Time).Round(time.Second),
		Usage: inv.Status.Usage, UsageReason: usageReason, TraceID: inv.Status.TraceID,
		Later: later, Degraded: inv.Status.Degraded,
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

// context is the line under the diagnosis: time, usage, trace.
func (p Publication) context() string {
	parts := []string{p.Since.String() + " after the alert"}
	if p.Usage != nil {
		parts = append(parts,
			fmt.Sprintf("%d model calls", p.Usage.ModelCalls),
			thousands(p.Usage.InputTokens+p.Usage.OutputTokens)+" tokens")
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
	var blocks []any
	blocks = append(blocks, map[string]any{keyType: "header", keyText: text("plain_text", p.header())})
	if p.Trial {
		blocks = append(blocks, map[string]any{keyType: "context", "elements": []any{text("mrkdwn", ":warning: *"+TrialBanner+"*")}})
	}
	body := p.Diagnosis
	if len(body) > sectionLimit {
		// Cut on a rune boundary, leaving room for the ellipsis' 3 bytes.
		cut := sectionLimit - len("…")
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = body[:cut] + "…"
	}
	blocks = append(blocks, map[string]any{keyType: "section", keyText: text("mrkdwn", body)})
	notes := p.notes()
	elements := make([]any, 0, 1+len(notes))
	elements = append(elements, text("mrkdwn", p.context()))
	for _, n := range notes {
		elements = append(elements, text("mrkdwn", n))
	}
	blocks = append(blocks, map[string]any{keyType: "context", "elements": elements})
	return blocks
}

// Text renders the same content as plain text: the log adapter's message
// and Slack's notification fallback.
func (p Publication) Text() string {
	var b strings.Builder
	b.WriteString(p.header() + "\n")
	if p.Trial {
		b.WriteString(TrialBanner + "\n")
	}
	b.WriteString(p.Diagnosis + "\n")
	b.WriteString(p.context() + "\n")
	for _, n := range p.notes() {
		b.WriteString(n + "\n")
	}
	return b.String()
}

// thousands writes 42560 as 42,560.
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
