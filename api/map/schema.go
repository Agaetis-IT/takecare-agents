// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package mapschema holds the JSON Schema of a Map entry (spec 08 §4): the
// file a customer's CI validates its Map with, embedded here for the Map
// server.
package mapschema

import (
	_ "embed"
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
