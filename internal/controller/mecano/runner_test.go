// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/codeserver"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	mecanoagent "github.com/agaetis-it/takecare-agents/internal/mecano"
	"github.com/agaetis-it/takecare-agents/internal/runs"
)

const (
	appImage   = "ghcr.io/example/app"
	mtName     = "maintenance-0a1b2c3d4e"
	firstTask  = "t-1"
	engineKind = "kagent"
	modeDone   = "complete"
	modeWork   = "working"
)

var unitStart = time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)

func fakeAPI(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(objs...).Build()
}

// unitMecano is a Ready Mécano on shop, bound to the cluster and code
// connectors, at level.
func unitMecano(level v1alpha1.Level, mutate func(*v1alpha1.TakeCareMecano)) *v1alpha1.TakeCareMecano {
	m := &v1alpha1.TakeCareMecano{Name: mecanoName, Namespace: ns, UID: types.UID("uid-" + mecanoName)}
	m.Spec.Level = level
	m.Spec.Scope = v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{shop}}
	m.Spec.Connectors.Cluster = []string{connCluster}
	m.Spec.Connectors.Code = []string{connCode}
	m.Status.Level = level
	meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: ReasonReady})
	if mutate != nil {
		mutate(m)
	}
	return m
}

// unitConnector is a Ready connector with its tools classed.
func unitConnector(name string, types []v1alpha1.ConnectorType, discovered []v1alpha1.ToolStatus, mutate func(*v1alpha1.TakeCareConnector)) *v1alpha1.TakeCareConnector {
	c := &v1alpha1.TakeCareConnector{Name: name, Namespace: ns}
	c.Spec.Types = types
	if slices.Contains(types, v1alpha1.ConnectorCluster) {
		c.Spec.Kubernetes = &v1alpha1.KubernetesSpec{Namespaces: []v1alpha1.Namespace{shop, "elsewhere"}}
	}
	c.Status.Tools = discovered
	meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: reasonTest})
	if mutate != nil {
		mutate(c)
	}
	return c
}

func unitMaintenance(name string, phase v1alpha1.RunPhase, mutate func(*v1alpha1.TakeCareMaintenance)) *v1alpha1.TakeCareMaintenance {
	mt := &v1alpha1.TakeCareMaintenance{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(unitStart)}
	mt.Spec.MecanoRef = v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareMecano, Name: mecanoName}
	mt.Spec.Trigger = v1alpha1.MaintenanceTriggerOnDemand
	mt.Status.Phase = phase
	if mutate != nil {
		mutate(mt)
	}
	return mt
}

func gatewayPlatform() *v1alpha1.TakeCarePlatform {
	p := &v1alpha1.TakeCarePlatform{Name: "main"}
	p.Status.GatewayURL = gwURL
	return p
}

// fakeCaller answers the collector as the kubernetes add-on and the code
// server would: one Deployment of shop running app 1.0.0, Git declaring
// 1.1.0. It refuses, as the operator's client does, any call Allowed
// refuses, and records every call.
type fakeCaller struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeCaller) Call(_ context.Context, ep mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("%s %s %v", ep.URL, tool, args))
	f.mu.Unlock()
	if !ep.Allowed(tool, args) {
		return mcp.Result{}, mcp.ErrCallForbidden
	}
	structured := func(v any) mcp.Result {
		raw, _ := json.Marshal(v)
		return mcp.Result{Structured: raw}
	}
	text := func(v any) mcp.Result {
		raw, _ := json.Marshal(v)
		return mcp.Result{Text: string(raw)}
	}
	switch {
	case tool == "resources_list" && args["kind"] == "Deployment":
		return structured(map[string]any{"items": []any{map[string]any{
			"apiVersion": "apps/v1", "kind": "Deployment", "Namespace": string(shop), "Name": "app",
			"Images": appImage + ":1.0.0", "Labels": "", "Ready": "1/1",
		}}}), nil
	case tool == "resources_list":
		return structured(map[string]any{"items": []any{}}), nil
	case tool == "repositories_list":
		return text([]map[string]string{{"name": "deploy", "revision": "abc"}}), nil
	case tool == "manifests_index":
		return text(codeserver.Index{Repository: "deploy", Revision: "abc", Documents: []codeserver.Document{
			{Path: "app.yaml", APIVersion: "apps/v1", Kind: "Deployment", Namespace: string(shop), Name: "app", Images: []string{appImage + ":1.1.0"}},
		}}), nil
	}
	return mcp.Result{IsError: true, Text: "unexpected"}, nil
}

