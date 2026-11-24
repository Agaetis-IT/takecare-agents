// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package azure is the Resource Graph importer of the Intendant's survey
// (spec 10 §4.1): operator code, no model, no Azure SDK. It reads resource
// groups and resources of a fixed list of types through a Querier, over the
// subscriptions and groups of a cloud connector's reach (ADR 0021 §5), and
// returns per-layer counts, what it could not read, and proposals for the
// Map, in the shape of the kubernetes import's result.
//
// It is not wired into the survey yet: no Querier exists until a cloud
// add-on serves one. The answers it parses are Resource Graph's
// QueryResponse at API version 2024-04-01, parsed strictly; a shape it does
// not know is a notSeen line, never a guess. Of a row it keeps the ID,
// name, type, subscription and resource group, nothing else: no properties,
// no tags.
package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	mapschema "github.com/agaetis-it/takecare-agents/api/map"
	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/coverage"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
)

// LayerDatastore is the layer of databases and caches, which a kubernetes
// ground does not list.
const LayerDatastore = "datastore"

const (
	sourceAzure    = "azure"
	tableGroups    = "ResourceContainers"
	tableResources = "Resources"
)

var layerOrder = []string{
	discovery.LayerEnvironment, discovery.LayerCompute, LayerDatastore,
	discovery.LayerNetwork, discovery.LayerIdentity, discovery.LayerStorage,
}

// resourceLayers are the layers the resources query fills.
var resourceLayers = layerOrder[1:]

// types maps a resource type, lower case as Resource Graph returns it, to
// its layer. Disks, network interfaces, security groups and virtual
// networks are left out as noise; managed clusters are grounds read through
// their own front.
var types = map[string]string{
	"microsoft.compute/virtualmachines":                discovery.LayerCompute,
	"microsoft.compute/virtualmachinescalesets":        discovery.LayerCompute,
	"microsoft.web/sites":                              discovery.LayerCompute,
	"microsoft.app/containerapps":                      discovery.LayerCompute,
	"microsoft.containerinstance/containergroups":      discovery.LayerCompute,
	"microsoft.sql/servers/databases":                  LayerDatastore,
	"microsoft.dbforpostgresql/flexibleservers":        LayerDatastore,
	"microsoft.dbformysql/flexibleservers":             LayerDatastore,
	"microsoft.documentdb/databaseaccounts":            LayerDatastore,
	"microsoft.cache/redis":                            LayerDatastore,
	"microsoft.storage/storageaccounts":                discovery.LayerStorage,
	"microsoft.network/loadbalancers":                  discovery.LayerNetwork,
	"microsoft.network/applicationgateways":            discovery.LayerNetwork,
	"microsoft.network/dnszones":                       discovery.LayerNetwork,
	"microsoft.cdn/profiles":                           discovery.LayerNetwork,
	"microsoft.apimanagement/service":                  discovery.LayerNetwork,
	"microsoft.managedidentity/userassignedidentities": discovery.LayerIdentity,
}

func mappedTypes() []string { return slices.Sorted(maps.Keys(types)) }

// deployedIn: the layers spec 08 §2 draws deployedIn from.
var deployedIn = map[string]bool{discovery.LayerCompute: true, LayerDatastore: true, discovery.LayerNetwork: true}

// Ground is one cloud connector the import reads through.
type Ground struct {
	// Connector is the TakeCareConnector's name, in evidence.
	Connector string
	Reach     v1alpha1.AzureReach
}

// scope is one request's scope: whole subscriptions, or one subscription
// narrowed to groups.
type scope struct {
	subscriptions, groups []string
}

// seen is a row kept, with the connector that read it.
type seen struct {
	row
	scope     mapschema.AzureScope
	connector string
	layer     string
	key       string
}

