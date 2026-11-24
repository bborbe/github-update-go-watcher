// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"sort"
	"strings"
	"time"

	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	"github.com/bborbe/github-update-go-watcher/pkg/filter"
)

// maxIgnoreScansPerCycle bounds how many repos the pass reads surfaces for in
// one cycle: 10 repos x <=7 GitHub calls = <=70 calls per cycle, which covers a
// ~210-repo fleet about once a day at a 10-minute poll interval.
const maxIgnoreScansPerCycle = 10

// ignoreRescanInterval is how long a repo's ignore surfaces are trusted before
// being read again.
const ignoreRescanInterval = 24 * libtime.Hour

// staleReemitInterval bounds the re-emit of an unchanged stale set to one task
// per repo per interval, so a task that dies without removing the ignore is
// re-filed, while a healthy repo is not spammed.
const staleReemitInterval = 7 * libtime.Day

// StaleIgnorePass is the second signal: it reads a repo's ignore surfaces, asks
// OSV whether any suppressed advisory has been withdrawn, and publishes one
// task per repo with a stale set. It runs inside the poll cycle, after the
// merge-detection pass, under the same CycleGate.
type StaleIgnorePass interface {
	// Scan runs one pass and returns an abort reason ("rate_limited" |
	// "github_error" | "") exactly as the watcher's other passes do. Every
	// per-repo failure is counted and logged; it never aborts the cycle.
	Scan(ctx context.Context, cursorState *Cursor, repos []Repo, latestGo Version) string
}

// NewStaleIgnorePass wires the pass's collaborators. allowlist is the raw
// operator allowlist (the same slice the filter chain was built from); the pass
// applies it itself because it must not inherit the Go-version gates.
func NewStaleIgnorePass(
	ghClient GitHubClient,
	osvClient OSVClient,
	publisher TaskPublisher,
	metrics Metrics,
	allowlist []string,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
) StaleIgnorePass {
	return &staleIgnorePass{
		ghClient:              ghClient,
		osvClient:             osvClient,
		publisher:             publisher,
		metrics:               metrics,
		allowlist:             allowlist,
		currentDateTimeGetter: currentDateTimeGetter,
	}
}

type staleIgnorePass struct {
	ghClient              GitHubClient
	osvClient             OSVClient
	publisher             TaskPublisher
	metrics               Metrics
	allowlist             []string
	currentDateTimeGetter libtime.CurrentDateTimeGetter
}

func (p *staleIgnorePass) Scan(
	ctx context.Context,
	cursorState *Cursor,
	repos []Repo,
	latestGo Version,
) string {
	now := p.currentDateTimeGetter.Now()
	for _, repo := range p.selectRepos(repos, cursorState, now) {
		if reason := p.scanRepo(ctx, cursorState, repo, latestGo, now); reason != "" {
			return reason
		}
	}
	return ""
}

// scanRepo runs the per-repo steps. An empty return means "continue with the
// next repo"; a non-empty return is the cycle abort reason.
func (p *staleIgnorePass) scanRepo(
	ctx context.Context,
	cursorState *Cursor,
	repo Repo,
	latestGo Version,
	now libtime.DateTime,
) string {
	select {
	case <-ctx.Done():
		glog.V(2).Infof("poll cancelled during staleIgnores at repo=%s", repo.Key())
		return ""
	default:
	}

	candidate, abortReason, dropped := gatherCandidate(ctx, p.ghClient, repo, latestGo)
	if abortReason != "" {
		p.metrics.IncIgnoreScan("rate_limited")
		return abortReason
	}
	if dropped {
		p.metrics.IncIgnoreScan("error")
		return ""
	}

	state := repoState(cursorState, repo)
	if !candidate.GoModPresent || candidate.Consent != filter.GrantedConsent {
		reason := "no_gomod"
		if candidate.GoModPresent {
			reason = "consent_not_granted"
		}
		glog.V(2).Infof("stale-ignore: repo skipped repo=%s reason=%s", repo.Key(), reason)
		state.IgnoreScannedAt = now.Format(time.RFC3339)
		return ""
	}

	surfaces, ids, abortReason, dropped := p.readSurfaces(ctx, repo)
	if abortReason != "" {
		p.metrics.IncIgnoreScan("rate_limited")
		return abortReason
	}
	if dropped {
		p.metrics.IncIgnoreScan("error")
		return ""
	}
	p.metrics.IncIgnoreScan("scanned")

	if len(ids) == 0 {
		p.metrics.IncIgnoreScan("clean")
		clearStaleSet(state)
		state.IgnoreScannedAt = now.Format(time.RFC3339)
		return ""
	}

	staleIDs, unresolved := p.resolveVerdicts(ctx, cursorState, ids, now)
	if len(staleIDs) == 0 {
		if unresolved {
			p.metrics.IncIgnoreScan("unresolved")
		} else {
			p.metrics.IncIgnoreScan("clean")
		}
		clearStaleSet(state)
		state.IgnoreScannedAt = now.Format(time.RFC3339)
		return ""
	}

	p.metrics.IncIgnoreScan("stale")
	finding := StaleIgnoreFinding{IDs: staleIDs, Surfaces: surfaces}
	if p.emit(ctx, state, candidate, finding, now) {
		state.IgnoreScannedAt = now.Format(time.RFC3339)
	}
	return ""
}

