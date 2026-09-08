# Contributing to GideonDB

Thank you for helping improve GideonDB. The project welcomes focused bug fixes,
tests, documentation, design discussion, and carefully measured performance
work.

## Before opening a change

For security vulnerabilities, follow [SECURITY.md](SECURITY.md) and do not open
a public issue. For substantial API, storage-format, consensus, or dependency
changes, open a proposal issue before implementation. Small fixes may go
directly to a pull request.

Keep changes focused. Add or update tests for behavior changes, document public
interfaces and operational consequences, and update `PENDING_DEVELOPMENT.md`
when completing or introducing deferred work.

## Development checks

Use the supported Go version declared in `go.mod`. Before submitting, run the
checks relevant to your change. The full local baseline is:

```sh
make test
make vet
make test-proto-contract
git diff --check
```

Changes affecting concurrency, persistence, replication, or unsafe memory use
should also run `make race`. Kubernetes changes must pass
`make validate-kubernetes` in an environment with Docker, kubectl, and kind.

## Pull requests

Explain the problem, the chosen behavior, compatibility and security impact,
and validation performed. Avoid unrelated formatting or generated-file churn.
Generated protobuf bindings must match their source schema.

Maintainers may request changes or close work that conflicts with the published
architecture, security model, compatibility policy, or project scope. Reviews
focus on correctness, recoverability, bounded resource use, compatibility,
operability, and test evidence.

## Developer Certificate of Origin

GideonDB uses the [Developer Certificate of Origin 1.1](https://developercertificate.org/)
instead of a Contributor License Agreement. Sign off every commit to certify
that you have the right to submit it under the project license:

```sh
git commit -s -m "Describe the change"
```

The sign-off line must use your real or otherwise legally attributable identity:

```text
Signed-off-by: Your Name <you@example.com>
```

By signing off, you agree to the DCO 1.1. Contributions without a valid sign-off
must be amended before merge.

## Conduct

Participation is governed by [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