type run struct {
	today  string
	layers map[string]*discovery.Layer
	byKey  map[string]mapserver.Entry
	// bySource: entry keys by azure source, the ARM ID lower-cased.
	bySource map[string][]string
	// ids: the lower-cased IDs kept. groupsRead, resourcesRead: the
	// "<sub>" and "<sub>/<rg>" scopes whose query read every row.
	ids, groupsRead, resourcesRead map[string]bool
	groups, resources              []*seen
	// envs: the environment key of a "<sub>/<rg>" group.
	envs map[string]string
	// reserved: the new keys assignKeys gave out.
	reserved     map[string]bool
	proposals    []discovery.Proposal
	proposedKeys map[string]bool
	updates      map[string]*pending
}

type pending struct {
	entry    mapserver.Entry
	layer    string
	evidence []string
}

// Import surveys the grounds' reaches against the Map's entries and
// returns what it saw and proposes. It returns an error only when ctx
// ends; everything else it could not read is a notSeen line.
func Import(ctx context.Context, q Querier, grounds []Ground, entries []mapserver.Entry, now time.Time) (discovery.Result, error) {
	r := &run{
		today: now.UTC().Format(time.DateOnly), layers: map[string]*discovery.Layer{},
		byKey: map[string]mapserver.Entry{}, bySource: map[string][]string{},
		ids: map[string]bool{}, groupsRead: map[string]bool{}, resourcesRead: map[string]bool{},
		envs: map[string]string{}, reserved: map[string]bool{}, proposedKeys: map[string]bool{}, updates: map[string]*pending{},
	}
	for _, name := range layerOrder {
		r.layers[name] = &discovery.Layer{Name: name}
	}
	for _, e := range entries {
		r.byKey[e.Key] = e
		for _, s := range e.Sources {
			if id, ok := s[sourceAzure]; ok {
				r.bySource[strings.ToLower(id)] = append(r.bySource[strings.ToLower(id)], e.Key)
			}
		}
	}
	for _, g := range grounds {
		if err := r.read(ctx, q, g); err != nil {
			return discovery.Result{}, err
		}
	}
	r.environments()
	r.objects()
	r.flushUpdates()
	r.removals()
	slices.SortStableFunc(r.proposals, func(a, b discovery.Proposal) int {
		if la, lb := slices.Index(layerOrder, a.Layer), slices.Index(layerOrder, b.Layer); la != lb {
			return la - lb
		}
		return strings.Compare(a.Key, b.Key)
	})
	out := discovery.Result{Proposals: r.proposals}
	for _, name := range layerOrder {
		out.Layers = append(out.Layers, *r.layers[name])
	}
	return out, nil
}

// plan splits a reach into requests: whole subscriptions together, at most
// maxSubscriptions a request, and each narrowed subscription alone. A
// group name that does not follow Azure's rule is not queried, and a
// subscription left with none is not queried at all: never wider than the
// reach.
func (r *run) plan(g Ground) []scope {
	var whole []string
	var out []scope
	for _, s := range g.Reach.Subscriptions {
		sub := strings.ToLower(s.ID)
		if len(s.ResourceGroups) == 0 {
			whole = append(whole, sub)
			continue
		}
		var groups []string
		for _, rg := range s.ResourceGroups {
			if !groupName.MatchString(string(rg)) {
				r.notSeen(discovery.LayerEnvironment, g.Connector+"/subscriptions/"+sub, reasonGroupName)
				continue
			}
			groups = append(groups, strings.ToLower(string(rg)))
		}
		if len(groups) > 0 {
			out = append(out, scope{subscriptions: []string{sub}, groups: groups})
		}
	}
	var plan []scope
	for chunk := range slices.Chunk(whole, maxSubscriptions) {
		plan = append(plan, scope{subscriptions: chunk})
	}
	return append(plan, out...)
}

