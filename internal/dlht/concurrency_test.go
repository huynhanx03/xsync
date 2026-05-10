package dlht

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConcurrentLoadOrStoreSingleWinner(t *testing.T) {
	m := New[string, int](Options{InitialSize: 4})
	const goroutines = 64

	var winners atomic.Int32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(v int) {
			defer wg.Done()
            if _, inserted := m.Insert("shared", v); inserted {
                winners.Add(1)
            }
		}(i)
	}
	wg.Wait()

	if got := winners.Load(); got != 1 {
		t.Fatalf("winners=%d, want 1", got)
	}
	if _, ok := m.Get("shared"); !ok {
		t.Fatal("shared key missing")
	}
}

func TestConcurrentMixedOperations(t *testing.T) {
	m := New[int, int](Options{InitialSize: 16})
	workers := max(runtime.GOMAXPROCS(0), 4)
	const opsPerWorker = 500

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := range workers {
		go func(worker int) {
			defer wg.Done()
			base := worker * opsPerWorker
			for i := range opsPerWorker {
				key := base + i
				m.Insert(key, key)
				if old, ok := m.Put(key, key*2); !ok || old != key {
					t.Errorf("Put(%d)=(%d,%v), want (%d,true)", key, old, ok, key)
				}
				if i%3 == 0 {
					if _, ok := m.Delete(key); !ok {
						t.Errorf("Delete(%d) unexpectedly missed key", key)
					}
				}
			}
		}(w)
	}
	wg.Wait()
}

func TestIssueStylePutDeleteRace(t *testing.T) {
	rounds := 20000
	if testing.Short() {
		rounds = 3000
	}

	const (
		key      = "hot"
		oldValue = -1
	)

	m := New[string, int](Options{InitialSize: 4})
	for round := range rounds {
		if _, ok := m.Put(key, oldValue); !ok {
			if _, inserted := m.Insert(key, oldValue); !inserted {
				t.Fatalf("round %d: failed to seed key", round)
			}
		}

		putValue := round*2 + 1
		insertValue := round*2 + 2

		start := make(chan struct{})
		var wg sync.WaitGroup
		var putOld, delOld int
		var putOK, delOK bool

		wg.Add(3)
		go func() {
			defer wg.Done()
			<-start
			putOld, putOK = m.Put(key, putValue)
		}()
		go func() {
			defer wg.Done()
			<-start
			delOld, delOK = m.Delete(key)
		}()
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 32; i++ {
				if _, ok := m.Insert(key, insertValue); ok {
					return
				}
				runtime.Gosched()
			}
		}()

		close(start)
		wg.Wait()

		if putOK && delOK && putOld == oldValue && delOld == oldValue {
			t.Fatalf("round %d: forbidden outcome putOld=%d delOld=%d", round, putOld, delOld)
		}
	}
}
