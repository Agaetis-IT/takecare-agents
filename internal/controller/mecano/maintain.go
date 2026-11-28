// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/controller/connector"
	"github.com/agaetis-it/takecare-agents/internal/coverage"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	mecanoagent "github.com/agaetis-it/takecare-agents/internal/mecano"
	"github.com/agaetis-it/takecare-agents/internal/mecano/collect"
	"github.com/agaetis-it/takecare-agents/internal/roleagent"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
	"github.com/agaetis-it/takecare-agents/internal/usage"
)

// Why a maintenance failed, in status.reason: a fixed phrase per class,
// never the model's or a server's text.
const (
	failNotReady    = "the Mécano is not ready to maintain"
	failGateway     = "the gateway is not ready"
	failCollector   = "the collector did not finish within half the maintenance's timeout"
	failNotFinished = "the maintenance did not finish"
	failQuestion    = "the maintenance asked a question, which no one answers during a maintenance; the question is not kept"
)

// usageNotRun is why a maintenance that never ran has no usage.
const usageNotRun = "the maintenance did not run"

// Blind spots and the event of a propose route not proven at a
// maintenance's start (spec 12 §6.4): the message then names no
// repository.
const (
	// ReasonProposeUnproven is the Mécano's warning event.
	ReasonProposeUnproven = "ProposeRuleUnproven"
	proposeNotEnforced    = "the gateway does not enforce the rule on the propose route at its current generation: no pull request in this maintenance"
	proposeCanaryFailed   = "the gateway did not refuse the propose route's canary (%s): no pull request in this maintenance"
)

// proposal is what the propose route allows in one maintenance.
type proposal struct {
	// listed are the repositories a pull request may be on; offered are
	// the ones the message names, empty unless the rule is proven.
	listed, offered []string
	spot            *v1alpha1.NotSeen
}

// maintain collects, runs the agent on the facts and reads its answer.
func (w *Runner) maintain(ctx context.Context, mt *v1alpha1.TakeCareMaintenance, m *v1alpha1.TakeCareMecano, lvl v1alpha1.Level) (v1alpha1.TakeCareMaintenanceStatus, error) {
	failed := func(reason string) v1alpha1.TakeCareMaintenanceStatus {
		out := v1alpha1.TakeCareMaintenanceStatus{Phase: v1alpha1.RunFailed, Reason: reason[:min(len(reason), maxReason)]}
		sayUsage(&out.Conditions, nil, usageNotRun, mt.Generation)
		return out
	}
	if m.Spec.Suspend || !meta.IsStatusConditionTrue(m.Status.Conditions, v1alpha1.ConditionReady) {
		return failed(failNotReady), nil
	}
	p, err := platformOf(ctx, w.Client)
	if err != nil || p == nil || p.Status.GatewayURL == "" {
		return failed(failGateway), err
	}
	role := RoleOf(m)
	route := func(name string) string { return p.Status.GatewayURL + routes.MCPPath(role, name) }
	timeout := timeoutOf(m)
	res, err := w.collect(ctx, mt, m, route, timeout)
	if err != nil {
		if ctx.Err() != nil {
			return v1alpha1.TakeCareMaintenanceStatus{}, ctx.Err()
		}
		return failed(failCollector), err
	}
	inv := res.Inventory
	spots := slices.Concat(res.BlindSpots, w.connectorSpots(ctx, m))
	prop := proposal{}
	if lvl != v1alpha1.LevelApprentice && !mt.Spec.Silent {
		if prop, err = w.propose(ctx, m, role, route); err != nil {
			return failed(failNotFinished), err
		}
		if prop.spot != nil {
			spots = append(spots, *prop.spot)
		}
	}
	msg, err := mecanoagent.Message(mecanoagent.Request{
		Maintenance: mt, Scope: m.Spec.Scope, Level: lvl, Inventory: inv, ClusterVersion: res.ClusterVersion,
		BlindSpots: sortSpots(spots), DatasetDate: w.Dataset.Date, Repositories: prop.offered, Copy: len(m.Spec.Connectors.Copy) > 0,
	})
	if err != nil {
		return failed(failNotFinished), err
	}
	run := engine.Run{Namespace: w.EngineNamespace, Agent: mecanoagent.AgentName(m), GatewayURL: p.Status.GatewayURL, Message: msg, ConversationID: mt.Name}
	var meter *usage.Meter
	unknown := "the operator restarted during the maintenance: its start was not measured"
	if e := mt.Status.Engine; e == nil || e.TaskID == "" {
		meter, unknown = w.meter(ctx, m, p)
	}
	ran, err := w.execute(ctx, mt, run, timeout)
	if ctx.Err() != nil {
		return v1alpha1.TakeCareMaintenanceStatus{}, ctx.Err()
	}
	var u *v1alpha1.Usage
	if meter != nil {
		u, unknown = meter.End(ctx, w.ScrapeWait)
	}
	out := v1alpha1.TakeCareMaintenanceStatus{Inventory: &inv}
	switch {
	case err != nil || ran.Outcome != engine.OutcomeCompleted:
		reason := failNotFinished
		switch {
		case ran.Reason == engine.ReasonQuestionAsked:
			reason = failQuestion
		case ran.TimedOut:
			reason += ": " + ran.Reason // what the follow saw, never the engine's text
		}
		out = failed(reason)
	default:
		out.Phase = v1alpha1.RunCompleted
		out.Counts = &v1alpha1.MaintenanceCounts{}
		a, perr := mecanoagent.Parse(ran.Text)
		if perr != nil {
			out.Reason = mecanoagent.ErrUnreadable.Error() // the phrase only: never an excerpt
			log.FromContext(ctx).Info(out.Reason, "maintenance", mt.Name, "task", ran.TaskID)
			break
		}
		rec := mecanoagent.Record(a, lvl, mecanoagent.Facts{
			Inventory: inv, Copy: copyFacts(res.Copy), Repositories: prop.listed, Branch: mecanoagent.Branch(mt.Name),
			Silent: mt.Spec.Silent, Unproven: prop.spot != nil, ClusterVersion: res.ClusterVersion,
			Known: mecanoagent.DatasetVersions(w.Dataset),
		})
		out.Findings, out.FindingsOmitted, out.Plan, out.Counts = rec.Findings, rec.FindingsOmitted, rec.Plan, &rec.Counts
		spots = append(spots, rec.BlindSpots...)
	}
	out.BlindSpots = sortSpots(spots)
	out.BlindSpots = out.BlindSpots[:min(len(out.BlindSpots), mecanoagent.MaxBlindSpots)]
	out.Usage = u
	sayUsage(&out.Conditions, u, unknown, mt.Generation)
	return out, nil
}

