// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

const (
	mapConnector = "map"
	servedRev    = "9b1f0c2d7a44"
	otherName    = "other"
)

// discoverObjects is an Intendant with its map connector serving
// servedRev, and three surveys: an older one, the newest, which surveyed
// against surveyRev, and one of another Intendant.
func discoverObjects() []client.Object {
	ref := v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: intendantName}
	props := fixtureProposals()
	props[0].Source = v1alpha1.ProposalFromAgent
	newest := &v1alpha1.TakeCareSurvey{ObjectMeta: meta(surveyName, testNS, 2*time.Hour),
		Spec: v1alpha1.TakeCareSurveySpec{IntendantRef: ref, Trigger: v1alpha1.TriggerSchedule},
		Status: v1alpha1.TakeCareSurveyStatus{RunStatus: v1alpha1.RunStatus{Phase: v1alpha1.RunCompleted}, MapRevision: surveyRev,
			ProposalCounts: &v1alpha1.ProposalCounts{Add: 1, Update: 1, Remove: 1, Total: 3}, Proposals: props[:2],
			Layers: []v1alpha1.SurveyLayer{
				{Name: "compute", Seen: 4, Matched: 3, Proposed: 1},
				{Name: "network", Seen: 2, Matched: 1, Proposed: 1, NotSeen: []v1alpha1.NotSeen{{Ref: "kube-system", Reason: "outside the Intendant's scope"}}},
			},
			BlindSpots: []v1alpha1.BlindSpot{{Entity: "service/ledger", Reason: v1alpha1.BlindSpotNoAccess, Message: "no connector reads it"}}}}
	return []client.Object{
		&v1alpha1.TakeCareIntendant{ObjectMeta: meta(intendantName, testNS, 0),
			Spec: v1alpha1.TakeCareIntendantSpec{Connectors: v1alpha1.IntendantConnectors{Map: []string{mapConnector}}}},
		&v1alpha1.TakeCareConnector{ObjectMeta: meta(mapConnector, testNS, 0), Status: v1alpha1.TakeCareConnectorStatus{MapRevision: servedRev}},
		&v1alpha1.TakeCareSurvey{ObjectMeta: meta("intendant-older", testNS, 30*time.Hour), Spec: v1alpha1.TakeCareSurveySpec{IntendantRef: ref}},
		newest,
		&v1alpha1.TakeCareSurvey{ObjectMeta: meta("other-new", testNS, 0),
			Spec: v1alpha1.TakeCareSurveySpec{IntendantRef: v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: otherName}}},
	}
}

// Spec 13 §3.3: the newest survey with both Map revisions, its proposals,
// layers and blind spots, and the command that creates a new survey.
func TestDiscoverGolden(t *testing.T) {
	d, err := readDiscover(t.Context(), reviewReader(t, discoverObjects()...), testNS, "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Survey == nil || d.Survey.Name != surveyName || !d.Survey.PredatesServedMap {
		t.Fatalf("survey = %+v, want %s predating the served Map", d.Survey, surveyName)
	}
	var text bytes.Buffer
	if err := d.write(&text, testNow); err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "discover.txt", text.Bytes())
	a := &app{stdout: new(bytes.Buffer)}
	if err := a.printJSON(d); err != nil {
		t.Fatal(err)
	}
	out := a.stdout.(*bytes.Buffer).Bytes()
	golden.Assert(t, "discover.json", out)
	var back Discover
	if err := json.Unmarshal(out, &back); err != nil || back.SchemaVersion != discoverSchemaVersion {
		t.Errorf("json round trip: %v, schemaVersion %d", err, back.SchemaVersion)
	}
}

func TestDiscoverCases(t *testing.T) {
	objs := discoverObjects()
	intendant, connector, newest := objs[0], objs[1], objs[3].(*v1alpha1.TakeCareSurvey)
	sameRev := newest.DeepCopy()
	sameRev.Status.MapRevision = servedRev
	other := &v1alpha1.TakeCareIntendant{ObjectMeta: meta(otherName, testNS, 0)}
	for _, tc := range []struct {
		name, flag, wantErr string
		objs                []client.Object
		check               func(*Discover) bool
	}{
		{name: "no survey yet: the command still", objs: []client.Object{intendant, connector},
			check: func(d *Discover) bool { return d.Survey == nil && d.ServedRevision == servedRev && d.Command != "" }},
		{name: "same revision does not predate", objs: []client.Object{intendant, connector, sameRev},
			check: func(d *Discover) bool { return !d.Survey.PredatesServedMap }},
		{name: "map connector not found", objs: []client.Object{intendant, newest},
			check: func(d *Discover) bool { return d.ServedRevision == "" && !d.Survey.PredatesServedMap }},
		{name: "--intendant picks one of several", flag: otherName, objs: []client.Object{intendant, other, objs[4]},
			check: func(d *Discover) bool { return d.Intendant == otherName && d.Survey.Name == "other-new" }},
		{name: "several need --intendant", objs: []client.Object{intendant, other}, wantErr: "2 Intendants in namespace takecare-system: pass --intendant"},
		{name: "none", wantErr: "no TakeCareIntendant in namespace takecare-system"},
		{name: "unknown --intendant", flag: "ghost", objs: []client.Object{intendant}, wantErr: "read the Intendant ghost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := readDiscover(t.Context(), reviewReader(t, tc.objs...), testNS, tc.flag)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || !tc.check(d) {
				t.Errorf("err %v, discover %+v", err, d)
			}
		})
	}
}
