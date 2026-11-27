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
	root, literal, err := g.configFile(*h.ciConfigPath)
	if err != nil || root == "" {
		return literal, err
	}
	r := &ciReader{g: g, h: h, seen: map[string]bool{root: true}, patterns: append([]string{root}, literal...)}
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

// externalCIRE is GitLab's test for another project's configuration file
// (lib/gitlab/ci/project_config/project_setting.rb, external_project_path?).
var externalCIRE = regexp.MustCompile(`\A.+(yml|yaml)@.+\z`)

// configFile reads ci_config_path the way GitLab does (project_setting.rb):
// empty is .gitlab-ci.yml; a URL is remote; a value externalCIRE matches is
// file@project[:ref], split at its first @; any other value is this
// repository's file as a whole, an @ included. It returns the file to read
// in this repository, empty when the configuration is outside it (the
// token, scoped to this project, cannot write there), and the paths to
// refuse besides: the value itself when it names another project's file,
// in case a file of that very name sits here.
func (g *gitlab) configFile(p string) (string, []string, error) {
	if p == "" {
		return defaultCIConfig, nil, nil
	}
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return "", nil, nil
	}
	if lower := strings.ToLower(p); strings.Contains(lower, "http:") || strings.Contains(lower, "https:") {
		// GitLab looks for a URL anywhere in the value: the server cannot
		// tell whether it reads a remote file or this one.
		return "", nil, refuse(CodeCIConfigUnknown, "ci_config_path %q may be read as a URL", p)
	}
	if !externalCIRE.MatchString(p) {
		clean, err := ciPath(p)
		if err != nil {
			return "", nil, err
		}
		return clean, nil, nil
	}
	literal := []string{strings.TrimPrefix(p, "/")}
	file, project, _ := strings.Cut(p, "@")
	name, _, hasRef := strings.Cut(project, ":")
	if !projectPathRE.MatchString(name) {
		return "", nil, refuse(CodeCIConfigUnknown, "ci_config_path %q: what follows @ is not a project path", p)
	}
	if !strings.EqualFold(name, g.project) {
		return "", literal, nil
	}
	if hasRef {
		// Another ref than the default branch's head: what it holds is not
		// what the branch reads.
		return "", nil, refuse(CodeCIConfigUnknown, "ci_config_path %q names a ref of this project", p)
	}
	clean, err := ciPath(file)
	if err != nil {
		return "", nil, err
	}
	return clean, literal, nil
}