// selectRepos applies the allowlist and the rescan throttle, then truncates to
// maxIgnoreScansPerCycle. Repos whose scan is due sort first, oldest first, so
// a fleet larger than one cycle's budget is covered in a stable rotation.
func (p *staleIgnorePass) selectRepos(
	repos []Repo,
	cursorState *Cursor,
	now libtime.DateTime,
) []Repo {
	allowFilter := filter.NewRepoAllowlistFilter(p.allowlist)
	type dueRepo struct {
		repo      Repo
		scannedAt *libtime.DateTime
	}
	var due []dueRepo
	for _, repo := range repos {
		if allowFilter.Skip(filter.Candidate{RepoKey: repo.Key()}) != "" {
			continue
		}
		state := cursorState.Repos[repo.Key()]
		if state == nil || state.IgnoreScannedAt == "" {
			due = append(due, dueRepo{repo: repo})
			continue
		}
		parsed, err := time.Parse(time.RFC3339, state.IgnoreScannedAt)
		if err != nil {
			glog.V(2).Infof(
				"stale-ignore: unparsable ignore_scanned_at repo=%s value=%q err=%v",
				repo.Key(),
				state.IgnoreScannedAt,
				err,
			)
			due = append(due, dueRepo{repo: repo})
			continue
		}
		scannedAt := libtime.DateTime(parsed)
		if now.Sub(scannedAt) >= ignoreRescanInterval {
			due = append(due, dueRepo{repo: repo, scannedAt: &scannedAt})
		}
	}
	sort.SliceStable(due, func(i, j int) bool {
		left, right := due[i], due[j]
		if (left.scannedAt == nil) != (right.scannedAt == nil) {
			return left.scannedAt == nil
		}
		if left.scannedAt != nil && !left.scannedAt.Equal(*right.scannedAt) {
			return left.scannedAt.Before(*right.scannedAt)
		}
		return left.repo.Key() < right.repo.Key()
	})
	result := make([]Repo, 0, len(due))
	for _, entry := range due {
		if len(result) >= maxIgnoreScansPerCycle {
			break
		}
		result = append(result, entry.repo)
	}
	return result
}

// repoState returns the cursor entry for repo, allocating and inserting one
// when absent. It never mutates the other fields.
func repoState(cursorState *Cursor, repo Repo) *RepoState {
	if cursorState.Repos == nil {
		cursorState.Repos = make(map[string]*RepoState)
	}
	state, ok := cursorState.Repos[repo.Key()]
	if !ok || state == nil {
		state = &RepoState{}
		cursorState.Repos[repo.Key()] = state
	}
	return state
}

