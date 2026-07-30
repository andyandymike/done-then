---
title: DoneThen
---

# DoneThen

**Safe post-task actions for coding agents.**

DoneThen is an open-source, Windows-first safety layer for actions that should
happen only after a coding task is genuinely complete. Its primary direction
is a Codex plugin that combines a Skill, typed MCP tools, lifecycle Hooks, and
persisted evidence without asking the user to open a second watcher window.

> **Current safety status:** the plugin is observe-only. It can prove and
> display its lifecycle in dry-run mode, but it cannot invoke a power action.
> A `Stop` or `SessionEnd` event is never accepted as task completion by itself.

## Start here

- [Project overview](https://github.com/andyandymike/done-then#readme)
- [Local plugin development]({{ '/plugin-development.html' | relative_url }})
- [Release checklist]({{ '/release-checklist.html' | relative_url }})
- [Contributing](https://github.com/andyandymike/done-then/blob/main/CONTRIBUTING.md)
- [Security policy](https://github.com/andyandymike/done-then/security/policy)

## Safety model

```text
explicit user intent
  -> typed arm request
  -> task and turn binding
  -> structured completion evidence
  -> matching lifecycle observation
  -> external final gate
  -> narrow, cancellable action
```

Unknown, malformed, partial, blocked, approval-waiting, or failed states keep
the machine on. DoneThen does not accept arbitrary model-generated post-task
commands and does not overwrite existing user or plugin Hooks.

## Build the pre-alpha source

```powershell
git clone https://github.com/andyandymike/done-then.git
Set-Location done-then
go test -count=1 ./...
go vet ./...
go build -trimpath -o bin\donethen.exe ./cmd/donethen
```

There is no supported or signed binary release yet. Do not disable antivirus
protection or add broad exclusions for local pre-alpha builds.
