// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Package platform reconciles the TakeCarePlatform (spec 01): one gateway
// model per declared model, the engine's configuration for each through the
// engine boundary, the gateway's tracing policy, and the platform's status.
// It writes nothing but generated objects and the platform's status
// subresource, so that a chart upgrade under server-side apply never
// conflicts with it.
package platform

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/recorder"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/apply"
	"github.com/agaetis-it/takecare-agents/internal/engine"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
)

// Names fixed by the charts: the platform singleton (ADR 0003) and the
// engine's dummy key Secret (ADR 0004), rendered by the chart in the engine
// namespace and only referenced here.
const (
	PlatformName       = "main"
	DummyKeySecretName = "takecare-engine-dummy-key"
	DummyKeyField      = "apiKey"

	tracingObjectName = "takecare-tracing"
	gatewayNameLabel  = "gateway.networking.k8s.io/gateway-name"
	partOfLabel       = "app.kubernetes.io/part-of"
	partOfValue       = "takecare"
	requeueNotReady   = 30 * time.Second
)

// Production gaps (spec 01): what is missing for a real production, reported
// and never blocking. Two more join the list with the roles and the
// connectors: role-without-report (PR 4) and connector-not-ready (PR 3b).
const (
	GapNoTelemetryExport        = "no-telemetry-export"
	GapContentCaptureOn         = "content-capture-on"
	GapSingleReplica            = "single-replica"
	GapGatewayMetricsNotScraped = "gateway-metrics-not-scraped"
	GapNoNetworkPolicies        = "no-network-policies"
	GapConnectorNotReady        = "connector-not-ready"

	ReasonNoGaps    = "NoGaps"
	ReasonGapsFound = "GapsFound"
)

// Condition reasons.
const (
	ReasonReady                           = "Ready"
	ReasonNotReady                        = "NotReady"
	ReasonGatewayMissing                  = "GatewayMissing"
	ReasonGatewayNotProgrammed            = "GatewayNotProgrammed"
	ReasonGatewayAddressUnknown           = "GatewayAddressUnknown"
	ReasonGatewayOutsideOperatorNamespace = "GatewayOutsideOperatorNamespace"
	ReasonEngineNamespaceForbidden        = "EngineNamespaceForbidden"
	ReasonEngineSecretMissing             = "EngineSecretMissing"
	ReasonSecretMissing                   = "SecretMissing"
	ReasonModelsPending                   = "ModelsPending"
	ReasonTracingConfigured               = "TracingConfigured"
	ReasonTracingExportDisabled           = "TracingExportDisabled"
	ReasonTracingInvalidEndpoint          = "TracingInvalidEndpoint"
)

// Kinds the controller reads and writes as unstructured content: Gateway API
// and agentgateway have Go modules, but the few fields used here do not
// justify the dependencies, and strict field validation guards the writes.
const (
	gatewayAPIGroup     = "gateway.networking.k8s.io"
	gatewayKind         = "Gateway"
	agentgatewayGroup   = "agentgateway.dev"
	agentgatewayVersion = "v1alpha1"
	fieldName           = "name"
)

var (
	gatewayGVK = schema.GroupVersionKind{Group: gatewayAPIGroup, Version: "v1", Kind: gatewayKind}
	modelGVK   = schema.GroupVersionKind{Group: agentgatewayGroup, Version: agentgatewayVersion, Kind: "AgentgatewayModel"}
	policyGVK  = schema.GroupVersionKind{Group: agentgatewayGroup, Version: agentgatewayVersion, Kind: "AgentgatewayPolicy"}
	backendGVK = schema.GroupVersionKind{Group: agentgatewayGroup, Version: agentgatewayVersion, Kind: "AgentgatewayBackend"}
)

// gatewayRefMap renders a Gateway API parent or target reference.
func gatewayRefMap(name string, extra map[string]any) map[string]any {
	m := map[string]any{"group": gatewayAPIGroup, "kind": gatewayKind, fieldName: name}
	maps.Copy(m, extra)
	return m
}

// OperatorNamespaceKinds lists the third-party kinds the controller reads or
// writes in the operator namespace, for the manager to scope its cache.
func OperatorNamespaceKinds() []schema.GroupVersionKind {
	return []schema.GroupVersionKind{gatewayGVK, modelGVK, policyGVK, backendGVK}
}

