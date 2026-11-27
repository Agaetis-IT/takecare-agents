// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package historyserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/alerts"
)

const (
	releaseNS = "takecare-system"
	otherNS   = "elsewhere"
	// diagnosisText and reviewText stand for the model's text: no list
	// may carry them.
	diagnosisText = "MODEL-TEXT the proxy idle timeout outlives the pool's recycling"
	reviewText    = "MODEL-TEXT add a check comparing the two timeouts"
	argLimit      = "limit"
	alertHigh     = "HighErrorRate"
	alertPool     = "PoolExhausted"
	invA          = "inv-a"
	invB          = "inv-b"
	invC          = "inv-c"
	invUndated    = "inv-undated"
	billingAPI    = "service/billing-api"
	caseLimit     = "a limit"
)

var day0 = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func at(days int) metav1.Time { return metav1.NewTime(day0.Add(time.Duration(days) * 24 * time.Hour)) }

func investigation(name string, days int, mutate ...func(*v1alpha1.TakeCareInvestigation)) *v1alpha1.TakeCareInvestigation {
	completed := at(days)
	inv := &v1alpha1.TakeCareInvestigation{
		Name: name, Namespace: releaseNS, Labels: map[string]string{LabelIncident: "inc-" + name},
		Spec: v1alpha1.TakeCareInvestigationSpec{
			RoleRef: v1alpha1.RoleRef{Kind: "TakeCareDoc", Name: "doc"},
			Level:   v1alpha1.LevelApprentice,
			Alert: v1alpha1.AlertRecord{
				Name: alertHigh, Namespace: "demo-apps", Cluster: "east", FiredAt: at(days),
				Entity: billingAPI, Labels: map[string]string{"severity": "critical"},
			},
		},
		Status: v1alpha1.TakeCareInvestigationStatus{
			RunStatus: v1alpha1.RunStatus{Phase: v1alpha1.RunCompleted, CompletedAt: &completed, Reason: "MODEL-TEXT reason"},
			Diagnosis: diagnosisText,
			Published: &v1alpha1.Publication{Connector: "report", OK: true},
			Alerts:    []v1alpha1.GroupedAlert{{Name: alertHigh, Fingerprint: "f1", FiredAt: at(days)}, {Name: alertPool, Fingerprint: "f2", FiredAt: at(days)}},
		},
	}
	for _, m := range mutate {
		m(inv)
	}
	return inv
}

func review(name string, days int, mutate ...func(*v1alpha1.TakeCareReview)) *v1alpha1.TakeCareReview {
	r := &v1alpha1.TakeCareReview{
		Name: name, Namespace: releaseNS, CreationTimestamp: at(days),
		Spec: v1alpha1.TakeCareReviewSpec{
			KineRef: v1alpha1.RoleRef{Kind: "TakeCareKine", Name: "kine"}, Trigger: v1alpha1.ReviewTriggerInvestigation,
			Investigations: []string{invA}, Incident: "inc-a",
		},
		Status: v1alpha1.TakeCareReviewStatus{
			RunStatus: v1alpha1.RunStatus{Phase: v1alpha1.RunCompleted},
			Problem:   &v1alpha1.Problem{Statement: reviewText, Recurrences: []v1alpha1.Recurrence{{Record: "INC-2024-0312"}}},
			Actions: []v1alpha1.CorrectiveAction{{
				ID: name + "-1", Text: reviewText, Kind: v1alpha1.ActionCheck, Origin: v1alpha1.ActionNew, State: v1alpha1.ActionProposed,
				Evidence: []v1alpha1.Evidence{{Tool: "incidents_get", Record: "INC-2024-0312"}},
			}},
			PostMortem: reviewText,
		},
	}
	for _, m := range mutate {
		m(r)
	}
	return r
}

// maintenanceText stands for the Mécano's model text: no list may carry
// it.
const maintenanceText = "MODEL-TEXT move the chart to the supported line"

