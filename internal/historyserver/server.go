// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package historyserver serves TakeCare's own records, the investigations,
// the reviews and the maintenances of the release namespace, as a
// read-only MCP server: the history the Kiné and the Mécano read (spec 11
// §4, spec 12 §3). Six tools, all read and none taking a free text: three
// lists that return state only, never the diagnosis nor any model text,
// and three gets by name that return it, the content tools (ADR 0014 §5)
// a role reads only when its contentTools list them.
//
// The records come from an informer cache scoped to the release namespace,
// filled under the add-on's own ServiceAccount, which may get, list and
// watch the three kinds there and nothing else (spec 11 §10, spec 12 §13). The lists
// leave silent records out, the benches' runs; a get by name still serves
// them, as an on-demand review may name one (spec 11 §3).
package historyserver

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// LabelIncident carries the incident key on an investigation; the alert
// intake sets it (internal/alerts, a test keeps both equal). Not imported:
// the intake would bring the operator's dependencies into the server.
const LabelIncident = "takecare.agaetis.fr/incident"

// Tool names, as the connector's contentTools and a role's allow-list name
// them.
const (
	ToolInvestigationsList = "investigations_list"
	ToolInvestigationGet   = "investigation_get"
	ToolReviewsList        = "reviews_list"
	ToolReviewGet          = "review_get"
	ToolMaintenancesList   = "maintenances_list"
	ToolMaintenanceGet     = "maintenance_get"
)

// ContentTools are the tools that return model text (spec 11 §4, spec 12
// §3).
var ContentTools = []string{ToolInvestigationGet, ToolReviewGet, ToolMaintenanceGet}

// Bounds of the lists.
const (
	limitDefault, limitMax = 20, 100
	// maxName bounds a name argument: an object's name.
	maxName                     = 253
	argSince, argUntil, argName = "since", "until", "name"
	// keyOmitted counts what a list left out past its limit.
	keyOmitted = "omitted"
)

// Config is where the records are read.
type Config struct {
	// Reader reads the records: the cache, scoped to Namespace.
	Reader client.Reader
	// Namespace is the release namespace, the only one served.
	Namespace string
	// Synced says whether the reader holds the records yet (/readyz).
	Synced func() bool
	Logger *slog.Logger
}

// Server serves the history.
type Server struct {
	cfg Config
	mcp *sdk.Server
}

// New builds the server.
func New(cfg Config) (*Server, error) {
	if cfg.Reader == nil || cfg.Namespace == "" {
		return nil, errors.New("a reader and the release namespace are required")
	}
	if cfg.Synced == nil {
		cfg.Synced = func() bool { return true }
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{cfg: cfg}
	s.mcp = sdk.NewServer(&sdk.Implementation{Name: "history", Version: "1"}, nil)
	no := false
	ro := &sdk.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no}
	window := map[string]any{
		argSince: prop("string", "Start of the window, an RFC 3339 time."),
		argUntil: prop("string", "End of the window, an RFC 3339 time."),
		"limit":  prop("integer", fmt.Sprintf("How many records, newest first: %d by default, %d at most.", limitDefault, limitMax)),
	}
	name := map[string]any{argName: prop("string", "The record's name.")}
	for _, t := range []struct {
		name, description string
		props             map[string]any
		required          []string
		handler           func(context.Context, json.RawMessage) (any, error)
	}{
		{ToolInvestigationsList, "TakeCare's past investigations whose alert fired in the window, newest first: name, alert name, namespace, cluster, entity, incident key, phase, firedAt, completedAt, resolvedAt, published. Never the diagnosis: read it with investigation_get.",
			with(window, map[string]any{
				"namespace": prop("string", "Only the alerts of this namespace."),
				"entity":    prop("string", "Only the alerts resolved to this Map entry (type/name)."),
				"alertname": prop("string", "Only this alert."),
				"incident":  prop("string", "Only this incident key."),
			}), []string{argSince, argUntil}, s.investigationsList},
		{ToolInvestigationGet, "One investigation by name: what investigations_list returns, plus the Doc's diagnosis and the names of the alerts grouped into it.",
			name, []string{argName}, s.investigationGet},
		{ToolReviewsList, "The Kiné's past reviews created in the window, newest first: name, Kiné, trigger, incident key, phase, and each action's id and state. Never the review's text: read it with review_get.",
			with(window, map[string]any{"kine": prop("string", "Only the reviews of this Kiné.")}), []string{argSince, argUntil}, s.reviewsList},
		{ToolReviewGet, "One review by name: what reviews_list returns, plus the investigations it reviewed, its problem statement and recurrences, its actions with their text and evidence, and its post-mortem draft.",
			name, []string{argName}, s.reviewGet},
		{ToolMaintenancesList, "The Mécano's past maintenances created in the window, newest first: name, Mécano, trigger, phase, the ids and severities of its findings, its counts, and whether it was published. Never the findings' text nor the plan: read them with maintenance_get.",
			with(window, map[string]any{"mecano": prop("string", "Only the maintenances of this Mécano.")}), []string{argSince, argUntil}, s.maintenancesList},
		{ToolMaintenanceGet, "One maintenance by name: what maintenances_list returns, plus the components it narrowed to, its inventory, its findings with their text and evidence, its plan with its pull requests, and its blind spots.",
			name, []string{argName}, s.maintenanceGet},
	} {
		s.mcp.AddTool(&sdk.Tool{Name: t.name, Description: t.description, InputSchema: schema(t.props, t.required...), Annotations: ro}, s.serve(t.handler))
	}
	return s, nil
}

