# Publishing SDK packages

The SDKs are prepared for an initial **0.1.0 experimental release**. Packaging
configuration does not mean these names are available or that publication has
occurred. These are client libraries; users still run a separate GideonDB server.

| Language | Registry | Package identity |
|---|---|---|
| JavaScript / TypeScript | npm | `@gideondb/client` |
| Python | PyPI | `gideondb-client` (import `gideondb`) |
| Rust | crates.io | `gideondb-client` (import `gideondb_client`) |
| .NET | NuGet | `GideonDB.Client` |
| Java | Maven Central | `io.github.bharanipbk:gideondb-client` |
| Go | Go module proxy / pkg.go.dev | `github.com/Bharanipbk/gideondb/pkg/client` |

## One-time account setup

Confirm ownership or availability of each package identity before releasing.
If a name is occupied, change its manifest and documentation before tagging.
For npm, create or obtain publishing access to the `gideondb` organization/scope.
For Maven, verify `io.github.bharanipbk` in the Central Portal; the Java source
package remains `io.gideondb.client` and is independent of Maven coordinates.

Create GitHub environments `npm`, `pypi`, `crates`, `nuget`, `maven`, and `go` in
`Bharanipbk/gideondb`. Restrict publishing environments to release tags and set
reviewers according to your release policy. Build-only runs use `sdk-validation`
and require no publishing credentials. Store secrets in GitHub environment
settings; never commit them or send their values in chat.

- **npm:** configure a GitHub trusted publisher on the package with owner
  `Bharanipbk`, repository `gideondb`, workflow `publish-sdks.yml`, environment
  `npm`, and permission for direct `npm publish`. The workflow uses Node 24 and
  OIDC. For a new package without publisher settings, bootstrap the first
  version from the tested tarball with `npm login` followed by
  `npm publish dist/npm/gideondb-client-0.1.0.tgz --access public`, from the
  repository root. Complete the browser/2FA prompt yourself. Then configure
  trusted publishing for subsequent versions; do not republish 0.1.0 in CI.
- **PyPI:** register a pending publisher for `gideondb-client`, owner
  `Bharanipbk`, repository `gideondb`, workflow `publish-sdks.yml`, environment
  `pypi`. Existing package owners add a trusted publisher instead. No PyPI API
  token is needed by this workflow.
- **crates.io:** sign in, verify package ownership/availability, and create a
  publish-scoped token. Set environment secret `CARGO_REGISTRY_TOKEN`.
- **NuGet:** create an account and API key scoped to this package. Set environment
  secret `NUGET_API_KEY`.
- **Maven Central:** verify the namespace, generate a Central Portal user token,
  and set its username/password as `MAVEN_USERNAME` / `MAVEN_PASSWORD`. Create a
  release signing key, make its public key available as required by Central,
  and set the ASCII-armored private key as `GPG_PRIVATE_KEY` and passphrase as
  `MAVEN_GPG_PASSPHRASE`. The release profile signs the main, sources, Javadoc,
  and POM artifacts and publishes via the Central Portal plugin.
- **Go:** keep the repository public. Go uses the existing root module and its
  `vX.Y.Z` tags; it has no separate registry upload or API key. The module
  currently requires Go 1.26. Tagging releases the root module, including the
  server source, rather than a new standalone SDK module.

Official setup references: [npm trusted publishing](https://docs.npmjs.com/trusted-publishers/),
[PyPI pending publishers](https://docs.pypi.org/trusted-publishers/creating-a-project-through-oidc/),
[Cargo publishing](https://doc.rust-lang.org/cargo/commands/cargo-publish.html),
[NuGet publishing](https://learn.microsoft.com/en-us/nuget/nuget-org/publish-a-package),
[Central namespaces](https://central.sonatype.org/register/namespace/),
[Central Maven plugin](https://central.sonatype.org/publish/publish-portal-maven/),
and [Go module publishing](https://go.dev/doc/modules/publishing).

## Build before publishing

Commit the packaging changes and push the workflow to the default branch.
In GitHub Actions, select **Package and publish SDKs**, choose the intended
branch, `registry=all`, and leave `publish=false`. Download and inspect the
five package artifacts. The Go job tests the existing module. Run the normal
repository CI baseline on the same release commit as well.

The workflow tests each SDK and builds its distribution. Python checks wheel
and source metadata and imports the installed wheel. Maven runs the existing
assertion-based Java test program and creates source/Javadoc artifacts. Rust
verifies the packaged crate. A build-only Maven run does not exercise signing
or Central's remote validation. Actual registry authentication is exercised
only during publication.

Locally, `python3 scripts/check-sdk-release.py` verifies that all five manifest
versions agree. Python 3.11+ is required for this release helper (the SDK itself
supports Python 3.10+). `--tag v0.1.0` also verifies the tag matches. Future
releases must update all manifests and the Rust lockfile together; this workflow
currently supports coordinated stable `X.Y.Z` versions only. The SDK API remains
experimental despite using a non-prerelease version number.

## Publish the tested commit

After CI passes and registry setup is complete, create an annotated tag on the
exact tested commit and push that tag. For the first release, the tag is
`v0.1.0`. **Pushing a public Go version tag already makes that module version
available**; the workflow's Go publication step only requests proxy indexing.
Do not move or reuse a published tag.

Run **Package and publish SDKs** from that tag with `publish=true`. Select one
registry for the first attempts; `all` is convenient only after every registry
is configured. The workflow rejects publishing from a branch or a mismatched
tag. It does not create tags or push commits for you.

Registry publication is not atomic across languages. If one fails, fix its
account/configuration and rerun only that registry. Do not rerun successful
uploads; registries generally prohibit overwriting a version. If package
contents must change after any successful upload, prepare a new version and
new tag. A successful Maven upload is followed through to published state.

## Verify installation after publication

Use fresh directories/environments and the public registry for each check:

```sh
npm install @gideondb/client@0.1.0
python -m pip install gideondb-client==0.1.0
cargo add gideondb-client@0.1.0
dotnet add package GideonDB.Client --version 0.1.0
go get github.com/Bharanipbk/gideondb/pkg/client@v0.1.0
```

For Maven, use the dependency coordinates
`io.github.bharanipbk:gideondb-client:0.1.0`. Confirm Java import
`io.gideondb.client.GideonDBClient`. Check registry pages, installed version,
license, README, exported API, and a request against an evaluation server.
Record the release tag and resulting registry links. Indexing may lag an upload;
inspect the registry status before deciding that a release failed.

## Local preparation evidence

On 2026-10-15, Python, JavaScript, Rust, Java, .NET, and Go SDK tests passed.
Built npm tarball, Python wheel/sdist, verified Rust crate, NuGet package, and
Maven main/source/Javadoc jars. Python metadata checks and installed npm/Python
smoke checks passed. Maven emitted missing-Javadoc-comment warnings. Workflow
static validation, matching/mismatched tag checks, and Markdown links passed.
Unsigned local builds do not verify Maven signing, registry authentication,
GitHub-hosted workflow execution, or public installation after publication.
