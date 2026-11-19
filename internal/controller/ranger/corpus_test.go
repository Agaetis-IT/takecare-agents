// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

//go:build guardcorpus

package ranger

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

// The guard corpus (ADR 0020): real tool outputs and messages, run
// through the pinned gateway's own engine with the Ranger's rendered
// guards, must come back byte for byte; samples that must be masked come
// back masked whole; a key cut before its END line is refused. It runs
// with `make guard-corpus` (Docker), at every gateway bump and whenever a
// pattern or the Ranger's rendering changes.

const (
	corpusDir = "../../../test/guardcorpus/testdata"
	cRole     = "role"
	cContent  = "content"
	cType     = "type"
	cMasked   = "<masked>"
	cMessage  = "message"
	cUser     = "user"
)

// echo is the provider behind the gateway: it keeps the last request.
type echo struct {
	mu   sync.Mutex
	last []byte
}

func (e *echo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	e.mu.Lock()
	e.last = body
	e.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
}

func (e *echo) lastContent(t *testing.T) string {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	var req struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(e.last, &req); err != nil || len(req.Messages) == 0 {
		t.Fatalf("the provider received no chat request: %v", err)
	}
	return req.Messages[len(req.Messages)-1].Content
}

// gatewayVersion is AGENTGATEWAY_VERSION from hack/versions.env: the pin
// the chart runs.
func gatewayVersion(t *testing.T) string {
	t.Helper()
	f, err := os.Open("../../../hack/versions.env")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if v, ok := strings.CutPrefix(s.Text(), "AGENTGATEWAY_VERSION="); ok {
			return v
		}
	}
	t.Fatal("AGENTGATEWAY_VERSION is not in hack/versions.env")
	return ""
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// gateway runs the pinned image with the rendered guards in front of the
// echo provider, and returns its chat completions URL.
func gateway(t *testing.T, provider int) string {
	t.Helper()
	g, err := standalone(guards(corpusRanger(), true))
	if err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"llm": map[string]any{"models": []any{map[string]any{
		"name":       "*",
		"provider":   map[string]any{"custom": map[string]any{"formats": []any{map[string]any{cType: "completions"}}}},
		"params":     map[string]any{"apiKey": "corpus", "model": "m", "baseUrl": fmt.Sprintf("http://host.docker.internal:%d", provider)},
		"guardrails": map[string]any{"request": g},
	}}}}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	image := "cr.agentgateway.dev/agentgateway:" + gatewayVersion(t)
	name := fmt.Sprintf("takecare-guard-corpus-%d", port)
	// The test's own container: its pinned image and a temporary config.
	run := exec.Command("docker", "run", "-d", "--rm", "--name", name, "--add-host", "host.docker.internal:host-gateway", //nolint:gosec // see above
		"-p", fmt.Sprintf("127.0.0.1:%d:4000", port), "-v", dir+":/corpus:ro", image, "-f", "/corpus/config.yaml")
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("docker run %s: %v\n%s", image, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() }) //nolint:gosec // the container named above
	// The pin is a version tag: the run's record names the image it judged
	// the corpus with.
	digest, _ := exec.Command("docker", "image", "inspect", "--format", "{{index .RepoDigests 0}}", image).Output() //nolint:gosec // the pinned image
	t.Logf("the corpus runs through %s (%s)", image, strings.TrimSpace(string(digest)))
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if code, _ := call(url, []any{map[string]any{cRole: cUser, cContent: "ready"}}); code == http.StatusOK {
			return url
		}
		time.Sleep(250 * time.Millisecond)
	}
	logs, _ := exec.Command("docker", "logs", name).CombinedOutput() //nolint:gosec // the container named above
	t.Fatalf("the gateway %s did not answer:\n%s", image, logs)
	return ""
}

func call(url string, msgs []any) (int, error) {
	body, _ := json.Marshal(map[string]any{"model": "m", "messages": append([]any{map[string]any{cRole: "system", cContent: "corpus"}}, msgs...)})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req) //nolint:gosec // the test's own local gateway
	if err != nil {
		return 0, err
	}
	_ = res.Body.Close()
	return res.StatusCode, nil
}

func toolMessages(name, args, content string) []any {
	return []any{
		map[string]any{cRole: cUser, cContent: "go"},
		map[string]any{cRole: "assistant", cContent: nil, "tool_calls": []any{map[string]any{"id": "t1", cType: "function", "function": map[string]any{"name": name, "arguments": args}}}},
		map[string]any{cRole: "tool", "tool_call_id": "t1", cContent: content},
	}
}

// shape shows where two texts differ without showing either: letters as
// a, digits as 9.
func shape(s string) string {
	return regexp.MustCompile(`[0-9]`).ReplaceAllString(regexp.MustCompile(`[A-Za-z]`).ReplaceAllString(s, "a"), "9")
}

func firstDiff(want, got string) string {
	i := 0
	for i < len(want) && i < len(got) && want[i] == got[i] {
		i++
	}
	from := max(0, i-40)
	return fmt.Sprintf("at byte %d: sent %q, received %q", i, shape(want[from:min(len(want), i+40)]), shape(got[from:min(len(got), i+40)]))
}

// corpusOutput is one real tool output of the corpus.
type corpusOutput struct {
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
	Output string         `json:"output"`
}

