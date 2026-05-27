//go:build !race

package dlht64

func atomicLoadSlotKey[V Integer](slot *Slot[V]) uint64 {
	return slot.Key
}

func atomicStoreSlotKey[V Integer](slot *Slot[V], key uint64) {
	slot.Key = key
}
