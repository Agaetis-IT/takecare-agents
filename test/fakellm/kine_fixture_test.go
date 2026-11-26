// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package fakellm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/kine"
)

// kineTools are the tools the demo Kiné's agent is offered at
// Practitioner (demo/manifests/kine.yaml, patched by hack/e2e.sh): the
// corpus's three tools of each of its four sources, the history's two
// lists and the two content gets listed in contentTools, and ask_user,
// which kagent gives every agent.
var kineTools = []string{
	"incidents_search", "incidents_get", "incidents_recent",
	"runbooks_search", "runbooks_get", "runbooks_recent",
	"support_search", "support_get", "support_recent",
	"deployments_search", "deployments_get", "deployments_recent",
	"investigations_list", "reviews_list", "investigation_get", "review_get",
	"ask_user",
}

// The hand-written e2e fixture of the Kiné's review (spec 11 §13): its one
// exchange is the first call of the Kiné's conversation, its offered hash
// is kineTools', and its answer is read by the Kiné's own parser and
// recorded at Practitioner with the recurrence, the earlier undone
// follow-up, a new action and a post-mortem the e2e's golden shows.
func TestKineReviewFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "e2e", "kine-review.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f Fixtures
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.Source != "hand-written" || len(f.Exchanges) != 1 {
		t.Fatalf("source %q, %d exchanges: want hand-written, 1", f.Source, len(f.Exchanges))
	}
	req := Request{Model: "e2e-doc", Messages: []Message{{Role: "system"}, {Role: "user"}}}
	for _, name := range kineTools {
		var tool Tool
		tool.Function.Name = name
		req.Tools = append(req.Tools, tool)
	}
	if got, want := f.Exchanges[0].Fingerprint.Key(), Of(&req).Key(); got != want {
		t.Errorf("fingerprint %s, want %s (the Kiné's first call)", got, want)
	}
	var resp struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(f.Exchanges[0].Response, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].FinishReason != "stop" {
		t.Fatalf("choices %+v: want one, finished", resp.Choices)
	}
	answer, err := kine.Parse(resp.Choices[0].Message.Content)
	if err != nil {
		t.Fatal(err)
	}
	rec := kine.Record(answer, "review", v1alpha1.LevelPractitioner, nil)
	if rec.Problem == nil || len(rec.Problem.Recurrences) != 1 || rec.Problem.Recurrences[0].Record != "INC-2024-0312" || rec.Problem.Recurrences[0].Date == nil {
		t.Errorf("problem %+v: want the recurrence INC-2024-0312 with its date", rec.Problem)
	}
	ids := make([]string, 0, len(rec.Actions))
	for _, a := range rec.Actions {
		ids = append(ids, a.ID+" "+string(a.State))
	}
	if want := []string{"INC-2024-0312/3 Open", "INC-2024-0312/1 Done", "review-1 Proposed"}; !slices.Equal(ids, want) {
		t.Errorf("actions %v, want %v", ids, want)
	}
	if rec.Counts != (v1alpha1.ActionCounts{Total: 3, Open: 2}) || rec.PostMortem == "" || len(rec.BlindSpots) != 0 {
		t.Errorf("counts %+v, post-mortem %d bytes, blind spots %v", rec.Counts, len(rec.PostMortem), rec.BlindSpots)
	}
}
