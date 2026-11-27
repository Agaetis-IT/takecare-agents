// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package proposeserver is the code connector's propose server (spec 12
// §6): the product's first write, outside Kubernetes. One tool,
// propose_change, opens a draft pull request (a draft merge request on
// GitLab) on a listed repository, on a branch under takecare/mecano/,
// with one commit, only when the default branch is protected, and never
// on a file of the CI configuration. It has no read tool, no merge, no
// comment and no ref deletion; each repository's write token is read
// from its mounted file and sent in a header only.
package proposeserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agaetis-it/takecare-agents/internal/httpclient"
)

// ToolProposeChange is the server's one tool.
const ToolProposeChange = "propose_change"

// Forges.
const (
	ForgeGitHub = "github"
	ForgeGitLab = "gitlab"
)

// Bounds of a call (spec 12 §6.2).
const (
	MaxRepositories = 16
	maxFiles        = 20
	maxContent      = 256 << 10
	maxTitle        = 200
	maxBody         = 32 << 10
	maxPath         = 1024
	callTimeout     = 2 * time.Minute
)

// Footer closes every pull request the server opens; a retry reuses only
// an open pull request that carries it.
const Footer = "Opened by the TakeCare Mécano as a draft. The Mécano never merges it: a person reviews it and merges it."

var (
	branchRE = regexp.MustCompile(`^takecare/mecano/[a-z0-9][a-z0-9-]{0,62}$`)
	// The connector's pattern (api/v1alpha1, ProposeRepository): no
	// segment is . or .., no .git suffix.
	repositoryRE = regexp.MustCompile(`^[A-Za-z0-9_.-]*[A-Za-z0-9_-][A-Za-z0-9_.-]*(/[A-Za-z0-9_.-]*[A-Za-z0-9_-][A-Za-z0-9_.-]*)+$`)
)

// Repository is one repository the server may propose on.
type Repository struct {
	Forge      string
	APIURL     string
	Repository string
	// TokenFile is the mounted write token, read at each call.
	TokenFile string
}

// Config is the server's repositories.
type Config struct {
	Repositories []Repository
	// CAFile is a PEM bundle trusted besides the system roots.
	CAFile string
	Logger *slog.Logger
	// HTTPClient replaces the forges' client in tests.
	HTTPClient *http.Client
}

// Server serves propose_change.
type Server struct {
	mcp    *sdk.Server
	repos  map[string]*target
	logger *slog.Logger
}

// target is a listed repository and its forge; calls on it are serialized,
// so that two calls never race to open two pull requests.
type target struct {
	cfg   Repository
	forge forge
	mu    sync.Mutex
}

// head is the default branch read at the start of a call.
type head struct {
	branch string
	commit string
	// tree is the root tree's SHA (GitHub).
	tree string
	// ciConfigPath is the project's ci_config_path (GitLab); nil when the
	// answer did not hold it.
	ciConfigPath *string
}

// entry is a tree entry: its name, its Git mode and, on GitHub, its SHA.
type entry struct {
	name, mode, id string
}

// change is a file of the commit and what the default branch holds there.
type change struct {
	path, content, mode string
	exists              bool
}

// forge is one forge's calls, the only ones the server makes.
type forge interface {
	// inspect reads the default branch, its head, and refuses when it is
	// not protected or when that cannot be told (spec 12 §6.3.3).
	inspect(ctx context.Context) (head, error)
	// ciPatterns are the paths and patterns of the CI configuration at the
	// head, beyond the fixed ones (GitLab, spec 12 §6.3.2).
	ciPatterns(ctx context.Context, h head) ([]string, error)
	branchExists(ctx context.Context, branch string) (bool, error)
	// openProposal is the URL of an open pull request from branch into
	// the default branch that the server opened, or "".
	openProposal(ctx context.Context, h head, branch string) (string, error)
	// list is a directory's entries at the head: by its SHA on GitHub, by
	// its path on GitLab.
	list(ctx context.Context, h head, dir, id string) ([]entry, error)
	// commit writes one commit with the changes on a new branch from the
	// head, never with force.
	commit(ctx context.Context, h head, branch, message string, changes []change) error
	// open opens the draft pull request and returns its URL.
	open(ctx context.Context, h head, branch, title, body string) (string, error)
}

