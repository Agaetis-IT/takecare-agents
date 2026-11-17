// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"fmt"
	"net"
	"regexp"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// ReasonHeadersUnsupported says that a url connector's headers cannot all be given
// to the gateway, so its backend is not rendered rather than rendered with
// part of its credentials.
const ReasonHeadersUnsupported = "HeadersUnsupported"

// maxHeaders is the gateway's limit on a backend's credentials
// (agentgateway 1.6, policies.auth.credentials, maxItems 8); the API holds
// headersFrom to it.
const maxHeaders = 8

// headerName is the gateway's pattern for a credential's header name, the
// API's too.
var headerName = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+\\-.^_`|~]+$")

// CARef names the chart's CA bundle (global.caBundle): a ConfigMap or a
// Secret in the operator's namespace, and its key. The operator trusts it
// for its own calls; the gateway is told to trust it for a url connector's
// https server. Empty: the system's roots, on both sides.
type CARef struct {
	Kind string // ConfigMap or Secret
	Name string
	Key  string
}

// headersUnsupported says why a url connector's headers cannot be given to
// the gateway, or "" when they can. Only a connector created before the
// API's limits can get here.
func headersUnsupported(headers []v1alpha1.HeaderFrom) string {
	if len(headers) > maxHeaders {
		return fmt.Sprintf("%d headers in headersFrom: the gateway takes at most %d; the backend is not rendered", len(headers), maxHeaders)
	}
	for _, h := range headers {
		if !headerName.MatchString(h.Name) {
			return fmt.Sprintf("header name %q is not a valid HTTP header name; the backend is not rendered", h.Name)
		}
	}
	return ""
}

// urlPolicies are a url connector's target policies: each header from its
// Secret, by reference only (the gateway reads the Secret in the backend's
// namespace, the connector's), and TLS for an https URL. Nil when there is
// neither.
func (r *Reconciler) urlPolicies(c *v1alpha1.TakeCareConnector, host string, tls bool) map[string]any {
	policies := map[string]any{}
	if headers := c.Spec.MCP.HeadersFrom; len(headers) > 0 {
		credentials := make([]any, 0, len(headers))
		for _, h := range headers {
			credentials = append(credentials, map[string]any{
				"secretRef": map[string]any{keyName: h.SecretKeyRef.Name, "key": h.SecretKeyRef.Key},
				"location":  map[string]any{"header": map[string]any{keyName: h.Name}},
			})
		}
		policies["auth"] = map[string]any{"credentials": credentials}
	}
	if tls {
		t := map[string]any{}
		if net.ParseIP(host) == nil {
			t["sni"] = host
		}
		if r.CABundle.Name != "" {
			t["caCertificateRefs"] = []any{map[string]any{keyKind: r.CABundle.Kind, keyName: r.CABundle.Name, "key": r.CABundle.Key}}
		}
		policies["tls"] = t
	}
	if len(policies) == 0 {
		return nil
	}
	return policies
}
