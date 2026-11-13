// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package discovery is the Intendant's deterministic import (spec 10 §4):
// operator code, no model. It lists the ground through the read tools of
// the cluster connectors given to the Intendant, compares what it sees
// with the Map, and returns per-layer counts, what it could not read, and
// proposals for the Map. It writes nothing: a human turns the proposals
// into a pull request.
//
// The answers it parses are those of the kubernetes add-on as pinned,
// containers/kubernetes-mcp-server v0.0.67 with list_output "table" (the
// add-on's default, which the Doc also reads): resources_list's
// structuredContent and namespaces_list's text table. Its alerting layer
// reads the metrics add-on's get_rules, as pinned (giantswarm/mcp-prometheus
// v0.11.11; rules.go). A version bump of either add-on recaptures testdata/
// (the README next to each set says how); a shape the parser does not know
// is a notSeen line, never a guess.
//
// It never reads a value: no object is fetched, only listed; of an
// object's labels only app.kubernetes.io/part-of and app.kubernetes.io/name
// are kept; environment variables, annotations, ConfigMap and Secret data
// never reach it.
package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	mapschema "github.com/agaetis-it/takecare-agents/api/map"
	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// Tools are the cluster connector's tools the import calls; mcp.Endpoint
// allows them when discovery classed them read and not content.
var Tools = mcp.DiscoveryTools

const (
	toolNamespaces = "namespaces_list"
	toolList       = "resources_list"
	apps           = "apps/v1"
	// sourceKubernetes is a Map source's kind (spec 08 §3).
	sourceKubernetes = "kubernetes"
)

// Layers, in the order the result lists them (spec 10 §4).
const (
	LayerEnvironment = "environment"
	LayerCompute     = "compute"
	LayerService     = "service"
	LayerNetwork     = "network"
	LayerIdentity    = "identity"
	LayerStorage     = "storage"
	LayerAlerting    = "alerting"
)

var layerOrder = []string{LayerEnvironment, LayerCompute, LayerService, LayerNetwork, LayerIdentity, LayerStorage, LayerAlerting}

// kind is a resource kind the import lists, and the layer (and entity
// type) it lands in.
type kind struct {
	layer, apiVersion, kind string
}

var kinds = []kind{
	{LayerCompute, apps, "Deployment"},
	{LayerCompute, apps, "StatefulSet"},
	{LayerCompute, apps, "DaemonSet"},
	{LayerCompute, "batch/v1", "CronJob"},
	{LayerNetwork, "v1", "Service"},
	{LayerNetwork, "networking.k8s.io/v1", "Ingress"},
	{LayerNetwork, "gateway.networking.k8s.io/v1", "HTTPRoute"},
	{LayerIdentity, "v1", "ServiceAccount"},
	{LayerStorage, "v1", "PersistentVolumeClaim"},
}

// deployedIn is the relation the import draws to a namespace, for the
// types spec 08 §2 allows it from.
var deployedIn = map[string]bool{LayerCompute: true, LayerNetwork: true}

// Caller calls a tool on an endpoint: *mcp.Client.
type Caller interface {
	Call(ctx context.Context, ep mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error)
}

// Ground is one cluster connector the import reads through.
type Ground struct {
	// Connector is the TakeCareConnector's name, in evidence.
	Connector string
	// Cluster is the workload cluster's name, "" for the unnamed one (ADR
	// 0016 §5).
	Cluster  string
	Endpoint mcp.Endpoint
	// Namespaces surveyed; empty: every namespace namespaces_list returns
	// but the kube-* ones.
	Namespaces []string
	// Agents says the connector may list kagent Agents: its add-on has the
	// opt-in read rule on kagent.dev agents (spec 10 §8).
	Agents bool
}

// Metrics is the metrics connector whose rules tool lists the alerting
// rules Prometheus has loaded (spec 10 §4).
type Metrics struct {
	// Connector is the TakeCareConnector's name, in evidence.
	Connector string
	Endpoint  mcp.Endpoint
}

// Index is the Map as the survey starts: its entries by key and by
// kubernetes source.
type Index struct {
	entries  map[string]mapserver.Entry
	bySource map[string][]string
}

// NewIndex indexes the Map's entries.
func NewIndex(entries []mapserver.Entry) *Index {
	ix := &Index{entries: map[string]mapserver.Entry{}, bySource: map[string][]string{}}
	for _, e := range entries {
		ix.entries[e.Key] = e
		for _, s := range e.Sources {
			if ref, ok := s[sourceKubernetes]; ok {
				ix.bySource[ref] = append(ix.bySource[ref], e.Key)
			}
		}
	}
	for ref := range ix.bySource {
		slices.Sort(ix.bySource[ref])
	}
	return ix
}

