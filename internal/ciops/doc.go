// Package ciops is a thin GitHub REST API client for the bridge's
// CI feedback loop: listing runs, waiting for a run to complete,
// downloading workflow logs, and filtering to failed-step output.
//
// Talks directly to api.github.com from the host (the Cowork sandbox
// cannot reach api.github.com — see ADR-0001 in PLAN.md). Auth is a
// fine-grained PAT loaded from the bridge env file.
//
// Scaffold only in M1 task 8; `ci_wait_for_run` / `ci_logs` arrive
// in M2.
package ciops
