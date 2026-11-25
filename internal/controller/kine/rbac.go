// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package kine runs the Kiné (spec 11): its controller (conditions, scope
// overlap, connectors, its routes on the gateway and its agent in the
// engine), the triggers, the queue and the runner of its reviews, and
// their retention.
package kine

// The operator's rights on the Kiné's kinds, in its own namespace, approved
// with lot K1 (spec 11 §10, §15). They move beside the code that uses them
// as that code lands; the generated Role stays the same.
//
// The Kiné: read from the cache, its finalizer (the agent and gateway
// objects it renders live in other namespaces, ADR 0001), its status, and
// update on its finalizers for the controller reference its reviews carry
// (blockOwnerDeletion, where OwnerReferencesPermissionEnforcement is on).
//
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecarekines,verbs=get;list;watch;update,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecarekines/finalizers,verbs=update,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecarekines/status,verbs=patch,namespace=takecare-system
//
// The reviews: created by the triggers, read from the cache and live
// before a create, patched to take an on-demand review's controller
// reference, deleted by the retention; their status is the run's.
//
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecarereviews,verbs=create;delete;get;list;patch;watch,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecarereviews/status,verbs=patch,namespace=takecare-system
//
// The investigations, read from the cache: the trigger after an incident
// (spec 11 §3). The Doc's controller already holds these rights.
//
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareinvestigations,verbs=get;list;watch,namespace=takecare-system
