# ECS implementation plan

Status: proposed implementation sequence; no cloud resources have been created
and no production qualification gate has passed.

This is the engineering backlog for the
[production deployment execution plan](production-deployment-plan.md). The
[AWS ECS storage plan](aws-ecs-storage-plan.md) remains the topology design.
Start with a single-node Fargate/EFS evaluation, qualify persistence, then
extend to three stable nodes. Storage selection for production remains subject
to measured workload results.

## 1. Release evidence automation

First implementation slice: add `scripts/collect-release-evidence.sh`, a
fixture-based test script, and a `make release-evidence` target. Produce a
versioned JSON manifest and checksum inventory in an explicit output directory.
Record commit, dirty-tree status, toolchain, platform, build version, supplied
image digest, and references/checksums for CI results, SBOM, and scan reports.
Never collect environment variables or secret-bearing runtime configuration.

Separate collection from qualification: incomplete evidence may be collected,
but a strict qualification mode must fail on a dirty tree, missing artifacts,
malformed image digest, unsuccessful required checks, or absent signature
verification evidence. Do not infer vulnerability approval from a report's
existence. Define supported scanner report formats and policy thresholds before
implementing automatic scan qualification. Use existing CI checks rather than
creating a second test matrix.

Acceptance: fixtures cover complete, missing, malformed, and failed evidence;
manifest output is deterministic for identical inputs; credentials never appear
in output; a local draft can be generated without Docker or AWS. Signing,
publishing, SBOM generation, and scanning remain explicit pipeline steps, not
claims made by the collection script.

## 2. Exclusive data-directory ownership

Inspect startup and shutdown in `cmd/gideondb/main.go` and storage initialization
before adding an ownership guard. Acquire ownership before any catalog, identity,
WAL, or metadata mutation, retain it for the process lifetime, and release it
after shutdown. Cover offline migration and other commands that mutate a data
directory. A PID file alone is insufficient across hosts.

Acceptance: a second process fails before modifying data; forced termination
permits safe recovery; distinct directories remain independent. Local process
tests are necessary but do not qualify EFS. Verify the chosen locking mechanism
on the actual shared filesystem and under mount interruption. Do not enable
overlapping replacements until that behavior is demonstrated; if filesystem
locking cannot guarantee exclusion, design external fencing before proceeding.

## 3. Single-node evaluation infrastructure

Proposed new directory: `deployments/ecs/`, containing infrastructure as code,
example non-secret inputs, validation instructions, and an operator runbook.
Select the infrastructure tool before introducing provider dependencies.

Implement private networking, node-specific encrypted EFS storage, stable
discovery, narrowly scoped roles, secret delivery, TLS ingress, centralized logs,
and a task definition mounting `/var/lib/vectordb`. Reference an immutable image
digest. Design replacement sequencing to preserve exclusive ownership; desired
count one alone does not prevent deployment overlap. Check current AWS task,
volume, secret-delivery, and deployment semantics during implementation.

Acceptance: static infrastructure validation and policy checks pass; a reviewed
plan identifies all resources and costs; an evaluation deployment survives task
replacement with identical node identity, catalog, records, and durable state.
Retain sanitized evidence. Provisioning requires a selected account, region,
network, budget, and secret references.

## 4. Three-node acceptance harness

Extend the infrastructure to three independently addressable services with
separate data locations. Reuse the documented membership workflow and existing
cluster acceptance assertions where possible. Add `scripts/ecs-gate.sh` and a
manual `make validate-ecs` target; require an explicit evaluation environment.
Bound every wait and preserve diagnostics on failure.

Acceptance: membership and placement converge; distributed CRUD, search, and
scroll succeed; leader replacement recovers; the majority progresses during
one-node failure; a minority rejects unsafe writes; replacements retain the
correct storage mapping. Report before/after counts, checksums, and recovery
duration. Fault injection must target only the designated evaluation resources.

## 5. Backup delivery and recovery drills

Wrap the existing application-consistent backup interfaces with delivery of
completed, checksummed archives to encrypted, versioned S3 storage. Specify
retention, recovery credentials, and object permissions. Avoid live-directory
copies. Restore into fresh empty destinations through the existing restore flow.

Acceptance: active-write backup and fresh-topology restore preserve the expected
records and search results; corrupt and truncated archives are rejected; upload
failure cannot advertise a completed backup; measured RPO/RTO meets the chosen
thresholds. Record the source recovery point and destination topology.

## 6. External observability and workload qualification

Extend [observability](observability.md) with deployment-specific metric
collection, log retention, dashboards, alert routing, and runbooks. Inventory
existing telemetry before writing alert expressions. Trace context in logs is
already available; OTLP export remains separate implementation work and must
not be represented as supported until implemented and tested.

Use `cmd/gideondb-loadtest` as a starting point for a versioned workload and
bounded soak harness. Add coverage for checkpoint, backup, replacement, repair,
rolling upgrade, and rollback. Record raw measurements and service-level
thresholds; compare EFS and EBS only with equivalent workloads and durability
settings. Add instrumentation where a required signal does not yet exist.

Acceptance: alert tests reach the selected destination; metrics and logs survive
task loss; workload and failure runs meet approved latency, error, resource,
RPO, and RTO thresholds. Failed or unexecuted gates remain visibly incomplete.

## Dependencies and decisions

Steps 1 and 2 can be implemented and tested locally. Step 3 depends on ownership
qualification and environment inputs; step 4 depends on a passing single-node
replacement drill. Steps 5 and 6 culminate in the production plan's release
gates, rather than granting production approval themselves.

Before cloud deployment, record the infrastructure tool, AWS account/region,
network and DNS ownership, secret and KMS references, budget, workload shape,
latency objectives, RPO/RTO, retention, and operational owners. Do not invent
acceptance thresholds to make a gate pass.

Kubernetes qualification remains separately pending for that supported target.
Its execution is not evidence that ECS is qualified, and this plan does not
silently close or remove its release checklist item.
