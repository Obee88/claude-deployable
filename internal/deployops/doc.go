// Package deployops holds the compose-pull + compose-up logic the
// VPS agent would run on demand (M3). Deferred in favour of
// container-restart-only semantics per the current PLAN.md decisions
// table; kept as a scaffold for a future `/deploy` endpoint.
//
// Scaffold only in M1 task 8.
package deployops
