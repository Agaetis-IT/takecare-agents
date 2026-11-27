// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mapserver

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/agaetis-it/takecare-agents/internal/gittest"
)

// held is the storage the source keeps between polls.
func held(g *gitSource) *memory.Storage {
	g.histMu.Lock()
	defer g.histMu.Unlock()
	return g.hist.store
}

// The source keeps the Map's history and nothing else of the repository:
// no commit object and no file outside map/, so that its memory does not
// grow with what else the repository holds or with the polls. A moved
// head brings a fresh storage; an unchanged one is not cloned again; and
// map_at still reads every revision (spec 08 §5).
func TestCompactHistory(t *testing.T) {
	r := newRemote(t, ".")
	c1 := r.commit("the demo Map", 1)
	var foreign []plumbing.Hash
	data := func(i int) {
		content := strings.Repeat(strconv.Itoa(i), 64<<10)
		r.write("apps/data.txt", content)
		foreign = append(foreign, plumbing.ComputeHash(plumbing.BlobObject, []byte(content)))
	}
	var last string
	for i := range 5 {
		data(i)
		last = r.commit("data", 2+i)
	}
	url, ca := gittest.HTTPS(t, r.dir)
	_, g := gitServer(t, GitConfig{URL: url, CAFile: ca})
	ctx := context.Background()

	check := func(when, head string) *memory.Storage {
		t.Helper()
		m, err := g.current()
		if err != nil || m.Revision != head {
			t.Fatalf("%s: serving %v (%v), want %s", when, m, err, head)
		}
		store := held(g)
		if n := len(store.Commits); n != 0 {
			t.Errorf("%s: %d commits in memory, want none", when, n)
		}
		for _, h := range foreign {
			if store.HasEncodedObject(h) == nil {
				t.Errorf("%s: a file outside map/ is in memory (%s)", when, h)
			}
		}
		if m, err := g.at(time.Date(2026, 11, 1, 23, 0, 0, 0, time.UTC)); err != nil || m.Revision != c1 {
			t.Errorf("%s: map_at on day 1 = %v (%v), want %s", when, m, err, c1)
		}
		return store
	}

	g.sync(ctx)
	first := check("first clone", last)

	g.sync(ctx)
	if check("unchanged head", last) != first {
		t.Error("an unchanged head was cloned again")
	}

	data(5)
	r.write("map/runbook/extra.yaml", "key: runbook/extra\nprovenance: declared\nstatus: validated\ncreated: 2026-11-10\n")
	moved := r.commit("moved", 10)
	g.sync(ctx)
	if check("moved head", moved) == first {
		t.Error("a moved head did not get a fresh storage")
	}
	if m, _ := g.current(); m.Entries["runbook/extra"] == nil {
		t.Error("the moved head's new entry is not served")
	}
}