func prop(typ, description string) map[string]any {
	return map[string]any{"type": typ, "description": description}
}

func with(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	maps.Copy(out, a)
	maps.Copy(out, b)
	return out
}

func schema(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

// serve runs a tool: its errors are tool errors the model reads, never
// transport errors.
func (s *Server) serve(h func(context.Context, json.RawMessage) (any, error)) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		out, err := h(ctx, req.Params.Arguments)
		if err != nil {
			return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: err.Error()}}}, nil
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return nil, fmt.Errorf("encode the result: %w", err)
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(raw)}}}, nil
	}
}

// Handler serves /mcp, /healthz, and /readyz, ready once the cache holds
// the records.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// A browser's cross-origin request is refused (403), as the MCP
	// specification requires of the Origin header; our callers are not
	// browsers and send none.
	mux.Handle("/mcp", http.NewCrossOriginProtection().Handler(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.mcp }, &sdk.StreamableHTTPOptions{Stateless: true})))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.cfg.Synced() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "the records are not read yet\n")
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// windowArgs are the arguments every list takes.
type windowArgs struct {
	Since string `json:"since"`
	Until string `json:"until"`
	Limit int    `json:"limit"`
}

type window struct {
	since, until time.Time
	limit        int
}

func (a windowArgs) parse() (window, error) {
	var w window
	var err error
	if w.since, err = time.Parse(time.RFC3339, a.Since); err != nil {
		return w, fmt.Errorf("since: an RFC 3339 time is required: %w", err)
	}
	if w.until, err = time.Parse(time.RFC3339, a.Until); err != nil {
		return w, fmt.Errorf("until: an RFC 3339 time is required: %w", err)
	}
	if !w.since.Before(w.until) {
		return w, errors.New("since must be before until")
	}
	w.limit = a.Limit
	if w.limit <= 0 {
		w.limit = limitDefault
	}
	w.limit = min(w.limit, limitMax)
	return w, nil
}

func (w window) holds(t time.Time) bool { return !t.Before(w.since) && !t.After(w.until) }

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("arguments: %w", err)
	}
	return nil
}

func nameArg(raw json.RawMessage) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := decode(raw, &args); err != nil {
		return "", err
	}
	if args.Name == "" || len(args.Name) > maxName {
		return "", fmt.Errorf("name: a record's name is required, at most %d characters", maxName)
	}
	return args.Name, nil
}

// InvestigationSummary is what a list says of an investigation: state,
// never the diagnosis.
type InvestigationSummary struct {
	Name        string       `json:"name"`
	Alertname   string       `json:"alertname"`
	Namespace   string       `json:"namespace,omitempty"`
	Cluster     string       `json:"cluster,omitempty"`
	Entity      string       `json:"entity,omitempty"`
	Incident    string       `json:"incident,omitempty"`
	Phase       string       `json:"phase,omitempty"`
	FiredAt     *metav1.Time `json:"firedAt,omitempty"`
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	ResolvedAt  *metav1.Time `json:"resolvedAt,omitempty"`
	Published   bool         `json:"published"`
}

