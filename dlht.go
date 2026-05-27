package xsync

import (
	"iter"
	"reflect"
	"unsafe"

	internaldlht "github.com/puzpuzpuz/xsync/v4/internal/dlht"
	internaldlht64 "github.com/puzpuzpuz/xsync/v4/internal/dlht64"
)

const (
	dlhtEngineGeneric uint8 = iota
	dlhtEngineInline64
	dlhtEngineInlineCodec
)

// DLHT is a concurrent hash table with lock-free reads and cooperative resize.
//
// Integer key/value pairs use the inline uint64 engine; all other pairs fall
// back to the generic allocator engine while keeping the same Map-like API.
//
// TODO(dlht): add a batch load API with optional arch-specific prefetch. The
// DLHT paper uses batching plus software prefetch to hide memory latency on
// memory-resident tables, but Go has no portable prefetch primitive today.
//
// TODO(dlht): add specialized string-key engines before the generic fallback.
// String keys are common enough in Go workloads to deserve a non-allocator
// fast path once the integer engine is stable.
//
// TODO(dlht): add a DLHTSet variant for key-only workloads. The DLHT paper has
// a HashSet mode, and it should be cheaper than storing a dummy value.
type DLHT[K comparable, V any] struct {
	mode uint8

	generic *internaldlht.Map[K, V]
	inline  *internaldlht64.Map[uint64]

	keyc   bitCodec[K]
	valuec bitCodec[V]
}

// DLHTConfig defines configurable DLHT options.
type DLHTConfig struct {
	sizeHint int
}

// DLHTStats is an approximate snapshot of a DLHT state.
type DLHTStats struct {
	Engine       string
	Bins         uint64
	Links        uint64
	LinkCapacity uint64
	Size         uint64
	Capacity     uint64
	LoadFactor   float64
	Resizing     bool
}

type bitCodec[T any] struct {
	toBits   func(T) uint64
	fromBits func(uint64) T
}

// WithDLHTPresize configures a new DLHT with capacity for sizeHint entries.
func WithDLHTPresize(sizeHint int) func(*DLHTConfig) {
	return func(c *DLHTConfig) {
		if sizeHint > 0 {
			c.sizeHint = sizeHint
		}
	}
}

// NewDLHT creates a new DLHT instance configured with the given options.
func NewDLHT[K comparable, V any](options ...func(*DLHTConfig)) *DLHT[K, V] {
	cfg := &DLHTConfig{sizeHint: 16}
	for _, o := range options {
		o(cfg)
	}

	m := &DLHT[K, V]{}
	m.init(cfg.sizeHint)
	return m
}

func (m *DLHT[K, V]) init(sizeHint int) {
	kt := reflect.TypeFor[K]()
	vt := reflect.TypeFor[V]()

	if isIntegerKind(kt.Kind()) && isIntegerKind(vt.Kind()) && kt.Size() == 8 && vt.Size() == 8 {
		m.mode = dlhtEngineInline64
		m.inline = internaldlht64.New[uint64](internaldlht64.Options{InitialSize: uint64(sizeHint)})
		return
	}

	kc, kok := integerCodec[K](kt)
	vc, vok := integerCodec[V](vt)
	if kok && vok {
		m.mode = dlhtEngineInlineCodec
		m.inline = internaldlht64.New[uint64](internaldlht64.Options{InitialSize: uint64(sizeHint)})
		m.keyc = kc
		m.valuec = vc
		return
	}

	m.mode = dlhtEngineGeneric
	m.generic = internaldlht.New[K, V](internaldlht.Options{InitialSize: uint64(sizeHint)})
}

// Load returns the value stored in the map for a key.
func (m *DLHT[K, V]) Load(key K) (value V, ok bool) {
	switch m.mode {
	case dlhtEngineInline64:
		v, ok := m.inline.Get(dlhtBits(key))
		if !ok {
			var zero V
			return zero, false
		}
		return dlhtFromBits[V](v), true
	case dlhtEngineInlineCodec:
		v, ok := m.inline.Get(m.keyc.toBits(key))
		if !ok {
			var zero V
			return zero, false
		}
		return m.valuec.fromBits(v), true
	default:
		return m.generic.Get(key)
	}
}

