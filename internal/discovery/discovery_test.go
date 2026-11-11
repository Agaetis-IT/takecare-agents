// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/golden"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/mcp/mcptest"
)

const (
	fixtures   = "testdata/kms-v0.0.67"
	demoApps   = "demo-apps"
	ackSystem  = "ack-system"
	connector  = "kubernetes"
	deployment = "Deployment"

	wantUnrecognizedList = "unrecognized resources_list output"
)

var today = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

// answer is a captured tools/call result.
type answer struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent any  `json:"structuredContent"`
	IsError           bool `json:"isError"`
}

func fixture(t *testing.T, name string) answer {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtures, name)) //nolint:gosec // a test's own testdata
	if err != nil {
		t.Fatal(err)
	}
	var a answer
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func (a answer) reply() (string, any, bool) {
	text := ""
	if len(a.Content) > 0 {
		text = a.Content[0].Text
	}
	return text, a.StructuredContent, a.IsError
}

// fakeGround serves the captured answers: demo-apps' objects, an empty list
// elsewhere, HTTPRoute not served; lists overrides an answer by
// "<namespace>/<Kind>", namespaces overrides namespaces_list's.
type fakeGround struct {
	lists      map[string]answer
	namespaces *answer
}

func (f fakeGround) start(t *testing.T) *mcptest.Server {
	t.Helper()
	ns := fixture(t, "namespaces_list.json")
	if f.namespaces != nil {
		ns = *f.namespaces
	}
	objects := map[string]answer{}
	for _, k := range []string{deployment, "Service", "ServiceAccount", "PersistentVolumeClaim"} {
		objects[demoApps+"/"+k] = fixture(t, "demo-apps-"+k+".json")
	}
	empty, notServed := fixture(t, "empty.json"), fixture(t, "not-served-HTTPRoute.json")
	return mcptest.Start(t,
		mcptest.ToolSpec{Name: toolNamespaces, ReadOnly: true, Handle: func(map[string]any) (string, any, bool) { return ns.reply() }},
		mcptest.ToolSpec{Name: toolList, ReadOnly: true, Handle: func(args map[string]any) (string, any, bool) {
			key := args["namespace"].(string) + "/" + args["kind"].(string)
			if a, ok := f.lists[key]; ok {
				return a.reply()
			}
			if a, ok := objects[key]; ok {
				return a.reply()
			}
			if args["kind"] == "HTTPRoute" {
				return notServed.reply()
			}
			return empty.reply()
		}},
	)
}

func endpoint(url string) mcp.Endpoint {
	return mcp.Endpoint{URL: url, Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}, Addon: true,
		StateTools: map[string]bool{toolNamespaces: true, toolList: true}}
}

