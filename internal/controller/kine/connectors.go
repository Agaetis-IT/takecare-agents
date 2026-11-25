// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kine

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
	// ReasonHistoryMissing: no usable history connector, so the Kiné cannot
	// see the incidents it reviews (spec 11 §2).
	ReasonHistoryMissing = "HistoryConnectorMissing"
	// ReasonConnectorMissing: the report connector is misconfigured, or
	// missing from the Practitioner level (as the Doc's, spec 02 §4).
	ReasonConnectorMissing = "ConnectorMissing"
	// ReasonConnectorsDegraded: an optional connector cannot be read; each
	// review says so in its blind spots, and the Kiné still runs.
	ReasonConnectorsDegraded = "ConnectorsDegraded"
	// ReasonContentToolUnknown: spec.contentTools lists a content tool no
	// bound connector exposes (the Doc's warning, spec 02).
	ReasonContentToolUnknown = "ContentToolUnknown"
)

// binding is what the Kiné's connectors give: the routes to render with
// the types they are bound as, and what is missing.
type binding struct {
	routes []routes.MCP
	types  map[v1alpha1.ConnectorType]bool
	// history says why the history connector cannot be read; problems why
	// the report connector cannot be published to; unavailable names each
	// optional connector that cannot be read.
	history               string
	problems, unavailable []string
	// content are the content tools the bound connectors expose; unknown is
	// set when one of them is missing or was never discovered.
	content map[string]bool
	unknown bool
}

// slots lists the Kiné's connector slots the agent reads, history first.
// The report connector is the operator's: no route.
func slots(k *v1alpha1.TakeCareKine) []struct {
	t     v1alpha1.ConnectorType
	names []string
} {
	c := k.Spec.Connectors
	return []struct {
		t     v1alpha1.ConnectorType
		names []string
	}{
		{v1alpha1.ConnectorHistory, c.History},
		{v1alpha1.ConnectorKnowledge, c.Knowledge},
		{v1alpha1.ConnectorTickets, c.Tickets},
		{v1alpha1.ConnectorChanges, c.Changes},
		{v1alpha1.ConnectorMap, c.Map},
	}
}

// connectors checks the bound connectors and sets ConnectorsReady. A
// connector gets a route once its tools are known, ready or not (a review
// degrades and says so, a blip does not remove the route): its read tools,
// content tools only when spec.contentTools lists them (ADR 0014 §5).
func (r *Reconciler) connectors(ctx context.Context, k *v1alpha1.TakeCareKine, st *status) (binding, error) {
	b := binding{content: map[string]bool{}, types: map[v1alpha1.ConnectorType]bool{}}
	for _, s := range slots(k) {
		for _, name := range s.names {
			problem, err := r.inspect(ctx, k, s.t, name, &b)
			if err != nil {
				return b, err
			}
			switch {
			case problem == "":
			case s.t == v1alpha1.ConnectorHistory:
				b.history = problem
			default:
				b.unavailable = append(b.unavailable, problem)
			}
		}
	}
	if len(k.Spec.Connectors.History) == 0 {
		// Stored before the API required it (the CEL rule says the same).
		b.history = "no history connector is given"
	}
	if err := r.inspectReport(ctx, k, st.level, &b); err != nil {
		return b, err
	}
	unknown := r.contentToolsUnknown(k, b)
	switch {
	case b.history != "":
		st.set(v1alpha1.ConditionConnectorsReady, false, ReasonHistoryMissing,
			strings.Join(slices.Concat([]string{b.history + ": the Kiné cannot see the incidents it reviews"}, b.problems, b.unavailable, unknown), "; "))
	case len(b.problems) > 0:
		st.set(v1alpha1.ConditionConnectorsReady, false, ReasonConnectorMissing, strings.Join(slices.Concat(b.problems, b.unavailable, unknown), "; "))
	case len(b.unavailable) > 0:
		st.set(v1alpha1.ConditionConnectorsReady, false, ReasonConnectorsDegraded,
			strings.Join(slices.Concat(b.unavailable, []string{"each review names them in its blind spots"}, unknown), "; "))
	default:
		st.set(v1alpha1.ConditionConnectorsReady, true, ReasonReady,
			strings.Join(slices.Concat([]string{fmt.Sprintf("%d connector(s) routed on the gateway", len(b.routes))}, unknown), "; "))
	}
	return b, nil
}

