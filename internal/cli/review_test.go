// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
)

const (
	surveyName    = "intendant-q6w6z"
	proposalsCM   = "survey-intendant-q6w6z-proposals"
	fileBilling   = "map/service/billing-api.yaml"
	fileWorker    = "map/compute/old-worker.yaml"
	fileNetwork   = "map/network/billing-api.yaml"
	idAddNetwork  = "7ea96a023f5377bf"
	idUpdBilling  = "c99d0a2b8b21007e"
	idRemWorker   = "0c1d2e3f40516273"
	flagAccept    = "--accept"
	flagNoCommit  = "--no-commit"
	flagMapDir    = "--map-dir"
	flagStateDir  = "--state-dir"
	cmdReview     = "review"
	acceptAll     = "all"
	reviewRecord  = "review.yaml"
	commentInFile = "# The billing API, owned by payments."
	surveyRev     = "470725dee572"
	intendantName = "intendant"
	nsApps        = "apps"
	verbGet       = "get"
	flagKubecfg   = "--kubeconfig"
	branchMain    = "main"
)

// fixtureMap is a small valid Map: a service that runs as a compute, in an
// environment, owned by a team.
var fixtureMap = map[string]string{
	"map/README.md":                "# The Map\n",
	"map/environment/prod.yaml":    "key: environment/prod\nprovenance: declared\nstatus: validated\ncreated: \"2026-11-01\"\n",
	"map/team/payments.yaml":       "key: team/payments\nprovenance: declared\nstatus: validated\ncreated: \"2026-11-01\"\n",
	"map/compute/billing-api.yaml": "key: compute/billing-api\nprovenance: discovered\nstatus: validated\ncreated: \"2026-11-01\"\n",
	fileWorker:                     "key: compute/old-worker\nprovenance: discovered\nstatus: validated\ncreated: \"2026-11-01\"\nrelations:\n  deployedIn: [environment/prod]\n",
	fileBilling: commentInFile + "\nkey: service/billing-api\nprovenance: declared\nstatus: validated\ncreated: \"2026-11-02\"\n" +
		"owner: team/payments\nrelations:\n  runsAs:\n    - compute/old-worker\n  belongsTo:\n    - team/payments\n",
}

// fixtureProposals are an Add, an Update and a Remove, as the Intendant
// writes them: an Update carries the entry as the Intendant saw it, here
// with another provenance, created and owner that review must not take.
func fixtureProposals() []v1alpha1.Proposal {
	return []v1alpha1.Proposal{
		{ID: idAddNetwork, Action: v1alpha1.ProposalAdd, Key: "network/billing-api", Source: v1alpha1.ProposalFromImport,
			Evidence: []string{"kubernetes/list_services"},
			Entry:    "key: network/billing-api\nprovenance: discovered\nstatus: proposed\ncreated: \"2026-11-24\"\nrelations:\n  deployedIn: [environment/prod]\n"},
		{ID: idUpdBilling, Action: v1alpha1.ProposalUpdate, Key: "service/billing-api", Source: v1alpha1.ProposalFromImport,
			Entry: "key: service/billing-api\nprovenance: discovered\nstatus: validated\ncreated: \"2026-11-24\"\n" +
				"lastSeen: \"2026-11-24\"\nrelations:\n  runsAs: [compute/billing-api, compute/old-worker]\n  belongsTo: [team/payments]\n"},
		{ID: idRemWorker, Action: v1alpha1.ProposalRemove, Key: "compute/old-worker", Source: v1alpha1.ProposalFromImport,
			Entry: fixtureMap[fileWorker]},
	}
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(p, "map/")))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// getter adapts a controller-runtime reader to review's get and list.
type getter struct{ client.Reader }

func (g getter) Get(ctx context.Context, key client.ObjectKey, obj client.Object) error {
	return g.Reader.Get(ctx, key, obj)
}

func surveyObjects(phase v1alpha1.RunPhase, withConfigMap bool) []client.Object {
	props := fixtureProposals()
	s := &v1alpha1.TakeCareSurvey{ObjectMeta: meta(surveyName, testNS, 0),
		Spec: v1alpha1.TakeCareSurveySpec{IntendantRef: v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: intendantName}},
		Status: v1alpha1.TakeCareSurveyStatus{RunStatus: v1alpha1.RunStatus{Phase: phase}, MapRevision: surveyRev,
			ProposalCounts: &v1alpha1.ProposalCounts{Add: 1, Update: 1, Remove: 1, Total: 3},
			Proposals:      props[:1], ProposalsConfigMap: proposalsCM}}
	objs := []client.Object{s, &v1alpha1.TakeCareIntendant{ObjectMeta: meta(intendantName, testNS, 0)}}
	if withConfigMap {
		data := map[string]string{}
		for _, p := range props {
			raw, _ := yaml.Marshal(p)
			data[p.ID] = string(raw)
		}
		objs = append(objs, &corev1.ConfigMap{Name: proposalsCM, Namespace: testNS, Data: data})
	}
	return objs
}

