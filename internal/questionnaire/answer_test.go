// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package questionnaire

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// Credential-shaped samples are assembled at run time, so that no literal
// in this file has the shape the repository's secret scanner looks for.
func samples() map[string]string {
	sum := sha256.Sum256([]byte("takecare questionnaire sample"))
	b64 := base64.RawURLEncoding.EncodeToString(sum[:]) // 43 characters
	alnum := strings.NewReplacer("-", "x", "_", "y").Replace(b64)
	return map[string]string{
		"private key":    "-----BEGIN " + "OPENSSH PRIVATE" + " KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA",
		"rsa key":        "-----BEGIN " + "RSA PRIVATE" + " KEY-----",
		"github":         "gh" + "p_" + alnum[:36],
		"github fine":    "github" + "_pat_" + alnum,
		"gitlab":         "gl" + "pat-" + alnum[:20],
		"slack bot":      "xo" + "xb-" + "1234567890-" + alnum[:24],
		"slack webhook":  "https://hooks.slack" + ".com/services/" + alnum,
		"anthropic":      "sk-" + "ant-" + alnum,
		"openai":         "sk-" + "proj-" + alnum,
		"aws":            "AK" + "IA" + strings.ToUpper(alnum[:16]),
		"gcp":            "AI" + "za" + alnum[:35],
		"jwt":            "ey" + "J" + alnum[:20] + ".ey" + "J" + alnum[20:40] + "." + alnum[:10],
		"url password":   "https://deploy:" + alnum[:12] + "@git.example.org/map.git",
		"high entropy":   b64,
		"in a sentence":  "the key is " + alnum + " today",
		"stripe":         "sk" + "_live_" + alnum[:24],
		"vault":          "hv" + "s." + alnum,
		"huggingface":    "h" + "f_" + strings.ToLower(alnum[:34]),
		"npm":            "np" + "m_" + alnum[:36],
		"digitalocean":   "do" + "p_v1_" + hex.EncodeToString(sum[:]),
		"age":            "AGE-SECRET" + "-KEY-1" + strings.ToUpper(alnum),
		"sendgrid":       "S" + "G." + alnum + "." + alnum,
		"pypi":           "pypi-" + "AgEIcHlwaS5vcmc" + alnum,
		"shopify":        "shp" + "at_" + hex.EncodeToString(sum[:16]),
		"databricks":     "da" + "pi" + hex.EncodeToString(sum[:16]),
		"grafana":        "gl" + "c_" + alnum,
		"slack app":      "xa" + "pp-1-" + strings.ToUpper(alnum[:10]) + "-123-" + strings.ToLower(alnum[:20]),
		"gitlab deploy":  "gl" + "dt-" + alnum[:20],
		"a url password": "postgres://user:" + alnum[:10] + "@db:5432/x",
	}
}

func TestCheckCredential(t *testing.T) {
	for name, s := range samples() {
		t.Run("refused "+name, func(t *testing.T) {
			err := CheckCredential(s)
			if !errors.Is(err, ErrCredential) {
				t.Fatalf("got %v, want ErrCredential", err)
			}
			if strings.Contains(err.Error(), s) || strings.Contains(err.Error(), s[len(s)-8:]) {
				t.Errorf("the refusal echoes the answer: %v", err)
			}
		})
	}
	// Factual answers the built-in questions expect.
	for _, s := range []string{
		"", "takecare-system", "payments-production-eu-west-3-blue", "#oncall-payments",
		"monitoring/kube-prometheus-stack-prometheus", "https://github.com/example/platform-map.git",
		"git@github.com:example/platform-map.git", "main", "map", "claude-sonnet-4-5", "gpt-4.1-mini",
		"secrets/map-deploy-key", "anthropic-api-key", "https://runbooks.example.org/mcp",
		hex.EncodeToString(make([]byte, 20)) + "0123456789abcdef01234567", // a hex digest
		"0123456789abcdef0123456789abcdef01234567",                        // a commit
		"The ghp_ prefix names a GitHub token", "sk-ant- is a prefix", "PrometheusAlertmanager2026Edition",
		"team-a, team-b, team-c, team-d", "arn:aws:iam::123456789012:role/takecare-reader",
	} {
		if err := CheckCredential(s); err != nil {
			t.Errorf("%q refused: %v", s, err)
		}
	}
}

// Random tokens of the length providers issue are refused; camel case
// names with digits, which a person types, are not (above).
func TestCheckCredentialRandom(t *testing.T) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // a seeded sample, not a secret
	for _, n := range []int{24, 32, 40} {
		missed := 0
		for range 1000 {
			b := make([]byte, n)
			for i := range b {
				b[i] = alphabet[rng.IntN(len(alphabet))]
			}
			if CheckCredential(string(b)) == nil {
				missed++
			}
		}
		if missed > 0 {
			t.Errorf("length %d: %d random tokens in 1000 accepted", n, missed)
		}
	}
}

