// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/duration"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/kubeview"
)

// statusSchemaVersion versions the shape of status --output json.
const statusSchemaVersion = 1

// newestRuns is how many surveys and investigations status shows.
const newestRuns = 5

// lister is what status needs of the cluster.
type lister interface {
	List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error
}

// Status is what status prints: the API's own words, never a readiness
// of its own.
type Status struct {
	SchemaVersion int    `json:"schemaVersion"`
	Namespace     string `json:"namespace"`
	// NotReady names the platform, roles and connectors whose Ready
	// condition is not True, as Kind/name; empty, the exit code is 0.
	NotReady       []string    `json:"notReady"`
	Platforms      []Platform  `json:"platforms"`
	Roles          []Role      `json:"roles"`
	Connectors     []Connector `json:"connectors"`
	Surveys        []RunRecord `json:"surveys"`
	Investigations []RunRecord `json:"investigations"`
}

// Condition is a condition as the API holds it.
type Condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// Platform is a TakeCarePlatform with its conditions.
type Platform struct {
	Name       string      `json:"name"`
	Conditions []Condition `json:"conditions"`
}

// Role is a role with its level, its Ready condition (nil when it has
// none), its other conditions not in their good state, and its blind spots.
type Role struct {
	Kind       string      `json:"kind"`
	Name       string      `json:"name"`
	Level      string      `json:"level"`
	Ready      *Condition  `json:"ready"`
	Conditions []Condition `json:"conditions"`
	BlindSpots []BlindSpot `json:"blindSpots"`
}

// BlindSpot is an entity a role cannot read, and why.
type BlindSpot struct {
	Entity  string `json:"entity"`
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
}

// Connector is a TakeCareConnector with its tools counted by class.
type Connector struct {
	Name        string     `json:"name"`
	Types       []string   `json:"types"`
	Ready       *Condition `json:"ready"`
	Tools       ToolCounts `json:"tools"`
	MapRevision string     `json:"mapRevision,omitempty"`
}

// ToolCounts counts a connector's tools by class; Content counts the
// tools marked content, whatever their class (ADR 0014 §5).
type ToolCounts struct {
	Read    int `json:"read"`
	Propose int `json:"propose"`
	Act     int `json:"act"`
	Content int `json:"content"`
}

// RunRecord is a survey or an investigation. Trigger is a survey's trigger or
// an investigation's alert name; Reason is set when it did not complete.
type RunRecord struct {
	Name    string    `json:"name"`
	Phase   string    `json:"phase"`
	Trigger string    `json:"trigger"`
	Created time.Time `json:"created"`
	Reason  string    `json:"reason,omitempty"`
}

func (a *app) status(ctx context.Context, args []string) int {
	var (
		c                           common
		namespace, kubeconfig, kctx string
	)
	fs := a.newFlags("status")
	c.register(fs)
	fs.StringVar(&namespace, "namespace", "takecare-system", "the operator's namespace")
	fs.StringVar(&kubeconfig, "kubeconfig", "", "the kubeconfig file (default: KUBECONFIG, then ~/.kube/config)")
	fs.StringVar(&kctx, "context", "", "the kubeconfig context, for this run only")
	if code := a.parse(fs, &c, args); code >= 0 {
		return code
	}
	cfg, err := kubeview.Load(kubeconfig, kctx)
	if err != nil {
		return a.fail("status: %v", err)
	}
	r, err := kubeview.New(cfg)
	if err != nil {
		return a.fail("status: %v", err)
	}
	s, err := readStatus(ctx, r, namespace)
	if err != nil {
		return a.fail("status: %v", err)
	}
	if c.output == outputJSON {
		err = a.printJSON(s)
	} else {
		err = s.write(a.stdout, a.now())
	}
	if err != nil {
		return a.fail("status: %v", err)
	}
	if len(s.NotReady) > 0 {
		return ExitFinding
	}
	return ExitOK
}

