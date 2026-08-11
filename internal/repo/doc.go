// Package repo hosts the repository governance tests for Koyori IDE: release
// version and platform metadata consistency, release workflow contracts,
// README honest claim boundaries, and repository hygiene (.gitignore / NOTICE
// / license inventory). The tests are file-based — they assert on repository
// content relative to the repo root (../../) and run in every CI go-test step.
//
// These tests are the "source of truth police": they keep README claims
// honest (V/S/U verification boundaries), keep release metadata synchronized
// with VERSION, and refuse stale license inventories. When renaming or
// rebranding the project, update this package's constants first.
package repo
