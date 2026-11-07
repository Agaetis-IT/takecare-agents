// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package alerts

import (
	"testing"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const (
	billingAPI2   = "billing-api"
	ordersDB      = "orders-db"
	notifWorker   = "notifications-worker"
	demoApps      = "demo-apps"
	notifPod      = "notifications-worker-5c7b9d8f4-q8zrt"
	alertNotReady = "PodNotReady"
)

func TestWorkloadFromLabelsOnly(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"deployment label wins", map[string]string{labelDeployment: billingAPI2, labelPod: "billing-api-7d9f8c6b5d-x2k4p"}, billingAPI2},
		{"statefulset label", map[string]string{labelStatefulSet: ordersDB}, ordersDB},
		{"job label", map[string]string{labelJob: "nightly-backup"}, "nightly-backup"},
		{"deployment pod name without its suffixes", map[string]string{labelPod: notifPod}, notifWorker},
		{"statefulset pod name without its ordinal", map[string]string{labelPod: "orders-db-2"}, ordersDB},
		{"service when there is no pod", map[string]string{labelService: billingAPI2}, billingAPI2},
		{"alert name as the last resort", map[string]string{labelAlertName: "HighErrorRate"}, "alert:HighErrorRate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Workload(tt.labels); got != tt.want {
				t.Errorf("Workload = %q, want %q", got, tt.want)
			}
		})
	}
	// The two alerts of missing-secret-key (spike 001) share their incident.
	crash := map[string]string{labelAlertName: "PodCrashLooping", labelNamespace: demoApps, labelPod: notifPod}
	notReady := map[string]string{labelAlertName: alertNotReady, labelNamespace: demoApps, labelPod: notifPod}
	if IncidentKey(v1alpha1.AlertRecord{Namespace: demoApps, Workload: Workload(crash)}) != IncidentKey(v1alpha1.AlertRecord{Namespace: demoApps, Workload: Workload(notReady)}) {
		t.Error("the two alerts of one pod must share an incident key")
	}
}

func TestInvestigationNamesDoNotCollideOnAPrefix(t *testing.T) {
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	a := InvestigationName("doc", "0a0a0a0a0a0a0a01", at)
	b := InvestigationName("doc", "0a0a0a0a0a0a0a02", at)
	if a == b {
		t.Errorf("two fingerprints sharing a prefix give the same name %s", a)
	}
	if InvestigationName("doc", "0a0a0a0a0a0a0a01", at) != a {
		t.Error("the name must be deterministic")
	}
	if InvestigationName("doc", "0a0a0a0a0a0a0a01", at.Add(time.Second)) == a {
		t.Error("a new startsAt is a new occurrence")
	}
}