func maintenance(name string, days int, mutate ...func(*v1alpha1.TakeCareMaintenance)) *v1alpha1.TakeCareMaintenance {
	m := &v1alpha1.TakeCareMaintenance{
		Name: name, Namespace: releaseNS, CreationTimestamp: at(days),
		Spec: v1alpha1.TakeCareMaintenanceSpec{
			MecanoRef: v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareMecano, Name: "mecano"}, Trigger: v1alpha1.MaintenanceTriggerSchedule,
			Level: v1alpha1.LevelPractitioner, Components: []string{billingAPI},
		},
		Status: v1alpha1.TakeCareMaintenanceStatus{
			RunStatus: v1alpha1.RunStatus{Phase: v1alpha1.RunCompleted},
			Inventory: &v1alpha1.MaintenanceInventory{Components: []v1alpha1.InventoryComponent{{Key: billingAPI, Live: "1.2.0", Declared: "1.2.0", Drift: v1alpha1.DriftNone}}},
			Findings: []v1alpha1.MaintenanceFinding{{
				ID: "Behind/service/billing-api", Kind: v1alpha1.FindingBehind, Component: billingAPI,
				Text: maintenanceText, Severity: v1alpha1.SeveritySoon, Evidence: []v1alpha1.Evidence{{Tool: "manifests_index", Record: "deploy/billing.yaml"}},
			}},
			Plan: &v1alpha1.MaintenancePlan{
				Steps:        []v1alpha1.PlanStep{{ID: "1", Text: maintenanceText, Findings: []string{"Behind/service/billing-api"}, Class: v1alpha1.ActionReversible}},
				Tested:       v1alpha1.PlanNotTested,
				PullRequests: []v1alpha1.PullRequestRef{{Repository: "team/deploy", URL: "https://forge.example/team/deploy/pull/7", Branch: "takecare/mecano/" + name}},
			},
			Counts:     &v1alpha1.MaintenanceCounts{Findings: 1, PullRequests: 1},
			BlindSpots: []v1alpha1.NotSeen{{Ref: "releases", Reason: "offline watch"}},
			Published:  &v1alpha1.Publication{Connector: "report", OK: true},
		},
	}
	for _, mutate := range mutate {
		mutate(m)
	}
	return m
}

func silentInvestigation(i *v1alpha1.TakeCareInvestigation) { i.Spec.Silent = true }

// start serves the objects through a fake client; synced drives /readyz.
func start(t *testing.T, objs ...client.Object) (*sdk.ClientSession, string, *atomic.Bool) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	synced := &atomic.Bool{}
	synced.Store(true)
	s, err := New(Config{Reader: reader, Namespace: releaseNS, Synced: synced.Load, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	session, err := c.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, srv.URL, synced
}

// call returns a tool's text and whether it is a tool error.
func call(t *testing.T, s *sdk.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res.Content[0].(*sdk.TextContent).Text, res.IsError
}

func span(since, until int, extra ...any) map[string]any {
	args := map[string]any{argSince: at(since).Format(time.RFC3339), argUntil: at(until).Format(time.RFC3339)}
	for i := 0; i+1 < len(extra); i += 2 {
		args[extra[i].(string)] = extra[i+1]
	}
	return args
}

func names(t *testing.T, text, key string) ([]string, int) {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%v: %s", err, text)
	}
	items, _ := out[key].([]any)
	got := make([]string, 0, len(items))
	for _, it := range items {
		got = append(got, it.(map[string]any)["name"].(string))
	}
	return got, int(out["omitted"].(float64))
}

// The tools: six, all read-only by their annotations, which an add-on's
// connector trusts (spec 05); the content ones are the three gets.
func TestToolsAreReadOnly(t *testing.T) {
	s, _, _ := start(t)
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
		if a := tool.Annotations; a == nil || !a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint {
			t.Errorf("%s is not annotated read-only: %+v", tool.Name, a)
		}
	}
	slices.Sort(got)
	if diff := cmp.Diff([]string{ToolInvestigationGet, ToolInvestigationsList, ToolMaintenanceGet, ToolMaintenancesList, ToolReviewGet, ToolReviewsList}, got); diff != "" {
		t.Errorf("tools (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{ToolInvestigationGet, ToolReviewGet, ToolMaintenanceGet}, ContentTools); diff != "" {
		t.Errorf("content tools (-want +got):\n%s", diff)
	}
}

