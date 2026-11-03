// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package fakeslack is the end-to-end tests' Slack (spec 04): auth.test and
// chat.postMessage of the Web API, with a bearer token checked, recording
// every posted message for the assertions (GET /messages).
package fakeslack

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Message is a posted message, as chat.postMessage received it.
type Message struct {
	Channel string          `json:"channel"`
	Text    string          `json:"text,omitempty"`
	Blocks  json.RawMessage `json:"blocks,omitempty"`
}

// Server is the fake Slack; Token is the only bearer token it accepts.
type Server struct {
	Token string

	mu       sync.Mutex
	messages []Message
}

// Messages returns the messages posted so far.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.messages...)
}

// Handler serves the Web API under /api, the messages at /messages and
// the probe at /healthz.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth.test", func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			refuse(w, "invalid_auth")
			return
		}
		answer(w, map[string]any{"ok": true, "team": "e2e", "user": "takecare"})
	})
	mux.HandleFunc("POST /api/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			refuse(w, "invalid_auth")
			return
		}
		var m Message
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&m); err != nil || m.Channel == "" {
			refuse(w, "invalid_arguments")
			return
		}
		s.mu.Lock()
		s.messages = append(s.messages, m)
		ts := strconv.Itoa(len(s.messages)) + ".000100"
		s.mu.Unlock()
		answer(w, map[string]any{"ok": true, "channel": m.Channel, "ts": ts})
	})
	mux.HandleFunc("GET /messages", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Messages())
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	return mux
}

func (s *Server) authorized(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && s.Token != "" && token == s.Token
}

// refuse answers as the Web API does: HTTP 200, ok false, an error code.
func refuse(w http.ResponseWriter, code string) {
	answer(w, map[string]any{"ok": false, "error": code})
}

func answer(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
