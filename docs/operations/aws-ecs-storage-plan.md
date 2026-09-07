# Future AWS ECS storage plan

Status: **planned future work**. This document is design guidance only. No ECS
infrastructure, task definition, automation, or runtime behavior described here
is implemented by this document.

Use this plan after the current Kubernetes validation, gRPC server, and release
readiness work is complete.

## Objective

Run VectorDB in Amazon ECS without losing its catalog, node identity, WAL,
segments, snapshots, operational event log, or Raft metadata when a container
or host is replaced. Use one stable internal mount point:

```text
/var/lib/vectordb
```

Configure VectorDB's data path to that directory. The exact task definition and
infrastructure configuration will be developed separately.

## Non-negotiable storage rule

Each VectorDB process must have exclusive ownership of its data directory:

```text
one VectorDB node -> one node identity -> one exclusive data location
```

Never allow multiple VectorDB nodes to open the same EFS directory or EBS file
system. Sharing one data directory can corrupt WAL, segment, ownership, catalog,
and Raft state even when the underlying filesystem supports locking.

## Storage choices

| Deployment | Storage | Intended use |
| --- | --- | --- |
| Fargate ephemeral volume | Task-local ephemeral storage | Disposable tests only |
| Fargate with EFS | Dedicated EFS access point per node | Evaluation and modest persistent workloads |
| ECS on EC2 with EBS | Dedicated encrypted gp3 volume per node | Preferred performance-oriented production option |
| Amazon S3 | Completed VectorDB backup archives | Backup retention, not a live data directory |

Fargate ephemeral storage must not hold the only durable copy of database data.
ECS service-managed EBS volumes must also be treated cautiously because their
lifecycle can follow task termination. Recheck current AWS lifecycle semantics
during implementation.

## Stage 1: single-node evaluation

Start with one ECS Fargate service and one task:

```text
ECS service, desired count 1
        |
        v
VectorDB container
        |
        v
EFS access point /vectordb/single
```

Recommended initial posture:

- Regional encrypted EFS;
- General Purpose performance mode;
- Elastic throughput mode;
- one EFS access point rooted at `/vectordb/single`;
- mount it at `/var/lib/vectordb` in the container;
- encryption in transit enabled;
- EFS mount targets in the task subnets;
- NFS port 2049 allowed only between the task and EFS security groups;
- ECS desired count fixed at one; and
- application-level backups copied to a separate S3 bucket.

This topology survives ordinary task replacement, but EFS adds network
filesystem latency to WAL synchronization, checkpoint publication, segment
access, and recovery.

## Stage 2: three-node Fargate evaluation

Use three independently named ECS services rather than three interchangeable
tasks in one service:

```text
vectordb-node-a -> /vectordb/node-a
vectordb-node-b -> /vectordb/node-b
vectordb-node-c -> /vectordb/node-c
```

Each service should have:

- desired count one;
- its own EFS access point and POSIX identity;
- a persistent VectorDB node identity in that access point;
- a stable private service-discovery name;
- placement in a distinct Availability Zone where practical;
- the other stable service names configured as peers;
- separate health and readiness checks; and
- no ECS autoscaling until stateful node replacement is explicitly designed.

Task replacement must remount the same node-specific access point and must not
adopt another node's identity or data.

## Stage 3: performance-oriented production

For sustained ingestion, frequent WAL synchronization, or large mmap-backed
segments, prefer ECS on EC2 with one encrypted gp3 EBS volume per node:

```text
Availability Zone A: node-a -> EBS volume-a
Availability Zone B: node-b -> EBS volume-b
Availability Zone C: node-c -> EBS volume-c
```

This requires more orchestration than EFS:

- constrain each task to the correct capacity and Availability Zone;
- prevent two tasks from mounting the same writable volume;
- preserve the volume when replacing its EC2 host;
- safely detach and reattach the volume during recovery;
- wait for attachment and filesystem checks before starting VectorDB;
- associate node identity with the volume rather than the host;
- provision gp3 IOPS and throughput from measured results; and
- document recovery when a node's Availability Zone is unavailable.

EBS is confined to an Availability Zone. VectorDB replication across nodes and
Availability Zones, plus tested backups, supplies the higher-level durability
model; one EBS volume is not a multi-AZ database.

## Backup and restore

Use two complementary layers:

1. **Application-consistent backups:** invoke VectorDB backup coordination and
   copy only completed, checksummed archives to versioned, encrypted S3 storage.
