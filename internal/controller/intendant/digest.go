// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/report"
)

// DigestKeys is how many keys a digest names, in the status's order; the
// rest is counted (spec 10 §6). The keys' lines stay within keysBudget
// bytes, under Block Kit's 3000 for a section's text; a header's text is
// at most headerLimit characters (docs.slack.dev, Block Kit reference).
const (
	DigestKeys  = 20
	keysBudget  = 2800
	headerLimit = 150
)

// What a digest may print: a Map key (the schema's pattern, whatever its
// type) and a Kubernetes name. Neither admits Slack markup (& < > @ * _ ~
// or a backquote); a value that does not match is printed as "?" and never
// as itself.
var (
	safeKey  = regexp.MustCompile(`^[a-z]+/[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
	safeName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
)

func safe(re *regexp.Regexp, s string) string {
	if re.MatchString(s) {
		return s
	}
	return "?"
}

// Digest is the message of one survey's proposals (spec 10 §6): counts,
// keys and names, built from typed fields only; never an entry, its
// evidence or anything the agent wrote.
type Digest struct {
	Namespace, Intendant, Survey, ConfigMap, Revision string
	// Counts by action and by source, over the full set.
	Add, Update, Remove, Import, Agent int
	// Keys are the first DigestKeys proposals as "<Action> <key>"; More
	// counts the rest.
	Keys []string
	More int
	// NotSeen and BlindSpots count the survey's not-seen lines and blind
	// spots.
	NotSeen, BlindSpots int
}

// NewDigest builds the digest of a completed survey: the counts by action
// from its status, which counts the full set; the counts by source from
// all, the proposals its ConfigMap holds; and the keys of the first
// proposals in its status, in order.
func NewDigest(s *v1alpha1.TakeCareSurvey, all []v1alpha1.Proposal) Digest {
	d := Digest{
		Namespace: safe(safeName, s.Namespace), Intendant: safe(safeName, s.Spec.IntendantRef.Name), Survey: safe(safeName, s.Name),
		ConfigMap: safe(safeName, s.Status.ProposalsConfigMap), Revision: safe(safeName, s.Status.MapRevision),
		BlindSpots: len(s.Status.BlindSpots),
	}
	if c := s.Status.ProposalCounts; c != nil {
		d.Add, d.Update, d.Remove = int(c.Add), int(c.Update), int(c.Remove)
	}
	for _, l := range s.Status.Layers {
		d.NotSeen += len(l.NotSeen)
	}
	for _, p := range all {
		if p.Source == v1alpha1.ProposalFromAgent {
			d.Agent++
		} else {
			d.Import++
		}
	}
	used := 0
	for _, p := range s.Status.Proposals {
		line := action(p.Action) + " " + safe(safeKey, string(p.Key))
		if len(d.Keys) == DigestKeys || used+len(line)+1 > keysBudget {
			break
		}
		d.Keys, used = append(d.Keys, line), used+len(line)+1
	}
	d.More = max(d.Total()-len(d.Keys), 0)
	return d
}

// action is the API's spelling of a known action, "?" otherwise.
func action(a v1alpha1.ProposalAction) string {
	switch a {
	case v1alpha1.ProposalAdd, v1alpha1.ProposalUpdate, v1alpha1.ProposalRemove:
		return string(a)
	}
	return "?"
}

// Total is the number of proposals.
func (d Digest) Total() int { return d.Add + d.Update + d.Remove }

func (d Digest) header() string {
	h := fmt.Sprintf("Intendant %s: %d proposals to review", d.Intendant, d.Total())
	if len(h) > headerLimit {
		// A long name: the count stays, the name is cut (ASCII only).
		h = fmt.Sprintf("Intendant: %d proposals to review", d.Total())
	}
	return h
}

func (d Digest) counts() string {
	return fmt.Sprintf("Add %d · Update %d · Remove %d · from the import %d, from the agent %d", d.Add, d.Update, d.Remove, d.Import, d.Agent)
}

func (d Digest) keys() string {
	s := strings.Join(d.Keys, "\n")
	if d.More > 0 {
		s += "\nand " + strconv.Itoa(d.More) + " more"
	}
	return s
}

func (d Digest) context() string {
	return fmt.Sprintf("Survey %s against the Map at %s · %d not seen · %d blind spots · kubectl -n %s get takecaresurvey %s · the full set: ConfigMap %s",
		d.Survey, d.Revision, d.NotSeen, d.BlindSpots, d.Namespace, d.Survey, d.ConfigMap)
}

// Message renders the digest for the report connector: Block Kit blocks
// for Slack, and the same lines as text, which the log adapter prints.
func (d Digest) Message() report.Message {
	const kType, kText = "type", "text"
	text := func(kind, s string) map[string]any { return map[string]any{kType: kind, kText: s} }
	blocks := []any{
		map[string]any{kType: "header", kText: text("plain_text", d.header())},
		map[string]any{kType: "section", kText: text("mrkdwn", d.counts())},
		map[string]any{kType: "section", kText: text("mrkdwn", "```\n"+d.keys()+"\n```")},
		map[string]any{kType: "context", "elements": []any{text("mrkdwn", d.context())}},
	}
	return report.Message{Blocks: blocks, Text: d.header() + "\n" + d.counts() + "\n" + d.keys() + "\n" + d.context() + "\n"}
}
