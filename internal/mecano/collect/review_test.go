// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package collect

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/codeserver"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/mecano/eos"
)

const (
	cronJob      = "CronJob"
	nInfra       = "infra"
	nWeb         = "web"
	tag020       = "0.2.0"
	pg172        = "17.2.0"
	hub          = "hub"
	libPostgres  = "library/postgres"
	pgRepo       = "postgres/postgres"
	kStale       = "stale"
	kVersions    = "versions"
	cmController = "quay.io/jetstack/cert-manager-controller"
	kindOCI      = "oci"
	kNameCol     = "Name"
	oldCluster   = "old"
	blindCluster = "blind"
	kindPod      = "Pod"
	batchBeta    = "batch/v1beta1"
	app          = "ghcr.io/agaetis-it/app"
	argoImage    = "quay.io/argoproj/argocd"
	kNamespace   = "Namespace"
	kImages      = "Images"
	kLabels      = "Labels"
	kReady       = "Ready"
	kStatus      = "Status"
	kVersion     = "Version"
	older        = "older than the dataset"
	unrecList    = "unrecognized resources_list output"
	unrecGet     = "unrecognized resources_get output"
	node124      = "v1.24.17"
)

// rows is a resources_list answer: one map per row, apiVersion, kind and
// namespace added.
func rows(t *testing.T, apiVersion, kind, namespace string, rs ...map[string]any) mcp.Result {
	t.Helper()
	items := make([]map[string]any, 0, len(rs))
	for _, r := range rs {
		m := map[string]any{"apiVersion": apiVersion, kKind: kind}
		if namespace != "" {
			m[kNamespace] = namespace
		}
		maps.Copy(m, r)
		items = append(items, m)
	}
	raw, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}
	return mcp.Result{Structured: raw}
}

func nodes(t *testing.T, versions ...string) mcp.Result {
	t.Helper()
	rs := make([]map[string]any, 0, len(versions))
	for i, v := range versions {
		rs = append(rs, map[string]any{kNameCol: fmt.Sprintf("node-%d", i), kVersion: v})
	}
	return rows(t, "v1", "Node", "", rs...)
}

func deployment(name, images, labels string) map[string]any {
	return map[string]any{kNameCol: name, kImages: images, kLabels: labels, kReady: "1/1"}
}

// bare is one cluster, demo-apps only, nodes at version, no code and no
// releases connector.
func bare(t *testing.T, version string) (*fake, Input) {
	t.Helper()
	f := &fake{answers: map[string]mcp.Result{}, errs: map[string]error{}}
	f.set(cluster, toolList, list("v1", "Node", ""), nodes(t, version))
	ds, err := eos.Load()
	if err != nil {
		t.Fatal(err)
	}
	return f, Input{
		Clusters:           []Cluster{{Connector: cluster, Endpoint: mcp.Endpoint{URL: cluster}, Namespaces: []string{demoApps}}},
		Dataset:            ds,
		CertificateWarning: 30 * 24 * time.Hour,
		Now:                now,
	}
}

func components(res Result) map[string]v1alpha1.InventoryComponent {
	out := map[string]v1alpha1.InventoryComponent{}
	for _, c := range res.Inventory.Components {
		out[c.Key] = c
	}
	return out
}

func hasBlindSpot(res Result, ref, reasonPart string) bool {
	return slices.ContainsFunc(res.BlindSpots, func(n v1alpha1.NotSeen) bool {
		return n.Ref == ref && strings.Contains(n.Reason, reasonPart)
	})
}

