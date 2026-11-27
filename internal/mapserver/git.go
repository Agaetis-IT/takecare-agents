// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mapserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/agaetis-it/takecare-agents/internal/gitauth"
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

// gitSource serves the Map of a branch's head and, for map_at, the Map at
// any revision of the branch, without keeping the repository in memory.
// On every poll it lists the branch's head on the remote; when the head
// moved, it clones the branch into a fresh storage, keeps a compact
// history of it (each commit's hash, date and first parent, and the trees
// and files under map/, shared between revisions) and drops the clone.
// Its memory is the Map's own history, whatever else the repository holds
// and however often it is polled; a clone of the whole branch is held
// only while a moved head is read.
type gitSource struct {
	cfg    GitConfig
	logger *slog.Logger
	now    func() time.Time
	auth   gitauth.Method
	// retry is how soon a failed clone is tried again.
	retry time.Duration

	// histMu guards hist and cache. A history is never changed once
	// built: a moved head brings a new one, built by the run loop alone,
	// so that map_at never waits for a clone.
	histMu sync.Mutex
	hist   *history
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

// history is the compact history of a branch.
type history struct {
	// revs are the commits reachable from the head, in committer-time
	// order (newest first, as git log --date-order walks them); head is
	// the head's index.
	revs []rev
	head int
	// byHash indexes revs.
	byHash map[plumbing.Hash]int
	// store holds the trees and files under map/ of every revision, and
	// nothing else.
	store *memory.Storage
}

// rev is one commit of a history.
type rev struct {
	hash plumbing.Hash
	when time.Time
	// parent is the first parent's index in revs, -1 for none.
	parent int
	// mapTree is the tree of map/ under the configured path; missing says
	// why there is none.
	mapTree plumbing.Hash
	missing string
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
	auth, err := gitauth.For(gitauth.Config{
		URL: cfg.URL, CredentialFile: cfg.CredentialFile, CredentialUsername: cfg.CredentialUsername,
		SSHKeyFile: cfg.SSHKeyFile, KnownHostsFile: cfg.KnownHostsFile, CAFile: cfg.CAFile, Timeout: gitTimeout,
	})
	if err != nil {
		return nil, err
	}
	g := &gitSource{cfg: cfg, logger: logger, now: now, retry: cloneRetry, auth: auth}
	return g, nil
}

// run clones, then polls every poll interval; a clone that fails is
// tried again sooner.
func (g *gitSource) run(ctx context.Context) error {
	for {
		g.sync(ctx)
		wait := g.cfg.PollInterval
		if g.loaded() == nil {
			wait = g.retry
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

func (g *gitSource) loaded() *history {
	g.histMu.Lock()
	defer g.histMu.Unlock()
	return g.hist
}

// sync reads the branch again when its head moved and loads the new head;
// a failed fetch keeps the history and the Map served.
func (g *gitSource) sync(ctx context.Context) {
	hist, err := g.fetch(ctx, g.loaded())
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
	if hist == nil {
		return // the head is the one loaded
	}
	g.histMu.Lock()
	defer g.histMu.Unlock()
	g.hist = hist
	g.load()
}

// fetch lists the branch's head on the remote and, when it is not the
// head of old, clones the branch into a fresh storage and returns its
// compact history. A nil history means old is current.
func (g *gitSource) fetch(ctx context.Context, old *history) (*history, error) {
	auth, err := g.auth()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	branch := plumbing.NewBranchReferenceName(g.cfg.Ref)
	if old != nil {
		remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: git.DefaultRemoteName, URLs: []string{g.cfg.URL}})
		refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth})
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", gitauth.Redact(g.cfg.URL), err)
		}
		i := slices.IndexFunc(refs, func(ref *plumbing.Reference) bool { return ref.Name() == branch })
		if i < 0 {
			return nil, fmt.Errorf("branch %s: %w", g.cfg.Ref, plumbing.ErrReferenceNotFound)
		}
		if refs[i].Hash() == old.revs[old.head].hash {
			return nil, nil
		}
	}
	repo, err := git.CloneContext(ctx, memory.NewStorage(), nil, &git.CloneOptions{
		URL: g.cfg.URL, Auth: auth, ReferenceName: branch, SingleBranch: true, Tags: git.NoTags,
	})
	if err != nil {
		return nil, fmt.Errorf("clone %s: %w", gitauth.Redact(g.cfg.URL), err)
	}
	ref, err := repo.Reference(plumbing.NewRemoteReferenceName(git.DefaultRemoteName, g.cfg.Ref), true)
	if err != nil {
		return nil, fmt.Errorf("branch %s: %w", g.cfg.Ref, err)
	}
	hist, err := g.compact(ctx, repo, ref.Hash(), old)
	if err != nil {
		return nil, fmt.Errorf("history of %s: %w", g.cfg.Ref, err)
	}
	return hist, nil
}

