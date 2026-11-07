// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package alerts

import (
	"regexp"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// The Alertmanager adapter (ADR 0015 §4): POST /v1/alerts takes
// Alertmanager's webhook and turns each alert into the internal record.
// Nothing below the adapter reads Alertmanager's format.

// Payload is Alertmanager's webhook body, version 4.
type Payload struct {
	Version string  `json:"version"`
	Status  string  `json:"status"`
	Alerts  []Alert `json:"alerts"`
}

// Alert is one alert of a webhook.
type Alert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	Fingerprint string            `json:"fingerprint"`
}

// Labels the intake reads.
const (
	labelAlertName   = "alertname"
	labelNamespace   = "namespace"
	labelService     = "service"
	labelPod         = "pod"
	labelSilent      = "takecare_silent"
	labelDeployment  = "deployment"
	labelStatefulSet = "statefulset"
	labelDaemonSet   = "daemonset"
	labelJob         = "job_name"
)

// podSuffix matches the generated suffixes of a pod name: a Deployment's
// "-<replicaset hash>-<pod hash>", or a StatefulSet's or Job's "-<ordinal>"
// or "-<pod hash>".
var podSuffix = regexp.MustCompile(`(-[a-z0-9]{8,10})?-[a-z0-9]{5}$|-[0-9]+$`)

// Workload is the incident key of an alert with its namespace (spec 02 §1),
// from the alert's labels only: the operator reads nothing in the
// customer's namespaces. A Deployment, StatefulSet, DaemonSet or Job label
// wins; then the pod name without its generated suffixes; then the
// service; then the alert name, so that unrelated alerts never merge on an
// empty key.
func Workload(labels map[string]string) string {
	for _, key := range []string{labelDeployment, labelStatefulSet, labelDaemonSet, labelJob} {
		if v := labels[key]; v != "" {
			return v
		}
	}
	if pod := labels[labelPod]; pod != "" {
		if base := podSuffix.ReplaceAllString(pod, ""); base != "" {
			return base
		}
		return pod
	}
	if svc := labels[labelService]; svc != "" {
		return svc
	}
	return "alert:" + labels[labelAlertName]
}

// The states an adapter gives an alert.
const (
	stateFiring   = "firing"
	stateResolved = "resolved"
)

// incoming is an alert as an adapter hands it to the intake: the record,
// and beside it what is not the record's: its state and the
// takecare_silent flag of the bench (spec 04).
type incoming struct {
	record v1alpha1.AlertRecord
	state  string
	silent bool
}

// fromAlertmanager turns a webhook's alerts into records.
func fromAlertmanager(p Payload) []incoming {
	out := make([]incoming, 0, len(p.Alerts))
	for _, a := range p.Alerts {
		out = append(out, incoming{
			record: v1alpha1.AlertRecord{
				Name: a.Labels[labelAlertName], Fingerprint: a.Fingerprint,
				Service: a.Labels[labelService], Namespace: a.Labels[labelNamespace], Workload: Workload(a.Labels),
				FiredAt: metav1.NewTime(a.StartsAt), Labels: a.Labels, Annotations: a.Annotations,
				Source: v1alpha1.SourceAlertmanager,
			},
			state:  a.Status,
			silent: a.Labels[labelSilent] == "true",
		})
	}
	return out
}