// Rights are Roles in the two namespaces the operator works in (spec 02 §3):
// its own, where the platform's Secrets, the Gateway and the generated
// gateway objects live, and the engine's, where the engine boundary writes
// (its RBAC markers live with it). The dummy key Secret is read by name
// through the API reader, outside the cache: no list or watch on Secrets
// in the engine namespace. The markers are a package-level comment:
// controller-gen ignores RBAC markers attached to a type.
//
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareplatforms,verbs=get;list;watch
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareplatforms/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch,namespace=takecare-system
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch,namespace=takecare-system
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch,namespace=takecare-system
// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewaymodels;agentgatewaypolicies;agentgatewaybackends,verbs=get;list;watch;create;update;patch;delete,namespace=takecare-system
// +kubebuilder:rbac:groups="",resources=secrets,resourceNames=takecare-engine-dummy-key,verbs=get,namespace=kagent
// +kubebuilder:rbac:groups=apps,resources=deployments,resourceNames=takecare-operator,verbs=get,namespace=takecare-system
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch,namespace=kagent
// +kubebuilder:rbac:groups=takecare.agaetis.fr,resources=takecareconnectors,verbs=get;list;watch,namespace=takecare-system

// Reconciler reconciles the TakeCarePlatform.
type Reconciler struct {
	client.Client
	// APIReader bypasses the cache for the one Secret read in the engine
	// namespace, which the operator may get by name but not list or watch.
	APIReader client.Reader
	Recorder  recorder.EventRecorder
	// OperatorNamespace holds the platform's Secrets and must hold the Gateway.
	OperatorNamespace string
	// EngineNamespace is the one namespace the operator writes engine objects in.
	EngineNamespace string
	Engine          engine.Models
	// OperatorDeployment names the operator's own Deployment, for the
	// single-replica gap.
	OperatorDeployment string
	// Probe asks Prometheus, outside the reconciliation, whether it scrapes
	// the gateway.
	Probe *PromProbe
	// Telemetry follows spec.observability: the operator's own trace export.
	Telemetry TelemetryApplier
}

// TelemetryApplier is what the controller needs from internal/telemetry.
type TelemetryApplier interface {
	Apply(ctx context.Context, spec v1alpha1.ObservabilitySpec) error
}