// readStatus lists what status shows. A list it cannot read stops it:
// it never shows a partial install as complete.
func readStatus(ctx context.Context, r lister, ns string) (*Status, error) {
	s := &Status{
		SchemaVersion: statusSchemaVersion, Namespace: ns, NotReady: []string{},
		Platforms: []Platform{}, Roles: []Role{}, Connectors: []Connector{},
	}
	var platforms v1alpha1.TakeCarePlatformList
	var docs v1alpha1.TakeCareDocList
	var intendants v1alpha1.TakeCareIntendantList
	var rangers v1alpha1.TakeCareRangerList
	var connectors v1alpha1.TakeCareConnectorList
	var surveys v1alpha1.TakeCareSurveyList
	var investigations v1alpha1.TakeCareInvestigationList
	for _, l := range []struct {
		what string
		list client.ObjectList
		ns   bool
	}{
		{"takecareplatforms", &platforms, false},
		{"takecaredocs", &docs, true},
		{"takecareintendants", &intendants, true},
		{"takecarerangers", &rangers, false},
		{"takecareconnectors", &connectors, true},
		{"takecaresurveys", &surveys, true},
		{"takecareinvestigations", &investigations, true},
	} {
		var opts []client.ListOption
		where := "the cluster"
		if l.ns {
			opts = append(opts, client.InNamespace(ns))
			where = "namespace " + ns
		}
		if err := r.List(ctx, l.list, opts...); err != nil {
			return nil, fmt.Errorf("list %s in %s: %w", l.what, where, err)
		}
	}

	if len(platforms.Items) == 0 {
		s.NotReady = append(s.NotReady, "TakeCarePlatform (none found)")
	}
	for _, p := range sortedByName(platforms.Items) {
		s.Platforms = append(s.Platforms, Platform{Name: p.Name, Conditions: conditions(p.Status.Conditions, false)})
		s.notReady("TakeCarePlatform", p.Name, p.Status.Conditions)
	}
	for _, d := range sortedByName(docs.Items) {
		s.addRole("TakeCareDoc", d.Name, d.Status.Level, d.Status.Conditions, d.Status.BlindSpots)
	}
	for _, i := range sortedByName(intendants.Items) {
		s.addRole("TakeCareIntendant", i.Name, i.Status.Level, i.Status.Conditions, i.Status.BlindSpots)
	}
	// The Ranger's status has no level of its own: its spec's is the one it runs at.
	for _, g := range sortedByName(rangers.Items) {
		s.addRole("TakeCareRanger", g.Name, g.Spec.Level, g.Status.Conditions, nil)
	}
	for _, k := range sortedByName(connectors.Items) {
		s.addConnector(k)
	}
	s.Surveys = newest(surveys.Items, func(v v1alpha1.TakeCareSurvey) RunRecord {
		return run(v.ObjectMeta, v.Status.RunStatus, string(v.Spec.Trigger))
	})
	s.Investigations = newest(investigations.Items, func(v v1alpha1.TakeCareInvestigation) RunRecord {
		return run(v.ObjectMeta, v.Status.RunStatus, v.Spec.Alert.Name)
	})
	return s, nil
}

func (s *Status) addRole(kind, name string, level v1alpha1.Level, conds []metav1.Condition, spots []v1alpha1.BlindSpot) {
	r := Role{Kind: kind, Name: name, Level: string(level), Ready: ready(conds), Conditions: conditions(conds, true), BlindSpots: []BlindSpot{}}
	for _, b := range spots {
		r.BlindSpots = append(r.BlindSpots, BlindSpot{Entity: string(b.Entity), Reason: string(b.Reason), Message: b.Message})
	}
	s.Roles = append(s.Roles, r)
	s.notReady(kind, name, conds)
}

func (s *Status) addConnector(k v1alpha1.TakeCareConnector) {
	c := Connector{Name: k.Name, Types: []string{}, Ready: ready(k.Status.Conditions)}
	for _, t := range k.Spec.Types {
		c.Types = append(c.Types, string(t))
		if t == v1alpha1.ConnectorMap {
			c.MapRevision = k.Status.MapRevision
		}
	}
	for _, t := range k.Status.Tools {
		switch t.Class {
		case v1alpha1.ToolRead:
			c.Tools.Read++
		case v1alpha1.ToolPropose:
			c.Tools.Propose++
		case v1alpha1.ToolAct:
			c.Tools.Act++
		}
		if t.Content {
			c.Tools.Content++
		}
	}
	s.Connectors = append(s.Connectors, c)
	s.notReady("TakeCareConnector", k.Name, k.Status.Conditions)
}

// notReady records kind/name when its Ready condition is absent or not True.
func (s *Status) notReady(kind, name string, conds []metav1.Condition) {
	if r := ready(conds); r == nil || r.Status != string(metav1.ConditionTrue) {
		s.NotReady = append(s.NotReady, kind+"/"+name)
	}
}

func ready(conds []metav1.Condition) *Condition {
	for _, c := range conds {
		if c.Type == v1alpha1.ConditionReady {
			return &Condition{Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: c.Message}
		}
	}
	return nil
}