// projectPathRE is a GitLab project's full path: a namespace, perhaps
// nested, and the project.
var projectPathRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)+$`)

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
// include and every trigger:include (a child pipeline's configuration),
// in every YAML document. Aliases and merge keys (<<) are followed: a
// trigger is looked for in every mapping at any depth, a hidden job's or
// an anchor's included, so that a trigger reached through an alias, a
// merge or extends: is never missed. A !reference tag under a trigger or
// an include cannot be resolved here: an error. Remote, template,
// component, artifact and other projects' files are outside the
// repository; a project file of this project is local. A kind or a key it
// does not know is an error.
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
		if len(doc.Content) == 0 {
			continue
		}
		top, err := deref(doc.Content[0])
		if err != nil {
			return nil, err
		}
		if top == nil || top.Kind != yaml.MappingNode {
			continue
		}
		inc, err := mapValue(top, "include")
		if err != nil {
			return nil, err
		}
		if inc != nil {
			l, err := includeNode(inc, project)
			if err != nil {
				return nil, err
			}
			out = append(out, l...)
		}
		err = walk(top, func(m *yaml.Node) error {
			for i := 0; i+1 < len(m.Content); i += 2 {
				k, err := deref(m.Content[i])
				if err != nil {
					return err
				}
				if k == nil || k.Value != "trigger" {
					continue
				}
				l, err := triggerIncludes(m.Content[i+1], project)
				if err != nil {
					return err
				}
				out = append(out, l...)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
}

// triggerIncludes are the files a trigger includes: none for a project
// name (a multi-project pipeline), its include's otherwise.
func triggerIncludes(n *yaml.Node, project string) ([]string, error) {
	if hasReference(n) {
		return nil, fmt.Errorf("a !reference under a trigger (line %d)", n.Line)
	}
	t, err := deref(n)
	if err != nil {
		return nil, err
	}
	if t == nil || t.Kind != yaml.MappingNode {
		return nil, nil
	}
	inc, err := mapValue(t, "include")
	if err != nil || inc == nil {
		return nil, err
	}
	return includeNode(inc, project)
}

// maxAliasDepth bounds alias and merge chains: past it, the server cannot
// tell what a key holds, an error and never "absent".
const maxAliasDepth = 32

var errAliasDepth = fmt.Errorf("an alias or merge chain deeper than %d", maxAliasDepth)

// deref follows an alias to its anchored node; an error past maxAliasDepth.
func deref(n *yaml.Node) (*yaml.Node, error) {
	for range maxAliasDepth {
		if n == nil || n.Kind != yaml.AliasNode {
			return n, nil
		}
		n = n.Alias
	}
	return nil, errAliasDepth
}

// walk calls visit on every mapping reachable from n, aliases followed,
// each once; it stops at the first error.
func walk(n *yaml.Node, visit func(*yaml.Node) error) error {
	seen := map[*yaml.Node]bool{}
	stack := []*yaml.Node{n}
	for len(stack) > 0 {
		n, err := deref(stack[len(stack)-1])
		if err != nil {
			return err
		}
		stack = stack[:len(stack)-1]
		if n == nil || seen[n] {
			continue
		}
		seen[n] = true
		if n.Kind == yaml.MappingNode {
			if err := visit(n); err != nil {
				return err
			}
		}
		stack = append(stack, n.Content...)
	}
	return nil
}

// hasReference tells whether a !reference tag sits anywhere under n,
// aliases followed.
func hasReference(n *yaml.Node) bool {
	seen := map[*yaml.Node]bool{}
	stack := []*yaml.Node{n}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == nil || seen[n] {
			continue
		}
		seen[n] = true
		if n.Tag == "!reference" {
			return true
		}
		if n.Kind == yaml.AliasNode {
			stack = append(stack, n.Alias)
			continue
		}
		stack = append(stack, n.Content...)
	}
	return false
}

// mapValue is the value of key in a mapping, aliases and merge keys (<<)
// followed: a key of the mapping itself wins over a merged one, and the
// first merged mapping over the next. A merge chain past maxAliasDepth is
// an error.
func mapValue(n *yaml.Node, key string) (*yaml.Node, error) {
	return mapValueDepth(n, key, 0)
}

func mapValueDepth(n *yaml.Node, key string, depth int) (*yaml.Node, error) {
	if depth > maxAliasDepth {
		return nil, errAliasDepth
	}
	n, err := deref(n)
	if err != nil || n == nil || n.Kind != yaml.MappingNode {
		return nil, err
	}
	var merges []*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, err := deref(n.Content[i])
		if err != nil {
			return nil, err
		}
		if k == nil {
			continue
		}
		if k.Value == key {
			return n.Content[i+1], nil
		}
		if k.Value == "<<" {
			merges = append(merges, n.Content[i+1])
		}
	}
	for _, m := range merges {
		m, err := deref(m)
		if err != nil {
			return nil, err
		}
		if m == nil {
			continue
		}
		sources := []*yaml.Node{m}
		if m.Kind == yaml.SequenceNode {
			sources = m.Content
		}
		for _, s := range sources {
			v, err := mapValueDepth(s, key, depth+1)
			if err != nil || v != nil {
				return v, err
			}
		}
	}
	return nil, nil
}

func includeNode(n *yaml.Node, project string) ([]string, error) {
	if hasReference(n) {
		return nil, fmt.Errorf("a !reference under an include (line %d)", n.Line)
	}
	n, err := deref(n)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, errors.New("an empty include")
	}
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
var includeKeys = map[string]bool{"rules": true, "inputs": true, keyRef: true, "file": true, "job": true, "integrity": true, "cache": true}

func includeMap(n *yaml.Node, project string) ([]string, error) {
	kind := ""
	for i := 0; i+1 < len(n.Content); i += 2 {
		kn, err := deref(n.Content[i])
		if err != nil {
			return nil, err
		}
		if kn == nil {
			return nil, fmt.Errorf("an include key that is empty (line %d)", n.Line)
		}
		k := kn.Value
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
		v, err := derefValue(n, "local")
		if err != nil {
			return nil, err
		}
		if v == nil || v.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("include:local is not a path (line %d)", n.Line)
		}
		return []string{v.Value}, nil
	case "project":
		v, err := derefValue(n, "project")
		if err != nil {
			return nil, err
		}
		if v == nil || v.Kind != yaml.ScalarNode || strings.Contains(v.Value, "$") {
			return nil, fmt.Errorf("include:project is not a project path (line %d)", n.Line)
		}
		if !strings.EqualFold(strings.Trim(v.Value, "/"), project) {
			return nil, nil
		}
		// This project's own files, perhaps at another ref: refused all
		// the same, and only readable at the head.
		ref, err := mapValue(n, keyRef)
		if err != nil {
			return nil, err
		}
		if ref != nil {
			return nil, fmt.Errorf("include:project names this project at a ref (line %d)", n.Line)
		}
		files, err := derefValue(n, "file")
		if err != nil {
			return nil, err
		}
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

// derefValue is mapValue's node, its alias followed.
func derefValue(n *yaml.Node, key string) (*yaml.Node, error) {
	v, err := mapValue(n, key)
	if err != nil {
		return nil, err
	}
	return deref(v)
}
