// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// Drift is the lesson of the POC: most demo failures were outdated objects
// (an old app, rules edited and never loaded), not missing ones. The
// controller tells what drifted from what it would write. What it writes
// itself (the app, the DBProxy, the corpus) it writes again, and reports
// as an event; what the chart owns (the alerting rules Prometheus loaded)
// it can only report, in SceneryReady.

// RulesConfigMap holds the demo's alerting rules as the chart renders them
// for Prometheus, so that the operator compares what Prometheus loaded with
// what the release says.
const (
	RulesConfigMap = "takecare-demo-rules"
	rulesKey       = "alerting_rules.yml"
)

// sceneryDrift compares the app and the DBProxy with what the phase wants,
// before they are applied again; nothing right after an action, whose
// differences are expected.
func (r *Reconciler) sceneryDrift(ctx context.Context, d *v1alpha1.TakeCareDemo, incident, action bool) ([]string, error) {
	if action {
		return nil, nil
	}
	var drift []string
	var dep appsv1.Deployment
	err := r.Reader.Get(ctx, client.ObjectKey{Namespace: string(d.Spec.AppNamespace), Name: appName}, &dep)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return nil, fmt.Errorf("read the app: %w", err)
	default:
		drift = append(drift, appDrift(&dep, appDeployment(d, r.AppImage, incident))...)
	}
	proxy := &unstructured.Unstructured{}
	proxy.SetGroupVersionKind(DBProxyGVK)
	err = r.Reader.Get(ctx, client.ObjectKey{Namespace: string(d.Spec.PlatformNamespace), Name: proxyName}, proxy)
	switch {
	case apierrors.IsNotFound(err) || meta.IsNoMatchError(err):
	case err != nil:
		return nil, fmt.Errorf("read the DBProxy: %w", err)
	default:
		got, set, _ := unstructured.NestedInt64(proxy.Object, "spec", "idleClientTimeout")
		switch {
		case incident && (!set || got != proxyIdleTimeout):
			drift = append(drift, fmt.Sprintf("DBProxy %s idleClientTimeout is %s, the incident wants %d", proxyName, timeoutString(got, set), proxyIdleTimeout))
		case !incident && set:
			drift = append(drift, fmt.Sprintf("DBProxy %s idleClientTimeout is %d, healthy wants AWS's default (field absent)", proxyName, got))
		}
	}
	return drift, nil
}

func timeoutString(v int64, set bool) string {
	if !set {
		return "absent"
	}
	return fmt.Sprint(v)
}

// appDrift names what differs in the app's pod template: its mode, its
// image, its environment.
func appDrift(got, want *appsv1.Deployment) []string {
	var drift []string
	if g, w := got.Spec.Template.Annotations[annotationMode], want.Spec.Template.Annotations[annotationMode]; g != w {
		drift = append(drift, fmt.Sprintf("%s runs in mode %q, the phase wants %q", appName, g, w))
	}
	gc, wc := container(got), container(want)
	if gc == nil {
		return append(drift, fmt.Sprintf("%s has no app container", appName))
	}
	if gc.Image != wc.Image {
		drift = append(drift, fmt.Sprintf("%s runs image %s, the operator's is %s", appName, gc.Image, wc.Image))
	}
	for _, e := range wc.Env {
		if i := slices.IndexFunc(gc.Env, func(g corev1.EnvVar) bool { return g.Name == e.Name }); i < 0 || gc.Env[i].Value != e.Value {
			drift = append(drift, fmt.Sprintf("%s's %s differs from %q", appName, e.Name, e.Value))
		}
	}
	return drift
}

func container(dep *appsv1.Deployment) *corev1.Container {
	for i, c := range dep.Spec.Template.Spec.Containers {
		if c.Name == "app" {
			return &dep.Spec.Template.Spec.Containers[i]
		}
	}
	return nil
}

// Rule is an alerting rule: its name, its expression, and how long it must
// hold before it fires.
type Rule struct {
	Name     string
	Query    string
	Duration time.Duration
}

// RulesFunc returns the alerting rules a Prometheus has loaded.
type RulesFunc func(ctx context.Context, prometheusURL string) ([]Rule, error)

