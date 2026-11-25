// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/agaetis-it/takecare-agents/internal/questionnaire"
	"github.com/agaetis-it/takecare-agents/internal/questionnaire/builtin"
	"github.com/agaetis-it/takecare-agents/internal/tui"
)

// initSchemaVersion versions the shape of init --output json.
const initSchemaVersion = 1

const answersFile = "answers.yaml"

// Init is what init prints once done.
type Init struct {
	SchemaVersion int `json:"schemaVersion"`
	// Asked are the ids asked on this run; Unknown those answered "I
	// don't know", on this run or before.
	Asked   []string `json:"asked"`
	Unknown []string `json:"unknown"`
	// Written are the files written, Unchanged those already as rendered,
	// Kept those a person edited and chose to keep.
	Written   []string `json:"written"`
	Unchanged []string `json:"unchanged"`
	Kept      []string `json:"kept"`
	Branch    string   `json:"branch,omitempty"`
}

type initOpts struct {
	common
	answers, dir, mapDir, resourcesDir, stateDir, branch string
	questions                                            multi
	overwrite, noCommit                                  bool
}

// multi is a repeatable string flag.
type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func (a *app) init(ctx context.Context, args []string) int {
	var o initOpts
	flags := a.newFlags("init")
	o.register(flags)
	flags.StringVar(&o.answers, "answers", "", "an answers file: run without asking")
	flags.Var(&o.questions, "questions", "a directory of extra question files (repeatable)")
	flags.StringVar(&o.dir, "dir", ".", "the repository directory")
	flags.StringVar(&o.mapDir, "map-dir", "map", "the Map directory, in the repository")
	flags.StringVar(&o.resourcesDir, "resources-dir", "takecare", "the directory of the chart values and resources, in the repository")
	flags.StringVar(&o.stateDir, "state-dir", ".takecare", "the directory of the answers record, in the repository")
	flags.BoolVar(&o.overwrite, "overwrite", false, "overwrite the files a person edited since the last run")
	flags.BoolVar(&o.noCommit, "no-commit", false, "write the files, make no branch and no commit")
	flags.StringVar(&o.branch, "branch", "", "the branch to commit on (default: takecare/init-<date>)")
	if code := a.parse(flags, &o.common, args); code >= 0 {
		return code
	}
	o.accessible = o.accessible || accessibleEnv(os.Getenv("TAKECARE_ACCESSIBLE"))
	if err := a.runInit(ctx, &o); err != nil {
		if _, ok := errors.AsType[findings](err); ok {
			_, _ = fmt.Fprintf(a.stderr, "takecare: init: nothing written:\n  %s\n", strings.ReplaceAll(err.Error(), "\n", "\n  "))
			return ExitError
		}
		return a.fail("init: %v", err)
	}
	return ExitOK
}

// accessibleEnv reads TAKECARE_ACCESSIBLE.
func accessibleEnv(v string) bool { return v == "1" || strings.EqualFold(v, "true") }

// findings wraps the faults of the question files or of the rendered
// files, printed one per line.
type findings struct{ error }

