package xsync_test

import (
	"strconv"
	"sync"
	"testing"

	. "github.com/puzpuzpuz/xsync/v4"
)

func TestDLHT_BasicMapAPI(t *testing.T) {
	m := NewDLHT[string, int](WithDLHTPresize(2))
	m.Store("a", 1)
	m.Store("b", 2)
	m.Store("a", 3)
	if got, ok := m.Load("a"); !ok || got != 3 {
		t.Fatalf("Load(a)=(%d,%v), want (3,true)", got, ok)
	}
	if got, loaded := m.LoadOrStore("a", 4); !loaded || got != 3 {
		t.Fatalf("LoadOrStore existing=(%d,%v), want (3,true)", got, loaded)
	}
	if got, loaded := m.LoadAndStore("a", 5); !loaded || got != 3 {
		t.Fatalf("LoadAndStore existing=(%d,%v), want (3,true)", got, loaded)
	}
	if got, ok := m.Load("a"); !ok || got != 5 {
		t.Fatalf("Load(a) after LoadAndStore=(%d,%v), want (5,true)", got, ok)
	}
	if got, loaded := m.LoadAndDelete("a"); !loaded || got != 5 {
		t.Fatalf("LoadAndDelete(a)=(%d,%v), want (5,true)", got, loaded)
	}
	if _, ok := m.Load("a"); ok {
		t.Fatal("deleted key is still present")
	}
	m.Delete("b")
	if got := m.Size(); got != 0 {
		t.Fatalf("Size()=%d, want 0", got)
	}
}

func TestDLHT_AllAndRange(t *testing.T) {
	m := NewDLHT[string, int]()
	for i := 0; i < 32; i++ {
		m.Store(strconv.Itoa(i), i)
	}
	seen := make(map[string]int)
	m.Range(func(k string, v int) bool {
		seen[k] = v
		return true
	})
	if len(seen) != 32 {
		t.Fatalf("Range saw %d keys, want 32", len(seen))
	}
	count := 0
	for k, v := range m.All() {
		if seen[k] != v {
			t.Fatalf("All yielded (%q,%d), Range had %d", k, v, seen[k])
		}
		count++
	}
	if count != 32 {
		t.Fatalf("All saw %d keys, want 32", count)
	}
}

func TestDLHT_EngineSelection(t *testing.T) {
	mInline := NewDLHT[uint64, uint64]()
	if got := mInline.Stats().Engine; got != "inline64" {
		t.Fatalf("Engine for uint64/uint64 = %q, want inline64", got)
	}

	mGeneric := NewDLHT[string, int]()
	if got := mGeneric.Stats().Engine; got != "generic" {
		t.Fatalf("Engine for string/int = %q, want generic", got)
	}
}

func TestDLHT_InlineSignedKeyRoundTrip(t *testing.T) {
	m := NewDLHT[int64, int64]()
	m.Store(-7, 11)

	v, ok := m.Load(-7)
	if !ok || v != 11 {
		t.Fatalf("Load(-7)=(%d,%v), want (11,true)", v, ok)
	}

	old, loaded := m.LoadAndStore(-7, 22)
	if !loaded || old != 11 {
		t.Fatalf("LoadAndStore(-7,22)=(%d,%v), want (11,true)", old, loaded)
	}

	del, ok := m.LoadAndDelete(-7)
	if !ok || del != 22 {
		t.Fatalf("LoadAndDelete(-7)=(%d,%v), want (22,true)", del, ok)
	}
}

func BenchmarkDLHT_WarmUp(b *testing.B) {
	const entries = 1000
	keys := make([]string, entries)
	for i := 0; i < entries; i++ {
		keys[i] = "k" + strconv.Itoa(i)
	}

	for _, readPct := range []int{100, 99, 90, 75} {
		b.Run("reads="+strconv.Itoa(readPct)+"%", func(b *testing.B) {
			m := NewDLHT[string, int](WithDLHTPresize(entries))
			for i, k := range keys {
				m.Store(k, i)
			}
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					idx := i % entries
					k := keys[idx]
					if i%100 < readPct {
						m.Load(k)
					} else if i&1 == 0 {
						m.Store(k, idx)
					} else {
						m.Delete(k)
					}
					i++
				}
			})
		})
	}
}

