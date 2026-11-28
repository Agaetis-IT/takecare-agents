// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"testing"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// ScheduleValid and the next maintenance (spec 12 §2): weekly by default,
// none plans nothing, a time zone or a bad expression is refused.
func TestSchedule(t *testing.T) {
	// A Wednesday.
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, schedule, reason string
		valid                  bool
		next                   time.Time
	}{
		{"the default, Monday 05:00 UTC", "", ReasonValid, true, time.Date(2026, 11, 30, 5, 0, 0, 0, time.UTC)},
		{"daily", "30 2 * * *", ReasonValid, true, time.Date(2026, 11, 26, 2, 30, 0, 0, time.UTC)},
		{"none", v1alpha1.ScheduleNone, ReasonValid, true, time.Time{}},
		{"a time zone", "CRON_TZ=Europe/Paris 0 5 * * 1", ReasonScheduleTimeZone, false, time.Time{}},
		{"six fields", "0 0 5 * * 1", ReasonInvalidSchedule, false, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &v1alpha1.TakeCareMecano{}
			m.Spec.Schedule = tc.schedule
			st := &status{now: now}
			next := schedule(m, st, now)
			if st.isTrue(ConditionScheduleValid) != tc.valid || st.reason(ConditionScheduleValid) != tc.reason {
				t.Errorf("ScheduleValid %v %s, want %v %s", st.isTrue(ConditionScheduleValid), st.reason(ConditionScheduleValid), tc.valid, tc.reason)
			}
			switch {
			case tc.next.IsZero() && next != nil:
				t.Errorf("next = %v, want none", next)
			case !tc.next.IsZero() && (next == nil || !next.Time.Equal(tc.next)):
				t.Errorf("next = %v, want %v", next, tc.next)
			}
		})
	}
}
