//go:build race && (amd64 || arm64)

package asm

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

type slotWords struct {
	Key uint64
	Val uint64
}

type slotPtrWords struct {
	Key uint64
	Val unsafe.Pointer
}

const dwcasStripeCount = 1 << 12

var dwcasLocks [dwcasStripeCount]sync.Mutex

func dwcasLockFor(slot unsafe.Pointer) *sync.Mutex {
	u := uintptr(slot) >> 4 // slot is 16-byte aligned
	return &dwcasLocks[u&(dwcasStripeCount-1)]
}

// DWCAS atomically updates a 16-byte slot under race build.
// It serializes by striped lock so the race detector sees synchronization.
func DWCAS(slot unsafe.Pointer, oldKey, oldVal, newKey, newVal uint64) bool {
	mu := dwcasLockFor(slot)
	mu.Lock()
	defer mu.Unlock()

	w := (*slotWords)(slot)
	curKey := atomic.LoadUint64(&w.Key)
	curVal := atomic.LoadUint64(&w.Val)
	if curKey != oldKey || curVal != oldVal {
		return false
	}
	atomic.StoreUint64(&w.Key, newKey)
	atomic.StoreUint64(&w.Val, newVal)
	return true
}

// DWCASPtr atomically updates a 16-byte slot under race build.
// It serializes by striped lock so the race detector sees synchronization.
func DWCASPtr(slot unsafe.Pointer, oldKey uint64, oldVal unsafe.Pointer, newKey uint64, newVal unsafe.Pointer) bool {
	mu := dwcasLockFor(slot)
	mu.Lock()
	defer mu.Unlock()

	w := (*slotPtrWords)(slot)
	curKey := atomic.LoadUint64(&w.Key)
	curVal := atomic.LoadPointer(&w.Val)
	if curKey != oldKey || curVal != oldVal {
		return false
	}
	atomic.StoreUint64(&w.Key, newKey)
	atomic.StorePointer(&w.Val, newVal)
	return true
}
