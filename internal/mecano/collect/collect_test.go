// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package collect

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

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/codeserver"
	"github.com/agaetis-it/takecare-agents/internal/golden"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/mecano/eos"
)

const (
	recorded = "testdata/kms-v0.0.67"
	shaped   = "testdata/shaped"
	demoApps = "demo-apps"
	cluster  = "kubernetes"
	code     = "code"
	watch    = "releases"
	repo     = "demo"

	kName       = "name"
	kKind       = "kind"
	kSource     = "source"
	kComponents = "components"
	kProduct    = "product"
	kRepository = "repository"
	kustom      = "Kustomization"
	tagNext     = "0.3.0"
	deploy      = "Deployment"
	fluxSystem  = "flux-system"
	certMgr     = "cert-manager"
	postgres    = "docker.io/library/postgres"
	eol116      = "2025-06-10"
	eolSource   = "eol"
	kHost       = "host"
	kURL        = "url"
	demoApp     = "ghcr.io/agaetis-it/takecare-agents-demo-app"
)

var now = time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)

// captured is a recorded tools/call result.
type captured struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

func load(t *testing.T, path string) mcp.Result {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // a test's own testdata
	if err != nil {
		t.Fatal(err)
	}
	var a captured
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	r := mcp.Result{IsError: a.IsError, Structured: a.StructuredContent}
	if len(a.Content) > 0 {
		r.Text = a.Content[0].Text
	}
	return r
}

func text(t *testing.T, v any) mcp.Result {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return mcp.Result{Text: string(raw)}
}

func toolError(msg string) mcp.Result { return mcp.Result{IsError: true, Text: msg} }

// fake answers by connector, tool and arguments ("<connector> <tool>
// k=v,k=v", keys sorted); resources_list falls back to an empty list.
// It records every call.
type fake struct {
	answers map[string]mcp.Result
	errs    map[string]error
	calls   []string
	cancel  func()
}

func key(connector, tool string, args map[string]any) string {
	kv := make([]string, 0, len(args))
	for k, v := range args {
		kv = append(kv, fmt.Sprintf("%s=%v", k, v))
	}
	slices.Sort(kv)
	return connector + " " + tool + " " + strings.Join(kv, ",")
}

func (f *fake) Call(_ context.Context, ep mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error) {
	k := key(ep.URL, tool, args)
	f.calls = append(f.calls, k)
	if f.cancel != nil {
		f.cancel()
	}
	if err := f.errs[k]; err != nil {
		return mcp.Result{}, err
	}
	if r, ok := f.answers[k]; ok {
		return r, nil
	}
	if tool == toolList {
		return mcp.Result{}, nil
	}
	return toolError("unexpected call " + k), nil
}

func (f *fake) set(connector, tool string, args map[string]any, r mcp.Result) {
	f.answers[key(connector, tool, args)] = r
}

func list(apiVersion, kind, namespace string) map[string]any {
	a := map[string]any{"apiVersion": apiVersion, kKind: kind}
	if namespace != "" {
		a["namespace"] = namespace
	}
	return a
}

func get(apiVersion, kind, namespace, name string) map[string]any {
	a := list(apiVersion, kind, namespace)
	a["name"] = name
	return a
}