func TestInvestigationsList(t *testing.T) {
	s, _, _ := start(t,
		investigation(invA, 2),
		investigation(invB, 5, func(i *v1alpha1.TakeCareInvestigation) {
			i.Spec.Alert.Name, i.Spec.Alert.Namespace, i.Spec.Alert.Entity = alertPool, "shop", "service/shop"
		}),
		investigation(invC, 8),
		investigation("inv-silent", 4, silentInvestigation),
		investigation("inv-early", -3),
		investigation("inv-other-ns", 3, func(i *v1alpha1.TakeCareInvestigation) { i.Namespace = otherNS }),
		// No firedAt: its creation dates it.
		investigation(invUndated, 0, func(i *v1alpha1.TakeCareInvestigation) {
			i.Spec.Alert.FiredAt = metav1.Time{}
			i.CreationTimestamp = at(6)
		}),
	)
	for _, tc := range []struct {
		name    string
		args    map[string]any
		want    []string
		omitted int
	}{
		{"the window, newest first, silent and other namespaces left out", span(0, 10), []string{invC, invUndated, invB, invA}, 0},
		{caseLimit, span(0, 10, argLimit, 2), []string{invC, invUndated}, 2},
		{"a limit past the bound is the bound", span(0, 10, argLimit, 1000), []string{invC, invUndated, invB, invA}, 0},
		{"by namespace", span(0, 10, "namespace", "shop"), []string{invB}, 0},
		{"by entity", span(0, 10, "entity", "service/shop"), []string{invB}, 0},
		{"by alert name", span(0, 10, "alertname", alertPool), []string{invB}, 0},
		{"by incident", span(0, 10, "incident", "inc-inv-a"), []string{invA}, 0},
		{"the bounds are inclusive", span(2, 5), []string{invB, invA}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, isErr := call(t, s, ToolInvestigationsList, tc.args)
			if isErr {
				t.Fatal(text)
			}
			got, omitted := names(t, text, "investigations")
			if diff := cmp.Diff(tc.want, got); diff != "" || omitted != tc.omitted {
				t.Errorf("omitted %d, want %d; names (-want +got):\n%s", omitted, tc.omitted, diff)
			}
		})
	}
}

// A list says state, never the diagnosis or any model text (spec 11 §4,
// acceptance criterion 4); the fields it does say are all there.
func TestInvestigationsListCarriesNoDiagnosis(t *testing.T) {
	s, _, _ := start(t, investigation(invA, 2))
	text, isErr := call(t, s, ToolInvestigationsList, span(0, 10))
	if isErr {
		t.Fatal(text)
	}
	if strings.Contains(text, "MODEL-TEXT") || strings.Contains(text, "diagnosis") || strings.Contains(text, "severity") {
		t.Errorf("the list carries text it must not: %s", text)
	}
	want := `{"investigations":[{"name":"inv-a","alertname":"HighErrorRate","namespace":"demo-apps","cluster":"east","entity":billingAPI,"incident":"inc-inv-a","phase":"Completed","firedAt":"2026-11-03T00:00:00Z","completedAt":"2026-11-03T00:00:00Z","published":true}],"omitted":0}`
	if text != want {
		t.Errorf("list\n got %s\nwant %s", text, want)
	}
}

func TestInvestigationGet(t *testing.T) {
	s, _, _ := start(t, investigation(invA, 2), investigation("inv-silent", 4, silentInvestigation),
		investigation("inv-other-ns", 3, func(i *v1alpha1.TakeCareInvestigation) { i.Namespace = otherNS }))
	text, isErr := call(t, s, ToolInvestigationGet, map[string]any{argName: invA})
	if isErr {
		t.Fatal(text)
	}
	var got Investigation
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got.Diagnosis != diagnosisText || !slices.Equal(got.Alerts, []string{alertHigh, alertPool}) || got.Incident != "inc-inv-a" {
		t.Errorf("investigation_get = %s", text)
	}
	// A silent record is served by name (an on-demand review may name it).
	if text, isErr := call(t, s, ToolInvestigationGet, map[string]any{argName: "inv-silent"}); isErr || !strings.Contains(text, diagnosisText) {
		t.Errorf("silent investigation by name: %v %s", isErr, text)
	}
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"unknown", map[string]any{argName: "inv-none"}, "no investigation named inv-none"},
		{"another namespace", map[string]any{argName: "inv-other-ns"}, "no investigation named inv-other-ns"},
		{"no name", map[string]any{}, "name: a record's name is required"},
		{"a name too long", map[string]any{argName: strings.Repeat("a", 254)}, "at most 253 characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, isErr := call(t, s, ToolInvestigationGet, tc.args)
			if !isErr || !strings.Contains(text, tc.want) {
				t.Errorf("got %v %q, want a tool error saying %q", isErr, text, tc.want)
			}
		})
	}
}