// matching lists the entries of a type with this kubernetes source.
func (ix *Index) matching(ref, typ string) []string {
	return slices.DeleteFunc(slices.Clone(ix.bySource[ref]), func(k string) bool { return !strings.HasPrefix(k, typ+"/") })
}

// Layer is what one layer of the ground gave.
type Layer struct {
	Name     string    `json:"name"`
	Seen     int32     `json:"seen"`
	Matched  int32     `json:"matched"`
	Proposed int32     `json:"proposed"`
	NotSeen  []NotSeen `json:"notSeen,omitempty"`
}

// NotSeen is what a connector could not read, with its reason.
type NotSeen struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

// Proposal is one change proposed to the Map (spec 10 §7).
type Proposal struct {
	ID       string                  `json:"id"`
	Action   v1alpha1.ProposalAction `json:"action"`
	Key      string                  `json:"key"`
	Entry    string                  `json:"entry"`
	Evidence []string                `json:"evidence"`
	Source   v1alpha1.ProposalSource `json:"source"`
	// Layer is the layer that made the proposal.
	Layer string `json:"-"`
	layer int
}

// Result is the import's outcome.
type Result struct {
	Layers    []Layer    `json:"layers"`
	Proposals []Proposal `json:"proposals"`
}

// ProposalID is a proposal's stable id: the first 16 hex characters of
// sha256(action + key).
func ProposalID(action v1alpha1.ProposalAction, key string) string {
	sum := sha256.Sum256([]byte(string(action) + key))
	return hex.EncodeToString(sum[:])[:16]
}

// seenObject is a listed object on a ground.
type seenObject struct {
	object
	cluster, connector string
}

func (o seenObject) ref() string { return clusterRef(o.cluster, o.Namespace+"/"+o.Kind+"/"+o.Name) }

func clusterRef(cluster, ref string) string {
	if cluster == "" {
		return ref
	}
	return cluster + "/" + ref
}

func envKey(cluster, namespace string) string {
	if cluster == "" {
		return "environment/" + namespace
	}
	return "environment/" + cluster + "." + namespace
}

// run holds one import's state.
type run struct {
	ix     *Index
	today  string
	layers map[string]*Layer
	// seen: the object and namespace refs listed. surveyed: the
	// "[cluster/]namespace/Kind" lists that succeeded. listedClusters: the
	// clusters whose namespaces were listed.
	seen, surveyed, listedClusters map[string]bool
	objects                        map[string][]seenObject // by layer
	namespaces                     []seenObject            // Namespace only
	proposals                      []Proposal
	proposedKeys                   map[string]bool
	// computeKeys: the key proposed for a compute object, by its ref.
	computeKeys map[string]string
	// updates: the entries the survey changes, gathered across layers
	// and proposed once each.
	updates map[string]*pending
}

// pending is an entry changed by one or more layers.
type pending struct {
	entry    mapserver.Entry
	layer    *Layer
	evidence []string
}

// Observer hears when the import's calls run, for a trace: each ground's
// listing and the rules' read, with their start and end. Either may be nil.
type Observer struct {
	Ground func(g Ground, start, end time.Time)
	Rules  func(start, end time.Time)
}

// Import surveys the grounds against the Map and returns what it saw and
// proposes. It returns an error only when ctx ends; everything else it
// could not read is a notSeen line.
func Import(ctx context.Context, caller Caller, grounds []Ground, metrics *Metrics, ix *Index, now time.Time, obs ...Observer) (Result, error) {
	var o Observer
	if len(obs) > 0 {
		o = obs[0]
	}
	r := &run{
		ix: ix, today: now.UTC().Format(time.DateOnly),
		layers: map[string]*Layer{}, seen: map[string]bool{}, surveyed: map[string]bool{}, listedClusters: map[string]bool{},
		objects: map[string][]seenObject{}, proposedKeys: map[string]bool{}, computeKeys: map[string]string{},
		updates: map[string]*pending{},
	}
	for _, name := range layerOrder {
		r.layers[name] = &Layer{Name: name}
	}
	for _, g := range grounds {
		start := time.Now()
		if err := r.list(ctx, caller, g); err != nil {
			return Result{}, err
		}
		if o.Ground != nil {
			o.Ground(g, start, time.Now())
		}
	}
	r.environments()
	for _, k := range []string{LayerCompute, LayerNetwork, LayerIdentity, LayerStorage} {
		r.objectsOf(k)
	}
	r.services()
	start := time.Now()
	if err := r.alerting(ctx, caller, metrics); err != nil {
		return Result{}, err
	}
	if o.Rules != nil && metrics != nil {
		o.Rules(start, time.Now())
	}
	r.flushUpdates()
	r.removals()
	// By layer, then key: the order a reviewer reads them in.
	slices.SortStableFunc(r.proposals, func(a, b Proposal) int {
		if a.layer != b.layer {
			return a.layer - b.layer
		}
		return strings.Compare(a.Key, b.Key)
	})
	out := Result{Proposals: r.proposals}
	for _, name := range layerOrder {
		out.Layers = append(out.Layers, *r.layers[name])
	}
	return out, nil
}

