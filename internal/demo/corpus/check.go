// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package corpus

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Check verifies what must hold for the scenario to work: a corpus that
// drowns or contradicts its own signal never reaches the cluster. The
// proxy-timeout facts the Doc has to put together:
//
//   - the deployment log shows ONE recent billing-api infrastructure change
//     that set the billing DBProxy's idleClientTimeout to 600 s;
//   - the past incident INC-2024-0312 links the proxy's idle timeout with the
//     pool's recycling (300 s against 600 s in 2024): a relation, not today's
//     numbers;
//   - RB-014 states the rule and its two traps (invisible under load, hidden
//     by a restart); RB-007, the memory runbook, points to it;
//   - the application's pool value (900 s) is never in the corpus: it is read
//     in the cluster, from the pod's banner and environment.
//
// The other scenarios stay neutral: no recent change on the services they
// break. Every date is at or before the incident, every identifier unique.
func Check(c Corpus, scenario string, incident time.Time) error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	checkSignal(c, scenario, incident, fail)
	checkNeutral(c, incident, fail)
	checkShape(c, incident, fail)
	return errors.Join(errs...)
}

type failFunc func(format string, args ...any)

// checkSignal checks the proxy-timeout facts.
func checkSignal(c Corpus, scenario string, incident time.Time, fail failFunc) {
	deployments := c[SourceDeployments]

	var proxy []Record
	for _, r := range recent(deployments, "billing-api", 24*time.Hour, incident) {
		if strings.Contains(r["diff"], "idleClientTimeout") {
			proxy = append(proxy, r)
		}
	}
	switch {
	case scenario != ScenarioProxyTimeout && len(proxy) > 0:
		fail("billing-api: no idleClientTimeout change outside proxy-timeout, found %v", ids(proxy))
	case scenario == ScenarioProxyTimeout && (len(proxy) != 1 || proxy[0]["type"] != "infrastructure" ||
		!strings.Contains(proxy[0]["diff"], "-> 600")):
		fail("proxy-timeout: want exactly one billing-api infrastructure change < 24 h setting idleClientTimeout to 600, found %v", ids(proxy))
	case scenario == ScenarioProxyTimeout && strings.Contains(proxy[0][keySummary]+proxy[0]["diff"], "900"):
		fail("proxy-timeout: the DBProxy change must not carry the pool's value (900 s)")
	}

	incidents := byID(c[SourceIncidents])
	past := incidents["INC-2024-0312"]
	if !strings.Contains(past["root_cause"], "300 s") || !strings.Contains(past["root_cause"], "600 s") {
		fail("proxy-timeout: INC-2024-0312 must link the proxy's 300 s with the pool's 600 s")
	}
	if strings.Contains(past["root_cause"], "900") {
		fail("proxy-timeout: INC-2024-0312 must not carry the pool's current value (900 s)")
	}
	runbooks := byID(c[SourceRunbooks])
	rb14 := runbooks["RB-014"]
	for _, want := range []string{"idle", "pool"} {
		if !strings.Contains(rb14["tags"], want) {
			fail("proxy-timeout: RB-014 lost the signal %q", want)
		}
	}
	for _, want := range []string{"under load", "restart"} {
		if !strings.Contains(strings.ToLower(rb14["warning"]), want) {
			fail("proxy-timeout: RB-014's warning must cover %q", want)
		}
	}
	if !strings.Contains(runbooks["RB-007"]["warning"], "RB-014") {
		fail("proxy-timeout: RB-007's warning must point to RB-014")
	}
}

// ongoingTitle is the vendor's false lead: an ongoing storage degradation in
// the region, true and unrelated.
const ongoingTitle = "Degraded performance of EBS gp3 volumes - eu-west-3a"

// checkNeutral checks that the other scenarios leave no live change and that
// the vendor's false lead is the only ongoing bulletin.
func checkNeutral(c Corpus, incident time.Time, fail failFunc) {
	for _, service := range []string{"orders-api", "notifications-worker"} {
		if r := recent(c[SourceDeployments], service, 6*time.Hour, incident); len(r) > 0 {
			fail("%s: no change < 6 h, its scenario is not carried; found %v", service, ids(r))
		}
	}
	var ongoing []Record
	for _, r := range c[SourceVendor] {
		if r[keyStatus] == "ongoing" {
			ongoing = append(ongoing, r)
		}
	}
	if len(ongoing) != 1 || ongoing[0][keyTitle] != ongoingTitle {
		fail("vendor: want exactly one ongoing bulletin, %q, found %v", ongoingTitle, ids(ongoing))
	}
}

// checkShape checks identifiers and dates.
func checkShape(c Corpus, incident time.Time, fail failFunc) {
	for _, source := range Sources {
		seen := map[string]bool{}
		for _, r := range c[source] {
			if seen[r["id"]] {
				fail("%s: duplicate identifier %s", source, r["id"])
			}
			seen[r["id"]] = true
			if d, ok := r[keyDate]; ok {
				if t, err := parseDate(d); err != nil {
					fail("%s: %s: %w", source, r["id"], err)
				} else if t.After(incident) {
					fail("%s: %s dated after the incident (%s)", source, r["id"], d)
				}
			}
		}
	}
}

// recent is the changes of a service within a window before the incident.
func recent(deployments []Record, service string, window time.Duration, incident time.Time) []Record {
	var out []Record
	for _, r := range deployments {
		if t, err := parseDate(r[keyDate]); err == nil && r[keyService] == service && incident.Sub(t) < window {
			out = append(out, r)
		}
	}
	return out
}

func byID(records []Record) map[string]Record {
	m := make(map[string]Record, len(records))
	for _, r := range records {
		m[r["id"]] = r
	}
	return m
}

func ids(records []Record) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r["id"])
	}
	return out
}
