// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// ScheduleValid and its reasons (spec 12 §2), as the Kiné's.
const (
	ConditionScheduleValid = "ScheduleValid"
	ReasonValid            = "Valid"
	ReasonInvalidSchedule  = "InvalidSchedule"
	ReasonScheduleTimeZone = "ScheduleTimeZone"
)

// DefaultSchedule is spec.schedule's default: weekly, Monday 05:00 UTC.
const DefaultSchedule = "0 5 * * 1"

// cronParser reads a standard five-field schedule, in UTC.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// schedule sets ScheduleValid and returns the next scheduled maintenance's
// time after now, nil when the schedule is refused or says none.
func schedule(m *v1alpha1.TakeCareMecano, st *status, now time.Time) *metav1.Time {
	spec := cmp.Or(m.Spec.Schedule, DefaultSchedule)
	if spec == v1alpha1.ScheduleNone {
		st.set(ConditionScheduleValid, true, ReasonValid, "spec.schedule none: no scheduled maintenance")
		return nil
	}
	if strings.Contains(spec, "TZ=") {
		// Stored before the API refused it (the CEL rule says the same).
		st.set(ConditionScheduleValid, false, ReasonScheduleTimeZone, v1alpha1.MessageScheduleUTC)
		return nil
	}
	sched, err := cronParser.Parse(spec)
	if err != nil {
		st.set(ConditionScheduleValid, false, ReasonInvalidSchedule, fmt.Sprintf("spec.schedule %q: %v", spec, err))
		return nil
	}
	st.set(ConditionScheduleValid, true, ReasonValid, "spec.schedule "+spec+", in UTC")
	next := metav1.NewTime(sched.Next(now.UTC()))
	return &next
}
