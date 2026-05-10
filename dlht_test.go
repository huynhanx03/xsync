package xsync_test

import (
	"strconv"
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
