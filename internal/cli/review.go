// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bufio"
	"cmp"
	"context"
	"flag"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/kubeview"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
)

// reviewSchemaVersion versions the shape of review --output json.
const reviewSchemaVersion = 1

// getLister is what review needs of the cluster: get and list only.
type getLister interface {
	lister
	Get(ctx context.Context, key client.ObjectKey, obj client.Object) error
}

type reviewOpts struct {
	common
	namespace, kubeconfig, kctx         string
	survey, intendant, mapDir, stateDir string
	accept, skip, branch                string
	markValidated, all, noCommit        bool
}

// Review is what review prints once done.
type Review struct {
	SchemaVersion int    `json:"schemaVersion"`
	Survey        string `json:"survey"`
	MapRevision   string `json:"mapRevision"`
	// Hidden counts the proposals already decided with the same content;
	// NotRead those the full set holds that could not be read.
	Hidden    int      `json:"hidden"`
	NotRead   int      `json:"notRead"`
	Accepted  []string `json:"accepted"`
	Validated []string `json:"validated"`
	Skipped   []string `json:"skipped"`
	Written   []string `json:"written"`
	Deleted   []string `json:"deleted"`
	Branch    string   `json:"branch,omitempty"`
	// Faults are the resulting Map's, when it does not validate.
	Faults []string `json:"faults"`
}

// flags registers review's flags.
func (o *reviewOpts) flags(fs *flag.FlagSet) {
	o.register(fs)
	fs.StringVar(&o.namespace, "namespace", "takecare-system", "the operator's namespace")
	fs.StringVar(&o.kubeconfig, "kubeconfig", "", "the kubeconfig file (default: KUBECONFIG, then ~/.kube/config)")
	fs.StringVar(&o.kctx, "context", "", "the kubeconfig context, for this run only")
	fs.StringVar(&o.survey, "survey", "", "the survey (default: the Intendant's newest Completed survey)")
	fs.StringVar(&o.intendant, "intendant", "", "the Intendant (default: the only one in the namespace)")
	fs.StringVar(&o.mapDir, "map-dir", "map", "the Map directory")
	fs.StringVar(&o.stateDir, "state-dir", ".takecare", "the directory of the review record")
	fs.StringVar(&o.accept, "accept", "", "the proposal ids to accept, comma-separated, or all")
	fs.StringVar(&o.skip, "skip", "", "the proposal ids to skip, comma-separated")
	fs.BoolVar(&o.markValidated, "mark-validated", false, "mark every accepted Add and Update validated, today")
	fs.BoolVar(&o.all, "all", false, "show the proposals already decided too")
	fs.StringVar(&o.branch, "branch", "", "the branch to commit on (default: takecare/review-<survey>)")
	fs.BoolVar(&o.noCommit, "no-commit", false, "write the files, make no branch and no commit")
}

func (a *app) review(ctx context.Context, args []string) int {
	var o reviewOpts
	fs := a.newFlags("review")
	o.flags(fs)
	if code := a.parse(fs, &o.common, args); code >= 0 {
		return code
	}
	cfg, err := kubeview.Load(o.kubeconfig, o.kctx)
	if err != nil {
		return a.fail("review: %v", err)
	}
	r, err := kubeview.New(cfg)
	if err != nil {
		return a.fail("review: %v", err)
	}
	return a.runReview(ctx, r, &o)
}

