// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package releasesserver

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Kind is what a source reads (spec 12 §7.1).
type Kind string

// Source kinds.
const (
	KindOCI       Kind = "oci"
	KindHelm      Kind = "helm"
	KindGitHub    Kind = "github"
	KindGitLab    Kind = "gitlab"
	KindEndOfLife Kind = "endoflife"
)

// schemeHTTPS is the one scheme a source is read over.
const schemeHTTPS = "https"

// MaxSources bounds the sources one server reads.
const MaxSources = 32

// maxComponents bounds a source's components.
const maxComponents = 128

// Source is one upstream the server reads, and only its host (with its
// declared token host) is ever called.
type Source struct {
	Name string
	Kind Kind
	// URL is the upstream's base: an OCI registry (https://ghcr.io), a
	// Helm repository (the directory of its index.yaml), a forge's REST
	// API (https://api.github.com, https://gitlab.com/api/v4) or
	// endoflife.date's API (https://endoflife.date/api/v1).
	URL string
	// AuthHost is the one other host an oci source may call: its token
	// realm when the registry names another host (Docker Hub's
	// auth.docker.io). Empty: the realm must be the registry's own host.
	AuthHost string
	// TokenFile is where the read token is mounted; empty: anonymous.
	TokenFile string
	// Username, with a token, sends it as HTTP basic credentials (a
	// Harbor or Artifactory robot account); empty: a Bearer token.
	Username string
	// TokenSecret is the Secret's name, shown by sources_list, never its
	// content.
	TokenSecret string
	// Components map inventory entries to a name of this source; for
	// github, gitlab and endoflife, the only names it serves.
	Components []Component
}

// Component maps an inventory entry (a Map key, an image repository or a
// chart name) to a name of its source.
type Component struct {
	Match string `json:"match"`
	Name  string `json:"name"`
}

// CacheConfig bounds the cache (spec 12 §7.1: once a day, dropped past 7
// days stale).
type CacheConfig struct {
	Refresh    time.Duration
	StaleMax   time.Duration
	MaxEntries int
}

var (
	sourceName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	hostName   = regexp.MustCompile(`^[A-Za-z0-9]([-.A-Za-z0-9]{0,252})$`)
	// The OCI distribution spec's repository name, slash-separated
	// components of lower-case letters and digits with separators.
	ociName = regexp.MustCompile(`^[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*(/[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*)*$`)
	// A chart name: letters, digits, dots, underscores and hyphens.
	chartName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	// owner/repo on GitHub.
	githubName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)
	// A project's full path on GitLab: groups and the project.
	gitlabName = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+){1,19}$`)
	// An endoflife.date product, as in its URLs.
	productName = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,62}[a-z0-9])?$`)
)

// validName checks a name given by a caller or a configuration for a
// kind: its pattern, and no . or .. segment, so that no name escapes its
// path on the upstream.
func validName(k Kind, name string) bool {
	if len(name) > 255 {
		return false
	}
	for seg := range strings.SplitSeq(name, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	switch k {
	case KindOCI:
		return ociName.MatchString(name)
	case KindHelm:
		return chartName.MatchString(name) && !strings.Contains(name, "..")
	case KindGitHub:
		return githubName.MatchString(name)
	case KindGitLab:
		return gitlabName.MatchString(name) && !strings.HasSuffix(name, ".git")
	case KindEndOfLife:
		return productName.MatchString(name) && !strings.Contains(name, "..")
	}
	return false
}

// baseURL checks a source's URL: https, a host, no user information,
// query or fragment. The URL returned has no trailing slash.
func baseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("url: not a URL")
	}
	switch {
	case u.Scheme != schemeHTTPS:
		return nil, fmt.Errorf("url: https only (helm sources on oci:// are refused: use an oci source)")
	case u.User != nil:
		return nil, fmt.Errorf("url: carries user information: the credential comes from tokenSecretRef, never the URL")
	case u.Host == "" || !hostName.MatchString(u.Hostname()):
		return nil, fmt.Errorf("url: no valid host")
	case u.RawQuery != "" || u.Fragment != "" || u.Opaque != "":
		return nil, fmt.Errorf("url: no query or fragment")
	case strings.Contains(u.Path, "/../") || strings.HasSuffix(u.Path, "/.."):
		return nil, fmt.Errorf("url: no .. segment")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

// validate checks one source and returns its base URL.
func (s Source) validate() (*url.URL, error) {
	if !sourceName.MatchString(s.Name) {
		return nil, fmt.Errorf("source %q: a name of lower-case letters, digits and hyphens, 63 at most", s.Name)
	}
	switch s.Kind {
	case KindOCI, KindHelm, KindGitHub, KindGitLab, KindEndOfLife:
	default:
		return nil, fmt.Errorf("source %s: kind %q: oci, helm, github, gitlab or endoflife", s.Name, s.Kind)
	}
	u, err := baseURL(s.URL)
	if err != nil {
		return nil, fmt.Errorf("source %s: %w", s.Name, err)
	}
	if s.AuthHost != "" && (s.Kind != KindOCI || !hostName.MatchString(s.AuthHost)) {
		return nil, fmt.Errorf("source %s: authHost is an oci source's token host, a host name", s.Name)
	}
	if s.Username != "" && (s.TokenFile == "" || (s.Kind != KindOCI && s.Kind != KindHelm)) {
		return nil, fmt.Errorf("source %s: a username goes with a token, on an oci or helm source", s.Name)
	}
	if len(s.Components) > maxComponents {
		return nil, fmt.Errorf("source %s: %d components at most", s.Name, maxComponents)
	}
	listed := s.Kind == KindGitHub || s.Kind == KindGitLab || s.Kind == KindEndOfLife
	if listed && len(s.Components) == 0 {
		return nil, fmt.Errorf("source %s: a %s source serves the names its components list: list one at least", s.Name, s.Kind)
	}
	for _, c := range s.Components {
		if c.Match == "" || len(c.Match) > 255 || !validName(s.Kind, c.Name) {
			return nil, fmt.Errorf("source %s: component %q -> %q: a match and a valid %s name", s.Name, c.Match, c.Name, s.Kind)
		}
	}
	return u, nil
}
