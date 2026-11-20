// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"strings"
	"testing"
	"time"

	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// logTool is a content tool a bound connector exposes.
const logTool = "pods_log"

// D: a content tool the Intendant lists that no bound connector offers is
// said, as the Doc says it: in ConnectorsReady's message and in one
// ContentToolUnknown warning, not one per reconcile.
func TestContentToolUnknownIsSaid(t *testing.T) {
	f := start(t)
	f.connector(t, "kn", v1alpha1.ConnectorKnowledge, []string{"search_runbooks", logTool}, logTool)
	f.ready(t, "tools", func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.Connectors.Knowledge = []string{"kn"}
		in.Spec.ContentTools = []string{logTool, "nope_tool"}
	})
	const said = "spec.contentTools lists nope_tool, which no bound connector exposes as a content tool"
	f.wait(t, "tools", func(in *v1alpha1.TakeCareIntendant) bool {
		c := meta.FindStatusCondition(in.Status.Conditions, v1alpha1.ConditionConnectorsReady)
		return c != nil && strings.Contains(c.Message, said)
	})
	if notes := f.events(t, ReasonContentToolUnknown); len(notes) != 1 || !strings.Contains(notes[0], "nope_tool") || strings.Contains(notes[0], logTool) {
		t.Fatalf("ContentToolUnknown events %v, want one naming nope_tool only", notes)
	}
	// Another reconcile: the same unknown tool is not said again.
	f.change(t, "tools", func(in *v1alpha1.TakeCareIntendant) { in.Labels = map[string]string{"poke": "1"} })
	time.Sleep(time.Second)
	f.eventCount(t, ReasonContentToolUnknown, 1)
	// The set changes: a second unknown tool is said, with the first.
	f.change(t, "tools", func(in *v1alpha1.TakeCareIntendant) {
		in.Spec.ContentTools = []string{logTool, "nope_tool", "other_tool"}
	})
	f.eventCount(t, ReasonContentToolUnknown, 2)
	// No unknown tool left: nothing more is said, and the condition drops it.
	f.change(t, "tools", func(in *v1alpha1.TakeCareIntendant) { in.Spec.ContentTools = []string{logTool} })
	f.wait(t, "tools", func(in *v1alpha1.TakeCareIntendant) bool {
		c := meta.FindStatusCondition(in.Status.Conditions, v1alpha1.ConditionConnectorsReady)
		return c != nil && c.ObservedGeneration == in.Generation && !strings.Contains(c.Message, "spec.contentTools lists")
	})
	time.Sleep(time.Second)
	f.eventCount(t, ReasonContentToolUnknown, 2)
}

// change updates the Intendant through mutate, on a fresh read.
// The operator writes the Intendant too (its finalizer, its status), so a
// write between the read and the update conflicts: f.update reads again
// and retries, as every other change in the package does.
func (f *fixture) change(t *testing.T, name string, mutate func(*v1alpha1.TakeCareIntendant)) {
	t.Helper()
	f.update(t, name, mutate)
}

// eventCount waits for exactly want events of a reason on the Intendants,
// and fails if there are more or fewer at the deadline.
func (f *fixture) eventCount(t *testing.T, reason string, want int) {
	t.Helper()
	var notes []string
	for deadline := time.Now().Add(waitFor); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		var list eventsv1.EventList
		if err := f.c.List(context.Background(), &list); err != nil {
			t.Fatal(err)
		}
		notes = notes[:0]
		for _, e := range list.Items {
			if e.Reason == reason && e.Regarding.Kind == v1alpha1.KindTakeCareIntendant {
				notes = append(notes, e.Note)
			}
		}
		if len(notes) == want {
			return
		}
	}
	t.Errorf("%s events %d, want %d: %v", reason, len(notes), want, notes)
}
