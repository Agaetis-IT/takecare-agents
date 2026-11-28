// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"strings"
	"testing"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

const maintenanceName = "maintenance-0a1b2c3d4e"

// demoInventory is a small inventory: a component behind and in drift, a
// removed apiVersion in Git, a certificate near expiry.
var demoInventory = v1alpha1.MaintenanceInventory{
	Components: []v1alpha1.InventoryComponent{
		{Key: "ghcr.io/agaetis-it/takecare-agents-demo-app", Live: "0.2.0", Declared: "0.3.0", Latest: "0.3.0", Drift: v1alpha1.DriftVersion,
			Evidence: []v1alpha1.Evidence{{Tool: "resources_list", Record: "kubernetes:demo-apps/Deployment/billing-api"}}},
		{Key: "kubernetes", Live: "1.32.8", Latest: "1.37.2", Drift: v1alpha1.DriftUnknown},
	},
	APIs: []v1alpha1.DeprecatedAPIUse{{APIVersion: "batch/v1beta1", Kind: "CronJob", Name: "nightly-export", RemovedIn: "1.25", Replacement: "batch/v1",
		Where: []v1alpha1.APIWhere{v1alpha1.APIGit}}},
	Certificates: []v1alpha1.CertificateState{{Namespace: "demo-apps", Name: "billing-tls", Ready: true}},
}

// The maintenance's request per trigger and level (spec 12 §9, golden):
// the facts and the task, the repositories only when a pull request may be
// opened.
func TestMessageGolden(t *testing.T) {
	mt := func(trigger v1alpha1.MaintenanceTrigger, components ...string) *v1alpha1.TakeCareMaintenance {
		m := &v1alpha1.TakeCareMaintenance{Name: maintenanceName, Namespace: "takecare-system"}
		m.Spec.Trigger, m.Spec.Components = trigger, components
		return m
	}
	spots := []v1alpha1.NotSeen{{Ref: "datasets", Reason: "offline watch, current as of 2026-11-20"}}
	scope := v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{"demo-apps"}}
	for _, tc := range []struct {
		golden string
		req    Request
	}{
		{"message-schedule-apprentice.txt", Request{Maintenance: mt(v1alpha1.MaintenanceTriggerSchedule), Scope: scope, Level: v1alpha1.LevelApprentice,
			Inventory: demoInventory, ClusterVersion: "1.32.8", BlindSpots: spots, DatasetDate: "2026-11-20"}},
		{"message-ondemand-practitioner.txt", Request{Maintenance: mt(v1alpha1.MaintenanceTriggerOnDemand, "kubernetes"), Scope: scope, Level: v1alpha1.LevelPractitioner,
			Inventory: demoInventory, DatasetDate: "2026-11-20", Repositories: []string{"team/deploy"}, Copy: true}},
		{"message-practitioner-no-propose.txt", Request{Maintenance: mt(v1alpha1.MaintenanceTriggerSchedule), Scope: v1alpha1.Scope{}, Level: v1alpha1.LevelPractitioner,
			Inventory: v1alpha1.MaintenanceInventory{}, DatasetDate: "2026-11-20"}},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			out, err := Message(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out, "{{") || strings.Contains(out, "<no value>") {
				t.Errorf("a template action left in the message:\n%s", out)
			}
			golden.Assert(t, tc.golden, []byte(out))
		})
	}
}
