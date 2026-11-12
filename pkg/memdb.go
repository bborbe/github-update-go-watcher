// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"

	libkv "github.com/bborbe/kv"
)

// errMemDBClosed is returned by a write on a closed DB rather than panicking on
// the nil bucket map.
var errMemDBClosed = errors.New("memdb: database is closed")

// NewMemDB returns a session-scoped, in-process libkv.DB implementation. It
// exists because github.com/bborbe/kv ships no public memory constructor (only
// NewDBWithMetrics), and the task-lifecycle consumer needs an offset store
// without a PVC.
//
// A session-scoped offset store means a restart replays the event topic from
// its oldest offset. That is safe here — the emitted task identifier is a
// deterministic UUID5, so a replay re-observes identifiers the store already
// holds and changes nothing — and it is useful: the replay rebuilds the
// non-progressing set from history, so a fresh pod can end claims left behind
// by emissions that failed before it started.
//
// The implementation is deliberately minimal: only the methods the offset store
// and the terminal-claims store touch.
func NewMemDB() libkv.DB {
	return &memDB{
		buckets: make(map[string]map[string][]byte),
	}
}

type memDB struct {
	mu      sync.RWMutex
	buckets map[string]map[string][]byte
	stats   libkv.Stats
}

func (d *memDB) Update(
	ctx context.Context,
	fn func(ctx context.Context, tx libkv.Tx) error,
) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return fn(ctx, &memTx{db: d})
}

func (d *memDB) View(
	ctx context.Context,
	fn func(ctx context.Context, tx libkv.Tx) error,
) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return fn(ctx, &memTx{db: d})
}

func (d *memDB) Sync() error { return nil }

func (d *memDB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buckets = nil
	return nil
}

func (d *memDB) Remove() error { return d.Close() }

func (d *memDB) Stats(ctx context.Context) (*libkv.Stats, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	s := d.stats
	return &s, nil
}

func (d *memDB) StatsDetailed(ctx context.Context) (*libkv.Stats, error) {
	return d.Stats(ctx)
}

type memTx struct {
	db *memDB
}

func (t *memTx) Bucket(
	ctx context.Context,
	name libkv.BucketName,
) (libkv.Bucket, error) {
	if t.db.buckets == nil {
		return nil, libkv.ErrBucketNotFound
	}
	b, ok := t.db.buckets[name.String()]
	if !ok {
		return nil, libkv.ErrBucketNotFound
	}
	return &memBucket{b: b}, nil
}

func (t *memTx) CreateBucket(
	ctx context.Context,
	name libkv.BucketName,
) (libkv.Bucket, error) {
	if t.db.buckets == nil {
		return nil, errMemDBClosed
	}
	if _, ok := t.db.buckets[name.String()]; ok {
		return nil, libkv.ErrBucketAlreadyExists
	}
	t.db.buckets[name.String()] = make(map[string][]byte)
	return &memBucket{b: t.db.buckets[name.String()]}, nil
}

func (t *memTx) CreateBucketIfNotExists(
	ctx context.Context,
	name libkv.BucketName,
) (libkv.Bucket, error) {
	if t.db.buckets == nil {
		return nil, errMemDBClosed
	}
	if _, ok := t.db.buckets[name.String()]; !ok {
		t.db.buckets[name.String()] = make(map[string][]byte)
	}
	return &memBucket{b: t.db.buckets[name.String()]}, nil
}

func (t *memTx) DeleteBucket(ctx context.Context, name libkv.BucketName) error {
	if t.db.buckets == nil {
		return errMemDBClosed
	}
	delete(t.db.buckets, name.String())
	return nil
}

func (t *memTx) ListBucketNames(ctx context.Context) (libkv.BucketNames, error) {
	names := make(libkv.BucketNames, 0, len(t.db.buckets))
	for name := range t.db.buckets {
		names = append(names, libkv.BucketName(name))
	}
	sort.Slice(names, func(i, j int) bool { return names[i].String() < names[j].String() })
	return names, nil
}

type memBucket struct {
	b map[string][]byte
}

func (b *memBucket) Put(ctx context.Context, key []byte, value []byte) error {
	b.b[string(key)] = append([]byte(nil), value...)
	return nil
}

func (b *memBucket) Get(ctx context.Context, key []byte) (libkv.Item, error) {
	v, ok := b.b[string(key)]
	if !ok {
		return libkv.NewByteItem(key, nil), nil
	}
	// Copy: the map value is live, and a caller holding it could mutate stored
	// state outside a transaction. Put copies on the way in, so read copies on
	// the way out.
	return libkv.NewByteItem(key, append([]byte(nil), v...)), nil
}

func (b *memBucket) Delete(ctx context.Context, key []byte) error {
	delete(b.b, string(key))
	return nil
}

func (b *memBucket) Iterator() libkv.Iterator {
	return newMemIterator(b.b, false)
}

func (b *memBucket) IteratorReverse() libkv.Iterator {
	return newMemIterator(b.b, true)
}

type memIterator struct {
	keys    []string
	values  map[string][]byte
	reverse bool
	index   int
}

func newMemIterator(values map[string][]byte, reverse bool) libkv.Iterator {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if reverse {
		slices.Reverse(keys)
	}
	return &memIterator{keys: keys, values: values, reverse: reverse}
}

func (i *memIterator) Close() {}

func (i *memIterator) Item() libkv.Item {
	if !i.Valid() {
		return libkv.NewByteItem(nil, nil)
	}
	key := i.keys[i.index]
	return libkv.NewByteItem([]byte(key), append([]byte(nil), i.values[key]...))
}

func (i *memIterator) Next() { i.index++ }

func (i *memIterator) Valid() bool { return i.index >= 0 && i.index < len(i.keys) }

// Rewind positions at the first element, so callers can use
// `for it.Rewind(); it.Valid(); it.Next()`.
func (i *memIterator) Rewind() { i.index = 0 }

// Seek positions at the first key >= key (forward) or <= key (reverse).
func (i *memIterator) Seek(key []byte) {
	k := string(key)
	i.index = sort.Search(len(i.keys), func(n int) bool {
		if i.reverse {
			return i.keys[n] <= k
		}
		return i.keys[n] >= k
	})
}