func (a *app) runInit(ctx context.Context, o *initOpts) error {
	set, err := loadSet(o)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(o.dir)
	if err != nil {
		return fmt.Errorf("open the repository directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	statePath := path.Join(o.stateDir, answersFile)
	state, prev, err := readAnswers(root, set, statePath)
	if err != nil {
		return err
	}
	today := a.now().Format(time.DateOnly)
	res := Init{SchemaVersion: initSchemaVersion, Asked: []string{}, Unknown: []string{},
		Written: []string{}, Unchanged: []string{}, Kept: []string{}}
	if !o.noCommit {
		// Before any question, so that no answer is lost (spec 13 §7).
		if res.Branch, err = branchReady(ctx, o.dir, cmp.Or(o.branch, "takecare/init-"+today)); err != nil {
			return err
		}
	}
	// With --output json, stdout holds the result alone.
	screen := tui.Screen{In: a.stdin, Out: a.stdout, Accessible: o.accessible, NoColor: o.noColor}
	if o.output == outputJSON {
		screen.Out = a.stderr
	}
	answers, err := a.gather(ctx, o, screen, set, state, prev, &res)
	if err != nil {
		return err
	}
	created := cmp.Or(state.Created, today)
	outputs, err := render(set, answers, o, created)
	if err != nil {
		return err
	}
	if err := questionnaire.Check(o.dir, o.mapDir, outputs); err != nil {
		if _, ok := errors.AsType[questionnaire.Faults](err); ok {
			return findings{err}
		}
		return err
	}
	// What the previous answers render to: a file that differs from it
	// was edited by a person since.
	before := map[string][]byte{}
	if len(prev) > 0 {
		if out, err := render(set, prev, o, created); err == nil {
			for _, r := range out {
				before[r.Path] = r.Data
			}
		}
	}
	write, local, edited, err := compare(root, outputs, before, &res)
	if err != nil {
		return err
	}
	if err := a.settle(ctx, o, screen, write, local, edited, len(prev) > 0, &res); err != nil {
		return err
	}
	state.QuestionSet, state.Created = builtin.Version, created
	raw, err := questionnaire.EncodeAnswers(set, state, answers)
	if err != nil {
		return fmt.Errorf("write the answers record: %w", err)
	}
	return a.writeInit(ctx, o, root, statePath, raw, write, &res, answers)
}

// loadSet checks the directories and loads the built-in question set
// with the files --questions gives.
func loadSet(o *initOpts) (*questionnaire.Set, error) {
	for _, d := range []struct{ flag, dir string }{{"--map-dir", o.mapDir}, {"--resources-dir", o.resourcesDir}, {"--state-dir", o.stateDir}} {
		if !filepath.IsLocal(d.dir) {
			return nil, fmt.Errorf("%s %q is not inside the repository directory", d.flag, d.dir)
		}
	}
	o.mapDir, o.resourcesDir, o.stateDir = filepath.ToSlash(o.mapDir), filepath.ToSlash(o.resourcesDir), filepath.ToSlash(o.stateDir)
	var extra []questionnaire.Source
	for _, d := range o.questions {
		src, err := questionnaire.ReadDir(d)
		if err != nil {
			return nil, err
		}
		extra = append(extra, src...)
	}
	set, err := questionnaire.Load(builtin.Sources(), extra)
	if err != nil {
		return nil, findings{err}
	}
	return set, nil
}

// gather returns the answers: the previous ones, those --answers gives,
// and those it asks for, which are the questions shown without an answer
// and those whose when changed since they were answered. Without a
// terminal, or with --answers, it asks nothing.
func (a *app) gather(ctx context.Context, o *initOpts, screen tui.Screen, set *questionnaire.Set,
	state *questionnaire.AnswersFile, prev map[string]questionnaire.Answer, res *Init) (map[string]questionnaire.Answer, error) {
	answers := maps.Clone(prev)
	given := map[string]bool{}
	if o.answers != "" {
		raw, err := os.ReadFile(o.answers)
		if err != nil {
			return nil, fmt.Errorf("read the answers file: %w", err)
		}
		_, in, err := questionnaire.DecodeAnswers(set, raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", o.answers, err)
		}
		for id, ans := range in {
			answers[id], given[id] = ans, true
		}
	}
	var missing []string
	for _, q := range set.Questions {
		if _, ok := answers[q.ID]; !set.Shown(q, answers) || ok && (given[q.ID] || state.When[q.ID] == questionnaire.WhenOf(q)) {
			continue
		}
		if o.answers != "" || !a.terminal {
			missing = append(missing, q.ID)
			continue
		}
		ans, err := screen.Ask(ctx, q)
		if err != nil {
			return nil, err
		}
		answers[q.ID] = ans
		res.Asked = append(res.Asked, q.ID)
	}
	if len(missing) > 0 {
		how := "pass --answers <file>"
		if o.answers != "" {
			how = "add them to " + o.answers
		}
		return nil, fmt.Errorf("no answer to %s: %s (a question you cannot answer goes under unknown)", strings.Join(missing, ", "), how)
	}
	return answers, nil
}

// settle shows the diffs and decides on the files a person edited: kept
// or overwritten when asked, overwritten with --overwrite, and otherwise
// a stop before writing (spec 13 §3.6).
func (a *app) settle(ctx context.Context, o *initOpts, screen tui.Screen, write, local map[string][]byte,
	edited []string, rerun bool, res *Init) error {
	if len(edited) == 0 || o.overwrite {
		if d, _ := diff(local, write); d != "" && rerun {
			_, _ = fmt.Fprint(screen.Out, d)
		}
		return nil
	}
	if o.answers != "" || !a.terminal {
		d, _ := diff(local, pick(write, edited))
		_, _ = fmt.Fprint(screen.Out, d)
		return fmt.Errorf("edited since the last run, nothing written: %s; pass --overwrite to replace them", strings.Join(edited, ", "))
	}
	for _, p := range edited {
		d, _ := diff(map[string][]byte{p: local[p]}, map[string][]byte{p: write[p]})
		_, _ = fmt.Fprint(screen.Out, d)
		yes, err := screen.Confirm(ctx, "Overwrite "+p+"?", "A person edited it since the last run; the diff is above.")
		if err != nil {
			return err
		}
		if !yes {
			delete(write, p)
			res.Kept = append(res.Kept, p)
		}
	}
	return nil
}

// render renders the skeleton and the question files' outputs, checks
// every path and bound, and refuses one under the state directory.
func render(set *questionnaire.Set, answers map[string]questionnaire.Answer, o *initOpts, created string) ([]questionnaire.Rendered, error) {
	data, err := set.Data(answers)
	if err != nil {
		return nil, err
	}
	extra, err := set.Render(data)
	if err != nil {
		return nil, findings{err}
	}
	out, err := builtin.Skeleton(set, answers, builtin.Dirs{Map: o.mapDir, Resources: o.resourcesDir}, created)
	if err != nil {
		return nil, err
	}
	out = append(out, extra...)
	if len(out) > questionnaire.MaxOutputs {
		return nil, fmt.Errorf("%d files to write, more than %d", len(out), questionnaire.MaxOutputs)
	}
	seen := map[string]bool{}
	var faults questionnaire.Faults
	for _, r := range out {
		switch err := questionnaire.CheckPath(r.Path); {
		case err != nil:
			faults = append(faults, questionnaire.Fault{File: r.Path, Msg: err.Error()})
		case seen[r.Path]:
			faults = append(faults, questionnaire.Fault{File: r.Path, Msg: "written by two outputs"})
		case strings.HasPrefix(strings.ToLower(r.Path), strings.ToLower(path.Clean(o.stateDir))+"/"):
			faults = append(faults, questionnaire.Fault{File: r.Path, Msg: "under the state directory " + o.stateDir})
		case len(r.Data) > questionnaire.MaxOutputSize:
			faults = append(faults, questionnaire.Fault{File: r.Path, Msg: fmt.Sprintf("more than %d bytes", questionnaire.MaxOutputSize)})
		}
		seen[r.Path] = true
	}
	if len(faults) > 0 {
		return nil, findings{faults}
	}
	return out, nil
}

// compare reads the files the outputs go to and sorts the outputs: those
// already as rendered are left out, and so are those a person edited
// where init renders what it rendered last time; the others are to write,
// and among them those a person edited since the last run (they differ
// from what the previous answers render to, or were never written by
// init).
func compare(root *os.Root, outputs []questionnaire.Rendered, before map[string][]byte, res *Init) (write, local map[string][]byte, edited []string, err error) {
	write, local = map[string][]byte{}, map[string][]byte{}
	for _, r := range outputs {
		disk, err := root.ReadFile(r.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			write[r.Path] = r.Data
			continue
		case err != nil:
			return nil, nil, nil, fmt.Errorf("read %s: %w", r.Path, err)
		case bytes.Equal(disk, r.Data):
			res.Unchanged = append(res.Unchanged, r.Path)
			continue
		}
		prev, ok := before[r.Path]
		if ok && bytes.Equal(prev, r.Data) {
			// A person edited it, and init has nothing new to write there.
			res.Kept = append(res.Kept, r.Path)
			continue
		}
		write[r.Path], local[r.Path] = r.Data, disk
		if !ok || !bytes.Equal(prev, disk) {
			edited = append(edited, r.Path)
		}
	}
	return write, local, edited, nil
}

// pick keeps the entries of m that keys names.
func pick(m map[string][]byte, keys []string) map[string][]byte {
	out := map[string][]byte{}
	for _, k := range keys {
		out[k] = m[k]
	}
	return out
}

// readAnswers reads the answers record, an empty one when there is none.
func readAnswers(root *os.Root, set *questionnaire.Set, p string) (*questionnaire.AnswersFile, map[string]questionnaire.Answer, error) {
	raw, err := root.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return &questionnaire.AnswersFile{}, map[string]questionnaire.Answer{}, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read the answers record: %w", err)
	}
	f, answers, err := questionnaire.DecodeAnswers(set, raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", p, err)
	}
	return f, answers, nil
}

// writeInit writes the files and the answers record, then makes the
// branch and the commit (spec 13 §7), unless --no-commit.
func (a *app) writeInit(ctx context.Context, o *initOpts, root *os.Root, statePath string, state []byte,
	write map[string][]byte, res *Init, answers map[string]questionnaire.Answer) error {
	paths := slices.Sorted(maps.Keys(write))
	all := append(slices.Clone(paths), statePath)
	if old, err := root.ReadFile(statePath); len(paths) == 0 && err == nil && bytes.Equal(old, state) {
		res.Branch = ""
		return a.printInit(o, res, "nothing changed: no file written")
	}
	if !o.noCommit {
		d, err := dirty(ctx, o.dir, all)
		if err != nil {
			return err
		}
		if len(d) > 0 {
			return fmt.Errorf("the working tree has changes on the paths init writes, nothing written: %s", strings.Join(d, ", "))
		}
		if _, err := git(ctx, o.dir, "switch", "--create", res.Branch); err != nil {
			return err
		}
	}
	out := make([]questionnaire.Rendered, 0, len(paths))
	for _, p := range paths {
		out = append(out, questionnaire.Rendered{Path: p, Data: write[p]})
	}
	if err := questionnaire.Write(o.dir, out); err != nil {
		return err
	}
	err := root.MkdirAll(o.stateDir, 0o750)
	if err == nil {
		err = root.WriteFile(statePath, state, 0o644) //nolint:gosec // the answers record, meant to be committed and read
	}
	if err != nil {
		return fmt.Errorf("write the answers record: %w", err)
	}
	res.Written = paths
	for id, ans := range answers {
		if ans.Unknown {
			res.Unknown = append(res.Unknown, id)
		}
	}
	slices.Sort(res.Unknown)
	if !o.noCommit {
		if _, err := git(ctx, o.dir, append([]string{"add", "--all", "--"}, all...)...); err != nil {
			return fmt.Errorf("the files are written: %w", err)
		}
		msg := fmt.Sprintf("takecare: init\n\nQuestion set version: %d\n", builtin.Version)
		if _, err := git(ctx, o.dir, append([]string{"commit", "--message", msg, "--"}, all...)...); err != nil {
			return fmt.Errorf("the files are written and staged: %w", err)
		}
	}
	text := fmt.Sprintf("%d files written, %d unchanged, %d kept as edited", len(res.Written), len(res.Unchanged), len(res.Kept))
	if len(res.Unknown) > 0 {
		text += fmt.Sprintf("\nanswered \"I don't know\": %s; the files hold a TODO where those answers go", strings.Join(res.Unknown, ", "))
	}
	if res.Branch != "" {
		text += fmt.Sprintf("\ncommitted on the branch %s; next: push it and open the pull request with your own tools", res.Branch)
	}
	return a.printInit(o, res, text)
}

// printInit prints the result: the JSON object with --output json, the
// text otherwise.
func (a *app) printInit(o *initOpts, res *Init, text string) error {
	if o.output == outputJSON {
		return a.printJSON(res)
	}
	_, err := fmt.Fprintln(a.stdout, text)
	return err
}
