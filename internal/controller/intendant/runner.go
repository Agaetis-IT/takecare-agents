// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mapschema "github.com/agaetis-it/takecare-agents/api/map"
	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/controller/connector"
	"github.com/agaetis-it/takecare-agents/internal/coverage"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/runs"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
)

// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaresurveys/status,verbs=get;update;patch,namespace=takecare-system

// Bounds of a survey's status (spec 10 §7): the first proposals and the
// not-seen lines of a layer; the full set goes elsewhere (I1d-3b).
const (
	maxProposals = 20
	maxNotSeen   = 50
	maxBlind     = 200
)

// Why a survey failed, in status.reason: a fixed phrase per class, never a
// server's or a tool's text (the import's rule); the full error is logged.
const (
	failMap       = "the Map could not be read"
	failIntendant = "the Intendant could not be read"
	failGateway   = "the gateway is not ready"
	failImport    = "the import failed"
	maxReason     = 256
)

// failed is a survey's failure: its class for the status, its error for
// the log.
type failed struct {
	class string
	err   error
}

func (f failed) Error() string { return f.class + ": " + f.err.Error() }
func (f failed) Unwrap() error { return f.err }

func fail(class string, err error) error { return failed{class: class, err: err} }

// SurveyQueuer feeds the runner: a survey with no phase is Queued at once,
// a Queued one goes into the queue.
type SurveyQueuer struct {
	client.Client
	Queue *runs.Queue
}

// Reconcile queues one survey.
func (q *SurveyQueuer) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var s v1alpha1.TakeCareSurvey
	if err := q.Get(ctx, req.NamespacedName, &s); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	switch s.Status.Phase {
	case "":
		before := s.DeepCopy()
		s.Status.Phase = v1alpha1.RunQueued
		if err := q.Status().Patch(ctx, &s, client.MergeFrom(before)); err != nil {
			return ctrl.Result{}, fmt.Errorf("queue survey %s: %w", s.Name, err)
		}
		q.Queue.Add(req.NamespacedName, s.CreationTimestamp.Time)
	case v1alpha1.RunQueued:
		q.Queue.Add(req.NamespacedName, s.CreationTimestamp.Time)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager watches the surveys.
func (q *SurveyQueuer) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("takecaresurvey").For(&v1alpha1.TakeCareSurvey{}).Complete(q)
}

// Runner runs the queued surveys, one per Intendant at a time, oldest
// first: an Intendant whose survey runs holds back only its own surveys
// (the investigation worker's rule, #102).
type Runner struct {
	Client client.Client
	// Reader reads a survey and its Intendant outside the cache.
	Reader client.Reader
	Queue  *runs.Queue
	Caller discovery.Caller
	// Route is the URL of the Intendant's route to a connector: the
	// gateway's address and the route's path when nil.
	Route func(gatewayURL string, role routes.Role, connector string) string
	Now   func() time.Time
	// GateRetry is how long the runner waits when every queued survey is
	// held back.
	GateRetry time.Duration
	// Tracer records a survey's trace (spec 10 §10); none when nil.
	Tracer trace.Tracer
	// Metrics, when set, hold what each Intendant's last survey proposes.
	Metrics *SurveyMetrics

	mu      sync.Mutex
	running map[types.NamespacedName]bool // by Intendant
	freed   chan struct{}
	wg      sync.WaitGroup
}

// NeedLeaderElection implements manager.LeaderElectionRunnable: the runs
// are the leader's.
func (*Runner) NeedLeaderElection() bool { return true }

func (w *Runner) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Start resumes what a stopped operator left Running, then runs surveys
// until ctx ends.
func (w *Runner) Start(ctx context.Context) error {
	w.running, w.freed = map[types.NamespacedName]bool{}, make(chan struct{}, 1)
	if w.GateRetry <= 0 {
		w.GateRetry = 5 * time.Second
	}
	defer w.wg.Wait()
	if err := w.resume(ctx); err != nil {
		log.FromContext(ctx).Error(err, "resume the surveys left running")
	}
	ownerOf := map[types.NamespacedName]types.NamespacedName{}
	blocked := map[types.NamespacedName]bool{}
	held := func(k types.NamespacedName) bool { return blocked[ownerOf[k]] }
	for {
		key, ok, err := w.Queue.NextExcept(ctx, held)
		if err != nil {
			return nil //nolint:nilerr // ctx ended: the manager stops
		}
		if ok {
			if owner, busy := w.admit(ctx, key); busy {
				ownerOf[key], blocked[owner] = owner, true
				w.Queue.Add(key, time.Time{})
				continue
			}
			delete(ownerOf, key)
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-w.freed:
		case <-w.Queue.Added():
		case <-time.After(w.GateRetry):
		}
		clear(blocked)
	}
}