// New checks the configuration and builds the server.
func New(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if n := len(cfg.Repositories); n == 0 || n > MaxRepositories {
		return nil, fmt.Errorf("serve 1 to %d repositories, not %d", MaxRepositories, n)
	}
	hc := cfg.HTTPClient
	if hc == nil {
		c := httpclient.FromEnv(30 * time.Second)
		c.CAFile = cfg.CAFile
		var err error
		if hc, err = c.New(); err != nil {
			return nil, fmt.Errorf("the HTTPS client: %w", err)
		}
	}
	// A redirect is never followed: GitLab's token header would go with it.
	client := *hc
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	s := &Server{repos: map[string]*target{}, logger: cfg.Logger}
	for _, r := range cfg.Repositories {
		if err := checkRepository(r); err != nil {
			return nil, err
		}
		if s.repos[r.Repository] != nil {
			return nil, fmt.Errorf("repository %s is listed twice", r.Repository)
		}
		a := &api{base: strings.TrimSuffix(r.APIURL, "/"), client: &client, tokenFile: r.TokenFile}
		var f forge
		switch r.Forge {
		case ForgeGitHub:
			f = newGitHub(a, r.Repository)
		case ForgeGitLab:
			f = newGitLab(a, r.Repository)
		}
		s.repos[r.Repository] = &target{cfg: r, forge: f}
	}
	s.mcp = sdk.NewServer(&sdk.Implementation{Name: "code-propose", Version: "1"}, nil)
	s.mcp.AddTool(&sdk.Tool{
		Name: ToolProposeChange,
		Description: fmt.Sprintf("Open a draft pull request (a draft merge request on GitLab) on a listed repository: one commit with the files given, "+
			"on a new branch under takecare/mecano/ from the default branch's head. Refused when the default branch is not protected, "+
			"on a CI configuration file, and on a branch that exists without its open pull request; a retry returns the pull request already open. "+
			"At most %d files and %d KiB.", maxFiles, maxContent>>10),
		InputSchema: inputSchema(),
		Annotations: annotations(),
	}, s.handle)
	return s, nil
}

func checkRepository(r Repository) error {
	if r.Forge != ForgeGitHub && r.Forge != ForgeGitLab {
		return fmt.Errorf("repository %s: forge %q: github or gitlab", r.Repository, r.Forge)
	}
	if !repositoryRE.MatchString(r.Repository) || strings.HasSuffix(r.Repository, ".git") {
		return fmt.Errorf("repository %q: the forge's path, without a . or .. segment or a .git suffix", r.Repository)
	}
	if r.Forge == ForgeGitHub && strings.Count(r.Repository, "/") != 1 {
		return fmt.Errorf("repository %s: owner/name on GitHub", r.Repository)
	}
	u, err := url.Parse(r.APIURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("repository %s: apiURL %q: an https URL without user information, query or fragment", r.Repository, r.APIURL)
	}
	if r.TokenFile == "" {
		return fmt.Errorf("repository %s: no token file", r.Repository)
	}
	return nil
}

// The tool writes, outside the cluster, without destroying: discovery
// classes it act, and the chart's connector overrides it to propose
// (spec 12 §6.4).
func annotations() *sdk.ToolAnnotations {
	no, yes := false, true
	return &sdk.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &no, IdempotentHint: false, OpenWorldHint: &yes}
}

func inputSchema() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"repository", "branch", "title", "body", "files"},
		"properties": map[string]any{
			"repository": str("A listed repository: owner/name on GitHub, the project's full path on GitLab."),
			"branch":     str("The branch to create: takecare/mecano/ then lower-case letters, digits and hyphens, as the operator's message names it."),
			"title":      str(fmt.Sprintf("The pull request's title, %d characters at most.", maxTitle)),
			"body":       str(fmt.Sprintf("The pull request's description, %d KiB at most.", maxBody>>10)),
			"files": map[string]any{
				"type": "array", "minItems": 1, "maxItems": maxFiles,
				"description": "The files the commit writes, each its whole new content.",
				"items": map[string]any{
					"type": "object", "additionalProperties": false, "required": []string{"path", "content"},
					"properties": map[string]any{
						"path":    str("A relative path in the repository."),
						"content": str("The file's whole content."),
					},
				},
			},
		},
	}
}

// Handler serves /mcp and /healthz; the server is ready as soon as it
// runs, each forge being reached per call.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// A browser's cross-origin request is refused, as the MCP
	// specification requires of the Origin header.
	mux.Handle("/mcp", http.NewCrossOriginProtection().Handler(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.mcp }, &sdk.StreamableHTTPOptions{Stateless: true})))
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	mux.HandleFunc("/healthz", ok)
	mux.HandleFunc("/readyz", ok)
	return mux
}

