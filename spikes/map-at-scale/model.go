// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// The noise model: what a live cluster lists and Git does not show, added
// to a rendered snapshot. Each rule cites its source in the report:
//
//   - StatefulSet PVCs: one per volumeClaimTemplate and replica, named
//     <template>-<statefulset>-<ordinal>, labelled with the template's labels
//     and the StatefulSet's selector; no ownerReference under the default
//     persistentVolumeClaimRetentionPolicy (Retain).
//   - Prometheus Operator: a Prometheus or Alertmanager resource becomes a
//     StatefulSet <kind>-<name> and a governing Service <kind>-operated,
//     owned by the resource (plus PVCs when it declares storage).
//   - CloudNativePG: a Cluster becomes the Services <name>-rw, -ro and -r, a
//     ServiceAccount <name> and one PVC <name>-<n> per instance, all owned.
//   - Ephemeral namespaces: preview environments, each a copy of one web
//     release in its own namespace (an ApplicationSet pull-request
//     generator's shape).
func applyModel(s *snapshot, previews int) {
	var added []*obj
	operated := map[string]bool{}
	for _, o := range s.objects {
		switch {
		case o.kind == "StatefulSet" && o.group() == "apps":
			added = append(added, statefulSetPVCs(o)...)
		case (o.kind == "Prometheus" || o.kind == "Alertmanager") && o.group() == "monitoring.coreos.com":
			lower := strings.ToLower(o.kind)
			sts := child(o, "apps/v1", "StatefulSet", lower+"-"+o.name)
			sts.labels = map[string]string{"app.kubernetes.io/name": lower, "app.kubernetes.io/instance": o.name, "app.kubernetes.io/managed-by": "prometheus-operator"}
			added = append(added, sts)
			if key := o.cluster + "/" + o.ns + "/" + lower; !operated[key] {
				operated[key] = true
				svc := child(o, "v1", "Service", lower+"-operated")
				svc.headless = true
				svc.labels = map[string]string{"operated-" + lower: "true"}
				added = append(added, svc)
			}
			spec, _ := o.raw["spec"].(map[string]any)
			if st, ok := spec["storage"].(map[string]any); ok && st["volumeClaimTemplate"] != nil {
				for i := range replicas(spec) {
					pvc := child(o, "v1", "PersistentVolumeClaim", fmt.Sprintf("%s-%s-db-%s-%s-%d", lower, o.name, lower, o.name, i))
					pvc.stsPVC, pvc.owned, pvc.labels = true, false, maps.Clone(sts.labels)
					added = append(added, pvc)
				}
			}
		case o.kind == "Cluster" && o.group() == "postgresql.cnpg.io":
			spec, _ := o.raw["spec"].(map[string]any)
			n := 1
			if v, ok := num(spec["instances"]); ok {
				n = v
			}
			labels := map[string]string{"cnpg.io/cluster": o.name}
			for _, suffix := range []string{"-rw", "-ro", "-r"} {
				svc := child(o, "v1", "Service", o.name+suffix)
				svc.labels = labels
				added = append(added, svc)
			}
			sa := child(o, "v1", "ServiceAccount", o.name)
			sa.labels = labels
			added = append(added, sa)
			for i := 1; i <= n; i++ {
				pvc := child(o, "v1", "PersistentVolumeClaim", fmt.Sprintf("%s-%d", o.name, i))
				pvc.labels = labels
				added = append(added, pvc)
			}
		}
	}
	s.objects = append(s.objects, added...)
	addPreviews(s, previews)
}

// child is an object a controller makes for o, owned by it.
func child(o *obj, apiVersion, kind, name string) *obj {
	return &obj{cluster: o.cluster, ns: o.ns, apiVersion: apiVersion, kind: kind, name: name, source: o.source,
		labels: map[string]string{}, annotations: map[string]string{}, owned: true, runtime: true}
}

func statefulSetPVCs(o *obj) []*obj {
	spec, _ := o.raw["spec"].(map[string]any)
	templates, _ := spec["volumeClaimTemplates"].([]any)
	selector, _ := spec["selector"].(map[string]any)
	match := strMap(selector["matchLabels"])
	var out []*obj
	for _, t := range templates {
		tm, _ := t.(map[string]any)
		meta, _ := tm["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		labels := strMap(meta["labels"])
		maps.Copy(labels, match)
		for i := range replicas(spec) {
			pvc := child(o, "v1", "PersistentVolumeClaim", fmt.Sprintf("%s-%s-%d", name, o.name, i))
			pvc.owned, pvc.stsPVC, pvc.labels = false, true, labels
			out = append(out, pvc)
		}
	}
	return out
}

func replicas(spec map[string]any) int {
	if v, ok := num(spec["replicas"]); ok {
		return v
	}
	return 1
}

func num(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

// addPreviews copies the first n web releases (sources -~team-NN~web-*)
// into namespaces preview-01..n.
func addPreviews(s *snapshot, n int) {
	if n == 0 {
		return
	}
	var webs []string
	for src := range s.sources {
		if strings.Contains(src, "~web-") {
			webs = append(webs, src)
		}
	}
	slices.Sort(webs)
	for i, src := range webs[:min(n, len(webs))] {
		ns := fmt.Sprintf("preview-%02d", i+1)
		for _, o := range slices.Clone(s.objects) {
			if o.source != src {
				continue
			}
			c := *o
			c.ns, c.runtime, c.source = ns, true, src+"#"+ns
			s.objects = append(s.objects, &c)
			s.namespace(c.cluster, ns)
		}
	}
}