func BenchmarkDLHT64_WriteHeavy(b *testing.B) {
	const entries = 100_000
	keys := make([]uint64, entries)
	for i := range entries {
		keys[i] = uint64(i + 1)
	}

	for _, bench := range []struct {
		name string
		run  func(*testing.B, []uint64)
	}{
		{name: "dlht", run: benchmarkDLHT64WriteHeavy},
		{name: "xsync", run: benchmarkXSync64WriteHeavy},
		{name: "sync.Map", run: benchmarkSyncMap64WriteHeavy},
	} {
		b.Run(bench.name, func(b *testing.B) {
			bench.run(b, keys)
		})
	}
}

func BenchmarkDLHT64_InsDelPutHeavy(b *testing.B) {
	const entries = 100_000
	keys := make([]uint64, entries)
	for i := range entries {
		keys[i] = uint64(i + 1)
	}

	b.Run("dlht", func(b *testing.B) {
		m := NewDLHT[uint64, uint64](WithDLHTPresize(len(keys)))
		for _, k := range keys[:entries/2] {
			m.Insert(k, k)
		}
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				k := keys[i%len(keys)]
				switch i % 10 {
				case 0, 1:
					m.Load(k)
				case 2, 3, 4:
					m.Insert(k, k+1)
				case 5, 6:
					m.Delete(k)
				default:
					m.Put(k, k+2)
				}
				i++
			}
		})
	})

	b.Run("xsync", func(b *testing.B) {
		m := NewMap[uint64, uint64](WithPresize(len(keys)))
		for _, k := range keys[:entries/2] {
			m.LoadOrStore(k, k)
		}
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				k := keys[i%len(keys)]
				switch i % 10 {
				case 0, 1:
					m.Load(k)
				case 2, 3, 4:
					m.LoadOrStore(k, k+1)
				case 5, 6:
					m.Delete(k)
				default:
					m.LoadAndStore(k, k+2)
				}
				i++
			}
		})
	})
}

func benchmarkDLHT64WriteHeavy(b *testing.B, keys []uint64) {
	m := NewDLHT[uint64, uint64](WithDLHTPresize(len(keys)))
	for _, k := range keys {
		m.Store(k, k)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			k := keys[i%len(keys)]
			switch i % 10 {
			case 0:
				m.Load(k)
			case 1, 2, 3, 4:
				m.Store(k, k+1)
			case 5, 6, 7, 8:
				m.Delete(k)
			default:
				m.LoadOrStore(k, k)
			}
			i++
		}
	})
}

func benchmarkXSync64WriteHeavy(b *testing.B, keys []uint64) {
	m := NewMap[uint64, uint64](WithPresize(len(keys)))
	for _, k := range keys {
		m.Store(k, k)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			k := keys[i%len(keys)]
			switch i % 10 {
			case 0:
				m.Load(k)
			case 1, 2, 3, 4:
				m.Store(k, k+1)
			case 5, 6, 7, 8:
				m.Delete(k)
			default:
				m.LoadOrStore(k, k)
			}
			i++
		}
	})
}

func benchmarkSyncMap64WriteHeavy(b *testing.B, keys []uint64) {
	var m sync.Map
	for _, k := range keys {
		m.Store(k, k)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			k := keys[i%len(keys)]
			switch i % 10 {
			case 0:
				m.Load(k)
			case 1, 2, 3, 4:
				m.Store(k, k+1)
			case 5, 6, 7, 8:
				m.Delete(k)
			default:
				m.LoadOrStore(k, k)
			}
			i++
		}
	})
}
