// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mapserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/agaetis-it/takecare-agents/internal/httpclient"
)

// GitConfig is the customer's repository holding the Map (spec 08 §5): one
// repository and one branch, read with a read-only credential.
type GitConfig struct {
	// URL is an HTTPS or SSH remote.
	URL string
	// Ref is a branch name; Path the directory holding map/.
	Ref, Path string
	// PollInterval is how often the branch is fetched.
	PollInterval time.Duration
	// CredentialFile holds an HTTPS token, read at every fetch;
	// CredentialUsername goes with it.
	CredentialFile, CredentialUsername string
	// SSHKeyFile and KnownHostsFile go together for an SSH remote; both are
	// read at every fetch, and a host key is always checked.
	SSHKeyFile, KnownHostsFile string
	// CAFile is a PEM bundle trusted for HTTPS besides the system roots.
	CAFile string
}

// Bounds of the Git source.
const (
	pollDefault = time.Minute
	pollMin     = 10 * time.Second
	cloneRetry  = 10 * time.Second
	gitTimeout  = 2 * time.Minute
	// walkBack bounds the search for a valid revision when none is loaded
	// yet and the head does not validate.
	walkBack = 16
	// cacheSize bounds the revisions map_at keeps loaded.
	cacheSize = 16
)

// gitSource keeps a clone in memory (a Map is small, and the history is
// what map_at will read), fetches the branch on an interval, and serves the
// last revision that validated.
type gitSource struct {
	cfg    GitConfig
	logger *slog.Logger
	now    func() time.Time
	auth   func() (transport.AuthMethod, error)
	// retry is how soon a failed clone is tried again.
	retry time.Duration

	// repoMu guards repo and cache: go-git's in-memory storage is not safe
	// for concurrent use; the run loop fetches, map_at reads the history,
	// and a map_at call waits while a fetch runs (bounded by gitTimeout).
	repoMu sync.Mutex
	repo   *git.Repository
	// cache holds the revisions map_at loaded, by commit, the oldest
	// evicted first.
	cache      map[string]*Map
	cacheOrder []string

	mu         sync.Mutex
	valid      *Map
	lastFetch  time.Time
	fetchErr   error
	fetchErrAt time.Time
	badCommit  string
	lastErr    error
	lastErrAt  time.Time
}

func newGitSource(cfg GitConfig, logger *slog.Logger, now func() time.Time) (*gitSource, error) {
	if cfg.Ref == "" {
		cfg.Ref = "main"
	}
	if cfg.Path == "" {
		cfg.Path = "."
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = pollDefault
	}
	if cfg.PollInterval < pollMin {
		return nil, fmt.Errorf("poll interval %s is below %s", cfg.PollInterval, pollMin)
	}
	ep, err := transport.NewEndpoint(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("git URL: %w", err)
	}
	g := &gitSource{cfg: cfg, logger: logger, now: now, retry: cloneRetry}
	switch ep.Protocol {
	case "ssh":
		if cfg.SSHKeyFile == "" || cfg.KnownHostsFile == "" {
			return nil, errors.New("an SSH remote needs a key file and a known_hosts file: host keys are always checked")
		}
		user := ep.User
		if user == "" {
			user = "git"
		}
		g.auth = func() (transport.AuthMethod, error) { return sshAuth(user, cfg.SSHKeyFile, cfg.KnownHostsFile) }
	case "https":
		hc := httpclient.FromEnv(gitTimeout)
		if cfg.CAFile != "" {
			hc.CAFile = cfg.CAFile
		}
		c, err := hc.New()
		if err != nil {
			return nil, fmt.Errorf("the HTTPS client: %w", err)
		}
		// go-git takes its HTTP client per scheme; this one has the proxy,
		// the CA bundle and the timeouts, never http.DefaultClient.
		client.InstallProtocol("https", githttp.NewClient(c))
		if cfg.CredentialFile != "" {
			username := cfg.CredentialUsername
			if username == "" {
				username = "x-access-token"
			}
			g.auth = func() (transport.AuthMethod, error) { return tokenAuth(username, cfg.CredentialFile) }
		}
	case "file":
		// Tests and local runs.
	default:
		return nil, fmt.Errorf("git URL scheme %s: use https or ssh", ep.Protocol)
	}
	if g.auth == nil {
		g.auth = func() (transport.AuthMethod, error) { return nil, nil }
	}
	return g, nil
}

