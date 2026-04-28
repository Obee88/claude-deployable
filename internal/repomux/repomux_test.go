package repomux

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLockSerialisesSameKey asserts that two goroutines holding the same
// key cannot run concurrently.  We use a counter that increments and
// decrements while the lock is held; if anything overlaps, the observed
// max exceeds 1.
func TestLockSerialisesSameKey(t *testing.T) {
	var m Mux
	var inFlight, peak int64

	const goroutines = 32
	const iterations = 50

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				unlock := m.Lock("repoA")
				cur := atomic.AddInt64(&inFlight, 1)
				for {
					p := atomic.LoadInt64(&peak)
					if cur <= p || atomic.CompareAndSwapInt64(&peak, p, cur) {
						break
					}
				}
				time.Sleep(50 * time.Microsecond)
				atomic.AddInt64(&inFlight, -1)
				unlock()
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&peak); got != 1 {
		t.Fatalf("expected peak in-flight count of 1, got %d", got)
	}
}

// TestLockDifferentKeysParallel confirms that distinct keys do not block
// each other: two goroutines holding different keys should both make
// progress without one waiting for the other.
func TestLockDifferentKeysParallel(t *testing.T) {
	var m Mux

	unlockA := m.Lock("A")
	defer unlockA()

	done := make(chan struct{})
	go func() {
		unlockB := m.Lock("B")
		unlockB()
		close(done)
	}()

	select {
	case <-done:
		// ok — B was not blocked by A
	case <-time.After(time.Second):
		t.Fatal("Lock(B) blocked behind Lock(A); per-repo mutex isn't actually per-repo")
	}
}
