// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package codeserver

import (
	"context"
	"strings"
	"testing"

	"github.com/agaetis-it/takecare-agents/internal/gittest"
)

// https serves the remote over HTTPS with git http-backend.
func (r *remote) https() (url, caFile string) { return gittest.HTTPS(r.t, r.dir) }

// The server keeps the head of the branch and nothing of its history: a
// depth-1 clone, replaced by a fresh one when the head moves and kept
// when it does not, so that memory never grows with the history or the
// polls (spec 12 §5: up to sixteen repositories in one pod).
func TestShallowClone(t *testing.T) {
	r := newRemote(t)
	var last string
	for i := range 5 {
		r.write("apps/data.txt", strings.Repeat(string(rune('a'+i)), 64<<10))
		last = r.commit("revision")
	}
	url, ca := r.https()
	s := newServer(t, Repository{Name: repoShop, URL: url, CAFile: ca})
	rp := s.repos[0]
	ctx := context.Background()

	check := func(when, want string) {
		t.Helper()
		st := rp.state()
		if st.FetchError != "" || st.Revision != want {
			t.Fatalf("%s: revision %q (error %q), want %s", when, st.Revision, st.FetchError, want)
		}
		rp.mu.Lock()
		store := rp.store
		rp.mu.Unlock()
		if n := len(store.Commits); n != 1 {
			t.Errorf("%s: %d commits in memory, want the head only", when, n)
		}
		if shallow, err := store.Shallow(); err != nil || len(shallow) != 1 {
			t.Errorf("%s: shallow commits %v (%v), want the head", when, shallow, err)
		}
	}

	rp.sync(ctx)
	check("first clone", last)
	rp.mu.Lock()
	first := rp.store
	rp.mu.Unlock()

	rp.sync(ctx)
	check("unchanged head", last)
	rp.mu.Lock()
	kept := rp.store == first
	rp.mu.Unlock()
	if !kept {
		t.Error("an unchanged head was cloned again")
	}

	r.write("apps/data.txt", "moved\n")
	moved := r.commit("moved")
	rp.sync(ctx)
	check("moved head", moved)
	rp.mu.Lock()
	replaced := rp.store != first
	rp.mu.Unlock()
	if !replaced {
		t.Error("a moved head did not get a fresh storage")
	}
	session, _ := connect(t, s)
	if got, isErr := call(t, session, ToolFileGet, map[string]any{argRepo: repoShop, argPath: "apps/data.txt"}); isErr || !strings.Contains(got, "moved") {
		t.Errorf("file_get after the move = %q", got)
	}
}
