// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/engine/kagent0103"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/roleagent"
	"github.com/agaetis-it/takecare-agents/internal/routes"
	"github.com/agaetis-it/takecare-agents/internal/surveyagent"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

const intendantsCRD = "takecareintendants.takecare.agaetis.fr"

// mapOnly is an Intendant bound to the map connector alone.
func mapOnly(name string, mutate func(*v1alpha1.TakeCareIntendant)) *v1alpha1.TakeCareIntendant {
	return newIntendant(name, func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.Connectors = v1alpha1.IntendantConnectors{Map: []string{connMap}}
		if mutate != nil {
			mutate(in)
		}
	})
}

// store creates in as a release before the API's rules let it through;
// probe, a dry run of the same past the rules, says when they are back.
func (f *fixture) store(t *testing.T, in *v1alpha1.TakeCareIntendant) {
	t.Helper()
	ctx := context.Background()
	probe := in.DeepCopy()
	probe.Name = in.Name[:len(in.Name)-1] + "x"
	testenv.StoreUnvalidated(t, f.cfg, intendantsCRD,
		func() error { return f.c.Create(ctx, in.DeepCopy()) },
		func() error { return f.c.Create(ctx, probe.DeepCopy(), client.DryRunAll) })
}

// redestine changes a field no rule bounds, so that the Intendant's
// generation moves: an edit of a stored object.
func (f *fixture) redestine(name, dest string) error {
	ctx := context.Background()
	var in v1alpha1.TakeCareIntendant
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &in); err != nil {
		return err
	}
	in.Spec.Report.Practitioner = dest
	return f.c.Update(ctx, &in)
}

// At the bound, the engine agent's name is exactly 63 characters, and the
// owner label, the survey's label and its name are all accepted.
func TestNameAtTheBound(t *testing.T) {
	t.Parallel()
	f := start(t)
	name := "b" + strings.Repeat("o", v1alpha1.MaxIntendantName-2) + "d"
	f.platform(t)
	f.connector(t, connMap, v1alpha1.ConnectorMap, []string{mcp.MapEntryTool, mcp.MapListTool, toolPath})
	f.mapRevision(t, rev1)
	f.connector(t, connKnowledge, v1alpha1.ConnectorKnowledge, []string{toolRunbooks})
	f.intendant(t, name, func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.Connectors = v1alpha1.IntendantConnectors{Map: []string{connMap}, Knowledge: []string{connKnowledge}}
	})
	s := f.waitSurveys(t, name, 1)[0]
	if s.Labels[LabelIntendant] != name {
		t.Errorf("the survey's label %q", s.Labels[LabelIntendant])
	}
	agent, err := f.engineObject(agentGVK, surveyagent.AgentName(&v1alpha1.TakeCareIntendant{Name: name}))
	if err != nil {
		t.Fatalf("the agent of an Intendant at the bound: %v", err)
	}
	if n := len(agent.GetName()); n != 63 || agent.GetLabels()["takecare.agaetis.fr/owner"] != name {
		t.Errorf("agent name of %d characters, owner label %q", n, agent.GetLabels()["takecare.agaetis.fr/owner"])
	}
}

// An Intendant stored with a longer name before the rule says so on its
// status, renders nothing, takes no finalizer, and can still be edited
// (the rule holds at creation only).
func TestStoredNameTooLong(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	f.connector(t, connMap, v1alpha1.ConnectorMap, []string{mcp.MapEntryTool, mcp.MapListTool})
	f.mapRevision(t, rev1)
	name := strings.Repeat("l", v1alpha1.MaxIntendantName+1)
	f.store(t, mapOnly(name, nil))
	in := f.wait(t, name, func(in *v1alpha1.TakeCareIntendant) bool {
		return isStatus(in, v1alpha1.ConditionReady, metav1.ConditionFalse)
	})
	if c := cond(in, v1alpha1.ConditionReady); c.Reason != ReasonNameTooLong || c.Message != v1alpha1.MessageIntendantName {
		t.Errorf("Ready %+v", c)
	}
	if controllerutil.ContainsFinalizer(in, Finalizer) || f.agentTools(name) != nil || len(f.surveys(t, name)) != 0 {
		t.Errorf("finalizers %v, agent %v, surveys %d", in.Finalizers, f.agentTools(name), len(f.surveys(t, name)))
	}
	if err := f.redestine(name, "elsewhere"); err != nil {
		t.Errorf("an edit of the stored Intendant: %v", err)
	}
	f.wait(t, name, func(in *v1alpha1.TakeCareIntendant) bool {
		return isStatus(in, v1alpha1.ConditionReady, metav1.ConditionFalse) && cond(in, v1alpha1.ConditionReady).Reason == ReasonNameTooLong
	})
}

