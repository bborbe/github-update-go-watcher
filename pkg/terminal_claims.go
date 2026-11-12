// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
)

//counterfeiter:generate -o ../mocks/terminal_claims.go --fake-name TerminalClaims . TerminalClaims

// TerminalClaims records task identifiers whose task can no longer progress,
// and hands out the entitlement to end a repo's emission claim for one of them.
//
// "Can no longer progress" is a dispatchability question, not a status string: a
// terminal status (completed, aborted), or phase human_review, which no Config
// dispatches and which a record cannot leave on its own. A queued or running
// task keeps its claim.
type TerminalClaims interface {
	// Observe records taskID as having reached a non-progressing state.
	// Idempotent: observing the same identifier twice changes nothing, and it
	// never re-arms a consumed entitlement.
	Observe(ctx context.Context, taskID string) error
	// ClaimClearAllowed reports whether taskID has been observed non-progressing
	// and the entitlement to end a claim for it has not yet been consumed. It
	// consumes the entitlement so a second cycle does not end the claim again.
	//
	// The consumption is what bounds the fix: without it a repo whose task
	// deterministically stops progressing re-emits every poll interval (10m)
	// instead of every InFlightTTL (14 days).
	ClaimClearAllowed(ctx context.Context, taskID string) (bool, error)
}

// terminalClaimsBucket holds one key per observed identifier, plus a
// ":cleared" sibling marking a consumed entitlement.
const terminalClaimsBucket = "terminal_claims"

// terminalClaimMarker is the stored value for both keys. It must be non-empty:
// libkv's byteItem.Exists() is len(value) > 0, so a nil value reads back as
// absent and Observe would be silently inert.
var terminalClaimMarker = []byte{1}

// NewTerminalClaims returns a TerminalClaims backed by db. The store is
// in-process and deliberately unbounded: it holds one short UUID per
// non-progressing task the event topic has ever carried. Eviction would need a
// clock the store does not have, and the alternative — a persisted store — needs
// a path, a PVC and a migration. See spec 007's Failure Modes table.
func NewTerminalClaims(db libkv.DB) TerminalClaims {
	return &terminalClaims{db: db}
}

type terminalClaims struct {
	db libkv.DB
}

func (c *terminalClaims) Observe(ctx context.Context, taskID string) error {
	return c.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(
			ctx,
			libkv.NewBucketName(terminalClaimsBucket),
		)
		if err != nil {
			return errors.Wrapf(ctx, err, "create bucket %s", terminalClaimsBucket)
		}
		if err := bucket.Put(ctx, []byte(taskID), terminalClaimMarker); err != nil {
			return errors.Wrapf(ctx, err, "observe taskID=%s", taskID)
		}
		return nil
	})
}

func (c *terminalClaims) ClaimClearAllowed(
	ctx context.Context,
	taskID string,
) (bool, error) {
	allowed := false
	err := c.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		bucket, err := tx.Bucket(ctx, libkv.NewBucketName(terminalClaimsBucket))
		if err != nil {
			// No bucket means nothing has been observed yet. That is a normal
			// state on a fresh process, not a fault — do not log per repo per
			// cycle over it.
			if stderrors.Is(err, libkv.ErrBucketNotFound) {
				return nil
			}
			return errors.Wrapf(ctx, err, "open bucket %s", terminalClaimsBucket)
		}
		observed, err := bucket.Get(ctx, []byte(taskID))
		if err != nil {
			return errors.Wrapf(ctx, err, "read taskID=%s", taskID)
		}
		if !observed.Exists() {
			return nil
		}
		clearedKey := taskID + ":cleared"
		cleared, err := bucket.Get(ctx, []byte(clearedKey))
		if err != nil {
			return errors.Wrapf(ctx, err, "read taskID=%s marker", taskID)
		}
		if cleared.Exists() {
			return nil
		}
		if err := bucket.Put(ctx, []byte(clearedKey), terminalClaimMarker); err != nil {
			return errors.Wrapf(ctx, err, "consume entitlement taskID=%s", taskID)
		}
		allowed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return allowed, nil
}
