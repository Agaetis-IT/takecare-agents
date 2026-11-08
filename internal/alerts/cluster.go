// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package alerts

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/controller/connector"
)

// labelCluster is the alert label a workload cluster's rules may carry.
const labelCluster = "cluster"

// candidate is a token the intake accepts and the cluster it identifies.
type candidate struct {
	cluster, from string
	token         []byte
}

// authenticate returns the cluster the bearer token identifies (ADR 0016
// §5): the operator's own token is the unnamed cluster (""), a cluster
// connector's alertTokenSecretRef its cluster. The token is compared with
// every candidate, in constant time and without stopping at a match, so
// that the time says nothing of which one matched. Two candidates with
// the presented value are no identity. Everything is read from the cache
// at every request: a rotated Secret is seen without a restart.
func (h *Handler) authenticate(ctx context.Context, r *http.Request) (string, error) {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || got == "" {
		return "", errors.New("no bearer token")
	}
	candidates, err := h.candidates(ctx)
	if err != nil {
		return "", err
	}
	var matched []candidate
	for _, c := range candidates {
		if len(c.token) > 0 && subtle.ConstantTimeCompare([]byte(got), c.token) == 1 {
			matched = append(matched, c)
		}
	}
	switch len(matched) {
	case 0:
		return "", errors.New("wrong token")
	case 1:
		return matched[0].cluster, nil
	}
	from := make([]string, 0, len(matched))
	for _, m := range matched {
		from = append(from, m.from)
	}
	log.FromContext(ctx).Info("one alert token for several clusters: refused", "from", strings.Join(from, ", "))
	return "", fmt.Errorf("the token is shared by %s", strings.Join(from, ", "))
}

// candidates are the operator's token and each cluster connector's.
func (h *Handler) candidates(ctx context.Context) ([]candidate, error) {
	name := h.TokenSecret
	if name == "" {
		name = TokenSecretName
	}
	var out []candidate
	var s corev1.Secret
	if err := h.Client.Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: name}, &s); err != nil {
		return nil, fmt.Errorf("token Secret: %w", err)
	}
	out = append(out, candidate{from: "the operator's token", token: s.Data[TokenSecretKey]})
	var conns v1alpha1.TakeCareConnectorList
	if err := h.Client.List(ctx, &conns, client.InNamespace(h.Namespace)); err != nil {
		return nil, fmt.Errorf("list connectors: %w", err)
	}
	for _, c := range conns.Items {
		if c.Spec.Cluster == nil || c.Spec.Cluster.AlertTokenSecretRef == nil {
			continue
		}
		var cs corev1.Secret
		if err := h.Client.Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: c.Spec.Cluster.AlertTokenSecretRef.Name}, &cs); err != nil {
			continue // a missing Secret is a token nobody can present
		}
		out = append(out, candidate{cluster: c.Spec.Cluster.Name, from: "connector " + c.Name, token: cs.Data[TokenSecretKey]})
	}
	return out, nil
}

// clustersOf are the clusters a Doc's cluster connectors read, from the
// cache: one source of truth with the Doc controller's (ADR 0016 §5). The
// unnamed cluster is "", and a Doc without a cluster connector reads it
// alone; a connector not found reads nothing.
func (h *Handler) clustersOf(ctx context.Context, d *v1alpha1.TakeCareDoc) ([]string, error) {
	if len(d.Spec.Connectors.Cluster) == 0 {
		return []string{""}, nil
	}
	var out []string
	for _, name := range d.Spec.Connectors.Cluster {
		var c v1alpha1.TakeCareConnector
		if err := h.Client.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: name}, &c); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("get connector %s: %w", name, err)
		}
		out = append(out, connector.ClusterOf(&c))
	}
	return out, nil
}
