// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package corpus

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// volume is the size of each source, curated records included: each has its
// own rhythm. A CI chain ships several deployments a day; an SRE team writes
// a few post-mortems a year.
var volume = map[string]int{
	SourceDeployments: 160, // ~5 a day over 30 days
	SourceSupport:     50,  // ~2.5 a day over 21 days
	SourceVendor:      15,  // vendor bulletins over 90 days
	SourceIncidents:   18,  // post-mortems over 2 years
	SourceRunbooks:    28,  // a team's runbook library
}

// checkoutAPI's tickets are the most recent ones on that service.
const checkoutAPI = "checkout-api"

// ciBase numbers the deployment log, chronologically.
const ciBase = 2300

// quiet are the windows before the incident where a service gets no noise
// change: its recent changes are curated (the signal and its hand-written
// competitors), never random.
var quiet = map[string]time.Duration{
	"billing-api":          24 * time.Hour, // proxy-timeout
	"orders-api":           6 * time.Hour,  // bad-config-rollout
	"notifications-worker": 6 * time.Hour,  // missing-secret-key
	checkoutAPI:            24 * time.Hour, // silence behind the checkout tickets
}

// seed fixes the noise: for a scenario the corpus always has the same shape
// and the same identifiers; only the dates move with the incident.
const seed = 20260909

//go:embed pools.json
var poolsJSON []byte

type text struct{ Summary, Tags string }

// pools are the texts the noise is drawn from.
type pools struct {
	Services    []struct{ Name, Team string }
	Deployments struct{ Image, Config, Secret, Helm, Fail []text }
	Support     struct {
		Customers, Channels []string
		Topics              []struct{ Title, Summary, Tags, Priority string }
	}
	Vendor    []struct{ Service, Region, Title, Summary, Tags string }
	Incidents []struct {
		Title, Service, Severity, Duration, Symptoms string
		RootCause                                    string `json:"root_cause"`
		Fix, Tags                                    string
	}
	Runbooks []struct{ Title, Domain, Symptoms, Procedure, Tags string }
}

// entry is a record with its age in minutes, the sort key.
type entry struct {
	age float64
	r   Record
}

type generator struct {
	rng      *rand.Rand
	incident time.Time
	p        pools
	team     map[string]string
}