func reviewReader(t *testing.T, objs ...client.Object) getLister {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return getter{fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()}
}

type reviewRun struct {
	code           int
	stdout, stderr string
}

// runReviewWith runs review on r with args, stdin as the person's answers
// when it is not empty.
func runReviewWith(t *testing.T, r getLister, stdin string, args ...string) reviewRun {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &app{stdin: strings.NewReader(stdin), stdout: &out, stderr: &errOut, terminal: stdin != "", now: func() time.Time { return testNow }}
	var o reviewOpts
	fs := a.newFlags(cmdReview)
	o.flags(fs)
	if code := a.parse(fs, &o.common, args); code >= 0 {
		t.Fatalf("parse %v: exit %d: %s", args, code, errOut.String())
	}
	code := a.runReview(t.Context(), r, &o)
	return reviewRun{code, out.String(), errOut.String()}
}

func readTree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files, err := readMapDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// Spec 13 §10, the writers on a fixture Map: Add, Update and Remove;
// Remove drops the inbound relations; Update keeps provenance, created and
// owner; the result validates.
func TestReviewWriters(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	writeTree(t, dir, fixtureMap)
	run := runReviewWith(t, reviewReader(t, surveyObjects(v1alpha1.RunCompleted, true)...), "",
		flagAccept, acceptAll, flagNoCommit, flagMapDir, dir, flagStateDir, state, flagOutput, outputJSON)
	if run.code != ExitOK {
		t.Fatalf("review: exit %d, stdout %s, stderr %s", run.code, run.stdout, run.stderr)
	}
	var res Review
	if err := json.Unmarshal([]byte(run.stdout), &res); err != nil {
		t.Fatalf("%v: %s", err, run.stdout)
	}
	want := wantReview()
	want.Accepted, want.Written, want.Deleted = []string{idAddNetwork, idUpdBilling, idRemWorker}, []string{fileNetwork, fileBilling}, []string{fileWorker}
	if diff := cmp.Diff(want, res); diff != "" {
		t.Errorf("result (-want +got):\n%s", diff)
	}
	files := readTree(t, dir)
	if _, errs := mapserver.Check(files); len(errs) > 0 {
		t.Fatalf("the written Map does not validate: %v", errs)
	}
	if _, ok := files[fileWorker]; ok {
		t.Errorf("%s is still there", fileWorker)
	}
	var billing mapserver.Entry
	if err := yaml.Unmarshal(files[fileBilling], &billing); err != nil {
		t.Fatal(err)
	}
	if billing.Provenance != "declared" || billing.Created != "2026-11-02" || billing.Owner != "team/payments" {
		t.Errorf("the Update did not keep provenance, created and owner: %+v", billing)
	}
	if diff := cmp.Diff([]string{"compute/billing-api"}, billing.Relations.RunsAs); diff != "" {
		t.Errorf("runsAs after the Remove (-want +got):\n%s", diff)
	}

	// Decided ids are hidden next time.
	run = runReviewWith(t, reviewReader(t, surveyObjects(v1alpha1.RunCompleted, true)...), "",
		flagAccept, acceptAll, flagNoCommit, flagMapDir, dir, flagStateDir, state)
	if run.code != ExitOK || !strings.Contains(run.stdout, "nothing decided") {
		t.Errorf("second review: exit %d, stdout %q; want nothing decided", run.code, run.stdout)
	}
}