// Args are propose_change's five arguments.
type Args struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	Files      []File `json:"files"`
}

// File is a file of the commit.
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Result is what propose_change returns.
type Result struct {
	Repository string `json:"repository"`
	URL        string `json:"url"`
	Branch     string `json:"branch"`
	// Reused is set when the pull request was already open (a retry).
	Reused bool `json:"reused,omitempty"`
}

// handle runs a call; every error is a tool error the agent reads, its
// first word a fixed code.
func (s *Server) handle(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	res, repo, err := s.propose(ctx, req.Params.Arguments)
	if err != nil {
		code := CodeForge
		var r *refusal
		if errors.As(err, &r) {
			code = r.code
		} else {
			err = &refusal{code: CodeForge, detail: err.Error()}
		}
		// The repository and the outcome, never the content.
		s.logger.Info("propose_change refused", "repository", repo, "outcome", code)
		return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: err.Error()}}}, nil
	}
	outcome := "opened"
	if res.Reused {
		outcome = "reused"
	}
	s.logger.Info("propose_change", "repository", res.Repository, "branch", res.Branch, "outcome", outcome, "url", res.URL)
	raw, err := json.Marshal(res)
	if err != nil {
		return nil, fmt.Errorf("encode the result: %w", err)
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(raw)}}}, nil
}

// propose follows spec 12 §6.2, in its order.
func (s *Server) propose(ctx context.Context, raw json.RawMessage) (*Result, string, error) {
	a, err := decodeArgs(raw)
	if err != nil {
		return nil, "", err
	}
	// 1. a listed repository.
	t := s.repos[a.Repository]
	if t == nil {
		return nil, a.Repository, refuse(CodeRepositoryNotListed, "%q is not a repository this server may propose on", a.Repository)
	}
	// 2. no path outside the repository nor under the fixed CI paths.
	if err := checkPaths(a.Files); err != nil {
		return nil, a.Repository, err
	}
	// 3. the branch under the server's fixed prefix.
	if !branchRE.MatchString(a.Branch) {
		return nil, a.Repository, refuse(CodeBranchRefused, "branch %q: takecare/mecano/ then lower-case letters, digits and hyphens, 63 at most", a.Branch)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// 4. the default branch protected, read on every call; on GitLab, the
	// files of the project's CI configuration.
	h, err := t.forge.inspect(ctx)
	if err != nil {
		return nil, a.Repository, err
	}
	patterns, err := t.forge.ciPatterns(ctx, h)
	if err != nil {
		return nil, a.Repository, err
	}
	for _, f := range a.Files {
		for _, p := range patterns {
			if matchCI(p, f.Path) {
				return nil, a.Repository, refuse(CodePathRefused, "%s is a file of the project's CI configuration (%s)", f.Path, p)
			}
		}
	}
	// 5. a new branch, or the pull request already open from it.
	exists, err := t.forge.branchExists(ctx, a.Branch)
	if err != nil {
		return nil, a.Repository, err
	}
	if exists {
		u, err := t.forge.openProposal(ctx, h, a.Branch)
		if err != nil {
			return nil, a.Repository, err
		}
		if u == "" {
			return nil, a.Repository, refuse(CodeBranchExists, "branch %s exists without an open pull request this server opened: the next maintenance uses a branch of its own name", a.Branch)
		}
		return &Result{Repository: a.Repository, URL: u, Branch: a.Branch, Reused: true}, a.Repository, nil
	}
	changes, err := resolve(ctx, t.forge, h, a.Files)
	if err != nil {
		return nil, a.Repository, err
	}
	// 6. one commit on the new branch; 7. the draft pull request.
	if err := t.forge.commit(ctx, h, a.Branch, a.Title, changes); err != nil {
		return nil, a.Repository, err
	}
	u, err := t.forge.open(ctx, h, a.Branch, a.Title, a.Body+"\n\n---\n"+Footer)
	if err != nil {
		return nil, a.Repository, err
	}
	return &Result{Repository: a.Repository, URL: u, Branch: a.Branch}, a.Repository, nil
}

func decodeArgs(raw json.RawMessage) (Args, error) {
	var a Args
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return a, refuse(CodeArguments, "%v", err)
	}
	switch {
	case a.Title == "" || utf8.RuneCountInString(a.Title) > maxTitle || strings.ContainsAny(a.Title, "\r\n"):
		return a, refuse(CodeArguments, "title: one line of 1 to %d characters", maxTitle)
	case len(a.Body) > maxBody:
		return a, refuse(CodeArguments, "body: %d KiB at most", maxBody>>10)
	case len(a.Files) == 0 || len(a.Files) > maxFiles:
		return a, refuse(CodeArguments, "files: 1 to %d", maxFiles)
	case !utf8.ValidString(a.Title) || !utf8.ValidString(a.Body):
		return a, refuse(CodeArguments, "title and body are text")
	}
	total := 0
	for _, f := range a.Files {
		total += len(f.Content)
		if !utf8.ValidString(f.Content) {
			return a, refuse(CodeArguments, "%s: the content is text", f.Path)
		}
	}
	if total > maxContent {
		return a, refuse(CodeArguments, "files: %d KiB of content at most", maxContent>>10)
	}
	return a, nil
}

