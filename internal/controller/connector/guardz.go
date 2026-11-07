// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// Reasons of a remote cluster's guard and name (ADR 0016 §3-4).
const (
	ReasonGuardUnavailable = "GuardUnavailable"
	ReasonClusterNameTaken = "ClusterNameTaken"
)

const guardzTimeout = 5 * time.Second

// guardz is the front's /guardz body.
type guardz struct {
	ServiceAccount string   `json:"serviceAccount"`
	Namespaces     []string `json:"namespaces"`
	Violations     []string `json:"violations"`
	Missing        []string `json:"missing"`
	Error          string   `json:"error"`
}

// remoteGuard reads the read-only guard of a remote cluster from its
// front's /guardz, with the connector's client (ADR 0016 §4): the proof is
// made where the data is. 200 carries the verdict; anything else, or no
// answer, is GuardUnavailable, and the connector is not Ready.
func (r *Reconciler) remoteGuard(ctx context.Context, c *v1alpha1.TakeCareConnector, hc *http.Client, st *status) {
	g, err := fetchGuardz(ctx, hc, c.Spec.MCP.URL)
	if err != nil {
		st.set(ConditionReadOnlyGuard, false, ReasonGuardUnavailable, fmt.Sprintf("the front's /guardz gave no verdict: %v", err))
		return
	}
	declared := make([]string, 0, len(c.Spec.Kubernetes.Namespaces))
	for _, ns := range c.Spec.Kubernetes.Namespaces {
		declared = append(declared, string(ns))
	}
	slices.Sort(declared)
	checked := slices.Sorted(slices.Values(g.Namespaces))
	switch {
	case len(g.Violations) > 0:
		st.set(ConditionReadOnlyGuard, false, ReasonReadOnlyGuardViolated, fmt.Sprintf("the ServiceAccount %s may %s: the read-only guard forbids it (spec 04)", g.ServiceAccount, strings.Join(g.Violations, ", ")))
	case !slices.Equal(declared, checked):
		st.set(ConditionReadOnlyGuard, false, ReasonRBACMissing, fmt.Sprintf("the front checks the namespaces %s, the connector declares %s: install the takecare-cluster chart with the declared ones (drift)", strings.Join(checked, ", "), strings.Join(declared, ", ")))
	case len(g.Missing) > 0:
		st.set(ConditionReadOnlyGuard, false, ReasonRBACMissing, fmt.Sprintf("the ServiceAccount %s cannot %s: the takecare-cluster chart's Roles do not cover spec.kubernetes.namespaces", g.ServiceAccount, strings.Join(g.Missing, ", ")))
	default:
		st.set(ConditionReadOnlyGuard, true, ReasonReady, fmt.Sprintf("%s holds nothing the read-only guard forbids in %d namespace(s) and at the cluster scope (sampled by the front's /guardz)", g.ServiceAccount, len(checked)))
	}
}

// fetchGuardz GETs /guardz on the MCP URL's origin.
func fetchGuardz(ctx context.Context, hc *http.Client, mcpURL string) (*guardz, error) {
	u, err := url.Parse(mcpURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, guardzTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/guardz"}).String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var g guardz
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&g)
	switch {
	case resp.StatusCode != http.StatusOK && g.Error != "":
		return nil, fmt.Errorf("%s: %s", resp.Status, g.Error)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s", resp.Status)
	case decodeErr != nil:
		return nil, fmt.Errorf("the body is not the guard's verdict: %w", decodeErr)
	}
	return &g, nil
}

// nameTaken returns the connector that registered the same cluster name
// first (creation time, then name), if any: two connectors cannot be one
// cluster's.
func (r *Reconciler) nameTaken(ctx context.Context, c *v1alpha1.TakeCareConnector) (string, error) {
	var list v1alpha1.TakeCareConnectorList
	if err := r.List(ctx, &list, client.InNamespace(c.Namespace)); err != nil {
		return "", fmt.Errorf("list connectors: %w", err)
	}
	for i := range list.Items {
		o := &list.Items[i]
		if o.Name == c.Name || o.Spec.Cluster == nil || o.Spec.Cluster.Name != c.Spec.Cluster.Name || !o.DeletionTimestamp.IsZero() {
			continue
		}
		if cmp := o.CreationTimestamp.Compare(c.CreationTimestamp.Time); cmp < 0 || (cmp == 0 && o.Name < c.Name) {
			return o.Name, nil
		}
	}
	return "", nil
}