// A schedule stored with TZ= before the rule: ScheduleValid False with the
// CEL rule's sentence, no survey, and one Warning at the transition however
// many reconciles follow. The stored object stays editable.
func TestStoredScheduleTimeZone(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	f.connector(t, connMap, v1alpha1.ConnectorMap, []string{mcp.MapEntryTool, mcp.MapListTool})
	f.mapRevision(t, rev1)
	f.store(t, mapOnly("zoned", func(in *v1alpha1.TakeCareIntendant) { in.Spec.Schedule = "CRON_TZ=Europe/Paris 0 3 * * *" }))
	for i := range 3 {
		if err := f.redestine("zoned", fmt.Sprintf("d%d", i)); err != nil {
			t.Fatalf("an edit of the stored Intendant: %v", err)
		}
		f.wait(t, "zoned", func(in *v1alpha1.TakeCareIntendant) bool {
			return isStatus(in, ConditionScheduleValid, metav1.ConditionFalse)
		})
	}
	in := f.wait(t, "zoned", func(*v1alpha1.TakeCareIntendant) bool { return true })
	if c := cond(in, ConditionScheduleValid); c.Reason != ReasonScheduleTimeZone || c.Message != v1alpha1.MessageScheduleUTC {
		t.Errorf("ScheduleValid %+v", c)
	}
	if in.Status.NextSurveyTime != nil || !isStatus(in, v1alpha1.ConditionReady, metav1.ConditionFalse) || len(f.surveys(t, "zoned")) != 0 {
		t.Errorf("next %v, Ready %+v, %d surveys", in.Status.NextSurveyTime, cond(in, v1alpha1.ConditionReady), len(f.surveys(t, "zoned")))
	}
	if notes := f.events(t, ReasonScheduleTimeZone); len(notes) != 1 || notes[0] != v1alpha1.MessageScheduleUTC {
		t.Errorf("events %q, want one", notes)
	}
}

// What a stored list past a new cap does: an edit elsewhere in the spec is
// accepted (the API server ratchets an unchanged field), an edit of the
// list itself must come within the cap.
func TestStoredOverCap(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	f.connector(t, connMap, v1alpha1.ConnectorMap, []string{mcp.MapEntryTool, mcp.MapListTool})
	names := make([]string, 17)
	for i := range names {
		names[i] = fmt.Sprintf("k%d", i)
	}
	f.store(t, mapOnly("wide", func(in *v1alpha1.TakeCareIntendant) { in.Spec.Connectors.Knowledge = names }))
	if err := f.redestine("wide", "elsewhere"); err != nil {
		t.Errorf("an edit elsewhere: %v", err)
	}
	ctx := context.Background()
	var in v1alpha1.TakeCareIntendant
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "wide"}, &in); err != nil {
		t.Fatal(err)
	}
	in.Spec.Connectors.Knowledge = append(in.Spec.Connectors.Knowledge, "k17")
	if err := f.c.Update(ctx, &in); err == nil || !strings.Contains(err.Error(), "knowledge") {
		t.Errorf("an edit of the list past the cap: %v", err)
	}
}

// Suspended alone requeues nothing: every input is watched. The schedule's
// wake-up is the only one left.
func TestSuspendedDoesNotPoll(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.ready(t, "asleep", func(in *v1alpha1.TakeCareIntendant) { in.Spec.Suspend = true })
	// The agent ready: a pending agent is a reason of its own to look
	// again, whose status no watch sees.
	f.agentReady(t, "asleep", true)
	f.engineReady(t, "asleep", metav1.ConditionTrue)
	f.wait(t, "asleep", func(in *v1alpha1.TakeCareIntendant) bool {
		c := cond(in, v1alpha1.ConditionReady)
		return c != nil && c.Reason == ReasonSuspended && c.ObservedGeneration == in.Generation
	})
	r := f.reconciler()
	res, err := r.Reconcile(context.Background(), reconcileRequest("asleep"))
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's clock reads testStart, 10:00 UTC, whatever the hour
	// the test runs at: the default schedule (03:00 UTC) is 17 hours away,
	// so its wake-up can never be mistaken for the 30-second poll.
	if want := 17 * time.Hour; res.RequeueAfter != want {
		t.Errorf("a suspended Intendant requeues after %s, want the schedule's %s", res.RequeueAfter, want)
	}
}

