// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	stderrors "errors"

	"github.com/bborbe/kafka"
	libkv "github.com/bborbe/kv"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("MemoryDB", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("serves the offset store the consumer uses", func() {
		store := kafka.NewOffsetStore(pkg.NewMemoryDB())
		topic := kafka.Topic("agent-task-v1-event")

		Expect(store.Set(ctx, topic, kafka.Partition(0), kafka.Offset(42))).To(Succeed())

		offset, err := store.Get(ctx, topic, kafka.Partition(0))
		Expect(err).NotTo(HaveOccurred())
		Expect(offset).To(Equal(kafka.Offset(42)))
	})

	It("returns an error wrapping bucket-not-found before any offset is set", func() {
		store := kafka.NewOffsetStore(pkg.NewMemoryDB())

		_, err := store.Get(ctx, kafka.Topic("agent-task-v1-event"), kafka.Partition(0))
		Expect(err).To(HaveOccurred())
		Expect(stderrors.Is(err, libkv.BucketNotFoundError)).To(BeTrue())
	})
})
