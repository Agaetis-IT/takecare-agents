// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/mcp/mcptest"
)

const (
	demoApps       = "demo-apps"
	kmsFixtures    = "../../discovery/testdata/kms-v0.0.67"
	demoMap        = "../../mapserver/testdata/demo"
	toolNamespaces = "namespaces_list"
	toolResources  = "resources_list"
	clusterTools   = toolNamespaces + "," + toolResources
)

// gate holds a server's calls until it is released, at the latest when the
// test ends, before its server closes: a failing test never hangs on it.
type gate struct {
	ch   chan struct{}
	once sync.Once
}

func newGate() *gate { return &gate{ch: make(chan struct{})} }

func (g *gate) release() { g.once.Do(func() { close(g.ch) }) }

// mapServer serves the demo Map, as the map add-on would behind the route;
// hold, when set, makes every call wait until it is released.
func mapServer(t *testing.T, hold *gate) string { return mapServerAt(t, demoMap, hold) }

// mapServerAt serves the Map of dir.
func mapServerAt(t *testing.T, dir string, hold *gate) string {
	t.Helper()
	s, err := mapserver.New(mapserver.Config{Dir: dir, Logger: slog.New(slog.DiscardHandler), Now: func() time.Time { return testStart }})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hold != nil {
			<-hold.ch
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	if hold != nil {
		t.Cleanup(hold.release) // runs before srv.Close
	}
	return srv.URL + "/mcp"
}

// clusterServer serves the kubernetes add-on's captured answers: the
// demo-apps namespace's objects, an empty list elsewhere.
func clusterServer(t *testing.T) string {
	t.Helper()
	answer := func(name string) (string, any, bool) {
		raw, err := os.ReadFile(filepath.Join(kmsFixtures, name)) //nolint:gosec // a test's own testdata
		if err != nil {
			t.Fatal(err)
		}
		var a struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			StructuredContent any  `json:"structuredContent"`
			IsError           bool `json:"isError"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			t.Fatal(err)
		}
		return a.Content[0].Text, a.StructuredContent, a.IsError
	}
	srv := mcptest.Start(t,
		mcptest.ToolSpec{Name: toolNamespaces, ReadOnly: true, Handle: func(map[string]any) (string, any, bool) { return answer("namespaces_list.json") }},
		mcptest.ToolSpec{Name: toolResources, ReadOnly: true, Handle: func(args map[string]any) (string, any, bool) {
			name := args["namespace"].(string) + "-" + args["kind"].(string) + ".json"
			if _, err := os.Stat(filepath.Join(kmsFixtures, name)); err == nil {
				return answer(name)
			}
			return answer("empty.json")
		}},
	)
	return srv.URL
}

// surveyed sets up an Intendant over the demo Map and the demo-apps
// ground, its map route served by mapURL.
func (f *fixture) surveyed(t *testing.T, name, mapURL string, mutate func(*v1alpha1.TakeCareIntendant)) {
	t.Helper()
	f.routes.Store(name+"/"+connMap, mapURL)
	f.routes.Store(name+"/"+connCluster, clusterServer(t))
	f.intendant(t, name, func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.Scope = v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{demoApps}}
		in.Spec.Connectors = v1alpha1.IntendantConnectors{Map: []string{connMap}, Cluster: []string{connCluster}}
		if mutate != nil {
			mutate(in)
		}
	})
}

// grounded creates the platform and the map and cluster connectors once.
func (f *fixture) grounded(t *testing.T) {
	t.Helper()
	f.platform(t)
	f.connector(t, connMap, v1alpha1.ConnectorMap, []string{mcp.MapListTool})
	f.mapRevision(t, rev1)
	f.connector(t, connCluster, v1alpha1.ConnectorCluster, strings.Split(clusterTools, ","))
	var c v1alpha1.TakeCareConnector
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: connCluster}, &c); err != nil {
		t.Fatal(err)
	}
	c.Spec.Kubernetes.Namespaces = []v1alpha1.Namespace{demoApps}
	if err := f.c.Update(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
}

// waitPhase waits until every survey of the Intendant is in phase, n of
// them.
func (f *fixture) waitPhase(t *testing.T, name string, n int, phase v1alpha1.RunPhase) []v1alpha1.TakeCareSurvey {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	var got []v1alpha1.TakeCareSurvey
	for time.Now().Before(deadline) {
		got = f.surveys(t, name)
		done := 0
		for _, s := range got {
			if s.Status.Phase == phase {
				done++
			}
		}
		if done == n {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: surveys %+v, want %d %s", name, phases(got), n, phase)
	return nil
}

func phases(l []v1alpha1.TakeCareSurvey) []string {
	out := make([]string, 0, len(l))
	for _, s := range l {
		out = append(out, s.Name+"="+string(s.Status.Phase)+" "+s.Status.Reason)
	}
	return out
}

// A survey picked up with no phase is Queued, runs and is Completed with
// the import's layers, its counts and its first proposals; the next one is
// not blocked.
func TestSurveyRuns(t *testing.T) {
	f := startRunner(t)
	f.grounded(t)
	f.surveyed(t, "runs", mapServer(t, nil), nil)
	s := f.waitPhase(t, "runs", 1, v1alpha1.RunCompleted)[0]
	st := s.Status
	if len(st.Layers) != 9 || st.ProposalCounts == nil || st.ProposalCounts.Total == 0 || len(st.Proposals) > maxProposals || st.MapRevision == "" {
		t.Fatalf("status: layers %d, counts %+v, %d proposals, revision %q", len(st.Layers), st.ProposalCounts, len(st.Proposals), st.MapRevision)
	}
	if st.StartedAt == nil || st.CompletedAt == nil || st.Attempts != 1 {
		t.Errorf("run: started %v, completed %v, attempts %d", st.StartedAt, st.CompletedAt, st.Attempts)
	}
	f.checkProposals(t, &s)
	f.checkInventory(t, &s)
	f.mapRevision(t, rev2)
	f.waitPhase(t, "runs", 2, v1alpha1.RunCompleted)
}

// checkInventory checks a survey's inventory (spec 10 §8): the connectors
// from the cache with their tools, the engine's Agents, and, the cluster
// connector having no kagent.dev rule, the line saying the Agents in scope
// are not listed; its layer only counts.
func (f *fixture) checkInventory(t *testing.T, s *v1alpha1.TakeCareSurvey) {
	t.Helper()
	inv := s.Status.Inventory
	if inv == nil {
		t.Fatal("no inventory")
	}
	var conns v1alpha1.TakeCareConnectorList
	if err := f.c.List(context.Background(), &conns, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	if int(inv.MCPServers) != len(conns.Items) {
		t.Errorf("inventory: %d MCP servers, want the %d connectors", inv.MCPServers, len(conns.Items))
	}
	found := map[string]int32{}
	for _, it := range inv.Items {
		found[it.Kind+" "+it.Namespace+"/"+it.Name] = it.Tools
	}
	for _, c := range conns.Items {
		if tools, ok := found["Connector "+ns+"/"+c.Name]; !ok || int(tools) != len(c.Status.Tools) {
			t.Errorf("inventory: connector %s with %d tools, want %d (items %v)", c.Name, tools, len(c.Status.Tools), inv.Items)
		}
	}
	for _, it := range inv.Items {
		if it.Kind == "Agent" && it.Namespace != engineNS {
			t.Errorf("inventory: an Agent outside the engine namespace without the kagent.dev rule: %+v", it)
		}
	}
	var layer *v1alpha1.SurveyLayer
	for i := range s.Status.Layers {
		if s.Status.Layers[i].Name == "inventory" {
			layer = &s.Status.Layers[i]
		}
	}
	if layer == nil || int(layer.Seen) != len(inv.Items) || layer.Matched != 0 || layer.Proposed != 0 ||
		len(layer.NotSeen) != 1 || layer.NotSeen[0].Reason != "agents in scope cannot be listed: the kubernetes add-on has no kagent.dev rule" {
		t.Errorf("inventory layer %+v", layer)
	}
}

// An Intendant whose survey runs holds back only its own: another's runs.
func TestSurveysDoNotStarve(t *testing.T) {
	f := startRunner(t)
	f.grounded(t)
	hold := newGate()
	f.surveyed(t, "slow", mapServer(t, hold), nil)
	f.waitPhase(t, "slow", 1, v1alpha1.RunRunning)
	byHand := &v1alpha1.TakeCareSurvey{Name: "slow-by-hand", Namespace: ns}
	byHand.Spec.IntendantRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: "slow"}
	if err := f.c.Create(context.Background(), byHand); err != nil {
		t.Fatal(err)
	}
	f.surveyed(t, "fast", mapServer(t, nil), nil)
	f.waitPhase(t, "fast", 1, v1alpha1.RunCompleted)
	if got := f.surveys(t, "slow"); len(got) != 2 || f.waitPhase(t, "slow", 1, v1alpha1.RunQueued) == nil {
		t.Fatalf("slow's surveys %v", phases(got))
	}
	hold.release()
	f.waitPhase(t, "slow", 2, v1alpha1.RunCompleted)
}

// A Map that does not answer fails the survey, with the reason.
func TestSurveyMapFailure(t *testing.T) {
	f := startRunner(t)
	f.grounded(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	f.surveyed(t, "nomap", dead.URL+"/mcp", nil)
	s := f.waitPhase(t, "nomap", 1, v1alpha1.RunFailed)[0]
	if s.Status.Reason != failMap || strings.Contains(s.Status.Reason, "http") || s.Status.CompletedAt == nil {
		t.Errorf("failed survey %+v", s.Status.RunStatus)
	}
}

// A survey a stopped operator left Running is run again from the start:
// no half status is kept.
func TestSurveyResumed(t *testing.T) {
	f := startRunner(t)
	f.grounded(t)
	f.stop()
	ctx := context.Background()
	f.routes.Store("resumed/"+connMap, mapServer(t, nil))
	f.routes.Store("resumed/"+connCluster, clusterServer(t))
	in := &v1alpha1.TakeCareIntendant{Name: "resumed", Namespace: ns}
	in.Spec.Scope = v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{demoApps}}
	in.Spec.Connectors = v1alpha1.IntendantConnectors{Map: []string{connMap}, Cluster: []string{connCluster}}
	if err := f.c.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	left := &v1alpha1.TakeCareSurvey{Name: "left-running", Namespace: ns}
	left.Spec.IntendantRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: "resumed"}
	if err := f.c.Create(ctx, left); err != nil {
		t.Fatal(err)
	}
	half := metav1.NewTime(time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC))
	left.Status = v1alpha1.TakeCareSurveyStatus{
		Phase: v1alpha1.RunRunning, StartedAt: &half, Attempts: 1,
		Layers: []v1alpha1.SurveyLayer{{Name: "half", Seen: 99}},
	}
	if err := f.c.Status().Update(ctx, left); err != nil {
		t.Fatal(err)
	}
	// What the stopped run had recorded is replaced, not added to.
	stale := &corev1.ConfigMap{Name: proposalsName(left.Name), Namespace: ns, Data: map[string]string{"stale": "x"}}
	stale.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(left, v1alpha1.GroupVersion.WithKind(surveyKind))}
	if err := f.c.Create(ctx, stale); err != nil {
		t.Fatal(err)
	}
	f.startManager(t)
	var got v1alpha1.TakeCareSurvey
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if err := f.c.Get(ctx, client.ObjectKeyFromObject(left), &got); err == nil && got.Status.Phase == v1alpha1.RunCompleted {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got.Status.Phase != v1alpha1.RunCompleted || got.Status.Attempts != 2 || len(got.Status.Layers) != 9 || got.Status.Layers[0].Name == "half" {
		t.Fatalf("resumed survey: %+v", got.Status)
	}
	f.checkProposals(t, &got)
}

// An entity-only scope is surveyed through its entities' Map sources: the
// demo's billing-api, in demo-apps, gives a ground on demo-apps.
func TestEntityScope(t *testing.T) {
	f := startRunner(t)
	f.grounded(t)
	f.surveyed(t, "entities", mapServer(t, nil), func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.Scope = v1alpha1.Scope{Entities: []v1alpha1.EntityKey{billingService}}
	})
	st := f.waitPhase(t, "entities", 1, v1alpha1.RunCompleted)[0].Status
	if compute := st.Layers[1]; compute.Name != "compute" || compute.Seen == 0 {
		t.Errorf("compute layer %+v: the entity's namespace was not surveyed", compute)
	}
	if len(st.BlindSpots) != 0 {
		t.Errorf("blind spots %+v", st.BlindSpots)
	}
}

// What of an entity scope nothing reads is a blind spot, with the reason
// the coverage rule gives: a source in a namespace no connector declares,
// a proposed entry, an entry with no kubernetes source.
func TestEntityBlindSpots(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(demoMap)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "map", "service"), 0o750); err != nil {
		t.Fatal(err)
	}
	payments := "key: service/payments\nprovenance: declared\nstatus: validated\ncreated: 2026-11-05\nsources:\n  - url: https://payments.example\n"
	if err := os.WriteFile(filepath.Join(dir, "map", "service", "payments.yaml"), []byte(payments), 0o600); err != nil {
		t.Fatal(err)
	}
	f := startRunner(t)
	f.grounded(t)
	f.surveyed(t, "blind", mapServerAt(t, dir, nil), func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.Scope = v1alpha1.Scope{Entities: []v1alpha1.EntityKey{billingService, "network/billing-db-proxy", "service/notifications", "service/payments"}}
	})
	st := f.waitPhase(t, "blind", 1, v1alpha1.RunCompleted)[0].Status
	want := map[v1alpha1.EntityKey]struct {
		reason v1alpha1.BlindSpotReason
		says   string
	}{
		"network/billing-db-proxy": {v1alpha1.BlindSpotNoAccess, "its namespace `ack-system`: no connector reads it"},
		"service/notifications":    {v1alpha1.BlindSpotNotValidated, "proposed in the Map"},
		"service/payments":         {v1alpha1.BlindSpotNoAccess, "no kubernetes source"},
	}
	if len(st.BlindSpots) != len(want) {
		t.Fatalf("blind spots %+v", st.BlindSpots)
	}
	for _, b := range st.BlindSpots {
		if w, ok := want[b.Entity]; !ok || b.Reason != w.reason || !strings.Contains(b.Message, w.says) {
			t.Errorf("blind spot %+v, want %+v", b, want[b.Entity])
		}
	}
	if st.Layers[1].Seen == 0 {
		t.Errorf("billing-api's namespace was not surveyed: %+v", st.Layers[1])
	}
}