// Reconcile computes the desired state from the platform's spec, applies it
// and reports it; it never assumes what a previous run did.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	var platform v1alpha1.TakeCarePlatform
	if err := r.Get(ctx, req.NamespacedName, &platform); err != nil {
		// Gone: the generated objects follow it (owner references), the
		// roles report PlatformMissing themselves (spec 01).
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	st := newStatus(&platform)

	gatewayNS, gatewayName := r.gatewayRef(&platform)
	switch {
	case gatewayName == "":
		st.set(v1alpha1.ConditionGatewayReady, false, ReasonGatewayMissing, "spec.gateway.gatewayRef names no Gateway")
		st.set(v1alpha1.ConditionModelsReady, false, ReasonGatewayMissing, "no gateway to route the models through")
	case gatewayNS != r.OperatorNamespace:
		msg := fmt.Sprintf("the Gateway %s/%s is outside the operator namespace %s: the models the operator generates must sit next to the provider key Secret, which it never copies elsewhere; use a Gateway in %s", gatewayNS, gatewayName, r.OperatorNamespace, r.OperatorNamespace)
		st.set(v1alpha1.ConditionGatewayReady, false, ReasonGatewayOutsideOperatorNamespace, msg)
		st.set(v1alpha1.ConditionModelsReady, false, ReasonGatewayOutsideOperatorNamespace, msg)
	default:
		route, err := r.reconcileGateway(ctx, &platform, st, gatewayNS, gatewayName)
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcileModels(ctx, &platform, st, route); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcileTracing(ctx, &platform, st, gatewayNS, gatewayName); err != nil {
			return ctrl.Result{}, err
		}
	}
	if _, found := st.get(v1alpha1.ConditionObservability); !found {
		st.set(v1alpha1.ConditionObservability, false, ReasonNotReady, "not evaluated while the gateway is not usable")
	}
	if r.Telemetry != nil {
		if err := r.Telemetry.Apply(ctx, platform.Spec.Observability); err != nil {
			// The operator's own export failing never blocks the platform:
			// the condition carries it.
			st.set(v1alpha1.ConditionObservability, false, ReasonTracingInvalidEndpoint, fmt.Sprintf("operator trace export: %v", err))
		}
	}
	if err := r.productionGaps(ctx, &platform, st, gatewayNS, gatewayName); err != nil {
		return ctrl.Result{}, err
	}

	ready := st.allTrue(v1alpha1.ConditionGatewayReady, v1alpha1.ConditionModelsReady, v1alpha1.ConditionObservability)
	if ready {
		st.set(v1alpha1.ConditionReady, true, ReasonReady, "gateway, models and observability are ready")
	} else {
		st.set(v1alpha1.ConditionReady, false, ReasonNotReady, st.firstFalse(v1alpha1.ConditionGatewayReady, v1alpha1.ConditionModelsReady, v1alpha1.ConditionObservability))
	}
	wasReady := meta.IsStatusConditionTrue(platform.Status.Conditions, v1alpha1.ConditionReady)
	if err := r.writeStatus(ctx, &platform, st); err != nil {
		return ctrl.Result{}, err
	}
	if ready != wasReady {
		if ready {
			r.Recorder.Eventf(&platform, nil, corev1.EventTypeNormal, ReasonReady, "Reconcile", "the platform is ready")
		} else {
			r.Recorder.Eventf(&platform, nil, corev1.EventTypeWarning, ReasonNotReady, "Reconcile", "%s", st.firstFalse(v1alpha1.ConditionGatewayReady, v1alpha1.ConditionModelsReady, v1alpha1.ConditionObservability))
		}
	}
	logger.V(1).Info("reconciled", "ready", ready)
	if !ready {
		// Statuses of generated objects arrive through watches; the engine
		// Secret, read outside the cache, does not: poll while not ready.
		return ctrl.Result{RequeueAfter: requeueNotReady}, nil
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) gatewayRef(p *v1alpha1.TakeCarePlatform) (namespace, name string) {
	ref := p.Spec.Gateway.GatewayRef
	if ref == nil {
		return "", ""
	}
	namespace = ref.Namespace
	if namespace == "" {
		namespace = r.OperatorNamespace
	}
	return namespace, ref.Name
}

// reconcileGateway sets GatewayReady and returns the gateway's base URL (empty
// when unknown).
func (r *Reconciler) reconcileGateway(ctx context.Context, _ *v1alpha1.TakeCarePlatform, st *status, namespace, name string) (string, error) {
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, gw); err != nil {
		if apierrors.IsNotFound(err) {
			st.set(v1alpha1.ConditionGatewayReady, false, ReasonGatewayMissing, fmt.Sprintf("Gateway %s/%s does not exist", namespace, name))
			return "", nil
		}
		return "", fmt.Errorf("get Gateway %s/%s: %w", namespace, name, err)
	}
	accepted, programmed := conditionTrue(gw.Object, "Accepted"), conditionTrue(gw.Object, "Programmed")
	if !accepted || !programmed {
		st.set(v1alpha1.ConditionGatewayReady, false, ReasonGatewayNotProgrammed, fmt.Sprintf("Gateway %s/%s: Accepted=%t, Programmed=%t", namespace, name, accepted, programmed))
		return "", nil
	}
	route, err := r.gatewayBaseURL(ctx, gw)
	if err != nil {
		return "", err
	}
	if route == "" {
		st.set(v1alpha1.ConditionGatewayReady, false, ReasonGatewayAddressUnknown, fmt.Sprintf("Gateway %s/%s has neither a labeled Service nor a status address", namespace, name))
		return "", nil
	}
	st.set(v1alpha1.ConditionGatewayReady, true, ReasonReady, fmt.Sprintf("Gateway %s/%s is programmed at %s", namespace, name, route))
	return route, nil
}

