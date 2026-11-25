// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/agent/command/task"
	"github.com/golang/glog"
)

//counterfeiter:generate -o ../mocks/task_publisher.go --fake-name TaskPublisher . TaskPublisher

// TaskPublisher builds the CreateTaskCommand for a Candidate and sends it via
// the supplied CreateCommandSender. Returns true only on a successful send —
// the caller records the repo's HEAD in the cursor only on true.
type TaskPublisher interface {
	PublishCreate(ctx context.Context, candidate Candidate) bool
	// PublishMonthly builds and sends the monthly full-update CreateTaskCommand
	// for month (YYYY-MM). Returns true only on a successful send — the caller
	// records the month in the cursor only on true.
	PublishMonthly(ctx context.Context, candidate Candidate, month string) bool
	PublishDecision(ctx context.Context, candidate Candidate) bool
	// PublishStaleIgnore builds and sends the stale-ignore CreateTaskCommand.
	// Returns true only on a successful send — the caller records the emission
	// in the cursor only on true.
	PublishStaleIgnore(
		ctx context.Context,
		candidate Candidate,
		finding StaleIgnoreFinding,
		generation int,
	) bool
}

// NewTaskPublisher returns a TaskPublisher wrapping the given sender + metrics.
func NewTaskPublisher(
	sender task.CreateCommandSender,
	metrics Metrics,
	cfg TaskConfig,
) TaskPublisher {
	return &taskPublisher{
		sender:  sender,
		metrics: metrics,
		cfg:     cfg,
	}
}

type taskPublisher struct {
	sender  task.CreateCommandSender
	metrics Metrics
	cfg     TaskConfig
}

func (p *taskPublisher) PublishCreate(
	ctx context.Context,
	candidate Candidate,
) bool {
	cmd := BuildCreateCommand(candidate, p.cfg)
	if err := p.sender.SendCommand(ctx, cmd); err != nil {
		glog.Errorf(
			"publish create-task failed repo=%s sha=%s taskID=%s err=%v",
			candidate.Repo.Key(),
			candidate.HeadSHA,
			string(cmd.TaskIdentifier),
			err,
		)
		p.metrics.IncPublished("error")
		return false
	}
	glog.V(2).Infof(
		"published CreateTaskCommand repo=%s sha=%s taskID=%s stage=%s",
		candidate.Repo.Key(),
		candidate.HeadSHA,
		string(cmd.TaskIdentifier),
		p.cfg.Stage,
	)
	p.metrics.IncPublished("create")
	return true
}

func (p *taskPublisher) PublishMonthly(
	ctx context.Context,
	candidate Candidate,
	month string,
) bool {
	cmd := BuildMonthlyCreateCommand(candidate, p.cfg, month)
	if err := p.sender.SendCommand(ctx, cmd); err != nil {
		glog.Errorf(
			"publish monthly create-task failed repo=%s month=%s sha=%s taskID=%s err=%v",
			candidate.Repo.Key(),
			month,
			candidate.HeadSHA,
			string(cmd.TaskIdentifier),
			err,
		)
		p.metrics.IncPublished("error")
		return false
	}
	glog.V(2).Infof(
		"published monthly CreateTaskCommand repo=%s month=%s sha=%s taskID=%s stage=%s",
		candidate.Repo.Key(),
		month,
		candidate.HeadSHA,
		string(cmd.TaskIdentifier),
		p.cfg.Stage,
	)
	p.metrics.IncPublished("create")
	return true
}

func (p *taskPublisher) PublishStaleIgnore(
	ctx context.Context,
	candidate Candidate,
	finding StaleIgnoreFinding,
	generation int,
) bool {
	cmd := BuildStaleIgnoreCommand(candidate, finding, generation, p.cfg)
	if err := p.sender.SendCommand(ctx, cmd); err != nil {
		glog.Errorf(
			"publish stale-ignore task failed repo=%s generation=%d taskID=%s err=%v",
			candidate.Repo.Key(),
			generation,
			string(cmd.TaskIdentifier),
			err,
		)
		p.metrics.IncPublished("error")
		return false
	}
	glog.V(2).Infof(
		"published StaleIgnoreCommand repo=%s stale_ids=%s generation=%d taskID=%s stage=%s",
		candidate.Repo.Key(),
		strings.Join(finding.IDs, ","),
		generation,
		string(cmd.TaskIdentifier),
		p.cfg.Stage,
	)
	p.metrics.IncPublished("create")
	return true
}

func (p *taskPublisher) PublishDecision(
	ctx context.Context,
	candidate Candidate,
) bool {
	cmd := BuildDecisionCommand(candidate, p.cfg)
	if err := p.sender.SendCommand(ctx, cmd); err != nil {
		glog.Errorf(
			"publish decision-task failed repo=%s taskID=%s err=%v",
			candidate.Repo.Key(),
			string(cmd.TaskIdentifier),
			err,
		)
		p.metrics.IncPublished("error")
		return false
	}
	glog.V(2).Infof(
		"published DecisionTaskCommand repo=%s taskID=%s stage=%s",
		candidate.Repo.Key(),
		string(cmd.TaskIdentifier),
		p.cfg.Stage,
	)
	p.metrics.IncPublished("create")
	return true
}
