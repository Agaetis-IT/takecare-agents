// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package eos is the Mécano's end-of-support dataset (spec 12 §7.2): a
// YAML file versioned with each release and embedded in the operator. It
// gives each Kubernetes minor's end of life and latest patch, the
// apiVersions each minor removed, and the supported lines of the
// components TakeCare ships or reads. Every entry names its source page;
// the operator makes no online lookup.
package eos

import (
	_ "embed" // the dataset
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

//go:embed dataset.yaml
var embedded []byte

// MaxAge is how old the dataset may be before each maintenance says so
// (spec 12 §7.2).
const MaxAge = 6 * 30 * 24 * time.Hour

// Dataset is the parsed dataset.
type Dataset struct {
	// Date is when the dataset was last updated, YYYY-MM-DD.
	Date       string      `json:"date"`
	Kubernetes Kubernetes  `json:"kubernetes"`
	Components []Component `json:"components"`
}

// Kubernetes is the project's minors and removed apiVersions.
type Kubernetes struct {
	Source      string      `json:"source"`
	Minors      []Minor     `json:"minors"`
	RemovedAPIs RemovedAPIs `json:"removedAPIs"`
}

// Minor is one Kubernetes minor.
type Minor struct {
	Minor     string `json:"minor"`
	Latest    string `json:"latest"`
	EndOfLife string `json:"endOfLife"`
}

// RemovedAPIs is the Deprecated API Migration Guide's list.
type RemovedAPIs struct {
	Source  string       `json:"source"`
	Entries []RemovedAPI `json:"entries"`
}

// RemovedAPI is an apiVersion of a kind that a minor stops serving.
type RemovedAPI struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	RemovedIn  string `json:"removedIn"`
	// Replacement is empty when the guide names no other apiVersion.
	Replacement string `json:"replacement,omitempty"`
	Namespaced  bool   `json:"namespaced,omitempty"`
}

// Component is a product's supported lines.
type Component struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Match  Match  `json:"match"`
	Latest string `json:"latest"`
	Lines  []Line `json:"lines,omitempty"`
}

// Match says which running objects are the component: by image
// repository (registry host included), by chart name, or by their
// app.kubernetes.io/part-of label.
type Match struct {
	Images []string `json:"images,omitempty"`
	Charts []string `json:"charts,omitempty"`
	PartOf string   `json:"partOf,omitempty"`
}

// Line is one release cycle; SupportEnds is empty when the source gives
// none.
type Line struct {
	Line        string `json:"line"`
	Latest      string `json:"latest"`
	SupportEnds string `json:"supportEnds,omitempty"`
}

// Load parses the embedded dataset.
func Load() (*Dataset, error) { return Parse(embedded) }

// Parse reads a dataset strictly (an unknown field is refused) and checks
// it.
func Parse(raw []byte) (*Dataset, error) {
	var d Dataset
	if err := yaml.UnmarshalStrict(raw, &d); err != nil {
		return nil, fmt.Errorf("parse the end-of-support dataset: %w", err)
	}
	if err := d.validate(); err != nil {
		return nil, fmt.Errorf("end-of-support dataset: %w", err)
	}
	return &d, nil
}

var (
	minorRE   = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	versionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	groupRE   = regexp.MustCompile(`^([a-z0-9.-]+/)?v[0-9]+((alpha|beta)[0-9]+)?$`)
	kindRE    = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
)

func (d *Dataset) validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if !isDate(d.Date) {
		bad("date %q: a date, YYYY-MM-DD", d.Date)
	}
	errs = append(errs, d.Kubernetes.validate()...)
	return errors.Join(append(errs, d.validateComponents()...)...)
}

func (k Kubernetes) validate() []error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if !isSource(k.Source) || !isSource(k.RemovedAPIs.Source) {
		bad("kubernetes: the minors and the removed APIs each name their source page, https")
	}
	if len(k.Minors) == 0 {
		bad("kubernetes: no minor")
	}
	for i, m := range k.Minors {
		if !minorRE.MatchString(m.Minor) || !versionRE.MatchString(m.Latest) || MinorOf(m.Latest) != m.Minor || !isDate(m.EndOfLife) {
			bad("kubernetes minor %q: a minor, its latest patch on it and its end of life", m.Minor)
		}
		if i > 0 && Compare(m.Minor, k.Minors[i-1].Minor) >= 0 {
			bad("kubernetes minor %q: the minors go newest first", m.Minor)
		}
	}
	seen := map[string]bool{}
	for _, r := range k.RemovedAPIs.Entries {
		key := r.APIVersion + " " + r.Kind
		switch {
		case !groupRE.MatchString(r.APIVersion) || !kindRE.MatchString(r.Kind) || !minorRE.MatchString(r.RemovedIn):
			bad("removed API %s: an apiVersion, a kind and the minor that removed it", key)
		case r.Replacement != "" && !groupRE.MatchString(r.Replacement):
			bad("removed API %s: replacement %q is not an apiVersion", key, r.Replacement)
		case seen[key]:
			bad("removed API %s: listed twice", key)
		}
		seen[key] = true
	}
	return errs
}

