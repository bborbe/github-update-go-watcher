// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"

	"github.com/IBM/sarama"
	agentlib "github.com/bborbe/agent"
	base "github.com/bborbe/cqrs/base"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

// taskEvent marshals a real agentlib.Task, so a field or JSON-tag mismatch
// cannot pass the way a hand-written fixture would.
func taskEvent(taskID string, frontmatter agentlib.TaskFrontmatter) *sarama.ConsumerMessage {
	payload, err := json.Marshal(agentlib.Task{
		TaskIdentifier: agentlib.TaskIdentifier(taskID),
		Frontmatter:    frontmatter,
	})
	Expect(err).NotTo(HaveOccurred())
	return &sarama.ConsumerMessage{Key: []byte(taskID), Value: payload}
}

var _ = Describe("terminal claims consumer", func() {
	var (
		ctx   context.Context
		store pkg.TerminalClaims
	)

	BeforeEach(func() {
		ctx = context.Background()
		store = pkg.NewTerminalClaims(pkg.NewMemDB())
	})

	consume := func(msg *sarama.ConsumerMessage) error {
		return pkg.NewTerminalClaimsMessageHandler(store).ConsumeMessage(ctx, nil, msg)
	}

	allowed := func(taskID string) bool {
		ok, err := store.ClaimClearAllowed(ctx, taskID)
		Expect(err).NotTo(HaveOccurred())
		return ok
	}

	DescribeTable("records only tasks that can no longer progress",
		func(frontmatter agentlib.TaskFrontmatter, recorded bool) {
			Expect(consume(taskEvent("task-1", frontmatter))).To(Succeed())
			Expect(allowed("task-1")).To(Equal(recorded))
		},
		Entry("completed", agentlib.TaskFrontmatter{"status": "completed"}, true),
		Entry("aborted", agentlib.TaskFrontmatter{"status": "aborted"}, true),
		Entry("in_progress parked at human_review",
			agentlib.TaskFrontmatter{"status": "in_progress", "phase": "human_review"}, true),
		Entry("hold parked at human_review",
			agentlib.TaskFrontmatter{"status": "hold", "phase": "human_review"}, true),
		Entry("running at execution",
			agentlib.TaskFrontmatter{"status": "in_progress", "phase": "execution"}, false),
		Entry("running at ai_review",
			agentlib.TaskFrontmatter{"status": "in_progress", "phase": "ai_review"}, false),
		Entry("queued at planning",
			agentlib.TaskFrontmatter{"status": "next", "phase": "planning"}, false),
		Entry("queued with no phase",
			agentlib.TaskFrontmatter{"status": "next"}, false),
	)

	It("ignores a delete event", func() {
		Expect(consume(&sarama.ConsumerMessage{
			Key:   []byte("task-1"),
			Value: nil,
		})).To(Succeed())
		Expect(allowed("task-1")).To(BeFalse())
	})

	It("derives the topic from the prefix", func() {
		Expect(pkg.TerminalClaimsTopicForTest(base.TopicPrefix("master")).String()).
			To(Equal("master-agent-task-v1-event"))
		Expect(pkg.TerminalClaimsTopicForTest(base.TopicPrefix("")).String()).
			To(Equal("agent-task-v1-event"))
	})
})
