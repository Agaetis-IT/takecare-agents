// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/roleagent"
	"github.com/agaetis-it/takecare-agents/internal/routes"
)

// ConnectorsReady reasons.
const (
	// ReasonClusterMissing: no usable cluster connector, so the Mécano
	// cannot read what runs live (spec 12 §2).
	ReasonClusterMissing = "ClusterConnectorMissing"
	// ReasonCodeMissing: no usable code connector, so the Mécano cannot
	// read what Git declares.
	ReasonCodeMissing = "CodeConnectorMissing"
	// ReasonConnectorMissing: the report connector is misconfigured, or
	// missing from the Practitioner level (as the Doc's, spec 02 §4).
	ReasonConnectorMissing = "ConnectorMissing"
	// ReasonConnectorsDegraded: an optional connector cannot be read, or
	// the propose route cannot propose; each maintenance says so in its
	// blind spots, and the Mécano still runs.
	ReasonConnectorsDegraded = "ConnectorsDegraded"
	// ReasonContentToolUnknown: spec.contentTools lists a content tool no
	// bound connector exposes (the Doc's warning, spec 02).
	ReasonContentToolUnknown = "ContentToolUnknown"
)

// ReleasesTools are the releases connector's tools (spec 12 §7.1), the
// only ones its route admits: the collector's, never the agent's.
var ReleasesTools = []string{"cycles_get", "sources_list", "versions_list"}

// binding is what the Mécano's connectors give: the routes to render, the
// tools given to the agent, and what is missing.
type binding struct {
	routes []routes.MCP
	// agent are the tools given to the agent, by connector: never a
	// releases or copy connector's, which the collector reads.
	agent map[string][]string
	// types are the types the agent's connectors are given for, for the
	// instructions.
	types map[v1alpha1.ConnectorType]bool
	// cluster and code say why no cluster or code connector can be read;
	// problems why the report connector cannot be published to;
	// unavailable names each optional connector that cannot be read, and
	// what keeps the propose route from proposing.
	cluster, code         string
	problems, unavailable []string
	// content are the content tools the agent's connectors expose; unknown
	// is set when one of them is missing or was never discovered.
	content map[string]bool
	unknown bool
}

// slot is one of the Mécano's connector slots read through a route.
type slot struct {
	name  string
	t     v1alpha1.ConnectorType
	names []string
	// agent says the agent is given the slot's tools; the others are the
	// collector's (spec 12 §2, §7.1, §8).
	agent bool
	// only bounds the route's allow-list to these tools; nil for all the
	// read tools.
	only []string
}

// slots lists the slots read through a route, cluster and code first. The
// propose slot has its own rule (propose); the report connector is the
// operator's: no route.
func slots(m *v1alpha1.TakeCareMecano) []slot {
	c := m.Spec.Connectors
	return []slot{
		{"cluster", v1alpha1.ConnectorCluster, c.Cluster, true, nil},
		{"code", v1alpha1.ConnectorCode, c.Code, true, nil},
		{"knowledge", v1alpha1.ConnectorKnowledge, c.Knowledge, true, nil},
		{"changes", v1alpha1.ConnectorChanges, c.Changes, true, nil},
		{"history", v1alpha1.ConnectorHistory, c.History, true, nil},
		{"map", v1alpha1.ConnectorMap, c.Map, true, nil},
		{"copy", v1alpha1.ConnectorCluster, c.Copy, false, nil},
		{"releases", v1alpha1.ConnectorReleases, c.Releases, false, ReleasesTools},
	}
}