func (a *app) runReview(ctx context.Context, r getLister, o *reviewOpts) int {
	s, err := findSurvey(ctx, r, o)
	if err != nil {
		return a.fail("review: %v", err)
	}
	if s.Status.Phase != v1alpha1.RunCompleted {
		return a.fail("review: the survey %s is %s, not Completed: %s", s.Name, cmp.Or(string(s.Status.Phase), "not started"), s.Status.Reason)
	}
	props, notRead, err := a.readProposals(ctx, r, s)
	if err != nil {
		return a.fail("review: %v", err)
	}
	local, err := readMapDir(o.mapDir)
	if err != nil {
		return a.fail("review: %v", err)
	}
	state, err := loadReviewState(o.stateDir)
	if err != nil {
		return a.fail("review: %v", err)
	}
	res := Review{SchemaVersion: reviewSchemaVersion, Survey: s.Name, MapRevision: s.Status.MapRevision, NotRead: notRead,
		Accepted: []string{}, Validated: []string{}, Skipped: []string{}, Written: []string{}, Deleted: []string{}, Faults: []string{}}
	if !o.noCommit {
		// Before any question, so that no answer is lost (spec 13 §7).
		if res.Branch, err = branchReady(ctx, ".", cmp.Or(o.branch, "takecare/review-"+s.Name)); err != nil {
			return a.fail("review: %v", err)
		}
	}
	var shown []*v1alpha1.Proposal
	for _, p := range props {
		if !o.all && state.decided(p) {
			res.Hidden++
			continue
		}
		shown = append(shown, p)
	}
	today := a.now().Format(time.DateOnly)
	var choices map[string]string
	switch {
	case o.accept != "" || o.skip != "":
		choices, err = fromFlags(props, shown, o)
	case !a.terminal:
		return a.fail("review: no terminal to ask on: pass --accept <ids>|all or --skip <ids>")
	default:
		a.revisionNote(ctx, s, local, o.mapDir)
		choices, err = a.ask(shown, local, today)
	}
	if err != nil {
		return a.fail("review: %v", err)
	}

	// The Removes go last, so that their relations are dropped from the
	// entries an Update rewrote too.
	result := maps.Clone(local)
	var ds []decision
	removesLast := slices.Concat(
		slices.DeleteFunc(slices.Clone(props), func(p *v1alpha1.Proposal) bool { return p.Action == v1alpha1.ProposalRemove }),
		slices.DeleteFunc(slices.Clone(props), func(p *v1alpha1.Proposal) bool { return p.Action != v1alpha1.ProposalRemove }))
	for _, p := range removesLast {
		c, ok := choices[p.ID]
		if !ok {
			continue
		}
		if c == decidedSkipped {
			ds = append(ds, decision{ID: p.ID, Decision: c, Digest: digest(p)})
			res.Skipped = append(res.Skipped, p.ID)
			continue
		}
		validated := c == decidedValidated || o.markValidated && p.Action != v1alpha1.ProposalRemove
		if validated {
			c = decidedValidated
			res.Validated = append(res.Validated, p.ID)
		} else {
			res.Accepted = append(res.Accepted, p.ID)
		}
		ds = append(ds, decision{ID: p.ID, Decision: c, Digest: digest(p)})
		if err := apply(result, p, validated, today); err != nil {
			return a.fail("review: %v", err)
		}
	}
	if len(ds) == 0 {
		res.Branch = ""
		return a.printReview(o, &res, ExitOK, "nothing decided: no file written")
	}
	if f := faults(result); len(f) > 0 {
		res.Branch, res.Faults = "", f
		return a.printReview(o, &res, ExitFinding, "the resulting Map does not validate, nothing written:\n  "+strings.Join(f, "\n  "))
	}
	return a.writeReview(ctx, o, s, state, ds, local, result, &res)
}

// printReview prints the result: the JSON object on every path with
// --output json, the text otherwise.
func (a *app) printReview(o *reviewOpts, res *Review, code int, text string) int {
	if o.output == outputJSON {
		if err := a.printJSON(res); err != nil {
			return a.fail("review: %v", err)
		}
		return code
	}
	_, _ = fmt.Fprintln(a.stdout, text)
	return code
}

// branchReady checks, before anything is asked or written, that dir is
// in a git work tree and that the branch the command is to create does
// not exist yet.
func branchReady(ctx context.Context, dir, branch string) (string, error) {
	if _, err := git(ctx, dir, "rev-parse", "--is-inside-work-tree"); err != nil {
		return "", fmt.Errorf("not in a git work tree: run it from the repository, or pass --no-commit: %w", err)
	}
	if _, err := git(ctx, dir, "check-ref-format", "--branch", branch); err != nil {
		return "", fmt.Errorf("%q is not a branch name: %w", branch, err)
	}
	out, err := git(ctx, dir, "branch", "--list", branch)
	if err != nil {
		return "", err
	}
	if out != "" {
		return "", fmt.Errorf("the branch %s exists already, nothing asked: pass --branch <name> for this pass, "+
			"or merge that branch and run the command again from it with --branch", branch)
	}
	return branch, nil
}

