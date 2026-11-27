// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package releasesserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Paging bounds per upstream.
const (
	ociPage       = 1000
	ociMaxPages   = 10
	forgePage     = 100
	forgeMaxPages = 3
)

// published is one version as an upstream gives it, before ordering.
type published struct {
	tag        string
	date       string
	prerelease bool
}

// Cycle is one release cycle of an endoflife.date product.
type Cycle struct {
	Name       string `json:"name"`
	Latest     string `json:"latest,omitempty"`
	Maintained bool   `json:"maintained"`
	EOL        bool   `json:"eol"`
	EOLFrom    string `json:"eolFrom,omitempty"`
	LTS        bool   `json:"lts,omitempty"`
}

const (
	headerAuthorization = "Authorization"
	headerAccept        = "Accept"
	keyPage             = "page"
	keyPerPage          = "per_page"
)

// versions reads a name's versions from its source.
func (r *Server) versions(ctx context.Context, s *source, name string) ([]published, error) {
	switch s.Kind {
	case KindOCI:
		return r.ociTags(ctx, s, name)
	case KindHelm:
		return r.helmVersions(ctx, s, name)
	case KindGitHub:
		return r.githubReleases(ctx, s, name)
	case KindGitLab:
		return r.gitlabReleases(ctx, s, name)
	}
	return nil, fmt.Errorf("source %s is %s: versions_list reads oci, helm, github and gitlab sources", s.Name, s.Kind)
}

// challengeParam reads one parameter of a WWW-Authenticate challenge.
var challengeParam = regexp.MustCompile(`([A-Za-z]+)="([^"]*)"`)

// ociTags lists a repository's tags (OCI distribution spec v1.1, "Listing
// Tags"), paged with n and the last tag the Link header names. A 401
// asking for a Bearer token is answered once from the challenge's realm
// (the Docker Registry token authentication), which must be the
// registry's host or the source's authHost.
func (r *Server) ociTags(ctx context.Context, s *source, name string) ([]published, error) {
	segments := append([]string{"v2"}, strings.Split(name, "/")...)
	u := join(s.base, append(segments, "tags", "list")...)
	header := http.Header{}
	cred, err := s.credentials()
	if err != nil {
		return nil, err
	}
	if cred != "" && s.Username == "" {
		header.Set(headerAuthorization, cred)
	}
	out, last, challenged := []published{}, "", false
	for page := 0; page < ociMaxPages; page++ {
		q := url.Values{"n": {strconv.Itoa(ociPage)}}
		if last != "" {
			q.Set("last", last)
		}
		u.RawQuery = q.Encode()
		raw, h, err := r.get(ctx, s, u, header, maxJSON)
		if se, ok := errors.AsType[*statusError](err); ok && se.status == http.StatusUnauthorized && !challenged {
			challenged = true
			auth, terr := r.ociAuth(ctx, s, name, se.header.Get("WWW-Authenticate"), cred)
			if terr != nil {
				return nil, terr
			}
			header.Set(headerAuthorization, auth)
			page--
			continue
		}
		if err != nil {
			return nil, err
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("source %s: the tag list is not JSON", s.Name)
		}
		for _, t := range body.Tags {
			out = append(out, published{tag: t})
		}
		next := nextLast(h.Get("Link"))
		if next == "" || len(body.Tags) == 0 {
			return out, nil
		}
		last = next
	}
	return out, nil
}

// nextLast is the last query parameter of a Link header's rel="next"
// target; the target itself is never called: the next request is built
// on the source's own URL.
func nextLast(link string) string {
	for part := range strings.SplitSeq(link, ",") {
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		start, end := strings.Index(part, "<"), strings.Index(part, ">")
		if start < 0 || end < start {
			return ""
		}
		target, err := url.Parse(part[start+1 : end])
		if err != nil {
			return ""
		}
		return target.Query().Get("last")
	}
	return ""
}

