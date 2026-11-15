// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"sync"

	"github.com/bborbe/errors"
)

//counterfeiter:generate -o ../mocks/terminal_claims_store.go --fake-name TerminalClaimsStore . TerminalClaimsStore

// TerminalClaimsStore answers, from in-process state only, whether a repo's
// emitted task can no longer progress. It holds no clock, no GitHub call and no
// cursor access: the task lifecycle consumer feeds it, the poll loop reads it.
type TerminalClaimsStore interface {
	// Observe records that a non-progressing task event was seen for repo. It
	// deliberately leaves a consumed entitlement consumed: a repeat sighting of
	// the same claim generation does not re-arm it.
	Observe(ctx context.Context, repo string) error

	// CanConsume reports whether the entitlement to end a claim for repo is
	// available: observed and not yet consumed. It does not consume it, so a
	// cycle that aborts before SaveCursor does not burn the entitlement.
	CanConsume(ctx context.Context, repo string) (bool, error)

	// Consume marks the entitlement for repo consumed.
	Consume(ctx context.Context, repo string) error

	// Rearm resets repo after a publish wrote a fresh claim, so that claim's
	// eventual non-progressing event can end it. It clears BOTH flags: the prior
	// observation belonged to the claim that just ended. If observed were left
	// set, the next cycle would immediately clear the fresh claim.
	Rearm(ctx context.Context, repo string) error
}

// NewInMemoryTerminalClaimsStore returns a TerminalClaimsStore backed by a
// mutex-guarded map. The map grows with the topic's non-progressing repo
// cardinality and is rebuilt from the topic on restart.
func NewInMemoryTerminalClaimsStore() TerminalClaimsStore {
	return &terminalClaimsStore{states: make(map[string]*terminalClaimState)}
}

type terminalClaimState struct {
	observed bool
	consumed bool
}

type terminalClaimsStore struct {
	mu     sync.Mutex
	states map[string]*terminalClaimState
}

func (s *terminalClaimsStore) Observe(ctx context.Context, repo string) error {
	if repo == "" {
		return errors.Errorf(ctx, "repo must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state(repo).observed = true
	return nil
}

func (s *terminalClaimsStore) CanConsume(_ context.Context, repo string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[repo]
	if !ok {
		return false, nil
	}
	return state.observed && !state.consumed, nil
}

func (s *terminalClaimsStore) Consume(_ context.Context, repo string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state(repo).consumed = true
	return nil
}

func (s *terminalClaimsStore) Rearm(_ context.Context, repo string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.state(repo)
	state.observed = false
	state.consumed = false
	return nil
}

// state returns the entry for repo, creating it on first use. Callers hold mu.
func (s *terminalClaimsStore) state(repo string) *terminalClaimState {
	state, ok := s.states[repo]
	if !ok {
		state = &terminalClaimState{}
		s.states[repo] = state
	}
	return state
}
