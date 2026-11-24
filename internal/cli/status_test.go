// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

const testNS = "takecare-system"

var testNow = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

func cond(typ string, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{Type: typ, Status: status, Reason: reason, Message: message}
}

func meta(name, ns string, age time.Duration) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(testNow.Add(-age))}
}

// fixtures is an install with one of each kind, a few faults, and six
// surveys, one more than status shows.
func fixtures() []client.Object {
	readyTrue := cond(v1alpha1.ConditionReady, metav1.ConditionTrue, "Ready", "")
	objs := []client.Object{
		&v1alpha1.TakeCarePlatform{ObjectMeta: meta("main", "", time.Hour), Status: v1alpha1.TakeCarePlatformStatus{Conditions: []metav1.Condition{
			readyTrue,
			cond(v1alpha1.ConditionProductionReady, metav1.ConditionFalse, "Gaps", "2 advisory gaps"),
		}}},
		&v1alpha1.TakeCareDoc{ObjectMeta: meta("doc", testNS, time.Hour), Status: v1alpha1.TakeCareDocStatus{RoleStatus: v1alpha1.RoleStatus{
			Level: v1alpha1.LevelPractitioner,
			Conditions: []metav1.Condition{
				readyTrue,
				cond(v1alpha1.ConditionDegraded, metav1.ConditionTrue, v1alpha1.ReasonBlindSpots, "1 entity not covered"),
				cond(v1alpha1.ConditionConnectorsReady, metav1.ConditionTrue, "Ready", ""),
			},
			BlindSpots: []v1alpha1.BlindSpot{{Entity: "service/billing-api", Reason: v1alpha1.BlindSpotNotValidated, Message: "the entry is proposed"}},
		}}},
		&v1alpha1.TakeCareIntendant{ObjectMeta: meta("intendant", testNS, time.Hour), Status: v1alpha1.TakeCareIntendantStatus{RoleStatus: v1alpha1.RoleStatus{
			Level: v1alpha1.LevelApprentice,
			Conditions: []metav1.Condition{
				cond(v1alpha1.ConditionReady, metav1.ConditionFalse, "ConnectorsNotReady", "map is not Ready"),
				cond(v1alpha1.ConditionConnectorsReady, metav1.ConditionFalse, "NotReady", "map"),
				// Degraded is good when False: not shown.
				cond(v1alpha1.ConditionDegraded, metav1.ConditionFalse, "Complete", "the scope is covered"),
			},
		}}},
		// Another namespace's role is not shown.
		&v1alpha1.TakeCareDoc{ObjectMeta: meta("elsewhere", "other", time.Hour)},
		&v1alpha1.TakeCareRanger{ObjectMeta: meta("main", "", time.Hour), Spec: v1alpha1.TakeCareRangerSpec{GuardrailCommonSpec: v1alpha1.GuardrailCommonSpec{Level: v1alpha1.LevelApprentice}},
			Status: v1alpha1.TakeCareRangerStatus{Conditions: []metav1.Condition{readyTrue}}},
		&v1alpha1.TakeCareConnector{ObjectMeta: meta("kubernetes", testNS, time.Hour),
			Spec: v1alpha1.TakeCareConnectorSpec{Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster, v1alpha1.ConnectorLogs}},
			Status: v1alpha1.TakeCareConnectorStatus{Conditions: []metav1.Condition{readyTrue}, Tools: []v1alpha1.ToolStatus{
				{Name: "list_pods", Class: v1alpha1.ToolRead}, {Name: "get_events", Class: v1alpha1.ToolRead},
				{Name: "pod_logs", Class: v1alpha1.ToolRead, Content: true}, {Name: "scale", Class: v1alpha1.ToolAct},
			}}},
		&v1alpha1.TakeCareConnector{ObjectMeta: meta("map", testNS, time.Hour),
			Spec: v1alpha1.TakeCareConnectorSpec{Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorMap}},
			Status: v1alpha1.TakeCareConnectorStatus{
				Conditions:  []metav1.Condition{cond(v1alpha1.ConditionReady, metav1.ConditionFalse, "Unreachable", "connection refused")},
				Tools:       []v1alpha1.ToolStatus{{Name: "propose_entry", Class: v1alpha1.ToolPropose}},
				MapRevision: "4f9c2e1",
			}},
		// No Ready condition yet: not Ready.
		&v1alpha1.TakeCareConnector{ObjectMeta: meta("report", testNS, time.Hour),
			Spec: v1alpha1.TakeCareConnectorSpec{Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorReport}}},
		&v1alpha1.TakeCareInvestigation{ObjectMeta: meta("doc-higherrorrate-1", testNS, 3*time.Minute),
			Spec:   v1alpha1.TakeCareInvestigationSpec{Alert: v1alpha1.AlertRecord{Name: "HighErrorRate"}},
			Status: v1alpha1.TakeCareInvestigationStatus{RunStatus: v1alpha1.RunStatus{Phase: v1alpha1.RunFailed, Reason: "TimedOut"}}},
		&v1alpha1.TakeCareInvestigation{ObjectMeta: meta("doc-higherrorrate-0", testNS, 2*time.Hour),
			Spec:   v1alpha1.TakeCareInvestigationSpec{Alert: v1alpha1.AlertRecord{Name: "HighErrorRate"}},
			Status: v1alpha1.TakeCareInvestigationStatus{RunStatus: v1alpha1.RunStatus{Phase: v1alpha1.RunCompleted, Reason: "ignored once completed"}}},
	}
	for i := range 6 {
		objs = append(objs, &v1alpha1.TakeCareSurvey{ObjectMeta: meta(fmt.Sprintf("intendant-%d", i), testNS, time.Duration(i+1)*24*time.Hour),
			Spec:   v1alpha1.TakeCareSurveySpec{Trigger: v1alpha1.TriggerSchedule},
			Status: v1alpha1.TakeCareSurveyStatus{RunStatus: v1alpha1.RunStatus{Phase: v1alpha1.RunCompleted}}})
	}
	return objs
}

