// Package repomux provides a per-repo mutex used by the bridge to serialise
// any tool call that mutates a working tree (pull, branch, commit, push,
// reset, abort). Read-only tools (status, ci_*) intentionally do not take
// the lock so they don't block while a push is in flight.
//
// See PLAN.md "Concurrency" — per-repo mutex around state-mutating tools.
package repomux

import "sync"

// Mux owns a sync.Mutex per repo key.  Keys are caller-defined; the bridge
// uses the absolute, symlink-resolved path of each allowlisted repo so two
// argument spellings of the same repo serialise correctly.
//
// The zero value is ready to use.
type Mux struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Lock acquires the mutex for the given key, creating it on first use.
// It returns an unlock func; call it (typically with defer) to release.
//
// Lock blocks until the prior holder releases. There is no timeout — the
// caller's context is the timeout. The bridge surfaces ctx-cancellation as
// a structured error before it ever reaches Lock.
func (m *Mux) Lock(key string) (unlock func()) {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = make(map[string]*sync.Mutex)
	}
	rm, ok := m.locks[key]
	if !ok {
		rm = &sync.Mutex{}
		m.locks[key] = rm
	}
	m.mu.Unlock()

	rm.Lock()
	return rm.Unlock
}
