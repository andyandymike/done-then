# Changelog

All notable changes to DoneThen will be documented in this file.

The project follows [Semantic Versioning](https://semver.org/). It has not yet
published a tagged release.

## [Unreleased]

### Added

- `after_all_stop` multi-session barriers for 2-16 explicit Codex session ids:
  one redacted schema-v4 Job reserves every target, records independent
  turn/workspace Stop state, and completes one dry-run only after all targets
  stop. Real Stop-based execution remains authority-gated.
- Fail-closed barrier revocation markers, live target-index revalidation, and a
  detached cancel-only recovery worker that can never schedule a power action.
- Default `after_stop` plugin policy that binds an arm to the current Codex
  session, turn, and workspace and records a normal `Stop` without claiming
  task success.
- Explicit acknowledgement for after-stop execute, a minimum two-minute
  cancellation window, cancellation on later prompts and Stop-Hook
  continuations, and dry-run completion without `finish` or a verifier.
- Per-policy build, backend-family, backend-preflight, and execute-readiness
  reporting for `after_stop`, `after_all_stop`, and `verified_success`.
- Owner-controlled create-once recovery records now preserve the pre-call
  cancellation receipt, the no-retry Schedule call-start boundary, the
  backend-returned receipt, and a positive inert resolution independently of
  the mutable plugin job projection.

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
- Stop-based execute is rejected before arm while no trusted final Hook
  arbitration provider exists. The supervisor independently enforces the same
  gate before Schedule, after an accepted Schedule call, and during countdown
  monitoring so persisted or hand-crafted state cannot bypass it.
- Verified-success execution has the same independent supervisor-side grant
  boundary; editable job state and App Server snapshots are evidence, not
  sufficient power authority.
- Hook events mutate only the exact immutable binding captured before the
  event update; a Stop captured before a concurrent arm cannot be credited to
  the new job.
- Production Hooks use bounded retries for the short global-state lock, so a
  burst of multi-session Stop events gets the same contention handling as the
  concurrency contract tests while staying inside the Hook timeout budget.
- Countdown monitoring uses its retained receipt to attempt emergency
  cancellation before returning on state-load or receipt-consistency failure.
- CLI, supervisor, MCP cancellation, and the detached cancel worker can settle
  an independent recovery record after cancellation is positively confirmed;
  corrupt or missing mutable job JSON no longer removes the only cancellation
  handle.
- Durable revocation markers now cover all plugin policies and remain until a
  post-intent cancel worker confirms the external action is inert.
- Experimental verified-success execute remains unavailable until a stable
  authoritative same-host attachment exists.
- Manual cancellation is persisted before backend cancellation; an in-flight
  scheduling outcome remains unresolved until a confirmed cancellation can
  safely terminalize it.
- Observer Hooks emit no stdout, do not steer Codex, and never schedule a power
  action; scheduling remains in the detached supervisor.
- Multi-session target and controller ids are persisted only as SHA-256
  identities; status exposes unique short references rather than raw ids.
- Unknown schedule outcomes retain a cancellable recovery intent and are never
  retried automatically.

### Known limitations

- Source pre-alpha only; no real Windows countdown-and-cancel acceptance record
  has been completed for a public release.
- Portable release targets exist for Windows, Linux, and macOS on `amd64` and
  `arm64`, but only Windows `amd64` currently reaches C2 and Linux `amd64`
  reaches C1. Cross-build-only targets remain C0 candidates until their own
  architecture test evidence exists.
- Windows binaries are not code-signed, and macOS binaries are neither signed
  nor notarized.
- Public plugin execute is disabled. Stop policies are waiting for trusted
  final Hook arbitration; verified-success is waiting for authoritative
  same-host attachment. Real cancel and power-off acceptance also remains
  incomplete.
- The local development scripts have automated dry-run coverage, but a live
  installed-plugin and trusted-Hook smoke record has not yet been completed.
- Linux helper installation and real-host cancel/power-off acceptance are not
  complete. A signed/notarized macOS privileged helper is not delivered.
- Completion without an external verifier remains an agent self-report and
  requires explicit opt-in in execute mode.

[Unreleased]: https://github.com/andyandymike/done-then/commits/main
