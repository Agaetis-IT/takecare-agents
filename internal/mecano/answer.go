// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/answerblock"
	"github.com/agaetis-it/takecare-agents/internal/mecano/eos"
)

//go:embed answer.schema.json
var answerSchemaSource []byte

var answerSchema = func() *jsonschema.Resolved {
	var s jsonschema.Schema
	if err := json.Unmarshal(answerSchemaSource, &s); err != nil {
		panic(fmt.Sprintf("the embedded answer schema: %v", err))
	}
	r, err := s.Resolve(nil)
	if err != nil {
		panic(fmt.Sprintf("the embedded answer schema: %v", err))
	}
	return r
}()

// MaxAnswerBytes bounds the answer: past it nothing is read, since a
// partial read would be a guess.
const MaxAnswerBytes = 256 << 10

// ErrUnreadable is the one class of an answer that is not read; its
// message is the fixed phrase of the maintenance's status.reason (spec 12
// §9).
var ErrUnreadable = errors.New("the maintenance's answer could not be read: the inventory is kept without findings")

// Answer is the agent's answer, the shape of answer.schema.json.
type Answer struct {
	Findings     []AnswerFinding `json:"findings"`
	Plan         *AnswerPlan     `json:"plan"`
	PullRequests []AnswerPR      `json:"pullRequests"`
}

// AnswerFinding is one finding as the agent states it.
type AnswerFinding struct {
	ID        string               `json:"id"`
	Kind      v1alpha1.FindingKind `json:"kind"`
	Component string               `json:"component"`
	Text      string               `json:"text"`
	Severity  v1alpha1.Severity    `json:"severity"`
	Evidence  []v1alpha1.Evidence  `json:"evidence"`
}

// AnswerPlan is the plan as the agent states it.
type AnswerPlan struct {
	Steps []AnswerStep `json:"steps"`
}

// AnswerStep is one step of the plan.
type AnswerStep struct {
	ID       string               `json:"id"`
	Text     string               `json:"text"`
	Findings []string             `json:"findings"`
	Class    v1alpha1.ActionClass `json:"class"`
}

// AnswerPR is a pull request as the agent says propose_change returned it.
type AnswerPR struct {
	Repository string `json:"repository"`
	URL        string `json:"url"`
	Branch     string `json:"branch"`
}