// demoIndex is what the demo repository declares: the billing API one
// tag ahead through its kustomization, the log shipper's tag raised by the
// Flux Kustomization, the export CronJob on a removed apiVersion, a
// worker that does not run, and cert-manager's HelmRelease.
var demoIndex = codeserver.Index{Repository: repo, Revision: "4f1c2a9", Documents: []codeserver.Document{
	{Path: "apps/kustomization.yaml", Kind: kustom,
		ImageOverrides: []codeserver.ImageOverride{{Name: demoApp, NewTag: tagNext}}},
	{Path: "apps/billing/deployment.yaml", APIVersion: apps, Kind: deploy, Namespace: demoApps, Name: "billing-api",
		Images: []string{"envoyproxy/envoy:v1.31.2", "ghcr.io/agaetis-it/takecare-agents-demo-app:0.1.0"}},
	{Path: "apps/ledger/statefulset.yaml", APIVersion: apps, Kind: "StatefulSet", Name: "ledger-db", Images: []string{"postgres:16.4"}},
	{Path: "apps/export/cronjob.yaml", APIVersion: "batch/v1beta1", Kind: "CronJob", Name: "nightly-export", Images: []string{"busybox:1.37"}},
	{Path: "apps/logs/daemonset.yaml", APIVersion: apps, Kind: "DaemonSet", Name: "log-shipper", Images: []string{"cr.fluentbit.io/fluent/fluent-bit:3.1.9"}},
	{Path: "apps/old/deployment.yaml", APIVersion: apps, Kind: deploy, Name: "legacy-worker", Images: []string{"ghcr.io/agaetis-it/legacy:1.0.0"}},
	{Path: "apps/other/deployment.yaml", APIVersion: apps, Kind: deploy, Namespace: "elsewhere", Name: "other", Images: []string{"ghcr.io/agaetis-it/other:1.0.0"}},
	{Path: "clusters/demo/apps.yaml", APIVersion: "kustomize.toolkit.fluxcd.io/v1", Kind: kustom, Namespace: fluxSystem, Name: "apps",
		ImageOverrides: []codeserver.ImageOverride{{Name: "cr.fluentbit.io/fluent/fluent-bit", NewTag: "3.2.0"}}},
	{Path: "infra/cert-manager.yaml", APIVersion: "helm.toolkit.fluxcd.io/v2", Kind: "HelmRelease", Namespace: demoApps, Name: certMgr,
		Charts: []codeserver.ChartRef{{Name: certMgr, Version: "v1.16.2", Repository: "https://charts.jetstack.io/"}}},
}}

// demo is the recorded cluster (demo-apps, argocd, flux-system) and the
// demo repository.
func demo(t *testing.T) (*fake, Input) {
	t.Helper()
	f := &fake{answers: map[string]mcp.Result{}, errs: map[string]error{}}
	for _, k := range workloadKinds {
		f.set(cluster, toolList, list(k[0], k[1], demoApps), load(t, filepath.Join(recorded, "demo-apps-"+k[1]+".json")))
	}
	f.set(cluster, toolList, list("v1", "Node", ""), load(t, filepath.Join(recorded, "Node.json")))
	f.set(cluster, toolList, list(certManager, "Certificate", demoApps), load(t, filepath.Join(recorded, "demo-apps-Certificate.json")))
	f.set(cluster, toolGet, get(certManager, "Certificate", demoApps, "billing-tls"), load(t, filepath.Join(recorded, "demo-apps-Certificate-get-billing-tls.json")))
	f.set(cluster, toolGet, get(certManager, "Certificate", demoApps, "notifications-tls"), load(t, filepath.Join(recorded, "forbidden-ack-system-Deployment.json")))
	f.set(cluster, toolList, list(argoCD, "Application", "argocd"), load(t, filepath.Join(recorded, "argocd-Application.json")))
	f.set(cluster, toolList, list(fluxKustomize, kustom, fluxSystem), load(t, filepath.Join(recorded, "flux-system-Kustomization.json")))
	for _, n := range []string{"apps", "infra"} {
		f.set(cluster, toolGet, get(fluxKustomize, kustom, fluxSystem, n), load(t, filepath.Join(shaped, "flux-system-Kustomization-get-"+n+".json")))
	}
	f.set(code, toolRepositories, map[string]any{}, text(t, []map[string]string{{kName: repo, "revision": "4f1c2a9"}}))
	f.set(code, toolIndex, map[string]any{kRepository: repo}, text(t, demoIndex))
	ds, err := eos.Load()
	if err != nil {
		t.Fatal(err)
	}
	return f, Input{
		Clusters:           []Cluster{{Connector: cluster, Endpoint: mcp.Endpoint{URL: cluster}, Namespaces: []string{"argocd", demoApps, fluxSystem}}},
		Code:               []Source{{Connector: code, Endpoint: mcp.Endpoint{URL: code}}},
		Dataset:            ds,
		CertificateWarning: 30 * 24 * time.Hour,
		Now:                now,
	}
}

