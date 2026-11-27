// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package releasesserver

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
)

// Bounds of what is read of an upstream's answer.
const (
	maxJSON     = 4 << 20
	maxIndex    = 16 << 20
	maxRedirect = 3
)

// errRedirect refuses a redirect to another host or off https: the
// server calls only its sources' hosts (spec 12 §7.1).
var errRedirect = errors.New("a redirect off the source's host is refused")

// checkRedirect follows at most three redirects, each on https and on the
// first request's host.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirect || req.URL.Scheme != schemeHTTPS || req.URL.Host != via[0].URL.Host {
		return errRedirect
	}
	return nil
}

// source is a configured source with what the server keeps of it.
type source struct {
	Source
	base *url.URL
	// fetch runs the calls to one upstream: one at a time, which
	// also bounds the memory of large Helm indexes.
	fetch sync.Mutex

	mu      sync.Mutex
	last    string
	outcome string
}

// allowed says whether a URL is on one of the source's hosts.
func (s *source) allowed(u *url.URL) bool {
	if u.Scheme != schemeHTTPS || u.User != nil {
		return false
	}
	return u.Host == s.base.Host || (s.AuthHost != "" && u.Hostname() == s.AuthHost)
}

// token reads the source's token at each fetch, so that a renewed Secret
// is taken without a restart; "" when the source is anonymous.
func (s *source) token() (string, error) {
	if s.TokenFile == "" {
		return "", nil
	}
	raw, err := os.ReadFile(s.TokenFile)
	if err != nil {
		return "", errors.New("read the token file")
	}
	t := strings.TrimSpace(string(raw))
	if t == "" {
		return "", errors.New("the token file is empty")
	}
	return t, nil
}

// credentials is the Authorization header's value for the source, "" when
// anonymous: basic with a username, else Bearer.
func (s *source) credentials() (string, error) {
	tok, err := s.token()
	if err != nil || tok == "" {
		return "", err
	}
	if s.Username != "" {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(s.Username+":"+tok)), nil
	}
	return "Bearer " + tok, nil
}

// statusError is an upstream's answer outside 2xx.
type statusError struct {
	status int
	header http.Header
}

func (e *statusError) Error() string { return fmt.Sprintf("answered %d", e.status) }

// get calls u on one of the source's hosts and returns at most limit bytes
// of a 2xx answer. Its errors name the host and the path only: never a
// query, never a credential, which travel in headers.
func (r *Server) get(ctx context.Context, s *source, u *url.URL, header http.Header, limit int64) ([]byte, http.Header, error) {
	where := u.Host + u.EscapedPath()
	if !s.allowed(u) {
		return nil, nil, fmt.Errorf("GET %s: not a host of source %s, refused", u.Host, s.Name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("GET %s: build the request", where)
	}
	maps.Copy(req.Header, header)
	req.Header.Set("User-Agent", "takecare-agents-mcp-releases")
	res, err := r.client.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return nil, nil, fmt.Errorf("GET %s: %w", where, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		return nil, res.Header, fmt.Errorf("GET %s: %w", where, &statusError{status: res.StatusCode, header: res.Header})
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, nil, fmt.Errorf("GET %s: read the answer: %w", where, err)
	}
	if int64(len(raw)) > limit {
		return nil, nil, fmt.Errorf("GET %s: the answer is larger than %d MiB", where, limit>>20)
	}
	return raw, res.Header, nil
}

// join adds path segments to a base URL, each escaped as one segment: a
// slash inside one (a GitLab project's path) is sent as %2F.
func join(base *url.URL, segments ...string) *url.URL {
	u := *base
	raw, plain := base.EscapedPath(), base.Path
	for _, s := range segments {
		raw += "/" + url.PathEscape(s)
		plain += "/" + s
	}
	u.Path, u.RawPath = plain, raw
	return &u
}
