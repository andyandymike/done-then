# Release checklist

This checklist applies to the first DoneThen Windows alpha and later releases.
It does not authorize a release by itself. The maintainer performing the
release owns every manual confirmation below.

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

- [ ] Every command exits successfully.
- [ ] Tests at the action boundary use only the fake backend or injected fake
      process runner.
- [ ] A clean checkout passes `.github/workflows/ci.yml`.

## 3. Manual countdown-and-cancel acceptance

This section intentionally reaches the real Windows shutdown backend. Run it
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
- [ ] The zip contains exactly `donethen.exe`, `README.md`, `LICENSE`, and
      `CHANGELOG.md`.
- [ ] `donethen.exe --version` matches the tag without the leading `v`.
- [ ] `go version -m donethen.exe` reports the tagged `vcs.revision`,
      `vcs.modified=false`, and retained build metadata.
- [ ] The published zip hash matches `SHA256SUMS.txt`.
- [ ] `gh attestation verify <zip> --repo andyandymike/done-then` succeeds.
- [ ] A prerelease tag is marked as a GitHub prerelease and not Latest.
- [ ] Release notes say that the Windows binary is unsigned.

## 5. Post-release

- [ ] Install the published zip into a path containing spaces and run
      `donethen.exe --version` and `donethen.exe help`.
- [ ] Run a dry-run using the published binary; do not repeat the real action
      merely to smoke-test the download.
- [ ] Confirm the GitHub Security tab links to `SECURITY.md`.
- [ ] Update the changelog comparison links for the new release.
- [ ] If any release asset is wrong, withdraw the release and publish a new
      prerelease version; never silently replace a published binary.
