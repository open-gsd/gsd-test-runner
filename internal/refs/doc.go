// Package refs resolves user-supplied git refs (branch names, tags,
// SHA prefixes, "HEAD") to full commit SHAs by shelling out to
// `git rev-parse <ref>^{commit}`.
//
// Resolution lives in this package, not in internal/worktree, so the
// worktree module can take SHAs only — see
// docs/adr/0010-prref-resolution-contract.md.
package refs