func TestGuardCorpus(t *testing.T) {
	provider := &echo{}
	l, err := net.Listen("tcp", "0.0.0.0:0") //nolint:gosec // the gateway's container reaches it through the host
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: provider, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	url := gateway(t, l.Addr().(*net.TCPAddr).Port)

	t.Run("real outputs come back unchanged", func(t *testing.T) {
		files, _ := filepath.Glob(filepath.Join(corpusDir, "outputs", "*.json"))
		if len(files) == 0 {
			t.Fatal("no corpus output: the corpus did not bite")
		}
		for _, f := range files {
			raw, err := os.ReadFile(f) //nolint:gosec // the corpus's own files, from a glob of testdata
			if err != nil {
				t.Fatal(err)
			}
			var o corpusOutput
			if err := json.Unmarshal(raw, &o); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			args, _ := json.Marshal(o.Args)
			if code, err := call(url, toolMessages(o.Tool, string(args), o.Output)); err != nil || code != http.StatusOK {
				t.Errorf("%s: HTTP %d, %v", filepath.Base(f), code, err)
				continue
			}
			if got := provider.lastContent(t); got != o.Output {
				t.Errorf("%s (%s): changed %s", filepath.Base(f), o.Tool, firstDiff(o.Output, got))
			}
		}
	})

	t.Run("real messages come back unchanged", func(t *testing.T) {
		files, _ := filepath.Glob(filepath.Join(corpusDir, "messages", "*.txt"))
		if len(files) == 0 {
			t.Fatal("no corpus message: the corpus did not bite")
		}
		for _, f := range files {
			raw, err := os.ReadFile(f) //nolint:gosec // the corpus's own files, from a glob of testdata
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			if code, err := call(url, []any{map[string]any{cRole: cUser, cContent: text}}); err != nil || code != http.StatusOK {
				t.Errorf("%s: HTTP %d, %v", filepath.Base(f), code, err)
				continue
			}
			if got := provider.lastContent(t); got != text {
				t.Errorf("%s: changed %s", filepath.Base(f), firstDiff(text, got))
			}
		}
	})

	t.Run("samples are masked whole; a secret shape in a message is refused", func(t *testing.T) {
		for _, s := range mustMask() {
			for _, where := range []string{cMessage, "tool output"} {
				text := "before " + s.value + " after"
				msgs := []any{map[string]any{cRole: cUser, cContent: text}}
				if where == "tool output" {
					msgs = toolMessages("pods_log", "{}", text)
				}
				want := http.StatusOK
				if s.secret && where == cMessage {
					// Spec 09 §4: masked whole in a tool output, refused in a
					// message.
					want = standaloneRejectStatus
				}
				if code, err := call(url, msgs); err != nil || code != want {
					t.Errorf("%s in a %s: HTTP %d, %v; want %d", s.name, where, code, err, want)
					continue
				}
				if want != http.StatusOK {
					continue
				}
				got := provider.lastContent(t)
				if !strings.HasPrefix(got, "before ") || !strings.HasSuffix(got, " after") || !strings.Contains(got, s.token) || strings.Contains(got, s.keep) {
					t.Errorf("%s in a %s: received %q", s.name, where, shape(got))
				}
			}
		}
	})

	t.Run("a key cut before its END line is refused", func(t *testing.T) {
		code, err := call(url, toolMessages("pods_log", "{}", "log: "+keyBegin()+"\nMIIEpAIBAAKCAQEAcut"))
		if err != nil || code != standaloneRejectStatus {
			t.Errorf("HTTP %d, %v; want %d", code, err, standaloneRejectStatus)
		}
	})
}

// sample is a made-up value that must reach the provider masked: token is
// what replaces it, keep a fragment that must not remain; a secret shape
// is refused in a message.
type sample struct {
	name, value, token, keep string
	secret                   bool
}

// keyBegin assembles a private key's first line, so that no secret
// scanner reads a literal one in the repository.
func keyBegin() string { return "-----BEGIN RSA " + "PRIVATE KEY-----" }

// mustMask builds the samples at run time: the secret shapes are
// assembled from pieces, never written whole.
func mustMask() []sample {
	end := "-----END RSA " + "PRIVATE KEY-----"
	return []sample{
		{"private key", keyBegin() + "\nMIIEpAIBAAKCAQEAsample\n" + end, cMasked, "MIIEpA", true},
		{"cloud access key", "AKIA" + "ABCDEFGHIJKLMNOP", cMasked, "ABCDEFGHIJKLMNOP", true},
		{"Slack token", "xox" + "b-1234567890-abcdefghijkl", cMasked, "abcdefghijkl", true},
		{"card", "4111 1111 1111 1111", "<CREDIT_CARD>", "4111 1111", false},
		{"email", "oncall.corpus@example.com", "<EMAIL_ADDRESS>", "oncall.corpus", false},
		{"phone, E.164", "+33 1 23 45 67 89", cMasked, "45 67 89", false},
		{"phone, French", "01 23 45 67 89", cMasked, "45 67 89", false},
		{"phone, North American", "(415) 555-0123", cMasked, "555-0123", false},
		{"social security number, US", "123-45-6789", cMasked, "45-6789", false},
		{"social security number, French", "2 91 07 75 112 345 18", cMasked, "345 18", false},
	}
}
