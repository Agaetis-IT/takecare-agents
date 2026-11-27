// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package proposeserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// GitHub's REST API, version 2022-11-28: the repository, its default
// branch and rules, the Git data API (trees, commits, refs) and the pull
// requests, nothing else (spec 12 §6.1).
type github struct {
	api   *api
	repo  string // owner/name
	owner string
}

const (
	githubAPIVersion = "2022-11-28"
	// maxRulePages bounds the reading of a branch's rules (100 a page).
	maxRulePages = 10
)

func newGitHub(a *api, repo string) *github {
	a.auth = func(r *http.Request, token string) {
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Accept", "application/vnd.github+json")
		r.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	}
	owner, _, _ := strings.Cut(repo, "/")
	return &github{api: a, repo: repo, owner: owner}
}

func (g *github) path(format string, args ...any) string {
	return "/repos/" + escapeSegments(g.repo) + fmt.Sprintf(format, args...)
}

// inspect: the default branch from the repository, its head and its
// protected flag from the branch (Contents read), and its rulesets' rules
// (Metadata read). Protected when the flag is true or a pull_request rule
// applies; a classic protection is taken as it is (its details need
// Administration read, which the token does not get).
func (g *github) inspect(ctx context.Context) (head, error) {
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if _, err := g.api.do(ctx, http.MethodGet, g.path(""), nil, nil, &repo); err != nil {
		return head{}, refuse(CodeProtectionUnknown, "read the repository %s: %v", g.repo, err)
	}
	if repo.DefaultBranch == "" {
		return head{}, refuse(CodeProtectionUnknown, "the repository %s names no default branch", g.repo)
	}
	var br struct {
		Protected *bool `json:"protected"`
		Commit    struct {
			SHA    string `json:"sha"`
			Commit struct {
				Tree struct {
					SHA string `json:"sha"`
				} `json:"tree"`
			} `json:"commit"`
		} `json:"commit"`
	}
	if _, err := g.api.do(ctx, http.MethodGet, g.path("/branches/%s", escapeSegments(repo.DefaultBranch)), nil, nil, &br); err != nil {
		return head{}, refuse(CodeProtectionUnknown, "read the default branch %s: %v", repo.DefaultBranch, err)
	}
	if br.Protected == nil {
		return head{}, refuse(CodeProtectionUnknown, "the default branch %s: the answer has no protected field", repo.DefaultBranch)
	}
	if br.Commit.SHA == "" || br.Commit.Commit.Tree.SHA == "" {
		return head{}, refuse(CodeProtectionUnknown, "the default branch %s: the answer has no head commit and tree", repo.DefaultBranch)
	}
	h := head{branch: repo.DefaultBranch, commit: br.Commit.SHA, tree: br.Commit.Commit.Tree.SHA}
	if *br.Protected {
		return h, nil
	}
	pr, err := g.pullRequestRule(ctx, repo.DefaultBranch)
	if err != nil {
		return head{}, refuse(CodeProtectionUnknown, "read the rules of the default branch %s: %v", repo.DefaultBranch, err)
	}
	if !pr {
		return head{}, refuse(CodeUnprotected, "the default branch %s of %s is not protected: no classic protection and no pull_request rule", repo.DefaultBranch, g.repo)
	}
	return h, nil
}

func (g *github) pullRequestRule(ctx context.Context, branch string) (bool, error) {
	for page := 1; page <= maxRulePages; page++ {
		var rules []struct {
			Type *string `json:"type"`
		}
		q := url.Values{keyPerPage: {"100"}, "page": {strconv.Itoa(page)}}
		if _, err := g.api.do(ctx, http.MethodGet, g.path("/rules/branches/%s", escapeSegments(branch)), q, nil, &rules); err != nil {
			return false, err
		}
		for _, r := range rules {
			if r.Type == nil {
				return false, fmt.Errorf("a rule without a type")
			}
			if *r.Type == "pull_request" {
				return true, nil
			}
		}
		if len(rules) < 100 {
			return false, nil
		}
	}
	return false, fmt.Errorf("more than %d rules", maxRulePages*100)
}

// ciPatterns: GitHub's workflows are among the fixed paths, and the token
// has no Workflows permission (spec 12 §6.3.1).
func (g *github) ciPatterns(context.Context, head) ([]string, error) { return nil, nil }

