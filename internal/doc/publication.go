// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package doc

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// ApprenticeBanner opens every message at the Apprentice level (spec 02 §4).
const ApprenticeBanner = "Apprentice: shadow diagnosis, no one was paged by TakeCare."

// sectionLimit is Slack's limit on a section's text; keyText is Block
// Kit's text field.
const (
	sectionLimit = 3000
	keyText      = "text"
	keyType      = "type"
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
	if p.Apprentice {
		blocks = append(blocks, map[string]any{keyType: "context", "elements": []any{text("mrkdwn", ":warning: *"+ApprenticeBanner+"*")}})
	}
	body := mrkdwn(p.Diagnosis)
	if len(body) > sectionLimit {
		// Cut on a rune boundary, leaving room for the ellipsis' 3 bytes,
		// and never inside an escape such as &amp;.
		cut := sectionLimit - len("…")
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		if i := strings.LastIndexByte(body[:cut], '&'); i >= 0 && cut-i < len("&amp;") && !strings.Contains(body[i:cut], ";") {
			cut = i
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
	if p.Apprentice {
		b.WriteString(ApprenticeBanner + "\n")
	}
	b.WriteString(plain(p.Diagnosis) + "\n")
	b.WriteString(p.context() + "\n")
	for _, n := range p.notes() {
		b.WriteString(n + "\n")
	}
	return b.String()
}

// The model writes Markdown; Slack's mrkdwn is another syntax
// (https://docs.slack.dev/messaging/formatting-message-text).
var (
	// Slack shows no headings: a heading becomes a bold line.
	mdHeading = regexp.MustCompile(`^#{1,6}[ \t]+(.+?)[ \t#]*$`)
	// Slack's bold is one asterisk; Markdown's is two, or two underscores.
	mdBold = regexp.MustCompile(`\*\*(\S(?:.*?\S)??)\*\*`)
	// Underscores only between word boundaries: A__B__C is an identifier.
	mdBoldUnder = regexp.MustCompile(`(^|[^\pL\pN_])__(\S(?:.*?\S)??)__([^\pL\pN_]|$)`)
	// Slack renders no Markdown list: a dash or an asterisk becomes a bullet.
	mdBullet = regexp.MustCompile(`^([ \t]*)[-*][ \t]+`)
	// Slack's link is <url|text>.
	mdLink = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	// Slack requires &, < and > to be escaped in mrkdwn text, code included.
	mrkdwnEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
)

// fence reports whether a line opens or closes a fenced code block, whose
// content is only escaped.
func fence(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "```")
}

// unbold replaces bold markers with mark on each side. The underscore
// form runs until stable: a match consumes the character after it, which
// may open the next one (__a__ __b__).
func unbold(line, mark string) string {
	line = mdBold.ReplaceAllString(line, mark+"$1"+mark)
	for {
		next := mdBoldUnder.ReplaceAllString(line, "$1"+mark+"$2"+mark+"$3")
		if next == line {
			return line
		}
		line = next
	}
}

// mrkdwn renders the model's Markdown as Slack mrkdwn, line by line.
func mrkdwn(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	code := false
	for _, line := range lines {
		line = strings.TrimRight(mrkdwnEscape.Replace(line), " \t\r")
		if fence(line) || code {
			code = code != fence(line)
			out = append(out, line)
			continue
		}
		line = unbold(line, "*")
		if m := mdHeading.FindStringSubmatch(line); m != nil {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			line = "*" + strings.Trim(m[1], "*") + "*"
		}
		line = mdBullet.ReplaceAllString(line, "$1• ")
		out = append(out, mdLink.ReplaceAllString(line, "<$2|$1>"))
	}
	return strings.Join(out, "\n")
}

// plain drops the Markdown markers a reader would see in plain text:
// heading marks, bold marks; a link becomes "text (url)".
func plain(s string) string {
	lines := strings.Split(s, "\n")
	code := false
	for i, line := range lines {
		if fence(line) || code {
			code = code != fence(line)
			continue
		}
		line = unbold(line, "")
		line = mdHeading.ReplaceAllString(line, "$1")
		lines[i] = mdLink.ReplaceAllString(line, "$1 ($2)")
	}
	return strings.Join(lines, "\n")
}

// thousands writes 42560 as 42,560.
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
