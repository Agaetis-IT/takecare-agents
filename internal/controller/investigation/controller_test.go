// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package investigation

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/runs"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

const ns = "takecare-system"

func manager(t *testing.T, cfg *ctrlConfig, queue *runs.Queue) context.CancelFunc {
	t.Helper()
	skip := true
	mgr, err := ctrl.NewManager(cfg.rest, ctrl.Options{Scheme: cfg.scheme, Metrics: metricsserver.Options{BindAddress: "0"}, Controller: config.Controller{SkipNameValidation: &skip}})
	if err != nil {
		t.Fatal(err)
	}
	phases, err := NewPhaseMetric(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Reconciler{Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("test"), Queue: queue, Phases: phases}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	return cancel
}

type ctrlConfig struct {
	rest   *rest.Config
	scheme *runtime.Scheme
}

func phaseOf(t *testing.T, c client.Client, name string) v1alpha1.RunPhase {
	t.Helper()
	var inv v1alpha1.TakeCareInvestigation
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &inv); err != nil {
		return ""
	}
	return inv.Status.Phase
}

func waitPhase(t *testing.T, c client.Client, name string, want v1alpha1.RunPhase) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if phaseOf(t, c, name) == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s did not reach %s (at %q)", name, want, phaseOf(t, c, name))
}

// waitQueued waits for the record to be in the queue: the phase is written
// one reconcile before the record is added.
func waitQueued(t *testing.T, q *runs.Queue, name string) {
	t.Helper()
	key := types.NamespacedName{Namespace: ns, Name: name}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if q.Contains(key) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s is Queued but not in the queue", name)
}

func create(t *testing.T, c client.Client, name string) {
	t.Helper()
	inv := &v1alpha1.TakeCareInvestigation{
		Name: name, Namespace: ns,
		Spec: v1alpha1.TakeCareInvestigationSpec{
			RoleRef: v1alpha1.RoleRef{Kind: "TakeCareDoc", Name: docName},
			Alert:   v1alpha1.AlertRecord{Name: alertName, Namespace: "demo-apps"},
			Level:   v1alpha1.LevelPractitioner,
		},
	}
	if err := c.Create(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
}

// New records are Queued and held in the bounded queue; one that does not
// fit is Dropped with a reason; a new leader rebuilds the queue from the
// Queued records.
func TestAdmission(t *testing.T) {
	restCfg := testenv.Start(t)
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.Background(), &corev1.Namespace{Name: ns}); err != nil {
		t.Fatal(err)
	}
	cfg := &ctrlConfig{rest: restCfg, scheme: scheme}

	queue := runs.NewQueue(2)
	stop := manager(t, cfg, queue)
	// The order the controller admits records in decides which one is
	// dropped; wait for each admission before creating the next, rather than
	// betting on a delay (a slow runner admitted doc-run-2 before doc-run-1).
	for i, want := range []v1alpha1.RunPhase{v1alpha1.PhaseQueued, v1alpha1.PhaseQueued, v1alpha1.PhaseDropped} {
		name := fmt.Sprintf("doc-run-%d", i)
		create(t, c, name)
		waitPhase(t, c, name, want)
		if want == v1alpha1.PhaseQueued {
			waitQueued(t, queue, name)
		}
	}
	var dropped v1alpha1.TakeCareInvestigation
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "doc-run-2"}, &dropped); err != nil {
		t.Fatal(err)
	}
	if dropped.Status.Reason == "" || dropped.Status.CompletedAt == nil {
		t.Errorf("a dropped run says why and when: %+v", dropped.Status.RunStatus)
	}
	if queue.Len() != 2 {
		t.Errorf("queue holds %d", queue.Len())
	}

	// Failover: a new leader starts with an empty queue and rebuilds it.
	stop()
	rebuilt := runs.NewQueue(5)
	stop = manager(t, cfg, rebuilt)
	defer stop()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && rebuilt.Len() < 2 {
		time.Sleep(100 * time.Millisecond)
	}
	for _, n := range []string{"doc-run-0", "doc-run-1"} {
		if !rebuilt.Contains(types.NamespacedName{Namespace: ns, Name: n}) {
			t.Errorf("the new leader's queue misses %s", n)
		}
	}
	if rebuilt.Contains(types.NamespacedName{Namespace: ns, Name: "doc-run-2"}) {
		t.Error("a dropped run came back into the queue")
	}
	next, err := rebuilt.Next(context.Background())
	if err != nil || next.Name != "doc-run-0" {
		t.Errorf("the oldest run is served first: %v, %v", next, err)
	}
}
