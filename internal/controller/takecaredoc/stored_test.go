// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package takecaredoc

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/doc"
	"github.com/agaetis-it/takecare-agents/internal/engine/kagent0103"
	"github.com/agaetis-it/takecare-agents/internal/roleagent"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

const docsCRD = "takecaredocs.takecare.agaetis.fr"

// storeDoc creates d as a release before the API's rules let it through;
// a dry run of the same past the rules says when they are back.
func (f *fixture) storeDoc(t *testing.T, d *v1alpha1.TakeCareDoc) {
	t.Helper()
	ctx := context.Background()
	probe := d.DeepCopy()
	probe.Name = d.Name[:len(d.Name)-1] + "x"
	probe.Finalizers = nil
	testenv.StoreUnvalidated(t, f.cfg, docsCRD,
		func() error { return f.c.Create(ctx, d.DeepCopy()) },
		func() error { return f.c.Create(ctx, probe.DeepCopy(), client.DryRunAll) })
}

func longDoc(name string, finalized bool) *v1alpha1.TakeCareDoc {
	d := &v1alpha1.TakeCareDoc{Name: name, Namespace: ns}
	d.Spec.Level = v1alpha1.LevelPractitioner
	d.Spec.Scope.Namespaces = []v1alpha1.Namespace{scope}
	d.Spec.Connectors = v1alpha1.DocConnectors{Cluster: []string{k8sConnector}}
	if finalized {
		d.Finalizers = []string{Finalizer}
	}
	return d
}

// A Doc stored with a longer name before the rule says so on its status,
// renders nothing, takes no finalizer, and can still be edited (the rule
// holds at creation only).
func TestStoredDocNameTooLong(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	name := strings.Repeat("l", v1alpha1.MaxDocName+1)
	f.storeDoc(t, longDoc(name, false))
	d := f.waitCondition(t, name, v1alpha1.ConditionReady, metav1.ConditionFalse, v1alpha1.ReasonNameTooLong)
	if c := meta.FindStatusCondition(d.Status.Conditions, v1alpha1.ConditionReady); c.Message != v1alpha1.MessageDocName {
		t.Errorf("Ready %+v", c)
	}
	if controllerutil.ContainsFinalizer(d, Finalizer) {
		t.Errorf("finalizers %v", d.Finalizers)
	}
	if _, err := f.get(agentGVK, engineNS, doc.AgentName(d)); !apierrors.IsNotFound(err) {
		t.Errorf("an agent of the stored Doc: %v", err)
	}
	f.updateDoc(t, name, func(d *v1alpha1.TakeCareDoc) { d.Spec.Report.Practitioner = "elsewhere" })
}

// A name of 51 to 63 characters got its routes, its model configuration
// and the finalizer from an earlier release, never its agent (its tool
// servers carry the agent's name as a label value, refused past 63
// bytes). The deletion path runs before the name's check: deleting the
// Doc removes what exists. Before the prune fix, the tool servers' label
// selector was refused and the deletion failed at every attempt.
func TestStoredLongDocDeleted(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	name := strings.Repeat("d", 55)
	f.storeDoc(t, longDoc(name, true))
	ctx := context.Background()
	d := f.getDoc(t, name)
	var p v1alpha1.TakeCarePlatform
	if err := f.c.Get(ctx, client.ObjectKey{Name: "main"}, &p); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{}
	role, agentName := r.role(d), doc.AgentName(d)
	gw := routes.Gateway{Name: p.Spec.Gateway.GatewayRef.Name}
	model, _, _ := roleagent.Model(&p, nil)
	agents, models := &kagent0103.Agents{Client: f.c}, &kagent0103.Models{Client: f.c}
	for _, seed := range []func() error{
		func() error { return routes.EnsureA2A(ctx, f.c, gw, role, agents.Endpoint(engineNS, agentName)) },
		func() error { return routes.EnsureModel(ctx, f.c, gw, role, model) },
		func() error {
			return models.EnsureModel(ctx, roleagent.ModelConfig(&p, role, model, agentName, engineNS, doc.OwnerLabels(d)))
		},
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.c.Delete(ctx, d); err != nil {
		t.Fatal(err)
	}
	gone := func() bool {
		return apierrors.IsNotFound(f.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &v1alpha1.TakeCareDoc{}))
	}
	for deadline := time.Now().Add(20 * time.Second); !gone() && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
	}
	if !gone() {
		t.Fatal("the Doc is left after its deletion: its finalizer stayed")
	}
	if _, err := f.get(modelConfigGVK, engineNS, agentName); !apierrors.IsNotFound(err) {
		t.Errorf("the model configuration is left: %v", err)
	}
	if _, err := f.get(routes.HTTPRouteGVK, ns, "role-doc-"+name+"-a2a"); !apierrors.IsNotFound(err) {
		t.Errorf("the A2A route is left: %v", err)
	}
}

// A Doc stored with a slot past its new cap: an edit elsewhere in the
// spec is accepted (the API server keeps an unchanged list), an edit of
// the list itself must come within the cap.
func TestStoredDocOverCap(t *testing.T) {
	t.Parallel()
	f := start(t)
	d := longDoc("wide", false)
	for i := range 17 {
		d.Spec.Connectors.Knowledge = append(d.Spec.Connectors.Knowledge, "k"+strings.Repeat("x", i))
	}
	f.storeDoc(t, d)
	ctx := context.Background()
	got := f.getDoc(t, "wide")
	got.Spec.Report.Practitioner = "elsewhere"
	if err := f.c.Update(ctx, got); err != nil {
		t.Errorf("an edit elsewhere: %v", err)
	}
	got = f.getDoc(t, "wide")
	got.Spec.Connectors.Knowledge = append(got.Spec.Connectors.Knowledge, "one-more")
	if err := f.c.Update(ctx, got); err == nil || !strings.Contains(err.Error(), "knowledge") {
		t.Errorf("an edit of the list past the cap: %v", err)
	}
}
