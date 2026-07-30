# Security Policy

DoneThen can schedule operating-system power actions. Treat reports that could
cause an action without explicit authorization, bypass a completion gate,
prevent cancellation, execute model-controlled commands, or expose task data
as security-sensitive.

## Supported versions

DoneThen has not published its first release. Until then, only the current
`main` branch receives security fixes. This policy will be updated when the
first tagged release is available.

## Reporting a vulnerability

Please do not disclose a suspected vulnerability in a public issue, pull
request, discussion, log, or screenshot.

Use GitHub's private vulnerability reporting entry under the repository's
Security tab. If that entry is unavailable, contact the repository owner
through the private contact method listed on the
[@andyandymike GitHub profile](https://github.com/andyandymike) before sharing
technical details.

Include only what is needed to reproduce and assess the problem:

- affected commit or version;
- Windows version and architecture;
- exact DoneThen arguments with prompts, tokens, usernames, and paths redacted;
- expected and observed state transitions;
- whether Windows accepted a shutdown countdown;
- safe reproduction steps using dry-run or the fake backend whenever possible.

Do not test a report against someone else's machine, account, repository, or
active task. Do not attach authentication tokens, full prompts, transcripts,
environment dumps, or proprietary source code.

The maintainer will acknowledge a report when it is received, coordinate a
fix and disclosure timeline, and credit reporters who request attribution.
Exact response times are not guaranteed during the pre-alpha period.

## Safe research boundary

Automated security tests must not invoke the real Windows action backend.
Tests that reach the shutdown boundary must inject a fake backend and assert
the resulting executable and argv. Any real countdown test requires explicit
authorization, a non-critical Windows test machine, at least a five-minute
delay, and immediate cancellation verification.