// Investigation is investigation_get's answer.
type Investigation struct {
	InvestigationSummary
	Diagnosis string   `json:"diagnosis,omitempty"`
	Alerts    []string `json:"alerts,omitempty"`
}

// firedAt is when an investigation's alert fired, its creation when the
// alert carries no time.
func firedAt(inv *v1alpha1.TakeCareInvestigation) time.Time {
	if !inv.Spec.Alert.FiredAt.IsZero() {
		return inv.Spec.Alert.FiredAt.Time
	}
	return inv.CreationTimestamp.Time
}

func summarizeInvestigation(inv *v1alpha1.TakeCareInvestigation) InvestigationSummary {
	out := InvestigationSummary{
		Name:        inv.Name,
		Alertname:   inv.Spec.Alert.Name,
		Namespace:   inv.Spec.Alert.Namespace,
		Cluster:     inv.Spec.Alert.Cluster,
		Entity:      string(inv.Spec.Alert.Entity),
		Incident:    inv.Labels[LabelIncident],
		Phase:       string(inv.Status.Phase),
		CompletedAt: inv.Status.CompletedAt,
		ResolvedAt:  inv.Status.ResolvedAt,
		Published:   inv.Status.Published != nil && inv.Status.Published.OK,
	}
	if t := firedAt(inv); !t.IsZero() {
		out.FiredAt = &metav1.Time{Time: t}
	}
	return out
}

// investigationsList is the investigations whose alert fired in the
// window, silent ones left out, newest first.
func (s *Server) investigationsList(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		windowArgs
		Namespace string `json:"namespace"`
		Entity    string `json:"entity"`
		Alertname string `json:"alertname"`
		Incident  string `json:"incident"`
	}
	if err := decode(raw, &args); err != nil {
		return nil, err
	}
	w, err := args.parse()
	if err != nil {
		return nil, err
	}
	var list v1alpha1.TakeCareInvestigationList
	if err := s.cfg.Reader.List(ctx, &list, client.InNamespace(s.cfg.Namespace)); err != nil {
		return nil, fmt.Errorf("list the investigations: %w", err)
	}
	var kept []*v1alpha1.TakeCareInvestigation
	for i := range list.Items {
		inv := &list.Items[i]
		a := inv.Spec.Alert
		switch {
		case inv.Spec.Silent, !w.holds(firedAt(inv)),
			args.Namespace != "" && a.Namespace != args.Namespace,
			args.Entity != "" && string(a.Entity) != args.Entity,
			args.Alertname != "" && a.Name != args.Alertname,
			args.Incident != "" && inv.Labels[LabelIncident] != args.Incident:
			continue
		}
		kept = append(kept, inv)
	}
	slices.SortFunc(kept, func(a, b *v1alpha1.TakeCareInvestigation) int {
		return cmp.Or(firedAt(b).Compare(firedAt(a)), cmp.Compare(a.Name, b.Name))
	})
	out := make([]InvestigationSummary, 0, min(len(kept), w.limit))
	for _, inv := range kept[:min(len(kept), w.limit)] {
		out = append(out, summarizeInvestigation(inv))
	}
	return map[string]any{"investigations": out, keyOmitted: len(kept) - len(out)}, nil
}

// investigationGet is one investigation with its diagnosis, silent or not.
func (s *Server) investigationGet(ctx context.Context, raw json.RawMessage) (any, error) {
	name, err := nameArg(raw)
	if err != nil {
		return nil, err
	}
	var inv v1alpha1.TakeCareInvestigation
	if err := s.cfg.Reader.Get(ctx, client.ObjectKey{Namespace: s.cfg.Namespace, Name: name}, &inv); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("no investigation named %s", name)
		}
		return nil, fmt.Errorf("get the investigation %s: %w", name, err)
	}
	out := Investigation{InvestigationSummary: summarizeInvestigation(&inv), Diagnosis: inv.Status.Diagnosis}
	for _, a := range inv.Status.Alerts {
		out.Alerts = append(out.Alerts, a.Name)
	}
	return out, nil
}

// ActionState is what a list says of an action: its id and state, never
// its text.
type ActionState struct {
	ID    string               `json:"id"`
	State v1alpha1.ActionState `json:"state"`
}

