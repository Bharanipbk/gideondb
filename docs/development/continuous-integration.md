# Continuous integration

The required GitHub Actions workflow is `.github/workflows/ci.yml`. It runs on
pull requests, pushes to `main`, and manual dispatch with read-only repository
permissions and per-ref cancellation of obsolete runs.

The workflow enforces:

- Go formatting, `go vet`, pinned Staticcheck, and all unit tests;
- the race detector plus uncached REST and cluster integration suites;
- a bounded metadata-filter parser fuzz smoke test;
- protobuf structure, additive wire compatibility, Buf lint/format, and
  generated-binding reproducibility;
- OpenAPI structure and repository-local Markdown links;
- Python SDK and framework integrations, TypeScript, Java, Rust, and .NET SDKs,
  plus dashboard JavaScript syntax and pinned desktop/mobile Chromium
  interaction tests; and
- two byte-identical, trimmed Linux builds with fixed version metadata.

Staticcheck runs all checks except package-comment diagnostics for generated
protobuf code, preservation of existing Raft error capitalization, and two
style-only simplification rules (`ST1000`, `ST1005`, `S1008`, and `S1011`). The
exclusions do not disable correctness checks.

The Kubernetes gate remains manual because it needs a functional Docker daemon,
kind cluster, CNI behavior, and storage lifecycle. Run it separately with:

```sh
make validate-kubernetes
```

Developers can reproduce most CI jobs through `make test`, `make vet`,
`make race`, and `make test-proto-contract`. Contract-specific scripts are
dependency-free and may be run directly from `scripts/`.