// fixedCI are refused on every forge, whatever the project's settings:
// GitHub's workflows, GitLab's default configuration and its directory.
var fixedCI = []string{".github/workflows/", ".gitlab-ci.yml", ".gitlab/"}

// checkPaths refuses a path outside the repository, a .git element, a
// path twice, and the fixed CI paths.
func checkPaths(files []File) error {
	seen := map[string]bool{}
	for _, f := range files {
		p := f.Path
		if p == "" || len(p) > maxPath || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") || !utf8.ValidString(p) {
			return refuse(CodePathRefused, "path %q: a relative path inside the repository", p)
		}
		for el := range strings.SplitSeq(p, "/") {
			if el == "" || el == "." || el == ".." || strings.EqualFold(el, ".git") {
				return refuse(CodePathRefused, "path %q: a relative path inside the repository, without a ., .. or .git element", p)
			}
		}
		lower := strings.ToLower(p)
		for _, c := range fixedCI {
			if lower == strings.TrimSuffix(c, "/") || (strings.HasSuffix(c, "/") && strings.HasPrefix(lower, c)) {
				return refuse(CodePathRefused, "%s is a CI configuration path (%s)", p, c)
			}
		}
		if seen[p] {
			return refuse(CodePathRefused, "%s is given twice", p)
		}
		seen[p] = true
	}
	return nil
}

// Git modes.
const (
	modeFile    = "100644"
	modeExec    = "100755"
	modeTree    = "040000"
	modeSymlink = "120000"
)

// resolve walks each path from the head's root: every directory on the
// way must be a directory, and the file a regular file or new. A symbolic
// link or a submodule on the way is refused. An existing file keeps its
// mode.
func resolve(ctx context.Context, f forge, h head, files []File) ([]change, error) {
	dirs := map[string][]entry{}
	listing := func(dir, id string) ([]entry, error) {
		if e, ok := dirs[dir]; ok {
			return e, nil
		}
		e, err := f.list(ctx, h, dir, id)
		if err != nil {
			return nil, fmt.Errorf("list %q at the default branch: %w", dir, err)
		}
		dirs[dir] = e
		return e, nil
	}
	out := make([]change, 0, len(files))
	for _, file := range files {
		c := change{path: file.Path, content: file.Content, mode: modeFile}
		parts := strings.Split(file.Path, "/")
		dir, id := "", h.tree
	walk:
		for i, name := range parts {
			entries, err := listing(dir, id)
			if err != nil {
				return nil, err
			}
			k := slices.IndexFunc(entries, func(e entry) bool { return e.name == name })
			if k < 0 {
				break walk // new from here on
			}
			e := entries[k]
			last := i == len(parts)-1
			switch {
			case !last && e.mode == modeTree:
				dir, id = strings.Join(parts[:i+1], "/"), e.id
			case last && (e.mode == modeFile || e.mode == modeExec):
				c.exists, c.mode = true, e.mode
			case e.mode == modeSymlink:
				return nil, refuse(CodePathRefused, "%s: %s is a symbolic link", file.Path, strings.Join(parts[:i+1], "/"))
			default:
				return nil, refuse(CodePathRefused, "%s: %s is not a regular file or a directory (mode %s)", file.Path, strings.Join(parts[:i+1], "/"), e.mode)
			}
		}
		out = append(out, c)
	}
	return out, nil
}
