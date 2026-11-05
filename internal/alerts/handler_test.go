// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

const (
	ns         = "takecare-system"
	token      = "intake-test-token"
	appNS      = "demo-apps"
	billingAPI = "billing-api"
	sevLabel   = "severity"
)

type fixture struct {
	t       *testing.T
	c       client.Client
	srv     *httptest.Server
	metrics *Metrics
	now     time.Time
}

func start(t *testing.T) *fixture {
	t.Helper()
	cfg := testenv.Start(t)
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, obj := range []client.Object{
		&corev1.Namespace{Name: ns},
		&corev1.Secret{Name: TokenSecretName, Namespace: ns, StringData: map[string]string{TokenSecretKey: token}},
		&v1alpha1.TakeCareDoc{
			Name: "doc", Namespace: ns,
			Spec: v1alpha1.TakeCareDocSpec{
				RoleCommonSpec: v1alpha1.RoleCommonSpec{Level: v1alpha1.LevelPractitioner, Scope: v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{appNS}}},
				Alerts:         v1alpha1.AlertsSpec{Matchers: []v1alpha1.AlertMatcher{{Name: sevLabel, Value: "critical"}}},
			},
		},
	} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	metrics, err := NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, c: c, metrics: metrics, now: time.Now()}
	f.srv = httptest.NewServer(&Handler{Client: c, Namespace: ns, Metrics: metrics, Now: func() time.Time { return f.now }})
	t.Cleanup(f.srv.Close)
	return f
}

func alert(name, fp, pod string, startsAt time.Time, extra map[string]string) Alert {
	labels := map[string]string{labelAlertName: name, labelNamespace: appNS, sevLabel: "critical", labelPod: pod, labelService: "notifications-worker"}
	maps.Copy(labels, extra)
	return Alert{Status: "firing", Labels: labels, Annotations: map[string]string{"description": "x"}, StartsAt: startsAt, Fingerprint: fp}
}

func (f *fixture) post(auth string, alerts ...Alert) int {
	f.t.Helper()
	body, _ := json.Marshal(Payload{Version: "4", Status: "firing", Alerts: alerts})
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL, bytes.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // test server
	if err != nil {
		f.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func (f *fixture) investigations() []v1alpha1.TakeCareInvestigation {
	var list v1alpha1.TakeCareInvestigationList
	if err := f.c.List(context.Background(), &list, client.InNamespace(ns)); err != nil {
		f.t.Fatal(err)
	}
	return list.Items
}

func (f *fixture) ignored(reason string) float64 {
	return testutil.ToFloat64(f.metrics.Ignored.WithLabelValues(reason))
}

func TestIntake(t *testing.T) {
	f := start(t)
	t0 := time.Now().Add(-time.Minute).Truncate(time.Second)
	crash := alert("PodCrashLooping", "3fa29c1b07e4d2aa", "notifications-worker-5c7b9d8f4-q8zrt", t0, nil)

	steps := []struct {
		name string
		run  func(*testing.T, *fixture, time.Time, Alert)
	}{
		{"a missing or wrong token gets 401 and creates nothing", intakeStep0},
		{"a malformed payload gets 400", intakeStep1},
		{"a firing alert in scope creates one investigation, 202", intakeStep2},
		{"the same alert sent again is one investigation", intakeStep3},
		{"the same fingerprint with a new startsAt within the cooldown is skipped and counted", intakeStep4},
		{"another alert of the same incident is folded into the first investigation", intakeStep5},
		{"another workload is another incident", intakeStep6},
		{"out of scope, not matched, not firing: nothing, counted", intakeStep7},
		{"a silent alert is recorded as silent", intakeStep8},
		{"a resolved alert stamps resolvedAt", intakeStep9},
		{"after the cooldown, the same incident opens a new investigation", intakeStep10},
		{"a suspended Doc takes nothing", intakeStep11},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) { s.run(t, f, t0, crash) })
	}
}

// intakeStep0: a missing or wrong token gets 401 and creates nothing.
func intakeStep0(t *testing.T, f *fixture, t0 time.Time, crash Alert) {
	t.Helper()
	if code := f.post("", crash); code != http.StatusUnauthorized {
		t.Errorf("no token: %d", code)
	}
	if code := f.post("wrong", crash); code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", code)
	}
	if n := len(f.investigations()); n != 0 {
		t.Errorf("%d investigations created", n)
	}
}