// gatewayBaseURL prefers the Service agentgateway labels with the Gateway's
// name (a stable in-cluster DNS name) over the Gateway's status addresses,
// which are load balancer addresses that in-cluster clients may not reach.
func (r *Reconciler) gatewayBaseURL(ctx context.Context, gw *unstructured.Unstructured) (string, error) {
	var services corev1.ServiceList
	if err := r.List(ctx, &services, client.InNamespace(gw.GetNamespace()), client.MatchingLabels{gatewayNameLabel: gw.GetName()}); err != nil {
		return "", fmt.Errorf("list Services of Gateway %s: %w", gw.GetName(), err)
	}
	slices.SortFunc(services.Items, func(a, b corev1.Service) int { return strings.Compare(a.Name, b.Name) })
	for _, svc := range services.Items {
		for _, port := range svc.Spec.Ports {
			if port.Port == 80 {
				return fmt.Sprintf("http://%s.%s.svc", svc.Name, svc.Namespace), nil
			}
		}
		if len(svc.Spec.Ports) > 0 {
			return fmt.Sprintf("http://%s.%s.svc:%d", svc.Name, svc.Namespace, svc.Spec.Ports[0].Port), nil
		}
	}
	addresses, _, _ := unstructured.NestedSlice(gw.Object, "status", "addresses")
	for _, a := range addresses {
		addr, ok := a.(map[string]any)
		if !ok {
			continue
		}
		if value, _ := addr["value"].(string); value != "" {
			return "http://" + value, nil
		}
	}
	return "", nil
}

// reconcileModels applies one gateway model and one engine configuration per
// declared model, and sets ModelsReady from the Secrets and the objects'
// statuses. Objects are applied whatever the Secrets' state: the condition
// names what is missing, and restoring it needs no other action (spec 01).
func (r *Reconciler) reconcileModels(ctx context.Context, p *v1alpha1.TakeCarePlatform, st *status, route string) error {
	if p.Spec.Engine.Namespace != r.EngineNamespace {
		st.set(v1alpha1.ConditionModelsReady, false, ReasonEngineNamespaceForbidden,
			fmt.Sprintf("spec.engine.namespace is %q but the operator may only write engine objects in %q (chart value kagent.namespaceOverride, which also places its Roles)", p.Spec.Engine.Namespace, r.EngineNamespace))
		return nil
	}
	var problems []string
	var dummy corev1.Secret
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: r.EngineNamespace, Name: DummyKeySecretName}, &dummy); err != nil {
		if !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) {
			return fmt.Errorf("get engine Secret %s/%s: %w", r.EngineNamespace, DummyKeySecretName, err)
		}
		problems = append(problems, fmt.Sprintf("Secret %s/%s (key %s) is missing: the chart renders it with kagent enabled, an engine you bring must hold it (value \"dummy\")", r.EngineNamespace, DummyKeySecretName, DummyKeyField))
	} else if _, ok := dummy.Data[DummyKeyField]; !ok {
		problems = append(problems, fmt.Sprintf("Secret %s/%s has no key %s", r.EngineNamespace, DummyKeySecretName, DummyKeyField))
	}
	if route == "" {
		st.set(v1alpha1.ConditionModelsReady, false, ReasonGatewayNotProgrammed, "waiting for the gateway's route")
		return nil
	}

	owner := *metav1.NewControllerRef(p, v1alpha1.GroupVersion.WithKind("TakeCarePlatform"))
	labels := r.labels(p)
	pending := []string{}
	var models []v1alpha1.ModelStatus
	for _, m := range p.Spec.Models {
		var secret corev1.Secret
		err := r.Get(ctx, client.ObjectKey{Namespace: r.OperatorNamespace, Name: m.APIKeySecretRef.Name}, &secret)
		switch {
		case apierrors.IsNotFound(err):
			problems = append(problems, fmt.Sprintf("model %s: Secret %s/%s does not exist", m.Name, r.OperatorNamespace, m.APIKeySecretRef.Name))
		case err != nil:
			return fmt.Errorf("get Secret %s/%s: %w", r.OperatorNamespace, m.APIKeySecretRef.Name, err)
		default:
			if _, ok := secret.Data[m.APIKeySecretRef.Key]; !ok {
				problems = append(problems, fmt.Sprintf("model %s: Secret %s/%s has no key %s", m.Name, r.OperatorNamespace, m.APIKeySecretRef.Name, m.APIKeySecretRef.Key))
			}
		}
		if err := r.applyGatewayModel(ctx, p, m, owner, labels); err != nil {
			return err
		}
		if err := r.Engine.EnsureModel(ctx, engine.Model{
			Name: m.Name, Namespace: r.EngineNamespace, Model: m.Model, GatewayURL: route + "/v1",
			DummyKeySecret: DummyKeySecretName, DummyKeyField: DummyKeyField, Owner: owner, Labels: labels,
		}); err != nil {
			return err
		}
		if ready, msg := r.gatewayModelReadiness(ctx, m.Name); !ready {
			pending = append(pending, fmt.Sprintf("model %s: gateway: %s", m.Name, msg))
		}
		readiness, err := r.Engine.ModelReadiness(ctx, r.EngineNamespace, m.Name)
		if err != nil {
			return err
		}
		if !readiness.Ready {
			pending = append(pending, fmt.Sprintf("model %s: engine: %s %s", m.Name, readiness.Reason, readiness.Message))
		}
		models = append(models, v1alpha1.ModelStatus{Name: m.Name, Route: route + "/v1"})
	}
	st.models = models
	switch {
	case len(problems) > 0:
		reason := ReasonSecretMissing
		if strings.Contains(problems[0], DummyKeySecretName) {
			reason = ReasonEngineSecretMissing
		}
		st.set(v1alpha1.ConditionModelsReady, false, reason, strings.Join(problems, "; "))
	case len(pending) > 0:
		st.set(v1alpha1.ConditionModelsReady, false, ReasonModelsPending, strings.Join(pending, "; "))
	default:
		st.set(v1alpha1.ConditionModelsReady, true, ReasonReady, fmt.Sprintf("%d model(s) accepted by the gateway and the engine", len(models)))
	}
	return nil
}

