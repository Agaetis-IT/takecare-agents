// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mapserver

import (
	"maps"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

const (
	demoDir      = "testdata/demo"
	cartFile     = "map/service/cart.yaml"
	cartNotMatch = cartFile + ": does not match"
	runbookFile  = "map/runbook/r.yaml"
	keyOrders    = "service/orders"
)

// entryFile is a valid entry for a key, with extra YAML lines.
func entryFile(key string, extra ...string) *fstest.MapFile {
	lines := append([]string{"key: " + key, "provenance: declared", "status: validated", "created: 2026-11-05"}, extra...)
	return &fstest.MapFile{Data: []byte(strings.Join(lines, "\n") + "\n")}
}

func TestLoadDemo(t *testing.T) {
	m, err := Load(os.DirFS(demoDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 10 || m.Revision == "" {
		t.Errorf("%d entries, revision %q", len(m.Entries), m.Revision)
	}
	if got := m.Entries["service/notifications"].Status; got != "proposed" {
		t.Errorf("a proposed entry is served with its status, got %q", got)
	}
}

// Spec 08 §3-4: an alert's expression on alertedBy, beside a bare name, and
// a runbook mapped to a knowledge record.
func TestLoadExpressionsAndKnowledge(t *testing.T) {
	m, err := Load(os.DirFS("testdata/expressions"))
	if err != nil {
		t.Fatal(err)
	}
	got := m.Entries[keyOrders].Relations.AlertedBy
	if len(got) != 2 || got[0].Name != "HighLatency" || got[0].Expr == "" || got[1] != (Alert{Name: "PodNotReady"}) {
		t.Errorf("alertedBy = %+v", got)
	}
	if src := m.Entries["runbook/orders-latency"].Sources; len(src) != 1 || src[0][sourceKnowledge] != "runbooks/RB-021" {
		t.Errorf("sources = %v", src)
	}
}

// ADR 0016 §5: named clusters beside the unnamed one.
func TestLoadNamedClusters(t *testing.T) {
	m, err := Load(fstest.MapFS{
		"map/environment/east.yaml":      entryFile("environment/east"),
		"map/environment/east.apps.yaml": entryFile("environment/east.apps", "sources: [{kubernetes: east/apps}]"),
		"map/environment/apps.yaml":      entryFile("environment/apps", "sources: [{kubernetes: apps}]"),
		cartFile:                         entryFile("service/cart", "sources: [{kubernetes: east/apps/Deployment/cart}, {kubernetes: apps/Deployment/cart}]"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 4 {
		t.Errorf("%d entries", len(m.Entries))
	}
}

func TestLoadRefuses(t *testing.T) {
	base := func() fstest.MapFS {
		return fstest.MapFS{
			"map/README.md":              {Data: []byte("written by the Map tooling\n")},
			"map/.git/config":            {Data: []byte("ignored\n")},
			"map/service/shop.yaml":      entryFile("service/shop"),
			"map/compute/shop.yaml":      entryFile("compute/shop"),
			"map/environment/apps.yaml":  entryFile("environment/apps"),
			"map/datastore/shop-db.yaml": entryFile("datastore/shop-db"),
		}
	}
	for name, tc := range map[string]struct {
		files fstest.MapFS
		want  []string
	}{
		"bad key":                                   {fstest.MapFS{"map/service/Shop.yaml": entryFile("service/Shop")}, []string{"map/service/Shop.yaml: does not match"}},
		"key not matching path":                     {fstest.MapFS{cartFile: entryFile("service/shop")}, []string{"key service/shop does not match the file's place, service/cart"}},
		"dangling target":                           {fstest.MapFS{cartFile: entryFile("service/cart", "relations: {dependsOn: [datastore/none]}")}, []string{"dependsOn datastore/none: no such entry"}},
		"runsAs from a datastore":                   {fstest.MapFS{"map/datastore/cart.yaml": entryFile("datastore/cart", "relations: {runsAs: [compute/shop]}")}, []string{"runsAs starts from service, not from datastore"}},
		"deployedIn to a service":                   {fstest.MapFS{"map/compute/cart.yaml": entryFile("compute/cart", "relations: {deployedIn: [service/shop]}")}, []string{"deployedIn service/shop: it points to environment, not to service"}},
		"a second file with a taken key":            {fstest.MapFS{"map/service/shop-copy.yaml": entryFile("service/shop")}, []string{"map/service/shop-copy.yaml: key service/shop does not match"}},
		"unknown property":                          {fstest.MapFS{cartFile: entryFile("service/cart", "shade: blue")}, []string{cartNotMatch}},
		"date that does not exist":                  {fstest.MapFS{cartFile: entryFile("service/cart", "lastSeen: 2026-02-30")}, []string{"lastSeen 2026-02-30 is not a date"}},
		"file outside the layout":                   {fstest.MapFS{"map/notes.yaml": entryFile("service/notes")}, []string{"map/notes.yaml: not a map/<type>/<name>.yaml file"}},
		"a dotted environment without its cluster":  {fstest.MapFS{"map/environment/east.apps.yaml": entryFile("environment/east.apps")}, []string{"the key environment/east.apps names the cluster east, which has no entry environment/east"}},
		"a four-segment source without its cluster": {fstest.MapFS{cartFile: entryFile("service/cart", "sources: [{kubernetes: east/apps/Deployment/cart}]")}, []string{"the source east/apps/Deployment/cart names the cluster east"}},
		"a cluster that is not a DNS label":         {fstest.MapFS{cartFile: entryFile("service/cart", "sources: [{kubernetes: East_1/apps/Deployment/cart}]")}, []string{cartNotMatch}},
		"five segments":                             {fstest.MapFS{cartFile: entryFile("service/cart", "sources: [{kubernetes: a/b/c/d/e}]")}, []string{cartNotMatch}},
		"an alert named twice, in both forms":       {fstest.MapFS{cartFile: entryFile("service/cart", "relations: {alertedBy: [HighErrorRate, {name: HighErrorRate, expr: up == 0}]}")}, []string{"alertedBy names HighErrorRate twice"}},
		"an alert named twice with two expressions": {fstest.MapFS{cartFile: entryFile("service/cart", "relations: {alertedBy: [{name: Down, expr: up == 0}, {name: Down, expr: up < 1}]}")}, []string{"alertedBy names Down twice"}},
		"an alert object without its expression":    {fstest.MapFS{cartFile: entryFile("service/cart", "relations: {alertedBy: [{name: Down}]}")}, []string{cartNotMatch}},
		"an alert object with another property":     {fstest.MapFS{cartFile: entryFile("service/cart", "relations: {alertedBy: [{name: Down, expr: up == 0, for: 1m}]}")}, []string{cartNotMatch}},
		"a knowledge source on a service":           {fstest.MapFS{cartFile: entryFile("service/cart", "sources: [{knowledge: runbooks/RB-014}]")}, []string{"the knowledge source runbooks/RB-014 is on a service entry"}},
		"a knowledge source without a record":       {fstest.MapFS{runbookFile: entryFile("runbook/r", "sources: [{knowledge: runbooks/}]")}, []string{runbookFile + ": does not match"}},
		"a knowledge source with a bad connector":   {fstest.MapFS{runbookFile: entryFile("runbook/r", "sources: [{knowledge: Run_Books/RB-014}]")}, []string{runbookFile + ": does not match"}},
		"every fault at once": {fstest.MapFS{
			"map/service/a.yaml": entryFile("service/b"),
			"map/service/c.yaml": entryFile("service/c", "relations: {dependsOn: [datastore/none]}"),
			runbookFile:          entryFile("runbook/r", "lastSeen: 2026-13-01"),
			cartFile:             entryFile("service/cart", "sources: [{knowledge: runbooks/RB-014}]", "relations: {alertedBy: [Down, {name: Down, expr: up == 0}]}"),
		}, []string{"service/a.yaml", "datastore/none", "runbook/r.yaml: lastSeen 2026-13-01 is not a date", "alertedBy names Down twice", "the knowledge source runbooks/RB-014"}},
	} {
		t.Run(name, func(t *testing.T) {
			fsys := base()
			maps.Copy(fsys, tc.files)
			_, err := Load(fsys)
			if err == nil {
				t.Fatal("loaded")
			}
			lines := strings.Split(err.Error(), "\n")
			if len(lines) != len(tc.want) {
				t.Errorf("%d errors, want %d:\n%v", len(lines), len(tc.want), err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("no error says %q:\n%v", w, err)
				}
			}
		})
	}
}
