// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package surveyagent

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	mapschema "github.com/agaetis-it/takecare-agents/api/map"
	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
)

// Layer is the survey layer of the agent pass (spec 10 §5).
const Layer = "agent"

const runbookPrefix = "runbook/"

// Bounds of one item: past either, the item is dropped.
const (
	maxTargets  = 50
	maxEvidence = 20
)

// The schema's patterns (api/map/entry.schema.json): a Map key and a
// knowledge source. A test keeps them equal to the schema's.
var (
	keyPattern       = regexp.MustCompile(`^(service|compute|datastore|storage|network|identity|external|team|environment|observability|runbook)/[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
	knowledgePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?/[^/\s]+$`)
)

// Why an item is dropped, in the order of the not-seen lines.
const (
	dropSize     = "size"
	dropType     = "type"
	dropSource   = "source"
	dropEvidence = "evidence"
	dropScope    = "scope"
	dropTarget   = "target"
	dropRemoved  = "removed"
	dropEntry    = "entry"
)

var dropOrder = []string{dropSize, dropType, dropSource, dropEvidence, dropScope, dropTarget, dropRemoved, dropEntry}

// Ground is what the agent's items are checked against.
type Ground struct {
	// Entries are the Map's entries at the survey's revision.
	Entries []mapserver.Entry
	Scope   v1alpha1.Scope
	// Tools are the names of the tools the agent was given.
	Tools []string
	// Today is the date a new entry is created on (YYYY-MM-DD).
	Today string
}

// Merge joins the agent's items to the import's proposals (spec 10 §5).
//
// No free text can pass: every string of an accepted item is a Map key
// (the schema's pattern), a knowledge source (its pattern) or the name of
// a tool the agent was given; a string that is none of these drops the
// item. That is how a proposal never carries a value, rather than trusting
// the instructions to say so.
//
// An item is a patch: relations to add, a knowledge source on a runbook.
// It is applied onto the import's proposal for its key if there is one,
// else onto the Map's entry (an Update that keeps the entry's status and
// provenance), else onto a new entry, proposed. The agent only adds: it
// never removes a relation, a source or an entry. One proposal per key
// results, with its stable id; a proposal the agent changed has the source
// Agent. What is dropped is counted, by reason, on the agent's layer.
func Merge(res discovery.Result, a Answer, g Ground) discovery.Result {
	m := newMerger(res, g)
	layer := discovery.Layer{Name: Layer, Seen: int32(len(a.Proposals))} //nolint:gosec // bounded by MaxItems
	// The runbooks first: a new runbook becomes a target the other items
	// may name only once its own item has given a proposal.
	var runbooks, others []Item
	for _, it := range a.Proposals {
		if strings.HasPrefix(it.Key, runbookPrefix) {
			runbooks = append(runbooks, it)
		} else {
			others = append(others, it)
		}
	}
	for _, it := range slices.Concat(runbooks, others) {
		if reason := m.check(it); reason != "" {
			m.dropped[reason]++
			continue
		}
		switch matched, changed, reason := m.apply(it); {
		case reason != "":
			m.dropped[reason]++
		case changed:
			m.known[it.Key] = true
			layer.Proposed++
			if matched {
				layer.Matched++
			}
		}
	}
	for _, r := range dropOrder {
		if n := m.dropped[r]; n > 0 {
			layer.NotSeen = append(layer.NotSeen, discovery.NotSeen{Ref: Layer, Reason: fmt.Sprintf("%d agent proposals dropped: %s", n, r)})
		}
	}
	out := discovery.Result{Layers: append(slices.Clone(res.Layers), layer), Proposals: m.proposals}
	return out
}

// NotRead is the result when the agent's answer is not read, or the pass
// did not finish: the import's proposals, and the agent's layer saying why.
func NotRead(res discovery.Result, reason string) discovery.Result {
	layer := discovery.Layer{Name: Layer, NotSeen: []discovery.NotSeen{{Ref: Layer, Reason: reason}}}
	return discovery.Result{Layers: append(slices.Clone(res.Layers), layer), Proposals: slices.Clone(res.Proposals)}
}

type merger struct {
	g         Ground
	proposals []discovery.Proposal
	// byKey indexes the proposals by key.
	byKey   map[string]int
	entries map[string]mapserver.Entry
	inScope map[string]bool
	known   map[string]bool
	tools   map[string]bool
	dropped map[string]int
}

func newMerger(res discovery.Result, g Ground) *merger {
	m := &merger{
		g: g, proposals: slices.Clone(res.Proposals), byKey: map[string]int{},
		entries: map[string]mapserver.Entry{}, inScope: map[string]bool{}, known: map[string]bool{},
		tools: map[string]bool{}, dropped: map[string]int{},
	}
	removed := map[string]bool{}
	for i, p := range m.proposals {
		m.byKey[p.Key], m.inScope[p.Key] = i, true
		if p.Action == v1alpha1.ProposalRemove {
			removed[p.Key] = true
		} else {
			m.known[p.Key] = true
		}
	}
	for _, t := range g.Tools {
		m.tools[t] = true
	}
	namespaces := map[string]bool{}
	for _, ns := range g.Scope.Namespaces {
		namespaces[string(ns)] = true
		m.inScope["environment/"+string(ns)] = true
	}
	var scoped []mapserver.Entry
	for _, e := range g.Entries {
		// An entry this survey proposes to remove is no target.
		m.entries[e.Key], m.known[e.Key] = e, !removed[e.Key]
		if slices.Contains(g.Scope.Entities, v1alpha1.EntityKey(e.Key)) || inNamespaces(e, namespaces) || m.inScope[e.Key] {
			m.inScope[e.Key] = true
			scoped = append(scoped, e)
		}
	}
	for _, e := range scoped {
		r := e.Relations
		for _, k := range slices.Concat(r.DependsOn, r.RunsAs, r.BelongsTo, r.DeployedIn, r.ObservedBy, r.DocumentedBy) {
			m.inScope[k] = true
		}
	}
	return m
}