// applyGatewayModel renders one AgentgatewayModel: an OpenAI-compatible
// provider at the declared base URL, authenticated with the customer's
// Secret, which the gateway alone reads (ADR 0004).
func (r *Reconciler) applyGatewayModel(ctx context.Context, p *v1alpha1.TakeCarePlatform, m v1alpha1.ModelSpec, owner metav1.OwnerReference, labels map[string]string) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(modelGVK)
	obj.SetName(m.Name)
	obj.SetNamespace(r.OperatorNamespace)
	obj.SetLabels(labels)
	obj.SetOwnerReferences([]metav1.OwnerReference{owner})
	_, gatewayName := r.gatewayRef(p)
	obj.Object["spec"] = map[string]any{
		"parentRefs": []any{gatewayRefMap(gatewayName, map[string]any{"sectionName": "http"})},
		"match":      map[string]any{"model": m.Model},
		"provider":   "OpenAI",
		"baseURL":    m.BaseURL,
		"policies": map[string]any{"auth": map[string]any{"secretRef": map[string]any{
			fieldName: m.APIKeySecretRef.Name, "key": m.APIKeySecretRef.Key,
		}}},
	}
	return apply.Strict(ctx, r.Client, obj)
}

// gatewayModelReadiness reads the Accepted and ResolvedRefs conditions the
// gateway sets per parent.
func (r *Reconciler) gatewayModelReadiness(ctx context.Context, name string) (bool, string) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(modelGVK)
	if err := r.Get(ctx, client.ObjectKey{Namespace: r.OperatorNamespace, Name: name}, obj); err != nil {
		return false, fmt.Sprintf("AgentgatewayModel not readable: %v", err)
	}
	parents, _, _ := unstructured.NestedSlice(obj.Object, "status", "parents")
	if len(parents) == 0 {
		return false, "not attached to the gateway yet"
	}
	for _, par := range parents {
		parent, ok := par.(map[string]any)
		if !ok {
			continue
		}
		for _, t := range []string{"Accepted", "ResolvedRefs"} {
			if !conditionTrue(parent, t) {
				return false, fmt.Sprintf("%s is not True: %s", t, conditionMessage(parent, t))
			}
		}
	}
	return true, ""
}