// intakeStep1: a malformed payload gets 400.
func intakeStep1(t *testing.T, f *fixture, t0 time.Time, crash Alert) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL, strings.NewReader("{not json"))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // test server
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d", resp.StatusCode)
	}
}

// intakeStep2: a firing alert in scope creates one investigation, 202.
func intakeStep2(t *testing.T, f *fixture, t0 time.Time, crash Alert) {
	t.Helper()
	if code := f.post(token, crash); code != http.StatusAccepted {
		t.Fatalf("status %d", code)
	}
	items := f.investigations()
	if len(items) != 1 {
		t.Fatalf("%d investigations", len(items))
	}
	inv := items[0]
	if inv.Name != InvestigationName("doc", crash.Fingerprint, t0) || inv.Spec.Alert.Workload != "notifications-worker" || inv.Spec.Level != v1alpha1.LevelPractitioner {
		t.Errorf("investigation = %s, %+v", inv.Name, inv.Spec.Alert)
	}
	if inv.Labels[LabelIncident] != IncidentKey(appNS, "notifications-worker") || inv.Labels[v1alpha1.LabelFingerprint] != "3fa29c1b07e4d2aa" {
		t.Errorf("labels = %v", inv.Labels)
	}
}

// intakeStep3: the same alert sent again is one investigation.
func intakeStep3(t *testing.T, f *fixture, t0 time.Time, crash Alert) {
	t.Helper()
	f.post(token, crash)
	if n := len(f.investigations()); n != 1 || f.ignored(ReasonDuplicate) < 1 {
		t.Errorf("%d investigations, duplicates %v", n, f.ignored(ReasonDuplicate))
	}
}

// intakeStep4: the same fingerprint with a new startsAt within the cooldown is skipped and counted.
func intakeStep4(t *testing.T, f *fixture, t0 time.Time, crash Alert) {
	t.Helper()
	again := crash
	again.StartsAt = t0.Add(30 * time.Second)
	f.post(token, again)
	if n := len(f.investigations()); n != 1 || f.ignored(ReasonCooldown) != 1 {
		t.Errorf("%d investigations, cooldown %v", n, f.ignored(ReasonCooldown))
	}
}

// intakeStep5: another alert of the same incident is folded into the first investigation.
func intakeStep5(t *testing.T, f *fixture, t0 time.Time, crash Alert) {
	t.Helper()
	notReady := alert(alertNotReady, "aa11bb22cc33dd44", "notifications-worker-5c7b9d8f4-q8zrt", t0.Add(time.Minute), nil)
	f.post(token, notReady)
	items := f.investigations()
	if len(items) != 1 {
		t.Fatalf("%d investigations for one incident", len(items))
	}
	if a := items[0].Status.Alerts; len(a) != 1 || a[0].Name != alertNotReady || a[0].Fingerprint != "aa11bb22cc33dd44" {
		t.Errorf("status.alerts = %+v", a)
	}
	f.post(token, notReady)
	if a := f.investigations()[0].Status.Alerts; len(a) != 1 {
		t.Errorf("a folded alert sent again is folded once: %+v", a)
	}
}

// intakeStep6: another workload is another incident.
func intakeStep6(t *testing.T, f *fixture, t0 time.Time, crash Alert) {
	t.Helper()
	other := alert("HighErrorRate", "77ee88ff99aa00bb", "billing-api-6b5c4d3e2f-abcde", t0, map[string]string{labelService: billingAPI})
	f.post(token, other)
	if n := len(f.investigations()); n != 2 {
		t.Errorf("%d investigations, want 2", n)
	}
}

// intakeStep7: out of scope, not matched, not firing: nothing, counted.
func intakeStep7(t *testing.T, f *fixture, t0 time.Time, crash Alert) {
	t.Helper()
	before := len(f.investigations())
	outside := alert("HighErrorRate", "0102030405060708", "x-1", t0, map[string]string{labelNamespace: "kube-system"})
	warning := alert("HighLatencyP99", "1112131415161718", "y-1", t0, map[string]string{sevLabel: "warning"})
	f.post(token, outside, warning)
	if n := len(f.investigations()); n != before {
		t.Errorf("%d investigations created", n-before)
	}
	if f.ignored(ReasonOutOfScope) != 1 || f.ignored(ReasonNotMatched) != 1 {
		t.Errorf("out of scope %v, not matched %v", f.ignored(ReasonOutOfScope), f.ignored(ReasonNotMatched))
	}
}