// Spec 13 §10: a proposal decided once is shown again when its entry
// changed; skipped ones are recorded and write no Map file.
func TestReviewShowsChangedEntriesAgain(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	writeTree(t, dir, fixtureMap)
	run := runReviewWith(t, reviewReader(t, surveyObjects(v1alpha1.RunCompleted, true)...), "",
		"--skip", idAddNetwork+","+idUpdBilling+","+idRemWorker, flagNoCommit, flagMapDir, dir, flagStateDir, state)
	if run.code != ExitOK {
		t.Fatalf("skip: exit %d: %s", run.code, run.stderr)
	}
	if diff := cmp.Diff(fixtureMapBytes(), readTree(t, dir)); diff != "" {
		t.Errorf("a skip changed the Map (-want +got):\n%s", diff)
	}
	raw, err := os.ReadFile(filepath.Join(state, reviewRecord)) //nolint:gosec // the test's own file
	if err != nil {
		t.Fatal(err)
	}
	var st reviewState
	if err := yaml.UnmarshalStrict(raw, &st); err != nil || len(st.Surveys) != 1 || len(st.Surveys[0].Proposals) != 3 {
		t.Fatalf("review record %s: %v", raw, err)
	}

	objs := surveyObjects(v1alpha1.RunCompleted, true)
	cm := objs[2].(*corev1.ConfigMap)
	p := fixtureProposals()[0]
	p.Entry = strings.Replace(p.Entry, "status: proposed", "description: seen again\nstatus: proposed", 1)
	b, _ := yaml.Marshal(p)
	cm.Data[idAddNetwork] = string(b)
	run = runReviewWith(t, reviewReader(t, objs...), "", flagAccept, acceptAll, flagNoCommit, flagMapDir, dir, flagStateDir, state, flagOutput, outputJSON)
	var res Review
	if err := json.Unmarshal([]byte(run.stdout), &res); err != nil {
		t.Fatalf("exit %d, %v: %s %s", run.code, err, run.stdout, run.stderr)
	}
	if diff := cmp.Diff([]string{idAddNetwork}, res.Accepted); diff != "" || res.Hidden != 2 {
		t.Errorf("accepted (-want +got):\n%s; hidden %d, want 2", diff, res.Hidden)
	}
}

// wantReview is the result of a review of the fixture survey that decided
// nothing yet: every list empty, never null.
func wantReview() Review {
	return Review{SchemaVersion: 1, Survey: surveyName, MapRevision: surveyRev,
		Accepted: []string{}, Validated: []string{}, Skipped: []string{}, Written: []string{}, Deleted: []string{}, Faults: []string{}}
}

func fixtureMapBytes() map[string][]byte {
	out := map[string][]byte{}
	for p, c := range fixtureMap {
		out[p] = []byte(c)
	}
	return out
}

// Spec 13 §3.4: a result that does not validate writes nothing (exit 1).
func TestReviewInvalidResultWritesNothing(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	writeTree(t, dir, fixtureMap)
	objs := surveyObjects(v1alpha1.RunCompleted, false)
	s := objs[0].(*v1alpha1.TakeCareSurvey)
	s.Status.Proposals[0].Entry = strings.Replace(s.Status.Proposals[0].Entry, "environment/prod", "environment/staging", 1)
	run := runReviewWith(t, reviewReader(t, objs...), "", flagAccept, idAddNetwork, flagNoCommit, flagMapDir, dir, flagStateDir, state)
	if run.code != ExitFinding || !strings.Contains(run.stdout, "environment/staging: no such entry") {
		t.Fatalf("exit %d, stdout %q; want 1 naming the fault", run.code, run.stdout)
	}
	if diff := cmp.Diff(fixtureMapBytes(), readTree(t, dir)); diff != "" {
		t.Errorf("the Map changed (-want +got):\n%s", diff)
	}
	if _, err := os.Stat(filepath.Join(state, reviewRecord)); !os.IsNotExist(err) {
		t.Errorf("the review record was written: %v", err)
	}

	// The same with --output json: one object, the faults in it.
	run = runReviewWith(t, reviewReader(t, objs...), "", flagAccept, idAddNetwork, flagNoCommit, flagMapDir, dir, flagStateDir, state, flagOutput, outputJSON)
	var res Review
	if err := json.Unmarshal([]byte(run.stdout), &res); err != nil || run.code != ExitFinding {
		t.Fatalf("exit %d, %v: %s", run.code, err, run.stdout)
	}
	if len(res.Faults) == 0 || !strings.Contains(res.Faults[0], "environment/staging: no such entry") || len(res.Written) != 0 {
		t.Errorf("faults %q, written %q", res.Faults, res.Written)
	}
}

