// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package alerts

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/agaetis-it/takecare-agents/internal/golden"
)

// One webhook of three alerts gives three records, with their state and
// silent flag beside them.
func TestAlertmanagerAdapter(t *testing.T) {
	raw, err := os.ReadFile("testdata/alertmanager-payload.json")
	if err != nil {
		t.Fatal(err)
	}
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	type view struct {
		Record any    `json:"record"`
		State  string `json:"state"`
		Silent bool   `json:"silent"`
	}
	records := fromAlertmanager(p)
	got := make([]view, 0, len(records))
	for _, in := range records {
		got = append(got, view{in.record, in.state, in.silent})
	}
	out, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "alertmanager-records.json", append(out, '\n'))
}