// resume puts back in the queue every survey a stopped operator left
// Running, from the start: the import is a snapshot, so a re-run is safe,
// and no half status is kept.
func (w *Runner) resume(ctx context.Context) error {
	var list v1alpha1.TakeCareSurveyList
	if err := w.Reader.List(ctx, &list); err != nil {
		return fmt.Errorf("list surveys: %w", err)
	}
	for i := range list.Items {
		s := &list.Items[i]
		if s.Status.Phase != v1alpha1.RunRunning {
			continue
		}
		before := s.DeepCopy()
		s.Status = v1alpha1.TakeCareSurveyStatus{Phase: v1alpha1.RunQueued, Attempts: before.Status.Attempts}
		if err := w.Client.Status().Patch(ctx, s, client.MergeFrom(before)); err != nil {
			return fmt.Errorf("put survey %s back in the queue: %w", s.Name, err)
		}
		w.Queue.Add(client.ObjectKeyFromObject(s), s.CreationTimestamp.Time)
	}
	return nil
}

// admit starts a queued survey unless its Intendant already runs one; it
// returns that Intendant and true when the survey must wait.
func (w *Runner) admit(ctx context.Context, key types.NamespacedName) (types.NamespacedName, bool) {
	var s v1alpha1.TakeCareSurvey
	if err := w.Reader.Get(ctx, key, &s); err != nil || s.Status.Phase != v1alpha1.RunQueued {
		return types.NamespacedName{}, false
	}
	owner := types.NamespacedName{Namespace: s.Namespace, Name: s.Spec.IntendantRef.Name}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running[owner] {
		return owner, true
	}
	w.running[owner] = true
	w.wg.Go(func() {
		defer w.release(owner)
		w.run(ctx, &s)
	})
	return owner, false
}

func (w *Runner) release(owner types.NamespacedName) {
	w.mu.Lock()
	delete(w.running, owner)
	w.mu.Unlock()
	select {
	case w.freed <- struct{}{}:
	default:
	}
}

// run surveys the ground once and writes the outcome; a stopped operator
// leaves the survey Running for the next leader to run again.
func (w *Runner) tracer() trace.Tracer {
	if w.Tracer != nil {
		return w.Tracer
	}
	return noop.NewTracerProvider().Tracer("")
}

func (w *Runner) run(ctx context.Context, s *v1alpha1.TakeCareSurvey) {
	// The survey's trace: its root is the Intendant's invoke_agent, the
	// agent pass (I1e) to come as its child.
	ctx, span := w.tracer().Start(ctx, semconv.OperationInvokeAgent+" "+Kind, trace.WithAttributes(
		attribute.String(semconv.GenAIOperationName, semconv.OperationInvokeAgent),
		attribute.String(semconv.GenAIAgentName, Kind),
		attribute.String(semconv.SurveyName, s.Name),
		attribute.String(semconv.SurveyTrigger, string(s.Spec.Trigger))))
	defer span.End()
	traceID := span.SpanContext().TraceID().String()
	logger := log.FromContext(ctx).WithValues("survey", s.Name, "traceID", traceID)
	now := metav1.NewTime(w.now())
	if err := w.patch(ctx, s, func(st *v1alpha1.TakeCareSurveyStatus) {
		st.Phase, st.StartedAt, st.Attempts, st.TraceID = v1alpha1.RunRunning, &now, st.Attempts+1, traceID
	}); err != nil {
		logger.Error(err, "mark the survey running")
		return
	}
	out, res, err := w.survey(ctx, s)
	if ctx.Err() != nil {
		return
	}
	done := metav1.NewTime(w.now())
	if err != nil {
		reason := failImport
		if f, ok := errors.AsType[failed](err); ok {
			reason = f.class
		}
		logger.Error(err, "the survey failed", "intendant", s.Spec.IntendantRef.Name)
		span.SetStatus(codes.Error, reason)
		out = v1alpha1.TakeCareSurveyStatus{Phase: v1alpha1.RunFailed, Reason: reason[:min(len(reason), maxReason)]}
	} else {
		out.Phase = v1alpha1.RunCompleted
		span.SetAttributes(attribute.Int(semconv.SurveyProposals, len(res.Proposals)))
		w.Metrics.Record(s.Spec.IntendantRef.Name, res)
	}
	if err := w.patch(ctx, s, func(st *v1alpha1.TakeCareSurveyStatus) {
		out.StartedAt, out.Attempts, out.CompletedAt, out.TraceID = st.StartedAt, st.Attempts, &done, traceID
		*st = out
	}); err != nil {
		logger.Error(err, "record the survey's outcome")
	}
}