func TestReviewsList(t *testing.T) {
	s, _, _ := start(t,
		review("review-a", 2),
		review("review-b", 5, func(r *v1alpha1.TakeCareReview) { r.Spec.KineRef.Name = "kine-shop" }),
		review("review-silent", 4, func(r *v1alpha1.TakeCareReview) { r.Spec.Silent = true }),
		review("review-late", 12),
	)
	for _, tc := range []struct {
		name    string
		args    map[string]any
		want    []string
		omitted int
	}{
		{"the window, newest first, silent left out", span(0, 10), []string{"review-b", "review-a"}, 0},
		{"by Kiné", span(0, 10, "kine", "kine-shop"), []string{"review-b"}, 0},
		{caseLimit, span(0, 20, argLimit, 1), []string{"review-late"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, isErr := call(t, s, ToolReviewsList, tc.args)
			if isErr {
				t.Fatal(text)
			}
			got, omitted := names(t, text, "reviews")
			if diff := cmp.Diff(tc.want, got); diff != "" || omitted != tc.omitted {
				t.Errorf("omitted %d, want %d; names (-want +got):\n%s", omitted, tc.omitted, diff)
			}
		})
	}
	text, _ := call(t, s, ToolReviewsList, span(1, 3))
	want := `{"omitted":0,"reviews":[{"name":"review-a","kine":"kine","trigger":"Investigation","incident":"inc-a","phase":"Completed","created":"2026-11-03T00:00:00Z","actions":[{"id":"review-a-1","state":"Proposed"}]}]}`
	if text != want {
		t.Errorf("list\n got %s\nwant %s", text, want)
	}
}

func TestReviewGet(t *testing.T) {
	s, _, _ := start(t, review("review-a", 2), review("review-silent", 4, func(r *v1alpha1.TakeCareReview) { r.Spec.Silent = true }))
	text, isErr := call(t, s, ToolReviewGet, map[string]any{argName: "review-a"})
	if isErr {
		t.Fatal(text)
	}
	var got Review
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got.Problem == nil || got.Problem.Statement != reviewText || got.PostMortem != reviewText ||
		len(got.Actions) != 1 || got.Actions[0].Text != reviewText || !slices.Equal(got.Investigations, []string{invA}) {
		t.Errorf("review_get = %s", text)
	}
	if text, isErr := call(t, s, ToolReviewGet, map[string]any{argName: "review-silent"}); isErr || !strings.Contains(text, reviewText) {
		t.Errorf("silent review by name: %v %s", isErr, text)
	}
	if text, isErr := call(t, s, ToolReviewGet, map[string]any{argName: "review-none"}); !isErr || text != "no review named review-none" {
		t.Errorf("unknown review: %v %s", isErr, text)
	}
}

func TestMaintenancesList(t *testing.T) {
	s, _, _ := start(t,
		maintenance("maintenance-a", 2),
		maintenance("maintenance-b", 5, func(m *v1alpha1.TakeCareMaintenance) { m.Spec.MecanoRef.Name = "mecano-shop" }),
		maintenance("maintenance-silent", 4, func(m *v1alpha1.TakeCareMaintenance) { m.Spec.Silent = true }),
		maintenance("maintenance-late", 12),
		maintenance("maintenance-other-ns", 3, func(m *v1alpha1.TakeCareMaintenance) { m.Namespace = otherNS }),
	)
	for _, tc := range []struct {
		name    string
		args    map[string]any
		want    []string
		omitted int
	}{
		{"the window, newest first, silent and other namespaces left out", span(0, 10), []string{"maintenance-b", "maintenance-a"}, 0},
		{"by Mécano", span(0, 10, "mecano", "mecano-shop"), []string{"maintenance-b"}, 0},
		{caseLimit, span(0, 20, argLimit, 1), []string{"maintenance-late"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, isErr := call(t, s, ToolMaintenancesList, tc.args)
			if isErr {
				t.Fatal(text)
			}
			got, omitted := names(t, text, "maintenances")
			if diff := cmp.Diff(tc.want, got); diff != "" || omitted != tc.omitted {
				t.Errorf("omitted %d, want %d; names (-want +got):\n%s", omitted, tc.omitted, diff)
			}
		})
	}
	// State only: the findings' ids and severities, never their text, the
	// plan or the inventory (spec 12 §3, spec 11 §4).
	text, _ := call(t, s, ToolMaintenancesList, span(1, 3))
	want := `{"maintenances":[{"name":"maintenance-a","mecano":"mecano","trigger":"Schedule","level":"Practitioner","phase":"Completed","created":"2026-11-03T00:00:00Z","findings":[{"id":"Behind/service/billing-api","severity":"Soon"}],"counts":{"findings":1,"now":0,"pullRequests":1},"published":true}],"omitted":0}`
	if text != want {
		t.Errorf("list\n got %s\nwant %s", text, want)
	}
	if strings.Contains(text, "MODEL-TEXT") {
		t.Errorf("the list carries model text: %s", text)
	}
}

