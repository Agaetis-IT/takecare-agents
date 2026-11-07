// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mapserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

const alertHigh = "HighErrorRate"

func query(namespace, workload, service, alert string) url.Values {
	return url.Values{"namespace": {namespace}, "workload": {workload}, "service": {service}, "alert": {alert}}
}

func TestResolve(t *testing.T) {
	const billing, demoApps = "billing-api", "demo-apps"
	for _, tc := range []struct {
		name, dir string
		query     url.Values
		code      int
		want      string // the key on a hit, the reason on a miss
	}{
		{"the alert's workload names the Deployment of the service and the compute: alertedBy decides", demoDir,
			query(demoApps, billing, billing, alertHigh), http.StatusOK, keyService},
		{"by the service label when the workload names nothing", demoDir,
			query(demoApps, "billing-api-7d9f", billing, alertHigh), http.StatusOK, keyService},
		{"two candidates, neither alerted by this alert", demoDir,
			query(demoApps, billing, "", "PodNotReady"), http.StatusNotFound, missAmbiguous},
		{"a proposed entry never resolves", demoDir,
			query(demoApps, "notifications-worker", "", alertHigh), http.StatusNotFound, missNone},
		{"another namespace", demoDir,
			query("ack-system", billing, "", alertHigh), http.StatusNotFound, missNone},
		{"two candidates without alertedBy are ambiguous", "testdata/ambiguous",
			query("shop", "orders", "", "HighLatency"), http.StatusNotFound, missAmbiguous},
		{"no namespace", demoDir, query("", billing, "", alertHigh), http.StatusBadRequest, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := New(Config{Dir: tc.dir, Logger: slog.New(slog.DiscardHandler), Now: func() time.Time { return loadedAt }})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/resolve?"+tc.query.Encode(), nil))
			if rec.Code != tc.code {
				t.Fatalf("code %d, want %d: %s", rec.Code, tc.code, rec.Body)
			}
			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			switch tc.code {
			case http.StatusOK:
				if got[argKey] != tc.want || got[keyRevision] == "" {
					t.Errorf("got %v, want key %s with a revision", got, tc.want)
				}
			case http.StatusNotFound:
				if got["reason"] != tc.want {
					t.Errorf("got %v, want reason %s", got, tc.want)
				}
			}
		})
	}
}