// list reads one ground: its namespaces, then every kind in each.
func (r *run) list(ctx context.Context, caller Caller, g Ground) error {
	listed, reason, err := call(ctx, caller, g.Endpoint, toolNamespaces, map[string]any{})
	if err != nil {
		return err
	}
	var existing []string
	if reason == "" {
		if existing, err = parseNamespaces(listed); err != nil {
			reason = fmt.Sprintf(reasonUnrecognized, toolNamespaces)
		}
	}
	if reason != "" {
		r.notSeen(LayerEnvironment, g.Connector, reason)
		if len(g.Namespaces) == 0 {
			return nil
		}
	} else {
		// Every namespace the cluster has exists, in scope or not: an
		// environment entry of one is never proposed for removal.
		r.listedClusters[g.Cluster] = true
		for _, ns := range existing {
			r.seen[clusterRef(g.Cluster, ns)] = true
		}
	}
	namespaces := g.Namespaces
	if len(namespaces) == 0 {
		namespaces = slices.DeleteFunc(slices.Clone(existing), func(ns string) bool { return strings.HasPrefix(ns, "kube-") })
	}
	slices.Sort(namespaces)
	notServed := map[string]bool{}
	for _, ns := range namespaces {
		if reason == "" && !slices.Contains(existing, ns) {
			r.notSeen(LayerEnvironment, clusterRef(g.Cluster, ns), reasonNoNamespace)
			continue
		}
		if reason == "" {
			r.seen[clusterRef(g.Cluster, ns)] = true
			r.namespaces = append(r.namespaces, seenObject{Namespace: ns, cluster: g.Cluster, connector: g.Connector})
		}
		for _, k := range kinds {
			if notServed[k.kind] {
				continue
			}
			list := clusterRef(g.Cluster, ns+"/"+k.kind)
			res, why, err := call(ctx, caller, g.Endpoint, toolList, map[string]any{"apiVersion": k.apiVersion, "kind": k.kind, "namespace": ns})
			if err != nil {
				return err
			}
			var objs []object
			if why == "" {
				if objs, err = parseList(res, k.apiVersion, k.kind, ns); err != nil {
					why = fmt.Sprintf(reasonUnrecognized, toolList)
				}
			}
			switch {
			case why == reasonNotServed:
				// Once per kind on a cluster: the API is the cluster's.
				notServed[k.kind] = true
				r.notSeen(k.layer, clusterRef(g.Cluster, k.kind), why)
				continue
			case why != "":
				r.notSeen(k.layer, list, why)
				continue
			}
			r.surveyed[list] = true
			for _, o := range objs {
				if k.kind == "ServiceAccount" && o.Name == "default" {
					continue // every namespace has one: not an identity of the ground's
				}
				so := seenObject{object: o, cluster: g.Cluster, connector: g.Connector}
				r.seen[so.ref()] = true
				r.objects[k.layer] = append(r.objects[k.layer], so)
			}
		}
	}
	return nil
}

// call runs a tool and classes its failure: a reason for a notSeen line,
// or an error when ctx ended.
func call(ctx context.Context, caller Caller, ep mcp.Endpoint, tool string, args map[string]any) (mcp.Result, string, error) {
	res, err := caller.Call(ctx, ep, tool, args)
	switch {
	case ctx.Err() != nil:
		return res, "", fmt.Errorf("survey %s: %w", tool, ctx.Err())
	case errors.Is(err, mcp.ErrCallForbidden):
		return res, reasonNotAllowed, nil
	case err != nil:
		return res, reasonCallFailed, nil
	case res.IsError:
		return res, failure(res), nil
	}
	return res, "", nil
}

func (r *run) notSeen(layer, ref, reason string) {
	l := r.layers[layer]
	l.NotSeen = append(l.NotSeen, NotSeen{Ref: ref, Reason: reason})
}

