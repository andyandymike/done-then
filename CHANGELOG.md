# Changelog

All notable changes to DoneThen will be documented in this file.

The project follows [Semantic Versioning](https://semver.org/). It has not yet
published a tagged release.

## [Unreleased]

### Added

- Windows-first `codex exec` supervisor with dry-run as the default.
- Strict structured completion envelope and fail-closed completion policy.
- Optional argv-only external verifier.
- Atomic local job records, redacted lifecycle events, cancellation markers,
  orphan recovery, and cross-session power-job serialization.
- Cancellable Windows shutdown backend with a fake backend for automated tests.
- `run`, `cancel`, and `status` commands.
- Windows CI and alpha release packaging workflow.
- Inspectable Windows release builds that retain Go symbols and embed the
  source Git revision.
- Observe-only Codex plugin package with a Skill, stdio MCP server, and
  plugin-owned lifecycle Hooks.
- Typed plugin `arm`, `finish`, `pause`, `cancel`, and `status` operations.
- Atomic plugin job records, cross-process state locking, session/turn binding,
  idempotent Hook event keys, and stale-evidence invalidation.
- CLI status and cancellation recovery for plugin jobs.
- Reversible local plugin development install, reinstall, status, and uninstall
  tooling backed by a disposable ignored marketplace.
- Observe-only live-smoke tooling for Hook configuration hashes, dry-run job
  state, lifecycle ordering, redacted events, and the power-action boundary.

### Security

- Dangerous Codex flags are rejected unless explicitly allowed.
- Model output cannot select the action executable or action argv.
- Automated tests never invoke a real power action.
- Plugin execute mode is rejected before job creation until authoritative Hook
  and active-task inventory gates exist.
- Observer Hooks emit no stdout, do not steer Codex, and cannot reach the
  action backend.

### Known limitations

- Source pre-alpha only; no real Windows countdown-and-cancel acceptance record
  has been completed for a public release.
- Windows `amd64` is the only release target.
- Release binaries are not code-signed.
- The plugin can observe its original Codex task, but is dry-run only.
- The local development scripts have automated dry-run coverage, but a live
  installed-plugin and trusted-Hook smoke record has not yet been completed.
- Pre-registered plugin verifier profiles, authoritative effective-Hook
  inventory, active-task inventory, and the final action supervisor are not
  implemented.
- Completion without an external verifier remains an agent self-report and
  requires explicit opt-in in execute mode.

[Unreleased]: https://github.com/andyandymike/done-then/commits/main
