// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"

	agentlib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/errors"
	"github.com/golang/glog"

	"github.com/bborbe/github-update-go-watcher/pkg/filter"
)

//counterfeiter:generate -o ../mocks/watcher.go --fake-name Watcher . Watcher

// Watcher scans one GitHub owner for repos whose declared Go version is behind
// current stable and publishes one CreateTaskCommand per qualifying repo.
type Watcher interface {
	// Poll runs one scan cycle. Safe to call repeatedly on an interval.
	//
	// force=true omits SHAUnchangedFilter from this cycle's chain, so repos
	// whose HEAD has not moved are re-evaluated. Every other gate (allowlist,
	// go.mod presence, parsability, version comparison, opt-in consent) still
	// applies. The interval loop always passes false.
	Poll(ctx context.Context, force bool) error
}

// NewWatcher wires the cycle's collaborators.
//
// owner is a single GitHub org per watcher instance (multi-org = multiple
// deployments). taskCreationFilter is the cycle-invariant chain built at
// wiring time; SHAUnchangedFilter is composed in per cycle because it needs a
// fresh CursorReader. completeSender publishes CompleteCommands for repos
// whose update PR merged (merge-detection → task completion).
func NewWatcher(
	ghClient GitHubClient,
	goDevClient GoDevClient,
	publisher TaskPublisher,
	completeSender task.CompleteCommandSender,
	metrics Metrics,
	cursorPath string,
	owner string,
	taskCreationFilter filter.TaskCreationFilter,
) Watcher {
	return &watcher{
		ghClient:           ghClient,
		goDevClient:        goDevClient,
		publisher:          publisher,
		completeSender:     completeSender,
		metrics:            metrics,
		cursorPath:         cursorPath,
		owner:              owner,
		taskCreationFilter: taskCreationFilter,
	}
}

type watcher struct {
	ghClient           GitHubClient
	goDevClient        GoDevClient
	publisher          TaskPublisher
	completeSender     task.CompleteCommandSender
	metrics            Metrics
	cursorPath         string
	owner              string
	taskCreationFilter filter.TaskCreationFilter
}

func (w *watcher) Poll(ctx context.Context, force bool) error {
	cursorState, err := LoadCursor(ctx, w.cursorPath)
	if err != nil {
		return errors.Wrapf(ctx, err, "load cursor path=%s", w.cursorPath)
	}

	latestGo, err := w.goDevClient.LatestStable(ctx)
	if err != nil {
		w.metrics.IncPollCycle("go_version_error")
		glog.Errorf("poll cycle aborted: stable go lookup failed err=%v", err)
		return nil
	}

	repos, err := w.ghClient.ListRepos(ctx, w.owner)
	if err != nil {
		if stderrors.Is(err, ErrRateLimited) {
			w.metrics.IncPollCycle("rate_limited")
			glog.Warningf(
				"poll cycle aborted: rate limited during ListRepos owner=%s",
				w.owner,
			)
		} else {
			w.metrics.IncPollCycle("github_error")
			glog.Warningf(
				"poll cycle aborted: ListRepos owner=%s err=%v",
				w.owner,
				err,
			)
		}
		return nil
	}

	w.metrics.IncReposScanned(len(repos))

	cycleFilter := filter.TaskCreationFilterList{w.taskCreationFilter}
	if !force {
		cycleFilter = append(
			cycleFilter,
			filter.NewSHAUnchangedFilter(NewCursorReader(cursorState)),
		)
	}

	abortReason := w.processRepos(ctx, cursorState, repos, cycleFilter, latestGo)
	if abortReason != "" {
		w.metrics.IncPollCycle(abortReason)
		return nil
	}

	// Merge-detection pass: a repo whose update PR merged gets its vault task
	// auto-completed (complete-task command) instead of sitting in human_review
	// until a manual close-sweep. Rate-limited here aborts the whole cycle the
	// same way it does during processRepos — one throttle is shared.
	if reason := w.completeMergedUpdates(ctx, cursorState, repos); reason != "" {
		w.metrics.IncPollCycle(reason)
		// Persist cursor mutations made before the abort (completed-head
		// markers) so a completion already published this cycle is not
		// re-published next cycle after a lost in-memory state.
		if err := SaveCursor(ctx, w.cursorPath, cursorState); err != nil {
			glog.Warningf("save cursor failed path=%s err=%v", w.cursorPath, err)
		}
		return nil
	}

	if err := SaveCursor(ctx, w.cursorPath, cursorState); err != nil {
		glog.Warningf("save cursor failed path=%s err=%v", w.cursorPath, err)
	}

	w.metrics.IncPollCycle("success")
	return nil
}

func (w *watcher) processRepos(
	ctx context.Context,
	cursorState *Cursor,
	repos []Repo,
	cycleFilter filter.TaskCreationFilter,
	latestGo Version,
) string {
	for _, repo := range repos {
		select {
		case <-ctx.Done():
			glog.V(2).Infof(
				"poll cancelled during processRepos at repo=%s",
				repo.Key(),
			)
			return ""
		default:
		}

		candidate, abortReason, dropped := w.gatherCandidate(ctx, repo, latestGo)
		if abortReason != "" {
			return abortReason
		}
		if dropped {
			continue
		}

		if reason := cycleFilter.Skip(candidate.FilterCandidate()); reason != "" {
			w.metrics.IncFilterSkipped(reason)
			glog.V(2).Infof(
				"repo skipped repo=%s reason=%s",
				repo.Key(),
				reason,
			)
			continue
		}

		if w.publisher.PublishCreate(ctx, candidate) {
			if cursorState.Repos == nil {
				cursorState.Repos = make(map[string]*RepoState)
			}
			cursorState.Repos[repo.Key()] = &RepoState{
				LastSeenHeadSHA: candidate.HeadSHA,
			}
		}
	}
	return ""
}