// read runs both queries over every scope of a ground's reach and keeps
// the rows its reach holds.
func (r *run) read(ctx context.Context, q Querier, g Ground) error {
	reach := coverage.Reach{Connector: g.Connector, Azure: &g.Reach}
	dropped := map[string]int{}
	for _, sc := range r.plan(g) {
		for _, resources := range []bool{false, true} {
			req := Request{Query: groupsQuery(sc.groups), Subscriptions: sc.subscriptions}
			table, read := tableGroups, r.groupsRead
			if resources {
				req.Query, table, read = resourcesQuery(sc.groups), tableResources, r.resourcesRead
			}
			rows, why, err := readAll(ctx, q, req, resources)
			if err != nil {
				return err
			}
			for _, row := range rows {
				if !r.keep(reach, row, resources) {
					dropped[table]++
				}
			}
			if why != "" {
				layers := []string{discovery.LayerEnvironment}
				if resources {
					layers = resourceLayers
				}
				for _, sub := range sc.subscriptions {
					for _, l := range layers {
						r.notSeen(l, g.Connector+"/subscriptions/"+sub, why)
					}
				}
				continue
			}
			for _, sub := range sc.subscriptions {
				if len(sc.groups) == 0 {
					read[sub] = true
				}
				for _, rg := range sc.groups {
					read[sub+"/"+rg] = true
				}
			}
		}
	}
	for _, table := range []string{tableGroups, tableResources} {
		if dropped[table] > 0 {
			r.notSeen(discovery.LayerEnvironment, g.Connector+"/"+table, fmt.Sprintf(reasonOutside, dropped[table]))
		}
	}
	return nil
}

// keep checks a row against the reach again (ADR 0021 §6) and its shape,
// and keeps it once. It says false for a row it drops.
func (r *run) keep(reach coverage.Reach, rw row, resources bool) bool {
	sc, ok := mapschema.ParseAzure(rw.ID)
	if !ok || sc.Subscription != rw.Subscription || !reach.Holds(sc) || sc.ResourceGroup == "" {
		return false
	}
	layer := discovery.LayerEnvironment
	if resources {
		layer = types[rw.Type]
		if layer == "" || sc.ResourceGroup != rw.ResourceGroup {
			return false
		}
	} else if strings.Count(rw.ID, "/") != 4 {
		return false // a group's ID is /subscriptions/<id>/resourceGroups/<name>
	}
	id := strings.ToLower(rw.ID)
	if r.ids[id] {
		return true // read through another connector too
	}
	r.ids[id] = true
	s := &seen{row: rw, scope: sc, connector: reach.Connector, layer: layer}
	if resources {
		r.resources = append(r.resources, s)
	} else {
		r.groups = append(r.groups, s)
	}
	return true
}

func (r *run) notSeen(layer, ref, reason string) {
	l := r.layers[layer]
	l.NotSeen = append(l.NotSeen, discovery.NotSeen{Ref: ref, Reason: reason})
}

// matching lists the entries of a layer with this azure source.
func (r *run) matching(id, layer string) []string {
	return slices.DeleteFunc(slices.Clone(r.bySource[strings.ToLower(id)]), func(k string) bool { return !strings.HasPrefix(k, layer+"/") })
}

// environments matches or proposes an environment entry per group.
func (r *run) environments() {
	l := r.layers[discovery.LayerEnvironment]
	var fresh []*seen
	for _, s := range r.groups {
		l.Seen++
		if keys := r.matching(s.ID, l.Name); len(keys) > 0 {
			l.Matched++
			r.envs[s.scope.Subscription+"/"+s.scope.ResourceGroup] = keys[0]
			continue
		}
		fresh = append(fresh, s)
	}
	r.assignKeys(fresh)
	for _, s := range fresh {
		if s.key == "" {
			continue
		}
		r.envs[s.scope.Subscription+"/"+s.scope.ResourceGroup] = s.key
		r.add(l, mapserver.Entry{Key: s.key, Sources: []map[string]string{{sourceAzure: s.ID}}}, []string{s.connector + "/" + tableGroups})
	}
}

