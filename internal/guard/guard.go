// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package guard is the read-only guard's run-time check (spec 04): what a
// connector's ServiceAccount must not be allowed to do, and the one thing
// it must, asked to the authorization API. The operator asks it for an
// add-on's ServiceAccount with SubjectAccessReviews; the front of a
// workload cluster asks it for its own with SelfSubjectAccessReviews
// (ADR 0016 §4). Both check the same things.
package guard

import (
	"context"
	"fmt"
)

// Probe is one question asked about the ServiceAccount: it must be denied.
type Probe struct {
	Group, Resource, Subresource, Verb string
	// What names the right in a message: "read Secrets".
	What string
}

const (
	verbCreate   = "create"
	resourcePods = "pods"
)

// Probes samples what the read-only guard forbids: a detection of drift at
// run time, by sampling. The proof, for what a chart renders, is the
// render test on its Roles.
var Probes = []Probe{
	{"", "secrets", "", "get", "read Secrets"},
	{"", "secrets", "", "list", "list Secrets"},
	{"", resourcePods, "exec", verbCreate, "exec into pods"},
	{"", resourcePods, "attach", verbCreate, "attach to pods"},
	{"", resourcePods, "portforward", verbCreate, "port-forward to pods"},
	{"", resourcePods, "", "delete", "delete pods"},
	{"", resourcePods, "", "update", "update pods"},
	{"", "configmaps", "", verbCreate, "create ConfigMaps"},
	{"apps", "deployments", "", "patch", "patch Deployments"},
	{"", "serviceaccounts", "token", verbCreate, "mint ServiceAccount tokens"},
	{"", "users", "", "impersonate", "impersonate users"},
	// A wildcard shows as a yes on a resource and a verb that do not exist.
	{"takecare.agaetis.fr", "takecare-guard-probe", "", "get", "a wildcard on resources"},
	{"", resourcePods, "", "takecare-guard-probe", "a wildcard on verbs"},
}

// Can asks whether the ServiceAccount may do a verb on a resource in a
// namespace ("" for the cluster scope).
type Can func(ctx context.Context, namespace, group, resource, subresource, verb string) (bool, error)

// Verdict is what the check found: rights the guard forbids, and
// namespaces where pods cannot even be listed.
type Verdict struct {
	Violations []string `json:"violations"`
	Missing    []string `json:"missing"`
}

// Check asks every probe at the cluster scope, then in each namespace, and
// whether pods can be listed in each namespace.
func Check(ctx context.Context, can Can, namespaces []string) (Verdict, error) {
	v := Verdict{Violations: []string{}, Missing: []string{}}
	for _, ns := range append([]string{""}, namespaces...) {
		for _, p := range Probes {
			allowed, err := can(ctx, ns, p.Group, p.Resource, p.Subresource, p.Verb)
			if err != nil {
				return v, err
			}
			if allowed {
				v.Violations = append(v.Violations, where(p.What, ns))
			}
		}
		if ns == "" {
			continue
		}
		allowed, err := can(ctx, ns, "", resourcePods, "", "list")
		if err != nil {
			return v, err
		}
		if !allowed {
			v.Missing = append(v.Missing, fmt.Sprintf("list pods in %s", ns))
		}
	}
	return v, nil
}

func where(what, ns string) string {
	if ns == "" {
		return what + " cluster-wide"
	}
	return what + " in " + ns
}
