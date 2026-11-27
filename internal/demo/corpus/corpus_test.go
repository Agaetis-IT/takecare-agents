// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package corpus

import (
	"strings"
	"testing"
	"time"
)

var incident = time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)

// lastHour is an hour before the incident, as a record's date.
const lastHour = "2026-10-30T02:00:00Z"

func load(t *testing.T, scenario string) Corpus {
	t.Helper()
	c, err := Seeds(scenario, incident)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func find(c Corpus, source, id string) Record {
	for _, r := range c[source] {
		if r["id"] == id {
			return r
		}
	}
	return nil
}

// The seeds hold every scenario's facts, and only the composed scenario's
// live change: the corpus with no scenario holds none.
func TestSeedsPassTheirInvariants(t *testing.T) {
	for _, scenario := range append([]string{ScenarioNone}, Scenarios...) {
		t.Run("scenario="+scenario, func(t *testing.T) {
			if err := Check(load(t, scenario), scenario, incident); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestLiveChangeFollowsTheScenario(t *testing.T) {
	live := []struct{ owner, id string }{
		{ScenarioProxyTimeout, "CI-SEED-INFRA"},
		{ScenarioBadConfigRollout, "CI-SEED-ORDERS-390"},
		{ScenarioMissingSecretKey, "CI-SEED-NOTIF-200"},
	}
	for _, scenario := range append([]string{ScenarioNone}, Scenarios...) {
		c := load(t, scenario)
		for _, l := range live {
			owner, id := l.owner, l.id
			r := find(c, "deployments", id)
			switch {
			case owner == scenario && r == nil:
				t.Errorf("%s without its live change %s", scenario, id)
			case owner != scenario && r != nil:
				t.Errorf("%s holds %s's live change %s", scenario, owner, id)
			case r != nil && r["scenario"] != "":
				t.Errorf("%s: the scenario field reaches the output", id)
			}
		}
	}
	if _, err := Seeds("disk-full", incident); err == nil {
		t.Error("a scenario the demo does not carry was composed")
	}
}

// Markers become UTC dates before the incident; static dates stay.
func TestDates(t *testing.T) {
	c := load(t, ScenarioProxyTimeout)
	for _, tc := range []struct{ source, id, want string }{
		{"deployments", "CI-SEED-INFRA", "2026-10-29T14:48:00Z"}, // @TS732@
		{"vendor", "AWS-SEED-RDS", "2026-10-24T03:00:00Z"},       // @DAY6@
		{"incidents", "INC-2024-0312", "2024-03-12"},
	} {
		if got := find(c, tc.source, tc.id)["date"]; got != tc.want {
			t.Errorf("%s %s: date %q, want %q", tc.source, tc.id, got, tc.want)
		}
	}
	for _, source := range Sources {
		for _, r := range c[source] {
			if strings.Contains(r["date"], "@") {
				t.Errorf("%s %s: unresolved marker %q", source, r["id"], r["date"])
			}
		}
	}
}

// Each broken fact is named: the invariants bite.
func TestCheckNamesWhatBreaks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(Corpus)
		want   string
	}{
		{"no DBProxy change", func(c Corpus) { find(c, "deployments", "CI-SEED-INFRA")["diff"] = "" }, "exactly one billing-api"},
		{"DBProxy value changed", func(c Corpus) {
			find(c, "deployments", "CI-SEED-INFRA")["diff"] = "DBProxy/billing-db-proxy: idleClientTimeout: -> 1200"
		}, "exactly one billing-api"},
		{"pool value told", func(c Corpus) { find(c, "deployments", "CI-SEED-INFRA")["summary"] += " Pool: 900 s." }, "900 s"},
		{"past incident without its relation", func(c Corpus) { find(c, "incidents", "INC-2024-0312")["root_cause"] = "pool" }, "300 s with the pool's 600 s"},
		{"past incident with today's value", func(c Corpus) { find(c, "incidents", "INC-2024-0312")["root_cause"] += " 900" }, "current value"},
		{"RB-014 without its trap", func(c Corpus) { find(c, "runbooks", "RB-014")["warning"] = "careful" }, `"under load"`},
		{"RB-007 not pointing to RB-014", func(c Corpus) { find(c, "runbooks", "RB-007")["warning"] = "" }, "point to RB-014"},
		{"another scenario's live change", func(c Corpus) {
			c["deployments"] = append(c["deployments"], Record{"id": "X", keyService: "orders-api", keyDate: lastHour})
		}, "orders-api: no change < 6 h outside bad-config-rollout"},
		{"a release of notifications-worker", func(c Corpus) {
			c["deployments"] = append(c["deployments"], Record{"id": "X", keyService: "notifications-worker", keyDate: lastHour})
		}, "notifications-worker: no change < 6 h outside missing-secret-key"},
		{"two ongoing bulletins", func(c Corpus) { find(c, "vendor", "AWS-SEED-EC2")["status"] = "ongoing" }, "exactly one ongoing"},
		{"a date in the future", func(c Corpus) { find(c, "support", "SUP-4471")["date"] = "2026-10-30T03:01:00Z" }, "after the incident"},
		{"a duplicate", func(c Corpus) { c["runbooks"] = append(c["runbooks"], find(c, "runbooks", "RB-003")) }, "duplicate identifier RB-003"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := load(t, ScenarioProxyTimeout)
			tc.mutate(c)
			err := Check(c, ScenarioProxyTimeout, incident)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Check = %v, want an error naming %q", err, tc.want)
			}
		})
	}
}

// Without the scenario, a DBProxy change on billing-api is a contradiction.
func TestCheckRefusesTheChangeOutsideTheScenario(t *testing.T) {
	err := Check(load(t, ScenarioProxyTimeout), ScenarioNone, incident)
	if err == nil || !strings.Contains(err.Error(), "outside proxy-timeout") {
		t.Errorf("Check = %v", err)
	}
}

// The two ported scenarios' facts bite too.
func TestCheckNamesWhatBreaksInThePortedScenarios(t *testing.T) {
	for _, tc := range []struct {
		name, scenario string
		mutate         func(Corpus)
		want           string
	}{
		{"the rollout on another host", ScenarioBadConfigRollout, func(c Corpus) {
			find(c, "deployments", "CI-SEED-ORDERS-390")["diff"] = "DB_HOST: orders-db -> orders-db-replica"
		}, "moving DB_HOST to orders-db-primary"},
		{"a second orders-api change", ScenarioBadConfigRollout, func(c Corpus) {
			c["deployments"] = append(c["deployments"], Record{"id": "X", keyService: "orders-api", keyDate: lastHour})
		}, "exactly one orders-api configmap change"},
		{"the split already done", ScenarioBadConfigRollout, func(c Corpus) { find(c, "deployments", "CI-SEED-ORDERS-SPLIT")["status"] = "success" }, "only scheduled"},
		{"RB-021 without readiness", ScenarioBadConfigRollout, func(c Corpus) { find(c, "runbooks", "RB-021")["tags"] = "pods" }, `"readiness"`},
		{"the release without the new key", ScenarioMissingSecretKey, func(c Corpus) {
			find(c, "deployments", "CI-SEED-NOTIF-200")["summary"] = "Major version."
		}, "naming SMTP_PASSWORD"},
		{"the rotation telling the answer", ScenarioMissingSecretKey, func(c Corpus) {
			find(c, "deployments", "CI-SEED-NOTIF-ROT")["summary"] += " SMTP_PASSWORD is not a key of it."
		}, "never SMTP_PASSWORD"},
		{"RB-026 without secret", ScenarioMissingSecretKey, func(c Corpus) { find(c, "runbooks", "RB-026")["tags"] = "restart" }, `"secret"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := load(t, tc.scenario)
			tc.mutate(c)
			err := Check(c, tc.scenario, incident)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Check = %v, want an error naming %q", err, tc.want)
			}
		})
	}
}
