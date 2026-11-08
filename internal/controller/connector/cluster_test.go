// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
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

// guardAnswer is what the fake front's /guardz answers: a status and a body.
type guardAnswer struct {
	code int
	body string
}

const cleanGuard = `{"serviceAccount":"system:serviceaccount:takecare-cluster:takecare-cluster","namespaces":["demo-apps"],"violations":[],"missing":[]}`

// startFront plays a workload cluster's front: TLS with a client
// certificate required, verified against the pool, /guardz from guard,
// everything else forwarded to srv.
func startFront(t *testing.T, m certtest.Mutual, srv *mcptest.Server, guard *atomic.Value) *httptest.Server {
	t.Helper()
	up, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(&url.URL{Scheme: up.Scheme, Host: up.Host}) }}
	front := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/guardz" {
			proxy.ServeHTTP(w, r)
			return
		}
		a, _ := guard.Load().(guardAnswer)
		w.WriteHeader(a.code)
		_, _ = io.WriteString(w, a.body)
	}))
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

// ADR 0016 §2-4: a remote cluster connector reaches its front with the
// client certificate from its Secrets, the gateway's backend carries the
// same material, and the read-only guard comes from the front's /guardz.
func TestRemoteClusterConnector(t *testing.T) {
	srv := mcptest.Start(t, mcptest.ToolSpec{Name: toolPodsList, ReadOnly: true}, mcptest.ToolSpec{Name: mcp.ProbeTool, ReadOnly: true, Reply: "[]"})
	m := certtest.MutualTLS(t)
	var guard atomic.Value
	guard.Store(guardAnswer{http.StatusOK, cleanGuard})
	front := startFront(t, m, srv, &guard)
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
	h.waitCondition(eastName, ConditionReadOnlyGuard, metav1.ConditionTrue, ReasonReady)
	h.waitCondition(eastName, v1alpha1.ConditionReady, metav1.ConditionTrue, "")

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

	for _, tc := range []struct {
		name, reason string
		answer       guardAnswer
	}{
		{"a violation", ReasonReadOnlyGuardViolated, guardAnswer{http.StatusOK, `{"serviceAccount":"sa","namespaces":["demo-apps"],"violations":["read Secrets cluster-wide"],"missing":[]}`}},
		{"a namespace not readable", ReasonRBACMissing, guardAnswer{http.StatusOK, `{"serviceAccount":"sa","namespaces":["demo-apps"],"violations":[],"missing":["list pods in demo-apps"]}`}},
		{"the front checks other namespaces (drift)", ReasonRBACMissing, guardAnswer{http.StatusOK, `{"serviceAccount":"sa","namespaces":["other"],"violations":[],"missing":[]}`}},
		{"no verdict", ReasonGuardUnavailable, guardAnswer{http.StatusServiceUnavailable, `{"error":"the API server is unreachable"}`}},
	} {
		t.Run(tc.name+" is not Ready", func(t *testing.T) {
			guard.Store(tc.answer)
			h.touch(eastName)
			h.waitCondition(eastName, ConditionReadOnlyGuard, metav1.ConditionFalse, tc.reason)
			h.waitCondition(eastName, v1alpha1.ConditionReady, metav1.ConditionFalse, "")
		})
	}
	guard.Store(guardAnswer{http.StatusOK, cleanGuard})

	t.Run("a second connector with the same cluster name is ClusterNameTaken", func(t *testing.T) {
		h.connector("east-again", spec)
		h.acceptBackend("east-again")
		cond := h.waitCondition("east-again", v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonClusterNameTaken)
		if cond.Message != "the cluster name east is registered by the connector east, which came first" {
			t.Errorf("message %q", cond.Message)
		}
	})

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

// touch annotates a connector so that it is reconciled now, not at its
// next discovery.
func (h *harness) touch(name string) {
	h.t.Helper()
	// The controller writes the status meanwhile: retry on a conflict.
	h.must(retry.RetryOnConflict(retry.DefaultRetry, func() error {
		c := h.get(name)
		if c.Annotations == nil {
			c.Annotations = map[string]string{}
		}
		c.Annotations["takecare.agaetis.fr/test-touch"] = time.Now().Format(time.RFC3339Nano)
		return h.c.Update(h.ctx, c)
	}))
}
