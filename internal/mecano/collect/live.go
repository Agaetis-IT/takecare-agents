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
	certManager   = "cert-manager.io/v1"
	argoCD        = "argoproj.io/v1alpha1"
	fluxKustomize = "kustomize.toolkit.fluxcd.io/v1"
	kindPod       = "Pod"
	conditionTrue = "True"
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
	Sync       string          `json:"Sync Status"`
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

// clusterVersion reads the cluster's version: the nodes' lowest kubelet
// version (a lower bound of the control plane's, version skew policy),
// otherwise the kube-apiserver static pod's image when kube-system is
// read through the connector.
func (c *collector) clusterVersion(ctx context.Context, cl Cluster) error {
	nodes, why, err := c.list(ctx, cl.Source, "v1", "Node", "")
	if err != nil {
		return err
	}
	if why == "" {
		for _, n := range nodes {
			v, _, _ := strings.Cut(strings.TrimPrefix(n.Version, "v"), "+")
			if eos.MinorOf(v) != "" && (c.version == "" || eos.Compare(v, c.version) < 0) {
				c.version, c.versionFrom, c.versionRef = v, "kubelet", cl.Connector+":Node/"+*n.Name
			}
		}
		if c.version != "" {
			return nil
		}
	}
	if !slices.Contains(cl.Namespaces, "kube-system") {
		return nil
	}
	pods, why, err := c.list(ctx, cl.Source, "v1", kindPod, "kube-system")
	if err != nil || why != "" {
		return err
	}
	for _, p := range pods {
		if !strings.HasPrefix(*p.Name, "kube-apiserver-") {
			continue
		}
		res, why, ok, err := c.get(ctx, cl.Source, map[string]any{argAPIVersion: "v1", argKind: kindPod, argNamespace: "kube-system", argName: *p.Name})
		if err != nil || !ok || why != "" {
			return err
		}
		var pod struct {
			Spec struct {
				Containers []struct {
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		}
		if json.Unmarshal(res.Structured, &pod) != nil {
			return nil
		}
		for _, ct := range pod.Spec.Containers {
			if im := parseImage(ct.Image); strings.HasSuffix(im.Repository, "/kube-apiserver") && eos.MinorOf(im.Tag) != "" {
				c.version, c.versionFrom = strings.TrimPrefix(im.Tag, "v"), "kube-apiserver"
				c.versionRef, c.versionTool = cl.Connector+":kube-system/Pod/"+*p.Name, toolGet
				return nil
			}
		}
		return nil
	}
	return nil
}

type apiKey struct{ apiVersion, kind, namespace, name string }

// liveAPIs lists the scope's objects on each namespaced apiVersion the
// dataset says is removed after the cluster's version, which the server
// may still serve: every one of them when the version is unknown.
func (c *collector) liveAPIs(ctx context.Context, cl Cluster) error {
	minor := eos.MinorOf(c.version)
	for _, r := range c.in.Dataset.Kubernetes.RemovedAPIs.Entries {
		if !r.Namespaced || (minor != "" && eos.Compare(r.RemovedIn, minor) <= 0) {
			continue
		}
		served := cl.Connector + " " + r.APIVersion + " " + r.Kind
		for _, ns := range cl.Namespaces {
			if c.notServed[served] {
				break
			}
			items, why, err := c.list(ctx, cl.Source, r.APIVersion, r.Kind, ns)
			switch {
			case err != nil:
				return err
			case why == reasonNotServed:
				c.notServed[served] = true
				continue
			case why != "":
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

// object is what resources_get's structuredContent gives of an object's
// status, for the kinds the collector gets.
type object struct {
	Status struct {
		NotAfter              *metav1.Time `json:"notAfter"`
		LastAppliedRevision   string       `json:"lastAppliedRevision"`
		LastAttemptedRevision string       `json:"lastAttemptedRevision"`
		Conditions            []condition  `json:"conditions"`
	} `json:"status"`
}

func (o object) ready() bool {
	return slices.ContainsFunc(o.Status.Conditions, func(x condition) bool { return x.Type == "Ready" && x.Status == conditionTrue })
}

type condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

func (c *collector) getObject(ctx context.Context, s Source, apiVersion, kind, namespace, name string) (*object, string, error) {
	res, why, ok, err := c.get(ctx, s, map[string]any{argAPIVersion: apiVersion, argKind: kind, argNamespace: namespace, argName: name})
	if err != nil || !ok || why != "" {
		return nil, why, err
	}
	var o object
	if len(res.Structured) == 0 || json.Unmarshal(res.Structured, &o) != nil {
		return nil, fmt.Sprintf(reasonUnrecognized, toolGet), nil
	}
	return &o, "", nil
}

// certificates lists cert-manager's Certificates and gets each for its
// notAfter, which the list does not give; it keeps those not Ready or
// expiring within the warning. A cluster without cert-manager has none.
func (c *collector) certificates(ctx context.Context, cl Cluster) error {
	for _, ns := range cl.Namespaces {
		if c.notServed[cl.Connector+" Certificate"] {
			return nil
		}
		items, why, err := c.list(ctx, cl.Source, certManager, "Certificate", ns)
		switch {
		case err != nil:
			return err
		case why == reasonNotServed:
			c.notServed[cl.Connector+" Certificate"] = true
			continue
		case why != "":
			c.blindSpot(cl.Connector+":"+ns+"/Certificate", why)
			continue
		}
		for _, it := range items {
			st := v1alpha1.CertificateState{Namespace: ns, Name: cut(*it.Name, maxRef)}
			var listed string
			_ = json.Unmarshal(it.Ready, &listed)
			st.Ready = listed == conditionTrue
			o, why, err := c.getObject(ctx, cl.Source, certManager, "Certificate", ns, *it.Name)
			if err != nil {
				return err
			}
			if o != nil {
				st.NotAfter, st.Ready = o.Status.NotAfter, o.ready()
			} else if why != "" && st.Ready {
				c.blindSpot(cl.Connector+":"+ns+"/Certificate/"+*it.Name, why)
			}
			if !st.Ready || (st.NotAfter != nil && st.NotAfter.Sub(c.in.Now) <= c.in.CertificateWarning) {
				c.certs = append(c.certs, st)
			}
		}
	}
	return nil
}

// gitOps lists Argo CD Applications not Synced and Flux Kustomizations not
// Ready or whose last attempted revision is not their last applied one
// (got one by one: the list gives neither revision).
func (c *collector) gitOps(ctx context.Context, cl Cluster) error {
	for _, k := range [][2]string{{argoCD, "Application"}, {fluxKustomize, "Kustomization"}} {
		key := cl.Connector + " " + k[1]
		for _, ns := range cl.Namespaces {
			if c.notServed[key] {
				break
			}
			items, why, err := c.list(ctx, cl.Source, k[0], k[1], ns)
			switch {
			case err != nil:
				return err
			case why == reasonNotServed:
				c.notServed[key] = true
				continue
			case why != "":
				c.blindSpot(cl.Connector+":"+ns+"/"+k[1], why)
				continue
			}
			for _, it := range items {
				st := v1alpha1.GitOpsState{Kind: k[1], Namespace: ns, Name: cut(*it.Name, maxRef)}
				if k[1] == "Application" {
					if it.Sync != "Synced" {
						st.State, st.Revision = cut(it.Sync, 63), cut(it.Revision, maxRef)
						c.gitops = append(c.gitops, st)
					}
					continue
				}
				if err := c.kustomization(ctx, cl, it, st); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (c *collector) kustomization(ctx context.Context, cl Cluster, it item, st v1alpha1.GitOpsState) error {
	o, why, err := c.getObject(ctx, cl.Source, fluxKustomize, "Kustomization", st.Namespace, *it.Name)
	if err != nil {
		return err
	}
	if o == nil {
		var listed string
		_ = json.Unmarshal(it.Ready, &listed)
		if listed != conditionTrue {
			st.State = "NotReady"
			c.gitops = append(c.gitops, st)
		} else if why != "" {
			c.blindSpot(cl.Connector+":"+st.Namespace+"/Kustomization/"+*it.Name, why)
		}
		return nil
	}
	st.Revision = cut(o.Status.LastAttemptedRevision, maxRef)
	switch {
	case !o.ready():
		st.State = "NotReady"
	case o.Status.LastAttemptedRevision != o.Status.LastAppliedRevision:
		st.State = "RevisionNotApplied"
	default:
		return nil
	}
	c.gitops = append(c.gitops, st)
	return nil
}