// ReviewSummary is what a list says of a review.
type ReviewSummary struct {
	Name     string                 `json:"name"`
	Kine     string                 `json:"kine"`
	Trigger  v1alpha1.ReviewTrigger `json:"trigger,omitempty"`
	Incident string                 `json:"incident,omitempty"`
	Phase    string                 `json:"phase,omitempty"`
	Created  metav1.Time            `json:"created"`
	Actions  []ActionState          `json:"actions,omitempty"`
}

// Review is review_get's answer.
type Review struct {
	ReviewSummary
	Investigations []string                    `json:"investigations,omitempty"`
	Period         *v1alpha1.ReviewPeriod      `json:"period,omitempty"`
	Problem        *v1alpha1.Problem           `json:"problem,omitempty"`
	Actions        []v1alpha1.CorrectiveAction `json:"actions,omitempty"`
	ActionsOmitted int32                       `json:"actionsOmitted,omitempty"`
	PostMortem     string                      `json:"postMortem,omitempty"`
}

func summarizeReview(r *v1alpha1.TakeCareReview) ReviewSummary {
	out := ReviewSummary{
		Name:     r.Name,
		Kine:     r.Spec.KineRef.Name,
		Trigger:  r.Spec.Trigger,
		Incident: r.Spec.Incident,
		Phase:    string(r.Status.Phase),
		Created:  r.CreationTimestamp,
	}
	for _, a := range r.Status.Actions {
		out.Actions = append(out.Actions, ActionState{ID: a.ID, State: a.State})
	}
	return out
}

// reviewsList is the reviews created in the window, silent ones left out,
// newest first.
func (s *Server) reviewsList(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		windowArgs
		Kine string `json:"kine"`
	}
	if err := decode(raw, &args); err != nil {
		return nil, err
	}
	w, err := args.parse()
	if err != nil {
		return nil, err
	}
	var list v1alpha1.TakeCareReviewList
	if err := s.cfg.Reader.List(ctx, &list, client.InNamespace(s.cfg.Namespace)); err != nil {
		return nil, fmt.Errorf("list the reviews: %w", err)
	}
	var kept []*v1alpha1.TakeCareReview
	for i := range list.Items {
		r := &list.Items[i]
		if r.Spec.Silent || !w.holds(r.CreationTimestamp.Time) || (args.Kine != "" && r.Spec.KineRef.Name != args.Kine) {
			continue
		}
		kept = append(kept, r)
	}
	slices.SortFunc(kept, func(a, b *v1alpha1.TakeCareReview) int {
		return cmp.Or(b.CreationTimestamp.Compare(a.CreationTimestamp.Time), cmp.Compare(a.Name, b.Name))
	})
	out := make([]ReviewSummary, 0, min(len(kept), w.limit))
	for _, r := range kept[:min(len(kept), w.limit)] {
		out = append(out, summarizeReview(r))
	}
	return map[string]any{"reviews": out, keyOmitted: len(kept) - len(out)}, nil
}

// reviewGet is one review with its text, silent or not.
func (s *Server) reviewGet(ctx context.Context, raw json.RawMessage) (any, error) {
	name, err := nameArg(raw)
	if err != nil {
		return nil, err
	}
	var r v1alpha1.TakeCareReview
	if err := s.cfg.Reader.Get(ctx, client.ObjectKey{Namespace: s.cfg.Namespace, Name: name}, &r); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("no review named %s", name)
		}
		return nil, fmt.Errorf("get the review %s: %w", name, err)
	}
	return Review{
		ReviewSummary:  summarizeReview(&r),
		Investigations: r.Spec.Investigations,
		Period:         r.Spec.Period,
		Problem:        r.Status.Problem,
		Actions:        r.Status.Actions,
		ActionsOmitted: r.Status.ActionsOmitted,
		PostMortem:     r.Status.PostMortem,
	}, nil
}

// FindingState is what a list says of a finding: its id and severity,
// never its text.
type FindingState struct {
	ID       string            `json:"id"`
	Severity v1alpha1.Severity `json:"severity"`
}