// inNamespaces says whether an entry has a kubernetes source in one of
// the scope's namespaces, on any cluster: the scope's namespaces are not
// cluster-qualified, as for the Doc's coverage (ADR 0016 §5).
func inNamespaces(e mapserver.Entry, namespaces map[string]bool) bool {
	for _, s := range e.Sources {
		if ref, ok := s["kubernetes"]; ok {
			if src, ok := mapschema.ParseKubernetes(ref); ok && namespaces[src.Namespace] {
				return true
			}
		}
	}
	return false
}

// check returns why an item is dropped before it is applied, or "".
func (m *merger) check(it Item) string {
	targets := slices.Concat(it.Relations.DependsOn, it.Relations.BelongsTo, it.Relations.DocumentedBy)
	if len(targets) > maxTargets || len(it.Evidence) > maxEvidence || len(it.Sources) > maxTargets {
		return dropSize
	}
	for _, k := range append([]string{it.Key}, targets...) {
		if !keyPattern.MatchString(k) {
			return dropType
		}
	}
	for _, t := range it.Relations.BelongsTo {
		if !strings.HasPrefix(t, "team/") {
			return dropType
		}
	}
	for _, s := range it.Sources {
		if !knowledgePattern.MatchString(s.Knowledge) || !strings.HasPrefix(it.Key, runbookPrefix) {
			return dropSource
		}
	}
	if len(it.Evidence) == 0 {
		return dropEvidence
	}
	for _, e := range it.Evidence {
		if !m.tools[e] {
			return dropEvidence
		}
	}
	if !m.inScope[it.Key] && !strings.HasPrefix(it.Key, runbookPrefix) {
		return dropScope
	}
	for _, k := range targets {
		if !m.known[k] {
			return dropTarget
		}
	}
	return ""
}

// apply patches the item's entry and records its proposal. matched says
// it changed an entry the import or the Map already had; changed says a
// proposal resulted.
func (m *merger) apply(it Item) (matched, changed bool, reason string) {
	var e mapserver.Entry
	action := v1alpha1.ProposalAdd
	i, proposed := m.byKey[it.Key]
	switch existing, inMap := m.entries[it.Key]; {
	case proposed && m.proposals[i].Action == v1alpha1.ProposalRemove:
		return false, false, dropRemoved
	case proposed:
		if err := yaml.Unmarshal([]byte(m.proposals[i].Entry), &e); err != nil {
			return false, false, dropEntry
		}
		action, matched = m.proposals[i].Action, true
	case inMap:
		e, action, matched = clone(existing), v1alpha1.ProposalUpdate, true
	default:
		e = mapserver.Entry{Key: it.Key, Provenance: "proposed", Status: "proposed", Created: m.g.Today}
	}
	changed = addAll(&e.Relations.DependsOn, it.Relations.DependsOn)
	changed = addAll(&e.Relations.BelongsTo, it.Relations.BelongsTo) || changed
	changed = addAll(&e.Relations.DocumentedBy, it.Relations.DocumentedBy) || changed
	for _, s := range it.Sources {
		if !slices.ContainsFunc(e.Sources, func(x map[string]string) bool { return x["knowledge"] == s.Knowledge }) {
			e.Sources, changed = append(e.Sources, map[string]string{"knowledge": s.Knowledge}), true
		}
	}
	if !changed {
		return matched, false, ""
	}
	if err := mapserver.ValidateEntry(e); err != nil {
		return matched, false, dropEntry
	}
	raw, err := yaml.Marshal(e)
	if err != nil {
		return matched, false, dropEntry
	}
	if proposed {
		p := &m.proposals[i]
		p.Entry, p.Source = string(raw), v1alpha1.ProposalFromAgent
		addAll(&p.Evidence, it.Evidence)
		return matched, true, ""
	}
	m.byKey[it.Key] = len(m.proposals)
	m.proposals = append(m.proposals, discovery.Proposal{
		ID: discovery.ProposalID(action, it.Key), Action: action, Key: it.Key, Entry: string(raw),
		Evidence: slices.Clone(it.Evidence), Source: v1alpha1.ProposalFromAgent, Layer: Layer,
	})
	return matched, true, ""
}

// addAll appends what dst lacks of src, keeping dst's order; it says
// whether anything was added.
func addAll(dst *[]string, src []string) bool {
	added := false
	for _, s := range src {
		if !slices.Contains(*dst, s) {
			*dst, added = append(*dst, s), true
		}
	}
	return added
}

// clone copies an entry deeply enough to patch it.
func clone(e mapserver.Entry) mapserver.Entry {
	e.Sources = slices.Clone(e.Sources)
	r := &e.Relations
	r.DependsOn, r.BelongsTo, r.DocumentedBy = slices.Clone(r.DependsOn), slices.Clone(r.BelongsTo), slices.Clone(r.DocumentedBy)
	return e
}
