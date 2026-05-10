package xsync

import (
	"iter"

	internaldlht "github.com/puzpuzpuz/xsync/v4/internal/dlht"
)

// DLHT is a concurrent hash table with lock-free reads and cooperative resize.
//
// It intentionally exposes a simple Map-like API and does not expose Compute.
type DLHT[K comparable, V any] struct {
	m *internaldlht.Map[K, V]
}

// DLHTConfig defines configurable DLHT options.
type DLHTConfig struct {
	sizeHint int
}

// DLHTStats is an approximate snapshot of a DLHT state.
type DLHTStats struct {
	Bins         uint64
	Links        uint64
	LinkCapacity uint64
	Size         uint64
	Capacity     uint64
	LoadFactor   float64
	Resizing     bool
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
	return &DLHT[K, V]{
		m: internaldlht.New[K, V](internaldlht.Options{InitialSize: uint64(cfg.sizeHint)}),
	}
}

// Load returns the value stored in the map for a key.
func (m *DLHT[K, V]) Load(key K) (value V, ok bool) {
	return m.m.Get(key)
}

// Store sets the value for a key.
func (m *DLHT[K, V]) Store(key K, value V) {
	for {
		if _, ok := m.m.Put(key, value); ok {
			return
		}
		if _, inserted := m.m.Insert(key, value); inserted {
			return
		}
	}
}

// LoadOrStore returns the existing value for the key if present.
func (m *DLHT[K, V]) LoadOrStore(key K, value V) (actual V, loaded bool) {
	if existing, inserted := m.m.Insert(key, value); inserted {
		return value, false
	} else {
		return existing, true
	}
}

// LoadAndStore stores value and returns the previous value if present.
func (m *DLHT[K, V]) LoadAndStore(key K, value V) (actual V, loaded bool) {
	for {
		if old, updated := m.m.Put(key, value); updated {
			return old, true
		}
		if _, inserted := m.m.Insert(key, value); inserted {
			return value, false
		}
	}
}

// LoadAndDelete deletes the value for a key and returns the previous value if present.
func (m *DLHT[K, V]) LoadAndDelete(key K) (value V, loaded bool) {
	return m.m.Delete(key)
}

// Delete deletes the value for a key.
func (m *DLHT[K, V]) Delete(key K) {
	_, _ = m.m.Delete(key)
}

// Range calls f for each key and value present in the map.
func (m *DLHT[K, V]) Range(f func(key K, value V) bool) {
	m.m.Range(f)
}

// All returns an iterator over every key/value pair.
func (m *DLHT[K, V]) All() iter.Seq2[K, V] {
	return m.m.All()
}

// Size returns the approximate number of entries.
func (m *DLHT[K, V]) Size() int {
	return int(m.m.Size())
}

// Stats returns an approximate snapshot of table occupancy and resize state.
func (m *DLHT[K, V]) Stats() DLHTStats {
	s := m.m.Stats()
	return DLHTStats{
		Bins:         s.Bins,
		Links:        s.Links,
		LinkCapacity: s.LinkCapacity,
		Size:         s.Size,
		Capacity:     s.Capacity,
		LoadFactor:   s.LoadFactor,
		Resizing:     s.Resizing,
	}
}
