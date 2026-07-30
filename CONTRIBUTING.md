# Contributing to DoneThen

Thank you for helping make post-task automation safer and more predictable.
DoneThen is pre-alpha, so small, reviewable changes with explicit evidence are
preferred over broad compatibility claims.

By submitting a contribution, you agree that it may be distributed under the
Apache License 2.0.

## Development setup

The current target is Windows 10/11 `amd64` with Go 1.26 or newer.

```powershell
go version
go test -count=1 ./...
go vet ./...
go build ./...
```

Format Go changes before submitting them:

```powershell
go fmt ./...
```

## Safety requirements

- Never invoke `shutdown.exe /s` from an automated test, CI workflow, or
  ordinary reproduction script. Documentation that reaches the real backend
  must be confined to the explicitly authorized manual release checklist.
- Exercise the action boundary through `actions.FakeBackend` or an injected
  `ProcessRunner`.
- Preserve dry-run as the default.
- Unknown, malformed, interrupted, partial, blocked, approval-waiting, or
  failed outcomes must remain fail-closed.
- Do not accept an action executable, action type, delay, comment, or argv from
  model output.
- Keep cancellation available and do not add forced shutdown.
- Do not log prompts, transcripts, environment variables, model response
  bodies, tokens, or file contents.
- A new action or platform backend requires a separate design, tests, and an
  explicit trust-boundary review.

Real shutdown testing belongs only in an explicitly authorized manual release
check on a non-critical Windows machine. See
[`docs/release-checklist.md`](docs/release-checklist.md).

## Pull requests

Describe:

1. the behavior or failure mode being changed;
2. the safety boundary affected;
3. tests and commands run;
4. any proof that still requires manual or external validation.

Keep unrelated changes out of the same pull request. Update public docs when a
user-visible contract changes. Repository-local implementation specs under
`spec/` and `specs/` are intentionally ignored; do not force-add them.

Security vulnerabilities must follow [`SECURITY.md`](SECURITY.md), not the
normal issue or pull-request process.
