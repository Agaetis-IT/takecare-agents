// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mapserver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"golang.org/x/crypto/ssh"

	"github.com/agaetis-it/takecare-agents/internal/golden"
)

var fileTransport sync.Once

// remote is a repository on disk the Git source clones over file://,
// served in process: no git binary. The in-process server opens the
// repository's .git as a bare one.
type remote struct {
	t    *testing.T
	dir  string
	repo *git.Repository
}

func newRemote(t *testing.T, sub string) *remote {
	t.Helper()
	fileTransport.Do(func() { client.InstallProtocol("file", server.NewClient(server.DefaultLoader)) })
	dir := t.TempDir()
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{DefaultBranch: plumbing.Main})
	if err != nil {
		t.Fatal(err)
	}
	r := &remote{t: t, dir: dir, repo: repo}
	// The demo Map, under sub.
	err = fs.WalkDir(os.DirFS(demoDir), "map", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(os.DirFS(demoDir), p)
		if err == nil {
			r.write(filepath.Join(sub, p), string(raw))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *remote) url() string { return "file://" + filepath.Join(r.dir, ".git") }

func (r *remote) write(p, content string) {
	r.t.Helper()
	full := filepath.Join(r.dir, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil { //nolint:gosec // the test's own temporary directory
		r.t.Fatal(err)
	}
}

// commit records the work tree on main at a date.
func (r *remote) commit(msg string, day int) string {
	r.t.Helper()
	wt, err := r.repo.Worktree()
	if err != nil {
		r.t.Fatal(err)
	}
	if err := wt.AddGlob("."); err != nil {
		r.t.Fatal(err)
	}
	sig := &object.Signature{Name: "Map tooling", Email: "map@example.com", When: time.Date(2026, 11, day, 12, 0, 0, 0, time.UTC)}
	h, err := wt.Commit(msg, &git.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		r.t.Fatal(err)
	}
	return h.String()
}

const (
	sshRemote    = "git@github.com:example/map.git"
	httpsRemote  = "https://git.example.com/map.git"
	billingFile  = "map/service/billing-api.yaml"
	billingBoth  = "dependsOn: [datastore/billing-db, network/billing-db-proxy]"
	billingProxy = "dependsOn: [network/billing-db-proxy]"
)

// history commits the demo Map (day 1), a change (day 3) and a fault (day 5).
func (r *remote) history(sub string) (c1, c2, c3 string) {
	c1 = r.commit("the demo Map", 1)
	raw, err := os.ReadFile(filepath.Join(r.dir, sub, billingFile)) //nolint:gosec // the test's own temporary directory
	if err != nil {
		r.t.Fatal(err)
	}
	r.write(filepath.Join(sub, billingFile), strings.Replace(string(raw), billingBoth, billingProxy, 1))
	c2 = r.commit("billing-api reaches the database through its proxy only", 3)
	r.write(filepath.Join(sub, "map/runbook/broken.yaml"), "key: runbook/broken\nprovenance: declared\nstatus: validated\ncreated: 2026-11-05\nrelations: {dependsOn: [service/none]}\n")
	c3 = r.commit("a runbook pointing nowhere", 5)
	return c1, c2, c3
}

var fetchedAt = time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

func gitServer(t *testing.T, cfg GitConfig) (*Server, *gitSource) {
	t.Helper()
	s, err := New(Config{Git: &cfg, Logger: slog.New(slog.DiscardHandler), Now: func() time.Time { return fetchedAt }})
	if err != nil {
		t.Fatal(err)
	}
	return s, s.src.(*gitSource)
}

func TestGitSource(t *testing.T) {
	r := newRemote(t, ".")
	_, c2, c3 := r.history(".")
	s, g := gitServer(t, GitConfig{URL: r.url()})
	ctx := context.Background()

	g.sync(ctx)
	m, err := s.snapshot()
	if err != nil || m.Revision != c2 || !m.LoadedAt.Equal(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("a start on a bad head serves %v (%v), want commit 2", m, err)
	}
	status := g.status()
	status["url"] = "file:///remote/.git" // a temporary directory
	st, _ := json.MarshalIndent(status, "", "  ")
	golden.Assert(t, "statusz-git.json", append(st, '\n'))
	if !strings.Contains(string(st), c3) {
		t.Errorf("lastError does not name commit 3: %s", st)
	}
	if got, _ := json.Marshal(m.Entries["service/billing-api"].Relations["dependsOn"]); string(got) != `["network/billing-db-proxy"]` {
		t.Errorf("commit 2's billing-api depends on %s", got)
	}

	t.Run("the next poll picks up a fix", func(t *testing.T) {
		if err := os.Remove(filepath.Join(r.dir, "map/runbook/broken.yaml")); err != nil {
			t.Fatal(err)
		}
		c4 := r.commit("the runbook goes", 6)
		g.sync(ctx)
		m, _ := s.snapshot()
		if m.Revision != c4 || g.status()["lastError"] != nil {
			t.Errorf("serving %s with %v, want commit 4 and no error", m.Revision, g.status()["lastError"])
		}
	})

	t.Run("a credential that cannot be read keeps the Map served", func(t *testing.T) {
		g.auth = func() (transport.AuthMethod, error) {
			return tokenAuth("x-access-token", filepath.Join(t.TempDir(), "missing"))
		}
		g.sync(ctx)
		if _, err := s.snapshot(); err != nil || g.status()["fetchError"] == nil {
			t.Errorf("snapshot %v, fetchError %v", err, g.status()["fetchError"])
		}
	})
}

func TestGitPath(t *testing.T) {
	r := newRemote(t, "ops/catalog")
	c1 := r.commit("the demo Map under ops/catalog", 1)
	s, g := gitServer(t, GitConfig{URL: r.url(), Path: "ops/catalog"})
	g.sync(context.Background())
	if m, err := s.snapshot(); err != nil || m.Revision != c1 || len(m.Entries) != 10 {
		t.Errorf("snapshot %v, %v", m, err)
	}
}

func TestGitConfigRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  GitConfig
		want string
	}{
		"SSH without known hosts": {GitConfig{URL: sshRemote, SSHKeyFile: "/key"}, "host keys are always checked"},
		"plain HTTP":              {GitConfig{URL: "http://git.example.com/map.git"}, "use https or ssh"},
		"a poll too often":        {GitConfig{URL: httpsRemote, PollInterval: time.Second}, "below 10s"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newGitSource(tc.cfg, slog.New(slog.DiscardHandler), time.Now); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestAuth(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := tokenAuth("oauth2", token)
	if b, ok := a.(*githttp.BasicAuth); err != nil || !ok || b.Username != "oauth2" || b.Password != "s3cret" {
		t.Errorf("token auth = %#v, %v", a, err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	key, hosts := filepath.Join(dir, "key"), filepath.Join(dir, "known_hosts")
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hosts, []byte("git.example.com "+string(ssh.MarshalAuthorizedKey(sshPub))), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err = sshAuth("git", key, hosts)
	if err != nil || a.Name() != "ssh-public-keys" {
		t.Errorf("ssh auth = %v, %v", a, err)
	}
	if _, err := sshAuth("git", key, filepath.Join(dir, "none")); err == nil {
		t.Error("a missing known_hosts file is accepted")
	}
}

// tokenRemote carries a made-up token, the point of TestRedact.
const (
	tokenRemote    = "https://x-access-token:s3cret@git.example.com/map.git" //nolint:gosec // made up
	redactedRemote = "https://x-access-token@git.example.com/map.git"        //nolint:gosec // a username, no token
)

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		tokenRemote: redactedRemote,
		httpsRemote: httpsRemote,
		sshRemote:   sshRemote,
	} {
		if got := redact(in); got != want {
			t.Errorf("redact(%s) = %s, want %s", in, got, want)
		}
	}
}
