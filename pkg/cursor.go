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
//   - Corrupt JSON -> the file is renamed to <path>.corrupt and the cycle
//     cold-starts. This re-files repos already reported, which deterministic
//     UUID5 task identifiers dedup downstream; the earlier behaviour (return
//     an error, operator deletes the file) wedged every cycle indefinitely
//     because nothing rewrites a file that fails to load.
//   - Unreadable file (permissions, I/O) -> error. That is an environment
//     fault, not bad content, and retrying the same read is the right move.
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
		// Preserve the bad file, then cold-start. Returning an error here
		// aborted every poll cycle forever on a file nothing rewrites, so a
		// single corrupt byte wedged the watcher until an operator noticed.
		// Cold-starting re-files repos already reported, but the emitted
		// task_identifier is a deterministic UUID5, so downstream dedup
		// absorbs the repeat — the same reasoning that already makes a
		// missing file a valid cold start.
		bad := path + ".corrupt"
		if rerr := os.Rename(path, bad); rerr != nil {
			glog.Warningf("preserve corrupt cursor failed path=%s err=%v", path, rerr)
		}
		glog.Warningf("cursor corrupt, cold-starting path=%s saved=%s err=%v", path, bad, err)
		return &Cursor{Repos: make(map[string]*RepoState)}, nil
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
