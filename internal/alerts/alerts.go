// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package alerts is the Doc's trigger (spec 02 §1, spec 06 §2): alert
// sources' adapters (Alertmanager's webhook) turn alerts into one internal
// record, v1alpha1.AlertRecord, resolved to a Map entry when the Map knows
// it; the intake turns firing records in a Doc's scope into
// TakeCareInvestigation records. Creating the record is the deduplication;
// alerts of one incident (same Map entry, or else same namespace and
// workload, within the cooldown)
// are folded into the first one's record. The package creates records and
// nothing else: the run on the engine is the Doc controller's.
package alerts

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// IncidentKey is a label-safe digest of the alert's Map entry when it
// resolved to one, of its namespace and workload otherwise (spec 02 §1).
func IncidentKey(r v1alpha1.AlertRecord) string {
	if r.Entity != "" {
		return digest("entity:"+string(r.Entity), 16)
	}
	return digest(r.Namespace+"/"+r.Workload, 16)
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