// With --output json, every path prints one object whose lists are [] when
// empty; --mark-validated is recorded as validated, in a review record
// meant to be committed (0644); a field a later operator adds to a
// proposal is ignored.
func TestReviewJSONAndRecord(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	writeTree(t, dir, fixtureMap)
	objs := surveyObjects(v1alpha1.RunCompleted, true)
	cm := objs[2].(*corev1.ConfigMap)
	cm.Data[idAddNetwork] += "addedLater: a field this takecare does not know\n"
	run := runReviewWith(t, reviewReader(t, objs...), "", flagAccept, idAddNetwork, "--skip", idUpdBilling+","+idRemWorker, "--mark-validated",
		flagNoCommit, flagMapDir, dir, flagStateDir, state, flagOutput, outputJSON)
	if run.code != ExitOK || strings.Contains(run.stdout, "null") {
		t.Fatalf("exit %d, stdout %s, stderr %s; want 0 and no null", run.code, run.stdout, run.stderr)
	}
	record := filepath.Join(state, reviewRecord)
	raw, err := os.ReadFile(record) //nolint:gosec // the test's own file
	if err != nil {
		t.Fatal(err)
	}
	var st reviewState
	if err := yaml.UnmarshalStrict(raw, &st); err != nil || len(st.Surveys) != 1 || len(st.Surveys[0].Proposals) != 3 {
		t.Fatalf("review record %s: %v", raw, err)
	}
	if diff := cmp.Diff(decision{ID: idAddNetwork, Decision: decidedValidated, Digest: digest(&fixtureProposals()[0])}, st.Surveys[0].Proposals[1]); diff != "" {
		t.Errorf("recorded (-want +got):\n%s", diff)
	}
	if fi, err := os.Stat(record); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("the review record's mode: %v, %v; want 0644", fi.Mode().Perm(), err)
	}

	run = runReviewWith(t, reviewReader(t, objs...), "", flagAccept, acceptAll, flagNoCommit, flagMapDir, dir, flagStateDir, state, flagOutput, outputJSON)
	want := wantReview()
	want.Hidden = 3
	var res Review
	if err := json.Unmarshal([]byte(run.stdout), &res); err != nil || run.code != ExitOK {
		t.Fatalf("nothing decided: exit %d, %v: %s", run.code, err, run.stdout)
	}
	if diff := cmp.Diff(want, res); diff != "" || strings.Contains(run.stdout, "null") {
		t.Errorf("nothing decided (-want +got):\n%s\n%s", diff, run.stdout)
	}
}

// The screens print what the cluster holds without its control characters.
func TestPrintable(t *testing.T) {
	if got := printable("a\x1b]0;title\x07b\n\tc\r"); got != "a]0;titleb\n\tc" {
		t.Errorf("printable: %q", got)
	}
}

// What review refuses, each with exit code 2 and nothing written; and the
// status's proposals when the ConfigMap cannot be read.
func TestReviewRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		objs  []client.Object
		args  []string
		stdin string
		want  string
	}{
		{"survey not Completed", surveyObjects(v1alpha1.RunFailed, true), []string{"--survey", surveyName, flagAccept, acceptAll}, "", "is Failed, not Completed"},
		{"no Completed survey", surveyObjects(v1alpha1.RunRunning, true), []string{flagAccept, acceptAll}, "", "no Completed survey of the Intendant intendant"},
		{"unknown id", surveyObjects(v1alpha1.RunCompleted, true), []string{flagAccept, "ffff"}, "", "--accept: no proposal ffff"},
		{"accepted and skipped", surveyObjects(v1alpha1.RunCompleted, true), []string{flagAccept, idAddNetwork, "--skip", idAddNetwork}, "", "both accepted and skipped"},
		{"no terminal, no flag", surveyObjects(v1alpha1.RunCompleted, true), nil, "", "pass --accept"},
		{"two Intendants", append(surveyObjects(v1alpha1.RunCompleted, true), &v1alpha1.TakeCareIntendant{ObjectMeta: meta("other", testNS, 0)}), []string{flagAccept, acceptAll}, "", "pass --intendant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, state := t.TempDir(), t.TempDir()
			writeTree(t, dir, fixtureMap)
			args := append([]string{flagNoCommit, flagMapDir, dir, flagStateDir, state}, tc.args...)
			run := runReviewWith(t, reviewReader(t, tc.objs...), tc.stdin, args...)
			if run.code != ExitError || !strings.Contains(run.stderr, tc.want) {
				t.Errorf("exit %d, stderr %q; want 2 and %q", run.code, run.stderr, tc.want)
			}
			if diff := cmp.Diff(fixtureMapBytes(), readTree(t, dir)); diff != "" {
				t.Errorf("the Map changed (-want +got):\n%s", diff)
			}
		})
	}

	t.Run("ConfigMap not read", func(t *testing.T) {
		dir, state := t.TempDir(), t.TempDir()
		writeTree(t, dir, fixtureMap)
		run := runReviewWith(t, reviewReader(t, surveyObjects(v1alpha1.RunCompleted, false)...), "",
			flagAccept, acceptAll, flagNoCommit, flagMapDir, dir, flagStateDir, state, flagOutput, outputJSON)
		var res Review
		if err := json.Unmarshal([]byte(run.stdout), &res); err != nil {
			t.Fatalf("exit %d, %v: %s", run.code, err, run.stderr)
		}
		if res.NotRead != 2 || len(res.Accepted) != 1 || !strings.Contains(run.stderr, "2 of the survey's 3 proposals were not read") {
			t.Errorf("notRead %d, accepted %v, stderr %q", res.NotRead, res.Accepted, run.stderr)
		}
	})
}

