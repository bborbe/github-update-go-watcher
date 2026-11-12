// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	stderrors "errors"

	libkafka "github.com/bborbe/kafka"
	libkv "github.com/bborbe/kv"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("MemDB", func() {
	var (
		ctx context.Context
		db  libkv.DB
	)

	BeforeEach(func() {
		ctx = context.Background()
		db = pkg.NewMemDB()
	})

	It("serves the offset store the consumer actually uses", func() {
		store := libkafka.NewOffsetStore(db)
		Expect(store.Set(ctx, libkafka.Topic("t"), 0, 42)).To(Succeed())

		// A second transaction must see the value: the offset store writes on
		// one cycle and reads on the next.
		var got libkafka.Offset
		Expect(db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
			v, err := store.Get(ctx, libkafka.Topic("t"), 0)
			got = v
			return err
		})).To(Succeed())
		Expect(got).To(Equal(libkafka.Offset(42)))
	})

	It("creates a bucket and reports a missing one", func() {
		Expect(db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
			_, err := tx.Bucket(ctx, libkv.NewBucketName("absent"))
			Expect(stderrors.Is(err, libkv.ErrBucketNotFound)).To(BeTrue())

			bucket, err := tx.CreateBucketIfNotExists(ctx, libkv.NewBucketName("b"))
			Expect(err).NotTo(HaveOccurred())
			Expect(bucket).NotTo(BeNil())

			again, err := tx.Bucket(ctx, libkv.NewBucketName("b"))
			Expect(err).NotTo(HaveOccurred())
			Expect(again).NotTo(BeNil())
			return nil
		})).To(Succeed())
	})

	It("returns an empty item and no error for a missing key", func() {
		Expect(db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
			bucket, err := tx.CreateBucketIfNotExists(ctx, libkv.NewBucketName("b"))
			Expect(err).NotTo(HaveOccurred())

			item, err := bucket.Get(ctx, []byte("nope"))
			Expect(err).NotTo(HaveOccurred())
			Expect(item.Exists()).To(BeFalse())
			return nil
		})).To(Succeed())
	})

	It("iterates forward and reverse in key order", func() {
		Expect(db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
			bucket, err := tx.CreateBucketIfNotExists(ctx, libkv.NewBucketName("b"))
			Expect(err).NotTo(HaveOccurred())
			for _, k := range []string{"b", "a", "c"} {
				Expect(bucket.Put(ctx, []byte(k), []byte{1})).To(Succeed())
			}

			var forward []string
			it := bucket.Iterator()
			defer it.Close()
			for it.Rewind(); it.Valid(); it.Next() {
				forward = append(forward, string(it.Item().Key()))
			}
			Expect(forward).To(Equal([]string{"a", "b", "c"}))

			var reverse []string
			rit := bucket.IteratorReverse()
			defer rit.Close()
			for rit.Rewind(); rit.Valid(); rit.Next() {
				reverse = append(reverse, string(rit.Item().Key()))
			}
			Expect(reverse).To(Equal([]string{"c", "b", "a"}))
			return nil
		})).To(Succeed())
	})

	It("seeks to the first key at or after the target", func() {
		Expect(db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
			bucket, err := tx.CreateBucketIfNotExists(ctx, libkv.NewBucketName("b"))
			Expect(err).NotTo(HaveOccurred())
			for _, k := range []string{"a", "c", "e"} {
				Expect(bucket.Put(ctx, []byte(k), []byte{1})).To(Succeed())
			}

			it := bucket.Iterator()
			defer it.Close()
			it.Seek([]byte("b"))
			Expect(it.Valid()).To(BeTrue())
			Expect(string(it.Item().Key())).To(Equal("c"))
			return nil
		})).To(Succeed())
	})
})
