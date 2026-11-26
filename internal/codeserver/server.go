// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package codeserver is the code connector's read server (spec 12 §5): one
// or more of the customer's Git repositories, each a remote and a branch,
// cloned and polled with a read-only credential, served as four read
// tools. manifests_index says what the manifests declare, never a value;
// file_get, the one content tool (ADR 0014 §5), returns a file. No tool
// takes a path outside the repositories it serves.
package codeserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool names, as the code connector and a role's allow-list name them.
const (
	ToolRepositoriesList = "repositories_list"
	ToolManifestsIndex   = "manifests_index"
	ToolFilesList        = "files_list"
	ToolFileGet          = "file_get"
)

// ContentTools are the tools whose answer carries a file's content.
var ContentTools = []string{ToolFileGet}

// Bounds of the tools.
const (
	maxFiles    = 1000
	maxFileSize = 256 << 10
	argRepo     = "repository"
	argPath     = "path"
	description = "description"
	typeString  = "string"
	keyType     = "type"
)

// Config is the repositories the server reads.
type Config struct {
	Repositories []Repository
	Logger       *slog.Logger
	// Now is replaceable in tests.
	Now func() time.Time
}

// Server serves the repositories.
type Server struct {
	mcp   *sdk.Server
	repos []*repo
	byKey map[string]*repo
}

// New builds the server; Run keeps the repositories current.
func New(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if len(cfg.Repositories) == 0 || len(cfg.Repositories) > MaxRepositories {
		return nil, fmt.Errorf("serve 1 to %d repositories, not %d", MaxRepositories, len(cfg.Repositories))
	}
	s := &Server{byKey: map[string]*repo{}}
	for _, rc := range cfg.Repositories {
		r, err := newRepo(rc, cfg.Logger, cfg.Now)
		if err != nil {
			return nil, err
		}
		if s.byKey[rc.Name] != nil {
			return nil, fmt.Errorf("repository %s is listed twice", rc.Name)
		}
		s.repos, s.byKey[rc.Name] = append(s.repos, r), r
	}
	s.mcp = sdk.NewServer(&sdk.Implementation{Name: "code", Version: "1"}, nil)
	no := false
	ro := &sdk.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no}
	repository := map[string]any{keyType: typeString, description: "A repository's name, as repositories_list gives it."}
	prefix := map[string]any{keyType: typeString, description: "Only the files under this directory of the repository."}
	file := map[string]any{keyType: typeString, description: "A file's path in the repository, as files_list gives it."}
	for _, t := range []struct {
		name, description string
		props             map[string]any
		required          []string
		handler           func(json.RawMessage) (any, error)
	}{
		{ToolRepositoriesList, "The repositories served: name, url, branch, the revision served and when it was fetched.",
			map[string]any{}, nil, s.repositoriesList},
		{ToolManifestsIndex, fmt.Sprintf("Per YAML document of the repository: path, apiVersion, kind, namespace, name, the images its pod templates run and the Helm charts it names (Chart.yaml, Flux HelmRelease, Argo CD Application), never a value; %d documents at most.", maxDocuments),
			map[string]any{argRepo: repository, argPath: prefix}, []string{argRepo}, s.manifestsIndex},
		{ToolFilesList, fmt.Sprintf("The repository's files, paths and sizes, %d at most.", maxFiles),
			map[string]any{argRepo: repository, argPath: prefix}, []string{argRepo}, s.filesList},
		{ToolFileGet, fmt.Sprintf("A file's content, %d KiB at most. Values files and manifests may hold credentials.", maxFileSize>>10),
			map[string]any{argRepo: repository, argPath: file}, []string{argRepo, argPath}, s.fileGet},
	} {
		schema := map[string]any{keyType: "object", "properties": t.props, "additionalProperties": false}
		// JSON Schema wants "required" to be an array: a tool with no
		// required argument leaves it out rather than send null.
		if len(t.required) > 0 {
			schema["required"] = t.required
		}
		s.mcp.AddTool(&sdk.Tool{Name: t.name, Description: t.description, InputSchema: schema, Annotations: ro}, serve(t.handler))
	}
	return s, nil
}

// serve runs a tool: its errors are tool errors the model reads, never
// transport errors.
func serve(h func(json.RawMessage) (any, error)) sdk.ToolHandler {
	return func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		out, err := h(req.Params.Arguments)
		if err != nil {
			return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: err.Error()}}}, nil
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return nil, fmt.Errorf("encode the result: %w", err)
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(raw)}}}, nil
	}
}

// Run keeps every repository current until ctx ends.
func (s *Server) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, r := range s.repos {
		wg.Go(func() { r.run(ctx) })
	}
	wg.Wait()
	return nil
}

// Handler serves /mcp, /healthz, /readyz (ready once a repository is
// loaded: one that is not is an error of its tools, and a blind spot of
// the role reading it) and /statusz.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// A browser's cross-origin request is refused (403), as the MCP
	// specification requires of the Origin header; our callers are not
	// browsers and send none.
	mux.Handle("/mcp", http.NewCrossOriginProtection().Handler(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.mcp }, &sdk.StreamableHTTPOptions{Stateless: true})))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if slices.ContainsFunc(s.repos, func(r *repo) bool { return r.served() != nil }) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "no repository loaded yet\n")
	})
	mux.HandleFunc("/statusz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.states())
	})
	return mux
}