// connectors checks the bound connectors and sets ConnectorsReady. A
// connector gets a route once its tools are known, ready or not (a
// maintenance degrades and says so, a blip does not remove the route): its
// read tools, content tools only when spec.contentTools lists them (ADR
// 0014 §5), and on the propose slot its propose tool (propose).
func (r *Reconciler) connectors(ctx context.Context, m *v1alpha1.TakeCareMecano, role routes.Role, st *status) (binding, error) {
	b := binding{agent: map[string][]string{}, types: map[v1alpha1.ConnectorType]bool{}, content: map[string]bool{}}
	for _, s := range slots(m) {
		var failed []string
		usable := 0
		for _, name := range s.names {
			problem, err := r.inspect(ctx, m, s, name, &b)
			if err != nil {
				return b, err
			}
			if problem == "" {
				usable++
			} else {
				failed = append(failed, problem)
			}
		}
		missing := "no " + s.name + " connector is given"
		if len(failed) > 0 {
			missing = strings.Join(failed, "; ")
		}
		// Without a usable cluster and code connector, live and Git cannot
		// be compared; any other slot, or a second cluster or code
		// connector, is a blind spot.
		switch {
		case s.name == "cluster" && usable == 0:
			b.cluster = missing
		case s.name == "code" && usable == 0:
			b.code = missing
		default:
			b.unavailable = append(b.unavailable, failed...)
		}
	}
	if err := r.propose(ctx, m, st.level, role, &b); err != nil {
		return b, err
	}
	if err := r.inspectReport(ctx, m, st.level, &b); err != nil {
		return b, err
	}
	unknown := r.contentToolsUnknown(m, b)
	switch {
	case b.cluster != "":
		st.set(v1alpha1.ConditionConnectorsReady, false, ReasonClusterMissing,
			strings.Join(slices.Concat([]string{b.cluster + ": the Mécano cannot read what runs live"}, b.problems, b.unavailable, unknown), "; "))
	case b.code != "":
		st.set(v1alpha1.ConditionConnectorsReady, false, ReasonCodeMissing,
			strings.Join(slices.Concat([]string{b.code + ": the Mécano cannot read what Git declares"}, b.problems, b.unavailable, unknown), "; "))
	case len(b.problems) > 0:
		st.set(v1alpha1.ConditionConnectorsReady, false, ReasonConnectorMissing, strings.Join(slices.Concat(b.problems, b.unavailable, unknown), "; "))
	case len(b.unavailable) > 0:
		st.set(v1alpha1.ConditionConnectorsReady, false, ReasonConnectorsDegraded,
			strings.Join(slices.Concat(b.unavailable, []string{"each maintenance names them in its blind spots"}, unknown), "; "))
	default:
		st.set(v1alpha1.ConditionConnectorsReady, true, ReasonReady,
			strings.Join(slices.Concat([]string{fmt.Sprintf("%d connector(s) routed on the gateway", len(b.routes))}, unknown), "; "))
	}
	return b, nil
}

// inspect checks one bound connector and routes it when its read tools are
// known; it returns why it cannot be read, empty when it can.
func (r *Reconciler) inspect(ctx context.Context, m *v1alpha1.TakeCareMecano, s slot, name string, b *binding) (string, error) {
	c, err := r.connector(ctx, m.Namespace, name)
	switch {
	case err != nil:
		return "", err
	case c == nil:
		b.unknown = b.unknown || s.agent
		return fmt.Sprintf("connector %s does not exist", name), nil
	case !slices.Contains(c.Spec.Types, s.t):
		return fmt.Sprintf("connector %s is bound as %s but does not declare the type %s", name, s.name, s.t), nil
	}
	content := m.Spec.ContentTools
	if s.agent {
		for _, tool := range c.Status.Tools {
			if tool.Content {
				b.content[tool.Name] = true
			}
		}
	} else {
		// The collector reads state, never content.
		content = nil
	}
	read, _ := roleagent.Tools(c, content)
	if s.only != nil {
		read = slices.DeleteFunc(read, func(t string) bool { return !slices.Contains(s.only, t) })
	}
	switch {
	case len(c.Status.Tools) == 0:
		b.unknown = b.unknown || s.agent
		return fmt.Sprintf("connector %s was never discovered: what it exposes is unknown", name), nil
	case len(read) == 0:
		why := ""
		if slices.ContainsFunc(c.Status.Tools, func(t v1alpha1.ToolStatus) bool { return t.Source == v1alpha1.ToolSourceUntrusted }) {
			why = " (a customer's server: its tools are act until its spec.tools lists them as read)"
		}
		return fmt.Sprintf("connector %s has no read tool%s", name, why), nil
	}
	b.addRoute(routes.MCP{Connector: name, Backend: "mcp-" + name, Tools: read})
	if s.agent {
		b.agent[name] = union(b.agent[name], read)
		b.types[s.t] = true
	}
	if !meta.IsStatusConditionTrue(c.Status.Conditions, v1alpha1.ConditionReady) {
		return fmt.Sprintf("connector %s is not Ready", name), nil
	}
	return "", nil
}

