// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package chart

import (
	"slices"
	"strings"
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

// The prod profile takes kagent's database URL as a file (urlFile) and
// refuses an inline url, alone or beside urlFile, with the steps to follow
// and never the value: kagent 0.10.3 logs its configuration at each start.
// The demo's bundled PostgreSQL sets no url and renders.
func TestKagentDatabaseURLFile(t *testing.T) {
	const inline = "postgres://takecare:not-a-real-password@db.example.invalid:5432/kagent" //nolint:gosec // a refused placeholder, never rendered
	// prodSets pairs each value with its flag: keep every pair but the database.
	args := profileArgs("prod")
	for i := 0; i+1 < len(prodSets); i += 2 {
		if prodSets[i+1] != dbSet {
			args = append(args, prodSets[i], prodSets[i+1])
		}
	}
	t.Run("urlFile alone renders", func(t *testing.T) {
		objs := render(t, releaseName, append(slices.Clone(args), flagSet, dbSet)...)
		var env []string
		for _, d := range ofKind(objs, "Deployment") {
			if d.GetName() != "kagent-controller" {
				continue
			}
			cs, _, _ := unstructured.NestedSlice(d.Object, "spec", "template", "spec", "containers")
			for _, c := range cs {
				es, _, _ := unstructured.NestedSlice(c.(map[string]any), "env")
				for _, e := range es {
					m := e.(map[string]any)
					if n, _ := m["name"].(string); strings.HasPrefix(n, "POSTGRES_DATABASE_URL") {
						v, _ := m["value"].(string)
						env = append(env, n+"="+v)
					}
				}
			}
		}
		if !slices.Equal(env, []string{"POSTGRES_DATABASE_URL_FILE=/etc/kagent-db/url"}) {
			t.Errorf("kagent-controller's database env: %v, want the file only", env)
		}
	})
	for _, tc := range []struct {
		name string
		sets []string
		want []string
	}{
		{"an inline url is refused, with the steps", []string{flagSet, "kagent.database.postgres.url=" + inline},
			[]string{"kagent.database.postgres.url is refused", "1. create a Secret in the namespace kagent with the key url", "2. mount it under kagent.controller.volumes and kagent.controller.volumeMounts", "3. set kagent.database.postgres.urlFile="}},
		{"url beside urlFile is refused", []string{flagSet, dbSet, flagSet, "kagent.database.postgres.url=" + inline},
			[]string{"kagent.database.postgres.url must be empty when kagent.database.postgres.urlFile is set"}},
		{"neither is refused, as before", nil, []string{"database.postgres.urlFile"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := helmTemplate(t, releaseName, releaseNamespace, append(slices.Clone(args), tc.sets...)...).CombinedOutput()
			if err == nil {
				t.Fatalf("rendered: %s", out)
			}
			for _, w := range tc.want {
				if !strings.Contains(string(out), w) {
					t.Errorf("the error does not say %q:\n%s", w, out)
				}
			}
			for _, part := range []string{inline, "not-a-real-password", "db.example.invalid"} {
				if strings.Contains(string(out), part) {
					t.Errorf("the error prints the URL or part of it (%q)", part)
				}
			}
		})
	}
	t.Run("the demo's bundled PostgreSQL renders", func(t *testing.T) {
		render(t, releaseName, profileArgs("demo")...)
	})
}
