## Summary

Describe the user-visible outcome and why the change is needed.

## Safety boundary

Describe any effect on completion gates, cancellation, persistence, process
execution, logging, or the Windows action backend. Write `None` if there is no
effect.

## Verification

List the exact commands and manual checks performed.

- [ ] `go fmt ./...`
- [ ] `go test -count=1 ./...`
- [ ] `go vet ./...`
- [ ] `go build ./...`
- [ ] Automated tests did not invoke a real power action.
- [ ] Public docs were updated for user-visible behavior.
- [ ] Remaining manual or external proof is stated explicitly.

## Sensitive data

- [ ] This change contains no prompt, transcript, credential, environment
      dump, private source, or local implementation spec.