// writeReview writes the result and the review record, then makes the
// branch and the commit (spec 13 §7), unless --no-commit.
func (a *app) writeReview(ctx context.Context, o *reviewOpts, s *v1alpha1.TakeCareSurvey, state *reviewState,
	ds []decision, local, result map[string][]byte, res *Review) int {
	diffs := changed(local, result)
	paths := make([]string, 0, len(diffs)+1)
	paths = append(paths, filepath.Join(o.stateDir, reviewStateFile))
	for _, p := range diffs {
		paths = append(paths, filepath.Join(o.mapDir, strings.TrimPrefix(p, "map/")))
		if _, ok := result[p]; ok {
			res.Written = append(res.Written, p)
		} else {
			res.Deleted = append(res.Deleted, p)
		}
	}
	if !o.noCommit {
		d, err := dirty(ctx, ".", paths)
		if err != nil {
			return a.fail("review: %v", err)
		}
		if len(d) > 0 {
			return a.fail("review: the working tree has changes on the paths review writes, nothing written: %s", strings.Join(d, ", "))
		}
		if _, err := git(ctx, ".", "switch", "--create", res.Branch); err != nil {
			return a.fail("review: %v", err)
		}
	}
	if err := writeMap(o.mapDir, local, result); err != nil {
		return a.fail("review: %v", err)
	}
	state.record(s.Name, s.Status.MapRevision, ds)
	raw, err := yaml.Marshal(state)
	if err == nil {
		err = os.MkdirAll(o.stateDir, 0o750)
	}
	if err == nil {
		err = os.WriteFile(paths[0], raw, 0o644) //nolint:gosec // the review record, meant to be committed and read
	}
	if err != nil {
		return a.fail("review: write the review record: %v", err)
	}
	if !o.noCommit {
		if _, err := git(ctx, ".", append([]string{"add", "--all", "--"}, paths...)...); err != nil {
			return a.fail("review: the files are written: %v", err)
		}
		if _, err := git(ctx, ".", append([]string{"commit", "--message", commitMessage(s, res), "--"}, paths...)...); err != nil {
			return a.fail("review: the files are written and staged: %v", err)
		}
	}
	text := fmt.Sprintf("accepted %d, validated %d, skipped %d; %d files written, %d deleted",
		len(res.Accepted), len(res.Validated), len(res.Skipped), len(res.Written), len(res.Deleted))
	if res.Branch != "" {
		text += fmt.Sprintf("\ncommitted on the branch %s; next: push it and open the pull request with your own tools", res.Branch)
	}
	return a.printReview(o, res, ExitOK, text)
}

func commitMessage(s *v1alpha1.TakeCareSurvey, res *Review) string {
	var b strings.Builder
	fmt.Fprintf(&b, "takecare: review %s\n\n", s.Name)
	for _, l := range []struct {
		what string
		ids  []string
	}{{"Accepted", res.Accepted}, {"Accepted and validated", res.Validated}, {"Skipped", res.Skipped}} {
		if len(l.ids) > 0 {
			fmt.Fprintf(&b, "%s: %s\n", l.what, strings.Join(l.ids, ", "))
		}
	}
	fmt.Fprintf(&b, "Map revision of the survey: %s\n", cmp.Or(s.Status.MapRevision, "unknown"))
	return b.String()
}

