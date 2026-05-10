# Product-Grade DLHT Design

## Goal

Add a new production-grade `DLHT[K, V]` concurrent hash table to `github.com/puzpuzpuz/xsync/v4` without replacing the existing `Map[K, V]`. The public surface stays simple and idiomatic for existing `xsync` users, while the DLHT internals live behind a clean `internal/dlht` boundary so the reference folders `cc/` and `dlht/` can be deleted later.

## Public API

The root package will expose exactly one new root source file for this feature: `dlht.go`. That file owns the public type, constructor, config, options, stats alias/wrapper, and method forwarding.

Public type and constructor:

```go
type DLHT[K comparable, V any] struct {
    m *dlht.Map[K, V]
}

type DLHTConfig struct {
    sizeHint int
}

func NewDLHT[K comparable, V any](options ...func(*DLHTConfig)) *DLHT[K, V]
func WithDLHTPresize(sizeHint int) func(*DLHTConfig)
```

Public methods mirror the simple `Map` API rather than the paper's `Get`/`Insert`/`Put` names:

```go
func (m *DLHT[K, V]) Load(key K) (value V, ok bool)
func (m *DLHT[K, V]) Store(key K, value V)
func (m *DLHT[K, V]) LoadOrStore(key K, value V) (actual V, loaded bool)
func (m *DLHT[K, V]) LoadAndStore(key K, value V) (actual V, loaded bool)
func (m *DLHT[K, V]) LoadAndDelete(key K) (value V, loaded bool)
func (m *DLHT[K, V]) Delete(key K)
func (m *DLHT[K, V]) Range(f func(key K, value V) bool)
func (m *DLHT[K, V]) All() iter.Seq2[K, V]
func (m *DLHT[K, V]) Size() int
func (m *DLHT[K, V]) Stats() DLHTStats
```

The first implementation will not expose `Compute`. DLHT's lock-free slot protocol cannot safely execute arbitrary user callbacks inside an update path without adding a separate synchronization design. Keeping `Compute` out is intentional product scope, not a missing piece.

## Internal Package Layout

All non-trivial implementation details go under `internal/dlht`:

```text
internal/dlht/
  map.go          constructor and internal Map type
  types.go        Entry, Slot, Header, LinkMeta, primary/link bucket layouts
  config.go       internal Options and Stats
  hash.go         maphash plus fast integer-key hashing path
  header.go       bin state, slot state, version helpers
  index.go        index allocation, bucket addressing, alignment helpers
  link.go         link bucket allocation and attachment
  ops_load.go     Load and contains-style helpers
  ops_store.go    Store, LoadOrStore, LoadAndStore
  ops_delete.go   LoadAndDelete and Delete
  resize.go       cooperative non-blocking resize
  iter.go         weak iteration and Go 1.23+ iterator adapter
  stats.go        approximate size and stats
  asm_amd64.go    amd64 DWCAS wrapper declarations and write-barrier integration
  asm_amd64.s     CMPXCHG16B implementation
  asm_arm64.go    arm64 DWCAS wrapper declarations and write-barrier integration
  asm_arm64.s     CASPAL/CASPD implementation
  asm_generic.go  build-tagged fallback for unsupported architectures
```

The root package must not import or depend on the untracked reference folders `cc/` or `dlht/`. Those folders are source material only.

## Core Data Structure

DLHT uses closed addressing with bounded cache-line chaining, following the HPDC'24 DLHT paper rather than the open-addressing DHLT-style prototype in `cc/`.

Each index contains:

- A power-of-two primary bin array.
- A smaller link-bucket arena for bounded overflow.
- A per-index `indexNext` pointer used during resize generation traversal.
- Bottom-up link allocation for foreground inserts.
- Top-down link allocation during resize transfer to avoid fighting foreground allocation.

Each bin contains one primary bucket plus up to three link buckets:

- Primary bucket: `Header`, `LinkMeta`, and 3 slots.
- Link bucket: 4 slots.
- Maximum slots per bin: 15.

Each slot stores:

```go
type slot[K comparable, V any] struct {
    hash uint64
    ptr  *entry[K, V]
}
```

The heap entry stores the real key and value:

```go
type entry[K comparable, V any] struct {
    key   K
    value V
}
```

Storing the hash in-slot keeps fixed 16-byte slots suitable for double-word CAS and lets lookups filter candidates before dereferencing heap entries.

## Header Protocol

Each primary bucket has a 64-bit header:

```text
[63:32] version | [31:30] bin state | [29:0] 15 two-bit slot states
```

Bin states:

- `NoTransfer`: normal operation.
- `InTransfer`: this bin is being migrated to the next index.
- `DoneTransfer`: operations continue in `indexNext`.

Slot states:

- `Invalid`: empty and reusable.
- `Trying`: reserved by an insert/upsert path, invisible to readers.
- `Valid`: visible live entry.

Readers use a seqlock-style validation: load header, inspect visible slots, load header again, and retry if it changed. Writers publish by transitioning slot states through header CASes. Delete makes the slot invisible and reusable immediately.

## Operations

### Load

`Load` hashes the key, finds the bin, checks resize state, scans valid primary slots, then scans attached link slots. It does not write shared memory on the normal path. It retries if the bin header changes during the read.

### Store

`Store` is an upsert built on the DLHT slot protocol:

- If the key exists, replace the slot `{hash, *entry}` using DWCAS.
- If the key is absent, reserve an invalid slot with a header CAS, fill hash and entry pointer, then publish with another header CAS.
- If no slot is available, trigger/help resize and retry.