// readSurfaces reads every ignore surface in order and returns the ID -> files
// map plus the sorted, de-duplicated ID list. A nil content means the file is
// absent, which is an empty surface. A rate limit aborts the pass; any other
// error drops just this repo for this cycle.
func (p *staleIgnorePass) readSurfaces(
	ctx context.Context,
	repo Repo,
) (map[string][]string, []string, string, bool) {
	surfaces := make(map[string][]string)
	var ids []string
	seen := make(map[string]struct{})
	for _, file := range IgnoreSurfaceFiles {
		content, err := p.ghClient.GetFile(ctx, repo, file)
		if err != nil {
			if stderrors.Is(err, ErrRateLimited) {
				return nil, nil, "rate_limited", false
			}
			glog.Warningf(
				"repo dropped from cycle: owner=%s repo=%s step=%s err=%v",
				repo.Owner,
				repo.Name,
				"stale_ignore:"+file,
				err,
			)
			return nil, nil, "", true
		}
		if content == nil {
			continue
		}
		for _, id := range ParseIgnoreSurface(file, content) {
			surfaces[id] = append(surfaces[id], file)
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return surfaces, ids, "", false
}

// resolveVerdicts classifies every advisory ID, reusing cached verdicts and
// caching freshly resolved ones. The returned stale IDs are sorted; unresolved
// reports whether at least one ID could not be classified.
func (p *staleIgnorePass) resolveVerdicts(
	ctx context.Context,
	cursorState *Cursor,
	ids []string,
	now libtime.DateTime,
) ([]string, bool) {
	var staleIDs []string
	unresolved := false
	for _, id := range ids {
		verdict, ok := cursorState.CachedVerdict(ctx, id, now, osvVerdictTTL)
		if !ok {
			var err error
			verdict, err = p.osvClient.CheckWithdrawn(ctx, id)
			if err != nil {
				glog.Warningf("stale-ignore: osv lookup failed id=%s err=%v", id, err)
			}
			if verdict != OSVVerdictUnresolved && verdict != "" {
				cursorState.SetVerdict(id, verdict, now)
			}
		}
		switch verdict {
		case OSVVerdictWithdrawn:
			staleIDs = append(staleIDs, id)
		case OSVVerdictLegitimate:
		default:
			unresolved = true
		}
	}
	sort.Strings(staleIDs)
	return staleIDs, unresolved
}

// emit publishes the finding unless the recorded set is unchanged and the
// re-emit interval has not elapsed. It returns true when the scan may advance
// its timestamp — either because nothing is pending or because the publish
// succeeded. A failed publish leaves every cursor field alone so the next
// cycle retries the whole repo.
func (p *staleIgnorePass) emit(
	ctx context.Context,
	state *RepoState,
	candidate Candidate,
	finding StaleIgnoreFinding,
	now libtime.DateTime,
) bool {
	joined := strings.Join(finding.IDs, ",")
	if joined == state.IgnoreStaleIDs && !staleReemitDue(ctx, state, now) {
		glog.V(2).Infof(
			"stale-ignore: no emission repo=%s reason=unchanged generation=%d",
			candidate.Repo.Key(),
			state.IgnoreStaleGeneration,
		)
		return true
	}
	generation := state.IgnoreStaleGeneration + 1
	if !p.publisher.PublishStaleIgnore(ctx, candidate, finding, generation) {
		return false
	}
	state.IgnoreStaleIDs = joined
	state.IgnoreStaleGeneration = generation
	state.IgnoreStaleEmittedAt = now.Format(time.RFC3339)
	return true
}

// staleReemitDue reports whether the recorded emission is old enough to file
// again. An empty or unparsable timestamp never counts as due — a bad value
// must not cause a weekly emission every cycle.
func staleReemitDue(ctx context.Context, state *RepoState, now libtime.DateTime) bool {
	if state.IgnoreStaleEmittedAt == "" {
		return false
	}
	emittedAt, err := libtime.ParseDateTime(ctx, state.IgnoreStaleEmittedAt)
	if err != nil {
		glog.V(2).Infof(
			"stale-ignore: unparsable ignore_stale_emitted_at value=%q err=%v",
			state.IgnoreStaleEmittedAt,
			err,
		)
		return false
	}
	return now.Sub(emittedAt) >= staleReemitInterval
}

// clearStaleSet forgets the recorded stale set when it became empty, so a later
// identical set emits again. The generation is monotonic and is never reset.
func clearStaleSet(state *RepoState) {
	if state.IgnoreStaleIDs != "" {
		state.IgnoreStaleIDs = ""
	}
}
