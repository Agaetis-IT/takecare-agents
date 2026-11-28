// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Command map-at-scale counts what the Intendant's survey would propose on
// rendered manifests (spike: docs/spikes/003-map-at-scale.md). It runs the
// product's own import, internal/discovery.Import, against an in-process
// fake of the kubernetes add-on that serves the rendered objects (see
// fakeGround for what that bypasses), with and without candidate noise
// filters, and measures the two-tier Map's residue and the week-to-week
// churn of replayed Git histories.
//
//	go run ./spikes/map-at-scale -estate <dir> -history name=<dir> ... -json <file>
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
)

var layers = []string{
	discovery.LayerEnvironment, discovery.LayerCompute, discovery.LayerService,
	discovery.LayerNetwork, discovery.LayerIdentity, discovery.LayerStorage,
}

// filter is a candidate noise filter: objects it drops before the survey
// lists them, namespaces it takes out of the survey's scope.
type filter struct {
	name      string
	drop      func(*obj) bool
	excludeNS func(string) bool
}

func never(*obj) bool  { return false }
func noNS(string) bool { return false }
func systemNS(n string) bool {
	return strings.HasSuffix(n, "-system") || strings.HasPrefix(n, "preview-")
}

// cloudRole says whether a ServiceAccount is bound to a cloud identity
// (EKS IRSA, GKE Workload Identity, Azure Workload Identity).
func cloudRole(o *obj) bool {
	for _, k := range []string{"eks.amazonaws.com/role-arn", "iam.gke.io/gcp-service-account", "azure.workload.identity/client-id"} {
		if o.annotations[k] != "" || o.labels[k] != "" {
			return true
		}
	}
	return false
}

var single = []filter{
	{"none", never, noNS},
	{"system/ephemeral namespaces", never, systemNS},
	{"owner-referenced", func(o *obj) bool { return o.owned }, noNS},
	{"StatefulSet PVCs", func(o *obj) bool { return o.stsPVC }, noNS},
	{"ServiceAccounts without a cloud role", func(o *obj) bool { return o.kind == "ServiceAccount" && !cloudRole(o) }, noNS},
	{"headless Services", func(o *obj) bool { return o.headless }, noNS},
}

var all = filter{"all filters", func(o *obj) bool {
	for _, f := range single[1:] {
		if f.drop(o) {
			return true
		}
	}
	return false
}, systemNS}

func filters() []filter { return append(slices.Clone(single), all) }

func apply(s *snapshot, f filter) []*obj {
	return slices.DeleteFunc(slices.Clone(s.objects), f.drop)
}

// count is one survey's outcome.
type count struct {
	Filter    string         `json:"filter"`
	Proposals int            `json:"proposals"`
	ByLayer   map[string]int `json:"byLayer"`
	Invalid   int            `json:"invalidEntries"`
}

func countOf(name string, res discovery.Result) count {
	c := count{Filter: name, ByLayer: map[string]int{}}
	for _, p := range res.Proposals {
		c.ByLayer[p.Layer]++
		c.Proposals++
	}
	for _, l := range res.Layers {
		for _, ns := range l.NotSeen {
			if strings.Contains(ns.Reason, "does not pass the Map's schema") {
				c.Invalid++
			}
		}
	}
	return c
}

// twoTier is the residue of a Map where only service and environment
// entries are reviewed and the rest attaches to them by selector.
type twoTier struct {
	Filter     string         `json:"filter"`
	Reviewed   int            `json:"reviewed"`
	Releases   int            `json:"releases"`
	Attachable int            `json:"attachable"`
	Unattached map[string]int `json:"unattached"` // by strategy
}

var strategies = []string{"part-of label", "part-of or instance label", "GitOps owner"}

func attaches(o *obj, strategy string) bool {
	partOf := o.labels["app.kubernetes.io/part-of"] != ""
	instance := o.labels["app.kubernetes.io/instance"] != ""
	switch strategy {
	case "part-of label":
		return partOf
	case "part-of or instance label":
		return partOf || instance
	default:
		// Applied from Git (the tool tracks it), owned by something applied
		// from Git, or labelled with its release.
		return !o.runtime || o.owned || strings.Contains(o.source, "#preview-") || partOf || instance
	}
}

