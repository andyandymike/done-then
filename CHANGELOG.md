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

### Security

- Dangerous Codex flags are rejected unless explicitly allowed.
- Model output cannot select the action executable or action argv.
- Automated tests never invoke a real power action.

### Known limitations

- Source pre-alpha only; no real Windows countdown-and-cancel acceptance record
  has been completed for a public release.
- Windows `amd64` is the only release target.
- Release binaries are not code-signed.
- Existing Codex Desktop tasks cannot be attached or monitored.
- Completion without an external verifier remains an agent self-report and
  requires explicit opt-in in execute mode.

[Unreleased]: https://github.com/andyandymike/done-then/commits/main
