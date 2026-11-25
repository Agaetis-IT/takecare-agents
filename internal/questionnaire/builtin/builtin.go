// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package builtin holds the built-in question set of takecare init (spec
// 13 §4.1) and its outputs, the skeleton files of §5. A question file's
// outputs have literal paths; the skeleton writes one Map file per
// namespace and per team, so its outputs are rendered here, from the
// answers, and then go through the same rules as any output: the path
// checks, the size bound, the checks before writing and the write through
// an os.Root.
package builtin

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"text/template"

	"github.com/agaetis-it/takecare-agents/internal/questionnaire"
)

// Version is the version of the built-in question set, recorded in
// .takecare/answers.yaml and in the commit message.
const Version = 1

// Name is the built-in file's name in faults.
const Name = "built-in questions"

//go:embed questions.yaml
var questions []byte

// Sources returns the built-in question set, for questionnaire.Load.
func Sources() []questionnaire.Source {
	return []questionnaire.Source{{Name: Name, Data: questions}}
}

// Dirs are the directories the skeleton writes into, relative to the
// repository.
type Dirs struct {
	Map, Resources string
}

// Skeleton renders the skeleton files from the answers of set's
// built-in questions. A question answered "I don't know" leaves a TODO
// comment where its value goes, never a guessed value. created is the
// date the Map entries carry.
func Skeleton(set *questionnaire.Set, answers map[string]questionnaire.Answer, dirs Dirs, created string) ([]questionnaire.Rendered, error) {
	v := newView(set, answers, created)
	var out []questionnaire.Rendered
	add := func(p, name string, data any) error {
		var b strings.Builder
		if err := templates.ExecuteTemplate(&b, name, data); err != nil {
			return fmt.Errorf("render %s: %w", p, err)
		}
		out = append(out, questionnaire.Rendered{Path: p, Data: []byte(b.String())})
		return nil
	}
	m := func(p string) string { return path.Join(dirs.Map, p) }
	r := func(p string) string { return path.Join(dirs.Resources, p) }
	if err := add(m("README.md"), "readme", v); err != nil {
		return nil, err
	}
	for _, e := range v.Entries {
		if err := add(m(e.Key+".yaml"), "entry", e); err != nil {
			return nil, err
		}
	}
	files := []struct{ path, name string }{
		{r("values.yaml"), "values"}, {r("roles/doc.yaml"), "doc"}, {r("roles/intendant.yaml"), "intendant"},
	}
	if v.Knowledge != "" {
		files = append(files, struct{ path, name string }{r("connectors/knowledge.yaml"), "knowledge"})
	}
	for _, f := range files {
		if err := add(f.path, f.name, v); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// answer is one built-in answer as the writers read it.
type answer struct {
	known bool
	value any
}

func (a answer) text() string {
	s, _ := a.value.(string)
	return s
}

func (a answer) list() []string {
	l, _ := a.value.([]string)
	return l
}

// secret is a secretRef answer, Known false when it is unknown.
type secret struct {
	Known                bool
	Namespace, Name, Key string
}

func (a answer) secret() secret {
	r, ok := a.value.(questionnaire.SecretRef)
	return secret{Known: a.known && ok, Namespace: r.Namespace, Name: r.Name, Key: r.Key}
}

// entry is one Map file.
type entry struct {
	Key, Created, Owner, Source string
	ObservedBy                  string
}

// view is what the templates read. A field Unknown* is true when the
// question was answered "I don't know".
type view struct {
	Version                       int
	Namespace                     string
	UnknownNamespace              bool
	Namespaces                    []string
	UnknownEnvironments           bool
	UnknownTeams                  bool
	Entries                       []entry
	Alerts                        string
	Metrics                       string // <namespace>/<name>, empty for none
	MetricsNamespace, MetricsName string
	UnknownMetrics                bool
	Report                        string
	Destinations                  [][2]string
	ReportSecret                  secret
	ModelURL, ModelName           string
	UnknownModel                  bool
	ModelSecret                   secret
	MapURL, MapRef, MapPath       string
	MapSSH                        bool
	UnknownMap                    bool
	MapSecret                     secret
	Knowledge                     string
	UnknownKnowledge              bool
}

func newView(set *questionnaire.Set, answers map[string]questionnaire.Answer, created string) *view {
	get := func(id string) answer {
		q := set.Question(id)
		a, ok := answers[id]
		if q == nil || !ok || a.Unknown || !set.Shown(q, answers) {
			return answer{}
		}
		return answer{known: true, value: a.Value}
	}
	v := &view{Version: Version}
	ns := get("operator.namespace")
	v.Namespace, v.UnknownNamespace = ns.text(), !ns.known
	cluster := get("cluster.name").text()
	envs, teams := get("environments"), get("teams")
	v.Namespaces, v.UnknownEnvironments, v.UnknownTeams = envs.list(), !envs.known, !teams.known

	metrics := get("metrics.service")
	v.Metrics, v.UnknownMetrics = metrics.text(), !metrics.known
	v.MetricsNamespace, v.MetricsName, _ = strings.Cut(v.Metrics, "/")
	observed := ""
	if v.Metrics != "" {
		observed = "observability/" + v.MetricsName
	}

	owners := map[string]string{}
	teamEntries := make([]entry, 0, len(teams.list()))
	for _, t := range teams.list() {
		name, nss, _ := strings.Cut(t, "=")
		teamEntries = append(teamEntries, entry{Key: "team/" + name, Created: created})
		for n := range strings.FieldsSeq(nss) {
			if _, taken := owners[n]; !taken {
				owners[n] = "team/" + name
			}
		}
	}
	envKey, source := func(n string) string { return "environment/" + n }, func(n string) string { return n }
	if cluster != "" {
		envKey = func(n string) string { return "environment/" + cluster + "." + n }
		source = func(n string) string { return cluster + "/" + n }
		v.Entries = append(v.Entries, entry{Key: "environment/" + cluster, Created: created})
	}
	for _, n := range v.Namespaces {
		v.Entries = append(v.Entries, entry{Key: envKey(n), Created: created, Owner: owners[n], Source: source(n), ObservedBy: observed})
	}
	if observed != "" {
		v.Entries = append(v.Entries, entry{Key: observed, Created: created,
			Source: source(v.MetricsNamespace) + "/Service/" + v.MetricsName})
	}
	v.Entries = append(v.Entries, teamEntries...)
	slices.SortStableFunc(v.Entries, func(a, b entry) int { return strings.Compare(a.Key, b.Key) })
	v.Entries = slices.CompactFunc(v.Entries, func(a, b entry) bool { return a.Key == b.Key })

	if a := get("alerts.source"); a.known {
		v.Alerts = a.text()
	}
	if r := get("report.kind"); r.known {
		v.Report = r.text()
	}
	for _, d := range get("report.destinations").list() {
		use, channel, _ := strings.Cut(d, "=")
		v.Destinations = append(v.Destinations, [2]string{use, channel})
	}
	v.ReportSecret = get("report.secret").secret()

	model := get("model.provider")
	v.ModelURL, v.ModelName, _ = strings.Cut(model.text(), " ")
	v.UnknownModel = !model.known
	v.ModelSecret = get("model.secret").secret()

	repo := get("map.repository")
	if f := strings.Fields(repo.text()); len(f) == 3 {
		v.MapURL, v.MapRef, v.MapPath = f[0], f[1], f[2]
		v.MapSSH = !strings.HasPrefix(v.MapURL, "https://")
	}
	v.UnknownMap = !repo.known
	v.MapSecret = get("map.secret").secret()

	k := get("knowledge")
	v.Knowledge, v.UnknownKnowledge = k.text(), !k.known
	return v
}

// has tells whether the destinations name use.
func (v *view) Has(use string) bool {
	return slices.ContainsFunc(v.Destinations, func(d [2]string) bool { return d[0] == use })
}

// Slack tells whether results are posted to Slack, Log to the report
// server's log.
func (v *view) Slack() bool { return v.Report == "Slack" }

// Log tells whether results are posted to the report server's log.
func (v *view) Log() bool { return v.Report == "The report server's log" }

// Prometheus tells whether the prometheus add-on is enabled.
func (v *view) Prometheus() bool { return v.Metrics != "" }

// Map tells whether the map add-on is enabled.
func (v *view) Map() bool { return v.MapURL != "" }

// quote writes s as a double-quoted YAML scalar: a JSON string is one.
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// flow writes a list of strings as a YAML flow sequence.
func flow(l []string) string {
	q := make([]string, len(l))
	for i, s := range l {
		q[i] = quote(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// dict pairs its arguments as a map, for a template that takes several.
func dict(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		k, _ := kv[i].(string)
		m[k] = kv[i+1]
	}
	return m
}

var templates = template.Must(template.New("builtin").
	Funcs(template.FuncMap{"q": quote, "flow": flow, "dict": dict}).
	Option("missingkey=error").
	Parse(skeleton))
