// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"testing"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const (
	scoped  = "demo-apps"
	foreign = "kube-system"
	kDeploy = "Deployment"
	vApps   = "apps/v1"
	vCert   = "cert-manager.io/v1"
	kCert   = "Certificate"
	kRepo   = "repository"
	kSource = "source"
	rDemo   = "demo"
	sGHCR   = "ghcr"
	kName   = "name"
)

// collectorEndpoint is a collector endpoint of the given type, every
// collector tool classed read and state.
func collectorEndpoint(t v1alpha1.ConnectorType) Endpoint {
	state := map[string]bool{}
	for _, tool := range []string{collectorList, collectorGet, collectorRepositories, collectorIndex, collectorSources, collectorVersions, collectorCycles} {
		state[tool] = true
	}
	return Endpoint{Types: []v1alpha1.ConnectorType{t}, Collector: true, Namespaces: []string{scoped, "argocd"}, StateTools: state, ReadTools: state}
}

// TestCollectorAllowed is the table of the Mécano collector's shapes
// (spec 12 §4): each call it sends is admitted, anything around it is not.
func TestCollectorAllowed(t *testing.T) {
	cluster := collectorEndpoint(v1alpha1.ConnectorCluster)
	code := collectorEndpoint(v1alpha1.ConnectorCode)
	releases := collectorEndpoint(v1alpha1.ConnectorReleases)
	list := func(apiVersion, kind, ns string) map[string]any {
		a := map[string]any{"apiVersion": apiVersion, "kind": kind}
		if ns != "" {
			a["namespace"] = ns
		}
		return a
	}
	get := func(apiVersion, kind, ns, name string) map[string]any {
		a := list(apiVersion, kind, ns)
		a[kName] = name
		return a
	}
	with := func(a map[string]any, k string, v any) map[string]any { a[k] = v; return a }
	tests := []struct {
		name string
		ep   Endpoint
		tool string
		args map[string]any
		want bool
	}{
		{"deployments of the scope", cluster, collectorList, list(vApps, kDeploy, scoped), true},
		{"statefulsets", cluster, collectorList, list(vApps, "StatefulSet", scoped), true},
		{"daemonsets", cluster, collectorList, list(vApps, "DaemonSet", scoped), true},
		{"cronjobs", cluster, collectorList, list("batch/v1", "CronJob", scoped), true},
		{"certificates", cluster, collectorList, list(vCert, kCert, scoped), true},
		{"argo applications", cluster, collectorList, list("argoproj.io/v1alpha1", "Application", "argocd"), true},
		{"flux kustomizations", cluster, collectorList, list("kustomize.toolkit.fluxcd.io/v1", "Kustomization", scoped), true},
		{"nodes, no namespace", cluster, collectorList, list("v1", "Node", ""), true},
		{"a removed apiVersion of the dataset", cluster, collectorList, list("batch/v1beta1", "CronJob", scoped), true},
		{"its replacement", cluster, collectorList, list("autoscaling/v2", "HorizontalPodAutoscaler", scoped), true},
		{"a certificate", cluster, collectorGet, get(vCert, kCert, scoped, "billing-tls"), true},
		{"repositories", code, collectorRepositories, map[string]any{}, true},
		{"the index", code, collectorIndex, map[string]any{kRepo: rDemo}, true},
		{"sources", releases, collectorSources, map[string]any{}, true},
		{"versions", releases, collectorVersions, map[string]any{kSource: sGHCR, kName: "agaetis-it/demo-app"}, true},
		{"cycles", releases, collectorCycles, map[string]any{kSource: "eol", "product": "kubernetes"}, true},

		{"secrets", cluster, collectorList, list("v1", "Secret", scoped), false},
		{"a secret by name", cluster, collectorGet, get("v1", "Secret", scoped, "token"), false},
		{"a deployment by name", cluster, collectorGet, get(vApps, kDeploy, scoped, "billing-api"), false},
		{"configmaps", cluster, collectorList, list("v1", "ConfigMap", scoped), false},
		{"an unknown kind", cluster, collectorList, list("example.com/v1", "Widget", scoped), false},
		{"a namespace out of scope", cluster, collectorList, list(vApps, kDeploy, foreign), false},
		{"a certificate out of scope", cluster, collectorGet, get(vCert, kCert, foreign, "x"), false},
		{"a list without namespace", cluster, collectorList, list(vApps, kDeploy, ""), false},
		{"nodes in a namespace", cluster, collectorList, list("v1", "Node", scoped), false},
		{"an extra key on a list", cluster, collectorList, with(list(vApps, kDeploy, scoped), "labelSelector", "app=x"), false},
		{"an extra key on a get", cluster, collectorGet, with(get(vCert, kCert, scoped, "x"), "output", "yaml"), false},
		{"a name too long", cluster, collectorGet, get(vCert, kCert, scoped, strings.Repeat("a", 254)), false},
		{"a namespace of another type", cluster, collectorList, with(list(vApps, kDeploy, ""), "namespace", 3), false},
		{"another cluster tool", cluster, "pods_log", map[string]any{"namespace": scoped, kName: "x"}, false},
		{"a list on a code connector", code, collectorList, list(vApps, kDeploy, scoped), false},
		{"a path on the index", code, collectorIndex, map[string]any{kRepo: rDemo, "path": "apps/"}, false},
		{"file_get", code, "file_get", map[string]any{kRepo: rDemo, "path": "values.yaml"}, false},
		{"an index without repository", code, collectorIndex, map[string]any{}, false},
		{"arguments on repositories", code, collectorRepositories, map[string]any{kRepo: rDemo}, false},
		{"the index on a cluster connector", cluster, collectorIndex, map[string]any{kRepo: rDemo}, false},
		{"versions without a name", releases, collectorVersions, map[string]any{kSource: sGHCR}, false},
		{"versions with an extra key", releases, collectorVersions, map[string]any{kSource: sGHCR, kName: "x", "url": "https://evil.example"}, false},
		{"cycles with a name", releases, collectorCycles, map[string]any{kSource: "eol", kName: "kubernetes"}, false},
		{"an empty source", releases, collectorVersions, map[string]any{kSource: "", kName: "x"}, false},
		{"a source too long", releases, collectorCycles, map[string]any{kSource: strings.Repeat("s", 254), "product": "x"}, false},
		{"versions on a code connector", code, collectorVersions, map[string]any{kSource: sGHCR, kName: "x"}, false},
		{"a report connector's post", collectorEndpoint(v1alpha1.ConnectorReport), "post_message", map[string]any{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ep.Allowed(tt.tool, tt.args); got != tt.want {
				t.Errorf("Allowed(%s, %v) = %v, want %v", tt.tool, tt.args, got, tt.want)
			}
		})
	}
}