func (w *watcher) gatherCandidate(
	ctx context.Context,
	repo Repo,
	latestGo Version,
) (Candidate, string, bool) {
	headSHA, err := w.ghClient.GetHeadSHA(ctx, repo)
	if err != nil {
		if stderrors.Is(err, ErrRateLimited) {
			return Candidate{}, "rate_limited", false
		}
		return dropRepo(repo, "head_sha", err)
	}

	goModContent, err := w.ghClient.GetGoMod(ctx, repo)
	if err != nil {
		if stderrors.Is(err, ErrRateLimited) {
			return Candidate{}, "rate_limited", false
		}
		return dropRepo(repo, "go_mod", err)
	}

	cfg, err := w.ghClient.GetMaintainerConfig(ctx, repo)
	if err != nil {
		if stderrors.Is(err, ErrRateLimited) {
			return Candidate{}, "rate_limited", false
		}
		return dropRepo(repo, "maintainer_config", err)
	}

	candidate := Candidate{
		Repo:       repo,
		HeadSHA:    headSHA,
		LatestGo:   latestGo,
		AutoUpdate: cfg.GoUpdate.AutoUpdate,
	}
	if goModContent != nil {
		candidate.GoModPresent = true
		currentGo, perr := ParseGoModVersion(ctx, goModContent)
		if perr == nil {
			candidate.GoModParsable = true
			candidate.CurrentGo = currentGo
		} else {
			glog.V(2).Infof(
				"go.mod unparsable repo=%s err=%v",
				repo.Key(),
				perr,
			)
		}
	}
	return candidate, "", false
}

// completeMergedUpdates scans the cursor's repos for a merged update PR and,
// when found, publishes a CompleteCommand for the task filed at that HEAD
// (same deterministic DeriveTaskID used at create time, so the controller
// closes the exact task the watcher created). Returns an abort reason (e.g.
// "rate_limited") or "" to continue the cycle.
//
// Only repos that have a cursor entry (i.e. the watcher emitted a task for
// them at LastSeenHeadSHA) are considered — a repo never scanned can't have a
// task to complete. A repo already completed for that HEAD is skipped via the
// cursor marker, so the no-op completion stays off the wire.
func (w *watcher) completeMergedUpdates(
	ctx context.Context,
	cursorState *Cursor,
	repos []Repo,
) string {
	for _, repo := range repos {
		select {
		case <-ctx.Done():
			glog.V(2).Infof(
				"poll cancelled during completeMergedUpdates at repo=%s",
				repo.Key(),
			)
			return ""
		default:
		}

		state, ok := cursorState.Repos[repo.Key()]
		if !ok || state == nil || state.LastSeenHeadSHA == "" {
			continue
		}
		if state.LastSeenHeadSHA == state.CompletedHeadSHA {
			continue
		}

		merged, err := w.ghClient.GetMergedUpdatePR(ctx, repo, state.LastSeenHeadSHA)
		if err != nil {
			if stderrors.Is(err, ErrRateLimited) {
				glog.Warningf(
					"poll cycle aborted: rate limited during GetMergedUpdatePR owner=%s",
					w.owner,
				)
				return "rate_limited"
			}
			glog.Warningf(
				"complete-task: GetMergedUpdatePR failed repo=%s sha=%s err=%v",
				repo.Key(),
				state.LastSeenHeadSHA,
				err,
			)
			continue
		}
		if !merged {
			continue
		}

		w.completeTask(ctx, repo, state, state.LastSeenHeadSHA)
	}
	return ""
}

// completeTask publishes a CompleteCommand for the update task filed at
// headSHA and marks the cursor entry so the completion fires once. The
// controller's executor is idempotent (skips when ## Resolution is already
// present), so a lost publish is safe — the next cycle retries. Errors are
// logged and counted, never fatal to the poll.
func (w *watcher) completeTask(
	ctx context.Context,
	repo Repo,
	state *RepoState,
	headSHA string,
) {
	taskID := DeriveTaskID(repo.Owner, repo.Name, headSHA)
	if err := w.completeSender.SendCommand(ctx, task.CompleteCommand{
		TaskIdentifier: agentlib.TaskIdentifier(taskID.String()),
	}); err != nil {
		w.metrics.IncCompleted("error")
		glog.Errorf(
			"complete-task: publish failed repo=%s/%s sha=%s taskID=%s err=%v",
			repo.Owner,
			repo.Name,
			headSHA,
			taskID,
			err,
		)
		return
	}
	w.metrics.IncCompleted("complete")
	// Merge into the existing RepoState in place — preserve every field
	// (LastSeenHeadSHA, plus any future additions) and only advance the
	// completed marker.
	state.CompletedHeadSHA = headSHA
	glog.V(2).Infof(
		"complete-task: published repo=%s/%s sha=%s taskID=%s",
		repo.Owner,
		repo.Name,
		headSHA,
		taskID,
	)
}

// dropRepo logs the always-on per-repo drop line. The phrase
// "repo dropped from cycle" is the operator's grep handle — do not reword it.
// Warning level (not V(2)) so the line is visible at default verbosity.
func dropRepo(repo Repo, step string, err error) (Candidate, string, bool) {
	glog.Warningf(
		"repo dropped from cycle: owner=%s repo=%s step=%s err=%v",
		repo.Owner,
		repo.Name,
		step,
		err,
	)
	return Candidate{}, "", true
}
