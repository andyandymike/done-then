---
layout: default
title: Release checklist
description: Source, CI, cancellation, artifact, and post-release gates for DoneThen releases.
---

# Release checklist

This checklist applies to portable prereleases and later power-capable releases.
It does not authorize a release or raise a capability level by itself. The
maintainer performing the release owns every manual confirmation below, and
each OS/architecture is evaluated independently against the capability manifest.

## 1. Source and policy review

- [ ] The release commit is reviewed and the working tree is clean.
- [ ] `CHANGELOG.md` describes the release and its known limitations.
- [ ] `README.md`, `SECURITY.md`, and CLI help agree with the implementation.
- [ ] No local file under `spec/`, `specs/`, `.tmp/`, or a runtime state
      directory is tracked.
- [ ] The release contains no prompt, transcript, environment dump, token,
      private path, or model response artifact.
- [ ] CI and release workflows contain no real shutdown invocation.
- [ ] Release Action versions and permissions have been reviewed.

## 2. Automated gates

Run from the repository root on Windows:

```powershell
go fmt ./...
go test -count=1 ./...
go vet ./...
go build ./...
git diff --check
```

CI additionally runs the full suite on Windows, Ubuntu, and macOS, race tests
for concurrent state/authority paths, PowerShell parse checks, and CGO-free
cross-builds for all six published OS/architecture pairs.

- [ ] Every command exits successfully.
- [ ] Tests at the action boundary use only the fake backend or injected fake
      process runner.
- [ ] Countdown tests cover user continuation, authority/policy/verifier drift,
      host loss, excessive wake lateness, and cancel-during-schedule races.
- [ ] Linux helper reconciliation proves inactive stale state is released and
      never invokes or retries `poweroff`.
- [ ] A clean checkout passes `.github/workflows/ci.yml`.

## 3. Manual Windows power acceptance

This section intentionally reaches the real Windows shutdown backend. Backend
acceptance has two separate cases: countdown/cancel and completed
power-off/reconciliation. The current-task plugin path has an additional
after-stop acceptance case below.
Run either case
only with explicit authorization on a non-critical Windows test machine. Close
important work first and ensure no unrelated shutdown or restart is pending.

Build the supervisor and deterministic fake Codex fixture:

```powershell
New-Item -ItemType Directory -Force bin | Out-Null
go build -trimpath -o bin\donethen.exe ./cmd/donethen
go build -trimpath -o bin\fake-codex.exe ./tests/fixtures/fake-codex
go version -m bin\donethen.exe
```

Schedule a five-minute countdown through the complete execute path:

```powershell
.\bin\donethen.exe run `
  --action shutdown `
  --execute `
  --delay 5m `
  --allow-agent-only-success `
  --codex-path .\bin\fake-codex.exe `
  -- codex exec "Manual release acceptance fixture"
```

Copy the emitted job ID and cancel immediately:

```powershell
.\bin\donethen.exe cancel <job-id>
.\bin\donethen.exe status <job-id>
```

- [ ] Windows visibly accepted the original countdown.
- [ ] `cancel` reported that the countdown was aborted, or safely reported that
      no shutdown remained in progress.
- [ ] `status` shows `CANCELLED` and a recorded cancellation request.
- [ ] The machine remains on after the original five-minute window.
- [ ] The acceptance record contains no sensitive data.

Passing the cancel case raises no claim above C3. It does not prove that the
machine can power off.

For the power-off case, use the same reviewed artifact on a non-critical test
machine or recoverable VM, but allow the five-minute countdown to finish. After
starting the machine again, run:

```powershell
.\bin\donethen.exe reconcile <job-id>
.\bin\donethen.exe status <job-id>
```

- [ ] The machine visibly powered off rather than merely ending the agent turn.
- [ ] The boot identity changed.
- [ ] `reconcile` did not schedule or retry any action.
- [ ] Platform evidence justifies `ACTION_EXECUTED_CONFIRMED`; otherwise the
      result remains `ACTION_EXECUTION_UNVERIFIED` and the capability does not
      advance.
- [ ] A trusted final Stop-arbitration provider is installed, validated, and
      reports `execute_ready_by_policy.after_stop=true` before any plugin power
      test or C5 claim.
- [ ] A separate current-task Plugin after-stop execute run is completed before
      claiming C5.

The plugin-path case is currently blocked and MUST NOT be attempted while
`execute_ready_by_policy.after_stop=false`. Once a separately reviewed trusted
provider makes that field true, install the reviewed artifact through the
development or release plugin workflow, open a new Codex task, inspect and
trust the exact DoneThen Hooks, then explicitly request an `after_stop` execute
job with a five-minute delay. The prompt must acknowledge that Stop is not
proof of task success. Record the returned job ID.

- [ ] `PostToolUse(arm)` bound the expected session, turn, and workspace.
- [ ] A normal `Stop` with `stop_hook_active=false` advanced the same job.
- [ ] The detached supervisor scheduled a five-minute countdown only after that
      Stop.
- [ ] Sending a new prompt during one run cancelled the countdown and the
      machine stayed on.
- [ ] A separate authorized run powered off and reconciled without retrying the
      action after boot.
- [ ] Other user, project, managed, and plugin Hook definitions were unchanged.

If any observation is ambiguous, run the following emergency cancellation and
do not publish the release:

```powershell
& "$env:SystemRoot\System32\shutdown.exe" /a
```

## 4. Tag and artifact review

- [ ] The version follows `vMAJOR.MINOR.PATCH` or a SemVer prerelease such as
      `v0.1.0-alpha`.
- [ ] The tag points to the reviewed release commit.
- [ ] Pushing the tag completes `.github/workflows/release.yml`.
- [ ] The release has archives for Windows, Linux, and macOS on `amd64` and
      `arm64`; the archive set matches the release workflow allowlist exactly.
- [ ] Every archive contains `README.md`, `LICENSE`, `CHANGELOG.md`,
      `CAPABILITIES.json`, and `BUILDINFO.txt`; Linux alone also contains the
      helper, plan-first installer, service, and tmpfiles definition.
- [ ] Windows `amd64` `donethen.exe --version` matches the tag without `v`.
- [ ] Every `BUILDINFO.txt` reports the tagged `vcs.revision`,
      `vcs.modified=false`, and retained build metadata.
- [ ] Every published archive hash matches the combined `SHA256SUMS.txt`.
- [ ] Every archive has exactly one matching `.spdx.json` asset whose SPDX 2.3
      file checksum matches the downloaded archive.
- [ ] `gh attestation verify <archive> --repo andyandymike/done-then` succeeds
      for every archive.
- [ ] A prerelease tag is marked as a GitHub prerelease and not Latest.
- [ ] Release notes state that Windows is unsigned, macOS is not signed or
      notarized, and portable packages do not imply accepted power support.

## 5. Post-release

- [ ] Exercise each native archive on its matching architecture; include a
      Windows path containing spaces and a Unicode path.
- [ ] Run the Linux installer without `--apply` and confirm it writes nothing.
- [ ] Run a dry-run using the published binary; do not repeat the real action
      merely to smoke-test the download.
- [ ] Confirm the GitHub Security tab links to `SECURITY.md`.
- [ ] Update the changelog comparison links for the new release.
- [ ] If any release asset is wrong, withdraw the release and publish a new
      prerelease version; never silently replace a published binary.
