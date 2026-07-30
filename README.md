# DoneThen

**Safe post-task actions for coding agents.**

[![CI](https://github.com/andyandymike/done-then/actions/workflows/ci.yml/badge.svg)](https://github.com/andyandymike/done-then/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

DoneThen is an open-source, Windows-first task supervisor. It performs an
explicitly armed action only after a coding-agent task has been verified as
complete.

The first integration target is OpenAI Codex, while the core completion and
action model is intended to remain agent-agnostic.

> [!IMPORTANT]
> DoneThen now has a source-level Windows pre-alpha implementation, but there is
> no published or signed release yet. The repository contains a tag-driven
> packaging workflow, but a public tag must not be created until the manual
> countdown-and-cancel acceptance is complete. Automated tests never invoke a
> real power action. Use dry-run first, and treat `--execute` as an explicit
> request to use the native Windows shutdown backend.

## Current status

The current source implements the complete pre-alpha `codex exec` supervision
path: structured completion validation, an optional external verifier,
one-shot state, dry-run, cancellation, and the Windows shutdown backend.

There is no supported binary download yet. The first public alpha remains
blocked on the documented manual countdown-and-cancel acceptance. Existing
Codex Desktop tasks cannot currently be attached; DoneThen starts and
supervises a new non-interactive Codex run.

## Why DoneThen?

A stopped agent turn is not the same thing as a successfully completed task.
The agent might be waiting for approval, reporting partial work, failing a
verification step, or asking the user a question. Running a shutdown command
directly from the agent is therefore both fragile and unsafe.

DoneThen separates four responsibilities:

1. The user explicitly arms an action.
2. A Codex adapter observes the original task lifecycle.
3. The supervisor validates structured completion evidence.
4. A narrow operating-system backend performs the approved action after a
   cancellable countdown.

```text
User arm
   -> Codex completion adapter
   -> fail-closed policy evaluation
   -> cancellable countdown
   -> native post-task action
```

## Safety model

DoneThen uses these rules:

- Actions must be armed by the user outside the model.
- Unknown, malformed, interrupted, partial, blocked, or failed results keep the
  machine on.
- Every armed job is one-shot, time-limited, and bound to a unique nonce.
- Destructive actions use a visible countdown and remain cancellable.
- Dry-run is the default during setup and testing.
- DoneThen never executes model-provided post-task command strings.
- Adapters report lifecycle events; they do not own shutdown authority.
- Real power jobs are serialized across Windows sessions and unresolved action
  records block later jobs until explicitly cancelled.
- Duplicate events and process restarts must not execute an action twice.

### Trust boundaries

- DoneThen supervises Codex; it is not a general operating-system sandbox for
  the coding task itself.
- Dangerous Codex flags are rejected by default. Explicitly allowing them can
  let Codex invoke host commands outside DoneThen's safety boundary.
- An external verifier is a user-selected trusted executable. It is run with
  argv, without a shell, but is not sandboxed by DoneThen.
- Windows `shutdown.exe /a` is system-global. Cancelling a DoneThen countdown
  can also abort another shutdown that was scheduled concurrently.

## Codex integrations

### `codex exec`

The implemented MVP wraps a new non-interactive `codex exec` run. DoneThen
combines the process exit status with a final response constrained by
`--output-schema`, then validates that response again outside Codex.

The MVP does not attach to an already running Codex Desktop task. App Server
and Hook support remain separate future adapters.

### Codex App Server

A later adapter will start or resume turns through the Codex App Server, apply
a per-turn `outputSchema`, and consume the final `turn/completed` event.

### Codex Hooks

An optional plugin adapter may forward `Stop` hook events to the supervisor.
The hook will never perform a power action directly: `Stop` runs whenever a
turn stops and does not, by itself, prove semantic task completion.

Relevant upstream documentation:

- [Codex non-interactive mode](https://learn.chatgpt.com/docs/developer-commands#codex-exec)
- [Codex App Server turns](https://learn.chatgpt.com/docs/app-server#turns)
- [Codex App Server lifecycle](https://learn.chatgpt.com/docs/app-server#lifecycle-overview)
- [Codex Stop hooks](https://learn.chatgpt.com/docs/hooks#stop)
- [Codex plugin packaging](https://developers.openai.com/plugins/build/plugins)

## Actions

The implemented MVP supports:

- Windows shutdown

Possible later actions include:

- Desktop notification
- Sound notification
- Lock screen
- Sleep
- Hibernate
- Cross-platform shutdown

Actions use narrow, platform-specific backends rather
than model-generated command strings.

## Requirements

- Windows 10 or 11 on `amd64` for the implemented action backend.
- Go 1.26 or newer to build the pre-alpha source.
- An installed and authenticated Codex CLI available as `codex`, or an
  explicit path supplied with `--codex-path`.

## Build from source

Clone a checkout you can review, then run:

```powershell
git clone https://github.com/andyandymike/done-then.git
Set-Location done-then
go test -count=1 ./...
go vet ./...
New-Item -ItemType Directory -Force bin | Out-Null
go build -trimpath -o bin\donethen.exe ./cmd/donethen
go version -m bin\donethen.exe
.\bin\donethen.exe --version
```

The CLI remains pre-alpha and may change before the first release.

Tagged releases are designed to contain an unsigned Windows `amd64` zip and a
`SHA256SUMS.txt` file. A checksum detects accidental or malicious file changes;
it is not a substitute for code signing or independent provenance. Release
binaries retain Go symbols and embedded Git revision metadata instead of being
stripped into a smaller, less inspectable executable.

## Usage

### Start with dry-run

Run the complete supervision path without allowing a power action:

```powershell
.\bin\donethen.exe run `
  --action shutdown `
  --dry-run `
  --delay 120s `
  --verify-program go `
  --verify-arg test `
  --verify-arg ./... `
  -- codex exec -C C:\path\to\your-project "Implement the requested change and verify it"
```

Dry-run still starts Codex and the configured verifier, but it does not call
the operating-system action backend.

### Explicitly allow the Windows action

Allow a real shutdown only after Codex and the external verifier both succeed:

```powershell
.\bin\donethen.exe run `
  --action shutdown `
  --execute `
  --delay 120s `
  --verify-program go `
  --verify-arg test `
  --verify-arg ./... `
  -- codex exec -C C:\path\to\your-project "Implement the requested change and verify it"
```

Without an external verifier, execute mode additionally requires
`--allow-agent-only-success`. This explicitly accepts that completion is based
only on Codex's structured self-report.

Cancel an armed job or an active Windows countdown:

```powershell
.\bin\donethen.exe cancel [job-id]
```

Inspect local jobs:

```powershell
.\bin\donethen.exe status
.\bin\donethen.exe status [job-id]
```

Job records and redacted JSONL lifecycle logs live under
`%LOCALAPPDATA%\DoneThen`. Prompts, transcripts, environment variables, and
model response bodies are not stored there.

## Antivirus and unsigned binaries

Pre-alpha binaries are not code-signed. A newly built Windows executable can
occasionally receive a heuristic antivirus detection even when built from the
reviewed source. DoneThen release builds deliberately avoid packers,
obfuscation, and stripped Go symbols; they retain Git build metadata and are
published with a SHA-256 checksum. These measures improve inspectability but
do not guarantee that every antivirus product will agree on every build.

Do not disable antivirus protection or add a broad folder exclusion to run
DoneThen. If a build is detected, keep it quarantined and record the DoneThen
commit, exact file hash, antivirus product and database version, and detection
name. Reproduce from a clean checkout, then open a
[bug report](https://github.com/andyandymike/done-then/issues/new?template=bug_report.yml)
with identifying paths and task data redacted. Use the private process in
[SECURITY.md](SECURITY.md) instead if the binary behaves differently from the
reviewed source or the report may expose sensitive information.

## Repository layout

```text
cmd/donethen/             CLI entry point
internal/cli/             run, cancel, and status commands
internal/codexexec/       validated Codex exec adapter
internal/completion/      completion envelope and policy
internal/supervisor/      one-shot job state machine
internal/store/           atomic records and redacted logs
internal/verifier/        argv-only external verification
internal/actions/         fake and Windows shutdown backends
internal/platform/        Windows power-job lock
internal/processgroup/    child-process tree cleanup
tests/                    contract tests and fake Codex fixture
docs/                     public release and operational guidance
```

Maintainer working notes under `spec/` or `specs/` are intentionally excluded
from version control and are not part of the public contract.
Contributor-facing requirements and stable decisions must be documented in
tracked files such as this README, `CONTRIBUTING.md`, or `docs/`.

## Roadmap

- [x] Define the completion-envelope schema and one-shot job state machine.
- [x] Build a Windows-first `codex exec` wrapper with dry-run support.
- [x] Add countdown, cancellation, and fail-closed restart recovery.
- [x] Test the shutdown action boundary through a fake backend in CI.
- [x] Add license, community health files, and checksum-producing release CI.
- [ ] Complete the real Windows countdown-and-cancel release acceptance.
- [ ] Add the Codex App Server adapter.
- [ ] Package the optional Codex Hook/plugin adapter.
- [ ] Add Linux and macOS action backends.
- [ ] Publish signed cross-platform binaries.

## Contributing

DoneThen is still pre-alpha. Read [CONTRIBUTING.md](CONTRIBUTING.md) before
submitting a change, report vulnerabilities through [SECURITY.md](SECURITY.md),
and follow [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md). Release maintainers must
complete the [release checklist](docs/release-checklist.md). Use the public
[issue tracker](https://github.com/andyandymike/done-then/issues) for ordinary
bugs and feature requests that contain no sensitive data.

## License

DoneThen is licensed under the [Apache License 2.0](LICENSE).