func (s *Server) states() []state {
	out := make([]state, 0, len(s.repos))
	for _, r := range s.repos {
		out = append(out, r.state())
	}
	return out
}

// repositoriesList takes no argument and refuses any, like the other tools
// refuse those they do not know.
func (s *Server) repositoriesList(raw json.RawMessage) (any, error) {
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&struct{}{}); err != nil {
			return nil, fmt.Errorf("arguments: %w", err)
		}
	}
	return s.states(), nil
}

type pathArgs struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
}

// args decodes a call's arguments and finds its repository; the path is
// checked by the caller, as a prefix or a file.
func (s *Server) args(raw json.RawMessage) (*repo, pathArgs, error) {
	var a pathArgs
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&a); err != nil {
			return nil, a, fmt.Errorf("arguments: %w", err)
		}
	}
	r := s.byKey[a.Repository]
	if r == nil {
		return nil, a, fmt.Errorf("unknown repository: %q (repositories_list names them)", a.Repository)
	}
	return r, a, nil
}

// cleanPath checks a path given by the model: relative, inside the
// repository, no "..", no ".", no empty element. An empty prefix is the
// whole repository.
func cleanPath(p string, prefix bool) (string, error) {
	if prefix {
		p = strings.TrimSuffix(p, "/")
		if p == "" {
			return "", nil
		}
	}
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return "", fmt.Errorf("path %q: a relative path inside the repository", p)
	}
	for el := range strings.SplitSeq(p, "/") {
		if el == "" || el == "." || el == ".." {
			return "", fmt.Errorf("path %q: a relative path inside the repository, without a . or .. element", p)
		}
	}
	return p, nil
}

func under(p, prefix string) bool {
	return prefix == "" || p == prefix || strings.HasPrefix(p, prefix+"/")
}

// regular is a file's own content: a symbolic link or a submodule points
// elsewhere and is never read.
func regular(m filemode.FileMode) bool { return m == filemode.Regular || m == filemode.Executable }

type fileInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func (s *Server) filesList(raw json.RawMessage) (any, error) {
	r, a, err := s.args(raw)
	if err != nil {
		return nil, err
	}
	prefix, err := cleanPath(a.Path, true)
	if err != nil {
		return nil, err
	}
	out := map[string]any{argRepo: r.cfg.Name}
	err = r.read(func(revision string, tree *object.Tree) error {
		files, omitted := []fileInfo{}, 0
		err := tree.Files().ForEach(func(f *object.File) error {
			if !regular(f.Mode) || !under(f.Name, prefix) {
				return nil
			}
			if len(files) >= maxFiles {
				omitted++
				return nil
			}
			files = append(files, fileInfo{f.Name, f.Size})
			return nil
		})
		out["revision"], out["files"] = revision, files
		if omitted > 0 {
			out["omitted"] = omitted
		}
		return err
	})
	return out, err
}

func (s *Server) manifestsIndex(raw json.RawMessage) (any, error) {
	r, a, err := s.args(raw)
	if err != nil {
		return nil, err
	}
	prefix, err := cleanPath(a.Path, true)
	if err != nil {
		return nil, err
	}
	ix := &Index{Repository: r.cfg.Name, Documents: []Document{}}
	err = r.read(func(revision string, tree *object.Tree) error {
		ix.Revision = revision
		return tree.Files().ForEach(func(f *object.File) error {
			if !regular(f.Mode) || !under(f.Name, prefix) || !isYAML(f.Name) {
				return nil
			}
			if f.Size > maxIndexedFile {
				ix.skip(f.Name, "larger than 1 MiB")
				return nil
			}
			content, err := f.Contents()
			if err != nil {
				return fmt.Errorf("%s: %w", f.Name, err)
			}
			ix.add(f.Name, []byte(content))
			return nil
		})
	})
	return ix, err
}

func (s *Server) fileGet(raw json.RawMessage) (any, error) {
	r, a, err := s.args(raw)
	if err != nil {
		return nil, err
	}
	p, err := cleanPath(a.Path, false)
	if err != nil {
		return nil, err
	}
	out := map[string]any{argRepo: r.cfg.Name, argPath: p}
	err = r.read(func(revision string, tree *object.Tree) error {
		entry, err := tree.FindEntry(p)
		if errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrDirectoryNotFound) {
			return fmt.Errorf("no file %s in %s", p, r.cfg.Name)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if !regular(entry.Mode) {
			return fmt.Errorf("%s is not a regular file (a directory, a symbolic link or a submodule)", p)
		}
		f, err := tree.TreeEntryFile(entry)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		f.Name = p
		if f.Size > maxFileSize {
			return fmt.Errorf("%s is %d bytes, past the %d KiB file_get returns", p, f.Size, maxFileSize>>10)
		}
		content, err := f.Contents()
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if !utf8.ValidString(content) {
			return fmt.Errorf("%s is not text", p)
		}
		out["revision"], out["content"] = revision, content
		return nil
	})
	return out, err
}
