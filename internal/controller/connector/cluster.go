// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/httpclient"
)

// Reasons of a remote cluster connector (ADR 0016).
const (
	ReasonTLSMaterialMissing = "TLSMaterialMissing"
)

// Keys of the TLS Secrets of a remote cluster connector.
const (
	keyCA   = "ca.crt"
	keyCert = "tls.crt"
	keyKey  = "tls.key"
	keyName = "name"
)

const clusterTimeout = 30 * time.Second

// clusterClient is the operator's client to a remote cluster's front: its
// CA only, the connector's client certificate, the server name. One per
// connector, built again when a Secret changes (the cache is keyed by their
// resourceVersions). A missing Secret or key is a problem to report, named,
// never a value.
func (r *Reconciler) clusterClient(ctx context.Context, c *v1alpha1.TakeCareConnector) (*http.Client, string, error) {
	tlsSpec := c.Spec.Cluster.TLS
	ca, problem, err := r.secretKeys(ctx, c.Namespace, tlsSpec.CASecretRef.Name, keyCA)
	if problem != "" || err != nil {
		return nil, problem, err
	}
	cert, problem, err := r.secretKeys(ctx, c.Namespace, tlsSpec.ClientCertSecretRef.Name, keyCert, keyKey)
	if problem != "" || err != nil {
		return nil, problem, err
	}
	key := fmt.Sprintf("%s|%s|%s|%s", c.UID, ca.ResourceVersion, cert.ResourceVersion, tlsSpec.ServerName)
	r.mu.Lock()
	defer r.mu.Unlock()
	if hc, ok := r.clients[client.ObjectKeyFromObject(c)]; ok && hc.key == key {
		return hc.client, "", nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.Data[keyCA]) {
		return nil, fmt.Sprintf("Secret %s/%s: %s holds no PEM certificate", c.Namespace, ca.Name, keyCA), nil
	}
	pair, err := tls.X509KeyPair(cert.Data[keyCert], cert.Data[keyKey])
	if err != nil {
		return nil, fmt.Sprintf("Secret %s/%s: %s and %s are not a certificate and its key", c.Namespace, cert.Name, keyCert, keyKey), nil
	}
	cfg := httpclient.FromEnv(clusterTimeout)
	cfg.RootCAs, cfg.ClientCertificate, cfg.ServerName = pool, &pair, tlsSpec.ServerName
	hc, err := cfg.New()
	if err != nil {
		return nil, "", fmt.Errorf("client of connector %s: %w", c.Name, err)
	}
	if r.clients == nil {
		r.clients = map[types.NamespacedName]cachedClient{}
	}
	r.clients[client.ObjectKeyFromObject(c)] = cachedClient{key: key, client: hc}
	return hc, "", nil
}

// cachedClient is a remote cluster's client and what it was built from.
type cachedClient struct {
	key    string
	client *http.Client
}

// secretKeys reads a Secret that must hold the keys; its absence, or a
// key's, is a problem named for the status.
func (r *Reconciler) secretKeys(ctx context.Context, namespace, name string, keys ...string) (*corev1.Secret, string, error) {
	var s corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Sprintf("Secret %s/%s does not exist", namespace, name), nil
		}
		return nil, "", fmt.Errorf("get Secret %s: %w", name, err)
	}
	for _, k := range keys {
		if len(s.Data[k]) == 0 {
			return nil, fmt.Sprintf("Secret %s/%s has no %s", namespace, name, k), nil
		}
	}
	return &s, "", nil
}

// backendTLS is the gateway's TLS to a remote cluster's front: the client
// certificate and the CA from the connector's Secrets, the server name sent
// and verified (the URL's host by default).
func backendTLS(c *v1alpha1.TakeCareConnector) map[string]any {
	name := c.Spec.Cluster.TLS.ServerName
	if name == "" {
		if u, err := url.Parse(c.Spec.MCP.URL); err == nil {
			name = u.Hostname()
		}
	}
	return map[string]any{
		"mtlsCertificateRef":    []any{map[string]any{keyName: c.Spec.Cluster.TLS.ClientCertSecretRef.Name}},
		"caCertificateRefs":     []any{map[string]any{"kind": "Secret", keyName: c.Spec.Cluster.TLS.CASecretRef.Name}},
		"sni":                   name,
		"verifySubjectAltNames": []any{name},
	}
}

// usesSecret says whether a connector reads a Secret: a header, or its
// remote cluster's TLS material.
func usesSecret(c v1alpha1.TakeCareConnector, name string) bool {
	for _, h := range c.Spec.MCP.HeadersFrom {
		if h.SecretKeyRef.Name == name {
			return true
		}
	}
	return c.Spec.Cluster != nil && (c.Spec.Cluster.TLS.CASecretRef.Name == name || c.Spec.Cluster.TLS.ClientCertSecretRef.Name == name)
}