// MaintenanceSummary is what a list says of a maintenance.
type MaintenanceSummary struct {
	Name      string                      `json:"name"`
	Mecano    string                      `json:"mecano"`
	Trigger   v1alpha1.MaintenanceTrigger `json:"trigger,omitempty"`
	Level     v1alpha1.Level              `json:"level,omitempty"`
	Phase     string                      `json:"phase,omitempty"`
	Created   metav1.Time                 `json:"created"`
	Findings  []FindingState              `json:"findings,omitempty"`
	Counts    *v1alpha1.MaintenanceCounts `json:"counts,omitempty"`
	Published bool                        `json:"published"`
}

// Maintenance is maintenance_get's answer.
type Maintenance struct {
	MaintenanceSummary
	Components      []string                       `json:"components,omitempty"`
	Inventory       *v1alpha1.MaintenanceInventory `json:"inventory,omitempty"`
	Findings        []v1alpha1.MaintenanceFinding  `json:"findings,omitempty"`
	FindingsOmitted int32                          `json:"findingsOmitted,omitempty"`
	Plan            *v1alpha1.MaintenancePlan      `json:"plan,omitempty"`
	BlindSpots      []v1alpha1.NotSeen             `json:"blindSpots,omitempty"`
}

func summarizeMaintenance(m *v1alpha1.TakeCareMaintenance) MaintenanceSummary {
	out := MaintenanceSummary{
		Name:      m.Name,
		Mecano:    m.Spec.MecanoRef.Name,
		Trigger:   m.Spec.Trigger,
		Level:     m.Spec.Level,
		Phase:     string(m.Status.Phase),
		Created:   m.CreationTimestamp,
		Counts:    m.Status.Counts,
		Published: m.Status.Published != nil && m.Status.Published.OK,
	}
	for _, f := range m.Status.Findings {
		out.Findings = append(out.Findings, FindingState{ID: f.ID, Severity: f.Severity})
	}
	return out
}

// maintenancesList is the maintenances created in the window, silent ones
// left out, newest first.
func (s *Server) maintenancesList(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		windowArgs
		Mecano string `json:"mecano"`
	}
	if err := decode(raw, &args); err != nil {
		return nil, err
	}
	w, err := args.parse()
	if err != nil {
		return nil, err
	}
	var list v1alpha1.TakeCareMaintenanceList
	if err := s.cfg.Reader.List(ctx, &list, client.InNamespace(s.cfg.Namespace)); err != nil {
		return nil, fmt.Errorf("list the maintenances: %w", err)
	}
	var kept []*v1alpha1.TakeCareMaintenance
	for i := range list.Items {
		m := &list.Items[i]
		if m.Spec.Silent || !w.holds(m.CreationTimestamp.Time) || (args.Mecano != "" && m.Spec.MecanoRef.Name != args.Mecano) {
			continue
		}
		kept = append(kept, m)
	}
	slices.SortFunc(kept, func(a, b *v1alpha1.TakeCareMaintenance) int {
		return cmp.Or(b.CreationTimestamp.Compare(a.CreationTimestamp.Time), cmp.Compare(a.Name, b.Name))
	})
	out := make([]MaintenanceSummary, 0, min(len(kept), w.limit))
	for _, m := range kept[:min(len(kept), w.limit)] {
		out = append(out, summarizeMaintenance(m))
	}
	return map[string]any{"maintenances": out, keyOmitted: len(kept) - len(out)}, nil
}

// maintenanceGet is one maintenance with its findings and plan, silent or
// not.
func (s *Server) maintenanceGet(ctx context.Context, raw json.RawMessage) (any, error) {
	name, err := nameArg(raw)
	if err != nil {
		return nil, err
	}
	var m v1alpha1.TakeCareMaintenance
	if err := s.cfg.Reader.Get(ctx, client.ObjectKey{Namespace: s.cfg.Namespace, Name: name}, &m); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("no maintenance named %s", name)
		}
		return nil, fmt.Errorf("get the maintenance %s: %w", name, err)
	}
	return Maintenance{
		MaintenanceSummary: summarizeMaintenance(&m),
		Components:         m.Spec.Components,
		Inventory:          m.Status.Inventory,
		Findings:           m.Status.Findings,
		FindingsOmitted:    m.Status.FindingsOmitted,
		Plan:               m.Status.Plan,
		BlindSpots:         m.Status.BlindSpots,
	}, nil
}