func (f *fakeCaller) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// fakeCanary answers the propose canary with a fixed verdict and counts
// the calls.
type fakeCanary struct {
	verdict mcp.CanaryVerdict
	mu      sync.Mutex
	urls    []string
}

func (f *fakeCanary) ProposeCanary(_ context.Context, ep mcp.Endpoint) (mcp.CanaryVerdict, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls = append(f.urls, ep.URL)
	return f.verdict, 0
}

type fakeRuns struct {
	mu       sync.Mutex
	mode     string
	answer   string
	starts   int
	polled   []string
	messages []string
}

func (r *fakeRuns) Start(_ context.Context, run engine.Run) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	r.messages = append(r.messages, run.Message)
	return fmt.Sprintf("t-%d", r.starts), nil
}

func (r *fakeRuns) Poll(_ context.Context, _ engine.Run, taskID string) (engine.Result, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.polled = append(r.polled, taskID)
	if r.mode == modeDone {
		return engine.Result{Outcome: engine.OutcomeCompleted, Text: r.answer}, true, nil
	}
	return engine.Result{}, false, nil
}

func (r *fakeRuns) Cancel(context.Context, engine.Run, string) (engine.CancelResult, error) {
	return engine.CancelUnsupported, nil
}

func (r *fakeRuns) set(mode string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mode = mode
}

func (r *fakeRuns) seen() (int, []string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts, slices.Clone(r.polled), slices.Clone(r.messages)
}

// answer is the agent's answer: the drift, one step, and a pull request
// on repoDeploy and the maintenance's branch.
func answer(maintenance string) string {
	return "```json\n" + `{"findings": [{"kind": "Drift", "component": "` + appImage + `", "text": "Git declares 1.1.0, 1.0.0 runs.", "severity": "Now"}],
 "plan": {"steps": [{"id": "1", "text": "Roll out 1.1.0.", "findings": ["Drift/` + appImage + `"], "class": "reversible"}]},
 "pullRequests": [{"repository": "` + repoDeploy + `", "url": "https://github.com/team/deploy/pull/7", "branch": "` + mecanoagent.Branch(maintenance) + `"}]}` + "\n```"
}

type runnerFixture struct {
	c      client.WithWatch
	w      *Runner
	runs   *fakeRuns
	caller *fakeCaller
	canary *fakeCanary
	rec    *kevents.FakeRecorder
}

// newRunnerFixture runs the runner over objs, with the cluster and code
// connectors, until the test ends.
func newRunnerFixture(t *testing.T, mode string, verdict mcp.CanaryVerdict, objs ...client.Object) *runnerFixture {
	t.Helper()
	objs = append(objs, gatewayPlatform(),
		unitConnector(connCluster, []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}, tools(v1alpha1.ToolRead, "resources_get", "resources_list"), nil),
		unitConnector(connCode, []v1alpha1.ConnectorType{v1alpha1.ConnectorCode}, codeTools, nil))
	f := &runnerFixture{c: fakeAPI(t, objs...), runs: &fakeRuns{mode: mode, answer: answer(mtName)}, caller: &fakeCaller{},
		canary: &fakeCanary{verdict: verdict}, rec: kevents.NewFakeRecorder(20)}
	f.w = &Runner{
		Client: f.c, Reader: f.c, Queue: runs.NewQueue(8), Runs: f.runs, EngineKind: engineKind, EngineNamespace: engineNS,
		Interval: 20 * time.Millisecond, GateRetry: 50 * time.Millisecond,
		Caller: f.caller, Canary: f.canary, Recorder: f.rec,
	}
	return f
}