// ociAuth answers a registry's challenge: Basic with the source's
// username, or a Bearer token from the challenge's realm, for pull on the
// one repository.
func (r *Server) ociAuth(ctx context.Context, s *source, name, challenge, cred string) (string, error) {
	scheme, _, _ := strings.Cut(challenge, " ")
	params := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(challenge, -1) {
		params[strings.ToLower(m[1])] = m[2]
	}
	switch {
	case strings.EqualFold(scheme, "Basic") && s.Username != "":
		return cred, nil
	case !strings.EqualFold(scheme, "Bearer") || params["realm"] == "":
		return "", fmt.Errorf("source %s: the registry asks for credentials the source does not have", s.Name)
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || !s.allowed(realm) || realm.Fragment != "" {
		return "", fmt.Errorf("source %s: the token realm is not the registry's host nor its declared authHost: refused", s.Name)
	}
	q := realm.Query()
	if params["service"] != "" {
		q.Set("service", params["service"])
	}
	q.Set("scope", "repository:"+name+":pull")
	realm.RawQuery = q.Encode()
	header := http.Header{}
	if s.Username != "" {
		header.Set(headerAuthorization, cred)
	}
	raw, _, err := r.get(ctx, s, realm, header, maxJSON)
	if err != nil {
		return "", fmt.Errorf("source %s: token: %w", s.Name, err)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil || (tok.Token == "" && tok.AccessToken == "") {
		return "", fmt.Errorf("source %s: the token realm gave no token", s.Name)
	}
	if tok.Token == "" {
		tok.Token = tok.AccessToken
	}
	return "Bearer " + tok.Token, nil
}

// helmVersions reads a chart's versions from the repository's index.yaml
// (Helm, "The Chart Repository Guide").
func (r *Server) helmVersions(ctx context.Context, s *source, name string) ([]published, error) {
	header, err := r.authHeader(s, headerAuthorization)
	if err != nil {
		return nil, err
	}
	raw, _, err := r.get(ctx, s, join(s.base, "index.yaml"), header, maxIndex)
	if err != nil {
		return nil, err
	}
	var index struct {
		Entries map[string][]struct {
			Version string `yaml:"version"`
			Created string `yaml:"created"`
		} `yaml:"entries"`
	}
	if err := yaml.Unmarshal(raw, &index); err != nil {
		return nil, fmt.Errorf("source %s: index.yaml does not parse", s.Name)
	}
	entries, ok := index.Entries[name]
	if !ok {
		return nil, fmt.Errorf("source %s: no chart %s in index.yaml", s.Name, name)
	}
	out := make([]published, 0, len(entries))
	for _, e := range entries {
		out = append(out, published{tag: e.Version, date: e.Created})
	}
	return out, nil
}

// githubReleases reads a project's published releases (GitHub REST,
// "List releases"); drafts are skipped.
func (r *Server) githubReleases(ctx context.Context, s *source, name string) ([]published, error) {
	header, err := r.authHeader(s, headerAuthorization)
	if err != nil {
		return nil, err
	}
	header.Set(headerAccept, "application/vnd.github+json")
	header.Set("X-GitHub-Api-Version", "2022-11-28")
	owner, repo, _ := strings.Cut(name, "/")
	return r.forgePages(ctx, s, join(s.base, "repos", owner, repo, "releases"), header, func(raw []byte) ([]published, int, error) {
		var rs []struct {
			TagName     string `json:"tag_name"`
			Draft       bool   `json:"draft"`
			Prerelease  bool   `json:"prerelease"`
			PublishedAt string `json:"published_at"`
		}
		err := json.Unmarshal(raw, &rs)
		out := []published{}
		for _, x := range rs {
			if !x.Draft {
				out = append(out, published{tag: x.TagName, date: x.PublishedAt, prerelease: x.Prerelease})
			}
		}
		return out, len(rs), err
	})
}

// gitlabReleases reads a project's releases (GitLab REST, "Releases
// API"); an upcoming release is skipped.
func (r *Server) gitlabReleases(ctx context.Context, s *source, name string) ([]published, error) {
	header, err := r.authHeader(s, "PRIVATE-TOKEN")
	if err != nil {
		return nil, err
	}
	return r.forgePages(ctx, s, join(s.base, "projects", name, "releases"), header, func(raw []byte) ([]published, int, error) {
		var rs []struct {
			TagName    string `json:"tag_name"`
			ReleasedAt string `json:"released_at"`
			Upcoming   bool   `json:"upcoming_release"`
		}
		err := json.Unmarshal(raw, &rs)
		out := []published{}
		for _, x := range rs {
			if !x.Upcoming {
				out = append(out, published{tag: x.TagName, date: x.ReleasedAt})
			}
		}
		return out, len(rs), err
	})
}

// forgePages reads up to three pages of a hundred releases, newest first.
func (r *Server) forgePages(ctx context.Context, s *source, u *url.URL, header http.Header, parse func([]byte) ([]published, int, error)) ([]published, error) {
	out := []published{}
	for page := 1; page <= forgeMaxPages; page++ {
		u.RawQuery = url.Values{keyPerPage: {strconv.Itoa(forgePage)}, keyPage: {strconv.Itoa(page)}}.Encode()
		raw, _, err := r.get(ctx, s, u, header, maxJSON)
		if err != nil {
			return nil, err
		}
		got, n, err := parse(raw)
		if err != nil {
			return nil, fmt.Errorf("source %s: the releases are not JSON", s.Name)
		}
		out = append(out, got...)
		if n < forgePage {
			break
		}
	}
	return out, nil
}

// cycles reads a product's release cycles (endoflife.date API v1, GET
// /products/{product}).
func (r *Server) cycles(ctx context.Context, s *source, product string) ([]Cycle, error) {
	if s.Kind != KindEndOfLife {
		return nil, fmt.Errorf("source %s is %s: cycles_get reads endoflife sources", s.Name, s.Kind)
	}
	header, err := r.authHeader(s, headerAuthorization)
	if err != nil {
		return nil, err
	}
	raw, _, err := r.get(ctx, s, join(s.base, "products", product), header, maxJSON)
	if err != nil {
		return nil, err
	}
	var body struct {
		Result struct {
			Releases []struct {
				Name         string  `json:"name"`
				IsLts        bool    `json:"isLts"`
				IsEol        bool    `json:"isEol"`
				EolFrom      *string `json:"eolFrom"`
				IsMaintained bool    `json:"isMaintained"`
				Latest       *struct {
					Name string `json:"name"`
				} `json:"latest"`
			} `json:"releases"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("source %s: the product is not JSON", s.Name)
	}
	out := make([]Cycle, 0, len(body.Result.Releases))
	for _, c := range body.Result.Releases {
		cy := Cycle{Name: c.Name, Maintained: c.IsMaintained, EOL: c.IsEol, LTS: c.IsLts}
		if c.EolFrom != nil {
			cy.EOLFrom = *c.EolFrom
		}
		if c.Latest != nil {
			cy.Latest = c.Latest.Name
		}
		out = append(out, cy)
	}
	return out, nil
}

// authHeader sets the source's credentials in header key: PRIVATE-TOKEN
// takes the token itself.
func (r *Server) authHeader(s *source, key string) (http.Header, error) {
	header := http.Header{}
	if key != headerAuthorization {
		tok, err := s.token()
		if tok != "" {
			header.Set(key, tok)
		}
		return header, err
	}
	cred, err := s.credentials()
	if cred != "" {
		header.Set(key, cred)
	}
	return header, err
}