func fakeReader(t *testing.T, objs ...client.Object) client.Reader {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func TestStatusOutput(t *testing.T) {
	s, err := readStatus(t.Context(), fakeReader(t, fixtures()...), testNS)
	if err != nil {
		t.Fatal(err)
	}
	var text bytes.Buffer
	if err := s.write(&text, testNow); err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "status.txt", text.Bytes())

	a := &app{stdout: new(bytes.Buffer)}
	if err := a.printJSON(s); err != nil {
		t.Fatal(err)
	}
	out := a.stdout.(*bytes.Buffer).Bytes()
	golden.Assert(t, "status.json", out)
	var back Status
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back.SchemaVersion != statusSchemaVersion {
		t.Errorf("schemaVersion = %d, want %d", back.SchemaVersion, statusSchemaVersion)
	}
}

// Spec 13 §3.2: exit code 1 when the platform, a role or a connector is
// not Ready, and only then.
func TestStatusNotReady(t *testing.T) {
	ready := []metav1.Condition{cond(v1alpha1.ConditionReady, metav1.ConditionTrue, "Ready", "")}
	notReady := []metav1.Condition{cond(v1alpha1.ConditionReady, metav1.ConditionFalse, "X", "")}
	platform := func(c []metav1.Condition) client.Object {
		return &v1alpha1.TakeCarePlatform{ObjectMeta: meta("main", "", 0), Status: v1alpha1.TakeCarePlatformStatus{Conditions: c}}
	}
	doc := func(c []metav1.Condition) client.Object {
		return &v1alpha1.TakeCareDoc{ObjectMeta: meta("doc", testNS, 0), Status: v1alpha1.TakeCareDocStatus{RoleStatus: v1alpha1.RoleStatus{Conditions: c}}}
	}
	ranger := func(c []metav1.Condition) client.Object {
		return &v1alpha1.TakeCareRanger{ObjectMeta: meta("main", "", 0), Status: v1alpha1.TakeCareRangerStatus{Conditions: c}}
	}
	connector := func(c []metav1.Condition) client.Object {
		return &v1alpha1.TakeCareConnector{ObjectMeta: meta("k", testNS, 0), Status: v1alpha1.TakeCareConnectorStatus{Conditions: c}}
	}
	failed := &v1alpha1.TakeCareSurvey{ObjectMeta: meta("s", testNS, 0), Status: v1alpha1.TakeCareSurveyStatus{RunStatus: v1alpha1.RunStatus{Phase: v1alpha1.RunFailed}}}
	for _, tc := range []struct {
		name string
		objs []client.Object
		want []string
	}{
		{"all Ready", []client.Object{platform(ready), doc(ready), ranger(ready), connector(ready)}, []string{}},
		{"a failed survey is not a readiness finding", []client.Object{platform(ready), failed}, []string{}},
		{"no platform", []client.Object{doc(ready)}, []string{"TakeCarePlatform (none found)"}},
		{"platform not Ready", []client.Object{platform(notReady)}, []string{"TakeCarePlatform/main"}},
		{"platform without Ready", []client.Object{platform(nil)}, []string{"TakeCarePlatform/main"}},
		{"role not Ready", []client.Object{platform(ready), doc(notReady)}, []string{"TakeCareDoc/doc"}},
		{"Ranger not Ready", []client.Object{platform(ready), ranger(notReady)}, []string{"TakeCareRanger/main"}},
		{"connector without Ready", []client.Object{platform(ready), connector(nil)}, []string{"TakeCareConnector/k"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := readStatus(t.Context(), fakeReader(t, tc.objs...), testNS)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want, s.NotReady); diff != "" {
				t.Errorf("notReady (-want +got):\n%s", diff)
			}
		})
	}
}

// refusing fails the List of one resource.
type refusing struct {
	client.Reader
	refuse string
}

func (r refusing) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*v1alpha1.TakeCareConnectorList); ok {
		return errors.New(r.refuse)
	}
	return r.Reader.List(ctx, list, opts...)
}

// A list status cannot read stops it, naming what it could not read: a
// partial install is never shown as complete.
func TestStatusReadError(t *testing.T) {
	r := refusing{Reader: fakeReader(t, fixtures()...), refuse: "forbidden"}
	_, err := readStatus(t.Context(), r, testNS)
	if err == nil || err.Error() != "list takecareconnectors in namespace takecare-system: forbidden" {
		t.Errorf("err = %v", err)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want int
	}{
		{nil, ExitError},
		{[]string{"apply"}, ExitError},
		{[]string{"help"}, ExitOK},
		{[]string{"status", "-h"}, ExitOK},
		{[]string{"status", "--output", "yaml"}, ExitError},
		{[]string{"status", "extra"}, ExitError},
		{[]string{"status", "--unknown"}, ExitError},
		{[]string{"status", "--kubeconfig", "/nonexistent/kubeconfig"}, ExitError},
		{[]string{"version", "--output", "yaml"}, ExitError},
	} {
		var out, errOut bytes.Buffer
		a := &app{stdout: &out, stderr: &errOut, now: func() time.Time { return testNow }}
		if got := a.run(context.Background(), tc.args); got != tc.want {
			t.Errorf("%v: exit %d, want %d (stderr %q)", tc.args, got, tc.want, errOut.String())
		}
	}
}
