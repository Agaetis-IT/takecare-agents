// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package collect is the Mécano's collector (spec 12 §4): operator code,
// no model. It reads what runs live through the cluster connectors, what
// Git declares through the code connectors, and what exists through the
// releases connectors or, without them, the end-of-support dataset; it
// compares them and returns the maintenance's inventory, what it could not
// read, and what the copy runs (§8). The agent explains and plans from
// these facts; it never reads a releases or copy connector.
//
// The answers it parses are those of the kubernetes add-on as pinned,
// containers/kubernetes-mcp-server v0.0.67 (testdata/kms-v0.0.67/README.md
// says how they were captured), of the code server (internal/codeserver)
// and of the releases server (internal/releasesserver). A shape it does
// not know is a blind spot, never a guess.
//
// Known limits, each said in the package rather than guessed around:
//   - a Kustomize override applies to the documents of its directory and
//     below; a base referenced from elsewhere (an overlay's ../../base) is
//     not followed, since the index does not give a kustomization's
//     resources;
//   - a Flux Kustomization's spec.images apply to every document of its
//     repository, since the index does not give its spec.path;
//   - a chart's objects are compared by chart name and version only: the
//     collector renders no template;
//   - a component's key is its image repository or chart name: resolving
//     it to a Map key needs the Map connector, read by the agent.
package collect

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/mecano/eos"
)

// Caller calls a tool on an endpoint: *mcp.Client.
type Caller interface {
	Call(ctx context.Context, ep mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error)
}

// Source is one connector the collector reads through.
type Source struct {
	// Connector is the TakeCareConnector's name, in evidence and blind
	// spots.
	Connector string
	Endpoint  mcp.Endpoint
}

// Cluster is a cluster connector and the namespaces read through it.
type Cluster struct {
	Source
	// Namespaces read: the Mécano's scope on this cluster.
	Namespaces []string
}

// Input is what one maintenance's collection reads.
type Input struct {
	Clusters []Cluster
	Code     []Source
	// Releases are the version watch's connectors; none: the dataset
	// alone, the offline watch (spec 12 §7.2).
	Releases []Source
	// Copy is the team's staging, read like the live cluster (spec 12 §8).
	Copy *Cluster
	// Components narrows the inventory to these keys; empty: the whole
	// scope.
	Components         []string
	Dataset            *eos.Dataset
	CertificateWarning time.Duration
	Now                time.Time
}

// Result is the collection's outcome.
type Result struct {
	Inventory  v1alpha1.MaintenanceInventory `json:"inventory"`
	BlindSpots []v1alpha1.NotSeen            `json:"blindSpots"`
	// ClusterVersion is the version read, empty when none could be;
	// VersionFrom says where it was read: "kubelet" (the nodes' lowest,
	// a lower bound of the control plane's) or "kube-apiserver".
	ClusterVersion string `json:"clusterVersion,omitempty"`
	VersionFrom    string `json:"versionFrom,omitempty"`
	// Copy is what the copy runs, per component.
	Copy []CopyComponent `json:"copy"`
}

// CopyComponent is a component running on the copy.
type CopyComponent struct {
	Key     string `json:"key"`
	Version string `json:"version"`
	// Ready is true when every object running it on the copy is ready.
	Ready    bool                `json:"ready"`
	Evidence []v1alpha1.Evidence `json:"evidence"`
}

// Bounds of the inventory (spec 12 §2) and of what one collection reads.
const (
	maxComponents   = 200
	maxAPIs         = 100
	maxCertificates = 100
	maxGitOps       = 100
	maxEvidence     = 10
	// maxGets bounds the resources_get calls of one collection
	// (certificates, Kustomizations).
	maxGets  = 200
	maxRef   = 253
	maxField = 128
)