// TestCollectorAllowedNeedsState refuses a collector call on a tool
// discovery did not class read and state: content, act or unknown.
func TestCollectorAllowedNeedsState(t *testing.T) {
	ep := collectorEndpoint(v1alpha1.ConnectorCode)
	ep.StateTools = map[string]bool{collectorRepositories: true}
	if ep.Allowed(collectorIndex, map[string]any{kRepo: rDemo}) {
		t.Error("manifests_index admitted though discovery did not class it read and state")
	}
	if !ep.Allowed(collectorRepositories, map[string]any{}) {
		t.Error("repositories_list refused though classed read and state")
	}
}

// TestCollectorEndpointKeepsOtherRolesUnchanged: an endpoint not marked
// Collector keeps the other roles' rules; the collector's shapes do not
// widen them.
func TestCollectorEndpointKeepsOtherRolesUnchanged(t *testing.T) {
	ep := collectorEndpoint(v1alpha1.ConnectorCode)
	ep.Collector = false
	if ep.Allowed(collectorIndex, map[string]any{kRepo: rDemo}) {
		t.Error("manifests_index admitted on an endpoint that is not the collector's")
	}
	rel := collectorEndpoint(v1alpha1.ConnectorReleases)
	rel.Collector = false
	if rel.Allowed(collectorSources, map[string]any{}) {
		t.Error("sources_list admitted on an endpoint that is not the collector's")
	}
	// A collector endpoint never takes the discovery or probe rules: a
	// namespaces_list is not a collector call.
	cl := collectorEndpoint(v1alpha1.ConnectorCluster)
	cl.Addon, cl.StateTools["namespaces_list"] = true, true
	if cl.Allowed("namespaces_list", map[string]any{}) {
		t.Error("a collector endpoint admitted the Intendant's discovery tool")
	}
}