// TestFourReadings holds the collector to the readings the product owner
// accepted: resources_get on Certificates only, Flux from its list, the
// cluster's version from the nodes only.
func TestFourReadings(t *testing.T) {
	t.Run("resources_get on cert-manager Certificates only", func(t *testing.T) {
		f, in := demo(t)
		collect(t, f, in)
		for _, c := range f.calls {
			if strings.Contains(c, " "+toolGet+" ") && !strings.Contains(c, "kind="+kindCertificate+",") {
				t.Errorf("got something else than a Certificate: %s", c)
			}
		}
	})
	t.Run("Flux from the list: Ready, the applied revision, a blind spot", func(t *testing.T) {
		f, in := demo(t)
		f.set(cluster, toolList, list(fluxKustomize, kustom, fluxSystem), rows(t, fluxKustomize, kustom, fluxSystem,
			map[string]any{kNameCol: "apps", kReady: conditionTrue, kStatus: "Applied revision: main@sha1:4f1c2a9"},
			map[string]any{kNameCol: "infra", kReady: "False", kStatus: "kustomize build failed: accumulating resources"},
			map[string]any{kNameCol: nWeb, kReady: "False", kStatus: "Applied revision: main@sha1:abc1234"}))
		res := collect(t, f, in)
		var flux []v1alpha1.GitOpsState
		for _, g := range res.Inventory.GitOps {
			if g.Kind == kustom {
				flux = append(flux, g)
			}
		}
		want := []v1alpha1.GitOpsState{
			{Kind: kustom, Namespace: fluxSystem, Name: nInfra, State: stateNotReady},
			{Kind: kustom, Namespace: fluxSystem, Name: nWeb, State: stateNotReady, Revision: "main@sha1:abc1234"},
		}
		if fmt.Sprint(flux) != fmt.Sprint(want) {
			t.Errorf("flux: %+v", flux)
		}
		if !hasBlindSpot(res, cluster+":"+fluxSystem+"/"+kustom, "not compared") {
			t.Errorf("no blind spot saying the revisions are not compared: %+v", res.BlindSpots)
		}
	})
	t.Run("the version from the nodes only, never a kube-apiserver pod", func(t *testing.T) {
		f, in := demo(t)
		f.set(cluster, toolList, list("v1", "Node", ""), load(t, filepath.Join(recorded, "forbidden-ack-system-Deployment.json")))
		in.Clusters[0].Namespaces = append(in.Clusters[0].Namespaces, "kube-system")
		f.set(cluster, toolList, list("v1", kindPod, "kube-system"), rows(t, "v1", kindPod, "kube-system", map[string]any{kNameCol: "kube-apiserver-cp-0"}))
		res := collect(t, f, in)
		for _, c := range f.calls {
			if strings.Contains(c, "kind="+kindPod+",") {
				t.Errorf("read a Pod: %s", c)
			}
		}
		if res.ClusterVersion != "" || !hasBlindSpot(res, cluster+":Node", "nodes") {
			t.Errorf("version %q, blind spots %+v", res.ClusterVersion, res.BlindSpots)
		}
	})
}

// TestRemovedAPIGate probes a removed apiVersion only on a cluster old
// enough, and records its objects live only when the replacement is not
// served: a served replacement lists the same objects through both.
func TestRemovedAPIGate(t *testing.T) {
	old := rows(t, batchBeta, cronJob, demoApps, map[string]any{kNameCol: "nightly"})
	notServed := load(t, filepath.Join(recorded, "not-served-CronJob-v1beta1.json"))
	live := func(res Result) bool {
		return slices.ContainsFunc(res.Inventory.APIs, func(a v1alpha1.DeprecatedAPIUse) bool {
			return a.APIVersion == batchBeta && a.Name == "nightly" && slices.Contains(a.Where, v1alpha1.APILive)
		})
	}
	t.Run("replacement served: not a live use", func(t *testing.T) {
		f, in := bare(t, node124)
		f.set(cluster, toolList, list(batchBeta, cronJob, demoApps), old)
		res := collect(t, f, in)
		if live(res) {
			t.Errorf("a served replacement still lists the object on the old apiVersion: %+v", res.Inventory.APIs)
		}
	})
	t.Run("replacement not served: a live use", func(t *testing.T) {
		f, in := bare(t, node124)
		f.set(cluster, toolList, list(batchBeta, cronJob, demoApps), old)
		f.set(cluster, toolList, list("batch/v1", cronJob, demoApps), notServed)
		res := collect(t, f, in)
		if !live(res) {
			t.Errorf("live use not recorded: %+v", res.Inventory.APIs)
		}
		if !slices.ContainsFunc(f.calls, func(c string) bool { return strings.Contains(c, batchBeta) }) {
			t.Error("a 1.24 cluster is not probed for an apiVersion removed in 1.25")
		}
	})
}