// objects matches or proposes an entry per resource, deployed in its
// group's environment when the survey knows it.
func (r *run) objects() {
	var fresh []*seen
	for _, s := range r.resources {
		l := r.layers[s.layer]
		l.Seen++
		if keys := r.matching(s.ID, s.layer); len(keys) > 0 {
			l.Matched++
			for _, k := range keys {
				r.update(s, k)
			}
			continue
		}
		fresh = append(fresh, s)
	}
	r.assignKeys(fresh)
	for _, s := range fresh {
		if s.key == "" {
			continue
		}
		e := mapserver.Entry{Key: s.key, Sources: []map[string]string{{sourceAzure: s.ID}}}
		if env := r.envOf(s); env != "" {
			e.Relations.DeployedIn = []string{env}
		}
		r.add(r.layers[s.layer], e, []string{s.connector + "/" + tableResources})
	}
}

func (r *run) envOf(s *seen) string {
	if !deployedIn[s.layer] {
		return ""
	}
	return r.envs[s.scope.Subscription+"/"+s.scope.ResourceGroup]
}

// keyName turns an Azure name into a key's name: lower case, and every
// character the key pattern refuses turned to "-".
func keyName(name string) string {
	b := []byte(strings.ToLower(name))
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '-' {
			b[i] = '-'
		}
	}
	return strings.Trim(string(b), "-.")
}

// assignKeys gives each new row its key: <layer>/<name>. Rows whose name
// collides with another's in the survey, or with an entry of the Map, are
// qualified: by their resource group when that tells them apart, else by
// the first 8 hex characters of their subscription, else by both; an
// environment, a group itself, only by its subscription. A key still
// taken is not proposed, and the layer says it.
func (r *run) assignKeys(rows []*seen) {
	byBase := map[string][]*seen{}
	for _, s := range rows {
		base := s.layer + "/" + keyName(s.Name)
		byBase[base] = append(byBase[base], s)
	}
	for _, base := range slices.Sorted(maps.Keys(byBase)) {
		set := byBase[base]
		_, taken := r.byKey[base]
		for _, s := range set {
			s.key = base
		}
		if len(set) > 1 || taken {
			qualify(base, set)
		}
		for _, s := range set {
			if _, held := r.byKey[s.key]; held || r.reserved[s.key] || slices.ContainsFunc(set, func(o *seen) bool { return o != s && o.key == s.key }) {
				r.notSeen(s.layer, s.ID, reasonKeyTaken)
				s.key = ""
				continue
			}
		}
		for _, s := range set {
			if s.key != "" {
				r.reserved[s.key] = true
			}
		}
	}
}

func qualify(base string, set []*seen) {
	distinct := func(f func(*seen) string) bool {
		vals := map[string]bool{}
		for _, s := range set {
			vals[f(s)] = true
		}
		return len(vals) == len(set)
	}
	group := func(s *seen) string { return keyName(s.scope.ResourceGroup) }
	sub := func(s *seen) string { return s.scope.Subscription[:min(8, len(s.scope.Subscription))] }
	for _, s := range set {
		switch {
		case set[0].layer == discovery.LayerEnvironment:
			s.key = base + "-" + sub(s)
		case distinct(group):
			s.key = base + "-" + group(s)
		case distinct(sub):
			s.key = base + "-" + sub(s)
		default:
			s.key = base + "-" + group(s) + "-" + sub(s)
		}
	}
}

// add proposes a new entry, discovered and proposed.
func (r *run) add(l *discovery.Layer, e mapserver.Entry, evidence []string) {
	e.Provenance, e.Status, e.Created, e.LastSeen = "discovered", "proposed", r.today, r.today
	r.propose(l, v1alpha1.ProposalAdd, e, evidence)
}

