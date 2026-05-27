package dlht64

import (
	"math/bits"
	"runtime"
	"unsafe"

	"github.com/puzpuzpuz/xsync/v4/internal/dlht/asm"
	"github.com/puzpuzpuz/xsync/v4/internal/dlht/cpu"
)

// Store upserts a key/value pair. It returns the old value when the key existed.
func (m *Map[V]) Store(key uint64, value V) (V, bool) {
	hash := hashUint64(m.hashConfig.Seed, key)
	idx := m.getActiveIndex()
	newRawValue := uint64(value)

retry:
	pb := idx.getBin(hash)

	h0 := atomicLoadHeader(&pb.Header)

	if h0.getBinState() != BinNoTransfer {
		if h0.getBinState() == BinDoneTransfer {
			idx = idx.getNextIndex()
			goto retry
		}
		cpu.Yield()
		if atomicLoadHeader(&pb.Header).getBinState() == BinInTransfer {
			runtime.Gosched()
		}
		idx = m.getActiveIndex()
		goto retry
	}

	var targetSlot *Slot[V]
	var targetIdx int
	var targetVal uint64
	var found bool

	bitmap := matchPrimaryBucketKeys(pb, h0, key)
	for bitmap != 0 {
		slotIdx := bits.TrailingZeros32(bitmap) >> 1
		targetSlot = pb.slotAt(slotIdx)
		targetIdx = slotIdx
		targetVal = atomicLoadSlotVal(targetSlot)
		found = true
		break
	}

	if !found {
		targetIdx, targetSlot, targetVal = findKeyInLinks(idx, pb, h0, key)
		found = targetIdx != -1
	}

	h1 := atomicLoadHeader(&pb.Header)
	if h0 != h1 {
		goto retry
	}

	if found {
		if asm.DWCAS(unsafe.Pointer(targetSlot), key, targetVal, key, newRawValue) {
			return V(targetVal), true
		}

		for {
			hLoop := atomicLoadHeader(&pb.Header)
			if hLoop.getSlotState(targetIdx) != SlotValid {
				goto retry
			}

			curKey := atomicLoadSlotKey(targetSlot)
			if curKey != key {
				goto retry
			}

			val := atomicLoadSlotVal(targetSlot)
			if atomicLoadHeader(&pb.Header) != hLoop {
				continue
			}
			if atomicLoadSlotVal(targetSlot) != val {
				continue
			}

			if asm.DWCAS(unsafe.Pointer(targetSlot), curKey, val, curKey, newRawValue) {
				return V(val), true
			}
		}
	}

	var zero V

	var slotIndex int
	if im := h0.invalidMask3(); im != 0 {
		slotIndex = bits.TrailingZeros32(im) >> 1
	} else {
		slotIndex = m.chooseInsertSlot(idx, pb)
	}
	if slotIndex < 0 {
		m.triggerResize()
		m.helpResize()
		idx = m.getActiveIndex()
		goto retry
	}

	h2 := h0.setSlotStateAndVersion(slotIndex, SlotTrying)
	if !m.reserveSlot(pb, h0, h2) {
		goto retry
	}

	slot := idx.getSlotByIndex(pb, slotIndex)
	if slot == nil {
		m.unreserveSlot(idx, pb, slotIndex)
		goto retry
	}

	atomicStoreSlotKey(slot, key)
	atomicStoreSlotVal(slot, newRawValue)

	if m.finalizeSlot(pb, h2, slotIndex) {
		return zero, false
	}

retryWithSlot:
	h3 := atomicLoadHeader(&pb.Header)

	if h3.getBinState() != BinNoTransfer {
		if h3.getBinState() == BinDoneTransfer {
			m.unreserveSlot(idx, pb, slotIndex)
			idx = idx.getNextIndex()
			goto retry
		}
		cpu.Yield()
		if atomicLoadHeader(&pb.Header).getBinState() == BinInTransfer {
			runtime.Gosched()
		}
		m.unreserveSlot(idx, pb, slotIndex)
		idx = m.getActiveIndex()
		goto retry
	}

	bitmap2 := matchPrimaryBucketKeys(pb, h3, key)
	for bitmap2 != 0 {
		slotIdx := bits.TrailingZeros32(bitmap2) >> 1
		if slotIdx == slotIndex {
			bitmap2 &^= 1 << (slotIdx * 2)
			continue
		}
		if h4 := atomicLoadHeader(&pb.Header); h3 == h4 {
			m.unreserveSlot(idx, pb, slotIndex)
			goto retry
		}
		goto retryWithSlot
	}

	if _, duplicate := scanLinksForKey(idx, pb, h3, key).get(); duplicate {
		if h4 := atomicLoadHeader(&pb.Header); h3 == h4 {
			m.unreserveSlot(idx, pb, slotIndex)
			goto retry
		}
		goto retryWithSlot
	}

	if m.finalizeSlot(pb, h3, slotIndex) {
		return zero, false
	}

	goto retryWithSlot
}
