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
	// ids: the lower-cased IDs kept; dropped: those of the rows dropped,
	// which Resource Graph did return, so they are not gone.
	ids, dropped map[string]bool
	// groupsRead, resourcesRead: the "<sub>" and "<sub>/<rg>" scopes a
	// connector's own groups query read whole and showed a group in, with
	// that connector; resourcesRead also needs its resources query read
	// whole. A resource row or another connector's row never marks one.
	groupsRead, resourcesRead map[string]string
	groups, resources         []*seen
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
		ids: map[string]bool{}, dropped: map[string]bool{},
		groupsRead: map[string]string{}, resourcesRead: map[string]string{},
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
				r.notSeen(discovery.LayerEnvironment, g.Connector+"/subscriptions/"+sub+"/resourceGroups/"+string(rg), reasonGroupName)
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
// the rows its reach holds. A scope whose groups query read every row but
// returned no group, a whole subscription, or not the group named, a
// narrowed one, is a notSeen line: Resource Graph answers only with what
// the identity may read, so it was not read, or it is empty or gone.
func (r *run) read(ctx context.Context, q Querier, g Ground) error {
	reach := coverage.Reach{Connector: g.Connector, Azure: &g.Reach}
	dropped := map[string]int{}
	visible := map[string]bool{}
	var silent []string
	for _, sc := range r.plan(g) {
		complete := map[bool]bool{}
		for _, resources := range []bool{false, true} {
			req := Request{Query: groupsQuery(sc.groups), Subscriptions: sc.subscriptions}
			table := tableGroups
			if resources {
				req.Query, table = resourcesQuery(sc.groups), tableResources
			}
			rows, why, err := readAll(ctx, q, req, resources)
			if err != nil {
				return err
			}
			for _, row := range rows {
				sc, ok := r.keep(reach, row, resources)
				switch {
				case !ok:
					dropped[table]++
				case !resources:
					visible[sc.Subscription], visible[sc.Subscription+"/"+sc.ResourceGroup] = true, true
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
			complete[resources] = true
		}
		if !complete[false] {
			continue
		}
		for _, sub := range sc.subscriptions {
			keys, refs := []string{sub}, []string{sub}
			if len(sc.groups) > 0 {
				keys, refs = nil, nil
				for _, rg := range sc.groups {
					keys, refs = append(keys, sub+"/"+rg), append(refs, sub+"/resourceGroups/"+rg)
				}
			}
			for i, key := range keys {
				if !visible[key] {
					silent = append(silent, refs[i])
					continue
				}
				r.groupsRead[key] = g.Connector
				if complete[true] {
					r.resourcesRead[key] = g.Connector
				}
			}
		}
	}
	for _, ref := range silent {
		reason := reasonNoGroup
		if strings.Contains(ref, "/") {
			reason = reasonGroupMissing
		}
		r.notSeen(discovery.LayerEnvironment, g.Connector+"/subscriptions/"+ref, reason)
	}
	for _, table := range []string{tableGroups, tableResources} {
		if dropped[table] > 0 {
			r.notSeen(discovery.LayerEnvironment, g.Connector+"/"+table, fmt.Sprintf(reasonOutside, dropped[table]))
		}
	}
	return nil
}

// keep checks a row against the reach again (ADR 0021 §6) and its shape,
// and keeps it once. It says false for a row it drops, whose ID then
// counts as seen for removals: Resource Graph returned it.
func (r *run) keep(reach coverage.Reach, rw row, resources bool) (mapschema.AzureScope, bool) {
	id := strings.ToLower(rw.ID)
	sc, ok := mapschema.ParseAzure(rw.ID)
	layer := discovery.LayerEnvironment
	if resources {
		layer = types[rw.Type]
		ok = ok && layer != "" && sc.ResourceGroup == rw.ResourceGroup
	} else {
		ok = ok && strings.Count(rw.ID, "/") == 4 // a group's ID is /subscriptions/<id>/resourceGroups/<name>
	}
	if !ok || sc.Subscription != rw.Subscription || !reach.Holds(sc) || sc.ResourceGroup == "" {
		r.dropped[id] = true
		return sc, false
	}
	if r.ids[id] {
		return sc, true // read through another connector too
	}
	r.ids[id] = true
	s := &seen{row: rw, scope: sc, connector: reach.Connector, layer: layer}
	if resources {
		r.resources = append(r.resources, s)
	} else {
		r.groups = append(r.groups, s)
	}
	return sc, true
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
		if r.add(l, mapserver.Entry{Key: s.key, Sources: []map[string]string{{sourceAzure: s.ID}}}, []string{s.connector + "/" + tableGroups}) {
			r.envs[s.scope.Subscription+"/"+s.scope.ResourceGroup] = s.key
		}
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
func keyName(name string) string { return keyNameIn("", name) }

// keyNameIn is keyName for a layer: an environment's key carries no dot,
// since environment/<cluster>.<namespace> names a namespace (ADR 0016 §5).
func keyNameIn(layer, name string) string {
	if layer == discovery.LayerEnvironment {
		name = strings.ReplaceAll(name, ".", "-")
	}
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
// taken, by the Map, an earlier proposal or another row of the set, is
// not proposed, for none of the rows that share it, and the layer says it.
func (r *run) assignKeys(rows []*seen) {
	byBase := map[string][]*seen{}
	for _, s := range rows {
		base := s.layer + "/" + keyNameIn(s.layer, s.Name)
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
		count := map[string]int{}
		for _, s := range set {
			count[s.key]++
		}
		for _, s := range set {
			if _, held := r.byKey[s.key]; held || r.reserved[s.key] || count[s.key] > 1 {
				r.notSeen(s.layer, s.ID, reasonKeyTaken)
				s.key = ""
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

// add proposes a new entry, discovered and proposed, and says whether it
// did.
func (r *run) add(l *discovery.Layer, e mapserver.Entry, evidence []string) bool {
	e.Provenance, e.Status, e.Created, e.LastSeen = "discovered", "proposed", r.today, r.today
	return r.propose(l, v1alpha1.ProposalAdd, e, evidence)
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
		var evidence []string
		for _, s := range e.Sources {
			id, ok := s[sourceAzure]
			connector := ""
			if ok && !r.ids[strings.ToLower(id)] && !r.dropped[strings.ToLower(id)] {
				connector = r.surveyed(id, layer)
			}
			if connector == "" {
				evidence = nil
				break
			}
			table := tableResources
			if layer == discovery.LayerEnvironment {
				table = tableGroups
			}
			if ev := connector + "/" + table; !slices.Contains(evidence, ev) {
				evidence = append(evidence, ev)
			}
		}
		if evidence != nil {
			r.propose(r.layers[layer], v1alpha1.ProposalRemove, e, evidence)
		}
	}
}

// surveyed names the connector that read whole the scope of an ID of that
// layer, or says "": a group in a groups scope, a resource of a listed
// type in a resources scope. A scope counts only when the connector's own
// groups query showed a group in it (see read): an empty answer may be a
// scope the identity cannot read.
func (r *run) surveyed(id, layer string) string {
	sc, ok := mapschema.ParseAzure(id)
	if !ok || sc.ResourceGroup == "" {
		return ""
	}
	sub, group := sc.Subscription, sc.Subscription+"/"+sc.ResourceGroup
	groups := r.groupsRead[sub]
	if groups == "" {
		groups = r.groupsRead[group]
	}
	if groups == "" {
		return ""
	}
	if layer == discovery.LayerEnvironment {
		if strings.Count(id, "/") != 4 {
			return ""
		}
		return groups
	}
	if types[armType(id)] != layer {
		return ""
	}
	if c := r.resourcesRead[sub]; c != "" {
		return c
	}
	return r.resourcesRead[group]
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

func (r *run) propose(l *discovery.Layer, action v1alpha1.ProposalAction, e mapserver.Entry, evidence []string) bool {
	if r.proposedKeys[e.Key] {
		return false
	}
	if err := mapserver.ValidateEntry(e); err != nil {
		r.notSeen(l.Name, e.Key, reasonInvalidEntry)
		return false
	}
	raw, err := yaml.Marshal(e)
	if err != nil {
		return false // an Entry always marshals
	}
	r.proposedKeys[e.Key] = true
	l.Proposed++
	r.proposals = append(r.proposals, discovery.Proposal{
		ID: discovery.ProposalID(action, e.Key), Action: action, Key: e.Key, Entry: string(raw),
		Evidence: evidence, Source: v1alpha1.ProposalFromImport, Layer: l.Name,
	})
	return true
}
