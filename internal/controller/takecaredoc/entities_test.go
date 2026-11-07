// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package takecaredoc

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// mapCaller sends the Doc's entry calls to a test Map server, whatever URL
// the controller computed (the add-on's Service does not exist here).
type mapCaller struct {
	url   atomic.Value
	calls atomic.Int32
}

func (m *mapCaller) Call(ctx context.Context, ep mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error) {
	url, _ := m.url.Load().(string)
	if url == "" {
		return mcp.Result{}, fmt.Errorf("no Map server")
	}
	m.calls.Add(1)
	ep.URL = url
	return (&mcp.Client{HTTP: &http.Client{Timeout: 5 * time.Second}}).Call(ctx, ep, tool, args)
}

// startMap serves a Map directory built from entries (key → YAML body
// after the key line).
func startMap(t *testing.T, entries map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for key, body := range entries {
		path := filepath.Join(dir, "map/"+key+".yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("key: "+key+"\ncreated: 2026-11-05\n"+body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := mapserver.New(mapserver.Config{Dir: dir, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv.URL + "/mcp"
}

func (f *fixture) setMapRevision(t *testing.T, revision string) {
	t.Helper()
	var c v1alpha1.TakeCareConnector
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: mapConnector}, &c); err != nil {
		t.Fatal(err)
	}
	c.Status.MapRevision = revision
	if err := f.c.Status().Update(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
}

// updateDoc changes a Doc's spec, again on a conflict: the controller
// patches its status meanwhile.
func (f *fixture) updateDoc(t *testing.T, name string, change func(*v1alpha1.TakeCareDoc)) {
	t.Helper()
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		d := f.getDoc(t, name)
		change(d)
		return f.c.Update(context.Background(), d)
	}); err != nil {
		t.Fatal(err)
	}
}

// blindSpots lists a Doc's blind spots as key=reason, in order.
func blindSpots(d *v1alpha1.TakeCareDoc) string {
	got := make([]string, 0, len(d.Status.BlindSpots))
	for _, s := range d.Status.BlindSpots {
		got = append(got, fmt.Sprintf("%s=%s", s.Entity, s.Reason))
	}
	return strings.Join(got, " ")
}

// ADR 0014 §1, spec 08 §7: scope.entities covered through the Map.
func TestEntityCoverage(t *testing.T) {
	entities := []v1alpha1.EntityKey{"service/shop-api", "service/proposed", "service/ghost", "datastore/aws-db", "compute/billing-worker", "team/platform", "environment/other"}
	f := start(t)
	f.maps.url.Store(startMap(t, map[string]string{
		"service/shop-api":       "provenance: declared\nstatus: validated\nsources:\n  - kubernetes: shop/Deployment/shop-api\n",
		"service/proposed":       "provenance: discovered\nstatus: proposed\nsources:\n  - kubernetes: shop/Deployment/proposed\n",
		"datastore/aws-db":       "provenance: declared\nstatus: validated\nsources:\n  - aws: arn:aws:rds:eu-west-3:0:db:aws-db\n",
		"compute/billing-worker": "provenance: declared\nstatus: validated\nsources:\n  - kubernetes: billing/Deployment/billing-worker\n",
		"team/platform":          "provenance: declared\nstatus: validated\n",
	}))
	f.platform(t)
	cluster := []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}
	f.connector(t, k8sConnector, cluster, read("pods_list"), true)
	f.billingConnector(t)
	mapTools := read("blast_radius", "entry", "map_at", "neighbours", "path")
	f.connector(t, mapConnector, []v1alpha1.ConnectorType{v1alpha1.ConnectorMap}, mapTools, true)
	f.setMapRevision(t, "rev1")
	f.doc(t, "doc", func(d *v1alpha1.TakeCareDoc) {
		d.Spec.Level = v1alpha1.LevelApprentice
		d.Spec.Scope.Entities = entities
		d.Spec.Connectors = v1alpha1.DocConnectors{Cluster: []string{k8sConnector, billingConnector}, Map: []string{mapConnector}}
	})

	want := "compute/billing-worker=ConnectorNotReady datastore/aws-db=NoAccess environment/other=NoAccess service/ghost=NotInMap service/proposed=NotValidated"
	eventually(t, "the five outcomes", func() (bool, string) {
		got := blindSpots(f.getDoc(t, "doc"))
		return got == want, got
	})
	eventually(t, "the instructions name the entities' blind spots", func() (bool, string) {
		cm, err := f.get(schema.GroupVersionKind{Version: "v1", Kind: kindConfigMap}, engineNS, agent+"-instructions")
		if err != nil {
			return false, err.Error()
		}
		text, _, _ := unstructured.NestedString(cm.Object, "data", "instructions.md")
		return strings.Contains(text, "The entity `service/ghost`: not in the Map.") && strings.Contains(text, "The entity `service/proposed`: proposed in the Map, not validated."), text
	})

	// The same revision: the Map is not asked again.
	calls := f.maps.calls.Load()
	f.updateDoc(t, "doc", func(d *v1alpha1.TakeCareDoc) { d.Spec.Investigation.MaxConcurrent = 2 })
	eventually(t, "the new generation is reconciled", func() (bool, string) {
		d := f.getDoc(t, "doc")
		return d.Status.ObservedGeneration == d.Generation, describe(d)
	})
	if n := f.maps.calls.Load(); n != calls {
		t.Errorf("the Map was asked %d more times for the same revision", n-calls)
	}
	f.setMapRevision(t, "rev2")
	eventually(t, "a new revision asks the Map again", func() (bool, string) {
		return f.maps.calls.Load() > calls, "no new call"
	})

	// Nothing covered and a spot that will not pass by itself (NotInMap):
	// a misconfiguration, the agent goes.
	f.updateDoc(t, "doc", func(d *v1alpha1.TakeCareDoc) {
		d.Spec.Scope = v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{billingNS}, Entities: []v1alpha1.EntityKey{"service/ghost"}}
	})
	eventually(t, "the agent is removed", func() (bool, string) {
		_, err := f.get(agentGVK, engineNS, agent)
		return err != nil, blindSpots(f.getDoc(t, "doc"))
	})
	f.updateDoc(t, "doc", func(d *v1alpha1.TakeCareDoc) {
		d.Spec.Scope = v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{scope}, Entities: entities}
	})
	eventually(t, "the agent is back", func() (bool, string) {
		_, err := f.get(agentGVK, engineNS, agent)
		return err == nil, "no agent"
	})

	// The Map connector not given: every entity on a ground is
	// ConnectorNotReady, and the Doc still runs on its namespaces.
	f.setConnector(t, mapConnector, nil, false)
	want = "compute/billing-worker=ConnectorNotReady datastore/aws-db=ConnectorNotReady environment/other=NoAccess service/ghost=ConnectorNotReady service/proposed=ConnectorNotReady service/shop-api=ConnectorNotReady"
	eventually(t, "every entity waits for the Map", func() (bool, string) {
		got := blindSpots(f.getDoc(t, "doc"))
		return got == want, got
	})
	if _, err := f.get(agentGVK, engineNS, agent); err != nil {
		t.Errorf("the Doc stopped without the Map: %v", err)
	}
}
