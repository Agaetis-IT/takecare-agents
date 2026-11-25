// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package questionnaire

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
)

// MaxOutputSize is the most bytes one output renders to (spec 13 §4.3).
const MaxOutputSize = 1 << 20

// Rendered is one output, rendered.
type Rendered struct {
	Path string
	Data []byte
}

// Render renders every output with data (Set.Data), and returns every
// fault at once as Faults; nothing is returned then.
func (s *Set) Render(data map[string]any) ([]Rendered, error) {
	var (
		out    []Rendered
		faults Faults
	)
	for _, o := range s.Outputs {
		w := &limited{max: MaxOutputSize}
		if err := o.tmpl.Execute(w, data); err != nil {
			faults = append(faults, Fault{o.file, fmt.Sprintf("output %s: %v", o.Path, err)})
			continue
		}
		out = append(out, Rendered{Path: o.Path, Data: w.buf.Bytes()})
	}
	if len(faults) > 0 {
		return nil, faults
	}
	return out, nil
}

// limited is a buffer that refuses to grow past max bytes.
type limited struct {
	buf bytes.Buffer
	max int
}

var errTooLarge = fmt.Errorf("renders to more than %d bytes", MaxOutputSize)

func (l *limited) Write(p []byte) (int, error) {
	if l.buf.Len()+len(p) > l.max {
		return 0, errTooLarge
	}
	return l.buf.Write(p)
}

// Write writes the rendered outputs into dir, through an os.Root opened on
// it: a symbolic link cannot lead a write outside dir. Each path is checked
// again first, and nothing is written when one fails.
func Write(dir string, outputs []Rendered) error {
	for _, o := range outputs {
		if err := CheckPath(o.Path); err != nil {
			return fmt.Errorf("output %q: %w", o.Path, err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open the repository directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	// A link out of dir is found before the first write, not half-way.
	for _, o := range outputs {
		if err := reachable(root, o.Path); err != nil {
			return fmt.Errorf("write %s: %w", o.Path, err)
		}
	}
	for _, o := range outputs {
		if d := path.Dir(o.Path); d != "." {
			if err := root.MkdirAll(d, 0o755); err != nil {
				return fmt.Errorf("write %s: %w", o.Path, err)
			}
		}
		if err := root.WriteFile(o.Path, o.Data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", o.Path, err)
		}
	}
	return nil
}

// reachable stats p, or its deepest existing parent, through root: a
// symbolic link that leads outside root fails there.
func reachable(root *os.Root, p string) error {
	for cur := p; cur != "."; cur = path.Dir(cur) {
		_, err := root.Stat(cur)
		if err == nil {
			return nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}