// writeMap writes the files that changed between local and result,
// through an os.Root on the Map directory, and deletes those result no
// longer holds.
func writeMap(dir string, local, result map[string][]byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open the Map directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	for _, p := range changed(local, result) {
		rel := strings.TrimPrefix(p, "map/")
		raw, ok := result[p]
		if !ok {
			err = root.Remove(rel)
		} else if err = root.MkdirAll(path.Dir(rel), 0o750); err == nil {
			err = root.WriteFile(rel, raw, 0o644) //nolint:gosec // a Map file, meant to be committed and read
		}
		if err != nil {
			return fmt.Errorf("write %s: %w", p, err)
		}
	}
	return nil
}

// findSurvey reads the survey --survey names, or the newest Completed
// survey of the Intendant.
func findSurvey(ctx context.Context, r getLister, o *reviewOpts) (*v1alpha1.TakeCareSurvey, error) {
	if o.survey != "" {
		var s v1alpha1.TakeCareSurvey
		if err := r.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: o.survey}, &s); err != nil {
			return nil, fmt.Errorf("read the survey %s in namespace %s: %w", o.survey, o.namespace, err)
		}
		return &s, nil
	}
	name := o.intendant
	if name == "" {
		var is v1alpha1.TakeCareIntendantList
		if err := r.List(ctx, &is, client.InNamespace(o.namespace)); err != nil {
			return nil, fmt.Errorf("list takecareintendants in namespace %s: %w", o.namespace, err)
		}
		switch len(is.Items) {
		case 0:
			return nil, fmt.Errorf("no TakeCareIntendant in namespace %s", o.namespace)
		case 1:
			name = is.Items[0].Name
		default:
			return nil, fmt.Errorf("%d Intendants in namespace %s: pass --intendant", len(is.Items), o.namespace)
		}
	}
	var ss v1alpha1.TakeCareSurveyList
	if err := r.List(ctx, &ss, client.InNamespace(o.namespace)); err != nil {
		return nil, fmt.Errorf("list takecaresurveys in namespace %s: %w", o.namespace, err)
	}
	var newest *v1alpha1.TakeCareSurvey
	for i := range ss.Items {
		s := &ss.Items[i]
		if s.Spec.IntendantRef.Name != name || s.Status.Phase != v1alpha1.RunCompleted {
			continue
		}
		if newest == nil || s.CreationTimestamp.After(newest.CreationTimestamp.Time) ||
			s.CreationTimestamp.Equal(&newest.CreationTimestamp) && s.Name > newest.Name {
			newest = s
		}
	}
	if newest == nil {
		return nil, fmt.Errorf("no Completed survey of the Intendant %s in namespace %s", name, o.namespace)
	}
	return newest, nil
}

// readProposals reads the full set from the survey's ConfigMap, over the
// proposals its status holds. When the ConfigMap cannot be read, it works
// on the status's and says how many it did not read; it never guesses the
// rest. The proposals come sorted by key.
func (a *app) readProposals(ctx context.Context, r getLister, s *v1alpha1.TakeCareSurvey) ([]*v1alpha1.Proposal, int, error) {
	byID := map[string]*v1alpha1.Proposal{}
	for i := range s.Status.Proposals {
		byID[s.Status.Proposals[i].ID] = &s.Status.Proposals[i]
	}
	if name := s.Status.ProposalsConfigMap; name != "" {
		var cm corev1.ConfigMap
		if err := r.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, &cm); err != nil {
			_, _ = fmt.Fprintf(a.stderr, "takecare: review: could not read the ConfigMap %s, working on the proposals the status holds: %v\n", name, err)
		}
		for _, id := range slices.Sorted(maps.Keys(cm.Data)) {
			var p v1alpha1.Proposal
			if err := yaml.Unmarshal([]byte(cm.Data[id]), &p); err != nil {
				return nil, 0, fmt.Errorf("the ConfigMap %s: proposal %s: %w", name, id, err)
			}
			if p.ID != id {
				return nil, 0, fmt.Errorf("the ConfigMap %s: the key %s holds the proposal %s", name, id, p.ID)
			}
			byID[id] = &p
		}
	}
	notRead := 0
	if c := s.Status.ProposalCounts; c != nil && int(c.Total) > len(byID) {
		notRead = int(c.Total) - len(byID)
		_, _ = fmt.Fprintf(a.stderr, "takecare: review: %d of the survey's %d proposals were not read\n", notRead, c.Total)
	}
	props := slices.Collect(maps.Values(byID))
	slices.SortFunc(props, func(x, y *v1alpha1.Proposal) int {
		return cmp.Or(cmp.Compare(x.Key, y.Key), cmp.Compare(x.Action, y.Action))
	})
	return props, notRead, nil
}

