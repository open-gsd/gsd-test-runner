// Package config loads Local Engine configuration: target OSes, Benches
// registry, GHCR credentials, opt-in flags (--allow-skip-os, --sequential).
//
// Configuration file layout follows the Bench naming established in
// docs/adr/0007-dev-workstation-vs-bench-vocabulary.md
// (e.g., ~/.config/gsd-test/benches/linux).
package config
