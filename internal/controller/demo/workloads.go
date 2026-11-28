// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package demo

import (
	"fmt"
	"reflect"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/demo/corpus"
	"github.com/agaetis-it/takecare-agents/internal/demoapp"
)

// The other two workloads of the ground, always deployed (spec 14 §5), as
// spike 001 deployed them; the scenario picks which one the incident
// breaks. Both run the demo-app image, healthy.
const (
	// ordersName is ready only while its database host resolves. orders-db
	// is a Service without pods: its name resolves, which is all
	// orders-api checks; bad-config-rollout points DB_HOST at
	// orders-db-primary, an endpoint the database team has not created yet.
	ordersName     = "orders-api"
	ordersDBName   = "orders-db"
	ordersDBPort   = 5432
	ordersConfig   = "orders-config"
	ordersPrimary  = "orders-db-primary"
	ordersVersion  = "3.8.0"
	ordersRollout  = "3.9.0"
	ordersPoolSize = "20"
	// workerName refuses to start without its settings. Its SMTP password
	// comes from the Secret notifications-smtp the chart renders (the
	// operator holds no right on Secrets), key SMTP_PASS. Version 2.0.0
	// names it SMTP_PASSWORD, a key the Secret does not hold; the reference
	// is optional, as the chart has it in both versions, so the variable is
	// left unset, the process exits, and the pod restarts in a loop.
	workerName     = "notifications-worker"
	smtpSecret     = "notifications-smtp"
	smtpKey        = "SMTP_PASS"
	smtpKeyRelease = "SMTP_PASSWORD"
	workerVersion  = "1.9.4"
	workerRelease  = "2.0.0"
	// annotationConfig plays a Helm checksum annotation: a change of
	// orders-config rolls orders-api's pods.
	annotationConfig = "takecare.agaetis.fr/config-version"
	labelVersion     = "app.kubernetes.io/version"
)

// fault is what a scenario's incident does, for the event and the alert
// window.
type fault struct {
	// summary is the event's wording.
	summary string
	// window is when the first expected alert fires after the fault is in
	// place; proxy-timeout's follows the ramp instead (alertDelay).
	window func(ramp time.Duration) (from, to time.Duration)
}

// faults are the scenarios this controller injects: one per value of the
// TakeCareDemo scenario enum (spec 14 §3.1).
var faults = map[string]fault{
	corpus.ScenarioProxyTimeout: {
		summary: fmt.Sprintf("DBProxy idleClientTimeout %d s, billing-api leaky", proxyIdleTimeout),
		window:  alertDelay,
	},
	corpus.ScenarioBadConfigRollout: {
		summary: fmt.Sprintf("orders-config %s rolled out, DB_HOST on %s", ordersRollout, ordersPrimary),
		window:  notReadyDelay,
	},
	corpus.ScenarioMissingSecretKey: {
		summary: fmt.Sprintf("notifications-worker %s rolled out, reading key %s of Secret %s", workerRelease, smtpKeyRelease, smtpSecret),
		window:  notReadyDelay,
	},
}

// injected is the scenario whose fault the phase carries; none when
// healthy.
func injected(d *v1alpha1.TakeCareDemo, incident bool) string {
	if incident {
		return d.Spec.Scenario
	}
	return corpus.ScenarioNone
}

func ordersDBHost(d *v1alpha1.TakeCareDemo, host string) string {
	return fmt.Sprintf("%s.%s.svc:%d", host, d.Spec.AppNamespace, ordersDBPort)
}

// ordersConfigMap is orders-api's configuration, its DB_HOST on orders-db,
// or, in bad-config-rollout, on the primary endpoint not yet created.
func ordersConfigMap(d *v1alpha1.TakeCareDemo, faulty bool) *corev1.ConfigMap {
	host, version := ordersDBHost(d, ordersDBName), ordersVersion
	if faulty {
		host, version = ordersDBHost(d, ordersPrimary), ordersRollout
	}
	labels := ownerLabels(d)
	labels[labelVersion] = version
	return &corev1.ConfigMap{
		APIVersion: "v1", Kind: "ConfigMap",
		Name: ordersConfig, Namespace: string(d.Spec.AppNamespace), Labels: labels,
		Data: map[string]string{"DB_HOST": host, "DB_POOL_MAX_SIZE": ordersPoolSize},
	}
}

// ordersDeployment is orders-api. RollingUpdate without unavailability:
// the new pod of a rollout waits for its readiness while the old one keeps
// serving, so a bad rollout leaves one pod not ready (PodNotReady).
func ordersDeployment(d *v1alpha1.TakeCareDemo, image string, faulty bool) *appsv1.Deployment {
	version := ordersVersion
	if faulty {
		version = ordersRollout
	}
	zero, one := intstr.FromInt32(0), intstr.FromInt32(1)
	return workload(d, image, faulty, workloadSpec{
		name: ordersName, mode: demoapp.ModeHealthy, memoryLimit: 256,
		strategy: appsv1.DeploymentStrategy{
			Type:          appsv1.RollingUpdateDeploymentStrategyType,
			RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: &zero, MaxSurge: &one},
		},
		envFrom:     []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{Name: ordersConfig}}},
		annotations: map[string]string{annotationConfig: version},
	})
}