// Parse reads the answer strictly: exactly one fenced json block, checked
// against answer.schema.json. Prose around the block is ignored. Whatever
// fails is ErrUnreadable, its detail never the model's text.
func Parse(text string) (Answer, error) {
	if len(text) > MaxAnswerBytes {
		return Answer{}, fmt.Errorf("%w: larger than %d bytes", ErrUnreadable, MaxAnswerBytes)
	}
	block, err := answerblock.JSON(text)
	if err != nil {
		return Answer{}, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	var instance any
	dec := json.NewDecoder(strings.NewReader(block))
	if err := dec.Decode(&instance); err != nil || strings.TrimSpace(block[dec.InputOffset():]) != "" {
		return Answer{}, fmt.Errorf("%w: not one JSON value", ErrUnreadable)
	}
	// The schema's and the decoder's errors quote the answer's names and
	// values: they are dropped for a fixed detail.
	if answerSchema.Validate(instance) != nil {
		return Answer{}, fmt.Errorf("%w: the json block does not match the answer's schema", ErrUnreadable)
	}
	strict := json.NewDecoder(bytes.NewReader([]byte(block)))
	strict.DisallowUnknownFields()
	var a Answer
	if strict.Decode(&a) != nil {
		return Answer{}, fmt.Errorf("%w: the json block does not decode as an answer", ErrUnreadable)
	}
	return a, nil
}

// Bounds of the maintenance's status (spec 12 §2), as its CRD states them.
const (
	maxFindings    = 50
	maxSteps       = 20
	maxStepID      = 63
	maxFindingID   = 320
	maxEvidence    = 10
	maxRef         = 253
	maxRepository  = 255
	maxURL         = 2048
	maxStepFinding = 50
	// MaxBlindSpots is the CRD's bound on blindSpots.
	MaxBlindSpots = 20
)

// Blind spots of the answer's reading: fixed phrases.
const (
	// UnknownFact: a finding named a component, an apiVersion or a version
	// the inventory does not hold (spec 12 §9).
	UnknownFact = "a finding named a version the inventory did not read: it was dropped"
	// ForeignPR: a pull request on a repository the maintenance may not
	// target, or on another branch than its own.
	ForeignPR = "a pull request outside the maintenance's repositories or branch: it was dropped"
	// SilentPR: a silent maintenance opened a pull request (spec 12 §6.5).
	SilentPR = "a silent maintenance opened a pull request: it was dropped, and the pass fails"
	// UnprovenPR: a pull request kept although the propose route's rule
	// was not proven at the maintenance's start.
	UnprovenPR = "a pull request opened although the gateway's rule on the propose route was not proven"
)

// Facts is what the answer is checked against: the inventory, what the
// copy runs, and the pull requests the maintenance may open.
type Facts struct {
	Inventory v1alpha1.MaintenanceInventory
	// Copy is what the team's copy runs, by component key: its version,
	// whether it is ready, and the evidence read.
	Copy map[string]CopyFact
	// Repositories may carry the maintenance's pull requests; Branch is
	// the one branch they use.
	Repositories []string
	Branch       string
	// Silent drops every pull request (the bench); Unproven keeps the
	// valid ones with a blind spot each (spec 12 §6.4).
	Silent, Unproven bool
	// ClusterVersion is the collector's, a version the findings may name.
	ClusterVersion string
	// Known are other versions a finding may name: the dataset's
	// Kubernetes minors and lines, so that an upgrade one minor at a time
	// can name each minor on the way.
	Known []string
}

// CopyFact is one component as the copy runs it.
type CopyFact struct {
	Version  string
	Ready    bool
	Evidence []v1alpha1.Evidence
}

// Recorded is what a maintenance's answer becomes in its status.
type Recorded struct {
	Findings        []v1alpha1.MaintenanceFinding
	FindingsOmitted int32
	Plan            *v1alpha1.MaintenancePlan
	Counts          v1alpha1.MaintenanceCounts
	BlindSpots      []v1alpha1.NotSeen
}

// Record turns an answer into the maintenance's status (spec 12 §2, §9,
// §10): each finding gets its stable id, one the inventory does not hold
// is dropped, what exceeds a bound is cut and counted; at Practitioner,
// the plan keeps the findings it answers that were kept, says OnCopy only
// with the copy's evidence, and keeps only the pull requests on the
// maintenance's repositories and branch.
func Record(a Answer, level v1alpha1.Level, f Facts) Recorded {
	var out Recorded
	known := knownFacts(f)
	seen := map[string]bool{}
	unknown := false
	for _, x := range a.Findings {
		id, ok := findingID(x, f.Inventory, known)
		if !ok {
			unknown = true
			continue
		}
		if seen[id] {
			continue
		}
		if len(out.Findings) == maxFindings {
			out.FindingsOmitted++
			continue
		}
		seen[id] = true
		out.Findings = append(out.Findings, v1alpha1.MaintenanceFinding{
			ID: id, Kind: x.Kind, Component: cut(x.Component, maxRef), Text: cut(x.Text, v1alpha1.MaxFindingText),
			Severity: x.Severity, Evidence: evidence(x.Evidence),
		})
		out.Counts.Findings++
		if x.Severity == v1alpha1.SeverityNow {
			out.Counts.Now++
		}
	}
	if unknown {
		out.spot("findings", UnknownFact)
	}
	if level == v1alpha1.LevelApprentice {
		return out
	}
	out.Plan = plan(a.Plan, seen, f)
	for _, pr := range a.PullRequests {
		out.pullRequest(pr, f)
	}
	out.Counts.PullRequests = int32(len(out.Plan.PullRequests)) //nolint:gosec // bounded by maxRepositories
	return out
}

func (out *Recorded) spot(ref, reason string) {
	n := v1alpha1.NotSeen{Ref: cut(ref, maxRef), Reason: reason}
	if len(out.BlindSpots) < MaxBlindSpots && !slices.Contains(out.BlindSpots, n) {
		out.BlindSpots = append(out.BlindSpots, n)
	}
}

// pullRequest keeps one pull request the agent reports, or drops it with
// a blind spot (spec 12 §6.5, §9).
func (out *Recorded) pullRequest(pr AnswerPR, f Facts) {
	ref := cut(pr.Repository, maxRef)
	switch {
	case f.Silent:
		out.spot(ref, SilentPR)
		return
	case !slices.Contains(f.Repositories, pr.Repository) || pr.Branch != f.Branch || !validURL(pr.URL):
		out.spot(ref, ForeignPR)
		return
	case slices.ContainsFunc(out.Plan.PullRequests, func(p v1alpha1.PullRequestRef) bool { return p.Repository == pr.Repository }):
		return // one per repository: the first stands
	}
	out.Plan.PullRequests = append(out.Plan.PullRequests, v1alpha1.PullRequestRef{Repository: pr.Repository, URL: pr.URL, Branch: pr.Branch})
	if f.Unproven {
		out.spot(ref, UnprovenPR)
	}
}

// prURL is the CRD's pattern of a pull request's URL: https, no user
// information.
var prURL = regexp.MustCompile(`^https://[^\s/?#@]+([/?#][^\s]*)?$`)

func validURL(u string) bool { return len(u) <= maxURL && prURL.MatchString(u) }

// plan records the plan's steps, each with the kept findings it answers,
// and whether the copy shows it working.
func plan(p *AnswerPlan, kept map[string]bool, f Facts) *v1alpha1.MaintenancePlan {
	out := &v1alpha1.MaintenancePlan{Tested: v1alpha1.PlanNotTested}
	if p == nil {
		return out
	}
	ids := map[string]bool{}
	for _, s := range p.Steps {
		if len(out.Steps) == maxSteps || len(s.ID) > maxStepID || ids[s.ID] {
			continue
		}
		ids[s.ID] = true
		step := v1alpha1.PlanStep{ID: s.ID, Text: cut(s.Text, v1alpha1.MaxFindingText), Class: s.Class}
		for _, id := range s.Findings {
			if kept[id] && !slices.Contains(step.Findings, id) && len(step.Findings) < maxStepFinding {
				step.Findings = append(step.Findings, id)
			}
		}
		out.Steps = append(out.Steps, step)
	}
	out.Tested, out.Evidence = tested(out.Steps, f)
	return out
}

// tested says OnCopy only when every component the plan's findings name
// runs on the copy, ready, at the version Git declares or the newest one
// known, other than the one live: the plan's target, read there (spec 12
// §8). Anything else is NotTested; the plan never claims a test it did
// not read.
func tested(steps []v1alpha1.PlanStep, f Facts) (v1alpha1.PlanTested, []v1alpha1.Evidence) {
	components := map[string]v1alpha1.InventoryComponent{}
	for _, c := range f.Inventory.Components {
		components[c.Key] = c
	}
	var ev []v1alpha1.Evidence
	checked := map[string]bool{}
	for _, s := range steps {
		for _, id := range s.Findings {
			key := componentOf(id)
			c, ok := components[key]
			if !ok || checked[key] {
				continue
			}
			checked[key] = true
			cp, onCopy := f.Copy[key]
			target := cp.Version != "" && cp.Version != c.Live && (cp.Version == c.Declared || cp.Version == c.Latest)
			if !onCopy || !cp.Ready || !target {
				return v1alpha1.PlanNotTested, nil
			}
			ev = append(ev, cp.Evidence...)
		}
	}
	if len(checked) == 0 || len(ev) == 0 {
		return v1alpha1.PlanNotTested, nil
	}
	return v1alpha1.PlanOnCopy, ev[:min(len(ev), maxEvidence)]
}

// componentOf is the component of a finding id <kind>/<component>; a
// removed API's id names no component.
func componentOf(id string) string {
	kind, rest, _ := strings.Cut(id, "/")
	if kind == string(v1alpha1.FindingRemovedAPI) {
		return ""
	}
	return rest
}

// findingID is a finding's stable id (spec 12 §2), from what the
// inventory holds: <kind>/<component>, or RemovedAPI/<apiVersion>/<kind>.
// It is false when the finding names a component, an apiVersion or a
// version the inventory does not hold.
func findingID(x AnswerFinding, inv v1alpha1.MaintenanceInventory, known map[string]bool) (string, bool) {
	for _, v := range versionPattern.FindAllString(x.Text, -1) {
		if !known[strings.TrimPrefix(v, "v")] {
			return "", false
		}
	}
	switch x.Kind {
	case v1alpha1.FindingRemovedAPI:
		// The agent names the apiVersion and kind in its id, the way the
		// instructions give it; the component may be any name.
		rest, ok := strings.CutPrefix(x.ID, string(v1alpha1.FindingRemovedAPI)+"/")
		i := strings.LastIndex(rest, "/")
		if !ok || i <= 0 {
			return "", false
		}
		apiVersion, kind := rest[:i], rest[i+1:]
		if !slices.ContainsFunc(inv.APIs, func(a v1alpha1.DeprecatedAPIUse) bool { return a.APIVersion == apiVersion && a.Kind == kind }) {
			return "", false
		}
		return bounded(x.ID)
	case v1alpha1.FindingCertificate:
		if !slices.ContainsFunc(inv.Certificates, func(c v1alpha1.CertificateState) bool {
			return x.Component == c.Name || x.Component == c.Namespace+"/"+c.Name
		}) {
			return "", false
		}
	case v1alpha1.FindingDrift:
		if !hasComponent(inv, x.Component) && !slices.ContainsFunc(inv.GitOps, func(g v1alpha1.GitOpsState) bool {
			return x.Component == g.Name || x.Component == g.Namespace+"/"+g.Name
		}) {
			return "", false
		}
	default:
		if !hasComponent(inv, x.Component) {
			return "", false
		}
	}
	return bounded(string(x.Kind) + "/" + x.Component)
}

func bounded(id string) (string, bool) { return id, len(id) <= maxFindingID }

func hasComponent(inv v1alpha1.MaintenanceInventory, key string) bool {
	return key != "" && slices.ContainsFunc(inv.Components, func(c v1alpha1.InventoryComponent) bool { return c.Key == key })
}

// versionPattern finds the versions a finding's text names: two or three
// dotted numbers, a leading v allowed. A date or a plain number is not one.
var versionPattern = regexp.MustCompile(`\bv?\d+\.\d+(\.\d+)?\b`)

// knownFacts are the versions a finding may name: each version of the
// inventory and its minor, the minors that remove an apiVersion, the
// cluster's version, and the copy's.
func knownFacts(f Facts) map[string]bool {
	known := map[string]bool{}
	add := func(v string) {
		v = strings.TrimPrefix(v, "v")
		if v == "" {
			return
		}
		known[v] = true
		if m := eos.MinorOf(v); m != "" {
			known[m] = true
		}
		// A version with a suffix (1.31.2-alpine) is named by its numbers.
		if n := versionPattern.FindString(v); n != "" {
			known[strings.TrimPrefix(n, "v")] = true
		}
	}
	for _, c := range f.Inventory.Components {
		add(c.Live)
		add(c.Declared)
		add(c.Latest)
	}
	for _, a := range f.Inventory.APIs {
		add(a.RemovedIn)
	}
	add(f.ClusterVersion)
	for _, v := range f.Known {
		add(v)
	}
	for _, c := range f.Copy {
		add(c.Version)
	}
	return known
}

func evidence(in []v1alpha1.Evidence) []v1alpha1.Evidence {
	var out []v1alpha1.Evidence
	for _, e := range in {
		if len(out) == maxEvidence {
			break
		}
		out = append(out, v1alpha1.Evidence{Tool: cut(e.Tool, maxRef), Record: cut(e.Record, maxRef)})
	}
	return out
}

// cut keeps at most n bytes of s, on a rune boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// FindingIDs is the set of a maintenance's finding ids, sorted: the
// Service log is the set of these ids across a Mécano's maintenances
// (spec 12 §2).
func FindingIDs(findings []v1alpha1.MaintenanceFinding) []string {
	ids := make([]string, 0, len(findings))
	for _, f := range findings {
		ids = append(ids, f.ID)
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// Changed says whether two maintenances' findings differ as sets of ids,
// and which ids are new in the current one: "changed since the last one"
// is a comparison of sets (spec 12 §11).
func Changed(current, previous []v1alpha1.MaintenanceFinding) (bool, []string) {
	cur, prev := FindingIDs(current), FindingIDs(previous)
	var added []string
	for _, id := range cur {
		if _, found := slices.BinarySearch(prev, id); !found {
			added = append(added, id)
		}
	}
	return !slices.Equal(cur, prev), added
}
