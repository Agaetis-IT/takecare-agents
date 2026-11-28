// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package usage

import (
	"context"
	"time"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// Meter measures one run on a role's own model route: one run at a time
// per role, so it counts no other run's calls, except one the engine still
// runs after it did not finish: then it is approximate (spec 02 §5, spec
// 11 §6).
type Meter struct {
	source      Source
	start       Counters
	pricing     *v1alpha1.Pricing
	approximate bool
}

// StartMeter reads the start counters of src; it returns why the usage is
// unknown when they cannot be read.
func StartMeter(ctx context.Context, src Source, pricing *v1alpha1.Pricing, approximate bool) (*Meter, string) {
	m := &Meter{source: src, pricing: pricing, approximate: approximate}
	var err error
	if m.start, err = src.Snapshot(ctx); err != nil {
		return nil, "the start snapshot: " + err.Error()
	}
	return m, ""
}

// End reads the counters one scrape interval after the run: the usage, or
// why it is unknown.
func (m *Meter) End(ctx context.Context, wait time.Duration) (*v1alpha1.Usage, string) {
	select {
	case <-ctx.Done():
		return nil, "the operator stopped before the usage was read"
	case <-time.After(wait):
	}
	end, err := m.source.Snapshot(ctx)
	if err != nil {
		return nil, "the end snapshot: " + err.Error()
	}
	d, err := Delta(m.start, end)
	if err != nil {
		return nil, err.Error()
	}
	u, err := Record(d, m.pricing, m.approximate)
	if err != nil {
		return nil, "not priced: " + err.Error()
	}
	return &u, ""
}
