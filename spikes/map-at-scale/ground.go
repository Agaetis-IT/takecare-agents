// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/agaetis-it/takecare-agents/internal/discovery"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// served maps a group/kind to the version the API server serves it at,
// for the kinds the survey lists (discovery's kinds): a live server answers
// a list in its served version whatever version Git wrote.
var served = map[string]string{
	"apps/Deployment":                     "apps/v1",
	"apps/StatefulSet":                    "apps/v1",
	"apps/DaemonSet":                      "apps/v1",
	"batch/CronJob":                       "batch/v1",
	"/Service":                            "v1",
	"networking.k8s.io/Ingress":           "networking.k8s.io/v1",
	"extensions/Ingress":                  "networking.k8s.io/v1",
	"gateway.networking.k8s.io/HTTPRoute": "gateway.networking.k8s.io/v1",
	"/ServiceAccount":                     "v1",
	"/PersistentVolumeClaim":              "v1",
}

// fakeGround answers namespaces_list and resources_list as the kubernetes
// add-on does (kubernetes-mcp-server v0.0.67, list_output "table"): the
// namespaces as its text table, a list as structuredContent items with the
// table printer's LABELS column. What it bypasses: the MCP transport, the
// gateway, the endpoint's tool allow-list and the server's RBAC.
type fakeGround struct {
	namespaces []string
	lists      map[string][]*obj // apiVersion|kind|namespace
}

func newGround(objs []*obj, namespaces map[string]bool) *fakeGround {
	g := &fakeGround{namespaces: slices.Sorted(maps.Keys(namespaces)), lists: map[string][]*obj{}}
	for _, o := range objs {
		v, ok := served[o.group()+"/"+o.kind]
		if !ok {
			continue
		}
		k := v + "|" + o.kind + "|" + o.ns
		g.lists[k] = append(g.lists[k], o)
	}
	return g
}

func (g *fakeGround) Call(_ context.Context, _ mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error) {
	switch tool {
	case "namespaces_list":
		var b strings.Builder
		b.WriteString("APIVERSION   KIND        NAME   STATUS   AGE   LABELS\n")
		for _, ns := range g.namespaces {
			fmt.Fprintf(&b, "v1   Namespace   %s   Active   1d   <none>\n", ns)
		}
		return mcp.Result{Text: b.String()}, nil
	case "resources_list":
		apiVersion, _ := args["apiVersion"].(string)
		kind, _ := args["kind"].(string)
		ns, _ := args["namespace"].(string)
		objs := g.lists[apiVersion+"|"+kind+"|"+ns]
		if len(objs) == 0 {
			return mcp.Result{}, nil
		}
		items := make([]map[string]string, 0, len(objs))
		seen := map[string]bool{}
		for _, o := range objs {
			if seen[o.name] {
				continue // the API server holds one object per name: the last applied wins
			}
			seen[o.name] = true
			items = append(items, map[string]string{"apiVersion": apiVersion, "kind": kind, "Name": o.name, "Namespace": ns, "Labels": labelsCell(o.labels)})
		}
		raw, err := json.Marshal(map[string]any{"items": items})
		if err != nil {
			return mcp.Result{}, err
		}
		return mcp.Result{Structured: raw}, nil
	}
	return mcp.Result{IsError: true, Text: "unknown tool"}, nil
}

func labelsCell(l map[string]string) string {
	if len(l) == 0 {
		return "<none>"
	}
	pairs := make([]string, 0, len(l))
	for _, k := range slices.Sorted(maps.Keys(l)) {
		pairs = append(pairs, k+"="+l[k])
	}
	return strings.Join(pairs, ",")
}

// multiGround routes a call to the cluster its endpoint names.
type multiGround map[string]*fakeGround

func (m multiGround) Call(ctx context.Context, ep mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error) {
	return m[strings.TrimPrefix(ep.URL, "fake://")].Call(ctx, ep, tool, args)
}

var surveyDay = time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)

// survey runs the product's import over the objects, every namespace of
// each cluster in scope but those excluded (and kube-*, the import's own
// default exclusion).
func survey(s *snapshot, objs []*obj, excludeNS func(string) bool, ix *discovery.Index) (discovery.Result, error) {
	byCluster := map[string][]*obj{}
	for _, o := range objs {
		byCluster[o.cluster] = append(byCluster[o.cluster], o)
	}
	m := multiGround{}
	var grounds []discovery.Ground
	for _, c := range clusters(s) {
		m[c] = newGround(byCluster[c], s.namespaces[c])
		var scope []string
		for _, ns := range m[c].namespaces {
			if !strings.HasPrefix(ns, "kube-") && !excludeNS(ns) {
				scope = append(scope, ns)
			}
		}
		if len(scope) == 0 {
			continue
		}
		grounds = append(grounds, discovery.Ground{Connector: "kubernetes", Cluster: c, Endpoint: mcp.Endpoint{URL: "fake://" + c}, Namespaces: scope})
	}
	return discovery.Import(context.Background(), m, grounds, nil, ix, surveyDay)
}