// environments proposes an environment entry per namespace seen.
func (r *run) environments() {
	l := r.layers[LayerEnvironment]
	for _, ns := range r.namespaces {
		l.Seen++
		ref := clusterRef(ns.cluster, ns.Namespace)
		evidence := []string{ns.connector + "/" + toolNamespaces}
		if keys := r.ix.matching(ref, "environment"); len(keys) > 0 {
			l.Matched++
			for _, k := range keys {
				r.update(l, k, evidence, nil)
			}
			continue
		}
		key := envKey(ns.cluster, ns.Namespace)
		if _, taken := r.ix.entries[key]; taken {
			continue // a declared entry of that key maps elsewhere: a human's call
		}
		r.add(l, mapserver.Entry{Key: key, Sources: []map[string]string{{sourceKubernetes: ref}}}, evidence)
	}
}

// objectsOf proposes or updates the entries of one layer's objects.
func (r *run) objectsOf(layer string) {
	l := r.layers[layer]
	names := map[string]int{}
	for _, o := range r.objects[layer] {
		names[o.Name]++
	}
	for _, o := range r.objects[layer] {
		l.Seen++
		evidence := []string{o.connector + "/" + toolList}
		var relations mapserver.Relations
		if deployedIn[layer] {
			relations.DeployedIn = []string{r.environmentOf(o)}
		}
		if keys := r.ix.matching(o.ref(), layer); len(keys) > 0 {
			l.Matched++
			for _, k := range keys {
				r.update(l, k, evidence, &relations)
			}
			continue
		}
		// Two objects of one name in the ground, or a name the Map already
		// gives another entry, are qualified by their namespace (and
		// cluster): compute/orders.shop, compute/orders.east.shop.
		key := layer + "/" + o.Name
		if _, taken := r.ix.entries[key]; taken || names[o.Name] > 1 {
			key = layer + "/" + o.Name + "." + strings.ReplaceAll(clusterRef(o.cluster, o.Namespace), "/", ".")
		}
		if _, taken := r.ix.entries[key]; taken || r.proposedKeys[key] {
			continue
		}
		if layer == LayerCompute {
			r.computeKeys[o.ref()] = key
		}
		r.add(l, mapserver.Entry{Key: key, Sources: []map[string]string{{sourceKubernetes: o.ref()}}, Relations: relations}, evidence)
	}
}

// environmentOf is the key of the entry an object is deployed in: the
// Map's entry for its namespace, or the one the import proposes.
func (r *run) environmentOf(o seenObject) string {
	if keys := r.ix.matching(clusterRef(o.cluster, o.Namespace), "environment"); len(keys) > 0 {
		return keys[0]
	}
	return envKey(o.cluster, o.Namespace)
}

// services proposes a service per app.kubernetes.io/part-of (or, without
// it, app.kubernetes.io/name) of the compute objects no service entry
// maps, running as their compute entries. A service entry already mapping
// a workload is matched and kept up to date.
func (r *run) services() {
	l := r.layers[LayerService]
	type group struct {
		refs, runsAs []string
		evidence     []string
	}
	groups := map[string]*group{}
	for _, o := range r.objects[LayerCompute] {
		evidence := []string{o.connector + "/" + toolList}
		if keys := r.ix.matching(o.ref(), "service"); len(keys) > 0 {
			l.Seen++
			l.Matched++
			for _, k := range keys {
				r.update(l, k, evidence, nil)
			}
			continue
		}
		name := o.PartOf
		if name == "" {
			name = o.AppName
		}
		if name == "" {
			continue
		}
		g, ok := groups[name]
		if !ok {
			g = &group{}
			groups[name] = g
			l.Seen++
		}
		g.refs = append(g.refs, o.ref())
		if compute := r.computeOf(o); compute != "" {
			g.runsAs = append(g.runsAs, compute)
		}
		g.evidence = evidence
	}
	for _, name := range slices.Sorted(maps.Keys(groups)) {
		key := "service/" + name
		if _, taken := r.ix.entries[key]; taken {
			continue // the Map's service of that name maps other workloads: a human's call
		}
		g := groups[name]
		e := mapserver.Entry{Key: key, Relations: mapserver.Relations{RunsAs: g.runsAs}}
		for _, ref := range g.refs {
			e.Sources = append(e.Sources, map[string]string{sourceKubernetes: ref})
		}
		r.add(l, e, g.evidence)
	}
}

// computeOf is the key of an object's compute entry: the Map's, or the one
// the import proposes for it.
func (r *run) computeOf(o seenObject) string {
	if keys := r.ix.matching(o.ref(), "compute"); len(keys) > 0 {
		return keys[0]
	}
	return r.computeKeys[o.ref()]
}

