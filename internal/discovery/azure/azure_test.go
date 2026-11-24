// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	mapschema "github.com/agaetis-it/takecare-agents/api/map"
	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
	"github.com/agaetis-it/takecare-agents/internal/golden"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
)

const (
	sub1 = "11111111-1111-1111-1111-111111111111"
	sub2 = "22222222-2222-2222-2222-222222222222"
	sub3 = "33333333-3333-3333-3333-333333333333"
	// page2 is the synthetic continuation token.
	page2 = "page-2"
)

var now = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

// fake answers each request with what answer returns, and records it.
type fake struct {
	calls  []Request
	answer func(Request) ([]byte, error)
}

func (f *fake) Query(_ context.Context, req Request) ([]byte, error) {
	f.calls = append(f.calls, req)
	return f.answer(req)
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // a test's own testdata
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func isResources(req Request) bool { return strings.HasPrefix(req.Query, "Resources ") }

// shop answers the synthetic ground: sub1 whole, sub2 narrowed to rg-shop.
func shop(t *testing.T) func(Request) ([]byte, error) {
	return func(req Request) ([]byte, error) {
		subs := strings.Join(req.Subscriptions, ",")
		switch {
		case !isResources(req) && subs == sub1:
			return fixture(t, "synthetic-groups-whole.json"), nil
		case !isResources(req) && subs == sub2:
			return fixture(t, "synthetic-groups-narrowed.json"), nil
		case subs == sub1 && req.SkipToken == "":
			return fixture(t, "synthetic-resources-whole-1.json"), nil
		case subs == sub1 && req.SkipToken == page2:
			return fixture(t, "synthetic-resources-whole-2.json"), nil
		case subs == sub2:
			return fixture(t, "synthetic-resources-narrowed.json"), nil
		}
		return nil, fmt.Errorf("unexpected request %+v", req)
	}
}

func shopGround() Ground {
	return Ground{Connector: "azure-prod", Reach: v1alpha1.AzureReach{
		TenantID: "00000000-0000-0000-0000-000000000000",
		Subscriptions: []v1alpha1.AzureSubscription{
			{ID: strings.ToUpper(sub1)},
			{ID: sub2, ResourceGroups: []v1alpha1.ResourceGroupName{"RG-Shop"}},
		},
	}}
}

func entry(key, status string, sources ...map[string]string) mapserver.Entry {
	return mapserver.Entry{Key: key, Provenance: "declared", Status: status, Created: "2026-11-01", Sources: sources}
}

func az(id string) map[string]string { return map[string]string{"azure": id} }

// shopMap is the Map before the survey: one environment and one datastore
// matched by their azure source (in another case), a validated storage
// account that is gone and a proposed one (kept: only validated entries
// are proposed for removal), one out of the reach, a kubernetes entry, and a
// declared key a new load balancer's name collides with.
func shopMap() []mapserver.Entry {
	return []mapserver.Entry{
		entry("environment/data", "validated", az("/subscriptions/"+sub1+"/resourceGroups/rg-data")),
		entry("datastore/orders", "validated", az("/SUBSCRIPTIONS/"+sub1+"/RESOURCEGROUPS/RG-DATA/providers/Microsoft.Sql/servers/sql-orders/databases/orders")),
		entry("storage/draft", "proposed", az("/subscriptions/"+sub1+"/resourceGroups/rg-data/providers/Microsoft.Storage/storageAccounts/stdraft")),
		entry("storage/old-exports", "validated", az("/subscriptions/"+sub1+"/resourceGroups/rg-data/providers/Microsoft.Storage/storageAccounts/stexports")),
		entry("compute/legacy", "validated", az("/subscriptions/"+sub3+"/resourceGroups/rg-old/providers/Microsoft.Compute/virtualMachines/legacy")),
		entry("compute/billing-api", "validated", map[string]string{"kubernetes": "demo-apps/Deployment/billing-api"}),
		entry("network/lb-front", "validated"),
	}
}

func asJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func TestImport(t *testing.T) {
	f := &fake{answer: shop(t)}
	res, err := Import(context.Background(), f, []Ground{shopGround()}, shopMap(), now)
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "import.json", asJSON(t, res))

	for _, req := range f.calls {
		if len(req.Subscriptions) == 0 {
			t.Errorf("a request without subscriptions: %+v", req)
		}
		if strings.Contains(req.Query, "properties") || strings.Contains(req.Query, "tags") {
			t.Errorf("a query reads properties or tags: %s", req.Query)
		}
	}
	var narrowed, second []Request
	for _, req := range f.calls {
		if req.Subscriptions[0] == sub2 {
			narrowed = append(narrowed, req)
		}
		if req.SkipToken == page2 {
			second = append(second, req)
		}
	}
	for _, req := range narrowed {
		if !strings.Contains(req.Query, "in~ ('rg-shop')") {
			t.Errorf("the narrowed subscription's query is not narrowed to its group: %s", req.Query)
		}
	}
	if len(second) != 1 || second[0].Query != resourcesQuery(nil) || !slices.Equal(second[0].Subscriptions, []string{sub1}) {
		t.Errorf("the second page was not asked with the same query and scopes: %+v", second)
	}
}

