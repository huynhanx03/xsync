//go:build race

package dlht64

import "sync/atomic"

func atomicLoadSlotKey[V Integer](slot *Slot[V]) uint64 {
	return atomic.LoadUint64(&slot.Key)
}

func atomicStoreSlotKey[V Integer](slot *Slot[V], key uint64) {
	atomic.StoreUint64(&slot.Key, key)
}
