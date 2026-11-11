// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// The two labels the import reads; every other label is dropped as the
// answer is parsed (spec 10 §4).
const (
	labelPartOf = "app.kubernetes.io/part-of"
	labelName   = "app.kubernetes.io/name"
)

// Reasons of a notSeen line: fixed phrases, never the server's text.
const (
	reasonForbidden    = "forbidden: the connector's identity may not list it"
	reasonNotServed    = "API not served"
	reasonToolError    = "the tool reported an error"
	reasonCallFailed   = "the call failed"
	reasonNotAllowed   = "the operator may not call this tool on the connector"
	reasonUnrecognized = "unrecognized %s output"
	reasonNoNamespace  = "the namespace does not exist"
)

var errUnrecognized = errors.New("unrecognized output")

// object is what the import keeps of a listed object: where it is, what it
// is, and the two labels. Nothing else of the answer reaches a variable.
type object struct {
	Namespace, Kind, Name string
	PartOf, AppName       string
}

// keptLabels reads a LABELS cell ("k=v,k2=v2" or "<none>") and keeps the
// two labels the import uses.
type keptLabels struct {
	partOf, name string
}

// UnmarshalJSON parses the cell and drops every other label.
func (l *keptLabels) UnmarshalJSON(data []byte) error {
	var cell string
	if err := json.Unmarshal(data, &cell); err != nil {
		return errUnrecognized
	}
	*l = parseLabels(cell)
	return nil
}

func parseLabels(cell string) keptLabels {
	var l keptLabels
	if cell == "<none>" {
		return l
	}
	for pair := range strings.SplitSeq(cell, ",") {
		k, v, _ := strings.Cut(pair, "=")
		switch k {
		case labelPartOf:
			l.partOf = v
		case labelName:
			l.name = v
		}
	}
	return l
}

// structuredItem is one item of resources_list's structuredContent, with
// exactly the fields the import keeps: the server's other columns (images,
// selectors, addresses) are not decoded.
type structuredItem struct {
	APIVersion *string     `json:"apiVersion"`
	Kind       *string     `json:"kind"`
	Name       *string     `json:"Name"`
	Namespace  *string     `json:"Namespace"`
	Labels     *keptLabels `json:"Labels"`
}

// parseList reads resources_list's answer for one kind in one namespace:
// its structuredContent, or an empty text for an empty list. Any other
// shape, or an item of another kind or namespace, is unrecognized.
func parseList(res mcp.Result, apiVersion, kind, namespace string) ([]object, error) {
	if len(res.Structured) == 0 {
		if strings.TrimSpace(res.Text) == "" {
			return nil, nil
		}
		return nil, errUnrecognized
	}
	var content struct {
		Items *[]structuredItem `json:"items"`
	}
	if err := json.Unmarshal(res.Structured, &content); err != nil || content.Items == nil {
		return nil, errUnrecognized
	}
	objects := make([]object, 0, len(*content.Items))
	for _, it := range *content.Items {
		if it.APIVersion == nil || it.Kind == nil || it.Name == nil || it.Namespace == nil || it.Labels == nil ||
			*it.APIVersion != apiVersion || *it.Kind != kind || *it.Namespace != namespace || *it.Name == "" {
			return nil, errUnrecognized
		}
		objects = append(objects, object{Namespace: namespace, Kind: kind, Name: *it.Name, PartOf: it.Labels.partOf, AppName: it.Labels.name})
	}
	return objects, nil
}

// namespacesHeader is namespaces_list's table header, which the import
// requires exactly.
var namespacesHeader = []string{"APIVERSION", "KIND", "NAME", "STATUS", "AGE", "LABELS"}

// parseNamespaces reads namespaces_list's text table: the exact header,
// then rows of as many fields, each a v1 Namespace.
func parseNamespaces(res mcp.Result) ([]string, error) {
	lines := strings.Split(strings.TrimRight(res.Text, "\n"), "\n")
	if len(lines) == 0 || !slices.Equal(strings.Fields(lines[0]), namespacesHeader) {
		return nil, errUnrecognized
	}
	var names []string
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) != len(namespacesHeader) || f[0] != "v1" || f[1] != "Namespace" {
			return nil, errUnrecognized
		}
		names = append(names, f[2])
	}
	return names, nil
}

// failure classes a tool error by the shapes kubernetes-mcp-server v0.0.67
// gives (testdata/README.md); anything else is a tool error, with no text.
func failure(res mcp.Result) string {
	switch {
	case strings.Contains(res.Text, " is forbidden: "):
		return reasonForbidden
	case strings.Contains(res.Text, "no matches for kind"), strings.Contains(res.Text, "the server could not find the requested resource"):
		return reasonNotServed
	}
	return reasonToolError
}