func (f *runnerFixture) start(t *testing.T, queued ...*v1alpha1.TakeCareMaintenance) {
	t.Helper()
	for _, mt := range queued {
		f.w.Queue.Add(client.ObjectKeyFromObject(mt), mt.CreationTimestamp.Time)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = f.w.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func (f *runnerFixture) wait(t *testing.T, name, what string, cond func(*v1alpha1.TakeCareMaintenance) bool) *v1alpha1.TakeCareMaintenance {
	t.Helper()
	var mt *v1alpha1.TakeCareMaintenance
	eventually(t, name+" "+what, func() (bool, string) {
		var got v1alpha1.TakeCareMaintenance
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &got); err != nil {
			return false, err.Error()
		}
		mt = &got
		return cond(mt), fmt.Sprintf("%+v", got.Status.RunStatus)
	})
	return mt
}

func isEnded(mt *v1alpha1.TakeCareMaintenance) bool { return ended(mt.Status.Phase) }

// An Apprentice's maintenance runs on the collector's facts and records
// the findings (spec 12 §2, §9): the collector's calls pass Allowed, the
// inventory holds the drift, the message names no repository, and the
// maintenance made by hand gets its Mécano as controller.
func TestRunnerApprentice(t *testing.T) {
	mt := unitMaintenance(mtName, v1alpha1.RunQueued, nil)
	f := newRunnerFixture(t, modeDone, mcp.CanaryRefused, unitMecano(v1alpha1.LevelApprentice, nil), mt)
	f.start(t, mt)
	got := f.wait(t, mtName, "ended", isEnded)
	st := got.Status
	if st.Phase != v1alpha1.RunCompleted || st.Reason != "" || st.Engine == nil || st.Engine.TaskID != firstTask {
		t.Fatalf("status %+v", st.RunStatus)
	}
	if st.Inventory == nil || !slices.ContainsFunc(st.Inventory.Components, func(c v1alpha1.InventoryComponent) bool {
		return c.Key == appImage && c.Live == "1.0.0" && c.Declared == "1.1.0" && c.Drift == v1alpha1.DriftVersion
	}) {
		t.Errorf("inventory %+v", st.Inventory)
	}
	if !slices.Equal(mecanoagent.FindingIDs(st.Findings), []string{"Drift/" + appImage}) || st.Plan != nil ||
		st.Counts == nil || *st.Counts != (v1alpha1.MaintenanceCounts{Findings: 1, Now: 1}) {
		t.Errorf("findings %+v plan %+v counts %+v", st.Findings, st.Plan, st.Counts)
	}
	if ctrlRef := metav1.GetControllerOf(got); ctrlRef == nil || ctrlRef.Name != mecanoName {
		t.Errorf("controller %+v", ctrlRef)
	}
	for _, call := range f.caller.seen() {
		if strings.Contains(call, "elsewhere") {
			t.Errorf("a namespace out of scope was read: %s", call)
		}
	}
	_, _, msgs := f.runs.seen()
	if len(msgs) != 1 || !strings.Contains(msgs[0], appImage) || strings.Contains(msgs[0], repoDeploy) {
		t.Errorf("message %q", msgs)
	}
	if len(f.canary.urls) != 0 {
		t.Error("an Apprentice's maintenance sent the propose canary")
	}
	var m v1alpha1.TakeCareMecano
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: mecanoName}, &m); err != nil || m.Status.LastMaintenance != mtName {
		t.Errorf("the Mécano's last maintenance: %q (%v)", m.Status.LastMaintenance, err)
	}
}

