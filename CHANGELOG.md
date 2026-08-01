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
- `run`, `cancel`, `status`, read-only `reconcile`, `doctor`, `policy`, and
  `verifier` commands.
- Windows, Linux, and macOS CI, race-sensitive tests, six-architecture
  cross-builds, capability-bound release packaging, and deterministic SPDX 2.3
  SBOMs bound to each archive.
- Inspectable Windows release builds that retain Go symbols and embed the
  source Git revision.
- Default-dry-run Codex plugin package with a Skill, stdio MCP server, and
  plugin-owned lifecycle Hooks.
- Typed plugin `arm`, `finish`, `pause`, `cancel`, and `status` operations.
- Atomic plugin job records, cross-process state locking, session/turn binding,
  idempotent Hook event keys, and stale-evidence invalidation.
- CLI status and cancellation recovery for plugin jobs.
- Reversible local plugin development install, reinstall, status, and uninstall
  tooling backed by a disposable ignored marketplace.
- Dry-run live-smoke tooling for Hook configuration hashes, job
  state, lifecycle ordering, redacted events, and the power-action boundary.
- Initialized Codex App Server transport, fail-closed HostAuthority snapshots,
  Hook coexistence policy, task/child/background inventory, and live turn/Hook
  event tracking.
- Automatically detached one-shot plugin supervisor with stable H1/H2/H3
  policy snapshots, final quiescence, machine locking, monitored countdown,
  late-wake cancellation, process ownership, and cancel-race rollback.
- Owner-controlled, fingerprinted verifier profiles and hash-bound local power
  policy capture.
- Receipt schema v2, action-intent recovery handles, no-retry reconciliation,
  and explicit scheduled, execution-unverified, and executed-confirmed states.
- Linux systemd backend and root helper with fixed operations, peer identity,
  job-specific transient units, plan-first installation, and container/WSL
  rejection. Reconciliation releases stale inactive helper state without ever
  retrying the power action. macOS builds retain an explicit safe unsupported
  backend.

### Security

- Dangerous Codex flags are rejected unless explicitly allowed.
- Model output cannot select the action executable or action argv.
- Automated tests never invoke a real power action.
- Public Plugin execute is unavailable even after policy capture until a stable
  authoritative same-host attachment exists. Missing Hook, task, verifier,
  privilege, cancellation, or backend evidence fails closed.
- Manual cancellation is persisted before backend cancellation; an in-flight
  scheduling outcome remains unresolved until a confirmed cancellation can
  safely terminalize it.
- Observer Hooks emit no stdout, do not steer Codex, and cannot reach the
  action backend.
- Unknown schedule outcomes retain a cancellable recovery intent and are never
  retried automatically.

### Known limitations

- Source pre-alpha only; no real Windows countdown-and-cancel acceptance record
  has been completed for a public release.
- Portable release targets exist for Windows, Linux, and macOS on `amd64` and
  `arm64`, but only Windows `amd64` currently reaches C2; all others are C1.
- Windows binaries are not code-signed, and macOS binaries are neither signed
  nor notarized.
- Conditional Plugin execute code exists, but the public MCP server deliberately
  keeps it unavailable while stable authoritative attachment to the current
  Codex host and real power acceptance remain blockers.
- The local development scripts have automated dry-run coverage, but a live
  installed-plugin and trusted-Hook smoke record has not yet been completed.
- Linux helper installation and real-host cancel/power-off acceptance are not
  complete. A signed/notarized macOS privileged helper is not delivered.
- Completion without an external verifier remains an agent self-report and
  requires explicit opt-in in execute mode.

[Unreleased]: https://github.com/andyandymike/done-then/commits/main