// survey reads the Map and the ground through the Intendant's routes.
func (w *Runner) survey(ctx context.Context, s *v1alpha1.TakeCareSurvey) (v1alpha1.TakeCareSurveyStatus, discovery.Result, error) {
	var out v1alpha1.TakeCareSurveyStatus
	var none discovery.Result
	var in v1alpha1.TakeCareIntendant
	if err := w.Reader.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: s.Spec.IntendantRef.Name}, &in); err != nil {
		if apierrors.IsNotFound(err) {
			return out, none, fail(failIntendant, fmt.Errorf("the Intendant %s does not exist", s.Spec.IntendantRef.Name))
		}
		return out, none, fail(failIntendant, err)
	}
	p, err := platformOf(ctx, w.Client)
	switch {
	case err != nil:
		return out, none, fail(failGateway, err)
	case p == nil || p.Status.GatewayURL == "":
		return out, none, fail(failGateway, errors.New("the platform has no gateway URL"))
	}
	role := roleOf(&in)
	route := func(name string) string {
		if w.Route != nil {
			return w.Route(p.Status.GatewayURL, role, name)
		}
		return p.Status.GatewayURL + routes.MCPPath(role, name)
	}
	c := in.Spec.Connectors
	mapEP, err := w.endpoint(ctx, &in, c.Map, v1alpha1.ConnectorMap, mapTools, readTools, route)
	if err != nil || mapEP == nil {
		return out, none, fail(failMap, fmt.Errorf("the map connector cannot be used: %w", err))
	}
	mctx, mspan := w.tracer().Start(ctx, "read map")
	idx, err := readMap(mctx, w.Caller, mapEP.ep, w.now())
	mspan.SetAttributes(attribute.Int(semconv.SurveyEntries, len(idx.entries)))
	mspan.End()
	if err != nil {
		return out, none, fail(failMap, err)
	}
	grounds, blind, err := w.grounds(ctx, &in, idx, route)
	if err != nil {
		return out, none, fail(failImport, err)
	}
	var metrics *discovery.Metrics
	if m, _ := w.endpoint(ctx, &in, c.Metrics, v1alpha1.ConnectorMetrics, []string{mcp.RulesTool}, stateTools, route); m != nil {
		metrics = &discovery.Metrics{Connector: m.name, Endpoint: m.ep}
	}
	res, err := discovery.Import(ctx, w.Caller, grounds, metrics, discovery.NewIndex(idx.entries), w.now(), w.observer(ctx))
	if err != nil {
		return out, none, fail(failImport, err)
	}
	end := time.Now()
	for _, l := range res.Layers {
		_, ls := w.tracer().Start(ctx, "layer "+l.Name, trace.WithTimestamp(end), trace.WithAttributes(
			attribute.String(semconv.SurveyLayer, l.Name), attribute.Int(semconv.SurveySeen, int(l.Seen)),
			attribute.Int(semconv.SurveyMatched, int(l.Matched)), attribute.Int(semconv.SurveyProposed, int(l.Proposed)),
			attribute.Int(semconv.SurveyNotSeen, len(l.NotSeen))))
		ls.End(trace.WithTimestamp(end))
	}
	return outcome(res, idx.revision, blind), res, nil
}