// tokenAuth reads an HTTPS token from its file.
func tokenAuth(username, file string) (transport.AuthMethod, error) {
	raw, err := os.ReadFile(file) //nolint:gosec // a mounted Secret, named by the operator
	if err != nil {
		return nil, fmt.Errorf("read the credential: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return nil, errors.New("the credential file is empty")
	}
	return &githttp.BasicAuth{Username: username, Password: token}, nil
}

// sshAuth reads a deploy key and the hosts it may talk to.
func sshAuth(user, keyFile, knownHosts string) (transport.AuthMethod, error) {
	key, err := os.ReadFile(keyFile) //nolint:gosec // a mounted Secret, named by the operator
	if err != nil {
		return nil, fmt.Errorf("read the SSH key: %w", err)
	}
	keys, err := gitssh.NewPublicKeys(user, key, "")
	if err != nil {
		return nil, fmt.Errorf("the SSH key: %w", err)
	}
	cb, err := gitssh.NewKnownHostsCallback(knownHosts)
	if err != nil {
		return nil, fmt.Errorf("the known_hosts file: %w", err)
	}
	keys.HostKeyCallback = cb
	return keys, nil
}

// run clones, then fetches every poll interval; a clone that fails is
// tried again sooner.
func (g *gitSource) run(ctx context.Context) error {
	for {
		g.sync(ctx)
		wait := g.cfg.PollInterval
		if g.repo == nil {
			wait = g.retry
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// sync fetches the branch and loads its head when it moved.
func (g *gitSource) sync(ctx context.Context) {
	g.repoMu.Lock()
	defer g.repoMu.Unlock()
	err := g.fetch(ctx)
	g.mu.Lock()
	g.lastFetch = g.now()
	if err != nil {
		g.fetchErr, g.fetchErrAt = err, g.lastFetch
	} else {
		g.fetchErr = nil
	}
	g.mu.Unlock()
	if err != nil {
		g.logger.Error("map fetch failed", keyError, err)
		return
	}
	g.load()
}

func (g *gitSource) fetch(ctx context.Context) error {
	auth, err := g.auth()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	if g.repo == nil {
		repo, err := git.CloneContext(ctx, memory.NewStorage(), nil, &git.CloneOptions{
			URL: g.cfg.URL, Auth: auth, ReferenceName: plumbing.NewBranchReferenceName(g.cfg.Ref),
			SingleBranch: true, Tags: git.NoTags,
		})
		if err != nil {
			return fmt.Errorf("clone %s: %w", redact(g.cfg.URL), err)
		}
		g.repo = repo
		return nil
	}
	spec := config.RefSpec("+refs/heads/" + g.cfg.Ref + ":refs/remotes/origin/" + g.cfg.Ref)
	err = g.repo.FetchContext(ctx, &git.FetchOptions{Auth: auth, RefSpecs: []config.RefSpec{spec}, Tags: git.NoTags, Force: true})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetch %s: %w", g.cfg.Ref, err)
	}
	return nil
}

// load validates the branch's head; an invalid revision is reported once
// and the last valid one keeps serving.
func (g *gitSource) load() {
	ref, err := g.repo.Reference(plumbing.NewRemoteReferenceName("origin", g.cfg.Ref), true)
	if err != nil {
		g.invalid("", fmt.Errorf("branch %s: %w", g.cfg.Ref, err))
		return
	}
	hash := ref.Hash().String()
	g.mu.Lock()
	known := (g.valid != nil && g.valid.Revision == hash) || g.badCommit == hash
	g.mu.Unlock()
	if known {
		return
	}
	commit, err := g.repo.CommitObject(ref.Hash())
	if err == nil {
		var m *Map
		if m, err = revision(commit, g.cfg.Path); err == nil {
			g.serve(m, nil)
			return
		}
	}
	err = fmt.Errorf("revision %s does not validate: %w", hash, err)
	g.invalid(hash, err)
	g.mu.Lock()
	started := g.valid != nil
	g.mu.Unlock()
	if !started && commit != nil {
		// A start on a bad head serves the newest valid revision before it.
		g.serve(g.lastValid(commit), err)
	}
}

// revision loads a commit's Map, its hash and date as revision and time.
func revision(commit *object.Commit, path string) (*Map, error) {
	m, err := treeMap(commit, path)
	if err != nil {
		return nil, err
	}
	m.Revision, m.LoadedAt = commit.Hash.String(), commit.Committer.When.UTC()
	return m, nil
}

// lastValid walks the first parents of a commit for the newest revision
// that validates, walkBack commits at most; nil if none does.
func (g *gitSource) lastValid(commit *object.Commit) *Map {
	for range walkBack {
		parent, err := commit.Parent(0)
		if err != nil {
			return nil
		}
		commit = parent
		if m, err := revision(commit, g.cfg.Path); err == nil {
			return m
		}
	}
	return nil
}

// serve makes m the served Map; a head that did not validate stays the
// last error. Called by the run loop, under repoMu.
func (g *gitSource) serve(m *Map, headErr error) {
	if m == nil {
		return
	}
	g.remember(m) // map_at on the served revision reads no tree again
	g.mu.Lock()
	g.valid = m
	if headErr == nil {
		g.lastErr, g.badCommit = nil, ""
	}
	g.mu.Unlock()
	g.logger.Info("map loaded", "revision", m.Revision, "entries", len(m.Entries))
}

func (g *gitSource) invalid(commit string, err error) {
	g.mu.Lock()
	g.badCommit, g.lastErr, g.lastErrAt = commit, err, g.now()
	g.mu.Unlock()
	g.logger.Error("map revision refused", "revision", commit, keyError, err)
}

// treeMap reads map/ under path in a commit's tree.
func treeMap(commit *object.Commit, path string) (*Map, error) {
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("tree: %w", err)
	}
	if p := strings.Trim(path, "/"); p != "" && p != "." {
		if tree, err = tree.Tree(p); err != nil {
			return nil, fmt.Errorf("path %s: %w", p, err)
		}
	}
	files := map[string][]byte{}
	err = tree.Files().ForEach(func(f *object.File) error {
		if !strings.HasPrefix(f.Name, "map/") || strings.Contains("/"+f.Name, "/.") {
			return nil
		}
		content, err := f.Contents()
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
		files[f.Name] = []byte(content)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no map/ directory under %s", path)
	}
	return build(files)
}

// at returns the Map of the last commit of the branch at or before t, in
// committer-time order; a revision that does not validate is an error,
// never a fall-back to another.
func (g *gitSource) at(t time.Time) (*Map, error) {
	g.repoMu.Lock()
	defer g.repoMu.Unlock()
	if g.repo == nil {
		return g.current()
	}
	ref, err := g.repo.Reference(plumbing.NewRemoteReferenceName("origin", g.cfg.Ref), true)
	if err != nil {
		return nil, fmt.Errorf("branch %s: %w", g.cfg.Ref, err)
	}
	iter, err := g.repo.Log(&git.LogOptions{From: ref.Hash(), Until: &t, Order: git.LogOrderCommitterTime})
	if err != nil {
		return nil, fmt.Errorf("history of %s: %w", g.cfg.Ref, err)
	}
	defer iter.Close()
	commit, err := iter.Next()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("date before the first revision")
	}
	if err != nil {
		return nil, fmt.Errorf("history of %s: %w", g.cfg.Ref, err)
	}
	if m, ok := g.cache[commit.Hash.String()]; ok {
		return m, nil
	}
	m, err := revision(commit, g.cfg.Path)
	if err != nil {
		first, _, _ := strings.Cut(err.Error(), "\n")
		return nil, fmt.Errorf("revision %s at %s does not validate: %s", commit.Hash, commit.Committer.When.UTC().Format(time.RFC3339), first)
	}
	g.remember(m)
	return m, nil
}

// remember caches a revision, evicting the oldest beyond cacheSize; the
// caller holds repoMu.
func (g *gitSource) remember(m *Map) {
	if g.cache == nil {
		g.cache = map[string]*Map{}
	}
	if _, ok := g.cache[m.Revision]; ok {
		return
	}
	g.cache[m.Revision] = m
	g.cacheOrder = append(g.cacheOrder, m.Revision)
	if len(g.cacheOrder) > cacheSize {
		delete(g.cache, g.cacheOrder[0])
		g.cacheOrder = g.cacheOrder[1:]
	}
}

func (g *gitSource) current() (*Map, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.valid != nil {
		return g.valid, nil
	}
	switch {
	case g.lastErr != nil:
		return nil, fmt.Errorf("no valid Map loaded: %w", g.lastErr)
	case g.fetchErr != nil:
		return nil, fmt.Errorf("no valid Map loaded: %w", g.fetchErr)
	}
	return nil, errors.New("no valid Map loaded yet")
}

func (g *gitSource) status() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := map[string]any{"source": "git", "url": redact(g.cfg.URL), "ref": g.cfg.Ref, "path": g.cfg.Path,
		"pollInterval": g.cfg.PollInterval.String(), keyEntries: 0}
	if g.valid != nil {
		st[keyRevision], st["revisionTime"], st[keyEntries] = g.valid.Revision, g.valid.LoadedAt, len(g.valid.Entries)
	}
	if !g.lastFetch.IsZero() {
		st["lastFetch"] = g.lastFetch
	}
	if g.fetchErr != nil {
		st["fetchError"] = map[string]any{keyError: g.fetchErr.Error(), "at": g.fetchErrAt}
	}
	if g.lastErr != nil {
		st["lastError"] = map[string]any{"commit": g.badCommit, keyError: g.lastErr.Error(), "at": g.lastErrAt}
	}
	return st
}

// redact drops a password or token written into a URL.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User(u.User.Username())
	return u.String()
}
