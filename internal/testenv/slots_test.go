// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package testenv

import (
	"path/filepath"
	"testing"
	"time"
)

// A slot is held by one taker at a time and frees on release: a second
// taker of the only slot waits until then. flock(2) locks belong to an
// open file description, so two opens in one process exclude each other as
// two processes do.
func TestSlotsExclude(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envSlotsDir, dir)
	t.Setenv(envParallel, "1")

	release := acquireSlot(t)
	if _, ok := trySlot(t, filepath.Join(dir, "slot-0")); ok {
		t.Fatal("the only slot was taken twice")
	}
	got := make(chan struct{})
	go func() {
		r := acquireSlot(t)
		close(got)
		r()
	}()
	select {
	case <-got:
		t.Fatal("a second taker got the only slot while it was held")
	case <-time.After(3 * slotPoll):
	}
	release()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the released slot was not taken")
	}
}