// fromFlags reads the decisions of --accept and --skip. An id must be one
// of the survey's; all accepts every proposal shown.
func fromFlags(props, shown []*v1alpha1.Proposal, o *reviewOpts) (map[string]string, error) {
	known := map[string]bool{}
	for _, p := range props {
		known[p.ID] = true
	}
	choices := map[string]string{}
	accepted := decidedAccepted
	set := func(name, list, choice string) error {
		for id := range strings.SplitSeq(list, ",") {
			id = strings.TrimSpace(id)
			switch {
			case id == "":
			case !known[id]:
				return fmt.Errorf("%s: no proposal %s in the survey", name, id)
			case choices[id] != "":
				return fmt.Errorf("the proposal %s is both accepted and skipped", id)
			default:
				choices[id] = choice
			}
		}
		return nil
	}
	if err := set("--skip", o.skip, decidedSkipped); err != nil {
		return nil, err
	}
	if o.accept == "all" {
		for _, p := range shown {
			if choices[p.ID] == "" {
				choices[p.ID] = accepted
			}
		}
		return choices, nil
	}
	return choices, set("--accept", o.accept, accepted)
}

// revisionNote says when the survey's Map revision is not the local one:
// the diffs are against the local files, which the pull request changes.
func (a *app) revisionNote(ctx context.Context, s *v1alpha1.TakeCareSurvey, local map[string][]byte, dir string) {
	rev := s.Status.MapRevision
	head, _ := git(ctx, dir, "rev-parse", "HEAD")
	if rev != "" && (rev == mapserver.Revision(local) || head != "" && strings.HasPrefix(head, rev)) {
		return
	}
	_, _ = fmt.Fprintf(a.stdout, "The survey compared the ground with the Map revision %q; the local Map is at %s (HEAD %s). The diffs below are against the local files.\n\n",
		rev, mapserver.Revision(local), cmp.Or(head, "unknown"))
}

// ask shows one screen per proposal and reads the person's choice.
func (a *app) ask(shown []*v1alpha1.Proposal, local map[string][]byte, today string) (map[string]string, error) {
	choices := map[string]string{}
	in := bufio.NewScanner(a.stdin)
	for i, p := range shown {
		preview := maps.Clone(local)
		if err := apply(preview, p, false, today); err != nil {
			return nil, err
		}
		d, err := diff(local, preview)
		if err != nil {
			return nil, err
		}
		_, _ = fmt.Fprintf(a.stdout, "Proposal %d of %d: %s\n  action    %s\n  key       %s\n  source    %s\n  evidence  %s\n\n%s\n",
			i+1, len(shown), printable(p.ID), p.Action, printable(string(p.Key)), p.Source,
			printable(cmp.Or(strings.Join(p.Evidence, ", "), "none")), printable(d))
		for {
			_, _ = fmt.Fprint(a.stdout, "[a] accept  [v] accept and mark validated  [s] skip  [l] later  [q] stop, the rest later: ")
			if !in.Scan() {
				return choices, in.Err()
			}
			answer := strings.TrimSpace(in.Text())
			if c, ok := map[string]string{"a": decidedAccepted, "v": decidedValidated, "s": decidedSkipped}[answer]; ok {
				if c == decidedValidated && p.Action == v1alpha1.ProposalRemove {
					c = decidedAccepted
				}
				choices[p.ID] = c
			} else if answer == "q" {
				return choices, nil
			} else if answer != "l" {
				continue
			}
			_, _ = fmt.Fprintln(a.stdout)
			break
		}
	}
	return choices, nil
}

// printable drops the control characters, line breaks and tabs aside, of
// what the cluster holds, before it is printed on a terminal.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
