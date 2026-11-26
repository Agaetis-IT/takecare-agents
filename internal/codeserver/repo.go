// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package codeserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/agaetis-it/takecare-agents/internal/gitauth"
)

// Repository is one of the customer's repositories the server reads: a
// remote and a branch, with a read-only credential.
type Repository struct {
	// Name is how the tools name it: lower-case letters, digits and
	// hyphens.
	Name string
	// URL is an HTTPS or SSH remote; Ref the branch.
	URL, Ref string
	// PollInterval is how often the branch is fetched.
	PollInterval time.Duration
	// CredentialFile holds an HTTPS token, read at every fetch;
	// CredentialUsername goes with it.
	CredentialFile, CredentialUsername string
	// SSHKeyFile and KnownHostsFile go together for an SSH remote.
	SSHKeyFile, KnownHostsFile string
	// CAFile is a PEM bundle trusted for HTTPS besides the system roots.
	CAFile string
}

// Bounds of the Git sources.
const (
	// MaxRepositories is the most repositories one server reads.
	MaxRepositories = 16
	pollDefault     = 5 * time.Minute
	pollMin         = 10 * time.Second
	cloneRetry      = 10 * time.Second
	gitTimeout      = 2 * time.Minute
)

var repoName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// repo keeps a single-branch clone in memory, fetches the branch on an
// interval, and serves the tree of its head.
type repo struct {
	cfg    Repository
	logger *slog.Logger
	now    func() time.Time
	auth   gitauth.Method
	retry  time.Duration

	// gitMu guards git: go-git's in-memory storage is not safe for
	// concurrent use, so a fetch and a read wait for each other.
	gitMu sync.Mutex
	git   *git.Repository

	mu        sync.Mutex
	head      *object.Commit
	lastFetch time.Time
	fetchErr  error
}

func newRepo(cfg Repository, logger *slog.Logger, now func() time.Time) (*repo, error) {
	if !repoName.MatchString(cfg.Name) {
		return nil, fmt.Errorf("repository name %q: lower-case letters, digits and hyphens, 63 at most", cfg.Name)
	}
	if cfg.Ref == "" {
		cfg.Ref = "main"
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = pollDefault
	}
	if cfg.PollInterval < pollMin {
		return nil, fmt.Errorf("repository %s: poll interval %s is below %s", cfg.Name, cfg.PollInterval, pollMin)
	}
	auth, err := gitauth.For(gitauth.Config{
		URL: cfg.URL, CredentialFile: cfg.CredentialFile, CredentialUsername: cfg.CredentialUsername,
		SSHKeyFile: cfg.SSHKeyFile, KnownHostsFile: cfg.KnownHostsFile, CAFile: cfg.CAFile, Timeout: gitTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("repository %s: %w", cfg.Name, err)
	}
	return &repo{cfg: cfg, logger: logger, now: now, auth: auth, retry: cloneRetry}, nil
}

// run clones, then fetches every poll interval; a clone that fails is
// tried again sooner.
func (r *repo) run(ctx context.Context) {
	for {
		r.sync(ctx)
		wait := r.cfg.PollInterval
		if r.served() == nil {
			wait = r.retry
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// sync fetches the branch and serves its head; a failed fetch keeps the
// last head served.
func (r *repo) sync(ctx context.Context) {
	r.gitMu.Lock()
	defer r.gitMu.Unlock()
	head, err := r.fetch(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastFetch = r.now()
	r.fetchErr = err
	if err != nil {
		r.logger.Error("repository fetch failed", "repository", r.cfg.Name, "error", err)
		return
	}
	if r.head == nil || r.head.Hash != head.Hash {
		r.logger.Info("repository loaded", "repository", r.cfg.Name, "revision", head.Hash.String())
	}
	r.head = head
}

func (r *repo) fetch(ctx context.Context) (*object.Commit, error) {
	auth, err := r.auth()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	if r.git == nil {
		g, err := git.CloneContext(ctx, memory.NewStorage(), nil, &git.CloneOptions{
			URL: r.cfg.URL, Auth: auth, ReferenceName: plumbing.NewBranchReferenceName(r.cfg.Ref),
			SingleBranch: true, Tags: git.NoTags,
		})
		if err != nil {
			return nil, fmt.Errorf("clone %s: %w", gitauth.Redact(r.cfg.URL), err)
		}
		r.git = g
	} else {
		spec := config.RefSpec("+refs/heads/" + r.cfg.Ref + ":refs/remotes/origin/" + r.cfg.Ref)
		err := r.git.FetchContext(ctx, &git.FetchOptions{Auth: auth, RefSpecs: []config.RefSpec{spec}, Tags: git.NoTags, Force: true})
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return nil, fmt.Errorf("fetch %s: %w", r.cfg.Ref, err)
		}
	}
	ref, err := r.git.Reference(plumbing.NewRemoteReferenceName("origin", r.cfg.Ref), true)
	if err != nil {
		return nil, fmt.Errorf("branch %s: %w", r.cfg.Ref, err)
	}
	head, err := r.git.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("commit %s: %w", ref.Hash(), err)
	}
	return head, nil
}

func (r *repo) served() *object.Commit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.head
}

// read runs fn on the tree of the head served, or says why none is.
func (r *repo) read(fn func(revision string, tree *object.Tree) error) error {
	head := r.served()
	if head == nil {
		r.mu.Lock()
		err := r.fetchErr
		r.mu.Unlock()
		if err != nil {
			return fmt.Errorf("repository %s is not loaded: %w", r.cfg.Name, err)
		}
		return fmt.Errorf("repository %s is not loaded yet", r.cfg.Name)
	}
	r.gitMu.Lock()
	defer r.gitMu.Unlock()
	tree, err := head.Tree()
	if err != nil {
		return fmt.Errorf("repository %s: tree: %w", r.cfg.Name, err)
	}
	return fn(head.Hash.String(), tree)
}

// state is a repository as repositories_list and /statusz show it: never
// a credential.
type state struct {
	Name         string     `json:"name"`
	URL          string     `json:"url"`
	Branch       string     `json:"branch"`
	Revision     string     `json:"revision,omitempty"`
	RevisionTime *time.Time `json:"revisionTime,omitempty"`
	LastFetch    *time.Time `json:"lastFetch,omitempty"`
	FetchError   string     `json:"fetchError,omitempty"`
}

func (r *repo) state() state {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := state{Name: r.cfg.Name, URL: gitauth.Redact(r.cfg.URL), Branch: r.cfg.Ref}
	if r.head != nil {
		when := r.head.Committer.When.UTC()
		st.Revision, st.RevisionTime = r.head.Hash.String(), &when
	}
	if !r.lastFetch.IsZero() {
		at := r.lastFetch
		st.LastFetch = &at
	}
	if r.fetchErr != nil {
		st.FetchError = r.fetchErr.Error()
	}
	return st
}