2. **Infrastructure recovery copies:** use AWS Backup for EFS or scheduled EBS
   snapshots as a secondary disaster-recovery layer.

Application-consistent archives are the primary restore format. Do not copy the
live data directory or assume an arbitrary filesystem snapshot is consistent
unless writes were frozen and VectorDB's recovery fences were observed.

Define these requirements before implementation:

- recovery point objective and recovery time objective;
- backup frequency and retention tiers;
- cross-account or cross-Region copy requirements;
- KMS key ownership and recovery procedure;
- restore-test frequency; and
- archive deletion and legal-retention policy.

## Security plan

- Run the container as its existing non-root user.
- Apply EFS access-point UID/GID permissions or EBS filesystem ownership before
  starting the database.
- Encrypt EFS, EBS, snapshots, and S3 archives with approved KMS keys.
- Keep ECS tasks in private subnets.
- Restrict database ingress to approved application security groups.
- Restrict NFS 2049 to the EFS and task security groups.
- Keep API keys, TLS certificates, and internal mTLS material in AWS Secrets
  Manager or Systems Manager Parameter Store, not in the image.
- Grant the task role only the S3, KMS, secret, and discovery permissions its
  node requires.
- Send container stdout/stderr to CloudWatch Logs rather than the data volume.

## Observability

Monitor and alarm on:

- task replacements and restart loops;
- filesystem capacity and inode exhaustion;
- WAL write and synchronization latency;
- checkpoint failures and duration;
- recovery duration;
- EFS throughput, I/O limits, and client connections;
- EBS queue depth, latency, IOPS, throughput, and burst balance;
- replication lag and repair operations;
- Raft leadership changes and metadata convergence;
- backup age and failures; and
- restore-drill duration and correctness.

Alarms should distinguish database failure from storage throttling, task
replacement, network isolation, and unavailable quorum.

## Required benchmarks

Run the same dataset and durability settings against each candidate:

- sustained and burst batch ingestion;
- WAL `always` synchronization latency;
- checkpoint throughput and pause behavior;
- cold restart and WAL replay time;
- flat and HNSW search at representative concurrency;
- mmap segment scans and compaction;
- backup and restore throughput;
- task termination during writes; and
- node loss and replica repair.

Record p50, p95, and p99 latency, throughput, error rate, CPU, memory, storage
metrics, dataset size, dimensions, shard count, and replication factor. Do not
choose EFS only for convenience if its WAL or mmap latency fails the workload's
service-level objectives.

## Failure drills

Before production approval, demonstrate:

1. replacement of a single-node Fargate task with its data intact;
2. prevention of concurrent writers to one data location;
3. restart after forced termination during WAL activity;
4. replacement of one node in a three-node cluster;
5. continued majority progress during one-node failure;
6. recovery after an EFS mount interruption;
7. EBS detach and reattach to a replacement EC2 host;
8. restore from the newest valid archive into an empty destination;
9. rejection of corrupted or incomplete archives; and
10. recovery using documented credentials and KMS procedures.

## Future implementation checklist

- [ ] Select Fargate plus EFS or ECS-on-EC2 plus EBS from benchmark evidence.
- [ ] Define stable node names, identities, discovery records, and storage maps.
- [ ] Create encrypted storage and node-specific mount/access-point policy.
- [ ] Create private networking, security groups, and mount targets.
- [ ] Create least-privilege ECS task and infrastructure IAM roles.
- [ ] Store external and internal authentication material securely.
- [ ] Create task definitions with `/var/lib/vectordb` as the data mount.
- [ ] Prevent two writers from starting against one volume.
- [ ] Configure three independently addressable nodes for distributed mode.
- [ ] Add CloudWatch dashboards and actionable alarms.
- [ ] Automate application-consistent archive delivery to S3.
- [ ] Configure secondary AWS Backup or EBS snapshot policy.
- [ ] Run performance, restart, partition, replacement, backup, and restore gates.
- [ ] Write the ECS operator runbook and rollback procedure.
- [ ] Complete security and cost reviews.
- [ ] Approve production use only after measured acceptance criteria pass.

## Decision summary

Use Fargate with a dedicated EFS access point for the first persistent
evaluation. For a distributed Fargate evaluation, use three one-task services
and three isolated access points. Prefer ECS on EC2 with dedicated gp3 EBS
volumes when measured EFS latency is unacceptable. In every topology, preserve
exclusive data-directory ownership, keep replicas in separate failure domains,
and store verified application-consistent backups in S3.
