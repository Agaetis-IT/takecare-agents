// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package roleagent

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/controller/platform"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/routes"
)

const (
	fast        = "fast"
	podsLog     = "pods_log"
	resources   = "resources_list"
	configMaps  = "configmaps_get"
	namespaces  = "namespaces_list"
	proposeTool = "propose_change"
	fileGet     = "file_get"
)

func platformWith(models ...string) *v1alpha1.TakeCarePlatform {
	p := &v1alpha1.TakeCarePlatform{}
	for _, m := range models {
		p.Spec.Models = append(p.Spec.Models, v1alpha1.ModelSpec{Name: m, Model: "provider-" + m})
	}
	p.Status.GatewayURL = "http://gw.example"
	return p
}

func TestModel(t *testing.T) {
	for name, tc := range map[string]struct {
		p                      *v1alpha1.TakeCarePlatform
		want                   *v1alpha1.ModelRef
		model, reason, message string
	}{
		"no platform":      {nil, nil, "", ReasonPlatformMissing, "no TakeCarePlatform"},
		"no model":         {platformWith(), nil, "", ReasonModelUnknown, "the platform declares no model"},
		"the first":        {platformWith("default", fast), nil, "default", "", ""},
		"the one named":    {platformWith("default", fast), &v1alpha1.ModelRef{Name: fast}, fast, "", ""},
		"one not declared": {platformWith("default"), &v1alpha1.ModelRef{Name: "other"}, "", ReasonModelUnknown, "spec.model names other, which the platform does not declare"},
		"named, no models": {platformWith(), &v1alpha1.ModelRef{Name: fast}, "", ReasonModelUnknown, "the platform declares no model"},
	} {
		t.Run(name, func(t *testing.T) {
			model, reason, message := Model(tc.p, tc.want)
			if model != tc.model || reason != tc.reason || message != tc.message {
				t.Errorf("Model = %q, %q, %q; want %q, %q, %q", model, reason, message, tc.model, tc.reason, tc.message)
			}
		})
	}
}

// The configuration reaches the model through the role's own route, with
// the engine's dummy key, never a provider key.
func TestModelConfig(t *testing.T) {
	role := routes.Role{Kind: "intendant", Name: "main", Namespace: "takecare-system"}
	labels := map[string]string{"takecare.agaetis.fr/owner": "main"}
	got := ModelConfig(platformWith("default", fast), role, fast, "takecare-intendant-main", "kagent", labels)
	want := engine.Model{
		Name: "takecare-intendant-main", Namespace: "kagent", Model: "provider-fast",
		GatewayURL:     "http://gw.example/roles/intendant.main/models/fast/v1",
		DummyKeySecret: platform.DummyKeySecretName, DummyKeyField: platform.DummyKeyField,
		Labels: labels,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ModelConfig (-want +got):\n%s", diff)
	}
}

func TestTools(t *testing.T) {
	c := &v1alpha1.TakeCareConnector{}
	c.Status.Tools = []v1alpha1.ToolStatus{
		{Name: podsLog, Class: v1alpha1.ToolRead, Content: true},
		{Name: resources, Class: v1alpha1.ToolRead},
		{Name: configMaps, Class: v1alpha1.ToolRead, Content: true},
		{Name: "pods_delete", Class: v1alpha1.ToolAct},
		{Name: namespaces, Class: v1alpha1.ToolRead},
	}
	for name, tc := range map[string]struct {
		content        []string
		read, withheld []string
	}{
		"no content tool listed": {nil, []string{namespaces, resources}, []string{configMaps, podsLog}},
		"one listed":             {[]string{podsLog}, []string{namespaces, podsLog, resources}, []string{configMaps}},
		"an act tool listed":     {[]string{"pods_delete"}, []string{namespaces, resources}, []string{configMaps, podsLog}},
	} {
		t.Run(name, func(t *testing.T) {
			read, withheld := Tools(c, tc.content)
			if diff := cmp.Diff(tc.read, read); diff != "" {
				t.Errorf("read (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.withheld, withheld); diff != "" {
				t.Errorf("withheld (-want +got):\n%s", diff)
			}
		})
	}
	if read, withheld := Tools(&v1alpha1.TakeCareConnector{}, nil); read != nil || withheld != nil {
		t.Errorf("a connector never discovered: read %v, withheld %v", read, withheld)
	}
}

// A propose tool reaches a role only from the propose slot and only at
// Practitioner and above (spec 12 §6.4); read and act tools never come
// through it, and Tools never gives a propose tool.
func TestProposeTools(t *testing.T) {
	c := &v1alpha1.TakeCareConnector{}
	c.Status.Tools = []v1alpha1.ToolStatus{
		{Name: proposeTool, Class: v1alpha1.ToolPropose},
		{Name: fileGet, Class: v1alpha1.ToolRead, Content: true},
		{Name: "merge", Class: v1alpha1.ToolAct},
	}
	propose := []string{proposeTool}
	for name, tc := range map[string]struct {
		level v1alpha1.Level
		slot  string
		want  []string
	}{
		"Apprentice, propose slot":       {v1alpha1.LevelApprentice, ProposeSlot, nil},
		"no level, propose slot":         {"", ProposeSlot, nil},
		"Practitioner, propose slot":     {v1alpha1.LevelPractitioner, ProposeSlot, propose},
		"Expert, propose slot":           {v1alpha1.LevelExpert, ProposeSlot, propose},
		"Practitioner, code slot":        {v1alpha1.LevelPractitioner, "code", nil},
		"an unknown level, propose slot": {"Master", ProposeSlot, nil},
	} {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, ProposeTools(c, tc.level, tc.slot)); diff != "" {
				t.Errorf("ProposeTools (-want +got):\n%s", diff)
			}
		})
	}
	if read, _ := Tools(c, []string{fileGet, proposeTool}); !cmp.Equal(read, []string{fileGet}) {
		t.Errorf("Tools gives %v, want the read tool only", read)
	}
}