func (d *Dataset) validateComponents() []error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	names := map[string]bool{}
	for _, c := range d.Components {
		switch {
		case c.Name == "" || names[c.Name]:
			bad("component %q: a name, once", c.Name)
		case !isSource(c.Source):
			bad("component %s: its source page, https", c.Name)
		case len(c.Match.Images) == 0 && len(c.Match.Charts) == 0 && c.Match.PartOf == "":
			bad("component %s: matches nothing", c.Name)
		case !versionRE.MatchString(c.Latest):
			bad("component %s: latest %q is not a version", c.Name, c.Latest)
		}
		names[c.Name] = true
		for i, l := range c.Lines {
			if !minorRE.MatchString(l.Line) || !versionRE.MatchString(l.Latest) || MinorOf(l.Latest) != l.Line ||
				(l.SupportEnds != "" && !isDate(l.SupportEnds)) {
				bad("component %s line %q: a line, its latest version on it, an optional end of support", c.Name, l.Line)
			}
			if i > 0 && Compare(l.Line, c.Lines[i-1].Line) >= 0 {
				bad("component %s line %q: the lines go newest first", c.Name, l.Line)
			}
		}
		if len(c.Lines) > 0 && c.Lines[0].Latest != c.Latest {
			bad("component %s: latest %s is not its newest line's", c.Name, c.Latest)
		}
	}
	return errs
}

func isDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

func isSource(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// Updated is the dataset's date.
func (d *Dataset) Updated() time.Time {
	t, _ := time.Parse(time.DateOnly, d.Date) // checked by Parse
	return t
}

// Stale says whether the dataset is older than MaxAge at now.
func (d *Dataset) Stale(now time.Time) bool { return now.Sub(d.Updated()) > MaxAge }

// Minor returns the entry of a Kubernetes minor ("1.33"), nil when the
// dataset does not list it.
func (d *Dataset) Minor(minor string) *Minor {
	for i := range d.Kubernetes.Minors {
		if d.Kubernetes.Minors[i].Minor == minor {
			return &d.Kubernetes.Minors[i]
		}
	}
	return nil
}

// Removed returns the entry of an apiVersion and kind, nil when the guide
// does not list it.
func (d *Dataset) Removed(apiVersion, kind string) *RemovedAPI {
	for i, r := range d.Kubernetes.RemovedAPIs.Entries {
		if r.APIVersion == apiVersion && r.Kind == kind {
			return &d.Kubernetes.RemovedAPIs.Entries[i]
		}
	}
	return nil
}

// LineOf returns the component's line a version is on, nil when the dataset
// does not list it.
func (c *Component) LineOf(version string) *Line {
	line := MinorOf(version)
	for i := range c.Lines {
		if c.Lines[i].Line == line {
			return &c.Lines[i]
		}
	}
	return nil
}

// MinorOf is a version's MAJOR.MINOR, a leading "v" and any build or
// pre-release suffix dropped ("v1.33.4+k3s1" is "1.33"); empty when the
// version does not start with two numbers.
func MinorOf(version string) string {
	v := strings.TrimPrefix(version, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	minor := parts[1]
	if i := strings.IndexFunc(minor, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		minor = minor[:i]
	}
	if _, err := strconv.Atoi(parts[0]); err != nil || minor == "" {
		return ""
	}
	return parts[0] + "." + minor
}

// Compare compares two versions or lines numerically, element by element
// ("1.10" after "1.9"), a leading "v" and any suffix after the numbers
// ignored; a missing element counts as 0.
func Compare(a, b string) int {
	na, nb := numbers(a), numbers(b)
	for i := range max(len(na), len(nb)) {
		x, y := 0, 0
		if i < len(na) {
			x = na[i]
		}
		if i < len(nb) {
			y = nb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func numbers(v string) []int {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out []int
	for p := range strings.SplitSeq(v, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}
