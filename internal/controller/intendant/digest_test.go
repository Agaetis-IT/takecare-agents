// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

const (
	review         = "review"
	billingService = "service/billing-api"
)

// digestSurvey is a completed survey of the demo with the e2e golden's
// three proposals; their entries hold a value that a digest must never
// show.
func digestSurvey() (*v1alpha1.TakeCareSurvey, []v1alpha1.Proposal) {
	const secret = "description: password=hunter2\n"
	ps := []v1alpha1.Proposal{
		{ID: "7ea96a023f5377bf", Action: v1alpha1.ProposalAdd, Key: "network/billing-api", Entry: "key: network/billing-api\n" + secret, Source: v1alpha1.ProposalFromImport},
		{ID: "200ea0648e2f8914", Action: v1alpha1.ProposalUpdate, Key: "runbook/proxy-timeout", Entry: "key: runbook/proxy-timeout\n" + secret,
			Evidence: []string{"runbooks_get"}, Source: v1alpha1.ProposalFromAgent},
		{ID: "c99d0a2b8b21007e", Action: v1alpha1.ProposalUpdate, Key: billingService, Entry: "key: " + billingService + "\n" + secret, Source: v1alpha1.ProposalFromImport},
	}
	s := &v1alpha1.TakeCareSurvey{Name: "e2e-first", Namespace: "takecare-system"}
	s.Spec.IntendantRef = v1alpha1.RoleRef{Kind: "TakeCareIntendant", Name: "intendant"}
	s.Status = v1alpha1.TakeCareSurveyStatus{
		MapRevision: "470725dee572", ProposalsConfigMap: "survey-e2e-first-proposals", Proposals: ps,
		ProposalCounts: &v1alpha1.ProposalCounts{Add: 1, Update: 2, Total: 3},
		Layers:         []v1alpha1.SurveyLayer{{Name: "identity", NotSeen: []v1alpha1.NotSeen{{Ref: "demo-apps", Reason: "no serviceaccounts"}}}},
	}
	s.Status.Phase = v1alpha1.RunCompleted
	return s, ps
}

func render(t *testing.T, d Digest) []byte {
	t.Helper()
	m := d.Message()
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{"text": m.Text, "blocks": m.Blocks}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// The digest of the e2e's survey: Block Kit blocks and the text the
// report-log adapter prints, keys and counts only.
func TestDigestGolden(t *testing.T) {
	s, ps := digestSurvey()
	got := render(t, NewDigest(s, ps))
	golden.Assert(t, "digest.json", got)
	if bytes.Contains(got, []byte("hunter2")) || bytes.Contains(got, []byte("runbooks_get")) {
		t.Errorf("the digest shows an entry or its evidence:\n%s", got)
	}
}

// Past 20 proposals, the keys stop at 20 and the rest is counted; the
// counts are the status's, over the full set.
func TestDigestBound(t *testing.T) {
	s, _ := digestSurvey()
	s.Status.Proposals = nil
	for i := range 25 {
		s.Status.Proposals = append(s.Status.Proposals, v1alpha1.Proposal{Action: v1alpha1.ProposalAdd, Key: v1alpha1.EntityKey(fmt.Sprintf("compute/w-%02d", i))})
	}
	s.Status.Proposals = s.Status.Proposals[:20] // the status keeps 20
	s.Status.ProposalCounts = &v1alpha1.ProposalCounts{Add: 25, Total: 25}
	d := NewDigest(s, nil)
	if len(d.Keys) != DigestKeys || d.More != 5 || d.Total() != 25 {
		t.Fatalf("keys %d, more %d, total %d; want 20, 5, 25", len(d.Keys), d.More, d.Total())
	}
	if !strings.Contains(d.Message().Text, "\nand 5 more\n") {
		t.Errorf("no \"and 5 more\" line:\n%s", d.Message().Text)
	}
	// Long keys: the keys' section stays under Block Kit's 3000.
	for i := range s.Status.Proposals {
		s.Status.Proposals[i].Key = v1alpha1.EntityKey("compute/" + strings.Repeat("a", 240) + fmt.Sprint(i))
	}
	d = NewDigest(s, nil)
	if n := len(d.keys()); n > 3000 || d.More != 25-len(d.Keys) {
		t.Errorf("keys section %d bytes, %d keys, more %d", n, len(d.Keys), d.More)
	}
	// A long Intendant name: the header stays within 150 characters.
	s.Spec.IntendantRef.Name = strings.Repeat("i", 200)
	if h := NewDigest(s, nil).header(); len(h) > headerLimit {
		t.Errorf("header of %d characters", len(h))
	}
}

// Slack markup cannot pass: the Map schema's key pattern excludes every
// markup character, and so does the digest's own check, which prints "?"
// for a key or a name that does not match.
func TestDigestNoMarkup(t *testing.T) {
	raw, err := os.ReadFile("../../../api/map/entry.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs struct {
			Key struct {
				Pattern string `json:"pattern"`
			} `json:"key"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil || schema.Defs.Key.Pattern == "" {
		t.Fatalf("the schema's key pattern: %v", err)
	}
	schemaKey := regexp.MustCompile(schema.Defs.Key.Pattern)
	for _, c := range []string{"<", ">", "&", "@", "*", "_", "~", "`", "|", " "} {
		key := "service/a" + c + "b"
		if schemaKey.MatchString(key) || safeKey.MatchString(key) {
			t.Errorf("a key with %q passes", c)
		}
	}
	s, ps := digestSurvey()
	s.Status.Proposals[0].Key = "service/<!channel>"
	s.Status.Proposals[1].Action = "<@U123>"
	s.Name, s.Status.MapRevision = "*bold*", "<https://evil|x>"
	got := string(render(t, NewDigest(s, ps)))
	for _, bad := range []string{"<!channel>", "<@U123>", "*bold*", "evil"} {
		if strings.Contains(got, bad) {
			t.Errorf("the digest prints %q:\n%s", bad, got)
		}
	}
	if !strings.Contains(got, "Add ?") || !strings.Contains(got, "? runbook/proxy-timeout") {
		t.Errorf("a key or an action that does not match is not printed as \"?\":\n%s", got)
	}
}

// The destination is the Doc's rule (spec 10 §6).
func TestDigestDestination(t *testing.T) {
	for _, tc := range []struct {
		level              v1alpha1.Level
		apprentice, practi string
		want, why          string
	}{
		{v1alpha1.LevelApprentice, "", onCall, "", reasonApprentice},
		{v1alpha1.LevelApprentice, review, onCall, review, ""},
		{v1alpha1.LevelPractitioner, review, onCall, onCall, ""},
		{v1alpha1.LevelPractitioner, review, "", "", reasonNoDest},
		{"", "", onCall, "", reasonApprentice},
	} {
		in := &v1alpha1.TakeCareIntendant{}
		in.Spec.Level, in.Spec.Report = tc.level, v1alpha1.ReportDestinations{Apprentice: tc.apprentice, Practitioner: tc.practi}
		if got, why := destination(in); got != tc.want || why != tc.why {
			t.Errorf("%s %q/%q: %q %q, want %q %q", tc.level, tc.apprentice, tc.practi, got, why, tc.want, tc.why)
		}
	}
}
