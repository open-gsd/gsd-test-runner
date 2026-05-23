// Command gsd-test is the Dev Workstation entry point for the Local Engine.
//
// The orchestrator shape it will grow into is described in
// docs/adr/0009-local-engine-top-level-orchestration.md. This file is
// currently a compiling placeholder.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "gsd-test: skeleton — not yet implemented (see docs/adr/0009)")
	os.Exit(3) // ADR-0009: exit code 3 = Local Engine could not start
}
