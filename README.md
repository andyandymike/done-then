# DoneThen

**Safe post-task actions for coding agents.**

DoneThen is an open-source task supervisor that performs an explicitly armed
action only after a coding-agent task has been verified as complete.

The first integration target is OpenAI Codex, while the core completion and
action model is intended to remain agent-agnostic.

> [!IMPORTANT]
> DoneThen is currently in the design phase. There is no working release yet,
> and none of the commands shown below are available today.

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

DoneThen is being designed around these rules:

- Actions must be armed by the user outside the model.
- Unknown, malformed, interrupted, partial, blocked, or failed results keep the
  machine on.
- Every armed job is one-shot, time-limited, and bound to a unique nonce.
- Destructive actions use a visible countdown and remain cancellable.
- Dry-run is the default during setup and testing.
- Arbitrary model-provided shell commands are never executed.
- Adapters report lifecycle events; they do not own shutdown authority.
- Duplicate events and process restarts must not execute an action twice.

## Planned Codex integrations

### `codex exec`

The initial and most reliable mode will wrap non-interactive `codex exec` runs.
DoneThen will combine the process exit status with a validated final response
produced through Codex's `--output-schema` support.

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

## Planned actions

- Desktop notification
- Sound notification
- Lock screen
- Sleep
- Hibernate
- Shutdown

Actions will be implemented through narrow, platform-specific backends rather
than model-generated command strings.

## Proposed CLI

The exact interface may change before the first release.

```powershell
donethen run `
  --action shutdown `
  --delay 120s `
  --verify "go test ./..." `
  -- codex exec -C D:\project "Implement the requested change and verify it"
```

Cancel an armed job or active countdown:

```powershell
donethen cancel
```

Inspect a job without allowing side effects:

```powershell
donethen run --dry-run --action shutdown -- codex exec "Run the task"
```

## Planned repository layout

```text
cmd/donethen/             CLI entry point
internal/supervisor/      one-shot job state machine
internal/policy/          completion and safety evaluation
internal/adapters/        Codex exec, App Server, and Hook adapters
internal/actions/         Windows, Linux, and macOS backends
schemas/                  completion-envelope JSON Schemas
plugin/                   optional Codex plugin package
tests/                    unit, integration, and failure-path tests
```

Local implementation specifications will live under `spec/` or `specs/` and
are intentionally excluded from version control. Stable public decisions
should eventually be promoted into this README or the public documentation.

## Roadmap

- [ ] Define the completion-envelope schema and one-shot job state machine.
- [ ] Build a Windows-first `codex exec` wrapper with dry-run support.
- [ ] Add countdown, cancellation, expiration, and restart recovery.
- [ ] Test every power action through a fake backend in CI.
- [ ] Add the Codex App Server adapter.
- [ ] Package the optional Codex Hook/plugin adapter.
- [ ] Add Linux and macOS action backends.
- [ ] Publish signed cross-platform binaries.

## Contributing

DoneThen is not ready for external contributions yet. The contribution guide,
code of conduct, security policy, and license will be added before the first
public development release.

