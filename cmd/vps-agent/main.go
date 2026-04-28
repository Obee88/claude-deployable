// Command vps-agent is the claude-deployable VPS-side HTTP service.
//
// It runs on the deployment target behind Caddy + TLS and exposes the
// container introspection / restart endpoints documented in PLAN.md
// (M3). It is intentionally NOT introduced in M1 or M2 — the scaffold
// exists so `go build ./...` covers the full tree.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "vps-agent: scaffold only — endpoints introduced in M3 (see PLAN.md)")
	os.Exit(0)
}