// TestClusters reads each cluster's version and decides its probes on it.
func TestClusters(t *testing.T) {
	f, in := bare(t, "v1.32.8")
	in.Clusters = append(in.Clusters,
		Cluster{Connector: oldCluster, Endpoint: mcp.Endpoint{URL: oldCluster}, Namespaces: []string{demoApps}},
		Cluster{Connector: blindCluster, Endpoint: mcp.Endpoint{URL: blindCluster}, Namespaces: []string{demoApps}})
	f.set(oldCluster, toolList, list("v1", "Node", ""), nodes(t, node124))
	f.set(blindCluster, toolList, list("v1", "Node", ""), load(t, filepath.Join(recorded, "forbidden-ack-system-Deployment.json")))
	for _, cl := range []string{oldCluster, blindCluster} {
		f.set(cl, toolList, list("batch/v1", cronJob, demoApps), load(t, filepath.Join(recorded, "not-served-CronJob-v1beta1.json")))
	}
	res := collect(t, f, in)
	probed := map[string]bool{}
	for _, c := range f.calls {
		if strings.Contains(c, batchBeta) {
			probed[strings.Fields(c)[0]] = true
		}
	}
	if probed[cluster] || !probed[oldCluster] || !probed[blindCluster] {
		t.Errorf("probed %v: want old (1.24) and blind (unknown), not %s (1.32)", probed, cluster)
	}
	k := components(res)[keyKubernetes]
	if k.Live != "1.24.17" || len(k.Evidence) == 0 || !strings.HasPrefix(k.Evidence[0].Record, "old:") {
		t.Errorf("kubernetes: %+v, want the lowest cluster's version and its evidence", k)
	}
	if !hasBlindSpot(res, "blind:Node", "nodes") || hasBlindSpot(res, cluster+":Node", "") {
		t.Errorf("blind spots %+v: want one for the cluster whose nodes are not readable", res.BlindSpots)
	}
}

// TestOverlays: an image declared by several overlays matches the live
// object when any of them agrees.
func TestOverlays(t *testing.T) {
	index := func(liveTag string) (*fake, Input) {
		f, in := bare(t, "v1.33.4")
		in.Code = []Source{{Connector: code, Endpoint: mcp.Endpoint{URL: code}}}
		f.set(cluster, toolList, list(apps, deploy, demoApps), rows(t, apps, deploy, demoApps, deployment(nWeb, app+":"+liveTag, "<none>")))
		f.set(code, toolRepositories, map[string]any{}, text(t, []map[string]string{{kName: repo}}))
		doc := func(p string) codeserver.Document {
			return codeserver.Document{Path: p, APIVersion: apps, Kind: deploy, Namespace: demoApps, Name: nWeb, Images: []string{app + ":0.1.0"}}
		}
		f.set(code, toolIndex, map[string]any{kRepository: repo}, text(t, codeserver.Index{Repository: repo, Documents: []codeserver.Document{
			doc("base/deployment.yaml"),
			{Path: "overlays/staging/kustomization.yaml", Kind: kustom, ImageOverrides: []codeserver.ImageOverride{{Name: app, NewTag: tagNext}}},
			doc("overlays/staging/deployment.yaml"),
			{Path: "overlays/prod/kustomization.yaml", Kind: kustom, ImageOverrides: []codeserver.ImageOverride{{Name: app, NewTag: tag020}}},
			doc("overlays/prod/deployment.yaml"),
		}}))
		return f, in
	}
	t.Run("prod runs prod's tag: no drift", func(t *testing.T) {
		f, in := index(tag020)
		c := components(collect(t, f, in))[app]
		if c.Drift != v1alpha1.DriftNone || c.Declared != tag020 {
			t.Errorf("drift %q declared %q", c.Drift, c.Declared)
		}
	})
	t.Run("no overlay agrees and they differ: a blind spot, not a version drift", func(t *testing.T) {
		f, in := index("0.4.0")
		res := collect(t, f, in)
		if c := components(res)[app]; c.Drift == v1alpha1.DriftVersion {
			t.Errorf("drift %q", c.Drift)
		}
		if !hasBlindSpot(res, app, "differently") {
			t.Errorf("blind spots %+v", res.BlindSpots)
		}
	})
}

