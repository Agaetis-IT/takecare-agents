// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/agaetis-it/takecare-agents/internal/discovery"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// entryConcurrency bounds the Map calls a survey makes at once.
const entryConcurrency = 4

// mapIndex is the Map as a survey reads it: every entry, at one revision.
type mapIndex struct {
	revision string
	entries  []mapserver.Entry
}

// readMap reads the Map through the Intendant's map route, at one date: the
// list of keys (map_at without a key), then each entry (map_at with its
// key), so that every entry comes from the same revision. N entries cost
// N+1 calls, entryConcurrency at a time.
func readMap(ctx context.Context, caller discovery.Caller, ep mcp.Endpoint, now time.Time) (mapIndex, error) {
	date := now.UTC().Format(time.DateOnly)
	var list struct {
		Revision string `json:"revision"`
		Entries  []struct {
			Key string `json:"key"`
		} `json:"entries"`
	}
	if err := callJSON(ctx, caller, ep, map[string]any{"date": date}, &list); err != nil {
		return mapIndex{}, fmt.Errorf("list the Map: %w", err)
	}
	idx := mapIndex{revision: list.Revision, entries: make([]mapserver.Entry, len(list.Entries))}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(entryConcurrency)
	for i, it := range list.Entries {
		g.Go(func() error {
			var e struct {
				mapserver.Entry
				Revision string `json:"revision"`
			}
			if err := callJSON(gctx, caller, ep, map[string]any{"key": it.Key, "date": date}, &e); err != nil {
				return fmt.Errorf("read %s: %w", it.Key, err)
			}
			if e.Revision != list.Revision {
				return fmt.Errorf("read %s: revision %s, not the listed %s", it.Key, e.Revision, list.Revision)
			}
			idx.entries[i] = e.Entry
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return mapIndex{}, err
	}
	return idx, nil
}

var errTool = errors.New("the tool reported an error")

func callJSON(ctx context.Context, caller discovery.Caller, ep mcp.Endpoint, args map[string]any, out any) error {
	res, err := caller.Call(ctx, ep, mcp.MapListTool, args)
	if err != nil {
		return err
	}
	if res.IsError {
		return fmt.Errorf("%w: %s", errTool, res.Text)
	}
	if err := json.Unmarshal([]byte(res.Text), out); err != nil {
		return fmt.Errorf("the answer is not JSON: %w", err)
	}
	return nil
}