// reconcileTracing keeps the gateway's tracing policy in step with
// spec.observability.otlp (ADR 0006): a static backend for the OTLP endpoint
// and a policy on the Gateway, or neither when no endpoint is declared.
func (r *Reconciler) reconcileTracing(ctx context.Context, p *v1alpha1.TakeCarePlatform, st *status, gatewayNS, gatewayName string) error {
	otlp := p.Spec.Observability.OTLP
	if otlp == nil || otlp.Endpoint == "" {
		for _, gvk := range []schema.GroupVersionKind{policyGVK, backendGVK} {
			obj := &unstructured.Unstructured{}
			obj.SetGroupVersionKind(gvk)
			obj.SetName(tracingObjectName)
			obj.SetNamespace(gatewayNS)
			if err := r.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("delete %s %s: %w", gvk.Kind, tracingObjectName, err)
			}
		}
		st.set(v1alpha1.ConditionObservability, true, ReasonTracingExportDisabled, "no OTLP endpoint declared: traces are not exported")
		return nil
	}
	host, port, err := endpointHostPort(otlp.Endpoint, otlp.Protocol)
	if err != nil {
		st.set(v1alpha1.ConditionObservability, false, ReasonTracingInvalidEndpoint, err.Error())
		return nil
	}
	owner := *metav1.NewControllerRef(p, v1alpha1.GroupVersion.WithKind("TakeCarePlatform"))
	labels := r.labels(p)

	backend := &unstructured.Unstructured{}
	backend.SetGroupVersionKind(backendGVK)
	backend.SetName(tracingObjectName)
	backend.SetNamespace(gatewayNS)
	backend.SetLabels(labels)
	backend.SetOwnerReferences([]metav1.OwnerReference{owner})
	backend.Object["spec"] = map[string]any{"static": map[string]any{"host": host, "port": port}}
	if err := apply.Strict(ctx, r.Client, backend); err != nil {
		return err
	}

	protocol := "GRPC"
	if strings.EqualFold(otlp.Protocol, "http") {
		protocol = "HTTP"
	}
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(policyGVK)
	policy.SetName(tracingObjectName)
	policy.SetNamespace(gatewayNS)
	policy.SetLabels(labels)
	policy.SetOwnerReferences([]metav1.OwnerReference{owner})
	policy.Object["spec"] = map[string]any{
		"targetRefs": []any{gatewayRefMap(gatewayName, nil)},
		"frontend": map[string]any{"tracing": map[string]any{
			"backendRef": map[string]any{"group": agentgatewayGroup, "kind": "AgentgatewayBackend", fieldName: tracingObjectName, "port": port},
			"protocol":   protocol,
			// Only the traces the gateway starts are sampled at the platform's
			// ratio: a request that arrives with a traceparent keeps its
			// parent's decision (clientSampling defaults to 100 %,
			// parentNotSampled to honoring the client), so a trace the
			// operator kept never loses its gateway spans (ADR 0006).
			"randomSampling": strconv.FormatFloat(samplingRatio(p.Spec.Observability.SamplingPerMille), 'f', -1, 64),
			// The fallback correlation of ADR 0006: the caller's investigation
			// id, passed in a request header, lands on the gateway's spans.
			"attributes": map[string]any{"add": []any{map[string]any{
				fieldName: semconv.GenAIConversationID, "expression": `request.headers["x-conversation-id"]`,
			}}},
		}},
	}
	if err := apply.Strict(ctx, r.Client, policy); err != nil {
		return err
	}
	st.set(v1alpha1.ConditionObservability, true, ReasonTracingConfigured, fmt.Sprintf("gateway traces exported to %s over %s", otlp.Endpoint, protocol))
	return nil
}

// endpointHostPort splits an OTLP endpoint URL into what a static backend needs.
func endpointHostPort(endpoint, protocol string) (string, int64, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return "", 0, fmt.Errorf("observability.otlp.endpoint %q is not a URL with a host", endpoint)
	}
	port := int64(4317)
	if strings.EqualFold(protocol, "http") {
		port = 4318
	}
	if u.Port() != "" {
		p, err := strconv.ParseInt(u.Port(), 10, 32)
		if err != nil {
			return "", 0, fmt.Errorf("observability.otlp.endpoint %q: bad port", endpoint)
		}
		port = p
	}
	return u.Hostname(), port, nil
}

func (r *Reconciler) labels(p *v1alpha1.TakeCarePlatform) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": apply.FieldOwner,
		"app.kubernetes.io/part-of":    "takecare",
		"takecare.agaetis.fr/platform": p.Name,
	}
}

func (r *Reconciler) writeStatus(ctx context.Context, p *v1alpha1.TakeCarePlatform, st *status) error {
	p.Status.ObservedGeneration = p.Generation
	p.Status.Models = st.models
	p.Status.ProductionGaps = st.gaps
	for _, c := range st.conditions {
		meta.SetStatusCondition(&p.Status.Conditions, c)
	}
	if err := r.Status().Update(ctx, p); err != nil {
		return fmt.Errorf("update status of %s: %w", p.Name, err)
	}
	return nil
}

