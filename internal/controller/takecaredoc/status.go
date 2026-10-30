// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package takecaredoc

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// status collects one reconciliation's verdicts before they are written.
type status struct {
	generation   int64
	level        v1alpha1.Level
	pendingSince *metav1.Time
	conditions   []metav1.Condition
}

func (s *status) set(condType string, ok bool, reason, message string) {
	c := metav1.Condition{Type: condType, Status: metav1.ConditionFalse, Reason: reason, Message: message, ObservedGeneration: s.generation}
	if ok {
		c.Status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&s.conditions, c)
}

func (s *status) isTrue(condType string) bool {
	return meta.IsStatusConditionTrue(s.conditions, condType)
}

func (s *status) reason(condType string) string {
	if c := meta.FindStatusCondition(s.conditions, condType); c != nil {
		return c.Reason
	}
	return ""
}

// ready sets Ready from the other conditions. A connector known but
// unavailable degrades the Doc without stopping it.
func (s *status) ready(suspended bool) {
	if suspended {
		s.set(v1alpha1.ConditionReady, false, ReasonSuspended, "spec.suspend is set: the Doc handles nothing")
		return
	}
	connectorsOK := s.isTrue(v1alpha1.ConditionConnectorsReady) ||
		(s.reason(v1alpha1.ConditionConnectorsReady) == ReasonConnectorNotReady && s.isTrue(v1alpha1.ConditionEngineReady))
	for _, c := range []struct {
		ok   bool
		kind string
	}{
		{connectorsOK, v1alpha1.ConditionConnectorsReady},
		{s.isTrue(v1alpha1.ConditionModelResolved), v1alpha1.ConditionModelResolved},
		{s.isTrue(v1alpha1.ConditionEngineReady), v1alpha1.ConditionEngineReady},
	} {
		if !c.ok {
			msg := c.kind + " is False"
			if found := meta.FindStatusCondition(s.conditions, c.kind); found != nil {
				msg = c.kind + ": " + found.Message
			}
			s.set(v1alpha1.ConditionReady, false, ReasonNotReady, msg)
			return
		}
	}
	s.set(v1alpha1.ConditionReady, true, ReasonReady, "the Doc's agent answers through the gateway with its tools")
}