// ordersDBService is the orders database's name: a Service without
// selector nor pods, whose name resolves.
func ordersDBService(d *v1alpha1.TakeCareDemo) *corev1.Service {
	return &corev1.Service{
		APIVersion: "v1", Kind: "Service",
		Name: ordersDBName, Namespace: string(d.Spec.AppNamespace), Labels: ownerLabels(d),
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Name: "postgres", Port: ordersDBPort, TargetPort: intstr.FromInt32(ordersDBPort)}},
		},
	}
}

// workerDeployment is notifications-worker, version 1.9.4, or, in
// missing-secret-key, version 2.0.0 reading a key the Secret lacks.
func workerDeployment(d *v1alpha1.TakeCareDemo, image string, faulty bool) *appsv1.Deployment {
	version, password := workerVersion, smtpKey
	if faulty {
		version, password = workerRelease, smtpKeyRelease
	}
	return workload(d, image, faulty, workloadSpec{
		name: workerName, mode: demoapp.ModeHealthy, memoryLimit: 128,
		strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
		labels:   map[string]string{labelVersion: version},
		env: []corev1.EnvVar{
			{Name: "APP_VERSION", Value: version},
			{Name: "REQUIRED_SETTINGS", Value: "SMTP_HOST,SMTP_USER," + password},
			{Name: "SMTP_HOST", Value: "smtp.example.invalid:587"},
			{Name: "SMTP_USER", Value: "notifications"},
			{Name: password, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				Name: smtpSecret, Key: password, Optional: new(true),
			}}},
		},
	})
}

// scenery is every object the controller writes in the app and platform
// namespaces, with the fault of the scenario injected (none: healthy).
func scenery(d *v1alpha1.TakeCareDemo, image, scenario string) []client.Object {
	return []client.Object{
		appDeployment(d, image, scenario == corpus.ScenarioProxyTimeout), appService(d),
		dbProxy(d, scenario == corpus.ScenarioProxyTimeout),
		ordersConfigMap(d, scenario == corpus.ScenarioBadConfigRollout),
		ordersDeployment(d, image, scenario == corpus.ScenarioBadConfigRollout), service(d, ordersName), ordersDBService(d),
		workerDeployment(d, image, scenario == corpus.ScenarioMissingSecretKey),
	}
}

// deployments are the scenery's Deployments, in order.
func deployments(objs []client.Object) []*appsv1.Deployment {
	var out []*appsv1.Deployment
	for _, o := range objs {
		if dep, ok := o.(*appsv1.Deployment); ok {
			out = append(out, dep)
		}
	}
	return out
}

// Injection names the objects a scenario's incident changes, "<Kind>
// <namespace>/<name>", plus "corpus": every incident regenerates the
// corpus with its dates. A scenario's file describes the same steps (spec
// 14 §3.4), which a test checks against this. The demo is the one make
// demo-up applies.
func Injection(scenario string) ([]string, error) {
	if _, ok := faults[scenario]; !ok {
		return nil, fmt.Errorf("scenario %q is not injected by the TakeCareDemo controller", scenario)
	}
	d := &v1alpha1.TakeCareDemo{}
	d.Spec = v1alpha1.TakeCareDemoSpec{Scenario: scenario, AppNamespace: "demo-apps", PlatformNamespace: "ack-system", RampMinutes: 4}
	d.Status.LastHandled.Incident = "1"
	healthy, broken := scenery(d, "image", corpus.ScenarioNone), scenery(d, "image", scenario)
	var out []string
	for i, o := range broken {
		if !reflect.DeepEqual(o, healthy[i]) {
			out = append(out, fmt.Sprintf("%s %s/%s", o.GetObjectKind().GroupVersionKind().Kind, o.GetNamespace(), o.GetName()))
		}
	}
	return slices.Concat(out, []string{"corpus"}), nil
}

// AlertWindow is when the first of a scenario's alerts is expected after
// its fault is in place, as the status's expectedAlertWindow says; a
// scenario's file must allow at least this window (spec 14 §3.4).
func AlertWindow(scenario string, ramp time.Duration) (from, to time.Duration, err error) {
	f, ok := faults[scenario]
	if !ok {
		return 0, 0, fmt.Errorf("scenario %q is not injected by the TakeCareDemo controller", scenario)
	}
	from, to = f.window(ramp)
	return from, to, nil
}

// faultInPlace reports whether a Deployment broken on purpose runs the
// injected template: the controller saw it and created its pod, which is
// expected never to become available.
func faultInPlace(dep, want *appsv1.Deployment) bool {
	got := dep.Spec.Template.Annotations
	return got[annotationIncident] == want.Spec.Template.Annotations[annotationIncident] &&
		dep.Status.ObservedGeneration == dep.Generation && dep.Status.UpdatedReplicas >= 1
}
