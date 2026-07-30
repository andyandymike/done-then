# DoneThen

**Safe post-task actions for coding agents.**

[![CI](https://github.com/andyandymike/done-then/actions/workflows/ci.yml/badge.svg)](https://github.com/andyandymike/done-then/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

DoneThen is an open-source, Windows-first safety layer for post-task actions.
Its primary design is a Codex plugin: the user asks once inside the original
task, Codex calls typed DoneThen tools, and lifecycle hooks bind the result to
that same task. There is no second Codex run and the user does not manually
launch a watcher window.

The first integration target is OpenAI Codex, while the core completion and
action model is intended to remain agent-agnostic.

> [!IMPORTANT]
> The plugin is currently **observe-only**. Its `execute` arm mode is hard
> disabled until Codex can provide an authoritative effective-Hook inventory
> and the remaining coexistence gates are implemented. Neither an MCP call nor
> a Hook can invoke the power backend in this build. The older `codex exec`
> supervisor still contains an experimental `--execute` path, but it is not the
> plugin workflow and there is no published or signed release yet.

## Current status

The repository now contains the first plugin implementation slice:

- a Codex plugin manifest and `$done-then` Skill;
- a stdio MCP server with `arm`, `finish`, `pause`, `cancel`, and `status`;
- plugin-owned `PostToolUse`, `UserPromptSubmit`, `Stop`, and `SessionEnd`
  observers;
- atomic, process-serialized plugin job state and redacted event logs;
- structured task/turn binding, strict completion validation, generation-based
  stale-evidence invalidation, and CLI recovery through `status` and `cancel`.

The plugin never edits user, project, managed, or other-plugin Hook files. Its
own Hooks are loaded as another source and remain subject to Codex's normal
review and trust flow.

There is no supported binary download or one-command plugin installation yet.
Verifier profiles, authoritative Hook inventory, task inventory, and the final
power-action gate remain to be implemented before a power-enabled alpha.

## Why DoneThen?

A stopped agent turn is not the same thing as a successfully completed task.
The agent might be waiting for approval, reporting partial work, failing a
verification step, or asking the user a question. Running a shutdown command
directly from the agent is therefore both fragile and unsafe.

DoneThen separates four responsibilities:

1. The user explicitly requests and approves a time-limited action intention.
2. The Skill reports lifecycle state through typed MCP tools.
3. Non-interfering Hooks bind those reports to the original task and turn.
4. A supervisor outside the Hook evaluates every safety gate before a narrow
   operating-system backend can act.

```text
User request in the original Codex task
   -> Skill calls done_then.arm
   -> PostToolUse binds session and turn
   -> work / pause / resume
   -> done_then.finish validates structured evidence
   -> Stop observer records a candidate boundary only
   -> external final gate (not implemented; machine stays on)
```

## Safety model

DoneThen uses these rules:

- A user request and the configured MCP approval policy must authorize arming;
  an unapproved model decision is insufficient.
- Unknown, malformed, interrupted, partial, blocked, or failed results keep the
  machine on.
- Every armed job is one-shot, time-limited, and bound to a unique nonce.
- Destructive actions use a visible countdown and remain cancellable.
- Dry-run is the default during setup and testing.
- DoneThen never executes model-provided post-task command strings.
- Adapters report lifecycle events; they do not own shutdown authority.
- A `Stop` or `SessionEnd` event is never treated as semantic success.
- Matching Hooks from other sources are preserved. Any other effective `Stop`
  Hook, overlapping tool Hook, or unknown Hook inventory will block future
  execute mode by default.
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

### Plugin (primary direction)

The plugin combines three pieces instead of choosing between them:

- the Skill is the user-facing workflow;
- the MCP server owns typed commands and persisted state;
- Hooks observe host-provided session and turn boundaries.

Codex starts `donethen mcp` and invokes `donethen hook` automatically from the
plugin configuration. Those are transport entry points, not programs the user
opens separately. The Hook handler exits `0`, emits no stdout, never returns a
continuation or policy decision, and never calls an action backend.

Codex merges matching Hooks from every active source and launches same-event
command Hooks concurrently. Plugin Hooks also require explicit review and
trust. Inspect them with `/hooks`; do not use a trust-bypass flag for normal
DoneThen operation.

### `codex exec`

The earlier MVP wraps a new non-interactive `codex exec` run. DoneThen
combines the process exit status with a final response constrained by
`--output-schema`, then validates that response again outside Codex.

This path proves the completion and Windows action boundaries, but it starts a
second non-interactive run and is no longer the intended everyday UX.

### Codex App Server

A later adapter will start or resume turns through the Codex App Server, apply
a per-turn `outputSchema`, and consume the final `turn/completed` event.

Relevant upstream documentation:

- [Codex non-interactive mode](https://learn.chatgpt.com/docs/developer-commands#codex-exec)
- [Codex App Server turns](https://learn.chatgpt.com/docs/app-server#turns)
- [Codex App Server lifecycle](https://learn.chatgpt.com/docs/app-server#lifecycle-overview)
- [Codex Stop hooks](https://learn.chatgpt.com/docs/hooks#stop)
- [Codex plugin packaging](https://developers.openai.com/plugins/build/plugins)

## Actions

The legacy CLI supervisor implements:

- Windows shutdown

The plugin currently supports only dry-run observation. `mode: execute` returns
`execute_unavailable` before creating a job.

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
- A Codex build with plugin, MCP, and Hook support for the plugin preview.
- An installed and authenticated Codex CLI available as `codex`, or an
  explicit path supplied with `--codex-path`, only for the legacy wrapper.

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

For local plugin development, first review the reversible installation plan:

```powershell
pwsh -NoProfile -File .\scripts\dev-plugin.ps1 -Action Plan
```

The development installer stages an ignored local marketplace and uses the
Codex CLI for install and uninstall. It accepts the exact DoneThen runtime path,
so the binary does not need to be added to a persistent `PATH`. Mutating actions
require `-Apply`; the script does not directly rewrite personal Codex or Hook
configuration. See [Local plugin development](docs/plugin-development.md) for
the baseline, manual Hook trust, live dry-run, verification, and uninstall
workflow.

Tagged releases are designed to contain an unsigned Windows `amd64` zip and a
`SHA256SUMS.txt` file. A checksum detects accidental or malicious file changes;
it is not a substitute for code signing or independent provenance. Release
binaries retain Go symbols and embedded Git revision metadata instead of being
stripped into a smaller, less inspectable executable.

## Plugin development preview

After following the
[local development workflow](docs/plugin-development.md), starting a new Codex
task, and reviewing its Hooks with `/hooks`, ask inside the task itself:

```text
Use $done-then in dry-run mode. When this task is genuinely complete, report
structured completion and show me the resulting DoneThen status. For this
dry-run, I explicitly accept structured agent-only completion evidence.
```

Codex should call `arm` once, keep the returned `job_id`, and call `pause` when
waiting. It may call `finish` only when all reported checks passed and there is
no remaining work or approval. A later user prompt invalidates old READY
evidence. A matching `Stop` records `STOP_OBSERVED`, but the machine remains on
because execute authority is unavailable.

The current plugin build has no registered external verifier profiles. Without
that explicit agent-only acceptance, a `done` report ends in
`VERIFICATION_FAILED` instead of READY.

Recovery does not require the plugin to remain active:

```powershell
donethen status [job-id]
donethen cancel [job-id]
```

Plugin records live under `%LOCALAPPDATA%\DoneThen\plugin`. Status output is
redacted; persisted event logs hash session and turn identifiers and do not
store prompts, transcripts, environment variables, nonces, or model response
bodies.

## Legacy CLI usage

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
cmd/donethen/             CLI, MCP, and Hook process entry point
.codex-plugin/            Codex plugin manifest
.mcp.json                 bundled stdio MCP server declaration
skills/done-then/         user-facing plugin workflow
hooks/hooks.json          non-interfering lifecycle observers
internal/cli/             run, cancel, status, MCP, and Hook commands
internal/pluginapi/       typed DoneThen MCP operations
internal/pluginstate/     atomic plugin jobs, indexes, and redacted events
internal/mcpserver/       dependency-free stdio MCP transport
internal/hookobserver/    structured Codex lifecycle binding
internal/codexexec/       validated Codex exec adapter
internal/completion/      completion envelope and policy
internal/supervisor/      one-shot job state machine
internal/store/           atomic records and redacted logs
internal/verifier/        argv-only external verification
internal/actions/         fake and Windows shutdown backends
internal/platform/        Windows power-job lock
internal/processgroup/    child-process tree cleanup
tests/                    contract tests and fake Codex fixture
scripts/                  reversible plugin development and smoke-test tools
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
- [x] Add an observe-only Codex plugin, Skill, stdio MCP server, and narrow
  observer Hooks without modifying existing Hook sources.
- [x] Bind MCP results to session/turn events and invalidate stale completion
  evidence on later prompts.
- [ ] Add pre-registered external verifier profiles for plugin jobs.
- [ ] Obtain and validate an authoritative effective-Hook inventory at arm,
  finish, and the final action gate.
- [ ] Block power mode on overlapping Hooks or other active Codex tasks.
- [ ] Complete the real Windows countdown-and-cancel release acceptance.
- [ ] Add the Codex App Server adapter.
- [ ] Enable the plugin power-action supervisor only after all fail-closed gates
  pass in an isolated alpha profile.
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
