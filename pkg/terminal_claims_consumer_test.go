// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"sync"
	"time"

	"github.com/IBM/sarama"
	agentlib "github.com/bborbe/agent"
	base "github.com/bborbe/cqrs/base"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

// taskEvent marshals a real agentlib.Task, so a field or JSON-tag mismatch
// cannot pass the way a hand-written fixture would.
func taskEvent(repo, status, phase string) *sarama.ConsumerMessage {
	frontmatter := agentlib.TaskFrontmatter{"repo": repo, "status": status}
	if phase != "" {
		frontmatter["phase"] = phase
	}
	payload, err := json.Marshal(agentlib.Task{Frontmatter: frontmatter})
	Expect(err).NotTo(HaveOccurred())
	return &sarama.ConsumerMessage{Key: []byte(repo), Value: payload}
}

var _ = Describe("terminal claims consumer", func() {
	var (
		ctx    context.Context
		store  pkg.TerminalClaimsStore
		handle func(*sarama.ConsumerMessage) error
	)

	BeforeEach(func() {
		ctx = context.Background()
		store = pkg.NewInMemoryTerminalClaimsStore()
		handler := pkg.NewTerminalClaimsHandler(store)
		handle = func(msg *sarama.ConsumerMessage) error {
			return handler.ConsumeMessage(ctx, msg)
		}
	})

	canConsume := func(repo string) bool {
		ok, err := store.CanConsume(ctx, repo)
		Expect(err).NotTo(HaveOccurred())
		return ok
	}

	It("records only non-progressing tasks", func() {
		events := []struct {
			repo   string
			status string
			phase  string
		}{
			{"bborbe/completed", "completed", ""},
			{"bborbe/aborted", "aborted", ""},
			{"bborbe/human-review", "in_progress", "human_review"},
			{"bborbe/execution", "in_progress", "execution"},
			{"bborbe/planning", "next", "planning"},
		}
		for _, event := range events {
			Expect(handle(taskEvent(event.repo, event.status, event.phase))).To(Succeed())
		}
		Expect(handle(&sarama.ConsumerMessage{
			Key:   []byte("bborbe/deleted"),
			Value: nil,
		})).To(Succeed())

		Expect(canConsume("bborbe/completed")).To(BeTrue())
		Expect(canConsume("bborbe/aborted")).To(BeTrue())
		Expect(canConsume("bborbe/human-review")).To(BeTrue())
		Expect(canConsume("bborbe/execution")).To(BeFalse())
		Expect(canConsume("bborbe/planning")).To(BeFalse())
		Expect(canConsume("bborbe/deleted")).To(BeFalse())
	})

	It("hold is non-progressing", func() {
		Expect(handle(taskEvent("bborbe/held", "hold", "planning"))).To(Succeed())
		Expect(canConsume("bborbe/held")).To(BeTrue())
	})

	It("the entitlement is available exactly once", func() {
		Expect(store.Observe(ctx, "bborbe/disk-status")).To(Succeed())
		Expect(canConsume("bborbe/disk-status")).To(BeTrue())

		Expect(store.Consume(ctx, "bborbe/disk-status")).To(Succeed())
		Expect(canConsume("bborbe/disk-status")).To(BeFalse())
	})

	It("the consumer never returns on a recoverable error", func() {
		var mu sync.Mutex
		calls := 0
		consume := func(ctx context.Context) error {
			mu.Lock()
			calls++
			attempt := calls
			mu.Unlock()
			if attempt == 1 {
				return stderrors.New("broker unavailable")
			}
			<-ctx.Done()
			return ctx.Err()
		}

		ctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- pkg.RunTerminalClaimsConsumer(consume, time.Millisecond)(ctx)
		}()

		Eventually(func() int {
			mu.Lock()
			defer mu.Unlock()
			return calls
		}).Should(BeNumerically(">=", 2))

		cancel()
		Eventually(done).Should(Receive(BeNil()))
	})

	It("the event topic is derived from the prefix", func() {
		Expect(pkg.TerminalClaimsEventTopic(base.TopicPrefix("develop")).String()).
			To(Equal("develop-agent-task-v1-event"))
		Expect(pkg.TerminalClaimsEventTopic("").String()).
			To(Equal("agent-task-v1-event"))
	})
})