// conditions copies conds, in the API's order; with notGood, only the
// ones other than Ready not in their good state: not True, or True for
// Degraded, whose good state is False. Degraded=True is a gap the role
// works with (a blind spot, a missing recommended connector type).
func conditions(conds []metav1.Condition, notGood bool) []Condition {
	out := []Condition{}
	for _, c := range conds {
		good := c.Status == metav1.ConditionTrue
		if c.Type == v1alpha1.ConditionDegraded {
			good = c.Status == metav1.ConditionFalse
		}
		if notGood && (c.Type == v1alpha1.ConditionReady || good) {
			continue
		}
		out = append(out, Condition{Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: c.Message})
	}
	return out
}

func run(m metav1.ObjectMeta, st v1alpha1.RunStatus, trigger string) RunRecord {
	r := RunRecord{Name: m.Name, Phase: string(st.Phase), Trigger: trigger, Created: m.CreationTimestamp.UTC()}
	if st.Phase != v1alpha1.RunCompleted {
		r.Reason = st.Reason
	}
	return r
}

func sortedByName[T any, P interface {
	*T
	GetName() string
}](items []T) []T {
	out := slices.Clone(items)
	slices.SortFunc(out, func(a, b T) int { return cmp.Compare(P(&a).GetName(), P(&b).GetName()) })
	return out
}

// newest returns the newestRuns most recently created items, newest first.
func newest[T any](items []T, conv func(T) RunRecord) []RunRecord {
	runs := make([]RunRecord, 0, len(items))
	for _, it := range items {
		runs = append(runs, conv(it))
	}
	slices.SortFunc(runs, func(a, b RunRecord) int {
		return cmp.Or(b.Created.Compare(a.Created), cmp.Compare(a.Name, b.Name))
	})
	return runs[:min(len(runs), newestRuns)]
}

// write prints s as text, ages counted from now.
func (s *Status) write(w io.Writer, now time.Time) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Namespace %s\n\nPLATFORM\n", s.Namespace)
	if len(s.Platforms) == 0 {
		b.WriteString("  none found\n")
	}
	for _, p := range s.Platforms {
		fmt.Fprintf(&b, "  %s\n", p.Name)
		for _, c := range p.Conditions {
			fmt.Fprintf(&b, "    %s\n", c.text())
		}
	}
	b.WriteString("\nROLES\n")
	if len(s.Roles) == 0 {
		b.WriteString("  none found\n")
	}
	for _, r := range s.Roles {
		fmt.Fprintf(&b, "  %s %s, level %s: %s\n", r.Kind, r.Name, orDash(r.Level), readyText(r.Ready))
		for _, c := range r.Conditions {
			fmt.Fprintf(&b, "    %s\n", c.text())
		}
		for _, x := range r.BlindSpots {
			fmt.Fprintf(&b, "    blind spot %s: %s%s\n", x.Entity, x.Reason, suffix(x.Message))
		}
	}
	b.WriteString("\nCONNECTORS\n")
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  NAME\tTYPES\tREADY\tREAD\tPROPOSE\tACT\tCONTENT\tMAP REVISION")
	for _, c := range s.Connectors {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%d\t%d\t%d\t%d\t%s\n", c.Name, strings.Join(c.Types, ","), readyText(c.Ready),
			c.Tools.Read, c.Tools.Propose, c.Tools.Act, c.Tools.Content, orDash(c.MapRevision))
	}
	_ = tw.Flush()
	writeRuns(&b, "SURVEYS", "TRIGGER", s.Surveys, now)
	writeRuns(&b, "INVESTIGATIONS", "ALERT", s.Investigations, now)
	if len(s.NotReady) == 0 {
		b.WriteString("\nEverything is Ready.\n")
	} else {
		fmt.Fprintf(&b, "\nNot Ready: %s\n", strings.Join(s.NotReady, ", "))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeRuns(b *strings.Builder, title, trigger string, runs []RunRecord, now time.Time) {
	fmt.Fprintf(b, "\n%s (newest %d)\n", title, newestRuns)
	tw := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  NAME\tPHASE\t%s\tAGE\tREASON\n", trigger)
	for _, r := range runs {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", r.Name, orDash(r.Phase), orDash(r.Trigger),
			duration.HumanDuration(now.Sub(r.Created)), orDash(r.Reason))
	}
	_ = tw.Flush()
}

func (c Condition) text() string {
	return c.Type + " " + c.Status + reasonText(c.Reason) + suffix(c.Message)
}

func readyText(c *Condition) string {
	if c == nil {
		return "no Ready condition"
	}
	return c.Status + reasonText(c.Reason) + suffix(c.Message)
}

func reasonText(r string) string {
	if r == "" {
		return ""
	}
	return " (" + r + ")"
}

func suffix(m string) string {
	if m == "" {
		return ""
	}
	return ": " + m
}

func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}
