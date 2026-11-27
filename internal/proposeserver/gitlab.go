// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package proposeserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// GitLab's REST API v4: the project, its default branch, its repository's
// tree and files (the CI configuration), one commit and the merge
// requests, nothing else (spec 12 §6.1). The token is a project access
// token with the Developer role and the api scope (§6.3.2).
type gitlab struct {
	api     *api
	project string // the full path
}

const (
	// maxTreePages bounds a directory's listing (100 entries a page).
	maxTreePages = 50
	draftPrefix  = "Draft: "
)

func newGitLab(a *api, project string) *gitlab {
	a.auth = func(r *http.Request, token string) { r.Header.Set("PRIVATE-TOKEN", token) }
	return &gitlab{api: a, project: project}
}

func (g *gitlab) path(format string, args ...any) string {
	return "/projects/" + escapeWhole(g.project) + fmt.Sprintf(format, args...)
}

// inspect: the default branch and ci_config_path from the project, then
// the branch. Protected when protected is true and can_push,
// developers_can_push and developers_can_merge are all false: the token,
// a Developer, can then neither push nor merge into it (spec 12 §6.3.3).
func (g *gitlab) inspect(ctx context.Context) (head, error) {
	var project map[string]json.RawMessage
	if _, err := g.api.do(ctx, http.MethodGet, g.path(""), nil, nil, &project); err != nil {
		return head{}, refuse(CodeProtectionUnknown, "read the project %s: %v", g.project, err)
	}
	var branch string
	if err := json.Unmarshal(project["default_branch"], &branch); err != nil || branch == "" {
		return head{}, refuse(CodeProtectionUnknown, "the project %s names no default branch", g.project)
	}
	h := head{branch: branch}
	if raw, ok := project["ci_config_path"]; ok {
		var p *string
		if err := json.Unmarshal(raw, &p); err == nil {
			if p == nil {
				p = new(string) // null is the default configuration
			}
			h.ciConfigPath = p
		}
	}
	var br struct {
		Protected          *bool `json:"protected"`
		DevelopersCanPush  *bool `json:"developers_can_push"`
		DevelopersCanMerge *bool `json:"developers_can_merge"`
		CanPush            *bool `json:"can_push"`
		Commit             struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if _, err := g.api.do(ctx, http.MethodGet, g.path("/repository/branches/%s", escapeWhole(branch)), nil, nil, &br); err != nil {
		return head{}, refuse(CodeProtectionUnknown, "read the default branch %s: %v", branch, err)
	}
	if br.Protected == nil || br.DevelopersCanPush == nil || br.DevelopersCanMerge == nil || br.CanPush == nil || br.Commit.ID == "" {
		return head{}, refuse(CodeProtectionUnknown, "the default branch %s: the answer lacks protected, can_push, developers_can_push, developers_can_merge or its commit", branch)
	}
	if !*br.Protected || *br.CanPush || *br.DevelopersCanPush || *br.DevelopersCanMerge {
		return head{}, refuse(CodeUnprotected, "the default branch %s of %s is not protected against the token: protected=%t can_push=%t developers_can_push=%t developers_can_merge=%t",
			branch, g.project, *br.Protected, *br.CanPush, *br.DevelopersCanPush, *br.DevelopersCanMerge)
	}
	h.commit = br.Commit.ID
	return h, nil
}

func (g *gitlab) branchExists(ctx context.Context, branch string) (bool, error) {
	_, err := g.api.do(ctx, http.MethodGet, g.path("/repository/branches/%s", escapeWhole(branch)), nil, nil, nil)
	switch {
	case err == nil:
		return true, nil
	case httpStatus(err) == http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("read the branch %s: %w", branch, err)
	}
}

func (g *gitlab) openProposal(ctx context.Context, h head, branch string) (string, error) {
	var mrs []struct {
		WebURL       string `json:"web_url"`
		Description  string `json:"description"`
		SourceBranch string `json:"source_branch"`
	}
	q := url.Values{"state": {"opened"}, "source_branch": {branch}, "target_branch": {h.branch}, keyPerPage: {"100"}}
	if _, err := g.api.do(ctx, http.MethodGet, g.path("/merge_requests"), q, nil, &mrs); err != nil {
		return "", fmt.Errorf("list the merge requests of %s: %w", branch, err)
	}
	for _, m := range mrs {
		if m.SourceBranch == branch && strings.Contains(m.Description, Footer) {
			return m.WebURL, nil
		}
	}
	return "", nil
}

func (g *gitlab) list(ctx context.Context, h head, dir, _ string) ([]entry, error) {
	return g.tree(ctx, h, dir, false)
}

// tree lists a directory at the head, page by page; a directory that does
// not exist (a 404 on the first page) is empty, any other error is one.
func (g *gitlab) tree(ctx context.Context, h head, dir string, recursive bool) ([]entry, error) {
	var out []entry
	for page := 1; ; page++ {
		if page > maxTreePages {
			return nil, fmt.Errorf("%q has more than %d entries", dir, maxTreePages*100)
		}
		q := url.Values{keyRef: {h.commit}, keyPerPage: {"100"}, "page": {strconv.Itoa(page)}}
		if dir != "" {
			q.Set("path", dir)
		}
		if recursive {
			q.Set("recursive", "true")
		}
		var items []struct {
			Name string `json:"name"`
			Path string `json:"path"`
			Mode string `json:"mode"`
		}
		hdr, err := g.api.do(ctx, http.MethodGet, g.path("/repository/tree"), q, nil, &items)
		if httpStatus(err) == http.StatusNotFound && page == 1 {
			return out, nil
		}
		if err != nil {
			// Any other error, or a page that vanished, is not an empty
			// directory: the caller fails closed.
			return nil, fmt.Errorf("list %q, page %d: %w", dir, page, err)
		}
		for _, it := range items {
			name := it.Name
			if recursive {
				name = it.Path
			}
			out = append(out, entry{name: name, mode: it.Mode})
		}
		if hdr.Get("X-Next-Page") == "" {
			return out, nil
		}
	}
}

// raw reads a file at the head; found is false on a 404.
func (g *gitlab) raw(ctx context.Context, h head, p string) (content []byte, found bool, err error) {
	q := url.Values{keyRef: {h.commit}}
	_, err = g.api.do(ctx, http.MethodGet, g.path("/repository/files/%s/raw", escapeWhole(p)), q, nil, &content)
	if httpStatus(err) == http.StatusNotFound {
		return nil, false, nil
	}
	return content, err == nil, err
}

// commit creates the branch with one commit from the head's SHA, the one
// whose CI configuration was read; force is never sent.
func (g *gitlab) commit(ctx context.Context, h head, branch, message string, changes []change) error {
	actions := make([]map[string]string, 0, len(changes))
	for _, c := range changes {
		action := "create"
		if c.exists {
			action = "update"
		}
		actions = append(actions, map[string]string{"action": action, "file_path": c.path, keyContent: c.content})
	}
	in := map[string]any{keyBranch: branch, "start_sha": h.commit, "commit_message": message, "actions": actions}
	_, err := g.api.do(ctx, http.MethodPost, g.path("/repository/commits"), nil, in, nil)
	if s := httpStatus(err); s == http.StatusBadRequest && strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return refuse(CodeBranchExists, "branch %s was created meanwhile: %v", branch, err)
	}
	if err != nil {
		return fmt.Errorf("write the commit: %w", err)
	}
	return nil
}

// open opens the merge request as a draft: its title prefixed Draft:,
// GitLab's way, its create endpoint having no draft parameter.
func (g *gitlab) open(ctx context.Context, h head, branch, title, body string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(title), "draft:") {
		title = draftPrefix + title
	}
	var mr struct {
		WebURL         string `json:"web_url"`
		Draft          *bool  `json:"draft"`
		WorkInProgress *bool  `json:"work_in_progress"`
	}
	in := map[string]any{"source_branch": branch, "target_branch": h.branch, keyTitle: title, keyDescription: body}
	if _, err := g.api.do(ctx, http.MethodPost, g.path("/merge_requests"), nil, in, &mr); err != nil {
		return "", fmt.Errorf("open the merge request: %w", err)
	}
	// draft since GitLab 13.2, work_in_progress before (deprecated): one
	// of them must say it is a draft.
	if (mr.Draft == nil || !*mr.Draft) && (mr.WorkInProgress == nil || !*mr.WorkInProgress) {
		return "", fmt.Errorf("the forge opened %s without marking it a draft", mr.WebURL)
	}
	return mr.WebURL, nil
}