func TestMaintenanceGet(t *testing.T) {
	s, _, _ := start(t, maintenance("maintenance-a", 2), maintenance("maintenance-silent", 4, func(m *v1alpha1.TakeCareMaintenance) { m.Spec.Silent = true }))
	text, isErr := call(t, s, ToolMaintenanceGet, map[string]any{argName: "maintenance-a"})
	if isErr {
		t.Fatal(text)
	}
	var got Maintenance
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 1 || got.Findings[0].Text != maintenanceText || got.Plan == nil || len(got.Plan.Steps) != 1 ||
		len(got.Plan.PullRequests) != 1 || got.Inventory == nil || len(got.BlindSpots) != 1 || !slices.Equal(got.Components, []string{billingAPI}) {
		t.Errorf("maintenance_get = %s", text)
	}
	if text, isErr := call(t, s, ToolMaintenanceGet, map[string]any{argName: "maintenance-silent"}); isErr || !strings.Contains(text, maintenanceText) {
		t.Errorf("silent maintenance by name: %v %s", isErr, text)
	}
	if text, isErr := call(t, s, ToolMaintenanceGet, map[string]any{argName: "maintenance-none"}); !isErr || text != "no maintenance named maintenance-none" {
		t.Errorf("unknown maintenance: %v %s", isErr, text)
	}
}

func TestWindowArguments(t *testing.T) {
	s, _, _ := start(t)
	for _, tool := range []string{ToolInvestigationsList, ToolReviewsList, ToolMaintenancesList} {
		for _, tc := range []struct {
			name string
			args map[string]any
			want string
		}{
			{"no since", map[string]any{argUntil: at(1).Format(time.RFC3339)}, "since: an RFC 3339 time is required"},
			{"a date only", map[string]any{argSince: "2026-11-01", argUntil: at(1).Format(time.RFC3339)}, "since: an RFC 3339 time is required"},
			{"no until", map[string]any{argSince: at(1).Format(time.RFC3339)}, "until: an RFC 3339 time is required"},
			{"an empty window", span(3, 3), "since must be before until"},
			{"a reversed window", span(3, 1), "since must be before until"},
		} {
			t.Run(fmt.Sprintf("%s %s", tool, tc.name), func(t *testing.T) {
				text, isErr := call(t, s, tool, tc.args)
				if !isErr || !strings.Contains(text, tc.want) {
					t.Errorf("got %v %q, want a tool error saying %q", isErr, text, tc.want)
				}
			})
		}
	}
}

// Ready only once the cache holds the records.
func TestReadiness(t *testing.T) {
	_, url, synced := start(t)
	for _, tc := range []struct {
		synced bool
		want   int
	}{{false, http.StatusServiceUnavailable}, {true, http.StatusOK}} {
		synced.Store(tc.synced)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url+"/readyz", nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != tc.want {
			t.Errorf("synced %v: /readyz %d, want %d", tc.synced, res.StatusCode, tc.want)
		}
	}
}

// TestOriginChecked: a browser's cross-origin request is refused, a caller
// without an Origin accepted.
func TestOriginChecked(t *testing.T) {
	_, url, _ := start(t)
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"no Origin", nil, http.StatusOK},
		{"foreign Origin", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"cross-site fetch", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url+"/mcp", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if res.StatusCode != tc.want {
				t.Errorf("status %d, want %d", res.StatusCode, tc.want)
			}
		})
	}
}

// The server's copy of the incident label is the intake's.
func TestIncidentLabelIsTheIntakes(t *testing.T) {
	if LabelIncident != alerts.LabelIncident {
		t.Errorf("LabelIncident = %q, the intake sets %q", LabelIncident, alerts.LabelIncident)
	}
}

func TestNewRequiresAReaderAndANamespace(t *testing.T) {
	if _, err := New(Config{Namespace: releaseNS}); err == nil {
		t.Error("New without a reader succeeded")
	}
	if _, err := New(Config{Reader: fake.NewClientBuilder().Build()}); err == nil {
		t.Error("New without a namespace succeeded")
	}
}
