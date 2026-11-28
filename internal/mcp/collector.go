// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"slices"
	"sync"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mecano/eos"
)

// The Mécano collector's calls (spec 12 §4), the only ones a collector
// endpoint admits: each with its exact arguments, each on a tool discovery
// classed read and not content (StateTools).
const (
	collectorList         = "resources_list"
	collectorGet          = "resources_get"
	collectorRepositories = "repositories_list"
	collectorIndex        = "manifests_index"
	collectorSources      = "sources_list"
	collectorVersions     = "versions_list"
	collectorCycles       = "cycles_get"
)

// maxCollectorString bounds a name, source, product or repository the
// collector sends: a Kubernetes name's bound.
const maxCollectorString = 253

// collectorKinds are the apiVersion and kind pairs the collector lists
// besides the dataset's removed apiVersions and their replacements: the
// workloads, cert-manager's Certificates and the GitOps tools' objects
// (spec 12 §4). Node is cluster-scoped and listed without a namespace.
var collectorKinds = [][2]string{
	{"apps/v1", "Deployment"}, {"apps/v1", "StatefulSet"}, {"apps/v1", "DaemonSet"}, {"batch/v1", "CronJob"},
	{"cert-manager.io/v1", "Certificate"},
	{"argoproj.io/v1alpha1", "Application"},
	{"kustomize.toolkit.fluxcd.io/v1", "Kustomization"},
}

// nodeKind is the one cluster-scoped kind the collector lists.
var nodeKind = [2]string{"v1", "Node"}

// removedPairs are the dataset's removed apiVersions and their
// replacements, with their kinds: loaded once from the embedded dataset,
// nil when it does not load (the collector then fails before any call).
var removedPairs = sync.OnceValue(func() [][2]string {
	ds, err := eos.Load()
	if err != nil {
		return nil
	}
	var out [][2]string
	for _, r := range ds.Kubernetes.RemovedAPIs.Entries {
		out = append(out, [2]string{r.APIVersion, r.Kind})
		if r.Replacement != "" {
			out = append(out, [2]string{r.Replacement, r.Kind})
		}
	}
	return out
})

// collectorAllowed is Allowed on a collector endpoint: only the shapes the
// Mécano's collector sends, on the connector types it reads (cluster, code,
// releases), each tool classed read and state by discovery. Nothing else,
// whatever the caller asks: no Secret, no other kind, no namespace outside
// the Mécano's scope, no key outside the tool's own.
func (ep Endpoint) collectorAllowed(tool string, args map[string]any) bool {
	if !ep.StateTools[tool] {
		return false
	}
	has := func(t v1alpha1.ConnectorType) bool { return slices.Contains(ep.Types, t) }
	switch tool {
	case collectorList:
		return has(v1alpha1.ConnectorCluster) && ep.listAllowed(args)
	case collectorGet:
		return has(v1alpha1.ConnectorCluster) && exactKeys(args, "apiVersion", "kind", "namespace", "name") &&
			args["apiVersion"] == "cert-manager.io/v1" && args["kind"] == "Certificate" &&
			ep.inScope(args["namespace"]) && bounded(args["name"])
	case collectorRepositories:
		return has(v1alpha1.ConnectorCode) && len(args) == 0
	case collectorIndex:
		return has(v1alpha1.ConnectorCode) && exactKeys(args, "repository") && bounded(args["repository"])
	case collectorSources:
		return has(v1alpha1.ConnectorReleases) && len(args) == 0
	case collectorVersions:
		return has(v1alpha1.ConnectorReleases) && exactKeys(args, "source", "name") && bounded(args["source"]) && bounded(args["name"])
	case collectorCycles:
		return has(v1alpha1.ConnectorReleases) && exactKeys(args, "source", "product") && bounded(args["source"]) && bounded(args["product"])
	}
	return false
}

// listAllowed admits resources_list with keys among apiVersion, kind and
// namespace: Node without a namespace, any other listed pair with a
// namespace of the scope.
func (ep Endpoint) listAllowed(args map[string]any) bool {
	for k := range args {
		if k != "apiVersion" && k != "kind" && k != "namespace" {
			return false
		}
	}
	apiVersion, ok1 := args["apiVersion"].(string)
	kind, ok2 := args["kind"].(string)
	if !ok1 || !ok2 {
		return false
	}
	pair := [2]string{apiVersion, kind}
	ns, namespaced := args["namespace"]
	if pair == nodeKind {
		return !namespaced
	}
	if !slices.Contains(collectorKinds, pair) && !slices.Contains(removedPairs(), pair) {
		return false
	}
	return namespaced && ep.inScope(ns)
}

// inScope says whether v is one of the endpoint's namespaces.
func (ep Endpoint) inScope(v any) bool {
	s, ok := v.(string)
	return ok && s != "" && slices.Contains(ep.Namespaces, s)
}

// exactKeys says whether args holds exactly these keys.
func exactKeys(args map[string]any, keys ...string) bool {
	if len(args) != len(keys) {
		return false
	}
	for _, k := range keys {
		if _, ok := args[k]; !ok {
			return false
		}
	}
	return true
}

// bounded says whether v is a string of 1 to maxCollectorString bytes.
func bounded(v any) bool {
	s, ok := v.(string)
	return ok && s != "" && len(s) <= maxCollectorString
}
