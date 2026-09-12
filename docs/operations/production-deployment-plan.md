# Production deployment execution plan

**Status:** Planned; production approval is blocked until every required gate
below has recorded evidence.

This plan turns the remaining production-readiness work into an ordered release
process. It targets AWS ECS because that is the intended deployment platform.
Use the detailed [AWS ECS storage plan](aws-ecs-storage-plan.md) for storage and
topology decisions and the [staged adoption guide](../getting-started/adoption.md)
for application rollout.

## Target topology

Start with a non-production, three-node cluster that matches the intended
production shape:

- one GideonDB process and one exclusive data location per node;
- three independently addressable nodes distributed across failure domains;
- one shared cluster ID, compatible protocol versions, and identical collection
  definitions;
- replication factor selected from measured durability and latency results;
- private node-to-node networking with mutual TLS;
- public API and dashboard access only through a TLS load balancer;
- credentials supplied from a secret manager, never from the image or task
  definition plaintext; and
- application-consistent backups delivered to a separate S3 recovery boundary.

For the first persistent evaluation, use Fargate with one dedicated EFS access
point per task. For a performance-oriented production candidate, prefer ECS on
EC2 with one encrypted gp3 EBS volume per stable node when benchmark evidence
shows that EFS WAL or mmap latency cannot meet the workload objective.

## Gate 1: release candidate

Before infrastructure deployment:

- build from a signed, immutable commit and publish an image by digest;
- run the repository CI baseline, including unit, race, integration, SDK,
  compatibility, and dashboard suites;
- generate an SBOM and scan the image and dependencies for known critical or
  high-severity vulnerabilities;
- record the GideonDB version, Go version, image digest, configuration checksum,
  and persistent-format compatibility range; and
- keep rollback images and migration instructions available.

**Pass evidence:** CI URL or archived output, image digest, SBOM, scan report,
and release manifest.

## Gate 2: infrastructure qualification

Provision the candidate environment with infrastructure as code. Validate:

- storage encryption, measured IOPS/throughput/latency, mount or attachment
  recovery, and exclusive-writer enforcement;
- private subnets, security groups, DNS discovery, load-balancer health checks,
  and failure-domain separation;
- least-privilege task, infrastructure, backup, and KMS roles;
- Secrets Manager or equivalent delivery for API, dashboard, and TLS material;
- task CPU, memory, file-descriptor, ephemeral-space, and shutdown budgets; and
- NetworkPolicy-equivalent restrictions for every public and internal port.

**Pass evidence:** reviewed infrastructure plan, access-policy report, storage
benchmark, connectivity matrix, and successful replacement of each task.

## Gate 3: cluster acceptance

Bootstrap the first durable voter and add the remaining nodes through the
documented join workflow. Do not initialize independent voter sets. Confirm:

- all nodes report the same cluster ID, metadata epoch, membership, catalog,
  placement, and capacity manifests;
- readiness is true and every shard has the expected owners and replicas;
- write, read, filtered search, distributed search, scroll, and delete work
  through the client endpoint;
- a minority partition rejects unsafe progress while the majority continues;
- leader loss produces a bounded election and the replaced node catches up;
- replica repair and join/leave rebalancing complete without data loss; and
- graceful termination removes readiness before process exit.

**Pass evidence:** sanitized API responses, test results, event timeline, and
record-count/checksum comparison before and after each drill.

## Gate 4: workload qualification

Run a representative dataset, dimensions, filters, index type, request mix, and
concurrency for long enough to cover checkpoints, compaction, backup, and repair.
Measure p50, p95, and p99 latency, throughput, error rate, recall where HNSW is
used, CPU, memory, disk, network, WAL latency, checkpoint duration, compaction,
replication lag, and recovery time.

Define workload-specific service-level objectives before the run. A gate passes
only when the steady state and a one-node failure both stay within the approved
objectives without unbounded resource growth.

**Pass evidence:** versioned workload definition, raw results, summary report,
capacity headroom, and signed acceptance thresholds.

## Gate 5: backup and disaster recovery

Exercise application-consistent cluster backup while writes are active. Then:

- verify archive checksums and manifest completeness;
- restore into new empty data locations and new node identities;
- confirm collection configuration, record counts, sampled vectors, filters,
  search results, ownership, and replication state;
- reject corrupted, truncated, stale, and wrong-cluster inputs;
- measure recovery-point and recovery-time objectives; and
- repeat using only the operator runbook and stored recovery credentials.

**Pass evidence:** backup manifest, restore report, integrity comparison, RPO/RTO
measurement, and remediation notes.

## Gate 6: observability and operations

Export metrics, traces, and durable centralized logs outside the embedded
dashboard. Add alerts for readiness, quorum, leader churn, replication lag,
repair failure, disk capacity and latency, memory pressure, error rate, backup
age/failure, certificate expiry, and restore-drill age. Every alert must name an
owner and link to a tested runbook.

Complete rolling upgrade and rollback drills, credential and certificate
rotation, scale-out, scale-in, node replacement, capacity change, and on-call
handoff.

**Pass evidence:** dashboards, alert tests, runbook links, drill reports, and
named operational ownership.

## Gate 7: controlled release

Use a canary or low-risk workload first. Hold automatic expansion until the
observation window passes. Compare correctness, latency, resource use, and error
budgets against the previous system. Preserve a tested rollback path and do not
delete the previous data or recovery boundary during the rollback window.

Production approval requires sign-off from application, database, security,
infrastructure, and operations owners. Any failed gate returns the release to
the relevant earlier stage; it must not be waived only because a target date is
near.

## Execution record

Copy this table into the release issue and link immutable evidence.

| Gate | Owner | Status | Evidence | Approved on |
|---|---|---|---|---|
| Release candidate | Unassigned | Not started | — | — |
| Infrastructure qualification | Unassigned | Not started | — | — |
| Cluster acceptance | Unassigned | Not started | — | — |
| Workload qualification | Unassigned | Not started | — | — |
| Backup and disaster recovery | Unassigned | Not started | — | — |
| Observability and operations | Unassigned | Not started | — | — |
| Controlled release | Unassigned | Not started | — | — |