func (g *github) branchExists(ctx context.Context, branch string) (bool, error) {
	_, err := g.api.do(ctx, http.MethodGet, g.path("/git/ref/heads/%s", escapeSegments(branch)), nil, nil, nil)
	switch {
	case err == nil:
		return true, nil
	case httpStatus(err) == http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("read the branch %s: %w", branch, err)
	}
}

func (g *github) openProposal(ctx context.Context, h head, branch string) (string, error) {
	var pulls []struct {
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	q := url.Values{"state": {"open"}, "head": {g.owner + ":" + branch}, "base": {h.branch}, keyPerPage: {"100"}}
	if _, err := g.api.do(ctx, http.MethodGet, g.path("/pulls"), q, nil, &pulls); err != nil {
		return "", fmt.Errorf("list the pull requests of %s: %w", branch, err)
	}
	for _, p := range pulls {
		if p.Head.Ref == branch && strings.Contains(p.Body, Footer) {
			return p.HTMLURL, nil
		}
	}
	return "", nil
}

func (g *github) list(ctx context.Context, _ head, _, id string) ([]entry, error) {
	var tree struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			SHA  string `json:"sha"`
		} `json:"tree"`
	}
	if _, err := g.api.do(ctx, http.MethodGet, g.path("/git/trees/%s", url.PathEscape(id)), nil, nil, &tree); err != nil {
		return nil, err
	}
	if tree.Truncated {
		return nil, fmt.Errorf("the tree %s is truncated", id)
	}
	out := make([]entry, 0, len(tree.Tree))
	for _, e := range tree.Tree {
		out = append(out, entry{name: e.Path, mode: e.Mode, id: e.SHA})
	}
	return out, nil
}

// commit writes a tree on the head's, a commit on the head, then the
// branch's ref at that commit: the branch never exists without the
// commit, and no ref is ever updated.
func (g *github) commit(ctx context.Context, h head, branch, message string, changes []change) error {
	type treeEntry struct {
		Path    string `json:"path"`
		Mode    string `json:"mode"`
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	entries := make([]treeEntry, 0, len(changes))
	for _, c := range changes {
		entries = append(entries, treeEntry{Path: c.path, Mode: c.mode, Type: "blob", Content: c.content})
	}
	var tree, commit struct {
		SHA string `json:"sha"`
	}
	if _, err := g.api.do(ctx, http.MethodPost, g.path("/git/trees"), nil, map[string]any{"base_tree": h.tree, "tree": entries}, &tree); err != nil {
		return fmt.Errorf("write the tree: %w", err)
	}
	in := map[string]any{"message": message, "tree": tree.SHA, "parents": []string{h.commit}}
	if _, err := g.api.do(ctx, http.MethodPost, g.path("/git/commits"), nil, in, &commit); err != nil {
		return fmt.Errorf("write the commit: %w", err)
	}
	_, err := g.api.do(ctx, http.MethodPost, g.path("/git/refs"), nil, map[string]any{keyRef: "refs/heads/" + branch, "sha": commit.SHA}, nil)
	if httpStatus(err) == http.StatusUnprocessableEntity {
		return refuse(CodeBranchExists, "branch %s was created meanwhile: %v", branch, err)
	}
	if err != nil {
		return fmt.Errorf("create the branch %s: %w", branch, err)
	}
	return nil
}

// open asks for a draft; a forge that refuses drafts gets no pull request
// at all (spec 12 §6.1, §6.5).
func (g *github) open(ctx context.Context, h head, branch, title, body string) (string, error) {
	var pr struct {
		HTMLURL string `json:"html_url"`
		Draft   *bool  `json:"draft"`
	}
	in := map[string]any{keyTitle: title, keyBody: body, "head": branch, "base": h.branch, "draft": true}
	_, err := g.api.do(ctx, http.MethodPost, g.path("/pulls"), nil, in, &pr)
	if httpStatus(err) == http.StatusUnprocessableEntity && strings.Contains(strings.ToLower(err.Error()), "draft") {
		return "", refuse(CodeDraftUnavailable, "%s refuses draft pull requests; branch %s is left without one, the customer's to delete", g.repo, branch)
	}
	if err != nil {
		return "", fmt.Errorf("open the pull request: %w", err)
	}
	if pr.Draft == nil || !*pr.Draft {
		return "", fmt.Errorf("the forge opened %s without the draft flag", pr.HTMLURL)
	}
	return pr.HTMLURL, nil
}
