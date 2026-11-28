// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/mecano/eos"
)

// workloadKinds are the kinds whose pod templates name what runs.
var workloadKinds = [][2]string{
	{apps, "Deployment"}, {apps, "StatefulSet"}, {apps, "DaemonSet"}, {"batch/v1", "CronJob"},
}

const apps = "apps/v1"

// Labels the collector keeps; every other label is dropped as the answer
// is parsed.
const (
	labelChart   = "helm.sh/chart"
	labelPartOf  = "app.kubernetes.io/part-of"
	labelVersion = "app.kubernetes.io/version"
)

const (
	certManager     = "cert-manager.io/v1"
	argoCD          = "argoproj.io/v1alpha1"
	fluxKustomize   = "kustomize.toolkit.fluxcd.io/v1"
	kindCertificate = "Certificate"
	conditionTrue   = "True"
	stateNotReady   = "NotReady"
)

var errUnrecognized = errors.New("unrecognized output")

// workload is a live object running containers, and what is kept of it.
type workload struct {
	connector, Namespace, Kind, Name string
	Images                           []image
	// Chart and ChartVersion come from helm.sh/chart; PartOf and Version
	// from app.kubernetes.io/part-of and app.kubernetes.io/version.
	Chart, ChartVersion, PartOf, Version string
	Ready                                bool
}

func (w workload) ref() string { return w.connector + ":" + w.Namespace + "/" + w.Kind + "/" + w.Name }

// item is one item of resources_list's structuredContent, with the keys
// the collector reads: every other column is not decoded.
type item struct {
	APIVersion *string         `json:"apiVersion"`
	Kind       *string         `json:"kind"`
	Name       *string         `json:"Name"`
	Namespace  *string         `json:"Namespace"`
	Images     *string         `json:"Images"`
	Labels     *string         `json:"Labels"`
	Ready      json.RawMessage `json:"Ready"`
	Desired    json.RawMessage `json:"Desired"`
	Sync       *string         `json:"Sync Status"`
	Status     *string         `json:"Status"`
	Revision   string          `json:"Revision"`
	Version    string          `json:"Version"`
}

// list runs resources_list and checks every item is of the kind and
// namespace asked ("" for a cluster-scoped kind).
func (c *collector) list(ctx context.Context, s Source, apiVersion, kind, namespace string) ([]item, string, error) {
	args := map[string]any{argAPIVersion: apiVersion, argKind: kind}
	if namespace != "" {
		args[argNamespace] = namespace
	}
	res, why, err := c.call(ctx, s, toolList, args)
	if err != nil || why != "" {
		return nil, why, err
	}
	items, err := parseItems(res, apiVersion, kind, namespace)
	if err != nil {
		return nil, fmt.Sprintf(reasonUnrecognized, toolList), nil
	}
	return items, "", nil
}

func parseItems(res mcp.Result, apiVersion, kind, namespace string) ([]item, error) {
	if len(res.Structured) == 0 {
		if strings.TrimSpace(res.Text) == "" {
			return nil, nil
		}
		return nil, errUnrecognized
	}
	var content struct {
		Items *[]item `json:"items"`
	}
	if err := json.Unmarshal(res.Structured, &content); err != nil || content.Items == nil {
		return nil, errUnrecognized
	}
	for _, it := range *content.Items {
		ns := ""
		if it.Namespace != nil {
			ns = *it.Namespace
		}
		if it.APIVersion == nil || it.Kind == nil || it.Name == nil || *it.Name == "" ||
			*it.APIVersion != apiVersion || *it.Kind != kind || ns != namespace {
			return nil, errUnrecognized
		}
	}
	return *content.Items, nil
}

// workloads lists the workload kinds of a cluster's namespaces.
func (c *collector) workloads(ctx context.Context, cl Cluster, into []workload) ([]workload, error) {
	for _, ns := range cl.Namespaces {
		for _, k := range workloadKinds {
			items, why, err := c.list(ctx, cl.Source, k[0], k[1], ns)
			if err != nil {
				return nil, err
			}
			if why != "" {
				c.blindSpot(cl.Connector+":"+ns+"/"+k[1], why)
				continue
			}
			for _, it := range items {
				if it.Images == nil {
					c.blindSpot(cl.Connector+":"+ns+"/"+k[1], fmt.Sprintf(reasonUnrecognized, toolList))
					break
				}
				w := workload{connector: cl.Connector, Namespace: ns, Kind: k[1], Name: *it.Name, Ready: ready(it)}
				for ref := range strings.SplitSeq(*it.Images, ",") {
					if ref != "" {
						w.Images = append(w.Images, parseImage(ref))
					}
				}
				if it.Labels != nil {
					w.labels(*it.Labels)
				}
				into = append(into, w)
			}
		}
	}
	return into, nil
}