// removals proposes removing a validated entry whose kubernetes sources
// were all surveyed and none seen. A source of a kind or namespace the
// survey could not list keeps the entry.
func (r *run) removals() {
	for _, key := range slices.Sorted(maps.Keys(r.ix.entries)) {
		e := r.ix.entries[key]
		if e.Status != "validated" {
			continue
		}
		var refs []string
		for _, s := range e.Sources {
			if ref, ok := s[sourceKubernetes]; ok {
				refs = append(refs, ref)
			}
		}
		if len(refs) == 0 {
			continue
		}
		gone, layer := true, ""
		for _, ref := range refs {
			src, ok := mapschema.ParseKubernetes(ref)
			if !ok {
				gone = false
				break
			}
			surveyed := r.listedClusters[src.Cluster]
			if src.Kind != "" {
				surveyed = r.surveyed[clusterRef(src.Cluster, src.Namespace+"/"+src.Kind)]
			}
			if !surveyed || r.seen[ref] {
				gone = false
				break
			}
			layer = layerOfKind(src.Kind)
		}
		if !gone || layer == "" {
			continue
		}
		evidence := []string{toolNamespaces}
		if layer != LayerEnvironment {
			evidence = []string{toolList}
		}
		r.propose(r.layers[layer], v1alpha1.ProposalRemove, e, evidence)
	}
}

func layerOfKind(k string) string {
	if k == "" {
		return LayerEnvironment
	}
	for _, kk := range kinds {
		if kk.kind == k {
			return kk.layer
		}
	}
	return ""
}

// add proposes a new entry, discovered and proposed.
func (r *run) add(l *Layer, e mapserver.Entry, evidence []string) {
	e.Provenance, e.Status, e.Created, e.LastSeen = "discovered", "proposed", r.today, r.today
	r.propose(l, v1alpha1.ProposalAdd, e, evidence)
}

// update gathers the deployedIn relations the import draws on an existing
// entry seen again.
func (r *run) update(l *Layer, key string, evidence []string, relations *mapserver.Relations) {
	if relations == nil {
		return
	}
	r.change(l, key, evidence, func(e *mapserver.Entry) bool {
		changed := false
		for _, to := range relations.DeployedIn {
			if !slices.Contains(e.Relations.DeployedIn, to) {
				e.Relations.DeployedIn = append(e.Relations.DeployedIn, to)
				changed = true
			}
		}
		return changed
	})
}

// change applies a layer's change to an existing entry, on top of the
// changes other layers made to it; the first layer that changes it owns
// the proposal, and evidence accumulates.
func (r *run) change(l *Layer, key string, evidence []string, mutate func(*mapserver.Entry) bool) {
	p, ok := r.updates[key]
	if !ok {
		p = &pending{entry: clone(r.ix.entries[key])}
	}
	if !mutate(&p.entry) {
		return
	}
	if !ok {
		p.layer = l
		r.updates[key] = p
	}
	for _, ev := range evidence {
		if !slices.Contains(p.evidence, ev) {
			p.evidence = append(p.evidence, ev)
		}
	}
}

// flushUpdates proposes every changed entry once, with lastSeen refreshed.
// A survey never proposes a change of lastSeen alone: the review would
// touch the whole Map every day and hide the real changes.
func (r *run) flushUpdates() {
	for _, key := range slices.Sorted(maps.Keys(r.updates)) {
		p := r.updates[key]
		p.entry.LastSeen = r.today
		r.propose(p.layer, v1alpha1.ProposalUpdate, p.entry, p.evidence)
	}
}

// clone copies an entry deeply, so that a change never touches the index.
func clone(e mapserver.Entry) mapserver.Entry {
	raw, err := json.Marshal(e)
	if err != nil {
		return e // an Entry always marshals
	}
	var c mapserver.Entry
	if err := json.Unmarshal(raw, &c); err != nil {
		return e
	}
	return c
}

func (r *run) propose(l *Layer, action v1alpha1.ProposalAction, e mapserver.Entry, evidence []string) {
	if r.proposedKeys[e.Key] {
		return
	}
	raw, err := yaml.Marshal(e)
	if err != nil {
		return // an Entry always marshals
	}
	r.proposedKeys[e.Key] = true
	l.Proposed++
	r.proposals = append(r.proposals, Proposal{
		ID: ProposalID(action, e.Key), Action: action, Key: e.Key, Entry: string(raw),
		Evidence: evidence, Source: v1alpha1.ProposalFromImport, Layer: l.Name, layer: slices.Index(layerOrder, l.Name),
	})
}