// collect reads the inventory through the Mécano's routes, within half
// the maintenance's timeout from its start (spec 12 §4), in its own span.
func (w *Runner) collect(ctx context.Context, mt *v1alpha1.TakeCareMaintenance, m *v1alpha1.TakeCareMecano, route func(string) string, timeout time.Duration) (collect.Result, error) {
	ctx, span := w.tracer().Start(ctx, "collect")
	defer span.End()
	in, err := w.input(ctx, m, route)
	if err != nil {
		return collect.Result{}, err
	}
	in.Components, in.Dataset, in.Now = mt.Spec.Components, w.Dataset, w.now()
	// Half the timeout from the start; a maintenance resumed with its task
	// after a leader change collects again, from the resume: its task's
	// deadline still counts from the start.
	deadline := mt.Status.StartedAt.Add(timeout / 2)
	if e := mt.Status.Engine; e != nil && e.TaskID != "" {
		deadline = in.Now.Add(timeout / 2)
	}
	cctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	res, err := collect.Collect(cctx, w.Caller, in)
	if err != nil {
		span.SetStatus(codes.Error, failCollector)
		return collect.Result{}, err
	}
	span.SetAttributes(attribute.Int(semconv.MaintenanceComponents, len(res.Inventory.Components)),
		attribute.Int(semconv.MaintenanceBlindSpots, len(res.BlindSpots)))
	return res, nil
}