// The person's answers on a terminal: one screen per proposal, with its
// diff; v marks validated, s skips, l leaves for later.
func TestReviewAsks(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	writeTree(t, dir, fixtureMap)
	run := runReviewWith(t, reviewReader(t, surveyObjects(v1alpha1.RunCompleted, true)...), "x\nl\nv\ns\n",
		flagNoCommit, flagMapDir, dir, flagStateDir, state, flagOutput, outputJSON)
	if run.code != ExitOK {
		t.Fatalf("exit %d: %s", run.code, run.stderr)
	}
	for _, want := range []string{"Proposal 1 of 3: " + idRemWorker, "--- " + fileWorker + " (local)", "-    - compute/old-worker",
		"evidence  kubernetes/list_services", "+++ " + fileNetwork + " (proposed)", "the Map revision \"" + surveyRev + "\""} {
		if !strings.Contains(run.stdout, want) {
			t.Errorf("the screens lack %q:\n%s", want, run.stdout)
		}
	}
	_, after, _ := strings.Cut(run.stdout, "\n{")
	var res Review
	if err := json.Unmarshal([]byte("{"+after), &res); err != nil {
		t.Fatal(err)
	}
	want := wantReview()
	want.Validated, want.Skipped, want.Written = []string{idAddNetwork}, []string{idUpdBilling}, []string{fileNetwork}
	if diff := cmp.Diff(want, res); diff != "" {
		t.Errorf("result (-want +got):\n%s", diff)
	}
	var network mapserver.Entry
	if err := yaml.Unmarshal(readTree(t, dir)[fileNetwork], &network); err != nil {
		t.Fatal(err)
	}
	if network.Status != statusValidated || network.LastValidated != "2026-11-24" {
		t.Errorf("accept and mark validated: status %s, lastValidated %s", network.Status, network.LastValidated)
	}
}

func TestDropRelations(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		changed        bool
	}{
		{"kept comment, emptied relation dropped", fixtureMap[fileBilling], commentInFile +
			"\nkey: service/billing-api\nprovenance: declared\nstatus: validated\ncreated: \"2026-11-02\"\nowner: team/payments\nrelations:\n  belongsTo:\n    - team/payments\n", true},
		{"relations emptied", "key: a/b\nrelations:\n  dependsOn: [compute/old-worker]\n", "key: a/b\n", true},
		{"other targets kept", "key: a/b\nrelations:\n  dependsOn: [compute/old-worker, compute/x]\n", "key: a/b\nrelations:\n  dependsOn: [compute/x]\n", true},
		{"alertedBy names alerts", "key: a/b\nrelations:\n  alertedBy: [compute/old-worker]\n", "", false},
		{"no relation to it", fixtureMap["map/team/payments.yaml"], "", false},
		{"not YAML", "key: [", "", false},
		{"a value reads relations", "kind: relations\nrelations:\n  dependsOn: [compute/old-worker]\n", "kind: relations\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, changed, err := dropRelations([]byte(tc.in), "compute/old-worker")
			if err != nil || changed != tc.changed {
				t.Fatalf("changed %v, err %v; want %v", changed, err, tc.changed)
			}
			want := tc.want
			if !tc.changed {
				want = tc.in
			}
			if diff := cmp.Diff(want, string(out)); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestApplyRefusesAForeignKey(t *testing.T) {
	files := maps.Clone(fixtureMapBytes())
	for _, p := range []v1alpha1.Proposal{
		{ID: "a", Action: v1alpha1.ProposalAdd, Key: "service/x", Entry: "key: service/y\nprovenance: declared\nstatus: proposed\ncreated: \"2026-11-24\"\n"},
		{ID: "b", Action: v1alpha1.ProposalAdd, Key: "service/../../x", Entry: "key: service/../../x\n"},
	} {
		if err := apply(files, &p, false, "2026-11-24"); err == nil {
			t.Errorf("proposal %s: applied, want a refusal", p.ID)
		}
	}
	if diff := cmp.Diff(fixtureMapBytes(), files); diff != "" {
		t.Errorf("a refused proposal changed the Map (-want +got):\n%s", diff)
	}
}