// A name of 45 to 63 characters got its routes, its model configuration
// and the finalizer from an earlier release, never its agent: the agent's
// tool servers carry the agent's name as a label value, which the API
// server refuses past 63 bytes. The deletion path runs before the name's
// check: deleting the Intendant removes what exists.
func TestStoredLongNameDeleted(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.platform(t)
	f.connector(t, connMap, v1alpha1.ConnectorMap, []string{mcp.MapEntryTool, mcp.MapListTool})
	name := strings.Repeat("d", 50)
	f.store(t, mapOnly(name, func(in *v1alpha1.TakeCareIntendant) { in.Finalizers = []string{Finalizer} }))
	ctx := context.Background()
	var in v1alpha1.TakeCareIntendant
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &in); err != nil {
		t.Fatal(err)
	}
	// What the earlier release rendered, with the functions it used.
	var p v1alpha1.TakeCarePlatform
	if err := f.c.Get(ctx, client.ObjectKey{Name: "main"}, &p); err != nil {
		t.Fatal(err)
	}
	role, gw, agentName := roleOf(&in), routes.Gateway{Name: p.Spec.Gateway.GatewayRef.Name}, surveyagent.AgentName(&in)
	model, _, _ := roleagent.Model(&p, nil)
	agents, models := &kagent0103.Agents{Client: f.c}, &kagent0103.Models{Client: f.c}
	desired, err := surveyagent.Agent{Intendant: &in, EngineNamespace: engineNS, Model: agentName,
		Tools: []engine.ToolServer{{Connector: connMap, URL: gwURL + routes.MCPPath(role, connMap), Tools: []string{toolPath}}}}.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range []func() error{
		func() error { return routes.EnsureA2A(ctx, f.c, gw, role, agents.Endpoint(engineNS, agentName)) },
		func() error { return routes.EnsureModel(ctx, f.c, gw, role, model) },
		func() error {
			return models.EnsureModel(ctx, roleagent.ModelConfig(&p, role, model, agentName, engineNS, surveyagent.OwnerLabels(&in)))
		},
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}
	if err := agents.EnsureAgent(ctx, desired); err == nil {
		t.Error("the agent of a 50-character name was created: an earlier release could leave it behind")
	}
	if err := f.c.Delete(ctx, &in); err != nil {
		t.Fatal(err)
	}
	gone := func(get func() error) bool { return apierrors.IsNotFound(get()) }
	for deadline := time.Now().Add(waitFor); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if gone(func() error {
			return f.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &v1alpha1.TakeCareIntendant{})
		}) {
			break
		}
	}
	for what, get := range map[string]func() error{
		"the Intendant": func() error {
			return f.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &v1alpha1.TakeCareIntendant{})
		},
		"the agent":                func() error { _, err := f.engineObject(agentGVK, agentName); return err },
		"the model configuration":  func() error { _, err := f.engineObject(modelConfigGVK, agentName); return err },
		"the A2A and model routes": func() error { return f.routesLeft(t, role) },
	} {
		if !gone(get) {
			t.Errorf("%s is left after the deletion: %v", what, get())
		}
	}
}

// routesLeft returns NotFound once no HTTPRoute of the role is left.
func (f *fixture) routesLeft(t *testing.T, role routes.Role) error {
	t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRouteList"})
	if err := f.c.List(context.Background(), list, client.InNamespace(ns)); err != nil {
		return err
	}
	prefix := "role-" + role.Kind + "-" + role.Name + "-"
	for _, r := range list.Items {
		if strings.HasPrefix(r.GetName(), prefix) {
			return fmt.Errorf("route %s", r.GetName())
		}
	}
	return apierrors.NewNotFound(schema.GroupResource{Resource: "httproutes"}, prefix)
}

// A suspended Intendant whose routes cannot be rendered (no gateway yet)
// still looks again: render asks for it, whatever Ready's reason.
func TestSuspendedWithoutGatewayPolls(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.connector(t, connMap, v1alpha1.ConnectorMap, []string{mcp.MapEntryTool, mcp.MapListTool})
	f.intendant(t, "waiting", func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.Suspend = true
		in.Spec.Connectors = v1alpha1.IntendantConnectors{Map: []string{connMap}}
	})
	res, err := f.reconciler().Reconcile(context.Background(), reconcileRequest("waiting"))
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != requeueNotReady {
		t.Errorf("requeue after %s, want %s", res.RequeueAfter, requeueNotReady)
	}
}

// unindexed is the API client as the reconciler's cache: the API server
// knows no field index, and surveysOf filters by Intendant anyway.
type unindexed struct{ client.Client }

func (u unindexed) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	kept := make([]client.ListOption, 0, len(opts))
	for _, o := range opts {
		if _, ok := o.(client.MatchingFields); !ok {
			kept = append(kept, o)
		}
	}
	return u.Client.List(ctx, list, kept...)
}

// reconciler is a second Reconciler on the test's API server, called
// directly to read the Result the manager's keeps to itself.
func (f *fixture) reconciler() *Reconciler {
	return &Reconciler{
		Client: unindexed{f.c}, Reader: f.c, OperatorNamespace: ns, EngineNamespace: engineNS,
		AgentKinds: (&kagent0103.Agents{}).AgentTypes(), Now: f.clock.reconcilerNow, PassTimeout: passTimeout,
		Agents: &kagent0103.Agents{Client: f.c}, Models: &kagent0103.Models{Client: f.c},
	}
}

func reconcileRequest(name string) ctrl.Request {
	return ctrl.Request{Namespace: ns, Name: name}
}
