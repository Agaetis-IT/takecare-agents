// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package proposeserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// maxIncludes is GitLab's bound on the files a pipeline includes ("Use
// CI/CD configuration from other files"): past it, the server cannot tell.
const maxIncludes = 150

const defaultCIConfig = ".gitlab-ci.yml"

// ciPatterns reads the project's CI configuration at the head (spec 12
// §6.3.2): its file when it is in this project, then the files it includes
// locally, nested ones and those a wildcard matches included, each a path
// or a pattern to refuse. Whenever it cannot tell, CIConfigUnknown.
func (g *gitlab) ciPatterns(ctx context.Context, h head) ([]string, error) {
	if h.ciConfigPath == nil {
		return nil, refuse(CodeCIConfigUnknown, "the project's answer has no ci_config_path")
	}
	root, local, err := g.configFile(*h.ciConfigPath)
	if err != nil || !local {
		return nil, err
	}
	r := &ciReader{g: g, h: h, seen: map[string]bool{root: true}, patterns: []string{root}}
	content, found, err := g.raw(ctx, h, root)
	if err != nil {
		return nil, refuse(CodeCIConfigUnknown, "read %s: %v", root, err)
	}
	if !found {
		if *h.ciConfigPath == "" {
			return r.patterns, nil // no CI configuration
		}
		return nil, refuse(CodeCIConfigUnknown, "the configured %s cannot be read", root)
	}
	if err := r.follow(ctx, root, content); err != nil {
		return nil, err
	}
	return r.patterns, nil
}

// configFile reads ci_config_path: empty is .gitlab-ci.yml; a path, or a
// path@ this project, is in this repository; a URL or another project's
// file is outside it, which the token, scoped to this project, cannot
// write.
func (g *gitlab) configFile(p string) (string, bool, error) {
	if p == "" {
		return defaultCIConfig, true, nil
	}
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return "", false, nil
	}
	file, project, other := strings.Cut(p, "@")
	if other {
		if strings.Contains(project, ":") {
			// Another ref than the default branch's head: what it holds
			// is not what the branch reads.
			name, _, _ := strings.Cut(project, ":")
			if strings.EqualFold(name, g.project) {
				return "", false, refuse(CodeCIConfigUnknown, "ci_config_path %q names a ref of this project", p)
			}
			return "", false, nil
		}
		if !strings.EqualFold(project, g.project) {
			return "", false, nil
		}
	}
	clean, err := ciPath(file)
	if err != nil {
		return "", false, err
	}
	return clean, true, nil
}

// ciPath checks a path named by the configuration: relative to the
// repository's root, a leading / allowed, no variable, no . or ..
func ciPath(p string) (string, error) {
	if strings.Contains(p, "$") {
		return "", refuse(CodeCIConfigUnknown, "the include %q holds a variable", p)
	}
	clean := strings.TrimPrefix(p, "/")
	for el := range strings.SplitSeq(clean, "/") {
		if el == "" || el == "." || el == ".." {
			return "", refuse(CodeCIConfigUnknown, "the include %q is not a path inside the repository", p)
		}
	}
	return clean, nil
}

type ciReader struct {
	g        *gitlab
	h        head
	seen     map[string]bool
	patterns []string
	included int
}

// follow parses a configuration file and reads each file it includes.
func (r *ciReader) follow(ctx context.Context, name string, content []byte) error {
	locals, err := includes(content, r.g.project)
	if err != nil {
		return refuse(CodeCIConfigUnknown, "%s: %v", name, err)
	}
	for _, l := range locals {
		p, err := ciPath(l)
		if err != nil {
			return err
		}
		files := []string{p}
		if strings.Contains(p, "*") {
			r.patterns = append(r.patterns, p)
			if files, err = r.matching(ctx, p); err != nil {
				return err
			}
		}
		for _, f := range files {
			if r.seen[f] {
				continue
			}
			r.seen[f] = true
			r.patterns = append(r.patterns, f)
			if r.included++; r.included > maxIncludes {
				return refuse(CodeCIConfigUnknown, "more than %d included files", maxIncludes)
			}
			c, found, err := r.g.raw(ctx, r.h, f)
			if err != nil || !found {
				return refuse(CodeCIConfigUnknown, "%s, included by %s, cannot be read", f, name)
			}
			if err := r.follow(ctx, f, c); err != nil {
				return err
			}
		}
	}
	return nil
}

