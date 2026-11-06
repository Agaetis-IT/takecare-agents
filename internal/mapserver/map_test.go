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
	demoDir  = "testdata/demo"
	cartFile = "map/service/cart.yaml"
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
		"bad key":                        {fstest.MapFS{"map/service/Shop.yaml": entryFile("service/Shop")}, []string{"map/service/Shop.yaml: does not match"}},
		"key not matching path":          {fstest.MapFS{cartFile: entryFile("service/shop")}, []string{"key service/shop does not match the file's place, service/cart"}},
		"dangling target":                {fstest.MapFS{cartFile: entryFile("service/cart", "relations: {dependsOn: [datastore/none]}")}, []string{"dependsOn datastore/none: no such entry"}},
		"runsAs from a datastore":        {fstest.MapFS{"map/datastore/cart.yaml": entryFile("datastore/cart", "relations: {runsAs: [compute/shop]}")}, []string{"runsAs starts from service, not from datastore"}},
		"deployedIn to a service":        {fstest.MapFS{"map/compute/cart.yaml": entryFile("compute/cart", "relations: {deployedIn: [service/shop]}")}, []string{"deployedIn service/shop: it points to environment, not to service"}},
		"a second file with a taken key": {fstest.MapFS{"map/service/shop-copy.yaml": entryFile("service/shop")}, []string{"map/service/shop-copy.yaml: key service/shop does not match"}},
		"unknown property":               {fstest.MapFS{cartFile: entryFile("service/cart", "shade: blue")}, []string{"map/service/cart.yaml: does not match"}},
		"date that does not exist":       {fstest.MapFS{cartFile: entryFile("service/cart", "lastSeen: 2026-02-30")}, []string{"lastSeen 2026-02-30 is not a date"}},
		"file outside the layout":        {fstest.MapFS{"map/notes.yaml": entryFile("service/notes")}, []string{"map/notes.yaml: not a map/<type>/<name>.yaml file"}},
		"every fault at once": {fstest.MapFS{
			"map/service/a.yaml": entryFile("service/b"),
			"map/service/c.yaml": entryFile("service/c", "relations: {dependsOn: [datastore/none]}"),
			"map/runbook/r.yaml": entryFile("runbook/r", "lastSeen: 2026-13-01"),
		}, []string{"service/a.yaml", "datastore/none", "runbook/r.yaml: lastSeen 2026-13-01 is not a date"}},
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