// update draws the deployedIn relation on an existing entry seen again,
// when it lacks it.
func (r *run) update(s *seen, key string) {
	env := r.envOf(s)
	if env == "" {
		return
	}
	p, ok := r.updates[key]
	if !ok {
		p = &pending{entry: clone(r.byKey[key]), layer: s.layer}
	}
	if slices.Contains(p.entry.Relations.DeployedIn, env) {
		return
	}
	p.entry.Relations.DeployedIn = append(p.entry.Relations.DeployedIn, env)
	if ev := s.connector + "/" + tableResources; !slices.Contains(p.evidence, ev) {
		p.evidence = append(p.evidence, ev)
	}
	r.updates[key] = p
}

// flushUpdates proposes every changed entry once, lastSeen refreshed.
func (r *run) flushUpdates() {
	for _, key := range slices.Sorted(maps.Keys(r.updates)) {
		p := r.updates[key]
		p.entry.LastSeen = r.today
		r.propose(r.layers[p.layer], v1alpha1.ProposalUpdate, p.entry, p.evidence)
	}
}

// removals proposes removing a validated entry of the import's layers
// whose sources are all azure, all inside a scope whose query read every
// row, and none seen. An entry with another kind of source, or a source of
// a type the import does not list, is kept.
func (r *run) removals() {
	for _, key := range slices.Sorted(maps.Keys(r.byKey)) {
		e := r.byKey[key]
		layer, _, _ := strings.Cut(key, "/")
		if e.Status != "validated" || !slices.Contains(layerOrder, layer) || len(e.Sources) == 0 {
			continue
		}
		gone := true
		for _, s := range e.Sources {
			id, ok := s[sourceAzure]
			if !ok || !r.surveyed(id, layer) || r.ids[strings.ToLower(id)] {
				gone = false
				break
			}
		}
		if gone {
			table := tableResources
			if layer == discovery.LayerEnvironment {
				table = tableGroups
			}
			r.propose(r.layers[layer], v1alpha1.ProposalRemove, e, []string{table})
		}
	}
}

// surveyed says whether an ID of that layer sits in a scope read whole: a
// group in a groups scope, a resource of a listed type in a resources
// scope.
func (r *run) surveyed(id, layer string) bool {
	sc, ok := mapschema.ParseAzure(id)
	if !ok || sc.ResourceGroup == "" {
		return false
	}
	read := r.resourcesRead
	if layer == discovery.LayerEnvironment {
		if strings.Count(id, "/") != 4 {
			return false
		}
		read = r.groupsRead
	} else if types[armType(id)] != layer {
		return false
	}
	return read[sc.Subscription] || read[sc.Subscription+"/"+sc.ResourceGroup]
}

// armType is a resource ID's type, lower case: its provider namespace and
// every type segment after it, /providers/<ns>/<type>/<name>[/<type>/<name>].
func armType(id string) string {
	p := strings.Split(strings.ToLower(id), "/")
	i := slices.Index(p, "providers")
	if i < 0 || i+3 >= len(p) || slices.Index(p[i+1:], "providers") >= 0 {
		return ""
	}
	t := []string{p[i+1]}
	for j := i + 2; j < len(p); j += 2 {
		t = append(t, p[j])
	}
	return strings.Join(t, "/")
}

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

func (r *run) propose(l *discovery.Layer, action v1alpha1.ProposalAction, e mapserver.Entry, evidence []string) {
	if r.proposedKeys[e.Key] {
		return
	}
	if err := mapserver.ValidateEntry(e); err != nil {
		r.notSeen(l.Name, e.Key, reasonInvalidEntry)
		return
	}
	raw, err := yaml.Marshal(e)
	if err != nil {
		return // an Entry always marshals
	}
	r.proposedKeys[e.Key] = true
	l.Proposed++
	r.proposals = append(r.proposals, discovery.Proposal{
		ID: discovery.ProposalID(action, e.Key), Action: action, Key: e.Key, Entry: string(raw),
		Evidence: evidence, Source: v1alpha1.ProposalFromImport, Layer: l.Name,
	})
}
