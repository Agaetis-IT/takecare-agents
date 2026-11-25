// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

func runValidate(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &app{stdout: &out, stderr: &errOut, now: func() time.Time { return testNow }}
	code = a.run(t.Context(), append([]string{"validate"}, args...))
	return code, out.String(), errOut.String()
}

// Spec 13 §3.5 and criterion 7: every fault of a broken Map at once, each
// with its file, with the Map server's own checks.
func TestValidate(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, fixtureMap)
	code, out, stderr := runValidate(t, flagMapDir, dir)
	if code != ExitOK || out != dir+": valid, 5 entries\n" {
		t.Fatalf("a valid Map: exit %d, stdout %q, stderr %q", code, out, stderr)
	}

	broken := maps.Clone(fixtureMap)
	broken["map/team/payments.yaml"] = strings.Replace(broken["map/team/payments.yaml"], "team/payments", "team/billing", 1)
	broken[fileWorker] = strings.Replace(broken[fileWorker], "created: \"2026-11-01\"", "created: \"2026-13-01\"", 1)
	broken["map/compute/.hidden.yaml"] = "not: an entry\n"
	dir = t.TempDir()
	writeTree(t, dir, broken)
	code, out, _ = runValidate(t, flagMapDir, dir, flagOutput, outputJSON)
	if code != ExitFinding {
		t.Fatalf("a broken Map: exit %d, want 1; stdout %s", code, out)
	}
	var v Validation
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"map/compute/old-worker.yaml: created 2026-13-01 is not a date",
		"map/team/payments.yaml: key team/billing does not match the file's place, team/payments",
		"map/service/billing-api.yaml: runsAs compute/old-worker: no such entry",
		"map/service/billing-api.yaml: belongsTo team/payments: no such entry",
	}
	if diff := cmp.Diff(want, v.Faults); diff != "" || v.Valid {
		t.Errorf("faults (-want +got):\n%s", diff)
	}

	code, _, stderr = runValidate(t, flagMapDir, filepath.Join(dir, "absent"))
	if code != ExitError || !strings.Contains(stderr, "open the Map directory") {
		t.Errorf("an absent directory: exit %d, stderr %q", code, stderr)
	}
}

// gitRepo makes a repository holding the fixture Map at map/, committed,
// with an identity and no signing of its own, and moves into it.
func gitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[user]\n\tname = Map Keeper\n\temail = keeper@example.org\n[commit]\n\tgpgsign = false\n[init]\n\tdefaultBranch = main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	writeTree(t, filepath.Join(repo, "map"), fixtureMap)
	t.Chdir(repo)
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "--all"}, {"commit", "--quiet", "--message", "the Map"}} {
		mustGit(t, args...)
	}
	return repo
}