// Insert stores key/value only if key is not present.
// Returns (existingValue, inserted).
func (m *DLHT[K, V]) Insert(key K, value V) (V, bool) {
	switch m.mode {
	case dlhtEngineInline64:
		existing, inserted := m.inline.Insert(dlhtBits(key), dlhtBits(value))
		if inserted {
			var zero V
			return zero, true
		}
		return dlhtFromBits[V](existing), false
	case dlhtEngineInlineCodec:
		existing, inserted := m.inline.Insert(m.keyc.toBits(key), m.valuec.toBits(value))
		if inserted {
			var zero V
			return zero, true
		}
		return m.valuec.fromBits(existing), false
	default:
		return m.generic.Insert(key, value)
	}
}

// Put updates key/value only if key is present.
// Returns (oldValue, updated).
func (m *DLHT[K, V]) Put(key K, value V) (V, bool) {
	switch m.mode {
	case dlhtEngineInline64:
		old, loaded := m.inline.Put(dlhtBits(key), dlhtBits(value))
		if !loaded {
			var zero V
			return zero, false
		}
		return dlhtFromBits[V](old), true
	case dlhtEngineInlineCodec:
		old, loaded := m.inline.Put(m.keyc.toBits(key), m.valuec.toBits(value))
		if !loaded {
			var zero V
			return zero, false
		}
		return m.valuec.fromBits(old), true
	default:
		return m.generic.Put(key, value)
	}
}

// Store sets the value for a key.
func (m *DLHT[K, V]) Store(key K, value V) {
	switch m.mode {
	case dlhtEngineInline64:
		_, _ = m.inline.Store(dlhtBits(key), dlhtBits(value))
	case dlhtEngineInlineCodec:
		_, _ = m.inline.Store(m.keyc.toBits(key), m.valuec.toBits(value))
	default:
		for {
			if _, ok := m.generic.Put(key, value); ok {
				return
			}
			if _, inserted := m.generic.Insert(key, value); inserted {
				return
			}
		}
	}
}

// LoadOrStore returns the existing value for the key if present.
func (m *DLHT[K, V]) LoadOrStore(key K, value V) (actual V, loaded bool) {
	switch m.mode {
	case dlhtEngineInline64:
		existing, inserted := m.inline.Insert(dlhtBits(key), dlhtBits(value))
		if inserted {
			return value, false
		}
		return dlhtFromBits[V](existing), true
	case dlhtEngineInlineCodec:
		existing, inserted := m.inline.Insert(m.keyc.toBits(key), m.valuec.toBits(value))
		if inserted {
			return value, false
		}
		return m.valuec.fromBits(existing), true
	default:
		existing, inserted := m.generic.Insert(key, value)
		if inserted {
			return value, false
		}
		return existing, true
	}
}

// LoadAndStore stores value and returns the previous value if present.
func (m *DLHT[K, V]) LoadAndStore(key K, value V) (actual V, loaded bool) {
	switch m.mode {
	case dlhtEngineInline64:
		old, loaded := m.inline.Store(dlhtBits(key), dlhtBits(value))
		if loaded {
			return dlhtFromBits[V](old), true
		}
		return value, false
	case dlhtEngineInlineCodec:
		old, loaded := m.inline.Store(m.keyc.toBits(key), m.valuec.toBits(value))
		if loaded {
			return m.valuec.fromBits(old), true
		}
		return value, false
	default:
		for {
			old, loaded := m.generic.Put(key, value)
			if loaded {
				return old, true
			}
			if _, inserted := m.generic.Insert(key, value); inserted {
				return value, false
			}
		}
	}
}

// LoadAndDelete deletes the value for a key and returns the previous value if present.
func (m *DLHT[K, V]) LoadAndDelete(key K) (value V, loaded bool) {
	switch m.mode {
	case dlhtEngineInline64:
		old, loaded := m.inline.DeleteAndGet(dlhtBits(key))
		if !loaded {
			var zero V
			return zero, false
		}
		return dlhtFromBits[V](old), true
	case dlhtEngineInlineCodec:
		old, loaded := m.inline.DeleteAndGet(m.keyc.toBits(key))
		if !loaded {
			var zero V
			return zero, false
		}
		return m.valuec.fromBits(old), true
	default:
		return m.generic.Delete(key)
	}
}