// The propose route at a Practitioner's maintenance (spec 12 §6.4): the
// message names the repository only when the gateway enforces the rule
// rendered now and refuses the canary; otherwise no repository, a blind
// spot and a warning event. A pull request in the answer stays only on a
// valid repository and branch, never in a silent maintenance.
func TestRunnerPropose(t *testing.T) {
	m := unitMecano(v1alpha1.LevelPractitioner, practitionerSlots)
	role := RoleOf(m)
	for _, tc := range []struct {
		name    string
		policy  client.Object
		verdict mcp.CanaryVerdict
		silent  bool
		// offered: the message names the repository; kept: the pull
		// request stays; spot: the blind spot on the propose connector.
		offered, kept bool
		spot          string
	}{
		{name: "proven", policy: enforcedPolicy(role, repoDeploy), verdict: mcp.CanaryRefused, offered: true, kept: true},
		{name: "canary let through", policy: enforcedPolicy(role, repoDeploy), verdict: mcp.CanaryLetThrough, kept: true, spot: "did not refuse the propose route's canary (let-through)"},
		{name: "canary pending", policy: enforcedPolicy(role, repoDeploy), verdict: mcp.CanaryPending, kept: true, spot: "(pending)"},
		{name: "the rule of other repositories", policy: enforcedPolicy(role, repoDeploy, repoOther), verdict: mcp.CanaryRefused, kept: true, spot: "does not enforce the rule"},
		{name: "no verdict", verdict: mcp.CanaryRefused, kept: true, spot: "does not enforce the rule"},
		{name: "silent", policy: enforcedPolicy(role, repoDeploy), verdict: mcp.CanaryRefused, silent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mt := unitMaintenance(mtName, v1alpha1.RunQueued, func(mt *v1alpha1.TakeCareMaintenance) { mt.Spec.Silent = tc.silent })
			objs := []client.Object{m.DeepCopy(), mt, readyProposeConnector(nil)}
			if tc.policy != nil {
				objs = append(objs, tc.policy.DeepCopyObject().(client.Object))
			}
			f := newRunnerFixture(t, modeDone, tc.verdict, objs...)
			f.start(t, mt)
			st := f.wait(t, mtName, "ended", isEnded).Status
			if st.Phase != v1alpha1.RunCompleted || st.Plan == nil {
				t.Fatalf("status %+v plan %+v", st.RunStatus, st.Plan)
			}
			_, _, msgs := f.runs.seen()
			if offered := strings.Contains(msgs[0], "- "+repoDeploy); offered != tc.offered {
				t.Errorf("message names the repository: %v, want %v", offered, tc.offered)
			}
			if kept := len(st.Plan.PullRequests) == 1; kept != tc.kept {
				t.Errorf("pull request kept: %v, want %v (%+v)", kept, tc.kept, st.BlindSpots)
			}
			spot := slices.ContainsFunc(st.BlindSpots, func(n v1alpha1.NotSeen) bool { return n.Ref == connPropose && strings.Contains(n.Reason, tc.spot) })
			if tc.spot != "" && !spot {
				t.Errorf("no blind spot %q: %+v", tc.spot, st.BlindSpots)
			}
			if events := len(f.rec.Events); (tc.spot != "") != (events > 0) {
				t.Errorf("%d warning events, want one only with a blind spot", events)
			}
			if tc.silent && (len(f.canary.urls) != 0 || !slices.ContainsFunc(st.BlindSpots, func(n v1alpha1.NotSeen) bool { return n.Reason == mecanoagent.SilentPR })) {
				t.Errorf("silent: canary %v, blind spots %+v", f.canary.urls, st.BlindSpots)
			}
		})
	}
}

// practitionerSlots binds the propose connector with repoDeploy.
func practitionerSlots(m *v1alpha1.TakeCareMecano) {
	m.Spec.Connectors.Propose = []string{connPropose}
	m.Spec.Maintenance.Repositories = []v1alpha1.MaintenanceRepository{{Connector: connPropose, Repository: repoDeploy}}
}