// Tools the collector calls.
const (
	toolList         = "resources_list"
	toolGet          = "resources_get"
	toolRepositories = "repositories_list"
	toolIndex        = "manifests_index"
	toolSources      = "sources_list"
	toolVersions     = "versions_list"
	toolCycles       = "cycles_get"
)

// Arguments of the tools.
const (
	argAPIVersion = "apiVersion"
	argKind       = "kind"
	argNamespace  = "namespace"
	argName       = "name"
)

// Reasons of a blind spot: fixed phrases, never a server's text.
const (
	reasonForbidden    = "forbidden: the connector's identity may not list it"
	reasonNotServed    = "API not served"
	reasonToolError    = "the tool reported an error"
	reasonCallFailed   = "the call failed"
	reasonNotAllowed   = "the operator may not call this tool on the connector"
	reasonUnrecognized = "unrecognized %s output"
	reasonNoVersion    = "the cluster's version could not be read: neither the nodes (addons.kubernetes.readNodes) nor the kube-apiserver static pod; every apiVersion of the dataset is probed"
	reasonCharts       = "charts are compared by name and version only: their templates are not rendered"
	reasonOffline      = "offline watch, current as of %s"
	reasonDatasetOld   = "the end-of-support dataset is from %s"
	reasonStaleSource  = "the source did not answer, or answered stale past 7 days: the dataset's facts are used for %s"
	reasonGetsBound    = "more than %d objects to read one by one: the others are not read"
	reasonNoCode       = "no code connector could be read: what Git declares is unknown, and so is the drift"
)

// errCtx wraps the context's end: the only error Collect returns.
var errCtx = errors.New("collection stopped")

// Collect reads the sources and returns the inventory. It returns an
// error only when ctx ends (the caller gives it half the run's deadline);
// everything else it could not read is a blind spot.
func Collect(ctx context.Context, caller Caller, in Input) (Result, error) {
	c := &collector{caller: caller, in: in, notServed: map[string]bool{}, charts: map[string]bool{}, chartRepos: map[string]string{}}
	if err := c.run(ctx); err != nil {
		return Result{}, err
	}
	return c.result(), nil
}

type collector struct {
	caller    Caller
	in        Input
	blind     []v1alpha1.NotSeen
	notServed map[string]bool
	gets      int

	version, versionFrom string
	// versionRef is the object the version was read on, versionTool the
	// tool that read it (resources_list when empty).
	versionRef, versionTool string
	live                    []workload
	copied                  []workload
	git                     []document
	gitRead                 bool
	apis                    map[apiKey]*v1alpha1.DeprecatedAPIUse
	certs                   []v1alpha1.CertificateState
	gitops                  []v1alpha1.GitOpsState
	components              []v1alpha1.InventoryComponent
	// charts are the components that are charts; chartRepos a chart's
	// repository URL as Git names it.
	charts     map[string]bool
	chartRepos map[string]string
}

func (c *collector) run(ctx context.Context) error {
	c.apis = map[apiKey]*v1alpha1.DeprecatedAPIUse{}
	for _, cl := range c.in.Clusters {
		if err := c.clusterVersion(ctx, cl); err != nil {
			return err
		}
		if c.version != "" {
			break
		}
	}
	if c.version == "" && len(c.in.Clusters) > 0 {
		c.blindSpot("cluster version", reasonNoVersion)
	}
	for _, cl := range c.in.Clusters {
		var err error
		if c.live, err = c.workloads(ctx, cl, c.live); err != nil {
			return err
		}
		if err := c.liveAPIs(ctx, cl); err != nil {
			return err
		}
		if err := c.certificates(ctx, cl); err != nil {
			return err
		}
		if err := c.gitOps(ctx, cl); err != nil {
			return err
		}
	}
	if c.in.Copy != nil {
		var err error
		if c.copied, err = c.workloads(ctx, *c.in.Copy, nil); err != nil {
			return err
		}
	}
	for _, s := range c.in.Code {
		if err := c.index(ctx, s); err != nil {
			return err
		}
	}
	if !c.gitRead {
		c.blindSpot("code", reasonNoCode)
	}
	c.gitAPIs()
	c.compare()
	return c.versions(ctx)
}