// input binds the collector to the Mécano's connectors: each one usable
// through its route, a cluster connector with the scope's namespaces it
// reads. A connector that cannot be used is left out; connectorSpots says
// so.
func (w *Runner) input(ctx context.Context, m *v1alpha1.TakeCareMecano, route func(string) string) (collect.Input, error) {
	in := collect.Input{CertificateWarning: 30 * 24 * time.Hour}
	if cw := m.Spec.Maintenance.CertificateWarning; cw != nil && cw.Duration > 0 {
		in.CertificateWarning = cw.Duration
	}
	for _, s := range slots(m) {
		for _, name := range s.names {
			c, err := w.connector(ctx, m.Namespace, name)
			if err != nil {
				return in, err
			}
			if c == nil || !slices.Contains(c.Spec.Types, s.t) {
				continue
			}
			ep := mcp.Endpoint{URL: route(name), Types: []v1alpha1.ConnectorType{s.t}, Collector: true, StateTools: map[string]bool{}}
			for _, t := range c.Status.Tools {
				ep.StateTools[t.Name] = t.Class == v1alpha1.ToolRead && !t.Content
			}
			src := collect.Source{Connector: name, Endpoint: ep}
			switch s.name {
			case slotCluster, slotCopy:
				ns := namespacesOf(m, c)
				if len(ns) == 0 {
					continue
				}
				src.Endpoint.Namespaces = ns
				cl := collect.Cluster{Source: src, Namespaces: ns}
				if s.name == slotCopy {
					in.Copy = &cl
				} else {
					in.Clusters = append(in.Clusters, cl)
				}
			case slotCode:
				in.Code = append(in.Code, src)
			case slotReleases:
				in.Releases = append(in.Releases, src)
			}
		}
	}
	return in, nil
}

// namespacesOf are the scope's namespaces a cluster connector reads: the
// scope's namespaces and its environment entities' on that cluster (ADR
// 0016 §5). The copy is read in the same namespaces, on its own cluster.
func namespacesOf(m *v1alpha1.TakeCareMecano, c *v1alpha1.TakeCareConnector) []string {
	if c.Spec.Kubernetes == nil {
		return nil
	}
	cluster := connector.ClusterOf(c)
	wanted := map[v1alpha1.Namespace]bool{}
	for _, ns := range m.Spec.Scope.Namespaces {
		wanted[ns] = true
	}
	for _, e := range m.Spec.Scope.Entities {
		if t, name := coverage.TypeName(e); t == "environment" {
			if p := coverage.EnvironmentPlace(name); p.Cluster == cluster {
				wanted[p.Namespace] = true
			}
		}
	}
	var out []string
	for _, ns := range c.Spec.Kubernetes.Namespaces {
		if wanted[ns] {
			out = append(out, string(ns))
		}
	}
	slices.Sort(out)
	return out
}

// connectorSpots are the Mécano's connectors a maintenance could not
// read: missing, not Ready, or a content tool spec.contentTools does not
// list (spec 12 §2).
func (w *Runner) connectorSpots(ctx context.Context, m *v1alpha1.TakeCareMecano) []v1alpha1.NotSeen {
	var out []v1alpha1.NotSeen
	for _, s := range slots(m) {
		for _, name := range s.names {
			c, err := w.connector(ctx, m.Namespace, name)
			switch {
			case err != nil || c == nil:
				out = append(out, v1alpha1.NotSeen{Ref: name, Reason: "the " + s.name + " connector does not exist"})
			case !meta.IsStatusConditionTrue(c.Status.Conditions, v1alpha1.ConditionReady):
				out = append(out, v1alpha1.NotSeen{Ref: name, Reason: "the " + s.name + " connector is not Ready"})
			}
			if c == nil || !s.agent {
				continue
			}
			for _, t := range c.Status.Tools {
				if t.Content && !slices.Contains(m.Spec.ContentTools, t.Name) {
					out = append(out, v1alpha1.NotSeen{Ref: t.Name, Reason: "a content tool spec.contentTools does not list: the maintenance did not read it"})
				}
			}
		}
	}
	return out
}

