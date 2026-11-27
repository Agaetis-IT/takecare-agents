// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package doc

import (
	"testing"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

func TestStatedConfidence(t *testing.T) {
	const body = "Root cause: the proxy's idle timeout.\nAction: the platform team aligns it.\n"
	for _, tc := range []struct {
		name, answer string
		want         v1alpha1.Confidence
	}{
		{"high with an em dash", body + "Confidence: high — both settings were read.", v1alpha1.ConfidenceHigh},
		{"medium with a hyphen", body + "Confidence: medium - one link is inferred.", v1alpha1.ConfidenceMedium},
		{"low, capitalised", body + "Confidence: Low — the logs were not readable.", v1alpha1.ConfidenceLow},
		{"token alone", body + "Confidence: high", v1alpha1.ConfidenceHigh},
		{"bold label", body + "**Confidence:** high — read.", v1alpha1.ConfidenceHigh},
		{"bold token", body + "Confidence: **medium** — inferred.", v1alpha1.ConfidenceMedium},
		{"italic line", body + "_Confidence: low — nothing read._", v1alpha1.ConfidenceLow},
		{"list item", body + "- Confidence: medium — inferred.", v1alpha1.ConfidenceMedium},
		{"trailing blank lines", body + "Confidence: low — not read.\n\n  \n", v1alpha1.ConfidenceLow},
		{"CRLF", "Root cause: x.\r\nConfidence: high — read.\r\n", v1alpha1.ConfidenceHigh},
		{"no line", body, v1alpha1.ConfidenceUnstated},
		{"empty answer", "", v1alpha1.ConfidenceUnstated},
		{"not on the last line", "Confidence: high — read.\nRoot cause: x.", v1alpha1.ConfidenceUnstated},
		{"inside a sentence", body + "My confidence: high.", v1alpha1.ConfidenceUnstated},
		{"another token", body + "Confidence: certain — read.", v1alpha1.ConfidenceUnstated},
		{"a longer word", body + "Confidence: highest — read.", v1alpha1.ConfidenceUnstated},
		{"translated", body + "Confiance : élevée — lu.", v1alpha1.ConfidenceUnstated},
		{"a percentage", body + "Confidence: 90%", v1alpha1.ConfidenceUnstated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := StatedConfidence(tc.answer); got != tc.want {
				t.Errorf("StatedConfidence(%q) = %s, want %s", tc.answer, got, tc.want)
			}
		})
	}
}

func TestConfidenceCap(t *testing.T) {
	const (
		high   = "Root cause: x.\nConfidence: high — read."
		medium = "Root cause: x.\nConfidence: medium — inferred."
		entity = v1alpha1.EntityKey("service/billing-api")
	)
	alert := v1alpha1.AlertRecord{Name: "HighErrorRate", Namespace: "shop", Entity: entity}
	named := alert
	named.Cluster = "eu-1"
	spots := func(keys ...v1alpha1.EntityKey) []v1alpha1.BlindSpot {
		out := make([]v1alpha1.BlindSpot, 0, len(keys))
		for _, k := range keys {
			out = append(out, v1alpha1.BlindSpot{Entity: k, Reason: v1alpha1.BlindSpotNoAccess})
		}
		return out
	}
	for _, tc := range []struct {
		name       string
		answer     string
		alert      v1alpha1.AlertRecord
		spots      []v1alpha1.BlindSpot
		want       v1alpha1.Confidence
		wantCapped bool
	}{
		{"high, no blind spot", high, alert, nil, v1alpha1.ConfidenceHigh, false},
		{"high, another entity blind", high, alert, spots("service/orders-api", "environment/payments"), v1alpha1.ConfidenceHigh, false},
		{"high, its entity blind", high, alert, spots(entity), v1alpha1.ConfidenceMedium, true},
		{"high, its namespace blind", high, alert, spots("environment/shop"), v1alpha1.ConfidenceMedium, true},
		{"high, its namespace on a named cluster", high, named, spots("environment/eu-1.shop"), v1alpha1.ConfidenceMedium, true},
		{"high, same namespace on another cluster", high, named, spots("environment/shop"), v1alpha1.ConfidenceHigh, false},
		{"high, no entity and no namespace", high, v1alpha1.AlertRecord{Name: "X"}, spots("environment/shop"), v1alpha1.ConfidenceHigh, false},
		{"medium stays medium", medium, alert, spots(entity), v1alpha1.ConfidenceMedium, false},
		{"unstated stays unstated", "Root cause: x.", alert, spots(entity), v1alpha1.ConfidenceUnstated, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, capped := Confidence(tc.answer, tc.alert, tc.spots)
			if got != tc.want || capped != tc.wantCapped {
				t.Errorf("Confidence = %s, capped %t; want %s, capped %t", got, capped, tc.want, tc.wantCapped)
			}
		})
	}
}