func TestRequestBody(t *testing.T) {
	raw, err := Request{Query: "Q", Subscriptions: []string{sub1}, SkipToken: page2}.Body()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"subscriptions":["` + sub1 + `"],"query":"Q","options":{"$top":1000,"$skipToken":"page-2","resultFormat":"objectArray"}}`
	if string(raw) != want {
		t.Errorf("body:\n got %s\nwant %s", raw, want)
	}
	raw, _ = Request{Query: "Q", Subscriptions: []string{sub1}}.Body()
	if strings.Contains(string(raw), "skipToken") {
		t.Errorf("a first page carries a token: %s", raw)
	}
}

func TestParsePage(t *testing.T) {
	row := `{"id":"/subscriptions/` + sub1 + `/resourceGroups/rg-shop","name":"rg-shop","subscriptionId":"` + sub1 + `"}`
	for _, tc := range []struct {
		name, raw string
		resources bool
		ok        bool
	}{
		{"groups row", `{"count":1,"data":[` + row + `],"resultTruncated":"false"}`, false, true},
		{"no count", `{"data":[` + row + `],"resultTruncated":"false"}`, false, false},
		{"count differs from the rows", `{"count":2,"data":[` + row + `],"resultTruncated":"false"}`, false, false},
		{"no data", `{"count":0,"resultTruncated":"false"}`, false, false},
		{"truncated as a boolean", `{"count":1,"data":[` + row + `],"resultTruncated":true}`, false, false},
		{"truncated, another value", `{"count":1,"data":[` + row + `],"resultTruncated":"maybe"}`, false, false},
		{"row without id", `{"count":1,"data":[{"name":"x","subscriptionId":"` + sub1 + `"}],"resultTruncated":"false"}`, false, false},
		{"id not a string", `{"count":1,"data":[{"id":1,"name":"x","subscriptionId":"` + sub1 + `"}],"resultTruncated":"false"}`, false, false},
		{"resources row without type", `{"count":1,"data":[` + row + `],"resultTruncated":"false"}`, true, false},
		{"table format", string(fixture(t, "synthetic-table-format.json")), false, false},
		{"not JSON", `<html>`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsePage([]byte(tc.raw), tc.resources)
			if (err == nil) != tc.ok {
				t.Errorf("parsePage: err = %v, want ok %v", err, tc.ok)
			}
		})
	}
}

// notSeen lists a result's notSeen lines as "layer ref: reason".
func notSeen(res discovery.Result) []string {
	var out []string
	for _, l := range res.Layers {
		for _, n := range l.NotSeen {
			out = append(out, l.Name+" "+n.Ref+": "+n.Reason)
		}
	}
	return out
}

func TestUnreadResources(t *testing.T) {
	ground := Ground{Connector: "c", Reach: v1alpha1.AzureReach{Subscriptions: []v1alpha1.AzureSubscription{{ID: sub1}}}}
	groups := func(t *testing.T) []byte { return fixture(t, "synthetic-groups-whole.json") }
	always := []byte(`{"$skipToken":"next","count":0,"data":[],"resultTruncated":"false"}`)
	for _, tc := range []struct {
		name      string
		resources func(t *testing.T) ([]byte, error)
		reason    string
		calls     int
	}{
		{"truncated without a token", func(t *testing.T) ([]byte, error) { return fixture(t, "synthetic-truncated.json"), nil }, reasonTruncated, 1},
		{"page bound", func(*testing.T) ([]byte, error) { return always, nil }, reasonPageBound, maxPages},
		{"call failed", func(*testing.T) ([]byte, error) { return nil, errors.New("boom") }, reasonCallFailed, 1},
		{"unrecognized", func(t *testing.T) ([]byte, error) { return fixture(t, "synthetic-table-format.json"), nil }, reasonUnrecognized, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			f := &fake{answer: func(req Request) ([]byte, error) {
				if !isResources(req) {
					return groups(t), nil
				}
				calls++
				return tc.resources(t)
			}}
			gone := entry("compute/gone", "validated", az("/subscriptions/"+sub1+"/resourceGroups/rg-shop/providers/Microsoft.Compute/virtualMachines/gone"))
			res, err := Import(context.Background(), f, []Ground{ground}, []mapserver.Entry{gone}, now)
			if err != nil {
				t.Fatal(err)
			}
			if calls != tc.calls {
				t.Errorf("resources calls = %d, want %d", calls, tc.calls)
			}
			want := make([]string, 0, len(resourceLayers))
			for _, l := range resourceLayers {
				want = append(want, l+" c/subscriptions/"+sub1+": "+tc.reason)
			}
			if diff := cmp.Diff(want, notSeen(res)); diff != "" {
				t.Errorf("notSeen (-want +got):\n%s", diff)
			}
			for _, p := range res.Proposals {
				if p.Action == v1alpha1.ProposalRemove {
					t.Errorf("a removal proposed on a scope not read whole: %s", p.Key)
				}
			}
		})
	}
}

func TestContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fake{answer: func(Request) ([]byte, error) { cancel(); return nil, context.Canceled }}
	_, err := Import(ctx, f, []Ground{shopGround()}, nil, now)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestPlan(t *testing.T) {
	subs := make([]v1alpha1.AzureSubscription, 0, 152)
	for i := range 150 {
		subs = append(subs, v1alpha1.AzureSubscription{ID: fmt.Sprintf("%08x-0000-0000-0000-000000000000", i)})
	}
	subs = append(subs,
		v1alpha1.AzureSubscription{ID: sub2, ResourceGroups: []v1alpha1.ResourceGroupName{"rg-a", "bad'name"}},
		v1alpha1.AzureSubscription{ID: sub3, ResourceGroups: []v1alpha1.ResourceGroupName{"also bad"}},
	)
	f := &fake{answer: func(Request) ([]byte, error) {
		return []byte(`{"count":0,"data":[],"resultTruncated":"false"}`), nil
	}}
	res, err := Import(context.Background(), f, []Ground{{Connector: "c", Reach: v1alpha1.AzureReach{Subscriptions: subs}}}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	var sizes []int
	for _, req := range f.calls {
		if isResources(req) {
			sizes = append(sizes, len(req.Subscriptions))
		}
		if slices.Contains(req.Subscriptions, sub3) {
			t.Errorf("a subscription with no valid group was queried, wider than its reach: %+v", req)
		}
		if strings.Contains(req.Query, "bad") {
			t.Errorf("an invalid group name reached a query: %s", req.Query)
		}
	}
	if !slices.Equal(sizes, []int{100, 50, 1}) {
		t.Errorf("subscriptions per resources request = %v, want [100 50 1]", sizes)
	}
	want := []string{
		"environment c/subscriptions/" + sub2 + ": " + reasonGroupName,
		"environment c/subscriptions/" + sub3 + ": " + reasonGroupName,
	}
	if diff := cmp.Diff(want, notSeen(res)); diff != "" {
		t.Errorf("notSeen (-want +got):\n%s", diff)
	}
}

func TestKeyName(t *testing.T) {
	for in, want := range map[string]string{
		"vm-web-1":   "vm-web-1",
		"Cache_Shop": "cache-shop",
		"app.(prod)": "app.-prod",
		"_x_":        "x",
		"données":    "donn--es",
	} {
		if got := keyName(in); got != want {
			t.Errorf("keyName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQualify(t *testing.T) {
	s := func(layer, sub, rg string) *seen {
		return &seen{layer: layer, scope: scopeOf(sub, rg)}
	}
	for _, tc := range []struct {
		name string
		set  []*seen
		want []string
	}{
		{"two groups", []*seen{s("compute", sub1, "rg-a"), s("compute", sub1, "rg-b")}, []string{"compute/x-rg-a", "compute/x-rg-b"}},
		{"two subscriptions, one group name", []*seen{s("compute", sub1, "rg-a"), s("compute", sub2, "rg-a")}, []string{"compute/x-11111111", "compute/x-22222222"}},
		{"neither alone", []*seen{s("compute", sub1, "rg-a"), s("compute", sub2, "rg-a"), s("compute", sub1, "rg-b")}, []string{"compute/x-rg-a-11111111", "compute/x-rg-a-22222222", "compute/x-rg-b-11111111"}},
		{"taken in the Map", []*seen{s("compute", sub1, "rg-a")}, []string{"compute/x-rg-a"}},
		{"environments", []*seen{s("environment", sub1, "x"), s("environment", sub2, "x")}, []string{"environment/x-11111111", "environment/x-22222222"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			qualify(tc.set[0].layer+"/x", tc.set)
			var got []string
			for _, s := range tc.set {
				got = append(got, s.key)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("keys (-want +got):\n%s", diff)
			}
		})
	}
}

func TestArmType(t *testing.T) {
	for id, want := range map[string]string{
		"/subscriptions/" + sub1 + "/resourceGroups/g/providers/Microsoft.Sql/servers/s/databases/d":                                     "microsoft.sql/servers/databases",
		"/subscriptions/" + sub1 + "/resourceGroups/g/providers/Microsoft.Web/sites/a":                                                   "microsoft.web/sites",
		"/subscriptions/" + sub1 + "/resourceGroups/g":                                                                                   "",
		"/subscriptions/" + sub1 + "/resourceGroups/g/providers/Microsoft.Web/sites/a/providers/Microsoft.Insights/diagnosticSettings/d": "",
	} {
		if got := armType(id); got != want {
			t.Errorf("armType(%q) = %q, want %q", id, got, want)
		}
	}
}

func scopeOf(sub, rg string) mapschema.AzureScope {
	return mapschema.AzureScope{Subscription: sub, ResourceGroup: rg}
}
