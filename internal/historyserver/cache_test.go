// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package historyserver

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

// The server reads the records with the add-on's own identity, bound to
// the RBAC the chart renders (templates/addons/history.yaml): its cache
// lists and watches both kinds in the release namespace, a record created
// after the sync is served, and the same identity is refused anything
// else. envtest as admin would hide a missing right (0.3.0).
func TestServesUnderTheChartsRole(t *testing.T) {
	env := testenv.StartAddon(t, "history")
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := client.New(env.Admin, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := admin.Create(ctx, &corev1.Namespace{Name: otherNS}); err != nil {
		t.Fatal(err)
	}
	create := func(inv *v1alpha1.TakeCareInvestigation) {
		t.Helper()
		status := inv.Status
		inv.Labels[LabelIncident] = "inc"
		if err := admin.Create(ctx, inv); err != nil {
			t.Fatal(err)
		}
		inv.Status = status
		if err := admin.Status().Update(ctx, inv); err != nil {
			t.Fatal(err)
		}
	}
	now := int(time.Since(day0).Hours()/24) + 1
	create(investigation("inv-before", 0))
	create(investigation("inv-other-ns", 0, func(i *v1alpha1.TakeCareInvestigation) { i.Namespace = otherNS }))
	// The API server sets its creation time: now.
	if err := admin.Create(ctx, review("review-a", 0)); err != nil {
		t.Fatal(err)
	}

	records, err := NewCache(ctx, env.Addon, releaseNS)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := records.Start(ctx); err != nil {
			t.Errorf("cache stopped: %v", err)
		}
	}()
	syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Second)
	defer syncCancel()
	if !records.WaitForCacheSync(syncCtx) {
		t.Fatal("the cache did not sync under the add-on's Role")
	}
	s, err := New(Config{Reader: records, Namespace: releaseNS, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	session, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, &sdk.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	// Created after the sync: the watch brings it.
	create(investigation("inv-after", 1))
	deadline := time.Now().Add(20 * time.Second)
	var got []string
	for time.Now().Before(deadline) {
		text, isErr := call(t, session, ToolInvestigationsList, span(-1, now))
		if isErr {
			t.Fatal(text)
		}
		if got, _ = names(t, text, "investigations"); len(got) == 2 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if strings.Join(got, ",") != "inv-after,inv-before" {
		t.Errorf("investigations = %v, want the release namespace's two, the watched one first", got)
	}
	if text, isErr := call(t, session, ToolInvestigationGet, map[string]any{argName: "inv-before"}); isErr || !strings.Contains(text, diagnosisText) {
		t.Errorf("investigation_get: %v %s", isErr, text)
	}
	if text, isErr := call(t, session, ToolReviewsList, span(-30, now+1)); isErr || !strings.Contains(text, `"review-a"`) {
		t.Errorf("reviews_list: %v %s", isErr, text)
	}

	// The same identity reads nothing else.
	addon, err := client.New(env.Addon, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	for name, list := range map[string]func() error{
		"ConfigMaps of the release namespace": func() error {
			return addon.List(ctx, &corev1.ConfigMapList{}, client.InNamespace(releaseNS))
		},
		"Secrets of the release namespace": func() error {
			return addon.List(ctx, &corev1.SecretList{}, client.InNamespace(releaseNS))
		},
		"investigations of another namespace": func() error {
			return addon.List(ctx, &v1alpha1.TakeCareInvestigationList{}, client.InNamespace(otherNS))
		},
		"investigations cluster-wide": func() error {
			return addon.List(ctx, &v1alpha1.TakeCareInvestigationList{})
		},
		"the Kinés": func() error {
			return addon.List(ctx, &v1alpha1.TakeCareKineList{}, client.InNamespace(releaseNS))
		},
	} {
		if err := list(); !apierrors.IsForbidden(err) {
			t.Errorf("listing %s as the add-on: %v, want forbidden", name, err)
		}
	}
	if err := addon.Delete(ctx, investigation("inv-before", 0)); !apierrors.IsForbidden(err) {
		t.Errorf("deleting an investigation as the add-on: %v, want forbidden", err)
	}
}
