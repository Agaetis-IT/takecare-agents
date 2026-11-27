// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package demo

import (
	"time"

	"github.com/agaetis-it/takecare-agents/internal/demoapp"
)

// HighErrorRate as the demo's Prometheus evaluates it (values-demo.yaml):
// the 504 share of rate(http_requests_total[1m]) above 2 %, for 30 s, rules
// evaluated and targets scraped every 15 s. The cadence test checks these
// against the chart, so that a change of the rule alone fails it.
const (
	ruleWindow    = time.Minute
	ruleFor       = 30 * time.Second
	ruleThreshold = 0.02
	ruleInterval  = 15 * time.Second
)

// alertDelay is when HighErrorRate fires after the leaky pod starts, at the
// earliest and at the latest.
//
// Do not add the window's lag to the instantaneous crossing: it runs the
// other way here. The application's 504 counter is total(t) × ratio(p(t)),
// the current ratio applied to every request served since the start (as in
// the spike), so over [t-1m, t] the share rate() sees is
//
//	(t·ratio(t) − (t−60)·ratio(t−60)) / 60  ≈  ratio(t) + t·ratio'(t)
//
// which runs ahead of ratio(t). With ratio = 0.001 + 0.06·p² and rampMinutes
// 4, ratio(t) crosses 2 % at 136 s (p ≈ 0.5627) but the windowed share
// crosses it at about 107 s. The alert fires once that has held for `for`
// (30 s), and the scrape and evaluation phases add up to two intervals:
// 137 to 167 s after the pod starts, about 2:47 to 3:17 from the annotation
// with Recreate's restart; the spike measured 3:10 and 3:15. alertDelay
// simulates the windowed share second by second from demoapp's ErrorRatio,
// so a change of the ratio's defaults moves the window, and TestCadence
// pins it.
func alertDelay(ramp time.Duration) (from, to time.Duration) {
	cfg := demoapp.DefaultConfig()
	cfg.Mode = demoapp.ModeLeaky
	errors := func(t time.Duration) float64 { // 504s served by t, per req/s
		if t <= 0 {
			return 0
		}
		p := min(1, t.Seconds()/ramp.Seconds())
		return t.Seconds() * cfg.ErrorRatio(p)
	}
	crossing := ruleWindow
	for ; crossing < ruleWindow+2*ramp; crossing += time.Second {
		if (errors(crossing)-errors(crossing-ruleWindow))/ruleWindow.Seconds() > ruleThreshold {
			break
		}
	}
	return crossing + ruleFor, crossing + ruleFor + 2*ruleInterval
}

// PodNotReady as the demo's Prometheus evaluates it (values-demo.yaml):
// kube_pod_status_ready false for 5 minutes. bad-config-rollout's new pod
// and missing-secret-key's restarting one are never ready, from their
// creation on; PodNotReady is the first alert of both (spike 001 measured
// 5 min 36 s to 5 min 41 s for bad-config-rollout).
const (
	notReadyFor = 5 * time.Minute
	// notReadyLag is the readiness probe's failures (3 × 10 s), the pod's
	// start, and the scrape and evaluation phases (2 × 15 s): the latest
	// the alert fires after its `for`.
	notReadyLag = 90 * time.Second
)

// notReadyDelay is when PodNotReady fires after the broken pod is created,
// at the earliest and at the latest; the ramp plays no part.
func notReadyDelay(time.Duration) (from, to time.Duration) {
	return notReadyFor, notReadyFor + notReadyLag
}