// propose decides what the propose route allows in this maintenance
// (spec 12 §6.4): the repositories of maintenance.repositories the
// propose connector lists; offered in the message only when the gateway's
// status enforces the rule rendered now, at its current generation, and
// the gateway refuses the canary with a 403. Otherwise the message names
// no repository, the maintenance says why in a blind spot, and the
// Mécano gets a warning event. The propose server still refuses any
// repository it does not list.
func (w *Runner) propose(ctx context.Context, m *v1alpha1.TakeCareMecano, role routes.Role, route func(string) string) (proposal, error) {
	if len(m.Spec.Connectors.Propose) == 0 {
		return proposal{}, nil
	}
	name := m.Spec.Connectors.Propose[0]
	c, err := w.connector(ctx, m.Namespace, name)
	if err != nil || c == nil || c.Spec.Propose == nil {
		return proposal{}, err
	}
	var listed []string
	for _, r := range m.Spec.Maintenance.Repositories {
		if r.Connector == name && slices.ContainsFunc(c.Spec.Propose.Repositories, func(p v1alpha1.ProposeRepository) bool { return p.Repository == r.Repository }) {
			listed = append(listed, r.Repository)
		}
	}
	if len(listed) == 0 {
		return proposal{}, nil
	}
	slices.Sort(listed)
	out := proposal{listed: listed}
	enforced, _, err := routes.CallRuleEnforced(ctx, w.Reader, role, name, ProposeCallRule(listed))
	if err != nil {
		return out, err
	}
	reason := proposeNotEnforced
	if enforced {
		verdict := w.canary(ctx, m, name, route(name))
		if verdict == mcp.CanaryRefused {
			out.offered = listed
			return out, nil
		}
		reason = fmt.Sprintf(proposeCanaryFailed, verdict)
	}
	out.spot = &v1alpha1.NotSeen{Ref: name, Reason: reason}
	if w.Recorder != nil {
		w.Recorder.Eventf(m, nil, corev1.EventTypeWarning, ReasonProposeUnproven, "Maintenance", "%s", reason)
	}
	return out, nil
}

// canary proves the propose route's rule on the data plane at the time of
// use, in its own span: the status can say Accepted while the proxy keeps
// an older configuration. Only a refusal counts.
func (w *Runner) canary(ctx context.Context, m *v1alpha1.TakeCareMecano, name, url string) mcp.CanaryVerdict {
	if w.Canary == nil {
		return mcp.CanaryPending
	}
	ctx, span := w.tracer().Start(ctx, "check call rule")
	defer span.End()
	verdict, status := w.Canary.ProposeCanary(ctx, mcp.Endpoint{URL: url})
	span.SetAttributes(attribute.String(semconv.MaintenanceConnector, name), attribute.String(semconv.CallRuleVerdict, string(verdict)))
	if status != 0 {
		span.SetAttributes(attribute.Int(semconv.HTTPResponseStatusCode, status))
	}
	if verdict != mcp.CanaryRefused {
		span.SetStatus(codes.Error, string(verdict))
		log.FromContext(ctx).Info("the gateway's rule on the propose route is not proven", "mecano", m.Name, "connector", name, "verdict", string(verdict))
	}
	return verdict
}

// meter measures a maintenance on the Mécano's own model route: one
// maintenance runs at a time per Mécano.
func (w *Runner) meter(ctx context.Context, m *v1alpha1.TakeCareMecano, p *v1alpha1.TakeCarePlatform) (*usage.Meter, string) {
	if w.Usage == nil {
		return nil, "the operator measures no usage"
	}
	src := w.Usage(p)
	if src == nil {
		return nil, "the platform declares no Prometheus (observability.metrics.prometheusURL)"
	}
	name, _, _ := roleagent.Model(p, m.Spec.Model)
	if name == "" {
		return nil, "the Mécano's model is not resolved"
	}
	var pricing *v1alpha1.Pricing
	for _, spec := range p.Spec.Models {
		if spec.Name == name {
			pricing = spec.Pricing
		}
	}
	return usage.StartMeter(ctx, usage.ForRoute(src, routes.ModelRoute(RoleOf(m), name)), pricing, false)
}

// connector reads one connector of the Mécano's namespace, nil when there
// is none.
func (w *Runner) connector(ctx context.Context, namespace, name string) (*v1alpha1.TakeCareConnector, error) {
	var c v1alpha1.TakeCareConnector
	if err := w.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &c); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("get connector %s: %w", name, err)
	}
	return &c, nil
}

// copyFacts is what the copy runs, by component.
func copyFacts(in []collect.CopyComponent) map[string]mecanoagent.CopyFact {
	out := map[string]mecanoagent.CopyFact{}
	for _, c := range in {
		out[c.Key] = mecanoagent.CopyFact{Version: c.Version, Ready: c.Ready, Evidence: c.Evidence}
	}
	return out
}

// sortSpots orders blind spots by reference, then reason, without
// duplicates: a stable status from one maintenance to the next.
func sortSpots(in []v1alpha1.NotSeen) []v1alpha1.NotSeen {
	out := slices.Clone(in)
	slices.SortFunc(out, func(a, b v1alpha1.NotSeen) int {
		return cmp.Or(strings.Compare(a.Ref, b.Ref), strings.Compare(a.Reason, b.Reason))
	})
	return slices.Compact(out)
}
