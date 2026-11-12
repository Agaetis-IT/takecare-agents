// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr/funcr"
	ctrl "sigs.k8s.io/controller-runtime"
)

// logs holds every line the package's controllers log in its tests, so
// that a test can check what never reaches a log line (the model's text).
var logs struct {
	sync.Mutex
	b strings.Builder
}

func logged() string {
	logs.Lock()
	defer logs.Unlock()
	return logs.b.String()
}

// TestMain gives controller-runtime a logger before envtest starts: no
// warning, with a stack, that none was set, and the lines kept.
func TestMain(m *testing.M) {
	ctrl.SetLogger(funcr.New(func(prefix, args string) {
		logs.Lock()
		defer logs.Unlock()
		logs.b.WriteString(prefix + " " + args + "\n")
	}, funcr.Options{Verbosity: 1}))
	os.Exit(m.Run())
}