func TestCheckAnswer(t *testing.T) {
	set, err := Load([]Source{builtin}, []Source{{Name: fileOncall, Data: []byte(valid)}})
	if err != nil {
		t.Fatal(err)
	}
	q := set.Question
	token := samples()["github"]
	cases := []struct {
		q    *Question
		a    Answer
		want string // "" when accepted
	}{
		{q("oncall.channel"), Answer{Value: chanPayments}, ""},
		{q("oncall.channel"), Answer{Value: "payments"}, "oncall.channel: does not match ^#[a-z0-9-]{1,79}$"},
		{q("oncall.channel"), Answer{Unknown: true}, ""},
		{q("oncall.channel"), Answer{Value: true}, "oncall.channel: a yes or no answer to a text question"},
		{q("oncall.notes"), Answer{Value: "use " + token}, "oncall.notes: the answer holds a token prefix: " + ErrCredential.Error()},
		{q(idReportKind), Answer{Value: slack}, ""},
		{q(idReportKind), Answer{Value: "Teams"}, "report.kind: not one of the choices [Slack log]"},
		{q(idEnvs), Answer{Value: []string{envShop, "pay"}}, ""},
		{q(idEnvs), Answer{Value: []string{envShop, token}}, "environments: the answer holds a token prefix: " + ErrCredential.Error()},
		{q(idEnvs), Answer{Value: envShop}, "environments: a text answer to a list question"},
		{q("oncall.secret"), Answer{Value: SecretRef{Namespace: nsOps, Name: secretPager, Key: "token"}}, ""},
		{q("oncall.secret"), Answer{Value: SecretRef{Name: secretPager}}, "oncall.secret: a Secret reference needs a namespace and a name"},
		{q("oncall.secret"), Answer{Value: SecretRef{Namespace: nsOps, Name: secretPager, Key: token}}, "oncall.secret: the answer holds a token prefix: " + ErrCredential.Error()},
		{q("oncall.secret"), Answer{Value: "ops/pager"}, "oncall.secret: a text answer to a secretRef question"},
		{q("oncall.paged"), Answer{Value: 3}, "oncall.paged: an answer of type int"},
	}
	for _, tc := range cases {
		err := CheckAnswer(tc.q, tc.a)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != tc.want {
			t.Errorf("%s %+v: got %q, want %q", tc.q.ID, tc.a, got, tc.want)
		}
	}
}

func TestShownAndData(t *testing.T) {
	set, err := Load([]Source{builtin}, []Source{{Name: fileOncall, Data: []byte(valid)}})
	if err != nil {
		t.Fatal(err)
	}
	answers := map[string]Answer{
		idReportKind:     {Value: slack},
		"oncall.channel": {Value: chanPayments},
		"oncall.notes":   {Value: "read the runbook"},
		"oncall.secret":  {Value: SecretRef{Namespace: nsOps, Name: secretPager}},
		idEnvs:           {Unknown: true},
		"stale.answer":   {Value: "left by an older set"},
	}
	if !set.Shown(set.Question("oncall.notes"), answers) {
		t.Error("oncall.notes hidden with report.kind Slack and a channel")
	}
	data, err := set.Data(answers)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"report": map[string]any{"kind": slack},
		idEnvs:   []string{},
		"oncall": map[string]any{
			"channel": chanPayments,
			"notes":   "read the runbook",
			"secret":  map[string]any{"namespace": nsOps, "name": secretPager, "key": ""},
			"paged":   false,
		},
	}
	if diff := cmp.Diff(want, data); diff != "" {
		t.Errorf("data (-want +got):\n%s", diff)
	}

	// With report.kind log, the channel and the notes behind it are hidden:
	// their answers are left out of the data.
	answers[idReportKind] = Answer{Value: "log"}
	if set.Shown(set.Question("oncall.channel"), answers) || set.Shown(set.Question("oncall.notes"), answers) {
		t.Error("oncall.channel or oncall.notes shown with report.kind log")
	}
	data, err = set.Data(answers)
	if err != nil {
		t.Fatal(err)
	}
	if got := data["oncall"].(map[string]any)["notes"]; got != "" {
		t.Errorf("hidden notes: got %q", got)
	}

	answers[idReportKind] = Answer{Value: slack}
	answers["oncall.channel"] = Answer{Value: samples()["anthropic"]}
	if _, err := set.Data(answers); !errors.Is(err, ErrCredential) {
		t.Errorf("a credential in the answers: got %v", err)
	}
}