func collect(t *testing.T, f *fake, in Input) Result {
	t.Helper()
	res, err := Collect(context.Background(), f, in)
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

// TestCollectDemo is the whole collection on the recorded answers, without
// a releases connector: the offline watch.
func TestCollectDemo(t *testing.T) {
	f, in := demo(t)
	res := collect(t, f, in)
	golden.Assert(t, "demo.golden.json", asJSON(t, res))

	comp := map[string]v1alpha1.InventoryComponent{}
	for _, c := range res.Inventory.Components {
		comp[c.Key] = c
	}
	for k, want := range map[string]v1alpha1.Drift{
		demoApp:                             v1alpha1.DriftVersion, // the kustomization's tag
		"cr.fluentbit.io/fluent/fluent-bit": v1alpha1.DriftVersion, // the Flux Kustomization's tag
		"docker.io/envoyproxy/envoy":        v1alpha1.DriftNone,    // same tag, digest live
		postgres:                            v1alpha1.DriftNone,    // no namespace in Git
		"quay.io/jetstack/cert-manager-controller": v1alpha1.DriftNotInGit, // only its chart is in Git
		"ghcr.io/agaetis-it/legacy":                v1alpha1.DriftNotLive,
		certMgr:                                    v1alpha1.DriftNone, // chart, by version
		keyKubernetes:                              v1alpha1.DriftUnknown,
	} {
		if comp[k].Drift != want {
			t.Errorf("%s: drift %q, want %q", k, comp[k].Drift, want)
		}
	}
	if _, ok := comp["ghcr.io/agaetis-it/other"]; ok {
		t.Error("a manifest of a namespace out of scope is compared")
	}
	if c := comp[demoApp]; c.Live != "0.2.0" || c.Declared != tagNext {
		t.Errorf("demo app: live %q declared %q", c.Live, c.Declared)
	}
	if c := comp["quay.io/jetstack/cert-manager-controller"]; c.Latest != "1.21.3" || c.SupportEnds == nil || c.SupportEnds.Format(time.DateOnly) != eol116 {
		t.Errorf("cert-manager from the dataset: %+v", c)
	}
	if c := comp[keyKubernetes]; c.Live != "1.32.8" || c.Latest != "1.37.2" || c.SupportEnds.Format(time.DateOnly) != "2026-02-28" {
		t.Errorf("kubernetes: %+v", c)
	}
	if res.ClusterVersion != "1.32.8" || res.VersionFrom != "kubelet" {
		t.Errorf("cluster version %q from %q: want the lowest kubelet's", res.ClusterVersion, res.VersionFrom)
	}
	wantAPI := v1alpha1.DeprecatedAPIUse{APIVersion: "batch/v1beta1", Kind: "CronJob", Name: "nightly-export", RemovedIn: "1.25", Replacement: "batch/v1",
		Where: []v1alpha1.APIWhere{v1alpha1.APIGit}}
	if len(res.Inventory.APIs) != 1 || fmt.Sprint(res.Inventory.APIs[0]) != fmt.Sprint(wantAPI) {
		t.Errorf("apis: %+v", res.Inventory.APIs)
	}
	// billing-tls expires on 2026-12-10, within 30 days; notifications-tls
	// is not Ready (its get forbidden: the list's Ready stands).
	if got := res.Inventory.Certificates; len(got) != 2 || got[0].Name != "billing-tls" || !got[0].Ready || got[0].NotAfter == nil || got[1].Ready {
		t.Errorf("certificates: %+v", got)
	}
	wantGitOps := []v1alpha1.GitOpsState{
		{Kind: "Application", Namespace: "argocd", Name: "notifications", State: "OutOfSync", Revision: "9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d"},
		{Kind: kustom, Namespace: fluxSystem, Name: "infra", State: "NotReady", Revision: "main@sha1:9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d"},
	}
	if fmt.Sprint(res.Inventory.GitOps) != fmt.Sprint(wantGitOps) {
		t.Errorf("gitops: %+v", res.Inventory.GitOps)
	}
	for _, want := range []v1alpha1.NotSeen{
		{Ref: watch, Reason: "offline watch, current as of 2026-11-27"},
		{Ref: "charts", Reason: reasonCharts},
	} {
		if !slices.Contains(res.BlindSpots, want) {
			t.Errorf("blind spots %+v: want %+v", res.BlindSpots, want)
		}
	}
	// The cluster's version is 1.32: no apiVersion the dataset lists is
	// removed after it, so none is probed live.
	for _, c := range f.calls {
		if strings.Contains(c, "v1beta") {
			t.Errorf("probed %s", c)
		}
	}
}

func TestClusterVersion(t *testing.T) {
	forbidden := load(t, filepath.Join(recorded, "forbidden-ack-system-Deployment.json"))
	t.Run("kube-apiserver static pod when nodes are not readable", func(t *testing.T) {
		f, in := demo(t)
		f.set(cluster, toolList, list("v1", "Node", ""), forbidden)
		in.Clusters[0].Namespaces = append(in.Clusters[0].Namespaces, "kube-system")
		f.set(cluster, toolList, list("v1", kindPod, "kube-system"), load(t, filepath.Join(shaped, "kube-system-Pod.json")))
		f.set(cluster, toolGet, get("v1", kindPod, "kube-system", "kube-apiserver-cp-0"), load(t, filepath.Join(shaped, "kube-system-Pod-get-kube-apiserver.json")))
		res := collect(t, f, in)
		if res.ClusterVersion != "1.33.4" || res.VersionFrom != "kube-apiserver" {
			t.Errorf("version %q from %q", res.ClusterVersion, res.VersionFrom)
		}
	})
	t.Run("unknown: a blind spot, and every namespaced removed apiVersion probed", func(t *testing.T) {
		f, in := demo(t)
		f.set(cluster, toolList, list("v1", "Node", ""), forbidden)
		notServed := load(t, filepath.Join(recorded, "not-served-CronJob-v1beta1.json"))
		f.set(cluster, toolList, list("batch/v1beta1", "CronJob", "argocd"), notServed)
		f.answers[key(cluster, toolList, list("policy/v1beta1", "PodDisruptionBudget", demoApps))] = mcp.Result{
			Structured: json.RawMessage(`{"items":[{"Name":"billing","Namespace":"demo-apps","apiVersion":"policy/v1beta1","kind":"PodDisruptionBudget","Labels":"<none>"}]}`)}
		res := collect(t, f, in)
		if res.ClusterVersion != "" || !slices.Contains(res.BlindSpots, v1alpha1.NotSeen{Ref: "cluster version", Reason: reasonNoVersion}) {
			t.Errorf("version %q, blind spots %+v", res.ClusterVersion, res.BlindSpots)
		}
		probes := 0
		for _, c := range f.calls {
			if strings.Contains(c, "batch/v1beta1") && strings.Contains(c, "CronJob") {
				probes++
			}
		}
		if probes != 1 {
			t.Errorf("batch/v1beta1 CronJob probed %d times: a not-served API is asked once per cluster", probes)
		}
		found := false
		for _, a := range res.Inventory.APIs {
			if a.Kind == "PodDisruptionBudget" && a.Name == "billing" && slices.Equal(a.Where, []v1alpha1.APIWhere{v1alpha1.APILive}) {
				found = true
			}
		}
		if !found {
			t.Errorf("live PodDisruptionBudget on policy/v1beta1 not recorded: %+v", res.Inventory.APIs)
		}
	})
}

// TestReleases maps components to the version watch's sources and falls
// back to the dataset per component.
func TestReleases(t *testing.T) {
	sources := []map[string]any{
		{kName: "quay", kKind: "oci", kHost: "quay.io", kURL: "https://quay.io", kComponents: []any{}},
		{kName: "hub", kKind: "oci", kHost: "registry-1.docker.io", kURL: "https://registry-1.docker.io", kComponents: []any{}},
		{kName: "jetstack", kKind: "helm", kHost: "charts.jetstack.io", kURL: "https://charts.jetstack.io", kComponents: []any{}},
		{kName: eolSource, kKind: "endoflife", kHost: "endoflife.date", kURL: "https://endoflife.date/api/v1",
			kComponents: []any{map[string]string{"match": "quay.io/jetstack/cert-manager-controller", kName: certMgr}}},
	}
	versions := func(src, name string, v ...string) mcp.Result {
		vs := make([]map[string]any, 0, len(v))
		for _, x := range v {
			vs = append(vs, map[string]any{"version": x, "prerelease": strings.Contains(x, "-")})
		}
		return text(t, map[string]any{kSource: src, kName: name, "fetched": "2026-11-28T08:00:00Z", "stale": false, "versions": vs})
	}
	f, in := demo(t)
	in.Releases = []Source{{Connector: watch, Endpoint: mcp.Endpoint{URL: watch}}}
	f.set(watch, toolSources, map[string]any{}, text(t, sources))
	f.set(watch, toolVersions, map[string]any{kSource: "quay", kName: "jetstack/cert-manager-controller"}, versions("quay", "jetstack/cert-manager-controller", "v1.22.0-alpha.1", "v1.21.4"))
	f.set(watch, toolCycles, map[string]any{kSource: eolSource, kProduct: certMgr}, text(t, map[string]any{
		kSource: eolSource, kProduct: certMgr, "fetched": "2026-11-28T08:00:00Z", "stale": true,
		"cycles": []map[string]any{{kName: "1.21", "latest": "1.21.4", "maintained": true}, {kName: "1.16", "latest": "1.16.5", eolSource: true, "eolFrom": eol116}},
	}))
	f.set(watch, toolVersions, map[string]any{kSource: "hub", kName: "library/postgres"}, versions("hub", "library/postgres", "17.2.0", "16.6.0"))
	f.set(watch, toolVersions, map[string]any{kSource: "jetstack", kName: certMgr}, toolError("source jetstack, cert-manager: answer dropped: stale past 7 days"))
	res := collect(t, f, in)
	comp := map[string]v1alpha1.InventoryComponent{}
	for _, c := range res.Inventory.Components {
		comp[c.Key] = c
	}
	if c := comp["quay.io/jetstack/cert-manager-controller"]; c.Latest != "v1.21.4" || c.SupportEnds.Format(time.DateOnly) != eol116 {
		t.Errorf("cert-manager from its sources: latest %q, ends %v (the pre-release skipped)", c.Latest, c.SupportEnds)
	}
	if c := comp[postgres]; c.Latest != "17.2.0" {
		t.Errorf("postgres from Docker Hub: %q", c.Latest)
	}
	if c := comp[certMgr]; c.Latest != "1.21.3" {
		t.Errorf("the chart keeps the dataset's latest when its source fails: %q", c.Latest)
	}
	if !slices.Contains(res.BlindSpots, v1alpha1.NotSeen{Ref: "releases/jetstack", Reason: fmt.Sprintf(reasonStaleSource, certMgr)}) {
		t.Errorf("no blind spot for the failed source: %+v", res.BlindSpots)
	}
	if slices.ContainsFunc(res.BlindSpots, func(n v1alpha1.NotSeen) bool { return n.Ref == watch }) {
		t.Error("offline watch declared with a releases connector that answers")
	}
	// A source never gets a name it does not map: the GitHub image has no
	// ghcr.io source.
	for _, c := range f.calls {
		if strings.Contains(c, "ghcr.io") || strings.Contains(c, "agaetis-it") {
			t.Errorf("asked %s", c)
		}
	}

	t.Run("a releases connector that does not answer is the offline watch", func(t *testing.T) {
		f, in := demo(t)
		in.Releases = []Source{{Connector: watch, Endpoint: mcp.Endpoint{URL: watch}}}
		f.errs[key(watch, toolSources, map[string]any{})] = errors.New("connection refused")
		res := collect(t, f, in)
		for _, want := range []v1alpha1.NotSeen{{Ref: watch, Reason: reasonCallFailed}, {Ref: watch, Reason: "offline watch, current as of 2026-11-27"}} {
			if !slices.Contains(res.BlindSpots, want) {
				t.Errorf("blind spots %+v: want %+v", res.BlindSpots, want)
			}
		}
	})
}

func TestBlindSpots(t *testing.T) {
	forbidden := load(t, filepath.Join(recorded, "forbidden-ack-system-Deployment.json"))
	t.Run("no code connector: drift unknown", func(t *testing.T) {
		f, in := demo(t)
		f.errs[key(code, toolRepositories, map[string]any{})] = mcp.ErrCallForbidden
		res := collect(t, f, in)
		for _, c := range res.Inventory.Components {
			if c.Drift != v1alpha1.DriftUnknown {
				t.Errorf("%s: drift %q without Git", c.Key, c.Drift)
			}
		}
		for _, want := range []v1alpha1.NotSeen{{Ref: code, Reason: reasonNotAllowed}, {Ref: "code", Reason: reasonNoCode}} {
			if !slices.Contains(res.BlindSpots, want) {
				t.Errorf("blind spots %+v: want %+v", res.BlindSpots, want)
			}
		}
	})
	t.Run("a forbidden listing, an unknown shape, a stale dataset", func(t *testing.T) {
		f, in := demo(t)
		f.set(cluster, toolList, list(apps, "StatefulSet", demoApps), forbidden)
		f.set(cluster, toolList, list(apps, "DaemonSet", demoApps), mcp.Result{Text: "NAME\nx\n"})
		f.set(code, toolIndex, map[string]any{kRepository: repo}, mcp.Result{Text: `{"repository":"another"}`})
		in.Now = now.AddDate(1, 0, 0)
		res := collect(t, f, in)
		for _, want := range []v1alpha1.NotSeen{
			{Ref: cluster + ":demo-apps/StatefulSet", Reason: reasonForbidden},
			{Ref: cluster + ":demo-apps/DaemonSet", Reason: "unrecognized resources_list output"},
			{Ref: code + ":" + repo, Reason: "unrecognized manifests_index output"},
			{Ref: "end-of-support dataset", Reason: "the end-of-support dataset is from 2026-11-27"},
		} {
			if !slices.Contains(res.BlindSpots, want) {
				t.Errorf("blind spots %+v: want %+v", res.BlindSpots, want)
			}
		}
	})
	t.Run("the context ends: an error, no partial inventory", func(t *testing.T) {
		f, in := demo(t)
		ctx, cancel := context.WithCancel(context.Background())
		f.cancel = cancel
		if _, err := Collect(ctx, f, in); !errors.Is(err, context.Canceled) {
			t.Errorf("err %v", err)
		}
	})
}

func TestCopyAndNarrowing(t *testing.T) {
	f, in := demo(t)
	f.set("copy", toolList, list(apps, deploy, "demo-copy"), mcp.Result{Structured: json.RawMessage(`{"items":[
		{"Name":"billing-api","Namespace":"demo-copy","apiVersion":"apps/v1","kind":"Deployment","Ready":"1/1","Images":"ghcr.io/agaetis-it/takecare-agents-demo-app:0.3.0","Labels":"<none>"},
		{"Name":"worker","Namespace":"demo-copy","apiVersion":"apps/v1","kind":"Deployment","Ready":"0/1","Images":"postgres:17.2","Labels":"<none>"}]}`)})
	in.Copy = &Cluster{Connector: "copy", Endpoint: mcp.Endpoint{URL: "copy"}, Namespaces: []string{"demo-copy"}}
	in.Components = []string{postgres}
	res := collect(t, f, in)
	want := []CopyComponent{
		{Key: postgres, Version: "17.2", Evidence: []v1alpha1.Evidence{{Tool: toolList, Record: "copy:demo-copy/Deployment/worker"}}},
		{Key: demoApp, Version: tagNext, Ready: true,
			Evidence: []v1alpha1.Evidence{{Tool: toolList, Record: "copy:demo-copy/Deployment/billing-api"}}},
	}
	if fmt.Sprint(res.Copy) != fmt.Sprint(want) {
		t.Errorf("copy: %+v", res.Copy)
	}
	if len(res.Inventory.Components) != 1 || res.Inventory.Components[0].Key != postgres {
		t.Errorf("narrowed components: %+v", res.Inventory.Components)
	}
}

func TestBounds(t *testing.T) {
	f, in := demo(t)
	items := make([]string, 0, maxComponents+5)
	for i := range maxComponents + 5 {
		items = append(items, fmt.Sprintf(`{"Name":"w%d","Namespace":"demo-apps","apiVersion":"apps/v1","kind":"Deployment","Images":"ghcr.io/x/c%03d:1.0.0","Labels":"<none>"}`, i, i))
	}
	f.set(cluster, toolList, list(apps, deploy, demoApps), mcp.Result{Structured: json.RawMessage(`{"items":[` + strings.Join(items, ",") + `]}`)})
	res := collect(t, f, in)
	if len(res.Inventory.Components) != maxComponents || res.Inventory.Omitted.Components == 0 {
		t.Errorf("%d components, %d omitted", len(res.Inventory.Components), res.Inventory.Omitted.Components)
	}
}

func TestParsing(t *testing.T) {
	for ref, want := range map[string]image{
		"nginx":                              {Repository: "docker.io/library/nginx"},
		"nginx:1.27":                         {Repository: "docker.io/library/nginx", Tag: "1.27"},
		"envoyproxy/envoy:v1.31.2@sha256:ab": {Repository: "docker.io/envoyproxy/envoy", Tag: "v1.31.2", Digest: "sha256:ab"},
		"localhost:5000/app:1":               {Repository: "localhost:5000/app", Tag: "1"},
		"ghcr.io/a/b@sha256:cd":              {Repository: "ghcr.io/a/b", Digest: "sha256:cd"},
	} {
		if got := parseImage(ref); got != want {
			t.Errorf("parseImage(%q) = %+v, want %+v", ref, got, want)
		}
	}
	for label, want := range map[string][2]string{
		"cert-manager-v1.16.2":         {certMgr, "v1.16.2"},
		"kube-prometheus-stack-65.1.0": {"kube-prometheus-stack", "65.1.0"},
		"weird":                        {"weird", ""},
	} {
		if n, v := splitChart(label); n != want[0] || v != want[1] {
			t.Errorf("splitChart(%q) = %q %q", label, n, v)
		}
	}
	if !same("v1.2.3", "1.2.3") || same("1.2.3", "1.2.4") || same("latest", "1.2.3") {
		t.Error("same")
	}
	if got := lowest([]occurrence{{version: "1.10.0"}, {version: "1.9.2"}, {version: ""}}); got != "1.9.2" {
		t.Errorf("lowest %q", got)
	}
}
