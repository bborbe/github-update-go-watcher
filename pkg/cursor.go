// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// DefaultCursorPath is the default persisted-memory location. The quant Helm
// chart mounts the PVC at exactly this path; the binary must start with
// CURSOR_PATH unset.
const DefaultCursorPath = "/data/cursor.json"

// osvVerdictTTL bounds how long a resolved OSV verdict is reused before the
// advisory is re-checked. Unresolved verdicts are never cached.
const osvVerdictTTL = 24 * libtime.Hour

// Cursor is the per-repo head-SHA dedup state.
//
// Concurrency: not safe for concurrent use. Exactly one cycle runs at a time,
// so the file has a single writer — the cycle loads at start and saves at end.
type Cursor struct {
	Repos map[string]*RepoState `json:"repos"` // key: Repo.Key(), "github.com/owner/name"
	// OSVVerdicts caches resolved OSV verdicts by advisory ID (spec 008 DB7).
	// omitempty so a cursor written by the previous binary loads, and so a
	// cursor with no verdicts serialises to nothing. An unresolved ID is never
	// stored here — it is retried on the next cycle.
	OSVVerdicts map[string]*OSVVerdictEntry `json:"osv_verdicts,omitempty"`
}

// OSVVerdictEntry is one cached OSV verdict: whether the advisory was withdrawn
// and when the verdict was recorded (RFC3339, UTC). Only resolved verdicts are
// cached, so a present entry with Withdrawn=false means "legitimate".
//
// Withdrawn deliberately carries no omitempty: an entry that serialised without
// the key would read back as a different verdict than the one stored.
type OSVVerdictEntry struct {
	Withdrawn bool   `json:"withdrawn"`
	CheckedAt string `json:"checked_at"`
}

// RepoState is the cursor entry per repo.
type RepoState struct {
	LastSeenHeadSHA string `json:"last_seen_head_sha"`
	// LastSeenGoVersion is the cycle's resolved stable Go version at the moment
	// LastSeenHeadSHA was recorded, in the three-part form the emitted task
	// carries as latest_go (e.g. "1.27.2"). Together with LastSeenHeadSHA it is
	// the two-key dedup input: a new Go release moves no repo's HEAD, so a
	// HEAD-only key would skip a repo that is genuinely behind. omitempty so a
	// pre-fix cursor file (no such key) still loads — an absent value
	// deserialises to "" and therefore never equals a real version, so the repo
	// is re-evaluated once.
	LastSeenGoVersion string `json:"last_seen_go_version,omitempty"`
	// CompletedHeadSHA is the head SHA whose update task this watcher has
	// already auto-completed via a complete-task command (a merged update PR
	// was observed). Prevents re-publishing the same completion every cycle —
	// the controller executor is idempotent, but the cursor marker keeps the
	// no-op off the wire.
	CompletedHeadSHA string `json:"completed_head_sha,omitempty"`
	// InFlightTarget is the target Go version — three-part form, e.g. "1.27.2",
	// the same string the emitted task carries as latest_go — that this watcher
	// last filed a create-task for on this repo. InFlightSince is when it filed
	// it (RFC3339, UTC). Together they are the emission claim: while a claim
	// still targets the cycle's target version, the repo is not emitted again,
	// which covers the window between emission and the agent opening its update
	// PR — the window the open-PR gate cannot see. Both are omitempty so an
	// entry with no claim serialises to nothing, and a pre-fix cursor file (no
	// such keys) loads with an empty claim, so no migration is needed.
	//
	// A claim is cleared by convergence, by the appearance of an open update PR,
	// or by the InFlightTTL backstop; it is written only by markPublished, so
	// hand-authored "- reemit" repair tasks never carry one.
	InFlightTarget string `json:"in_flight_target,omitempty"`
	InFlightSince  string `json:"in_flight_since,omitempty"`
	// IgnoreScannedAt is when the stale-ignore pass last read this repo's
	// ignore surfaces (RFC3339, UTC). Absent means "never scanned", which sorts
	// first in the throttle. It is advanced on every completed scan and on every
	// eligibility skip, but NOT when the repo was dropped mid-scan, so a failed
	// read is retried on the next eligible cycle.
	IgnoreScannedAt string `json:"ignore_scanned_at,omitempty"`
	// IgnoreStaleIDs is the sorted stale advisory set recorded at the last
	// emission, joined by ",". Empty means "no stale set recorded" — a repo
	// whose set became empty has this cleared so a later identical set emits
	// again.
	IgnoreStaleIDs string `json:"ignore_stale_ids,omitempty"`
	// IgnoreStaleGeneration is monotonic per repo and never resets. It is part
	// of the task identifier, so every emission mints a NEW record instead of an
	// identity-identical create the controller would dedup into a no-op.
	IgnoreStaleGeneration int `json:"ignore_stale_generation,omitempty"`
	// IgnoreStaleEmittedAt is when the recorded set was last emitted (RFC3339,
	// UTC). Together with IgnoreStaleIDs it bounds the re-emit to one task per
	// staleReemitInterval while the set is unchanged.
	IgnoreStaleEmittedAt string `json:"ignore_stale_emitted_at,omitempty"`
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

// CachedVerdict returns the cached verdict for id when the entry exists and is
// younger than ttl. An absent entry, an empty or unparsable CheckedAt, and an
// entry at or past ttl all report false so the ID is re-resolved.
func (c *Cursor) CachedVerdict(
	ctx context.Context,
	id string,
	now libtime.DateTime,
	ttl libtime.Duration,
) (OSVVerdict, bool) {
	entry, ok := c.OSVVerdicts[id]
	if !ok || entry == nil {
		return "", false
	}
	checkedAt, err := libtime.ParseDateTime(ctx, entry.CheckedAt)
	if err != nil {
		glog.V(2).Infof(
			"osv verdict has unparsable checked_at id=%s value=%q err=%v",
			id,
			entry.CheckedAt,
			err,
		)
		return "", false
	}
	if now.Sub(checkedAt) >= ttl {
		return "", false
	}
	if entry.Withdrawn {
		return OSVVerdictWithdrawn, true
	}
	return OSVVerdictLegitimate, true
}

// SetVerdict records a resolved verdict for id at now (RFC3339, UTC). An
// unresolved verdict is never recorded — the caller re-asks next cycle.
func (c *Cursor) SetVerdict(id string, verdict OSVVerdict, now libtime.DateTime) {
	if verdict == "" || verdict == OSVVerdictUnresolved {
		return
	}
	if c.OSVVerdicts == nil {
		c.OSVVerdicts = make(map[string]*OSVVerdictEntry)
	}
	c.OSVVerdicts[id] = &OSVVerdictEntry{
		Withdrawn: verdict == OSVVerdictWithdrawn,
		CheckedAt: now.Format(time.RFC3339),
	}
}
