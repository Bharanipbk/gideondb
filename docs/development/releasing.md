# Releasing GideonDB

## Cadence

- **Minor releases (`v0.N.0`)** roughly every four weeks while GideonDB is
  pre-1.0. A minor release may contain documented breaking changes; the
  [compatibility contract](../operations/compatibility.md) lists what stays
  readable.
- **Patch releases (`v0.N.P`)** whenever a fix for data loss, a security
  issue or a crash is ready. Patches never change persisted formats or remove
  API fields.
- **Pre-releases (`v0.N.0-rc.1`)** before a minor release that changes a
  persisted format or the cluster protocol.

All `v0.*` and hyphenated tags are published as GitHub pre-releases.

## Changelog discipline

Every pull request that changes behaviour adds a line under
`## Unreleased` in [CHANGELOG.md](../../CHANGELOG.md), in one of `Added`,
`Changed`, `Fixed`, `Removed` or `Security`. Write it for users: what they
can now do or must now do, not how the code changed. Call out format bumps
and breaking changes explicitly.

## Cutting a release

1. Make sure `main` is green in CI.
2. In `CHANGELOG.md`, rename `## Unreleased` to `## 0.N.P — YYYY-MM-DD` and
   add a new empty `## Unreleased` above it. Commit to `main`.
3. Tag and push:

   ```bash
   git tag -s v0.N.P -m "GideonDB v0.N.P"
   git push origin v0.N.P
   ```

4. The [release workflow](../../.github/workflows/release.yml) then:
   - runs `go vet` and `go test`;
   - builds `gideondb` and `gideondb-mcp` for Linux, macOS and Windows on
     amd64 and arm64, with `checksums.txt`;
   - creates the GitHub release with the changelog section as notes, and
     fails if that section is missing;
   - pushes `ghcr.io/bharanipbk/gideondb:<version>`, `:<major>.<minor>` and
     `:latest` for `linux/amd64` and `linux/arm64`.
5. Check the release page and pull the image once:

   ```bash
   docker run --rm ghcr.io/bharanipbk/gideondb:0.N.P -version
   ```

SDK packages are published separately; see
[SDK publishing](sdk-publishing.md).

## One-time repository setup

- Make the `gideondb` package on GHCR public after the first image push
  (GitHub → Packages → gideondb → Package settings → Change visibility).
- Enable GitHub Discussions so the issue-template contact link works.
- Create the triage labels listed in [CONTRIBUTING.md](../../CONTRIBUTING.md).
