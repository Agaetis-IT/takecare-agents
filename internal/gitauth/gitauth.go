// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package gitauth is how TakeCare's Git readers (the Map server, the code
// server) reach a customer's remote with go-git: an HTTPS token or an SSH
// deploy key with its known_hosts, both read from mounted Secrets at every
// fetch, through the chart's HTTP client.
package gitauth

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"

	"github.com/agaetis-it/takecare-agents/internal/httpclient"
)

// Config is a remote and its read-only credential.
type Config struct {
	// URL is an HTTPS or SSH remote (file for tests and local runs).
	URL string
	// CredentialFile holds an HTTPS token, read at every fetch;
	// CredentialUsername goes with it.
	CredentialFile, CredentialUsername string
	// SSHKeyFile and KnownHostsFile go together for an SSH remote; both are
	// read at every fetch, and a host key is always checked.
	SSHKeyFile, KnownHostsFile string
	// CAFile is a PEM bundle trusted for HTTPS besides the system roots.
	CAFile string
	// Timeout bounds one HTTPS exchange.
	Timeout time.Duration
}

// Method is a remote's authentication, read again at every call: nil for
// an anonymous remote.
type Method func() (transport.AuthMethod, error)

// For checks a remote and returns its authentication. An HTTPS remote
// installs the chart's HTTP client (proxy, CA bundle, timeouts) for
// go-git's https scheme, never http.DefaultClient.
func For(cfg Config) (Method, error) {
	ep, err := transport.NewEndpoint(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("git URL: %w", err)
	}
	var auth Method
	switch ep.Protocol {
	case "ssh":
		if cfg.SSHKeyFile == "" || cfg.KnownHostsFile == "" {
			return nil, errors.New("an SSH remote needs a key file and a known_hosts file: host keys are always checked")
		}
		user := ep.User
		if user == "" {
			user = "git"
		}
		auth = func() (transport.AuthMethod, error) { return SSH(user, cfg.SSHKeyFile, cfg.KnownHostsFile) }
	case "https":
		hc := httpclient.FromEnv(cfg.Timeout)
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
			auth = func() (transport.AuthMethod, error) { return Token(username, cfg.CredentialFile) }
		}
	case "file":
		// Tests and local runs.
	default:
		return nil, fmt.Errorf("git URL scheme %s: use https or ssh", ep.Protocol)
	}
	if auth == nil {
		auth = func() (transport.AuthMethod, error) { return nil, nil }
	}
	return auth, nil
}

// Token reads an HTTPS token from its file.
func Token(username, file string) (transport.AuthMethod, error) {
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

// SSH reads a deploy key and the hosts it may talk to.
func SSH(user, keyFile, knownHosts string) (transport.AuthMethod, error) {
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

// Redact drops a password or token written into a URL.
func Redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User(u.User.Username())
	return u.String()
}
