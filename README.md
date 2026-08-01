# DoneThen

**Safely shut down a computer after a Codex task is truly complete.**

[![CI](https://github.com/andyandymike/done-then/actions/workflows/ci.yml/badge.svg)](https://github.com/andyandymike/done-then/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

DoneThen is an open-source, Windows-first safety layer for shutting down a computer
after a coding-agent task is genuinely complete. Its primary design is a Codex
plugin: the user asks once inside the original task, Codex calls typed DoneThen
tools, and lifecycle hooks bind the result to that same task. There is no second
Codex run and the user does not manually launch a watcher window.

The first integration target is OpenAI Codex, while the core completion and
action model is intended to remain agent-agnostic.

> [!IMPORTANT]
> Plugin power is **disabled by default and not yet a supported capability**.
> The conditional execute path, external supervisor, verifier registry,
> HostAuthority adapter, and platform backends are present, but no platform has
> completed the required real power-off acceptance. The public MCP server does
> not expose execute mode: policy capture records reviewed local identity for
> future development, but cannot substitute for an authoritative same-host
> attachment. Hooks themselves never invoke a power backend.

## Current status

The repository now contains the end-to-end implementation skeleton:

- a Codex plugin manifest and `$done-then` Skill;
- a stdio MCP server with `arm`, `finish`, `pause`, `cancel`, and `status`;
- plugin-owned `PostToolUse`, `UserPromptSubmit`, `Stop`, and `SessionEnd`
  observers;
- atomic, process-serialized plugin job state and redacted event logs;
- structured task/turn binding, strict completion validation, generation-based
  stale-evidence invalidation, and CLI recovery through `status`, `cancel`, and
  read-only `reconcile`;
- an automatically detached, one-shot execute supervisor with H1/H2/H3 Hook
  snapshots, active-task/child/background inventory, final quiescence checks,
  monitored countdown, late-wake cancellation, and cancellation-race rollback;
- owner-controlled, fingerprinted verifier profiles and power policy capture;
- receipt-bound Windows and Linux systemd backends; macOS remains a safe
  unsupported stub until a signed and notarized helper exists.

The plugin never edits user, project, managed, or other-plugin Hook files. Its
own Hooks are loaded as another source and remain subject to Codex's normal
review and trust flow.

There is no supported binary download or one-command plugin installation yet.
The principal remaining blockers are real Windows cancel/power-off evidence,
proof that the App Server connection is authoritative for the current Codex
host, Linux helper installation/E2E evidence, and a signed macOS helper.

### Capability matrix

Capability levels describe evidence, not the amount of code present. C1 means
portable build/unit-test coverage; C2 adds dry-run/fake-backend coverage. No
entry below claims that a real shutdown has been accepted or completed.

| Platform | Level | Published statement |
| --- | --- | --- |
| `windows-amd64` | C2 | preview; real backend code is present but real cancel and poweroff acceptance are pending |
| `windows-arm64` | C1 | portable build only; native acceptance is pending |
| `linux-amd64` | C1 | systemd helper code is present; installation and real-host acceptance are pending |
| `linux-arm64` | C1 | systemd helper code is present; installation and real-host acceptance are pending |
| `darwin-amd64` | C1 | portable build only; signed and notarized helper is not delivered |
| `darwin-arm64` | C1 | portable build only; signed and notarized helper is not delivered |

The machine-readable source is
[`internal/capability/manifest.json`](internal/capability/manifest.json).

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
   -> detached supervisor proves same-host, Hook, task, verifier, and power gates
   -> receipt-bound cancellable schedule (only if every gate is authoritative)
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
  Hook, overlapping tool Hook, changed definition, or unknown Hook inventory
  blocks execute mode.
- Real power jobs are serialized at the machine boundary and unresolved intent
  or receipt records block later jobs until explicitly reconciled or cancelled.
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

The HostAuthority adapter speaks the initialized App Server JSONL protocol and
consumes `hooks/list`, thread status, `turn/completed`, Hook lifecycle, loaded
threads, child threads, and background-terminal inventory. It deliberately
rejects an isolated App Server that cannot prove ownership of the target task
and live event stream. A stable same-host attachment mechanism is still an
upstream integration blocker, so this code does not raise any platform above
its published capability level.

Relevant upstream documentation:

- [Codex non-interactive mode](https://learn.chatgpt.com/docs/developer-commands#codex-exec)
- [Codex App Server turns](https://learn.chatgpt.com/docs/app-server#turns)
- [Codex App Server lifecycle](https://learn.chatgpt.com/docs/app-server#lifecycle-overview)
- [Codex Stop hooks](https://learn.chatgpt.com/docs/hooks#stop)
- [Codex plugin packaging](https://developers.openai.com/plugins/build/plugins)

## Actions

Implemented action boundaries are:

- Windows: absolute `shutdown.exe` scheduling and system-global `/a`
  cancellation, with receipt and boot reconciliation;
- Linux systemd: a root-owned, fixed-operation Unix-socket helper with
  job-specific transient units, cancellation, and reconciliation;
- macOS: explicit unsupported result until a signed/notarized privileged helper
  and authenticated IPC are delivered.

Plugin dry-run works without a power policy. The public MCP server currently
reports `execute_available=false` even when an owner-controlled, hash-bound
policy has been captured. Execute can be enabled only by a future integration
that proves it is attached to the authoritative host for the current task; the
detached supervisor then still fails closed unless every live host, verifier,
policy, cancellation, and platform gate passes.

Possible later actions include:

- Desktop notification
- Sound notification
- Lock screen
- Sleep
- Hibernate

Actions use narrow, platform-specific backends rather
than model-generated command strings.

## Requirements

- Go 1.26 or newer to build the pre-alpha source.
- A Codex build with plugin, MCP, and Hook support for the plugin preview.
- An installed and authenticated Codex CLI available as `codex`, or an
  explicit path supplied with `--codex-path`, for policy capture, HostAuthority,
  or the legacy wrapper.
- See the capability matrix above for OS/architecture status. A successful
  build is not proof of supported power behavior.

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

Tagged releases are designed to contain unsigned portable archives for Windows,
Linux, and macOS on `amd64` and `arm64`, plus `SHA256SUMS.txt` and one
artifact-bound SPDX 2.3 SBOM per archive. Linux archives also contain the
preview systemd helper and a plan-first installer; packaging does not upgrade
its C1 capability. GitHub Actions creates provenance
attestations. Checksums and attestations improve integrity and traceability but
are not code signing, notarization, or real-host acceptance. Binaries retain Go
symbols and embedded Git revision metadata.

The public documentation site is published from [`docs/`](docs/index.html) and
uses the project-owned visual contract in [`DESIGN.md`](DESIGN.md).

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
evidence. A matching `Stop` records `STOP_OBSERVED`; dry-run never schedules a
power action.

Verifier profiles are installed explicitly and are never supplied by the model:

```powershell
donethen verifier add --id repo-tests --program "C:\Program Files\Go\bin\go.exe" `
  --arg test --arg ./... --timeout 10m
# Review the plan, then repeat with --apply.
donethen verifier list
```

Without a registered profile or explicit per-job agent-only acceptance, a
`done` report ends in `VERIFICATION_FAILED` instead of becoming actionable.

Policy capture records the reviewed runtime and effective-Hook identity needed
by future execute development. Capture is plan-only unless `--apply` is
supplied:

```powershell
donethen policy capture --plugin-id done-then@done-then-dev
# Inspect the Hook keys and hashes, then repeat with --apply if intentional.
donethen doctor
```

Installing a policy does not enable execute in the public MCP server, bypass
same-host or platform gates, or change the capability matrix.

Recovery does not require the plugin to remain active:

```powershell
donethen status [job-id]
donethen cancel [job-id]
donethen reconcile <job-id>
donethen doctor [--json]
```

Plugin records live under the platform data root (`%LOCALAPPDATA%\DoneThen` on
Windows, `$XDG_STATE_HOME/donethen` or `~/.local/state/donethen` on Linux, and
the user configuration directory on macOS). Status output is redacted;
persisted event logs hash session and turn identifiers and do not store prompts,
transcripts, environment variables, nonces, or model response bodies.

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

### Explicitly allow a platform action

This is an unaccepted development path, not a supported operating procedure.
It allows the platform backend only after Codex and the external verifier both
succeed:

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
cmd/donethen/             CLI, MCP, Hook, and one-shot supervisor entry point
cmd/donethen-powerd/      Linux fixed-operation system power helper
.codex-plugin/            Codex plugin manifest
.mcp.json                 bundled stdio MCP server declaration
skills/done-then/         user-facing plugin workflow
hooks/hooks.json          non-interfering lifecycle observers
internal/cli/             run, cancel, status, MCP, and Hook commands
internal/pluginapi/       typed DoneThen MCP operations
internal/pluginstate/     atomic plugin jobs, indexes, and redacted events
internal/mcpserver/       dependency-free stdio MCP transport
internal/hookobserver/    structured Codex lifecycle binding
internal/hostauthority/   App Server transport, live evidence, and Hook policy
internal/pluginpower/     detached supervisor and final action gate
internal/powerpolicy/     owner-controlled, hash-bound execute policy
internal/verifierprofile/ fixed verifier profile registry
internal/codexexec/       validated Codex exec adapter
internal/completion/      completion envelope and policy
internal/supervisor/      one-shot job state machine
internal/store/           atomic records and redacted logs
internal/verifier/        argv-only external verification
internal/actions/         fake, Windows, Linux helper, and safe macOS backends
internal/powerdaemon/     Linux root helper protocol and systemd scheduling
internal/platform/        platform power-job locks
internal/processgroup/    child-process tree cleanup
tests/                    contract tests and fake Codex fixture
scripts/                  reversible plugin development and smoke-test tools
packaging/linux/          preview systemd service and tmpfiles definitions
docs/                     public release and operational guidance
DESIGN.md                 public visual identity and UI token contract
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
- [x] Add a default-dry-run Codex plugin, Skill, stdio MCP server, and narrow
  observer Hooks without modifying existing Hook sources.
- [x] Bind MCP results to session/turn events and invalidate stale completion
  evidence on later prompts.
- [x] Add owner-controlled external verifier profiles for plugin jobs.
- [x] Implement effective-Hook snapshots at arm, finish, and the final gate.
- [x] Block power mode on overlapping Hooks, other active tasks, children, or
  background terminals.
- [x] Add the App Server transport and fail-closed HostAuthority adapter.
- [x] Add the automatically detached plugin power supervisor.
- [x] Monitor the full countdown and cancel on continuation, authority drift,
  verifier/policy drift, host loss, or an excessively late wake.
- [x] Add the Linux systemd helper/backend and six-architecture build matrix.
- [ ] Complete the real Windows countdown-and-cancel release acceptance.
- [ ] Complete real Windows power-off and post-boot reconciliation acceptance.
- [ ] Establish a stable same-host App Server attachment for plugin execute.
- [ ] Complete Linux install/cancel/power-off acceptance on each architecture.
- [ ] Deliver and audit a signed/notarized macOS privileged helper.
- [ ] Publish signed/notarized artifacts without overstating capability levels.

## Contributing

DoneThen is still pre-alpha. Read [CONTRIBUTING.md](CONTRIBUTING.md) before
submitting a change, report vulnerabilities through [SECURITY.md](SECURITY.md),
and follow [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md). Release maintainers must
complete the [release checklist](docs/release-checklist.md). Use the public
[issue tracker](https://github.com/andyandymike/done-then/issues) for ordinary
bugs and feature requests that contain no sensitive data.

## License

DoneThen is licensed under the [Apache License 2.0](LICENSE).
