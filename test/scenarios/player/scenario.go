// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/test/scenarios"
)

var schema = mustResolve(scenarios.Schema)

func mustResolve(raw []byte) *jsonschema.Resolved {
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		panic(fmt.Sprintf("the scenario schema: %v", err))
	}
	r, err := s.Resolve(nil)
	if err != nil {
		panic(fmt.Sprintf("the scenario schema: %v", err))
	}
	return r
}

// Scenario is one scenario file (spec 14 §2).
type Scenario struct {
	APIVersion string      `json:"apiVersion"`
	Kind       string      `json:"kind"`
	ID         string      `json:"id"`
	Version    int         `json:"version"`
	Title      string      `json:"title"`
	Ground     Ground      `json:"ground"`
	Variant    string      `json:"variant"`
	Fault      Fault       `json:"fault"`
	Causes     Causes      `json:"causes"`
	Evidence   Evidence    `json:"evidence"`
	BlindSpots []BlindSpot `json:"blindSpots"`
	SafeAction SafeAction  `json:"safeAction"`
	Passes     int         `json:"passes"`
	Cutoff     Cutoff      `json:"cutoff"`
	Reserved   bool        `json:"reserved"`
	window     [2]time.Duration
}

// Ref is how a report names the scenario: <id>@<version> (spec 14 §3.1).
func (s *Scenario) Ref() string { return fmt.Sprintf("%s@%d", s.ID, s.Version) }

// Ground is where the scenario is played (spec 14 §3.2).
type Ground struct {
	Type       string   `json:"type"`
	Map        string   `json:"map"`
	Connectors []string `json:"connectors"`
}

// Fault is what breaks and what the player waits for (spec 14 §3.4).
type Fault struct {
	Summary   string    `json:"summary"`
	Injection Injection `json:"injection"`
	Signals   Signals   `json:"signals"`
}

// Injection says who injects the fault, and how.
type Injection struct {
	Mode   string   `json:"mode"`
	Driver string   `json:"driver"`
	Steps  []string `json:"steps"`
}

// Signals is what starts a pass.
type Signals struct {
	Trigger string   `json:"trigger"`
	Alerts  []string `json:"alerts"`
	Window  Window   `json:"window"`
}

// Window is when the signals are expected after the injection.
type Window struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Causes are the scenario's causes; None is the written-out "none" of a
// false alarm (spec 14 §3.5).
type Causes struct {
	None  bool
	Items []Cause
}

// UnmarshalJSON reads "none" or a list.
func (c *Causes) UnmarshalJSON(b []byte) error {
	if string(b) == `"none"` {
		*c = Causes{None: true}
		return nil
	}
	return strictJSON(b, &c.Items)
}

// MarshalJSON writes "none" or the list.
func (c Causes) MarshalJSON() ([]byte, error) {
	if c.None {
		return []byte(`"none"`), nil
	}
	return json.Marshal(c.Items)
}

// Cause is one cause, on an entry of the Map.
type Cause struct {
	Entity    string   `json:"entity"`
	With      []string `json:"with"`
	Statement string   `json:"statement"`
	Match     Matcher  `json:"match"`
}

// Evidence is what a good diagnosis shows, and the leads it should not
// follow (spec 14 §3.6).
type Evidence struct {
	Expected    []Expected   `json:"expected"`
	RedHerrings []RedHerring `json:"redHerrings"`
}

// Expected is a finding reported found or not found.
type Expected struct {
	ID    string  `json:"id"`
	Match Matcher `json:"match"`
}

// RedHerring is a lead reported absent, mentioned or concluded.
type RedHerring struct {
	ID        string   `json:"id"`
	Lure      string   `json:"lure"`
	Match     Matcher  `json:"match"`
	Concluded *Matcher `json:"concluded,omitempty"`
}

// BlindSpot is a blind spot the role is expected to have (spec 14 §3.7).
type BlindSpot struct {
	Key         string `json:"key"`
	Reason      string `json:"reason"`
	MustDeclare bool   `json:"mustDeclare"`
}

// SafeAction is the highest class of action allowed, and what would do
// harm (spec 14 §3.8).
type SafeAction struct {
	Class     string      `json:"class"`
	Remedy    string      `json:"remedy"`
	Dangerous []Dangerous `json:"dangerous"`
}

// Dangerous is an action that fails the pass when it matches.
type Dangerous struct {
	ID    string  `json:"id"`
	Match Matcher `json:"match"`
}

// Cutoff is the time past which nothing exists, for a replay; None is the
// written-out "none" of a live scenario (spec 14 §3.10).
type Cutoff struct {
	None     bool
	At       string   `json:"at"`
	Excluded []string `json:"excluded"`
}

// UnmarshalJSON reads "none" or an object.
func (c *Cutoff) UnmarshalJSON(b []byte) error {
	if string(b) == `"none"` {
		*c = Cutoff{None: true}
		return nil
	}
	type plain Cutoff
	var p plain
	if err := strictJSON(b, &p); err != nil {
		return err
	}
	*c = Cutoff(p)
	return nil
}