// samplingRatio converts samplingPerMille into a 0..1 ratio (nil: everything).
func samplingRatio(perMille *int32) float64 {
	if perMille == nil {
		return 1
	}
	return min(max(float64(*perMille)/1000, 0), 1)
}

// productionGaps evaluates the checks that depend on the platform alone and
// sets ProductionReady (spec 01): advisory, never part of Ready.
func (r *Reconciler) productionGaps(ctx context.Context, p *v1alpha1.TakeCarePlatform, st *status, gatewayNS, gatewayName string) error {
	var gaps []v1alpha1.ProductionGap
	add := func(id, detail string) { gaps = append(gaps, v1alpha1.ProductionGap{ID: id, Detail: detail}) }
	obs := p.Spec.Observability

	if obs.OTLP == nil || obs.OTLP.Endpoint == "" {
		add(GapNoTelemetryExport, "observability.otlp.endpoint is not set: no trace leaves the cluster")
	}
	if obs.CaptureContent {
		add(GapContentCaptureOn, "observability.captureContent is true: prompts, tool arguments and answers are recorded in traces")
	}

	if r.OperatorDeployment != "" {
		var dep appsv1.Deployment
		err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: r.OperatorNamespace, Name: r.OperatorDeployment}, &dep)
		switch {
		case err == nil:
			if dep.Spec.Replicas != nil && *dep.Spec.Replicas < 2 {
				add(GapSingleReplica, fmt.Sprintf("the operator Deployment %s runs %d replica", r.OperatorDeployment, *dep.Spec.Replicas))
			}
		case apierrors.IsNotFound(err) || apierrors.IsForbidden(err):
			add(GapSingleReplica, fmt.Sprintf("the operator Deployment %s/%s cannot be read: %v", r.OperatorNamespace, r.OperatorDeployment, err))
		default:
			return fmt.Errorf("get Deployment %s/%s: %w", r.OperatorNamespace, r.OperatorDeployment, err)
		}
	}

	if obs.Metrics == nil || obs.Metrics.PrometheusURL == "" {
		add(GapGatewayMetricsNotScraped, "observability.metrics.prometheusURL is not set: usage and cost will be unknown")
	} else if r.Probe != nil {
		r.Probe.SetTarget(ProbeTarget{PrometheusURL: obs.Metrics.PrometheusURL, GatewayNamespace: gatewayNS, GatewayName: gatewayName})
		res := r.Probe.Result()
		switch {
		case !res.Known && res.Detail != "":
			add(GapGatewayMetricsNotScraped, res.Detail)
		case !res.Known:
			add(GapGatewayMetricsNotScraped, "Prometheus has not answered yet")
		case !res.Scraped:
			add(GapGatewayMetricsNotScraped, res.Detail)
		}
	}

	var policies networkingv1.NetworkPolicyList
	if err := r.List(ctx, &policies, client.InNamespace(r.EngineNamespace), client.MatchingLabels{partOfLabel: partOfValue}); err != nil {
		return fmt.Errorf("list NetworkPolicies in %s: %w", r.EngineNamespace, err)
	}
	if len(policies.Items) == 0 {
		add(GapNoNetworkPolicies, fmt.Sprintf("no NetworkPolicy of TakeCare's in the engine namespace %s (networkPolicies.enabled is false); whether the CNI enforces them is not checked", r.EngineNamespace))
	}

	// connector-not-ready (spec 05): any connector of the operator namespace
	// that is not Ready. role-without-report joins with the Doc (PR 4).
	var connectors v1alpha1.TakeCareConnectorList
	if err := r.List(ctx, &connectors, client.InNamespace(r.OperatorNamespace)); err != nil {
		return fmt.Errorf("list TakeCareConnectors in %s: %w", r.OperatorNamespace, err)
	}
	var notReady []string
	for _, c := range connectors.Items {
		if !meta.IsStatusConditionTrue(c.Status.Conditions, v1alpha1.ConditionReady) {
			notReady = append(notReady, c.Name)
		}
	}
	if len(notReady) > 0 {
		slices.Sort(notReady)
		add(GapConnectorNotReady, fmt.Sprintf("connector(s) not Ready: %s", strings.Join(notReady, ", ")))
	}

	st.gaps = gaps
	if len(gaps) == 0 {
		st.set(v1alpha1.ConditionProductionReady, true, ReasonNoGaps, "no production gap found")
		return nil
	}
	details := make([]string, 0, len(gaps))
	for _, g := range gaps {
		details = append(details, g.ID)
	}
	st.set(v1alpha1.ConditionProductionReady, false, ReasonGapsFound, fmt.Sprintf("%d gap(s): %s", len(gaps), strings.Join(details, "; ")))
	return nil
}