func twoTierOf(name string, s *snapshot, objs []*obj, res discovery.Result) twoTier {
	byRef := map[string]*obj{}
	for _, o := range objs {
		byRef[o.ref()] = o
	}
	t := twoTier{Filter: name, Unattached: map[string]int{}}
	releases := map[string]bool{}
	for _, o := range objs {
		releases[strings.SplitN(o.source, "#", 2)[0]] = true
	}
	t.Releases = len(releases)
	for _, p := range res.Proposals {
		if p.Layer == discovery.LayerEnvironment || p.Layer == discovery.LayerService {
			t.Reviewed++
			continue
		}
		var e mapserver.Entry
		if err := yaml.Unmarshal([]byte(p.Entry), &e); err != nil || len(e.Sources) == 0 {
			continue
		}
		o := byRef[e.Sources[0]["kubernetes"]]
		if o == nil {
			continue
		}
		t.Attachable++
		for _, st := range strategies {
			if !attaches(o, st) {
				t.Unattached[st]++
			}
		}
	}
	return t
}

// estateReport is one snapshot's counts and two-tier residue per filter.
type estateReport struct {
	Objects   int            `json:"objects"`
	Sources   int            `json:"sources"`
	Kinds     map[string]int `json:"listedKinds"`
	Unmodeled map[string]int `json:"customResources"`
	Counts    []count        `json:"counts"`
	TwoTier   []twoTier      `json:"twoTier"`
}

func measureSnapshot(s *snapshot) (estateReport, error) {
	r := estateReport{Objects: len(s.objects), Sources: len(s.sources), Kinds: map[string]int{}, Unmodeled: map[string]int{}}
	for _, o := range s.objects {
		if _, ok := served[o.group()+"/"+o.kind]; ok {
			r.Kinds[o.kind]++
		} else if strings.Contains(o.group(), ".") && !strings.HasSuffix(o.group(), ".k8s.io") {
			r.Unmodeled[o.group()+"/"+o.kind]++
		}
	}
	for _, f := range filters() {
		objs := apply(s, f)
		res, err := survey(s, objs, f.excludeNS, discovery.NewIndex(nil))
		if err != nil {
			return r, err
		}
		r.Counts = append(r.Counts, countOf(f.name, res))
		if f.name == "none" || f.name == all.name {
			r.TwoTier = append(r.TwoTier, twoTierOf(f.name, s, objs, res))
		}
	}
	return r, nil
}

// week is one week-to-week comparison under one filter.
type week struct {
	From, To string
	// Changes by layer, then action (add, update, remove).
	Changes map[string]map[string]int `json:"changes"`
	Total   int                       `json:"total"`
	// Stale: entries whose object is gone but that no removal proposes.
	Stale   int `json:"stale"`
	Sources struct {
		// Compared: rendered in both weeks; Added, Removed: rendered in
		// one week and absent (not failed) in the other; Failed: failed in
		// either week, left out of both.
		Compared, Added, Removed, Failed int
	} `json:"sources"`
}

// failedOf reads a week's errors.txt: the first field of each line, a
// service name (Wikimedia) or cluster/directory (k8s.io).
func failedOf(dir string) map[string]bool {
	raw, _ := os.ReadFile(filepath.Join(dir, "errors.txt")) //nolint:gosec // the spike's own output
	out := map[string]bool{}
	for line := range strings.Lines(string(raw)) {
		if f := strings.Fields(line); len(f) > 0 {
			out[f[0]] = true
		}
	}
	return out
}

// failed says whether a source file's service or directory failed to
// render in a week: <cluster>~<namespace>~<source>.yaml is named either by
// <source> or by <cluster>/<namespace>.
func failed(file string, errs map[string]bool) bool {
	p := strings.SplitN(strings.TrimSuffix(file, ".yaml"), "~", 3)
	return errs[p[2]] || errs[p[0]+"/"+p[1]]
}

func sourcesOf(dir string) map[string]bool {
	files, _ := filepath.Glob(filepath.Join(dir, "*~*~*.yaml"))
	out := map[string]bool{}
	for _, f := range files {
		out[filepath.Base(f)] = true
	}
	return out
}