// addRoute adds a slot's route. A connector named in several slots has one
// route, the union of what each slot allows: two routes of one name would
// be written in turn at every reconcile, each write undoing the other.
func (b *binding) addRoute(m routes.MCP) {
	for i := range b.routes {
		if b.routes[i].Connector == m.Connector {
			b.routes[i].Tools = union(b.routes[i].Tools, m.Tools)
			return
		}
	}
	b.routes = append(b.routes, m)
}

// routed says whether a connector already has a route.
func (b *binding) routed(name string) bool {
	return slices.ContainsFunc(b.routes, func(m routes.MCP) bool { return m.Connector == name })
}

func union(a, b []string) []string {
	out := slices.Concat(a, b)
	slices.Sort(out)
	return slices.Compact(out)
}

// inspectReport checks the report connector, which the agent never gets:
// required from the Practitioner level, where every maintenance is
// published.
func (r *Reconciler) inspectReport(ctx context.Context, m *v1alpha1.TakeCareMecano, level v1alpha1.Level, b *binding) error {
	name := m.Spec.Connectors.Report
	if name == "" {
		if level != v1alpha1.LevelApprentice {
			b.problems = append(b.problems, "no report connector (required from the Practitioner level)")
		}
		return nil
	}
	c, err := r.connector(ctx, m.Namespace, name)
	switch {
	case err != nil:
		return err
	case c == nil:
		b.problems = append(b.problems, fmt.Sprintf("report connector %s does not exist", name))
	case !slices.Contains(c.Spec.Types, v1alpha1.ConnectorReport):
		b.problems = append(b.problems, fmt.Sprintf("connector %s is bound as report but does not declare that type", name))
	case !meta.IsStatusConditionTrue(c.Status.Conditions, v1alpha1.ConditionReady):
		b.unavailable = append(b.unavailable, fmt.Sprintf("report connector %s is not Ready", name))
	}
	return nil
}

// contentToolsUnknown names the content tools spec.contentTools lists that
// no connector given to the agent exposes, as a phrase for
// ConnectorsReady, and says it in a warning when the phrase is not in the
// condition already and was not said last: once per change of the set, a
// restart included. Nothing is said while a connector that could expose
// one is unknown.
func (r *Reconciler) contentToolsUnknown(m *v1alpha1.TakeCareMecano, b binding) []string {
	if b.unknown {
		return nil
	}
	var names []string
	for _, name := range m.Spec.ContentTools {
		if !b.content[name] {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		r.unsay(m, ReasonContentToolUnknown)
		return nil
	}
	phrase := "spec.contentTools lists " + strings.Join(names, ", ") + ", which no bound connector exposes as a content tool"
	if prev := meta.FindStatusCondition(m.Status.Conditions, v1alpha1.ConditionConnectorsReady); (prev == nil || !strings.Contains(prev.Message, phrase)) && r.say(m, ReasonContentToolUnknown, phrase) {
		r.Recorder.Eventf(m, nil, corev1.EventTypeWarning, ReasonContentToolUnknown, "Reconcile", "%s", phrase)
	}
	return []string{phrase}
}

// connector reads one connector of the Mécano's namespace, nil when there
// is none.
func (r *Reconciler) connector(ctx context.Context, namespace, name string) (*v1alpha1.TakeCareConnector, error) {
	var c v1alpha1.TakeCareConnector
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &c); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get connector %s: %w", name, err)
	}
	return &c, nil
}
