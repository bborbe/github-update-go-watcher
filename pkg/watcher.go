// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"

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
// fresh CursorReader.
func NewWatcher(
	ghClient GitHubClient,
	goDevClient GoDevClient,
	publisher TaskPublisher,
	metrics Metrics,
	cursorPath string,
	owner string,
	taskCreationFilter filter.TaskCreationFilter,
) Watcher {
	return &watcher{
		ghClient:           ghClient,
		goDevClient:        goDevClient,
		publisher:          publisher,
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

	cycleFilter := filter.TaskCreationFilters{w.taskCreationFilter}
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
		AutoUpdate: cfg != nil && cfg.GoUpdate.AutoUpdate,
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
