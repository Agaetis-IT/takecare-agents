// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"os"
	"testing"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
)

// TestMain gives controller-runtime a logger before envtest starts, so
// that it does not warn, with a stack, that none was set.
func TestMain(m *testing.M) {
	ctrl.SetLogger(logr.Discard())
	os.Exit(m.Run())
}
