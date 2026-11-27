// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package doc

import (
	"regexp"
	"strings"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/coverage"
)

// confidenceLine is the last line the message asks for: "Confidence:",
// one of three fixed English tokens, then the reason. Markdown emphasis
// around the label or the token is tolerated, a translated token is not.
var confidenceLine = regexp.MustCompile(`(?i)^confidence[*_]*\s*:[*_\s]*(high|medium|low)\b`)

// StatedConfidence reads the confidence the Doc stated on the last
// non-empty line of its answer; Unstated when that line states none.
func StatedConfidence(answer string) v1alpha1.Confidence {
	lines := strings.Split(strings.TrimSpace(answer), "\n")
	last := strings.TrimLeft(strings.TrimSpace(lines[len(lines)-1]), "*_>-•· ")
	m := confidenceLine.FindStringSubmatch(last)
	if m == nil {
		return v1alpha1.ConfidenceUnstated
	}
	switch strings.ToLower(m[1]) {
	case "high":
		return v1alpha1.ConfidenceHigh
	case "medium":
		return v1alpha1.ConfidenceMedium
	default:
		return v1alpha1.ConfidenceLow
	}
}

// Confidence is the confidence recorded for an investigation's answer: the
// stated one, except that a stated High is capped to Medium when the alert
// is in the Doc's blind spots, by its Map entry or its namespace (the Doc
// could not read everything the alert is about).
func Confidence(answer string, alert v1alpha1.AlertRecord, blindSpots []v1alpha1.BlindSpot) (level v1alpha1.Confidence, capped bool) {
	level = StatedConfidence(answer)
	if level != v1alpha1.ConfidenceHigh || !InBlindSpots(alert, blindSpots) {
		return level, false
	}
	return v1alpha1.ConfidenceMedium, true
}

// InBlindSpots says whether a blind spot names the alert's Map entry or
// the environment entry of its namespace.
func InBlindSpots(alert v1alpha1.AlertRecord, blindSpots []v1alpha1.BlindSpot) bool {
	var place v1alpha1.EntityKey
	if alert.Namespace != "" {
		place = coverage.Place{Cluster: alert.Cluster, Namespace: v1alpha1.Namespace(alert.Namespace)}.Key()
	}
	for _, b := range blindSpots {
		if (alert.Entity != "" && b.Entity == alert.Entity) || (place != "" && b.Entity == place) {
			return true
		}
	}
	return false
}