// PrometheusRules reads /api/v1/rules?type=alert.
func PrometheusRules(hc *http.Client) RulesFunc {
	return func(ctx context.Context, prometheusURL string) ([]Rule, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(prometheusURL, "/")+"/api/v1/rules?type=alert", nil)
		if err != nil {
			return nil, fmt.Errorf("build the rules request: %w", err)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("read the loaded rules: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("read the loaded rules: HTTP %d", resp.StatusCode)
		}
		var body struct {
			Data struct {
				Groups []struct {
					Rules []struct {
						Name     string  `json:"name"`
						Query    string  `json:"query"`
						Duration float64 `json:"duration"`
					} `json:"rules"`
				} `json:"groups"`
			} `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
			return nil, fmt.Errorf("decode the loaded rules: %w", err)
		}
		var out []Rule
		for _, g := range body.Data.Groups {
			for _, r := range g.Rules {
				out = append(out, Rule{Name: r.Name, Query: r.Query, Duration: time.Duration(r.Duration * float64(time.Second))})
			}
		}
		return out, nil
	}
}

// expectedRules parses the chart's demo rules (Prometheus rule file format).
func expectedRules(raw string) ([]Rule, error) {
	var file struct {
		Groups []struct {
			Rules []struct {
				Alert string `json:"alert"`
				Expr  string `json:"expr"`
				For   string `json:"for"`
			} `json:"rules"`
		} `json:"groups"`
	}
	if err := sigsyaml.Unmarshal([]byte(raw), &file); err != nil {
		return nil, fmt.Errorf("parse the demo rules: %w", err)
	}
	var out []Rule
	for _, g := range file.Groups {
		for _, r := range g.Rules {
			d, err := time.ParseDuration(r.For)
			if r.For != "" && err != nil {
				return nil, fmt.Errorf("rule %s: for %q: %w", r.Alert, r.For, err)
			}
			out = append(out, Rule{Name: r.Alert, Query: r.Expr, Duration: d})
		}
	}
	return out, nil
}

// compact drops whitespace: Prometheus prints a loaded expression again in
// its own layout ({a="1", b="2"} comes back {a="1",b="2"}).
func compact(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// rulesDrift names the demo rules Prometheus has not loaded as the chart
// renders them: missing, another expression, another `for`.
func rulesDrift(want, loaded []Rule) []string {
	var drift []string
	for _, w := range want {
		i := slices.IndexFunc(loaded, func(l Rule) bool { return l.Name == w.Name })
		switch {
		case i < 0:
			drift = append(drift, fmt.Sprintf("rule %s is not loaded in Prometheus", w.Name))
		case compact(loaded[i].Query) != compact(w.Query):
			drift = append(drift, fmt.Sprintf("rule %s loaded in Prometheus differs from the chart's expression", w.Name))
		case loaded[i].Duration != w.Duration:
			drift = append(drift, fmt.Sprintf("rule %s loaded in Prometheus holds for %s, the chart says %s", w.Name, loaded[i].Duration, w.Duration))
		}
	}
	return drift
}

// checkRules compares the rules Prometheus loaded with the chart's. It
// reports what it cannot check apart: an unreachable Prometheus is not a
// drift.
func (r *Reconciler) checkRules(ctx context.Context, d *v1alpha1.TakeCareDemo) (drift []string, unchecked string, err error) {
	if r.Rules == nil {
		return nil, "the rules are not checked (no Prometheus client)", nil
	}
	var platforms v1alpha1.TakeCarePlatformList
	if err := r.Client.List(ctx, &platforms); err != nil {
		return nil, "", fmt.Errorf("list the platforms: %w", err)
	}
	url := ""
	for _, p := range platforms.Items {
		if m := p.Spec.Observability.Metrics; m != nil && m.PrometheusURL != "" {
			url = m.PrometheusURL
		}
	}
	if url == "" {
		return nil, "the rules are not checked: the platform names no Prometheus", nil
	}
	var cm corev1.ConfigMap
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: RulesConfigMap}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return []string{fmt.Sprintf("ConfigMap %s, the chart's demo rules, is missing", RulesConfigMap)}, "", nil
		}
		return nil, "", fmt.Errorf("read the demo rules: %w", err)
	}
	want, err := expectedRules(cm.Data[rulesKey])
	if err != nil {
		return []string{err.Error()}, "", nil
	}
	loaded, err := r.Rules(ctx, url)
	if err != nil {
		return nil, fmt.Sprintf("the rules are not checked: %v", err), nil
	}
	return rulesDrift(want, loaded), "", nil
}