// inspect checks one bound connector and routes it when its read tools are
// known; it returns why it cannot be read, empty when it can.
func (r *Reconciler) inspect(ctx context.Context, k *v1alpha1.TakeCareKine, t v1alpha1.ConnectorType, name string, b *binding) (string, error) {
	c, err := r.connector(ctx, k.Namespace, name)
	switch {
	case err != nil:
		return "", err
	case c == nil:
		b.unknown = true
		return fmt.Sprintf("connector %s does not exist", name), nil
	case !slices.Contains(c.Spec.Types, t):
		return fmt.Sprintf("connector %s is bound as %s but does not declare that type", name, t), nil
	}
	for _, tool := range c.Status.Tools {
		if tool.Content {
			b.content[tool.Name] = true
		}
	}
	read, _ := roleagent.Tools(c, k.Spec.ContentTools)
	switch {
	case len(c.Status.Tools) == 0:
		b.unknown = true
		return fmt.Sprintf("connector %s was never discovered: what it exposes is unknown", name), nil
	case len(read) == 0:
		why := ""
		if slices.ContainsFunc(c.Status.Tools, func(t v1alpha1.ToolStatus) bool { return t.Source == v1alpha1.ToolSourceUntrusted }) {
			why = " (a customer's server: its tools are act until its spec.tools lists them as read)"
		}
		return fmt.Sprintf("connector %s has no read tool%s", name, why), nil
	}
	b.addRoute(routes.MCP{Connector: name, Backend: "mcp-" + name, Tools: read})
	b.types[t] = true
	if !meta.IsStatusConditionTrue(c.Status.Conditions, v1alpha1.ConditionReady) {
		return fmt.Sprintf("connector %s is not Ready", name), nil
	}
	return "", nil
}

// addRoute adds a connector's route. A connector named in several slots
// has one route, whose tools are the same whatever the slot.
func (b *binding) addRoute(m routes.MCP) {
	if !slices.ContainsFunc(b.routes, func(x routes.MCP) bool { return x.Connector == m.Connector }) {
		b.routes = append(b.routes, m)
	}
}

// inspectReport checks the report connector, which the agent never gets:
// required from the Practitioner level, where every review is published.
func (r *Reconciler) inspectReport(ctx context.Context, k *v1alpha1.TakeCareKine, level v1alpha1.Level, b *binding) error {
	name := k.Spec.Connectors.Report
	if name == "" {
		if level != v1alpha1.LevelApprentice {
			b.problems = append(b.problems, "no report connector (required from the Practitioner level)")
		}
		return nil
	}
	c, err := r.connector(ctx, k.Namespace, name)
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
// no bound connector exposes, as a phrase for ConnectorsReady, and says it
// in a warning when the phrase is not in the condition already and was not
// said last: once per change of the set, a restart included. Nothing is
// said while a connector that could expose one is unknown.
func (r *Reconciler) contentToolsUnknown(k *v1alpha1.TakeCareKine, b binding) []string {
	if b.unknown {
		return nil
	}
	var names []string
	for _, name := range k.Spec.ContentTools {
		if !b.content[name] {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		r.unsay(k, ReasonContentToolUnknown)
		return nil
	}
	phrase := "spec.contentTools lists " + strings.Join(names, ", ") + ", which no bound connector exposes as a content tool"
	if prev := meta.FindStatusCondition(k.Status.Conditions, v1alpha1.ConditionConnectorsReady); (prev == nil || !strings.Contains(prev.Message, phrase)) && r.say(k, ReasonContentToolUnknown, phrase) {
		r.Recorder.Eventf(k, nil, corev1.EventTypeWarning, ReasonContentToolUnknown, "Reconcile", "%s", phrase)
	}
	return []string{phrase}
}

// connector reads one connector of the Kiné's namespace, nil when there is
// none.
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