// observer turns the import's calls into the survey's child spans, timed
// as they ran: each ground's listing and the rules' read.
func (w *Runner) observer(ctx context.Context) discovery.Observer {
	return discovery.Observer{
		Ground: func(g discovery.Ground, start, end time.Time) {
			_, sp := w.tracer().Start(ctx, "list "+g.Connector, trace.WithTimestamp(start), trace.WithAttributes(
				attribute.String(semconv.SurveyConnector, g.Connector), attribute.String(semconv.SurveyCluster, g.Cluster),
				attribute.Int(semconv.SurveyNamespaces, len(g.Namespaces))))
			sp.End(trace.WithTimestamp(end))
		},
		Rules: func(start, end time.Time) {
			_, sp := w.tracer().Start(ctx, "read rules", trace.WithTimestamp(start))
			sp.End(trace.WithTimestamp(end))
		},
	}
}

type bound struct {
	name string
	conn *v1alpha1.TakeCareConnector
	ep   mcp.Endpoint
}

// endpoint is the first of the named connectors the Intendant can use, as
// its route; nil when none can be.
func (w *Runner) endpoint(ctx context.Context, in *v1alpha1.TakeCareIntendant, names []string, typ v1alpha1.ConnectorType, want []string, ok toolFilter, route func(string) string) (*bound, error) {
	for _, name := range names {
		b, err := w.bind(ctx, in, name, typ, want, ok, route)
		if err != nil || b != nil {
			return b, err
		}
	}
	return nil, nil
}

func (w *Runner) bind(ctx context.Context, in *v1alpha1.TakeCareIntendant, name string, typ v1alpha1.ConnectorType, want []string, ok toolFilter, route func(string) string) (*bound, error) {
	_, problem, conn, err := usable(ctx, w.Client, in.Namespace, name, typ, want, ok)
	if err != nil || problem != "" {
		return nil, err
	}
	ep := mcp.Endpoint{URL: route(name), Types: conn.Spec.Types, Addon: conn.Spec.MCP.Addon != "", ReadTools: map[string]bool{}, StateTools: map[string]bool{}}
	for _, t := range conn.Status.Tools {
		ep.ReadTools[t.Name] = readTools(t)
		ep.StateTools[t.Name] = stateTools(t)
	}
	return &bound{name: name, conn: conn, ep: ep}, nil
}

// grounds are the usable cluster connectors, each with the namespaces of
// the scope it reads: the scope's namespaces and the namespaces of the
// kubernetes sources of the scope's entities, per cluster (ADR 0016 §5),
// as the coverage rule decides. What nothing reads is a blind spot.
func (w *Runner) grounds(ctx context.Context, in *v1alpha1.TakeCareIntendant, idx mapIndex, route func(string) string) ([]discovery.Ground, []v1alpha1.BlindSpot, error) {
	var declared []coverage.Declared
	var usableConns []*bound
	given := map[string]bool{}
	for _, name := range in.Spec.Connectors.Cluster {
		b, err := w.bind(ctx, in, name, v1alpha1.ConnectorCluster, mcp.DiscoveryTools, stateTools, route)
		if err != nil {
			return nil, nil, err
		}
		var conn v1alpha1.TakeCareConnector
		if b != nil {
			conn = *b.conn
			given[name] = true
			usableConns = append(usableConns, b)
		} else if err := w.Client.Get(ctx, client.ObjectKey{Namespace: in.Namespace, Name: name}, &conn); client.IgnoreNotFound(err) != nil {
			return nil, nil, fmt.Errorf("get connector %s: %w", name, err)
		}
		if conn.Spec.Kubernetes != nil {
			declared = append(declared, coverage.Declared{Connector: name, Cluster: connector.ClusterOf(&conn), Namespaces: conn.Spec.Kubernetes.Namespaces})
		}
	}
	entries := map[v1alpha1.EntityKey]coverage.Entry{}
	places := map[v1alpha1.EntityKey][]coverage.Place{}
	for _, e := range idx.entries {
		k := v1alpha1.EntityKey(e.Key)
		if !slices.Contains(in.Spec.Scope.Entities, k) {
			continue
		}
		entry := coverage.Entry{Found: true, Validated: e.Status == "validated"}
		for _, src := range e.Sources {
			if ref, ok := src["kubernetes"]; ok {
				if p, ok := parsePlace(ref); ok {
					entry.Places = append(entry.Places, p)
				}
			}
		}
		entries[k], places[k] = entry, entry.Places
	}
	res := coverage.Cover(in.Spec.Scope, declared, given, entries, coverage.MapProblem{})
	wanted := map[coverage.Place]bool{}
	for _, cl := range coverage.Clusters(declared) {
		for _, ns := range in.Spec.Scope.Namespaces {
			wanted[coverage.Place{Cluster: cl, Namespace: ns}] = true
		}
	}
	for _, ps := range places {
		for _, p := range ps {
			wanted[p] = true
		}
	}
	var grounds []discovery.Ground
	for _, b := range usableConns {
		cluster := connector.ClusterOf(b.conn)
		var nss []string
		for _, ns := range b.conn.Spec.Kubernetes.Namespaces {
			if wanted[coverage.Place{Cluster: cluster, Namespace: ns}] {
				nss = append(nss, string(ns))
			}
		}
		if len(nss) > 0 {
			grounds = append(grounds, discovery.Ground{Connector: b.name, Cluster: cluster, Endpoint: b.ep, Namespaces: nss})
		}
	}
	return grounds, res.BlindSpots, nil
}