// call runs a tool and classes its failure: a reason for a blind spot, or
// an error when ctx ended.
func (c *collector) call(ctx context.Context, s Source, tool string, args map[string]any) (mcp.Result, string, error) {
	res, err := c.caller.Call(ctx, s.Endpoint, tool, args)
	switch {
	case ctx.Err() != nil:
		return res, "", fmt.Errorf("%w: %s on %s: %w", errCtx, tool, s.Connector, ctx.Err())
	case errors.Is(err, mcp.ErrCallForbidden):
		return res, reasonNotAllowed, nil
	case err != nil:
		return res, reasonCallFailed, nil
	case res.IsError:
		return res, failure(res), nil
	}
	return res, "", nil
}

// failure classes a tool error by the shapes kubernetes-mcp-server v0.0.67
// gives; anything else is a tool error, with no text.
func failure(res mcp.Result) string {
	switch {
	case strings.Contains(res.Text, " is forbidden: "):
		return reasonForbidden
	case strings.Contains(res.Text, "no matches for kind"), strings.Contains(res.Text, "the server could not find the requested resource"):
		return reasonNotServed
	}
	return reasonToolError
}

// get runs resources_get within the collection's bound; false when the
// bound is reached (said once).
func (c *collector) get(ctx context.Context, s Source, args map[string]any) (mcp.Result, string, bool, error) {
	if c.gets >= maxGets {
		if c.gets == maxGets {
			c.blindSpot(s.Connector, fmt.Sprintf(reasonGetsBound, maxGets))
			c.gets++
		}
		return mcp.Result{}, "", false, nil
	}
	c.gets++
	res, why, err := c.call(ctx, s, toolGet, args)
	return res, why, true, err
}

func (c *collector) blindSpot(ref, reason string) {
	n := v1alpha1.NotSeen{Ref: cut(ref, maxRef), Reason: reason}
	if !slices.Contains(c.blind, n) {
		c.blind = append(c.blind, n)
	}
}

func evidence(tool string, record string) v1alpha1.Evidence {
	return v1alpha1.Evidence{Tool: tool, Record: cut(record, maxRef)}
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// result bounds the lists, counts what is past each bound, and orders
// everything for a stable status.
func (c *collector) result() Result {
	out := Result{BlindSpots: c.blind, ClusterVersion: c.version, VersionFrom: c.versionFrom}
	inv := &out.Inventory
	comps := c.components
	if len(c.in.Components) > 0 {
		comps = slices.DeleteFunc(comps, func(x v1alpha1.InventoryComponent) bool { return !slices.Contains(c.in.Components, x.Key) })
	}
	inv.Components, inv.Omitted.Components = bound(comps, maxComponents)
	apis := make([]v1alpha1.DeprecatedAPIUse, 0, len(c.apis))
	for _, a := range c.apis {
		apis = append(apis, *a)
	}
	slices.SortFunc(apis, func(a, b v1alpha1.DeprecatedAPIUse) int {
		return strings.Compare(a.APIVersion+"/"+a.Kind+"/"+a.Namespace+"/"+a.Name, b.APIVersion+"/"+b.Kind+"/"+b.Namespace+"/"+b.Name)
	})
	inv.APIs, inv.Omitted.APIs = bound(apis, maxAPIs)
	inv.Certificates, inv.Omitted.Certificates = bound(c.certs, maxCertificates)
	inv.GitOps, inv.Omitted.GitOps = bound(c.gitops, maxGitOps)
	out.Copy = c.copyComponents()
	return out
}

func bound[T any](items []T, n int) ([]T, int32) {
	if len(items) <= n {
		return items, 0
	}
	return items[:n], int32(len(items) - n) //nolint:gosec // bounded by the lists' own sizes
}
