// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp/mcptest"
)

// secretValue must never leave its Secret: not in the backend, the
// connector's status or an event.
const secretValue = "url-connector-value-never-copied" //nolint:gosec // a test value

const (
	urlHost    = "mcp.example.com"
	urlHTTPS   = "https://" + urlHost + "/mcp"
	urlToken   = "url-token"
	corpCA     = "corp-ca"
	headerAuth = "Authorization"
	toolSearch = "search"
	tokenKey   = "token"
	fieldKey   = "key"
	fieldAuth  = "auth"
	fieldTLS   = "tls"
)

// credential is a backend's credential from the test Secret's key.
func credential(key, header string) any {
	return map[string]any{
		"secretRef": map[string]any{keyName: urlToken, fieldKey: key},
		"location":  map[string]any{"header": map[string]any{keyName: header}},
	}
}

// tlsTo is a target's TLS to urlHost, trusting the given bundle.
func tlsTo(bundle ...any) map[string]any {
	t := map[string]any{"sni": urlHost}
	if len(bundle) > 0 {
		t["caCertificateRefs"] = bundle
	}
	return t
}

// TestURLBackendCarriesReferences: a url connector's backend sends its
// headers from their Secrets, by reference, and speaks TLS to an https
// URL, trusting the chart's CA bundle when one is set (spec 05).
func TestURLBackendCarriesReferences(t *testing.T) {
	srv := mcptest.Start(t, mcptest.ToolSpec{Name: toolSearch, ReadOnly: true})
	headers := []v1alpha1.HeaderFrom{
		{Name: headerAuth, SecretKeyRef: v1alpha1.SecretKeyRef{Name: urlToken, Key: tokenKey}},
		{Name: "X-Api-Key", SecretKeyRef: v1alpha1.SecretKeyRef{Name: urlToken, Key: "apikey"}},
	}
	auth := map[string]any{"credentials": []any{credential(tokenKey, headerAuth), credential("apikey", "X-Api-Key")}}
	secretCA := CARef{Kind: "Secret", Name: corpCA, Key: "bundle.pem"}
	for _, tc := range []struct {
		name     string
		ca       CARef
		url      string
		headers  []v1alpha1.HeaderFrom
		policies map[string]any
	}{
		{"http with headers: the credentials, no TLS", CARef{}, "http://" + urlHost + "/mcp", headers, map[string]any{fieldAuth: auth}},
		{"https without a bundle: the system's roots", CARef{}, urlHTTPS, headers, map[string]any{fieldAuth: auth, fieldTLS: tlsTo()}},
		{"https with a ConfigMap bundle", CARef{Kind: "ConfigMap", Name: corpCA, Key: keyCA}, "https://" + urlHost + ":8443/mcp", nil,
			map[string]any{fieldTLS: tlsTo(map[string]any{keyKind: "ConfigMap", keyName: corpCA, fieldKey: keyCA})}},
		{"https with a Secret bundle", secretCA, urlHTTPS, headers,
			map[string]any{fieldAuth: auth, fieldTLS: tlsTo(map[string]any{keyKind: secretCA.Kind, keyName: corpCA, fieldKey: secretCA.Key})}},
		{"http without headers: no policies", CARef{}, "http://" + urlHost + "/mcp", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := start(t, srv, func(r *Reconciler) { r.CABundle = tc.ca })
			h.must(h.c.Create(h.ctx, &corev1.Secret{Name: urlToken, Namespace: operatorNS, StringData: map[string]string{tokenKey: secretValue, "apikey": secretValue}}))
			h.connector("tickets", v1alpha1.TakeCareConnectorSpec{
				Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorTickets},
				MCP:   v1alpha1.MCPSpec{URL: tc.url, HeadersFrom: tc.headers},
			})
			h.acceptBackend("tickets")
			h.waitCondition("tickets", v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady)

			backend := &unstructured.Unstructured{}
			backend.SetGroupVersionKind(backendGVK)
			h.must(h.c.Get(h.ctx, client.ObjectKey{Namespace: operatorNS, Name: "mcp-tickets"}, backend))
			targets, _, _ := unstructured.NestedSlice(backend.Object, "spec", "mcp", "targets")
			got, _ := targets[0].(map[string]any)["static"].(map[string]any)["policies"].(map[string]any)
			if diff := cmp.Diff(tc.policies, got); diff != "" {
				t.Errorf("target policies (-want +got):\n%s", diff)
			}

			var events corev1.EventList
			h.must(h.c.List(h.ctx, &events, client.InNamespace(operatorNS)))
			for what, obj := range map[string]any{"the backend": backend.Object, "the status": h.get("tickets").Status, "an event": events.Items} {
				raw, err := json.Marshal(obj)
				h.must(err)
				if strings.Contains(string(raw), secretValue) {
					t.Errorf("the Secret's value is in %s", what)
				}
			}
		})
	}
}

// TestHeadersFromLimits: the API holds headersFrom to the gateway's limits,
// and a connector stored before them gets no backend rather than part of
// its credentials.
func TestHeadersFromLimits(t *testing.T) {
	nine := make([]v1alpha1.HeaderFrom, 9)
	for i := range nine {
		nine[i] = v1alpha1.HeaderFrom{Name: "X-H" + string(rune('a'+i)), SecretKeyRef: v1alpha1.SecretKeyRef{Name: urlToken, Key: "k"}}
	}
	bad := []v1alpha1.HeaderFrom{{Name: "X Api Key", SecretKeyRef: v1alpha1.SecretKeyRef{Name: urlToken, Key: "k"}}}
	for _, tc := range []struct {
		name    string
		headers []v1alpha1.HeaderFrom
		want    string
	}{
		{"eight headers", nine[:8], ""},
		{"nine headers", nine, "9 headers in headersFrom: the gateway takes at most 8"},
		{"a header name with spaces", bad, `header name "X Api Key" is not a valid HTTP header name`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := headersUnsupported(tc.headers); !strings.HasPrefix(got, tc.want) || (tc.want == "") != (got == "") {
				t.Errorf("headersUnsupported = %q, want %q", got, tc.want)
			}
		})
	}

	h := start(t, mcptest.Start(t, mcptest.ToolSpec{Name: toolSearch, ReadOnly: true}))
	for name, headers := range map[string][]v1alpha1.HeaderFrom{"nine": nine, "bad-name": bad} {
		err := h.c.Create(h.ctx, &v1alpha1.TakeCareConnector{
			Name: name, Namespace: operatorNS,
			Spec: v1alpha1.TakeCareConnectorSpec{Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorTickets}, MCP: v1alpha1.MCPSpec{URL: urlHTTPS, HeadersFrom: headers}},
		})
		if err == nil {
			t.Errorf("the API accepted the %s headers", name)
		}
	}
}