// TestKeys keeps image repositories, chart names and part-of products
// apart.
func TestKeys(t *testing.T) {
	f, in := bare(t, "v1.33.4")
	f.set(cluster, toolList, list(apps, deploy, demoApps), rows(t, apps, deploy, demoApps,
		deployment("argocd-server", argoImage+":v3.2.0", "helm.sh/chart=argo-cd-8.1.2"),
		deployment("flux", "ghcr.io/fluxcd/source-controller:v1.7.0", "helm.sh/chart=flux-1.0.0,app.kubernetes.io/part-of=flux,app.kubernetes.io/version=v2.7.0")))
	res := collect(t, f, in)
	comp := components(res)
	if c := comp["argo-cd"]; c.Latest != "" || c.SupportEnds != nil {
		t.Errorf("the argo-cd chart got the product's facts: %+v", c)
	}
	if c := comp[argoImage]; c.Latest != "3.6.3" {
		t.Errorf("the argocd image: %+v", c)
	}
	if c := comp["flux"]; c.Live != "v2.7.0" || c.SupportEnds == nil || c.SupportEnds.Format(time.DateOnly) != "2026-10-26" {
		t.Errorf("flux, from its part-of label: %+v", c)
	}
	if !hasBlindSpot(res, "flux", "chart") {
		t.Errorf("no blind spot for the chart sharing the product's name: %+v", res.BlindSpots)
	}
}

func TestArgoSyncColumn(t *testing.T) {
	f, in := demo(t)
	f.set(cluster, toolList, list(argoCD, "Application", "argocd"), rows(t, argoCD, "Application", "argocd", map[string]any{kNameCol: "billing"}))
	res := collect(t, f, in)
	if slices.ContainsFunc(res.Inventory.GitOps, func(g v1alpha1.GitOpsState) bool { return g.Kind == "Application" }) {
		t.Errorf("an Application without a Sync Status column is reported: %+v", res.Inventory.GitOps)
	}
	if !hasBlindSpot(res, cluster+":argocd/Application", unrecList) {
		t.Errorf("blind spots %+v", res.BlindSpots)
	}
}

func TestNoDataset(t *testing.T) {
	f, in := demo(t)
	in.Dataset = nil
	var res Result
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic: %v", r)
			}
		}()
		res = collect(t, f, in)
	}()
	if c := components(res)[keyKubernetes]; c.Latest == "" {
		t.Errorf("the embedded dataset is not used: %+v", c)
	}
}

func TestOlderThanDataset(t *testing.T) {
	f, in := bare(t, "v1.27.3")
	f.set(cluster, toolList, list(apps, deploy, demoApps), rows(t, apps, deploy, demoApps,
		deployment("cert-manager", "quay.io/jetstack/cert-manager-controller:v1.12.0", "<none>")))
	res := collect(t, f, in)
	for _, ref := range []string{keyKubernetes, cmController} {
		if !hasBlindSpot(res, ref, older) {
			t.Errorf("%s: no blind spot %q in %+v", ref, older, res.BlindSpots)
		}
	}
}

func TestReleaseAnswers(t *testing.T) {
	f, in := demo(t)
	in.Releases = []Source{{Connector: watch, Endpoint: mcp.Endpoint{URL: watch}}}
	f.set(watch, toolSources, map[string]any{}, text(t, []map[string]any{
		{kName: "pg", kKind: "github", kHost: "github.com", kURL: "https://github.com",
			kComponents: []any{map[string]string{"match": postgres, kName: pgRepo}}},
		{kName: hub, kKind: kindOCI, kHost: "registry-1.docker.io", kURL: "https://registry-1.docker.io", kComponents: []any{}},
	}))
	f.set(watch, toolVersions, map[string]any{kSource: "pg", kName: pgRepo},
		text(t, map[string]any{kSource: "pg", kName: pgRepo, kStale: false, kVersions: []any{}}))
	f.set(watch, toolVersions, map[string]any{kSource: hub, kName: libPostgres},
		text(t, map[string]any{kSource: hub, kName: libPostgres, kStale: true, kVersions: []any{map[string]any{"version": pg172}}}))
	in.Components = []string{postgres}
	res := collect(t, f, in)
	if c := components(res)[postgres]; c.Latest != pg172 {
		t.Errorf("an empty answer stopped the other sources: %+v", c)
	}
	if !hasBlindSpot(res, watch+"/pg", "no version") || !hasBlindSpot(res, watch+"/hub", "last refresh failed") {
		t.Errorf("blind spots %+v", res.BlindSpots)
	}
	for _, c := range f.calls {
		if strings.Contains(c, toolVersions) && !strings.Contains(c, "postgres") {
			t.Errorf("looked up a component not kept: %s", c)
		}
	}
}

func certRows(t *testing.T, n int, ready string) mcp.Result {
	t.Helper()
	rs := make([]map[string]any, 0, n)
	for i := range n {
		rs = append(rs, map[string]any{kNameCol: fmt.Sprintf("c%03d", i), kReady: ready})
	}
	return rows(t, certManager, kindCertificate, demoApps, rs...)
}

