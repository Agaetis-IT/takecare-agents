// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package demo

import (
	"fmt"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/demoapp"
)

// The scenery of proxy-timeout (spec 03), as spike 001 deployed it.
const (
	appName   = "billing-api"
	appPort   = 8080
	proxyName = "billing-db-proxy"
	// leakCeilingMB and memoryLimitMB put the leak at about 70 % of the
	// limit (spec 03, AC 2): above RB-007's 40 % "memory pressure" line,
	// never OOM-killed.
	leakCeilingMB = 360
	memoryLimitMB = 512
	// poolIdleTimeout is the application half of the diagnosis: above the
	// proxy's 600 s during an incident, which AWS documents as the wrong way.
	poolIdleTimeout = 900
	// proxyIdleTimeout is the infrastructure half, set by the incident.
	proxyIdleTimeout = 600
)

// Labels and annotations of the scenery.
const (
	LabelOwner     = "takecare.agaetis.fr/demo"
	labelPartOf    = "app.kubernetes.io/part-of"
	labelApp       = "app"
	partOf         = "takecare-demo"
	annotationMode = "takecare.agaetis.fr/mode"
)

// DBProxyGVK is ACK's DBProxy: only its CRD is installed, the resource is
// documentation the Doc reads.
var DBProxyGVK = schema.GroupVersionKind{Group: "rds.services.k8s.aws", Version: "v1alpha1", Kind: "DBProxy"}

func ownerLabels(d *v1alpha1.TakeCareDemo) map[string]string {
	return map[string]string{LabelOwner: d.Name, labelPartOf: partOf}
}

// appDeployment is billing-api in a mode. Recreate: one pod at a time, so
// that the metrics never mix two pods; the ramp starts with the new pod.
func appDeployment(d *v1alpha1.TakeCareDemo, image, mode string) *appsv1.Deployment {
	labels := map[string]string{labelApp: appName, labelPartOf: partOf}
	env := []corev1.EnvVar{
		{Name: "MODE", Value: mode},
		{Name: "SERVICE", Value: appName},
		{Name: "RAMP_MINUTES", Value: strconv.Itoa(int(d.Spec.RampMinutes))},
		{Name: "LEAK_CEILING_MB", Value: strconv.Itoa(leakCeilingMB)},
		{Name: "MEMORY_LIMIT_MB", Value: strconv.Itoa(memoryLimitMB)},
		{Name: "DB_POOL_IDLE_TIMEOUT_SECONDS", Value: strconv.Itoa(poolIdleTimeout)},
	}
	probe := func(path string, period int32) *corev1.Probe {
		return &corev1.Probe{PeriodSeconds: period, HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromInt32(appPort)}}
	}
	return &appsv1.Deployment{
		APIVersion: "apps/v1", Kind: "Deployment",
		Name: appName, Namespace: string(d.Spec.AppNamespace), Labels: ownerLabels(d),
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(1)),
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{labelApp: appName}},
			Template: corev1.PodTemplateSpec{
				Labels: labels, Annotations: map[string]string{
					"prometheus.io/scrape": "true",
					"prometheus.io/port":   strconv.Itoa(appPort),
					"prometheus.io/path":   "/metrics",
					annotationMode:         mode,
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: new(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: new(true), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name: "app", Image: image, Env: env,
						Ports:          []corev1.ContainerPort{{Name: "http", ContainerPort: appPort}},
						ReadinessProbe: probe("/ready", 10),
						LivenessProbe:  probe("/healthz", 20),
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("20m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", memoryLimitMB))},
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: new(false), ReadOnlyRootFilesystem: new(true),
							Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
				},
			},
		},
	}
}

func appService(d *v1alpha1.TakeCareDemo) *corev1.Service {
	return &corev1.Service{
		APIVersion: "v1", Kind: "Service",
		Name: appName, Namespace: string(d.Spec.AppNamespace), Labels: ownerLabels(d),
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{labelApp: appName},
			Ports:    []corev1.ServicePort{{Name: "http", Port: appPort, TargetPort: intstr.FromInt32(appPort)}},
		},
	}
}

// dbProxy is the platform team's RDS Proxy. Healthy, idleClientTimeout is
// absent (AWS's default, 1800 s); the incident sets it to 600 s, below the
// pool's 900 s. Every other field is the spike's.
func dbProxy(d *v1alpha1.TakeCareDemo, incident bool) *unstructured.Unstructured {
	spec := map[string]any{
		"name":         proxyName,
		"engineFamily": "POSTGRESQL",
		"requireTLS":   true,
		"roleARN":      "arn:aws:iam::123456789012:role/rds-proxy-billing",
		"auth": []any{map[string]any{ //nolint:gosec // a fictional ARN and account: the DBProxy is scenery, it holds no credential
			"authScheme":  "SECRETS",
			"iamAuth":     "REQUIRED",
			"userName":    "billing_app",
			"description": "Application account of billing-api",
			"secretARN":   "arn:aws:secretsmanager:eu-west-3:123456789012:secret:rds/billing-app",
		}},
		"vpcSubnetIDs": []any{"subnet-0f1c9a2b3d4e5f601", "subnet-0f1c9a2b3d4e5f602"},
	}
	if incident {
		spec["idleClientTimeout"] = int64(proxyIdleTimeout)
	}
	u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	u.SetGroupVersionKind(DBProxyGVK)
	u.SetName(proxyName)
	u.SetNamespace(string(d.Spec.PlatformNamespace))
	u.SetLabels(ownerLabels(d))
	return u
}

// mode is the application's mode for a phase.
func mode(incident bool) string {
	if incident {
		return demoapp.ModeLeaky
	}
	return demoapp.ModeHealthy
}
