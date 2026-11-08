// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"crypto/tls"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/httpclient/certtest"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/mcp/mcptest"
)

const (
	eastName   = "east"
	eastCA     = "east-ca"
	eastClient = "east-client"
)

// startFront plays a workload cluster's front: TLS with a client
// certificate required, verified against the pool, forwarding to srv.
func startFront(t *testing.T, m certtest.Mutual, srv *mcptest.Server) *httptest.Server {
	t.Helper()
	up, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewUnstartedServer(&httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(&url.URL{Scheme: up.Scheme, Host: up.Host}) }})
	front.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{m.Server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: m.Pool}
	front.StartTLS()
	t.Cleanup(front.Close)
	return front
}

func (h *harness) secret(name string, data map[string][]byte) {
	h.t.Helper()
	s := &corev1.Secret{Name: name, Namespace: operatorNS}
	if err := h.c.Get(h.ctx, client.ObjectKeyFromObject(s), s); err == nil {
		s.Data = data
		h.must(h.c.Update(h.ctx, s))
		return
	}
	s.Data = data
	h.must(h.c.Create(h.ctx, s))
}

// ADR 0016 §2-3: a remote cluster connector reaches its front with the
// client certificate from its Secrets, the gateway's backend carries the
// same material, and the connector is not Ready until its guard is read.
func TestRemoteClusterConnector(t *testing.T) {
	srv := mcptest.Start(t, mcptest.ToolSpec{Name: toolPodsList, ReadOnly: true}, mcptest.ToolSpec{Name: mcp.ProbeTool, ReadOnly: true, Reply: "[]"})
	m := certtest.MutualTLS(t)
	front := startFront(t, m, srv)
	h := start(t, srv)
	h.lister.url = front.URL + "/mcp"
	h.secret(eastCA, map[string][]byte{keyCA: m.CAPEM})
	h.secret(eastClient, map[string][]byte{keyCert: m.ClientPEM, keyKey: m.ClientKey})
	spec := v1alpha1.TakeCareConnectorSpec{
		Types:      []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster},
		MCP:        v1alpha1.MCPSpec{URL: front.URL + "/mcp"},
		Kubernetes: &v1alpha1.KubernetesSpec{Namespaces: []v1alpha1.Namespace{appNS}},
		Cluster: &v1alpha1.ClusterSpec{Name: eastName, TLS: v1alpha1.ClusterTLS{
			CASecretRef: v1alpha1.SecretRef{Name: eastCA}, ClientCertSecretRef: v1alpha1.SecretRef{Name: eastClient}}},
	}
	h.connector(eastName, spec)
	h.acceptBackend(eastName)

	for _, c := range []string{v1alpha1.ConditionReachable, v1alpha1.ConditionToolsDiscovered, ConditionBackendAccepted, ConditionResourcesListable} {
		h.waitCondition(eastName, c, metav1.ConditionTrue, "")
	}
	h.waitCondition(eastName, ConditionReadOnlyGuard, metav1.ConditionFalse, ReasonGuardPending)
	h.waitCondition(eastName, v1alpha1.ConditionReady, metav1.ConditionFalse, "")

	backend := &unstructured.Unstructured{}
	backend.SetGroupVersionKind(backendGVK)
	h.must(h.c.Get(h.ctx, client.ObjectKey{Namespace: operatorNS, Name: "mcp-" + eastName}, backend))
	got, _, _ := unstructured.NestedMap(backend.Object, "spec", "policies", "tls")
	want := map[string]any{
		"mtlsCertificateRef":    []any{map[string]any{"name": eastClient}},
		"caCertificateRefs":     []any{map[string]any{"kind": "Secret", "name": eastCA}},
		"sni":                   "127.0.0.1",
		"verifySubjectAltNames": []any{"127.0.0.1"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("backend policies.tls (-want +got):\n%s", diff)
	}

	t.Run("a client certificate from another CA is refused by the front", func(t *testing.T) {
		other := certtest.MutualTLS(t)
		h.secret(eastClient, map[string][]byte{keyCert: other.ClientPEM, keyKey: other.ClientKey})
		h.waitCondition(eastName, v1alpha1.ConditionReachable, metav1.ConditionFalse, "")
	})
	t.Run("a missing key is named, never a value", func(t *testing.T) {
		h.secret(eastClient, map[string][]byte{keyCert: m.ClientPEM})
		cond := h.waitCondition(eastName, v1alpha1.ConditionReachable, metav1.ConditionFalse, ReasonTLSMaterialMissing)
		if cond.Message != "Secret takecare-system/east-client has no tls.key" {
			t.Errorf("message %q", cond.Message)
		}
	})
	t.Run("the material back, the server is reached again", func(t *testing.T) {
		h.secret(eastClient, map[string][]byte{keyCert: m.ClientPEM, keyKey: m.ClientKey})
		h.waitCondition(eastName, v1alpha1.ConditionReachable, metav1.ConditionTrue, "")
	})
}
