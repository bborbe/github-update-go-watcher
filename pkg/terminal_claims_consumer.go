// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"time"

	"github.com/IBM/sarama"
	agentlib "github.com/bborbe/agent"
	base "github.com/bborbe/cqrs/base"
	"github.com/bborbe/errors"
	"github.com/bborbe/kafka"
	"github.com/bborbe/run"
	"github.com/golang/glog"
)

// TerminalClaimsRetryDelay is the delay before retrying after a recoverable
// error. The poll loop keeps running meanwhile.
const TerminalClaimsRetryDelay = 5 * time.Second

// TerminalClaimsEventTopic is the task lifecycle event topic. Derived from the
// schema id and the configured prefix — a bare literal subscribes to the wrong
// topic under a prefix.
func TerminalClaimsEventTopic(prefix base.TopicPrefix) kafka.Topic {
	return agentlib.TaskV1SchemaID.EventTopic(prefix)
}

// NewTerminalClaimsHandler records repos whose emitted task can no longer
// progress. The payload decodes to agentlib.Task; the store key is the
// frontmatter `repo` field (owner/name), the same key the watcher passes as
// Repo.String(). An empty payload is a delete event and is ignored.
func NewTerminalClaimsHandler(store TerminalClaimsStore) kafka.MessageHandler {
	return kafka.MessageHandlerFunc(func(ctx context.Context, msg *sarama.ConsumerMessage) error {
		if len(msg.Value) == 0 {
			return nil
		}
		var task agentlib.Task
		if err := json.Unmarshal(msg.Value, &task); err != nil {
			return errors.Wrapf(ctx, err, "decode task event key=%s", string(msg.Key))
		}
		if !taskCanNoLongerProgress(task) {
			return nil
		}
		repo, ok := task.Frontmatter.String("repo")
		if !ok || repo == "" {
			glog.V(2).Infof("task event has no repo frontmatter, ignoring key=%s", string(msg.Key))
			return nil
		}
		return store.Observe(ctx, repo)
	})
}

// taskCanNoLongerProgress is the dispatchability predicate: a terminal status,
// status: hold (a block with no scheduled resume date), or phase: human_review
// (no Config dispatches it and a record cannot leave it on its own). Every other
// status is a queued or running wait the claim exists to cover.
func taskCanNoLongerProgress(task agentlib.Task) bool {
	switch task.Frontmatter.Status().String() {
	case "completed", "aborted", "hold":
		return true
	}
	phase := task.Frontmatter.Phase()
	return phase != nil && phase.String() == "human_review"
}

// RunTerminalClaimsConsumer runs consume until ctx is cancelled. A recoverable
// error is logged and retried; the function never returns on a recoverable
// error, because service.Run tears the whole service down when any run.Func
// returns. On ctx cancellation it returns nil.
func RunTerminalClaimsConsumer(
	consume func(context.Context) error,
	retryDelay time.Duration,
) run.Func {
	return func(ctx context.Context) error {
		for {
			err := consume(ctx)
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				glog.Warningf("task-event consumer stopped, retrying err=%v", err)
			}
			timer := time.NewTimer(retryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}
	}
}