// compact walks the clone from head and keeps, per commit, what map_at
// needs; what old already knew of a commit is not read again.
func (g *gitSource) compact(ctx context.Context, repo *git.Repository, head plumbing.Hash, old *history) (*history, error) {
	iter, err := repo.Log(&git.LogOptions{From: head, Order: git.LogOrderCommitterTime})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	h := &history{byHash: map[plumbing.Hash]int{}, store: memory.NewStorage()}
	var parents []plumbing.Hash
	err = iter.ForEach(func(c *object.Commit) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		r := rev{hash: c.Hash, when: c.Committer.When, parent: -1}
		if i, ok := old.index(c.Hash); ok {
			r.mapTree, r.missing = old.revs[i].mapTree, old.revs[i].missing
		} else {
			r.mapTree, r.missing = mapTree(c, g.cfg.Path)
		}
		if !r.mapTree.IsZero() {
			if err := copyTree(h.store, repo.Storer, r.mapTree); err != nil {
				return fmt.Errorf("map/ of %s: %w", c.Hash, err)
			}
		}
		parent := plumbing.ZeroHash
		if len(c.ParentHashes) > 0 {
			parent = c.ParentHashes[0]
		}
		h.byHash[c.Hash] = len(h.revs)
		h.revs = append(h.revs, r)
		parents = append(parents, parent)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i, p := range parents {
		if j, ok := h.byHash[p]; ok {
			h.revs[i].parent = j
		}
	}
	var ok bool
	if h.head, ok = h.byHash[head]; !ok {
		return nil, fmt.Errorf("head %s not in its own history", head)
	}
	return h, nil
}

func (h *history) index(hash plumbing.Hash) (int, bool) {
	if h == nil {
		return 0, false
	}
	i, ok := h.byHash[hash]
	return i, ok
}

// mapTree finds map/ under path in a commit's tree, or says why it is
// missing.
func mapTree(commit *object.Commit, path string) (plumbing.Hash, string) {
	tree, err := commit.Tree()
	if err != nil {
		return plumbing.ZeroHash, "tree: " + err.Error()
	}
	if p := strings.Trim(path, "/"); p != "" && p != "." {
		if tree, err = tree.Tree(p); err != nil {
			return plumbing.ZeroHash, fmt.Sprintf("path %s: %v", p, err)
		}
	}
	entry, err := tree.FindEntry("map")
	if err != nil || entry.Mode != filemode.Dir {
		return plumbing.ZeroHash, "no map/ directory under " + path
	}
	return entry.Hash, ""
}

// copyTree copies a tree, its subtrees and its files from src into dst,
// sharing their bytes; a tree already in dst is complete (it is stored
// after its entries) and not walked again.
func copyTree(dst *memory.Storage, src storer.EncodedObjectStorer, hash plumbing.Hash) error {
	if dst.HasEncodedObject(hash) == nil {
		return nil
	}
	obj, err := src.EncodedObject(plumbing.TreeObject, hash)
	if err != nil {
		return err
	}
	tree, err := object.DecodeTree(src, obj)
	if err != nil {
		return err
	}
	for _, e := range tree.Entries {
		switch {
		case e.Mode == filemode.Dir:
			err = copyTree(dst, src, e.Hash)
		case e.Mode.IsFile() && dst.HasEncodedObject(e.Hash) != nil:
			var blob plumbing.EncodedObject
			if blob, err = src.EncodedObject(plumbing.BlobObject, e.Hash); err == nil {
				_, err = dst.SetEncodedObject(blob)
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", e.Name, err)
		}
	}
	_, err = dst.SetEncodedObject(obj)
	return err
}

// load validates the head of the history; an invalid revision is
// reported once and the last valid one keeps serving. The caller holds
// histMu.
func (g *gitSource) load() {
	h := g.hist
	hash := h.revs[h.head].hash.String()
	g.mu.Lock()
	known := (g.valid != nil && g.valid.Revision == hash) || g.badCommit == hash
	g.mu.Unlock()
	if known {
		return
	}
	m, err := h.revision(h.head, g.cfg.Path)
	if err == nil {
		g.serve(m, nil)
		return
	}
	err = fmt.Errorf("revision %s does not validate: %w", hash, err)
	g.invalid(hash, err)
	g.mu.Lock()
	started := g.valid != nil
	g.mu.Unlock()
	if !started {
		// A start on a bad head serves the newest valid revision before it.
		g.serve(h.lastValid(h.head, g.cfg.Path), err)
	}
}

// revision loads the Map of revs[i], its hash and date as revision and
// time.
func (h *history) revision(i int, path string) (*Map, error) {
	r := h.revs[i]
	if r.missing != "" {
		return nil, errors.New(r.missing)
	}
	tree, err := object.GetTree(h.store, r.mapTree)
	if err != nil {
		return nil, fmt.Errorf("map/: %w", err)
	}
	files := map[string][]byte{}
	err = tree.Files().ForEach(func(f *object.File) error {
		name := "map/" + f.Name
		if strings.Contains("/"+name, "/.") {
			return nil
		}
		content, err := f.Contents()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		files[name] = []byte(content)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no map/ directory under %s", path)
	}
	m, err := build(files)
	if err != nil {
		return nil, err
	}
	m.Revision, m.LoadedAt = r.hash.String(), r.when.UTC()
	return m, nil
}

// lastValid walks the first parents of revs[i] for the newest revision
// that validates, walkBack commits at most; nil if none does.
func (h *history) lastValid(i int, path string) *Map {
	for range walkBack {
		if i = h.revs[i].parent; i < 0 {
			return nil
		}
		if m, err := h.revision(i, path); err == nil {
			return m
		}
	}
	return nil
}

// serve makes m the served Map; a head that did not validate stays the
// last error. The caller holds histMu.
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

// at returns the Map of the last commit of the branch at or before t, in
// committer-time order; a revision that does not validate is an error,
// never a fall-back to another.
func (g *gitSource) at(t time.Time) (*Map, error) {
	g.histMu.Lock()
	defer g.histMu.Unlock()
	h := g.hist
	if h == nil {
		return g.current()
	}
	i := slices.IndexFunc(h.revs, func(r rev) bool { return !r.when.After(t) })
	if i < 0 {
		return nil, errors.New("date before the first revision")
	}
	r := h.revs[i]
	if m, ok := g.cache[r.hash.String()]; ok {
		return m, nil
	}
	m, err := h.revision(i, g.cfg.Path)
	if err != nil {
		first, _, _ := strings.Cut(err.Error(), "\n")
		return nil, fmt.Errorf("revision %s at %s does not validate: %s", r.hash, r.when.UTC().Format(time.RFC3339), first)
	}
	g.remember(m)
	return m, nil
}

// remember caches a revision, evicting the oldest beyond cacheSize; the
// caller holds histMu.
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
	st := map[string]any{"source": "git", "url": gitauth.Redact(g.cfg.URL), "ref": g.cfg.Ref, "path": g.cfg.Path,
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
