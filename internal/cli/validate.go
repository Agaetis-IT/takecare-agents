// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/agaetis-it/takecare-agents/internal/mapserver"
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
}

func (a *app) validate(args []string) int {
	var (
		c      common
		mapDir string
	)
	flags := a.newFlags("validate")
	c.register(flags)
	flags.StringVar(&mapDir, "map-dir", "map", "the Map directory")
	if code := a.parse(flags, &c, args); code >= 0 {
		return code
	}
	files, err := readMapDir(mapDir)
	if err != nil {
		return a.fail("validate: %v", err)
	}
	v := Validation{SchemaVersion: validateSchemaVersion, MapDir: mapDir, Faults: faults(files)}
	v.Valid = len(v.Faults) == 0
	if v.Valid {
		v.Entries = entries(files)
	}
	if c.output == outputJSON {
		err = a.printJSON(v)
	} else if v.Valid {
		_, err = fmt.Fprintf(a.stdout, "%s: valid, %d entries\n", mapDir, v.Entries)
	} else {
		_, err = fmt.Fprintf(a.stdout, "%s: %d faults\n  %s\n", mapDir, len(v.Faults), strings.Join(v.Faults, "\n  "))
	}
	if err != nil {
		return a.fail("validate: %v", err)
	}
	if !v.Valid {
		return ExitFinding
	}
	return ExitOK
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
