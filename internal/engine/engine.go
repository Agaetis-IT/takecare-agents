// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Package engine is the boundary between TakeCare and its agent engine
// (ADR 0007). The interfaces are defined here, where the controllers consume
// them; the implementation for the pinned engine lives in a subpackage, and
// nothing outside that subpackage names an engine type.
package engine

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Model is what the engine needs to reach one model through the gateway
// (ADR 0004): never a provider key, only the gateway route and a Secret
// holding a dummy key the engine's runtime insists on.
type Model struct {
	// Name of the model entry in the platform; names the engine's object.
	Name string
	// Namespace of the engine, where the object is written.
	Namespace string
	// Model identifier the provider expects, passed through the gateway.
	Model string
	// GatewayURL is the OpenAI-compatible base URL of the gateway, with its
	// /v1 path.
	GatewayURL string
	// DummyKeySecret names the Secret, in Namespace, whose DummyKeyField holds
	// the dummy key.
	DummyKeySecret string
	DummyKeyField  string
	// Owner is set on the engine's object so that it follows the platform.
	Owner metav1.OwnerReference
	// Labels copied onto the engine's object.
	Labels map[string]string
}

// Readiness is the engine's verdict on one of its objects.
type Readiness struct {
	Ready   bool
	Reason  string
	Message string
}

// Models provisions the engine's model configuration. Implementations write
// their own objects and report on them; the platform controller never sees
// an engine type.
type Models interface {
	// EnsureModel creates or updates the engine's configuration for a model.
	EnsureModel(ctx context.Context, m Model) error
	// ModelReadiness reports whether the engine accepted a model.
	ModelReadiness(ctx context.Context, namespace, name string) (Readiness, error)
	// OwnedTypes lists the kinds EnsureModel writes, for the controller to watch.
	OwnedTypes() []schema.GroupVersionKind
}
