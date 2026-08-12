---
name: done-then
description: Dry-run, cancel, and inspect single-session or multi-session DoneThen Stop lifecycle grants for Codex, and report whether real shutdown execution is actually ready. Use when the user asks to shut down after one or more Codex tasks stop, requests a dry-run, asks for status or cancellation, or explicitly chooses the experimental verified-success trigger.
---

# Done Then

Use the typed `done_then` MCP tools. Never invoke an operating-system power
command directly.

## Default: after stop

Treat `after_stop` as an observable lifecycle trigger, not proof that the task
succeeded. A normal answer, partial result, question, block, or interruption can
all end in `Stop`.

1. Check `execute_ready_by_policy.after_stop` before requesting real shutdown.
   In the current public runtime it is false because trusted final Stop
   arbitration is unavailable. Explain that boundary and use `dry_run`; do not
   retry or imply that OS support makes execute available.
2. Call `done_then.arm` once in the same turn with:
   - `action: shutdown`
   - `trigger_policy: after_stop`
   - `acknowledge_stop_without_success: false` for dry-run
   - `verifier_profile: none`
   - `allow_agent_only_success: false`
   - a delay field of at least 30 seconds and a bounded expiry (dry-run never
     invokes the power backend)
3. Preserve the returned `job_id`. Explain that it is an observation grant, not
   an active shutdown countdown, and that it can be cancelled explicitly.
4. Continue the requested work in the same Codex turn. Do not call `finish` or
   `pause`; the bundled Hook observes `Stop` directly.
5. Call `cancel` if the user retracts the request. A later user prompt in the
   same Codex task also cancels an active after-stop grant. Activity in a different task does not identify this grant; use
   its `job_id` to cancel explicitly.
6. Use `status` to report the persisted state. Never call `arm` again merely to
   retry a failed or cancelled job.

## Multiple tasks: after all stop

Use `trigger_policy: after_all_stop` only when the user explicitly supplies
every target Codex session id. Do not invent, correct, autocomplete, enumerate,
or infer target ids from transcripts.

1. Require 2-16 exact `target_session_ids` and preserve their order.
2. Explain that each target must produce a normal Stop after the barrier is
   reserved; a historical Stop does not count, and Stop still does not prove
   success.
3. Check `execute_ready_by_policy.after_all_stop`. When false, use dry-run with
   both acknowledgement fields false and state the
   `stop_arbitration_unavailable` reason.
4. Explain the cross-turn rule before arming: before completion, a
   target that resumes is reopened inside the barrier and must Stop again on a
   later turn. This differs from single-session `after_stop`, where a later
   prompt cancels the grant.
5. Use `verifier_profile: none`, `allow_agent_only_success: false`, and a bounded
   expiry. Call `arm` exactly once; DoneThen creates one barrier Job.
6. Report progress from the `job.barrier` object returned by single-job
   `status` (or each `jobs[].barrier` entry when listing) using its redacted
   `session_ref` values. Never expose persisted session or turn hashes as if
   they were the original ids.
7. If any target id is wrong or never emits a Hook, the barrier remains pending
   until expiry and the machine stays on. Use the returned `job_id` to cancel.
8. Do not claim that DoneThen can detect a Hook which Codex never invokes
   (disabled, untrusted, skipped, or process failure).

## Experimental verified success

Use `trigger_policy: verified_success` only when the user explicitly selects
the stricter experimental workflow. Set
`acknowledge_stop_without_success: false`, use its registered verifier policy,
and call `finish` only with genuine completion evidence. Its public execute path
is also unavailable today; keep the machine on if the mode reports unavailable,
partial, blocked, failed, or ambiguous.

In the final response, distinguish an armed dry-run grant, an observed Stop,
execute readiness, a future scheduled countdown, cancellation, and
execution-unverified state. Never describe `ACTION_SCHEDULED` as proof that the
machine powered off, and never describe backend support as execute readiness.