// parsePlace reads the namespace of a kubernetes source.
func parsePlace(ref string) (coverage.Place, bool) {
	src, ok := mapschema.ParseKubernetes(ref)
	return coverage.Place{Cluster: src.Cluster, Namespace: v1alpha1.Namespace(src.Namespace)}, ok
}

// outcome turns the import's result into the survey's status, bounded.
func outcome(res discovery.Result, revision string, blind []v1alpha1.BlindSpot) v1alpha1.TakeCareSurveyStatus {
	out := v1alpha1.TakeCareSurveyStatus{MapRevision: revision, ProposalCounts: &v1alpha1.ProposalCounts{}}
	for _, l := range res.Layers {
		layer := v1alpha1.SurveyLayer{Name: l.Name, Seen: l.Seen, Matched: l.Matched, Proposed: l.Proposed}
		for i, n := range l.NotSeen {
			if i == maxNotSeen {
				break
			}
			layer.NotSeen = append(layer.NotSeen, v1alpha1.NotSeen{Ref: n.Ref, Reason: n.Reason})
		}
		out.Layers = append(out.Layers, layer)
	}
	for _, p := range res.Proposals {
		switch p.Action {
		case v1alpha1.ProposalAdd:
			out.ProposalCounts.Add++
		case v1alpha1.ProposalUpdate:
			out.ProposalCounts.Update++
		case v1alpha1.ProposalRemove:
			out.ProposalCounts.Remove++
		}
		out.ProposalCounts.Total++
		if len(out.Proposals) < maxProposals {
			out.Proposals = append(out.Proposals, v1alpha1.Proposal{
				ID: p.ID, Action: p.Action, Key: v1alpha1.EntityKey(p.Key), Entry: p.Entry, Evidence: p.Evidence, Source: p.Source,
			})
		}
	}
	if len(blind) > maxBlind {
		blind = blind[:maxBlind]
	}
	out.BlindSpots = blind
	return out
}

// patch changes a survey's status on a fresh read.
func (w *Runner) patch(ctx context.Context, s *v1alpha1.TakeCareSurvey, change func(*v1alpha1.TakeCareSurveyStatus)) error {
	var cur v1alpha1.TakeCareSurvey
	if err := w.Reader.Get(ctx, client.ObjectKeyFromObject(s), &cur); err != nil {
		return fmt.Errorf("read survey %s: %w", s.Name, err)
	}
	before := cur.DeepCopy()
	change(&cur.Status)
	cur.Status.ObservedGeneration = cur.Generation
	if err := w.Client.Status().Patch(ctx, &cur, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("update survey %s: %w", s.Name, err)
	}
	return nil
}
