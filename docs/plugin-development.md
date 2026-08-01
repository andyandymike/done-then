---
layout: default
title: Local plugin development
description: Install and verify DoneThen's default dry-run Codex plugin without overwriting existing Hooks.
---

# Local plugin development

This guide installs the DoneThen plugin through a disposable local Codex
marketplace and verifies one real Codex task in dry-run mode. Plugin power is
disabled by default. The live-smoke harness never installs a power policy,
starts an execute supervisor, or calls a platform action backend.

The source tree also contains a conditional execute implementation. It is not
part of this smoke workflow and does not represent accepted platform support.
The public MCP server keeps execute unavailable even after an owner-controlled
policy is captured, because policy identity is not authoritative same-host
evidence. A future host integration must supply that proof before the remaining
execute gates can be evaluated.

Codex loads plugin Hooks alongside user, project, managed, and other-plugin
Hooks. It does not replace those sources. Plugin Hooks also require a manual
trust review for their current definition. See the official
[plugin packaging](https://developers.openai.com/plugins/build/plugins#bundled-mcp-servers-and-lifecycle-hooks)
and [Hook](https://learn.chatgpt.com/docs/hooks) documentation.

## What the development scripts change

`scripts/dev-plugin.ps1` defaults to `Plan`. A mutating action requires both an
explicit action and `-Apply`. It:

- stages an allowlisted plugin package under the ignored
  `.tmp/done-then-dev-marketplace` directory;
- gives the staged manifest a local cache-buster version without changing the
  tracked manifest;
- points the staged MCP and Windows Hook launchers at the exact runtime passed
  through `-DoneThenPath`, including paths containing spaces;
- records that runtime's SHA-256 in an ignored receipt so live verification can
  detect replacement after install;
- invokes `codex plugin marketplace add/remove` and `codex plugin add/remove`;
- never writes `~/.codex/config.toml`, `~/.codex/hooks.json`, project Hook
  files, or Codex Hook trust records directly.

Uninstall removes only `done-then@done-then-dev`, the script-owned marketplace,
its ignored staging directory, and its ignored install receipt. It retains the
runtime supplied by the caller, `%LOCALAPPDATA%\DoneThen` evidence, and Codex
Hook trust history. A retained trust hash is inert when its plugin is absent
and can be reviewed from `/hooks`.

`scripts/live-smoke.ps1` takes hashes of these four Hook configuration sources:

- `~/.codex/hooks.json`
- `~/.codex/config.toml`
- `<repository>/.codex/hooks.json`
- `<repository>/.codex/config.toml`

It also reads `codex plugin list --json` and hashes every installed non-DoneThen
plugin manifest, declared Hook definition, and regular file under its `hooks/`
tree. If Codex cannot expose a local source path, or a Hook tree contains a
reparse point that cannot be inventoried safely, evidence capture fails instead
of silently omitting that plugin.

It stores only file paths, lengths, and SHA-256 hashes in the ignored `.tmp`
directory. It does not copy configuration contents.

## Prerequisites

- Windows PowerShell or PowerShell 7.
- A Codex CLI that provides `codex plugin` and `codex plugin marketplace`.
- A reviewed DoneThen runtime available as a command or file.

The installer does not build an executable. To build the Go runtime explicitly:

```powershell
New-Item -ItemType Directory -Force bin | Out-Null
go build -trimpath -o bin\donethen.exe ./cmd/donethen
go version -m bin\donethen.exe
```

Go may be installed in a path containing spaces. Pass the exact runtime path to
the installer. Do not disable antivirus or create a broad exclusion if a local
unsigned build is quarantined; follow the evidence-preserving process in the
README instead. The observer Hook has a three-second hard timeout, so a native
runtime with fast startup is recommended; a `go run` wrapper that misses that
budget is not valid live-smoke evidence.

## Install and verify

Run every command from the repository root.

1. Review the plan. This is read-only and is also the default action:

   ```powershell
   pwsh -NoProfile -File .\scripts\dev-plugin.ps1 -Action Plan
   ```

2. Capture the pre-install Hook configuration baseline:

   ```powershell
   pwsh -NoProfile -File .\scripts\live-smoke.ps1 -Action Snapshot -Apply
   ```

3. Install the disposable development plugin:

   ```powershell
   pwsh -NoProfile -File .\scripts\dev-plugin.ps1 `
     -Action Install `
     -DoneThenPath .\bin\donethen.exe `
     -Apply
   ```

   Use `-Action Reinstall` after changing the plugin package. The tracked
   manifest is not modified. Each staged reinstall changes the launcher name so
   Codex treats the current Hook definition as new; review it again in `/hooks`.

4. Start a **new Codex task**. Open `/hooks`, inspect the DoneThen definitions,
   and manually trust them. The installer intentionally cannot bypass or grant
   Hook trust.

5. Before running a task, prove that install and trust did not rewrite the four
   existing Hook sources:

   ```powershell
   pwsh -NoProfile -File .\scripts\live-smoke.ps1 -Action Compare
   ```

   Stop if this reports any changed target. Review the actual diff before
   replacing the baseline; do not normalize an unexplained change away.

6. In that new Codex task, use a bounded smoke request such as:

   ```text
   Use $done-then in dry-run mode for this task. Inspect README.md and report
   the project title. When the requested work and checks are genuinely complete,
   report structured completion. For this dry-run only, I explicitly accept
   structured agent-only completion evidence. Keep and show the DoneThen job_id.
   Do not invoke an operating-system power command.
   ```

   DoneThen should arm once, bind the `PostToolUse` event to this task, report
   completion, observe the matching `finish` Hook, and then observe the same
   turn's `Stop`. No countdown or power action is available.

7. After the response turn stops, but before closing or archiving the Codex
   task, verify the returned job ID from a terminal:

   ```powershell
   .\bin\donethen.exe status <job-id>
   pwsh -NoProfile -File .\scripts\live-smoke.ps1 `
     -Action Verify `
     -JobId <job-id>
   ```

   Verification requires all of the following:

   | Gate | Required observation |
   | --- | --- |
   | Hook coexistence | All four configuration hashes and every other installed plugin manifest/Hook hash still match the pre-install baseline |
   | Runtime identity | The current runtime SHA-256 still matches the ignored development-install receipt |
   | Job mode | `dry_run=true`, `action=shutdown`, agent-only evidence explicitly accepted |
   | Final lifecycle | `STOP_OBSERVED` with `matching_stop_observed_no_action` |
   | Event order | `mcp.arm`, `hook.post_tool.arm`, `mcp.finish`, `hook.post_tool.finish`, `hook.stop` |
   | Privacy | Event records contain only the allowlisted redacted schema and hashed session/turn identifiers |
   | Power boundary | No power, shutdown, execute, or scheduling event exists |

   `STOP_OBSERVED` is lifecycle evidence, not authorization to shut down. If
   any gate is missing, the smoke test fails closed.

## Status, recovery, and uninstall

Inspect installation state without changing it:

```powershell
pwsh -NoProfile -File .\scripts\dev-plugin.ps1 -Action Status
```

Cancel an unfinished dry-run job even if the plugin is disabled:

```powershell
.\bin\donethen.exe cancel <job-id>
```

Remove the development plugin and its script-owned marketplace:

```powershell
pwsh -NoProfile -File .\scripts\dev-plugin.ps1 -Action Uninstall -Apply
pwsh -NoProfile -File .\scripts\live-smoke.ps1 -Action Compare
```

The final comparison proves uninstall also left the pre-existing Hook sources
and other installed plugins unchanged. Run the same comparison after every
`Reinstall` while retaining the original pre-install baseline.

Pass `-KeepMarketplace` only when intentionally retaining the ignored staged
package for debugging. The script refuses to remove a marketplace named
`done-then-dev` if Codex reports that it points outside this repository's exact
script-owned staging path.

After install, reinstall, or uninstall, use a new Codex task so the active
task does not rely on stale plugin or Hook discovery state.
