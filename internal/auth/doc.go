// Package auth holds the bearer-token middleware used by the VPS
// agent (M3). The bridge itself does NOT authenticate callers —
// OS-user isolation and the repo allowlist are its boundaries
// (see ADR-0001 in PLAN.md).
//
// Scaffold only in M1 task 8; implementation arrives in M3.
package auth
