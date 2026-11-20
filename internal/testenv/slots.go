// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package testenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// Slots cap the envtest API servers that run at once across every test
// process of a `go test ./...` run, not per process: go test runs packages
// in parallel processes, and tests marked t.Parallel run their own servers
// within each, so that without a shared cap a full run starts more API
// servers than the machine can bring up in time.
//
// A slot is an exclusive flock(2) on one of N files in a directory every
// test process of the run sees, as envtest's own port reservation does
// (controller-runtime pkg/internal/testing/addr). A lock, not a counter: the
// kernel releases it when its process ends, a crash included, so no slot
// leaks.
//
//   - N is TESTENV_PARALLEL, by default the number of CPUs.
//   - The directory is TESTENV_SLOTS_DIR, by default
//     takecare-testenv-slots-<uid> under os.TempDir(). That fixed per-user
//     name is shared by every package's process of a run and by any other
//     run of the same user at the same time (another worktree, a loop): the
//     cap then holds for the machine, which is what the servers contend
//     for. To lift it for a deliberate comparison of two runs, give each
//     run its own TESTENV_SLOTS_DIR.
const (
	envParallel = "TESTENV_PARALLEL"
	envSlotsDir = "TESTENV_SLOTS_DIR"
	// slotPoll is how often a test waiting for a slot tries the locks
	// again: a wait on a lock another process holds, not a wait on state,
	// so a fixed short poll is right here.
	slotPoll = 100 * time.Millisecond
)

// acquireSlot blocks until one of the run's slots is free, takes it, and
// returns its release. The test's own timeout bounds the wait.
func acquireSlot(t *testing.T) func() {
	t.Helper()
	n := runtime.NumCPU()
	if v := os.Getenv(envParallel); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 {
			t.Fatalf("%s=%q: want a positive integer", envParallel, v)
		}
		n = p
	}
	dir := os.Getenv(envSlotsDir)
	if dir == "" {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("takecare-testenv-slots-%d", os.Getuid()))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // the test run's own slots directory, from its environment
		t.Fatalf("envtest slots directory %s: %v", dir, err)
	}
	for {
		for i := range n {
			if release, ok := trySlot(t, filepath.Join(dir, fmt.Sprintf("slot-%d", i))); ok {
				return release
			}
		}
		time.Sleep(slotPoll)
	}
}

// trySlot takes the slot at path when no other process or test holds it.
func trySlot(t *testing.T, path string) (func(), bool) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // a lock file in the run's own slots directory
	if err != nil {
		t.Fatalf("envtest slot %s: %v", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false
		}
		t.Fatalf("lock envtest slot %s: %v", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true
}