This gives `xsync` users the familiar `Store` semantics while preserving the paper-style insert/update machinery internally.

### LoadOrStore

`LoadOrStore` first searches for an existing key. If found, it returns the existing value. If absent, it reserves and publishes a new slot. Races with another insertion of the same key resolve by re-checking the bin before finalizing or by retrying after header changes.

### LoadAndStore

`LoadAndStore` returns the old value when the key exists and stores the new value. If the key is absent, it stores the new value and returns `loaded=false`, matching `xsync.Map` semantics.

### LoadAndDelete / Delete

Delete finds the target slot under seqlock validation, clears the slot pointer with DWCAS, then marks the slot state invalid so the index slot is immediately reusable. This is one of the key DLHT benefits over tombstone-heavy open-addressing designs.

### Range / All

Iteration is weakly consistent and non-blocking. It scans the active index's bins and reads each bin with seqlock validation. If it encounters a transferred bin, it follows `indexNext` for that shard. It may reflect concurrent changes but must not return torn entries or panic under resize.

### Size / Stats

`Size` is approximate and O(number of bins), counting valid slot states from headers. `Stats` exposes bins, allocated links, link capacity, approximate size, approximate load factor, and whether a resize is in progress.

## Resize Design

Resize is cooperative and per-bin non-blocking:

1. The triggering goroutine reserves a resize context.
2. It allocates a larger index using a growth factor that decreases as the index grows.
3. It publishes `old.indexNext = new` before transfer starts.
4. Helpers claim chunks of old bins.
5. Each bin transfer marks the old bin `InTransfer`, copies valid slots into the new index, then marks the old bin `DoneTransfer`.
6. Operations seeing `InTransfer` briefly yield and retry; operations seeing `DoneTransfer` continue through `indexNext`.
7. The last helper publishes the new active index and clears the resize context.

This design avoids a global stop-the-world map migration. It blocks only operations targeting a currently transferring bin.

## Hashing

The implementation reuses ideas already present in `xsync.Map`:

- Use `maphash.Comparable` for general comparable keys.
- Detect integer-like key types and use the existing fast multiply/xorshift integer hash style.
- Use power-of-two bin counts so bin selection is `hash & mask`.

Hash seed ownership lives inside the internal DLHT index/config so resizes preserve hash mapping assumptions where needed.

## Architecture Support

Product-grade support requires explicit build behavior:

- `amd64`: DWCAS through `CMPXCHG16B` assembly.
- `arm64`: DWCAS through pairwise CAS instructions where available.
- Other architectures: build-tagged fallback fails at build time with a clear unsupported-architecture message. DLHT requires reliable 16-byte CAS; silently weakening concurrency semantics would be worse than failing early.

The implementation must preserve Go GC write-barrier correctness when swapping entry pointers from assembly.

## Testing Strategy

Tests will be brought into root/internal style rather than depending on the reference module:

- Basic API parity tests against `Map` behavior for `Load`, `Store`, `LoadOrStore`, `LoadAndStore`, `LoadAndDelete`, `Delete`, `Range`, `All`, `Size`, and `Stats`.
- Typed key coverage for integer, string, and small struct keys.
- Resize preservation tests.
- Parallel insert/store/delete stress tests.
- Regression tests for Put/Delete/Insert races inspired by the reference `dlht/tests/issues` cases.
- Per-key linearizability tests for small histories and high-contention key sets.
- Race detector compatibility where feasible.
- Allocation/alignment tests for bucket layout and DWCAS slot alignment.

The implementation must pass the existing `go test ./...` for the root module and will add focused DLHT tests without importing `cc/` or `dlht/`.

## Benchmark Strategy

Benchmarks will compare:

- Existing `Map`.
- New `DLHT`.
- `sync.Map` where useful.

Workloads:

- Read-only warmed map.
- Read-heavy mixed workload.
- Balanced store/delete churn.
- Growth-heavy insert workload.
- Pre-sized vs grow-from-small.
- Integer and string keys.

Benchmarks live in root test files so they remain valid after the reference folders are deleted.

## Reuse Decisions

Reuse from current repo:

- Public API naming and behavior from `Map`.
- `cacheLineSize`, `nextPowOf2`, integer key detection and hashing concepts.
- Existing benchmark and stress-test style.

Use from `dlht/` as reference only:

- Header layout.
- Bounded cache-line chaining.
- DWCAS slot replacement.
- Per-bin resize transfer.
- Race regression ideas.

Use from `cc/` as reference only:

- Simplicity of public map operations.
- Some stress-test ideas.

Do not reuse directly:

- `cc/DHLTMap` open-addressing internals, because they are not the paper DLHT design.
- `dlht/` module paths or package names.
- Any reference folder imports.

## Non-Goals For First Product Version

The first product version will not include:

- Public paper-style `Get`/`Insert`/`Put` methods.
- Public batch/prefetch API.
- Inline-only specialized `uint64 -> uint64` public type.
- Namespaces or variable-size allocator mode exposed as separate APIs.
- `Compute` callback API.

These remain possible follow-up features, but adding them now would expand the correctness surface significantly.

## Acceptance Criteria

- Root package has only one public-facing new DLHT file: `dlht.go`.
- DLHT internals live under `internal/dlht`.
- Reference folders `cc/` and `dlht/` are not imported.
- `go test ./...` passes in the root module.
- New DLHT tests cover API parity, resize, high-contention writes/deletes, and race regressions.
- Public docs explain that `DLHT` is a product-grade high-performance map alternative and does not replace `Map`.
- The implementation is maintainable without reading the reference folders.
