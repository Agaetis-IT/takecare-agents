// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/agaetis-it/takecare-agents/internal/golden"
	"github.com/agaetis-it/takecare-agents/internal/questionnaire/builtin"
)

const (
	cmdInit       = "init"
	flagAnswers   = "--answers"
	flagDir       = "--dir"
	flagOverwrite = "--overwrite"
	demoAnswers   = "testdata/demo-answers.yaml"
	valuesFile    = "takecare/values.yaml"
	stateFile     = ".takecare/answers.yaml"
)

type initRun struct {
	code           int
	stdout, stderr string
}

// runInitWith runs init with args; stdin, when not empty, is a terminal
// the person answers on, one line at a time.
func runInitWith(t *testing.T, stdin string, args ...string) initRun {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &app{stdin: iotest.OneByteReader(strings.NewReader(stdin)), stdout: &out, stderr: &errOut,
		terminal: stdin != "", now: func() time.Time { return testNow }}
	code := a.run(t.Context(), append([]string{cmdInit}, args...))
	return initRun{code, out.String(), errOut.String()}
}

// tree reads every file under dir, by slash path.
func tree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(p) //nolint:gosec // the test's own temporary tree
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		files[filepath.ToSlash(rel)] = raw
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func paths(files map[string][]byte) []string {
	out := make([]string, 0, len(files))
	for p := range files {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// Criterion 1 and spec 13 §10: init --answers testdata/demo-answers.yaml
// writes exactly the golden tree, and its Map validates (its resources
// passed the CRDs' schemas before writing; test/chart renders its values).
func TestInitDemoGolden(t *testing.T) {
	dir := t.TempDir()
	run := runInitWith(t, "", flagAnswers, demoAnswers, flagDir, dir, flagNoCommit)
	if run.code != ExitOK {
		t.Fatalf("exit %d: %s %s", run.code, run.stdout, run.stderr)
	}
	got := tree(t, dir)
	for p, raw := range got {
		golden.Assert(t, "init-demo/"+p, raw)
	}
	if want := paths(tree(t, filepath.Join("testdata", "init-demo"))); !slices.Equal(paths(got), want) {
		t.Errorf("files %v, golden tree %v", paths(got), want)
	}
	files, err := readMapDir(filepath.Join(dir, "map"))
	if err != nil {
		t.Fatal(err)
	}
	if f := faults(files); len(f) > 0 {
		t.Errorf("the written Map does not validate: %v", f)
	}
}

// Spec 13 §3.6: run again, init writes nothing new; a file a person
// edited is kept when init has nothing new for it, and is not overwritten
// without --overwrite when it has.
func TestInitRerunAndEditedFiles(t *testing.T) {
	dir := t.TempDir()
	if run := runInitWith(t, "", flagAnswers, demoAnswers, flagDir, dir, flagNoCommit); run.code != ExitOK {
		t.Fatalf("exit %d: %s", run.code, run.stderr)
	}
	if run := runInitWith(t, "", flagDir, dir, flagNoCommit); run.code != ExitOK || !strings.Contains(run.stdout, "nothing changed") {
		t.Fatalf("a re-run with every answer: exit %d, %s %s", run.code, run.stdout, run.stderr)
	}
	values := filepath.Join(dir, valuesFile)
	edited := append(tree(t, dir)[valuesFile], []byte("# a person's note\n")...)
	if err := os.WriteFile(values, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	run := runInitWith(t, "", flagDir, dir, flagNoCommit, outputFlag, outputJSON)
	var res Init
	if err := json.Unmarshal([]byte(run.stdout), &res); err != nil || run.code != ExitOK || !slices.Equal(res.Kept, []string{valuesFile}) {
		t.Fatalf("the same answers over an edited file: exit %d, %+v, %v: %s", run.code, res, err, run.stderr)
	}

	// A new answer changes the values: refused without --overwrite.
	other := filepath.Join(t.TempDir(), "answers.yaml")
	if err := os.WriteFile(other, []byte("version: 2\nanswers:\n  model.provider: https://llm.example.com/v1 other-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := tree(t, dir)
	run = runInitWith(t, "", flagAnswers, other, flagDir, dir, flagNoCommit)
	if run.code != ExitError || !strings.Contains(run.stderr, "edited since the last run") || !strings.Contains(run.stderr, valuesFile) ||
		!strings.Contains(run.stdout, "+      model: \"other-model\"") {
		t.Fatalf("an edited file: exit %d, stdout %s, stderr %s", run.code, run.stdout, run.stderr)
	}
	if d := cmp.Diff(before, tree(t, dir)); d != "" {
		t.Fatalf("a refused run wrote (-before +after):\n%s", d)
	}
	run = runInitWith(t, "", flagAnswers, other, flagDir, dir, flagNoCommit, flagOverwrite)
	if run.code != ExitOK || !strings.Contains(string(tree(t, dir)[valuesFile]), "other-model") {
		t.Fatalf("--overwrite: exit %d, %s", run.code, run.stderr)
	}
}

const outputFlag = "--output"

// A re-run that changes nothing still reports the answers recorded as
// "I don't know" (spec 13 §3.6).
func TestInitNothingChangedReportsUnknown(t *testing.T) {
	raw, err := os.ReadFile(demoAnswers)
	if err != nil {
		t.Fatal(err)
	}
	answers := strings.Replace(string(raw), "  knowledge: http://runbooks.takecare-system.svc:8080/mcp\n", "", 1) + "unknown: [knowledge]\n"
	file := filepath.Join(t.TempDir(), "answers.yaml")
	if err := os.WriteFile(file, []byte(answers), 0o600); err != nil { //nolint:gosec // the test's own temporary file
		t.Fatal(err)
	}
	dir := t.TempDir()
	if run := runInitWith(t, "", flagAnswers, file, flagDir, dir, flagNoCommit); run.code != ExitOK {
		t.Fatalf("exit %d: %s", run.code, run.stderr)
	}
	run := runInitWith(t, "", flagDir, dir, flagNoCommit, outputFlag, outputJSON)
	var res Init
	if err := json.Unmarshal([]byte(run.stdout), &res); err != nil || run.code != ExitOK {
		t.Fatalf("exit %d, %v: %s %s", run.code, err, run.stdout, run.stderr)
	}
	if len(res.Written) != 0 || !slices.Equal(res.Unknown, []string{"knowledge"}) {
		t.Errorf("written %v, unknown %v; want nothing written and unknown [knowledge]", res.Written, res.Unknown)
	}
}

// Spec 13 §3.6 and criterion 2 and 9: a re-run asks only the questions
// without an answer and those whose when changed, in accessible mode
// (TAKECARE_ACCESSIBLE), each with its why.
func TestInitAsksOnlyWhatIsNew(t *testing.T) {
	dir := t.TempDir()
	if run := runInitWith(t, "", flagAnswers, demoAnswers, flagDir, dir, flagNoCommit); run.code != ExitOK {
		t.Fatalf("exit %d: %s", run.code, run.stderr)
	}
	state := filepath.Join(dir, stateFile)
	raw := string(tree(t, dir)[stateFile])
	raw = strings.Replace(raw, "  knowledge: http://runbooks.takecare-system.svc:8080/mcp\n", "", 1)
	raw = strings.Replace(raw, "report.kind: The report server's log", "report.kind: Slack\n"+
		"  report.destinations: [practitioner=C0123456789]\n"+
		"  report.secretRef: {namespace: takecare-system, name: slack, key: token}", 1)
	raw += "when:\n  report.destinations: report.kind=Slack\n  report.secretRef: report.kind\n"
	if err := os.WriteFile(state, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAKECARE_ACCESSIBLE", "1")
	// --no-color reaches the forms: Huh's line prompts are colored
	// by its default theme otherwise (spec 13 §3).
	// The Secret's namespace, name and key, then the knowledge source.
	run := runInitWith(t, "takecare-system\nslack-bot\ntoken\n\n", flagDir, dir, flagNoCommit, flagOverwrite, outputFlag, outputJSON,
		"--no-color")
	if run.code != ExitOK {
		t.Fatalf("exit %d: %s %s", run.code, run.stdout, run.stderr)
	}
	var res Init
	if err := json.Unmarshal([]byte(run.stdout), &res); err != nil {
		t.Fatalf("%v: %s", err, run.stdout)
	}
	if want := []string{"report.secretRef", "knowledge"}; !slices.Equal(res.Asked, want) {
		t.Errorf("asked %v, want %v", res.Asked, want)
	}
	for _, want := range []string{"Why: Named in the connector, never read", "Why: The Intendant's knowledge connectors"} {
		if !strings.Contains(run.stderr, want) {
			t.Errorf("no %q in the prompts:\n%s", want, run.stderr)
		}
	}
	if strings.Contains(run.stderr, "\x1b[") {
		t.Errorf("an escape sequence in the prompts with --no-color:\n%q", run.stderr)
	}
	files := tree(t, dir)
	if !strings.Contains(string(files[valuesFile]), `name: "slack-bot"`) {
		t.Errorf("the new answer is not in the values:\n%s", files[valuesFile])
	}
	if _, ok := files["takecare/connectors/knowledge.yaml"]; !ok {
		t.Error("the knowledge connector was deleted: init never deletes a file")
	}
}

// Spec 13 §3 and §4.2: without a terminal init never prompts, an invalid
// question file is refused with every fault and nothing written, and no
// output may sit under the state directory.
func TestInitRefusals(t *testing.T) {
	dir := t.TempDir()
	run := runInitWith(t, "", flagDir, dir, flagNoCommit)
	if run.code != ExitError || !strings.Contains(run.stderr, "pass --answers <file>") || !strings.Contains(run.stderr, "cluster.name") {
		t.Errorf("no terminal: exit %d, %s", run.code, run.stderr)
	}
	partial := filepath.Join(t.TempDir(), "answers.yaml")
	if err := os.WriteFile(partial, []byte("version: 2\nanswers:\n  cluster.name: \"\"\nunknown: [operator.namespace]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run = runInitWith(t, "", flagAnswers, partial, flagDir, dir, flagNoCommit)
	if run.code != ExitError || !strings.Contains(run.stderr, "add them to "+partial) || strings.Contains(run.stderr, "operator.namespace") {
		t.Errorf("a partial answers file: exit %d, %s", run.code, run.stderr)
	}
	// A default is never an answer without the person: with --answers, a
	// question that has one is still missing (spec 13 §4.2).
	for _, id := range []string{"alerts.source", "metrics.service"} {
		if !strings.Contains(run.stderr, id) {
			t.Errorf("%s, which has a default, is not missing: %s", id, run.stderr)
		}
	}

	questions := t.TempDir()
	bad := "version: 1\nquestions:\n  - { id: x, text: X, why: Y, kind: nope }\noutputs:\n  - { path: /abs.md, template: x }\n"
	if err := os.WriteFile(filepath.Join(questions, "bad.yaml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	run = runInitWith(t, "", flagAnswers, demoAnswers, flagDir, dir, flagNoCommit, "--questions", questions)
	if run.code != ExitError || !strings.Contains(run.stderr, `unknown kind "nope"`) || !strings.Contains(run.stderr, "an absolute path") {
		t.Errorf("an invalid question file: exit %d, %s", run.code, run.stderr)
	}

	under := t.TempDir()
	out := "version: 1\nquestions: []\noutputs:\n  - { path: state/notes.md, template: x }\n"
	if err := os.WriteFile(filepath.Join(under, "q.yaml"), []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	run = runInitWith(t, "", flagAnswers, demoAnswers, flagDir, dir, flagNoCommit, "--questions", under, flagStateDir, "state")
	if run.code != ExitError || !strings.Contains(run.stderr, "under the state directory state") {
		t.Errorf("an output under the state directory: exit %d, %s", run.code, run.stderr)
	}
	if run := runInitWith(t, "", flagAnswers, demoAnswers, flagDir, dir, flagNoCommit, flagMapDir, "../out"); run.code != ExitError ||
		!strings.Contains(run.stderr, "not inside the repository") {
		t.Errorf("a Map directory outside: exit %d, %s", run.code, run.stderr)
	}
	if files := tree(t, dir); len(files) != 0 {
		t.Errorf("refused runs wrote %v", paths(files))
	}
}

// Spec 13 §7: the branch and commit are made with the configured
// identity; a dirty path stops the run before writing; no remote.
func TestInitCommits(t *testing.T) {
	answers, err := filepath.Abs(demoAnswers)
	if err != nil {
		t.Fatal(err)
	}
	repo := gitRepo(t)
	const mapDir = "ground"
	if err := os.MkdirAll(filepath.Join(repo, "takecare"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, valuesFile), []byte("draft: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := runInitWith(t, "", flagAnswers, answers, flagMapDir, mapDir, flagOverwrite)
	if run.code != ExitError || !strings.Contains(run.stderr, "changes on the paths init writes") || !strings.Contains(run.stderr, valuesFile) {
		t.Fatalf("a dirty path: exit %d, %s", run.code, run.stderr)
	}
	if err := os.Remove(filepath.Join(repo, valuesFile)); err != nil {
		t.Fatal(err)
	}
	run = runInitWith(t, "", flagAnswers, answers, flagMapDir, mapDir)
	if run.code != ExitOK {
		t.Fatalf("exit %d: %s %s", run.code, run.stdout, run.stderr)
	}
	if got := mustGit(t, "branch", "--show-current"); got != "takecare/init-2026-11-24" {
		t.Errorf("branch %s", got)
	}
	log := mustGit(t, "log", "-1", "--format=%an <%ae>%n%B")
	for _, want := range []string{"Map Keeper <keeper@example.org>", "takecare: init", fmt.Sprintf("Question set version: %d", builtin.Version)} {
		if !strings.Contains(log, want) {
			t.Errorf("the commit lacks %q:\n%s", want, log)
		}
	}
	files := mustGit(t, "show", "--name-status", "--format=", "HEAD")
	for _, want := range []string{"A\t" + stateFile, "A\t" + valuesFile, "A\t" + mapDir + "/environment/demo-apps.yaml"} {
		if !strings.Contains(files, want) {
			t.Errorf("the commit lacks %q:\n%s", want, files)
		}
	}
	if st := mustGit(t, "status", "--porcelain"); st != "" {
		t.Errorf("the working tree is not clean: %s", st)
	}
	if remotes := mustGit(t, "remote"); remotes != "" {
		t.Errorf("remotes %q", remotes)
	}
	if !strings.Contains(run.stdout, "push it and open the pull request with your own tools") {
		t.Errorf("no next step: %s", run.stdout)
	}
}
