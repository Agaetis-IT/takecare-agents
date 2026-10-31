// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package demo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/internal/demoapp"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

// chartRules are the demo rules values-demo.yaml gives Prometheus.
func chartRules(t *testing.T) []Rule {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(testenv.RepoRoot(), "charts", "takecare", "values-demo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Prometheus struct {
			ServerFiles map[string]any `json:"serverFiles"`
		} `json:"prometheus"`
	}
	if err := sigsyaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	file, err := sigsyaml.Marshal(values.Prometheus.ServerFiles[rulesKey])
	if err != nil {
		t.Fatal(err)
	}
	rules, err := expectedRules(string(file))
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

// loadedByPrometheus is what Prometheus v3.15.0 (the chart's) returned from
// /api/v1/rules for those rules: expressions printed again in its layout.
func loadedByPrometheus() []Rule {
	return []Rule{
		{Name: "HighLatencyP99", Query: `http_request_duration_seconds{quantile="0.99"} > 2`, Duration: 5 * time.Minute},
		{Name: highErrorRate, Query: `sum by (service, namespace) (rate(http_requests_total{status="504"}[1m])) / sum by (service, namespace) (rate(http_requests_total[1m])) > 0.02`, Duration: 30 * time.Second},
		{Name: "PodNotReady", Query: `kube_pod_status_ready{condition="true",namespace="demo-apps"} == 0`, Duration: 5 * time.Minute},
		{Name: "PodCrashLooping", Query: `increase(kube_pod_container_status_restarts_total{namespace="demo-apps"}[15m]) > 3`, Duration: 5 * time.Minute},
	}
}

func TestRulesDrift(t *testing.T) {
	want := chartRules(t)
	if len(want) != 4 {
		t.Fatalf("%d demo rules in values-demo.yaml, want 4", len(want))
	}
	if drift := rulesDrift(want, loadedByPrometheus()); len(drift) != 0 {
		t.Errorf("the rules Prometheus loaded from the chart are reported as drift: %v", drift)
	}
	for _, tc := range []struct {
		name   string
		mutate func([]Rule) []Rule
		want   string
	}{
		{"a rule not loaded", func(l []Rule) []Rule { return l[1:] }, "rule HighLatencyP99 is not loaded"},
		{"another threshold", func(l []Rule) []Rule {
			l[1].Query = strings.Replace(l[1].Query, "0.02", "0.05", 1)
			return l
		}, "rule HighErrorRate loaded in Prometheus differs from the chart's expression"},
		{"another for", func(l []Rule) []Rule { l[1].Duration = time.Minute; return l }, "holds for 1m0s, the chart says 30s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drift := rulesDrift(want, tc.mutate(loadedByPrometheus()))
			if len(drift) != 1 || !strings.Contains(drift[0], tc.want) {
				t.Errorf("drift %v, want one naming %q", drift, tc.want)
			}
		})
	}
}

func TestPrometheusRules(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/rules" || r.URL.Query().Get("type") != "alert" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"groups":[{"name":"takecare-demo","rules":[{"name":"HighErrorRate","query":"up > 0.02","duration":30,"state":"inactive"}]}]}}`))
	}))
	defer srv.Close()
	got, err := PrometheusRules(srv.Client())(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]Rule{{Name: highErrorRate, Query: "up > 0.02", Duration: 30 * time.Second}}, got); diff != "" {
		t.Errorf("rules (-want +got):\n%s", diff)
	}
}

func TestAppDrift(t *testing.T) {
	want := appDeployment(testDemo(), "demo-app:test", false)
	if drift := appDrift(appDeployment(testDemo(), "demo-app:test", false), want); len(drift) != 0 {
		t.Errorf("no drift reported as %v", drift)
	}
	got := appDeployment(testDemo(), "demo-app:old", true)
	got.Spec.Template.Spec.Containers[0].Env[3].Value = "180" // LEAK_CEILING_MB
	drift := strings.Join(appDrift(got, want), "; ")
	for _, w := range []string{`mode "` + demoapp.ModeLeaky + `"`, "image demo-app:old", "LEAK_CEILING_MB", "MODE"} {
		if !strings.Contains(drift, w) {
			t.Errorf("drift %q does not name %s", drift, w)
		}
	}
}