// Delete deletes the value for a key.
func (m *DLHT[K, V]) Delete(key K) {
	switch m.mode {
	case dlhtEngineInline64:
		_ = m.inline.Delete(dlhtBits(key))
	case dlhtEngineInlineCodec:
		_ = m.inline.Delete(m.keyc.toBits(key))
	default:
		_, _ = m.generic.Delete(key)
	}
}

// Range calls f for each key and value present in the map.
func (m *DLHT[K, V]) Range(f func(key K, value V) bool) {
	switch m.mode {
	case dlhtEngineInline64:
		m.inline.Range(func(k uint64, v uint64) bool {
			return f(dlhtFromBits[K](k), dlhtFromBits[V](v))
		})
	case dlhtEngineInlineCodec:
		m.inline.Range(func(k uint64, v uint64) bool {
			return f(m.keyc.fromBits(k), m.valuec.fromBits(v))
		})
	default:
		m.generic.Range(f)
	}
}

// All returns an iterator over every key/value pair.
//
// TODO(dlht): expose explicit weak and strong snapshot iterator modes. The
// current iterator is intentionally weak; strong snapshots need a separate API
// because they will have different memory and latency costs.
func (m *DLHT[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		m.Range(yield)
	}
}

// Size returns the approximate number of entries.
func (m *DLHT[K, V]) Size() int {
	switch m.mode {
	case dlhtEngineInline64, dlhtEngineInlineCodec:
		return int(m.inline.Size())
	default:
		return int(m.generic.Size())
	}
}

// Stats returns an approximate snapshot of table occupancy and resize state.
func (m *DLHT[K, V]) Stats() DLHTStats {
	switch m.mode {
	case dlhtEngineInline64:
		return mapInlineDLHTStats(m.inline.Stats(), "inline64")
	case dlhtEngineInlineCodec:
		return mapInlineDLHTStats(m.inline.Stats(), "inline64-codec")
	default:
		return mapGenericDLHTStats(m.generic.Stats(), "generic")
	}
}

func mapInlineDLHTStats(s internaldlht64.Stats, engine string) DLHTStats {
	return DLHTStats{
		Engine:       engine,
		Bins:         s.Bins,
		Links:        s.Links,
		LinkCapacity: s.LinkCapacity,
		Size:         s.Size,
		Capacity:     s.Capacity,
		LoadFactor:   s.LoadFactor,
		Resizing:     s.Resizing,
	}
}

func mapGenericDLHTStats(s internaldlht.Stats, engine string) DLHTStats {
	return DLHTStats{
		Engine:       engine,
		Bins:         s.Bins,
		Links:        s.Links,
		LinkCapacity: s.LinkCapacity,
		Size:         s.Size,
		Capacity:     s.Capacity,
		LoadFactor:   s.LoadFactor,
		Resizing:     s.Resizing,
	}
}

func isIntegerKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	default:
		return false
	}
}

func integerCodec[T any](t reflect.Type) (bitCodec[T], bool) {
	if !isIntegerKind(t.Kind()) {
		return bitCodec[T]{}, false
	}

	switch t.Size() {
	case 1:
		return bitCodec[T]{
			toBits: func(v T) uint64 { return uint64(*(*uint8)(unsafe.Pointer(&v))) },
			fromBits: func(b uint64) T {
				var v T
				*(*uint8)(unsafe.Pointer(&v)) = uint8(b)
				return v
			},
		}, true
	case 2:
		return bitCodec[T]{
			toBits: func(v T) uint64 { return uint64(*(*uint16)(unsafe.Pointer(&v))) },
			fromBits: func(b uint64) T {
				var v T
				*(*uint16)(unsafe.Pointer(&v)) = uint16(b)
				return v
			},
		}, true
	case 4:
		return bitCodec[T]{
			toBits: func(v T) uint64 { return uint64(*(*uint32)(unsafe.Pointer(&v))) },
			fromBits: func(b uint64) T {
				var v T
				*(*uint32)(unsafe.Pointer(&v)) = uint32(b)
				return v
			},
		}, true
	case 8:
		return bitCodec[T]{
			toBits:   dlhtBits[T],
			fromBits: dlhtFromBits[T],
		}, true
	default:
		return bitCodec[T]{}, false
	}
}

func dlhtBits[T any](v T) uint64 {
	return *(*uint64)(unsafe.Pointer(&v))
}

func dlhtFromBits[T any](b uint64) T {
	return *(*T)(unsafe.Pointer(&b))
}
