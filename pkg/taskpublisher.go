// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/agent/command/task"
	"github.com/golang/glog"
)

//counterfeiter:generate -o ../mocks/task_publisher.go --fake-name TaskPublisher . TaskPublisher

// TaskPublisher builds the CreateTaskCommand for a Candidate and sends it via
// the supplied CreateCommandSender. Returns true only on a successful send —
// the caller records the repo's HEAD in the cursor only on true.
type TaskPublisher interface {
	PublishCreate(ctx context.Context, candidate Candidate) bool
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