// Generate composes the corpus of a scenario: the seeds plus the noise,
// numbered, sorted, and checked.
func Generate(scenario string, incident time.Time) (Corpus, error) {
	c, err := Seeds(scenario, incident)
	if err != nil {
		return nil, err
	}
	g := &generator{rng: rand.New(rand.NewPCG(seed, seed)), incident: incident, team: map[string]string{}} //nolint:gosec // reproducible noise, not a secret
	if err := json.Unmarshal(poolsJSON, &g.p); err != nil {
		return nil, fmt.Errorf("parse pools: %w", err)
	}
	for _, s := range g.p.Services {
		g.team[s.Name] = s.Team
	}
	if len(g.p.Vendor) < volume[SourceVendor]-len(c[SourceVendor]) {
		return nil, fmt.Errorf("vendor: %d bulletins in the pool, %d wanted", len(g.p.Vendor), volume[SourceVendor]-len(c[SourceVendor]))
	}
	oldestFirst := func(a, b entry) int { return -cmpFloat(a.age, b.age) }
	out := Corpus{}
	out[SourceDeployments] = renumber(g.compose(SourceDeployments, c, g.deployments, oldestFirst), "CI-", ciBase)
	support := g.compose(SourceSupport, c, g.support, oldestFirst)
	anchor := slices.IndexFunc(support, func(r Record) bool { return r["id"] == "SUP-4471" })
	out[SourceSupport] = renumber(support, "SUP-", 4471-anchor)
	out[SourceVendor] = g.vendorIDs(g.compose(SourceVendor, c, g.vendor, oldestFirst))
	out[SourceIncidents] = g.compose(SourceIncidents, c, g.incidents, oldestFirst)
	out[SourceRunbooks] = g.compose(SourceRunbooks, c, g.runbooks, func(a, b entry) int { return strings.Compare(a.r["id"], b.r["id"]) })
	if err := Check(out, scenario, incident); err != nil {
		return nil, err
	}
	return out, checkSequence(out)
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// compose adds the noise to the curated records of a source and sorts them;
// the sort is stable, curated records first among equals.
func (g *generator) compose(source string, c Corpus, noise func(int, []Record) []entry, order func(a, b entry) int) []Record {
	curated := c[source]
	all := make([]entry, 0, volume[source])
	for _, r := range curated {
		all = append(all, entry{g.age(r[keyDate]), r})
	}
	all = append(all, noise(volume[source]-len(curated), curated)...)
	slices.SortStableFunc(all, order)
	out := make([]Record, len(all))
	for i, e := range all {
		out[i] = e.r
	}
	return out
}

// age is how many minutes before the incident a date is; 0 for a record
// without a date.
func (g *generator) age(date string) float64 {
	t, err := parseDate(date)
	if err != nil {
		return 0
	}
	return g.incident.Sub(t).Minutes()
}

func (g *generator) at(age float64) string {
	return g.incident.Add(-time.Duration(age * float64(time.Minute))).UTC().Format(isoUTC)
}

func (g *generator) uniform(lo, hi float64) float64 { return lo + (hi-lo)*g.rng.Float64() }

func pick[T any](g *generator, from []T) T { return from[g.rng.IntN(len(from))] }

var version = regexp.MustCompile(`v\d+\.\d+\.\d+`)

// deployments never duplicates a curated change, its title or a version a
// seed mentions, and leaves at least 3 minutes between two changes.
func (g *generator) deployments(n int, curated []Record) []entry {
	taken := map[float64]bool{}
	reserved := map[string]bool{}
	for _, r := range curated {
		taken[g.age(r[keyDate])] = true
		reserved[r[keyTitle]] = true
		for _, v := range version.FindAllString(r[keyTitle], -1) {
			reserved[v] = true
		}
	}
	d := g.p.Deployments
	var out []entry
	for len(out) < n {
		age := g.uniform(20, 30*24*60)
		svc := pick(g, g.p.Services).Name
		if age < quiet[svc].Minutes() || slices.ContainsFunc(keys(taken), func(t float64) bool { return age-t < 3 && t-age < 3 }) {
			continue
		}
		taken[age] = true
		v := fmt.Sprintf("v%d.%d.%d", 1+g.rng.IntN(6), g.rng.IntN(10), g.rng.IntN(10))
		var t text
		var kind, title string
		status := "success"
		switch roll := g.rng.Float64(); {
		case roll < 0.06:
			t, kind, status, title = pick(g, d.Fail), "image", "failed", "Deployment failed: "+svc+" "+v
			if strings.HasPrefix(t.Summary, "Canceled") {
				status = "canceled"
			}
		case roll < 0.20:
			t, kind, title = pick(g, d.Config), "configmap", strings.Split(svc, "-")[0]+"-config "+v+" rolled out"
		case roll < 0.25:
			t, kind, title = pick(g, d.Secret), "secret", "Secret rotation on "+svc
		case roll < 0.30:
			t, kind, title = pick(g, d.Helm), "helm", "Helm revision of "+svc
		default:
			t, kind, title = pick(g, d.Image), "image", svc+" "+v+" rolled out"
		}
		if reserved[title] || reserved[v] {
			continue
		}
		out = append(out, entry{age, Record{
			keyDate: g.at(age), keyTitle: title, keyService: svc, "type": kind,
			"author": "CI pipeline (" + g.team[svc] + " team)", keySummary: t.Summary, keyStatus: status,
			keyTags: g.team[svc] + " " + t.Tags,
		}})
	}
	return out
}

func keys(m map[float64]bool) []float64 {
	out := make([]float64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// support leaves nothing on checkout-api more recent than its curated
// tickets.
func (g *generator) support(n int, curated []Record) []entry {
	floor := 21 * 24 * 60.0
	for _, r := range curated {
		if r[keyService] == checkoutAPI {
			floor = min(floor, g.age(r[keyDate]))
		}
	}
	s := g.p.Support
	var out []entry
	for len(out) < n {
		age := g.uniform(30, 21*24*60)
		svc := pick(g, g.p.Services).Name
		if svc == checkoutAPI && age < floor+15 {
			continue
		}
		topic := pick(g, s.Topics)
		channel, customer := pick(g, s.Channels), pick(g, s.Customers)
		status := "closed"
		if age <= 2*24*60 {
			status = pick(g, []string{"open", "in progress"})
		}
		out = append(out, entry{age, Record{
			keyDate: g.at(age), keyTitle: topic.Title, keyService: svc, "channel": channel,
			"priority": topic.Priority, "customer": customer, keySummary: topic.Summary,
			keyStatus: status, keyTags: g.team[svc] + " " + topic.Tags,
		}})
	}
	return out
}

// vendor draws resolved bulletins over 90 days, without replacement.
func (g *generator) vendor(n int, _ []Record) []entry {
	out := make([]entry, 0, n)
	for _, i := range g.rng.Perm(len(g.p.Vendor))[:n] {
		b := g.p.Vendor[i]
		days := g.uniform(3, 90)
		out = append(out, entry{days * 24 * 60, Record{
			keyDate: g.at(days * 24 * 60), keyTitle: "Resolved - " + strings.ToLower(b.Title) + ", " + b.Region,
			keyService: b.Service, "region": b.Region, keyStatus: "resolved", keySummary: b.Summary,
			keyTags: b.Tags + " " + b.Region + " resolved",
		}})
	}
	return out
}

// incidents are post-mortems over two years, named after their date.
func (g *generator) incidents(n int, curated []Record) []entry {
	used := map[string]bool{}
	for _, r := range curated {
		used[r["id"]] = true
	}
	out := make([]entry, 0, n)
	for i := range n {
		p := g.p.Incidents[i%len(g.p.Incidents)]
		days := g.uniform(40, 730)
		at := g.incident.Add(-time.Duration(days * 24 * float64(time.Hour))).UTC()
		id := "INC-" + at.Format("2006-0102")
		for used[id] {
			id += "b"
		}
		used[id] = true
		out = append(out, entry{days * 24 * 60, Record{
			"id": id, keyDate: at.Format(isoUTC), keyTitle: p.Title, keyService: p.Service,
			"severity": p.Severity, "duration": p.Duration, "symptoms": p.Symptoms,
			"root_cause": p.RootCause, "fix": p.Fix, keyTags: p.Tags,
		}})
	}
	return out
}

// runbooks fill the library's free numbers.
func (g *generator) runbooks(n int, curated []Record) []entry {
	used := map[string]bool{}
	for _, r := range curated {
		used[r["id"]] = true
	}
	out := make([]entry, 0, n)
	k := 1
	for i := range n {
		p := g.p.Runbooks[i%len(g.p.Runbooks)]
		for used[fmt.Sprintf("RB-%03d", k)] {
			k++
		}
		id := fmt.Sprintf("RB-%03d", k)
		used[id] = true
		revised := g.incident.AddDate(0, 0, -(60 + g.rng.IntN(841)))
		out = append(out, entry{0, Record{
			"id": id, keyTitle: p.Title, "domain": p.Domain, "services": "all",
			"revision": revised.Format(time.DateOnly), "symptoms": p.Symptoms, "procedure": p.Procedure,
			"verification": "The symptom disappears and does not come back within 30 minutes.", keyTags: p.Tags,
		}})
	}
	return out
}

// renumber numbers a chronological source from a base: CI and ticket
// counters grow with time.
func renumber(records []Record, prefix string, base int) []Record {
	for i, r := range records {
		r["id"] = prefix + strconv.Itoa(base+i)
	}
	return records
}

// vendorIDs names every bulletin after its date and a rank, the curated
// ones included: a seed's id is a placeholder, since its date is relative.
func (g *generator) vendorIDs(records []Record) []Record {
	taken := map[string]bool{}
	for _, r := range records {
		day := strings.ReplaceAll(r[keyDate][:10], "-", "")
		rank := 1 + g.rng.IntN(4)
		for taken[fmt.Sprintf("AWS-%s-%s-%02d", day[:4], day[4:], rank)] {
			rank++
		}
		r["id"] = fmt.Sprintf("AWS-%s-%s-%02d", day[:4], day[4:], rank)
		taken[r["id"]] = true
	}
	return records
}

// checkSequence checks that the deployment and ticket numbers grow with
// time.
func checkSequence(c Corpus) error {
	for _, source := range []string{"deployments", "support"} {
		records := slices.Clone(c[source])
		slices.SortStableFunc(records, func(a, b Record) int { return strings.Compare(a[keyDate], b[keyDate]) })
		if !slices.IsSortedFunc(records, func(a, b Record) int { return cmpID(a["id"], b["id"]) }) {
			return fmt.Errorf("%s: identifiers do not grow with time", source)
		}
	}
	return nil
}

func cmpID(a, b string) int {
	n := func(id string) int { v, _ := strconv.Atoi(id[strings.LastIndex(id, "-")+1:]); return v }
	return n(a) - n(b)
}
