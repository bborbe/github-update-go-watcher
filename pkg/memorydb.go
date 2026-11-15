// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"sort"
	"sync"

	libkv "github.com/bborbe/kv"
)

// NewMemoryDB returns an in-process libkv.DB. Offsets live in the process, so a
// restart replays the task event topic from its oldest offset — by design.
func NewMemoryDB() libkv.DB {
	return &memoryDB{buckets: make(map[string]map[string][]byte)}
}

type memoryDB struct {
	mu      sync.RWMutex
	buckets map[string]map[string][]byte
}

func (d *memoryDB) Update(
	ctx context.Context,
	fn func(ctx context.Context, tx libkv.Tx) error,
) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return fn(ctx, &memoryTx{db: d})
}

func (d *memoryDB) View(
	ctx context.Context,
	fn func(ctx context.Context, tx libkv.Tx) error,
) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return fn(ctx, &memoryTx{db: d})
}

func (d *memoryDB) Sync() error { return nil }

func (d *memoryDB) Close() error { return nil }

func (d *memoryDB) Remove() error { return nil }

func (d *memoryDB) Stats(_ context.Context) (*libkv.Stats, error) {
	return d.stats(false), nil
}

func (d *memoryDB) StatsDetailed(_ context.Context) (*libkv.Stats, error) {
	return d.stats(true), nil
}

func (d *memoryDB) stats(detailed bool) *libkv.Stats {
	d.mu.RLock()
	defer d.mu.RUnlock()
	names := make([]string, 0, len(d.buckets))
	for name := range d.buckets {
		names = append(names, name)
	}
	sort.Strings(names)
	buckets := make([]libkv.BucketStats, 0, len(names))
	for _, name := range names {
		entry := libkv.BucketStats{Name: libkv.NewBucketName(name)}
		if detailed {
			entry.KeyCount = int64(len(d.buckets[name]))
		}
		buckets = append(buckets, entry)
	}
	return &libkv.Stats{Backend: "memory", Detailed: detailed, Buckets: buckets}
}

type memoryTx struct {
	db *memoryDB
}

func (t *memoryTx) Bucket(
	_ context.Context,
	name libkv.BucketName,
) (libkv.Bucket, error) {
	bucket, ok := t.db.buckets[name.String()]
	if !ok {
		return nil, libkv.ErrBucketNotFound
	}
	return &memoryBucket{entries: bucket}, nil
}

func (t *memoryTx) CreateBucket(
	_ context.Context,
	name libkv.BucketName,
) (libkv.Bucket, error) {
	if _, ok := t.db.buckets[name.String()]; ok {
		return nil, libkv.ErrBucketAlreadyExists
	}
	t.db.buckets[name.String()] = make(map[string][]byte)
	return &memoryBucket{entries: t.db.buckets[name.String()]}, nil
}

func (t *memoryTx) CreateBucketIfNotExists(
	_ context.Context,
	name libkv.BucketName,
) (libkv.Bucket, error) {
	if _, ok := t.db.buckets[name.String()]; !ok {
		t.db.buckets[name.String()] = make(map[string][]byte)
	}
	return &memoryBucket{entries: t.db.buckets[name.String()]}, nil
}

func (t *memoryTx) DeleteBucket(_ context.Context, name libkv.BucketName) error {
	if _, ok := t.db.buckets[name.String()]; !ok {
		return libkv.ErrBucketNotFound
	}
	delete(t.db.buckets, name.String())
	return nil
}

func (t *memoryTx) ListBucketNames(_ context.Context) (libkv.BucketNames, error) {
	names := make(libkv.BucketNames, 0, len(t.db.buckets))
	for name := range t.db.buckets {
		names = append(names, libkv.NewBucketName(name))
	}
	sort.Slice(names, func(i, j int) bool {
		return names[i].String() < names[j].String()
	})
	return names, nil
}

type memoryBucket struct {
	entries map[string][]byte
}

func (b *memoryBucket) Put(_ context.Context, key []byte, value []byte) error {
	b.entries[string(key)] = append([]byte(nil), value...)
	return nil
}

func (b *memoryBucket) Get(_ context.Context, key []byte) (libkv.Item, error) {
	value, ok := b.entries[string(key)]
	if !ok {
		return libkv.NewByteItem(key, nil), nil
	}
	return libkv.NewByteItem(key, append([]byte(nil), value...)), nil
}

func (b *memoryBucket) Delete(_ context.Context, key []byte) error {
	delete(b.entries, string(key))
	return nil
}

func (b *memoryBucket) Iterator() libkv.Iterator {
	return newMemoryIterator(b.entries, false)
}

func (b *memoryBucket) IteratorReverse() libkv.Iterator {
	return newMemoryIterator(b.entries, true)
}

type memoryIterator struct {
	keys    []string
	values  map[string][]byte
	reverse bool
	index   int
}

func newMemoryIterator(values map[string][]byte, reverse bool) libkv.Iterator {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if reverse {
		for i, j := 0, len(keys)-1; i < j; i, j = i+1, j-1 {
			keys[i], keys[j] = keys[j], keys[i]
		}
	}
	return &memoryIterator{keys: keys, values: values, reverse: reverse}
}

func (i *memoryIterator) Close() {}

func (i *memoryIterator) Item() libkv.Item {
	if !i.Valid() {
		return libkv.NewByteItem(nil, nil)
	}
	key := i.keys[i.index]
	return libkv.NewByteItem([]byte(key), append([]byte(nil), i.values[key]...))
}

func (i *memoryIterator) Next() { i.index++ }

func (i *memoryIterator) Valid() bool { return i.index >= 0 && i.index < len(i.keys) }

func (i *memoryIterator) Rewind() { i.index = 0 }

func (i *memoryIterator) Seek(key []byte) {
	target := string(key)
	if i.reverse {
		i.index = sort.Search(len(i.keys), func(n int) bool {
			return i.keys[n] <= target
		})
		return
	}
	i.index = sort.SearchStrings(i.keys, target)
}
