// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package alerts

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

	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/controller/connector"
)

// Results of resolving an alert to a Map entry, the values of
// takecare_alerts_resolved_total's result label (spec 02 §1).
const (
	ResolvedHit         = "hit"
	ResolvedMiss        = "miss"
	ResolvedUnavailable = "unavailable"
	// ResolvedNone: no Map to ask, so that an install without a Map never
	// reads as a Map outage.
	ResolvedNone = "none"
)

const resolveTimeout = 2 * time.Second

// Resolver finds the Map entry of a firing alert, and says how it went.
type Resolver interface {
	Resolve(ctx context.Context, a v1alpha1.AlertRecord) (v1alpha1.EntityKey, string)
}

// MapResolver asks the Map servers of the Ready map add-on connectors of
// the operator namespace, by name: the first hit wins; a miss if any
// answered; unavailable if none did. A connector declared by URL exposes
// the MCP contract only, not the server's /resolve, and is not asked.
type MapResolver struct {
	Client    client.Reader
	HTTP      *http.Client
	Namespace string
}

// Resolve implements Resolver.
func (r *MapResolver) Resolve(ctx context.Context, a v1alpha1.AlertRecord) (v1alpha1.EntityKey, string) {
	logger := log.FromContext(ctx).WithName("alerts")
	var list v1alpha1.TakeCareConnectorList
	if err := r.Client.List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
		logger.V(1).Info("map connectors not listed", "error", err.Error())
		return "", ResolvedUnavailable
	}
	slices.SortFunc(list.Items, func(x, y v1alpha1.TakeCareConnector) int { return strings.Compare(x.Name, y.Name) })
	result := ResolvedNone
	for i := range list.Items {
		c := &list.Items[i]
		if !slices.Contains(c.Spec.Types, v1alpha1.ConnectorMap) || c.Spec.MCP.Addon == "" {
			continue
		}
		if !meta.IsStatusConditionTrue(c.Status.Conditions, v1alpha1.ConditionReady) {
			if result == ResolvedNone {
				result = ResolvedUnavailable
			}
			continue
		}
		key, revision, err := r.ask(ctx, connector.AddonURL(c.Spec.MCP.Addon, c.Namespace, "/resolve"), a)
		switch {
		case err != nil:
			logger.V(1).Info("map not reached", "connector", c.Name, "error", err.Error())
			if result == ResolvedNone {
				result = ResolvedUnavailable
			}
			continue
		case revision != c.Status.MapRevision:
			logger.V(1).Info("map revision differs from the connector's", "connector", c.Name, "revision", revision, "mapRevision", c.Status.MapRevision)
		}
		if key != "" {
			return v1alpha1.EntityKey(key), ResolvedHit
		}
		result = ResolvedMiss
	}
	return "", result
}

// ask calls /resolve: a key on a hit, an empty key on a miss, an error
// when the server did not answer.
func (r *MapResolver) ask(ctx context.Context, base string, a v1alpha1.AlertRecord) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	q := url.Values{"namespace": {a.Namespace}, "workload": {a.Workload}, "service": {a.Service}, "alert": {a.Name}}
	if a.Cluster != "" {
		q.Set("cluster", a.Cluster)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
	if err != nil {
		return "", "", err
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return "", "", fmt.Errorf("resolve: %s", resp.Status)
	}
	var out struct{ Key, Revision string }
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return "", "", fmt.Errorf("resolve: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		out.Key = ""
	}
	return out.Key, out.Revision, nil
}

// entity resolves a firing alert and counts the result.
func (h *Handler) entity(ctx context.Context, a v1alpha1.AlertRecord) v1alpha1.EntityKey {
	key, result := v1alpha1.EntityKey(""), ResolvedNone
	if h.Resolver != nil {
		key, result = h.Resolver.Resolve(ctx, a)
	}
	h.Metrics.Resolved.WithLabelValues(result).Inc()
	return key
}