// matching lists the files a wildcard include matches, from the directory
// before its first wildcard.
func (r *ciReader) matching(ctx context.Context, pattern string) ([]string, error) {
	dir := pattern[:strings.Index(pattern, "*")]
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		dir = dir[:i]
	} else {
		dir = ""
	}
	entries, err := r.g.tree(ctx, r.h, dir, true)
	if err != nil {
		return nil, refuse(CodeCIConfigUnknown, "list %q for the include %s: %v", dir, pattern, err)
	}
	var out []string
	for _, e := range entries {
		if e.mode != modeTree && matchCI(pattern, e.name) {
			out = append(out, e.name)
		}
	}
	return out, nil
}

// matchCI is GitLab's include matching: * within a directory, ** across
// directories; a path without a wildcard is itself.
func matchCI(pattern, p string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == p
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch {
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(".*")
			i++
		case pattern[i] == '*':
			b.WriteString("[^/]*")
		default:
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String()).MatchString(p)
}

// includes are the local files a configuration includes: its top-level
// include and its jobs' trigger:include (a child pipeline's
// configuration), in every YAML document. Remote, template, component,
// artifact and other projects' files are outside the repository; a
// project file of this project is local. A kind or a key it does not know
// is an error.
func includes(content []byte, project string) ([]string, error) {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	var out []string
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("parse: %w", err)
		}
		if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
			continue
		}
		top := doc.Content[0]
		for i := 0; i+1 < len(top.Content); i += 2 {
			key, val := top.Content[i].Value, top.Content[i+1]
			if key == "include" {
				l, err := includeNode(val, project)
				if err != nil {
					return nil, err
				}
				out = append(out, l...)
				continue
			}
			if trig := mapValue(val, "trigger"); trig != nil {
				if inc := mapValue(trig, "include"); inc != nil {
					l, err := includeNode(inc, project)
					if err != nil {
						return nil, err
					}
					out = append(out, l...)
				}
			}
		}
	}
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func includeNode(n *yaml.Node, project string) ([]string, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		if strings.HasPrefix(n.Value, "http://") || strings.HasPrefix(n.Value, "https://") {
			return nil, nil
		}
		return []string{n.Value}, nil
	case yaml.SequenceNode:
		var out []string
		for _, item := range n.Content {
			l, err := includeNode(item, project)
			if err != nil {
				return nil, err
			}
			out = append(out, l...)
		}
		return out, nil
	case yaml.MappingNode:
		return includeMap(n, project)
	default:
		return nil, fmt.Errorf("an include of a kind the server does not know (line %d)", n.Line)
	}
}

// includeKeys are the keys an include may carry besides its kind.
var includeKeys = map[string]bool{"rules": true, "inputs": true, "ref": true, "file": true, "job": true, "integrity": true, "cache": true}

func includeMap(n *yaml.Node, project string) ([]string, error) {
	kind := ""
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i].Value
		switch k {
		case "local", "project", "remote", "template", "component", "artifact":
			if kind != "" {
				return nil, fmt.Errorf("an include with both %s and %s (line %d)", kind, k, n.Line)
			}
			kind = k
		default:
			if !includeKeys[k] {
				return nil, fmt.Errorf("an include key the server does not know: %s (line %d)", k, n.Line)
			}
		}
	}
	switch kind {
	case "local":
		v := mapValue(n, "local")
		if v.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("include:local is not a path (line %d)", n.Line)
		}
		return []string{v.Value}, nil
	case "project":
		v := mapValue(n, "project")
		if v.Kind != yaml.ScalarNode || strings.Contains(v.Value, "$") {
			return nil, fmt.Errorf("include:project is not a project path (line %d)", n.Line)
		}
		if !strings.EqualFold(strings.Trim(v.Value, "/"), project) {
			return nil, nil
		}
		// This project's own files, perhaps at another ref: refused all
		// the same, and only readable at the head.
		if mapValue(n, "ref") != nil {
			return nil, fmt.Errorf("include:project names this project at a ref (line %d)", n.Line)
		}
		files := mapValue(n, "file")
		if files == nil {
			return nil, fmt.Errorf("include:project without file (line %d)", n.Line)
		}
		var out []string
		switch files.Kind {
		case yaml.ScalarNode:
			out = append(out, files.Value)
		case yaml.SequenceNode:
			for _, f := range files.Content {
				if f.Kind != yaml.ScalarNode {
					return nil, fmt.Errorf("include:file is not a path (line %d)", f.Line)
				}
				out = append(out, f.Value)
			}
		default:
			return nil, fmt.Errorf("include:file is not a path (line %d)", n.Line)
		}
		return out, nil
	case "remote", "template", "component", "artifact":
		return nil, nil
	default:
		return nil, fmt.Errorf("an include of a kind the server does not know (line %d)", n.Line)
	}
}