func (w *workload) labels(cell string) {
	for pair := range strings.SplitSeq(cell, ",") {
		k, v, _ := strings.Cut(pair, "=")
		switch k {
		case labelChart:
			w.Chart, w.ChartVersion = splitChart(v)
		case labelPartOf:
			w.PartOf = v
		case labelVersion:
			w.Version = v
		}
	}
}

// splitChart splits a helm.sh/chart label, <name>-<version>, at the last
// hyphen followed by a version: chart names hold hyphens too.
func splitChart(v string) (name, version string) {
	for i := strings.LastIndex(v, "-"); i > 0; i = strings.LastIndex(v[:i], "-") {
		if eos.MinorOf(v[i+1:]) != "" {
			return v[:i], v[i+1:]
		}
	}
	return v, ""
}

// ready reads the Ready column: "n/m" (Deployment, StatefulSet), or a
// count against Desired (DaemonSet). A CronJob has none: never ready.
func ready(it item) bool {
	var s string
	if json.Unmarshal(it.Ready, &s) == nil {
		have, want, ok := strings.Cut(s, "/")
		return ok && have == want && want != "0"
	}
	var have, want int
	return json.Unmarshal(it.Ready, &have) == nil && json.Unmarshal(it.Desired, &want) == nil && want > 0 && have == want
}

// clusterVersion reads a cluster's version from its nodes only: the
// lowest kubelet version, a lower bound of the control plane's (version
// skew policy). Nodes not readable (addons.kubernetes.readNodes off): a
// blind spot, and "" for the version.
func (c *collector) clusterVersion(ctx context.Context, cl Cluster) (string, error) {
	nodes, why, err := c.list(ctx, cl.Source, "v1", "Node", "")
	if err != nil {
		return "", err
	}
	version, ref := "", ""
	if why == "" {
		for _, n := range nodes {
			v, _, _ := strings.Cut(strings.TrimPrefix(n.Version, "v"), "+")
			if eos.MinorOf(v) != "" && (version == "" || eos.Compare(v, version) < 0) {
				version, ref = cut(v, maxField), cl.Connector+":Node/"+*n.Name
			}
		}
	}
	if version == "" {
		c.blindSpot(cl.Connector+":Node", reasonNoVersion)
		return "", nil
	}
	c.clusters = append(c.clusters, clusterVersion{version: version, ref: ref})
	return version, nil
}

type apiKey struct{ apiVersion, kind, namespace, name string }

// liveAPIs lists the scope's objects on each namespaced apiVersion the
// dataset says is removed after the cluster's version (every one of them
// when the version is unknown), and only when its replacement is not
// served: the API server serves an object through every served version, so
// with the replacement served the old apiVersion lists every object of the
// kind, whatever it was written with.
func (c *collector) liveAPIs(ctx context.Context, cl Cluster, version string) error {
	minor := eos.MinorOf(version)
	if len(cl.Namespaces) == 0 {
		return nil
	}
	for _, r := range c.in.Dataset.Kubernetes.RemovedAPIs.Entries {
		if !r.Namespaced || (minor != "" && eos.Compare(r.RemovedIn, minor) <= 0) {
			continue
		}
		if r.Replacement != "" {
			_, why, err := c.list(ctx, cl.Source, r.Replacement, r.Kind, cl.Namespaces[0])
			switch {
			case err != nil:
				return err
			case why == "":
				continue
			case why != reasonNotServed:
				c.blindSpot(cl.Connector+":"+r.Replacement+"/"+r.Kind, why)
				continue
			}
		}
		for _, ns := range cl.Namespaces {
			items, why, err := c.list(ctx, cl.Source, r.APIVersion, r.Kind, ns)
			if err != nil {
				return err
			}
			if why == reasonNotServed {
				break
			}
			if why != "" {
				c.blindSpot(cl.Connector+":"+ns+"/"+r.APIVersion+"/"+r.Kind, why)
				continue
			}
			for _, it := range items {
				c.api(r, ns, *it.Name, v1alpha1.APILive)
			}
		}
	}
	return nil
}

func (c *collector) api(r eos.RemovedAPI, namespace, name string, where v1alpha1.APIWhere) {
	k := apiKey{r.APIVersion, r.Kind, namespace, name}
	a := c.apis[k]
	if a == nil {
		a = &v1alpha1.DeprecatedAPIUse{APIVersion: r.APIVersion, Kind: r.Kind, Namespace: namespace, Name: cut(name, maxRef),
			RemovedIn: r.RemovedIn, Replacement: r.Replacement}
		c.apis[k] = a
	}
	if !slices.Contains(a.Where, where) {
		a.Where = append(a.Where, where)
		slices.Sort(a.Where)
	}
}

// certificate is what the collector decodes of a Certificate's
// resources_get: who it is, status.notAfter and the conditions' type and
// status; every other field is not decoded.
type certificate struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Status struct {
		NotAfter   *metav1.Time `json:"notAfter"`
		Conditions []condition  `json:"conditions"`
	} `json:"status"`
}

