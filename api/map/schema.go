// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package mapschema holds the JSON Schema of a Map entry (spec 08 §4): the
// file a customer's CI validates its Map with, embedded here for the Map
// server.
package mapschema

import (
	_ "embed"
	"slices"
	"strings"
)

// Entry is entry.schema.json.
//
//go:embed entry.schema.json
var Entry []byte

// Source is a kubernetes source of an entry: a namespace, or an object in
// it, on the unnamed cluster ("") or on a named one (ADR 0016 §5).
type Source struct {
	Cluster, Namespace, Kind, Name string
}

// ParseKubernetes reads [<cluster>/]<namespace>[/<Kind>/<name>], the
// segments' count telling them apart: one, a namespace; two, a cluster's
// namespace; three, an object; four, a cluster's object. Any other count is
// not a source.
func ParseKubernetes(ref string) (Source, bool) {
	p := strings.Split(ref, "/")
	switch len(p) {
	case 1:
		return Source{Namespace: p[0]}, true
	case 2:
		return Source{Cluster: p[0], Namespace: p[1]}, true
	case 3:
		return Source{Namespace: p[0], Kind: p[1], Name: p[2]}, true
	case 4:
		return Source{Cluster: p[0], Namespace: p[1], Kind: p[2], Name: p[3]}, true
	}
	return Source{}, false
}

// AzureScope is where an azure source sits (ADR 0021 §6): its subscription
// and, unless it sits outside any, its resource group, both lower-cased
// since Azure compares them regardless of case.
type AzureScope struct {
	Subscription, ResourceGroup string
}

// ParseAzure reads an ARM ID, /subscriptions/<id>[/resourceGroups/<name>]
// [/providers/<namespace>/<type>/<name>...], into its scope. After the
// provider namespace come type and name pairs; an extension resource nested
// under another (a second providers segment) keeps the first resource
// group. An empty segment (a doubled or trailing slash) or any other shape
// is not a source.
func ParseAzure(id string) (AzureScope, bool) {
	p := strings.Split(id, "/")
	if len(p) < 3 || p[0] != "" || !strings.EqualFold(p[1], "subscriptions") || slices.Contains(p[1:], "") {
		return AzureScope{}, false
	}
	s := AzureScope{Subscription: strings.ToLower(p[2])}
	rest := p[3:]
	if len(rest) >= 2 && strings.EqualFold(rest[0], "resourceGroups") {
		s.ResourceGroup = strings.ToLower(rest[1])
		rest = rest[2:]
	}
	// providers, a namespace, then at least one type and name pair.
	if len(rest) == 0 || strings.EqualFold(rest[0], "providers") && len(rest) >= 4 && len(rest)%2 == 0 {
		return s, true
	}
	return AzureScope{}, false
}
