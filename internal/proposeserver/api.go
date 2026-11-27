// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package proposeserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// The fixed errors the tool returns, each the first word of its message:
// the operator maps them to the maintenance's blind spots and outcomes
// (spec 12 §6.2, §6.3.3).
const (
	CodeArguments           = "ArgumentsRefused"
	CodeRepositoryNotListed = "RepositoryNotListed"
	CodePathRefused         = "PathRefused"
	CodeBranchRefused       = "BranchRefused"
	CodeBranchExists        = "BranchExists"
	CodeUnprotected         = "DefaultBranchUnprotected"
	CodeProtectionUnknown   = "ProtectionUnknown"
	CodeCIConfigUnknown     = "CIConfigUnknown"
	CodeDraftUnavailable    = "DraftUnavailable"
	CodeForge               = "ForgeError"
)

// refusal is a fixed error: its code, then what the agent may read.
type refusal struct{ code, detail string }

func (r *refusal) Error() string { return r.code + ": " + r.detail }

func refuse(code, format string, args ...any) error {
	return &refusal{code: code, detail: fmt.Sprintf(format, args...)}
}

// Keys of the forges' JSON and query strings the server repeats.
const (
	keyRef         = "ref"
	keyPerPage     = "per_page"
	keyBody        = "body"
	keyContent     = "content"
	keyBranch      = "branch"
	keyDescription = "description"
	keyTitle       = "title"
	keyType        = "type"
)

// maxResponse bounds what is read of a forge's answer.
const maxResponse = 4 << 20

// statusError is a forge's answer outside 2xx. Its message is the forge's
// own, bounded; it never holds the token, which is sent in a header.
type statusError struct {
	status  int
	message string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("the forge answered %d: %s", e.status, e.message)
}

func httpStatus(err error) int {
	if se, ok := errors.AsType[*statusError](err); ok {
		return se.status
	}
	return 0
}

// api calls one forge's REST API with one repository's token.
type api struct {
	base      string
	client    *http.Client
	tokenFile string
	// auth sets the token on a request, in the forge's header.
	auth func(*http.Request, string)
}

// token is read at each call, so that a renewed Secret is taken without
// a restart.
func (a *api) token() (string, error) {
	raw, err := os.ReadFile(a.tokenFile)
	if err != nil {
		return "", fmt.Errorf("read the token: %w", err)
	}
	t := strings.TrimSpace(string(raw))
	if t == "" {
		return "", errors.New("the token file is empty")
	}
	return t, nil
}

// do calls path (already escaped) with query, sends in as JSON when set,
// and decodes a 2xx answer into out: a *[]byte takes the raw body.
func (a *api) do(ctx context.Context, method, path string, query url.Values, in, out any) (http.Header, error) {
	u, err := url.Parse(a.base + path)
	if err != nil {
		return nil, fmt.Errorf("the forge URL: %w", err)
	}
	if query != nil {
		u.RawQuery = query.Encode()
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, fmt.Errorf("encode the request: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("build the request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	tok, err := a.token()
	if err != nil {
		return nil, err
	}
	a.auth(req, tok)
	res, err := a.client.Do(req)
	if err != nil {
		// The URL holds no credential: the token is in a header.
		return nil, fmt.Errorf("%s %s: %w", method, u.Path, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil {
		return nil, fmt.Errorf("%s %s: read the answer: %w", method, u.Path, err)
	}
	if len(raw) > maxResponse {
		return nil, fmt.Errorf("%s %s: the answer is larger than %d MiB", method, u.Path, maxResponse>>20)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, &statusError{status: res.StatusCode, message: forgeMessage(raw)}
	}
	switch o := out.(type) {
	case nil:
	case *[]byte:
		*o = raw
	default:
		if err := json.Unmarshal(raw, out); err != nil {
			return nil, fmt.Errorf("%s %s: decode the answer: %w", method, u.Path, err)
		}
	}
	return res.Header, nil
}

// forgeMessage keeps the forge's message and its validation errors, at
// most 300 bytes.
func forgeMessage(raw []byte) string {
	var m struct {
		Message any `json:"message"`
		Error   any `json:"error"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	text := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &m) == nil {
		parts := []string{}
		for _, v := range []any{m.Message, m.Error} {
			if v != nil {
				parts = append(parts, fmt.Sprint(v))
			}
		}
		for _, e := range m.Errors {
			parts = append(parts, e.Message)
		}
		text = strings.Join(parts, "; ")
	}
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}

// escapeSegments escapes each segment of a slash-separated name, keeping
// the slashes: GitHub's branch and owner/repo paths.
func escapeSegments(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// escapeWhole escapes a name as one path segment, slashes included:
// GitLab's project IDs, branches and file paths.
func escapeWhole(p string) string {
	return strings.ReplaceAll(url.PathEscape(p), "/", "%2F")
}
