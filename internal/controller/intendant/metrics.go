// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"errors"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
)

// SurveyMetrics are what awaits review now (spec 10 §10): gauges set from
// each Intendant's last completed survey, never counters, since the same
// proposals come back at every survey until a human merges them.
type SurveyMetrics struct {
	Proposals *prometheus.GaugeVec
	NotSeen   *prometheus.GaugeVec
}

// NewSurveyMetrics registers the survey gauges, or returns the ones
// already registered.
func NewSurveyMetrics(reg prometheus.Registerer) (*SurveyMetrics, error) {
	proposals, err := gauge(reg, "takecare_survey_proposals",
		"Proposals of each Intendant's last completed survey, by action and source: what awaits review now. One series per Intendant, a few per cluster.",
		"intendant", "action", "source")
	if err != nil {
		return nil, err
	}
	notSeen, err := gauge(reg, "takecare_survey_not_seen",
		"What each Intendant's last completed survey could not read, by layer. One series per Intendant, a few per cluster.",
		"intendant", "layer")
	if err != nil {
		return nil, err
	}
	return &SurveyMetrics{Proposals: proposals, NotSeen: notSeen}, nil
}

func gauge(reg prometheus.Registerer, name, help string, labels ...string) (*prometheus.GaugeVec, error) {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
	if err := reg.Register(g); err != nil {
		if already, ok := errors.AsType[prometheus.AlreadyRegisteredError](err); ok {
			if existing, ok := already.ExistingCollector.(*prometheus.GaugeVec); ok {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("register %s: %w", name, err)
	}
	return g, nil
}

// Record replaces an Intendant's series with its last completed survey's.
func (m *SurveyMetrics) Record(intendant string, res discovery.Result) {
	if m == nil {
		return
	}
	m.Forget(intendant)
	for _, action := range []v1alpha1.ProposalAction{v1alpha1.ProposalAdd, v1alpha1.ProposalUpdate, v1alpha1.ProposalRemove} {
		for _, source := range []v1alpha1.ProposalSource{v1alpha1.ProposalFromImport, v1alpha1.ProposalFromAgent} {
			n := 0
			for _, p := range res.Proposals {
				if p.Action == action && p.Source == source {
					n++
				}
			}
			m.Proposals.WithLabelValues(intendant, string(action), string(source)).Set(float64(n))
		}
	}
	for _, l := range res.Layers {
		m.NotSeen.WithLabelValues(intendant, l.Name).Set(float64(len(l.NotSeen)))
	}
}

// Forget removes an Intendant's series, when it goes.
func (m *SurveyMetrics) Forget(intendant string) {
	if m == nil {
		return
	}
	m.Proposals.DeletePartialMatch(prometheus.Labels{"intendant": intendant})
	m.NotSeen.DeletePartialMatch(prometheus.Labels{"intendant": intendant})
}
