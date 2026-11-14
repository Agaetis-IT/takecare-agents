// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package chart

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// kagent keeps sessions (events, tasks) in its database, deleted after an
// idle period only when its retention is set: both profiles set 30 days,
// and a value the installer gives is followed.
func TestKagentSessionRetention(t *testing.T) {
	retention := func(t *testing.T, args ...string) (string, int) {
		var got []string
		for _, cm := range ofKind(render(t, releaseName, args...), "ConfigMap") {
			if v, found, _ := unstructured.NestedString(cm.Object, "data", "SESSION_RETENTION_DAYS"); found {
				got = append(got, v)
			}
		}
		if len(got) != 1 {
			return "", len(got)
		}
		return got[0], 1
	}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"prod", append(profileArgs("prod"), prodSets...), "30"},
		{"demo", profileArgs("demo"), "30"},
		{"prod with a value given", append(append(profileArgs("prod"), prodSets...), flagSet, "kagent.database.postgres.sessionRetentionDays=7"), "7"},
		{"demo with retention off", append(profileArgs("demo"), flagSet, "kagent.database.postgres.sessionRetentionDays=0"), "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, n := retention(t, tt.args...)
			if n != 1 || got != tt.want {
				t.Errorf("SESSION_RETENTION_DAYS: %q in %d ConfigMaps, want %q in one", got, n, tt.want)
			}
		})
	}
}
