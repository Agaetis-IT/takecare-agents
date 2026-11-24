// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package coverage decides what of a role's scope its cluster and cloud
// connectors read (ADR 0014 §1-2, spec 08 §7): the namespaces of the scope,
// on every cluster the connectors read (ADR 0016 §5), and the scope's
// entities through the namespaces of their kubernetes sources and the
// scopes of their azure sources in the Map (ADR 0021 §6). What nothing
// reads is a blind spot, with its reason. The Doc and the Intendant share
// this rule.
package coverage

import (
	"fmt"
	"slices"
	"strings"

	mapschema "github.com/agaetis-it/takecare-agents/api/map"
	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const typeEnvironment = "environment"

// Place is a namespace of a cluster (ADR 0016 §5); the unnamed cluster is
// "" (an add-on, the demo).
type Place struct {
	Cluster   string
	Namespace v1alpha1.Namespace
}

// String is the namespace as the instructions name it: cluster/namespace
// on a named cluster, the namespace alone on the unnamed one.
func (p Place) String() string {
	if p.Cluster == "" {
		return string(p.Namespace)
	}
	return p.Cluster + "/" + string(p.Namespace)
}

// Key is the namespace's Map key: environment/<cluster>.<ns> on a named
// cluster, environment/<ns> on the unnamed one.
func (p Place) Key() v1alpha1.EntityKey {
	if p.Cluster == "" {
		return v1alpha1.EntityKey("environment/" + string(p.Namespace))
	}
	return v1alpha1.EntityKey("environment/" + p.Cluster + "." + string(p.Namespace))
}

// Declared is a bound cluster connector and the namespaces it declares.
type Declared struct {
	Connector  string
	Cluster    string
	Namespaces []v1alpha1.Namespace
}

// Reach is a bound cloud connector and the azure scopes it declares
// (ADR 0021 §5).
type Reach struct {
	Connector string
	Azure     *v1alpha1.AzureReach
}

// Holds says whether the reach holds an azure scope: its subscription is
// listed and, when that subscription lists resource groups, its group is
// one of them; a resource outside any group needs the whole subscription.
// Every comparison folds case.
func (r Reach) Holds(s mapschema.AzureScope) bool {
	if r.Azure == nil {
		return false
	}
	for _, sub := range r.Azure.Subscriptions {
		if strings.ToLower(sub.ID) != s.Subscription {
			continue
		}
		if len(sub.ResourceGroups) == 0 {
			return true
		}
		if s.ResourceGroup != "" && slices.ContainsFunc(sub.ResourceGroups, func(g v1alpha1.ResourceGroupName) bool {
			return strings.ToLower(string(g)) == s.ResourceGroup
		}) {
			return true
		}
	}
	return false
}

// Entry is what the Map says of one key.
type Entry struct {
	Found     bool
	Validated bool
	// Places are the namespaces of the entry's kubernetes sources, each on
	// its cluster (ADR 0016 §5).
	Places []Place
	// Azure are the entry's azure sources, ARM IDs that ParseAzure reads.
	Azure []string
	// Err is set when the Map did not answer.
	Err string
}

// MapProblem is why the Map could not be asked at all; empty when it was.
type MapProblem struct {
	Reason v1alpha1.BlindSpotReason
	Why    string
}

// Result is what a scope's coverage gives.
type Result struct {
	// Readable are the namespaces the given connectors read, in their
	// declaration order.
	Readable []Place
	// BlindSpots are what of the scope nothing given reads, by key.
	BlindSpots []v1alpha1.BlindSpot
	// Covered counts the scope's namespaces (per cluster) and entities read.
	Covered int
}

// OnGround says whether an entity type has state on a ground, and is
// checked for coverage (spec 08 §7); environment is checked through its
// namespace.
func OnGround(t string) bool {
	switch t {
	case "service", "compute", "datastore", "storage", "network", "identity":
		return true
	}
	return false
}

// EnvironmentPlace reads an environment entry's name: <cluster>.<ns> on a
// named cluster, <ns> on the unnamed one (a namespace has no dot).
func EnvironmentPlace(name string) Place {
	if cluster, ns, dotted := strings.Cut(name, "."); dotted {
		return Place{cluster, v1alpha1.Namespace(ns)}
	}
	return Place{"", v1alpha1.Namespace(name)}
}

// TypeName splits a Map key into its type and name.
func TypeName(k v1alpha1.EntityKey) (string, string) {
	t, name, _ := strings.Cut(string(k), "/")
	return t, name
}

// Clusters are the clusters the declared connectors read, sorted; the
// unnamed one when there is none.
func Clusters(declared []Declared) []string {
	var out []string
	for _, dc := range declared {
		if !slices.Contains(out, dc.Cluster) {
			out = append(out, dc.Cluster)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	slices.Sort(out)
	return out
}

// Unread says why a namespace nothing given reads is not read: NoAccess
// when no declared connector declares it, ConnectorNotReady when the ones
// that do are not given.
func Unread(declared []Declared, p Place) (v1alpha1.BlindSpotReason, string) {
	var by []string
	for _, dc := range declared {
		if dc.Cluster == p.Cluster && slices.Contains(dc.Namespaces, p.Namespace) && !slices.Contains(by, dc.Connector) {
			by = append(by, dc.Connector)
		}
	}
	return unreadBy(by)
}

// UnreadAzure says why an azure scope nothing given reads is not read, as
// Unread does for a namespace.
func UnreadAzure(reaches []Reach, s mapschema.AzureScope) (v1alpha1.BlindSpotReason, string) {
	var by []string
	for _, r := range reaches {
		if r.Holds(s) && !slices.Contains(by, r.Connector) {
			by = append(by, r.Connector)
		}
	}
	return unreadBy(by)
}

func unreadBy(by []string) (v1alpha1.BlindSpotReason, string) {
	if len(by) == 0 {
		return v1alpha1.BlindSpotNoAccess, "no connector reads it"
	}
	return v1alpha1.BlindSpotConnectorNotReady, fmt.Sprintf("its connector %s is not given to the agent (never discovered, or no read tool)", strings.Join(by, ", "))
}

// Cover decides what of the scope the given connectors read. A namespace
// of the scope names no cluster: it is wanted on every cluster the declared
// connectors read (ADR 0016 §5). An entity is read when one of its Map
// sources is in a readable namespace, or held by the reach of a given cloud
// connector; environment/<ns> when the namespace
// is readable; team, observability, runbook and external are never blind
// spots. entries are the Map's answers for the scope's entities that have
// state on a ground; problem, when set, says why the Map could not be asked.
func Cover(scope v1alpha1.Scope, declared []Declared, reaches []Reach, given map[string]bool, entries map[v1alpha1.EntityKey]Entry, problem MapProblem) Result {
	var res Result
	readable := map[Place]bool{}
	for _, dc := range declared {
		for _, ns := range dc.Namespaces {
			p := Place{dc.Cluster, ns}
			if given[dc.Connector] && !readable[p] {
				readable[p] = true
				res.Readable = append(res.Readable, p)
			}
		}
	}
	for _, ns := range scope.Namespaces {
		for _, cl := range Clusters(declared) {
			p := Place{cl, ns}
			if readable[p] {
				res.Covered++
				continue
			}
			reason, why := Unread(declared, p)
			res.BlindSpots = append(res.BlindSpots, v1alpha1.BlindSpot{Entity: p.Key(), Reason: reason, Message: fmt.Sprintf("The namespace `%s` (%s): %s.", p, p.Key(), why)})
		}
	}
	spot := func(k v1alpha1.EntityKey, reason v1alpha1.BlindSpotReason, why string) {
		res.BlindSpots = append(res.BlindSpots, v1alpha1.BlindSpot{Entity: k, Reason: reason, Message: fmt.Sprintf("The entity `%s`: %s.", k, why)})
	}
	for _, k := range scope.Entities {
		t, name := TypeName(k)
		env := EnvironmentPlace(name)
		switch {
		case t == typeEnvironment && env.Cluster == "" && slices.Contains(scope.Namespaces, env.Namespace):
			// Counted with the namespaces.
		case t == typeEnvironment && readable[env]:
			res.Covered++
		case t == typeEnvironment:
			reason, why := Unread(declared, env)
			spot(k, reason, why)
		case !OnGround(t):
		case problem.Why != "":
			spot(k, problem.Reason, problem.Why)
		default:
			res.Covered += coverEntry(k, entries[k], declared, reaches, given, readable, spot)
		}
	}
	slices.SortFunc(res.BlindSpots, func(x, y v1alpha1.BlindSpot) int { return strings.Compare(string(x.Entity), string(y.Entity)) })
	return res
}

func coverEntry(k v1alpha1.EntityKey, info Entry, declared []Declared, reaches []Reach, given map[string]bool, readable map[Place]bool, spot func(v1alpha1.EntityKey, v1alpha1.BlindSpotReason, string)) int {
	var scopes []mapschema.AzureScope
	var ids []string
	for _, id := range info.Azure {
		if s, ok := mapschema.ParseAzure(id); ok {
			scopes, ids = append(scopes, s), append(ids, id)
		}
	}
	held := func(s mapschema.AzureScope) bool {
		return slices.ContainsFunc(reaches, func(r Reach) bool { return given[r.Connector] && r.Holds(s) })
	}
	switch {
	case info.Err != "":
		spot(k, v1alpha1.BlindSpotConnectorNotReady, "the Map did not answer: "+info.Err)
	case !info.Found:
		spot(k, v1alpha1.BlindSpotNotInMap, "not in the Map")
	case !info.Validated:
		spot(k, v1alpha1.BlindSpotNotValidated, "proposed in the Map, not validated")
	case len(info.Places) == 0 && len(scopes) == 0:
		spot(k, v1alpha1.BlindSpotNoAccess, "no kubernetes or azure source in the Map, so no connector reads it")
	case slices.ContainsFunc(info.Places, func(p Place) bool { return readable[p] }), slices.ContainsFunc(scopes, held):
		return 1
	case len(info.Places) > 0:
		reason, why := Unread(declared, info.Places[0])
		spot(k, reason, fmt.Sprintf("its namespace `%s`: %s", info.Places[0], why))
	default:
		reason, why := UnreadAzure(reaches, scopes[0])
		spot(k, reason, fmt.Sprintf("its azure resource `%s`: %s", ids[0], why))
	}
	return 0
}
