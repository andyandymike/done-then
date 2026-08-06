---
name: done-then
description: Arm, cancel, and inspect a session-bound DoneThen shutdown countdown for Codex. Use when the user explicitly asks to shut down the computer after Codex stops or the current turn ends, requests a dry-run of that behavior, asks for DoneThen status or cancellation, or explicitly chooses the experimental verified-success trigger.
---

# Done Then

Use the typed `done_then` MCP tools. Never invoke an operating-system power
command directly.

## Default: after stop

Treat `after_stop` as an observable lifecycle trigger, not proof that the task
succeeded. A normal answer, partial result, question, block, or interruption can
all end in `Stop`.

1. Use `execute` only after the user explicitly requests a real shutdown and
   accepts shutdown after the current Codex turn stops. Otherwise use `dry_run`
   or ask for that confirmation.
2. Call `done_then.arm` once in the same turn with:
   - `action: shutdown`
   - `trigger_policy: after_stop`
   - `acknowledge_stop_without_success: true` for execute
   - `verifier_profile: none`
   - `allow_agent_only_success: false`
   - an execute delay of at least 120 seconds and a bounded expiry
3. Preserve the returned `job_id`. Tell the user the countdown is cancellable
   with `done_then.cancel` or `donethen cancel <job-id>`.
4. Continue the requested work in the same Codex turn. Do not call `finish` or
   `pause`; the bundled Hook observes `Stop` directly.
5. Call `cancel` if the user retracts the request. A later user prompt in the
   same Codex task also cancels an active after-stop grant or countdown
   fail-closed. Activity in a different task does not identify this grant; use
   its `job_id` to cancel explicitly.
6. Use `status` to report the persisted state. Never call `arm` again merely to
   retry a failed or cancelled job.

## Experimental verified success

Use `trigger_policy: verified_success` only when the user explicitly selects
the stricter experimental workflow. Set
`acknowledge_stop_without_success: false`, use its registered verifier policy,
and call `finish` only with genuine completion evidence. Keep the machine on if
that mode reports unavailable, partial, blocked, failed, or ambiguous.

In the final response, distinguish an armed grant, an observed Stop, a scheduled
countdown, cancellation, and execution-unverified state. Never describe
`ACTION_SCHEDULED` as proof that the machine powered off.