// conditionTrue reads a metav1-shaped condition from unstructured content.
func conditionTrue(obj map[string]any, condType string) bool {
	conditions, _, _ := unstructured.NestedSlice(obj, "status", "conditions")
	if conditions == nil {
		conditions, _, _ = unstructured.NestedSlice(obj, "conditions")
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if ok && cond["type"] == condType {
			return cond["status"] == "True"
		}
	}
	return false
}

func conditionMessage(obj map[string]any, condType string) string {
	conditions, _, _ := unstructured.NestedSlice(obj, "status", "conditions")
	if conditions == nil {
		conditions, _, _ = unstructured.NestedSlice(obj, "conditions")
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if ok && cond["type"] == condType {
			reason, _ := cond["reason"].(string)
			message, _ := cond["message"].(string)
			return strings.TrimSpace(reason + " " + message)
		}
	}
	return "condition absent"
}

// status collects the conditions of one reconcile before they are written.
type status struct {
	conditions []metav1.Condition
	models     []v1alpha1.ModelStatus
	gaps       []v1alpha1.ProductionGap
	generation int64
}

func newStatus(p *v1alpha1.TakeCarePlatform) *status {
	return &status{generation: p.Generation}
}

func (s *status) set(condType string, ok bool, reason, message string) {
	st := metav1.ConditionFalse
	if ok {
		st = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&s.conditions, metav1.Condition{Type: condType, Status: st, Reason: reason, Message: message, ObservedGeneration: s.generation})
}

func (s *status) get(condType string) (*metav1.Condition, bool) {
	c := meta.FindStatusCondition(s.conditions, condType)
	return c, c != nil
}

func (s *status) allTrue(kinds ...string) bool {
	for _, t := range kinds {
		if !meta.IsStatusConditionTrue(s.conditions, t) {
			return false
		}
	}
	return true
}

func (s *status) firstFalse(kinds ...string) string {
	for _, t := range kinds {
		if c := meta.FindStatusCondition(s.conditions, t); c != nil && c.Status != metav1.ConditionTrue {
			return fmt.Sprintf("%s: %s", t, c.Message)
		}
	}
	return ""
}

// SetupWithManager registers the controller: the platform, what it generates,
// and what it depends on (Secrets and Services of its namespace, the Gateway),
// every event mapped to the singleton.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	toMain := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{Name: PlatformName}}
	})
	inOperatorNS := predicate.NewPredicateFuncs(func(o client.Object) bool { return o.GetNamespace() == r.OperatorNamespace })
	b := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.TakeCarePlatform{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Secret{}, toMain, builder.WithPredicates(inOperatorNS)).
		Watches(&corev1.Service{}, toMain, builder.WithPredicates(inOperatorNS, predicate.NewPredicateFuncs(func(o client.Object) bool {
			_, labeled := o.GetLabels()[gatewayNameLabel]
			return labeled
		})))
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	b = b.Watches(gw, toMain, builder.WithPredicates(inOperatorNS))
	inEngineNS := predicate.NewPredicateFuncs(func(o client.Object) bool { return o.GetNamespace() == r.EngineNamespace })
	b = b.Watches(&networkingv1.NetworkPolicy{}, toMain, builder.WithPredicates(inEngineNS))
	b = b.Watches(&v1alpha1.TakeCareConnector{}, toMain, builder.WithPredicates(inOperatorNS))
	if r.Probe != nil {
		b = b.WatchesRawSource(source.TypedChannel(r.Probe.Events(), handler.TypedEnqueueRequestsFromMapFunc(
			func(_ context.Context, req reconcile.Request) []reconcile.Request { return []reconcile.Request{req} })))
	}
	for _, gvk := range append([]schema.GroupVersionKind{modelGVK, policyGVK, backendGVK}, r.Engine.OwnedTypes()...) {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		b = b.Owns(obj)
	}
	return b.Named("platform").Complete(r)
}
