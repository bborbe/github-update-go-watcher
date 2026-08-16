// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// DefaultCursorPath is the default persisted-memory location. The quant Helm
// chart mounts the PVC at exactly this path; the binary must start with
// CURSOR_PATH unset.
const DefaultCursorPath = "/data/cursor.json"

// Cursor is the per-repo head-SHA dedup state.
//
// Concurrency: not safe for concurrent use. Exactly one cycle runs at a time,
// so the file has a single writer — the cycle loads at start and saves at end.
type Cursor struct {
	Repos map[string]*RepoState `json:"repos"` // key: Repo.Key(), "github.com/owner/name"
}

// RepoState is the cursor entry per repo.
type RepoState struct {
	LastSeenHeadSHA string `json:"last_seen_head_sha"`
}

// LoadCursor reads cursor state from path.
//
//   - Missing file -> fresh empty cursor, nil error (cold start is valid and
//     re-publishes; downstream dedup by deterministic identifier absorbs it).
//   - Unreadable or corrupt JSON -> error. The caller MUST refuse to run the
//     cycle rather than proceed on a guessed-empty state, which would re-file
//     the entire fleet. Recovery is the operator deleting the file to accept
//     a cold start.
func LoadCursor(ctx context.Context, path string) (*Cursor, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is config-controlled
	if os.IsNotExist(err) {
		glog.V(2).Infof("cursor file not found, cold-start path=%s", path)
		return &Cursor{Repos: make(map[string]*RepoState)}, nil
	}
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read cursor file path=%s", path)
	}
	c := &Cursor{}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, errors.Wrapf(ctx, err, "unmarshal cursor file path=%s", path)
	}
	if c.Repos == nil {
		c.Repos = make(map[string]*RepoState)
	}
	return c, nil
}

// SaveCursor persists cursor state atomically via temp file + rename, so a
// crash mid-write can never leave a half-written file and no .tmp file
// survives a successful save.
func SaveCursor(ctx context.Context, path string, c *Cursor) error {
	data, err := json.Marshal(c)
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal cursor state path=%s", path)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil { // #nosec G306 -- intentional 0600
		return errors.Wrapf(ctx, err, "write cursor tmp path=%s", tmp)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return errors.Wrapf(ctx, err, "rename cursor tmp path=%s", tmp)
	}
	return nil
}