// Matcher says a diagnosis shows a finding: exactly one kind is set
// (spec 14 §3.6).
type Matcher struct {
	Text            *TextMatch        `json:"text,omitempty"`
	Record          string            `json:"record,omitempty"`
	RequiresContent bool              `json:"requiresContent,omitempty"`
	Entity          string            `json:"entity,omitempty"`
	Tool            string            `json:"tool,omitempty"`
	Args            map[string]string `json:"args,omitempty"`
	AllOf           []Matcher         `json:"allOf,omitempty"`
	AnyOf           []Matcher         `json:"anyOf,omitempty"`

	all, anyOf, none []*regexp.Regexp
	record           *regexp.Regexp
	args             map[string]*regexp.Regexp
}

// TextMatch holds Go regular expressions, case-insensitive.
type TextMatch struct {
	All  []string `json:"all,omitempty"`
	Any  []string `json:"any,omitempty"`
	None []string `json:"none,omitempty"`
}

// Load reads one scenario file: the schema first, then a strict decoding,
// then its regular expressions and durations. Semantic rules and the
// ground's keys are Check's.
func Load(path string) (*Scenario, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a path the player is given
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return parse(raw)
}

func parse(raw []byte) (*Scenario, error) {
	doc, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("not YAML: %w", err)
	}
	var instance any
	if err := json.Unmarshal(doc, &instance); err != nil {
		return nil, fmt.Errorf("not an object: %w", err)
	}
	if err := schema.Validate(instance); err != nil {
		return nil, fmt.Errorf("does not match test/scenarios/schema.json: %w", err)
	}
	var s Scenario
	if err := strictJSON(doc, &s); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if err := s.compile(); err != nil {
		return nil, err
	}
	return &s, nil
}

// strictJSON decodes refusing unknown fields: a misspelled key would
// otherwise score nothing.
func strictJSON(b []byte, v any) error {
	return yaml.Unmarshal(b, v, yaml.DisallowUnknownFields)
}

// compile parses the window and compiles every matcher; every fault is
// returned at once.
func (s *Scenario) compile() error {
	var errs []error
	for i, d := range []string{s.Fault.Signals.Window.From, s.Fault.Signals.Window.To} {
		v, err := time.ParseDuration(d)
		if err != nil {
			errs = append(errs, fmt.Errorf("fault.signals.window: %w", err))
		}
		s.window[i] = v
	}
	s.eachMatcher(func(where string, m *Matcher) {
		if err := m.compile(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
		}
	})
	return errors.Join(errs...)
}

// eachMatcher calls fn on every top-level matcher, naming where it is.
func (s *Scenario) eachMatcher(fn func(where string, m *Matcher)) {
	for i := range s.Causes.Items {
		fn(fmt.Sprintf("causes[%d].match", i), &s.Causes.Items[i].Match)
	}
	for i := range s.Evidence.Expected {
		fn("evidence.expected."+s.Evidence.Expected[i].ID, &s.Evidence.Expected[i].Match)
	}
	for i := range s.Evidence.RedHerrings {
		h := &s.Evidence.RedHerrings[i]
		fn("evidence.redHerrings."+h.ID, &h.Match)
		if h.Concluded != nil {
			fn("evidence.redHerrings."+h.ID+".concluded", h.Concluded)
		}
	}
	for i := range s.SafeAction.Dangerous {
		fn("safeAction.dangerous."+s.SafeAction.Dangerous[i].ID, &s.SafeAction.Dangerous[i].Match)
	}
}

// walk calls fn on m and every matcher it combines.
func (m *Matcher) walk(fn func(*Matcher)) {
	fn(m)
	for i := range m.AllOf {
		m.AllOf[i].walk(fn)
	}
	for i := range m.AnyOf {
		m.AnyOf[i].walk(fn)
	}
}

func (m *Matcher) compile() error {
	var errs []error
	re := func(p string) *regexp.Regexp {
		r, err := regexp.Compile("(?i)" + p)
		if err != nil {
			errs = append(errs, fmt.Errorf("regular expression %q: %w", p, err))
		}
		return r
	}
	m.walk(func(m *Matcher) {
		if m.Text != nil {
			m.all, m.anyOf, m.none = nil, nil, nil
			for _, p := range m.Text.All {
				m.all = append(m.all, re(p))
			}
			for _, p := range m.Text.Any {
				m.anyOf = append(m.anyOf, re(p))
			}
			for _, p := range m.Text.None {
				m.none = append(m.none, re(p))
			}
		}
		if m.Record != "" {
			m.record = recordRE(recordID(m.Record))
		}
		if len(m.Args) > 0 {
			m.args = map[string]*regexp.Regexp{}
			for k, p := range m.Args {
				m.args[k] = re(p)
			}
		}
	})
	return errors.Join(errs...)
}

// recordRE matches a record id alone, not inside a longer one
// (CI-SEED-INFRA is not CI-SEED-INFRA-2).
func recordRE(id string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(id) + `(?:$|[^A-Za-z0-9_-])`)
}

// recordID is the id of <connector>/<id>.
func recordID(ref string) string {
	_, id, _ := strings.Cut(ref, "/")
	return id
}

// recordSource is the connector of <connector>/<id>.
func recordSource(ref string) string {
	source, _, _ := strings.Cut(ref, "/")
	return source
}
