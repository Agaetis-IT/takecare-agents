// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package cachescope scopes the manager's cache per kind to the namespaces
// the operator's Roles cover (spec 02 §3): nothing namespaced is listed or
// watched cluster-wide, and Secrets are cached in the operator namespace
// only. The operator and the envtest managers share it, so that a test
// running under the operator's Roles watches what the operator watches.
package cachescope

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	takecarev1alpha1 "github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/engine/kagent0103"
	"github.com/agaetis-it/takecare-agents/internal/routes"
)

// Options is the cache of an operator working in operatorNS, with its
// engine in engineNS. gatewayKinds are the platform's third-party kinds in
// the operator namespace (platform.OperatorNamespaceKinds), passed in so
// that the platform's own tests can use this package.
func Options(operatorNS, engineNS string, gatewayKinds []schema.GroupVersionKind) cache.Options {
	operator := map[string]cache.Config{operatorNS: {}}
	engine := map[string]cache.Config{engineNS: {}}
	byObject := map[client.Object]cache.ByObject{
		&corev1.Secret{}:                          {Namespaces: operator},
		&corev1.ConfigMap{}:                       {Namespaces: engine},
		&corev1.Service{}:                         {Namespaces: operator},
		&networkingv1.NetworkPolicy{}:             {Namespaces: engine},
		&appsv1.Deployment{}:                      {Namespaces: operator},
		&discoveryv1.EndpointSlice{}:              {Namespaces: operator},
		&takecarev1alpha1.TakeCareConnector{}:     {Namespaces: operator},
		&takecarev1alpha1.TakeCareDoc{}:           {Namespaces: operator},
		&takecarev1alpha1.TakeCareInvestigation{}: {Namespaces: operator},
		&takecarev1alpha1.TakeCareDemo{}:          {Namespaces: operator},
		&takecarev1alpha1.TakeCareIntendant{}:     {Namespaces: operator},
		&takecarev1alpha1.TakeCareSurvey{}:        {Namespaces: operator},
		&takecarev1alpha1.TakeCareKine{}:          {Namespaces: operator},
		&takecarev1alpha1.TakeCareReview{}:        {Namespaces: operator},
		&takecarev1alpha1.TakeCareMecano{}:        {Namespaces: operator},
		&takecarev1alpha1.TakeCareMaintenance{}:   {Namespaces: operator},
	}
	// Third-party kinds, each once: two controllers may name the same.
	seen := map[schema.GroupVersionKind]bool{}
	scope := func(gvks []schema.GroupVersionKind, namespaces map[string]cache.Config) {
		for _, gvk := range gvks {
			if seen[gvk] {
				continue
			}
			seen[gvk] = true
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(gvk)
			byObject[u] = cache.ByObject{Namespaces: namespaces}
		}
	}
	scope(gatewayKinds, operator)
	scope(routes.Kinds(), operator)
	scope((&kagent0103.Models{}).OwnedTypes(), engine)
	scope((&kagent0103.Agents{}).AgentTypes(), engine)
	return cache.Options{ByObject: byObject}
}