// A maintenance whose Mécano does not exist ends Failed naming it, never
// left Queued (spec 12 §3); one whose Mécano is not Ready or suspended
// ends Failed without a run.
func TestRunnerRefuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mecano *v1alpha1.TakeCareMecano
		reason string
	}{
		{"no Mécano", nil, "the Mécano mecano does not exist"},
		{"suspended", unitMecano(v1alpha1.LevelApprentice, func(m *v1alpha1.TakeCareMecano) { m.Spec.Suspend = true }), failNotReady},
		{"not Ready", unitMecano(v1alpha1.LevelApprentice, func(m *v1alpha1.TakeCareMecano) {
			meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: ReasonNotReady})
		}), failNotReady},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mt := unitMaintenance(mtName, v1alpha1.RunQueued, nil)
			objs := []client.Object{mt}
			if tc.mecano != nil {
				objs = append(objs, tc.mecano)
			}
			f := newRunnerFixture(t, modeDone, mcp.CanaryRefused, objs...)
			f.start(t, mt)
			st := f.wait(t, mtName, "ended", isEnded).Status
			if st.Phase != v1alpha1.RunFailed || st.Reason != tc.reason {
				t.Errorf("status %+v", st.RunStatus)
			}
			if starts, _, _ := f.runs.seen(); starts != 0 {
				t.Errorf("%d runs started", starts)
			}
		})
	}
}

// An answer that does not parse leaves the maintenance Completed with the
// inventory only and the fixed phrase (spec 12 §9).
func TestRunnerUnreadable(t *testing.T) {
	mt := unitMaintenance(mtName, v1alpha1.RunQueued, nil)
	f := newRunnerFixture(t, modeDone, mcp.CanaryRefused, unitMecano(v1alpha1.LevelApprentice, nil), mt)
	f.runs.answer = "no json block, canary-text"
	f.start(t, mt)
	st := f.wait(t, mtName, "ended", isEnded).Status
	if st.Phase != v1alpha1.RunCompleted || st.Reason != mecanoagent.ErrUnreadable.Error() || st.Inventory == nil || len(st.Findings) != 0 {
		t.Errorf("status %+v findings %+v", st.RunStatus, st.Findings)
	}
}

// One maintenance at a time per Mécano: the second waits Queued with
// MecanoBusy, then runs once the first ends.
func TestRunnerMecanoBusy(t *testing.T) {
	first := unitMaintenance(mtName, v1alpha1.RunQueued, nil)
	second := unitMaintenance("maintenance-second", v1alpha1.RunQueued, func(mt *v1alpha1.TakeCareMaintenance) {
		mt.CreationTimestamp = metav1.NewTime(unitStart.Add(time.Minute))
	})
	f := newRunnerFixture(t, modeWork, mcp.CanaryRefused, unitMecano(v1alpha1.LevelApprentice, nil), first, second)
	f.start(t, first, second)
	f.wait(t, mtName, "running", func(mt *v1alpha1.TakeCareMaintenance) bool { return mt.Status.Phase == v1alpha1.RunRunning })
	f.wait(t, second.Name, "waiting", func(mt *v1alpha1.TakeCareMaintenance) bool {
		return mt.Status.Phase == v1alpha1.RunQueued && strings.HasPrefix(mt.Status.Reason, ReasonMecanoBusy+": the Mécano mecano runs maintenance "+mtName)
	})
	f.runs.set(modeDone)
	f.wait(t, second.Name, "ended", isEnded)
}

// A maintenance left Running with its task by a stopped leader is
// followed again, its task and start kept (spec 12 §9).
func TestRunnerResumes(t *testing.T) {
	started := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
	mt := unitMaintenance(mtName, v1alpha1.RunRunning, func(mt *v1alpha1.TakeCareMaintenance) {
		mt.Status.StartedAt, mt.Status.Attempts = &started, 1
		mt.Status.Engine = &v1alpha1.EngineRun{Kind: engineKind, Agent: agentMecano, TaskID: "t-old"}
	})
	f := newRunnerFixture(t, modeDone, mcp.CanaryRefused, unitMecano(v1alpha1.LevelApprentice, nil), mt)
	f.start(t)
	st := f.wait(t, mtName, "ended", isEnded).Status
	starts, polled, _ := f.runs.seen()
	if st.Phase != v1alpha1.RunCompleted || starts != 0 || !slices.Contains(polled, "t-old") || st.Attempts != 2 || !st.StartedAt.Equal(&started) {
		t.Errorf("status %+v, starts %d, polled %v", st.RunStatus, starts, polled)
	}
}
