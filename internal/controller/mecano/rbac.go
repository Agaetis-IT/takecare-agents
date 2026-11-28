// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package mecano runs the Mécano (spec 12): its controller (conditions,
// scope overlap, connectors, its routes on the gateway with the propose
// route's argument rule, and its agent in the engine), the triggers, the
// queue and the runner of its maintenances, and their retention.
package mecano

import "time"

// The operator's rights on the Mécano's kinds, in its own namespace,
// approved with lot M1a (spec 12 §13). They move beside the code that uses
// them as that code lands; the generated Role stays the same.
//
// The Mécano: read from the cache, its finalizer (the agent and gateway
// objects it renders live in other namespaces, ADR 0001), its status, and
// update on its finalizers for the controller reference its maintenances
// carry (blockOwnerDeletion, where OwnerReferencesPermissionEnforcement is
// on).
//
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaremecanos,verbs=get;list;watch;update,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaremecanos/finalizers,verbs=update,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaremecanos/status,verbs=patch,namespace=takecare-system
//
// The maintenances: created by the triggers, read from the cache and live
// before a create, patched to take an on-demand maintenance's controller
// reference, deleted by the retention; their status is the run's.
//
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaremaintenances,verbs=create;delete;get;list;patch;watch,namespace=takecare-system
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecaremaintenances/status,verbs=patch,namespace=takecare-system

// DefaultMaintenanceRetention is how long an ended maintenance is kept
// (spec 12 §14): 365 days. Each Mécano keeps its newest ended maintenance
// and its newest published one however old they are.
const DefaultMaintenanceRetention = 365 * 24 * time.Hour