func mustGit(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput() //nolint:gosec // the test's own git calls
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Spec 13 §7 and §10: the branch and commit are made with the configured
// identity, the review record committed with the files; no remote.
func TestReviewCommits(t *testing.T) {
	gitRepo(t)
	run := runReviewWith(t, reviewReader(t, surveyObjects(v1alpha1.RunCompleted, true)...), "", flagAccept, acceptAll)
	if run.code != ExitOK {
		t.Fatalf("exit %d: %s %s", run.code, run.stdout, run.stderr)
	}
	if got := mustGit(t, "branch", "--show-current"); got != "takecare/review-"+surveyName {
		t.Errorf("branch %s", got)
	}
	log := mustGit(t, "log", "-1", "--format=%an <%ae>%n%B")
	for _, want := range []string{"Map Keeper <keeper@example.org>", "takecare: review " + surveyName,
		"Accepted: " + idAddNetwork + ", " + idUpdBilling + ", " + idRemWorker, "Map revision of the survey: 470725dee572"} {
		if !strings.Contains(log, want) {
			t.Errorf("the commit lacks %q:\n%s", want, log)
		}
	}
	files := mustGit(t, "show", "--name-status", "--format=", "HEAD")
	for _, want := range []string{"A\t.takecare/review.yaml", "D\t" + fileWorker, "A\t" + fileNetwork, "M\t" + fileBilling} {
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

// A dirty path stops the run before writing (exit 2), and a refusing hook
// leaves the files written and staged (exit 2).
func TestReviewGitRefusals(t *testing.T) {
	t.Run("dirty path", func(t *testing.T) {
		repo := gitRepo(t)
		if err := os.WriteFile(filepath.Join(repo, fileBilling), []byte("edited\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		run := runReviewWith(t, reviewReader(t, surveyObjects(v1alpha1.RunCompleted, true)...), "", flagAccept, acceptAll)
		if run.code != ExitError || !strings.Contains(run.stderr, fileBilling) {
			t.Fatalf("exit %d, stderr %q; want 2 naming %s", run.code, run.stderr, fileBilling)
		}
		if got := mustGit(t, "branch", "--show-current"); got != branchMain {
			t.Errorf("branch %s, want main", got)
		}
		if st := mustGit(t, "status", "--porcelain"); st != "M "+fileBilling && st != " M "+fileBilling {
			t.Errorf("status %q: something else was written", st)
		}
	})
	t.Run("refusing hook", func(t *testing.T) {
		repo := gitRepo(t)
		hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\necho refused by the hook >&2\nexit 1\n"), 0o700); err != nil { //nolint:gosec // a hook must be executable
			t.Fatal(err)
		}
		run := runReviewWith(t, reviewReader(t, surveyObjects(v1alpha1.RunCompleted, true)...), "", flagAccept, acceptAll)
		if run.code != ExitError || !strings.Contains(run.stderr, "refused by the hook") {
			t.Fatalf("exit %d, stderr %q; want 2 with the hook's message", run.code, run.stderr)
		}
		staged := mustGit(t, "diff", "--cached", "--name-only")
		for _, want := range []string{".takecare/review.yaml", fileNetwork, fileWorker, fileBilling} {
			if !strings.Contains(staged, want) {
				t.Errorf("%s is not staged: %q", want, staged)
			}
		}
	})
}

// Spec 13 §3.4 step 5 and §7: the git preconditions hold before the first
// screen. A second pass on the same survey, with the first pass's branch
// there, stops with exit 2 naming --branch: no screen, nothing written.
// So does a directory outside a git work tree.
func TestReviewChecksGitBeforeAsking(t *testing.T) {
	t.Run("second pass on the same survey", func(t *testing.T) {
		gitRepo(t)
		// The Remove comes first: accept it, leave the rest for later.
		run := runReviewWith(t, reviewReader(t, surveyObjects(v1alpha1.RunCompleted, true)...), "a\nl\nl\n")
		if run.code != ExitOK {
			t.Fatalf("first pass: exit %d: %s %s", run.code, run.stdout, run.stderr)
		}
		head := mustGit(t, "rev-parse", "HEAD")
		run = runReviewWith(t, reviewReader(t, surveyObjects(v1alpha1.RunCompleted, true)...), "a\na\n")
		if run.code != ExitError || !strings.Contains(run.stderr, "--branch") || !strings.Contains(run.stderr, "takecare/review-"+surveyName) {
			t.Fatalf("second pass: exit %d, stderr %q; want 2 naming the branch and --branch", run.code, run.stderr)
		}
		if strings.Contains(run.stdout, "Proposal") {
			t.Errorf("a screen was printed: %s", run.stdout)
		}
		if got := mustGit(t, "rev-parse", "HEAD"); got != head {
			t.Errorf("HEAD moved: %s, want %s", got, head)
		}
		if st := mustGit(t, "status", "--porcelain"); st != "" {
			t.Errorf("something was written: %s", st)
		}

		// --branch names another branch for this pass: the Add accepted,
		// the Update, which still names the removed entry, skipped.
		run = runReviewWith(t, reviewReader(t, surveyObjects(v1alpha1.RunCompleted, true)...), "a\ns\n", "--branch", "takecare/review-second")
		if run.code != ExitOK || mustGit(t, "branch", "--show-current") != "takecare/review-second" {
			t.Errorf("with --branch: exit %d: %s %s", run.code, run.stdout, run.stderr)
		}
	})
	t.Run("not a git work tree", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
		writeTree(t, filepath.Join(dir, "map"), fixtureMap)
		t.Chdir(dir)
		run := runReviewWith(t, reviewReader(t, surveyObjects(v1alpha1.RunCompleted, true)...), "a\na\na\n")
		if run.code != ExitError || !strings.Contains(run.stderr, "not in a git work tree") || !strings.Contains(run.stderr, "--no-commit") {
			t.Fatalf("exit %d, stderr %q; want 2 naming --no-commit", run.code, run.stderr)
		}
		if strings.Contains(run.stdout, "Proposal") {
			t.Errorf("a screen was printed: %s", run.stdout)
		}
		if diff := cmp.Diff(fixtureMapBytes(), readTree(t, "map")); diff != "" {
			t.Errorf("the Map changed (-want +got):\n%s", diff)
		}
	})
}

// Spec 13 §3.5 and criterion 3: validate --questions checks question
// files without running them, every fault of every file at once.
func TestValidateQuestions(t *testing.T) {
	mapDir := t.TempDir()
	writeTree(t, mapDir, fixtureMap)
	questions := t.TempDir()
	good := "version: 1\nquestions:\n  - { id: oncall.channel, text: The on-call channel, why: Where diagnoses go, kind: text }\n" +
		"outputs:\n  - { path: takecare/oncall.md, template: \"{{ .oncall.channel }}\" }\n"
	if err := os.WriteFile(filepath.Join(questions, "a.yaml"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runValidate(t, flagMapDir, mapDir, "--questions", questions)
	if want := mapDir + ": valid, 5 entries\n" + questions + ": valid, 1 questions, 1 outputs\n"; code != ExitOK || out != want {
		t.Fatalf("valid files: exit %d, stdout %q, stderr %q", code, out, stderr)
	}

	bad := "version: 1\nquestions:\n  - { id: oncall.channel, text: Again, why: Twice, kind: secret }\n" +
		"outputs:\n  - { path: ../escape.md, template: \"{{ call .x }\" }\n"
	if err := os.WriteFile(filepath.Join(questions, "b.yml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runValidate(t, flagMapDir, mapDir, "--questions", questions, flagOutput, outputJSON)
	var v Validation
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	b := filepath.Join(questions, "b.yml")
	want := []string{
		b + `: question oncall.channel: unknown kind "secret", want one of [text choice list bool secretRef]`,
		b + ": question oncall.channel is already defined in " + filepath.Join(questions, "a.yaml"),
		b + `: output "../escape.md": leads out of the repository`,
		b + `: output "../escape.md": template does not parse: template: ../escape.md:1: unexpected "}" in operand`,
	}
	if diff := cmp.Diff(want, v.QuestionFaults); diff != "" || code != ExitFinding || v.Valid || len(v.Faults) != 0 {
		t.Errorf("exit %d, valid %v, map faults %v, question faults (-want +got):\n%s", code, v.Valid, v.Faults, diff)
	}

	empty := t.TempDir()
	code, out, _ = runValidate(t, flagMapDir, mapDir, "--questions", empty)
	if code != ExitFinding || !strings.Contains(out, empty+": no .yaml or .yml question file") {
		t.Errorf("no question file: exit %d, stdout %q", code, out)
	}
	code, _, stderr = runValidate(t, flagMapDir, mapDir, "--questions", filepath.Join(empty, "absent"))
	if code != ExitError || !strings.Contains(stderr, "open the question directory") {
		t.Errorf("an absent directory: exit %d, stderr %q", code, stderr)
	}
}
