// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mapserver

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// dirSource reads the Map from a directory, again whenever it changed.
type dirSource struct {
	dir    string
	logger *slog.Logger
	now    func() time.Time

	mu        sync.Mutex
	stamp     string
	tried     bool
	loaded    *Map
	lastErr   error
	lastErrAt time.Time
}

func (s *dirSource) run(context.Context) error { return nil }

func (s *dirSource) status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := map[string]any{"source": "dir", keyEntries: 0}
	if s.loaded != nil {
		st["revision"], st["loadedAt"], st[keyEntries] = s.loaded.Revision, s.loaded.LoadedAt, len(s.loaded.Entries)
	}
	if s.lastErr != nil {
		st["lastError"], st["lastErrorAt"] = s.lastErr.Error(), s.lastErrAt
	}
	return st
}

// current returns the current Map, loading the directory again when it
// changed since the last attempt. A load that fails keeps the previous
// Map and records the error. Every call walks and stats the directory:
// cheap for a Map's few hundred files.
func (s *dirSource) current() (*Map, error) {
	fsys := os.DirFS(s.dir)
	stamp, err := stampOf(fsys)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil && (!s.tried || stamp != s.stamp) {
		s.tried, s.stamp = true, stamp
		m, lerr := Load(fsys)
		if lerr != nil {
			err = lerr
		} else {
			m.LoadedAt = s.now()
			s.loaded, s.lastErr = m, nil
			s.logger.Info("map loaded", "revision", m.Revision, "entries", len(m.Entries))
		}
	}
	if err != nil {
		s.lastErr, s.lastErrAt = err, s.now()
		s.logger.Error("map not loaded", "error", err)
	}
	if s.loaded == nil {
		return nil, fmt.Errorf("no valid Map loaded: %w", s.lastErr)
	}
	return s.loaded, nil
}

// stampOf identifies a version of the directory: the names, sizes and
// modification times of its files, as the corpus server does for its one
// file (a mounted volume is updated by swapping a symlink).
func stampOf(fsys fs.FS) (string, error) {
	var b strings.Builder
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := fs.Stat(fsys, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %d %d\n", p, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("read the Map directory: %w", err)
	}
	return b.String(), nil
}
