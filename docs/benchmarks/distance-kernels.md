# Distance kernels and HNSW insert throughput

Gap 19 work, 2026-10-21. Two changes:

1. **Faster distance kernels** in `internal/distance`.
   - On arm64, `dot` and `l2Squared` run whole 16-float blocks through NEON
     assembly (`kernel_arm64.s`: four 4-lane `FMLA` accumulators) and finish
     the tail in Go.
   - Every other platform, and arm64 built with `-tags purego`, uses a
     portable kernel with eight accumulators and bounds-check-free 8-float
     windows.
   - Cosine and squared norms are built from `dot`, so cached norms
     (`SquaredNorm`) and the cosine paths still agree bit for bit.
2. **Concurrent inserts into a live HNSW graph.** `(*hnsw.Index).UpsertBatch`
   links a batch of new vectors into an existing mutable graph with the same
   per-node locking that parallel `Build` uses.
   - Batches under 2,048 vectors, or `workers == 1`, take the serial path and
     produce byte-for-byte the graph that the same `Upsert` calls would. A
     test checks this.
   - Searches wait on the index lock until a batch is fully linked, so they
     never see a half-linked graph.
   - The graph layout and persisted format are unchanged; graphs built by a
     live batch round-trip through `MarshalGraph`/`LoadGraph`.

## Correctness

- `TestKernelsMatchReferenceForEveryLength` checks the active kernel (NEON
  on arm64) and the portable kernel against a float64 reference. It covers
  every length from 0 to 300 plus 768, 1024, 1536 and 4096. It also checks
  the two kernels against each other, so builds with and without assembly
  rank candidates the same way within float tolerance.
- `FuzzKernels` does the same on arbitrary finite inputs. It ran for 30 s
  (about 660,000 executions) without a failure:
  `go test -run x -fuzz FuzzKernels -fuzztime 30s ./internal/distance`.
- `go test -tags purego ./internal/distance` runs every test on the portable
  kernel.
- `go test -race ./internal/distance ./internal/index/hnsw` passes. The live
  batch test runs concurrent searches while a batch is linked.
- amd64 has no assembly yet. It runs the portable kernel, which
  `GOARCH=amd64 go vet` checks. AVX2 assembly needs an amd64 host to show a
  clear speed-up and to run the equivalence tests natively, so it is a
  follow-up.

## Results

Apple M3 (8 cores, 16 GiB), macOS 26.3 (Darwin 25.3.0), Go 1.26.5
darwin/arm64.

> **Noisy host:** other build jobs shared the machine during these runs
> (load average 20–75 on 8 cores). Treat ratios measured in the same run as
> reliable and absolute times as upper bounds.

### Kernels (ns/op, median of 6)

```bash
go test -run x -bench Kernels -count 6 -benchtime 200000x ./internal/distance
```

`legacy` is the previous four-lane Go kernel, kept in `kernel_test.go` for
this comparison. `generic` is the new portable kernel and `active` is what
the build uses (NEON here).

| Kernel | Dimension | legacy | generic | active (NEON) |
|---|---:|---:|---:|---:|
| dot | 100 | 79.9 | 57.9 (1.4×) | 31.6 (2.5×) |
| dot | 128 | 86.7 | 47.8 (1.8×) | 32.5 (2.7×) |
| dot | 384 | 327.2 | 117.1 (2.8×) | 53.2 (6.1×) |
| dot | 768 | 414.2 | 168.4 (2.5×) | 83.4 (5.0×) |
| dot | 1536 | 821.2 | 345.1 (2.4×) | 161.2 (5.1×) |
| l2 | 100 | 87.4 | 43.2 (2.0×) | 33.7 (2.6×) |
| l2 | 128 | 117.0 | 63.2 (1.9×) | 34.8 (3.4×) |
| l2 | 384 | 214.1 | 104.8 (2.0×) | 48.7 (4.4×) |
| l2 | 768 | 445.4 | 208.2 (2.1×) | 84.7 (5.3×) |
| l2 | 1536 | 2110.0 | 1196.0 (1.8×) | 304.4 (6.9×) |

### HNSW build and search, before and after

```bash
# "before" is a `git archive` of fdde2a8; both trees ran alternately, 3 rounds.
go test ./internal/index/hnsw -run x -bench 'Build|Search5K' -benchtime 3x
```

Medians of the three rounds. The ranges show how much the shared host moved
them.

| Benchmark | Before | After | Speed-up |
|---|---:|---:|---:|
| `BuildSerial` (10,000 × 384 cosine, 1 worker) | 13.4 s (9.3–63.6) | 4.2 s (3.1–7.5) | ≈3.2× |
| `BuildParallel` (10,000 × 384 cosine, 8 workers) | 4.8 s (3.0–11.2) | 1.28 s (0.91–1.38) | ≈3.8× |
| `Build2Kx128` (live `Upsert` loop) | 0.75 s (0.59–1.89) | 0.34 s (0.30–0.53) | ≈2.2× |
| `Search5Kx128` (per query) | 0.34 ms (0.20–1.14) | 0.16 ms (0.14–0.42) | ≈2.1× |

### Live batch insert

```bash
go test ./internal/index/hnsw -run x -bench LiveInsert -benchtime 3x
```

Adds 8,000 × 128 L2 vectors to a live graph that already holds 2,000.

| Benchmark | Time | Speed-up |
|---|---:|---:|
| `LiveInsertSerial` (`workers = 1`, same graph as `Upsert`) | 1.37 s | 1× |
| `LiveInsertParallel` (8 workers) | 0.23 s | 5.8× |

The parallel live-batch test reaches recall@10 0.998 at `ef_search` 128
against exact search (48-dimensional clustered data).

## Not wired yet

- `internal/segment` still calls `Upsert` once per record. It belongs to
  another work stream, so this change leaves it alone. Calling
  `UpsertBatch` from the segment's batch-upsert path is what makes REST and
  gRPC batch writes into a mutable HNSW segment parallel. Until then, the
  live ingest rates in [standard datasets](standard-datasets.md) are serial
  per shard.
- AVX2 kernels for amd64 (see above).