// intakeStep8: a silent alert is recorded as silent.
func intakeStep8(t *testing.T, f *fixture, t0 time.Time, crash Alert) {
	t.Helper()
	silent := alert("HighErrorRate", "2122232425262728", "bench-1", t0, map[string]string{"takecare_silent": "true", labelDeployment: "bench-run-1"})
	f.post(token, silent)
	var inv v1alpha1.TakeCareInvestigation
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: InvestigationName("doc", silent.Fingerprint, t0)}, &inv); err != nil {
		t.Fatal(err)
	}
	if !inv.Spec.Silent {
		t.Error("takecare_silent=true must make the investigation silent")
	}
}

// intakeStep9: a resolved alert stamps resolvedAt.
func intakeStep9(t *testing.T, f *fixture, t0 time.Time, crash Alert) {
	t.Helper()
	resolved := crash
	resolved.Status = "resolved"
	f.post(token, resolved)
	var inv v1alpha1.TakeCareInvestigation
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: InvestigationName("doc", crash.Fingerprint, t0)}, &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Status.ResolvedAt == nil {
		t.Error("resolvedAt not set")
	}
}

// intakeStep10: after the cooldown, the same incident opens a new investigation.
func intakeStep10(t *testing.T, f *fixture, t0 time.Time, crash Alert) {
	t.Helper()
	f.now = time.Now().Add(16 * time.Minute)
	later := alert("PodCrashLooping", "3fa29c1b07e4d2aa", "notifications-worker-5c7b9d8f4-q8zrt", t0.Add(16*time.Minute), nil)
	before := len(f.investigations())
	f.post(token, later)
	if n := len(f.investigations()); n != before+1 {
		t.Errorf("%d new investigations, want 1", n-before)
	}
}

// intakeStep11: a suspended Doc takes nothing.
func intakeStep11(t *testing.T, f *fixture, t0 time.Time, crash Alert) {
	t.Helper()
	var doc v1alpha1.TakeCareDoc
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "doc"}, &doc); err != nil {
		t.Fatal(err)
	}
	doc.Spec.Suspend = true
	if err := f.c.Update(context.Background(), &doc); err != nil {
		t.Fatal(err)
	}
	before := len(f.investigations())
	f.post(token, alert("HighErrorRate", fmt.Sprintf("%016x", 99), "z-1", t0, map[string]string{labelDeployment: "elsewhere"}))
	if n := len(f.investigations()); n != before || f.ignored(ReasonSuspended) != 1 {
		t.Errorf("suspended Doc: %d new, counted %v", n-before, f.ignored(ReasonSuspended))
	}
}

// Two alerts of one incident in the same webhook (Alertmanager groups them)
// give one investigation: the second must not miss the record the first
// just created.
func TestOneWebhookOneIncident(t *testing.T) {
	f := start(t)
	t0 := time.Now().Add(-time.Minute).Truncate(time.Second)
	pod := "notifications-worker-5c7b9d8f4-q8zrt"
	if code := f.post(token, alert("PodCrashLooping", "0a0a0a0a0a0a0a01", pod, t0, nil), alert(alertNotReady, "0a0a0a0a0a0a0a02", pod, t0, nil)); code != http.StatusAccepted {
		t.Fatalf("status %d", code)
	}
	items := f.investigations()
	if len(items) != 1 {
		t.Fatalf("%d investigations for one incident in one webhook", len(items))
	}
	if a := items[0].Status.Alerts; len(a) != 1 || a[0].Name != alertNotReady {
		t.Errorf("status.alerts = %+v", a)
	}
}

// The token Secret can be one the customer created (alerting.existingSecret).
func TestTokenFromAnotherSecret(t *testing.T) {
	f := start(t)
	if err := f.c.Create(context.Background(), &corev1.Secret{Name: "gitops-token", Namespace: ns, StringData: map[string]string{TokenSecretKey: "from-gitops"}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&Handler{Client: f.c, Namespace: ns, Metrics: f.metrics, TokenSecret: "gitops-token"})
	defer srv.Close()
	for tok, want := range map[string]int{"from-gitops": http.StatusAccepted, token: http.StatusUnauthorized} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"version":"4","alerts":[]}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req) //nolint:gosec // test server
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("token %q: %d, want %d", tok, resp.StatusCode, want)
		}
	}
}

func TestBodyLimit(t *testing.T) {
	f := start(t)
	big := bytes.Repeat([]byte("a"), MaxBody+10)
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL, bytes.NewReader(big))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // test server
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a body over 1 MiB: %d", resp.StatusCode)
	}
}
