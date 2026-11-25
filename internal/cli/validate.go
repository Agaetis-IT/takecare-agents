// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/agaetis-it/takecare-agents/internal/mapserver"
	"github.com/agaetis-it/takecare-agents/internal/questionnaire"
	"github.com/agaetis-it/takecare-agents/internal/questionnaire/builtin"
)

// validateSchemaVersion versions the shape of validate --output json.
const validateSchemaVersion = 1

// Validation is what validate prints.
type Validation struct {
	SchemaVersion int    `json:"schemaVersion"`
	MapDir        string `json:"mapDir"`
	Valid         bool   `json:"valid"`
	Entries       int    `json:"entries"`
	// Faults are every fault at once, each naming its file as
	// map/<type>/<name>.yaml.
	Faults []string `json:"faults"`
	// QuestionsDir is the directory of question files --questions gave,
	// and Questions what it holds once loaded with the built-in set.
	QuestionsDir string `json:"questionsDir,omitempty"`
	Questions    int    `json:"questions,omitempty"`
	Outputs      int    `json:"outputs,omitempty"`
	// QuestionFaults are every fault of every question file at once, each
	// naming its file.
	QuestionFaults []string `json:"questionFaults,omitempty"`
}

func (a *app) validate(args []string) int {
	var (
		c            common
		mapDir       string
		questionsDir string
	)
	flags := a.newFlags("validate")
	c.register(flags)
	flags.StringVar(&mapDir, "map-dir", "map", "the Map directory")
	flags.StringVar(&questionsDir, "questions", "", "also check the question files of this directory, without running them")
	if code := a.parse(flags, &c, args); code >= 0 {
		return code
	}
	files, err := readMapDir(mapDir)
	if err != nil {
		return a.fail("validate: %v", err)
	}
	v := Validation{SchemaVersion: validateSchemaVersion, MapDir: mapDir, Faults: faults(files)}
	if v.Valid = len(v.Faults) == 0; v.Valid {
		v.Entries = entries(files)
	}
	if questionsDir != "" {
		if err := checkQuestions(questionsDir, &v); err != nil {
			return a.fail("validate: %v", err)
		}
		v.Valid = v.Valid && len(v.QuestionFaults) == 0
	}
	if c.output == outputJSON {
		err = a.printJSON(v)
	} else {
		err = a.printValidation(v)
	}
	if err != nil {
		return a.fail("validate: %v", err)
	}
	if !v.Valid {
		return ExitFinding
	}
	return ExitOK
}

// checkQuestions loads the question files of dir with the built-in set
// (an id of theirs may not redefine one of it),
// every fault at once. A directory without a question file is a fault: a
// check that read nothing did not pass.
func checkQuestions(dir string, v *Validation) error {
	v.QuestionsDir = dir
	extra, err := questionnaire.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(extra) == 0 {
		v.QuestionFaults = []string{dir + ": no .yaml or .yml question file"}
		return nil
	}
	set, err := questionnaire.Load(builtin.Sources(), extra)
	var faults questionnaire.Faults
	switch {
	case errors.As(err, &faults):
		for _, f := range faults {
			v.QuestionFaults = append(v.QuestionFaults, f.String())
		}
	case err != nil:
		return err
	default:
		v.Questions, v.Outputs = len(set.Questions), len(set.Outputs)
	}
	return nil
}

func (a *app) printValidation(v Validation) error {
	var b strings.Builder
	if len(v.Faults) == 0 {
		fmt.Fprintf(&b, "%s: valid, %d entries\n", v.MapDir, v.Entries)
	} else {
		fmt.Fprintf(&b, "%s: %d faults\n  %s\n", v.MapDir, len(v.Faults), strings.Join(v.Faults, "\n  "))
	}
	switch {
	case v.QuestionsDir == "":
	case len(v.QuestionFaults) == 0:
		fmt.Fprintf(&b, "%s: valid, %d questions, %d outputs\n", v.QuestionsDir, v.Questions, v.Outputs)
	default:
		fmt.Fprintf(&b, "%s: %d faults\n  %s\n", v.QuestionsDir, len(v.QuestionFaults), strings.Join(v.QuestionFaults, "\n  "))
	}
	_, err := io.WriteString(a.stdout, b.String())
	return err
}

// faults runs the Map server's checks over files, every fault at once.
func faults(files map[string][]byte) []string {
	_, errs := mapserver.Check(files)
	out := make([]string, 0, len(errs))
	for _, err := range errs {
		out = append(out, err.Error())
	}
	return out
}

// entries counts the entry files, the README left out.
func entries(files map[string][]byte) int {
	n := 0
	for p := range files {
		if p != mapReadme {
			n++
		}
	}
	return n
}

const mapReadme = "map/README.md"

// readMapDir reads the files of a Map directory as the Map server does,
// by path under map/, dotfiles left out. It reads through an os.Root, so
// that a symbolic link cannot lead it outside the directory.
func readMapDir(dir string) (map[string][]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open the Map directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	fsys := root.FS()
	files := map[string][]byte{}
	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case p != "." && strings.HasPrefix(d.Name(), "."):
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		case d.IsDir():
			return nil
		}
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		files[path.Join("map", p)] = raw
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the Map directory %s: %w", dir, err)
	}
	return files, nil
}
