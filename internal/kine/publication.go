// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/report"
)

// ApprenticeBanner opens every review published at the Apprentice level
// (spec 11 §8).
const ApprenticeBanner = "Apprentice: shadow review, no one was paged by TakeCare."

// PublishedActions is how many actions a message lists; the rest is
// counted (spec 11 §8).
const PublishedActions = 10

// headerLimit is Block Kit's bound on a header's text, in characters
// (docs.slack.dev, Block Kit reference).
const headerLimit = 150

// Publication is the message of one completed review (spec 11 §8): what
// the review recorded, at the level it is published at.
type Publication struct {
	// Kine is the Kiné's name, Namespace and Review the review's.
	Kine, Namespace, Review string
	// Alert and Target name the incident of a per-incident review: the
	// alert's name and its entity, else its namespace. Both are empty for
	// a periodic review; Target alone names an incident whose
	// investigation is gone.
	Alert, Target string
	Apprentice    bool
	Problem       v1alpha1.Problem
	Actions       []v1alpha1.CorrectiveAction
	// More counts the actions not listed: past PublishedActions, and
	// those the answer held past the status' bound. ToDo counts the
	// recorded actions still to do (Proposed or Open).
	More, ToDo int
	// PostMortem says that the review holds a post-mortem draft, which is
	// not posted.
	PostMortem     bool
	Investigations []string
	Period         *v1alpha1.ReviewPeriod
	// Usage is nil when it could not be measured; UsageReason says why.
	Usage       *v1alpha1.Usage
	UsageReason string
	TraceID     string
}

// NewPublication builds the message of a completed review published at
// level; inv is the oldest investigation it names, nil for a periodic
// review or when that investigation is gone.
func NewPublication(rv *v1alpha1.TakeCareReview, level v1alpha1.Level, inv *v1alpha1.TakeCareInvestigation, usageReason string) Publication {
	p := Publication{
		Kine: rv.Spec.KineRef.Name, Namespace: rv.Namespace, Review: rv.Name,
		Apprentice:     level == v1alpha1.LevelApprentice || level == "",
		Investigations: rv.Spec.Investigations, Period: rv.Spec.Period,
		Usage: rv.Status.Usage, UsageReason: usageReason, TraceID: rv.Status.TraceID,
		PostMortem: level != v1alpha1.LevelApprentice && level != "" && rv.Status.PostMortem != "",
	}
	if rv.Status.Problem != nil {
		p.Problem = *rv.Status.Problem
	}
	p.Actions = rv.Status.Actions[:min(len(rv.Status.Actions), PublishedActions)]
	p.More = len(rv.Status.Actions) - len(p.Actions) + int(rv.Status.ActionsOmitted)
	for _, a := range rv.Status.Actions {
		if a.State == v1alpha1.ActionOpen || a.State == v1alpha1.ActionProposed {
			p.ToDo++
		}
	}
	switch {
	case inv != nil:
		a := inv.Spec.Alert
		p.Alert, p.Target = a.Name, string(a.Entity)
		if p.Target == "" {
			p.Target = a.Namespace
		}
		if p.Target == "" {
			p.Target = inv.Namespace
		}
	case rv.Spec.Incident != "":
		p.Target = "incident " + rv.Spec.Incident
	case len(rv.Spec.Investigations) > 0:
		p.Target = rv.Spec.Investigations[0]
	}
	return p
}

// Empty says that the review found nothing to tell: no problem statement,
// no recurrence and no action. It is not posted (no "all clear").
func (p Publication) Empty() bool {
	return p.Problem.Statement == "" && len(p.Problem.Recurrences) == 0 && len(p.Actions) == 0 && p.More == 0
}

// header names the incident, or the periodic review, and the Kiné unless
// its name only repeats the kind (the chart's "kine").
func (p Publication) header() string {
	who := "Kiné"
	if p.Kine != Kind {
		who += " " + p.Kine
	}
	var h string
	switch {
	case p.Alert != "":
		h = fmt.Sprintf("%s · %s on %s", who, p.Alert, p.Target)
	case p.Target != "":
		h = fmt.Sprintf("%s · review of %s", who, p.Target)
	default:
		h = who + " · periodic review"
	}
	if utf8.RuneCountInString(h) > headerLimit {
		r := []rune(h)
		h = string(r[:headerLimit-1]) + "…"
	}
	return h
}

func (p Publication) recurrences() []string {
	if len(p.Problem.Recurrences) == 0 {
		return []string{"No earlier incident with the same cause found."}
	}
	lines := []string{"Recurrences:"}
	for _, r := range p.Problem.Recurrences {
		line := "• " + oneLine(r.Record)
		if r.Date != nil {
			line += " (" + r.Date.UTC().Format("2006-01-02") + ")"
		}
		lines = append(lines, line)
	}
	return lines
}