// churn compares consecutive weeks: last week's proposals accepted as the
// Map, this week surveyed against it. A source file that failed to render
// in either week is left out of both, so that a render failure is not
// churn; one that exists in one week only is real churn and counted.
func churn(dirs []string) (map[string][]week, error) {
	out := map[string][]week{}
	for i := 1; i < len(dirs); i++ {
		a, b := sourcesOf(dirs[i-1]), sourcesOf(dirs[i])
		ea, eb := failedOf(dirs[i-1]), failedOf(dirs[i])
		keep := map[string]bool{}
		var both, added, removed, lost int
		for _, set := range []map[string]bool{a, b} {
			for f := range set {
				if keep[f] {
					continue
				}
				switch {
				case failed(f, ea) || failed(f, eb):
					lost++
				case a[f] && b[f]:
					both++
					keep[f] = true
				case b[f]:
					added++
					keep[f] = true
				default:
					removed++
					keep[f] = true
				}
			}
		}
		prev, err := load(dirs[i-1], keep)
		if err != nil {
			return nil, err
		}
		cur, err := load(dirs[i], keep)
		if err != nil {
			return nil, err
		}
		applyModel(prev, 0)
		applyModel(cur, 0)
		for _, f := range filters() {
			w := week{From: filepath.Base(dirs[i-1]), To: filepath.Base(dirs[i]), Changes: map[string]map[string]int{}}
			w.Sources.Compared, w.Sources.Added, w.Sources.Removed, w.Sources.Failed = both, added, removed, lost
			before, err := survey(prev, apply(prev, f), f.excludeNS, discovery.NewIndex(nil))
			if err != nil {
				return nil, err
			}
			var entries []mapserver.Entry
			for _, p := range before.Proposals {
				var e mapserver.Entry
				if err := yaml.Unmarshal([]byte(p.Entry), &e); err != nil {
					return nil, fmt.Errorf("entry %s: %w", p.Key, err)
				}
				e.Status = "validated"
				entries = append(entries, e)
			}
			objs := apply(cur, f)
			after, err := survey(cur, objs, f.excludeNS, discovery.NewIndex(entries))
			if err != nil {
				return nil, err
			}
			removed := map[string]bool{}
			for _, p := range after.Proposals {
				if w.Changes[p.Layer] == nil {
					w.Changes[p.Layer] = map[string]int{}
				}
				w.Changes[p.Layer][string(p.Action)]++
				w.Total++
				if p.Action == v1alpha1.ProposalRemove {
					removed[p.Key] = true
				}
			}
			present := map[string]bool{}
			for _, o := range objs {
				if !f.excludeNS(o.ns) {
					present[o.ref()] = true
					present[nsRef(o.cluster, o.ns)] = true
				}
			}
			for _, e := range entries {
				if len(e.Sources) == 0 || strings.HasPrefix(e.Key, "service/") {
					continue
				}
				if ref := e.Sources[0]["kubernetes"]; !present[ref] && !removed[e.Key] {
					w.Stale++
				}
			}
			out[f.name] = append(out[f.name], w)
		}
	}
	return out, nil
}

func nsRef(cluster, ns string) string {
	if cluster == "" {
		return ns
	}
	return cluster + "/" + ns
}

type historyFlag []string

func (h *historyFlag) String() string     { return strings.Join(*h, ",") }
func (h *historyFlag) Set(v string) error { *h = append(*h, v); return nil }

