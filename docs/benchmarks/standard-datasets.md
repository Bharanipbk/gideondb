# Standard datasets: SIFT1M and GloVe

Gap 3 results, 2026-10-21. GideonDB only; comparisons with other databases
are not published yet (see [next step](#next-step-vectordbbench)).

## Environment

| | |
|---|---|
| Hardware | Apple M3, 8 cores (4P + 4E), 16 GiB |
| OS | macOS 26.3.1 (Darwin 25.3.0), darwin/arm64 |
| Go | 1.26.5 |
| GideonDB | branch `claude/perf-standard-benchmarks-6a5bc9` (main 4e7006d + this work), NEON kernels |
| Load | The host also ran other build jobs. Load averages are given per run; absolute QPS on an idle machine will be higher. |

The index defaults apply unless a config says otherwise:

- HNSW: M = 16, `ef_construction` 100, `ef_search` 64.
- DiskANN: the defaults on main.
- PQ: the subspace count chosen by the collection defaults, with full
  precision rescoring of 4×k (`pq`) or 10×k (`pq-x10`).

Recall@10 is measured against exact flat search on the same data. The bench
tool computes this ground truth itself; the dataset's own ground-truth files
are not used.

## Datasets

| Dataset | Source | Base | Queries | Dim | Metric |
|---|---|---:|---:|---:|---|
| SIFT1M (pending download) | `ftp://ftp.irisa.fr/local/texmex/corpus/sift.tar.gz` (corpus-texmex.irisa.fr), `sift_base.fvecs` / `sift_query.fvecs` | 1,000,000 | 1,000 of 10,000 | 128 | L2 |
| GloVe 6B 100d | `https://nlp.stanford.edu/data/glove.6B.zip` (Stanford NLP), `glove.6B.100d.txt` | 390,000 | 1,000 of 10,000 held out | 100 | cosine |

- **GloVe** has no official query split.
  [`glove_to_fvecs.py`](#glove-conversion-script) holds out 10,000 random
  words (seed 42), so no query is in the base set, and writes the rest as
  base vectors. Words are dropped.
- Both archives are untrusted data. They were downloaded into empty
  directories outside the repository, only read (never executed), and are
  not committed.
- SHA-256 of `glove.6B.zip`:
  `617afb2fe6cbd085c235baf7a465b96f4112bd7f7ccb2b2cbd649fed9cbcf2fb`.

## In-process results (`gideondb-bench`)

The bench drives one in-process segment with a single query thread. That
isolates the index from HTTP, WAL and sharding.

- **HNSW, PQ and DiskANN** build the way a sealed segment does, and HNSW
  builds in parallel. Use `-build live` for one-at-a-time inserts.
- **Flat and binary** insert into a mutable segment.

### GloVe 100d, cosine, 390,000 × 100, k = 10, 1,000 queries

Load average 2–19 during the run; it took 31 minutes, with peak RSS 1.8 GB.

| Config | Recall@10 | QPS | p50 ms | p99 ms | Build s | Segment heap MiB |
|---|---:|---:|---:|---:|---:|---:|
| flat | 1.000 | 6 | 174.0 | 200.2 | 0.47 | 419.3 |
| hnsw-ef16 | 0.912 | 5288 | 0.166 | 0.560 | 36.4 | 536.0 |
| hnsw-ef32 | 0.960 | 3334 | 0.270 | 0.699 | 36.6 | 536.0 |
| hnsw (ef 64) | 0.980 | 1912 | 0.531 | 0.996 | 39.2 | 536.0 |
| hnsw-ef128 | 0.992 | 1037 | 0.989 | 1.900 | 44.0 | 536.0 |
| hnsw-ef256 | 0.996 | 553 | 1.733 | 3.426 | 36.3 | 536.0 |
| binary (×4) | 0.610 | 6 | 165.8 | 209.9 | 0.75 | 275.0 |
| binary-x10 | 0.743 | 6 | 164.1 | 176.6 | 0.72 | 275.0 |
| pq (×4) | 0.991 | 7 | 141.2 | 170.7 | 3.89 | 270.5 |
| pq-x10 | 0.994 | 5 | 184.8 | 435.5 | 4.18 | 270.5 |
| diskann | 0.996 | 1028 | 0.971 | 1.685 | 427.9 | 131.5 |
| diskann-ef200 | 0.998 | 592 | 1.639 | 3.140 | 382.0 | 131.5 |

### SIFT1M, L2

**Not run yet.** The only approved source, the IRISA FTP server (its HTTP
mirror timed out), delivered under 20 KB/s and then stalled. After a day of
resumed transfers, only 83 of about 168 MB had arrived. The commands under
[Reproduce](#reproduce) are ready. Expect `binary` to do badly on SIFT (see
[findings](#findings)).

## End-to-end through the HTTP server

`gideondb-loadtest server` drives a running server over REST:

1. Ingest with concurrent `/vectors/batch` calls of 500 records. Each record
   carries `bucket = i % 10` metadata.
2. Run 1,000 unfiltered queries with 16 concurrent clients.
3. Run 1,000 filtered queries, each with `{"bucket": i % 10}`, which keeps
   10% of the data.

Recall is checked against exact search computed locally for each query,
with the same filter applied.

The server was a local build on loopback with an API key, `-wal-sync async`,
the rate limit raised and `-checkpoint-every 0`. See the
[findings](#findings) for why checkpoints were off. With checkpoints off,
data stays in live mutable HNSW segments, one per shard.

### GloVe 100d, cosine, 8 shards, HNSW

Load average 10–16.

| Phase | Clients | QPS | p50 ms | p95 ms | p99 ms | Recall@10 |
|---|---:|---:|---:|---:|---:|---:|
| Ingest (390,000 vectors) | 8 | 3,449 vectors/s (113 s) | | | | |
| Unfiltered | 16 | 1,168 | 12.0 | 27.1 | 35.7 | 0.997 |
| Filtered (10%) | 16 | 157 | 82.6 | 225.9 | 406.4 | 0.9997 |

SIFT1M through the server is pending the same download.

## Findings

- **HNSW recall/QPS on real data.**
  - On GloVe, recall@10 is 0.980 at the default `ef_search`
    64 and 0.996 at 256.
  - Recall rises steadily with `ef_search`, so the graph stays connected
    (the gap 18 fix holds on real data).
- **Binary quantization is a poor fit for both sets.**
  - GloVe (0.61–0.74) has a few dominant dimensions that sign bits
    summarize badly.
  - SIFT (not yet measured) is L2 over non-negative, uncentred histogram
    values. Sign bits taken against zero are then almost all the same, so
    expect binary codes to carry little information. Use binary only with
    centred embedding models (most modern text embedders).
- **PQ** reaches 0.99 on GloVe at about a third less heap than flat. Its
  scan is still exhaustive, so QPS matches flat. Pair it with a graph index
  for speed.
- **DiskANN** reaches 0.996 at about 1,000 QPS with a quarter of HNSW's
  heap. Its build is about 10× slower than parallel HNSW.
- **Flat and binary/PQ scans are slow in the segment.** Exact flat search
  over 390,000 × 100 takes about 150 ms per query. The kernel alone accounts
  for about 12 ms (390,000 × about 30 ns per 100-d dot product), so the time goes to per-candidate
  overhead in the segment search path, not the kernel. Follow-up.
- **Bug found and fixed: sealed HNSW graphs could fail to load.**
  - In a parallel build, other workers can link to a node before it appends
    its own neighbours, which overflowed its list.
  - `LoadGraph` then rejected the graph, so a large REST ingest returned
    HTTP 500 when a shard checkpointed.
  - The insert now prunes the node's own list too. A regression test
    (`TestParallelBuildRespectsNeighbourLimits`) covers it.
- **Default checkpoints stall large ingests.**
  - With the default `-checkpoint-every 1000`, each shard checkpoints, and
    seals its HNSW graph, inside the write path every 1,000 mutations.
  - Around 200,000 GloVe vectors, batch requests took 34 s. That exceeds
    the server's 30 s write timeout, so the client saw EOF.
  - Background or incremental sealing, together with calling
    `hnsw.UpsertBatch` from the segment batch path, is the follow-up.
    `-checkpoint-every 0` avoids it for benchmarks.
- **Filtered HNSW** keeps recall (0.9997) at 10% selectivity, but runs at
  about 13% of unfiltered QPS. Most rejected candidates are still visited.

## Reproduce

All downloads and generated files live outside the repository. `$W` is
any empty working directory.

```bash
W=$(mktemp -d); mkdir -p "$W/dl-sift" "$W/dl-glove" "$W/scripts" "$W/data/glove" "$W/bin"

# Datasets (only these two)
curl -fS -o "$W/dl-sift/sift.tar.gz" ftp://ftp.irisa.fr/local/texmex/corpus/sift.tar.gz
tar -xzf "$W/dl-sift/sift.tar.gz" -C "$W/dl-sift"          # sift/sift_base.fvecs, sift/sift_query.fvecs
curl -fSL -o "$W/dl-glove/glove.6B.zip" https://nlp.stanford.edu/data/glove.6B.zip
unzip -o "$W/dl-glove/glove.6B.zip" glove.6B.100d.txt -d "$W/dl-glove"
# save the script below as $W/scripts/glove_to_fvecs.py, then:
python3 -I "$W/scripts/glove_to_fvecs.py" "$W/dl-glove/glove.6B.100d.txt" "$W/data/glove" 10000 42

# Binaries (from the repository root)
go build -o "$W/bin/" ./cmd/gideondb ./cmd/gideondb-bench ./cmd/gideondb-loadtest

# In-process
"$W/bin/gideondb-bench" -base "$W/dl-sift/sift/sift_base.fvecs" -queries "$W/dl-sift/sift/sift_query.fvecs" \
  -query-count 1000 -metric l2 -k 10 \
  -configs flat,hnsw-ef16,hnsw-ef32,hnsw,hnsw-ef128,hnsw-ef256,binary,binary-x10,pq,pq-x10,diskann,diskann-ef200 -json sift.json
"$W/bin/gideondb-bench" -base "$W/data/glove/glove-100-base.fvecs" -queries "$W/data/glove/glove-100-query.fvecs" \
  -query-count 1000 -metric cosine -k 10 \
  -configs flat,hnsw-ef16,hnsw-ef32,hnsw,hnsw-ef128,hnsw-ef256,binary,binary-x10,pq,pq-x10,diskann,diskann-ef200 -json glove.json

# End to end: a throwaway local server
(umask 077; openssl rand -hex 24 > "$W/api-key")
GIDEONDB_DASHBOARD_USERNAME=bench GIDEONDB_DASHBOARD_PASSWORD="$(openssl rand -hex 16)" GIDEONDB_DASHBOARD_BOOTSTRAP=true \
  "$W/bin/gideondb" -data-path "$W/server-data" -http-address 127.0.0.1:16333 -grpc-address "" \
  -api-key-file "$W/api-key" -wal-sync async -rate-limit-per-second 1000000 -rate-limit-burst 1000000 \
  -checkpoint-every 0 &
"$W/bin/gideondb-loadtest" server -server http://127.0.0.1:16333 -api-key-file "$W/api-key" \
  -base "$W/data/glove/glove-100-base.fvecs" -queries "$W/data/glove/glove-100-query.fvecs" \
  -query-count 1000 -metric cosine -index hnsw -shards 8 -concurrency 16 -ingest-concurrency 8
# SIFT1M: same with -base/-queries from dl-sift and -metric l2
```

### GloVe conversion script

```python
"""Convert a GloVe text file into base and query .fvecs files.

Usage: python3 -I glove_to_fvecs.py GLOVE_TXT OUT_DIR [QUERIES] [SEED]
"""
import random
import struct
import sys

def main():
    source, out = sys.argv[1], sys.argv[2]
    query_count = int(sys.argv[3]) if len(sys.argv) > 3 else 10000
    seed = int(sys.argv[4]) if len(sys.argv) > 4 else 42
    rows, dimension = [], None
    with open(source, encoding="utf-8", errors="replace") as handle:
        for line in handle:
            values = [float(x) for x in line.rstrip("\n").split(" ")[1:]]
            dimension = dimension or len(values)
            if len(values) != dimension:
                raise SystemExit(f"row {len(rows)} has {len(values)} values, want {dimension}")
            rows.append(values)
    held_out = set(random.Random(seed).sample(range(len(rows)), query_count))
    header, packer = struct.pack("<i", dimension), struct.Struct("<%df" % dimension)
    with open(f"{out}/glove-{dimension}-base.fvecs", "wb") as base, \
         open(f"{out}/glove-{dimension}-query.fvecs", "wb") as queries:
        for index, values in enumerate(rows):
            target = queries if index in held_out else base
            target.write(header)
            target.write(packer.pack(*values))

main()
```

## Next step: VectorDBBench

Comparisons with other databases are deliberately not published yet. The
next step is to run [VectorDBBench](https://github.com/zilliztech/VectorDBBench)
against GideonDB:

1. Add a GideonDB client to VectorDBBench under `vectordb_bench/backend/clients/gideondb/`.
   It wraps the REST API:
   - `POST /v1/collections` with `index.type` hnsw or diskann
   - `POST /v1/collections/{name}/vectors/batch`
   - `POST /v1/collections/{name}/search` with `top_k`, `ef_search` and
     `filter`
   - the official Python SDK can stand in for raw REST calls.
2. Map its filter cases (`int >= x` on an `id` field) to a `{"id": {"$gte": x}}`
   metadata filter.
3. Run the standard cases on one machine, with the competitors in the same
   container limits:
   - Performance768D1M (Cohere 1M)
   - Performance1536D500K (OpenAI 500K)
   - the 1% and 99% filtered variants
4. Use the same server flags as above, but with checkpoints on once sealing
   moves off the write path (see findings), and the WAL in `always` mode for
   the durability-matched run.
5. Publish the raw VectorDBBench result JSON together with the commands.
