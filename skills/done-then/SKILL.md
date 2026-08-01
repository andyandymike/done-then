---
name: done-then
description: Safely arm, pause, finish, cancel, and inspect a time-limited DoneThen post-task action intention for the current Codex task. Use when the user asks for shutdown or another follow-up action after the task completes, or asks about an existing DoneThen plugin job.
---

# Done Then

Use the typed `done_then` MCP tools. Never invoke an operating-system power
command directly and never treat a stopped turn as proof that the task is done.

## Workflow

1. Default to `dry_run`. Use `execute` only when the user explicitly requested
   a real shutdown and the tool reports `execute_available: true`. If execute
   is unavailable, keep the machine on and report the missing local setup.
2. Call `done_then.arm` once with a bounded expiry and the user-requested delay.
   Preserve the returned `job_id`, show `donethen cancel <job-id>`, and name the
   pre-registered verifier profile. Set `allow_agent_only_success` only when the
   user explicitly accepts that weaker boundary and local policy permits it.
3. Continue the user's task in the same Codex task.
4. Call `done_then.pause` before yielding for user input, approval, or external
   state. Choose only one of the documented reason codes.
5. Call `done_then.finish` only after the work is genuinely complete. Supply a
   completion object with `status: done`, no remaining work, no pending
   approval, and only checks that actually passed.
6. Call `done_then.cancel` when the user retracts the request or the job should
   not continue.
7. Use `done_then.status` to report the persisted state. Do not expose or infer
   nonces, transcripts, prompts, hook commands, or environment variables.

If completion is partial, blocked, failed, unverified, expired, or ambiguous,
keep the machine on. Do not call `finish` with invented evidence.

In the final response, report the actual persisted state. Distinguish armed,
host monitoring, verification, action intent, scheduled countdown, cancelled,
and execution-unverified states; never describe `ACTION_SCHEDULED` as proof the
machine powered off.
