// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package gitauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"golang.org/x/crypto/ssh"
)

const (
	sshRemote   = "git@github.com:example/map.git"
	httpsRemote = "https://git.example.com/map.git"
)

func TestForRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  Config
		want string
	}{
		"SSH without known hosts": {Config{URL: sshRemote, SSHKeyFile: "/key"}, "host keys are always checked"},
		"plain HTTP":              {Config{URL: "http://git.example.com/map.git"}, "use https or ssh"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := For(tc.cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if auth, err := For(Config{URL: httpsRemote}); err != nil {
		t.Fatal(err)
	} else if a, err := auth(); a != nil || err != nil {
		t.Errorf("an HTTPS remote without a credential: auth %v, %v, want anonymous", a, err)
	}
}

func TestAuth(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := Token("oauth2", token)
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
	a, err = SSH("git", key, hosts)
	if err != nil || a.Name() != "ssh-public-keys" {
		t.Errorf("ssh auth = %v, %v", a, err)
	}
	if _, err := SSH("git", key, filepath.Join(dir, "none")); err == nil {
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
		if got := Redact(in); got != want {
			t.Errorf("Redact(%s) = %s, want %s", in, got, want)
		}
	}
}
