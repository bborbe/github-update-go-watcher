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
	libkafka "github.com/bborbe/kafka"
	libkv "github.com/bborbe/kv"
	"github.com/bborbe/log"
	"github.com/bborbe/run"
	"github.com/bborbe/vault-cli/pkg/domain"
	"github.com/golang/glog"
)

// terminalClaimsTopic returns the prefixed task lifecycle event topic. Derived
// from the schema id and the configured prefix — a bare literal would subscribe
// to the wrong topic under a prefix and receive nothing.
func terminalClaimsTopic(prefix base.TopicPrefix) libkafka.Topic {
	return agentlib.TaskV1SchemaID.EventTopic(prefix)
}

// isNonProgressingTask reports whether no Config will dispatch task again.
//
// Two populations qualify:
//   - a terminal status (completed, aborted) — the task is over;
//   - phase human_review — nothing dispatches it and a record cannot leave that
//     phase on its own, so it is parked, not in flight. A record parked there is
//     owned by nobody, and treating it as live is what freezes a repo for the
//     whole InFlightTTL.
//
// A task queued or running at a dispatchable phase (planning, execution,
// ai_review) must NOT qualify: the claim exists to cover that wait.
//
// Frontmatter.Phase() returns a pointer, so an absent phase is neither terminal
// nor parked and is ignored.
func isNonProgressingTask(task agentlib.Task) bool {
	switch task.Frontmatter.Status() {
	case domain.TaskStatusCompleted, domain.TaskStatusAborted:
		return true
	}
	phase := task.Frontmatter.Phase()
	return phase != nil && *phase == domain.TaskPhaseHumanReview
}

// NewTerminalClaimsMessageHandler returns a transaction message handler that
// records the identifier of a task that can no longer progress.
//
// An empty payload is a delete event and is ignored. A task still able to
// progress is ignored, so a queued or running emission keeps its claim. The task
// identifier is the Kafka message key, which is how the producer writes it.
func NewTerminalClaimsMessageHandler(claims TerminalClaims) libkafka.MessageHandlerTx {
	return libkafka.MessageHandlerTxFunc(
		func(ctx context.Context, tx libkv.Tx, msg *sarama.ConsumerMessage) error {
			if len(msg.Value) == 0 {
				return nil
			}
			var task agentlib.Task
			if err := json.Unmarshal(msg.Value, &task); err != nil {
				return errors.Wrap(ctx, err, "decode task event")
			}
			if !isNonProgressingTask(task) {
				return nil
			}
			taskID := string(msg.Key)
			if err := claims.Observe(ctx, taskID); err != nil {
				return errors.Wrapf(ctx, err, "observe taskID=%s", taskID)
			}
			glog.V(2).Infof("non-progressing task observed taskID=%s", taskID)
			return nil
		},
	)
}

// terminalClaimsReconnectDelay is how long the consumer waits before
// reconnecting after a recoverable error. The event topic is offset-tracked, so
// a reconnect resumes from the stored offset and nothing is lost. Deliberately
// not configurable: the spec adds no tuning knob.
const terminalClaimsReconnectDelay = 5 * time.Second

// NewTerminalClaimsConsumer returns a run.Func that consumes the task lifecycle
// event stream and records identifiers whose task can no longer progress.
//
// It never returns on a recoverable error: main.go passes it to service.Run,
// which wraps every func in run.CancelOnFirstFinishWait — a return would tear
// down the poll loop and the HTTP server. It retries internally and returns only
// when ctx is cancelled.
func NewTerminalClaimsConsumer(
	saramaClientProvider libkafka.SaramaClientProvider,
	db libkv.DB,
	prefix base.TopicPrefix,
	claims TerminalClaims,
) run.Func {
	consumer := libkafka.NewOffsetConsumerHighwaterMarksBatchWithProvider(
		saramaClientProvider,
		terminalClaimsTopic(prefix),
		libkafka.NewStoreOffsetManager(
			libkafka.NewOffsetStore(db),
			libkafka.OffsetOldest,
			libkafka.OffsetNewest,
		),
		libkafka.NewMessageHandlerBatchTxUpdate(
			db,
			libkafka.NewMessageHandlerBatchTx(
				libkafka.NewMessageHandlerTxSkipErrors(
					NewTerminalClaimsMessageHandler(claims),
					log.DefaultSamplerFactory,
				),
			),
		),
		libkafka.BatchSize(1),
		run.NewTrigger(),
		log.DefaultSamplerFactory,
	)
	return func(ctx context.Context) error {
		for {
			if err := consumer.Consume(ctx); err != nil && ctx.Err() == nil {
				glog.Warningf("terminal claims consumer: %v; reconnecting", err)
			}
			if ctx.Err() != nil {
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(terminalClaimsReconnectDelay):
			}
		}
	}
}