func demoIndex(t *testing.T, extra ...mapserver.Entry) *Index {
	t.Helper()
	m, err := mapserver.Load(os.DirFS("../mapserver/testdata/demo"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []mapserver.Entry
	for _, e := range m.Entries {
		entries = append(entries, *e)
	}
	return NewIndex(append(entries, extra...))
}

func survey(t *testing.T, srv *mcptest.Server, ix *Index, namespaces ...string) Result {
	t.Helper()
	caller := &mcp.Client{HTTP: &http.Client{Timeout: 5 * time.Second}}
	res, err := Import(context.Background(), caller, []Ground{{Connector: connector, Endpoint: endpoint(srv.URL), Namespaces: namespaces}}, ix, today)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func asJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

// The demo's ground against the demo's Map: its entries matched, the
// objects the Map lacks proposed, HTTPRoute's API not served.
func TestImportDemo(t *testing.T) {
	srv := fakeGround{}.start(t)
	res := survey(t, srv, demoIndex(t))
	golden.Assert(t, "demo.json", asJSON(t, res))

	again := survey(t, srv, demoIndex(t))
	if diff := cmp.Diff(string(asJSON(t, res)), string(asJSON(t, again))); diff != "" {
		t.Errorf("a second survey of the same ground differs (-first +second):\n%s", diff)
	}
	if got := ProposalID(v1alpha1.ProposalAdd, "compute/notifications-worker"); got != idOf(res, "compute/notifications-worker") || len(got) != 16 {
		t.Errorf("id %q is not the first 16 hex characters of sha256(action+key)", got)
	}
}

func idOf(res Result, key string) string {
	for _, p := range res.Proposals {
		if p.Key == key {
			return p.ID
		}
	}
	return ""
}

// What a connector could not read is a notSeen line with its reason.
func TestNotSeen(t *testing.T) {
	badHeader := answer{Content: []struct {
		Text string `json:"text"`
	}{{Text: "NAME   STATUS\ndemo-apps   Active\n"}}}
	noLabels := answer{StructuredContent: map[string]any{"items": []any{map[string]any{"apiVersion": "v1", "kind": "Service", "Name": "billing-api", "Namespace": demoApps}}}}
	otherKind := answer{StructuredContent: map[string]any{"items": []any{map[string]any{"apiVersion": "v1", "kind": "Secret", "Name": "x", "Namespace": demoApps, "Labels": "<none>"}}}}
	textOnly := answer{Content: []struct {
		Text string `json:"text"`
	}{{Text: "NAMESPACE   APIVERSION   KIND   NAME\n"}}}
	for _, tc := range []struct {
		name       string
		ground     fakeGround
		namespaces []string
		state      map[string]bool
		layer      string
		want       NotSeen
	}{
		{"forbidden namespace", fakeGround{lists: map[string]answer{ackSystem + "/" + deployment: fixture(t, "forbidden-ack-system-Deployment.json")}}, nil, nil,
			LayerCompute, NotSeen{Ref: ackSystem + "/" + deployment, Reason: reasonForbidden}},
		{"API not served, once per cluster", fakeGround{}, nil, nil, LayerNetwork, NotSeen{Ref: "HTTPRoute", Reason: reasonNotServed}},
		{"an item without its labels", fakeGround{lists: map[string]answer{demoApps + "/Service": noLabels}}, nil, nil,
			LayerNetwork, NotSeen{Ref: demoApps + "/Service", Reason: wantUnrecognizedList}},
		{"an item of another kind", fakeGround{lists: map[string]answer{demoApps + "/Service": otherKind}}, nil, nil,
			LayerNetwork, NotSeen{Ref: demoApps + "/Service", Reason: wantUnrecognizedList}},
		{"a text table without structured content", fakeGround{lists: map[string]answer{demoApps + "/Service": textOnly}}, nil, nil,
			LayerNetwork, NotSeen{Ref: demoApps + "/Service", Reason: wantUnrecognizedList}},
		{"namespaces_list with another header", fakeGround{namespaces: &badHeader}, nil, nil,
			LayerEnvironment, NotSeen{Ref: connector, Reason: "unrecognized namespaces_list output"}},
		{"a namespace of the scope that does not exist", fakeGround{}, []string{demoApps, "ghost"}, nil,
			LayerEnvironment, NotSeen{Ref: "ghost", Reason: reasonNoNamespace}},
		{"a tool the operator may not call", fakeGround{}, nil, map[string]bool{toolList: true},
			LayerEnvironment, NotSeen{Ref: connector, Reason: reasonNotAllowed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := tc.ground.start(t)
			ep := endpoint(srv.URL)
			if tc.state != nil {
				ep.StateTools = tc.state
			}
			caller := &mcp.Client{HTTP: &http.Client{Timeout: 5 * time.Second}}
			res, err := Import(context.Background(), caller, []Ground{{Connector: connector, Endpoint: ep, Namespaces: tc.namespaces}}, demoIndex(t), today)
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range res.Layers {
				if l.Name == tc.layer {
					if slices.Contains(l.NotSeen, tc.want) {
						return
					}
					t.Fatalf("layer %s notSeen = %v, want %v", l.Name, l.NotSeen, tc.want)
				}
			}
			t.Fatalf("no layer %s", tc.layer)
		})
	}
}

// A server that does not answer is a notSeen line, not an error.
func TestCallFailedIsNotSeen(t *testing.T) {
	srv := fakeGround{}.start(t)
	srv.Close()
	res := survey(t, srv, demoIndex(t))
	if got := res.Layers[0].NotSeen; len(got) != 1 || got[0] != (NotSeen{Ref: connector, Reason: reasonCallFailed}) {
		t.Errorf("environment notSeen = %v", got)
	}
	if len(res.Proposals) != 0 {
		t.Errorf("proposals from a ground never read: %v", res.Proposals)
	}
}

// Remove: a validated entry whose sources were all surveyed and none seen,
// its lastSeen kept. An entry of a kind or namespace the survey could not
// list, or a proposed one, stays.
func TestRemove(t *testing.T) {
	entry := func(key, status, source string) mapserver.Entry {
		return mapserver.Entry{Key: key, Provenance: "declared", Status: status, Created: "2026-11-01", LastSeen: "2026-11-01",
			Sources: []map[string]string{{"kubernetes": source}}}
	}
	ix := demoIndex(t,
		entry("compute/old-worker", "validated", demoApps+"/Deployment/old-worker"),
		entry("compute/hidden", "validated", ackSystem+"/Deployment/hidden"),
		entry("compute/maybe", "proposed", demoApps+"/Deployment/maybe"),
		entry("environment/gone", "validated", "gone"),
	)
	srv := fakeGround{lists: map[string]answer{ackSystem + "/" + deployment: fixture(t, "forbidden-ack-system-Deployment.json")}}.start(t)
	res := survey(t, srv, ix)
	removed := map[string]Proposal{}
	for _, p := range res.Proposals {
		if p.Action == v1alpha1.ProposalRemove {
			removed[p.Key] = p
		}
	}
	for _, key := range []string{"compute/old-worker", "environment/gone"} {
		p, ok := removed[key]
		if !ok {
			t.Errorf("no removal of %s: %v", key, removed)
			continue
		}
		if !strings.Contains(p.Entry, "lastSeen: \"2026-11-01\"") || p.ID != ProposalID(v1alpha1.ProposalRemove, key) {
			t.Errorf("removal of %s: id %s, entry\n%s", key, p.ID, p.Entry)
		}
	}
	for _, key := range []string{"compute/hidden", "compute/maybe", "network/billing-db-proxy"} {
		if _, ok := removed[key]; ok {
			t.Errorf("%s is proposed for removal", key)
		}
	}
}

// A workload no service entry maps gives a service from
// app.kubernetes.io/part-of, else app.kubernetes.io/name, running as its
// compute entry.
func TestServiceFromLabels(t *testing.T) {
	srv := fakeGround{}.start(t)
	res := survey(t, srv, NewIndex(nil), demoApps)
	want := map[string]string{
		"service/takecare-demo": "runsAs:\n  - compute/billing-api\n",
		"service/notifications": "runsAs:\n  - compute/notifications-worker\n",
	}
	for _, p := range res.Proposals {
		if w, ok := want[p.Key]; ok {
			if p.Action != v1alpha1.ProposalAdd || !strings.Contains(p.Entry, w) {
				t.Errorf("%s: %s\n%s", p.Key, p.Action, p.Entry)
			}
			delete(want, p.Key)
		}
	}
	if len(want) != 0 {
		t.Errorf("no proposal for %v", want)
	}
}

// A matched entry whose only difference is an old lastSeen is matched and
// proposed nothing; one missing its deployedIn gets an Update that also
// refreshes lastSeen.
func TestNoUpdateForLastSeenAlone(t *testing.T) {
	entry := func(key string, relations mapserver.Relations) mapserver.Entry {
		return mapserver.Entry{Key: key, Provenance: "declared", Status: "validated", Created: "2026-11-01", LastSeen: "2026-11-01",
			Sources: []map[string]string{{sourceKubernetes: demoApps + "/Deployment/" + strings.TrimPrefix(key, "compute/")}}, Relations: relations}
	}
	ix := NewIndex([]mapserver.Entry{
		{Key: "environment/demo-apps", Provenance: "declared", Status: "validated", Created: "2026-11-01", LastSeen: "2026-11-01",
			Sources: []map[string]string{{sourceKubernetes: demoApps}}},
		entry("compute/billing-api", mapserver.Relations{DeployedIn: []string{"environment/demo-apps"}}),
		entry("compute/notifications-worker", mapserver.Relations{}),
	})
	res := survey(t, fakeGround{}.start(t), ix, demoApps)
	byKey := map[string]Proposal{}
	for _, p := range res.Proposals {
		byKey[p.Key] = p
	}
	for _, key := range []string{"environment/demo-apps", "compute/billing-api"} {
		if p, ok := byKey[key]; ok {
			t.Errorf("%s, changed only by lastSeen, is proposed: %s", key, p.Action)
		}
	}
	p, ok := byKey["compute/notifications-worker"]
	if !ok || p.Action != v1alpha1.ProposalUpdate || !strings.Contains(p.Entry, "lastSeen: \"2026-11-11\"") || !strings.Contains(p.Entry, "deployedIn:\n  - environment/demo-apps") {
		t.Errorf("the entry missing its deployedIn: %+v", p)
	}
	if res.Layers[0].Matched != 1 || res.Layers[1].Matched != 2 {
		t.Errorf("matched: environment %d, compute %d", res.Layers[0].Matched, res.Layers[1].Matched)
	}
}