func TestCertificates(t *testing.T) {
	t.Run("at most 100 gets", func(t *testing.T) {
		f, in := bare(t, "v1.33.4")
		f.set(cluster, toolList, list(certManager, kindCertificate, demoApps), certRows(t, maxCertificates+5, conditionTrue))
		res := collect(t, f, in)
		gets := 0
		for _, c := range f.calls {
			if strings.Contains(c, " "+toolGet+" ") {
				gets++
			}
		}
		if gets != maxCertificates || !hasBlindSpot(res, cluster, "100") {
			t.Errorf("%d gets, blind spots %+v", gets, res.BlindSpots)
		}
	})
	t.Run("a failed get of a Ready kindCertificate is a blind spot", func(t *testing.T) {
		f, in := bare(t, "v1.33.4")
		f.set(cluster, toolList, list(certManager, kindCertificate, demoApps), certRows(t, 1, conditionTrue))
		f.set(cluster, toolGet, getCert("c000"), load(t, filepath.Join(recorded, "forbidden-ack-system-Deployment.json")))
		res := collect(t, f, in)
		if !hasBlindSpot(res, cluster+":"+demoApps+"/Certificate/c000", "forbidden") || len(res.Inventory.Certificates) != 0 {
			t.Errorf("certificates %+v, blind spots %+v", res.Inventory.Certificates, res.BlindSpots)
		}
	})
	t.Run("a get answering another object is not read", func(t *testing.T) {
		f, in := bare(t, "v1.33.4")
		f.set(cluster, toolList, list(certManager, kindCertificate, demoApps), certRows(t, 1, conditionTrue))
		f.set(cluster, toolGet, getCert("c000"), mcp.Result{Structured: json.RawMessage(
			`{"apiVersion":"cert-manager.io/v1","kind":"Certificate","metadata":{"name":"other","namespace":"demo-apps"},"status":{"notAfter":"2026-12-01T00:00:00Z","conditions":[{"type":"Ready","status":"True"}]}}`)})
		res := collect(t, f, in)
		if !hasBlindSpot(res, cluster+":"+demoApps+"/Certificate/c000", unrecGet) || len(res.Inventory.Certificates) != 0 {
			t.Errorf("certificates %+v, blind spots %+v", res.Inventory.Certificates, res.BlindSpots)
		}
	})
}

// TestChartNamedKubernetes: a chart named kubernetes would share the
// cluster's key; the cluster keeps it, with its version, and a blind spot
// says the chart is not compared.
func TestChartNamedKubernetes(t *testing.T) {
	f, in := bare(t, "v1.33.4")
	f.set(cluster, toolList, list(apps, deploy, demoApps), rows(t, apps, deploy, demoApps,
		deployment("kube-tools", "ghcr.io/example/kube-tools:1.0.0", "helm.sh/chart=kubernetes-9.9.9")))
	res := collect(t, f, in)
	n := 0
	for _, c := range res.Inventory.Components {
		if c.Key == keyKubernetes {
			n++
			if c.Live != "1.33.4" {
				t.Errorf("the kubernetes component is the chart's: %+v", c)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d components keyed kubernetes, want the cluster's alone", n)
	}
	if !hasBlindSpot(res, keyKubernetes, "shares the cluster's key") {
		t.Errorf("no blind spot for the chart named kubernetes: %+v", res.BlindSpots)
	}
}

// TestBlindSpotsSorted: the blind spots come sorted by reference, then
// reason, whatever the order the collector met them in, so that two
// maintenances of the same ground write the same status.
func TestBlindSpotsSorted(t *testing.T) {
	f, in := demo(t)
	f.errs[key(cluster, toolList, list(certManager, kindCertificate, demoApps))] = errors.New("down")
	in.Releases = []Source{{Connector: watch, Endpoint: mcp.Endpoint{URL: watch}}}
	res := collect(t, f, in)
	if len(res.BlindSpots) < 3 {
		t.Fatalf("want several blind spots, got %+v", res.BlindSpots)
	}
	if !slices.IsSortedFunc(res.BlindSpots, func(a, b v1alpha1.NotSeen) int {
		return cmp.Or(strings.Compare(a.Ref, b.Ref), strings.Compare(a.Reason, b.Reason))
	}) {
		t.Errorf("blind spots not sorted: %+v", res.BlindSpots)
	}
}