func (p Publication) actions() []string {
	if len(p.Actions) == 0 && p.More == 0 {
		return []string{"No corrective action."}
	}
	lines := []string{fmt.Sprintf("Corrective actions: %d to do, %d in all", p.ToDo, len(p.Actions)+p.More)}
	for _, a := range p.Actions {
		lines = append(lines, fmt.Sprintf("• %s · %s · %s", a.State, oneLine(a.ID), oneLine(a.Text)))
	}
	if p.More > 0 {
		lines = append(lines, fmt.Sprintf("and %d more: kubectl -n %s get takecarereview %s -o yaml", p.More, p.Namespace, p.Review))
	}
	return lines
}

// postMortem says where the draft is: it is not posted.
func (p Publication) postMortem() string {
	return fmt.Sprintf("Post-mortem draft: kubectl -n %s get takecarereview %s -o jsonpath='{.status.postMortem}'", p.Namespace, p.Review)
}

// context is the line under the review: what it read, its usage, its
// trace.
func (p Publication) context() string {
	var parts []string
	switch n := len(p.Investigations); {
	case n == 1:
		parts = append(parts, "investigation "+p.Investigations[0])
	case n > 1:
		parts = append(parts, fmt.Sprintf("%d investigations: %s", n, strings.Join(p.Investigations, ", ")))
	case p.Period != nil:
		parts = append(parts, "incidents from "+p.Period.From.UTC().Format("2006-01-02")+" to "+p.Period.To.UTC().Format("2006-01-02"))
	}
	if u := p.Usage; u != nil {
		parts = append(parts, fmt.Sprintf("%d model calls", u.ModelCalls), report.Thousands(u.InputTokens+u.OutputTokens)+" tokens")
		if u.EstimatedCost != "" {
			cost := u.EstimatedCost
			if u.Approximate {
				cost += " (approximate)"
			}
			parts = append(parts, cost)
		}
	} else {
		parts = append(parts, "usage unknown: "+p.UsageReason)
	}
	return strings.Join(append(parts, "trace "+p.TraceID), " · ")
}

// oneLine keeps a model's short text on its line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Message renders the review for the report connector: Block Kit for
// Slack, and the same content as text, which the log adapter prints.
func (p Publication) Message() report.Message {
	return report.Message{Blocks: p.blocks(), Text: p.text()}
}

func (p Publication) blocks() []any {
	const kType, kText = "type", "text"
	// verbatim: Slack links no URL, channel name or mention it did not
	// get as markup, written by the model or carried by a name
	// (docs.slack.dev, text object); &, < and > are escaped.
	mrkdwn := func(s string) map[string]any { return map[string]any{kType: "mrkdwn", kText: s, "verbatim": true} }
	section := func(s string) map[string]any {
		return map[string]any{kType: "section", kText: mrkdwn(report.Section(s))}
	}
	context := func(s string) map[string]any {
		return map[string]any{kType: "context", "elements": []any{mrkdwn(report.Section(s))}}
	}
	blocks := []any{map[string]any{kType: "header", kText: map[string]any{kType: "plain_text", kText: p.header()}}}
	if p.Apprentice {
		blocks = append(blocks, context(":warning: *"+ApprenticeBanner+"*"))
	}
	if p.Problem.Statement != "" {
		blocks = append(blocks, section(report.Mrkdwn(p.Problem.Statement)))
	}
	blocks = append(blocks, section(report.Escape(strings.Join(p.recurrences(), "\n"))))
	// One section per group of lines under Block Kit's bound: ten
	// actions of 512 characters do not fit in one.
	var group []string
	size := 0
	for _, line := range p.actions() {
		line = report.Escape(line)
		if size+len(line)+1 > report.SectionLimit && len(group) > 0 {
			blocks, group, size = append(blocks, section(strings.Join(group, "\n"))), nil, 0
		}
		group, size = append(group, line), size+len(line)+1
	}
	blocks = append(blocks, section(strings.Join(group, "\n")))
	if p.PostMortem {
		blocks = append(blocks, section(report.Escape(p.postMortem())))
	}
	return append(blocks, context(report.Escape(p.context())))
}

func (p Publication) text() string {
	var b strings.Builder
	b.WriteString(p.header() + "\n")
	if p.Apprentice {
		b.WriteString(ApprenticeBanner + "\n")
	}
	if p.Problem.Statement != "" {
		b.WriteString(report.Plain(p.Problem.Statement) + "\n")
	}
	for _, l := range p.recurrences() {
		b.WriteString(l + "\n")
	}
	for _, l := range p.actions() {
		b.WriteString(l + "\n")
	}
	if p.PostMortem {
		b.WriteString(p.postMortem() + "\n")
	}
	b.WriteString(p.context() + "\n")
	return b.String()
}