func main() {
	var histories historyFlag
	estate := flag.String("estate", "", "rendered synthetic estate")
	previews := flag.Int("previews", 10, "ephemeral preview namespaces added to the estate's second run")
	out := flag.String("json", "", "write every number here")
	flag.Var(&histories, "history", "name=<dir of dated snapshot dirs>, repeatable")
	flag.Parse()
	if err := run(*estate, *previews, histories, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(estate string, previews int, histories []string, out string) error {
	all := map[string]any{}
	if estate != "" {
		for _, n := range []int{0, previews} {
			s, err := load(estate, nil)
			if err != nil {
				return err
			}
			applyModel(s, n)
			r, err := measureSnapshot(s)
			if err != nil {
				return err
			}
			name := fmt.Sprintf("estate, %d previews", n)
			all[name] = r
			printSnapshot(name, r)
		}
	}
	for _, h := range histories {
		name, dir, _ := strings.Cut(h, "=")
		dirs, _ := filepath.Glob(filepath.Join(dir, "*", ".done"))
		for i := range dirs {
			dirs[i] = filepath.Dir(dirs[i])
		}
		sort.Strings(dirs)
		if len(dirs) == 0 {
			return fmt.Errorf("%s: no rendered week", dir)
		}
		last, err := load(dirs[len(dirs)-1], nil)
		if err != nil {
			return err
		}
		applyModel(last, 0)
		r, err := measureSnapshot(last)
		if err != nil {
			return err
		}
		all[name+", last week"] = r
		printSnapshot(name+", last week ("+filepath.Base(dirs[len(dirs)-1])+")", r)
		weeks, err := churn(dirs)
		if err != nil {
			return err
		}
		all[name+", churn"] = weeks
		printChurn(name, weeks)
	}
	if out == "" {
		return nil
	}
	raw, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(out, append(raw, '\n'), 0o600)
}

func printSnapshot(name string, r estateReport) {
	fmt.Printf("\n## %s: %d sources, %d objects\n\n", name, r.Sources, r.Objects)
	fmt.Printf("| filter | total | %s | invalid |\n|---|---|%s---|\n", strings.Join(layers, " | "), strings.Repeat("---|", len(layers)))
	for _, c := range r.Counts {
		fmt.Printf("| %s | %d |", c.Filter, c.Proposals)
		for _, l := range layers {
			fmt.Printf(" %d |", c.ByLayer[l])
		}
		fmt.Printf(" %d |\n", c.Invalid)
	}
	fmt.Printf("\n| two-tier | reviewed | releases | attachable | %s |\n|---|---|---|---|---|---|---|\n", strings.Join(strategies, " unattached | ")+" unattached")
	for _, t := range r.TwoTier {
		fmt.Printf("| %s | %d | %d | %d |", t.Filter, t.Reviewed, t.Releases, t.Attachable)
		for _, s := range strategies {
			fmt.Printf(" %d |", t.Unattached[s])
		}
		fmt.Println()
	}
	fmt.Printf("\nlisted kinds: %v\ncustom resources (not modeled unless named in the model): %d kinds, %d objects\n", r.Kinds, len(r.Unmodeled), sum(r.Unmodeled))
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func printChurn(name string, weeks map[string][]week) {
	fmt.Printf("\n## %s: week-to-week proposals (last week's proposals accepted as the Map)\n\n", name)
	fmt.Printf("| filter | weeks | total | mean/week | median | max | stale (sum) | %s |\n|---|---|---|---|---|---|---|%s\n",
		strings.Join(layers, " a/u/r | ")+" a/u/r", strings.Repeat("---|", len(layers)))
	for _, f := range filters() {
		ws := weeks[f.name]
		totals := make([]int, len(ws))
		byLayer := map[string]map[string]int{}
		stale := 0
		for i, w := range ws {
			totals[i] = w.Total
			stale += w.Stale
			for l, acts := range w.Changes {
				if byLayer[l] == nil {
					byLayer[l] = map[string]int{}
				}
				for a, n := range acts {
					byLayer[l][a] += n
				}
			}
		}
		t := slices.Clone(totals)
		slices.Sort(t)
		s := 0
		for _, v := range t {
			s += v
		}
		fmt.Printf("| %s | %d | %d | %.1f | %d | %d | %d |", f.name, len(ws), s, float64(s)/float64(max(1, len(ws))), t[len(t)/2], t[len(t)-1], stale)
		for _, l := range layers {
			a := byLayer[l]
			fmt.Printf(" %d/%d/%d |", a["Add"], a["Update"], a["Remove"])
		}
		fmt.Println()
	}
	if ws := weeks["none"]; len(ws) > 0 {
		c, ad, rm, fl := 0, 0, 0, 0
		for _, w := range ws {
			c += w.Sources.Compared
			ad += w.Sources.Added
			rm += w.Sources.Removed
			fl += w.Sources.Failed
		}
		fmt.Printf("\nsource files rendered in both weeks (mean): %.1f; over %d weeks: %d added, %d removed, %d left out (failed to render in one of the two weeks, counted per pair)\n",
			float64(c)/float64(len(ws)), len(ws), ad, rm, fl)
	}
}
