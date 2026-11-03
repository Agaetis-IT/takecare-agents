// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package alerts is the Doc's trigger (spec 02 §1, spec 06 §2): an
// Alertmanager webhook that turns firing alerts in a Doc's scope into
// TakeCareInvestigation records. Creating the record is the deduplication;
// alerts of one incident (same namespace and workload, within the cooldown)
// are folded into the first one's record. The package creates records and
// nothing else: the run on the engine is the Doc controller's.
package alerts

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
)

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

// IncidentKey is a label-safe digest of namespace and workload.
func IncidentKey(namespace, workload string) string {
	return digest(namespace+"/"+workload, 16)
}

// InvestigationName is deterministic per alert occurrence: the same alert
// sent twice (same fingerprint and startsAt) names the same record, and
// creating it twice is the deduplication (AlreadyExists is success).
func InvestigationName(role, fingerprint string, startsAt time.Time) string {
	fp := strings.ToLower(fingerprint)
	if len(fp) > 10 {
		fp = fp[:10]
	}
	if fp == "" {
		fp = "nofp"
	}
	// The digest covers the whole fingerprint, not only the readable prefix:
	// two fingerprints sharing their first ten characters must not name the
	// same record (a test and the smoke run both hit it).
	return role + "-" + fp + "-" + digest(strings.ToLower(fingerprint)+"|"+startsAt.UTC().Format(time.RFC3339Nano), 6)
}

func digest(s string, n int) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:n]
}
