// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Package fakellm is the fake model provider of the end-to-end tests
// (spec 04): an OpenAI-compatible chat completions server, behind the real
// agentgateway, so that the gateway's metrics and spans are exercised too.
//
// Replay serves fixtures and fails loudly on a request it does not know:
// no silent fallback, so a call that would have reached a real provider
// cannot pass as green. Record proxies to a real provider and writes the
// fixtures, in the same format, for a later replay.
//
// A request is known by its fingerprint: the model, the conversation's
// roles, the tools called so far and the tools offered. Not by its text:
// the alert's times, the pod names and the tools' outputs change at every
// run, while the conversation's shape is what the fixture drives.
package fakellm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Request is what the server reads of a chat completions request.
type Request struct {
	Model         string    `json:"model"`
	Messages      []Message `json:"messages"`
	Tools         []Tool    `json:"tools,omitempty"`
	Stream        bool      `json:"stream,omitempty"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
}

// Message is one message of the conversation.
type Message struct {
	Role      string     `json:"role"`
	Content   any        `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall is a call the model asked for.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Tool is a tool offered to the model.
type Tool struct {
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

// Fingerprint identifies a request by the conversation's shape.
type Fingerprint struct {
	Model string `json:"model"`
	// Roles of the messages, in order.
	Roles []string `json:"roles"`
	// Called are the tools the conversation called so far, in order.
	Called []string `json:"called"`
	// Offered is a hash of the sorted names of the tools offered.
	Offered string `json:"offered"`
}

// Of computes a request's fingerprint.
func Of(r *Request) Fingerprint {
	f := Fingerprint{Model: r.Model, Roles: []string{}, Called: []string{}}
	for _, m := range r.Messages {
		f.Roles = append(f.Roles, m.Role)
		for _, c := range m.ToolCalls {
			f.Called = append(f.Called, c.Function.Name)
		}
	}
	names := make([]string, 0, len(r.Tools))
	for _, t := range r.Tools {
		names = append(names, t.Function.Name)
	}
	slices.Sort(names)
	sum := sha256.Sum256([]byte(strings.Join(names, "\n")))
	f.Offered = hex.EncodeToString(sum[:8])
	return f
}

// Key is the fingerprint as a map key.
func (f Fingerprint) Key() string {
	return fmt.Sprintf("%s|%s|%s|%s", f.Model, strings.Join(f.Roles, ","), strings.Join(f.Called, ","), f.Offered)
}

// Fixtures is one fixture file: a conversation, request by request.
type Fixtures struct {
	// Source says how the file was made: "recorded" or "hand-written".
	Source      string     `json:"source"`
	Date        string     `json:"date"`
	Description string     `json:"description,omitempty"`
	Exchanges   []Exchange `json:"exchanges"`
}

// Exchange is a request's fingerprint and the response served for it, a
// chat completions response object.
type Exchange struct {
	Fingerprint Fingerprint     `json:"fingerprint"`
	Response    json.RawMessage `json:"response"`
}
