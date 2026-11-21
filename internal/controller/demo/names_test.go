// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package demo

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

// A demo stored with a name past the bound before the rule: SceneryReady
// and Ready False with the rule's sentence, no scenery applied, no
// finalizer.
func TestStoredDemoNameTooLong(t *testing.T) {
	t.Parallel()
	f := start(t, nil)
	stored := testDemo()
	stored.Name = strings.Repeat("d", v1alpha1.MaxDemoName+1)
	probe := stored.DeepCopy()
	probe.Name = strings.Repeat("p", v1alpha1.MaxDemoName+1)
	testenv.StoreUnvalidated(t, f.cfg, "takecaredemos.takecare.agaetis.fr",
		func() error { return f.c.Create(f.ctx, stored.DeepCopy()) },
		func() error { return f.c.Create(f.ctx, probe.DeepCopy(), client.DryRunAll) })
	var d v1alpha1.TakeCareDemo
	f.eventually("Ready NameTooLong", func() bool {
		if err := f.c.Get(f.ctx, client.ObjectKey{Namespace: ns, Name: stored.Name}, &d); err != nil {
			return false
		}
		c := meta.FindStatusCondition(d.Status.Conditions, v1alpha1.ConditionReady)
		return c != nil && c.Status == metav1.ConditionFalse && c.Reason == v1alpha1.ReasonNameTooLong
	})
	for _, typ := range []string{v1alpha1.ConditionReady, v1alpha1.ConditionSceneryReady} {
		if c := meta.FindStatusCondition(d.Status.Conditions, typ); c == nil || c.Message != v1alpha1.MessageDemoName {
			t.Errorf("%s %+v", typ, c)
		}
	}
	if controllerutil.ContainsFinalizer(&d, Finalizer) {
		t.Errorf("finalizers %v", d.Finalizers)
	}
}
