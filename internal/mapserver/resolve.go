// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mapserver

import (
	"encoding/json"
	"net/http"
	"slices"

	mapschema "github.com/agaetis-it/takecare-agents/api/map"
)

// Why an alert resolves to no entry.
const (
	missNone      = "none"
	missAmbiguous = "ambiguous"
)

// sourceKubernetes is the source kind an alert's labels can be matched to.
const sourceKubernetes = "kubernetes"

// resolve is the rule of spec 08 §7: among validated entries, those with a
// kubernetes source <namespace>/<Kind>/<name> in the alert's namespace,
// named after the workload or, failing that, the service. One candidate
// is a hit; with several, the one whose alertedBy lists the alert is;
// otherwise the reason of the miss.
func (m *Map) resolve(cluster, namespace, workload, service, alert string) (string, string) {
	var candidates []string
	for _, name := range []string{workload, service} {
		if name == "" {
			continue
		}
		candidates = m.bySource(cluster, namespace, name)
		if len(candidates) > 0 {
			break
		}
	}
	switch len(candidates) {
	case 0:
		return "", missNone
	case 1:
		return candidates[0], ""
	}
	alerted := slices.DeleteFunc(candidates, func(k string) bool {
		return !slices.Contains(m.Entries[k].Relations.alerts(), alert)
	})
	if len(alerted) != 1 {
		return "", missAmbiguous
	}
	return alerted[0], ""
}

// bySource lists the validated entries with a kubernetes source naming an
// object of the cluster's namespace, of any kind, by that name, sorted by
// key; the cluster "" is the unnamed one.
func (m *Map) bySource(cluster, namespace, name string) []string {
	var keys []string
	for k, e := range m.Entries {
		if e.Status != "validated" {
			continue
		}
		if slices.ContainsFunc(e.Sources, func(s map[string]string) bool {
			src, ok := mapschema.ParseKubernetes(s[sourceKubernetes])
			return ok && src.Kind != "" && src.Cluster == cluster && src.Namespace == namespace && src.Name == name
		}) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

// serveResolve answers GET /resolve for the operator's intake (spec 08
// §5): agents never call it and the gateway does not route it.
func (s *Server) serveResolve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	if r.Method != http.MethodGet || q.Get("namespace") == "" || q.Get("alert") == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{keyError: "GET with namespace and alert"})
		return
	}
	m, err := s.snapshot()
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{keyError: err.Error()})
		return
	}
	key, miss := m.resolve(q.Get("cluster"), q.Get("namespace"), q.Get("workload"), q.Get("service"), q.Get("alert"))
	if miss != "" {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"reason": miss, keyRevision: m.Revision})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{argKey: key, keyRevision: m.Revision})
}
