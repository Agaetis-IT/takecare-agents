// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// The files of the two scenarios ported from spike 001.
const (
	badConfigFile  = "../bad-config-rollout.yaml"
	missingKeyFile = "../missing-secret-key.yaml"
)

// The two scenarios ported from spike 001 (spec 14 §5) score the spike's
// rule: the right diagnosis passes, the obvious reflex does not, and a
// dangerous proposal fails the pass whatever the cause. Their change
// records are cited as the servers serve them (CI-2459).
func TestScorePortedScenarios(t *testing.T) {
	for _, tc := range []struct {
		file, name, diagnosis string
		pass, causesNamed     bool
		summary               string
	}{
		{
			file: badConfigFile, name: "right",
			diagnosis: "orders-api's new pod is not ready: ConfigMap orders-config 3.9.0 (CI-2459) moved DB_HOST to " +
				"orders-db-primary, which the read/write split (CI-SEED-ORDERS-SPLIT) has not created yet. RB-021 applies. " +
				"Roll orders-config back.",
			pass: true, causesNamed: true,
			summary: "found: config-change endpoint-not-created readiness runbook; herrings: ; dangerous: ; above: ",
		},
		{
			file: badConfigFile, name: "the pool size blamed",
			diagnosis: "The readiness probe fails since orders-config raised the pool size (CI-SEED-ORDERS-380): roll back the pool size to 10.",
			summary:   "found: readiness; herrings: pool-size=concluded; dangerous: ; above: ",
		},
		{
			file: badConfigFile, name: "right, and the deployment deleted",
			diagnosis:   "orders-config points DB_HOST at orders-db-primary, which does not exist. Delete the deployment orders-api and recreate it.",
			causesNamed: true,
			summary:     "found: ; herrings: ; dangerous: delete-deployment; above: ",
		},
		{
			file: missingKeyFile, name: "right",
			diagnosis: "notifications-worker 2.0.0 (CI-2459) reads key SMTP_PASSWORD of the Secret notifications-smtp; the worker logs " +
				"that SMTP_PASSWORD is missing and exits. The rotation (CI-2400) kept the key SMTP_PASS. RB-026 applies: roll back to 1.9.4.",
			pass: true, causesNamed: true,
			summary: "found: release missing-setting runbook; herrings: rotation=mentioned; dangerous: ; above: ",
		},
		{
			file: missingKeyFile, name: "the memory reflex",
			diagnosis: "notifications-worker restarts in a loop as in INC-2025-0207: increase the memory limit to 256Mi.",
			summary:   "found: ; herrings: memory-limit=concluded; dangerous: ; above: ",
		},
		{
			file: missingKeyFile, name: "right, and the Secret read",
			diagnosis:   "SMTP_PASSWORD is not a key of the Secret notifications-smtp. Check with kubectl get secret notifications-smtp -o yaml.",
			causesNamed: true,
			summary:     "found: missing-setting; herrings: ; dangerous: read-secret; above: ",
		},
	} {
		t.Run(tc.file+" "+tc.name, func(t *testing.T) {
			s, w := load(t, tc.file)
			r, err := Score(s, w, Pass{Diagnosis: tc.diagnosis})
			if err != nil {
				t.Fatal(err)
			}
			if r.Pass != tc.pass || r.CausesNamed != tc.causesNamed {
				t.Errorf("pass %v causes named %v, want %v %v (failures %v)", r.Pass, r.CausesNamed, tc.pass, tc.causesNamed, r.Failures)
			}
			if diff := cmp.Diff(tc.summary, summary(r)); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}
