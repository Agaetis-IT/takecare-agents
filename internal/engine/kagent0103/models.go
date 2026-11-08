// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package kagent0103 implements the engine boundary for kagent 0.10.3
// (ADR 0007). It writes kagent objects as unstructured content with the
// field names validated by spike 001, without kagent's Go module (its API
// moves before 1.0 and its dependency tree is heavy), and asks the API
// server to refuse unknown fields: a structural CRD would otherwise drop a
// misspelled field without a word.
package kagent0103

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/internal/apply"
	"github.com/agaetis-it/takecare-agents/internal/engine"
)

// Kind is the engine's name in the records it runs (EngineRun.Kind).
const Kind = "kagent"

// The kagent API group and version of 0.10.3.
const (
	kagentGroup   = "kagent.dev"
	kagentVersion = "v1alpha2"
	// conditionAccepted is set by kagent once it translated an object.
	conditionAccepted = "Accepted"
)

var modelConfigGVK = schema.GroupVersionKind{Group: kagentGroup, Version: kagentVersion, Kind: "ModelConfig"}

// RBAC the engine's objects need, in the engine namespace only. The kagent
// group is named here and nowhere else outside this package (ADR 0007).
//
// +kubebuilder:rbac:groups=kagent.dev,resources=modelconfigs,verbs=get;list;watch;create;update;patch;delete,namespace=kagent

// Models provisions kagent ModelConfigs.
type Models struct {
	Client client.Client
}

var _ engine.Models = (*Models)(nil)

// OwnedTypes implements engine.Models.
func (*Models) OwnedTypes() []schema.GroupVersionKind {
	return []schema.GroupVersionKind{modelConfigGVK}
}

// EnsureModel writes one ModelConfig: OpenAI provider, the gateway as base
// URL, the dummy key Secret (ADR 0004: kagent never sees the provider key).
func (m *Models) EnsureModel(ctx context.Context, model engine.Model) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(modelConfigGVK)
	obj.SetName(model.Name)
	obj.SetNamespace(model.Namespace)
	obj.SetLabels(model.Labels)
	if model.Owner.UID != "" {
		obj.SetOwnerReferences([]metav1.OwnerReference{model.Owner})
	}
	obj.Object["spec"] = map[string]any{
		"provider":        "OpenAI",
		"model":           model.Model,
		"apiKeySecret":    model.DummyKeySecret,
		"apiKeySecretKey": model.DummyKeyField,
		"openAI": map[string]any{
			"baseUrl":   model.GatewayURL,
			"apiFormat": "chatCompletions",
		},
	}
	return apply.Strict(ctx, m.Client, obj)
}

// DeleteModel deletes a ModelConfig; one already gone is no error.
func (m *Models) DeleteModel(ctx context.Context, namespace, name string) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(modelConfigGVK)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	if err := m.Client.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete ModelConfig %s/%s: %w", namespace, name, err)
	}
	return nil
}

// ModelReadiness reads the ModelConfig's Accepted condition.
func (m *Models) ModelReadiness(ctx context.Context, namespace, name string) (engine.Readiness, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(modelConfigGVK)
	if err := m.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return engine.Readiness{Reason: "ModelConfigMissing", Message: fmt.Sprintf("ModelConfig %s/%s does not exist", namespace, name)}, nil
		}
		return engine.Readiness{}, fmt.Errorf("get ModelConfig %s/%s: %w", namespace, name, err)
	}
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok || cond["type"] != conditionAccepted {
			continue
		}
		reason, _ := cond["reason"].(string)
		message, _ := cond["message"].(string)
		return engine.Readiness{Ready: cond["status"] == "True", Reason: reason, Message: message}, nil
	}
	return engine.Readiness{Reason: "ModelConfigPending", Message: fmt.Sprintf("ModelConfig %s/%s has no Accepted condition yet", namespace, name)}, nil
}