func (o certificate) ready() bool {
	return slices.ContainsFunc(o.Status.Conditions, func(x condition) bool { return x.Type == "Ready" && x.Status == conditionTrue })
}

type condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

// getCertificate reads one Certificate within the collection's bound; nil
// with no reason past it. An answer about another object is not read.
func (c *collector) getCertificate(ctx context.Context, s Source, namespace, name string) (*certificate, string, error) {
	if c.gets >= maxGets {
		if c.gets == maxGets {
			c.blindSpot(s.Connector, fmt.Sprintf(reasonGetsBound, maxGets))
			c.gets++
		}
		return nil, "", nil
	}
	c.gets++
	res, why, err := c.call(ctx, s, toolGet, map[string]any{argAPIVersion: certManager, argKind: kindCertificate, argNamespace: namespace, argName: name})
	if err != nil || why != "" {
		return nil, why, err
	}
	var o certificate
	if len(res.Structured) == 0 || json.Unmarshal(res.Structured, &o) != nil ||
		o.APIVersion != certManager || o.Kind != kindCertificate || o.Metadata.Name != name || o.Metadata.Namespace != namespace {
		return nil, fmt.Sprintf(reasonUnrecognized, toolGet), nil
	}
	return &o, "", nil
}

// certificates lists cert-manager's Certificates and gets each for its
// notAfter, which the list does not give; it keeps those not Ready or
// expiring within the warning. A cluster without cert-manager has none.
func (c *collector) certificates(ctx context.Context, cl Cluster) error {
	for _, ns := range cl.Namespaces {
		items, why, err := c.list(ctx, cl.Source, certManager, kindCertificate, ns)
		switch {
		case err != nil:
			return err
		case why == reasonNotServed:
			return nil
		case why != "":
			c.blindSpot(cl.Connector+":"+ns+"/"+kindCertificate, why)
			continue
		}
		for _, it := range items {
			st := v1alpha1.CertificateState{Namespace: ns, Name: cut(*it.Name, maxRef)}
			st.Ready = column(it.Ready) == conditionTrue
			o, why, err := c.getCertificate(ctx, cl.Source, ns, *it.Name)
			if err != nil {
				return err
			}
			if o != nil {
				st.NotAfter, st.Ready = o.Status.NotAfter, o.ready()
			} else if why != "" && st.Ready {
				c.blindSpot(cl.Connector+":"+ns+"/"+kindCertificate+"/"+*it.Name, why)
			}
			if !st.Ready || (st.NotAfter != nil && st.NotAfter.Sub(c.in.Now) <= c.in.CertificateWarning) {
				c.certs = append(c.certs, st)
			}
		}
	}
	return nil
}

// column reads a text column, "" when it is absent or not text.
func column(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// gitOps lists Argo CD Applications not Synced and Flux Kustomizations
// not Ready, from their lists only. A Kustomization's revision is the one
// its Status column names as applied, when it does; whether its last
// attempted revision is its last applied one is not read, which a blind
// spot says.
func (c *collector) gitOps(ctx context.Context, cl Cluster) error {
	for _, k := range [][2]string{{argoCD, "Application"}, {fluxKustomize, "Kustomization"}} {
		for _, ns := range cl.Namespaces {
			items, why, err := c.list(ctx, cl.Source, k[0], k[1], ns)
			if err != nil {
				return err
			}
			if why == reasonNotServed {
				break
			}
			if why != "" {
				c.blindSpot(cl.Connector+":"+ns+"/"+k[1], why)
				continue
			}
			if k[1] == "Kustomization" && len(items) > 0 {
				c.blindSpot(cl.Connector+":"+ns+"/"+k[1], reasonFluxRevisions)
			}
			c.gitOpsItems(cl, k[1], ns, items)
		}
	}
	return nil
}

func (c *collector) gitOpsItems(cl Cluster, kind, ns string, items []item) {
	for _, it := range items {
		st := v1alpha1.GitOpsState{Kind: kind, Namespace: ns, Name: cut(*it.Name, maxRef)}
		if kind == "Application" {
			if it.Sync == nil {
				c.blindSpot(cl.Connector+":"+ns+"/"+kind, fmt.Sprintf(reasonUnrecognized, toolList))
				return
			}
			if *it.Sync != "Synced" {
				st.State, st.Revision = cut(*it.Sync, 63), cut(it.Revision, maxRef)
				c.gitops = append(c.gitops, st)
			}
			continue
		}
		ready := column(it.Ready)
		if ready == "" {
			c.blindSpot(cl.Connector+":"+ns+"/"+kind, fmt.Sprintf(reasonUnrecognized, toolList))
			return
		}
		if ready == conditionTrue {
			continue
		}
		st.State = stateNotReady
		if it.Status != nil {
			if rev, ok := strings.CutPrefix(*it.Status, "Applied revision: "); ok {
				st.Revision = cut(rev, maxRef)
			}
		}
		c.gitops = append(c.gitops, st)
	}
}
